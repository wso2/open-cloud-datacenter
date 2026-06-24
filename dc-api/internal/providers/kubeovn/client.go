// Package kubeovn implements the NetworkProvider interface against self-managed
// upstream KubeOVN running as a Multus secondary CNI on the Harvester cluster.
//
// ── Architecture overview ────────────────────────────────────────────────────
//
// KubeOVN exposes its objects as Kubernetes CRDs.  We use the same dynamic
// client pattern as the harvester driver — no KubeOVN SDK required.
//
// CRDs are cluster-scoped (Vpc, Subnet, VpcPeering, VpcDns) or namespaced
// (NetworkAttachmentDefinition).  NADs are created in the tenant namespace
// ("dc-<tenantID>") because Multus resolves them relative to the pod/VM
// namespace.
//
// ── Tagging convention (patch-not-delete, gotcha 4) ─────────────────────────
//
// Every route entry written by this driver carries a KubeOVN "ecmpMode"
// comment that embeds the owning resource UUID:
//
//	routetable-<uuid>   for RouteTable-owned staticRoutes
//	peering-<uuid>      for VpcPeering reciprocal staticRoutes
//
// ACL entries written onto Subnet.spec.acls embed the NSG UUID in the
// OVN ACL "name" field:
//
//	nsg-<nsg-uuid>/<rule-name>
//
// When updating or deleting, the driver reads the current slice, filters out
// entries whose comment/name starts with the owning tag, then writes the
// remainder (plus any new entries) back via JSON MergePatch.  The parent CRD
// is NEVER deleted just to change a field.
//
// ── Phantom IP detection (gotcha 5) ─────────────────────────────────────────
//
// KubeOVN creates an "ips.kubeovn.io" object for every pod/VM in the cluster,
// including Canal-only pods that don't use KubeOVN IPAM.  These are phantom
// entries.  isPhantomIP() detects them: if the IP CRD's spec.ipAddress doesn't
// match the pod's status.podIP, it's a phantom and must not be treated as a
// real allocation.  This driver does not currently query IP CRDs directly, but
// the helper is here for future callers (GetSubnet status enrichment, etc.).
//
// ── DNS: VpcDns vs ConfigMap fallback ───────────────────────────────────────
//
// The driver first attempts to use the "vpcdnses" CRD.  If the cluster's
// KubeOVN installation does not have the VpcDns CRD (older installs or
// stripped Helm chart), it falls back to a ConfigMap-per-zone in the tenant
// namespace named "dc-dns-<zone-uuid>".  The fallback is structured for
// CoreDNS file-plugin consumption.  Detection happens at zone create time via
// a check against the API server's CRD list; the result is cached per-client.
//
// ── MAC pinning boundary (gotcha 1) ─────────────────────────────────────────
//
// KubeOVN enforces port-security per OVN logical switch port (LSP), NOT
// per Subnet — verified against KubeOVN v1.15 CRD: there is no
// `privateSecurityGroups` (or any equivalent) on Subnet.spec. An earlier
// comment here claimed otherwise; that was a hallucination. The actual
// per-port toggle is the pod annotation
// `<nad>.<ns>.kubernetes.io/port_security: "false"` (templated form) or
// `ovn.kubernetes.io/port_security: "false"` (direct form). Both are
// per-pod and must be set on the launcher pod / VMI template at creation.
//
// The kubeovn driver is responsible for:
//   - Creating Subnet CRDs cleanly. Port-security toggling does not happen
//     here because there is nothing to toggle at this level.
//
// The kubeovn driver is NOT responsible for:
//   - Pinning the MAC on the VM.  That is the compute (harvester) driver's job
//     when it constructs the VM manifest.  The VM's multus annotation
//     "<nad>.<ns>.ovn.kubernetes.io/mac_address" and
//     domain.devices.interfaces[].macAddress must match each other.
//     This driver only ensures the Subnet CRD setup doesn't undermine it.
package kubeovn

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
	"github.com/wso2/dc-api/internal/providers/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ── GroupVersionResources ─────────────────────────────────────────────────────
//
// Plural resource names verified against a KubeOVN v1.15 installation on the
// lk-dev Harvester cluster (2026-05-06):
//
//   kubectl get crd | grep kubeovn.io
//   → vpcs.kubeovn.io
//   → subnets.kubeovn.io
//   → vpc-peerings.kubeovn.io    (note the hyphen — not "vpcpeerings")
//   → vpcdnses.kubeovn.io
//   → ips.kubeovn.io
//
// If the VpcDns CRD is missing (older install), the driver falls back to
// ConfigMaps (see vpcDnsAvailable field and DNS methods below).
var (
	vpcGVR        = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpcs"}
	subnetGVR     = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "subnets"}
	// vpcPeeringGVR removed — KubeOVN v1.15 (and earlier since v1.10) does NOT
	// register a standalone `vpc-peerings.kubeovn.io` CRD. Peering is a field
	// on the parent Vpc CRD itself: `spec.vpcPeerings: [{remoteVpc, ...}]`.
	// See `patchVpcVpcPeerings` below.
	vpcDnsGVR     = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpcdnses"}
	ipGVR         = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "ips"}
	nadGVR        = schema.GroupVersionResource{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}
	namespacesGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
)

// Client is the KubeOVN NetworkProvider driver.
type Client struct {
	dynamic    dynamic.Interface
	restConfig *rest.Config // retained so callers (kvi proxy) can build typed HTTP clients
	namespace  string       // KubeOVN daemon namespace (default: "kube-ovn") — used only for NAD provider sync notes

	// access is the per-zone cluster-access seam (mirrors harvester.Client.access).
	// It defaults to clusteraccess.Direct over `dynamic` (byte-identical to the
	// pre-seam behaviour) unless WithRoutedAccessor injects a Routed accessor. ONLY
	// the CRD CRUD lifecycle of the three onboarded network families routes through
	// it: CreateVNet/GetVNet/DeleteVNet (Vpc), the Subnet+NAD create/get/delete in
	// CreateSubnet/GetSubnet/DeleteSubnet. Everything else (the stuck-finalizer
	// recovery, ACL/route/peering patches, DNS ConfigMaps, NAT/CoreDNS plumbing,
	// namespace/quota provisioning) still calls c.dynamic directly — those GVRs are
	// not in the agent's mapper/RBAC and are deferred to a later slice. The seam
	// never leaks an agent concept past the NetworkProvider interface.
	access clusteraccess.Accessor

	// vpcDnsAvailable is set to true at construction time if the
	// vpcdnses.kubeovn.io CRD exists on the cluster.  If false, the driver
	// falls back to ConfigMaps for DNS zone management.
	vpcDnsAvailable bool

	// extNet holds the F15 external network configuration injected via
	// WithExternalNetwork(). nil means F15 NAT is not configured.
	extNet *ExternalNetworkConfig

	// dnsConf holds the F20 per-VPC DNS configuration injected via
	// WithDNSConfig(). nil means F20 DNS is not configured.
	dnsConf *DNSConfig

	// remoteRegion/remoteZone are non-empty ONLY for a credential-free REMOTE
	// client built by NewRemoteClient. For such a client c.dynamic is nil and the
	// network CRD lifecycle is NOT yet routed through the agent (the network
	// plumbing — NAD creation, ACL/route patches, NAT/CoreDNS — has no agent
	// path), so every NetworkProvider method returns a clear local-only error
	// naming the zone instead of dereferencing a nil dynamic client. Empty for the
	// LOCAL client → today's behaviour. This is the documented boundary of the
	// remote-zone build: VPC networking for a remote zone is deferred behind this
	// explicit error rather than silently misrouting to the local cluster.
	remoteRegion, remoteZone string
}

// localOnlyErr is returned by a REMOTE client's NetworkProvider methods. dc-api
// holds no kubeconfig for a remote zone, and the kubeovn lifecycle still leans on
// c.dynamic for the NAD/ACL/route/NAT/DNS plumbing that has no agent path. Per
// the design, remote-zone VPC networking is deferred behind this clear error.
func (c *Client) localOnlyErr(op string) error {
	return fmt.Errorf(
		"%s is not supported for remote zone %s/%s yet: dc-api holds no direct KubeOVN credentials there and the VPC network plumbing (NAD/ACL/route/NAT/DNS) is local-only for now",
		op, c.remoteRegion, c.remoteZone)
}

// NewRemoteClient builds a credential-free KubeOVN client for a REMOTE zone. It
// has NO kubeconfig and NO dynamic client; every NetworkProvider method returns
// localOnlyErr. region/zone are carried only for clear error messages. This
// keeps the per-zone resolver uniform (every zone yields a *ProviderSet) while
// remote-zone networking stays explicitly gated rather than misrouted.
func NewRemoteClient(region, zone string) *Client {
	return &Client{remoteRegion: region, remoteZone: zone}
}

// New creates a KubeOVN Client.
//
//   - kubeconfig: base64-encoded kubeconfig string (same as DCAPI_HARVESTER_KUBECONFIG).
//     Falls back to treating the input as raw YAML if base64 decoding fails.
//   - namespace: KubeOVN daemon namespace, typically "kube-ovn".
//     Used only as informational metadata in NAD annotations; all tenant CRDs
//     are in "dc-<tenantID>" namespaces.
func New(kubeconfig, namespace string) (*Client, error) {
	kubeconfigBytes, err := base64.StdEncoding.DecodeString(kubeconfig)
	if err != nil {
		// Not base64 — treat as raw kubeconfig YAML.
		kubeconfigBytes = []byte(kubeconfig)
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("parse kubeovn kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create kubeovn dynamic client: %w", err)
	}

	c := &Client{
		dynamic:    dynClient,
		restConfig: restConfig,
		namespace:  namespace,
		// Default seam: Direct on the SAME dynamic client → byte-identical to the
		// pre-seam behaviour. providers.NewRegistry replaces this with a Routed
		// accessor via WithRoutedAccessor when the agent path is wired.
		access: clusteraccess.NewDirect(dynClient),
	}

	// Probe for VpcDns CRD availability (best-effort; if the probe call itself
	// fails for non-404 reasons, we conservatively fall back to ConfigMap mode).
	ctx := context.Background()
	_, probeErr := dynClient.Resource(vpcDnsGVR).List(ctx, metav1.ListOptions{Limit: 1})
	if probeErr == nil {
		c.vpcDnsAvailable = true
		log.Info().Msg("kubeovn: VpcDns CRD detected — using VpcDns for private DNS zones")
	} else {
		log.Warn().Err(probeErr).Msg("kubeovn: VpcDns CRD not available — falling back to ConfigMap DNS mode")
	}

	return c, nil
}

// Name satisfies providers.NetworkProvider.
func (c *Client) Name() string { return "kubeovn" }

// Dynamic exposes the underlying dynamic client so other packages — in
// particular providers/endpoints — can construct their own resource clients
// without duplicating kubeconfig parsing.
func (c *Client) Dynamic() dynamic.Interface { return c.dynamic }

// WithRoutedAccessor replaces the client's cluster-access seam with a routed
// accessor produced from the client's own dynamic client. build receives a
// clusteraccess.Direct over c.dynamic (the Direct fallback the routed accessor
// must use so the off-path is byte-identical) and returns the Accessor to
// install. Returns the same *Client for chaining. A nil build (or nil result)
// leaves the default Direct seam in place. Mirrors harvester.Client verbatim so
// the registry can wire both compute and network with one decision closure.
func (c *Client) WithRoutedAccessor(build func(direct clusteraccess.Accessor) clusteraccess.Accessor) *Client {
	if build == nil {
		return c
	}
	if a := build(clusteraccess.NewDirect(c.dynamic)); a != nil {
		c.access = a
	}
	return c
}

// RESTConfig exposes the underlying *rest.Config so callers (e.g. the KVI
// OpenBao proxy) can build typed Kubernetes REST clients against the same
// cluster without re-parsing the kubeconfig.
func (c *Client) RESTConfig() *rest.Config { return c.restConfig }

// ── VNet ─────────────────────────────────────────────────────────────────────

// CreateVNet provisions a KubeOVN Vpc CRD.
//
// KubeOVN's Vpc CRD has no cidrBlock field — a VPC is an OVN logical router,
// not an address space.  The address_space is enforced entirely by DC-API at
// the handler layer; the driver passes it through in the returned resource
// for the DB row but does NOT write it to the CRD.
//
// The Vpc CRD is cluster-scoped; name = "vnet-<uuid>".
// projectID is the human-readable project slug; together with tenantID it
// determines the Kubernetes namespace: "dc-<tenant>-<project>". The namespace
// must already exist (created by the project handler via EnsureProjectNamespace).
func (c *Client) CreateVNet(ctx context.Context, tenantID, projectID string, spec models.VNetSpec) (*models.VNetResource, error) {
	if c.dynamic == nil {
		return nil, c.localOnlyErr("CreateVNet")
	}
	ns := common.NamespaceForProject(tenantID, projectID)

	vpc := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1",
			"kind":       "Vpc",
			"metadata": map[string]interface{}{
				"name": vpcName(spec.Name, tenantID, projectID),
				"labels": map[string]interface{}{
					"dc-api/managed":   "true",
					"dc-api/tenant":    tenantID,
					"dc-api/project":   projectID,
					"dc-api/vnet-name": spec.Name,
				},
				"annotations": map[string]interface{}{
					// Store description as an annotation — Vpc CRD has no description field.
					"dc-api/description": spec.Description,
				},
			},
			"spec": map[string]interface{}{
				// namespaces: list of Kubernetes namespaces whose pods are
				// allowed to use this VPC.  We seed it with the project namespace;
				// additional namespaces can be added later.
				"namespaces": []interface{}{ns},
				// staticRoutes and policyRoutes start empty; RouteTable operations
				// append to / remove from these slices via PATCH.
				"staticRoutes": []interface{}{},
			},
		},
	}

	// Vpc is cluster-scoped → ns="" (Direct's .Namespace("") is the cluster-scoped
	// client, identical to the prior c.dynamic.Resource(vpcGVR).Create). Routes via
	// the agent only under DCAPI_AGENT_ROUTE_WRITES + a live agent; otherwise Direct.
	created, err := c.access.Create(ctx, vpcGVR, "", vpc, metav1.CreateOptions{})
	if err != nil {
		if k8serrors.IsAlreadyExists(err) {
			// Idempotent: return the existing VPC's name as BackendUID.
			return &models.VNetResource{
				BackendUID: vpcName(spec.Name, tenantID, projectID),
				Status:     models.StatusActive,
				Message:    "vpc already exists",
			}, nil
		}
		return nil, fmt.Errorf("create kubeovn vpc %q: %w", vpcName(spec.Name, tenantID, projectID), err)
	}

	return &models.VNetResource{
		BackendUID: created.GetName(),
		Status:     models.StatusPending,
		Message:    "vpc created, waiting for kubeovn controller to initialise",
	}, nil
}

// GetVNet returns the current provider state of a VNet.
func (c *Client) GetVNet(ctx context.Context, backendUID string) (*models.VNetResource, error) {
	if c.dynamic == nil {
		return nil, c.localOnlyErr("GetVNet")
	}
	// Cluster-scoped → ns="". Routes via the agent only under DCAPI_AGENT_ROUTE_READS
	// + a live agent; otherwise the byte-identical Direct Get.
	obj, err := c.access.Get(ctx, vpcGVR, "", backendUID, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("vpc %q not found", backendUID)
		}
		return nil, fmt.Errorf("get kubeovn vpc %q: %w", backendUID, err)
	}
	return &models.VNetResource{
		BackendUID: obj.GetName(),
		Status:     vpcStatus(obj),
		Message:    vpcMessage(obj),
	}, nil
}

// DeleteVNet removes the KubeOVN Vpc CRD.
//
// IMPORTANT: KubeOVN's finalizer holds the delete until all Subnet CRDs
// referencing this VPC are removed.  The caller (handler/reconciler) MUST
// delete child subnets before calling DeleteVNet.  This driver does NOT
// force-remove finalizers.
func (c *Client) DeleteVNet(ctx context.Context, backendUID string) error {
	if c.dynamic == nil {
		return c.localOnlyErr("DeleteVNet")
	}
	// Cluster-scoped → ns="". Routes via the agent only under DCAPI_AGENT_ROUTE_WRITES
	// + a live agent; otherwise the byte-identical Direct Delete.
	err := c.access.Delete(ctx, vpcGVR, "", backendUID, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete kubeovn vpc %q: %w", backendUID, err)
	}
	return nil
}

// ── Subnet ───────────────────────────────────────────────────────────────────

// CreateSubnet provisions a KubeOVN Subnet CRD and a NetworkAttachmentDefinition.
//
// Two objects are created atomically (best-effort — no Kubernetes transaction):
//  1. A kubeovn.io/v1 Subnet CRD (cluster-scoped) named "subnet-<uuid>".
//  2. A k8s.cni.cncf.io/v1 NetworkAttachmentDefinition in the tenant namespace,
//     also named "subnet-<uuid>".
//
// The NAD's provider field and the Subnet's spec.provider field MUST match
// exactly for KubeOVN's IPAM to bind the multus interface to the right logical
// switch port.  The convention is "<nad-name>.<tenant-namespace>.ovn".
//
// Port-security is left at its default (enabled) so OVN enforces the MAC
// allow-list.  The harvester driver is responsible for pinning the MAC on the
// VM side (gotcha 1); our job is to NOT disable port-security here.
//
// vnetUID is the KubeOVN Vpc CRD name (the backendUID of the parent VNet row).
func (c *Client) CreateSubnet(ctx context.Context, vnetUID string, spec models.SubnetSpec) (*models.SubnetResource, error) {
	if c.dynamic == nil {
		return nil, c.localOnlyErr("CreateSubnet")
	}
	// Derive tenant namespace from the Vpc name ("vnet-<name>-<tenantID>" → extract tenantID).
	// The Vpc CRD holds a dc-api/tenant label — fetch it rather than parsing.
	vpcObj, err := c.dynamic.Resource(vpcGVR).Get(ctx, vnetUID, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("create subnet: fetch parent vpc %q: %w", vnetUID, err)
	}
	tenantID, _, _ := unstructured.NestedString(vpcObj.Object, "metadata", "labels", "dc-api/tenant")
	if tenantID == "" {
		return nil, fmt.Errorf("create subnet: vpc %q missing dc-api/tenant label", vnetUID)
	}
	projectID, _, _ := unstructured.NestedString(vpcObj.Object, "metadata", "labels", "dc-api/project")
	if projectID == "" {
		return nil, fmt.Errorf("create subnet: vpc %q missing dc-api/project label", vnetUID)
	}
	ns := common.NamespaceForProject(tenantID, projectID)

	// Derive stable names from the parent VNet, subnet spec name, and tenant.
	// Including vnetUID keeps subnet names globally unique per-tenant even when
	// two VNets use the same human-readable subnet name (e.g. "sub-app" in
	// vnet-A and vnet-B). Pre-existing subnets keep their stored backend_uid
	// in the DB so this is a forward-only change.
	nadName := subnetResourceName(vnetUID, spec.Name, tenantID, projectID)
	subnetName := nadName // Subnet and NAD share the same name for O(1) lookup.
	providerStr := nadName + "." + ns + ".ovn"

	// Determine gateway: default to first usable IP if omitted.
	gw := spec.Gateway
	if gw == "" {
		gw = firstUsableIP(spec.CIDR)
	}

	// ── 1. NetworkAttachmentDefinition ───────────────────────────────────────
	nadConfig := fmt.Sprintf(`{"cniVersion":"0.3.1","type":"kube-ovn","server_socket":"/run/openvswitch/kube-ovn-daemon.sock","provider":%q}`,
		providerStr)

	nad := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k8s.cni.cncf.io/v1",
			"kind":       "NetworkAttachmentDefinition",
			"metadata": map[string]interface{}{
				"name":      nadName,
				"namespace": ns,
				"labels": map[string]interface{}{
					"dc-api/managed":      "true",
					"dc-api/tenant":       tenantID,
					"dc-api/subnet-name":  spec.Name,
					"dc-api/parent-vnet":  vnetUID,
				},
			},
			"spec": map[string]interface{}{
				"config": nadConfig,
			},
		},
	}

	// NAD is namespaced → pass ns. Routes via the agent under DCAPI_AGENT_ROUTE_WRITES
	// + a live agent; otherwise the byte-identical Direct Create.
	if _, err := c.access.Create(ctx, nadGVR, ns, nad, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create NAD %q in %s: %w", nadName, ns, err)
		}
	}

	// Wait for Harvester's network-manager controller to stamp
	// `network.harvesterhci.io/ready=true` on the NAD. The Harvester admission
	// webhook validating kubeovn Subnet creation reads the NAD's type label
	// (set by the same controller); if we race ahead, it rejects with
	// "network type of nad is not kubeovn instead".
	if err := c.waitForNADReady(ctx, ns, nadName); err != nil {
		// Rollback the NAD we just created through the same accessor it was created
		// with, so the create+rollback pair share one credential.
		_ = c.access.Delete(ctx, nadGVR, ns, nadName, metav1.DeleteOptions{})
		return nil, fmt.Errorf("create subnet: %w", err)
	}

	// ── 2. Subnet CRD ────────────────────────────────────────────────────────
	subnet := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1",
			"kind":       "Subnet",
			"metadata": map[string]interface{}{
				"name": subnetName,
				"labels": map[string]interface{}{
					"dc-api/managed":     "true",
					"dc-api/tenant":      tenantID,
					"dc-api/subnet-name": spec.Name,
					"dc-api/parent-vnet": vnetUID,
				},
			},
			"spec": map[string]interface{}{
				"vpc":       vnetUID,
				"protocol":  "IPv4",
				"provider":  providerStr,
				"cidrBlock": spec.CIDR,
				"gateway":   gw,
				"excludeIps": []interface{}{gw}, // exclude gateway IP from IPAM pool
				"namespaces": []interface{}{ns},
				// private: false — keep OVN's default allow-all between subnets
				// in the same VPC.  Inter-subnet restrictions are applied via ACLs
				// (NSG attach), not by setting private=true.
				"private": false,
				// (Port-security is per-LSP, set via pod annotations — not via
				// any Subnet field. See the gotcha 1 comment at the top of this
				// file.)
				"acls": []interface{}{}, // empty; populated by AttachNSGToSubnet
			},
		},
	}

	// Subnet is cluster-scoped → ns="". Routes via the agent under
	// DCAPI_AGENT_ROUTE_WRITES + a live agent; otherwise the byte-identical Direct.
	if _, err := c.access.Create(ctx, subnetGVR, "", subnet, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			// Best-effort NAD cleanup on Subnet create failure — through the same
			// accessor the NAD was created with (single-credential rollback).
			_ = c.access.Delete(ctx, nadGVR, ns, nadName, metav1.DeleteOptions{})
			return nil, fmt.Errorf("create kubeovn subnet %q: %w", subnetName, err)
		}
	}

	return &models.SubnetResource{
		BackendUID: subnetName,
		Status:     models.StatusPending,
		Message:    "subnet created, waiting for kubeovn controller to assign IPAM",
	}, nil
}

// GetSubnet returns the current provider state of a Subnet.
func (c *Client) GetSubnet(ctx context.Context, backendUID string) (*models.SubnetResource, error) {
	if c.dynamic == nil {
		return nil, c.localOnlyErr("GetSubnet")
	}
	// Cluster-scoped → ns="". Routes via the agent under DCAPI_AGENT_ROUTE_READS
	// + a live agent; otherwise the byte-identical Direct Get.
	obj, err := c.access.Get(ctx, subnetGVR, "", backendUID, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("subnet %q not found", backendUID)
		}
		return nil, fmt.Errorf("get kubeovn subnet %q: %w", backendUID, err)
	}
	return &models.SubnetResource{
		BackendUID: obj.GetName(),
		Status:     subnetStatus(obj),
		Message:    subnetMessage(obj),
	}, nil
}

// DeleteSubnet removes the KubeOVN Subnet CRD and the matching NAD.
//
// ACLs must be cleared before the Subnet CRD can be deleted (the KubeOVN
// finalizer blocks deletion while consumers exist).  The caller ensures VMs
// detach first; the driver patches ACLs to empty before issuing the delete.
func (c *Client) DeleteSubnet(ctx context.Context, backendUID string) error {
	if c.dynamic == nil {
		return c.localOnlyErr("DeleteSubnet")
	}
	// First: fetch the subnet to find the tenant namespace (for the NAD).
	// Cluster-scoped → ns="". This Get is the leading read of the delete lifecycle,
	// so it routes with the same accessor as the Delete below (under the reads
	// toggle); otherwise the byte-identical Direct Get.
	obj, err := c.access.Get(ctx, subnetGVR, "", backendUID, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil // already gone — idempotent
		}
		return fmt.Errorf("delete subnet: fetch %q: %w", backendUID, err)
	}
	tenantID, _, _ := unstructured.NestedString(obj.Object, "metadata", "labels", "dc-api/tenant")
	projectID, _, _ := unstructured.NestedString(obj.Object, "metadata", "labels", "dc-api/project")
	ns := common.NamespaceForProject(tenantID, projectID)
	if projectID == "" {
		// Fallback for subnets created before the project label was added.
		// Read the parent VPC to get the project label.
		parentVnet, _, _ := unstructured.NestedString(obj.Object, "metadata", "labels", "dc-api/parent-vnet")
		if parentVnet != "" {
			if vpcObj, err2 := c.dynamic.Resource(vpcGVR).Get(ctx, parentVnet, metav1.GetOptions{}); err2 == nil {
				projectID, _, _ = unstructured.NestedString(vpcObj.Object, "metadata", "labels", "dc-api/project")
			}
		}
		if projectID != "" {
			ns = common.NamespaceForProject(tenantID, projectID)
		} else {
			// Last-resort fallback: keep the old tenant-only namespace derivation
			// so existing resources created pre-M2.5 can still be deleted cleanly.
			ns = "dc-" + strings.ToLower(tenantID)
		}
	}

	// Clear ACLs before deleting to avoid finalizer deadlock (gotcha 4).
	if err := c.patchSubnetACLs(ctx, backendUID, []interface{}{}); err != nil {
		log.Warn().Err(err).Str("subnet", backendUID).Msg("kubeovn: failed to clear ACLs before subnet delete; proceeding anyway")
	}

	// Delete the Subnet CRD. Cluster-scoped → ns="". This is the PRIMARY delete verb
	// the reconciler observes; it routes via the agent under DCAPI_AGENT_ROUTE_WRITES
	// + a live agent, otherwise the byte-identical Direct Delete. (The stuck-finalizer
	// recovery below stays on c.dynamic — a cold, best-effort self-heal that needs
	// ips/pods verbs the network families deliberately do NOT declare.)
	if err := c.access.Delete(ctx, subnetGVR, "", backendUID, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete kubeovn subnet %q: %w", backendUID, err)
	}

	// Wait for the KubeOVN Subnet to fully disappear before deleting the NAD —
	// the Harvester network webhook rejects NAD deletion while a Subnet still
	// references it via finalizer. KubeOVN typically clears the finalizer in
	// 1-3s once consumers (VMs / ACLs) are gone.
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stuck := false
	for {
		_, err := c.dynamic.Resource(subnetGVR).Get(ctx, backendUID, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			break
		}
		select {
		case <-waitCtx.Done():
			stuck = true
		case <-time.After(1 * time.Second):
			continue
		}
		break
	}

	// F16: KubeOVN's finalizer occasionally doesn't clear within the wait —
	// usually a webhook cycle between the kubeovn Subnet and the matching NAD,
	// or a stale LSP reference in OVN northbound DB. The resulting "stuck
	// Terminating Subnet" then jams the parent tenant namespace forever.
	//
	// Force-remove the finalizer as a last resort, BUT only when no LSPs
	// remain on the subnet — force-removing while LSPs are live silently
	// corrupts OVN-nb (orphan IPAM entries that block re-allocation when the
	// CIDR is recreated). If LSPs are still pinning, return an error naming
	// them so the caller / operator can clean up first.
	//
	// When the pre-check passes, the trade is debuggable orphan logical
	// switch (scrub via `kubectl ko nbctl ls-del`) vs permanently wedged
	// tenant namespace. We pick orphan switch.
	if stuck {
		// Ownership guard: only force-remove finalizers on a Subnet we created.
		// The caller's chain (handler reads DB row, passes BackendUID) already
		// ensures this — but a hand-crafted name collision or a future
		// refactor bug shouldn't be able to wipe finalizers on someone else's
		// kubeovn Subnet. The dc-api/managed=true label is set by CreateSubnet
		// and is the canonical marker of dc-api ownership.
		obj, ownErr := c.dynamic.Resource(subnetGVR).Get(ctx, backendUID, metav1.GetOptions{})
		if ownErr != nil {
			if k8serrors.IsNotFound(ownErr) {
				// Cleaned up between our wait loop and now — nothing to do.
				return nil
			}
			return fmt.Errorf("delete subnet %q: re-fetch for ownership check: %w", backendUID, ownErr)
		}
		if obj.GetLabels()["dc-api/managed"] != "true" {
			return fmt.Errorf(
				"delete subnet %q: subnet exists but does not carry dc-api/managed=true label — refusing to force-remove finalizers on a subnet dc-api didn't create",
				backendUID)
		}
		lspList, lspErr := c.dynamic.Resource(ipGVR).List(ctx, metav1.ListOptions{
			LabelSelector: "ovn.kubernetes.io/subnet=" + backendUID,
		})
		if lspErr != nil {
			return fmt.Errorf("delete subnet %q: list LSPs to verify safe force-remove: %w", backendUID, lspErr)
		}
		if len(lspList.Items) > 0 {
			names := make([]string, 0, len(lspList.Items))
			for _, ip := range lspList.Items {
				names = append(names, ip.GetName())
			}
			return fmt.Errorf(
				"delete subnet %q: KubeOVN finalizer stuck and %d LSPs still pinning the subnet (%s) — delete those resources first",
				backendUID, len(lspList.Items), strings.Join(names, ", "))
		}
		log.Warn().Str("subnet", backendUID).Msg("kubeovn: subnet stuck after 30s with no LSPs — force-removing finalizers")
		patch := []byte(`{"metadata":{"finalizers":[]}}`)
		if _, err := c.dynamic.Resource(subnetGVR).Patch(ctx, backendUID, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil && !k8serrors.IsNotFound(err) {
			log.Warn().Err(err).Str("subnet", backendUID).Msg("kubeovn: finalizer force-remove failed; proceeding with NAD delete anyway")
		} else {
			// Give Kubernetes GC a moment to actually remove the object now
			// that the finalizer is gone.
			time.Sleep(2 * time.Second)
		}
	}

	// Delete the NAD — same name, in the tenant namespace (namespaced → pass ns).
	// Routes via the agent under DCAPI_AGENT_ROUTE_WRITES + a live agent; otherwise
	// the byte-identical Direct Delete.
	if err := c.access.Delete(ctx, nadGVR, ns, backendUID, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete NAD %q in %s: %w", backendUID, ns, err)
	}

	// F43: the NAD has its own finalizer
	// (wrangler.cattle.io/harvester-network-manager-nad-controller). If the
	// harvester-network-manager controller is slow or unable to process it,
	// the NAD stays in Terminating, which pins the parent tenant namespace
	// in Terminating too. Mirror F16's three-layer safety: poll briefly,
	// then force-remove the NAD finalizer only when (1) the NAD didn't
	// drain on its own, (2) it carries the dc-api/managed=true ownership
	// label, and (3) no pod still references it via Multus annotation.
	if err := c.forceRemoveNADFinalizerIfStuck(ctx, ns, backendUID); err != nil {
		// Log only — primary subnet/NAD delete already issued; the namespace
		// teardown caller will surface this if it actually blocks.
		log.Warn().Err(err).Str("nad", backendUID).Str("ns", ns).
			Msg("kubeovn: NAD finalizer cleanup encountered an issue")
	}

	return nil
}

// forceRemoveNADFinalizerIfStuck implements the F43 NAD-finalizer fallback.
//
// Returns nil when the NAD drained on its own OR when force-remove succeeded
// OR when the safety pre-checks refuse (in those refusal cases we log and
// move on — F16's caller will eventually report a stuck namespace if the
// admin needs to intervene).
func (c *Client) forceRemoveNADFinalizerIfStuck(ctx context.Context, ns, nadName string) error {
	// 1. Poll up to ~15s for graceful drain. The wrangler controller usually
	//    clears its finalizer in well under that window when healthy.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, err := c.dynamic.Resource(nadGVR).Namespace(ns).Get(ctx, nadName, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return nil // graceful — nothing to do
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}

	// 2. Still around after 15s — apply safety pre-checks before patching.
	obj, err := c.dynamic.Resource(nadGVR).Namespace(ns).Get(ctx, nadName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("re-fetch NAD %q: %w", nadName, err)
	}

	// Pre-check 1: ownership label. CreateSubnet sets dc-api/managed=true on
	// every NAD we create; this guard refuses to wipe finalizers off a NAD
	// dc-api didn't create (defensive — should never trip in practice).
	if obj.GetLabels()["dc-api/managed"] != "true" {
		log.Warn().Str("nad", nadName).Str("ns", ns).
			Msg("kubeovn: NAD stuck but missing dc-api/managed=true label — refusing to force-remove finalizers")
		return nil
	}

	// Pre-check 2: scan all pods for an active Multus annotation referencing
	// this NAD. The annotation form is "<ns>/<nad-name>" (we used it
	// extensively in chunk-2 proxy pods, F20 CoreDNS, F15 NAT GW). If any
	// pod still references the NAD, removing it now would silently break
	// that pod's secondary-NIC binding when it next gets scheduled.
	ref := ns + "/" + nadName
	pods, err := c.dynamic.Resource(podGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list pods to verify safe NAD force-remove: %w", err)
	}
	for _, p := range pods.Items {
		annotations := p.GetAnnotations()
		if annotations == nil {
			continue
		}
		// Match either the bare reference "<ns>/<nad>" (used by Pod-shape
		// referers like spike-debug, chunk-2 proxies) or the JSON-array
		// form virt-launcher pods use ({"name":"<nad>","namespace":"<ns>"}).
		if strings.Contains(annotations["k8s.v1.cni.cncf.io/networks"], ref) ||
			strings.Contains(annotations["k8s.v1.cni.cncf.io/networks"], `"name":"`+nadName+`"`) {
			log.Warn().Str("nad", nadName).Str("ns", ns).Str("pod", p.GetNamespace()+"/"+p.GetName()).
				Msg("kubeovn: NAD stuck but a pod still references it — refusing to force-remove finalizers (delete that pod first)")
			return nil
		}
	}

	// All three layers cleared — force-remove. Same merge-patch shape F16
	// uses for the Subnet.
	log.Warn().Str("nad", nadName).Str("ns", ns).
		Msg("kubeovn: NAD stuck after 15s with no pod references — force-removing finalizers")
	patch := []byte(`{"metadata":{"finalizers":[]}}`)
	if _, err := c.dynamic.Resource(nadGVR).Namespace(ns).Patch(ctx, nadName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("patch NAD %q finalizers: %w", nadName, err)
	}
	return nil
}

// ── Route Table ──────────────────────────────────────────────────────────────

// CreateRouteTable appends the spec's routes to the parent Vpc.spec.staticRoutes.
//
// Route tables are NOT separate KubeOVN CRDs — they are logical groupings in
// DC-API's data model.  All routes end up on the VPC (M2 stance (a)).
// Each route entry is tagged with the route-table UUID in the entry's
// "routeTable" annotation field so updates / deletes can filter by ownership.
//
// backend_uid returned = parent Vpc CRD name (routes live there, not in their
// own CRD). See RouteTableResource.BackendUID docs in network.go.
//
// This method is synchronous (no reconciler loop needed).
func (c *Client) CreateRouteTable(ctx context.Context, vnetUID string, spec models.RouteTableSpec) (*models.RouteTableResource, error) {
	if c.dynamic == nil {
		return nil, c.localOnlyErr("CreateRouteTable")
	}
	if len(spec.Routes) == 0 {
		// Nothing to write to the VPC — return immediately.
		return &models.RouteTableResource{
			BackendUID: vnetUID,
			Status:     models.StatusActive,
			Message:    "route table created (empty)",
		}, nil
	}

	// Build the tagged route entries.  We use the RouteTable Name as the
	// human-friendly identifier; the UUID tag is added by UpdateRouteTableRoutes
	// which is always the true source of truth for the VPC patch.
	// For the initial create, we delegate directly to UpdateRouteTableRoutes.
	// The RouteTableSpec.Name is passed via the spec; we use vnetUID as both
	// the vnetUID and as the route-table "backendUID" in UpdateRouteTableRoutes.
	//
	// Callers: the handler creates a DB row (gets a UUID), then calls
	// CreateRouteTable which returns vnetUID as BackendUID.  It then calls
	// UpdateRouteTableRoutes(ctx, backendUID=<routeTableUUID>, routes).
	// For the initial create, we have no route-table UUID at this layer.
	// We return the vnetUID and let the handler call UpdateRouteTableRoutes
	// next with the real UUID.  This matches the design in interface.go.
	return &models.RouteTableResource{
		BackendUID: vnetUID,
		Status:     models.StatusActive,
		Message:    "route table created; call UpdateRouteTableRoutes to apply routes",
	}, nil
}

// UpdateRouteTableRoutes replaces the set of routes owned by this route table.
//
// backendUID here is a composite "<vnetUID>/<routeTableUUID>".  The handler
// must encode it in this format so the driver can identify both the target VPC
// and the tag prefix.  Format: "vnet-<name>-<tenantID>/<routeTableUUID>".
//
// Steps:
//  1. Read current Vpc.spec.staticRoutes.
//  2. Remove all entries whose routetable-tag matches <routeTableUUID>.
//  3. Append new entries tagged with <routeTableUUID>.
//  4. JSON MergePatch the Vpc — never delete the CRD (gotcha 4).
func (c *Client) UpdateRouteTableRoutes(ctx context.Context, backendUID string, routes []models.RouteRule) error {
	if c.dynamic == nil {
		return c.localOnlyErr("UpdateRouteTableRoutes")
	}
	vnetUID, rtUUID, err := parseRouteTableUID(backendUID)
	if err != nil {
		return fmt.Errorf("update route table routes: %w", err)
	}

	// Fetch current VPC.
	vpc, err := c.dynamic.Resource(vpcGVR).Get(ctx, vnetUID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("update route table routes: fetch vpc %q: %w", vnetUID, err)
	}

	// Read existing staticRoutes.
	existing, _, _ := unstructured.NestedSlice(vpc.Object, "spec", "staticRoutes")

	// Filter out entries owned by this route table.
	tag := routeTableTag(rtUUID)
	filtered := filterSliceByTag(existing, "routeTable", tag)

	// Build new entries.
	newEntries := buildStaticRouteEntries(routes, tag)

	// Merge and patch.
	merged := append(filtered, newEntries...)
	return c.patchVPCStaticRoutes(ctx, vnetUID, merged)
}

// DeleteRouteTable removes only the route entries owned by this route table.
//
// backendUID format: "<vnetUID>/<routeTableUUID>" (same as UpdateRouteTableRoutes).
// The Vpc CRD itself is NOT deleted.
func (c *Client) DeleteRouteTable(ctx context.Context, backendUID string) error {
	if c.dynamic == nil {
		return c.localOnlyErr("DeleteRouteTable")
	}
	vnetUID, rtUUID, err := parseRouteTableUID(backendUID)
	if err != nil {
		return fmt.Errorf("delete route table: %w", err)
	}

	vpc, err := c.dynamic.Resource(vpcGVR).Get(ctx, vnetUID, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil // VPC already gone — nothing to clean
		}
		return fmt.Errorf("delete route table: fetch vpc %q: %w", vnetUID, err)
	}

	existing, _, _ := unstructured.NestedSlice(vpc.Object, "spec", "staticRoutes")
	tag := routeTableTag(rtUUID)
	filtered := filterSliceByTag(existing, "routeTable", tag)

	return c.patchVPCStaticRoutes(ctx, vnetUID, filtered)
}

// AssociateRouteTable is a no-op in M2 (stance a): all routes already apply
// at the VPC level.  The association is purely informational, stored in the
// DC-API DB only.  When OVN policy routes land in M2.5 (issue #152), this
// method will patch Vpc.spec.policyRoutes.
func (c *Client) AssociateRouteTable(_ context.Context, _, _ string) error {
	if c.dynamic == nil {
		return c.localOnlyErr("AssociateRouteTable")
	}
	// M2 stance (a): routes apply VPC-wide.  No backend change.
	// See m2-network-api-design.md § 13 Decision 3.
	return nil
}

// DisassociateRouteTable is a no-op for the same reason as AssociateRouteTable.
func (c *Client) DisassociateRouteTable(_ context.Context, _, _ string) error {
	if c.dynamic == nil {
		return c.localOnlyErr("DisassociateRouteTable")
	}
	return nil
}

// ── NSG ──────────────────────────────────────────────────────────────────────

// CreateNSG creates an NSG record.
//
// NSG itself has no backend CRD until it is attached to a subnet via
// AttachNSGToSubnet.  The driver records backend_uid = the NSG UUID string
// itself (no KubeOVN CRD created here).  Rule application happens at attach
// time.
func (c *Client) CreateNSG(_ context.Context, _, _ string, spec models.NSGSpec) (*models.NSGResource, error) {
	if c.dynamic == nil {
		return nil, c.localOnlyErr("CreateNSG")
	}
	// No KubeOVN object created.  BackendUID = "" (will be populated when
	// attached to a subnet).  The handler generates the UUID and stores it.
	return &models.NSGResource{
		BackendUID: "", // populated at attach time
		Status:     models.StatusActive,
		Message:    "nsg record created; attach to a subnet to apply rules",
	}, nil
}

// UpdateNSGRules replaces the ACL rule set for this NSG on all currently
// attached subnets.
//
// backendUID here is the NSG UUID (set at attach time, stored in the DB row).
// The caller must pass the list of attached subnet backend UIDs separately;
// this driver reads the attached subnets from the spec/DB via the handler,
// but the interface only passes backendUID and rules.
//
// IMPORTANT: because the interface does not pass the list of attached subnets,
// this method accepts a specially formatted backendUID:
//
//	"<nsgUUID>|<subnetUID1>|<subnetUID2>|..."
//
// The handler encodes attached subnet UIDs pipe-separated into the backendUID
// before calling this method.  Subnet UIDs are the KubeOVN Subnet CRD names.
// If no subnets are attached (no "|" separator), the method is a no-op
// (rules are buffered in the DB only).
func (c *Client) UpdateNSGRules(ctx context.Context, backendUID string, rules []models.NSGRule) error {
	if c.dynamic == nil {
		return c.localOnlyErr("UpdateNSGRules")
	}
	nsgUID, subnetUIDs := parseNSGBackendUID(backendUID)
	if len(subnetUIDs) == 0 {
		// NSG not yet attached — rules are buffered in DC-API DB.
		return nil
	}

	aclEntries := buildACLEntries(rules, nsgUID)

	for _, subnetUID := range subnetUIDs {
		if err := c.replaceNSGACLsOnSubnet(ctx, subnetUID, nsgUID, aclEntries); err != nil {
			return fmt.Errorf("update nsg rules on subnet %q: %w", subnetUID, err)
		}
	}
	return nil
}

// DeleteNSG removes the NSG.  The caller (handler) guarantees no attachments
// exist (returns 409 if attachments remain).  Nothing to do at the backend.
func (c *Client) DeleteNSG(_ context.Context, _ string) error {
	if c.dynamic == nil {
		return c.localOnlyErr("DeleteNSG")
	}
	// No backend CRD exists for an unattached NSG.  Attached NSGs must be
	// detached (which patches the Subnet CRD) before calling DeleteNSG.
	return nil
}

// AttachNSGToSubnet writes the NSG's rules to the target Subnet CRD's
// spec.acls field (stateless OVN ACLs).
//
// Steps (patch-not-delete, gotcha 4):
//  1. Read current Subnet.spec.acls.
//  2. Remove any existing entries with name prefix "nsg-<nsgUID>/".
//  3. Append new entries for each rule.
//  4. MergePatch Subnet.spec.acls.
//
// Rule translation: DC-API NSGRule → KubeOVN ACL entry.
// nsgUID is the NSG UUID (no "nsg-" prefix — the driver adds it internally).
// subnetUID is the KubeOVN Subnet CRD name.
func (c *Client) AttachNSGToSubnet(ctx context.Context, nsgUID, subnetUID string) error {
	if c.dynamic == nil {
		return c.localOnlyErr("AttachNSGToSubnet")
	}
	// For attach with no rules provided, we fetch the NSG rules from the
	// backendUID.  However, this interface only gives us nsgUID and subnetUID.
	// The caller should ensure UpdateNSGRules is called after attach to push
	// the actual rules.  AttachNSGToSubnet here reserves the ACL "slot" by
	// appending an empty tag-marker, which UpdateNSGRules then replaces.
	//
	// This is consistent: attach = "register ownership"; UpdateNSGRules =
	// "write the actual ACLs".  The handler sequence is:
	//   1. AttachNSGToSubnet(nsgUID, subnetUID) — creates the slot
	//   2. UpdateNSGRules(backendUID, rules)    — writes the ACLs
	//
	// For attach we do a no-op read-then-verify to ensure the subnet exists.
	_, err := c.dynamic.Resource(subnetGVR).Get(ctx, subnetUID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("attach nsg: get subnet %q: %w", subnetUID, err)
	}
	// ACLs are written by UpdateNSGRules — no-op here beyond the existence check.
	return nil
}

// DetachNSGFromSubnet removes the NSG's ACL entries from the Subnet CRD
// via PATCH.  The Subnet CRD is NOT deleted (gotcha 4).
func (c *Client) DetachNSGFromSubnet(ctx context.Context, nsgUID, subnetUID string) error {
	if c.dynamic == nil {
		return c.localOnlyErr("DetachNSGFromSubnet")
	}
	// Read current ACLs.
	subnet, err := c.dynamic.Resource(subnetGVR).Get(ctx, subnetUID, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil // subnet already gone — nothing to do
		}
		return fmt.Errorf("detach nsg: get subnet %q: %w", subnetUID, err)
	}

	existing, _, _ := unstructured.NestedSlice(subnet.Object, "spec", "acls")
	tag := nsgACLTag(nsgUID)
	filtered := filterACLsByTag(existing, tag)

	return c.patchSubnetACLs(ctx, subnetUID, filtered)
}

// ── Peering ───────────────────────────────────────────────────────────────────

// CreatePeering creates a KubeOVN VpcPeering CRD and adds reciprocal
// staticRoutes to both VPC CRDs.
//
// The allow_forwarded_traffic field is stored in the DC-API DB but is NOT
// enforced by this driver in M2 (per § 14 "allow_forwarded_traffic on
// VNetPeering — accepted but no-op").  The returned PeeringResource carries a
// warning message explaining this.
//
// Route tagging: entries on both VPCs are tagged with "peering-<peeringUUID>"
// in the "routeTable" field of each staticRoutes entry, enabling clean removal
// in DeletePeering.
//
// Both vnetUID and peerVnetUID are KubeOVN Vpc CRD names.
// BUG A FIX: KubeOVN v1.15 (and v1.10+) does not have a standalone
// vpc-peerings.kubeovn.io CRD. Peering is configured as entries on each Vpc
// CRD's spec.vpcPeerings field. We patch both VPCs read-modify-write.
//
// BackendUID format: "<vnetA>/<vnetB>" (composite). DeletePeering parses
// this to recover the two VPC names without needing a separate CRD lookup.
//
// Reciprocal staticRoutes addition keeps the same per-peering tag so they
// can be cleanly removed on delete.
func (c *Client) CreatePeering(ctx context.Context, vnetUID, peerVnetUID string, spec models.PeeringSpec) (*models.PeeringResource, error) {
	if c.dynamic == nil {
		return nil, c.localOnlyErr("CreatePeering")
	}
	backendUID := vnetUID + "/" + peerVnetUID

	// F6: the peering handler allocates the transit /24 from a DB-backed
	// pool and passes it through `spec.TransitCIDR`. Empty falls back to
	// the legacy SHA-256 hash so existing peerings created pre-F6 keep
	// working on the localConnectIP they already advertise.
	transitNetwork := spec.TransitCIDR

	// Append the peering entry on both VPCs (sets localConnectIP deterministically).
	if err := c.appendVpcPeering(ctx, vnetUID, peerVnetUID, transitNetwork); err != nil {
		return nil, fmt.Errorf("create peering: patch %q: %w", vnetUID, err)
	}
	if err := c.appendVpcPeering(ctx, peerVnetUID, vnetUID, transitNetwork); err != nil {
		return nil, fmt.Errorf("create peering: patch %q: %w", peerVnetUID, err)
	}

	// Add reciprocal staticRoutes for the *whole* peer address-space.
	// Azure-style semantics: peering exposes the entire peer VNet, not just
	// existing subnets. Falls back to per-subnet CIDRs if the spec is missing
	// the address-space (legacy callers).
	vnetCIDRs := spec.AddressSpace
	peerCIDRs := spec.PeerAddressSpace
	if len(vnetCIDRs) == 0 {
		var err error
		vnetCIDRs, err = c.subnetCIDRsForVPC(ctx, vnetUID)
		if err != nil {
			return nil, fmt.Errorf("create peering: collect subnets for %q: %w", vnetUID, err)
		}
	}
	if len(peerCIDRs) == 0 {
		var err error
		peerCIDRs, err = c.subnetCIDRsForVPC(ctx, peerVnetUID)
		if err != nil {
			return nil, fmt.Errorf("create peering: collect subnets for %q: %w", peerVnetUID, err)
		}
	}

	// Routes go to the default (empty) routeTable so subnets pick them up.
	// nextHopIP is the PEER's localConnectIP address — confirmed live on
	// KubeOVN v1.15 (2026-05-09): writing 0.0.0.0 as a sentinel does NOT get
	// resolved by the controller; the route lands in OVN with literal 0.0.0.0
	// and packets have no real next-hop. We must compute the peer's transit
	// IP and write it explicitly.
	if err := c.appendPeeringRoutes(ctx, vnetUID, peerCIDRs, transitLocalIPAddr(peerVnetUID, vnetUID, transitNetwork)); err != nil {
		return nil, fmt.Errorf("create peering: add routes on %q: %w", vnetUID, err)
	}
	if err := c.appendPeeringRoutes(ctx, peerVnetUID, vnetCIDRs, transitLocalIPAddr(vnetUID, peerVnetUID, transitNetwork)); err != nil {
		return nil, fmt.Errorf("create peering: add routes on %q: %w", peerVnetUID, err)
	}

	warning := ""
	if spec.AllowForwardedTraffic {
		warning = "allow_forwarded_traffic is accepted but not yet enforced — slated for M2.5"
	}

	return &models.PeeringResource{
		BackendUID: backendUID,
		Status:     models.StatusActive, // peering is synchronous now (no CRD finalizer wait)
		Message:    warning,
	}, nil
}

// DeletePeering removes the peering entry from both VPCs' spec.vpcPeerings
// and the reciprocal staticRoutes that were added for this peering.
//
// backendUID is "<vnetA>/<vnetB>" (the composite stored in peerings.backend_uid).
// localCIDRs is the address-space of vnetA; peerCIDRs is the address-space of vnetB.
// Both are required to identify which staticRoutes to remove now that routes
// are in the default (empty) routeTable and cannot be filtered by a named tag.
func (c *Client) DeletePeering(ctx context.Context, backendUID string, localCIDRs, peerCIDRs []string) error {
	if c.dynamic == nil {
		return c.localOnlyErr("DeletePeering")
	}
	parts := strings.SplitN(backendUID, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("delete peering: backendUID %q must be \"<vnetA>/<vnetB>\"", backendUID)
	}
	vnetA, vnetB := parts[0], parts[1]

	// Remove peering entries from both VPCs' spec.vpcPeerings.
	if err := c.removeVpcPeering(ctx, vnetA, vnetB); err != nil {
		log.Warn().Err(err).Str("vpc", vnetA).Str("peer", vnetB).
			Msg("kubeovn: failed to remove peering entry; proceeding with route cleanup")
	}
	if err := c.removeVpcPeering(ctx, vnetB, vnetA); err != nil {
		log.Warn().Err(err).Str("vpc", vnetB).Str("peer", vnetA).
			Msg("kubeovn: failed to remove peering entry; proceeding with route cleanup")
	}

	// Remove vnetA's routes to vnetB (peerCIDRs on vnetA).
	if err := c.removePeeringRoutes(ctx, vnetA, peerCIDRs); err != nil {
		log.Warn().Err(err).Str("vpc", vnetA).Str("peering", backendUID).
			Msg("kubeovn: failed to remove peering routes")
	}
	// Remove vnetB's routes to vnetA (localCIDRs on vnetB).
	if err := c.removePeeringRoutes(ctx, vnetB, localCIDRs); err != nil {
		log.Warn().Err(err).Str("vpc", vnetB).Str("peering", backendUID).
			Msg("kubeovn: failed to remove peering routes")
	}
	return nil
}

// appendVpcPeering reads the Vpc, upserts a {remoteVpc, localConnectIP} entry
// in spec.vpcPeerings, and MergePatches the result.
//
// localConnectIP is a /24 from the 100.64.0.0/10 range (RFC 6598 Shared
// Address Space — safe for transit links). Both sides of the peering pick
// the same /24 without coordination, then assign host octets .1 or .2
// based on lexicographic order of the VPC names.
//
// F6: the /24 itself comes from `transitNetwork` (the spec.TransitCIDR
// allocated by the peering handler). When empty, the function falls back
// to the legacy SHA-256(sorted-names) derivation so pre-F6 peerings keep
// working on the localConnectIP they already advertise.
//
// If an entry for peerVpcName already exists it is REPLACED (idempotent).
func (c *Client) appendVpcPeering(ctx context.Context, vpcName, peerVpcName, transitNetwork string) error {
	obj, err := c.dynamic.Resource(vpcGVR).Get(ctx, vpcName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get vpc %s: %w", vpcName, err)
	}
	existing, _, _ := unstructured.NestedSlice(obj.Object, "spec", "vpcPeerings")

	localIP := transitLocalIP(vpcName, peerVpcName, transitNetwork) // e.g. "100.64.42.1/24"
	newEntry := map[string]interface{}{
		"remoteVpc":      peerVpcName,
		"localConnectIP": localIP,
	}

	// Replace existing entry for this peer (if any) rather than duplicating.
	replaced := false
	for i, e := range existing {
		if m, ok := e.(map[string]interface{}); ok {
			if rv, _ := m["remoteVpc"].(string); rv == peerVpcName {
				existing[i] = newEntry
				replaced = true
				break
			}
		}
	}
	if !replaced {
		existing = append(existing, newEntry)
	}

	patch := map[string]interface{}{"spec": map[string]interface{}{"vpcPeerings": existing}}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal vpcPeerings patch: %w", err)
	}
	_, err = c.dynamic.Resource(vpcGVR).Patch(ctx, vpcName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	return err
}

// transitLocalIP returns the localConnectIP CIDR vpcName advertises in its
// peering with peerVpcName.
//
// If transitNetwork is non-empty (F6 allocator path) it MUST be a /24
// inside 100.64.0.0/10 — the function just substitutes the host octet
// (.1 or .2) based on lexicographic order of the two VPC names.
//
// If transitNetwork is empty (legacy / pre-F6) the /24 is derived from
// SHA-256(sorted-names) instead. The hash space is ~16,384 buckets so
// the birthday paradox would produce a collision around ~128 peerings;
// new peerings always hit the allocator path so this branch only runs
// for peerings created before F6 shipped.
//
// In both modes the lexicographically-lesser VPC name receives .1 and
// the other receives .2, so the two sides agree without any coordination.
func transitLocalIP(vpcName, peerVpcName, transitNetwork string) string {
	side := transitSide(vpcName, peerVpcName)

	if transitNetwork != "" {
		// Split "100.64.X.0/24" → "100.64.X" and re-glue with the host octet.
		netPart, _, ok := strings.Cut(transitNetwork, "/")
		if ok {
			lastDot := strings.LastIndex(netPart, ".")
			if lastDot != -1 {
				return fmt.Sprintf("%s.%d/24", netPart[:lastDot], side)
			}
		}
		// Fall through to the hash path on a malformed network; the
		// allocator never produces one of these, but be defensive.
	}

	names := []string{vpcName, peerVpcName}
	sort.Strings(names)
	h := sha256.Sum256([]byte(names[0] + "|" + names[1]))
	o3 := int(h[0])
	o4 := int(h[1] & 0x3F)
	return fmt.Sprintf("100.64.%d.%d/24", o3, o4+side)
}

// transitSide returns 1 if vpcName sorts before peerVpcName, 2 otherwise.
// Both sides of a peering call this with their own pair and arrive at
// opposite host octets without exchanging any state.
func transitSide(vpcName, peerVpcName string) int {
	if vpcName <= peerVpcName {
		return 1
	}
	return 2
}

// transitLocalIPAddr is the same as transitLocalIP but returns the bare IP
// (no /24). Used as nextHopIP on staticRoutes that route into the peering link.
func transitLocalIPAddr(vpcName, peerVpcName, transitNetwork string) string {
	cidr := transitLocalIP(vpcName, peerVpcName, transitNetwork)
	if i := strings.Index(cidr, "/"); i != -1 {
		return cidr[:i]
	}
	return cidr
}

// removeVpcPeering reads the Vpc and MergePatches spec.vpcPeerings with the
// matching entry filtered out. Idempotent.
func (c *Client) removeVpcPeering(ctx context.Context, vpcName, peerVpcName string) error {
	obj, err := c.dynamic.Resource(vpcGVR).Get(ctx, vpcName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get vpc %s: %w", vpcName, err)
	}
	existing, _, _ := unstructured.NestedSlice(obj.Object, "spec", "vpcPeerings")
	filtered := make([]interface{}, 0, len(existing))
	for _, e := range existing {
		if m, ok := e.(map[string]interface{}); ok {
			if rv, _ := m["remoteVpc"].(string); rv == peerVpcName {
				continue
			}
		}
		filtered = append(filtered, e)
	}
	patch := map[string]interface{}{"spec": map[string]interface{}{"vpcPeerings": filtered}}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal vpcPeerings patch: %w", err)
	}
	_, err = c.dynamic.Resource(vpcGVR).Patch(ctx, vpcName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	return err
}

// ── Private DNS ───────────────────────────────────────────────────────────────

// CreatePrivateDnsZone creates a VpcDns CRD or a ConfigMap-based DNS zone.
//
// Preference: VpcDns CRD (detected at Client construction).  Fallback:
// ConfigMap named "dc-dns-<zoneID>" in the tenant namespace, structured for
// CoreDNS file-plugin consumption.
//
// vnetUID is the KubeOVN Vpc CRD name.  The zone record is scoped to the VPC.
//
// NOTE: The zone's DC-API UUID is embedded in the returned BackendUID so the
// handler can later address the ConfigMap / CRD for record operations.  Format:
//   - VpcDns mode:    "vpcdns-<zoneUID>"
//   - ConfigMap mode: "configmap-<ns>/<zoneUID>"
func (c *Client) CreatePrivateDnsZone(ctx context.Context, vnetUID string, spec models.DnsZoneSpec) (*models.DnsZoneResource, error) {
	if c.dynamic == nil {
		return nil, c.localOnlyErr("CreatePrivateDnsZone")
	}
	// Fetch the VPC to find the tenant.
	vpcObj, err := c.dynamic.Resource(vpcGVR).Get(ctx, vnetUID, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("create dns zone: fetch vpc %q: %w", vnetUID, err)
	}
	tenantID, _, _ := unstructured.NestedString(vpcObj.Object, "metadata", "labels", "dc-api/tenant")
	projectID, _, _ := unstructured.NestedString(vpcObj.Object, "metadata", "labels", "dc-api/project")
	var ns string
	if projectID != "" {
		ns = common.NamespaceForProject(tenantID, projectID)
	} else {
		// Fallback for VPCs created pre-M2.5 (no dc-api/project label).
		ns = "dc-" + strings.ToLower(tenantID)
	}

	// BUG B FIX: always use the ConfigMap path. The VpcDns CRD on KubeOVN v1.15
	// has no per-record API and our `createVpcDnsZone` had two issues
	// (double-prefixed BackendUID, missing required `subnet` field) that left
	// rows stuck in PENDING. The ConfigMap path is synchronous, returns
	// ACTIVE immediately, and supports record CRUD via key-by-UUID. When
	// KubeOVN ships a per-record DNS API in a future release (#153), revisit.
	_ = vnetUID
	return c.createConfigMapDnsZone(ctx, ns, spec)
}

// DeletePrivateDnsZone removes the VpcDns CRD or ConfigMap for the zone.
func (c *Client) DeletePrivateDnsZone(ctx context.Context, backendUID string) error {
	if c.dynamic == nil {
		return c.localOnlyErr("DeletePrivateDnsZone")
	}
	if strings.HasPrefix(backendUID, "vpcdns-") {
		crdName := strings.TrimPrefix(backendUID, "vpcdns-")
		err := c.dynamic.Resource(vpcDnsGVR).Delete(ctx, crdName, metav1.DeleteOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete vpcdns %q: %w", crdName, err)
		}
		return nil
	}

	if strings.HasPrefix(backendUID, "configmap-") {
		rest := strings.TrimPrefix(backendUID, "configmap-")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 {
			return fmt.Errorf("delete dns zone: invalid configmap backendUID %q", backendUID)
		}
		ns, cmName := parts[0], "dc-dns-"+parts[1]
		err := c.dynamic.Resource(configMapGVR()).Namespace(ns).Delete(ctx, cmName, metav1.DeleteOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete dns configmap %s/%s: %w", ns, cmName, err)
		}
		return nil
	}

	return fmt.Errorf("delete dns zone: unrecognised backendUID format %q", backendUID)
}

// UpsertDnsRecord creates or replaces a DNS record in the zone's ConfigMap
// (or VpcDns CRD record list).
//
// zoneUID is the zone's BackendUID ("vpcdns-..." or "configmap-.../<zoneUUID>").
// Record entries are keyed by the DC-API record UUID so they are individually
// addressable without scanning the full record list.
func (c *Client) UpsertDnsRecord(ctx context.Context, zoneUID string, record models.DnsRecord) error {
	if c.dynamic == nil {
		return c.localOnlyErr("UpsertDnsRecord")
	}
	if strings.HasPrefix(zoneUID, "vpcdns-") {
		return c.upsertVpcDnsRecord(ctx, zoneUID, record)
	}
	if strings.HasPrefix(zoneUID, "configmap-") {
		return c.upsertConfigMapDnsRecord(ctx, zoneUID, record)
	}
	return fmt.Errorf("upsert dns record: unrecognised zone backendUID %q", zoneUID)
}

// DeleteDnsRecord removes a specific DNS record from the zone.
func (c *Client) DeleteDnsRecord(ctx context.Context, zoneUID, recordID string) error {
	if c.dynamic == nil {
		return c.localOnlyErr("DeleteDnsRecord")
	}
	if strings.HasPrefix(zoneUID, "vpcdns-") {
		return c.deleteVpcDnsRecord(ctx, zoneUID, recordID)
	}
	if strings.HasPrefix(zoneUID, "configmap-") {
		return c.deleteConfigMapDnsRecord(ctx, zoneUID, recordID)
	}
	return fmt.Errorf("delete dns record: unrecognised zone backendUID %q", zoneUID)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// waitForNADReady polls the NAD until Harvester's network-manager controller
// sets `network.harvesterhci.io/ready=true`. Without this, the kubeovn Subnet
// admission webhook rejects creation because the NAD's harvester-side
// type label hasn't been stamped yet.
func (c *Client) waitForNADReady(ctx context.Context, ns, nadName string) error {
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		obj, err := c.dynamic.Resource(nadGVR).Namespace(ns).Get(ctx, nadName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get NAD %s/%s: %w", ns, nadName, err)
		}
		if ready, _, _ := unstructured.NestedString(obj.Object, "metadata", "labels", "network.harvesterhci.io/ready"); ready == "true" {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("NAD %s/%s not labelled ready by Harvester within timeout", ns, nadName)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// patchVPCStaticRoutes replaces Vpc.spec.staticRoutes via JSON MergePatch.
// This is the core of the patch-not-delete pattern (gotcha 4).
func (c *Client) patchVPCStaticRoutes(ctx context.Context, vpcName string, routes []interface{}) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"staticRoutes": routes,
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal staticRoutes patch: %w", err)
	}
	_, err = c.dynamic.Resource(vpcGVR).Patch(ctx, vpcName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch vpc %q staticRoutes: %w", vpcName, err)
	}
	return nil
}

// patchSubnetACLs replaces Subnet.spec.acls via JSON MergePatch.
func (c *Client) patchSubnetACLs(ctx context.Context, subnetName string, acls []interface{}) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"acls": acls,
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal acls patch: %w", err)
	}
	_, err = c.dynamic.Resource(subnetGVR).Patch(ctx, subnetName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch subnet %q acls: %w", subnetName, err)
	}
	return nil
}

// replaceNSGACLsOnSubnet reads the current ACL list, removes entries owned
// by nsgUID, appends the new aclEntries, then patches the Subnet.
func (c *Client) replaceNSGACLsOnSubnet(ctx context.Context, subnetUID, nsgUID string, newEntries []interface{}) error {
	subnet, err := c.dynamic.Resource(subnetGVR).Get(ctx, subnetUID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetch subnet %q: %w", subnetUID, err)
	}
	existing, _, _ := unstructured.NestedSlice(subnet.Object, "spec", "acls")
	tag := nsgACLTag(nsgUID)
	filtered := filterACLsByTag(existing, tag)
	merged := append(filtered, newEntries...)
	return c.patchSubnetACLs(ctx, subnetUID, merged)
}

// appendPeeringRoutes adds peering staticRoutes to a VPC's default route table.
// cidrs is the list of CIDRs belonging to the *remote* VPC.
// nextHopIP is the peer's transit IP (its localConnectIP without the prefix).
//
// We must write nextHopIP explicitly: KubeOVN v1.15 does NOT resolve a
// 0.0.0.0 sentinel to the peer's localConnectIP. If you write 0.0.0.0,
// the route lands in OVN with that literal value and packets have no
// real next hop. Confirmed live (2026-05-09): writing the peer's actual
// transit IP makes ovn-nbctl lr-route-list show "<cidr> → 100.64.x.y
// dst-ip" and ping flows.
//
// routeTable is intentionally set to "" (the empty string, meaning the VPC's
// default routing table). Subnets have spec.routeTable="" by default, so
// routes written here apply to all subnet traffic. Prior code set routeTable
// to a per-peering tag string, which silently placed routes in a named VRF
// that no subnet referenced.
//
// Cleanup on delete: entries are identified by (cidr ∈ peerCIDRs &&
// policy=="policyDst" && nextHopIP starts with "100.64.") — see
// removePeeringRoutes.
func (c *Client) appendPeeringRoutes(ctx context.Context, vpcName string, cidrs []string, peerNextHopIP string) error {
	vpc, err := c.dynamic.Resource(vpcGVR).Get(ctx, vpcName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetch vpc %q: %w", vpcName, err)
	}
	existing, _, _ := unstructured.NestedSlice(vpc.Object, "spec", "staticRoutes")
	// Build a set of CIDRs we are about to add so we can skip duplicates.
	toAdd := make(map[string]struct{}, len(cidrs))
	for _, c := range cidrs {
		toAdd[c] = struct{}{}
	}
	// Remove any existing peering route for these CIDRs (idempotent replace
	// rather than append — prevents duplicates on retry). We match on
	// (cidr, policyDst, nextHopIP starts with 100.64.) to avoid stomping
	// on user-managed RouteTable entries.
	filtered := make([]interface{}, 0, len(existing))
	for _, e := range existing {
		m, ok := e.(map[string]interface{})
		if !ok {
			filtered = append(filtered, e)
			continue
		}
		cidr, _ := m["cidr"].(string)
		policy, _ := m["policy"].(string)
		nextHop, _ := m["nextHopIP"].(string)
		isPeeringRoute := policy == "policyDst" &&
			(strings.HasPrefix(nextHop, "100.64.") || nextHop == "0.0.0.0")
		if _, isOurs := toAdd[cidr]; isOurs && isPeeringRoute {
			continue // will be replaced below
		}
		filtered = append(filtered, e)
	}
	for _, cidr := range cidrs {
		filtered = append(filtered, map[string]interface{}{
			"cidr":       cidr,
			"nextHopIP":  peerNextHopIP, // peer's localConnectIP (e.g. "100.64.176.20")
			"policy":     "policyDst",
			"routeTable": "",            // default routing table — subnets use "" by default
		})
	}
	return c.patchVPCStaticRoutes(ctx, vpcName, filtered)
}

// removePeeringRoutes removes staticRoutes that were written for a specific
// peering, identified by the signature:
//
//	policy == "policyDst" && nextHopIP == "0.0.0.0" && cidr ∈ peerCIDRs
//
// This signature is unambiguous because:
//   - Peering routes use nextHopIP="0.0.0.0" (the KubeOVN sentinel).
//   - RouteTable-driven routes always have a real next-hop IP.
//   - Each peering targets the other VNet's exclusive address-space CIDRs;
//     no two active peerings for the same VPC can share a peer CIDR
//     (overlap is rejected at peering-create time).
//
// peerCIDRs must be the address-space of the VNet whose routes we are
// removing from vpcName (i.e. the remote VNet's CIDRs).
func (c *Client) removePeeringRoutes(ctx context.Context, vpcName string, peerCIDRs []string) error {
	vpc, err := c.dynamic.Resource(vpcGVR).Get(ctx, vpcName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("fetch vpc %q: %w", vpcName, err)
	}

	// Build a set of CIDRs to remove.
	remove := make(map[string]struct{}, len(peerCIDRs))
	for _, c := range peerCIDRs {
		remove[c] = struct{}{}
	}

	existing, _, _ := unstructured.NestedSlice(vpc.Object, "spec", "staticRoutes")
	filtered := make([]interface{}, 0, len(existing))
	for _, e := range existing {
		m, ok := e.(map[string]interface{})
		if !ok {
			filtered = append(filtered, e)
			continue
		}
		cidr, _ := m["cidr"].(string)
		policy, _ := m["policy"].(string)
		nextHop, _ := m["nextHopIP"].(string)
		// Peering routes are identified by (cidr ∈ peer's address-space) AND
		// (policyDst) AND (nextHopIP is a transit-pool address). The 0.0.0.0
		// match is kept for backward compatibility with routes written by the
		// pre-fix driver — they can still be cleaned up after upgrade.
		isPeeringRoute := policy == "policyDst" &&
			(strings.HasPrefix(nextHop, "100.64.") || nextHop == "0.0.0.0")
		if _, isRemote := remove[cidr]; isRemote && isPeeringRoute {
			continue // this route belongs to the peering being deleted
		}
		filtered = append(filtered, e)
	}
	return c.patchVPCStaticRoutes(ctx, vpcName, filtered)
}

// subnetCIDRsForVPC returns the cidrBlock for every Subnet whose spec.vpc
// matches vpcName.
func (c *Client) subnetCIDRsForVPC(ctx context.Context, vpcName string) ([]string, error) {
	list, err := c.dynamic.Resource(subnetGVR).List(ctx, metav1.ListOptions{
		LabelSelector: "dc-api/parent-vnet=" + vpcName,
	})
	if err != nil {
		return nil, fmt.Errorf("list subnets for vpc %q: %w", vpcName, err)
	}
	var cidrs []string
	for _, item := range list.Items {
		cidr, _, _ := unstructured.NestedString(item.Object, "spec", "cidrBlock")
		if cidr != "" {
			cidrs = append(cidrs, cidr)
		}
	}
	return cidrs, nil
}

// ── DNS helpers ───────────────────────────────────────────────────────────────

func (c *Client) createVpcDnsZone(ctx context.Context, vnetUID, ns string, spec models.DnsZoneSpec) (*models.DnsZoneResource, error) {
	crdName := "vpcdns-" + sanitizeDNSName(spec.ZoneName)
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1",
			"kind":       "VpcDns",
			"metadata": map[string]interface{}{
				"name": crdName,
				"labels": map[string]interface{}{
					"dc-api/managed":   "true",
					"dc-api/parent-vpc": vnetUID,
					"dc-api/zone-name":  spec.ZoneName,
				},
			},
			"spec": map[string]interface{}{
				"vpc":     vnetUID,
				"service": "coredns/coredns", // default CoreDNS service in kube-ovn namespace
			},
		},
	}
	if _, err := c.dynamic.Resource(vpcDnsGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create vpcdns %q: %w", crdName, err)
		}
	}
	return &models.DnsZoneResource{
		BackendUID: "vpcdns-" + crdName,
		Status:     models.StatusPending,
		Message:    "vpcdns zone created",
	}, nil
}

func (c *Client) createConfigMapDnsZone(ctx context.Context, ns string, spec models.DnsZoneSpec) (*models.DnsZoneResource, error) {
	// ConfigMap-based DNS zone for KubeOVN installations without VpcDns CRD.
	// Named "dc-dns-<zoneName>" in the project namespace.
	// Content is a CoreDNS file-plugin Corefile that will be hot-reloaded.
	//
	// The namespace is expected to already exist — it is created by the project
	// handler via EnsureProjectNamespace on project creation.
	cmName := "dc-dns-" + sanitizeDNSName(spec.ZoneName)
	cm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name":      cmName,
				"namespace": ns,
				"labels": map[string]interface{}{
					"dc-api/managed":   "true",
					"dc-api/zone-name": spec.ZoneName,
					"dc-api/dns-zone":  "true",
				},
				"annotations": map[string]interface{}{
					"dc-api/description": spec.Description,
					"dc-api/zone-name":   spec.ZoneName,
				},
			},
			"data": map[string]interface{}{
				// CoreDNS file plugin zone content — starts empty.
				// Records are added by UpsertDnsRecord.
				spec.ZoneName: fmt.Sprintf("$ORIGIN %s.\n$TTL 300\n@ IN SOA ns1 admin 1 3600 900 86400 300\n", spec.ZoneName),
			},
		},
	}
	if _, err := c.dynamic.Resource(configMapGVR()).Namespace(ns).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create dns configmap %s/%s: %w", ns, cmName, err)
		}
	}
	return &models.DnsZoneResource{
		BackendUID: "configmap-" + ns + "/" + sanitizeDNSName(spec.ZoneName),
		Status:     models.StatusActive,
		Message:    "dns zone created via configmap (vpcdns crd not available)",
	}, nil
}

func (c *Client) upsertVpcDnsRecord(_ context.Context, _ string, _ models.DnsRecord) error {
	// VpcDns record management via CRD is not yet specified in KubeOVN v1.15's
	// VpcDns spec (it uses a referenced ConfigMap internally).  Delegate to
	// ConfigMap path even in VpcDns mode for record-level operations.
	// TODO: revisit when KubeOVN exposes per-record API on VpcDns.
	return fmt.Errorf("vpcdns record upsert: record-level API not available on VpcDns CRD — use configmap mode")
}

func (c *Client) upsertConfigMapDnsRecord(ctx context.Context, zoneUID string, record models.DnsRecord) error {
	ns, cmName, err := parseConfigMapBackendUID(zoneUID)
	if err != nil {
		return err
	}

	// Read current ConfigMap.
	obj, err := c.dynamic.Resource(configMapGVR()).Namespace(ns).Get(ctx, cmName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("upsert dns record: fetch configmap %s/%s: %w", ns, cmName, err)
	}

	data, _, _ := unstructured.NestedStringMap(obj.Object, "data")
	if data == nil {
		data = map[string]string{}
	}

	// The record is stored as "<recordID>: <zone-file-line(s)>".
	key := "dc-" + record.ID.String()
	data[key] = formatDNSRecord(record)

	patch := map[string]interface{}{"data": toInterface(data)}
	patchBytes, _ := json.Marshal(patch)
	_, err = c.dynamic.Resource(configMapGVR()).Namespace(ns).Patch(ctx, cmName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	return err
}

func (c *Client) deleteVpcDnsRecord(_ context.Context, _ string, _ string) error {
	return fmt.Errorf("vpcdns record delete: record-level API not available on VpcDns CRD — use configmap mode")
}

func (c *Client) deleteConfigMapDnsRecord(ctx context.Context, zoneUID, recordID string) error {
	ns, cmName, err := parseConfigMapBackendUID(zoneUID)
	if err != nil {
		return err
	}

	obj, err := c.dynamic.Resource(configMapGVR()).Namespace(ns).Get(ctx, cmName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("delete dns record: fetch configmap %s/%s: %w", ns, cmName, err)
	}

	data, _, _ := unstructured.NestedStringMap(obj.Object, "data")
	if data == nil {
		return nil
	}
	key := "dc-" + recordID
	delete(data, key)

	patch := map[string]interface{}{"data": toInterface(data)}
	patchBytes, _ := json.Marshal(patch)
	_, err = c.dynamic.Resource(configMapGVR()).Namespace(ns).Patch(ctx, cmName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	return err
}

// ── Status helpers ────────────────────────────────────────────────────────────

// vpcStatus maps KubeOVN Vpc status conditions to ResourceStatus.
// A Vpc without a status block (just created) is PENDING.
func vpcStatus(obj *unstructured.Unstructured) models.ResourceStatus {
	// KubeOVN sets status.conditions[?].type=="Ready" once the VPC is initialised.
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "Ready" {
			if s, _ := m["status"].(string); s == "True" {
				return models.StatusActive
			}
		}
	}
	// No Ready=True condition — still initialising.
	return models.StatusPending
}

func vpcMessage(obj *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "Ready" {
			if msg, _ := m["message"].(string); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// subnetStatus maps KubeOVN Subnet status to ResourceStatus.
func subnetStatus(obj *unstructured.Unstructured) models.ResourceStatus {
	// KubeOVN sets status.isDefault or status.ready depending on version.
	// v1.15 uses "ready" boolean in status.
	ready, found, _ := unstructured.NestedBool(obj.Object, "status", "ready")
	if found && ready {
		return models.StatusActive
	}
	return models.StatusPending
}

func subnetMessage(obj *unstructured.Unstructured) string {
	msg, _, _ := unstructured.NestedString(obj.Object, "status", "message")
	return msg
}

// ── Naming helpers ────────────────────────────────────────────────────────────

// vpcName builds a stable KubeOVN Vpc CRD name. M2.5 includes projectID so
// the same VNet slug can repeat across two projects in the same tenant
// (`choreo-sre/dev/dev-vnet` vs `choreo-sre/staging/dev-vnet`) without
// colliding on the cluster-scoped Vpc CRD name.
//
// Capped at 63 chars (kube-ovn-controller uses the VPC name as a label value
// on per-pod CRs during IP allocation, and label values cap at 63). Length
// overrun falls back to a sha1[:10] suffix that preserves uniqueness without
// reading nicely; tenants who pick maximum-length slugs everywhere may end
// up there. Slug caps (tenant ≤32 / project ≤20 / resource ≤32) plus this
// 63-char target shape are intentionally near the wall — see the M2.5
// design in CLAUDE.md / issue #126.
func vpcName(vnetName, tenantID, projectID string) string {
	const maxLen = 63
	n := strings.ToLower(vnetName)
	t := strings.ToLower(tenantID)
	p := strings.ToLower(projectID)
	name := "vnet-" + t + "-" + p + "-" + n
	if len(name) <= maxLen {
		return name
	}
	sum := sha1.Sum([]byte(name))
	prefix := "vnet-" + t + "-" + p + "-"
	if len(prefix)+10 > maxLen {
		prefix = prefix[:maxLen-10]
	}
	return prefix + hex.EncodeToString(sum[:5]) // 10 hex chars
}

// subnetResourceName builds a stable name for both the Subnet CRD and its NAD.
// Includes the parent VNet's backend UID so subnet names can repeat across
// VNets within the same project. vnetUID is expected to follow the
// "vnet-<tenant>-<project>-<name>" shape; the "vnet-<tenant>-<project>-"
// prefix is stripped to keep the resulting name compact. Falls back to the
// raw vnetUID if the prefix isn't present (defensive).
//
// Capped at 63 chars (same kube-ovn label-value reason as vpcName). Overrun
// falls back to a sha1[:10] suffix.
func subnetResourceName(vnetUID, subnetName, tenantID, projectID string) string {
	const maxLen = 63
	n := strings.ToLower(subnetName)
	t := strings.ToLower(tenantID)
	p := strings.ToLower(projectID)
	v := strings.TrimPrefix(strings.ToLower(vnetUID), "vnet-"+t+"-"+p+"-")
	name := "subnet-" + t + "-" + p + "-" + v + "-" + n
	if len(name) <= maxLen {
		return name
	}
	sum := sha1.Sum([]byte(name))
	prefix := "subnet-" + t + "-" + p + "-"
	if len(prefix)+10 > maxLen {
		prefix = prefix[:maxLen-10]
	}
	return prefix + hex.EncodeToString(sum[:5]) // 10 hex chars
}

// peeringCRDName builds a stable VpcPeering CRD name from two VPC names.
// Sorted alphabetically so A↔B and B↔A produce the same name (idempotent).
func peeringCRDName(vnetA, vnetB string) string {
	a, b := vnetA, vnetB
	if a > b {
		a, b = b, a
	}
	name := "peering-" + a + "-" + b
	if len(name) > 253 {
		// Fall back to a hash-like truncation.
		name = "peering-" + a[:40] + "-" + b[:40]
	}
	return name
}

// ── Tag helpers ───────────────────────────────────────────────────────────────

// routeTableTag returns the tag string embedded in staticRoutes entries
// belonging to a given route table UUID.
func routeTableTag(rtUUID string) string { return "routetable-" + rtUUID }

// peeringTag returns the tag string for peering-owned staticRoutes.
func peeringTag(peeringName string) string { return "peering-" + peeringName }

// nsgACLTag returns the tag prefix for ACL entries owned by an NSG.
func nsgACLTag(nsgUID string) string { return "nsg-" + nsgUID }

// parseRouteTableUID splits "<vnetUID>/<routeTableUUID>" into its parts.
func parseRouteTableUID(backendUID string) (vnetUID, rtUUID string, err error) {
	parts := strings.SplitN(backendUID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid route table backendUID %q — expected format \"<vnetUID>/<routeTableUUID>\"", backendUID)
	}
	return parts[0], parts[1], nil
}

// parseNSGBackendUID splits "<nsgUUID>|<subnetUID1>|..." into nsgUUID and subnet list.
func parseNSGBackendUID(backendUID string) (nsgUID string, subnetUIDs []string) {
	parts := strings.Split(backendUID, "|")
	if len(parts) == 0 {
		return backendUID, nil
	}
	return parts[0], parts[1:]
}

// parseConfigMapBackendUID splits "configmap-<ns>/<zoneID>" into ns and cmName.
func parseConfigMapBackendUID(backendUID string) (ns, cmName string, err error) {
	rest := strings.TrimPrefix(backendUID, "configmap-")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid configmap backendUID %q", backendUID)
	}
	return parts[0], "dc-dns-" + parts[1], nil
}

// filterSliceByTag returns entries from slice where entry[field] != tag.
// Used to remove owned entries from staticRoutes lists.
func filterSliceByTag(slice []interface{}, field, tag string) []interface{} {
	out := make([]interface{}, 0, len(slice))
	for _, item := range slice {
		m, ok := item.(map[string]interface{})
		if !ok {
			out = append(out, item)
			continue
		}
		if v, _ := m[field].(string); v == tag {
			continue // owned by the tag — exclude
		}
		out = append(out, item)
	}
	return out
}

// filterACLsByTag returns ACL entries whose match string does NOT contain tag.
// Tag is embedded as a substring inside the OVN match expression (see
// buildACLEntries) because the Subnet CRD strips any unknown top-level field.
func filterACLsByTag(slice []interface{}, tag string) []interface{} {
	out := make([]interface{}, 0, len(slice))
	for _, item := range slice {
		m, ok := item.(map[string]interface{})
		if !ok {
			out = append(out, item)
			continue
		}
		if match, _ := m["match"].(string); strings.Contains(match, tag) {
			continue // owned by this NSG — exclude
		}
		out = append(out, item)
	}
	return out
}

// buildStaticRouteEntries converts RouteRule slice → KubeOVN staticRoutes entries.
// Each entry is tagged with the route-table UUID via the "routeTable" field.
func buildStaticRouteEntries(routes []models.RouteRule, tag string) []interface{} {
	entries := make([]interface{}, 0, len(routes))
	for _, r := range routes {
		entry := map[string]interface{}{
			"cidr":       r.DestinationCIDR,
			"routeTable": tag,
			"policy":     "policyDst",
		}
		if r.NextHopIP != "" {
			entry["nextHopIP"] = r.NextHopIP
		} else {
			// vnet_local / internet / none: KubeOVN uses empty nextHopIP to mean
			// "route to the logical router's own subnet" (local routing).
			entry["nextHopIP"] = ""
		}
		entries = append(entries, entry)
	}
	return entries
}

// buildACLEntries converts NSGRule slice → KubeOVN ACL entries.
//
// The KubeOVN Subnet CRD's ACL schema only accepts {action, direction, match,
// priority} — any "name" field is silently pruned by the api-server. To track
// ownership we therefore embed the NSG UID inside the OVN `match` string itself
// as an always-false clause: `inport == "nsg-<uid>" || (<real_match>)`. OVN
// accepts this as valid syntax; the inport check never matches (no such logical
// port exists), so the ACL behaviour is identical to `(<real_match>)`.
// Both deletion-by-tag and ownership lookup parse the match string for the
// "nsg-<uid>" substring.
//
// Direction mapping:
//   - "inbound"  → KubeOVN "to-lport"   (traffic arriving at the logical port)
//   - "outbound" → KubeOVN "from-lport" (traffic leaving the logical port)
func buildACLEntries(rules []models.NSGRule, nsgUID string) []interface{} {
	tag := nsgACLTag(nsgUID)
	entries := make([]interface{}, 0, len(rules))
	for _, r := range rules {
		dir := "to-lport"
		if r.Direction == "outbound" {
			dir = "from-lport"
		}
		action := "allow-related"
		if r.Action == "deny" {
			action = "drop"
		}
		realMatch := buildACLMatch(r)
		taggedMatch := fmt.Sprintf(`inport == "%s" || (%s)`, tag, realMatch)
		entries = append(entries, map[string]interface{}{
			"direction": dir,
			"priority":  r.Priority,
			"match":     taggedMatch,
			"action":    action,
		})
	}
	return entries
}

// buildACLMatch converts an NSGRule to an OVN ACL match expression.
// This is a simplified translation; a production implementation would handle
// port ranges, ICMP type/code, and VnetLocal address groups.
func buildACLMatch(r models.NSGRule) string {
	var parts []string

	// Protocol
	switch r.Protocol {
	case "tcp":
		parts = append(parts, "tcp")
	case "udp":
		parts = append(parts, "udp")
	case "icmp":
		parts = append(parts, "icmp4")
	default:
		parts = append(parts, "ip4")
	}

	// Source address
	if r.SourceAddressPrefix != "" && r.SourceAddressPrefix != "*" && r.SourceAddressPrefix != "VnetLocal" {
		parts = append(parts, fmt.Sprintf("ip4.src == %s", r.SourceAddressPrefix))
	}

	// Destination address
	if r.DestinationAddressPrefix != "" && r.DestinationAddressPrefix != "*" && r.DestinationAddressPrefix != "VnetLocal" {
		parts = append(parts, fmt.Sprintf("ip4.dst == %s", r.DestinationAddressPrefix))
	}

	// Destination port
	if r.DestinationPortRange != "" && r.DestinationPortRange != "*" && (r.Protocol == "tcp" || r.Protocol == "udp") {
		parts = append(parts, fmt.Sprintf("%s.dst == %s", r.Protocol, r.DestinationPortRange))
	}

	if len(parts) == 0 {
		return "ip4"
	}
	return strings.Join(parts, " && ")
}

// ── Ghost IP detection (gotcha 5) ────────────────────────────────────────────

// isPhantomIP returns true if the IP CRD entry is a phantom allocation.
//
// KubeOVN creates an ips.kubeovn.io object for every pod, including pods that
// use only Canal (not KubeOVN).  Those are "phantom" entries: the CRD exists,
// but the pod's actual IP is different from the KubeOVN-allocated IP.
//
// Detection: compare spec.ipAddress (KubeOVN's allocation) against the pod's
// status.podIP.  If they differ, the KubeOVN entry is a phantom.
//
// ipCRDAddress: the spec.ipAddress value from ips.kubeovn.io
// podPrimaryIP:  the pod's status.podIP
func isPhantomIP(ipCRDAddress, podPrimaryIP string) bool {
	if ipCRDAddress == "" || podPrimaryIP == "" {
		return false
	}
	return ipCRDAddress != podPrimaryIP
}

// ── Miscellaneous helpers ─────────────────────────────────────────────────────

// firstUsableIP returns the first usable IP in a CIDR block (network address + 1).
// E.g., "10.1.1.0/24" → "10.1.1.1".
// This is a simple string manipulation; a full implementation would use net.ParseCIDR.
func firstUsableIP(cidr string) string {
	parts := strings.Split(cidr, "/")
	if len(parts) != 2 {
		return ""
	}
	octets := strings.Split(parts[0], ".")
	if len(octets) != 4 {
		return ""
	}
	// Increment last octet by 1 (works for /24 and smaller).
	last := octets[3]
	switch last {
	case "0":
		octets[3] = "1"
	default:
		// For non-.0 network addresses, just append 1 to the last octet numerically.
		var n int
		fmt.Sscanf(last, "%d", &n)
		octets[3] = fmt.Sprintf("%d", n+1)
	}
	return strings.Join(octets, ".")
}

// sanitizeDNSName converts a DNS zone name to a valid Kubernetes resource name.
// Replaces dots with hyphens and lowercases.  Max 63 chars.
func sanitizeDNSName(zoneName string) string {
	s := strings.ToLower(strings.ReplaceAll(zoneName, ".", "-"))
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

// configMapGVR returns the GVR for core v1 ConfigMaps.
func configMapGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
}

// toInterface converts map[string]string to map[string]interface{} for JSON marshalling.
func toInterface(m map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// formatDNSRecord converts a DnsRecord to a zone-file line for the ConfigMap.
func formatDNSRecord(r models.DnsRecord) string {
	ttl := r.TTL
	if ttl == 0 {
		ttl = 300
	}
	var lines []string
	for _, val := range r.Values {
		lines = append(lines, fmt.Sprintf("%s %d IN %s %s", r.Name, ttl, r.RecordType, val))
	}
	return strings.Join(lines, "\n")
}

// ── ProjectNamespaceProvisioner ───────────────────────────────────────────────

var resourceQuotaGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "resourcequotas"}

// EnsureProjectNamespace satisfies providers.ProjectNamespaceProvisioner.
//
// Idempotently creates the namespace "dc-<tenantID>-<projectID>" with standard
// dc-api.wso2.com/* labels, then applies a ResourceQuota in that namespace
// mirroring the project's capacity limits and volume guardrails.
//
// If the namespace exists in Terminating state, the function waits up to 90s
// for it to fully disappear before recreating (mirrors the pattern used for
// the old per-tenant namespace).
func (c *Client) EnsureProjectNamespace(
	ctx context.Context,
	tenantID, projectID string,
	projectUUID interface{ String() string },
	cpuCores, memoryGB, storageGB, maxVolumes int,
) error {
	ns := common.NamespaceForProject(tenantID, projectID)

	// ── 1. Create (or wait for) the namespace ────────────────────────────────
	nsObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]interface{}{
				"name": ns,
				"labels": map[string]interface{}{
					"dc-api/managed":  "true",
					"dc-api/tenant":   strings.ToLower(tenantID),
					"dc-api/project":  strings.ToLower(projectID),
					// dc-api.wso2.com/* namespace labels for selector queries
					"dc-api.wso2.com/tenant":       strings.ToLower(tenantID),
					"dc-api.wso2.com/project":      strings.ToLower(projectID),
					"dc-api.wso2.com/project-uuid": projectUUID.String(),
					"dc-api.wso2.com/resource-kind": "project",
				},
			},
		},
	}

	// Wait out any Terminating namespace before recreating.
	waitCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		existing, err := c.dynamic.Resource(namespacesGVR).Get(waitCtx, ns, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			break // good — proceed to Create
		}
		if err != nil {
			return fmt.Errorf("ensure project namespace %s: get: %w", ns, err)
		}
		if existing.GetDeletionTimestamp().IsZero() {
			// Namespace is healthy — skip Create but still apply the ResourceQuota below.
			goto applyQuota
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("ensure project namespace %s: stuck in Terminating beyond timeout", ns)
		case <-time.After(2 * time.Second):
		}
	}

	if _, err := c.dynamic.Resource(namespacesGVR).Create(ctx, nsObj, metav1.CreateOptions{}); err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure project namespace %s: create: %w", ns, err)
	}

applyQuota:
	// ── 2. Apply ResourceQuota ────────────────────────────────────────────────
	// ResourceQuota hard limits mirror the project capacity + guardrails.
	// Using MergePatch so this is idempotent — re-applying always wins.
	quota := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ResourceQuota",
			"metadata": map[string]interface{}{
				"name":      "dc-project-quota",
				"namespace": ns,
				"labels": map[string]interface{}{
					"dc-api/managed": "true",
					"dc-api/project": strings.ToLower(projectID),
				},
			},
			"spec": map[string]interface{}{
				"hard": map[string]interface{}{
					"requests.cpu":    fmt.Sprintf("%d", cpuCores),
					"requests.memory": fmt.Sprintf("%dGi", memoryGB),
					"requests.storage": fmt.Sprintf("%dGi", storageGB),
					"persistentvolumeclaims": fmt.Sprintf("%d", maxVolumes),
				},
			},
		},
	}

	existing, getErr := c.dynamic.Resource(resourceQuotaGVR).Namespace(ns).Get(ctx, "dc-project-quota", metav1.GetOptions{})
	if k8serrors.IsNotFound(getErr) {
		if _, err := c.dynamic.Resource(resourceQuotaGVR).Namespace(ns).Create(ctx, quota, metav1.CreateOptions{}); err != nil && !k8serrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensure project namespace %s: create ResourceQuota: %w", ns, err)
		}
	} else if getErr == nil {
		// Quota exists — patch the hard limits to reflect current project config.
		_ = existing // used only to confirm existence; patch is safer than update (no resourceVersion needed)
		patchBytes, _ := json.Marshal(map[string]interface{}{
			"spec": map[string]interface{}{
				"hard": map[string]interface{}{
					"requests.cpu":           fmt.Sprintf("%d", cpuCores),
					"requests.memory":        fmt.Sprintf("%dGi", memoryGB),
					"requests.storage":       fmt.Sprintf("%dGi", storageGB),
					"persistentvolumeclaims": fmt.Sprintf("%d", maxVolumes),
				},
			},
		})
		if _, err := c.dynamic.Resource(resourceQuotaGVR).Namespace(ns).Patch(ctx, "dc-project-quota", types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
			// Non-fatal — the namespace and any existing quota are valid; just log.
			log.Warn().Err(err).Str("namespace", ns).Msg("kubeovn: failed to patch project ResourceQuota (namespace is still ready)")
		}
	}

	return nil
}

// EnsureTenantNamespace idempotently provisions the per-tenant namespace
// "dc-tenant-<tenantID>". Mirrors the EnsureProjectNamespace structure but
// without a ResourceQuota — see TenantNamespaceProvisioner docs.
func (c *Client) EnsureTenantNamespace(
	ctx context.Context,
	tenantID string,
	tenantUUID interface{ String() string },
) error {
	ns := common.NamespaceForTenant(tenantID)

	nsObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]interface{}{
				"name": ns,
				"labels": map[string]interface{}{
					"dc-api/managed":               "true",
					"dc-api/tenant":                strings.ToLower(tenantID),
					"dc-api.wso2.com/tenant":       strings.ToLower(tenantID),
					"dc-api.wso2.com/tenant-uuid":  tenantUUID.String(),
					"dc-api.wso2.com/scope":        "tenant-services",
				},
			},
		},
	}

	// Wait out any Terminating namespace before recreating.
	waitCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		existing, err := c.dynamic.Resource(namespacesGVR).Get(waitCtx, ns, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			break
		}
		if err != nil {
			return fmt.Errorf("ensure tenant namespace %s: get: %w", ns, err)
		}
		if existing.GetDeletionTimestamp().IsZero() {
			// Already exists and healthy — done.
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("ensure tenant namespace %s: stuck in Terminating beyond timeout", ns)
		case <-time.After(2 * time.Second):
		}
	}

	if _, err := c.dynamic.Resource(namespacesGVR).Create(ctx, nsObj, metav1.CreateOptions{}); err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure tenant namespace %s: create: %w", ns, err)
	}
	return nil
}
