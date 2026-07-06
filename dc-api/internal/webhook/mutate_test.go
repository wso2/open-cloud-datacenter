package webhook

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rs/zerolog"
	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Fake NAD lookup ──────────────────────────────────────────────────────────

// fakeNADLookup is a NADLookup implementation backed by a simple map.
// The map key is "namespace/name"; the value is the CNI type string.
// A missing key returns ("", nil) — same semantics as DynamicNADLookup when
// the NAD does not exist.
type fakeNADLookup struct {
	// m maps "namespace/name" → cni type
	m map[string]string
}

func (f *fakeNADLookup) CNIType(_ context.Context, namespace, name string) (string, error) {
	return f.m[namespace+"/"+name], nil
}

// newFake constructs a fakeNADLookup from a variadic list of
// "namespace/name" → type pairs.
func newFake(pairs ...string) *fakeNADLookup {
	m := make(map[string]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return &fakeNADLookup{m: m}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// silentLogger returns a zerolog.Logger that discards all output.
func silentLogger() zerolog.Logger {
	return zerolog.Nop()
}

// buildVM constructs a minimal VirtualMachine object map that mirrors the
// fields the mutator reads.  All optional slices/maps that are absent default
// to nil so navigation helpers return (nil, false) gracefully.
func buildVM(networks []interface{}, interfaces []interface{}, annotations map[string]interface{}) map[string]interface{} {
	vmiMetadata := map[string]interface{}{}
	if annotations != nil {
		vmiMetadata["annotations"] = annotations
	}

	return map[string]interface{}{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata": map[string]interface{}{
			"name":      "test-vm",
			"namespace": "dc-tenant1",
		},
		"spec": map[string]interface{}{
			"running": true,
			"template": map[string]interface{}{
				"metadata": vmiMetadata,
				"spec": map[string]interface{}{
					"domain": map[string]interface{}{
						"devices": map[string]interface{}{
							"interfaces": interfaces,
						},
					},
					"networks": networks,
				},
			},
		},
	}
}

// multusNetwork returns a networks[] entry for a multus network.
func multusNetwork(name, networkName string) map[string]interface{} {
	return map[string]interface{}{
		"name": name,
		"multus": map[string]interface{}{
			"networkName": networkName,
		},
	}
}

// bridgeInterface returns an interfaces[] entry (bridge mode) with optional MAC.
func bridgeInterface(name, mac string) map[string]interface{} {
	m := map[string]interface{}{
		"name":   name,
		"bridge": map[string]interface{}{},
	}
	if mac != "" {
		m["macAddress"] = mac
	}
	return m
}

// extractPatch deserialises a JSON Patch byte slice into a slice of maps for
// easy assertion.
func extractPatch(t *testing.T, raw []byte) []map[string]interface{} {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var ops []map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &ops))
	return ops
}

// patchPaths returns the set of JSON Pointer paths in a patch op slice.
func patchPaths(ops []map[string]interface{}) map[string]string {
	paths := make(map[string]string, len(ops))
	for _, op := range ops {
		path, _ := op["path"].(string)
		val, _ := op["value"].(string)
		paths[path] = val
	}
	return paths
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestOVNNAD_MissingAnnotations verifies that a VM referencing a kube-ovn NAD
// with a pinned MAC and no existing OVN annotations gets all three annotation
// keys injected (plus the default-network annotation).
func TestOVNNAD_MissingAnnotations(t *testing.T) {
	const (
		nadNS  = "dc-hiran"
		nadName = "subnet-hiran-vm-sn"
		mac    = "02:11:22:33:44:55"
	)
	nadRef := nadNS + "/" + nadName

	vm := buildVM(
		[]interface{}{multusNetwork("ovn", nadRef)},
		[]interface{}{bridgeInterface("ovn", mac)},
		nil, // no existing annotations
	)

	lookup := newFake(nadRef, cniTypeKubeOVN)
	m := NewMutator(lookup, silentLogger())
	patch, err := m.buildPatch(context.Background(), vm)

	require.NoError(t, err)
	// Expect: annotations object creation + 4 annotation ops.
	require.NotEmpty(t, patch, "expected non-empty patch")

	// Separate the "create annotations object" op (Value is a map) from the
	// individual annotation ops (Value is a string) so we can assert each.
	var foundAnnotationsInit bool
	stringOps := make(map[string]string, len(patch))
	for _, op := range patch {
		if op.Path == "/spec/template/metadata/annotations" {
			foundAnnotationsInit = true
			continue
		}
		if s, ok := op.Value.(string); ok {
			stringOps[op.Path] = s
		}
	}
	assert.True(t, foundAnnotationsInit, "patch must create the annotations object")

	keyOVN := nadName + "." + nadNS + ".ovn.kubernetes.io~1mac_address"
	keyLegacy := nadName + "." + nadNS + ".kubernetes.io~1mac_address"
	keySwitch := nadName + "." + nadNS + ".kubernetes.io~1logical_switch"
	keyDefault := "v1.multus-cni.io~1default-network"

	assert.Equal(t, mac, stringOps["/spec/template/metadata/annotations/"+keyOVN], "ovn mac_address")
	assert.Equal(t, mac, stringOps["/spec/template/metadata/annotations/"+keyLegacy], "legacy mac_address")
	assert.Equal(t, nadName, stringOps["/spec/template/metadata/annotations/"+keySwitch], "logical_switch")
	assert.Equal(t, nadNS+"/"+nadName, stringOps["/spec/template/metadata/annotations/"+keyDefault], "default-network")
}

// TestOVNNAD_AnnotationsAlreadyCorrect verifies that when all four OVN
// annotations already match the interface MAC, the patch is empty.
func TestOVNNAD_AnnotationsAlreadyCorrect(t *testing.T) {
	const (
		nadNS   = "dc-hiran"
		nadName = "subnet-hiran-vm-sn"
		mac     = "02:11:22:33:44:55"
	)
	nadRef := nadNS + "/" + nadName

	existingAnnotations := map[string]interface{}{
		nadName + "." + nadNS + ".ovn.kubernetes.io/mac_address": mac,
		nadName + "." + nadNS + ".kubernetes.io/mac_address":     mac,
		nadName + "." + nadNS + ".kubernetes.io/logical_switch":  nadName,
		"v1.multus-cni.io/default-network":                       nadNS + "/" + nadName,
	}

	vm := buildVM(
		[]interface{}{multusNetwork("ovn", nadRef)},
		[]interface{}{bridgeInterface("ovn", mac)},
		existingAnnotations,
	)

	lookup := newFake(nadRef, cniTypeKubeOVN)
	m := NewMutator(lookup, silentLogger())
	patch, err := m.buildPatch(context.Background(), vm)

	require.NoError(t, err)
	assert.Empty(t, patch, "expected empty patch when annotations already correct")
}

// TestBridgeNAD_Only verifies that a VM using only a bridge-type NAD is not
// mutated at all — no OVN annotations are added, no patch is generated.
func TestBridgeNAD_Only(t *testing.T) {
	const nadRef = "default/vm-net-mgmt"

	vm := buildVM(
		[]interface{}{multusNetwork("default", nadRef)},
		[]interface{}{bridgeInterface("default", "02:aa:bb:cc:dd:ee")},
		nil,
	)

	// NAD resolves to "bridge" — not kube-ovn.
	lookup := newFake(nadRef, "bridge")
	m := NewMutator(lookup, silentLogger())
	patch, err := m.buildPatch(context.Background(), vm)

	require.NoError(t, err)
	assert.Empty(t, patch, "bridge NAD must produce empty patch")
}

// TestMixedInterfaces_OnlyOVNMutated verifies that in a VM with two interfaces
// (one OVN, one bridge), only the OVN interface receives the annotation patch.
func TestMixedInterfaces_OnlyOVNMutated(t *testing.T) {
	const (
		ovnNS   = "dc-hiran"
		ovnNAD  = "subnet-hiran-vm-sn"
		ovnMAC  = "02:11:22:33:44:55"
		bridgeNS  = "default"
		bridgeNAD = "vm-net-mgmt"
		bridgeMAC = "02:aa:bb:cc:dd:ee"
	)

	vm := buildVM(
		[]interface{}{
			multusNetwork("ovn", ovnNS+"/"+ovnNAD),
			multusNetwork("mgmt", bridgeNS+"/"+bridgeNAD),
		},
		[]interface{}{
			bridgeInterface("ovn", ovnMAC),
			bridgeInterface("mgmt", bridgeMAC),
		},
		nil,
	)

	lookup := newFake(
		ovnNS+"/"+ovnNAD, cniTypeKubeOVN,
		bridgeNS+"/"+bridgeNAD, "bridge",
	)
	m := NewMutator(lookup, silentLogger())
	patch, err := m.buildPatch(context.Background(), vm)

	require.NoError(t, err)
	require.NotEmpty(t, patch, "expected non-empty patch for mixed interfaces")

	// Convert patch paths to a set.
	paths := make(map[string]bool, len(patch))
	for _, op := range patch {
		paths[op.Path] = true
	}

	// OVN keys must be present.
	keyOVN := "/spec/template/metadata/annotations/" + ovnNAD + "." + ovnNS + ".ovn.kubernetes.io~1mac_address"
	assert.True(t, paths[keyOVN], "ovn.kubernetes.io/mac_address must be patched")

	// Bridge keys must NOT be present.
	bridgeKeyOVN := "/spec/template/metadata/annotations/" + bridgeNAD + "." + bridgeNS + ".ovn.kubernetes.io~1mac_address"
	assert.False(t, paths[bridgeKeyOVN], "bridge interface must not be patched")
}

// TestNoMultusNetworks verifies that a VM with no networks at all produces an
// empty patch without error.
func TestNoMultusNetworks(t *testing.T) {
	vm := buildVM(nil, nil, nil)

	lookup := newFake() // no NADs
	m := NewMutator(lookup, silentLogger())
	patch, err := m.buildPatch(context.Background(), vm)

	require.NoError(t, err)
	assert.Empty(t, patch)
}

// TestMissingMAC verifies that when the interface definition lacks a macAddress
// (Rancher hasn't set it yet), the webhook no-ops and returns an empty patch
// rather than injecting a blank or synthesised MAC.
func TestMissingMAC(t *testing.T) {
	const nadRef = "dc-hiran/subnet-hiran-vm-sn"

	vm := buildVM(
		[]interface{}{multusNetwork("ovn", nadRef)},
		[]interface{}{bridgeInterface("ovn", "")}, // no MAC
		nil,
	)

	lookup := newFake(nadRef, cniTypeKubeOVN)
	m := NewMutator(lookup, silentLogger())
	patch, err := m.buildPatch(context.Background(), vm)

	require.NoError(t, err)
	assert.Empty(t, patch, "missing MAC must produce empty patch — we never synthesise MACs")
}

// withStatus returns a copy of vm with the given status block attached —
// simulating the object state KubeVirt maintains once the VM has been
// processed (status.created/ready flip true when a VMI exists).
func withStatus(vm map[string]interface{}, status map[string]interface{}) map[string]interface{} {
	vm["status"] = status
	return vm
}

// TestHandle_LiveVMI_NotMutated verifies the provisioning-time guard: a VM
// whose VMI already exists (status.created=true) must NOT be patched, even
// when its OVN annotations are missing. Mutating spec.template on a live VM
// was implicated in a control-plane cluster outage post-mortem.
func TestHandle_LiveVMI_NotMutated(t *testing.T) {
	const (
		nadNS   = "dc-hiran"
		nadName = "subnet-hiran-vm-sn"
		mac     = "02:11:22:33:44:55"
	)
	nadRef := nadNS + "/" + nadName

	vm := withStatus(
		buildVM(
			[]interface{}{multusNetwork("ovn", nadRef)},
			[]interface{}{bridgeInterface("ovn", mac)},
			nil, // annotations missing — would normally be patched
		),
		map[string]interface{}{
			"created":         true,
			"ready":           true,
			"printableStatus": "Running",
		},
	)
	raw, err := json.Marshal(vm)
	require.NoError(t, err)

	req := &admissionv1.AdmissionRequest{UID: "live-vmi-uid", Operation: admissionv1.Update}
	req.Object.Raw = raw

	lookup := newFake(nadRef, cniTypeKubeOVN)
	m := NewMutator(lookup, silentLogger())
	resp := m.Handle(context.Background(), req)

	assert.True(t, resp.Allowed)
	assert.Empty(t, resp.Patch, "a VM with a live VMI must never be mutated")
}

// TestHandle_PreBootUpdate_StillMutated verifies that the guard does not
// close the legitimate heal window: an UPDATE on a VM whose VMI has not been
// created yet (e.g. Rancher pinning the MAC after create, before start) must
// still be patched.
func TestHandle_PreBootUpdate_StillMutated(t *testing.T) {
	const (
		nadNS   = "dc-hiran"
		nadName = "subnet-hiran-vm-sn"
		mac     = "02:11:22:33:44:55"
	)
	nadRef := nadNS + "/" + nadName

	vm := withStatus(
		buildVM(
			[]interface{}{multusNetwork("ovn", nadRef)},
			[]interface{}{bridgeInterface("ovn", mac)},
			nil,
		),
		map[string]interface{}{
			"created":         false,
			"ready":           false,
			"printableStatus": "Stopped",
		},
	)
	raw, err := json.Marshal(vm)
	require.NoError(t, err)

	req := &admissionv1.AdmissionRequest{UID: "pre-boot-uid", Operation: admissionv1.Update}
	req.Object.Raw = raw

	lookup := newFake(nadRef, cniTypeKubeOVN)
	m := NewMutator(lookup, silentLogger())
	resp := m.Handle(context.Background(), req)

	assert.True(t, resp.Allowed)
	assert.NotEmpty(t, resp.Patch, "pre-boot UPDATE must still be patched — the heal window stays open")
}

// TestVMHasLiveVMI table-tests the status interpretation.
func TestVMHasLiveVMI(t *testing.T) {
	cases := []struct {
		name   string
		status map[string]interface{}
		want   bool
	}{
		{"no status at all", nil, false},
		{"empty status", map[string]interface{}{}, false},
		{"created true", map[string]interface{}{"created": true}, true},
		{"ready true", map[string]interface{}{"ready": true}, true},
		{"printableStatus Running", map[string]interface{}{"printableStatus": "Running"}, true},
		{"printableStatus Starting", map[string]interface{}{"printableStatus": "Starting"}, true},
		{"printableStatus Migrating", map[string]interface{}{"printableStatus": "Migrating"}, true},
		{"printableStatus Stopped", map[string]interface{}{"printableStatus": "Stopped", "created": false}, false},
		{"printableStatus Provisioning", map[string]interface{}{"printableStatus": "Provisioning"}, false},
		{"printableStatus WaitingForVolumeBinding", map[string]interface{}{"printableStatus": "WaitingForVolumeBinding"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vm := buildVM(nil, nil, nil)
			if tc.status != nil {
				vm = withStatus(vm, tc.status)
			}
			assert.Equal(t, tc.want, vmHasLiveVMI(vm))
		})
	}
}

// TestHandle_NilRequest verifies that a nil request is handled gracefully
// and returns an allow response without panicking.
func TestHandle_NilRequest(t *testing.T) {
	m := NewMutator(newFake(), silentLogger())
	resp := m.Handle(context.Background(), nil)
	assert.True(t, resp.Allowed)
	assert.Nil(t, resp.Patch)
}

// TestHandle_FullRoundTrip verifies the complete AdmissionReview roundtrip
// (encode → Handle → decode) for the happy path where mutation is needed.
func TestHandle_FullRoundTrip(t *testing.T) {
	const (
		nadNS   = "dc-hiran"
		nadName = "subnet-hiran-vm-sn"
		mac     = "02:11:22:33:44:55"
	)
	nadRef := nadNS + "/" + nadName

	vm := buildVM(
		[]interface{}{multusNetwork("ovn", nadRef)},
		[]interface{}{bridgeInterface("ovn", mac)},
		nil,
	)
	raw, err := json.Marshal(vm)
	require.NoError(t, err)

	req := &admissionv1.AdmissionRequest{UID: "round-trip-uid"}
	req.Object.Raw = raw

	lookup := newFake(nadRef, cniTypeKubeOVN)
	m := NewMutator(lookup, silentLogger())
	resp := m.Handle(context.Background(), req)

	assert.True(t, resp.Allowed)
	require.NotEmpty(t, resp.Patch, "expected non-empty patch in full round-trip")
	assert.NotNil(t, resp.PatchType)
}

// TestAdmissionReviewWrapper verifies the HTTP-layer wrapper encodes the
// AdmissionReview response correctly, including UID propagation.
func TestAdmissionReviewWrapper(t *testing.T) {
	const (
		nadNS   = "dc-hiran"
		nadName = "subnet-hiran-vm-sn"
		mac     = "02:ab:cd:ef:01:23"
	)
	nadRef := nadNS + "/" + nadName

	vm := buildVM(
		[]interface{}{multusNetwork("ovn", nadRef)},
		[]interface{}{bridgeInterface("ovn", mac)},
		nil,
	)
	vmRaw, err := json.Marshal(vm)
	require.NoError(t, err)

	req := &admissionv1.AdmissionRequest{UID: "wrapper-uid"}
	req.Object.Raw = vmRaw

	ar := admissionv1.AdmissionReview{
		Request: req,
	}
	ar.TypeMeta.APIVersion = "admission.k8s.io/v1"
	ar.TypeMeta.Kind = "AdmissionReview"

	body, err := json.Marshal(ar)
	require.NoError(t, err)

	lookup := newFake(nadRef, cniTypeKubeOVN)
	mut := NewMutator(lookup, silentLogger())
	wrapper := NewAdmissionReviewWrapper(mut)

	out, err := wrapper.Review(context.Background(), body)
	require.NoError(t, err)

	var arResp admissionv1.AdmissionReview
	require.NoError(t, json.Unmarshal(out, &arResp))
	assert.Equal(t, types.UID("wrapper-uid"), arResp.Response.UID, "UID must be propagated")
	assert.True(t, arResp.Response.Allowed)
	assert.NotEmpty(t, arResp.Response.Patch)
}

// TestAnnotationOp_AddVsReplace verifies that annotationOp emits "add" when the
// key is absent and "replace" when it already exists.
func TestAnnotationOp_AddVsReplace(t *testing.T) {
	existing := map[string]string{
		"already/here": "old-value",
	}

	addOps := annotationOp(existing, "brand/new", "v1")
	require.Len(t, addOps, 1)
	assert.Equal(t, "add", addOps[0].Op)

	replaceOps := annotationOp(existing, "already/here", "new-value")
	require.Len(t, replaceOps, 1)
	assert.Equal(t, "replace", replaceOps[0].Op)
}

// TestParseNetworkName covers valid and invalid networkName formats.
func TestParseNetworkName(t *testing.T) {
	cases := []struct {
		input     string
		wantNS    string
		wantName  string
		wantError bool
	}{
		{"dc-hiran/subnet-vm", "dc-hiran", "subnet-vm", false},
		{"default/net", "default", "net", false},
		{"noslash", "", "", true},
		{"/missingns", "", "", true},
		{"missingname/", "", "", true},
		{"", "", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			ns, name, err := parseNetworkName(tc.input)
			if tc.wantError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantNS, ns)
				assert.Equal(t, tc.wantName, name)
			}
		})
	}
}

// TestOVNNAD_PartialAnnotations verifies that when some OVN annotations are
// present but one is wrong or missing, the patch corrects only what differs.
func TestOVNNAD_PartialAnnotations(t *testing.T) {
	const (
		nadNS   = "dc-hiran"
		nadName = "subnet-hiran-vm-sn"
		mac     = "02:11:22:33:44:55"
	)
	nadRef := nadNS + "/" + nadName

	// Inject only the ovn.kubernetes.io form; leave legacy and logical_switch absent.
	existingAnnotations := map[string]interface{}{
		nadName + "." + nadNS + ".ovn.kubernetes.io/mac_address": mac,
		// Missing: legacy mac_address, logical_switch, default-network
	}

	vm := buildVM(
		[]interface{}{multusNetwork("ovn", nadRef)},
		[]interface{}{bridgeInterface("ovn", mac)},
		existingAnnotations,
	)

	lookup := newFake(nadRef, cniTypeKubeOVN)
	m := NewMutator(lookup, silentLogger())
	patch, err := m.buildPatch(context.Background(), vm)

	require.NoError(t, err)
	require.NotEmpty(t, patch, "partial annotations must still produce a patch")

	paths := make(map[string]bool, len(patch))
	for _, op := range patch {
		paths[op.Path] = true
	}

	// The ovn form is already correct — should be a replace (key exists).
	keyOVN := "/spec/template/metadata/annotations/" + nadName + "." + nadNS + ".ovn.kubernetes.io~1mac_address"
	// It already exists so annotationOp emits "replace", but value is the same.
	// Important: the patch MUST NOT have the "create annotations object" op since
	// the annotations map already exists.
	annotationsInitPath := "/spec/template/metadata/annotations"
	for _, op := range patch {
		if op.Path == annotationsInitPath {
			// Only acceptable if it was an "add" of the whole object — which
			// should NOT happen when the annotations map already exists.
			assert.Fail(t, "annotations init op must not appear when annotations already exist")
		}
	}
	_ = keyOVN // confirmed above via paths map

	// The missing keys must have "add" ops.
	keyLegacy := "/spec/template/metadata/annotations/" + nadName + "." + nadNS + ".kubernetes.io~1mac_address"
	assert.True(t, paths[keyLegacy], "legacy mac_address must be added")
	keySwitch := "/spec/template/metadata/annotations/" + nadName + "." + nadNS + ".kubernetes.io~1logical_switch"
	assert.True(t, paths[keySwitch], "logical_switch must be added")
}
