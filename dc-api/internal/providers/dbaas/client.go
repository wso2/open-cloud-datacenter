// Package dbaas is the dc-api-side driver for the dbaas operator's CRDs.
//
// dc-api never talks to the dbaas REST gateway — it only ever creates / reads
// / deletes DBInstance Custom Resources of group dbaas.opencloud.wso2.com/
// v1alpha1. The dbaas controller (separate workload at
// github.com/wso2/open-cloud-datacenter) watches those CRs and provisions the
// underlying KubeVirt VM running PostgreSQL.
//
// This package implements providers.DatabaseProvisioner against a dynamic
// client (no typed K8s client — the dbaas operator's Go types live in a
// different module that dc-api does not import). Same pattern as
// providers/kvi/client.go.
//
// Task 1 (D2): 1:1 model — one DBInstance CR == one VM. No Backend CRD.
package dbaas

import (
	"context"
	"encoding/base64"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/wso2/dc-api/internal/providers"
)

// DBInstance CRD identity. Must match config/crd/bases/ in the dbaas repo.
var (
	groupVersion       = "dbaas.opencloud.wso2.com/v1alpha1"
	apiGroup           = "dbaas.opencloud.wso2.com"
	apiVersionV1alpha1 = "v1alpha1"

	dbInstancesGVR = schema.GroupVersionResource{
		Group: apiGroup, Version: apiVersionV1alpha1, Resource: "dbinstances",
	}
	secretsGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "secrets",
	}
)

// Client implements providers.DatabaseProvisioner.
type Client struct {
	dyn dynamic.Interface
}

// NewClient builds a DBaaS provisioner backed by the given dynamic client.
// Reuse an existing dynamic client (e.g. from kubeovn.Client.Dynamic()) so
// dc-api keeps one connection pool, not two.
func NewClient(dyn dynamic.Interface) *Client {
	return &Client{dyn: dyn}
}

// Compile-time interface satisfaction check.
var _ providers.DatabaseProvisioner = (*Client)(nil)

// ─── DBInstance CR ──────────────────────────────────────────────────────────

// CreateDatabaseInstance creates a DBInstance CR in req.Namespace with the
// seven standard dc-api.wso2.com/* labels stamped on it. Returns the raw
// K8s API error on failure (handler maps AlreadyExists → 409).
func (c *Client) CreateDatabaseInstance(ctx context.Context, req providers.DatabaseInstanceCreateRequest) error {
	labels := map[string]interface{}{}
	for k, v := range req.Labels {
		labels[k] = v
	}

	// NetworkRef is emitted as a "<namespace>/<name>" string per dbaas CRD
	// pattern B (api/v1alpha1/dbinstance_types.go — NetworkRef is a string
	// field, not a struct). The handler's resolver guarantees both fields
	// are populated.
	networkRef := req.NetworkRef.Namespace + "/" + req.NetworkRef.Name

	specMap := map[string]interface{}{
		"dbInstanceClass":  req.InstanceClass,
		"allocatedStorage": int64(req.AllocatedStorageGB),
		"networkRef":       networkRef,
	}
	// engineVersion is reserved in the dbaas CRD (Task 2 enables it).
	// Carry it through so the row's view stays consistent — the controller
	// silently ignores fields it doesn't act on (contract §4.2).
	if req.EngineVersion != "" {
		specMap["engineVersion"] = req.EngineVersion
	}
	// osImage selects the Harvester VirtualMachineImage to boot from. When
	// empty the controller uses its own CRD default (which may not exist on
	// every cluster — see DCAPI_DBAAS_OS_IMAGE).
	if req.OSImage != "" {
		specMap["osImage"] = req.OSImage
	}
	// dnsServerIP pins the VM's resolver to the per-VPC CoreDNS (VPC mode).
	// Without it, a VM on an isolated OVN subnet can't resolve the apt archive
	// during cloud-init and Postgres never installs. Empty for legacy NADs.
	if req.NetworkRef.DNSServerIP != "" {
		specMap["dnsServerIP"] = req.NetworkRef.DNSServerIP
	}

	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": groupVersion,
		"kind":       "DBInstance",
		"metadata": map[string]interface{}{
			"name":      req.Name,
			"namespace": req.Namespace,
			"labels":    labels,
		},
		"spec": specMap,
	}}

	if _, err := c.dyn.Resource(dbInstancesGVR).Namespace(req.Namespace).Create(ctx, cr, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create DBInstance %s/%s: %w", req.Namespace, req.Name, err)
	}
	return nil
}

// GetDatabaseInstance returns the CR's translated status, or (nil, nil) when
// the CR does not exist (handler treats that as "not yet provisioned").
//
// The dbaas controller writes RDS-style status.phase strings (creating,
// available, modifying, stopped, starting, stopping, deleting, failed). The
// translation table below maps those to the contract-canonical five-phase
// enum (Pending / Provisioning / Ready / Failed / Terminating).
func (c *Client) GetDatabaseInstance(ctx context.Context, namespace, name string) (*providers.DatabaseInstanceStatus, error) {
	obj, err := c.dyn.Resource(dbInstancesGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get DBInstance %s/%s: %w", namespace, name, err)
	}

	status, _ := obj.Object["status"].(map[string]interface{})
	if status == nil {
		// CR exists but controller hasn't written status yet — return
		// an empty status so callers see Phase="" and requeue.
		return &providers.DatabaseInstanceStatus{}, nil
	}

	out := &providers.DatabaseInstanceStatus{}

	// Phase mapping. Empty string when unknown — handler keeps DB row status.
	rdsPhase, _ := status["phase"].(string)
	out.Phase = mapRDSPhase(rdsPhase)

	// Message preference: controller's status.message wins. Fall back to
	// provisioningPhase when message is empty so the user always sees a
	// hint of what's happening during long provisions (contract §5.1
	// Pattern C).
	out.Message, _ = status["message"].(string)
	if out.Message == "" {
		if pp, _ := status["provisioningPhase"].(string); pp != "" {
			out.Message = "provisioning: " + pp
		}
	}

	// status.endpoint — populated only after the controller reaches
	// DatabaseReady (the VM has an IP on the data NIC and PostgreSQL is
	// listening). Both fields stay empty until then.
	if ep, ok := status["endpoint"].(map[string]interface{}); ok {
		out.EndpointAddress, _ = ep["address"].(string)
		// JSON numbers unmarshal as float64 via the dynamic client.
		if p, ok := ep["port"].(float64); ok {
			out.EndpointPort = int(p)
		} else if p, ok := ep["port"].(int64); ok {
			out.EndpointPort = int(p)
		}
	}

	// status.masterUserSecret.name — dbaas-specific RDS-style placement
	// (not the framework default status.endpoint.secretRef.name).
	if mus, ok := status["masterUserSecret"].(map[string]interface{}); ok {
		out.SecretRefName, _ = mus["name"].(string)
	}

	return out, nil
}

// DeleteDatabaseInstance removes the DBInstance CR. The dbaas controller's
// finalizer runs the upstream cleanup (VM, DataVolumes, Service, Secret).
// Idempotent: NotFound is treated as success so a double-DELETE from the
// handler doesn't error.
func (c *Client) DeleteDatabaseInstance(ctx context.Context, namespace, name string) error {
	err := c.dyn.Resource(dbInstancesGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete DBInstance %s/%s: %w", namespace, name, err)
	}
	return nil
}

// GetDatabaseCredentialsSecret returns the raw data map of the credentials
// Secret the dbaas controller wrote (typical keys: admin_user, admin_password,
// ca_cert, server_cert, server_key, repl_password, exporter_password, luks_key).
// The handler picks the subset to surface on GET .../credentials.
// Returns (nil, nil) when the Secret does not exist.
func (c *Client) GetDatabaseCredentialsSecret(ctx context.Context, namespace, name string) (map[string][]byte, error) {
	obj, err := c.dyn.Resource(secretsGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get Secret %s/%s: %w", namespace, name, err)
	}

	raw, _ := obj.Object["data"].(map[string]interface{})
	out := make(map[string][]byte, len(raw))
	for k, v := range raw {
		// data field values come back as either base64-encoded string or
		// []byte depending on the decoder path.
		switch x := v.(type) {
		case []byte:
			out[k] = x
		case string:
			out[k] = decodeBase64Maybe(x)
		}
	}
	return out, nil
}

// DeleteCredentialsSecret removes the credentials Secret from Kubernetes.
// Idempotent: NotFound is treated as success.
func (c *Client) DeleteCredentialsSecret(ctx context.Context, namespace, name string) error {
	err := c.dyn.Resource(secretsGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// decodeBase64Maybe returns the base64-decoded bytes of s, or s as bytes
// when s is not valid base64. K8s API responses sometimes return secret
// data values already-decoded as []byte and sometimes as base64 strings
// depending on the decoder path; this normalises both.
func decodeBase64Maybe(s string) []byte {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b
	}
	return []byte(s)
}

// mapRDSPhase translates the dbaas controller's RDS-style status.phase
// strings into the framework's canonical five-phase enum (per
// docs/managed-services-integration.md §5.1 Pattern B).
//
// Power-cycle states (stopped / starting / stopping) are mapped to Ready
// per §5.4 — the status enum stays at five values; stop/start lives on a
// separate action endpoint (not implemented in v1).
//
// Empty string when the source phase is unknown — handler keeps the DB
// row's existing status (don't overwrite with garbage on a malformed CR).
func mapRDSPhase(phase string) string {
	switch phase {
	case "creating", "modifying", "starting":
		return "Provisioning"
	case "available", "stopped", "stopping":
		return "Ready"
	case "failed":
		return "Failed"
	case "deleting":
		return "Terminating"
	}
	return ""
}
