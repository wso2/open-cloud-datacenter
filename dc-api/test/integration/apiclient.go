//go:build integration

// apiclient.go — thin HTTP client SDK for DC-API networking endpoints.
// Returns parsed structs + status codes. NO assertions live here — assertions
// live in *_test.go files. This file is a faithful Go binding around the API.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type APIClient struct {
	baseURL   string
	token     string
	tenantID  string
	projectID string // when set, resource calls go under /projects/{projectID}/
	http      *http.Client
}

// NewAPIClient creates an API client without a bound tenant. Use
// NewAPIClientForTenant when the client should scope all resource calls to a
// specific tenant (the common case in integration tests).
func NewAPIClient(baseURL, token string) *APIClient {
	return &APIClient{baseURL: baseURL, token: token, http: &http.Client{}}
}

// NewAPIClientForTenant creates an API client bound to a specific tenant.
// All resource endpoints (vnets, virtual-machines, security-groups, etc.)
// are automatically prefixed with /v1/tenants/{tenantID}/.
func NewAPIClientForTenant(baseURL, token, tenantID string) *APIClient {
	return &APIClient{baseURL: baseURL, token: token, tenantID: tenantID, http: &http.Client{}}
}

// NewAPIClientForProject creates an API client bound to a specific tenant and
// project. All project-scoped resource endpoints (vnets, virtual-machines, etc.)
// are prefixed with /v1/tenants/{tenantID}/projects/{projectID}/.
func NewAPIClientForProject(baseURL, token, tenantID, projectID string) *APIClient {
	return &APIClient{baseURL: baseURL, token: token, tenantID: tenantID, projectID: projectID, http: &http.Client{}}
}

// tenantBasePath returns /v1/tenants/{tenantID}. Panics if tenantID is empty.
func (c *APIClient) tenantBasePath() string {
	if c.tenantID == "" {
		panic("APIClient.tenantID is empty: use NewAPIClientForTenant or set tenantID directly")
	}
	return "/v1/tenants/" + c.tenantID
}

// projectPath returns the project-scoped base path, e.g.
// "/v1/tenants/my-tenant/projects/default". Used for all resource endpoints
// that live under a project. Panics if projectID is not set.
func (c *APIClient) projectPath() string {
	if c.projectID == "" {
		panic("APIClient.projectID is empty: use NewAPIClientForProject or WithProject")
	}
	return c.tenantBasePath() + "/projects/" + c.projectID
}

// tenantPath returns the base path for tenant-scoped or project-scoped
// resources. When a projectID is bound to the client it returns the project
// path (/v1/tenants/{tid}/projects/{pid}); otherwise it returns the tenant
// path (/v1/tenants/{tid}). This allows existing resource method calls
// (CreateVNet, ListVNets, etc.) to transparently route to the project-scoped
// URL after the M2.5 migration without changing every call site.
func (c *APIClient) tenantPath() string {
	if c.projectID != "" {
		return c.projectPath()
	}
	return c.tenantBasePath()
}

func (c *APIClient) do(ctx context.Context, method, path string, body interface{}) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal body: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

func ErrorBody(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil {
		return e.Error
	}
	return string(b)
}

// ── VNet ─────────────────────────────────────────────────────────────────────

type VNetResponse struct {
	ID           string   `json:"id"`
	TenantID     string   `json:"tenant_id"`
	Name         string   `json:"name"`
	Region       string   `json:"region"`
	AddressSpace []string `json:"address_space"`
	Description  string   `json:"description"`
	Status       string   `json:"status"`
	ProviderType string   `json:"provider_type"`
	Message      string   `json:"message"`
	Warning      string   `json:"warning,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

type CreateVNetRequest struct {
	Name         string   `json:"name"`
	AddressSpace []string `json:"address_space"`
	Region       string   `json:"region"`
	Description  string   `json:"description,omitempty"`
}

type CreateVNetResp struct {
	Resource VNetResponse `json:"resource"`
	Note     string       `json:"note"`
}

func (c *APIClient) CreateVNet(ctx context.Context, req CreateVNetRequest) (CreateVNetResp, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPost, c.tenantPath()+"/vnets", req)
	if err != nil {
		return CreateVNetResp{}, b, status, err
	}
	var resp CreateVNetResp
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) GetVNet(ctx context.Context, id string) (VNetResponse, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/vnets/"+id, nil)
	var resp VNetResponse
	_ = json.Unmarshal(b, &resp)
	return resp, status, err
}

func (c *APIClient) ListVNets(ctx context.Context) ([]VNetResponse, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/vnets", nil)
	var resp []VNetResponse
	_ = json.Unmarshal(b, &resp)
	return resp, status, err
}

// ListVNetsRaw returns the raw response body alongside the status code. Use
// this when the test needs to inspect the body of a non-200 response (e.g. a
// 403 error payload) where the JSON cannot be decoded into []VNetResponse.
func (c *APIClient) ListVNetsRaw(ctx context.Context) ([]byte, int, error) {
	return c.do(ctx, http.MethodGet, c.tenantPath()+"/vnets", nil)
}

func (c *APIClient) DeleteVNet(ctx context.Context, id string) ([]byte, int, error) {
	return c.do(ctx, http.MethodDelete, c.tenantPath()+"/vnets/"+id, nil)
}

// ── Subnet ───────────────────────────────────────────────────────────────────

type SubnetResponse struct {
	ID           string `json:"id"`
	VNetID       string `json:"vnet_id"`
	TenantID     string `json:"tenant_id"`
	Name         string `json:"name"`
	CIDR         string `json:"cidr"`
	Gateway      string `json:"gateway"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	ProviderType string `json:"provider_type"`
	Message      string `json:"message"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type CreateSubnetRequest struct {
	Name        string `json:"name"`
	CIDR        string `json:"cidr"`
	Gateway     string `json:"gateway,omitempty"`
	Description string `json:"description,omitempty"`
}

type CreateSubnetResp struct {
	Resource SubnetResponse `json:"resource"`
	Note     string         `json:"note"`
}

func (c *APIClient) CreateSubnet(ctx context.Context, vnetID string, req CreateSubnetRequest) (CreateSubnetResp, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPost, c.tenantPath()+"/vnets/"+vnetID+"/subnets", req)
	if err != nil {
		return CreateSubnetResp{}, b, status, err
	}
	var resp CreateSubnetResp
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) GetSubnet(ctx context.Context, vnetID, subnetID string) (SubnetResponse, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/vnets/"+vnetID+"/subnets/"+subnetID, nil)
	var resp SubnetResponse
	_ = json.Unmarshal(b, &resp)
	return resp, status, err
}

func (c *APIClient) DeleteSubnet(ctx context.Context, vnetID, subnetID string) ([]byte, int, error) {
	body, status, err := c.do(ctx, http.MethodDelete, c.tenantPath()+"/vnets/"+vnetID+"/subnets/"+subnetID, nil)
	return body, status, err
}

// ── RouteTable ───────────────────────────────────────────────────────────────

type RouteRuleDTO struct {
	Name            string `json:"name"`
	DestinationCIDR string `json:"destination_cidr"`
	NextHopType     string `json:"next_hop_type"`
	NextHopIP       string `json:"next_hop_ip,omitempty"`
}

type RouteTableResponse struct {
	ID           string         `json:"id"`
	VNetID       string         `json:"vnet_id"`
	TenantID     string         `json:"tenant_id"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Routes       []RouteRuleDTO `json:"routes"`
	Status       string         `json:"status"`
	ProviderType string         `json:"provider_type"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
}

type CreateRouteTableRequest struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Routes      []RouteRuleDTO `json:"routes,omitempty"`
}

func (c *APIClient) CreateRouteTable(ctx context.Context, vnetID string, req CreateRouteTableRequest) (RouteTableResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPost, c.tenantPath()+"/vnets/"+vnetID+"/route-tables", req)
	if err != nil {
		return RouteTableResponse{}, b, status, err
	}
	var resp RouteTableResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) GetRouteTable(ctx context.Context, vnetID, rtID string) (RouteTableResponse, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/vnets/"+vnetID+"/route-tables/"+rtID, nil)
	var resp RouteTableResponse
	_ = json.Unmarshal(b, &resp)
	return resp, status, err
}

func (c *APIClient) UpdateRouteTableRoutes(ctx context.Context, vnetID, rtID string, routes []RouteRuleDTO) (RouteTableResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPut, c.tenantPath()+"/vnets/"+vnetID+"/route-tables/"+rtID, map[string]interface{}{"routes": routes})
	if err != nil {
		return RouteTableResponse{}, b, status, err
	}
	var resp RouteTableResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) DeleteRouteTable(ctx context.Context, vnetID, rtID string) (int, error) {
	_, status, err := c.do(ctx, http.MethodDelete, c.tenantPath()+"/vnets/"+vnetID+"/route-tables/"+rtID, nil)
	return status, err
}

func (c *APIClient) AssociateRouteTable(ctx context.Context, vnetID, rtID, subnetID string) (map[string]interface{}, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPost, c.tenantPath()+"/vnets/"+vnetID+"/route-tables/"+rtID+"/associations", map[string]string{"subnet_id": subnetID})
	if err != nil {
		return nil, b, status, err
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// ── NSG ──────────────────────────────────────────────────────────────────────

type NSGRuleDTO struct {
	Name                     string `json:"name"`
	Direction                string `json:"direction"`
	Priority                 int    `json:"priority"`
	Protocol                 string `json:"protocol"`
	SourceAddressPrefix      string `json:"source_address_prefix"`
	SourcePortRange          string `json:"source_port_range"`
	DestinationAddressPrefix string `json:"destination_address_prefix"`
	DestinationPortRange     string `json:"destination_port_range"`
	Action                   string `json:"action"`
}

type NSGAttachmentResponse struct {
	ID         string `json:"id"`
	SGiD       string `json:"sg_id"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	CreatedAt  string `json:"created_at"`
}

type NSGResponse struct {
	ID           string                  `json:"id"`
	TenantID     string                  `json:"tenant_id"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description"`
	Rules        []NSGRuleDTO            `json:"rules"`
	Attachments  []NSGAttachmentResponse `json:"attachments"`
	Status       string                  `json:"status"`
	ProviderType string                  `json:"provider_type"`
	CreatedAt    string                  `json:"created_at"`
	UpdatedAt    string                  `json:"updated_at"`
}

type CreateNSGRequest struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Rules       []NSGRuleDTO `json:"rules,omitempty"`
}

func (c *APIClient) CreateNSG(ctx context.Context, req CreateNSGRequest) (NSGResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPost, c.tenantPath()+"/security-groups", req)
	if err != nil {
		return NSGResponse{}, b, status, err
	}
	var resp NSGResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) GetNSG(ctx context.Context, sgID string) (NSGResponse, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/security-groups/"+sgID, nil)
	var resp NSGResponse
	_ = json.Unmarshal(b, &resp)
	return resp, status, err
}

func (c *APIClient) UpdateNSGRules(ctx context.Context, sgID string, rules []NSGRuleDTO) (NSGResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPut, c.tenantPath()+"/security-groups/"+sgID+"/rules", map[string]interface{}{"rules": rules})
	if err != nil {
		return NSGResponse{}, b, status, err
	}
	var resp NSGResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) AttachNSG(ctx context.Context, sgID string, req map[string]string) (NSGAttachmentResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPost, c.tenantPath()+"/security-groups/"+sgID+"/attachments", req)
	if err != nil {
		return NSGAttachmentResponse{}, b, status, err
	}
	var resp NSGAttachmentResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) DetachNSG(ctx context.Context, sgID, attID string) (int, error) {
	_, status, err := c.do(ctx, http.MethodDelete, c.tenantPath()+"/security-groups/"+sgID+"/attachments/"+attID, nil)
	return status, err
}

func (c *APIClient) DeleteNSG(ctx context.Context, sgID string) (int, error) {
	_, status, err := c.do(ctx, http.MethodDelete, c.tenantPath()+"/security-groups/"+sgID, nil)
	return status, err
}

// ── Peering ──────────────────────────────────────────────────────────────────

type PeeringResponse struct {
	ID                    string `json:"id"`
	VNetID                string `json:"vnet_id"`
	PeerVNetID            string `json:"peer_vnet_id"`
	TenantID              string `json:"tenant_id"`
	Name                  string `json:"name"`
	AllowForwardedTraffic bool   `json:"allow_forwarded_traffic"`
	Status                string `json:"status"`
	ProviderType          string `json:"provider_type"`
	Message               string `json:"message"`
	Warning               string `json:"warning"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
}

type CreatePeeringResp struct {
	Resource PeeringResponse `json:"resource"`
	Note     string          `json:"note"`
}

func (c *APIClient) CreatePeering(ctx context.Context, vnetID string, req map[string]interface{}) (CreatePeeringResp, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPost, c.tenantPath()+"/vnets/"+vnetID+"/peerings", req)
	if err != nil {
		return CreatePeeringResp{}, b, status, err
	}
	var resp CreatePeeringResp
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) GetPeering(ctx context.Context, vnetID, peeringID string) (PeeringResponse, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/vnets/"+vnetID+"/peerings/"+peeringID, nil)
	var resp PeeringResponse
	_ = json.Unmarshal(b, &resp)
	return resp, status, err
}

func (c *APIClient) DeletePeering(ctx context.Context, vnetID, peeringID string) (int, error) {
	_, status, err := c.do(ctx, http.MethodDelete, c.tenantPath()+"/vnets/"+vnetID+"/peerings/"+peeringID, nil)
	return status, err
}

func (c *APIClient) ListPeerings(ctx context.Context, vnetID string) ([]PeeringResponse, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/vnets/"+vnetID+"/peerings", nil)
	if err != nil {
		return nil, status, err
	}
	var resp []PeeringResponse
	_ = json.Unmarshal(b, &resp)
	return resp, status, nil
}

// ── DNS Zone ─────────────────────────────────────────────────────────────────

type DNSZoneResponse struct {
	ID           string `json:"id"`
	VNetID       string `json:"vnet_id"`
	TenantID     string `json:"tenant_id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	ProviderType string `json:"provider_type"`
	Message      string `json:"message"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type CreateDNSZoneResp struct {
	Resource DNSZoneResponse `json:"resource"`
	Note     string          `json:"note"`
}

func (c *APIClient) CreateDNSZone(ctx context.Context, vnetID string, req map[string]string) (CreateDNSZoneResp, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPost, c.tenantPath()+"/vnets/"+vnetID+"/dns-zones", req)
	if err != nil {
		return CreateDNSZoneResp{}, b, status, err
	}
	var resp CreateDNSZoneResp
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) GetDNSZone(ctx context.Context, vnetID, zoneID string) (DNSZoneResponse, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/vnets/"+vnetID+"/dns-zones/"+zoneID, nil)
	var resp DNSZoneResponse
	_ = json.Unmarshal(b, &resp)
	return resp, status, err
}

func (c *APIClient) DeleteDNSZone(ctx context.Context, vnetID, zoneID string) (int, error) {
	_, status, err := c.do(ctx, http.MethodDelete, c.tenantPath()+"/vnets/"+vnetID+"/dns-zones/"+zoneID, nil)
	return status, err
}

type DNSRecordResponse struct {
	ID        string   `json:"id"`
	ZoneID    string   `json:"zone_id"`
	TenantID  string   `json:"tenant_id"`
	Type      string   `json:"type"`
	Name      string   `json:"name"`
	Values    []string `json:"values"`
	TTL       int      `json:"ttl"`
	CreatedAt string   `json:"created_at"`
}

func (c *APIClient) CreateDNSRecord(ctx context.Context, vnetID, zoneID string, req map[string]interface{}) (DNSRecordResponse, []byte, int, error) {
	path := fmt.Sprintf("%s/vnets/%s/dns-zones/%s/records", c.tenantPath(), vnetID, zoneID)
	b, status, err := c.do(ctx, http.MethodPost, path, req)
	if err != nil {
		return DNSRecordResponse{}, b, status, err
	}
	var resp DNSRecordResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) DeleteDNSRecord(ctx context.Context, vnetID, zoneID, recordID string) (int, error) {
	path := fmt.Sprintf("%s/vnets/%s/dns-zones/%s/records/%s", c.tenantPath(), vnetID, zoneID, recordID)
	_, status, err := c.do(ctx, http.MethodDelete, path, nil)
	return status, err
}

// ── VirtualMachine ───────────────────────────────────────────────────────────

// VMResponse mirrors handlers.VMResponse.
type VMResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Size         string `json:"size"`
	Status       string `json:"status"`
	TenantID     string `json:"tenant_id"`
	ProviderType string `json:"provider_type"`
	IPAddress    string `json:"ip_address,omitempty"`
	Message      string `json:"message,omitempty"`
	CreatedAt    string `json:"created_at"`
}

// CreateVMRequest mirrors the JSON shape of handlers.CreateVMRequest.
// For the VPC path set VNetID + SubnetID; leave NetworkName empty.
// For the legacy bridge path set NetworkName; leave VNetID + SubnetID empty.
type CreateVMRequest struct {
	Name        string `json:"name"`
	Size        string `json:"size"`
	DiskGB      int    `json:"disk_gb,omitempty"`
	ImageName   string `json:"image_name"`
	NetworkName string `json:"network_name,omitempty"`
	VNetID      string `json:"vnet_id,omitempty"`
	SubnetID    string `json:"subnet_id,omitempty"`
}

// CreateVMResp is the 202 response body from POST /v1/virtual-machines.
type CreateVMResp struct {
	Resource        VMResponse `json:"resource"`
	PrivateKey      string     `json:"private_key"`
	ConsolePassword string     `json:"console_password"`
	Note            string     `json:"note"`
}

func (c *APIClient) CreateVM(ctx context.Context, req CreateVMRequest) (CreateVMResp, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPost, c.tenantPath()+"/virtual-machines", req)
	if err != nil {
		return CreateVMResp{}, b, status, err
	}
	var resp CreateVMResp
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) GetVM(ctx context.Context, vmID string) (VMResponse, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/virtual-machines/"+vmID, nil)
	var resp VMResponse
	_ = json.Unmarshal(b, &resp)
	return resp, status, err
}

func (c *APIClient) DeleteVM(ctx context.Context, vmID string) (int, error) {
	_, status, err := c.do(ctx, http.MethodDelete, c.tenantPath()+"/virtual-machines/"+vmID, nil)
	return status, err
}

// ── Projects ──────────────────────────────────────────────────────────────────

// ProjectResponse mirrors handlers.projectResponse.
type ProjectResponse struct {
	ID           string `json:"id"`
	TenantID     string `json:"tenant_id"`
	ProjectUUID  string `json:"project_uuid"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	CPUCores     int    `json:"cpu_cores"`
	MemoryGB     int    `json:"memory_gb"`
	StorageGB    int    `json:"storage_gb"`
	MaxVNets     int    `json:"max_vnets"`
	MaxClusters  int    `json:"max_clusters"`
	MaxVolumes   int    `json:"max_volumes"`
	MaxPublicIPs int    `json:"max_public_ips"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	CreatedBy    string `json:"created_by"`
}

// CreateProjectRequest mirrors handlers.createProjectRequest.
type CreateProjectRequest struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Description  string `json:"description,omitempty"`
	CPUCores     int    `json:"cpu_cores,omitempty"`
	MemoryGB     int    `json:"memory_gb,omitempty"`
	StorageGB    int    `json:"storage_gb,omitempty"`
	MaxVNets     int    `json:"max_vnets,omitempty"`
	MaxClusters  int    `json:"max_clusters,omitempty"`
	MaxVolumes   int    `json:"max_volumes,omitempty"`
	MaxPublicIPs int    `json:"max_public_ips,omitempty"`
}

// PatchProjectRequest is the PATCH body for /v1/tenants/{tid}/projects/{pid}.
type PatchProjectRequest struct {
	CPUCores  *int `json:"cpu_cores,omitempty"`
	MemoryGB  *int `json:"memory_gb,omitempty"`
	StorageGB *int `json:"storage_gb,omitempty"`
}

// CreateProject calls POST /v1/tenants/{tenant_id}/projects.
func (c *APIClient) CreateProject(ctx context.Context, req CreateProjectRequest) (ProjectResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPost, c.tenantBasePath()+"/projects", req)
	if err != nil {
		return ProjectResponse{}, b, status, err
	}
	var resp ProjectResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// GetProject calls GET /v1/tenants/{tenant_id}/projects/{project_id}.
func (c *APIClient) GetProject(ctx context.Context, projectID string) (ProjectResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantBasePath()+"/projects/"+projectID, nil)
	if err != nil {
		return ProjectResponse{}, b, status, err
	}
	var resp ProjectResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// ListProjects calls GET /v1/tenants/{tenant_id}/projects.
func (c *APIClient) ListProjects(ctx context.Context) ([]ProjectResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantBasePath()+"/projects", nil)
	if err != nil {
		return nil, b, status, err
	}
	var resp []ProjectResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// PatchProject calls PATCH /v1/tenants/{tenant_id}/projects/{project_id}.
func (c *APIClient) PatchProject(ctx context.Context, projectID string, req PatchProjectRequest) (ProjectResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPatch, c.tenantBasePath()+"/projects/"+projectID, req)
	if err != nil {
		return ProjectResponse{}, b, status, err
	}
	var resp ProjectResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// DeleteProject calls DELETE /v1/tenants/{tenant_id}/projects/{project_id}.
func (c *APIClient) DeleteProject(ctx context.Context, projectID string) ([]byte, int, error) {
	return c.do(ctx, http.MethodDelete, c.tenantBasePath()+"/projects/"+projectID, nil)
}

// ── Tenant Cap Usage ─────────────────────────────────────────────────────────

// TenantCapUsageResponse mirrors models.TenantCapUsage as returned by GET /cap-usage.
type TenantCapUsageResponse struct {
	Cap       TenantCapDTO `json:"cap"`
	Allocated TenantCapDTO `json:"allocated"`
	Available TenantCapDTO `json:"available"`
}

// TenantCapDTO mirrors models.TenantCap.
type TenantCapDTO struct {
	CPUCores  int `json:"cpu_cores"`
	MemoryGB  int `json:"memory_gb"`
	StorageGB int `json:"storage_gb"`
}

// GetTenantCapUsage calls GET /v1/tenants/{tenant_id}/cap-usage.
func (c *APIClient) GetTenantCapUsage(ctx context.Context) (TenantCapUsageResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantBasePath()+"/cap-usage", nil)
	if err != nil {
		return TenantCapUsageResponse{}, b, status, err
	}
	var resp TenantCapUsageResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// PatchAdminTenantCap calls PATCH /v1/admin/tenants/{tenant_id} to update
// a tenant's capacity ceiling. Admin-only endpoint. Fields are pointers so
// omitted dimensions keep their current value.
type PatchTenantCapRequest struct {
	CPUCoresCap  *int `json:"cpu_cores_cap,omitempty"`
	MemoryGBCap  *int `json:"memory_gb_cap,omitempty"`
	StorageGBCap *int `json:"storage_gb_cap,omitempty"`
}

func (c *APIClient) PatchAdminTenantCap(ctx context.Context, req PatchTenantCapRequest) ([]byte, int, error) {
	return c.do(ctx, http.MethodPatch, "/v1/admin/tenants/"+c.tenantID, req)
}

// ── Members ───────────────────────────────────────────────────────────────────

// MemberResponse is the JSON shape returned by the members endpoints.
type MemberResponse struct {
	ID             string `json:"id"`
	PrincipalType  string `json:"principal_type"`
	PrincipalID    string `json:"principal_id"`
	ScopeType      string `json:"scope_type"`
	ScopeID        string `json:"scope_id"`
	RoleDefinition string `json:"role_definition"`
	GrantedAt      string `json:"granted_at"`
	GrantedBy      string `json:"granted_by"`
	DisplayAlias   string `json:"display_alias,omitempty"`
}

// ListMembersResponse wraps the role-assignments list array.
type ListMembersResponse struct {
	Members []MemberResponse `json:"role_assignments"`
}

// InviteMember calls POST /v1/tenants/{tenant_id}/role-assignments.
// Returns the created MemberResponse, the raw body, and the HTTP status code.
func (c *APIClient) InviteMember(ctx context.Context, tenantID, userSub, roleDefinition string) (MemberResponse, []byte, int, error) {
	return c.InviteMemberWithAlias(ctx, tenantID, userSub, roleDefinition, "")
}

// InviteMemberWithAlias is InviteMember with the Option D display_alias
// field set. Empty alias produces the same body InviteMember sends.
func (c *APIClient) InviteMemberWithAlias(ctx context.Context, tenantID, userSub, roleDefinition, displayAlias string) (MemberResponse, []byte, int, error) {
	path := fmt.Sprintf("/v1/tenants/%s/role-assignments", tenantID)
	body := map[string]string{"user_sub": userSub, "role_definition": roleDefinition}
	if displayAlias != "" {
		body["display_alias"] = displayAlias
	}
	b, status, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return MemberResponse{}, b, status, err
	}
	var resp MemberResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// ListMembers calls GET /v1/tenants/{tenant_id}/role-assignments.
// Returns the parsed response, raw body, and HTTP status code.
func (c *APIClient) ListMembers(ctx context.Context, tenantID string) (ListMembersResponse, int, error) {
	path := fmt.Sprintf("/v1/tenants/%s/role-assignments", tenantID)
	b, status, err := c.do(ctx, http.MethodGet, path, nil)
	var resp ListMembersResponse
	_ = json.Unmarshal(b, &resp)
	return resp, status, err
}

// RemoveMember calls DELETE /v1/tenants/{tenant_id}/role-assignments/{principal_id}.
// Returns the raw body and HTTP status code.
func (c *APIClient) RemoveMember(ctx context.Context, tenantID, principalID string) ([]byte, int, error) {
	path := fmt.Sprintf("/v1/tenants/%s/role-assignments/%s", tenantID, principalID)
	return c.do(ctx, http.MethodDelete, path, nil)
}

// CreateProjectRoleAssignment calls POST
// /v1/tenants/{tenant_id}/projects/{project_id}/role-assignments — granting a
// role at PROJECT scope. Returns the created MemberResponse, raw body, and status.
func (c *APIClient) CreateProjectRoleAssignment(ctx context.Context, tenantID, projectID, userSub, roleDefinition string) (MemberResponse, []byte, int, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/role-assignments", tenantID, projectID)
	body := map[string]string{"user_sub": userSub, "role_definition": roleDefinition}
	b, status, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return MemberResponse{}, b, status, err
	}
	var resp MemberResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// ListProjectRoleAssignments calls GET
// /v1/tenants/{tenant_id}/projects/{project_id}/role-assignments.
func (c *APIClient) ListProjectRoleAssignments(ctx context.Context, tenantID, projectID string) (ListMembersResponse, int, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/role-assignments", tenantID, projectID)
	b, status, err := c.do(ctx, http.MethodGet, path, nil)
	var resp ListMembersResponse
	_ = json.Unmarshal(b, &resp)
	return resp, status, err
}

// ── Service Accounts ─────────────────────────────────────────────────────────

// CreateServiceAccountResponse is the JSON shape returned by POST
// /v1/tenants/{tenant_id}/service-accounts. It includes the raw token, which
// is returned exactly once — never on subsequent GET/LIST calls.
type CreateServiceAccountResponse struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at"`
	// Token carries the raw dcapi_sa_<lookup_id>_<secret> bearer token.
	// Save it immediately — this is the only time it is returned.
	Token string `json:"token"`
}

// ServiceAccountResponse is the JSON shape for GET / LIST responses.
// token and token_hash are intentionally absent.
type ServiceAccountResponse struct {
	ID          string  `json:"id"`
	TenantID    string  `json:"tenant_id"`
	Name        string  `json:"name"`
	Role        string  `json:"role"`
	Description string  `json:"description,omitempty"`
	CreatedAt   string  `json:"created_at"`
	LastUsed    *string `json:"last_used,omitempty"`
}

// ListServiceAccountsResponse wraps the service_accounts list array.
type ListServiceAccountsResponse struct {
	ServiceAccounts []ServiceAccountResponse `json:"service_accounts"`
}

// CreateServiceAccount calls POST .../service-accounts on the project-scoped
// path derived from the client's bound tenant and project. The tenantID
// parameter is kept for the cross-tenant test case — when it differs from the
// client's bound tenant the call deliberately targets a foreign namespace.
// For normal use pass c.tenantID (or let ownerClient derive it automatically).
//
// Path: /v1/tenants/{tenantID}/projects/{defaultProjectID}/service-accounts
func (c *APIClient) CreateServiceAccount(ctx context.Context, tenantID, name, role, description string) (CreateServiceAccountResponse, []byte, int, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/service-accounts", tenantID, defaultProjectID)
	body := map[string]string{"name": name, "role": role, "description": description}
	b, status, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return CreateServiceAccountResponse{}, b, status, err
	}
	var resp CreateServiceAccountResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// GetServiceAccount calls GET .../service-accounts/{sa_id} on the
// project-scoped path. tenantID kept for cross-tenant test coverage.
//
// Path: /v1/tenants/{tenantID}/projects/{defaultProjectID}/service-accounts/{sa_id}
func (c *APIClient) GetServiceAccount(ctx context.Context, tenantID, saID string) (ServiceAccountResponse, []byte, int, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/service-accounts/%s", tenantID, defaultProjectID, saID)
	b, status, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return ServiceAccountResponse{}, b, status, err
	}
	var resp ServiceAccountResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// ListServiceAccounts calls GET .../service-accounts on the project-scoped
// path. tenantID kept for cross-tenant test coverage.
//
// Path: /v1/tenants/{tenantID}/projects/{defaultProjectID}/service-accounts
func (c *APIClient) ListServiceAccounts(ctx context.Context, tenantID string) (ListServiceAccountsResponse, []byte, int, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/service-accounts", tenantID, defaultProjectID)
	b, status, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return ListServiceAccountsResponse{}, b, status, err
	}
	var resp ListServiceAccountsResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// DeleteServiceAccount calls DELETE .../service-accounts/{sa_id} on the
// project-scoped path. tenantID kept for cross-tenant test coverage.
//
// Path: /v1/tenants/{tenantID}/projects/{defaultProjectID}/service-accounts/{sa_id}
func (c *APIClient) DeleteServiceAccount(ctx context.Context, tenantID, saID string) ([]byte, int, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/service-accounts/%s", tenantID, defaultProjectID, saID)
	return c.do(ctx, http.MethodDelete, path, nil)
}

// ── Tenants ─────────────────────────────────────────────────────────────────

// TenantSummary mirrors #/components/schemas/TenantSummary in openapi.yaml.
type TenantSummary struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}

// ListTenants calls GET /v1/tenants and returns the list of tenants the
// authenticated principal has access to.
func (c *APIClient) ListTenants(ctx context.Context) ([]TenantSummary, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, "/v1/tenants", nil)
	if err != nil {
		return nil, b, status, err
	}
	var resp []TenantSummary
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// ── Key Vaults (M3 chunk 1) ──────────────────────────────────────────────────

// CreateKeyVaultRequest mirrors the JSON shape of handlers.createKeyVaultRequest.
type CreateKeyVaultRequest struct {
	Name           string `json:"name"`
	SoftDeleteDays int    `json:"soft_delete_days,omitempty"`
}

// KeyVaultResponse mirrors the keyVaultResponse handler shape.
type KeyVaultResponse struct {
	ID             string `json:"id"`
	TenantID       string `json:"tenant_id"`
	Name           string `json:"name"`
	SoftDeleteDays int    `json:"soft_delete_days"`
	Status         string `json:"status"`
	Message        string `json:"message,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}


func (c *APIClient) CreateKeyVault(ctx context.Context, req CreateKeyVaultRequest) (KeyVaultResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPost, c.tenantPath()+"/keyvaults", req)
	if err != nil {
		return KeyVaultResponse{}, b, status, err
	}
	var resp KeyVaultResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) GetKeyVault(ctx context.Context, id string) (KeyVaultResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/keyvaults/"+id, nil)
	if err != nil {
		return KeyVaultResponse{}, b, status, err
	}
	var resp KeyVaultResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) ListKeyVaults(ctx context.Context) ([]KeyVaultResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/keyvaults", nil)
	if err != nil {
		return nil, b, status, err
	}
	var resp []KeyVaultResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) DeleteKeyVault(ctx context.Context, id string) ([]byte, int, error) {
	return c.do(ctx, http.MethodDelete, c.tenantPath()+"/keyvaults/"+id, nil)
}

// ── Key Vault Private Endpoints (M3 chunk 2) ─────────────────────────────────

type CreatePrivateEndpointRequest struct {
	Name     string `json:"name"`
	VNetID   string `json:"vnet_id"`
	SubnetID string `json:"subnet_id"`
}

type PrivateEndpointResponse struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	VNetID     string `json:"vnet_id"`
	SubnetID   string `json:"subnet_id"`
	Name       string `json:"name"`
	IPAddress  string `json:"ip_address,omitempty"`
	Hostname   string `json:"hostname,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func (c *APIClient) CreateKeyVaultPrivateEndpoint(ctx context.Context, kvID string, req CreatePrivateEndpointRequest) (PrivateEndpointResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPost, c.tenantPath()+"/keyvaults/"+kvID+"/private-endpoints", req)
	if err != nil {
		return PrivateEndpointResponse{}, b, status, err
	}
	var resp PrivateEndpointResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) GetKeyVaultPrivateEndpoint(ctx context.Context, kvID, epID string) (PrivateEndpointResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/keyvaults/"+kvID+"/private-endpoints/"+epID, nil)
	if err != nil {
		return PrivateEndpointResponse{}, b, status, err
	}
	var resp PrivateEndpointResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) ListKeyVaultPrivateEndpoints(ctx context.Context, kvID string) ([]PrivateEndpointResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodGet, c.tenantPath()+"/keyvaults/"+kvID+"/private-endpoints", nil)
	if err != nil {
		return nil, b, status, err
	}
	var resp []PrivateEndpointResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

func (c *APIClient) DeleteKeyVaultPrivateEndpoint(ctx context.Context, kvID, epID string) ([]byte, int, error) {
	return c.do(ctx, http.MethodDelete, c.tenantPath()+"/keyvaults/"+kvID+"/private-endpoints/"+epID, nil)
}

// ── Key Vault Secrets (M3 chunk 3) ───────────────────────────────────────────

// KeyVaultSecretSummary mirrors handlers.keyVaultSecretSummary.
type KeyVaultSecretSummary struct {
	Name          string  `json:"name"`
	LatestVersion int     `json:"latest_version"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
	DeletedAt     *string `json:"deleted_at,omitempty"`
}

// KeyVaultSecretListResponse mirrors handlers.keyVaultSecretListResponse.
type KeyVaultSecretListResponse struct {
	Items      []KeyVaultSecretSummary `json:"items"`
	NextCursor *string                 `json:"next_cursor,omitempty"`
	TotalCount int                     `json:"total_count"`
}

// KeyVaultSecretResponse mirrors handlers.keyVaultSecretResponse.
type KeyVaultSecretResponse struct {
	Key       string            `json:"key"`
	Value     string            `json:"value"`
	Version   int               `json:"version"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt string            `json:"created_at"`
	DeletedAt *string           `json:"deleted_at,omitempty"`
}

// PutKeyVaultSecretRequest mirrors handlers.putKeyVaultSecretRequest.
type PutKeyVaultSecretRequest struct {
	Value    string            `json:"value"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ListKeyVaultSecrets calls GET .../keyvaults/{id}/secrets with optional
// cursor and limit query params. Pass empty cursor to start from the beginning.
func (c *APIClient) ListKeyVaultSecrets(ctx context.Context, kvID, cursor string, limit int) (KeyVaultSecretListResponse, []byte, int, error) {
	path := c.tenantPath() + "/keyvaults/" + kvID + "/secrets"
	sep := "?"
	if cursor != "" {
		path += sep + "cursor=" + cursor
		sep = "&"
	}
	if limit > 0 {
		path += sep + "limit=" + fmt.Sprintf("%d", limit)
	}
	b, status, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return KeyVaultSecretListResponse{}, b, status, err
	}
	var resp KeyVaultSecretListResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// GetKeyVaultSecret calls GET .../keyvaults/{id}/secrets/{key}.
// Pass version=0 to get the latest; pass version>0 to get a specific version.
func (c *APIClient) GetKeyVaultSecret(ctx context.Context, kvID, key string, version int) (KeyVaultSecretResponse, []byte, int, error) {
	path := c.tenantPath() + "/keyvaults/" + kvID + "/secrets/" + key
	if version > 0 {
		path += fmt.Sprintf("?version=%d", version)
	}
	b, status, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return KeyVaultSecretResponse{}, b, status, err
	}
	var resp KeyVaultSecretResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// PutKeyVaultSecret calls PUT .../keyvaults/{id}/secrets/{key}.
func (c *APIClient) PutKeyVaultSecret(ctx context.Context, kvID, key string, req PutKeyVaultSecretRequest) (KeyVaultSecretResponse, []byte, int, error) {
	b, status, err := c.do(ctx, http.MethodPut, c.tenantPath()+"/keyvaults/"+kvID+"/secrets/"+key, req)
	if err != nil {
		return KeyVaultSecretResponse{}, b, status, err
	}
	var resp KeyVaultSecretResponse
	_ = json.Unmarshal(b, &resp)
	return resp, b, status, nil
}

// DeleteKeyVaultSecret calls DELETE .../keyvaults/{id}/secrets/{key}.
func (c *APIClient) DeleteKeyVaultSecret(ctx context.Context, kvID, key string) ([]byte, int, error) {
	return c.do(ctx, http.MethodDelete, c.tenantPath()+"/keyvaults/"+kvID+"/secrets/"+key, nil)
}
