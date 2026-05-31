// Package handlers — subnet.go
//
// SubnetHandler implements /v1/vnets/{vnet_id}/subnets endpoints.
// Subnets are async (202 on create/delete). A subnet must be within the parent
// VNet's address_space and must not overlap siblings (checked before KubeOVN).
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/providers/common"
	"github.com/wso2/dc-api/internal/providers/kubeovn"
	"github.com/wso2/dc-api/internal/rbac"
)

// SubnetHandler handles all /v1/vnets/{vnet_id}/subnets endpoints.
type SubnetHandler struct {
	repo     *db.Repository
	provider providers.NetworkProvider
	nat      providers.VPCNATProvisioner  // nil = F15 disabled (no SNAT provisioning)
	dns      providers.VPCDNSProvisioner  // nil = F20 disabled (no per-VPC DNS)
	log      zerolog.Logger
}

// NewSubnetHandler creates a SubnetHandler with injected dependencies.
// nat and dns may be nil — when nil, the respective provisioning steps are skipped.
func NewSubnetHandler(repo *db.Repository, provider providers.NetworkProvider, nat providers.VPCNATProvisioner, dns providers.VPCDNSProvisioner, log zerolog.Logger) *SubnetHandler {
	return &SubnetHandler{repo: repo, provider: provider, nat: nat, dns: dns, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type createSubnetRequest struct {
	Name        string `json:"name"`
	CIDR        string `json:"cidr"`
	Gateway     string `json:"gateway"`
	Description string `json:"description"`
}

type subnetResponse struct {
	ID           string `json:"id"`
	VNetID       string `json:"vnet_id"`
	TenantID     string `json:"tenant_id"`
	Name         string `json:"name"`
	CIDR         string `json:"cidr"`
	Gateway      string `json:"gateway,omitempty"`
	Description  string `json:"description,omitempty"`
	Status       string `json:"status"`
	ProviderType string `json:"provider_type"`
	Message      string `json:"message,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func subnetToResponse(s *models.Subnet) subnetResponse {
	return subnetResponse{
		ID:           s.ID.String(),
		VNetID:       s.VNetID.String(),
		TenantID:     s.TenantID,
		Name:         s.Name,
		CIDR:         s.CIDR,
		Gateway:      s.Gateway,
		Description:  s.Description,
		Status:       string(s.Status),
		ProviderType: s.ProviderType,
		Message:      s.Message,
		CreatedAt:    s.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    s.UpdatedAt.Format(time.RFC3339),
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Create handles POST /v1/vnets/{vnet_id}/subnets.
func (h *SubnetHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionSubnetWrite) {
		return
	}
	userID, _ := middleware.UserFromContext(r.Context())

	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectID, _ := middleware.ProjectFromContext(r.Context())
	_, projectUUID, okP := lookupProjectUUID(w, r)
	if !okP {
		return
	}

	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}

	// Fetch parent VNet.
	vnet, err := h.repo.GetVNet(r.Context(), vnetID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}
	if vnet.Status != models.StatusActive {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("VNet is %s — wait for it to become ACTIVE before adding subnets", vnet.Status))
		return
	}

	var req createSubnetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := validateResourceName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.CIDR == "" {
		writeError(w, http.StatusBadRequest, "cidr is required")
		return
	}

	// Load region reserved CIDRs for subnet CIDR validation.
	rawReserved, err := h.repo.GetRegionReservedCIDRs(r.Context(), vnet.Region)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load reserved CIDRs")
		return
	}
	reserved, _ := ParseReservedCIDRs(rawReserved)

	// Validate CIDR: valid, RFC1918, /8-/28, not reserved.
	if err := validateRFC1918CIDR(req.CIDR, 8, 28); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := checkNotReserved(req.CIDR, reserved); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Subnet CIDR must be contained in the parent VNet's address space.
	if err := cidrContainedInAny(req.CIDR, vnet.AddressSpace); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Sibling overlap check (DB query for existing subnet CIDRs in this VNet).
	siblings, err := h.repo.ListActiveSubnetCIDRsByVNet(r.Context(), vnetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check CIDR overlap")
		return
	}
	if err := checkNoSiblingOverlap(req.CIDR, siblings); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Gateway default.
	gw := req.Gateway
	if gw == "" {
		gw = defaultGateway(req.CIDR)
	} else {
		if err := gatewayInCIDR(gw, req.CIDR); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Quota check.
	quota, err := h.repo.GetNetworkQuota(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quota check failed")
		return
	}
	subnetCount, err := h.repo.CountSubnetsByVNet(r.Context(), vnetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quota check failed")
		return
	}
	if subnetCount >= quota.MaxSubnetsPerVNet {
		writeError(w, http.StatusForbidden,
			fmt.Sprintf("subnet quota exceeded: %d/%d subnets in VNet", subnetCount, quota.MaxSubnetsPerVNet))
		return
	}

	subnet := &models.Subnet{
		VNetID:       vnetID,
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    projectID,
		ProjectUUID:  projectUUID,
		Name:         req.Name,
		CIDR:         req.CIDR,
		Gateway:      gw,
		Description:  req.Description,
		Status:       models.StatusPending,
		ProviderType: h.provider.Name(),
	}
	subnet, err = h.repo.CreateSubnet(r.Context(), subnet)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("create subnet in DB")
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, fmt.Sprintf("a subnet named %q already exists in this VNet", req.Name))
			return
		}
		if strings.Contains(err.Error(), "23P01") || strings.Contains(err.Error(), "exclusion") {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("CIDR %s overlaps an existing subnet in this VNet (overlap exclusion constraint)", req.CIDR))
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to register subnet")
		return
	}

	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: subnet.ID, ActorID: userID, Action: "CREATE", ToStatus: models.StatusPending,
	})

	// Async: call KubeOVN CreateSubnet.
	go h.asyncProvisionSubnet(subnet.ID, vnet.ID, tenantID, projectID, userID, vnet.BackendUID, models.SubnetSpec{
		Name:        req.Name,
		CIDR:        req.CIDR,
		Gateway:     gw,
		Description: req.Description,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"resource": subnetToResponse(subnet),
		"note": fmt.Sprintf("Subnet is being provisioned. Poll GET /v1/vnets/%s/subnets/%s for status.",
			vnetID, subnet.ID),
	})
}

// Get handles GET .../vnets/{vnet_id}/subnets/{subnet_id}.
func (h *SubnetHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	_, projectUUID, okP := lookupProjectUUID(w, r)
	if !okP {
		return
	}
	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}
	subnetID, err := uuid.Parse(chi.URLParam(r, "subnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid subnet ID")
		return
	}

	// Verify parent VNet belongs to this tenant+project.
	_, err = h.repo.GetVNet(r.Context(), vnetID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	subnet, err := h.repo.GetSubnet(r.Context(), subnetID)
	if err != nil || subnet.VNetID != vnetID {
		writeError(w, http.StatusNotFound, "subnet not found")
		return
	}
	writeJSON(w, http.StatusOK, subnetToResponse(subnet))
}

// List handles GET .../vnets/{vnet_id}/subnets.
func (h *SubnetHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	_, projectUUID, okP := lookupProjectUUID(w, r)
	if !okP {
		return
	}
	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}

	_, err = h.repo.GetVNet(r.Context(), vnetID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	subnets, err := h.repo.ListSubnetsByVNet(r.Context(), vnetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list subnets")
		return
	}
	resp := make([]subnetResponse, 0, len(subnets))
	for _, s := range subnets {
		resp = append(resp, subnetToResponse(s))
	}
	writeJSON(w, http.StatusOK, resp)
}

// Delete handles DELETE /v1/vnets/{vnet_id}/subnets/{subnet_id}.
func (h *SubnetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionSubnetDelete) {
		return
	}
	userID, _ := middleware.UserFromContext(r.Context())

	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}
	subnetID, err := uuid.Parse(chi.URLParam(r, "subnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid subnet ID")
		return
	}

	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	_, projectUUID, okP := lookupProjectUUID(w, r)
	if !okP {
		return
	}

	vnet, err := h.repo.GetVNet(r.Context(), vnetID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	subnet, err := h.repo.GetSubnet(r.Context(), subnetID)
	if err != nil || subnet.VNetID != vnetID {
		writeError(w, http.StatusNotFound, "subnet not found")
		return
	}

	// Delete guard: reject if any VMs, bastions, or clusters are still
	// attached to this subnet via the resources table's subnet_id column.
	// KubeOVN's subnet finalizer would otherwise refuse to release the OVN
	// logical switch while LSPs are pinned, leaving the subnet stuck in
	// FAILED. This mirrors the VNet → subnets/route-tables/peerings guard.
	attached, err := h.repo.ListResourcesBySubnet(r.Context(), subnetID)
	if err != nil {
		h.log.Error().Err(err).Str("subnet", subnetID.String()).Msg("list resources by subnet")
		writeError(w, http.StatusInternalServerError, "failed to check subnet dependencies")
		return
	}
	if len(attached) > 0 {
		names := make([]string, 0, len(attached))
		for _, a := range attached {
			names = append(names, fmt.Sprintf("%s (%s)", a.Name, a.Type))
		}
		writeError(w, http.StatusConflict,
			fmt.Sprintf("subnet has attached resources; delete them first: %s", strings.Join(names, ", ")))
		return
	}

	if err := h.repo.UpdateSubnetStatus(r.Context(), subnetID, models.StatusDeleting, "deletion requested", ""); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update subnet status")
		return
	}
	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: subnetID, ActorID: userID, Action: "DELETE",
		FromStatus: subnet.Status, ToStatus: models.StatusDeleting,
	})

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if subnet.BackendUID == "" {
			_ = h.repo.DeleteSubnet(ctx, subnetID)
			return
		}

		// Determine if this is the last non-failed/non-deleting subnet for the VNet.
		// If so, we must tear down per-VPC infrastructure (F15 NAT gateway, F20
		// CoreDNS Deployment) BEFORE calling provider.DeleteSubnet. Both the NAT
		// gateway pod and the CoreDNS pod have Multus secondary NICs whose LSPs are
		// attached to this subnet's OVN logical switch. KubeOVN sets a deletion
		// finalizer on the logical switch and refuses to delete it while those LSPs
		// are live — causing subnet delete to hang until dc-api's timeout fires.
		otherActive, err := h.repo.CountActiveSubnetsForVNet(ctx, vnetID, subnetID)
		if err != nil {
			h.log.Warn().Err(err).Str("subnet", subnetID.String()).
				Msg("subnet-delete: CountActiveSubnetsForVNet failed; skipping per-VPC infra teardown")
		} else if otherActive == 0 {
			// Last active subnet — release per-VPC infra that pins LSPs to this subnet.
			// After each Delete call we poll until the pod is actually gone, because
			// KubeOVN sets a deletion finalizer on the logical switch and refuses to
			// delete it while LSPs (pod secondary NICs) are still live. Polling on
			// actual pod absence is the only reliable way to know those LSPs have
			// been released; a fixed sleep is not enough under load and wastes time
			// under low load.
			if h.dns != nil {
				if dnsErr := h.dns.DeleteVpcDNS(ctx, vnet.BackendUID); dnsErr != nil {
					h.log.Warn().Err(dnsErr).Str("vpc", vnet.BackendUID).
						Msg("subnet-delete: DeleteVpcDNS failed; continuing with subnet delete")
				} else {
					h.log.Info().Str("vpc", vnet.BackendUID).
						Msg("subnet-delete: deleted per-VPC CoreDNS Deployment to release LSP")
					// Clear the cached DNS IP so re-provisioning on the next subnet create is clean.
					if clearErr := h.repo.SetVNetDNSServerIP(ctx, vnetID, nil); clearErr != nil {
						h.log.Warn().Err(clearErr).Str("vpc", vnet.BackendUID).Msg("subnet-delete: failed to clear dns_server_ip")
					}
				}
				if waitErr := h.dns.WaitVpcDNSPodsGone(ctx, vnet.BackendUID, 90*time.Second); waitErr != nil {
					h.log.Warn().Err(waitErr).Str("vpc", vnet.BackendUID).
						Msg("subnet-delete: vpc-dns pods didn't drain in time; subnet delete will proceed but may be slower")
				}
			}
			if h.nat != nil {
				if natErr := h.nat.DeleteVpcNAT(ctx, vnet.BackendUID); natErr != nil {
					h.log.Warn().Err(natErr).Str("vpc", vnet.BackendUID).
						Msg("subnet-delete: DeleteVpcNAT failed; continuing with subnet delete")
				} else {
					h.log.Info().Str("vpc", vnet.BackendUID).
						Msg("subnet-delete: deleted per-VPC NAT to release LSP")
					// Clear the cached outbound IP for the same reason.
					if clearErr := h.repo.SetVNetOutboundIP(ctx, vnetID, nil); clearErr != nil {
						h.log.Warn().Err(clearErr).Str("vpc", vnet.BackendUID).Msg("subnet-delete: failed to clear outbound_ip")
					}
				}
				if waitErr := h.nat.WaitVpcNATPodsGone(ctx, vnet.BackendUID, 90*time.Second); waitErr != nil {
					h.log.Warn().Err(waitErr).Str("vpc", vnet.BackendUID).
						Msg("subnet-delete: nat-gw pods didn't drain in time; subnet delete will proceed but may be slower")
				}
			}
		}

		if err := h.provider.DeleteSubnet(ctx, subnet.BackendUID); err != nil {
			h.log.Error().Err(err).Str("backend_uid", subnet.BackendUID).Msg("kubeovn DeleteSubnet failed")
			_ = h.repo.UpdateSubnetStatus(ctx, subnetID, models.StatusFailed, "deletion failed: "+err.Error(), "")
			return
		}
		_ = h.repo.DeleteSubnet(ctx, subnetID)
	}()

	w.WriteHeader(http.StatusAccepted)
}

// ── Async Provisioner ────────────────────────────────────────────────────────

func (h *SubnetHandler) asyncProvisionSubnet(subnetID, vnetID uuid.UUID, tenantID, projectID, userID, vnetUID string, spec models.SubnetSpec) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	providerRes, err := h.provider.CreateSubnet(ctx, vnetUID, spec)
	if err != nil {
		h.log.Error().Err(err).Str("subnet", spec.Name).Msg("kubeovn CreateSubnet failed")
		_ = h.repo.UpdateSubnetStatus(ctx, subnetID, models.StatusFailed,
			"provisioning failed: "+err.Error(), "")
		_ = h.repo.AppendAuditEvent(ctx, &models.AuditEvent{
			ResourceID: subnetID, ActorID: userID, Action: "STATUS_CHANGE",
			FromStatus: models.StatusPending, ToStatus: models.StatusFailed, Message: err.Error(),
		})
		return
	}

	_ = h.repo.UpdateSubnetStatus(ctx, subnetID, models.StatusActive,
		"subnet provisioned", providerRes.BackendUID)
	_ = h.repo.AppendAuditEvent(ctx, &models.AuditEvent{
		ResourceID: subnetID, ActorID: userID, Action: "STATUS_CHANGE",
		FromStatus: models.StatusPending, ToStatus: models.StatusActive,
	})

	// ── F15: provision per-VPC SNAT if this is the first subnet on the VPC ────
	// SNAT operates at the VPC router boundary — opaque to any cluster CNI a
	// user later runs inside VMs on this subnet (Cilium / Calico / BYO chart all
	// work because their VXLAN-encapped pod traffic emerges with the VM's OVN IP
	// as outer source). See FOLLOWUPS F15 + memory project_f15_recipe.md.
	if h.nat != nil {
		present, err := h.nat.IsVpcNATPresent(ctx, vnetUID)
		if err != nil {
			h.log.Warn().Err(err).Str("vpc", vnetUID).Msg("kubeovn: IsVpcNATPresent failed; skipping NAT provision")
			return
		}
		if present {
			return
		}
		lanIP, err := kubeovn.ComputeLanIP(spec.CIDR)
		if err != nil {
			h.log.Warn().Err(err).Str("vpc", vnetUID).Msg("kubeovn: computeLanIP failed; SNAT not provisioned")
			return
		}
		assignedEIP, err := h.nat.EnsureVpcNAT(ctx, vnetUID, spec.CIDR, providerRes.BackendUID, lanIP)
		if err != nil {
			h.log.Error().Err(err).Str("vpc", vnetUID).Msg("kubeovn: EnsureVpcNAT failed")
			return
		}
		if err := h.repo.SetVNetOutboundIP(ctx, vnetID, assignedEIP); err != nil {
			h.log.Warn().Err(err).Str("vpc", vnetUID).Stringer("eip", assignedEIP).Msg("kubeovn: failed to cache outbound IP — NAT is up, but the IP isn't visible on the VNet row")
		}
		h.log.Info().Str("vpc", vnetUID).Stringer("eip", assignedEIP).Msg("kubeovn: SNAT provisioned for VPC")
	}

	// ── F20: provision per-VPC CoreDNS if this is the first subnet on the VPC ──
	// Each VPC gets exactly one CoreDNS Deployment in kube-system with a Multus
	// secondary NIC on this subnet, pinned at <cidr>+2.  Subnet's dhcpV4Options
	// is patched to advertise that IP as dns_server.
	// Every VM on a VPC subnet gets dnsPolicy=None + dnsConfig.nameservers=[dns_ip]
	// injected by the harvester driver (silences KubeVirt's bridge-mode DHCP race).
	if h.dns != nil {
		tenantNS := common.NamespaceForProject(tenantID, projectID)
		present, err := h.dns.IsVpcDNSPresent(ctx, vnetUID)
		if err != nil {
			h.log.Warn().Err(err).Str("vpc", vnetUID).Msg("kubeovn: IsVpcDNSPresent failed; skipping DNS provision")
			return
		}
		if present {
			return
		}
		dnsIP, err := h.dns.EnsureVpcDNS(ctx, vnetUID, spec.CIDR, providerRes.BackendUID, tenantNS)
		if err != nil {
			h.log.Error().Err(err).Str("vpc", vnetUID).Msg("kubeovn: EnsureVpcDNS failed")
			return
		}
		if err := h.repo.SetVNetDNSServerIP(ctx, vnetID, dnsIP); err != nil {
			h.log.Warn().Err(err).Str("vpc", vnetUID).Stringer("dns_ip", dnsIP).Msg("kubeovn: failed to cache DNS server IP — DNS pod is up, but the IP isn't visible on the VNet row")
		}
		h.log.Info().Str("vpc", vnetUID).Stringer("dns_ip", dnsIP).Msg("kubeovn: per-VPC CoreDNS provisioned")
	}
}
