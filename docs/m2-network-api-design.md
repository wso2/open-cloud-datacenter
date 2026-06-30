# M2 Networking API Design

**Status:** Design — ready for backend implementation
**Author:** api-designer agent
**Date:** 2026-05-06
**Implements:** M2 Networking from MILESTONES.md
**Backend mapping:** `docs/m2-network-api-mapping.md`

---

## Table of Contents

1. [API Conventions](#1-api-conventions)
2. [Reserved CIDR Configuration](#2-reserved-cidr-configuration)
3. [Status Lifecycle](#3-status-lifecycle)
4. [VNet](#4-vnet)
5. [Subnet](#5-subnet)
6. [RouteTable](#6-routetable)
7. [NetworkSecurityGroup](#7-networksecuritygroup)
8. [VNetPeering](#8-vnetpeering)
9. [NatGateway](#9-natgateway)
10. [PublicIp](#10-publicip)
11. [PrivateDnsZone](#11-privatednzone)
12. [DNS Records](#12-dns-records)
13. [Design Decisions](#13-design-decisions)
14. [Open Questions](#14-open-questions)

---

## 1. API Conventions

These conventions apply to every endpoint in this document. They match the
established pattern from `vm.go` and `cluster.go`.

**Authentication.** Every `/v1/*` endpoint requires a valid Asgardeo JWT in the
`Authorization: Bearer <token>` header. The auth middleware resolves `tenant_id`
and `user_id` from the token and injects them into the request context. Handlers
read from context only — never from the request body.

**Async create.** `POST` on any network resource returns `202 Accepted`
immediately. The response body contains the full resource object at `status:
PENDING` plus a `note` field with the polling URL. The caller polls
`GET /{resource}/{id}` until `status` is `ACTIVE` or `FAILED`.

**Synchronous resources.** A small number of purely data-plane-light operations
(DNS record creation, RouteTable, NSG rule changes) can be synchronous because
the KubeOVN CRD patch completes in < 2 seconds. These are noted per-resource.
When in doubt, async is safer.

**Tenant isolation.** List and Get always filter by `tenant_id` from context.
A 404 is returned for any resource that does not belong to the requesting tenant
— indistinguishable from "not found", preventing enumeration attacks.

**Error format.** Flat envelope, no nesting:

```json
{"error": "human-readable message describing exactly what is wrong"}
```

**Status codes.**

| Code | Meaning |
|------|---------|
| `200` | OK — GET or synchronous operation succeeded |
| `202` | Accepted — async operation started; poll for completion |
| `400` | Bad request — validation failure; error message is actionable |
| `401` | Unauthorized — no or invalid token |
| `403` | Forbidden — quota exceeded or insufficient role |
| `404` | Not found — resource does not exist or belongs to another tenant |
| `409` | Conflict — name collision or state conflict (e.g. deleting a VNet with active subnets) |
| `500` | Server error — log entry created; surface a ticket reference if available |

**Pagination.** Deferred. All list endpoints return a flat JSON array for M2.
Pagination query params (`?limit=` and `?cursor=`) will be added in M3 as a
non-breaking addition.

**Timestamps.** All timestamps are RFC3339 strings (`created_at`, `updated_at`).
IDs are UUIDs v4.

---

## 2. Reserved CIDR Configuration

DC-API validates every tenant-supplied CIDR against a per-region reserved list
before creating a VNet or Subnet.

### What is reserved

- The Harvester management network (e.g. `192.168.10.0/24` in lk). Overlap
  breaks NAT and routing in subtle ways — DNS, corporate-network reachability,
  audit traffic. Reject outright.
- The Kubernetes service CIDR of the Harvester cluster (RKE2 default:
  `10.43.0.0/16`).
- The Kubernetes pod CIDR of the Harvester cluster (RKE2 default:
  `10.42.0.0/16`).
- Any CIDR in the per-region operator-defined `DCAPI_RESERVED_CIDRS` list.
- Loopback `127.0.0.0/8`, link-local `169.254.0.0/16`, and any non-RFC1918
  address space. Tenant VNets are RFC1918 only: `10.0.0.0/8`,
  `172.16.0.0/12`, `192.168.0.0/16`.

### Configuration

Reserved CIDRs are configured via a single environment variable:

```
DCAPI_RESERVED_CIDRS=192.168.10.0/24,10.42.0.0/16,10.43.0.0/16
```

The variable accepts a comma-separated list of CIDR blocks. It is read at
startup into the config struct alongside the existing `DCAPI_*` vars. Each
region's deployment sets its own value in `deploy/configmap.yaml`.

When validation fails, the 400 error message names the conflicting reserved
range:

```json
{"error": "CIDR 10.42.5.0/24 overlaps reserved range 10.42.0.0/16 (Kubernetes pod CIDR)"}
```

The label in the error message (the parenthetical) is configured alongside
the CIDR; format is `DCAPI_RESERVED_CIDRS=10.42.0.0/16:pod-cidr,...`.
The handler formats it as "reserved range `<cidr>` (`<label>`)".

---

## 3. Status Lifecycle

All network resources follow the same lifecycle as VMs and clusters:

```
PENDING → ACTIVE      provisioning completed successfully
PENDING → FAILED      provisioning failed; message field contains reason
ACTIVE  → DELETING    DELETE requested
DELETING → (row removed)  reconciler confirms provider deletion
DELETING → FAILED     deletion failed; manual remediation required
```

MAC addresses, IP allocations, and other backend assignments are internal to the
kubeovn driver. They are never surfaced in the API response. What is surfaced is
the resource `status` and a human-readable `message` describing the last
transition.

---

## 4. VNet

A VNet is the top-level isolation boundary for tenant networking. It maps to a
KubeOVN `Vpc` CRD. The `address_space` is enforced by DC-API only — KubeOVN's
Vpc CRD has no CIDR field. See `m2-network-api-mapping.md` — "The headline
insight" section.

### Endpoints

| Method | Path | Description | Success code |
|--------|------|-------------|--------------|
| `POST` | `/v1/vnets` | Create a VNet (async) | 202 |
| `GET` | `/v1/vnets` | List VNets for tenant | 200 |
| `GET` | `/v1/vnets/{vnet_id}` | Get VNet by ID | 200 |
| `DELETE` | `/v1/vnets/{vnet_id}` | Delete VNet (async) | 202 |

### Create request

```json
{
  "name": "prod-vnet",
  "address_space": ["10.1.0.0/16"],
  "region": "lk",
  "zone": "zone-1",
  "description": "Production VNet for team-alpha"
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `name` | string | yes | Unique within tenant; alphanumeric + hyphen |
| `address_space` | []string | yes | One or more RFC1918 CIDRs. At least one required. |
| `region` | string | yes | Must match a known region (e.g. `lk`). Single-region for M2. |
| `zone` | string | no | Availability zone within the region (e.g. `zone-1`). Defaults to the control plane's local zone. Immutable after create. Child resources (subnets, VMs, clusters) inherit this zone. |
| `description` | string | no | Free-text; max 256 chars |

### Create response (202)

```json
{
  "resource": {
    "id": "a1b2c3d4-0000-0000-0000-000000000001",
    "name": "prod-vnet",
    "address_space": ["10.1.0.0/16"],
    "region": "lk",
    "zone": "zone-1",
    "description": "Production VNet for team-alpha",
    "status": "PENDING",
    "tenant_id": "team-alpha",
    "created_at": "2026-05-06T10:00:00Z",
    "updated_at": "2026-05-06T10:00:00Z"
  },
  "note": "VNet is being provisioned. Poll GET /v1/vnets/a1b2c3d4-0000-0000-0000-000000000001 for status."
}
```

### Get / List response (200)

`GET /v1/vnets/{id}` returns the `resource` object above (without the `note`
wrapper). `GET /v1/vnets` returns a flat JSON array of resource objects.

### Validation rules

- `name`: required, 1-63 characters, `[a-z0-9-]` only, must start with a letter.
- `address_space`: at least one CIDR; each CIDR must be valid, RFC1918, not
  overlap a reserved range (section 2), not `/32` or smaller. Maximum 5 CIDRs
  per VNet for M2.
- `region`: must match a value in `DCAPI_REGIONS` config (default: `lk`).
- Duplicate name within a tenant → 409.
- Quota: `max_vnets` per tenant. Default `10` (see § 14 Quotas). Rejection → 403.

### Special semantics

**Address space mutability.** For M2, `address_space` is immutable after create.
This is the safe default — KubeOVN Vpc has no CIDR, so DC-API is the enforcement
point; allowing expansion post-create requires checking all existing subnets and
peerings, which is a distinct feature. Add CIDRs via `PATCH /v1/vnets/{id}` in
M3 once the use case is proven necessary.

**Delete guard.** `DELETE /v1/vnets/{vnet_id}` returns 409 if the VNet has any
active Subnets, RouteTables, Peerings, or NatGateways. The error message lists
the blocking resources. Callers must delete dependents first. This mirrors Azure
behaviour and prevents orphaned KubeOVN CRDs.

### KubeOVN backend

Creates a KubeOVN `Vpc` CRD named after the resource UUID. `address_space` is
stored only in DC-API's DB. Deletion issues a KubeOVN `Vpc` delete; the driver
must first confirm no `Subnet` CRDs reference the VPC (the finalizer will block
if it doesn't).

---

## 5. Subnet

A Subnet carves a CIDR from a parent VNet. It maps to a KubeOVN `Subnet` CRD.
KubeOVN enforces no-overlap within the same VPC; DC-API additionally enforces
CIDR containment within the parent VNet's `address_space`.

### Endpoints

| Method | Path | Description | Success code |
|--------|------|-------------|--------------|
| `POST` | `/v1/vnets/{vnet_id}/subnets` | Create a Subnet (async) | 202 |
| `GET` | `/v1/vnets/{vnet_id}/subnets` | List Subnets in a VNet | 200 |
| `GET` | `/v1/vnets/{vnet_id}/subnets/{subnet_id}` | Get Subnet by ID | 200 |
| `DELETE` | `/v1/vnets/{vnet_id}/subnets/{subnet_id}` | Delete Subnet (async) | 202 |

### Create request

```json
{
  "name": "app-subnet",
  "cidr": "10.1.1.0/24",
  "gateway": "10.1.1.1",
  "description": "Application tier subnet"
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `name` | string | yes | Unique within the VNet |
| `cidr` | string | yes | Must be contained in parent VNet's `address_space` |
| `gateway` | string | no | Defaults to first usable IP in the CIDR (e.g. `10.1.1.1` for `/24`) |
| `description` | string | no | Free-text; max 256 chars |

### Create response (202)

```json
{
  "resource": {
    "id": "a1b2c3d4-0000-0000-0000-000000000002",
    "vnet_id": "a1b2c3d4-0000-0000-0000-000000000001",
    "name": "app-subnet",
    "cidr": "10.1.1.0/24",
    "gateway": "10.1.1.1",
    "description": "Application tier subnet",
    "status": "PENDING",
    "tenant_id": "team-alpha",
    "created_at": "2026-05-06T10:01:00Z",
    "updated_at": "2026-05-06T10:01:00Z"
  },
  "note": "Subnet is being provisioned. Poll GET /v1/vnets/.../subnets/a1b2c3d4-0000-0000-0000-000000000002 for status."
}
```

### Validation rules

- `cidr`: must be a valid CIDR, must be contained in at least one entry of the
  parent VNet's `address_space`, must not overlap any sibling Subnet CIDR in the
  same VNet (checked in DB before hitting KubeOVN), must be no larger than `/8`
  and no smaller than `/28`.
- `gateway`: if provided, must be a valid IP within `cidr` and not the network
  or broadcast address. If omitted, defaults to first usable IP.
- VNet must be `ACTIVE` at creation time. Creating a subnet under a `PENDING`
  VNet → 409 with a message telling the caller to wait.
- Duplicate name within the VNet → 409.

### Special semantics

**CIDR immutability.** Subnet CIDR is immutable after create. Resize is not
supported in M2 — it would require draining all VMs and reassigning IPs,
which is a disruptive operation. Tenants create a new subnet and migrate VMs.

**Delete guard.** Returns 409 if any VMs or clusters are attached to the subnet.
The error message lists attached resource IDs. VM attachment is tracked via a
`subnet_id` foreign key on the resource row (added in M2 DB migration).

### KubeOVN backend

Creates a `Subnet` CRD inside the parent `Vpc`. The CRD's `spec.cidrBlock`,
`spec.gateway`, and `spec.vpc` are set from the request. The `NetworkAttachmentDefinition`
(NAD) for multus is also created in the same operation — the NAD is what the
Harvester VM driver references when attaching a VM to this subnet.

---

## 6. RouteTable

A RouteTable holds static routing rules for a VNet. It maps to routes on
KubeOVN's `Vpc.spec.staticRoutes`. Multiple RouteTables can exist per VNet;
their route entries are concatenated on the VPC. Deletion removes only the
entries belonging to that RouteTable.

**Important constraint from `m2-network-api-mapping.md`:** KubeOVN's logical
router is per-VPC, not per-subnet. Two subnets in the same VNet cannot have
meaningfully different route tables — all routes are applied at the VPC router
level, not at subnet boundaries. DC-API exposes RouteTable as an object for
Azure-API parity, but the per-subnet routing granularity implied by Azure is not
implemented. See section 13 for the design decision.

### Endpoints

| Method | Path | Description | Success code |
|--------|------|-------------|--------------|
| `POST` | `/v1/vnets/{vnet_id}/route-tables` | Create a RouteTable (sync) | 201 |
| `GET` | `/v1/vnets/{vnet_id}/route-tables` | List RouteTables | 200 |
| `GET` | `/v1/vnets/{vnet_id}/route-tables/{rt_id}` | Get RouteTable by ID | 200 |
| `PUT` | `/v1/vnets/{vnet_id}/route-tables/{rt_id}` | Replace route list (sync) | 200 |
| `DELETE` | `/v1/vnets/{vnet_id}/route-tables/{rt_id}` | Delete RouteTable (sync) | 204 |

RouteTable create and delete are **synchronous** (201/204, not 202) because the
operation is a single PATCH to the parent `Vpc` CRD, which KubeOVN applies
immediately. No reconciler loop is needed.

### Create request

```json
{
  "name": "default-rt",
  "routes": [
    {
      "name": "to-internet",
      "destination_cidr": "0.0.0.0/0",
      "next_hop_type": "internet",
      "next_hop_ip": null
    },
    {
      "name": "to-appliance",
      "destination_cidr": "10.99.0.0/16",
      "next_hop_type": "virtual_appliance",
      "next_hop_ip": "10.1.1.250"
    }
  ]
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `name` | string | yes | Unique within the VNet |
| `routes` | []Route | no | Empty list is valid (table with no rules) |
| `routes[].name` | string | yes | Unique within the route table |
| `routes[].destination_cidr` | string | yes | Valid CIDR or `0.0.0.0/0` |
| `routes[].next_hop_type` | string | yes | `vnet_local`, `internet`, `virtual_appliance`, `none` |
| `routes[].next_hop_ip` | string | conditional | Required when `next_hop_type` is `virtual_appliance` |

### Validation rules

- `next_hop_ip` must be a valid IP and must fall within a subnet of this VNet
  when `next_hop_type` is `virtual_appliance`.
- `destination_cidr`: must be a valid CIDR or `0.0.0.0/0`. No uniqueness check
  across routes in the same table (same behaviour as Azure — last-match wins at
  the data plane level).
- VNet must be `ACTIVE`.
- Duplicate route table name within the VNet → 409.

### KubeOVN backend

On create, the driver reads the current `Vpc.spec.staticRoutes`, appends the new
entries (tagged with a comment containing the route-table UUID for identification),
and PATCHes the VPC. On delete, it reads, filters out entries tagged with the
route-table UUID, and PATCHes. This is the patch-not-delete pattern from gotcha
4 in `m2-network-api-mapping.md`.

---

## 7. NetworkSecurityGroup

An NSG defines a named set of security rules (inbound/outbound). It can be
associated with Subnets (subnet-level stateless ACLs via `Subnet.spec.acls`) or
NICs/VMs (VM-level stateful rules via KubeOVN `SecurityGroup` CRD). A single NSG
can have multiple associations.

NSG create and rule updates are **synchronous**. Association/detachment are
**synchronous** (the KubeOVN patch lands in under 2 seconds). Neither needs the
reconciler.

### Endpoints

| Method | Path | Description | Success code |
|--------|------|-------------|--------------|
| `POST` | `/v1/security-groups` | Create NSG (sync) | 201 |
| `GET` | `/v1/security-groups` | List NSGs for tenant | 200 |
| `GET` | `/v1/security-groups/{sg_id}` | Get NSG by ID | 200 |
| `PUT` | `/v1/security-groups/{sg_id}/rules` | Replace rule set (sync) | 200 |
| `POST` | `/v1/security-groups/{sg_id}/attachments` | Attach NSG to a target (sync) | 201 |
| `DELETE` | `/v1/security-groups/{sg_id}/attachments/{attachment_id}` | Detach NSG (sync) | 204 |
| `DELETE` | `/v1/security-groups/{sg_id}` | Delete NSG (sync) | 204 |

The attachment sub-endpoint is separate from the NSG body — see section 13 for
the design decision and reasoning.

### NSG create request

```json
{
  "name": "web-tier-sg",
  "description": "Allow inbound 443, deny all else",
  "rules": [
    {
      "name": "allow-https-in",
      "direction": "inbound",
      "priority": 100,
      "protocol": "tcp",
      "source_address_prefix": "*",
      "source_port_range": "*",
      "destination_address_prefix": "*",
      "destination_port_range": "443",
      "action": "allow"
    },
    {
      "name": "deny-all-in",
      "direction": "inbound",
      "priority": 4096,
      "protocol": "*",
      "source_address_prefix": "*",
      "source_port_range": "*",
      "destination_address_prefix": "*",
      "destination_port_range": "*",
      "action": "deny"
    }
  ]
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `name` | string | yes | Unique within tenant |
| `description` | string | no | |
| `rules` | []Rule | no | Empty is valid (no-op NSG) |
| `rules[].name` | string | yes | Unique within the NSG |
| `rules[].direction` | string | yes | `inbound` or `outbound` |
| `rules[].priority` | int | yes | 100–4096; lower is higher priority |
| `rules[].protocol` | string | yes | `tcp`, `udp`, `icmp`, `*` |
| `rules[].source_address_prefix` | string | yes | CIDR, `*`, or `VnetLocal` |
| `rules[].source_port_range` | string | yes | Port, `*`, or range `1024-2048` |
| `rules[].destination_address_prefix` | string | yes | Same as source |
| `rules[].destination_port_range` | string | yes | Same as source_port_range |
| `rules[].action` | string | yes | `allow` or `deny` |

### NSG create response (201)

```json
{
  "id": "a1b2c3d4-0000-0000-0000-000000000010",
  "name": "web-tier-sg",
  "description": "Allow inbound 443, deny all else",
  "rules": [ ... ],
  "attachments": [],
  "tenant_id": "team-alpha",
  "created_at": "2026-05-06T10:03:00Z",
  "updated_at": "2026-05-06T10:03:00Z"
}
```

### Attachment create request

`POST /v1/security-groups/{sg_id}/attachments`

```json
{
  "target_type": "subnet",
  "target_id": "a1b2c3d4-0000-0000-0000-000000000002"
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `target_type` | string | yes | `subnet` (M2) or `nic` (M3 — see below) |
| `target_id` | string | yes | UUID of the Subnet (or VM, when `nic` ships in M3) |

**The two attachment modes — what's in M2 vs deferred:**

- **`target_type: subnet` is the AWS-NACL-equivalent path.** Stateless ACL
  applied at the subnet boundary. The driver writes the NSG's rules to the
  parent Subnet's `spec.acls` field. This is **fully implemented in M2** and
  was proven by spike gate 4 (apply ACL → cross-subnet traffic blocked →
  remove ACL → traffic restored, deterministically).
- **`target_type: nic` is the AWS-Security-Group-equivalent path.** Stateful,
  per-instance, applied via the KubeOVN `SecurityGroup` CRD plus a
  `ovn.kubernetes.io/security_groups: <sg-name>` annotation on the VM's pod.
  This **requires NIC to be a first-class resource** so an NSG can target
  "VM-X's NIC-Y" specifically. NIC modelling is deferred to M3 (see § 14
  "Public IPs and NICs — deferred to M3"), so NIC-level NSG attachment goes
  with it.

**M2 handler behaviour:** `target_type: nic` is documented in the API spec
for forward compatibility (so M3 doesn't break clients) but the M2 handler
rejects it with HTTP 400 and message `"NIC-level NSG attachment is deferred
to M3 — use target_type: subnet for M2"`. Subnet-level attachments work
normally.

### Attachment response (201)

```json
{
  "id": "a1b2c3d4-0000-0000-0000-000000000011",
  "sg_id": "a1b2c3d4-0000-0000-0000-000000000010",
  "target_type": "subnet",
  "target_id": "a1b2c3d4-0000-0000-0000-000000000002",
  "created_at": "2026-05-06T10:04:00Z"
}
```

### Validation rules

- `priority`: unique within `direction` in the same NSG. Duplicate priority in
  same direction → 409.
- `target_id` must belong to the same tenant. Cross-tenant attachment → 404
  (indistinguishable from not-found, preventing enumeration).
- Attaching a subnet-type NSG to a VM resource → 400: "target_type subnet
  requires target_id to be a subnet UUID".
- NSG with active attachments cannot be deleted → 409 listing attachment IDs.
- ACL rule changes are PATCH operations on the Subnet CRD per gotcha 4 from
  `m2-network-api-mapping.md` — the driver must never delete the Subnet CRD to
  remove ACLs.

### KubeOVN backend

`target_type: subnet` → the driver writes rules as `Subnet.spec.acls` entries on
the named KubeOVN Subnet (stateless, OVN ACL syntax). `target_type: nic` → the
driver creates or updates a `SecurityGroup` CRD and annotates the target VM pod.
Rule update is always a full replacement (PUT on the rules list); the driver reads
current state, replaces, and PATCHes the CRD.

---

## 8. VNetPeering

A VNetPeering creates a routed L3 link between two VNets in the same tenant
subscription. Both VNets must be ACTIVE and have non-overlapping address spaces.

**Explicit non-feature: cross-subscription peering is not supported in M2 (or
planned for M3).** This is the same as Azure's original peering scope. The API
makes this clear: `peer_vnet_id` must resolve to a VNet owned by the same
tenant. The error on a cross-tenant peer attempt is 404, not 403 — the target
VNet is invisible to the requesting tenant.

### Endpoints

| Method | Path | Description | Success code |
|--------|------|-------------|--------------|
| `POST` | `/v1/vnets/{vnet_id}/peerings` | Create peering (async) | 202 |
| `GET` | `/v1/vnets/{vnet_id}/peerings` | List peerings for a VNet | 200 |
| `GET` | `/v1/vnets/{vnet_id}/peerings/{peering_id}` | Get peering by ID | 200 |
| `DELETE` | `/v1/vnets/{vnet_id}/peerings/{peering_id}` | Delete peering (async) | 202 |

### Create request

```json
{
  "name": "prod-to-shared",
  "peer_vnet_id": "a1b2c3d4-0000-0000-0000-000000000020",
  "allow_forwarded_traffic": false
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `name` | string | yes | Unique within the requesting VNet |
| `peer_vnet_id` | string | yes | UUID of the target VNet (must be same tenant) |
| `allow_forwarded_traffic` | bool | no | Default false. When true, VNet-to-VNet forwarding is allowed. Implemented in M2 as a no-op annotation; enforced in M3. |

### Create response (202)

```json
{
  "resource": {
    "id": "a1b2c3d4-0000-0000-0000-000000000030",
    "name": "prod-to-shared",
    "vnet_id": "a1b2c3d4-0000-0000-0000-000000000001",
    "peer_vnet_id": "a1b2c3d4-0000-0000-0000-000000000020",
    "allow_forwarded_traffic": false,
    "status": "PENDING",
    "tenant_id": "team-alpha",
    "created_at": "2026-05-06T10:05:00Z",
    "updated_at": "2026-05-06T10:05:00Z"
  },
  "note": "Peering is being configured. Poll GET /v1/vnets/.../peerings/a1b2c3d4-0000-0000-0000-000000000030 for status."
}
```

### Validation rules

- `peer_vnet_id`: must be a valid UUID resolving to an ACTIVE VNet owned by the
  same tenant. Returns 404 if the VNet doesn't exist or belongs to another
  tenant.
- Address spaces of the two VNets must not overlap. Check at create time; reject
  with 400: "VNet address spaces overlap: 10.1.0.0/16 (prod-vnet) and
  10.1.128.0/17 (shared-vnet)".
- A peering in either direction already exists between the two VNets → 409.
- Both VNets must be in the same region for M2 (multi-region peering is out of
  scope per `m2-network-api-mapping.md`).
- Both VNets must be in the same zone (cross-zone peering is not supported).

### Special semantics

**Peering is directional in the API but bi-directional in the backend.** When a
tenant calls `POST /v1/vnets/A/peerings` with `peer_vnet_id=B`, DC-API creates
one peering record in its DB and the driver creates the KubeOVN `VpcPeering` CRD
plus reciprocal `staticRoutes` entries on both Vpc CRDs. The tenant sees one
peering on VNet A; there is no separate peering object on VNet B (unlike Azure
which requires two peering objects — one per side). This is a simpler UX
justified by the fact that both VNets are in the same subscription.

**Audit log.** Both VNet IDs are recorded in the audit event for the peering
operation. This is important for compliance tracing.

### KubeOVN backend

Creates a `VpcPeering` CRD and adds reciprocal `staticRoutes` entries to both
VPC CRDs (patch, not delete). On deletion, removes the `VpcPeering` CRD and
patches the routes out of both VPCs. The driver tags route entries with the
peering UUID in a comment field so they can be reliably removed.

---

## 9. NatGateway — DEFERRED TO M3

> **Status (locked 2026-05-06):** Like PublicIp, NatGateway is removed from
> the M2 API surface. The driver path that allocates external IPs and binds
> them to a `VpcNatGateway` CRD is dropped. The endpoints below stay
> documented as the forward-reference for M3. See § 14 "Public IPs and NICs —
> deferred to M3".

A NatGateway provides SNAT for outbound traffic from one or more subnets to the
internet via a PublicIp. It maps to a KubeOVN `VpcNatGateway` CRD using
`OvnEip` (not `IptablesEip`). `OvnEip` is the preferred mode since KubeOVN
v1.15 and is the only mode supported in DC-API.

### Endpoints

| Method | Path | Description | Success code |
|--------|------|-------------|--------------|
| `POST` | `/v1/vnets/{vnet_id}/nat-gateways` | Create NatGateway (async) | 202 |
| `GET` | `/v1/vnets/{vnet_id}/nat-gateways` | List NatGateways in a VNet | 200 |
| `GET` | `/v1/vnets/{vnet_id}/nat-gateways/{ngw_id}` | Get NatGateway by ID | 200 |
| `DELETE` | `/v1/vnets/{vnet_id}/nat-gateways/{ngw_id}` | Delete NatGateway (async) | 202 |

### Create request

```json
{
  "name": "prod-nat",
  "public_ip_id": "a1b2c3d4-0000-0000-0000-000000000040",
  "subnet_associations": [
    "a1b2c3d4-0000-0000-0000-000000000002"
  ]
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `name` | string | yes | Unique within the VNet |
| `public_ip_id` | string | yes | UUID of an unassociated PublicIp owned by the tenant |
| `subnet_associations` | []string | yes | At least one Subnet UUID from this VNet |

### Create response (202)

```json
{
  "resource": {
    "id": "a1b2c3d4-0000-0000-0000-000000000050",
    "name": "prod-nat",
    "vnet_id": "a1b2c3d4-0000-0000-0000-000000000001",
    "public_ip_id": "a1b2c3d4-0000-0000-0000-000000000040",
    "subnet_associations": ["a1b2c3d4-0000-0000-0000-000000000002"],
    "status": "PENDING",
    "tenant_id": "team-alpha",
    "created_at": "2026-05-06T10:06:00Z",
    "updated_at": "2026-05-06T10:06:00Z"
  },
  "note": "NatGateway is being provisioned. Poll GET /v1/vnets/.../nat-gateways/a1b2c3d4-0000-0000-0000-000000000050 for status."
}
```

### Validation rules

- `public_ip_id`: must be an ACTIVE PublicIp owned by the same tenant with
  `associated_to` null (not already in use).
- Each subnet in `subnet_associations` must belong to this VNet and be ACTIVE.
- A Subnet can be associated with at most one NatGateway. Attempting a second
  association → 409.
- VNet must be ACTIVE.

### Special semantics

**The API does not expose EIP addresses or MAC assignments.** The KubeOVN driver
handles EIP allocation internally. The tenant sees only the `public_ip_id`
reference; the actual IP value is on the PublicIp resource.

### KubeOVN backend

Creates a `VpcNatGateway` CRD linked to an `OvnEip` (pre-allocated when the
PublicIp resource was created). Subnet associations configure SNAT rules on the
gateway. The driver stores the `VpcNatGateway` name as `backend_uid`.

---

## 10. PublicIp — DEFERRED TO M3

> **Status (locked 2026-05-06):** The entire `PublicIp` resource is removed from
> the M2 API surface. The endpoints below are documented as a forward-reference
> for M3 implementation; they are NOT to be implemented in M2. Reason: lk
> has no public IP pool. See § 14 "Public IPs and NICs — deferred to M3".

A PublicIp represents an externally routable IP address from a pre-configured
operator-defined pool. Tenants allocate an address from the pool; they do not
choose a specific IP.

**Operator constraint.** IP pools are defined per-region by the platform operator
in `DCAPI_PUBLIC_IP_POOL_<REGION>=192.0.2.0/28`. Tenants cannot pick an IP from
outside the pool. The available pool is read-only from the tenant perspective;
the API does not expose which specific IPs are still free (only whether an
allocation succeeded).

### Endpoints

| Method | Path | Description | Success code |
|--------|------|-------------|--------------|
| `POST` | `/v1/public-ips` | Allocate a PublicIp (async) | 202 |
| `GET` | `/v1/public-ips` | List PublicIps for tenant | 200 |
| `GET` | `/v1/public-ips/{ip_id}` | Get PublicIp by ID | 200 |
| `DELETE` | `/v1/public-ips/{ip_id}` | Release PublicIp (async) | 202 |

### Create request

```json
{
  "name": "prod-egress-ip",
  "allocation_method": "static",
  "pool": "default"
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `name` | string | yes | Unique within tenant |
| `allocation_method` | string | yes | `static` or `dynamic`. In M2 both result in a static KubeOVN EIP — `dynamic` is reserved for future DHCP-style allocation |
| `pool` | string | no | Which pool to allocate from. Default: `default`. Pool names are configured per-region by the operator. |

### Create response (202)

```json
{
  "resource": {
    "id": "a1b2c3d4-0000-0000-0000-000000000040",
    "name": "prod-egress-ip",
    "allocation_method": "static",
    "pool": "default",
    "ip_address": null,
    "associated_to": null,
    "status": "PENDING",
    "tenant_id": "team-alpha",
    "created_at": "2026-05-06T10:07:00Z",
    "updated_at": "2026-05-06T10:07:00Z"
  },
  "note": "PublicIp is being allocated. Poll GET /v1/public-ips/a1b2c3d4-0000-0000-0000-000000000040 for status."
}
```

Once ACTIVE, `ip_address` is populated with the assigned address (e.g.
`"192.0.2.5"`).

### Validation rules

- `pool`: must be a known pool name configured in `DCAPI_PUBLIC_IP_POOLS`.
  Unknown pool name → 400.
- Pool exhausted → 403: "public IP pool 'default' is exhausted; contact the
  platform team to expand the pool".
- Quota: `max_public_ips` per tenant. Default `3`.
- PublicIp with an active `associated_to` cannot be deleted → 409. Detach from
  NatGateway or NIC first.

### Special semantics

**IP address value is assigned by KubeOVN, not chosen by the tenant.** The
`ip_address` field is null in the PENDING response and populated once ACTIVE.
This matches how AWS Elastic IPs work when allocation_method is static in a VPC
context — you get the next available IP from the pool.

**Future NIC attachment.** `associated_to` supports `{ "type": "nat_gateway",
"id": "..." }` in M2. Direct NIC attachment (`type: nic`) is designed for M3
when first-class NIC resources exist.

### KubeOVN backend

Creates an `OvnEip` CRD in the KubeOVN external subnet (the operator-configured
external network). The allocated IP from the EIP's `status.v4Ip` is written back
to `ip_address` on the DC-API resource row by the reconciler once the EIP is
ready.

---

## 11. PrivateDnsZone

A PrivateDnsZone is a custom DNS zone scoped to a VNet. VMs in the VNet resolve
names in the zone via a KubeOVN `VpcDns` CoreDNS instance. This maps to the
Azure Private DNS Zone concept.

### Endpoints

| Method | Path | Description | Success code |
|--------|------|-------------|--------------|
| `POST` | `/v1/vnets/{vnet_id}/dns-zones` | Create a DNS zone (async) | 202 |
| `GET` | `/v1/vnets/{vnet_id}/dns-zones` | List DNS zones in a VNet | 200 |
| `GET` | `/v1/vnets/{vnet_id}/dns-zones/{zone_id}` | Get DNS zone by ID | 200 |
| `DELETE` | `/v1/vnets/{vnet_id}/dns-zones/{zone_id}` | Delete DNS zone (async) | 202 |

### Create request

```json
{
  "name": "internal.lk.wso2.com",
  "description": "Internal DNS for WSO2 LK workloads"
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `name` | string | yes | DNS zone name (e.g. `internal.lk.wso2.com`). Must be a valid DNS name. |
| `description` | string | no | |

### Create response (202)

```json
{
  "resource": {
    "id": "a1b2c3d4-0000-0000-0000-000000000060",
    "vnet_id": "a1b2c3d4-0000-0000-0000-000000000001",
    "name": "internal.lk.wso2.com",
    "description": "Internal DNS for WSO2 LK workloads",
    "status": "PENDING",
    "tenant_id": "team-alpha",
    "created_at": "2026-05-06T10:08:00Z",
    "updated_at": "2026-05-06T10:08:00Z"
  },
  "note": "DNS zone is being configured. Poll GET /v1/vnets/.../dns-zones/a1b2c3d4-0000-0000-0000-000000000060 for status."
}
```

### Validation rules

- `name`: must be a valid DNS zone name. Punycode allowed. No leading dots.
  Maximum 253 characters.
- Duplicate zone name within the VNet → 409.
- A zone name can exist in multiple VNets (different tenants, different logical
  scopes); no global uniqueness constraint.
- VNet must be ACTIVE.

### KubeOVN backend

Creates a `VpcDns` CRD scoped to the parent VPC. DNS records are stored in a
`ConfigMap` referenced by the `VpcDns` CRD. Record management is via the
sub-resource in section 12.

---

## 12. DNS Records

DNS records are sub-resources of a PrivateDnsZone. Operations on records are
synchronous — they patch a ConfigMap in KubeOVN, which CoreDNS picks up within
seconds via hot reload.

### Endpoints

| Method | Path | Description | Success code |
|--------|------|-------------|--------------|
| `POST` | `/v1/vnets/{vnet_id}/dns-zones/{zone_id}/records` | Create a record (sync) | 201 |
| `GET` | `/v1/vnets/{vnet_id}/dns-zones/{zone_id}/records` | List records | 200 |
| `GET` | `/v1/vnets/{vnet_id}/dns-zones/{zone_id}/records/{record_id}` | Get record | 200 |
| `PUT` | `/v1/vnets/{vnet_id}/dns-zones/{zone_id}/records/{record_id}` | Replace record (sync) | 200 |
| `DELETE` | `/v1/vnets/{vnet_id}/dns-zones/{zone_id}/records/{record_id}` | Delete record (sync) | 204 |

### Create request

```json
{
  "name": "api",
  "type": "A",
  "ttl": 300,
  "values": ["10.1.1.5"]
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `name` | string | yes | Relative name within the zone (e.g. `api` → `api.internal.lk.wso2.com`) |
| `type` | string | yes | `A`, `AAAA`, `CNAME`, `TXT`, `SRV`, `MX` |
| `ttl` | int | no | Seconds; default 300; min 30; max 86400 |
| `values` | []string | yes | One or more record values. Format depends on `type`. |

### Validation rules

- `CNAME` records must have exactly one value.
- `A` values must be valid IPv4 addresses.
- `AAAA` values must be valid IPv6 addresses.
- `SRV` values must be in `priority weight port target` format.
- Zone must be ACTIVE.
- Duplicate name+type within the zone → 409.

---

## 13. Design Decisions

### Decision 1 — VNet address space mutability

**Stance: immutable in M2.**

Adding CIDRs to an existing VNet post-creation would require: (1) checking that
the new CIDR does not overlap existing subnets or peerings, (2) updating the
reserved CIDR validation list, (3) propagating to any UI pre-fill. This is
meaningful but not urgent — the use case is rare in practice (tenants usually
size their address space upfront). M3 will add `PATCH /v1/vnets/{id}` with an
append-only `address_space` update once the validation logic is built and tested.

### Decision 2 — NSG association API shape: separate sub-endpoint

**Stance: `POST /v1/security-groups/{id}/attachments` (separate sub-endpoint),
not inline `associations:[]` in the NSG body.**

Reasoning: an NSG can be associated and disassociated independently of its
rules — these are different operations with different lifecycles. If associations
were inline in the NSG body, every rule update would also require passing the
full association list, risking accidental detachment. The sub-endpoint pattern
(`/attachments`) gives each association its own ID, making it directly deletable
without affecting other associations or the rule set. It also maps cleanly to the
KubeOVN driver needing to know the target type at attach time to choose the right
CRD backend.

### Decision 3 — RouteTable on VPC vs per-subnet

**Stance: single-VPC-level routing for M2, subnet association is metadata only.**

KubeOVN's logical router is per-VPC, not per-subnet. There is no technical
mechanism to enforce per-subnet routing at the data plane. DC-API exposes a
`RouteTable` object per VNet (as many as needed per VNet) for Azure API
familiarity, but routes from all tables in a VNet are concatenated onto the VPC
router. Per-subnet differentiation is not enforced at the data plane.

This is documented as a stated limitation in the API. When a tenant creates two
route tables under the same VNet and attempts to associate them to different
subnets, the association is stored as metadata but has no data-plane effect —
the routes apply VNet-wide. The API returns a `warning` field on the association
response: "KubeOVN enforces routes at the VPC level; this association is
informational only in M2."

Revisit when Harvester's bundled KubeOVN GA (v1.9+) ships per-subnet routing
primitives.

### Decision 4 — VNet peering scope

**Cross-subscription peering: not supported, not designed, not on the roadmap.**

The `peer_vnet_id` must resolve to a VNet owned by the same tenant. The API
enforces this with a tenant-scoped lookup — a foreign VNet UUID returns 404,
not a meaningful error. This eliminates the entire class of accidental
cross-tenant reachability bugs. Documented clearly in the VNetPeering section.
Revisit in M5 when the Org/Subscription hierarchy lands if there is a validated
need for same-org cross-subscription peering.

### Decision 5 — Public IP allocation pool

**IPs are operator-defined, tenant-allocated, not tenant-chosen.**

Tenants request an IP from a named pool; they do not specify a target address.
The pool is configured per-region by the platform operator via
`DCAPI_PUBLIC_IP_POOL_<REGION>`. The pool name is exposed in the API so tenants
can see which pool they are drawing from (useful when a region has separate pools
for, say, production vs dev). The actual IP is assigned by KubeOVN's EIP
controller and returned once ACTIVE. This is the only safe model — allowing
tenants to request specific IPs would require IP availability checks, conflict
resolution, and reservation management that is not warranted for M2.

### Decision 6 — MAC addresses are an internal concern

MAC addresses are generated by the kubeovn driver at VM create time (stable MAC
from UUID hash per gotcha 1 in `m2-network-api-mapping.md`) and are never
surfaced in the API. The IP address and reachability `status` are what the API
exposes. This is consistent with every major cloud provider: AWS, Azure, and GCP
do not expose MAC addresses in their public APIs.

### Decision 7 — Post-migration rebind window

The 5-30 second OVN port rebind window after live migration (gotcha 7 in
`m2-network-api-mapping.md`) is not surfaced as a separate API state. The VM
stays ACTIVE throughout. Callers who need to know when connectivity is fully
restored after a migration should poll the VM endpoint and check for a stable
`ip_address` field. The kubeovn driver waits on the `Port_Binding.chassis` field
before declaring migration complete — the reconciler should not set status back
to ACTIVE until that wait completes.

---

## 14. Locked design decisions (2026-05-06)

The following decisions were made during the M2 scope-locking review and supersede
the earlier Open Questions section. Each item is final for M2; items marked
"deferred to M3" carry forward unchanged.

### Quotas

Per-tenant quota defaults, persisted by extending the existing `quotas` table:

| Field | Default | Notes |
|---|---|---|
| `max_vnets` | 10 | Hard stop on `POST /v1/vnets` (HTTP 403 with quota message) |
| `max_subnets_per_vnet` | 10 | Hard stop on `POST /v1/vnets/{id}/subnets` |
| `max_public_ips` | 3 | Quota row reserved; enforcement deferred to M3 (no public IP pool in lk) |

The DB migration adds three new columns to the `quotas` table with the defaults
above (`ALTER TABLE quotas ADD COLUMN ... DEFAULT N`); existing rows get the
defaults automatically.

### Regions — DB-backed

Stored in a new `regions` DB table. Operators populate rows when standing up a
region (lk, future EU, future US). The VNet handler validates the
`region` field against this table at create time
(`SELECT 1 FROM regions WHERE name = $1`).

Schema sketch (#147 owns the final form):

```sql
CREATE TABLE regions (
    name        TEXT PRIMARY KEY,
    description TEXT,
    -- per-region config, accreted as more lands:
    reserved_cidrs TEXT[],          -- M2: validated against tenant CIDRs
    public_ip_pool JSONB,           -- M3: external IP pool config
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

The earlier suggestion (`DCAPI_REGIONS` env var) was rejected because:
- Adding a region with an env var requires rolling DC-API. With a DB row, an
  `INSERT` and the next request picks it up.
- Per-region config (reserved CIDRs, IP pools, DNS forwarders) accretes — DB row
  is the natural single source of truth.

### Public IPs and NICs — deferred to M3

- **`PublicIp` resource (Section 10):** removed from M2 API surface. lk
  has no public IP pool yet; building the resource without a pool ships fake
  controls. M3 lights this up alongside the public IP pool config.
- **NIC resource:** NIC was never first-class in M2 anyway. Its primary use
  case (NIC-direct public IP attachment) follows public IPs to M3.
- **NSG (Section 7) is fully in M2 — only the per-NIC attachment is deferred.**
  Subnet-level attachments (`target_type: subnet`) are the AWS-NACL-equivalent
  path; they map to KubeOVN `Subnet.spec.acls` and were proven end-to-end by
  spike gate 4. NIC-level attachments (`target_type: nic`) are the
  AWS-Security-Group-equivalent path; they require NIC as a first-class
  resource, so they follow NIC to M3. The handler accepts `target_type: subnet`
  in M2 and rejects `target_type: nic` with HTTP 400. The API shape keeps both
  values so M3 doesn't break clients.
- **NAT gateway (Section 9) in M2:** the resource exists, but the
  `public_ip_id` field is reserved-but-not-enforced. Driver path that allocates
  external IPs is dropped from M2.

### DNS zone name collisions across tenants — allowed

Two different tenants can both create a private DNS zone named
`internal.lk.wso2.com` in their respective VNets. They never collide because:

- DNS resolution happens within each VNet's link list
- VNets are tenant-scoped and overlay-isolated (Geneve encapsulation)

This is the standard cloud behaviour (Azure, AWS). The design intentionally
does not enforce global uniqueness.

### `allow_forwarded_traffic` on VNetPeering — accepted but no-op

The field is part of the M2 API. Persisted in DB. The kubeovn driver stores it
but does not enforce it (no traffic transformation happens based on the value).
The create response includes a `warning` field:

```json
{
  "id": "...",
  "allow_forwarded_traffic": true,
  "warning": "allow_forwarded_traffic is accepted but not yet enforced — slated for M2.5"
}
```

Rationale: API stability. Tenants writing IaC can include the field today;
when enforcement lights up in M2.5 their manifests are still valid.

### Route table semantics — stance (a) for M2, stance (b) for M2.5

M2 surfaces `RouteTable` as a first-class resource. The kubeovn driver flattens
all route entries onto the parent VPC's `Vpc.spec.staticRoutes`. When two
different route tables are associated to two different subnets in the same
VPC with **conflicting** entries, the merged set lands on the VPC and both
subnets see both rule sets — there is no per-subnet enforcement. The
association response surfaces this with a `warning` field:

```json
{
  "id": "...",
  "route_table_id": "...",
  "subnet_id": "...",
  "warning": "per-subnet route differentiation is informational in M2 — all routes apply at the VPC level. Slated for M2.5 (OVN policy routes)."
}
```

The M2.5 upgrade — issue **#152** — moves the driver to OVN policy routes
(`Vpc.spec.policyRoutes`) which match traffic by source-subnet CIDR and route
accordingly. That gives true per-subnet routing without breaking the API
surface.

### Cross-tenant CIDR overlap — explicitly allowed

Two different tenants can both create VNets with `address_space:
["10.0.0.0/16"]`. They will never collide. This is the fundamental
tenant-isolation property of overlay networking and matches AWS/Azure
behaviour.

Why it's safe:
- Each VPC has its own OVN logical router.
- Inter-VM traffic is **Geneve-encapsulated** by OVS on the host before it
  leaves; the tunnel ID identifies which VPC the packet belongs to. The
  receiving host only delivers a packet to interfaces matching that tunnel ID.
- A VM can't forge the tunnel header — OVS adds it on the host, never the VM.

The CIDR uniqueness constraints in DC-API are:

1. **Within a single VPC:** subnets must not overlap each other (KubeOVN
   enforces).
2. **Across two peered VPCs:** address spaces must not overlap (DC-API
   enforces at peering create time).
3. **Against the per-region reserved list** (Harvester mgmt CIDR, RKE2
   service/pod CIDRs, etc.): tenant CIDRs reject (DC-API enforces at VNet/Subnet
   create time).

That is the complete list. Two unpeered tenants → no constraint. Identical
CIDRs are fine and never observable across tenant boundaries.
