-- DC-API PostgreSQL Schema
-- Run: psql -h <host> -U dc_api -d dc_api -f schema.sql
--
-- This file is fully idempotent — every statement is safe to run repeatedly.
-- dc-api executes it on every boot via internal/db/migrate.go.
--
-- Idempotency rules used here:
--   CREATE EXTENSION  → IF NOT EXISTS
--   CREATE TYPE       → wrapped in DO block, swallow duplicate_object
--   CREATE TABLE      → IF NOT EXISTS
--   CREATE INDEX      → IF NOT EXISTS
--   CREATE TRIGGER    → DROP TRIGGER IF EXISTS + CREATE TRIGGER
--                       (PG 14+ has CREATE OR REPLACE TRIGGER but we still
--                        target PG 14 for the alpine image, and the drop/create
--                        pattern is portable.)
--   CREATE FUNCTION   → CREATE OR REPLACE FUNCTION
--   INSERT seeds      → ON CONFLICT DO {NOTHING|UPDATE}
--   ALTER TABLE ADD   → ADD COLUMN IF NOT EXISTS (used for post-launch columns
--                       so upgrade-path DBs catch up without us touching the
--                       original CREATE TABLE body)
--
-- When adding new state to this file:
--   - new tables: define inline with CREATE TABLE IF NOT EXISTS;
--   - new columns on existing tables: append an ALTER TABLE ADD COLUMN IF NOT
--     EXISTS below; do NOT rewrite the original CREATE TABLE body (a fresh-DB
--     install still needs the column, and IF NOT EXISTS in CREATE TABLE only
--     guards the whole table — so the ALTER is what catches both fresh and
--     existing DBs uniformly);
--   - new enum values: add to the CREATE TYPE body AND mirror as
--     `ALTER TYPE … ADD VALUE IF NOT EXISTS` below the CREATE TYPE block.

-- ─────────────────────────── Extensions ────────────────────────────────────
CREATE EXTENSION IF NOT EXISTS "pgcrypto"; -- for gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS "btree_gist"; -- for subnets CIDR overlap exclusion

-- ─────────────────────────── Enums ─────────────────────────────────────────
DO $$
BEGIN
    CREATE TYPE resource_type AS ENUM (
        'VIRTUAL_MACHINE',
        'CLUSTER',
        'VOLUME',
        'BASTION'
    );
EXCEPTION WHEN duplicate_object THEN NULL;
END;
$$;

-- Upgrade-path mirror: an older DB created resource_type without 'BASTION'.
-- ADD VALUE IF NOT EXISTS is a PG 12+ idempotent op.
ALTER TYPE resource_type ADD VALUE IF NOT EXISTS 'BASTION';

DO $$
BEGIN
    CREATE TYPE resource_status AS ENUM (
        'PENDING',
        'ACTIVE',
        'FAILED',
        'DELETING',
        'DELETED' -- terminal; appears only on audit events, never on rows
    );
EXCEPTION WHEN duplicate_object THEN NULL;
END;
$$;

-- ─────────────────────────── Resources ─────────────────────────────────────
-- The central registry. Every resource managed by DC-API has exactly one row.
--
-- OwnerID:     Asgardeo "sub" claim (user ID). Who triggered the creation.
-- TenantID:    Asgardeo group → Tenant mapping. Used for quota checks and isolation.
-- BackendUID:  The provider-specific identifier (Harvester VM name, Rancher cluster ID).
--              NULL until the backend confirms creation.
-- Metadata:    JSONB — stores SSH fingerprint, IP address, kubeconfig ref, etc.
--              Using JSONB avoids a schema migration every time we add a field.

CREATE TABLE IF NOT EXISTS resources (
    id            UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     TEXT          NOT NULL,
    owner_id      TEXT          NOT NULL,
    name          TEXT          NOT NULL,
    type          resource_type NOT NULL,
    size          TEXT,
    status        resource_status NOT NULL DEFAULT 'PENDING',
    provider_type TEXT          NOT NULL,
    backend_uid   TEXT,
    ip_address    TEXT,
    message       TEXT,
    metadata      JSONB         NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

-- Post-launch columns (kept as ALTER so upgrade-path DBs pick them up too).
-- F10 bastion mgmt-VLAN IP; NULL for non-bastion resources.
ALTER TABLE resources ADD COLUMN IF NOT EXISTS mgmt_ip            TEXT;
-- F41/F32 VPC attachment + Rancher Steve metadata for clusters.
ALTER TABLE resources ADD COLUMN IF NOT EXISTS vnet_id            UUID;
ALTER TABLE resources ADD COLUMN IF NOT EXISTS subnet_id          UUID;
ALTER TABLE resources ADD COLUMN IF NOT EXISTS rancher_uid        TEXT;
ALTER TABLE resources ADD COLUMN IF NOT EXISTS kubernetes_version TEXT;
ALTER TABLE resources ADD COLUMN IF NOT EXISTS machine_pool_size  INTEGER;

CREATE INDEX IF NOT EXISTS idx_resources_tenant      ON resources (tenant_id);
CREATE INDEX IF NOT EXISTS idx_resources_backend_uid ON resources (backend_uid) WHERE backend_uid IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_resources_unique_name ON resources (tenant_id, name, type);

-- Trigger: auto-update updated_at on every row change.
CREATE OR REPLACE FUNCTION touch_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS resources_updated_at ON resources;
CREATE TRIGGER resources_updated_at
    BEFORE UPDATE ON resources
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ─────────────────────────── Audit Events ──────────────────────────────────
-- Append-only audit log. Never UPDATE or DELETE rows from this table.

CREATE TABLE IF NOT EXISTS audit_events (
    id          UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    resource_id UUID, -- point-in-time pointer; snapshots below are the rendering truth
    actor_id    TEXT          NOT NULL,
    action      TEXT          NOT NULL,
    from_status resource_status,
    to_status   resource_status,
    message     TEXT,
    created_at  TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_audit_resource ON audit_events (resource_id);
CREATE INDEX IF NOT EXISTS idx_audit_actor    ON audit_events (actor_id);
CREATE INDEX IF NOT EXISTS idx_audit_time     ON audit_events (created_at DESC);

-- ─────────────────────────── Quotas ────────────────────────────────────────

CREATE TABLE IF NOT EXISTS quotas (
    tenant_id     TEXT PRIMARY KEY,
    max_vms       INT  NOT NULL DEFAULT 5,
    max_clusters  INT  NOT NULL DEFAULT 2,
    max_cpu       INT  NOT NULL DEFAULT 20,
    max_memory_gb INT  NOT NULL DEFAULT 64,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Post-launch quota columns (M2 networking limits).
ALTER TABLE quotas ADD COLUMN IF NOT EXISTS max_vnets            INTEGER NOT NULL DEFAULT 10;
ALTER TABLE quotas ADD COLUMN IF NOT EXISTS max_public_ips       INTEGER NOT NULL DEFAULT 3;
ALTER TABLE quotas ADD COLUMN IF NOT EXISTS max_subnets_per_vnet INTEGER NOT NULL DEFAULT 10;

-- Seed: default quota for new tenants until an admin adjusts it.
INSERT INTO quotas (tenant_id, max_vms, max_clusters, max_cpu, max_memory_gb)
VALUES ('__default__', 3, 1, 12, 32)
ON CONFLICT (tenant_id) DO NOTHING;

-- ─────────────────────────── M2 Networking ──────────────────────────────────
-- VNet, Subnet, RouteTable, NSG, Peering, PrivateDnsZone, DnsRecord.
-- These mirror the Azure-shaped public API; the KubeOVN driver translates them
-- to OVN logical objects.

-- ── Regions ──────────────────────────────────────────────────────────────────
-- Operator-managed region catalog. Populated once per DC deployment.
-- reserved_cidrs: per-region CIDRs tenant address spaces must not overlap.

CREATE TABLE IF NOT EXISTS regions (
    name            TEXT        PRIMARY KEY,
    description     TEXT,
    reserved_cidrs  TEXT[]      NOT NULL DEFAULT '{}',
    public_ip_pool  JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed: lk region. ON CONFLICT DO UPDATE refreshes reserved_cidrs on every
-- boot so the seeded list always reflects the actual cluster claims. Tenants
-- that pick an overlapping address space get a 400 instead of silently owning
-- IPs that collide with infrastructure pods. (Absorbs former F45 logic.)
INSERT INTO regions (name, description, reserved_cidrs)
VALUES (
    'lk',
    'WSO2 Sri Lanka Datacenter (lk-dev)',
    ARRAY[
        '10.16.0.0/16:harvester-ovn-default',
        '10.52.0.0/16:harvester-pod-cidr',
        '10.53.0.0/16:harvester-service-cidr',
        '10.96.0.0/12:rke2-service-cluster-ip-range',
        '100.64.0.0/16:kube-ovn-join',
        '192.168.10.0/24:harvester-mgmt-lan',
        '172.22.100.0/25:operator-mgmt-vlan'
    ]
)
ON CONFLICT (name) DO UPDATE
   SET reserved_cidrs = EXCLUDED.reserved_cidrs,
       description    = EXCLUDED.description;

-- ── VNets ─────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS vnets (
    id            UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     TEXT            NOT NULL,
    name          TEXT            NOT NULL,
    region        TEXT            NOT NULL REFERENCES regions(name),
    address_space TEXT[]          NOT NULL,
    description   TEXT,
    status        resource_status NOT NULL DEFAULT 'PENDING',
    backend_uid   TEXT,
    provider_type TEXT            NOT NULL DEFAULT 'kubeovn',
    message       TEXT,
    created_at    TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

-- F15 outbound IP cache (display-only; KubeOVN IPAM is source of truth).
ALTER TABLE vnets ADD COLUMN IF NOT EXISTS outbound_ip   INET;
-- F20 per-VPC CoreDNS pod IP cache (display-only).
ALTER TABLE vnets ADD COLUMN IF NOT EXISTS dns_server_ip INET;

CREATE UNIQUE INDEX IF NOT EXISTS idx_vnets_unique_name ON vnets (tenant_id, name);
CREATE INDEX        IF NOT EXISTS idx_vnets_tenant      ON vnets (tenant_id);
CREATE INDEX        IF NOT EXISTS idx_vnets_backend_uid ON vnets (backend_uid) WHERE backend_uid IS NOT NULL;

DROP TRIGGER IF EXISTS vnets_updated_at ON vnets;
CREATE TRIGGER vnets_updated_at
    BEFORE UPDATE ON vnets
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ── Subnets ───────────────────────────────────────────────────────────────────
-- CIDR is immutable after create (M2 — no resize support).
CREATE TABLE IF NOT EXISTS subnets (
    id            UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    vnet_id       UUID            NOT NULL REFERENCES vnets(id) ON DELETE CASCADE,
    tenant_id     TEXT            NOT NULL,
    name          TEXT            NOT NULL,
    cidr          CIDR            NOT NULL,
    gateway       INET,
    description   TEXT,
    status        resource_status NOT NULL DEFAULT 'PENDING',
    backend_uid   TEXT,
    provider_type TEXT            NOT NULL DEFAULT 'kubeovn',
    message       TEXT,
    created_at    TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    -- CIDR overlap exclusion within the same VNet (requires btree_gist).
    EXCLUDE USING gist (vnet_id WITH =, cidr inet_ops WITH &&)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_subnets_unique_name ON subnets (vnet_id, name);
CREATE INDEX        IF NOT EXISTS idx_subnets_vnet        ON subnets (vnet_id);
CREATE INDEX        IF NOT EXISTS idx_subnets_tenant      ON subnets (tenant_id);
CREATE INDEX        IF NOT EXISTS idx_subnets_backend_uid ON subnets (backend_uid) WHERE backend_uid IS NOT NULL;

DROP TRIGGER IF EXISTS subnets_updated_at ON subnets;
CREATE TRIGGER subnets_updated_at
    BEFORE UPDATE ON subnets
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ── Route Tables ──────────────────────────────────────────────────────────────
-- Holds static routing rules for a VNet. KubeOVN's logical router is per-VPC;
-- subnet-level association is informational only in M2. See m2-network-api-design.md §13.
CREATE TABLE IF NOT EXISTS route_tables (
    id            UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    vnet_id       UUID            NOT NULL REFERENCES vnets(id) ON DELETE CASCADE,
    tenant_id     TEXT            NOT NULL,
    name          TEXT            NOT NULL,
    description   TEXT,
    routes        JSONB           NOT NULL DEFAULT '[]',
    status        resource_status NOT NULL DEFAULT 'ACTIVE',
    backend_uid   TEXT,
    provider_type TEXT            NOT NULL DEFAULT 'kubeovn',
    created_at    TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_route_tables_unique_name ON route_tables (vnet_id, name);
CREATE INDEX        IF NOT EXISTS idx_route_tables_vnet        ON route_tables (vnet_id);
CREATE INDEX        IF NOT EXISTS idx_route_tables_tenant      ON route_tables (tenant_id);

DROP TRIGGER IF EXISTS route_tables_updated_at ON route_tables;
CREATE TRIGGER route_tables_updated_at
    BEFORE UPDATE ON route_tables
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ── Route Table Associations ──────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS route_table_associations (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    route_table_id  UUID        NOT NULL REFERENCES route_tables(id) ON DELETE CASCADE,
    subnet_id       UUID        NOT NULL REFERENCES subnets(id) ON DELETE CASCADE,
    tenant_id       TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (subnet_id)
);

CREATE INDEX IF NOT EXISTS idx_rta_route_table ON route_table_associations (route_table_id);
CREATE INDEX IF NOT EXISTS idx_rta_subnet      ON route_table_associations (subnet_id);

-- ── Network Security Groups ───────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS network_security_groups (
    id            UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     TEXT            NOT NULL,
    name          TEXT            NOT NULL,
    description   TEXT,
    status        resource_status NOT NULL DEFAULT 'ACTIVE',
    backend_uid   TEXT,
    provider_type TEXT            NOT NULL DEFAULT 'kubeovn',
    message       TEXT,
    created_at    TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_nsg_unique_name ON network_security_groups (tenant_id, name);
CREATE INDEX        IF NOT EXISTS idx_nsg_tenant      ON network_security_groups (tenant_id);

DROP TRIGGER IF EXISTS nsg_updated_at ON network_security_groups;
CREATE TRIGGER nsg_updated_at
    BEFORE UPDATE ON network_security_groups
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ── NSG Rules ─────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS nsg_rules (
    id                          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    nsg_id                      UUID        NOT NULL REFERENCES network_security_groups(id) ON DELETE CASCADE,
    name                        TEXT        NOT NULL,
    direction                   TEXT        NOT NULL CHECK (direction IN ('inbound', 'outbound')),
    priority                    INT         NOT NULL CHECK (priority BETWEEN 100 AND 4096),
    protocol                    TEXT        NOT NULL,
    source_address_prefix       TEXT        NOT NULL,
    source_port_range           TEXT        NOT NULL,
    destination_address_prefix  TEXT        NOT NULL,
    destination_port_range      TEXT        NOT NULL,
    action                      TEXT        NOT NULL CHECK (action IN ('allow', 'deny')),
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (nsg_id, direction, priority)
);

CREATE INDEX IF NOT EXISTS idx_nsg_rules_nsg ON nsg_rules (nsg_id);

-- ── NSG Attachments ───────────────────────────────────────────────────────────
-- M2 accepts target_type='subnet'; 'nic' is reserved for M3 and rejected at API.
CREATE TABLE IF NOT EXISTS nsg_attachments (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    nsg_id          UUID        NOT NULL REFERENCES network_security_groups(id) ON DELETE CASCADE,
    tenant_id       TEXT        NOT NULL,
    target_type     TEXT        NOT NULL CHECK (target_type IN ('subnet', 'nic')),
    target_id       UUID        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (nsg_id, target_type, target_id)
);

CREATE INDEX IF NOT EXISTS idx_nsg_attachments_nsg    ON nsg_attachments (nsg_id);
CREATE INDEX IF NOT EXISTS idx_nsg_attachments_target ON nsg_attachments (target_id);

-- ── Peerings ─────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS peerings (
    id                      UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    vnet_id                 UUID            NOT NULL REFERENCES vnets(id) ON DELETE CASCADE,
    peer_vnet_id            UUID            NOT NULL REFERENCES vnets(id) ON DELETE CASCADE,
    tenant_id               TEXT            NOT NULL,
    name                    TEXT            NOT NULL,
    allow_forwarded_traffic BOOLEAN         NOT NULL DEFAULT false,
    status                  resource_status NOT NULL DEFAULT 'PENDING',
    backend_uid             TEXT,
    provider_type           TEXT            NOT NULL DEFAULT 'kubeovn',
    message                 TEXT,
    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    UNIQUE (vnet_id, peer_vnet_id)
);

CREATE INDEX IF NOT EXISTS idx_peerings_vnet      ON peerings (vnet_id);
CREATE INDEX IF NOT EXISTS idx_peerings_peer_vnet ON peerings (peer_vnet_id);
CREATE INDEX IF NOT EXISTS idx_peerings_tenant    ON peerings (tenant_id);

DROP TRIGGER IF EXISTS peerings_updated_at ON peerings;
CREATE TRIGGER peerings_updated_at
    BEFORE UPDATE ON peerings
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ── Private DNS Zones ─────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS private_dns_zones (
    id            UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    vnet_id       UUID            NOT NULL REFERENCES vnets(id) ON DELETE CASCADE,
    tenant_id     TEXT            NOT NULL,
    zone_name     TEXT            NOT NULL,
    description   TEXT,
    status        resource_status NOT NULL DEFAULT 'PENDING',
    backend_uid   TEXT,
    provider_type TEXT            NOT NULL DEFAULT 'kubeovn',
    message       TEXT,
    created_at    TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    UNIQUE (vnet_id, zone_name)
);

CREATE INDEX IF NOT EXISTS idx_dns_zones_vnet   ON private_dns_zones (vnet_id);
CREATE INDEX IF NOT EXISTS idx_dns_zones_tenant ON private_dns_zones (tenant_id);

DROP TRIGGER IF EXISTS private_dns_zones_updated_at ON private_dns_zones;
CREATE TRIGGER private_dns_zones_updated_at
    BEFORE UPDATE ON private_dns_zones
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ── DNS Records ───────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS dns_records (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id     UUID        NOT NULL REFERENCES private_dns_zones(id) ON DELETE CASCADE,
    tenant_id   TEXT        NOT NULL,
    record_type TEXT        NOT NULL CHECK (record_type IN ('A','AAAA','CNAME','SRV','TXT','MX')),
    name        TEXT        NOT NULL,
    values      TEXT[]      NOT NULL,
    ttl         INT         NOT NULL DEFAULT 300,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (zone_id, record_type, name)
);

CREATE INDEX IF NOT EXISTS idx_dns_records_zone   ON dns_records (zone_id);
CREATE INDEX IF NOT EXISTS idx_dns_records_tenant ON dns_records (tenant_id);

-- ── Peering transit CIDR allocations (F6) ────────────────────────────────────
-- One row per active peering, tracking which /24 inside `100.64.0.0/10` is
-- being used for the OVN logical-router transit link between two VPCs.
-- Pre-F6 the /24 was derived from SHA-256(sorted-peer-names), giving a
-- ~16,384-bucket space where the birthday paradox produces a collision at
-- ~128 peerings. This table replaces the hash with a deterministic
-- "first unused index" allocator: a row is inserted on Peering create,
-- removed on Peering delete, and `cidr_index` is UNIQUE so two peerings
-- can never share a transit /24.
--
-- The CIDR is derived from `cidr_index` in Go:
--   network = 100.64.0.0 + (cidr_index * 256) bytes  ⇒  100.64.0.0 .. 100.127.255.0/24
-- Max 16,384 active peerings (the same total the hash space could theoretically
-- represent — now actually reachable without collisions).
CREATE TABLE IF NOT EXISTS peering_transit_cidrs (
    peering_id  UUID    PRIMARY KEY REFERENCES peerings(id) ON DELETE CASCADE,
    cidr_index  INTEGER NOT NULL UNIQUE CHECK (cidr_index >= 0 AND cidr_index < 16384),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─────────────────────────── M1.5 RBAC ──────────────────────────────────────
-- role_assignments: scope-polymorphic role bindings (Azure-shaped).
-- service_accounts: DC-API-issued long-lived tokens for CI/CD principals.

CREATE TABLE IF NOT EXISTS role_assignments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    principal_type  TEXT NOT NULL,
    principal_id    TEXT NOT NULL,
    scope_type      TEXT NOT NULL,                -- 'tenant' | 'project' | 'resource' (RBAC v2)
    scope_id        TEXT NOT NULL,
    role_definition TEXT NOT NULL,                -- RBAC v2: built-in role key or custom role_definitions.id
    granted_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    granted_by      TEXT NOT NULL
    -- Uniqueness is a CREATE UNIQUE INDEX in the RBAC v2 migration block below
    -- (one statement serves both fresh installs and the in-place column rename).
);

CREATE INDEX IF NOT EXISTS idx_role_assignments_principal ON role_assignments (principal_type, principal_id);
CREATE INDEX IF NOT EXISTS idx_role_assignments_scope     ON role_assignments (scope_type, scope_id);

CREATE TABLE IF NOT EXISTS service_accounts (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        TEXT NOT NULL,
    name             TEXT NOT NULL,
    -- token_lookup_id: first 12 chars of the raw token (not secret on its own).
    -- Default '' lets the ADD COLUMN below cover older DBs without a token_lookup_id;
    -- new rows are required to populate it (enforced at application layer).
    token_lookup_id  TEXT NOT NULL DEFAULT '',
    token_hash       TEXT NOT NULL,
    description      TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used        TIMESTAMPTZ,
    UNIQUE (tenant_id, name)
);

-- Upgrade-path mirror for token_lookup_id (older deploys lacked it).
ALTER TABLE service_accounts ADD COLUMN IF NOT EXISTS token_lookup_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_sa_token_lookup_id
    ON service_accounts (token_lookup_id) WHERE token_lookup_id != '';

-- ─────────────────────────── M3 Key Vault ───────────────────────────────────
-- A Key Vault is a logical container for secrets. The OpenBao KV-v2 mount,
-- per-VPC Private Endpoints, and access policies are layered on top.
CREATE TABLE IF NOT EXISTS key_vaults (
    id                UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         TEXT            NOT NULL,
    name              TEXT            NOT NULL,
    soft_delete_days  INTEGER         NOT NULL DEFAULT 30,
    status            resource_status NOT NULL DEFAULT 'ACTIVE',
    message           TEXT,
    created_at        TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS idx_key_vaults_tenant ON key_vaults (tenant_id);

DROP TRIGGER IF EXISTS key_vaults_updated_at ON key_vaults;
CREATE TRIGGER key_vaults_updated_at
    BEFORE UPDATE ON key_vaults
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ─────────────────────────── M3 Private Endpoints ───────────────────────────
-- Generic primitive reused across all managed services. A Private Endpoint is
-- a per-(target, vnet) network attachment: an IP allocated from the tenant's
-- subnet CIDR forwarded by a dual-NIC nginx pod to the target service's
-- in-cluster backend.
CREATE TABLE IF NOT EXISTS private_endpoints (
    id                UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         TEXT            NOT NULL,
    target_type       TEXT            NOT NULL,
    target_id         UUID            NOT NULL,
    vnet_id           UUID            NOT NULL REFERENCES vnets(id)   ON DELETE CASCADE,
    subnet_id         UUID            NOT NULL REFERENCES subnets(id) ON DELETE CASCADE,
    name              TEXT            NOT NULL,
    ip_address        INET,
    hostname          TEXT,
    backend_addr      TEXT            NOT NULL,
    proxy_pod_name    TEXT,
    status            resource_status NOT NULL DEFAULT 'PENDING',
    message           TEXT,
    created_at        TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    UNIQUE (target_type, target_id, vnet_id)
);

CREATE INDEX IF NOT EXISTS idx_private_endpoints_target ON private_endpoints (target_type, target_id);
CREATE INDEX IF NOT EXISTS idx_private_endpoints_tenant ON private_endpoints (tenant_id);
CREATE INDEX IF NOT EXISTS idx_private_endpoints_vnet   ON private_endpoints (vnet_id);

DROP TRIGGER IF EXISTS private_endpoints_updated_at ON private_endpoints;
CREATE TRIGGER private_endpoints_updated_at
    BEFORE UPDATE ON private_endpoints
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ─────────────────────────── Tenants registry ───────────────────────────────
-- Canonical "what tenants exist" registry. Populated two ways:
--   1. autoprovision in the auth middleware — UPSERTs a row on first sight
--      of any `dc-tenant-<id>` group claim in a JWT
--   2. explicit registration via POST /v1/admin/tenants — admin pre-creates
--      a row so empty tenants (Asgardeo groups with no members yet) are
--      visible to platform admins via GET /v1/tenants
-- Before this table existed, GET /v1/tenants (admin path) derived the list
-- from DISTINCT role_assignments.scope_id, which made empty tenants
-- structurally invisible.
CREATE TABLE IF NOT EXISTS tenants (
    id              TEXT        PRIMARY KEY,
    name            TEXT        NOT NULL,
    asgardeo_group  TEXT,
    description     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by      TEXT        NOT NULL DEFAULT 'autoprovision'
);

CREATE INDEX IF NOT EXISTS idx_tenants_asgardeo_group ON tenants (asgardeo_group);

-- ─────────────────────────── Historical cleanups ────────────────────────────
-- F15-v0 table that early dev clusters may still have. Safe no-op when absent.
DROP TABLE IF EXISTS vpc_external_ips;

-- Post-create message columns (M2 networking — schema mirror).
ALTER TABLE peerings                ADD COLUMN IF NOT EXISTS message TEXT;
ALTER TABLE private_dns_zones       ADD COLUMN IF NOT EXISTS message TEXT;
ALTER TABLE network_security_groups ADD COLUMN IF NOT EXISTS message TEXT;

-- Admin-set mnemonic for principals (no IdP-sourced PII stored).
-- Optional. When set, cloud-ui shows this string instead of the opaque sub
-- in the members list. Operator's own bookkeeping; nothing in dc-api
-- derives behaviour from it.
ALTER TABLE role_assignments ADD COLUMN IF NOT EXISTS display_alias TEXT;

-- ─────────────────────────── Phase 6a — UUID-keyed tenancy ──────────────────
-- Every per-tenant resource gets a tenant_uuid column that points at
-- tenants.tenant_uuid (the canonical, immutable identity). The slug
-- (tenant_id text column) stays for display + URL ergonomics — it is
-- effectively a renameable handle now.
--
-- Why: re-registering a previously-used slug today silently inherits every
-- orphan row that still references the slug. With tenant_uuid as the actual
-- ownership ref, a re-registered slug gets a fresh UUID and the old rows
-- become invisible to all tenants (`WHERE tenant_uuid = $1` produces no
-- match). Orphans become a storage-hygiene concern rather than a data-leak
-- hazard. See docs/defense-in-depth.md §6a.
--
-- All ALTERs are nullable. Backfill happens in migrate.go (Go-side step
-- after this SQL runs) so the schema apply itself stays SQL-only. A future
-- migration can make the columns NOT NULL once we've soaked the backfill.

-- tenants registry: its own canonical UUID.
ALTER TABLE tenants            ADD COLUMN IF NOT EXISTS tenant_uuid UUID NOT NULL DEFAULT gen_random_uuid();
CREATE UNIQUE INDEX IF NOT EXISTS tenants_tenant_uuid_uq ON tenants (tenant_uuid);

-- Per-tenant resource tables: nullable tenant_uuid column + index.
ALTER TABLE resources                 ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE quotas                    ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE vnets                     ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE subnets                   ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE route_tables              ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE route_table_associations  ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE network_security_groups   ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE nsg_attachments           ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE peerings                  ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE private_dns_zones         ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE dns_records               ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE service_accounts          ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE key_vaults                ADD COLUMN IF NOT EXISTS tenant_uuid UUID;
ALTER TABLE private_endpoints         ADD COLUMN IF NOT EXISTS tenant_uuid UUID;

CREATE INDEX IF NOT EXISTS idx_resources_tenant_uuid                 ON resources                (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_quotas_tenant_uuid                    ON quotas                   (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_vnets_tenant_uuid                     ON vnets                    (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_subnets_tenant_uuid                   ON subnets                  (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_route_tables_tenant_uuid              ON route_tables             (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_route_table_assoc_tenant_uuid         ON route_table_associations (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_nsg_tenant_uuid                       ON network_security_groups  (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_nsg_attach_tenant_uuid                ON nsg_attachments          (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_peerings_tenant_uuid                  ON peerings                 (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_dns_zones_tenant_uuid                 ON private_dns_zones        (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_dns_records_tenant_uuid               ON dns_records              (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_service_accounts_tenant_uuid          ON service_accounts         (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_key_vaults_tenant_uuid                ON key_vaults               (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_private_endpoints_tenant_uuid         ON private_endpoints        (tenant_uuid);

-- role_assignments uses scope_id (polymorphic — today only scope_type='tenant',
-- M5 will add 'subscription'/'resource_group'). scope_uuid is the immutable
-- reference. For scope_type='tenant' it points at tenants.tenant_uuid.
ALTER TABLE role_assignments ADD COLUMN IF NOT EXISTS scope_uuid UUID;
CREATE INDEX IF NOT EXISTS idx_role_assignments_scope_uuid ON role_assignments (scope_uuid);

-- ─────────────────────────── RBAC v2 (action-based roles) ───────────────────
-- Clean break from the v1 owner/member/viewer rank (no production users):
-- role_assignments.role becomes role_definition, holding a role-definition key —
-- a built-in ('Owner','Contributor','Reader', plus the per-resource-type roles)
-- or a custom role_definitions.id. scope_type also gains 'resource' (free TEXT,
-- no DDL). See docs/rbac-v2.md §8. One-time in-place conversion, no retention.
ALTER TABLE role_assignments ADD COLUMN IF NOT EXISTS role_definition TEXT;
-- Backfill from the v1 column only on upgraded DBs (fresh installs never had it,
-- so referencing `role` unconditionally would error — guard on its existence).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_name = 'role_assignments' AND column_name = 'role') THEN
        UPDATE role_assignments
           SET role_definition = CASE role
               WHEN 'owner'  THEN 'Owner'
               WHEN 'member' THEN 'Contributor'
               WHEN 'viewer' THEN 'Reader'
               ELSE role_definition
           END
         WHERE role_definition IS NULL;
    END IF;
END $$;
-- Retire the v1 rank column (also drops the old inline UNIQUE that referenced it).
ALTER TABLE role_assignments DROP COLUMN IF EXISTS role;
-- New uniqueness keyed on role_definition (serves fresh + migrated installs).
CREATE UNIQUE INDEX IF NOT EXISTS uq_role_assignments_principal_scope_roledef
    ON role_assignments (principal_type, principal_id, scope_type, scope_id, role_definition);

-- Custom (tenant-owned) role definitions. Built-ins live in the Go registry and
-- are resolved by reserved key first; this table holds user-defined roles that
-- the engine resolves identically. UI/CRUD ships in a later phase — the table is
-- created now so the foundation is in place. See docs/rbac-v2.md §8.1.
CREATE TABLE IF NOT EXISTS role_definitions (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_uuid       UUID NOT NULL,
    name              TEXT NOT NULL,
    description       TEXT,
    actions           JSONB NOT NULL DEFAULT '[]',
    not_actions       JSONB NOT NULL DEFAULT '[]',
    data_actions      JSONB NOT NULL DEFAULT '[]',
    not_data_actions  JSONB NOT NULL DEFAULT '[]',
    assignable_scopes JSONB NOT NULL DEFAULT '[]',
    created_by        TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_uuid, name)
);
CREATE INDEX IF NOT EXISTS idx_role_definitions_tenant_uuid ON role_definitions (tenant_uuid);

-- ─────────────────────────── M2.5 Projects hierarchy ─────────────────────────
-- Tenant → Project → Resource. Project is the workspace boundary: own
-- namespace, own ResourceQuota, own membership (project-scoped roles), own
-- resources. Every per-tenant table grows a `project_uuid` column; queries
-- now filter on (tenant_uuid, project_uuid) so resources in different
-- projects can't collide on name uniqueness across the same tenant.
--
-- Why UUID alongside the slug: same reasoning as Phase 6a for tenants —
-- a deleted-then-recycled project slug must not inherit orphan resources.
-- See docs/defense-in-depth.md §6a for the pattern.
--
-- M5 (later): role_assignments.scope_type='project' is the natural
-- extension of the polymorphic scope model — no schema change needed.

CREATE TABLE IF NOT EXISTS projects (
    id           TEXT        NOT NULL,                            -- slug, unique within tenant
    tenant_id    TEXT        NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    tenant_uuid  UUID        NOT NULL,                            -- mirror of tenants.tenant_uuid for fast joins
    project_uuid UUID        NOT NULL DEFAULT gen_random_uuid(),  -- canonical, immutable identity
    name         TEXT        NOT NULL,                            -- display name; defaults to id
    description  TEXT,
    -- Capacity quotas (per project). Object guardrails live in the quotas table.
    cpu_cores    INTEGER     NOT NULL DEFAULT 20,
    memory_gb    INTEGER     NOT NULL DEFAULT 64,
    storage_gb   INTEGER     NOT NULL DEFAULT 500,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by   TEXT        NOT NULL,
    PRIMARY KEY (tenant_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_project_uuid ON projects (project_uuid);
CREATE INDEX        IF NOT EXISTS idx_projects_tenant_uuid  ON projects (tenant_uuid);

DROP TRIGGER IF EXISTS projects_updated_at ON projects;
CREATE TRIGGER projects_updated_at
    BEFORE UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- Object-count guardrails per project (capacity caps live on projects table).
-- Keyed by project_uuid so re-registering a slug never inherits its quotas.
CREATE TABLE IF NOT EXISTS project_quotas (
    project_uuid    UUID PRIMARY KEY REFERENCES projects(project_uuid) ON DELETE CASCADE,
    max_vnets       INTEGER NOT NULL DEFAULT 10,
    max_clusters    INTEGER NOT NULL DEFAULT 2,
    max_volumes     INTEGER NOT NULL DEFAULT 50,
    max_public_ips  INTEGER NOT NULL DEFAULT 3,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Per-tenant resource tables: project_id (slug, nullable for transition only —
-- the projects-stage-2 wipe means every row should have it populated) and
-- project_uuid (FK to projects.project_uuid via the indexed column).
-- ON DELETE RESTRICT — projects must be empty before they can be deleted.
ALTER TABLE resources                ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE resources                ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;
ALTER TABLE vnets                    ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE vnets                    ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;
ALTER TABLE subnets                  ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE subnets                  ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;
ALTER TABLE route_tables             ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE route_tables             ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;
ALTER TABLE route_table_associations ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE route_table_associations ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;
ALTER TABLE network_security_groups  ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE network_security_groups  ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;
ALTER TABLE nsg_attachments          ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE nsg_attachments          ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;
ALTER TABLE peerings                 ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE peerings                 ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;
ALTER TABLE private_dns_zones        ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE private_dns_zones        ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;
ALTER TABLE dns_records              ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE dns_records              ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;
ALTER TABLE service_accounts         ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE service_accounts         ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;
ALTER TABLE key_vaults               ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE key_vaults               ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;
-- M3 chunk 3 / 9d: shown-once flag for the GET .../credentials endpoint.
-- NULL = creds not yet retrieved; timestamp = already consumed → endpoint
-- returns 410 Gone on subsequent calls.
ALTER TABLE key_vaults               ADD COLUMN IF NOT EXISTS credentials_consumed_at TIMESTAMPTZ;
ALTER TABLE private_endpoints        ADD COLUMN IF NOT EXISTS project_id   TEXT;
ALTER TABLE private_endpoints        ADD COLUMN IF NOT EXISTS project_uuid UUID REFERENCES projects(project_uuid) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS idx_resources_project_uuid                 ON resources                (project_uuid);
CREATE INDEX IF NOT EXISTS idx_vnets_project_uuid                     ON vnets                    (project_uuid);
CREATE INDEX IF NOT EXISTS idx_subnets_project_uuid                   ON subnets                  (project_uuid);
CREATE INDEX IF NOT EXISTS idx_route_tables_project_uuid              ON route_tables             (project_uuid);
CREATE INDEX IF NOT EXISTS idx_route_table_assoc_project_uuid         ON route_table_associations (project_uuid);
CREATE INDEX IF NOT EXISTS idx_nsg_project_uuid                       ON network_security_groups  (project_uuid);
CREATE INDEX IF NOT EXISTS idx_nsg_attach_project_uuid                ON nsg_attachments          (project_uuid);
CREATE INDEX IF NOT EXISTS idx_peerings_project_uuid                  ON peerings                 (project_uuid);
CREATE INDEX IF NOT EXISTS idx_dns_zones_project_uuid                 ON private_dns_zones        (project_uuid);
CREATE INDEX IF NOT EXISTS idx_dns_records_project_uuid               ON dns_records              (project_uuid);
CREATE INDEX IF NOT EXISTS idx_service_accounts_project_uuid          ON service_accounts         (project_uuid);
CREATE INDEX IF NOT EXISTS idx_key_vaults_project_uuid                ON key_vaults               (project_uuid);
CREATE INDEX IF NOT EXISTS idx_private_endpoints_project_uuid         ON private_endpoints        (project_uuid);

-- role_assignments: scope_type='project' is the polymorphic extension.
-- scope_id continues to hold the slug; scope_uuid (already added in Phase 6a)
-- holds either tenant_uuid (when scope_type='tenant') or project_uuid (when
-- scope_type='project'). No schema change needed beyond what Phase 6a shipped.

-- ─────────────────────────── M2.5 Tenant capacity caps ──────────────────────
-- Hybrid quota model: platform admin sets per-tenant ceiling at tenant create
-- (or PATCH /v1/admin/tenants/{tid}); tenant owner distributes within via
-- per-project quotas. Defaults are conservative — a 4-medium-project budget —
-- but admins routinely override based on the team's stated need.
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS cpu_cores_cap  INTEGER NOT NULL DEFAULT 80;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS memory_gb_cap  INTEGER NOT NULL DEFAULT 256;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS storage_gb_cap INTEGER NOT NULL DEFAULT 2000;

-- ─────────────────────────── AKS-style node pools ───────────────────────────
-- Each Cluster resource has exactly one system pool (role='system',
-- name='system') and zero or more worker pools (role='worker').
-- The reconciler derives per-pool status from Rancher machinePool conditions.
--
-- cluster_id → resources(id) ON DELETE CASCADE so removing a Cluster row
-- automatically removes its pool rows.
-- (cluster_id, name) UNIQUE enforces pool-name uniqueness within a cluster;
-- the handler maps a violation to 409 Conflict.

-- node_pool_role: system pool runs cp+etcd; worker pool runs workloads only.
DO $$
BEGIN
    CREATE TYPE node_pool_role AS ENUM ('system', 'worker');
EXCEPTION WHEN duplicate_object THEN NULL;
END;
$$;

-- node_pool_status: dc-api-side lifecycle (reconciler derives from Rancher
-- machinePool conditions).
DO $$
BEGIN
    CREATE TYPE node_pool_status AS ENUM (
        'provisioning',
        'ready',
        'scaling',
        'deleting',
        'failed'
    );
EXCEPTION WHEN duplicate_object THEN NULL;
END;
$$;

CREATE TABLE IF NOT EXISTS cluster_node_pools (
    id                       UUID             PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id               UUID             NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    name                     TEXT             NOT NULL,
    role                     node_pool_role   NOT NULL,
    size                     TEXT             NOT NULL,
    count                    INT              NOT NULL CHECK (count >= 1 AND count <= 50),
    disk_gb                  INT,
    taints                   JSONB            NOT NULL DEFAULT '[]'::jsonb,
    labels                   JSONB            NOT NULL DEFAULT '{}'::jsonb,
    harvester_config_name    TEXT             NOT NULL DEFAULT '',
    status                   node_pool_status NOT NULL DEFAULT 'provisioning',
    message                  TEXT,
    created_at               TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    UNIQUE (cluster_id, name)
);

CREATE INDEX IF NOT EXISTS idx_cluster_node_pools_cluster ON cluster_node_pools (cluster_id);

DROP TRIGGER IF EXISTS cluster_node_pools_updated_at ON cluster_node_pools;
CREATE TRIGGER cluster_node_pools_updated_at
    BEFORE UPDATE ON cluster_node_pools
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ─────────────────────────── Task 1 — DBaaS (Databases) ─────────────────────
-- One row per managed Database. 1:1 model — each row maps to exactly one
-- DBInstance CR (group dbaas.opencloud.wso2.com/v1alpha1) in the project
-- namespace, which the dbaas controller provisions as a KubeVirt VM running
-- PostgreSQL. No DatabaseBackend CRD — databases don't share infrastructure
-- (different from key_vaults, which has a per-tenant OpenBao Backend).
--
-- Network mode is per-instance:
--   'vpc'    → vnet_id + subnet_id must be set; the dbaas controller attaches
--              the VM to the KubeOVN-managed NAD at
--              (project-ns, subnets.backend_uid)
--   'legacy' → nad_ref must be set ('<namespace>/<nad-name>' of a pre-existing
--              Multus NAD on a VLAN bridge); used by lk prod today
--
-- engine is postgres-only in v1. Task 2 extends this to 'mysql', 'mssql' via
-- DROP CONSTRAINT / ADD CONSTRAINT on databases_engine_check.
--
-- credentials_consumed_at follows the shown-once pattern from key_vaults:
-- NULL = creds not yet retrieved; timestamp = consumed, GET .../credentials
-- returns 410 Gone on subsequent calls.

CREATE TABLE IF NOT EXISTS databases (
    id                      UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id               TEXT            NOT NULL,
    tenant_uuid             UUID            NOT NULL,
    project_id              TEXT            NOT NULL,
    project_uuid            UUID            NOT NULL REFERENCES projects(project_uuid) ON DELETE RESTRICT,

    name                    TEXT            NOT NULL,
    engine                  TEXT            NOT NULL DEFAULT 'postgres'
        CONSTRAINT databases_engine_check CHECK (engine IN ('postgres')),
    engine_version          TEXT,
    instance_class          TEXT            NOT NULL,
    allocated_storage_gb    INTEGER         NOT NULL CHECK (allocated_storage_gb > 0),

    -- Network selection. Exactly one mode's fields must be populated.
    network_mode            TEXT            NOT NULL CHECK (network_mode IN ('vpc', 'legacy')),
    vnet_id                 UUID,
    subnet_id               UUID,
    nad_ref                 TEXT,

    status                  resource_status NOT NULL DEFAULT 'PENDING',
    message                 TEXT,

    -- Endpoint cache, populated from the live CR status once the controller
    -- has assigned the Multus IP and PostgreSQL is accepting connections.
    endpoint_address        TEXT,
    endpoint_port           INTEGER,

    credentials_consumed_at TIMESTAMPTZ,

    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),

    UNIQUE (project_uuid, name),

    -- Defense-in-depth: handler validates first, but if anything bypasses it
    -- the row still can't be wedged into an invalid network state.
    CONSTRAINT databases_network_fields_check CHECK (
        (network_mode = 'vpc'
            AND vnet_id IS NOT NULL AND subnet_id IS NOT NULL
            AND nad_ref IS NULL)
        OR
        (network_mode = 'legacy'
            AND nad_ref IS NOT NULL
            AND vnet_id IS NULL AND subnet_id IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_databases_tenant_uuid  ON databases (tenant_uuid);
CREATE INDEX IF NOT EXISTS idx_databases_project_uuid ON databases (project_uuid);

DROP TRIGGER IF EXISTS databases_updated_at ON databases;
CREATE TRIGGER databases_updated_at
    BEFORE UPDATE ON databases
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ─────────────────────── Activity feed durability ────────────────────────────
-- audit_events originally carried only resource_id behind an ON DELETE CASCADE
-- FK, so deleting a resource erased its entire history — including the DELETE
-- event itself. Snapshot the resource identity onto each event at write time
-- and drop the FK: the feed reads the snapshot, so history outlives the
-- resource.
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS resource_name TEXT;
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS resource_type TEXT;
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS tenant_uuid  UUID;
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS project_uuid UUID;
ALTER TABLE audit_events ALTER COLUMN resource_id DROP NOT NULL;

ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_resource_id_fkey;
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_resource_id_setnull_fkey;
-- The FK to resources is dropped entirely (both historical names): the
-- activity framework audits EVERY resource family (vnets, subnets, NSGs,
-- peerings, DNS zones, key vaults, databases, private endpoints), whose
-- UUIDs live in their own tables. resource_id is a point-in-time pointer —
-- it may reference a row that has since been deleted; the snapshot columns
-- are the rendering truth.

-- Backfill snapshots for pre-migration events whose resource still exists
-- (events whose resource was already deleted have cascaded away — nothing
-- left to backfill).
UPDATE audit_events ae
SET    resource_name = res.name,
       resource_type = res.type::text,
       tenant_uuid   = res.tenant_uuid,
       project_uuid  = res.project_uuid
FROM   resources res
WHERE  ae.resource_id = res.id AND ae.resource_name IS NULL;

CREATE INDEX IF NOT EXISTS idx_audit_project_time ON audit_events (project_uuid, created_at DESC);

-- ─────────────────────── Multi-region foundation (phase 0) ───────────────────
-- Extends the existing `regions` catalog (seeded above) with zones, dc-agent
-- connection records, and agent auth tokens. Zones are availability domains
-- within a region; each zone is served by one dc-agent that dials in over the
-- /v1/agent/ws WebSocket. Zone/region health is DERIVED from agents.last_seen
-- (no stored status column — status decays naturally when an agent goes quiet).
--
-- These are operator/platform tables, not tenant resource families — they are
-- deliberately NOT wired into the audit/activity framework (see activity.go).

ALTER TABLE regions ADD COLUMN IF NOT EXISTS display_name TEXT;

-- ── Zones ─────────────────────────────────────────────────────────────────────
-- One row per availability zone within a region. Operator-managed, like the
-- regions catalog.
CREATE TABLE IF NOT EXISTS zones (
    region_name TEXT        NOT NULL REFERENCES regions(name) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    description TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (region_name, name)
);

-- Seed: the lk region's single zone (mirrors the 'lk' region seed precedent).
INSERT INTO zones (region_name, name, description)
VALUES ('lk', 'zone-1', 'Default zone for region lk')
ON CONFLICT (region_name, name) DO NOTHING;

-- ── Agents ────────────────────────────────────────────────────────────────────
-- One row per (region, zone) dc-agent. The /v1/agent/ws handler UPSERTs on
-- hello (refreshing version/remote_addr/connected_at) and bumps last_seen on
-- every frame. Nothing is written on disconnect — health is computed from
-- last_seen age by GET /v1/regions.
CREATE TABLE IF NOT EXISTS agents (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    region_name  TEXT        NOT NULL,
    zone_name    TEXT        NOT NULL,
    version      TEXT,
    remote_addr  TEXT,
    connected_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- UNIQUE: one agent row per zone, and the upsert target for the hello frame.
CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_region_zone ON agents (region_name, zone_name);

-- ── Agent tokens ──────────────────────────────────────────────────────────────
-- Bearer credentials for dc-agents (format: "dcagent_<random>"). Only the
-- sha256 hex digest is stored; the raw token is returned exactly once by
-- POST /v1/admin/regions/{region}/zones/{zone}/agent-token.
CREATE TABLE IF NOT EXISTS agent_tokens (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    region_name  TEXT        NOT NULL,
    zone_name    TEXT        NOT NULL,
    token_hash   TEXT        NOT NULL UNIQUE,  -- sha256 hex of the raw token
    created_by   TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ
);

-- ── Phase-0 region stamps on existing resource families ─────────────────────
-- Every row records which region's control plane created it. New rows are
-- stamped from DCAPI_LOCAL_REGION in the repo Create* methods; pre-existing
-- rows are backfilled to 'lk' (matches the seeded-region precedent). The
-- UPDATEs are guarded on `region IS NULL` so re-runs are no-ops.
-- (vnets already carry a region column; subnets/peerings/DNS zones derive
-- theirs from the parent VNet by containment.)
ALTER TABLE resources         ADD COLUMN IF NOT EXISTS region TEXT;
ALTER TABLE key_vaults        ADD COLUMN IF NOT EXISTS region TEXT;
ALTER TABLE databases         ADD COLUMN IF NOT EXISTS region TEXT;
ALTER TABLE private_endpoints ADD COLUMN IF NOT EXISTS region TEXT;

UPDATE resources         SET region = 'lk' WHERE region IS NULL;
UPDATE key_vaults        SET region = 'lk' WHERE region IS NULL;
UPDATE databases         SET region = 'lk' WHERE region IS NULL;
UPDATE private_endpoints SET region = 'lk' WHERE region IS NULL;
