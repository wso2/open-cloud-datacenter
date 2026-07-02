// Package rancher implements providers.ClusterProvider against Rancher v2
// using the Provisioning v2 API (fleet-default namespace, RKE2).
//
// Why provisioning v2 and not /v3/clusters?
//   /v3/clusters is the legacy RKE1 endpoint. RKE2 clusters on Harvester use
//   the provisioning.cattle.io API group. This is the same API the Rancher UI
//   uses since Rancher 2.6+.
//
// Two-step create flow:
//  1. POST a HarvesterConfig (machine config) defining node VM specs.
//     Each cluster gets its own machine config in fleet-default namespace.
//  2. POST the provisioning cluster referencing that machine config +
//     the pre-existing Harvester cloud credential.
//
// Prerequisite (operator one-time setup):
//   A Harvester cloud credential must already exist in Rancher.
//   Create it: Rancher UI → Cluster Management → Cloud Credentials → Harvester.
//   Set DCAPI_RANCHER_HARVESTER_CREDENTIAL to the resulting secret name.
package rancher

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers/common"
)

const (
	provisioningAPIPrefix = "/v1/provisioning.cattle.io.clusters"
	machineConfigPrefix   = "/v1/rke-machine-config.cattle.io.harvesterconfigs"
	fleetDefault          = "fleet-default"
)

// Client implements providers.ClusterProvider against Rancher provisioning v2.
type Client struct {
	baseURL          string
	token            string
	credential       string // Harvester cloud credential secret name
	operatorSSHKey   string // IaaS team SSH public key injected into every node
	operatorPassword string // IaaS team recovery console password
	httpClient       *http.Client

	// provisioner is the Steve-based orchestrator (F32). When set (VPC path),
	// CreateCluster/GetCluster/DeleteCluster delegate to it. When nil (legacy
	// bridge path or tests) the old Norman v1 path is used.
	provisioner *ClusterProvisioner

	// mgmtNAD is the operator management NAD, stored so WithHarvesterProviders
	// can build the provisioner after both clients are initialised in main.go.
	mgmtNAD     string
	vmNamespace string
}

// NewClient creates a Rancher client.
//
// credential:       name of the pre-existing Harvester cloud credential in Rancher.
// mgmtNAD:          operator management NAD, e.g. "iaas/vm-network-001".
// vmNamespace:      default Harvester namespace for node VMs.
// operatorSSHKey:   IaaS break-glass SSH public key; empty = not injected.
// operatorPassword: IaaS break-glass console password; empty = not injected.
//
// After calling NewClient, optionally call WithHarvesterProviders to wire the
// cloud-provider SA ensurer so cluster creates on VPCs bootstrap correctly.
func NewClient(baseURL, token string, insecure bool, credential, mgmtNAD, vmNamespace, operatorSSHKey, operatorPassword string) (*Client, error) {
	transport := http.DefaultTransport
	if insecure {
		// #nosec G402 — intentionally disabled for dev environments with self-signed certs.
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}
	steve := NewSteveClient(strings.TrimRight(baseURL, "/"), token, httpClient)
	c := &Client{
		baseURL:          strings.TrimRight(baseURL, "/"),
		token:            token,
		credential:       credential,
		mgmtNAD:          mgmtNAD,
		vmNamespace:      vmNamespace,
		operatorSSHKey:   strings.TrimSpace(operatorSSHKey),
		operatorPassword: operatorPassword,
		httpClient:       httpClient,
	}
	// Build the Steve-based provisioner immediately (saEnsurer/apiInfoProvider nil
	// until WithHarvesterProviders is called).
	c.provisioner = NewClusterProvisioner(
		steve,
		credential, mgmtNAD, vmNamespace, operatorSSHKey, operatorPassword,
		nil, nil,
	)
	return c, nil
}

// WithHarvesterProviders wires the cloud-provider SA ensurer and Harvester API
// info provider into the Steve-based cluster provisioner. Call this in main.go
// after both the harvester client and the rancher client are initialised:
//
//	rancherClient.WithHarvesterProviders(harvesterClient, harvesterClient)
//
// When not called, cluster creates on VPCs skip the SA bootstrap step and fall
// back to the global cloud credential (the pre-F32 behaviour).
func (c *Client) WithHarvesterProviders(
	saEnsurer CloudProviderSAEnsurer,
	apiInfo HarvesterAPIInfoProvider,
) {
	httpClient := &http.Client{
		Transport: c.httpClient.Transport,
		Timeout:   30 * time.Second,
	}
	steve := NewSteveClient(c.baseURL, c.token, httpClient)
	c.provisioner = NewClusterProvisioner(
		steve,
		c.credential, c.mgmtNAD, c.vmNamespace, c.operatorSSHKey, c.operatorPassword,
		saEnsurer, apiInfo,
	)
}

// Name satisfies providers.ClusterProvider.
func (c *Client) Name() string { return "rancher" }

// ─────────────────────────── Create ─────────────────────────────────────────

// CreateCluster creates an RKE2 cluster on Harvester.
//
// VPC path (spec.VNetBackendUID + spec.SubnetBackendUID set): delegates to the
// Steve-based ClusterProvisioner which handles SA bootstrap, harvesterconfig
// Secret pre-creation, system-pool HarvesterConfig, and the Cluster CR atomically.
//
// Legacy path (NetworkName set): uses the old Norman v1 path (single NIC, no
// SA bootstrap). Kept for backward compat with bridge-mode clusters.
//
// spec.SystemPool must be non-nil for the VPC path; the handler populates it
// including HarvesterConfigName before calling CreateCluster.
//
// BackendUID stored as "fleet-default/<cluster-name>" for direct lookup.
// projectID is the human-readable project slug; together with tenantID it
// determines the Kubernetes namespace for cluster VMs: "dc-<tenant>-<project>".
func (c *Client) CreateCluster(ctx context.Context, tenantID, projectID string, spec models.ClusterSpec) (*models.Resource, error) {
	isVPCPath := spec.VNetBackendUID != "" && spec.SubnetBackendUID != ""

	if isVPCPath {
		// Resolve the project namespace and full NAD reference.
		tenantNS := common.NamespaceForProject(tenantID, projectID)
		tenantNAD := tenantNS + "/" + spec.SubnetBackendUID

		// Resolve compute values from the system pool's size.
		var nodeCPU, nodeMemGB, nodeDiskGB int
		if spec.SystemPool != nil {
			if sz, ok := models.Sizes[spec.SystemPool.Size]; ok {
				nodeCPU = sz.CPU
				nodeMemGB = sz.MemoryGB
				nodeDiskGB = sz.DefaultDiskGB
				if spec.SystemPool.DiskGB != nil {
					nodeDiskGB = *spec.SystemPool.DiskGB
				}
			}
		}

		pSpec := ClusterCreateSpec{
			ClusterName:     spec.Name,
			K8sVersion:      spec.K8sVersion,
			NodeImage:       spec.ImageName,
			TenantSubnetNAD: tenantNAD,
			VMNamespace:     tenantNS,
			SystemPool:      spec.SystemPool,
			WorkerPools:     spec.WorkerPools,
			NodeCPU:         nodeCPU,
			NodeMemoryGB:    nodeMemGB,
			NodeDiskGB:      nodeDiskGB,
		}

		backendUID, err := c.provisioner.CreateCluster(ctx, pSpec)
		if err != nil {
			return nil, fmt.Errorf("steve create cluster: %w", err)
		}
		return &models.Resource{
			ID:           uuid.New(),
			TenantID:     tenantID,
			Name:         spec.Name,
			Type:         models.ResourceTypeCluster,
			Status:       models.StatusPending,
			ProviderType: c.Name(),
			BackendUID:   backendUID,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}, nil
	}

	// Legacy bridge path — single NIC, no SA bootstrap.
	// Build a synthetic system pool for the legacy path so the provisioner can use
	// buildMachinePool() uniformly. The handler may not populate SystemPool for the
	// legacy path, so we construct a minimal one here.
	legacyPool := &models.NodePool{
		Name:                "pool",
		Role:                models.NodePoolRoleSystem,
		HarvesterConfigName: spec.Name + "-pool",
	}
	if spec.SystemPool != nil {
		legacyPool = spec.SystemPool
	}
	machineConfigName := legacyPool.HarvesterConfigName
	if machineConfigName == "" {
		machineConfigName = spec.Name + "-pool"
	}
	if err := c.createHarvesterConfig(ctx, machineConfigName, spec); err != nil {
		return nil, fmt.Errorf("create harvester machine config: %w", err)
	}
	if err := c.createProvisioningCluster(ctx, spec, machineConfigName); err != nil {
		return nil, fmt.Errorf("create rancher cluster: %w", err)
	}
	return &models.Resource{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Name:         spec.Name,
		Type:         models.ResourceTypeCluster,
		Status:       models.StatusPending,
		ProviderType: c.Name(),
		BackendUID:   fleetDefault + "/" + spec.Name,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, nil
}

// createHarvesterConfig POSTs a HarvesterConfig for the legacy (bridge-mode)
// path. Compute values are derived from spec.SystemPool if set; otherwise
// defaults are used. This path is kept for backward compatibility only.
func (c *Client) createHarvesterConfig(ctx context.Context, name string, spec models.ClusterSpec) error {
	// Resolve compute from the system pool size (legacy path may or may not have it).
	cpu, mem, disk := 4, 8, 40 // sensible defaults
	if spec.SystemPool != nil {
		if sz, ok := models.Sizes[spec.SystemPool.Size]; ok {
			cpu, mem, disk = sz.CPU, sz.MemoryGB, sz.DefaultDiskGB
		}
		if spec.SystemPool.DiskGB != nil {
			disk = *spec.SystemPool.DiskGB
		}
	}

	body := map[string]interface{}{
		"type": "rke-machine-config.cattle.io.harvesterconfig",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": fleetDefault,
		},
		"cpuCount":    fmt.Sprintf("%d", cpu),
		"memorySize":  fmt.Sprintf("%d", mem),
		"diskSize":    fmt.Sprintf("%d", disk),
		"networkName": spec.NetworkName,
		"imageName":   spec.ImageName,
		"diskBus":     "virtio",
		"sshUser":     "ubuntu",
		"vmNamespace": "default",
		"userData":    c.buildNodeUserData(),
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}

	resp, err := c.do(ctx, http.MethodPost, machineConfigPrefix, raw)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// createProvisioningCluster POSTs the provisioning.cattle.io.cluster resource.
// Used only by the legacy bridge path; VPC clusters go through the
// ClusterProvisioner (Steve path) via CreateCluster.
func (c *Client) createProvisioningCluster(ctx context.Context, spec models.ClusterSpec, machineConfigName string) error {
	// Node count for the legacy single-pool: from SystemPool.Count or default 1.
	nodeCount := 1
	if spec.SystemPool != nil && spec.SystemPool.Count > 0 {
		nodeCount = spec.SystemPool.Count
	}

	body := map[string]interface{}{
		"type": "provisioning.cattle.io.cluster",
		"metadata": map[string]interface{}{
			"name":      spec.Name,
			"namespace": fleetDefault,
		},
		"spec": map[string]interface{}{
			"kubernetesVersion":         spec.K8sVersion,
			"cloudCredentialSecretName": c.credential,
			"rkeConfig": map[string]interface{}{
				"machineGlobalConfig": map[string]interface{}{
					"cni": "calico",
				},
				"machinePools": []interface{}{
					map[string]interface{}{
						"name":             "pool",
						"quantity":         nodeCount,
						"etcdRole":         true,
						"controlPlaneRole": true,
						"workerRole":       true,
						"drainBeforeDelete": true,
						"machineOS":        "linux",
						"machineConfigRef": map[string]interface{}{
							"kind": "HarvesterConfig",
							"name": machineConfigName,
						},
					},
				},
			},
		},
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}

	resp, err := c.do(ctx, http.MethodPost, provisioningAPIPrefix, raw)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// ─────────────────────────── Get ────────────────────────────────────────────

// GetCluster fetches current cluster state from Rancher provisioning v2 via Steve.
// backendUID is "fleet-default/<cluster-name>".
func (c *Client) GetCluster(ctx context.Context, backendUID string) (*models.Resource, error) {
	_, name, err := parseBackendUID(backendUID)
	if err != nil {
		return nil, err
	}

	cs, err := c.provisioner.GetClusterStatus(ctx, backendUID)
	if err != nil {
		// IsSteveNotFound → propagate as a typed not-found sentinel so the
		// reconciler can clean up structurally (NotFound()) instead of
		// substring-matching. Msg keeps the pre-existing error text.
		if IsSteveNotFound(err) {
			return nil, &common.NotFoundError{
				Kind: "cluster",
				Name: backendUID,
				Msg:  fmt.Sprintf("cluster %q not found in Rancher", backendUID),
			}
		}
		return nil, err
	}

	var status models.ResourceStatus
	msg := cs.Message

	switch {
	case cs.Ready:
		status = models.StatusActive
		msg = ""
	case msg != "" && !cs.Ready:
		// A non-empty message on a non-ready cluster typically means the
		// Stalled condition is True (permanent failure). Map to FAILED so
		// the reconciler stops polling.
		status = models.StatusFailed
	default:
		// Still provisioning — transient state.
		status = models.StatusPending
	}

	return &models.Resource{
		Type:         models.ResourceTypeCluster,
		ProviderType: c.Name(),
		BackendUID:   backendUID,
		Name:         name,
		Status:       status,
		Message:      msg,
	}, nil
}

// ─────────────────────────── Delete ─────────────────────────────────────────

// DeleteCluster deletes a cluster and its associated artifacts from Rancher.
// Delegates to the Steve-based ClusterProvisioner which handles all cleanup
// (Cluster CR, HarvesterConfig CR, harvesterconfig Secret).
func (c *Client) DeleteCluster(ctx context.Context, backendUID string) error {
	return c.provisioner.DeleteCluster(ctx, backendUID)
}

// ─────────────────────────── Pool lifecycle ──────────────────────────────────

// AddNodePool creates a HarvesterConfig and appends a worker pool to the cluster.
// pool.HarvesterConfigName must already be set by the handler.
func (c *Client) AddNodePool(
	ctx context.Context,
	clusterName string,
	pool *models.NodePool,
	mgmtNAD, tenantSubnetNAD, vmNamespace, nodeImage string,
) error {
	// Resolve compute values from the pool's size.
	sz, ok := models.Sizes[pool.Size]
	if !ok {
		return fmt.Errorf("add node pool: unknown size %q", pool.Size)
	}
	cpuCount := sz.CPU
	memoryGB := sz.MemoryGB
	diskGB := sz.DefaultDiskGB
	if pool.DiskGB != nil {
		diskGB = *pool.DiskGB
	}
	return c.provisioner.AddNodePool(ctx, clusterName, pool, mgmtNAD, tenantSubnetNAD, vmNamespace, nodeImage, cpuCount, memoryGB, diskGB)
}

// ScaleNodePool sets machinePools[i].quantity for the named pool via GET-then-PUT.
func (c *Client) ScaleNodePool(ctx context.Context, clusterName, poolName string, newCount int) error {
	return c.provisioner.ScaleNodePool(ctx, clusterName, poolName, newCount)
}

// UpdateNodePoolTaintsLabels replaces taints and labels on a pool via GET-then-PUT.
func (c *Client) UpdateNodePoolTaintsLabels(
	ctx context.Context,
	clusterName, poolName string,
	taints []models.NodePoolTaint,
	labels map[string]string,
) error {
	return c.provisioner.UpdateNodePoolTaintsLabels(ctx, clusterName, poolName, taints, labels)
}

// RemoveNodePool drops the pool from machinePools[] and deletes the HarvesterConfig CR.
func (c *Client) RemoveNodePool(ctx context.Context, clusterName, poolName, harvesterConfigName string) error {
	return c.provisioner.RemoveNodePool(ctx, clusterName, poolName, harvesterConfigName)
}

// GetNodePoolStatuses returns per-pool status from the live Cluster CR.
func (c *Client) GetNodePoolStatuses(ctx context.Context, clusterName string) (map[string]models.NodePoolStatus, error) {
	return c.provisioner.GetNodePoolStatuses(ctx, clusterName)
}

// ─────────────────────────── Kubeconfig ─────────────────────────────────────

// GetKubeconfig fetches the kubeconfig for an active cluster.
//
// Two-step process required by Rancher provisioning v2:
//  1. Fetch the provisioning cluster to obtain status.clusterName (the mgmt cluster ID).
//  2. Call POST /v3/clusters/<mgmt-id>?action=generateKubeconfig on the v3 API.
//
// The provisioning v2 endpoint does not expose generateKubeconfig directly.
func (c *Client) GetKubeconfig(ctx context.Context, backendUID string) (string, error) {
	ns, name, err := parseBackendUID(backendUID)
	if err != nil {
		return "", err
	}

	// Step 1: resolve management cluster ID from the provisioning cluster status.
	provPath := fmt.Sprintf("%s/%s/%s", provisioningAPIPrefix, ns, name)
	provResp, err := c.do(ctx, http.MethodGet, provPath, nil)
	if err != nil {
		return "", fmt.Errorf("fetch provisioning cluster: %w", err)
	}
	defer provResp.Body.Close()

	if provResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(provResp.Body)
		return "", fmt.Errorf("fetch provisioning cluster HTTP %d: %s", provResp.StatusCode, string(b))
	}

	var provResult struct {
		Status struct {
			ClusterName string `json:"clusterName"`
		} `json:"status"`
	}
	if err := json.NewDecoder(provResp.Body).Decode(&provResult); err != nil {
		return "", fmt.Errorf("decode provisioning cluster: %w", err)
	}
	mgmtID := provResult.Status.ClusterName
	if mgmtID == "" {
		return "", fmt.Errorf("cluster %q has no management cluster ID yet — still provisioning?", name)
	}

	// Step 2: generate kubeconfig via the v3 management clusters endpoint.
	kcPath := fmt.Sprintf("/v3/clusters/%s?action=generateKubeconfig", mgmtID)
	kcResp, err := c.do(ctx, http.MethodPost, kcPath, nil)
	if err != nil {
		return "", fmt.Errorf("generateKubeconfig: %w", err)
	}
	defer kcResp.Body.Close()

	if kcResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(kcResp.Body)
		return "", fmt.Errorf("generateKubeconfig HTTP %d: %s", kcResp.StatusCode, string(b))
	}

	var result struct {
		Config string `json:"config"`
	}
	if err := json.NewDecoder(kcResp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode kubeconfig response: %w", err)
	}
	return result.Config, nil
}

// ─────────────────────────── HTTP helper ────────────────────────────────────

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	return c.httpClient.Do(req)
}

// ─────────────────────────── Helpers ────────────────────────────────────────

// buildNodeUserData returns a cloud-init userData string applied to every cluster
// node VM. It always installs qemu-guest-agent (required: Rancher's provisioner
// reads the node IP from the Harvester API via the guest agent; without it the
// machine stays stuck at "Waiting for Node Ref"). It also installs the packages
// and kernel modules used by all production clusters in this datacenter.
// Operator SSH key / password are appended when configured.
func (c *Client) buildNodeUserData() string {
	var b strings.Builder
	b.WriteString("#cloud-config\n")
	b.WriteString("package_update: true\n")
	b.WriteString("packages:\n")
	b.WriteString("  - qemu-guest-agent\n")
	b.WriteString("  - nfs-common\n")
	b.WriteString("  - net-tools\n")
	b.WriteString("  - ipvsadm\n")
	b.WriteString("write_files:\n")
	b.WriteString("  - path: /etc/sysctl.d/99-inotify.conf\n")
	b.WriteString("    content: |\n")
	b.WriteString("      fs.inotify.max_user_watches=524288\n")
	b.WriteString("      fs.inotify.max_user_instances=8192\n")
	b.WriteString("      fs.inotify.max_queued_events=65536\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: '0644'\n")
	b.WriteString("  - path: /etc/modules-load.d/ipvs.conf\n")
	b.WriteString("    content: |\n")
	b.WriteString("      ip_vs\n")
	b.WriteString("      ip_vs_rr\n")
	b.WriteString("      ip_vs_wrr\n")
	b.WriteString("      ip_vs_sh\n")
	b.WriteString("      nf_conntrack\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: '0644'\n")
	b.WriteString("runcmd:\n")
	b.WriteString("  - systemctl enable --now qemu-guest-agent.service\n")
	b.WriteString("  - modprobe ip_vs\n")
	b.WriteString("  - modprobe ip_vs_rr\n")
	b.WriteString("  - modprobe ip_vs_wrr\n")
	b.WriteString("  - modprobe ip_vs_sh\n")
	b.WriteString("  - modprobe nf_conntrack\n")
	b.WriteString("  - sysctl --system\n")

	if c.operatorPassword != "" {
		b.WriteString("ssh_pwauth: true\n")
		b.WriteString("chpasswd:\n")
		b.WriteString("  expire: false\n")
		b.WriteString(fmt.Sprintf("  list: |\n    ubuntu:%s\n", c.operatorPassword))
	}

	if c.operatorSSHKey != "" {
		b.WriteString("ssh_authorized_keys:\n")
		b.WriteString(fmt.Sprintf("  - %s\n", c.operatorSSHKey))
	}

	return b.String()
}

func parseBackendUID(backendUID string) (namespace, name string, err error) {
	parts := strings.SplitN(backendUID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid rancher BackendUID %q — expected \"namespace/name\"", backendUID)
	}
	return parts[0], parts[1], nil
}

// conditionsToStatus maps Rancher provisioning v2 status to DC-API ResourceStatus.
//
// The provisioning v2 API (used for RKE2 clusters) does not use a "phase" field.
// Readiness is communicated via status.ready and a set of conditions.
// Key conditions:
//   - Ready=True → cluster is fully up
//   - Stalled=True → permanent error, will not self-heal
//   - Reconciling=True → still being updated (transient, not an error)
//   - Provisioned/Updated/MachinesReady etc. → intermediate states with
//     informative `message` describing what the cluster is waiting on
//
// Returns (status, message). The message is:
//   - empty when status=ACTIVE
//   - the Stalled condition's message when status=FAILED
//   - the most informative non-True condition's message when status=PENDING
//     (F39: surface provisioning progress in GET /v1/clusters/{id}). If no
//     condition carries a message yet, returns empty string — the reconciler
//     persists message changes regardless of status, so a transient empty
//     message is fine.
func conditionsToStatus(ready bool, conditions []struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
}) (models.ResourceStatus, string) {
	if ready {
		return models.StatusActive, ""
	}

	// Check for permanent failure (Stalled=True).
	for _, c := range conditions {
		if c.Type == "Stalled" && c.Status == "True" {
			msg := c.Message
			if msg == "" {
				msg = "cluster stalled"
			}
			return models.StatusFailed, msg
		}
	}

	// Not ready and not stalled → still provisioning. Surface the most
	// informative condition message as a progress hint.
	return models.StatusPending, pickProgressMessage(conditions)
}

// pickProgressMessage scans the conditions array and returns the first
// `message` that's likely to be useful to a tenant watching their cluster
// come up. Priority order:
//
//  1. Stalled=False with message (rare but means the cluster recovered from
//     a stall — worth surfacing)
//  2. Reconciling=True with message (current activity)
//  3. Any non-True condition with message (covers Provisioned, Updated,
//     MachinesReady, etc. — Rancher fills these in with phrases like
//     "waiting for viable init node" or "control plane is initializing")
//
// Returns empty string if no condition carries a message yet. That's fine;
// the reconciler keeps polling and will surface a later message when one
// appears.
func pickProgressMessage(conditions []struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
}) string {
	// Pass 1: prefer Reconciling=True (signals current activity).
	for _, c := range conditions {
		if c.Type == "Reconciling" && c.Status == "True" && c.Message != "" {
			return c.Message
		}
	}
	// Pass 2: any non-True condition with a populated message.
	for _, c := range conditions {
		if c.Status != "True" && c.Message != "" && c.Type != "Stalled" {
			return c.Message
		}
	}
	return ""
}
