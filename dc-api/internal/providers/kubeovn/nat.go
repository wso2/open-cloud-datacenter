// Package kubeovn — nat.go
//
// F15 SNAT implementation: EnsureExternalNetworkBootstrap (one-time cluster
// setup) and per-VPC EnsureVpcNAT / DeleteVpcNAT / IsVpcNATPresent.
//
// IMPORTANT — cluster-CNI compatibility note:
// This SNAT operates strictly at the VPC router boundary. Traffic from RKE2
// cluster pods (Cilium, Calico, or any user-provided CNI) is VXLAN-encapped by
// the cluster CNI before it reaches the VM's KubeOVN NIC. The outer source IP
// that the SNAT rule sees is always the VM's KubeOVN IP, never a pod IP. This
// means whatever CNI a tenant chooses is completely opaque to F15 — swapping
// cluster CNIs does not affect SNAT behaviour and this code must never reference
// or depend on any cluster CNI configuration.
//
// Recipe reference: memory/project_f15_recipe.md (proven 2026-05-11).
// Three hard-coded gotchas from the recipe are called out inline:
//   1. External subnet name is hardcoded "ovn-vpc-external-network".
//   2. Subnet provider must be 3-dot-segment: <name>.<ns>.ovn.
//   3. VPC 0.0.0.0/0 route is YOUR responsibility; merge, don't overwrite.
//
// Every Kubernetes op in this file runs through the cluster-access seam
// (c.access) on onboarded capability families — NAT is agent-routable, so the
// whole lifecycle works in a remote (agent-only) zone. All created objects use
// FIXED, deterministic metadata.name values (never generateName), which is what
// makes the agent path's SSA create idempotent.
package kubeovn

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ── GVRs for F15 NAT resources ────────────────────────────────────────────────
//
// All verified against KubeOVN v1.15 on lk-dev (2026-05-11):
//
//	kubectl get crd | grep -E 'iptables|vpcnatgw|providernetwork|vlan'
var (
	providerNetworkGVR  = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "provider-networks"}
	vlanGVR             = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vlans"}
	vpcNatGatewayGVR    = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpc-nat-gateways"}
	iptablesEIPGVR      = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "iptables-eips"}
	iptablesSnatRuleGVR = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "iptables-snat-rules"}
	podGVR              = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
)

// ExternalNetworkConfig holds the external network parameters passed from
// config.Config into the kubeovn client. Set via WithExternalNetwork().
//
// KubeOVN's IPAM owns IP allocation on the external subnet — we don't manage
// a pool ourselves. ReservedIPs lists IPs already in use by infra (host NICs,
// ingress LB, etc.) that KubeOVN must NOT hand out.
type ExternalNetworkConfig struct {
	Bridge      string   // host bridge name, e.g. "mgmt-br"
	CIDR        string   // e.g. "192.168.10.0/24"
	Gateway     string   // e.g. "192.168.10.254"
	ReservedIPs []string // IPs to exclude from KubeOVN's IPAM (host nodes, LBs, etc.)
	VLANID      int
}

// externalNet is stored on the Client after WithExternalNetwork() is called.
// nil means F15 bootstrap has not been configured (old code path / non-kubeovn
// provider tests).
var _ = (*ExternalNetworkConfig)(nil) // ensure type is exported

// WithExternalNetwork attaches the F15 network config to the client.
// Must be called before EnsureExternalNetworkBootstrap or EnsureVpcNAT.
func (c *Client) WithExternalNetwork(cfg ExternalNetworkConfig) {
	c.extNet = &cfg
}

// EnsureExternalNetworkBootstrap creates the one-time cluster-level resources
// needed for VpcNatGateway to function. Safe to call on every startup —
// AlreadyExists on any resource is treated as success.
//
// Objects created (all in kube-system unless noted):
//  1. ProviderNetwork — binds host bridge to KubeOVN SDN.
//  2. Vlan            — references the ProviderNetwork; id = externalVLANID.
//  3. Subnet "ovn-vpc-external-network" (cluster-scoped, no namespace) —
//     gotcha 1: name is hardcoded in kube-ovn-controller's vpc_nat_gw_eip.go.
//  4. NetworkAttachmentDefinition "ovn-vpc-external-network" in kube-system —
//     used by VpcNatGateway pods to acquire their external NIC.
func (c *Client) EnsureExternalNetworkBootstrap(ctx context.Context) error {
	if c.extNet == nil {
		return fmt.Errorf("EnsureExternalNetworkBootstrap: external network config not set — call WithExternalNetwork first")
	}
	cfg := c.extNet

	providerName := bridgeToProviderName(cfg.Bridge)

	// 1. ProviderNetwork
	if err := c.ensureProviderNetwork(ctx, providerName, cfg.Bridge); err != nil {
		return fmt.Errorf("ensure external network bootstrap: provider network: %w", err)
	}

	// 2. Vlan
	vlanName := fmt.Sprintf("ext-vlan-%d", cfg.VLANID)
	if err := c.ensureVlan(ctx, vlanName, providerName, cfg.VLANID); err != nil {
		return fmt.Errorf("ensure external network bootstrap: vlan: %w", err)
	}

	// 3. NAD in kube-system — MUST exist before the Subnet because Harvester's
	// network webhook validates that the NAD exists when a kube-ovn Subnet with
	// the same name is created. Without this ordering the Subnet create fails
	// with "nad ovn-vpc-external-network not created in namespace kube-system".
	// gotcha 2: provider must be 3-dot-segment "<name>.kube-system.ovn"
	nadProvider := "ovn-vpc-external-network.kube-system.ovn"
	if err := c.ensureExternalNAD(ctx, nadProvider); err != nil {
		return fmt.Errorf("ensure external network bootstrap: NAD: %w", err)
	}

	// 4. Subnet "ovn-vpc-external-network" (HARDCODED — gotcha 1)
	// excludeIps covers the gateway IP plus any operator-reserved IPs (host
	// nodes, ingress LB, etc.) — everything else in the CIDR is free for
	// KubeOVN's IPAM to allocate to gateway pod NICs and IptablesEIPs.
	exclIPs := buildExcludeIPs(cfg.Gateway, cfg.ReservedIPs)
	if err := c.ensureExternalSubnet(ctx, providerName, cfg.CIDR, cfg.Gateway, vlanName, exclIPs); err != nil {
		return fmt.Errorf("ensure external network bootstrap: external subnet: %w", err)
	}

	log.Info().Str("bridge", cfg.Bridge).Str("cidr", cfg.CIDR).Msg("kubeovn: external network bootstrap complete")
	return nil
}

// EnsureVpcNAT idempotently provisions the per-VPC SNAT chain:
// VpcNatGateway → IptablesEIP (auto-allocated by KubeOVN) → IptablesSnatRule
// → VPC default route. Returns the EIP address that KubeOVN assigned, so the
// caller can persist it on the VNet row for display.
//
// Parameters:
//   - vpcName         : KubeOVN Vpc CRD name (the BackendUID from the DB row)
//   - tenantSubnetCIDR: tenant subnet CIDR, e.g. "192.168.1.0/24"
//   - tenantSubnetName: KubeOVN Subnet CRD name (the subnet BackendUID)
//   - lanIP           : unused IP inside the tenant subnet for the gateway's LAN NIC
//
// We deliberately do NOT pass an EIP IP — IptablesEIP.spec.v4ip is omitted so
// KubeOVN's IPAM picks one from the external subnet (avoiding the collision
// we'd have if we picked one in parallel with the gateway pod NIC allocation).
func (c *Client) EnsureVpcNAT(
	ctx context.Context,
	vpcName string,
	tenantSubnetCIDR string,
	tenantSubnetName string,
	lanIP net.IP,
) (net.IP, error) {
	gwName := natGWName(vpcName)
	eipName := natEIPName(vpcName)
	snatName := natSnatName(vpcName)

	// ── 1. VpcNatGateway ─────────────────────────────────────────────────────
	gw := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1",
			"kind":       "VpcNatGateway",
			"metadata": map[string]interface{}{
				"name": gwName,
				"labels": map[string]interface{}{
					"dc-api/managed":  "true",
					"dc-api/vpc-name": vpcName,
				},
			},
			"spec": map[string]interface{}{
				"vpc":    vpcName,
				"subnet": tenantSubnetName,
				"lanIp":  lanIP.String(),
			},
		},
	}
	// Cluster-scoped → ns="". Seam Create: local Direct keeps the plain POST with
	// its IsAlreadyExists tolerance; the agent path is an SSA, which is idempotent
	// on the fixed name so a retry converges instead of erroring.
	if _, err := c.access.Create(ctx, vpcNatGatewayGVR, "", gw, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("ensure vpc nat: create VpcNatGateway %q: %w", gwName, err)
		}
		log.Debug().Str("gw", gwName).Msg("kubeovn: VpcNatGateway already exists — skipping create")
	}

	// ── 2. Wait for NatGateway pod to be Running ──────────────────────────────
	podName := "vpc-nat-gw-" + gwName + "-0"
	if err := c.waitForPodRunning(ctx, "kube-system", podName, 90*time.Second); err != nil {
		return nil, fmt.Errorf("ensure vpc nat: wait for gateway pod %q: %w", podName, err)
	}

	// ── 3. IptablesEIP (spec.v4ip omitted — KubeOVN auto-allocates) ──────────
	eipObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1",
			"kind":       "IptablesEIP",
			"metadata": map[string]interface{}{
				"name": eipName,
				"labels": map[string]interface{}{
					"dc-api/managed":  "true",
					"dc-api/vpc-name": vpcName,
				},
			},
			"spec": map[string]interface{}{
				"natGwDp": gwName,
				// v4ip intentionally omitted — KubeOVN picks an unused IP from the
				// external subnet (status.ip after ready). Removes the parallel-
				// allocator collision we'd hit if dc-api picked too.
			},
		},
	}
	if _, err := c.access.Create(ctx, iptablesEIPGVR, "", eipObj, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("ensure vpc nat: create IptablesEIP %q: %w", eipName, err)
		}
		log.Debug().Str("eip", eipName).Msg("kubeovn: IptablesEIP already exists — skipping create")
	}

	// ── 4. Wait for EIP ready and read the assigned IP ───────────────────────
	if err := c.waitForEIPReady(ctx, eipName, 2*time.Minute); err != nil {
		return nil, fmt.Errorf("ensure vpc nat: wait for IptablesEIP %q ready: %w", eipName, err)
	}
	assignedIP, err := c.readEIPAssignedIP(ctx, eipName)
	if err != nil {
		return nil, fmt.Errorf("ensure vpc nat: read assigned IP for %q: %w", eipName, err)
	}

	// ── 5. IptablesSnatRule ───────────────────────────────────────────────────
	snat := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1",
			"kind":       "IptablesSnatRule",
			"metadata": map[string]interface{}{
				"name": snatName,
				"labels": map[string]interface{}{
					"dc-api/managed":  "true",
					"dc-api/vpc-name": vpcName,
				},
			},
			"spec": map[string]interface{}{
				"eip":          eipName,
				"internalCIDR": tenantSubnetCIDR,
			},
		},
	}
	if _, err := c.access.Create(ctx, iptablesSnatRuleGVR, "", snat, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("ensure vpc nat: create IptablesSnatRule %q: %w", snatName, err)
		}
		log.Debug().Str("snat", snatName).Msg("kubeovn: IptablesSnatRule already exists — skipping create")
	}

	// ── 6. Wait for SNAT rule ready ───────────────────────────────────────────
	if err := c.waitForSnatReady(ctx, snatName, 2*time.Minute); err != nil {
		return nil, fmt.Errorf("ensure vpc nat: wait for IptablesSnatRule %q ready: %w", snatName, err)
	}

	// ── 7. Patch VPC default route (gotcha 3: merge, don't overwrite) ─────────
	// KubeOVN does NOT auto-add 0.0.0.0/0 when a VpcNatGateway is created.
	// Without this route, packets from the tenant subnet cannot reach the gateway.
	if err := c.ensureVPCDefaultRoute(ctx, vpcName, lanIP.String()); err != nil {
		return nil, fmt.Errorf("ensure vpc nat: patch VPC default route for %q: %w", vpcName, err)
	}

	log.Info().
		Str("vpc", vpcName).
		Str("eip", assignedIP.String()).
		Str("lan_ip", lanIP.String()).
		Msg("kubeovn: VPC NAT provisioned")
	return assignedIP, nil
}

// DeleteVpcNAT removes all per-VPC NAT resources in reverse order and strips
// the 0.0.0.0/0 default route from the VPC's staticRoutes.
//
// Idempotent — NotFound errors on any resource are silently ignored.
func (c *Client) DeleteVpcNAT(ctx context.Context, vpcName string) error {
	gwName := natGWName(vpcName)
	eipName := natEIPName(vpcName)
	snatName := natSnatName(vpcName)

	// ── 1. Remove VPC default route FIRST (gotcha 3) ─────────────────────────
	// Remove before deleting the gateway so the controller doesn't enter a
	// confused state where the route target disappears before the route does.
	if err := c.removeVPCDefaultRoute(ctx, vpcName); err != nil {
		log.Warn().Err(err).Str("vpc", vpcName).Msg("kubeovn: failed to remove VPC default route during NAT delete; continuing")
	}

	// ── 2. Delete IptablesSnatRule ────────────────────────────────────────────
	// Cluster-scoped → ns="". Seam Deletes: the agent path already treats a
	// missing object as a successful idempotent delete; the IsNotFound tolerance
	// below covers the local Direct path.
	if err := c.access.Delete(ctx, iptablesSnatRuleGVR, "", snatName, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete vpc nat: delete IptablesSnatRule %q: %w", snatName, err)
	}

	// ── 3. Delete IptablesEIP ─────────────────────────────────────────────────
	if err := c.access.Delete(ctx, iptablesEIPGVR, "", eipName, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete vpc nat: delete IptablesEIP %q: %w", eipName, err)
	}

	// ── 4. Delete VpcNatGateway ───────────────────────────────────────────────
	if err := c.access.Delete(ctx, vpcNatGatewayGVR, "", gwName, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete vpc nat: delete VpcNatGateway %q: %w", gwName, err)
	}

	log.Info().Str("vpc", vpcName).Msg("kubeovn: VPC NAT resources deleted")
	return nil
}

// WaitVpcNATPodsGone polls kube-system until all StatefulSet pods for the VPC's
// NAT gateway are gone, or until timeout expires.
//
// KubeOVN's VpcNatGateway controller creates a StatefulSet named
// "vpc-nat-gw-<gwName>". The pods carry the label "app=vpc-nat-gw-<gwName>".
// We use this label selector rather than a pod-name prefix match so that the
// function is not confused by pod restarts (a new pod after a crash would still
// match the selector, correctly blocking the subnet delete until it too is gone).
//
// Called by the subnet-delete goroutine after DeleteVpcNAT to ensure the
// gateway pod's Multus secondary NIC (which holds a KubeOVN LSP on the tenant
// subnet) is released before the subnet CRD delete. Idempotent: returns nil
// immediately if no matching pods exist.
func (c *Client) WaitVpcNATPodsGone(ctx context.Context, vpcUID string, timeout time.Duration) error {
	gwName := natGWName(vpcUID)
	selector := fmt.Sprintf("app=vpc-nat-gw-%s", gwName)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Pods are namespaced → pass the namespace. Seam List so the poll works in
		// a remote (agent-only) zone; the pod family routes reads only.
		list, err := c.access.List(ctx, podGVR, "kube-system", metav1.ListOptions{
			LabelSelector: selector,
		})
		if err == nil && len(list.Items) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("wait vpc nat pods gone: pods matching %q in kube-system still present after %s", selector, timeout)
}

// IsVpcNATPresent returns true if the VpcNatGateway and IptablesEIP both exist
// (SnatRule is inferred present if EIP exists). Used by the startup backfill
// loop to skip VPCs that are already fully provisioned.
func (c *Client) IsVpcNATPresent(ctx context.Context, vpcName string) (bool, error) {
	gwName := natGWName(vpcName)
	// Cluster-scoped → ns="". Seam Gets: the agent path shapes a missing object
	// as a k8s NotFound, so the IsNotFound checks fire identically on both seams.
	_, err := c.access.Get(ctx, vpcNatGatewayGVR, "", gwName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check vpc nat present (%s): get VpcNatGateway: %w", gwName, err)
	}

	eipName := natEIPName(vpcName)
	_, err = c.access.Get(ctx, iptablesEIPGVR, "", eipName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check vpc nat present (%s): get IptablesEIP: %w", eipName, err)
	}

	return true, nil
}

// ── Bootstrap helpers ─────────────────────────────────────────────────────────

func (c *Client) ensureProviderNetwork(ctx context.Context, name, bridge string) error {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1",
			"kind":       "ProviderNetwork",
			"metadata": map[string]interface{}{
				"name": name,
				"labels": map[string]interface{}{
					"dc-api/managed": "true",
				},
			},
			"spec": map[string]interface{}{
				"defaultInterface": bridge,
			},
		},
	}
	// Cluster-scoped → ns="". Seam Create: local Direct keeps the plain POST with
	// its IsAlreadyExists tolerance; the agent path is an idempotent SSA on the
	// fixed (bridge-derived) name.
	_, err := c.access.Create(ctx, providerNetworkGVR, "", obj, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("create ProviderNetwork %q: %w", name, err)
	}
	return nil
}

func (c *Client) ensureVlan(ctx context.Context, name, providerName string, vlanID int) error {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1",
			"kind":       "Vlan",
			"metadata": map[string]interface{}{
				"name": name,
				"labels": map[string]interface{}{
					"dc-api/managed": "true",
				},
			},
			"spec": map[string]interface{}{
				// int64, not int — unstructured content must be JSON-typed
				// (runtime.DeepCopyJSON panics on plain int; the wire bytes are
				// identical either way).
				"id":       int64(vlanID),
				"provider": providerName,
			},
		},
	}
	// Cluster-scoped → ns="". Seam Create (same tolerance rationale as
	// ensureProviderNetwork); the name is fixed ("ext-vlan-<id>").
	_, err := c.access.Create(ctx, vlanGVR, "", obj, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("create Vlan %q: %w", name, err)
	}
	return nil
}

// ensureExternalSubnet creates (or verifies existence of) the cluster-scoped
// Subnet named "ovn-vpc-external-network".
//
// Gotcha 1: name is HARDCODED — kube-ovn-controller's vpc_nat_gw_eip.go reads
// this exact name to attach NatGateway pods to the external network.
// Gotcha 2: provider must be "ovn-vpc-external-network.kube-system.ovn"
// (3 dot-segments). Harvester's webhook rejects 2-segment providers.
func (c *Client) ensureExternalSubnet(ctx context.Context, providerName, cidr, gateway, vlanName string, excludeIPs []string) error {
	const subnetName = "ovn-vpc-external-network" // HARDCODED — gotcha 1
	const nadProvider = "ovn-vpc-external-network.kube-system.ovn" // HARDCODED — gotcha 2

	excl := make([]interface{}, len(excludeIPs))
	for i, ip := range excludeIPs {
		excl[i] = ip
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1",
			"kind":       "Subnet",
			"metadata": map[string]interface{}{
				"name": subnetName,
				"labels": map[string]interface{}{
					"dc-api/managed": "true",
				},
			},
			"spec": map[string]interface{}{
				"protocol":   "IPv4",
				"provider":   nadProvider,
				"cidrBlock":  cidr,
				"gateway":    gateway,
				"vlan":       vlanName,
				"excludeIps": excl,
				// This subnet is used ONLY by VpcNatGateway pods in kube-system.
				// No tenant namespaces are listed here.
				"namespaces": []interface{}{"kube-system"},
			},
		},
	}
	// Check existence by Get first — Harvester's network webhook returns a
	// non-AlreadyExists error ("subnet is using the provider already") when the
	// Subnet exists, which our IsAlreadyExists check below cannot catch.
	// Cluster-scoped → ns="". subnetGVR is an onboarded family, so both the Get
	// and the Create route through the seam for a remote zone.
	if _, getErr := c.access.Get(ctx, subnetGVR, "", subnetName, metav1.GetOptions{}); getErr == nil {
		return nil // already exists, idempotent
	}
	_, err := c.access.Create(ctx, subnetGVR, "", obj, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("create external Subnet %q: %w", subnetName, err)
	}
	return nil
}

// ensureExternalNAD creates the NetworkAttachmentDefinition for the external
// network in kube-system. VpcNatGateway pods reference this NAD to acquire
// their external-facing NIC.
func (c *Client) ensureExternalNAD(ctx context.Context, provider string) error {
	const nadName = "ovn-vpc-external-network"
	const ns = "kube-system"

	// Derive the Harvester clusternetwork from the bridge name. Harvester's
	// convention is "<clusternetwork>-br" (e.g. mgmt-br → mgmt). If the bridge
	// doesn't follow the convention we fall back to "mgmt" — operators can
	// override by editing the label post-create if needed.
	clusterNetwork := "mgmt"
	if c.extNet != nil && strings.HasSuffix(c.extNet.Bridge, "-br") {
		clusterNetwork = strings.TrimSuffix(c.extNet.Bridge, "-br")
	}

	nadConfig := fmt.Sprintf(
		`{"cniVersion":"0.3.1","type":"kube-ovn","server_socket":"/run/openvswitch/kube-ovn-daemon.sock","provider":%q}`,
		provider,
	)
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k8s.cni.cncf.io/v1",
			"kind":       "NetworkAttachmentDefinition",
			"metadata": map[string]interface{}{
				"name":      nadName,
				"namespace": ns,
				"labels": map[string]interface{}{
					// Harvester's network webhook reads these BEFORE its own controller
					// labels the NAD, so they must be set at create-time. Without them,
					// the SUBNET create that follows fails with
					// "network type of nad is not kubeovn instead".
					"dc-api/managed":                            "true",
					"network.harvesterhci.io/type":              "OverlayNetwork",
					"network.harvesterhci.io/clusternetwork":    clusterNetwork,
				},
			},
			"spec": map[string]interface{}{
				"config": nadConfig,
			},
		},
	}
	// NAD is namespaced → pass ns. nadGVR is an onboarded family, so this create
	// routes through the seam for a remote zone (fixed, hardcoded name).
	_, err := c.access.Create(ctx, nadGVR, ns, obj, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("create external NAD %s/%s: %w", ns, nadName, err)
	}
	return nil
}

// ── VPC default route helpers ─────────────────────────────────────────────────

// ensureVPCDefaultRoute adds a 0.0.0.0/0 staticRoute pointing to lanIP on the
// VPC, merging with any existing routes (gotcha 3 — peering routes must be
// preserved). Idempotent: if a default route with the same nextHopIP already
// exists it is not duplicated.
func (c *Client) ensureVPCDefaultRoute(ctx context.Context, vpcName, lanIP string) error {
	// Seam Get on the onboarded vpcGVR (cluster-scoped → ns="") feeding the
	// read-modify-write; the write below (patchVPCStaticRoutes) is already an SSA
	// through the seam, so the whole op is agent-routable.
	vpc, err := c.access.Get(ctx, vpcGVR, "", vpcName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get vpc %q: %w", vpcName, err)
	}

	existing, _, _ := unstructured.NestedSlice(vpc.Object, "spec", "staticRoutes")

	// Check if the default route via this lanIP already exists.
	for _, e := range existing {
		m, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		if cidr, _ := m["cidr"].(string); cidr == "0.0.0.0/0" {
			if nh, _ := m["nextHopIP"].(string); nh == lanIP {
				return nil // already present — idempotent
			}
		}
	}

	// Remove any stale 0.0.0.0/0 route (e.g. leftover from a failed previous
	// provision attempt with a different lanIP). Preserve everything else.
	filtered := make([]interface{}, 0, len(existing))
	for _, e := range existing {
		m, ok := e.(map[string]interface{})
		if !ok {
			filtered = append(filtered, e)
			continue
		}
		if cidr, _ := m["cidr"].(string); cidr == "0.0.0.0/0" {
			// Remove the old default route; we will add the correct one below.
			continue
		}
		filtered = append(filtered, e)
	}

	// Append the new default route into the MAIN route table (empty string).
	// KubeOVN's routeTable field is the destination table name — a route in a
	// named table is only consulted via policy routing, not for normal traffic.
	// Yesterday's spike confirmed: main table is what makes traffic flow.
	// We identify the route for deletion by (cidr=0.0.0.0/0, nextHopIP=lanIP).
	filtered = append(filtered, map[string]interface{}{
		"cidr":       "0.0.0.0/0",
		"nextHopIP":  lanIP,
		"policy":     "policyDst",
		"routeTable": "",
	})

	return c.patchVPCStaticRoutes(ctx, vpcName, filtered)
}

// removeVPCDefaultRoute removes any 0.0.0.0/0 route from the VPC's static
// routes, preserving everything else (peering routes etc.) Tenant VPCs are
// fully managed by dc-api in F15 v1, so we treat any 0.0.0.0/0 as ours.
// Idempotent — safe to call when no such route exists.
func (c *Client) removeVPCDefaultRoute(ctx context.Context, vpcName string) error {
	// Seam Get (see ensureVPCDefaultRoute); the agent path shapes a missing VPC
	// as a k8s NotFound so the already-gone tolerance fires on both seams.
	vpc, err := c.access.Get(ctx, vpcGVR, "", vpcName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return nil // VPC already gone
	}
	if err != nil {
		return fmt.Errorf("get vpc %q: %w", vpcName, err)
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
		if cidr == "0.0.0.0/0" {
			continue // remove this entry
		}
		filtered = append(filtered, e)
	}
	return c.patchVPCStaticRoutes(ctx, vpcName, filtered)
}

// ── Wait helpers ──────────────────────────────────────────────────────────────

// waitForPodRunning polls kube-system for a pod by name until it reports
// phase==Running or the timeout expires.
func (c *Client) waitForPodRunning(ctx context.Context, ns, podName string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		// Seam Get (pods are namespaced → pass ns): the pod family routes reads
		// only, so this status poll works in a remote (agent-only) zone.
		obj, err := c.access.Get(ctx, podGVR, ns, podName, metav1.GetOptions{})
		if err == nil {
			phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
			if phase == "Running" {
				return nil
			}
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("pod %s/%s did not reach Running within %s", ns, podName, timeout)
		case <-time.After(3 * time.Second):
		}
	}
}

// waitForEIPReady polls until IptablesEIP.status.ready == true.
func (c *Client) waitForEIPReady(ctx context.Context, eipName string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		// Seam Get on the onboarded iptables-eips family (cluster-scoped → ns="").
		obj, err := c.access.Get(ctx, iptablesEIPGVR, "", eipName, metav1.GetOptions{})
		if err == nil {
			if ready, _, _ := unstructured.NestedBool(obj.Object, "status", "ready"); ready {
				return nil
			}
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("IptablesEIP %q did not become ready within %s", eipName, timeout)
		case <-time.After(3 * time.Second):
		}
	}
}

// readEIPAssignedIP returns the IP that KubeOVN's IPAM assigned to the
// IptablesEIP. The status field is `.status.ip` (NOT `.status.v4ip` — that
// difference between spec and status is a KubeOVN quirk verified live on
// v1.15.4).
func (c *Client) readEIPAssignedIP(ctx context.Context, eipName string) (net.IP, error) {
	obj, err := c.access.Get(ctx, iptablesEIPGVR, "", eipName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get IptablesEIP %q: %w", eipName, err)
	}
	ipStr, _, _ := unstructured.NestedString(obj.Object, "status", "ip")
	if ipStr == "" {
		return nil, fmt.Errorf("IptablesEIP %q has no .status.ip — controller hasn't allocated yet", eipName)
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil, fmt.Errorf("IptablesEIP %q .status.ip=%q is not a valid IP", eipName, ipStr)
	}
	return ip, nil
}

// waitForSnatReady polls until IptablesSnatRule.status.ready == true.
func (c *Client) waitForSnatReady(ctx context.Context, snatName string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		// Seam Get on the onboarded iptables-snat-rules family (cluster-scoped → ns="").
		obj, err := c.access.Get(ctx, iptablesSnatRuleGVR, "", snatName, metav1.GetOptions{})
		if err == nil {
			if ready, _, _ := unstructured.NestedBool(obj.Object, "status", "ready"); ready {
				return nil
			}
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("IptablesSnatRule %q did not become ready within %s", snatName, timeout)
		case <-time.After(3 * time.Second):
		}
	}
}

// ── IP range helpers ──────────────────────────────────────────────────────────

// buildExcludeIPs builds the excludeIps list for the external Subnet.
// KubeOVN's IPAM owns allocation on the external subnet; the only IPs we
// have to keep it away from are the upstream gateway and the operator-
// supplied reserved IPs (host nodes, ingress LB, anything already in use).
// KubeOVN itself reserves the network and broadcast addresses, so we don't
// need to list them.
func buildExcludeIPs(gateway string, reserved []string) []string {
	out := []string{gateway}
	out = append(out, reserved...)
	return out
}

// ComputeLanIP computes the second-to-last usable IP in a CIDR for use as the
// VpcNatGateway's lanIp field. Example: for 192.168.1.0/24 → 192.168.1.254.
// The last usable IP (broadcast - 1) is chosen to minimise collision with
// tenant VMs that typically start from .2 or .10 onwards.
func ComputeLanIP(cidr string) (net.IP, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("compute lan ip: invalid CIDR %q: %w", cidr, err)
	}
	network := ipNet.IP.To4()
	if network == nil {
		return nil, fmt.Errorf("compute lan ip: only IPv4 CIDRs supported")
	}
	mask := ipNet.Mask

	// Broadcast = network | ~mask
	broadcast := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		broadcast[i] = network[i] | ^mask[i]
	}

	// Penultimate = broadcast - 1
	penultimate := cloneIP(broadcast)
	decrementIPv4(penultimate)

	// Sanity: must not be the network address itself (would happen for /31 or /32)
	if penultimate.Equal(network) {
		return nil, fmt.Errorf("compute lan ip: subnet %q is too small for a lan IP", cidr)
	}

	return penultimate, nil
}

// ── Name derivation helpers ───────────────────────────────────────────────────

// NatGWName returns the stable VpcNatGateway CRD name for a VPC.
// Exported so integration tests can derive the same name without duplicating
// the hash logic.
//
// VpcNatGateway creates a StatefulSet whose pods are named
// "vpc-nat-gw-<gwName>-0". The pod NAME budget is 63 chars, but K8s also adds a
// `controller-revision-hash` LABEL whose value is "<SS-name>-<10-or-11-char-hash>",
// and label values are *also* capped at 63 chars. The label is what trips the
// 63-char limit first when gwName is in the 35..50 range, so we budget against
// the label form, not the pod-name form:
//
//	<SS-name = "vpc-nat-gw-" + gwName> + "-" + <11-char hash> ≤ 63
//	=> "vpc-nat-gw-" (11) + gwName + "-" (1) + 11 ≤ 63
//	=> gwName ≤ 40 chars.
//
// gwName itself carries a 6-char "natgw-" prefix, so the VPC-name portion has
// 34 chars to play with before we have to hash. Hashing produces a 12-char
// SHA-1 suffix so any oversized name collapses to "natgw-<12hex>" = 18 chars,
// which leaves the label well under 63 even with the K8s hash appended.
func NatGWName(vpcName string) string { return natGWName(vpcName) }

func natGWName(vpcName string) string {
	const prefix = "natgw-"
	// 40-char budget covers both the pod name AND the controller-revision-hash
	// label that K8s synthesises for StatefulSet-managed pods. See doc above.
	const budget = 40
	candidate := prefix + vpcName
	if len(candidate) <= budget {
		return candidate
	}
	sum := sha1.Sum([]byte(vpcName))
	return prefix + hex.EncodeToString(sum[:6]) // 12 hex chars → total 18
}

// natEIPName returns the stable IptablesEIP CRD name for a VPC.
func natEIPName(vpcName string) string {
	n := "eip-" + vpcName
	if len(n) > 253 {
		n = n[:253]
	}
	return n
}

// natSnatName returns the stable IptablesSnatRule CRD name for a VPC.
func natSnatName(vpcName string) string {
	n := "snat-" + vpcName
	if len(n) > 253 {
		n = n[:253]
	}
	return n
}

// bridgeToProviderName converts a bridge name to a safe ProviderNetwork name.
// KubeOVN enforces metadata.name ≤ 12 bytes on ProviderNetwork, so we cannot
// append a "-provider" suffix to a 7-char bridge. We DNS-1123 sanitise the
// bridge name and truncate to 12 chars. e.g. "mgmt-br" → "mgmt-br".
func bridgeToProviderName(bridge string) string {
	safe := strings.ToLower(strings.ReplaceAll(bridge, "_", "-"))
	if len(safe) > 12 {
		safe = safe[:12]
	}
	return safe
}

// ── Low-level IP helpers ──────────────────────────────────────────────────────

func cloneIP(ip net.IP) net.IP {
	c := make(net.IP, len(ip))
	copy(c, ip)
	return c
}

func incrementIPv4Slice(ip net.IP) {
	for i := 3; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return
		}
	}
}

func decrementIPv4(ip net.IP) {
	for i := 3; i >= 0; i-- {
		if ip[i] > 0 {
			ip[i]--
			return
		}
		ip[i] = 255
	}
}

func ipLessOrEqual(a, b net.IP) bool {
	a4 := a.To4()
	b4 := b.To4()
	for i := 0; i < 4; i++ {
		if a4[i] < b4[i] {
			return true
		}
		if a4[i] > b4[i] {
			return false
		}
	}
	return true // equal
}

// ── patchVPCStaticRoutes is already defined in client.go; accessible here
// because both files are in the same package. ─────────────────────────────────

// marshalPatch is a thin wrapper around json.Marshal used for patching.
func marshalPatch(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}
