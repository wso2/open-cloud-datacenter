package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rs/zerolog"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// cniTypeKubeOVN is the CNI type field value emitted by KubeOVN NADs.
const cniTypeKubeOVN = "kube-ovn"

// jsonPatchOp is a single RFC 6902 JSON Patch operation.
type jsonPatchOp struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

// Mutator holds the dependencies for the admission webhook handler.
type Mutator struct {
	nad NADLookup
	log zerolog.Logger
}

// NewMutator constructs a Mutator.
func NewMutator(nad NADLookup, log zerolog.Logger) *Mutator {
	return &Mutator{nad: nad, log: log}
}

// Handle processes an AdmissionReview request for a VirtualMachine resource.
// It returns a response that is either:
//   - An allow with a non-empty JSON Patch when OVN annotations need to be added.
//   - An allow with an empty patch when the VM is already correct or does not use an OVN NAD.
//
// The webhook uses failurePolicy:Ignore in the MutatingWebhookConfiguration,
// so any error returned here is logged and results in an allow-without-patch
// rather than blocking the VM creation.
func (m *Mutator) Handle(ctx context.Context, req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	if req == nil {
		return allow(nil)
	}

	// Decode the raw VirtualMachine object from the request.
	var vm map[string]interface{}
	if err := json.Unmarshal(req.Object.Raw, &vm); err != nil {
		m.log.Error().Err(err).Str("uid", string(req.UID)).Msg("webhook: failed to unmarshal VirtualMachine")
		return allow(nil)
	}

	// Provisioning-time only: never mutate a VM that already has a live VMI.
	// The OVN annotations live in spec.template and only take effect when the
	// VMI is (re)created, so patching a running VM buys nothing — and a
	// spec.template mutation on a live VM can trigger KubeVirt/CDI
	// reconciliation of the running instance (implicated in a control-plane
	// cluster outage post-mortem). MAC heals that arrive after first boot
	// apply naturally on the next restart, when this webhook sees the
	// pre-boot UPDATE again.
	if vmHasLiveVMI(vm) {
		m.log.Info().
			Str("uid", string(req.UID)).
			Str("operation", string(req.Operation)).
			Str("namespace", req.Namespace).
			Str("name", req.Name).
			Msg("webhook: VM already has a live VMI — provisioning-time only, skipping")
		return allow(nil)
	}

	patch, err := m.buildPatch(ctx, vm)
	if err != nil {
		m.log.Error().Err(err).Str("uid", string(req.UID)).Msg("webhook: failed to build patch — allowing unchanged")
		return allow(nil)
	}

	if len(patch) == 0 {
		return allow(nil)
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		m.log.Error().Err(err).Str("uid", string(req.UID)).Msg("webhook: failed to marshal patch — allowing unchanged")
		return allow(nil)
	}

	m.log.Info().
		Str("uid", string(req.UID)).
		Int("ops", len(patch)).
		Msg("webhook: patching VirtualMachine with OVN MAC annotations")

	pt := admissionv1.PatchTypeJSONPatch
	return &admissionv1.AdmissionResponse{
		UID:       req.UID,
		Allowed:   true,
		PatchType: &pt,
		Patch:     patchBytes,
	}
}

// buildPatch inspects the VirtualMachine object and returns the JSON Patch ops
// needed to inject OVN MAC annotations for any interfaces that reference a
// kube-ovn NAD.  Returns nil, nil when no mutation is needed.
func (m *Mutator) buildPatch(ctx context.Context, vm map[string]interface{}) ([]jsonPatchOp, error) {
	// Navigate to spec.template.spec.networks and
	// spec.template.spec.domain.devices.interfaces.
	//
	// We do not fail hard on navigation errors — an unexpected shape is treated
	// as "no OVN network" and returns an empty patch.

	networks, _ := getNetworks(vm)
	interfaces, _ := getInterfaces(vm)

	if len(networks) == 0 || len(interfaces) == 0 {
		return nil, nil
	}

	// If the VM declares a `pod` network, that NIC is meant to be the launcher
	// pod's primary interface (eth0) — e.g. the dbaas DB VM's mgmt-net
	// masquerade, which the dbaas controller reaches over the cluster pod
	// network to probe Postgres readiness. In that case we must NOT force
	// v1.multus-cni.io/default-network onto the OVN NAD: doing so steals eth0
	// and strands the VM on the isolated tenant VPC, unreachable from its
	// controller. MAC + logical_switch pinning for the OVN data NIC still
	// apply. dc-api's own compute VMs are pure-multus (no pod network), so
	// they are unaffected and still get default-network set.
	hasPodNetwork := false
	for _, net := range networks {
		if _, ok := net["pod"]; ok {
			hasPodNetwork = true
			break
		}
	}

	// Build a map from interface name → MAC so we can look up the MAC once we
	// know which interfaces correspond to OVN NADs.
	ifaceMAC := make(map[string]string, len(interfaces))
	for _, iface := range interfaces {
		name, _ := iface["name"].(string)
		mac, _ := iface["macAddress"].(string)
		if name != "" {
			ifaceMAC[name] = mac
		}
	}

	// Read existing VMI template annotations so we can check idempotency and
	// construct safe "add" vs "replace" operations.
	existingAnnotations := getTemplateAnnotations(vm)

	// Ensure the annotation path exists before we try to add keys into it.
	// If spec.template.metadata.annotations is absent we need to create the
	// object first, then add the individual keys.
	annotationsExist := templateAnnotationsExist(vm)

	var ops []jsonPatchOp

	for _, net := range networks {
		multus, ok := net["multus"].(map[string]interface{})
		if !ok {
			continue // not a multus network
		}
		networkName, _ := multus["networkName"].(string)
		if networkName == "" {
			continue
		}

		nadNS, nadName, err := parseNetworkName(networkName)
		if err != nil {
			m.log.Warn().Err(err).Str("networkName", networkName).Msg("webhook: cannot parse multus networkName — skipping")
			continue
		}

		// Look up the NAD to determine its CNI type.
		cniType, err := m.nad.CNIType(ctx, nadNS, nadName)
		if err != nil {
			m.log.Warn().Err(err).
				Str("nad_ns", nadNS).Str("nad_name", nadName).
				Msg("webhook: NAD lookup failed — skipping interface")
			continue
		}
		if cniType != cniTypeKubeOVN {
			continue // non-OVN NAD — no mutation needed
		}

		// Match the network entry back to the interface by name so we can read
		// the pinned MAC from the interface definition.
		ifaceName, _ := net["name"].(string)
		mac := ifaceMAC[ifaceName]
		if mac == "" {
			m.log.Warn().
				Str("iface", ifaceName).Str("nad", networkName).
				Msg("webhook: interface has no macAddress — Rancher has not yet set it; skipping")
			continue
		}

		// Build the three OVN annotation keys.
		keyOVN := fmt.Sprintf("%s.%s.ovn.kubernetes.io/mac_address", nadName, nadNS)
		keyLegacy := fmt.Sprintf("%s.%s.kubernetes.io/mac_address", nadName, nadNS)
		keySwitch := fmt.Sprintf("%s.%s.kubernetes.io/logical_switch", nadName, nadNS)
		keyDefault := "v1.multus-cni.io/default-network"

		// Idempotency: if all three annotation values already match the interface
		// MAC (and the default-network annotation is set), skip this interface.
		// When the VM has a pod network, default-network is deliberately left
		// alone (see hasPodNetwork above), so it must not factor into the
		// idempotency check.
		defaultNetworkOK := hasPodNetwork || existingAnnotations[keyDefault] == nadNS+"/"+nadName
		if existingAnnotations[keyOVN] == mac &&
			existingAnnotations[keyLegacy] == mac &&
			existingAnnotations[keySwitch] == nadName &&
			defaultNetworkOK {
			m.log.Debug().Str("iface", ifaceName).Msg("webhook: OVN annotations already correct — no-op")
			continue
		}

		// We need at least one annotation change. Ensure the parent annotations
		// object exists first (only need to do this once, before the first add).
		if !annotationsExist {
			ops = append(ops, jsonPatchOp{
				Op:    "add",
				Path:  "/spec/template/metadata/annotations",
				Value: map[string]interface{}{},
			})
			annotationsExist = true
			// The existing map is now empty — clear it so further comparisons work.
			existingAnnotations = map[string]string{}
		}

		ops = append(ops, annotationOp(existingAnnotations, keyOVN, mac)...)
		ops = append(ops, annotationOp(existingAnnotations, keyLegacy, mac)...)
		ops = append(ops, annotationOp(existingAnnotations, keySwitch, nadName)...)
		if !hasPodNetwork {
			ops = append(ops, annotationOp(existingAnnotations, keyDefault, nadNS+"/"+nadName)...)
		}

		// Update the local map so subsequent iterations see the new values (for
		// multiple OVN interfaces — rare but safe to handle).
		existingAnnotations[keyOVN] = mac
		existingAnnotations[keyLegacy] = mac
		existingAnnotations[keySwitch] = nadName
		if !hasPodNetwork {
			existingAnnotations[keyDefault] = nadNS + "/" + nadName
		}
	}

	return ops, nil
}

// vmHasLiveVMI reports whether the VirtualMachine already has a live VMI
// behind it. status.created flips true as soon as KubeVirt instantiates the
// VMI (status.ready once it is running); printableStatus is checked as a
// belt-and-suspenders signal for in-flight lifecycle states. Pre-boot states
// (no status, Stopped, Provisioning, WaitingForVolumeBinding, …) return
// false so provisioning-time UPDATEs — e.g. Rancher pinning the MAC after
// create but before start — are still mutated.
func vmHasLiveVMI(vm map[string]interface{}) bool {
	status, ok := getNestedMap(vm, "status")
	if !ok {
		return false
	}
	if created, _ := status["created"].(bool); created {
		return true
	}
	if ready, _ := status["ready"].(bool); ready {
		return true
	}
	switch s, _ := status["printableStatus"].(string); s {
	case "Starting", "Running", "Paused", "Stopping", "Terminating", "Migrating":
		return true
	}
	return false
}

// annotationOp returns the right RFC 6902 op ("add" or "replace") for a single
// annotation key.  If the key already exists in existing, we emit "replace";
// otherwise "add".  This avoids the "add to existing key" error from some
// JSON Patch implementations.
func annotationOp(existing map[string]string, key, value string) []jsonPatchOp {
	op := "add"
	if _, ok := existing[key]; ok {
		op = "replace"
	}
	// JSON Pointer (RFC 6901) requires "/" and "~" to be escaped.
	escapedKey := strings.NewReplacer("~", "~0", "/", "~1").Replace(key)
	path := "/spec/template/metadata/annotations/" + escapedKey
	return []jsonPatchOp{{Op: op, Path: path, Value: value}}
}

// allow returns a permissive AdmissionResponse with an optional patch.
// patch may be nil for a no-op allow.
func allow(patch []byte) *admissionv1.AdmissionResponse {
	resp := &admissionv1.AdmissionResponse{
		Allowed: true,
		Result:  &metav1.Status{Code: 200},
	}
	if len(patch) > 0 {
		pt := admissionv1.PatchTypeJSONPatch
		resp.PatchType = &pt
		resp.Patch = patch
	}
	return resp
}

// parseNetworkName splits a Multus networkName in "namespace/name" form.
// Returns an error when the format is unexpected.
func parseNetworkName(networkName string) (namespace, name string, err error) {
	parts := strings.SplitN(networkName, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid multus networkName %q: expected namespace/name", networkName)
	}
	return parts[0], parts[1], nil
}

// ── Navigation helpers ────────────────────────────────────────────────────────
// These return nil slices/maps (not errors) on structural mismatch so the
// caller can treat an unexpected VM shape as "no OVN network" gracefully.

func getNetworks(vm map[string]interface{}) ([]map[string]interface{}, bool) {
	return getNestedSlice(vm, "spec", "template", "spec", "networks")
}

func getInterfaces(vm map[string]interface{}) ([]map[string]interface{}, bool) {
	return getNestedSlice(vm, "spec", "template", "spec", "domain", "devices", "interfaces")
}

func getTemplateAnnotations(vm map[string]interface{}) map[string]string {
	raw, ok := getNestedMap(vm, "spec", "template", "metadata", "annotations")
	if !ok {
		return map[string]string{}
	}
	result := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	return result
}

func templateAnnotationsExist(vm map[string]interface{}) bool {
	_, ok := getNestedMap(vm, "spec", "template", "metadata", "annotations")
	return ok
}

// getNestedSlice walks a chain of map keys and returns the final value as a
// slice of maps.  Any structural mismatch returns (nil, false).
func getNestedSlice(obj map[string]interface{}, fields ...string) ([]map[string]interface{}, bool) {
	cur := obj
	for i, f := range fields {
		v, ok := cur[f]
		if !ok {
			return nil, false
		}
		if i == len(fields)-1 {
			rawSlice, ok := v.([]interface{})
			if !ok {
				return nil, false
			}
			out := make([]map[string]interface{}, 0, len(rawSlice))
			for _, item := range rawSlice {
				m, ok := item.(map[string]interface{})
				if ok {
					out = append(out, m)
				}
			}
			return out, true
		}
		m, ok := v.(map[string]interface{})
		if !ok {
			return nil, false
		}
		cur = m
	}
	return nil, false
}

// getNestedMap walks a chain of map keys and returns the final value as a map.
// Any structural mismatch returns (nil, false).
func getNestedMap(obj map[string]interface{}, fields ...string) (map[string]interface{}, bool) {
	cur := obj
	for _, f := range fields {
		v, ok := cur[f]
		if !ok {
			return nil, false
		}
		m, ok := v.(map[string]interface{})
		if !ok {
			return nil, false
		}
		cur = m
	}
	return cur, true
}

// AdmissionReviewWrapper wraps the full AdmissionReview roundtrip for the HTTP handler.
type AdmissionReviewWrapper struct {
	mutator *Mutator
}

// NewAdmissionReviewWrapper constructs an AdmissionReviewWrapper.
func NewAdmissionReviewWrapper(m *Mutator) *AdmissionReviewWrapper {
	return &AdmissionReviewWrapper{mutator: m}
}

// Review decodes an AdmissionReview from raw bytes, calls the mutator, and
// returns the AdmissionReview bytes to send back.
func (w *AdmissionReviewWrapper) Review(ctx context.Context, body []byte) ([]byte, error) {
	var ar admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("unmarshal AdmissionReview: %w", err)
	}
	if ar.Request == nil {
		return nil, fmt.Errorf("AdmissionReview has no Request")
	}

	resp := w.mutator.Handle(ctx, ar.Request)
	resp.UID = ar.Request.UID

	out := admissionv1.AdmissionReview{
		TypeMeta: ar.TypeMeta,
		Response: resp,
	}
	// Ensure the TypeMeta is set — some versions of kube-apiserver reject a
	// response without apiVersion/kind.
	if out.TypeMeta.Kind == "" {
		out.TypeMeta = metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal AdmissionReview response: %w", err)
	}
	return b, nil
}

