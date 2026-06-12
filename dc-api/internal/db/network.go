// Package db — network.go
//
// Repository methods for M2 networking resources: VNet, Subnet, RouteTable,
// RouteTableAssociation, NSG, NSGRule, NSGAttachment, Peering, PrivateDnsZone,
// and DnsRecord.
//
// All methods are on *Repository to reuse the same pgxpool.Pool; no separate
// connection is introduced. SQL lives here — handlers never call pool directly.
//
// Patterns:
//   - RETURNING clause on INSERT to avoid a second round-trip.
//   - Nullable columns scanned into *string/*int (pointer); dereferenced if not nil.
//   - Transactions used only when multi-step operations must be atomic.
//   - touch_updated_at trigger means we never need to set updated_at in Go.
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wso2/dc-api/internal/audit"
	"github.com/wso2/dc-api/internal/models"
)


// ─────────────────────────── VNet ───────────────────────────────────────────

// CreateVNet inserts a new VNet row in PENDING status.
// Returns the row with its auto-generated ID and timestamps populated.
// M2.5: includes project_id and project_uuid alongside tenant_uuid.
func (r *Repository) CreateVNet(ctx context.Context, v *models.VNet) (*models.VNet, error) {
	const q = `
		INSERT INTO vnets
			(tenant_id, tenant_uuid, project_id, project_uuid, name, region, address_space, description, status, backend_uid, provider_type, message)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, created_at, updated_at`

	row := r.pool.QueryRow(ctx, q,
		v.TenantID,
		v.TenantUUID,
		nilIfEmpty(v.ProjectID),
		v.ProjectUUID,
		v.Name,
		v.Region,
		v.AddressSpace,
		nilIfEmpty(v.Description),
		string(v.Status),
		nilIfEmpty(v.BackendUID),
		v.ProviderType,
		nilIfEmpty(v.Message),
	)
	if err := row.Scan(&v.ID, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, fmt.Errorf("db create vnet: %w", err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: v.ID, Name: v.Name, Kind: familyVNet.kind,
		TenantUUID: &v.TenantUUID, ProjectUUID: nilIfNilUUID(v.ProjectUUID),
		Action: audit.ActionCreate, To: v.Status,
	})
	return v, nil
}

// GetVNet retrieves a VNet by its DC-API UUID, scoped to the given tenantUUID
// and projectUUID. Both UUIDs enforce isolation at the DB layer.
// Callers no longer need a post-fetch tenant/project check.
func (r *Repository) GetVNet(ctx context.Context, id uuid.UUID, tenantUUID uuid.UUID, projectUUID uuid.UUID) (*models.VNet, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, COALESCE(project_id,''), project_uuid,
		       name, region, address_space, description,
		       status, backend_uid, provider_type, message,
		       COALESCE(dns_server_ip::TEXT, ''),
		       created_at, updated_at
		FROM   vnets WHERE id = $1 AND tenant_uuid = $2 AND project_uuid = $3`

	var v models.VNet
	var desc, backendUID, msg *string
	err := r.pool.QueryRow(ctx, q, id, tenantUUID, projectUUID).Scan(
		&v.ID, &v.TenantID, &v.TenantUUID, &v.ProjectID, &v.ProjectUUID,
		&v.Name, &v.Region, &v.AddressSpace, &desc,
		&v.Status, &backendUID, &v.ProviderType, &msg,
		&v.DNSServerIP,
		&v.CreatedAt, &v.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("vnet %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get vnet: %w", err)
	}
	if desc != nil {
		v.Description = *desc
	}
	if backendUID != nil {
		v.BackendUID = *backendUID
	}
	if msg != nil {
		v.Message = *msg
	}
	return &v, nil
}

// GetVNetByTenant retrieves a VNet by its DC-API UUID, scoped only to tenantUUID.
// Used by handlers that don't (yet) have a projectUUID in context — specifically
// the peering, bastion, cluster, and private-endpoint handlers that resolve
// VNet IDs from request bodies, not from the project-scoped URL.
func (r *Repository) GetVNetByTenant(ctx context.Context, id uuid.UUID, tenantUUID uuid.UUID) (*models.VNet, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, COALESCE(project_id,''), project_uuid,
		       name, region, address_space, description,
		       status, backend_uid, provider_type, message,
		       COALESCE(dns_server_ip::TEXT, ''),
		       created_at, updated_at
		FROM   vnets WHERE id = $1 AND tenant_uuid = $2`

	var v models.VNet
	var desc, backendUID, msg *string
	err := r.pool.QueryRow(ctx, q, id, tenantUUID).Scan(
		&v.ID, &v.TenantID, &v.TenantUUID, &v.ProjectID, &v.ProjectUUID,
		&v.Name, &v.Region, &v.AddressSpace, &desc,
		&v.Status, &backendUID, &v.ProviderType, &msg,
		&v.DNSServerIP,
		&v.CreatedAt, &v.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("vnet %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get vnet by tenant: %w", err)
	}
	if desc != nil {
		v.Description = *desc
	}
	if backendUID != nil {
		v.BackendUID = *backendUID
	}
	if msg != nil {
		v.Message = *msg
	}
	return &v, nil
}

// GetVNetInternal retrieves a VNet by its DC-API UUID without tenant/project
// scope filtering. Used by the reconciler, async goroutines, and other internal
// callers that need the full VNet record without a request context.
func (r *Repository) GetVNetInternal(ctx context.Context, id uuid.UUID) (*models.VNet, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, COALESCE(project_id,''), project_uuid,
		       name, region, address_space, description,
		       status, backend_uid, provider_type, message,
		       COALESCE(dns_server_ip::TEXT, ''),
		       created_at, updated_at
		FROM   vnets WHERE id = $1`

	var v models.VNet
	var desc, backendUID, msg *string
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&v.ID, &v.TenantID, &v.TenantUUID, &v.ProjectID, &v.ProjectUUID,
		&v.Name, &v.Region, &v.AddressSpace, &desc,
		&v.Status, &backendUID, &v.ProviderType, &msg,
		&v.DNSServerIP,
		&v.CreatedAt, &v.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("vnet %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get vnet internal: %w", err)
	}
	if desc != nil {
		v.Description = *desc
	}
	if backendUID != nil {
		v.BackendUID = *backendUID
	}
	if msg != nil {
		v.Message = *msg
	}
	return &v, nil
}

// ListVNetsByProject returns all VNets for a project, newest first.
// M2.5: filters on both tenant_uuid and project_uuid.
func (r *Repository) ListVNetsByProject(ctx context.Context, tenantUUID uuid.UUID, projectUUID uuid.UUID) ([]*models.VNet, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, COALESCE(project_id,''), project_uuid,
		       name, region, address_space, description,
		       status, backend_uid, provider_type, message,
		       COALESCE(dns_server_ip::TEXT, ''),
		       created_at, updated_at
		FROM   vnets
		WHERE  tenant_uuid = $1 AND project_uuid = $2
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantUUID, projectUUID)
	if err != nil {
		return nil, fmt.Errorf("db list vnets: %w", err)
	}
	defer rows.Close()

	var results []*models.VNet
	for rows.Next() {
		var v models.VNet
		var desc, backendUID, msg *string
		if err := rows.Scan(
			&v.ID, &v.TenantID, &v.TenantUUID, &v.ProjectID, &v.ProjectUUID,
			&v.Name, &v.Region, &v.AddressSpace, &desc,
			&v.Status, &backendUID, &v.ProviderType, &msg,
			&v.DNSServerIP,
			&v.CreatedAt, &v.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan vnet: %w", err)
		}
		if desc != nil {
			v.Description = *desc
		}
		if backendUID != nil {
			v.BackendUID = *backendUID
		}
		if msg != nil {
			v.Message = *msg
		}
		results = append(results, &v)
	}
	return results, rows.Err()
}

// ListVNetsByTenant returns all VNets for a tenant across all projects, newest first.
// Used by internal callers (reconciler backfill, NAT/DNS startup loops) that walk
// all tenant resources. Handler code should prefer ListVNetsByProject.
func (r *Repository) ListVNetsByTenant(ctx context.Context, tenantUUID uuid.UUID) ([]*models.VNet, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, COALESCE(project_id,''), project_uuid,
		       name, region, address_space, description,
		       status, backend_uid, provider_type, message,
		       COALESCE(dns_server_ip::TEXT, ''),
		       created_at, updated_at
		FROM   vnets
		WHERE  tenant_uuid = $1
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantUUID)
	if err != nil {
		return nil, fmt.Errorf("db list vnets by tenant: %w", err)
	}
	defer rows.Close()

	var results []*models.VNet
	for rows.Next() {
		var v models.VNet
		var desc, backendUID, msg *string
		if err := rows.Scan(
			&v.ID, &v.TenantID, &v.TenantUUID, &v.ProjectID, &v.ProjectUUID,
			&v.Name, &v.Region, &v.AddressSpace, &desc,
			&v.Status, &backendUID, &v.ProviderType, &msg,
			&v.DNSServerIP,
			&v.CreatedAt, &v.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan vnet: %w", err)
		}
		if desc != nil {
			v.Description = *desc
		}
		if backendUID != nil {
			v.BackendUID = *backendUID
		}
		if msg != nil {
			v.Message = *msg
		}
		results = append(results, &v)
	}
	return results, rows.Err()
}

// UpdateVNetStatus updates status, message, and (optionally) backend_uid.
// Passing an empty backendUID leaves the existing value in place (COALESCE).
func (r *Repository) UpdateVNetStatus(ctx context.Context, id uuid.UUID, status models.ResourceStatus, message, backendUID string) error {
	return r.auditedStatusUpdate(ctx, familyVNet, id, status, message, backendUID)
}

// ListAllActiveVNets returns every ACTIVE VNet across all tenants. Used by
// the F15 NAT and F20 DNS startup backfill loops.
//
// F19 bug fixed here: address_space is PostgreSQL TEXT[] (OID 1009), NOT JSONB.
// Scanning into []byte and calling json.Unmarshal fails with:
//   "can't scan _text (OID 1009) in binary format into *[]uint8"
// The fix mirrors ListVNetsByTenant: scan directly into []string and let pgx
// use its native TEXT[] decoder. No json.Unmarshal needed.
func (r *Repository) ListAllActiveVNets(ctx context.Context) ([]*models.VNet, error) {
	const q = `
		SELECT id, tenant_id, name, address_space, region, status, message, backend_uid, created_at, updated_at
		FROM   vnets
		WHERE  status = 'ACTIVE'
		ORDER  BY created_at`
	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("db list all active vnets: %w", err)
	}
	defer rows.Close()
	var out []*models.VNet
	for rows.Next() {
		var v models.VNet
		var msg, backendUID *string
		// address_space is TEXT[] — pgx decodes it natively into []string.
		// Do NOT scan into []byte + json.Unmarshal (F19 bug).
		if err := rows.Scan(&v.ID, &v.TenantID, &v.Name, &v.AddressSpace, &v.Region,
			&v.Status, &msg, &backendUID, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("db scan vnet: %w", err)
		}
		if msg != nil {
			v.Message = *msg
		}
		if backendUID != nil {
			v.BackendUID = *backendUID
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}

// ListSubnetsByVNetBackfill is a backfill-only alias for ListSubnetsByVNet —
// returns subnets in any state (PENDING / ACTIVE) so the startup NAT backfill
// can pick the first one to wire SNAT against.
func (r *Repository) ListSubnetsByVNetBackfill(ctx context.Context, vnetID uuid.UUID) ([]*models.Subnet, error) {
	return r.ListSubnetsByVNet(ctx, vnetID)
}

// SetVNetOutboundIP caches the SNAT EIP assigned by KubeOVN on the VNet row.
// Display-only — KubeOVN remains the source of truth (IptablesEIP.status.ip).
// Passing nil clears the column (used on NAT teardown).
func (r *Repository) SetVNetOutboundIP(ctx context.Context, id uuid.UUID, ip net.IP) error {
	var arg interface{}
	if ip == nil {
		arg = nil
	} else {
		arg = ip.String()
	}
	if _, err := r.pool.Exec(ctx, `UPDATE vnets SET outbound_ip = $2 WHERE id = $1`, id, arg); err != nil {
		return fmt.Errorf("db set vnet outbound_ip %s: %w", id, err)
	}
	return nil
}

// SetVNetDNSServerIP caches the CoreDNS pod IP assigned for this VPC's F20 DNS
// on the VNet row. Display-only — the Deployment in kube-system is the source
// of truth. Passing nil clears the column (used on VPC teardown).
func (r *Repository) SetVNetDNSServerIP(ctx context.Context, id uuid.UUID, ip net.IP) error {
	var arg interface{}
	if ip == nil {
		arg = nil
	} else {
		arg = ip.String()
	}
	if _, err := r.pool.Exec(ctx, `UPDATE vnets SET dns_server_ip = $2 WHERE id = $1`, id, arg); err != nil {
		return fmt.Errorf("db set vnet dns_server_ip %s: %w", id, err)
	}
	return nil
}

// DeleteVNet removes a VNet row. Called after the provider confirms deletion.
func (r *Repository) DeleteVNet(ctx context.Context, id uuid.UUID) error {
	// Children (subnets, peerings, DNS zones, route tables) cascade away with
	// the parent row at the DB layer, which would leave them without terminal
	// audit events — record theirs first, while the rows can still speak.
	for _, child := range []auditedTable{familySubnet, familyPeering, familyDNSZone, familyRouteTable} {
		fk := "vnet_id"
		nameExpr := "x." + child.nameCol
		q := `SELECT x.id, ` + nameExpr + `, x.status::text FROM ` + child.table + ` x WHERE x.` + fk + ` = $1`
		rows, err := r.pool.Query(ctx, q, id)
		if err != nil {
			continue // audit is best-effort; the delete below is what matters
		}
		type c struct {
			id     uuid.UUID
			name   string
			status string
		}
		var cs []c
		for rows.Next() {
			var e c
			if rows.Scan(&e.id, &e.name, &e.status) == nil {
				cs = append(cs, e)
			}
		}
		rows.Close()
		tuid, puid := r.vnetIdentity(ctx, id)
		for _, e := range cs {
			r.recordAudit(ctx, r.pool, auditInsert{
				ID: e.id, Name: e.name, Kind: child.kind, TenantUUID: tuid, ProjectUUID: puid,
				Action: audit.ActionDelete,
				From:   models.ResourceStatus(e.status), To: models.StatusDeleted,
				Message: "removed with parent VNet",
			})
		}
	}
	return r.auditedDelete(ctx, familyVNet, id)
}

// CountVNetsByProject counts PENDING + ACTIVE VNets for quota enforcement.
// M2.5: filters on project_uuid.
func (r *Repository) CountVNetsByProject(ctx context.Context, projectUUID uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM vnets WHERE project_uuid = $1 AND status IN ('PENDING','ACTIVE')`,
		projectUUID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db count vnets by project: %w", err)
	}
	return count, nil
}

// CountVNetsByTenant counts PENDING + ACTIVE VNets for a tenant (all projects).
// Kept for backwards-compat with callers that don't have project context.
func (r *Repository) CountVNetsByTenant(ctx context.Context, tenantUUID uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM vnets WHERE tenant_uuid = $1 AND status IN ('PENDING','ACTIVE')`,
		tenantUUID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db count vnets: %w", err)
	}
	return count, nil
}

// GetNetworkQuota retrieves VNet/subnet/public-IP quotas for a tenant.
// Falls back to the __default__ row if no tenant-specific row exists.
// quotas still uses tenant_id as primary key (slug) because __default__ is a
// non-UUID fixture. TenantUUID is populated when available.
func (r *Repository) GetNetworkQuota(ctx context.Context, tenantID string) (*models.NetworkQuota, error) {
	const q = `
		SELECT tenant_id, COALESCE(tenant_uuid, '00000000-0000-0000-0000-000000000000'::uuid),
		       max_vnets, max_public_ips, max_subnets_per_vnet
		FROM   quotas
		WHERE  tenant_id = $1 OR tenant_id = '__default__'
		ORDER  BY CASE WHEN tenant_id = $1 THEN 0 ELSE 1 END
		LIMIT  1`

	var nq models.NetworkQuota
	err := r.pool.QueryRow(ctx, q, tenantID).Scan(
		&nq.TenantID, &nq.TenantUUID,
		&nq.MaxVNets, &nq.MaxPublicIPs, &nq.MaxSubnetsPerVNet,
	)
	if err != nil {
		return nil, fmt.Errorf("db get network quota for %s: %w", tenantID, err)
	}
	return &nq, nil
}

// GetRegionReservedCIDRs returns the reserved_cidrs column for a named region.
// Returns (nil, nil) if the region does not exist in the DB (caller returns 400).
func (r *Repository) GetRegionReservedCIDRs(ctx context.Context, region string) ([]string, error) {
	var cidrs []string
	err := r.pool.QueryRow(ctx,
		`SELECT reserved_cidrs FROM regions WHERE name = $1`, region,
	).Scan(&cidrs)
	if err == pgx.ErrNoRows {
		return nil, nil // region unknown — caller returns 400
	}
	if err != nil {
		return nil, fmt.Errorf("db get region reserved cidrs: %w", err)
	}
	return cidrs, nil
}

// ─────────────────────────── Subnet ──────────────────────────────────────────

// CreateSubnet inserts a new Subnet row in PENDING status.
// M2.5: includes project_id and project_uuid alongside tenant_uuid.
func (r *Repository) CreateSubnet(ctx context.Context, s *models.Subnet) (*models.Subnet, error) {
	const q = `
		INSERT INTO subnets
			(vnet_id, tenant_id, tenant_uuid, project_id, project_uuid, name, cidr, gateway, description,
			 status, backend_uid, provider_type, message)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id, created_at, updated_at`

	row := r.pool.QueryRow(ctx, q,
		s.VNetID, s.TenantID, s.TenantUUID, nilIfEmpty(s.ProjectID), s.ProjectUUID,
		s.Name, s.CIDR,
		nilIfEmpty(s.Gateway), nilIfEmpty(s.Description),
		string(s.Status), nilIfEmpty(s.BackendUID), s.ProviderType,
		nilIfEmpty(s.Message),
	)
	if err := row.Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, fmt.Errorf("db create subnet: %w", err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: s.ID, Name: s.Name, Kind: familySubnet.kind,
		TenantUUID: &s.TenantUUID, ProjectUUID: nilIfNilUUID(s.ProjectUUID),
		Action: audit.ActionCreate, To: s.Status,
	})
	return s, nil
}

// GetSubnet retrieves a Subnet by ID. No tenant/project scope filter — callers
// enforce isolation by verifying the parent VNet's tenant/project UUIDs.
func (r *Repository) GetSubnet(ctx context.Context, id uuid.UUID) (*models.Subnet, error) {
	const q = `
		SELECT id, vnet_id, tenant_id, tenant_uuid, COALESCE(project_id,''), project_uuid,
		       name, cidr::TEXT, gateway::TEXT,
		       description, status, backend_uid, provider_type, message,
		       created_at, updated_at
		FROM   subnets WHERE id = $1`

	var s models.Subnet
	var gw, desc, backendUID, msg *string
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&s.ID, &s.VNetID, &s.TenantID, &s.TenantUUID, &s.ProjectID, &s.ProjectUUID,
		&s.Name, &s.CIDR, &gw,
		&desc, &s.Status, &backendUID, &s.ProviderType, &msg,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("subnet %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get subnet: %w", err)
	}
	if gw != nil {
		s.Gateway = *gw
	}
	if desc != nil {
		s.Description = *desc
	}
	if backendUID != nil {
		s.BackendUID = *backendUID
	}
	if msg != nil {
		s.Message = *msg
	}
	return &s, nil
}

// ListSubnetsByVNet returns all subnets for a VNet, newest first.
// Isolation is enforced by verifying the parent VNet's tenant/project UUIDs upstream.
func (r *Repository) ListSubnetsByVNet(ctx context.Context, vnetID uuid.UUID) ([]*models.Subnet, error) {
	const q = `
		SELECT id, vnet_id, tenant_id, tenant_uuid, COALESCE(project_id,''), project_uuid,
		       name, cidr::TEXT, gateway::TEXT,
		       description, status, backend_uid, provider_type, message,
		       created_at, updated_at
		FROM   subnets
		WHERE  vnet_id = $1
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, vnetID)
	if err != nil {
		return nil, fmt.Errorf("db list subnets: %w", err)
	}
	defer rows.Close()

	var results []*models.Subnet
	for rows.Next() {
		var s models.Subnet
		var gw, desc, backendUID, msg *string
		if err := rows.Scan(
			&s.ID, &s.VNetID, &s.TenantID, &s.TenantUUID, &s.ProjectID, &s.ProjectUUID,
			&s.Name, &s.CIDR, &gw,
			&desc, &s.Status, &backendUID, &s.ProviderType, &msg,
			&s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan subnet: %w", err)
		}
		if gw != nil {
			s.Gateway = *gw
		}
		if desc != nil {
			s.Description = *desc
		}
		if backendUID != nil {
			s.BackendUID = *backendUID
		}
		if msg != nil {
			s.Message = *msg
		}
		results = append(results, &s)
	}
	return results, rows.Err()
}

// UpdateSubnetStatus updates status, message, and optionally backend_uid.
func (r *Repository) UpdateSubnetStatus(ctx context.Context, id uuid.UUID, status models.ResourceStatus, message, backendUID string) error {
	return r.auditedStatusUpdate(ctx, familySubnet, id, status, message, backendUID)
}

// DeleteSubnet removes a Subnet row by ID.
func (r *Repository) DeleteSubnet(ctx context.Context, id uuid.UUID) error {
	return r.auditedDelete(ctx, familySubnet, id)
}

// CountSubnetsByVNet counts PENDING + ACTIVE subnets in a VNet (for quota).
func (r *Repository) CountSubnetsByVNet(ctx context.Context, vnetID uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM subnets WHERE vnet_id = $1 AND status IN ('PENDING','ACTIVE')`,
		vnetID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db count subnets: %w", err)
	}
	return count, nil
}

// CountActiveSubnetsForVNet returns the number of ACTIVE or PENDING subnets
// in a VNet, excluding the subnet identified by excludeID. Used by the subnet
// delete handler to determine whether this is the last active subnet so it can
// tear down per-VPC infrastructure (NAT gateway, CoreDNS) before calling
// provider.DeleteSubnet.
//
// "Active" here means the subnet is not FAILED and not already DELETING — i.e.
// it will remain on the VPC's logical switch once the current delete completes.
func (r *Repository) CountActiveSubnetsForVNet(ctx context.Context, vnetID, excludeID uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM subnets
		 WHERE vnet_id = $1
		   AND id != $2
		   AND status NOT IN ('FAILED', 'DELETING')`,
		vnetID, excludeID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db count active subnets for vnet %s: %w", vnetID, err)
	}
	return count, nil
}

// ListActiveSubnetCIDRsByVNet returns the CIDRs of all non-failed subnets in a
// VNet. Used by the handler to check CIDR overlap before inserting a new subnet.
func (r *Repository) ListActiveSubnetCIDRsByVNet(ctx context.Context, vnetID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT cidr::TEXT FROM subnets WHERE vnet_id = $1 AND status NOT IN ('FAILED','DELETING')`,
		vnetID,
	)
	if err != nil {
		return nil, fmt.Errorf("db list subnet cidrs: %w", err)
	}
	defer rows.Close()
	var cidrs []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("db scan subnet cidr: %w", err)
		}
		cidrs = append(cidrs, c)
	}
	return cidrs, rows.Err()
}

// ─────────────────────────── Route Table ──────────────────────────────────────

// routesJSON marshals a []models.RouteRule to JSONB; panics are impossible here
// because the slice was already validated in the handler.
func routesJSON(routes []models.RouteRule) []byte {
	if routes == nil {
		routes = []models.RouteRule{}
	}
	b, _ := json.Marshal(routes)
	return b
}

// CreateRouteTable inserts a new RouteTable row.
// Status defaults to ACTIVE (route tables are synchronous, §6 of the design doc).
// M2.5: includes project_id and project_uuid.
func (r *Repository) CreateRouteTable(ctx context.Context, rt *models.RouteTable) (*models.RouteTable, error) {
	const q = `
		INSERT INTO route_tables
			(vnet_id, tenant_id, tenant_uuid, project_id, project_uuid, name, description, routes,
			 status, backend_uid, provider_type)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at`

	row := r.pool.QueryRow(ctx, q,
		rt.VNetID, rt.TenantID, rt.TenantUUID, nilIfEmpty(rt.ProjectID), rt.ProjectUUID,
		rt.Name, nilIfEmpty(rt.Description),
		routesJSON(rt.Routes),
		string(rt.Status),
		nilIfEmpty(rt.BackendUID),
		rt.ProviderType,
	)
	if err := row.Scan(&rt.ID, &rt.CreatedAt, &rt.UpdatedAt); err != nil {
		return nil, fmt.Errorf("db create route table: %w", err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: rt.ID, Name: rt.Name, Kind: familyRouteTable.kind,
		TenantUUID: &rt.TenantUUID, ProjectUUID: nilIfNilUUID(rt.ProjectUUID),
		Action: audit.ActionCreate, To: rt.Status,
	})
	return rt, nil
}

// GetRouteTable retrieves a RouteTable by ID.
func (r *Repository) GetRouteTable(ctx context.Context, id uuid.UUID) (*models.RouteTable, error) {
	const q = `
		SELECT id, vnet_id, tenant_id, tenant_uuid, COALESCE(project_id,''), project_uuid,
		       name, description, routes,
		       status, backend_uid, provider_type, created_at, updated_at
		FROM   route_tables WHERE id = $1`

	var rt models.RouteTable
	var desc, backendUID *string
	var routesRaw []byte
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&rt.ID, &rt.VNetID, &rt.TenantID, &rt.TenantUUID, &rt.ProjectID, &rt.ProjectUUID,
		&rt.Name, &desc, &routesRaw,
		&rt.Status, &backendUID, &rt.ProviderType,
		&rt.CreatedAt, &rt.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("route table %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get route table: %w", err)
	}
	if desc != nil {
		rt.Description = *desc
	}
	if backendUID != nil {
		rt.BackendUID = *backendUID
	}
	if err := json.Unmarshal(routesRaw, &rt.Routes); err != nil {
		return nil, fmt.Errorf("db unmarshal routes: %w", err)
	}
	return &rt, nil
}

// ListRouteTablesByVNet returns all route tables for a VNet.
func (r *Repository) ListRouteTablesByVNet(ctx context.Context, vnetID uuid.UUID) ([]*models.RouteTable, error) {
	const q = `
		SELECT id, vnet_id, tenant_id, tenant_uuid, COALESCE(project_id,''), project_uuid,
		       name, description, routes,
		       status, backend_uid, provider_type, created_at, updated_at
		FROM   route_tables WHERE vnet_id = $1 ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, vnetID)
	if err != nil {
		return nil, fmt.Errorf("db list route tables: %w", err)
	}
	defer rows.Close()

	var results []*models.RouteTable
	for rows.Next() {
		var rt models.RouteTable
		var desc, backendUID *string
		var routesRaw []byte
		if err := rows.Scan(
			&rt.ID, &rt.VNetID, &rt.TenantID, &rt.TenantUUID, &rt.ProjectID, &rt.ProjectUUID,
			&rt.Name, &desc, &routesRaw,
			&rt.Status, &backendUID, &rt.ProviderType,
			&rt.CreatedAt, &rt.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan route table: %w", err)
		}
		if desc != nil {
			rt.Description = *desc
		}
		if backendUID != nil {
			rt.BackendUID = *backendUID
		}
		if err := json.Unmarshal(routesRaw, &rt.Routes); err != nil {
			return nil, fmt.Errorf("db unmarshal routes: %w", err)
		}
		results = append(results, &rt)
	}
	return results, rows.Err()
}

// UpdateRouteTableRoutes replaces the routes JSONB column and updates backend_uid.
func (r *Repository) UpdateRouteTableRoutes(ctx context.Context, id uuid.UUID, routes []models.RouteRule, backendUID string) error {
	const q = `
		UPDATE route_tables
		SET    routes = $2,
		       backend_uid = COALESCE(NULLIF($3, ''), backend_uid)
		WHERE  id = $1`
	if _, err := r.pool.Exec(ctx, q, id, routesJSON(routes), backendUID); err != nil {
		return fmt.Errorf("db update route table routes %s: %w", id, err)
	}
	return nil
}

// DeleteRouteTable removes a RouteTable row.
func (r *Repository) DeleteRouteTable(ctx context.Context, id uuid.UUID) error {
	if err := r.auditedDelete(ctx, familyRouteTable, id); err != nil {
		return fmt.Errorf("db delete route table %s: %w", id, err)
	}
	return nil
}

// RouteTableAssociation is a lightweight DB type for route_table_associations rows.
type RouteTableAssociation struct {
	ID            uuid.UUID
	RouteTableID  uuid.UUID
	SubnetID      uuid.UUID
	TenantID      string
	CreatedAt     time.Time
}

// CreateRouteTableAssociation inserts a route_table_associations row.
// The UNIQUE(subnet_id) constraint means a second call for the same subnet returns a
// unique-constraint error — the handler converts that to 409.
func (r *Repository) CreateRouteTableAssociation(ctx context.Context, assoc *RouteTableAssociation) (*RouteTableAssociation, error) {
	const q = `
		INSERT INTO route_table_associations (route_table_id, subnet_id, tenant_id)
		VALUES ($1, $2, $3)
		RETURNING id, created_at`
	err := r.pool.QueryRow(ctx, q, assoc.RouteTableID, assoc.SubnetID, assoc.TenantID).
		Scan(&assoc.ID, &assoc.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("db create route table association: %w", err)
	}
	return assoc, nil
}

// DeleteRouteTableAssociation removes an association by its ID.
func (r *Repository) DeleteRouteTableAssociation(ctx context.Context, id uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM route_table_associations WHERE id = $1`, id); err != nil {
		return fmt.Errorf("db delete route table association %s: %w", id, err)
	}
	return nil
}

// GetRouteTableAssociation retrieves an association by its ID.
func (r *Repository) GetRouteTableAssociation(ctx context.Context, id uuid.UUID) (*RouteTableAssociation, error) {
	const q = `SELECT id, route_table_id, subnet_id, tenant_id, created_at
	           FROM route_table_associations WHERE id = $1`
	var a RouteTableAssociation
	err := r.pool.QueryRow(ctx, q, id).Scan(&a.ID, &a.RouteTableID, &a.SubnetID, &a.TenantID, &a.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("route table association %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get route table association: %w", err)
	}
	return &a, nil
}

// ─────────────────────────── NSG ──────────────────────────────────────────────

// CreateNSG inserts a new NSG row (ACTIVE status — synchronous resource).
// M2.5: includes project_id and project_uuid.
func (r *Repository) CreateNSG(ctx context.Context, nsg *models.NSG) (*models.NSG, error) {
	const q = `
		INSERT INTO network_security_groups
			(tenant_id, tenant_uuid, project_id, project_uuid, name, description, status, backend_uid, provider_type)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`

	row := r.pool.QueryRow(ctx, q,
		nsg.TenantID, nsg.TenantUUID, nilIfEmpty(nsg.ProjectID), nsg.ProjectUUID,
		nsg.Name, nilIfEmpty(nsg.Description),
		string(nsg.Status), nilIfEmpty(nsg.BackendUID), nsg.ProviderType,
	)
	if err := row.Scan(&nsg.ID, &nsg.CreatedAt, &nsg.UpdatedAt); err != nil {
		return nil, fmt.Errorf("db create nsg: %w", err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: nsg.ID, Name: nsg.Name, Kind: familyNSG.kind,
		TenantUUID: &nsg.TenantUUID, ProjectUUID: nilIfNilUUID(nsg.ProjectUUID),
		Action: audit.ActionCreate, To: nsg.Status,
	})
	return nsg, nil
}

// GetNSG retrieves an NSG (without rules/attachments — callers load those separately).
func (r *Repository) GetNSG(ctx context.Context, id uuid.UUID) (*models.NSG, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, COALESCE(project_id,''), project_uuid,
		       name, description, status, backend_uid, provider_type,
		       created_at, updated_at
		FROM   network_security_groups WHERE id = $1`

	var nsg models.NSG
	var desc, backendUID *string
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&nsg.ID, &nsg.TenantID, &nsg.TenantUUID, &nsg.ProjectID, &nsg.ProjectUUID,
		&nsg.Name, &desc, &nsg.Status, &backendUID, &nsg.ProviderType,
		&nsg.CreatedAt, &nsg.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("nsg %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get nsg: %w", err)
	}
	if desc != nil {
		nsg.Description = *desc
	}
	if backendUID != nil {
		nsg.BackendUID = *backendUID
	}
	return &nsg, nil
}

// ListNSGsByProject returns all NSGs for a project, newest first.
func (r *Repository) ListNSGsByProject(ctx context.Context, tenantUUID uuid.UUID, projectUUID uuid.UUID) ([]*models.NSG, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, COALESCE(project_id,''), project_uuid,
		       name, description, status, backend_uid, provider_type,
		       created_at, updated_at
		FROM   network_security_groups
		WHERE  tenant_uuid = $1 AND project_uuid = $2 ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantUUID, projectUUID)
	if err != nil {
		return nil, fmt.Errorf("db list nsgs: %w", err)
	}
	defer rows.Close()

	var results []*models.NSG
	for rows.Next() {
		var nsg models.NSG
		var desc, backendUID *string
		if err := rows.Scan(
			&nsg.ID, &nsg.TenantID, &nsg.TenantUUID, &nsg.ProjectID, &nsg.ProjectUUID,
			&nsg.Name, &desc, &nsg.Status, &backendUID, &nsg.ProviderType,
			&nsg.CreatedAt, &nsg.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan nsg: %w", err)
		}
		if desc != nil {
			nsg.Description = *desc
		}
		if backendUID != nil {
			nsg.BackendUID = *backendUID
		}
		results = append(results, &nsg)
	}
	return results, rows.Err()
}

// ListNSGsByTenant returns all NSGs for a tenant (all projects), newest first.
// Kept for internal callers that don't have project context.
func (r *Repository) ListNSGsByTenant(ctx context.Context, tenantUUID uuid.UUID) ([]*models.NSG, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, COALESCE(project_id,''), project_uuid,
		       name, description, status, backend_uid, provider_type,
		       created_at, updated_at
		FROM   network_security_groups
		WHERE  tenant_uuid = $1 ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantUUID)
	if err != nil {
		return nil, fmt.Errorf("db list nsgs by tenant: %w", err)
	}
	defer rows.Close()

	var results []*models.NSG
	for rows.Next() {
		var nsg models.NSG
		var desc, backendUID *string
		if err := rows.Scan(
			&nsg.ID, &nsg.TenantID, &nsg.TenantUUID, &nsg.ProjectID, &nsg.ProjectUUID,
			&nsg.Name, &desc, &nsg.Status, &backendUID, &nsg.ProviderType,
			&nsg.CreatedAt, &nsg.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan nsg: %w", err)
		}
		if desc != nil {
			nsg.Description = *desc
		}
		if backendUID != nil {
			nsg.BackendUID = *backendUID
		}
		results = append(results, &nsg)
	}
	return results, rows.Err()
}

// UpdateNSGBackendUID sets the backend_uid after driver creation succeeds.
func (r *Repository) UpdateNSGBackendUID(ctx context.Context, id uuid.UUID, backendUID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE network_security_groups SET backend_uid = $2 WHERE id = $1`,
		id, backendUID,
	)
	if err != nil {
		return fmt.Errorf("db update nsg backend uid %s: %w", id, err)
	}
	return nil
}

// DeleteNSG removes an NSG row. Cascade deletes nsg_rules and nsg_attachments.
func (r *Repository) DeleteNSG(ctx context.Context, id uuid.UUID) error {
	return r.auditedDelete(ctx, familyNSG, id)
}

// ReplaceNSGRules atomically deletes all existing rules for an NSG and inserts
// the new set in a single transaction. Called by PUT /security-groups/{id}/rules.
func (r *Repository) ReplaceNSGRules(ctx context.Context, nsgID uuid.UUID, rules []models.NSGRule) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db begin tx replace nsg rules: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM nsg_rules WHERE nsg_id = $1`, nsgID); err != nil {
		return fmt.Errorf("db delete nsg rules: %w", err)
	}

	for _, rule := range rules {
		const q = `
			INSERT INTO nsg_rules
				(nsg_id, name, direction, priority, protocol,
				 source_address_prefix, source_port_range,
				 destination_address_prefix, destination_port_range, action)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`
		if _, err := tx.Exec(ctx, q,
			nsgID, rule.Name, rule.Direction, rule.Priority, rule.Protocol,
			rule.SourceAddressPrefix, rule.SourcePortRange,
			rule.DestinationAddressPrefix, rule.DestinationPortRange,
			rule.Action,
		); err != nil {
			return fmt.Errorf("db insert nsg rule %q: %w", rule.Name, err)
		}
	}

	return tx.Commit(ctx)
}

// ListNSGRules returns all rules for an NSG, ordered by direction + priority.
func (r *Repository) ListNSGRules(ctx context.Context, nsgID uuid.UUID) ([]models.NSGRule, error) {
	const q = `
		SELECT name, direction, priority, protocol,
		       source_address_prefix, source_port_range,
		       destination_address_prefix, destination_port_range, action
		FROM   nsg_rules WHERE nsg_id = $1
		ORDER  BY direction, priority`

	rows, err := r.pool.Query(ctx, q, nsgID)
	if err != nil {
		return nil, fmt.Errorf("db list nsg rules: %w", err)
	}
	defer rows.Close()

	var rules []models.NSGRule
	for rows.Next() {
		var rule models.NSGRule
		if err := rows.Scan(
			&rule.Name, &rule.Direction, &rule.Priority, &rule.Protocol,
			&rule.SourceAddressPrefix, &rule.SourcePortRange,
			&rule.DestinationAddressPrefix, &rule.DestinationPortRange,
			&rule.Action,
		); err != nil {
			return nil, fmt.Errorf("db scan nsg rule: %w", err)
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

// CreateNSGAttachment inserts an nsg_attachments row.
// The UNIQUE(nsg_id, target_type, target_id) constraint catches duplicate attach.
// M2.5: includes project_id, project_uuid in INSERT.
func (r *Repository) CreateNSGAttachment(ctx context.Context, att *models.NSGAttachment) (*models.NSGAttachment, error) {
	const q = `
		INSERT INTO nsg_attachments (nsg_id, tenant_id, tenant_uuid, project_id, project_uuid, target_type, target_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`
	err := r.pool.QueryRow(ctx, q,
		att.NSGiD, att.TenantID, att.TenantUUID,
		nilIfEmpty(att.ProjectID), nilIfNilUUID(att.ProjectUUID),
		att.TargetType, att.TargetID,
	).Scan(&att.ID, &att.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("db create nsg attachment: %w", err)
	}
	return att, nil
}

// GetNSGAttachment retrieves an attachment by its ID.
// M2.5: includes project_id, project_uuid in SELECT.
func (r *Repository) GetNSGAttachment(ctx context.Context, id uuid.UUID) (*models.NSGAttachment, error) {
	const q = `SELECT id, nsg_id, tenant_id, tenant_uuid, project_id, project_uuid, target_type, target_id, created_at
	           FROM nsg_attachments WHERE id = $1`
	var a models.NSGAttachment
	var projectID *string
	var projectUUID *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&a.ID, &a.NSGiD, &a.TenantID, &a.TenantUUID,
		&projectID, &projectUUID,
		&a.TargetType, &a.TargetID, &a.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("nsg attachment %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get nsg attachment: %w", err)
	}
	if projectID != nil {
		a.ProjectID = *projectID
	}
	if projectUUID != nil {
		a.ProjectUUID = *projectUUID
	}
	return &a, nil
}

// ListNSGAttachments returns all attachments for an NSG.
// M2.5: includes project_id, project_uuid in SELECT.
func (r *Repository) ListNSGAttachments(ctx context.Context, nsgID uuid.UUID) ([]models.NSGAttachment, error) {
	const q = `
		SELECT id, nsg_id, tenant_id, tenant_uuid, project_id, project_uuid, target_type, target_id, created_at
		FROM   nsg_attachments WHERE nsg_id = $1 ORDER BY created_at`

	rows, err := r.pool.Query(ctx, q, nsgID)
	if err != nil {
		return nil, fmt.Errorf("db list nsg attachments: %w", err)
	}
	defer rows.Close()

	var results []models.NSGAttachment
	for rows.Next() {
		var a models.NSGAttachment
		var projectID *string
		var projectUUID *uuid.UUID
		if err := rows.Scan(
			&a.ID, &a.NSGiD, &a.TenantID, &a.TenantUUID,
			&projectID, &projectUUID,
			&a.TargetType, &a.TargetID, &a.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan nsg attachment: %w", err)
		}
		if projectID != nil {
			a.ProjectID = *projectID
		}
		if projectUUID != nil {
			a.ProjectUUID = *projectUUID
		}
		results = append(results, a)
	}
	return results, rows.Err()
}

// ListAttachmentSubnetUIDs returns the list of subnet UUIDs (as strings) currently
// attached to an NSG. Used to build the composite backendUID for UpdateNSGRules.
//
// DEPRECATED: prefer ListAttachmentSubnetBackendUIDs — the kubeovn driver
// patches Subnet CRDs by their KubeOVN backend_uid, not by DC-API's UUID.
// This function is kept for callers that genuinely need the DC-API UUIDs.
func (r *Repository) ListAttachmentSubnetUIDs(ctx context.Context, nsgID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT target_id::TEXT FROM nsg_attachments WHERE nsg_id = $1 AND target_type = 'subnet'`,
		nsgID,
	)
	if err != nil {
		return nil, fmt.Errorf("db list attachment subnet uids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("db scan attachment uid: %w", err)
		}
		ids = append(ids, s)
	}
	return ids, rows.Err()
}

// ListAttachmentSubnetBackendUIDs returns the KubeOVN backend_uid (Subnet CRD
// name) for every subnet attached to the NSG. This is what the kubeovn driver
// needs to PATCH the Subnet CRDs' spec.acls field. Skips attachments whose
// target subnet has no backend_uid yet (parent subnet still PENDING).
func (r *Repository) ListAttachmentSubnetBackendUIDs(ctx context.Context, nsgID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT s.backend_uid
		 FROM nsg_attachments a
		 JOIN subnets s ON s.id = a.target_id
		 WHERE a.nsg_id = $1 AND a.target_type = 'subnet' AND s.backend_uid <> ''`,
		nsgID,
	)
	if err != nil {
		return nil, fmt.Errorf("db list attachment subnet backend_uids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("db scan attachment backend_uid: %w", err)
		}
		ids = append(ids, s)
	}
	return ids, rows.Err()
}

// DeleteNSGAttachment removes an attachment row by its ID.
func (r *Repository) DeleteNSGAttachment(ctx context.Context, id uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM nsg_attachments WHERE id = $1`, id); err != nil {
		return fmt.Errorf("db delete nsg attachment %s: %w", id, err)
	}
	return nil
}

// HasNSGAttachments returns true if the NSG still has active attachments.
// Used to guard NSG deletion (409 if any remain).
func (r *Repository) HasNSGAttachments(ctx context.Context, nsgID uuid.UUID) (bool, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM nsg_attachments WHERE nsg_id = $1`, nsgID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("db count nsg attachments: %w", err)
	}
	return count > 0, nil
}

// ─────────────────────────── Peering ──────────────────────────────────────────

// CreatePeering inserts a new Peering row in PENDING status.
// M2.5: includes project_id, project_uuid in INSERT.
func (r *Repository) CreatePeering(ctx context.Context, p *models.Peering) (*models.Peering, error) {
	const q = `
		INSERT INTO peerings
			(vnet_id, peer_vnet_id, tenant_id, tenant_uuid, project_id, project_uuid,
			 name, allow_forwarded_traffic, status, backend_uid, provider_type)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at`

	row := r.pool.QueryRow(ctx, q,
		p.VNetID, p.PeerVNetID, p.TenantID, p.TenantUUID,
		nilIfEmpty(p.ProjectID), nilIfNilUUID(p.ProjectUUID),
		p.Name,
		p.AllowForwardedTraffic,
		string(p.Status),
		nilIfEmpty(p.BackendUID),
		p.ProviderType,
	)
	if err := row.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, fmt.Errorf("db create peering: %w", err)
	}
	tuid, puid := r.vnetIdentity(ctx, p.VNetID)
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: p.ID, Name: p.Name, Kind: familyPeering.kind,
		TenantUUID: tuid, ProjectUUID: puid,
		Action: audit.ActionCreate, To: p.Status,
	})
	return p, nil
}

// GetPeering retrieves a Peering by ID.
// M2.5: includes project_id, project_uuid in SELECT.
func (r *Repository) GetPeering(ctx context.Context, id uuid.UUID) (*models.Peering, error) {
	const q = `
		SELECT id, vnet_id, peer_vnet_id, tenant_id, tenant_uuid, project_id, project_uuid,
		       name, allow_forwarded_traffic, status, backend_uid, provider_type, created_at, updated_at
		FROM   peerings WHERE id = $1`

	var p models.Peering
	var backendUID, projectID *string
	var projectUUID *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&p.ID, &p.VNetID, &p.PeerVNetID, &p.TenantID, &p.TenantUUID,
		&projectID, &projectUUID,
		&p.Name,
		&p.AllowForwardedTraffic, &p.Status, &backendUID,
		&p.ProviderType, &p.CreatedAt, &p.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("peering %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get peering: %w", err)
	}
	if backendUID != nil {
		p.BackendUID = *backendUID
	}
	if projectID != nil {
		p.ProjectID = *projectID
	}
	if projectUUID != nil {
		p.ProjectUUID = *projectUUID
	}
	return &p, nil
}

// ListPeeringsByVNet returns all peerings where the VNet participates as
// either the initiator (vnet_id = $1) OR the peer (peer_vnet_id = $1),
// ordered newest first.
//
// A VNet peering is a bidirectional L3 link. Before this fix the query only
// returned peerings initiated from this VNet, so the same peering was invisible
// when queried from the other side — inconsistent with Azure's symmetric model.
// The response columns (vnet_id, peer_vnet_id) are preserved as written in the
// DB; callers can tell which side they are on by comparing vnet_id to their
// own VNet ID.
func (r *Repository) ListPeeringsByVNet(ctx context.Context, vnetID uuid.UUID) ([]*models.Peering, error) {
	const q = `
		SELECT id, vnet_id, peer_vnet_id, tenant_id, tenant_uuid, project_id, project_uuid,
		       name, allow_forwarded_traffic, status, backend_uid, provider_type, created_at, updated_at
		FROM   peerings
		WHERE  vnet_id = $1 OR peer_vnet_id = $1
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, vnetID)
	if err != nil {
		return nil, fmt.Errorf("db list peerings: %w", err)
	}
	defer rows.Close()

	var results []*models.Peering
	for rows.Next() {
		var p models.Peering
		var backendUID, projectID *string
		var projectUUID *uuid.UUID
		if err := rows.Scan(
			&p.ID, &p.VNetID, &p.PeerVNetID, &p.TenantID, &p.TenantUUID,
			&projectID, &projectUUID,
			&p.Name,
			&p.AllowForwardedTraffic, &p.Status, &backendUID,
			&p.ProviderType, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan peering: %w", err)
		}
		if backendUID != nil {
			p.BackendUID = *backendUID
		}
		if projectID != nil {
			p.ProjectID = *projectID
		}
		if projectUUID != nil {
			p.ProjectUUID = *projectUUID
		}
		results = append(results, &p)
	}
	return results, rows.Err()
}

// UpdatePeeringStatus updates status, message, and optionally backend_uid.
func (r *Repository) UpdatePeeringStatus(ctx context.Context, id uuid.UUID, status models.ResourceStatus, message, backendUID string) error {
	return r.auditedStatusUpdate(ctx, familyPeering, id, status, message, backendUID)
}

// DeletePeering removes a Peering row.
func (r *Repository) DeletePeering(ctx context.Context, id uuid.UUID) error {
	return r.auditedDelete(ctx, familyPeering, id)
}

// PeeringExistsBetween returns true if a peering already exists in either
// direction between the two VNets.
func (r *Repository) PeeringExistsBetween(ctx context.Context, vnetA, vnetB uuid.UUID) (bool, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM peerings
		WHERE (vnet_id = $1 AND peer_vnet_id = $2)
		   OR (vnet_id = $2 AND peer_vnet_id = $1)
		AND status NOT IN ('FAILED')`,
		vnetA, vnetB,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("db check peering exists: %w", err)
	}
	return count > 0, nil
}

// ── Peering transit CIDR allocator (F6) ──────────────────────────────────────
// The transit CIDR is the /24 OVN uses as the logical-router link between the
// two VPCs in a peering. Pre-F6 it was SHA-256(sorted-names)-derived → birthday
// collision around ~128 peerings. AllocateTransitCIDR picks the lowest unused
// `cidr_index` (0..16383) and binds it to the peering row with a UNIQUE
// constraint so two peerings can never overlap. The CIDR string itself is
// rendered by the caller via TransitCIDRFromIndex below.

// AllocateTransitCIDR returns the CIDR bound to peeringID. If no row exists
// yet, the lowest-numbered free index is allocated; the function is therefore
// idempotent — the same peeringID always gets the same CIDR.
//
// Concurrent allocators race-safely via the UNIQUE constraint: on conflict
// the loop retries with a fresh "lowest free index" query.
func (r *Repository) AllocateTransitCIDR(ctx context.Context, peeringID uuid.UUID) (string, error) {
	// Fast path: existing binding.
	var idx int
	err := r.pool.QueryRow(ctx,
		`SELECT cidr_index FROM peering_transit_cidrs WHERE peering_id = $1`,
		peeringID).Scan(&idx)
	if err == nil {
		return TransitCIDRFromIndex(idx), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("db lookup transit cidr for peering %s: %w", peeringID, err)
	}

	// Allocate. Each attempt finds the lowest free index then tries to
	// claim it; a UNIQUE-violation from a concurrent allocator drops us
	// back to the top of the loop.
	for attempt := 0; attempt < 16; attempt++ {
		freeIdx, err := r.lowestFreeTransitIndex(ctx)
		if err != nil {
			return "", err
		}
		_, err = r.pool.Exec(ctx,
			`INSERT INTO peering_transit_cidrs (peering_id, cidr_index) VALUES ($1, $2)`,
			peeringID, freeIdx)
		if err == nil {
			return TransitCIDRFromIndex(freeIdx), nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// UNIQUE collision — another transaction took this index. Retry.
			continue
		}
		return "", fmt.Errorf("db insert transit cidr for peering %s: %w", peeringID, err)
	}
	return "", fmt.Errorf("db allocate transit cidr for peering %s: too many concurrent attempts", peeringID)
}

// LookupTransitCIDR returns the CIDR bound to peeringID, or ("", nil) if no
// row exists (legacy peerings predating F6).
func (r *Repository) LookupTransitCIDR(ctx context.Context, peeringID uuid.UUID) (string, error) {
	var idx int
	err := r.pool.QueryRow(ctx,
		`SELECT cidr_index FROM peering_transit_cidrs WHERE peering_id = $1`,
		peeringID).Scan(&idx)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("db lookup transit cidr: %w", err)
	}
	return TransitCIDRFromIndex(idx), nil
}

// ReleaseTransitCIDR removes the row for peeringID, freeing its index for
// future allocations. Idempotent — not-found is fine.
func (r *Repository) ReleaseTransitCIDR(ctx context.Context, peeringID uuid.UUID) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM peering_transit_cidrs WHERE peering_id = $1`,
		peeringID); err != nil {
		return fmt.Errorf("db release transit cidr for peering %s: %w", peeringID, err)
	}
	return nil
}

// lowestFreeTransitIndex returns the smallest cidr_index not currently in
// peering_transit_cidrs. Returns 0 when the table is empty. The query uses
// generate_series so the answer is computed server-side in one round-trip
// even if the table is large.
func (r *Repository) lowestFreeTransitIndex(ctx context.Context) (int, error) {
	const q = `
		SELECT MIN(g.idx)
		FROM   generate_series(0, 16383) AS g(idx)
		LEFT JOIN peering_transit_cidrs p ON p.cidr_index = g.idx
		WHERE  p.cidr_index IS NULL`
	var n sql.NullInt32
	if err := r.pool.QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("db lowest-free transit index: %w", err)
	}
	if !n.Valid {
		return 0, errors.New("transit CIDR pool exhausted (16384 active peerings)")
	}
	return int(n.Int32), nil
}

// TransitCIDRFromIndex renders a cidr_index 0..16383 as a /24 inside
// 100.64.0.0/10 (RFC 6598 Shared Address Space — safe for transit links).
// Layout: cidr_index 0 → 100.64.0.0/24, … 256 → 100.65.0.0/24, …
// 16383 → 100.127.255.0/24.
func TransitCIDRFromIndex(idx int) string {
	if idx < 0 {
		idx = 0
	}
	if idx >= 16384 {
		idx = 16383
	}
	octet2 := 64 + (idx >> 8)   // 64..127
	octet3 := idx & 0xff        // 0..255
	return fmt.Sprintf("100.%d.%d.0/24", octet2, octet3)
}

// ─────────────────────────── Private DNS Zone ─────────────────────────────────

// CreateDNSZone inserts a new PrivateDnsZone row in PENDING status.
// M2.5: includes project_id, project_uuid in INSERT.
func (r *Repository) CreateDNSZone(ctx context.Context, z *models.PrivateDnsZone) (*models.PrivateDnsZone, error) {
	const q = `
		INSERT INTO private_dns_zones
			(vnet_id, tenant_id, tenant_uuid, project_id, project_uuid, zone_name, description, status, backend_uid, provider_type)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, created_at, updated_at`

	row := r.pool.QueryRow(ctx, q,
		z.VNetID, z.TenantID, z.TenantUUID,
		nilIfEmpty(z.ProjectID), nilIfNilUUID(z.ProjectUUID),
		z.ZoneName,
		nilIfEmpty(z.Description),
		string(z.Status),
		nilIfEmpty(z.BackendUID),
		z.ProviderType,
	)
	if err := row.Scan(&z.ID, &z.CreatedAt, &z.UpdatedAt); err != nil {
		return nil, fmt.Errorf("db create dns zone: %w", err)
	}
	tuid, puid := r.vnetIdentity(ctx, z.VNetID)
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: z.ID, Name: z.ZoneName, Kind: familyDNSZone.kind,
		TenantUUID: tuid, ProjectUUID: puid,
		Action: audit.ActionCreate, To: z.Status,
	})
	return z, nil
}

// GetDNSZone retrieves a PrivateDnsZone by ID.
// M2.5: includes project_id, project_uuid in SELECT.
func (r *Repository) GetDNSZone(ctx context.Context, id uuid.UUID) (*models.PrivateDnsZone, error) {
	const q = `
		SELECT id, vnet_id, tenant_id, tenant_uuid, project_id, project_uuid, zone_name, description,
		       status, backend_uid, provider_type, created_at, updated_at
		FROM   private_dns_zones WHERE id = $1`

	var z models.PrivateDnsZone
	var desc, backendUID, projectID *string
	var projectUUID *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&z.ID, &z.VNetID, &z.TenantID, &z.TenantUUID,
		&projectID, &projectUUID,
		&z.ZoneName, &desc,
		&z.Status, &backendUID, &z.ProviderType,
		&z.CreatedAt, &z.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("dns zone %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get dns zone: %w", err)
	}
	if desc != nil {
		z.Description = *desc
	}
	if backendUID != nil {
		z.BackendUID = *backendUID
	}
	if projectID != nil {
		z.ProjectID = *projectID
	}
	if projectUUID != nil {
		z.ProjectUUID = *projectUUID
	}
	return &z, nil
}

// ListDNSZonesByVNet returns all DNS zones for a VNet.
// M2.5: includes project_id, project_uuid in SELECT.
func (r *Repository) ListDNSZonesByVNet(ctx context.Context, vnetID uuid.UUID) ([]*models.PrivateDnsZone, error) {
	const q = `
		SELECT id, vnet_id, tenant_id, tenant_uuid, project_id, project_uuid, zone_name, description,
		       status, backend_uid, provider_type, created_at, updated_at
		FROM   private_dns_zones WHERE vnet_id = $1 ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, vnetID)
	if err != nil {
		return nil, fmt.Errorf("db list dns zones: %w", err)
	}
	defer rows.Close()

	var results []*models.PrivateDnsZone
	for rows.Next() {
		var z models.PrivateDnsZone
		var desc, backendUID, projectID *string
		var projectUUID *uuid.UUID
		if err := rows.Scan(
			&z.ID, &z.VNetID, &z.TenantID, &z.TenantUUID,
			&projectID, &projectUUID,
			&z.ZoneName, &desc,
			&z.Status, &backendUID, &z.ProviderType,
			&z.CreatedAt, &z.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan dns zone: %w", err)
		}
		if desc != nil {
			z.Description = *desc
		}
		if backendUID != nil {
			z.BackendUID = *backendUID
		}
		if projectID != nil {
			z.ProjectID = *projectID
		}
		if projectUUID != nil {
			z.ProjectUUID = *projectUUID
		}
		results = append(results, &z)
	}
	return results, rows.Err()
}

// UpdateDNSZoneStatus updates status, message, and optionally backend_uid.
func (r *Repository) UpdateDNSZoneStatus(ctx context.Context, id uuid.UUID, status models.ResourceStatus, message, backendUID string) error {
	return r.auditedStatusUpdate(ctx, familyDNSZone, id, status, message, backendUID)
}

// DeleteDNSZone removes a DNS zone row (cascades to dns_records).
func (r *Repository) DeleteDNSZone(ctx context.Context, id uuid.UUID) error {
	return r.auditedDelete(ctx, familyDNSZone, id)
}

// ─────────────────────────── DNS Record ──────────────────────────────────────

// UpsertDNSRecord inserts or replaces a DNS record (matched on zone_id+record_type+name).
// M2.5: includes project_id, project_uuid in INSERT.
func (r *Repository) UpsertDNSRecord(ctx context.Context, rec *models.DnsRecord) (*models.DnsRecord, error) {
	const q = `
		INSERT INTO dns_records (zone_id, tenant_id, tenant_uuid, project_id, project_uuid, record_type, name, values, ttl)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (zone_id, record_type, name) DO UPDATE
			SET values = EXCLUDED.values, ttl = EXCLUDED.ttl
		RETURNING id, created_at`
	err := r.pool.QueryRow(ctx, q,
		rec.ZoneID, rec.TenantID, rec.TenantUUID,
		nilIfEmpty(rec.ProjectID), nilIfNilUUID(rec.ProjectUUID),
		rec.RecordType, rec.Name, rec.Values, rec.TTL,
	).Scan(&rec.ID, &rec.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("db upsert dns record: %w", err)
	}
	return rec, nil
}

// GetDNSRecord retrieves a DNS record by ID.
// M2.5: includes project_id, project_uuid in SELECT.
func (r *Repository) GetDNSRecord(ctx context.Context, id uuid.UUID) (*models.DnsRecord, error) {
	const q = `SELECT id, zone_id, tenant_id, tenant_uuid, project_id, project_uuid, record_type, name, values, ttl, created_at
	           FROM dns_records WHERE id = $1`
	var rec models.DnsRecord
	var projectID *string
	var projectUUID *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&rec.ID, &rec.ZoneID, &rec.TenantID, &rec.TenantUUID,
		&projectID, &projectUUID,
		&rec.RecordType, &rec.Name,
		&rec.Values, &rec.TTL, &rec.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("dns record %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get dns record: %w", err)
	}
	if projectID != nil {
		rec.ProjectID = *projectID
	}
	if projectUUID != nil {
		rec.ProjectUUID = *projectUUID
	}
	return &rec, nil
}

// ListDNSRecordsByZone returns all DNS records for a zone.
// M2.5: includes project_id, project_uuid in SELECT.
func (r *Repository) ListDNSRecordsByZone(ctx context.Context, zoneID uuid.UUID) ([]*models.DnsRecord, error) {
	const q = `SELECT id, zone_id, tenant_id, tenant_uuid, project_id, project_uuid, record_type, name, values, ttl, created_at
	           FROM dns_records WHERE zone_id = $1 ORDER BY record_type, name`
	rows, err := r.pool.Query(ctx, q, zoneID)
	if err != nil {
		return nil, fmt.Errorf("db list dns records: %w", err)
	}
	defer rows.Close()
	var results []*models.DnsRecord
	for rows.Next() {
		var rec models.DnsRecord
		var projectID *string
		var projectUUID *uuid.UUID
		if err := rows.Scan(
			&rec.ID, &rec.ZoneID, &rec.TenantID, &rec.TenantUUID,
			&projectID, &projectUUID,
			&rec.RecordType, &rec.Name,
			&rec.Values, &rec.TTL, &rec.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan dns record: %w", err)
		}
		if projectID != nil {
			rec.ProjectID = *projectID
		}
		if projectUUID != nil {
			rec.ProjectUUID = *projectUUID
		}
		results = append(results, &rec)
	}
	return results, rows.Err()
}

// DeleteDNSRecord removes a DNS record by ID.
func (r *Repository) DeleteDNSRecord(ctx context.Context, id uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM dns_records WHERE id = $1`, id); err != nil {
		return fmt.Errorf("db delete dns record %s: %w", id, err)
	}
	return nil
}
