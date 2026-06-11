// Package endpoints — kubeovn_provisioner.go
//
// KubeOVN-backed implementation of the generic Provisioner interface.
//
// What this code does, in one pass per Private Endpoint:
//
//   Provision:
//     1. Ensure the dc-api-endpoints namespace exists (lazy bootstrap).
//     2. Create a KubeOVN Vip CR in the tenant subnet → kube-ovn IPAM allocates
//        an IP and reports it back via status.v4ip.
//     3. Create the nginx-config ConfigMap.
//     4. Create the proxy Deployment with two NICs:
//        - eth0 → cluster default (Calico on Harvester) — used to reach the
//          backend's in-cluster Service.
//        - net1 → Multus + KubeOVN NAD into the tenant subnet, pinned to the
//          Vip's IP via the per-provider ip_address annotation (same form
//          F20 uses for the per-VPC CoreDNS pod).
//     5. Wait for the Deployment to roll out so callers can rely on the
//        endpoint being reachable when Provision returns.
//     6. Regenerate the per-VPC Corefile ConfigMap to include all current host
//        records (siblings + this new one). CoreDNS's reload plugin picks up
//        the change within ~30s; we don't restart the DNS pod.
//
//   Teardown reverses the above with a deterministic pod-drain wait between
//   Deployment delete and Vip delete — same F26 lesson the F20 CoreDNS lifecycle
//   already obeys: the Multus LSP pins the subnet until the pod is fully gone.
//
// This file holds the KubeOVN-specific work. The provisioner is otherwise
// target-agnostic — it never knows or cares that the backend it forwards to is
// OpenBao, Postgres, or anything else.
package endpoints

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/wso2/dc-api/internal/providers/kubeovn"
)

// KubeOVNProvisionerOptions tune the runtime behaviour of the provisioner.
// Sensible defaults are applied when fields are zero-valued.
type KubeOVNProvisionerOptions struct {
	// EndpointsNamespace is where proxy pods live. Default: "dc-api-endpoints".
	//
	// Was briefly "kube-system" during chunk-2 development — at the time
	// kube-ovn-controller appeared to refuse secondary-NIC allocation for
	// pods in a non-kube-system namespace. That turned out to be the
	// 63-char label-value cap on subnet names (fixed by the cap in
	// subnetResourceName / natGWName); namespace was a red herring. With
	// that fix in place, a dedicated dc-api-owned namespace is the cleaner
	// home for proxy pods — keeps dc-api's resources distinct from
	// kube-system's own infrastructure.
	EndpointsNamespace string        // default: "dc-api-endpoints"
	BootstrapNamespace string        // default: "kube-system" — where vpc-dns-corefile-* lives
	NginxImage         string        // default: "nginx:1.27-alpine"
	WaitTimeout        time.Duration // default: 90s
	DNSForwarders      []string      // default: ["1.1.1.1", "8.8.8.8"]
}

// KubeOVNProvisioner implements Provisioner against a Harvester+KubeOVN cluster.
type KubeOVNProvisioner struct {
	dynamic     dynamic.Interface
	endpointsNS string
	bootstrapNS string
	nginxImage  string
	waitTimeout time.Duration
	forwarders  []string
}

// NewKubeOVNProvisioner constructs a provisioner backed by the given dynamic
// client. Callers in main.go pass the same dynamic.Interface used by the rest
// of the kubeovn package.
func NewKubeOVNProvisioner(dyn dynamic.Interface, opts KubeOVNProvisionerOptions) *KubeOVNProvisioner {
	p := &KubeOVNProvisioner{
		dynamic:     dyn,
		endpointsNS: opts.EndpointsNamespace,
		bootstrapNS: opts.BootstrapNamespace,
		nginxImage:  opts.NginxImage,
		waitTimeout: opts.WaitTimeout,
		forwarders:  opts.DNSForwarders,
	}
	if p.endpointsNS == "" {
		p.endpointsNS = "dc-api-endpoints"
	}
	if p.bootstrapNS == "" {
		p.bootstrapNS = "kube-system"
	}
	if p.nginxImage == "" {
		p.nginxImage = "nginx:1.27-alpine"
	}
	if p.waitTimeout == 0 {
		p.waitTimeout = 90 * time.Second
	}
	if len(p.forwarders) == 0 {
		p.forwarders = []string{"1.1.1.1", "8.8.8.8"}
	}
	return p
}

// ── GVRs ─────────────────────────────────────────────────────────────────────

var (
	vipGVR        = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vips"}
	namespaceGVR  = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	configmapGVR  = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	deploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	podGVR        = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
)

// ── Naming ───────────────────────────────────────────────────────────────────

// ResourceName derives the shared name used for the Vip, the ConfigMap, and
// the proxy Deployment. Kept short for the k8s 63-char label limit.
//
// Format: "pe-<endpointID-short-hash>" (12 hex chars) — collision-free in
// practice and stable across pod restarts (recreate strategy reuses it).
// Exported so integration tests can derive the same name.
func ResourceName(endpointID uuid.UUID) string {
	sum := sha1.Sum([]byte(endpointID.String()))
	return "pe-" + hex.EncodeToString(sum[:6])
}

// formatHostname builds the DNS name a tenant resolves via per-VPC CoreDNS.
func formatHostname(name, serviceClass string) string {
	return fmt.Sprintf("%s.%s.dc.internal", strings.ToLower(name), strings.ToLower(serviceClass))
}

// ── Public API ───────────────────────────────────────────────────────────────

// Provision implements Provisioner.Provision.
//
// Steps are ordered so the call is restartable: every kubernetes create
// returns AlreadyExists as success, every wait is bounded, and the per-VPC
// Corefile is regenerated last so partial failures don't dangle a DNS record
// pointing at a not-yet-ready proxy.
//
// IPAM model: we let kube-ovn auto-allocate the secondary-NIC IP via the
// Multus annotation path (same mechanism F20's CoreDNS pod uses), then read
// the assigned IP back from the pod's network-status annotation. Skipping
// the Vip CR step avoids the "Vip stays without status.v4ip until something
// consumes it" race we hit on chunk-2's first pass — the spike already proved
// the annotation-driven path is sufficient. A future hardening pass can
// reintroduce Vip CRs for explicit IP reservation if pod-restart IP changes
// become a real problem.
func (p *KubeOVNProvisioner) Provision(ctx context.Context, in ProvisionInput) (ProvisionResult, error) {
	if err := p.ensureNamespace(ctx, p.endpointsNS); err != nil {
		return ProvisionResult{}, fmt.Errorf("ensure endpoints ns: %w", err)
	}

	name := ResourceName(in.EndpointID)
	hostname := formatHostname(in.Name, in.ServiceClass)

	// 1. Pick an IP from the tenant subnet CIDR. kube-ovn-controller doesn't
	//    auto-allocate secondary-NIC IPs reliably for arbitrary cross-namespace
	//    pods on our Harvester (the "no address allocated" failure mode from
	//    the chunk-2 first pass), so we pin an explicit IP via the per-provider
	//    ip_address annotation. The IP is chosen from a deterministic high-end
	//    range to avoid collision with F15's NAT GW (network+last-1) and F20's
	//    DNS pin (network+2): pick `network + 250 - len(siblings)`. Once
	//    siblings hit ~50 in the same /24 we'll need a smarter allocator.
	ip, err := pickEndpointIP(in.SubnetCIDR, len(in.SiblingHosts))
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("provision endpoint: pick IP: %w", err)
	}

	// 2. nginx config (idempotent).
	if err := p.ensureNginxConfigMap(ctx, name, in.BackendAddr, in.BackendPort); err != nil {
		return ProvisionResult{}, fmt.Errorf("provision endpoint: nginx config: %w", err)
	}

	// 3. Deployment with Multus secondary NIC + pinned IP.
	if err := p.ensureProxyDeployment(ctx, name, ip, in); err != nil {
		return ProvisionResult{}, fmt.Errorf("provision endpoint: deployment: %w", err)
	}

	// 4. Wait for pod ready.
	if err := p.waitDeploymentReady(ctx, name); err != nil {
		return ProvisionResult{}, fmt.Errorf("provision endpoint: wait ready: %w", err)
	}

	// 5. Regenerate per-VPC Corefile so the hostname resolves.
	all := append(append([]HostRecord{}, in.SiblingHosts...), HostRecord{Hostname: hostname, IPAddress: ip})
	if err := p.regenerateVpcCorefile(ctx, in.VNetBackendUID, all); err != nil {
		return ProvisionResult{}, fmt.Errorf("provision endpoint: corefile: %w", err)
	}

	log.Info().
		Str("endpoint", in.EndpointID.String()).
		Str("ip", ip).
		Str("hostname", hostname).
		Str("backend", fmt.Sprintf("%s:%d", in.BackendAddr, in.BackendPort)).
		Msg("endpoints: provisioned private endpoint")

	return ProvisionResult{
		IPAddress:    ip,
		Hostname:     hostname,
		ProxyPodName: name,
		VipName:      name, // retained for symmetry with TeardownInput (no Vip CR today)
	}, nil
}

// SyncCorefile rewrites the per-VPC Corefile so its hosts block contains
// exactly the given records. Callers that own host-record state in their own
// tables (e.g. the DBaaS DNS reconciler that derives records from the
// `databases` table without going through the proxy-pod path) use this to
// keep the Corefile in sync without invoking the full Provision/Teardown
// machinery.
//
// The records are written verbatim — the caller is responsible for assembling
// the complete list (its own records plus any siblings from other host-record
// sources in the same VPC, e.g. private_endpoints rows). The Corefile is the
// union of every record source, so passing only a partial list would silently
// drop the omitted entries.
func (p *KubeOVNProvisioner) SyncCorefile(ctx context.Context, vnetBackendUID string, hosts []HostRecord) error {
	return p.regenerateVpcCorefile(ctx, vnetBackendUID, hosts)
}

// Teardown implements Provisioner.Teardown.
//
// Order matters: Deployment first (pod terminates, Multus LSP releases the
// switch port), then ConfigMap, then Vip (frees the IPAM entry), then Corefile
// regen with the remaining sibling list. Each delete tolerates NotFound so
// retries are idempotent.
func (p *KubeOVNProvisioner) Teardown(ctx context.Context, in TeardownInput) error {
	name := in.ProxyPodName
	if name == "" {
		name = ResourceName(in.EndpointID)
	}
	vipName := in.VipName
	if vipName == "" {
		vipName = ResourceName(in.EndpointID)
	}

	if err := p.deleteIfFound(ctx, p.dynamic.Resource(deploymentGVR).Namespace(p.endpointsNS), name, "Deployment"); err != nil {
		return fmt.Errorf("teardown endpoint: deployment: %w", err)
	}
	if err := p.waitPodsGone(ctx, name); err != nil {
		// Log + keep going — pod stragglers shouldn't permanently block Vip cleanup.
		log.Warn().Err(err).Str("endpoint", in.EndpointID.String()).Msg("endpoints: proxy pod drain timed out — continuing teardown")
	}
	if err := p.deleteIfFound(ctx, p.dynamic.Resource(configmapGVR).Namespace(p.endpointsNS), name, "ConfigMap"); err != nil {
		return fmt.Errorf("teardown endpoint: configmap: %w", err)
	}
	// No Vip CR to delete in the annotation-driven IPAM path — kube-ovn frees
	// the LSP automatically when the pod terminates. The vipName parameter is
	// retained on TeardownInput for forward compatibility when we add Vips back.
	_ = vipName
	if err := p.regenerateVpcCorefile(ctx, in.VNetBackendUID, in.RemainingHosts); err != nil {
		return fmt.Errorf("teardown endpoint: corefile: %w", err)
	}

	log.Info().Str("endpoint", in.EndpointID.String()).Msg("endpoints: torn down private endpoint")
	return nil
}

// ── Internals ────────────────────────────────────────────────────────────────

func (p *KubeOVNProvisioner) ensureNamespace(ctx context.Context, name string) error {
	ns := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]interface{}{
				"name": name,
				"labels": map[string]interface{}{
					"dc-api/managed": "true",
				},
			},
		},
	}
	_, err := p.dynamic.Resource(namespaceGVR).Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// pickEndpointIP picks a deterministic IP from the subnet CIDR for a new
// endpoint, leaving room for sibling endpoints already in the VPC.
//
// Allocation scheme: start at <network>+250 for the first endpoint, decrement
// by `siblingCount` for subsequent ones. Avoids F20's <network>+2 (DNS pin)
// and F15's <broadcast>-1 (NAT GW). Good for ~50 endpoints per /24 — adequate
// for chunk-2 first ship. Replace with a proper allocator (Vip CRs or a
// dc-api-side IPAM table) before scale.
func pickEndpointIP(cidr string, siblingCount int) (string, error) {
	if cidr == "" {
		return "", fmt.Errorf("subnet CIDR is empty")
	}
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("parse CIDR %q: %w", cidr, err)
	}
	network := ipNet.IP.To4()
	if network == nil {
		return "", fmt.Errorf("only IPv4 CIDRs supported (got %q)", cidr)
	}
	// Compute network + 250 - siblingCount as a 4-byte slice arithmetic op.
	ip := make(net.IP, len(network))
	copy(ip, network)
	offset := 250 - siblingCount
	if offset < 3 {
		return "", fmt.Errorf("subnet %q is full for endpoint IPs (sibling count %d ≥ 247)", cidr, siblingCount)
	}
	ip[3] = byte(int(ip[3]) + offset)
	if !ipNet.Contains(ip) {
		return "", fmt.Errorf("computed IP %s falls outside subnet %s", ip, cidr)
	}
	return ip.String(), nil
}

// readNet1IP — retained for future use; the explicit-pin path doesn't need it
// today. Kept around so a future "read back kube-ovn-assigned IP" mode is a
// one-line switch instead of a re-implementation.
//
// Polls because the network-status annotation is written by Multus shortly
// after CNI completes — racing with pod-ready can briefly see an empty value.
//
//nolint:unused
func (p *KubeOVNProvisioner) readNet1IP(ctx context.Context, deployName, subnetBackendUID, tenantNS string) (string, error) {
	nadName := tenantNS + "/" + subnetBackendUID
	deadline := time.Now().Add(p.waitTimeout)
	for time.Now().Before(deadline) {
		list, err := p.dynamic.Resource(podGVR).Namespace(p.endpointsNS).List(ctx, metav1.ListOptions{
			LabelSelector: "app=private-endpoint",
		})
		if err == nil {
			for _, pod := range list.Items {
				// Match by ReplicaSet ownership (pod name is "<deploy>-<rs>-<hash>")
				matched := false
				for _, ref := range pod.GetOwnerReferences() {
					if strings.HasPrefix(ref.Name, deployName+"-") {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
				ann := pod.GetAnnotations()
				statusJSON := ann["k8s.v1.cni.cncf.io/network-status"]
				if statusJSON == "" {
					continue
				}
				ip := extractNet1IP(statusJSON, nadName)
				if ip != "" {
					return ip, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return "", fmt.Errorf("net1 IP not found on any pod for deployment %q within %s", deployName, p.waitTimeout)
}

// extractNet1IP parses the JSON-encoded network-status annotation and returns
// the first IP from the entry whose `name` matches the given NAD ref. Returns
// empty string if no match.
//
// The annotation looks like:
//
//	[{"name":"k8s-pod-network","ips":["10.52.x.x"],"interface":"eth0", ...},
//	 {"name":"<ns>/<nad>","ips":["10.231.1.5"],"interface":"net1", ...}]
func extractNet1IP(statusJSON, nadName string) string {
	type netStatusEntry struct {
		Name string   `json:"name"`
		IPs  []string `json:"ips"`
	}
	var entries []netStatusEntry
	if err := json.Unmarshal([]byte(statusJSON), &entries); err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Name == nadName && len(e.IPs) > 0 {
			return e.IPs[0]
		}
	}
	return ""
}

// ensureVipAllocated is retained but unused in the chunk-2 first-cut path.
// Kept for future hardening (explicit IP reservation across pod restarts).
//
//nolint:unused
func (p *KubeOVNProvisioner) ensureVipAllocated(ctx context.Context, name, subnetBackendUID string, endpointID uuid.UUID) (string, error) {
	vip := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1",
			"kind":       "Vip",
			"metadata": map[string]interface{}{
				"name": name,
				"labels": map[string]interface{}{
					"dc-api/managed":  "true",
					"dc-api/endpoint": endpointID.String(),
				},
			},
			"spec": map[string]interface{}{
				"subnet": subnetBackendUID,
			},
		},
	}
	if _, err := p.dynamic.Resource(vipGVR).Create(ctx, vip, metav1.CreateOptions{}); err != nil && !k8serrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create Vip %q: %w", name, err)
	}

	deadline := time.Now().Add(p.waitTimeout)
	for time.Now().Before(deadline) {
		obj, err := p.dynamic.Resource(vipGVR).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			if v4, _, _ := unstructured.NestedString(obj.Object, "status", "v4ip"); v4 != "" {
				return v4, nil
			}
			if v4, _, _ := unstructured.NestedString(obj.Object, "spec", "v4ip"); v4 != "" {
				return v4, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return "", fmt.Errorf("vip %q did not receive an IP within %s", name, p.waitTimeout)
}

// ensureNginxConfigMap creates (or updates) the nginx stream-forward
// ConfigMap. Update-on-change so a backend-port edit ripples through without
// requiring a delete + recreate.
func (p *KubeOVNProvisioner) ensureNginxConfigMap(ctx context.Context, name, backendAddr string, backendPort int) error {
	conf := fmt.Sprintf(`user nginx;
worker_processes auto;
pid /var/run/nginx.pid;
error_log /dev/stderr info;
events { worker_connections 1024; }
stream {
  log_format proxy '$remote_addr [$time_local] $protocol $status bytes_sent=$bytes_sent bytes_recv=$bytes_received session=$session_time upstream=$upstream_addr';
  access_log /dev/stdout proxy;
  upstream backend {
    server %s:%d;
  }
  server {
    listen 443;
    proxy_pass backend;
    proxy_timeout 30s;
    proxy_connect_timeout 5s;
  }
}
`, backendAddr, backendPort)

	cm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": p.endpointsNS,
				"labels": map[string]interface{}{
					"dc-api/managed": "true",
					"app":            "private-endpoint",
				},
			},
			"data": map[string]interface{}{
				"nginx.conf": conf,
			},
		},
	}
	if _, err := p.dynamic.Resource(configmapGVR).Namespace(p.endpointsNS).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return fmt.Errorf("create CM %q: %w", name, err)
		}
		// Update the conf in case backend addr/port changed (rare but possible).
		patch := []byte(fmt.Sprintf(`{"data":{"nginx.conf":%q}}`, conf))
		if _, err := p.dynamic.Resource(configmapGVR).Namespace(p.endpointsNS).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("patch CM %q: %w", name, err)
		}
	}
	return nil
}

// ensureProxyDeployment renders and creates the dual-NIC nginx Deployment.
// AlreadyExists is treated as success — re-running Provision should be safe.
//
// Pins the secondary-NIC IP via the per-provider ip_address annotation
// (same form F20's CoreDNS pod uses) because kube-ovn auto-allocation
// without this hint doesn't engage reliably on Harvester.
func (p *KubeOVNProvisioner) ensureProxyDeployment(ctx context.Context, name, ip string, in ProvisionInput) error {
	multusRef := in.TenantNS + "/" + in.SubnetBackendUID
	ipAnnotationKey := in.SubnetBackendUID + "." + in.TenantNS + ".ovn.kubernetes.io/ip_address"

	deploy := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": p.endpointsNS,
				"labels": map[string]interface{}{
					"app":             "private-endpoint",
					"dc-api/managed":  "true",
					"dc-api/endpoint": in.EndpointID.String(),
					"dc-api/target":   in.VNetBackendUID,
				},
			},
			"spec": map[string]interface{}{
				// Recreate so two replicas don't fight over the pinned IP.
				"strategy": map[string]interface{}{"type": "Recreate"},
				"replicas": int64(1),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app":             "private-endpoint",
						"dc-api/endpoint": in.EndpointID.String(),
					},
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"app":             "private-endpoint",
							"dc-api/managed":  "true",
							"dc-api/endpoint": in.EndpointID.String(),
						},
						"annotations": map[string]interface{}{
							"k8s.v1.cni.cncf.io/networks": multusRef,
							ipAnnotationKey:               ip,
						},
					},
					"spec": map[string]interface{}{
						// Drain the proxy pod fast on delete so the LSP releases
						// promptly (mirrors F20's CoreDNS terminationGracePeriod).
						"terminationGracePeriodSeconds": int64(2),
						"containers": []interface{}{
							map[string]interface{}{
								"name":            "proxy",
								"image":           p.nginxImage,
								"imagePullPolicy": "IfNotPresent",
								"ports": []interface{}{
									map[string]interface{}{
										"containerPort": int64(443),
										"name":          "api",
									},
								},
								"volumeMounts": []interface{}{
									map[string]interface{}{
										"name":      "conf",
										"mountPath": "/etc/nginx/nginx.conf",
										"subPath":   "nginx.conf",
									},
								},
								"readinessProbe": map[string]interface{}{
									"tcpSocket": map[string]interface{}{
										"port": int64(443),
									},
									"initialDelaySeconds": int64(2),
									"periodSeconds":       int64(5),
								},
							},
						},
						"volumes": []interface{}{
							map[string]interface{}{
								"name": "conf",
								"configMap": map[string]interface{}{
									"name": name,
									"items": []interface{}{
										map[string]interface{}{
											"key":  "nginx.conf",
											"path": "nginx.conf",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if _, err := p.dynamic.Resource(deploymentGVR).Namespace(p.endpointsNS).Create(ctx, deploy, metav1.CreateOptions{}); err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("create Deployment %q: %w", name, err)
	}
	return nil
}

// waitDeploymentReady polls the Deployment until status.readyReplicas >= 1.
func (p *KubeOVNProvisioner) waitDeploymentReady(ctx context.Context, name string) error {
	deadline := time.Now().Add(p.waitTimeout)
	for time.Now().Before(deadline) {
		obj, err := p.dynamic.Resource(deploymentGVR).Namespace(p.endpointsNS).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			ready, _, _ := unstructured.NestedInt64(obj.Object, "status", "readyReplicas")
			if ready >= 1 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("deployment %q not ready within %s", name, p.waitTimeout)
}

// waitPodsGone polls until all pods owned by ReplicaSets prefixed with the
// proxy Deployment name are absent. Bounded so a wedged pod can't trap teardown.
func (p *KubeOVNProvisioner) waitPodsGone(ctx context.Context, deployName string) error {
	deadline := time.Now().Add(p.waitTimeout)
	for time.Now().Before(deadline) {
		list, err := p.dynamic.Resource(podGVR).Namespace(p.endpointsNS).List(ctx, metav1.ListOptions{
			LabelSelector: "app=private-endpoint",
		})
		if err == nil {
			anyMatch := false
			for _, item := range list.Items {
				for _, ref := range item.GetOwnerReferences() {
					if strings.HasPrefix(ref.Name, deployName+"-") {
						anyMatch = true
						break
					}
				}
				if anyMatch {
					break
				}
			}
			if !anyMatch {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("pods for deployment %q still present after %s", deployName, p.waitTimeout)
}

// regenerateVpcCorefile rewrites the per-VPC Corefile so the hosts block
// contains exactly the given records. Idempotent — patches in-place.
//
// The per-VPC Corefile ConfigMap is named per the F20 refactor's
// vpc-dns-corefile-<vpcUID> convention. We reuse the kubeovn package's
// VpcDNSCorefileName helper to stay in lockstep.
func (p *KubeOVNProvisioner) regenerateVpcCorefile(ctx context.Context, vnetBackendUID string, hosts []HostRecord) error {
	cmName := kubeovn.VpcDNSCorefileName(vnetBackendUID)
	corefile := p.renderCorefile(hosts)

	patch := []byte(fmt.Sprintf(`{"data":{"Corefile":%q}}`, corefile))
	if _, err := p.dynamic.Resource(configmapGVR).Namespace(p.bootstrapNS).Patch(ctx, cmName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			// VPC has no DNS bootstrap yet (no subnet has been created). Skip — when
			// the first subnet lands, the per-VPC Corefile is built fresh and we'll
			// re-trigger this regen at that point. For chunk 2 this should never
			// happen in practice (private endpoints require a subnet to exist).
			log.Warn().Str("cm", cmName).Msg("endpoints: per-VPC Corefile CM not found — skipping host-record write")
			return nil
		}
		return fmt.Errorf("patch Corefile %q: %w", cmName, err)
	}
	return nil
}

// renderCorefile builds the Corefile string from the same forward+cache base
// F20 uses, plus an optional hosts block when records are present.
func (p *KubeOVNProvisioner) renderCorefile(hosts []HostRecord) string {
	var hostsBlock strings.Builder
	if len(hosts) > 0 {
		hostsBlock.WriteString("    hosts {\n")
		for _, h := range hosts {
			hostsBlock.WriteString(fmt.Sprintf("        %s %s\n", h.IPAddress, h.Hostname))
		}
		hostsBlock.WriteString("        fallthrough\n")
		hostsBlock.WriteString("    }\n")
	}
	return fmt.Sprintf(`.:53 {
    errors
    health {
        lameduck 5s
    }
    ready
%s    forward . %s { max_concurrent 1000 }
    cache 300
    loop
    reload
    loadbalance
}
`, hostsBlock.String(), strings.Join(p.forwarders, " "))
}

// deleteIfFound deletes a resource and treats NotFound as success.
func (p *KubeOVNProvisioner) deleteIfFound(ctx context.Context, res dynamic.ResourceInterface, name, kind string) error {
	if err := res.Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete %s %q: %w", kind, name, err)
	}
	return nil
}
