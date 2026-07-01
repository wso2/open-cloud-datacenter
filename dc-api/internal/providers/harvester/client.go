// Package harvester implements the providers.ComputeProvider interface
// against the Harvester HCI platform.
//
// How Harvester works (important context for SREs):
//
//	Harvester is built ON TOP OF Kubernetes. Every VM is a Kubernetes Custom
//	Resource of kind VirtualMachine (from the KubeVirt API). To create a VM,
//	you apply a VirtualMachine CRD manifest — exactly like applying a Deployment.
//
//	This means our Harvester driver is really a Kubernetes client that manages
//	VirtualMachine CRDs. We use client-go for this.
//
// BackendUID format:
//
//	We store BackendUID as "namespace:vmname" (e.g., "dc-teamalpha:web-01").
//	This lets GetVM and DeleteVM do a direct O(1) lookup by namespace+name
//	instead of a slow List+filter by Kubernetes UID. The name is deterministic
//	(it comes from the spec), making this safe.
//
// Namespace convention:
//
//	One Kubernetes namespace per project: "dc-<tenantID>-<projectID>".
//	The namespace must be created by the project handler (EnsureProjectNamespace)
//	before any VM can be created. CreateVM does not create or ensure the namespace.
package harvester

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
	"github.com/wso2/dc-api/internal/providers/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// vmImageGVR is the GroupVersionResource for Harvester VirtualMachineImages.
var vmImageGVR = schema.GroupVersionResource{
	Group:    "harvesterhci.io",
	Version:  "v1beta1",
	Resource: "virtualmachineimages",
}

// harvesterVMResource is the GroupVersionResource for KubeVirt VirtualMachines.
// This is the Kubernetes API endpoint we POST to when creating a VM.
var harvesterVMResource = schema.GroupVersionResource{
	Group:    "kubevirt.io",
	Version:  "v1",
	Resource: "virtualmachines",
}

// harvesterVMIResource is the GroupVersionResource for KubeVirt VirtualMachineInstances.
// The VMI is the running instance (like a Pod). qemu-guest-agent reports the IP here,
// not on the VirtualMachine object.
var harvesterVMIResource = schema.GroupVersionResource{
	Group:    "kubevirt.io",
	Version:  "v1",
	Resource: "virtualmachineinstances",
}

// namespacesGVR is the GroupVersionResource for core Kubernetes Namespaces.
// Group is empty string "" for core API resources (v1).
var namespacesGVR = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "namespaces",
}

// networkAttachmentGVR is the GroupVersionResource for Multus NetworkAttachmentDefinitions.
// In Harvester, VM networks are modelled as these CRDs.
var networkAttachmentGVR = schema.GroupVersionResource{
	Group:    "k8s.cni.cncf.io",
	Version:  "v1",
	Resource: "network-attachment-definitions",
}

// Client implements providers.ComputeProvider using the Harvester Kubernetes API.
type Client struct {
	dynamic    dynamic.Interface // Kubernetes dynamic client (handles CRDs and core resources)
	namespace  string            // fallback namespace (unused now — we derive from tenantID)
	restConfig *rest.Config      // stored so we can expose server URL + CA for SA kubeconfigs (F32)

	// access is the per-zone cluster-access seam (M-C). It defaults to a
	// clusteraccess.Direct wrapping `dynamic` (today's behaviour, byte-identical)
	// unless WithRoutedAccessor injects a routed accessor. The VM-object ops route
	// through it: GetVM's primary VirtualMachine read (read slice), CreateVM's
	// create + DeleteVM's delete (write slice 1), the collection reads
	// ListVMs/ListImages/ListNetworks (list slice — M-D), the image slice —
	// CreateImage's create and resolveImage's create-time storageClass lookup — and
	// the cloud-provider SA bootstrap (EnsureCloudProviderSA — the SA/RoleBinding/
	// Secret creates + the token-population Secret Get, F32). Only the VMI read
	// inside GetVM still calls c.dynamic directly. The seam never leaks an agent
	// concept past the ComputeProvider interface.
	access clusteraccess.Accessor

	// remoteRegion/remoteZone are non-empty ONLY for a credential-free REMOTE
	// client built by NewRemoteClient (multi-zone routing). For such a client
	// c.dynamic is nil: the seam ops (CreateVM/GetVM/DeleteVM via c.access, the
	// list reads via c.access.List, the image slice — CreateImage's create and
	// resolveImage's lookup — via c.access, and the cloud-provider SA bootstrap
	// EnsureCloudProviderSA — the three creates + the token-population Get via
	// c.access) route to that zone's agent. The two remaining direct paths behave
	// differently on a remote client:
	//   - GetVM's VMI IP enrichment read DEGRADES GRACEFULLY: it is SKIPPED (the
	//     `c.dynamic == nil` guard returns the agent-supplied VM status WITHOUT IP
	//     enrichment — no error), so a remote VM's status is still reported.
	//   - the one genuinely local-only op — the full VM create orchestration — has
	//     no direct path and returns a clear local-only error (localOnlyErr) naming
	//     the zone instead of panicking on a nil dynamic client.
	// Empty for the LOCAL client → today's behaviour.
	remoteRegion, remoteZone string
}

// localOnlyErr is returned by a REMOTE client's one remaining direct-only method:
// the full VM create orchestration (CreateVM — manifest build, MAC pinning, DNS
// injection — is not yet routed for a remote zone; see CreateVM's note). It
// touches c.dynamic, which a remote client does not have. Failing here — BEFORE a
// PENDING row or a provisioner call — is the documented local-only constraint for
// the remote-zone build. NOTE: image resolution/import (resolveImage,
// CreateImage), the collection reads, AND the cloud-provider SA bootstrap
// (EnsureCloudProviderSA) are NO LONGER local-only — they route through the
// cluster-access seam (c.access), so a remote client serves them via the agent.
// Only the full VM create orchestration remains behind this explicit error.
func (c *Client) localOnlyErr(op string) error {
	return fmt.Errorf(
		"%s is not supported for remote zone %s/%s yet: dc-api holds no direct Harvester credentials there (the full VM create orchestration is local-only for now)",
		op, c.remoteRegion, c.remoteZone)
}

// NewRemoteClient builds a credential-free Harvester client for a REMOTE zone.
// It has NO kubeconfig and NO dynamic client; every VM-object op runs through
// the supplied agent-only Accessor (a clusteraccess.Routed whose Direct fallback
// is a clusteraccess.NoCreds, so a missing agent fails clearly rather than
// hitting the local cluster). The direct-only methods return localOnlyErr.
//
// This is the second mandatory red-team gap: a remote provider cannot just stub
// Direct — it must route the lifecycle through the agent while refusing the
// direct-only ops. region/zone are carried only for clear error messages.
func NewRemoteClient(access clusteraccess.Accessor, region, zone string) *Client {
	return &Client{
		// dynamic + restConfig deliberately nil — guarded by remoteZone.
		access:       access,
		remoteRegion: region,
		remoteZone:   zone,
	}
}

// NewClient creates a Harvester client from a base64-encoded kubeconfig string.
// The kubeconfig is stored in DCAPI_HARVESTER_KUBECONFIG.
// It tries base64 decoding first; falls back to treating the input as a raw kubeconfig.
func NewClient(kubeconfigB64 string, namespace string) (*Client, error) {
	kubeconfigBytes, err := base64.StdEncoding.DecodeString(kubeconfigB64)
	if err != nil {
		// Not base64 — treat as raw kubeconfig YAML
		kubeconfigBytes = []byte(kubeconfigB64)
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("parse harvester kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create harvester dynamic client: %w", err)
	}

	return &Client{
		dynamic:    dynClient,
		namespace:  namespace,
		restConfig: restConfig,
		// Default seam: Direct on the same dynamic client → byte-identical to the
		// pre-M-C behaviour. providers.NewRegistry replaces this with a Routed
		// accessor via WithRoutedAccessor when the agent path is wired.
		access: clusteraccess.NewDirect(dynClient),
	}, nil
}

// Dynamic exposes the underlying dynamic client so the provider registry can
// build the Direct fallback of a Routed accessor over the SAME client every
// other method uses (no second connection, no kubeconfig re-parse). Mirrors
// kubeovn.Client.Dynamic().
func (c *Client) Dynamic() dynamic.Interface { return c.dynamic }

// WithRoutedAccessor replaces the client's cluster-access seam with a routed
// accessor produced from the client's own dynamic client. build receives a
// clusteraccess.Direct over c.dynamic (the Direct fallback the routed accessor
// must use so the off-path is byte-identical) and returns the Accessor to
// install. Returns the same *Client for chaining.
//
// The registry uses this so the Direct fallback and every not-yet-routed method
// share one dynamic client. A nil build (or a nil result) leaves the default
// Direct seam in place.
func (c *Client) WithRoutedAccessor(build func(direct clusteraccess.Accessor) clusteraccess.Accessor) *Client {
	if build == nil {
		return c
	}
	if a := build(clusteraccess.NewDirect(c.dynamic)); a != nil {
		c.access = a
	}
	return c
}

// Name satisfies providers.ComputeProvider.
func (c *Client) Name() string { return "harvester" }

// CreateVM creates a KubeVirt VirtualMachine CRD in Harvester.
//
// We use the dynamic client (not a typed client) because KubeVirt CRDs don't
// have an official Go client library. The dynamic client works with any CRD by
// using map[string]interface{} instead of generated Go structs.
//
// BackendUID stored as "namespace:vmname" so GetVM/DeleteVM can do O(1) lookups.
// projectID is the human-readable project slug; together with tenantID it
// determines the namespace: "dc-<tenant>-<project>".
// The namespace must already exist (created by the project handler via
// EnsureProjectNamespace). CreateVM does NOT create it — missing namespace
// is a handler-layer bug, not a recoverable provider condition.
func (c *Client) CreateVM(ctx context.Context, tenantID, projectID string, spec models.VMSpec) (*models.Resource, error) {
	if c.dynamic == nil {
		// REMOTE client: routing the full VM create lifecycle to a remote zone is a
		// later slice (resolveImage now routes through c.access, but wiring the whole
		// create — manifest build, MAC pinning, DNS injection — for a remote zone is
		// out of scope here). Fail clearly BEFORE building the manifest so the handler
		// surfaces a useful error rather than misrouting.
		return nil, c.localOnlyErr("VM create")
	}
	ns := common.NamespaceForProject(tenantID, projectID)

	// Resolve the image display name or ID to a "namespace/resource-name" string.
	// Callers may pass either the full ID ("default/image-abc123") or a display name
	// ("ubuntu-22.04"). We check if it looks like an ID first, then fall back to lookup.
	imageID, storageClass, err := c.resolveImage(ctx, spec.ImageName)
	if err != nil {
		return nil, fmt.Errorf("resolve image %q: %w", spec.ImageName, err)
	}

	// Derive a stable MAC address for this VM.  When the VPC path is used the
	// MAC must be pinned on both the KubeVirt interface and the KubeOVN
	// annotation so OVN port-security doesn't drop the VM's frames (gotcha 1).
	// We generate it from spec.ResourceID so it is deterministic across retries.
	mac := stableMAC(spec.ResourceID)

	vmManifest := buildVMManifest(spec, ns, imageID, storageClass, mac)

	// VM-object create goes through the cluster-access seam (c.access). With the
	// write toggle OFF (default) this is clusteraccess.Direct.Create — the exact
	// dynamic-client POST (.Resource(gvr).Namespace(ns).Create(...)) this method ran
	// before the write slice, so it is byte-identical. With the toggle ON and a live
	// agent for the zone, this routes to the agent's create, which is a server-side
	// apply (the only create mechanism the agent exposes); the agent error is
	// terminal (no silent Direct fallback). The asymmetry (direct=POST, agent=SSA)
	// lives only on the opt-in agent path. Image resolution above stays on
	// c.dynamic.
	_, err = c.access.Create(ctx, harvesterVMResource, ns, vmManifest, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("harvester create VM %q in %s: %w", spec.Name, ns, err)
	}

	// BackendUID = "namespace:name" — deterministic and directly addressable.
	// The handler sets the DB row's Type before calling here; the provider's
	// returned Resource is consumed only for BackendUID/Status — Type is just
	// echoed back for symmetry.
	backendUID := ns + ":" + spec.Name

	return &models.Resource{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Name:         spec.Name,
		Type:         models.ResourceTypeVM,
		Status:       models.StatusPending, // PENDING until reconciler confirms Running
		ProviderType: c.Name(),
		BackendUID:   backendUID,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, nil
}

// GetVM fetches the current live state of a VM from Harvester and maps it to
// our ResourceStatus. Called by the reconciler goroutine every 60 seconds.
//
// KubeVirt reports status via status.printableStatus:
//
//	"Running"                 → StatusActive
//	"Starting","Migrating"... → StatusPending  (transitional states)
//	"Terminating"             → StatusDeleting
//	error states              → StatusFailed
func (c *Client) GetVM(ctx context.Context, backendUID string) (*models.Resource, error) {
	ns, name, err := parseBackendUID(backendUID)
	if err != nil {
		return nil, err
	}

	// M-C read slice: the primary VirtualMachine read goes through the
	// cluster-access seam (c.access). With the agent toggle OFF (default) this
	// resolves to the exact c.dynamic.Resource(...).Get(...) call below; with it
	// ON and a live agent for the zone, it routes to Session.GetStatus. We read
	// only status.printableStatus from `obj`, which the agent's status snapshot
	// fully supplies. The VMI read further down stays on c.dynamic for this slice.
	obj, err := c.access.Get(ctx, harvesterVMResource, ns, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("VM %s not found in Harvester", backendUID)
		}
		return nil, fmt.Errorf("harvester get VM %s: %w", backendUID, err)
	}

	// Fetch the VMI (VirtualMachineInstance) for the IP address(es).
	// qemu-guest-agent reports IPs on the VMI, not the VM object.
	// It's OK if the VMI doesn't exist yet (VM still starting) — IPs stay empty.
	//
	// For dual-NIC VMs (F10 bastions), we extract two IPs by NIC name:
	// "ovn" → IPAddress (the OVN/tenant subnet IP)
	// "mgmt" → MgmtIP (the operator-reachable VLAN IP)
	// For single-NIC VMs neither is named "ovn" or "mgmt" (it's "default" or
	// the legacy single "ovn"), so fall back to the legacy "first IP" reader.
	var ip, mgmtIP string
	if c.dynamic == nil {
		// REMOTE client: status came from the agent (c.access above); IP
		// enrichment via the direct VMI read is local-only. Return the status
		// without IPs rather than dereferencing a nil dynamic client.
		return &models.Resource{
			Type:         models.ResourceTypeVM,
			ProviderType: c.Name(),
			BackendUID:   backendUID,
			Name:         name,
			Status:       vmStatusFromUnstructured(obj),
		}, nil
	}
	vmi, vmiErr := c.dynamic.
		Resource(harvesterVMIResource).
		Namespace(ns).
		Get(ctx, name, metav1.GetOptions{})
	if vmiErr != nil {
		log.Debug().Err(vmiErr).Str("vm", backendUID).Msg("VMI fetch failed")
	} else {
		ip, mgmtIP = vmIPByInterfaceName(vmi)
		if ip == "" && mgmtIP == "" {
			// Single-NIC VM whose NIC isn't named "ovn"/"mgmt" — fall back.
			ip = vmIPFromUnstructured(vmi)
		}
		// Log the raw VMI status.interfaces so we can see what Harvester returns.
		if ifaces, ok := vmi.Object["status"]; ok {
			raw, _ := json.Marshal(ifaces)
			log.Debug().Str("vm", backendUID).Str("ip", ip).Str("mgmt_ip", mgmtIP).RawJSON("vmi_status", raw).Msg("VMI status")
		}
	}

	return &models.Resource{
		Type:         models.ResourceTypeVM,
		ProviderType: c.Name(),
		BackendUID:   backendUID,
		Name:         name,
		Status:       vmStatusFromUnstructured(obj),
		IPAddress:    ip,
		MgmtIP:       mgmtIP,
	}, nil
}

// DeleteVM deletes a VirtualMachine CRD from Harvester.
// We use PropagationPolicy=Background so the API call returns immediately while
// Kubernetes garbage-collects the VMI, disks, and PVCs asynchronously.
func (c *Client) DeleteVM(ctx context.Context, backendUID string) error {
	ns, name, err := parseBackendUID(backendUID)
	if err != nil {
		return err
	}

	// VM-object delete goes through the cluster-access seam (c.access). With the
	// write toggle OFF (default) this is clusteraccess.Direct.Delete — the exact
	// dynamic-client delete with PropagationPolicy=Background as before, byte-
	// identical. With the toggle ON and a live agent for the zone, this routes to
	// Session.Delete; the agent error is terminal (no silent Direct fallback). On
	// both seams a missing object is a successful idempotent delete: Direct surfaces
	// a NotFound that IsNotFound swallows here; the agent maps Existed==false to a
	// nil error.
	propagation := metav1.DeletePropagationBackground
	err = c.access.Delete(ctx, harvesterVMResource, ns, name, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if err != nil && !k8serrors.IsNotFound(err) {
		// IsNotFound is fine — the VM is already gone, deletion goal is achieved.
		return fmt.Errorf("harvester delete VM %s: %w", backendUID, err)
	}
	return nil
}

// ListVMs returns all VMs in the tenant+project namespace.
// projectID is the human-readable project slug.
func (c *Client) ListVMs(ctx context.Context, tenantID, projectID string) ([]*models.Resource, error) {
	ns := common.NamespaceForProject(tenantID, projectID)
	// The VM list goes through the cluster-access seam (c.access). With the List
	// toggle OFF (default) this resolves to the exact
	// c.dynamic.Resource(gvr).Namespace(ns).List(...) call this method ran before
	// — byte-identical. With it ON and a live agent for the zone, it routes to
	// Session.List; the field extraction below operates on the returned items the
	// same way on both seams.
	list, err := c.access.List(ctx, harvesterVMResource, ns, metav1.ListOptions{
		LabelSelector: "dc-api/managed=true",
	})
	if err != nil {
		return nil, fmt.Errorf("harvester list VMs in %s: %w", ns, err)
	}

	resources := make([]*models.Resource, 0, len(list.Items))
	for _, item := range list.Items {
		resources = append(resources, unstructuredToResource(item, tenantID, ns))
	}
	return resources, nil
}

// ─────────────────────────── Helpers ────────────────────────────────────────

// ── F32: Cloud Provider SA bootstrap ─────────────────────────────────────────

// serviceAccountsGVR is the GroupVersionResource for Kubernetes ServiceAccounts.
var serviceAccountsGVR = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "serviceaccounts",
}

// roleBindingsGVR is the GroupVersionResource for Kubernetes RoleBindings.
var roleBindingsGVR = schema.GroupVersionResource{
	Group:    "rbac.authorization.k8s.io",
	Version:  "v1",
	Resource: "rolebindings",
}

// secretsGVR is the GroupVersionResource for Kubernetes Secrets.
var secretsGVR = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "secrets",
}

// HarvesterAPIInfo holds the Harvester apiserver URL and CA data needed to
// build a kubeconfig for a ServiceAccount token. Returned by APIInfo().
type HarvesterAPIInfo struct {
	// ServerURL is the Kubernetes API server URL from the kubeconfig.
	// Example: "https://192.168.10.6:6443"
	ServerURL string
	// CACert is the base64-encoded CA certificate bundle for the server.
	// Use this to populate kubeconfig clusters[].cluster.certificate-authority-data.
	CACert []byte
}

// APIInfo returns the Harvester apiserver URL and CA certificate for use when
// building per-cluster SA kubeconfigs (F32). The values come from the
// restConfig parsed from DCAPI_HARVESTER_KUBECONFIG at startup.
func (c *Client) APIInfo() HarvesterAPIInfo {
	return HarvesterAPIInfo{
		ServerURL: c.restConfig.Host,
		CACert:    c.restConfig.CAData,
	}
}

// HarvesterServerURL satisfies providers.HarvesterAPIInfoProvider.
// Returns the Harvester apiserver URL (e.g. "https://192.168.10.6:6443").
func (c *Client) HarvesterServerURL() string { return c.restConfig.Host }

// HarvesterCACert satisfies providers.HarvesterAPIInfoProvider.
// Returns the raw CA bundle bytes for embedding in kubeconfigs.
func (c *Client) HarvesterCACert() []byte { return c.restConfig.CAData }

// EnsureCloudProviderSA idempotently creates (or verifies the existence of)
// the three Kubernetes objects that the Harvester Cloud Provider plugin needs
// to register nodes and manage LoadBalancers for a tenant cluster:
//
//  1. ServiceAccount "harvester-cloud-provider-<ns>" in namespace <ns>.
//  2. RoleBinding "harvester-cloud-provider-<ns>" in <ns> binding to
//     ClusterRole "harvesterhci.io:cloudprovider".
//  3. Secret "harvester-cloud-provider-<ns>-token" of type
//     kubernetes.io/service-account-token with the SA annotation, so the
//     token controller populates .data.token.
//
// All three carry the label dc-api/managed=true for operator visibility.
// IsAlreadyExists on any create is treated as success (idempotent).
//
// Returns the raw SA token bytes (NOT base64-encoded — already decoded from
// the Secret's .data.token field). Polls up to 30 s for the token controller
// to populate the Secret after creation.
func (c *Client) EnsureCloudProviderSA(ctx context.Context, tenantNamespace string) ([]byte, error) {
	saName := "harvester-cloud-provider-" + tenantNamespace

	commonLabels := map[string]interface{}{
		"dc-api/managed": "true",
	}

	// The three creates and the token-population Get all go through the
	// cluster-access seam (c.access), so a REMOTE (agent-only) client serves them
	// via the agent. With the toggle OFF (default / local client) each is the exact
	// dynamic-client call this method ran before — byte-identical POST/GET. With the
	// toggle ON and a live agent for the zone, the creates route to the agent's
	// server-side apply and the Get to the agent's full-object read. SSA is
	// idempotent (apply-on-existing is a no-op update, not an already-exists error),
	// so the routed create won't hit IsAlreadyExists; the guard is kept for the
	// local POST path, where re-running the bootstrap must treat an existing object
	// as success.

	// ── Step 1: ServiceAccount ────────────────────────────────────────────────
	saObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ServiceAccount",
			"metadata": map[string]interface{}{
				"name":      saName,
				"namespace": tenantNamespace,
				"labels":    commonLabels,
			},
		},
	}
	_, err := c.access.Create(ctx, serviceAccountsGVR, tenantNamespace, saObj, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("create cloud-provider ServiceAccount in %s: %w", tenantNamespace, err)
	}

	// ── Step 2: RoleBinding ───────────────────────────────────────────────────
	rbObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "RoleBinding",
			"metadata": map[string]interface{}{
				"name":      saName,
				"namespace": tenantNamespace,
				"labels":    commonLabels,
			},
			"roleRef": map[string]interface{}{
				"apiGroup": "rbac.authorization.k8s.io",
				"kind":     "ClusterRole",
				"name":     "harvesterhci.io:cloudprovider",
			},
			"subjects": []interface{}{
				map[string]interface{}{
					"kind":      "ServiceAccount",
					"name":      saName,
					"namespace": tenantNamespace,
				},
			},
		},
	}
	_, err = c.access.Create(ctx, roleBindingsGVR, tenantNamespace, rbObj, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("create cloud-provider RoleBinding in %s: %w", tenantNamespace, err)
	}

	// ── Step 3: token Secret ─────────────────────────────────────────────────
	secretName := saName + "-token"
	secretObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      secretName,
				"namespace": tenantNamespace,
				"labels":    commonLabels,
				"annotations": map[string]interface{}{
					"kubernetes.io/service-account.name": saName,
				},
			},
			"type": "kubernetes.io/service-account-token",
		},
	}
	_, err = c.access.Create(ctx, secretsGVR, tenantNamespace, secretObj, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("create cloud-provider token Secret in %s: %w", tenantNamespace, err)
	}

	// ── Step 4: poll for token population ────────────────────────────────────
	// The Kubernetes token controller populates .data.token asynchronously
	// after seeing the kubernetes.io/service-account-token Secret.
	// Poll up to 30 s in 2 s intervals. The read routes through the seam
	// (c.access.Get) — the agent's full-object Get returns the Secret with its
	// populated .data.token.
	deadline := time.Now().Add(30 * time.Second)
	for {
		secret, err := c.access.Get(ctx, secretsGVR, tenantNamespace, secretName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get cloud-provider token Secret %s: %w", secretName, err)
		}
		data, _, _ := unstructured.NestedMap(secret.Object, "data")
		if tokenB64, ok := data["token"].(string); ok && tokenB64 != "" {
			tokenBytes, err := base64.StdEncoding.DecodeString(tokenB64)
			if err != nil {
				return nil, fmt.Errorf("decode SA token from Secret %s: %w", secretName, err)
			}
			log.Info().
				Str("namespace", tenantNamespace).
				Str("secret", secretName).
				Msg("harvester: cloud-provider SA token ready")
			return tokenBytes, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for token controller to populate Secret %s in %s", secretName, tenantNamespace)
		}
		// Brief sleep before next poll. Context cancellation is respected via the
		// next Get call that propagates ctx.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────

// parseBackendUID splits a "namespace:vmname" BackendUID into its parts.
func parseBackendUID(backendUID string) (namespace, name string, err error) {
	parts := strings.SplitN(backendUID, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf(
			"invalid harvester BackendUID %q — expected format \"namespace:vmname\"", backendUID)
	}
	return parts[0], parts[1], nil
}

// vmStatusFromUnstructured reads status.printableStatus from a KubeVirt
// VirtualMachine object and maps it to our internal ResourceStatus.
//
// KubeVirt printableStatus reference:
//
//	Running                → VM is up and accepting connections
//	Starting               → VM is booting
//	Stopping               → VM is shutting down
//	Stopped                → VM is off (we always provision in running state,
//	                         so Stopped means something went wrong)
//	Paused                 → VM is paused
//	Migrating              → VM is live-migrating to another node
//	Terminating            → VM is being deleted
//	WaitingForVolumeBinding→ PVC not yet bound (image still importing)
//	CrashLoopBackOff       → VM keeps crashing
//	ErrImagePull           → Image download failed
//	ImagePullBackOff       → Retrying image download
//	FailedUnschedulable    → No node has enough resources
//	Unknown                → Status not yet reported by KubeVirt
func vmStatusFromUnstructured(obj *unstructured.Unstructured) models.ResourceStatus {
	printable, _, _ := unstructured.NestedString(obj.Object, "status", "printableStatus")
	switch printable {
	case "Running":
		return models.StatusActive
	case "Terminating":
		return models.StatusDeleting
	case "CrashLoopBackOff", "ErrImagePull", "ImagePullBackOff",
		"FailedUnschedulable", "Stopped", "Paused":
		// Stopped/Paused are treated as Failed because we always start VMs in
		// running=true state. If a VM ends up stopped, something went wrong.
		return models.StatusFailed
	default:
		// Starting, Stopping, Migrating, WaitingForVolumeBinding, Unknown, ""
		return models.StatusPending
	}
}

// vmIPFromUnstructured reads the first IP address reported by qemu-guest-agent.
// KubeVirt exposes guest IPs via status.interfaces[].ipAddress once the agent is running.
// Returns empty string if the agent hasn't reported yet.
func vmIPFromUnstructured(obj *unstructured.Unstructured) string {
	ifaces, _, _ := unstructured.NestedSlice(obj.Object, "status", "interfaces")
	for _, iface := range ifaces {
		m, ok := iface.(map[string]interface{})
		if !ok {
			continue
		}
		if ip, ok := m["ipAddress"].(string); ok && ip != "" {
			return ip
		}
	}
	return ""
}

// vmIPByInterfaceName extracts IPs per NIC by the interface NAMES we set
// in buildVMManifest. Returns (ovnIP, mgmtIP). Either may be empty if the
// guest agent hasn't reported yet or that NIC wasn't configured.
//
// KubeVirt's status.interfaces[] has shape:
//
//	{ "name": "ovn",  "interfaceName": "enp2s0", "ipAddress": "10.55.1.5", ... }
//	{ "name": "mgmt", "interfaceName": "enp1s0", "ipAddress": "172.22.100.42", ... }
//
// We match by the .name field (which the manifest builder controls), NOT by
// the guest's interfaceName (which depends on PCI ordering and image quirks).
func vmIPByInterfaceName(obj *unstructured.Unstructured) (ovnIP, mgmtIP string) {
	ifaces, _, _ := unstructured.NestedSlice(obj.Object, "status", "interfaces")
	for _, iface := range ifaces {
		m, ok := iface.(map[string]interface{})
		if !ok {
			continue
		}
		ip, _ := m["ipAddress"].(string)
		if ip == "" {
			continue
		}
		switch m["name"] {
		case "ovn":
			ovnIP = ip
		case "mgmt":
			mgmtIP = ip
		}
	}
	return ovnIP, mgmtIP
}

// ListImages returns all VirtualMachineImages available in Harvester across all namespaces.
// The Image.ID field ("namespace/resource-name") is what callers pass to CreateVM.
func (c *Client) ListImages(ctx context.Context) ([]*models.Image, error) {
	// Routed through the cluster-access seam (c.access): Direct (byte-identical
	// cross-namespace dynamic List) when the toggle is OFF, Session.List when ON
	// and an agent serves the zone. Field extraction below is seam-agnostic.
	list, err := c.access.List(ctx, vmImageGVR, "", metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("harvester list images: %w", err)
	}
	images := make([]*models.Image, 0, len(list.Items))
	for _, item := range list.Items {
		displayName, _, _ := unstructured.NestedString(item.Object, "spec", "displayName")
		if displayName == "" {
			displayName = item.GetName()
		}
		images = append(images, &models.Image{
			ID:          item.GetNamespace() + "/" + item.GetName(),
			DisplayName: displayName,
			Namespace:   item.GetNamespace(),
		})
	}
	return images, nil
}

// ListNetworks returns all NetworkAttachmentDefinitions across all namespaces.
// The Network.ID field ("namespace/resource-name") is what callers pass to CreateVM.
// Harvester stores a human-readable label in annotation "network.harvesterhci.io/route"
// or falls back to the resource name.
func (c *Client) ListNetworks(ctx context.Context) ([]*models.Network, error) {
	// Routed through the cluster-access seam (c.access): Direct (byte-identical
	// cross-namespace dynamic List) when the toggle is OFF, Session.List when ON
	// and an agent serves the zone. Field extraction below is seam-agnostic.
	list, err := c.access.List(ctx, networkAttachmentGVR, "", metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("harvester list networks: %w", err)
	}
	networks := make([]*models.Network, 0, len(list.Items))
	for _, item := range list.Items {
		// Prefer the Harvester UI display label if present.
		displayName := item.GetAnnotations()["field.cattle.io/description"]
		if displayName == "" {
			displayName = item.GetName()
		}
		networks = append(networks, &models.Network{
			ID:          item.GetNamespace() + "/" + item.GetName(),
			DisplayName: displayName,
			Namespace:   item.GetNamespace(),
		})
	}
	return networks, nil
}

// CreateImage creates a VirtualMachineImage CRD in Harvester, which triggers
// Harvester to download the image from the given URL into Longhorn storage.
// The image is available for VM creation once its status transitions to "active".
//
// The object is created through the cluster-access seam (c.access.Create): the
// Direct path (toggle OFF) is a plain dynamic POST — byte-identical to the
// pre-seam behaviour except that the name is now client-assigned; the agent path
// (toggle ON + live agent) is a server-side apply, the agent's only create
// mechanism. SSA REQUIRES a name (generateName cannot SSA), so we assign a fixed
// "image-<rand>" name here rather than relying on the apiserver's generateName.
// A POST accepts the explicit name fine, so both seams agree. A REMOTE client
// (c.dynamic nil) now serves this through the agent — it no longer returns
// localOnlyErr.
func (c *Client) CreateImage(ctx context.Context, displayName, downloadURL string) (*models.Image, error) {
	// Images are created in the "default" namespace in Harvester.
	const imageNamespace = "default"

	// Client-assigned name: SSA (the agent create path) has no generateName
	// equivalent, so we must supply metadata.name. The short crypto-random suffix
	// mirrors what the apiserver's generateName would have produced ("image-XXXXX")
	// and keeps names collision-resistant. resolveImage matches by full ID, name,
	// OR displayName, so a client-assigned name is transparent to VM create.
	suffix, err := randHexSuffix()
	if err != nil {
		return nil, fmt.Errorf("harvester create image %q: generate name suffix: %w", displayName, err)
	}
	name := "image-" + suffix

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "harvesterhci.io/v1beta1",
			"kind":       "VirtualMachineImage",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": imageNamespace,
				"labels": map[string]interface{}{
					"dc-api/managed": "true",
				},
			},
			"spec": map[string]interface{}{
				"displayName": displayName,
				"sourceType":  "download",
				"url":         downloadURL,
			},
		},
	}

	created, err := c.access.Create(ctx, vmImageGVR, imageNamespace, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("harvester create image %q: %w", displayName, err)
	}

	return &models.Image{
		ID:          imageNamespace + "/" + created.GetName(),
		DisplayName: displayName,
		Namespace:   imageNamespace,
	}, nil
}

// randHexSuffix returns 8 hex characters (4 crypto-random bytes) for a
// client-assigned image name. It mirrors the "image-XXXXX" shape the apiserver's
// generateName produced, but is chosen here because the agent create path is a
// server-side apply, which requires a name up front.
func randHexSuffix() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// resolveImage resolves a user-supplied image string to a "namespace/resource-name" ID
// and the storage class Harvester created for it.
//
// The storage class is read from status.storageClassName on the VirtualMachineImage
// object — Harvester sets this once the image finishes importing. Do NOT derive it
// from the image name: that is fragile and will produce the wrong class name.
//
// Accepts either:
//   - A full ID:      "default/image-abc123"  (looked up by namespace+name)
//   - A display name: "ubuntu-22.04"          (looked up by spec.displayName)
func (c *Client) resolveImage(ctx context.Context, nameOrID string) (imageID, storageClass string, err error) {
	// Routed through the cluster-access seam (c.access): Direct (byte-identical
	// cross-namespace dynamic List) when the toggle is OFF, Session.List when ON
	// and an agent serves the zone. This is why a REMOTE client (c.dynamic nil)
	// can now resolve an image — the lookup no longer touches c.dynamic. Field
	// extraction (displayName, status.storageClassName) below is seam-agnostic.
	list, err := c.access.List(ctx, vmImageGVR, "", metav1.ListOptions{})
	if err != nil {
		return "", "", fmt.Errorf("list images for lookup: %w", err)
	}

	for _, item := range list.Items {
		fullID := item.GetNamespace() + "/" + item.GetName()
		displayName, _, _ := unstructured.NestedString(item.Object, "spec", "displayName")

		matched := fullID == nameOrID || item.GetName() == nameOrID || displayName == nameOrID
		if !matched {
			continue
		}

		sc, _, _ := unstructured.NestedString(item.Object, "status", "storageClassName")
		if sc == "" {
			return "", "", fmt.Errorf("image %q has no storageClassName yet — is it still importing?", nameOrID)
		}
		return fullID, sc, nil
	}
	return "", "", fmt.Errorf("image %q not found — run `dcctl get images` to list available images", nameOrID)
}

// buildVMIMetadata constructs the metadata block for the VMI pod template.
// Labels always include kubevirt.io/vm=<name> (required by KubeVirt).
// Annotations are only set when the map is non-empty to avoid cluttering
// the object with an empty annotations field.
func buildVMIMetadata(vmName string, annotations map[string]interface{}) map[string]interface{} {
	m := map[string]interface{}{
		"labels": map[string]interface{}{
			"kubevirt.io/vm": vmName,
		},
	}
	if len(annotations) > 0 {
		m["annotations"] = annotations
	}
	return m
}

// buildVMManifest constructs the KubeVirt VirtualMachine manifest.
// imageID must be in "namespace/resource-name" format (from resolveImage).
// Harvester clones the image into the VM's root disk via the harvesterhci.io/imageId annotation.
// The DataVolume storage class must be the image-specific class that Harvester creates for each
// VirtualMachineImage: "harvester-longhorn-<namespace>-<resource-name>".
//
// mac is the stable locally-administered MAC derived from the resource UUID (stableMAC).
// It is used on the VPC path to satisfy OVN port-security (gotcha 1).
// On the legacy bridge path it is also set so the interface definition is
// consistent — the bridge NAD does not enforce MAC allow-lists, so the value
// is harmless there.
func buildVMManifest(spec models.VMSpec, namespace, imageID, storageClass, mac string) *unstructured.Unstructured {
	memoryStr := fmt.Sprintf("%dGi", spec.MemoryGB)
	diskStr := fmt.Sprintf("%dGi", spec.DiskGB)
	rootDiskName := spec.Name + "-rootdisk"

	useVPC := spec.VNetBackendUID != "" && spec.SubnetBackendUID != ""

	// ── VMI template annotations ─────────────────────────────────────────────
	// On the VPC path we set three annotations on the VMI pod template:
	//   1. logical_switch — tells KubeOVN's IPAM which subnet to allocate from.
	//   2. ovn.kubernetes.io/mac_address — pins the MAC on the OVN LSP allow-list
	//      (the authoritative form that kube-ovn actually reads; gotcha 1).
	//   3. kubernetes.io/mac_address — belt-and-suspenders for older kube-ovn versions.
	// The annotation key format is "<nad-name>.<nad-namespace>.<type>/<field>".
	// NOTE: "kubevirt.io/vm" is a LABEL (not an annotation) and goes in spec.template.metadata.labels.
	vmiAnnotations := map[string]interface{}{}
	if useVPC {
		nadName := spec.SubnetBackendUID
		nadNS := namespace // NAD lives in the tenant namespace
		// logical_switch annotation routes IPAM to the correct KubeOVN subnet.
		vmiAnnotations[nadName+"."+nadNS+".kubernetes.io/logical_switch"] = nadName
		// MAC pinning — both annotation forms for kube-ovn compatibility.
		vmiAnnotations[nadName+"."+nadNS+".ovn.kubernetes.io/mac_address"] = mac
		vmiAnnotations[nadName+"."+nadNS+".kubernetes.io/mac_address"] = mac
		// Gotcha 6: tell multus to use the kubeovn NAD as the primary network.
		// In practice Canal still attaches eth0 on Harvester's RKE2, but setting
		// this signals intent and may affect future multus versions.
		vmiAnnotations["v1.multus-cni.io/default-network"] = nadNS + "/" + nadName
	}

	// ── Network attachment and interface ──────────────────────────────────────
	// Three shapes:
	//   - VPC + MgmtNAD (F10 bastion): dual NIC. nic-0 on mgmt NAD (first, so
	//     cloud-init's default metadata discovery finds it), nic-1 on OVN.
	//     MAC pinning applies to the OVN NIC only.
	//   - VPC only: single NIC named "ovn" on the KubeOVN NAD with MAC pin.
	//   - Legacy bridge: single NIC named "default" on the bridge NAD.
	var networks []interface{}
	var interfaces []interface{}
	switch {
	case useVPC && spec.MgmtNAD != "":
		nadRef := namespace + "/" + spec.SubnetBackendUID
		// No `default: true` on either entry — the working F32 cluster-node
		// pattern relies on the `v1.multus-cni.io/default-network` annotation
		// pointing at the OVN NAD (set in the useVPC vmiAnnotations block
		// above) to wire kube-ovn-cni as the primary attachment. Setting
		// `default: true` on the mgmt bridge NAD breaks pod sandbox setup
		// with "failed to find network info for sandbox".
		networks = []interface{}{
			map[string]interface{}{
				"name": "mgmt",
				"multus": map[string]interface{}{
					"networkName": spec.MgmtNAD,
				},
			},
			map[string]interface{}{
				"name": "ovn",
				"multus": map[string]interface{}{
					"networkName": nadRef,
				},
			},
		}
		interfaces = []interface{}{
			map[string]interface{}{
				"name":   "mgmt",
				"bridge": map[string]interface{}{},
			},
			map[string]interface{}{
				"name":       "ovn",
				"bridge":     map[string]interface{}{},
				"macAddress": mac,
			},
		}
	case useVPC:
		nadRef := namespace + "/" + spec.SubnetBackendUID
		networks = []interface{}{
			map[string]interface{}{
				"name": "ovn",
				"multus": map[string]interface{}{
					"networkName": nadRef,
					"default":     true, // make this the primary multus interface
				},
			},
		}
		interfaces = []interface{}{
			map[string]interface{}{
				"name":       "ovn",
				"bridge":     map[string]interface{}{},
				"macAddress": mac,
			},
		}
	default:
		networks = []interface{}{
			map[string]interface{}{
				"name": "default",
				"multus": map[string]interface{}{
					"networkName": spec.NetworkName,
				},
			},
		}
		interfaces = []interface{}{
			map[string]interface{}{
				"name":   "default",
				"bridge": map[string]interface{}{},
			},
		}
	}

	// ── CPU model (gotcha 2) ──────────────────────────────────────────────────
	// The lk-dev cluster has heterogeneous nodes (Skylake + IvyBridge). To enable
	// live migration across both, we pin cpu.model = IvyBridge (highest common
	// denominator). A production cluster with homogeneous CPUs does not need this;
	// omitting the field lets KubeVirt optimise. We set it unconditionally for the
	// lk cluster to match the verified spike configuration.
	// TODO(ops): query node cpu-model labels and compute intersection at deploy time.
	cpuSpec := map[string]interface{}{
		"cores": spec.CPU,
		"model": "IvyBridge",
	}

	// ── DNS policy injection (F20) ───────────────────────────────────────────
	// When the VM is placed on a VPC subnet, its virt-launcher pod's resolv.conf
	// must agree with what OVN's DHCP advertises as option 6 (dns_server).
	// Without this, KubeVirt's bridge-mode internal DHCP server (at 169.254.75.10)
	// copies the pod's /etc/resolv.conf (which has cluster DNS 10.53.0.10) into
	// the VM, overriding whatever OVN told the VM — the DHCP race we proved in the
	// spike. Setting dnsPolicy=None + dnsConfig.nameservers silences it.
	// See memory/project_f20_spike_outcome.md "Critical gotcha" section.
	var vmSpecPatch map[string]interface{}
	if spec.DNSServerIP != "" {
		// The DB stores dns_server_ip as INET, which Postgres canonicalises to
		// "10.55.1.2/32". KubeVirt's VM-validating admission webhook rejects
		// CIDR-form nameservers ("must be valid IP address"). Strip any /XX
		// suffix before injection.
		dnsIP := spec.DNSServerIP
		if i := strings.Index(dnsIP, "/"); i > 0 {
			dnsIP = dnsIP[:i]
		}
		nameservers := []interface{}{dnsIP}
		dnsConfig := map[string]interface{}{
			"nameservers": nameservers,
		}
		if searchDomain := spec.DNSSearchDomain; searchDomain != "" {
			dnsConfig["searches"] = []interface{}{searchDomain}
		}
		vmSpecPatch = map[string]interface{}{
			"dnsPolicy": "None",
			"dnsConfig": dnsConfig,
		}
	}

	// Merge vmSpecPatch into the vmi template spec map after construction.
	buildVMISpec := func(base map[string]interface{}) map[string]interface{} {
		if vmSpecPatch != nil {
			for k, v := range vmSpecPatch {
				base[k] = v
			}
		}
		return base
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":      spec.Name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"dc-api/managed": "true",
				},
				"annotations": map[string]interface{}{
					"dc-api/ssh-public-key": spec.SSHPublicKey,
				},
			},
			"spec": map[string]interface{}{
				"running": true,
				"template": map[string]interface{}{
					"metadata": buildVMIMetadata(spec.Name, vmiAnnotations),
					"spec": buildVMISpec(map[string]interface{}{
						"domain": map[string]interface{}{
							"cpu": cpuSpec,
							"resources": map[string]interface{}{
								"requests": map[string]interface{}{
									"memory": memoryStr,
								},
								"limits": map[string]interface{}{
									"memory": memoryStr,
								},
							},
							"devices": map[string]interface{}{
								"disks": []interface{}{
									map[string]interface{}{
										"name": "rootdisk",
										"disk": map[string]interface{}{"bus": "virtio"},
									},
									map[string]interface{}{
										"name": "cloudinit",
										"disk": map[string]interface{}{"bus": "virtio"},
									},
								},
								"interfaces": interfaces,
							},
						},
						"networks": networks,
						"volumes": []interface{}{
							map[string]interface{}{
								"name":       "rootdisk",
								"dataVolume": map[string]interface{}{"name": rootDiskName},
							},
							map[string]interface{}{
								"name": "cloudinit",
								"cloudInitNoCloud": map[string]interface{}{
									"userData": buildCloudInit(spec.SSHPublicKey, spec.Password, spec.MgmtNAD != ""),
								},
							},
						},
					}),
				},
				// Harvester clones the VirtualMachineImage into a new PVC via the
				// harvesterhci.io/imageId annotation. The source is blank — Harvester's
				// controller handles the actual data copy from the image.
				"dataVolumeTemplates": []interface{}{
					map[string]interface{}{
						"metadata": map[string]interface{}{
							"name": rootDiskName,
							"annotations": map[string]interface{}{
								"harvesterhci.io/imageId": imageID,
							},
						},
						"spec": map[string]interface{}{
							"source": map[string]interface{}{
								"blank": map[string]interface{}{},
							},
							"pvc": map[string]interface{}{
								"accessModes": []interface{}{"ReadWriteMany"},
								"volumeMode":  "Block",
								"resources": map[string]interface{}{
									"requests": map[string]interface{}{
										"storage": diskStr,
									},
								},
								"storageClassName": storageClass,
							},
						},
					},
				},
			},
		},
	}
}

// stableMAC derives a locally-administered MAC from a resource ID string.
// Uses the first 5 bytes of SHA-256(resourceID) with the `02:` prefix.
// The `02:` prefix sets the locally-administered bit (bit 1 of the first octet),
// avoiding collisions with globally-assigned (OUI-assigned) hardware MACs.
// This is deterministic: calling it twice with the same ID returns the same MAC.
func stableMAC(resourceID string) string {
	if resourceID == "" {
		// Fallback for tests that don't set ResourceID.  A fixed locally-
		// administered address is safe — no OUI collision possible.
		return "02:dc:00:00:00:00"
	}
	h := sha256.Sum256([]byte(resourceID))
	return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x", h[0], h[1], h[2], h[3], h[4])
}

// buildCloudInit generates a cloud-init user-data string that:
//   - Sets a console password on the default ubuntu user (for VNC/serial console login)
//   - Enables SSH password authentication as a fallback
//   - Injects the SSH public key into the default user's authorized_keys
//   - Installs and starts qemu-guest-agent so Harvester can report the VM's IP
//
// When dualNIC is true (F10 bastion path), also adds a bootcmd block that
// writes an explicit netplan to DHCP BOTH enp1s0 (mgmt) and enp2s0 (OVN).
// The Ubuntu cloud image's default netplan only configures the first NIC, so
// without this enp2s0 stays admin-DOWN and the bastion has no OVN connectivity.
// This is the same recipe validated for F32 RKE2 cluster nodes.
//
// We deliberately avoid a `users:` block. On Ubuntu cloud images the default user
// is "ubuntu"; adding a users: list without `- default` as the first entry replaces
// that definition and breaks the top-level `password:` directive.
func buildCloudInit(sshPublicKey, password string, dualNIC bool) string {
	bootcmd := ""
	if dualNIC {
		bootcmd = `bootcmd:
  - |
    cat > /etc/netplan/01-dhcp-all.yaml <<NETPLAN
    network:
      version: 2
      renderer: networkd
      ethernets:
        enp1s0:
          dhcp4: true
        enp2s0:
          dhcp4: true
    NETPLAN
  - chmod 600 /etc/netplan/01-dhcp-all.yaml
  - rm -f /etc/netplan/50-cloud-init.yaml
  - netplan apply
`
	}
	return fmt.Sprintf(`#cloud-config
password: %s
chpasswd: {expire: false}
ssh_pwauth: true
ssh_authorized_keys:
  - %s
packages:
  - qemu-guest-agent
%sruncmd:
  - systemctl enable qemu-guest-agent
  - systemctl start qemu-guest-agent
`, password, strings.TrimSpace(sshPublicKey), bootcmd)
}

// unstructuredToResource maps a KubeVirt VirtualMachine object to a models.Resource.
func unstructuredToResource(item unstructured.Unstructured, tenantID, ns string) *models.Resource {
	return &models.Resource{
		TenantID:     tenantID,
		Name:         item.GetName(),
		Type:         models.ResourceTypeVM,
		ProviderType: "harvester",
		BackendUID:   ns + ":" + item.GetName(),
		Status:       vmStatusFromUnstructured(&item),
	}
}
