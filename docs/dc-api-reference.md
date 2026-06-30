# DC-API Terraform Provider Reference

This document is the authoritative reference for building a Terraform provider that wraps the DC-API. It covers every resource type, exact request/response schemas, field classifications, async behaviour, and authentication requirements.

---

## Table of Contents

1. [Base URL & Versioning](#base-url--versioning)
2. [Authentication](#authentication)
3. [Error Format](#error-format)
4. [Async Lifecycle](#async-lifecycle)
5. [Resources](#resources)
   - [Tenant](#resource-tenant)
   - [Project](#resource-project)
   - [VirtualMachine](#resource-virtualmachine)
   - [Bastion](#resource-bastion)
   - [Cluster](#resource-cluster)
   - [NodePool](#resource-nodepool)
   - [VNet](#resource-vnet)
   - [Subnet](#resource-subnet)
   - [RouteTable](#resource-routetable)
   - [RouteTableAssociation](#resource-routetableassociation)
   - [NetworkSecurityGroup](#resource-networksecuritygroup)
   - [NSGAttachment](#resource-nsgattachment)
   - [VNetPeering](#resource-vnetpeering)
   - [PrivateDnsZone](#resource-privatednszoned)
   - [DnsRecord](#resource-dnsrecord)
   - [KeyVault](#resource-keyvault)
   - [PrivateEndpoint](#resource-privateendpoint)
   - [TenantMember](#resource-tenantmember)
   - [ServiceAccount](#resource-serviceaccount)
6. [Data Sources](#data-sources)
7. [Key Vault Secrets](#key-vault-secrets)
8. [VM Size Catalog](#vm-size-catalog)
9. [Cross-Resource Dependency Graph](#cross-resource-dependency-graph)
10. [Terraform Provider Implementation Notes](#terraform-provider-implementation-notes)

---

## Base URL & Versioning

| Environment | Base URL |
|---|---|
| Production (VPN may be required) | `https://dcapi.example.com` |
| Local development | `http://localhost:8080` |

- All authenticated endpoints are under the `/v1` path prefix.
- There is only one API version. All current endpoints are under `/v1`.
- Full base: `https://dcapi.example.com/v1`

---

## Authentication

```
Type:              Bearer token (JWT, RS256)
Header:            Authorization: Bearer <token>
Credential source: OIDC JWT from Asgardeo (PKCE flow) OR
                   Service account token (format: dcapi_sa_<lookup_id>_<secret>)
Token expiry:      YES — OIDC JWTs are short-lived (typically 1 hour, Asgardeo-controlled)
Refresh:           No DC-API refresh endpoint; client re-authenticates via Asgardeo PKCE
Login endpoint:    GET /v1/auth/login (browser/BFF flow only — not suitable for provider use)
```

**For Terraform**: use a service account token. It is long-lived, never expires unless the SA is deleted, and requires no OIDC flow. Create a dedicated SA with at minimum `member` role; use `owner` role only if the provider needs to manage members or service accounts.

Service account token format: `dcapi_sa_<lookup_id>_<secret>`
Pass as: `Authorization: Bearer dcapi_sa_<lookup_id>_<secret>`

---

## Error Format

```
HTTP codes used: 200, 201, 202, 204, 400, 401, 403, 404, 409, 410, 500, 501, 502, 503
```

**Standard error body** (all error codes except quota-exceeded):

```json
{
  "error": "string"
}
```

**Quota-exceeded body** (HTTP 400, when `error` = `"quota_exceeded"`):

```json
{
  "error":      "quota_exceeded",
  "message":    "string",
  "tenant_cap": { "cpu_cores": 80, "memory_gb": 256, "storage_gb": 2000 },
  "allocated":  { "cpu_cores": 20, "memory_gb": 64,  "storage_gb": 500  },
  "available":  { "cpu_cores": 60, "memory_gb": 192, "storage_gb": 1500 },
  "requested":  { "cpu_cores": 100, "memory_gb": 512, "storage_gb": 3000 }
}
```

**404 body** (use for Terraform drift detection — resource deleted externally):

```json
{ "error": "not found" }
```

**Conflict body** (409 — duplicate name, bad state transition, dependencies still exist):

```json
{ "error": "string describing the conflict" }
```

**Gone body** (410 — credentials already consumed, secret purged):

```json
{ "error": "string" }
```

---

## Async Lifecycle

Resources that return `202 Accepted` follow this status machine:

```
PENDING  →  ACTIVE     (provisioning succeeded)
PENDING  →  FAILED     (provisioning error — terminal, do not retry without user action)
ACTIVE   →  DELETING   (delete requested)
DELETING →  (row removed from DB — GET returns 404)
```

Poll `GET /{id}` and inspect the `status` field. `FAILED` is a terminal error state. `DELETING` eventually results in a 404, which is the terminal success state for deletion.

---

## Resources

### Field Classification Legend

| Classification | Meaning |
|---|---|
| `USER_REQUIRED` | User must provide; cannot change after create |
| `USER_OPTIONAL` | User may provide; cannot change after create |
| `USER_UPDATABLE` | User provides; can be changed on existing resource |
| `COMPUTED` | API generates this; user cannot control it |
| `COMPUTED_OPTIONAL` | User may optionally provide; API uses default if omitted |

---

### RESOURCE: Tenant

> Admin-only — requires `is_admin` claim in the caller's JWT.

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/admin/tenants` |
| List | GET | `/v1/tenants` |
| Update (cap) | PATCH | `/v1/admin/tenants/{tenant_id}` |
| _(no Read-by-id, no Delete)_ | | |

#### Create Request Body

```json
{
  "id":             "string",  // REQUIRED — slug, regex: ^[a-z][a-z0-9-]{0,30}[a-z0-9]$
  "name":           "string",  // OPTIONAL
  "description":    "string",  // OPTIONAL
  "cpu_cores_cap":  0,         // OPTIONAL — platform ceiling; 0 = default (80)
  "memory_gb_cap":  0,         // OPTIONAL — platform ceiling; 0 = default (256)
  "storage_gb_cap": 0          // OPTIONAL — platform ceiling; 0 = default (2000)
}
```

#### Create Response Body (201)

```json
{
  "id":             "my-org",                   // USER_SET  — the slug passed in
  "tenant_uuid":    "550e8400-e29b-...",       // COMPUTED  — UUID4, immutable identity
  "name":           "WSO2",                   // USER_SET
  "description":    "string",                 // USER_SET
  "asgardeo_group": "dc-tenant-my-org",         // COMPUTED  — derived: "dc-tenant-<id>"
  "cpu_cores_cap":  80,                       // USER_SET  (may differ if 0 was given)
  "memory_gb_cap":  256,                      // USER_SET
  "storage_gb_cap": 2000,                     // USER_SET
  "created_at":     "2026-05-27T10:00:00Z",   // COMPUTED  — RFC3339
  "created_by":     "user-sub-string"         // COMPUTED  — OIDC sub of caller
}
```

#### Update Request Body (PATCH)

```json
{
  "cpu_cores_cap":  100,  // OPTIONAL — min 1; rejected if new value < sum of project allocations
  "memory_gb_cap":  512,  // OPTIONAL — same constraint
  "storage_gb_cap": 4000  // OPTIONAL — same constraint
}
```

Update returns the same shape as Create (HTTP 200).

#### Field Classification

| Field | Classification |
|---|---|
| `id` | USER_REQUIRED (immutable) |
| `name` | USER_OPTIONAL (immutable; no name-update endpoint) |
| `description` | USER_OPTIONAL (immutable) |
| `cpu_cores_cap` | USER_UPDATABLE |
| `memory_gb_cap` | USER_UPDATABLE |
| `storage_gb_cap` | USER_UPDATABLE |
| `tenant_uuid` | COMPUTED |
| `asgardeo_group` | COMPUTED |
| `created_at` | COMPUTED |
| `created_by` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (slug string, e.g. `"my-org"`) |
| ID source | USER_DEFINED |
| ID location | RESPONSE_BODY |

#### Async

| Operation | Behaviour |
|---|---|
| Create | SYNC (201) |
| Update | SYNC (200) |
| Delete | NOT SUPPORTED |

---

### RESOURCE: Project

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}` |
| List | GET | `/v1/tenants/{tenant_id}/projects` |
| Update (quota) | PATCH | `/v1/tenants/{tenant_id}/projects/{project_id}` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}` |

#### Create Request Body

```json
{
  "id":            "infra",    // REQUIRED — slug, regex: [a-z0-9-], starts with letter, max 48 chars
  "name":          "string",   // OPTIONAL
  "description":   "string",   // OPTIONAL
  "cpu_cores":     20,         // OPTIONAL — default 20; sum across projects must ≤ tenant cap
  "memory_gb":     64,         // OPTIONAL — default 64
  "storage_gb":    500,        // OPTIONAL — default 500
  "max_vnets":     10,         // OPTIONAL — default 10
  "max_clusters":  2,          // OPTIONAL — default 2
  "max_volumes":   50,         // OPTIONAL — default 50
  "max_public_ips": 3          // OPTIONAL — default 3
}
```

#### Create Response Body (201)

```json
{
  "id":            "infra",
  "tenant_id":     "my-org",
  "project_uuid":  "550e8400-e29b-...",
  "tenant_uuid":   "660e8400-e29b-...",
  "name":          "string",
  "description":   "string",
  "cpu_cores":     20,
  "memory_gb":     64,
  "storage_gb":    500,
  "max_vnets":     10,
  "max_clusters":  2,
  "max_volumes":   50,
  "max_public_ips": 3,
  "created_at":    "2026-05-27T10:00:00Z",
  "updated_at":    "2026-05-27T10:00:00Z",
  "created_by":    "user-sub-string"
}
```

#### Update Request Body (PATCH)

```json
{
  "cpu_cores":  40,   // OPTIONAL — must not shrink below in-use; sum must ≤ tenant cap
  "memory_gb":  128,  // OPTIONAL — same constraints
  "storage_gb": 1000  // OPTIONAL — same constraints
}
```

> `max_vnets`, `max_clusters`, `max_volumes`, `max_public_ips` are **not patchable**.

#### Field Classification

| Field | Classification |
|---|---|
| `id` | USER_REQUIRED (immutable) |
| `tenant_id` | USER_REQUIRED (path param, immutable) |
| `name` | USER_OPTIONAL (immutable; not patchable) |
| `description` | USER_OPTIONAL (immutable; not patchable) |
| `cpu_cores` | USER_UPDATABLE |
| `memory_gb` | USER_UPDATABLE |
| `storage_gb` | USER_UPDATABLE |
| `max_vnets` | USER_OPTIONAL (immutable after create) |
| `max_clusters` | USER_OPTIONAL (immutable after create) |
| `max_volumes` | USER_OPTIONAL (immutable after create) |
| `max_public_ips` | USER_OPTIONAL (immutable after create) |
| `project_uuid` | COMPUTED |
| `tenant_uuid` | COMPUTED |
| `created_at` | COMPUTED |
| `updated_at` | COMPUTED |
| `created_by` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (slug, e.g. `"infra"`) |
| ID source | USER_DEFINED |
| ID location | RESPONSE_BODY |
| Note | `project_uuid` is the immutable UUID; `id` is the handle used in API paths |

#### Async

| Operation | Behaviour |
|---|---|
| Create | SYNC (201) |
| Update | SYNC (200) |
| Delete | SYNC (204) |

#### Dependencies

- `tenant_id` → `Tenant.id`

---

### RESOURCE: VirtualMachine

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/virtual-machines` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/virtual-machines/{id}` |
| List | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/virtual-machines` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/virtual-machines/{id}` |
| _(no Update endpoint)_ | | |

#### Create Request Body

```json
{
  "name":         "web-01",                // REQUIRED — max 63 chars
  "size":         "medium",               // REQUIRED — enum: "small"|"medium"|"large"|"xlarge"
  "disk_gb":      40,                     // OPTIONAL — min 10; defaults to size default
  "image_name":   "rancher-infra/ubuntu-22-04",  // REQUIRED — format: "namespace/resource-name"

  // Network path — choose exactly ONE of the following two options:
  // Option A (legacy bridge mode):
  "network_name": "iaas/vm-network-001",  // OPTIONAL

  // Option B (VPC mode — both fields required together):
  "vnet_id":   "550e8400-e29b-...",       // OPTIONAL — UUID of VNet
  "subnet_id": "660e8400-e29b-..."        // OPTIONAL — UUID of Subnet (required with vnet_id)
}
```

`network_name` is mutually exclusive with `vnet_id`/`subnet_id`.

#### Create Response Body (202)

```json
{
  "resource": {
    "id":            "770e8400-e29b-...",  // COMPUTED — UUID4
    "name":          "web-01",
    "size":          "medium",
    "status":        "PENDING",            // COMPUTED — always "PENDING" at creation
    "tenant_id":     "my-org",              // COMPUTED — from auth context
    "provider_type": "harvester",         // COMPUTED
    "ip_address":    "",                  // COMPUTED — empty until ACTIVE
    "message":       "provisioning VM",   // COMPUTED
    "created_at":    "2026-05-27T10:00:00Z"
  },
  "private_key":      "-----BEGIN OPENSSH PRIVATE KEY-----\n...",  // COMPUTED — SHOWN ONCE
  "console_password": "aB3xZ7kM2pQr4tYv",                         // COMPUTED — SHOWN ONCE
  "note":             "Poll GET /virtual-machines/{id} for status"
}
```

> **`private_key` and `console_password` are returned exactly once and never stored server-side. Store both in Terraform state as sensitive.**

#### Read Response Body (200)

```json
{
  "id":            "770e8400-e29b-...",
  "name":          "web-01",
  "size":          "medium",
  "status":        "ACTIVE",
  "tenant_id":     "my-org",
  "provider_type": "harvester",
  "ip_address":    "10.1.2.3",
  "message":       "",
  "created_at":    "2026-05-27T10:00:00Z"
}
```

> `private_key` and `console_password` are **not** returned on subsequent reads.

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable) |
| `size` | USER_REQUIRED (immutable) |
| `disk_gb` | USER_OPTIONAL (immutable) |
| `image_name` | USER_REQUIRED (immutable) |
| `network_name` | COMPUTED_OPTIONAL (immutable; legacy mode) |
| `vnet_id` | COMPUTED_OPTIONAL (immutable; VPC mode) |
| `subnet_id` | COMPUTED_OPTIONAL (immutable; VPC mode) |
| `id` | COMPUTED |
| `status` | COMPUTED |
| `tenant_id` | COMPUTED |
| `provider_type` | COMPUTED |
| `ip_address` | COMPUTED |
| `message` | COMPUTED |
| `created_at` | COMPUTED |
| `private_key` | COMPUTED (sensitive; shown once on create only) |
| `console_password` | COMPUTED (sensitive; shown once on create only) |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY (inside `"resource"` object) |

#### Async

| Operation | Behaviour |
|---|---|
| Create | ASYNC — poll `GET /virtual-machines/{id}` on `status`; `"ACTIVE"` = success, `"FAILED"` = error |
| Update | NOT SUPPORTED |
| Delete | ASYNC — poll until 404 |

#### Dependencies

| Field | References |
|---|---|
| `image_name` | `Image.id` |
| `network_name` | `Network.id` (legacy mode only) |
| `vnet_id` | `VNet.id` (VPC mode) |
| `subnet_id` | `Subnet.id` (VPC mode) |
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: Bastion

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/bastions` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/bastions/{id}` |
| List | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/bastions` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/bastions/{id}` |

#### Create Request Body

```json
{
  "name":        "jump-01",            // REQUIRED — max 63 chars
  "vnet_id":     "550e8400-e29b-...", // REQUIRED — UUID of VNet
  "subnet_id":   "660e8400-e29b-...", // REQUIRED — UUID of Subnet
  "description": "SSH jump host"      // OPTIONAL — max 256 chars
}
```

#### Create Response Body (202)

```json
{
  "resource": {
    "id":           "770e8400-e29b-...",
    "name":         "jump-01",
    "status":       "PENDING",
    "tenant_id":    "my-org",
    "vnet_id":      "550e8400-e29b-...",
    "subnet_id":    "660e8400-e29b-...",
    "provider_type": "harvester",
    "mgmt_ip":      "",   // COMPUTED — management-plane IP; empty until ACTIVE
    "internal_ip":  "",   // COMPUTED — VPC-side IP; empty until ACTIVE
    "description":  "SSH jump host",
    "message":      "",
    "created_at":   "2026-05-27T10:00:00Z"
  },
  "private_key":      "-----BEGIN OPENSSH PRIVATE KEY-----\n...",  // SHOWN ONCE
  "console_password": "aB3xZ7kM2pQr4tYv",                         // SHOWN ONCE
  "note":             "Poll GET /bastions/{id} for status"
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable) |
| `vnet_id` | USER_REQUIRED (immutable) |
| `subnet_id` | USER_REQUIRED (immutable) |
| `description` | USER_OPTIONAL (immutable) |
| `id` | COMPUTED |
| `status` | COMPUTED |
| `tenant_id` | COMPUTED |
| `provider_type` | COMPUTED |
| `mgmt_ip` | COMPUTED |
| `internal_ip` | COMPUTED |
| `message` | COMPUTED |
| `created_at` | COMPUTED |
| `private_key` | COMPUTED (sensitive; shown once) |
| `console_password` | COMPUTED (sensitive; shown once) |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY (inside `"resource"` object) |

#### Async

| Operation | Behaviour |
|---|---|
| Create | ASYNC — poll `status`; `"ACTIVE"` = success, `"FAILED"` = error |
| Delete | ASYNC — poll until 404 |

#### Dependencies

| Field | References |
|---|---|
| `vnet_id` | `VNet.id` |
| `subnet_id` | `Subnet.id` |
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: Cluster

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/clusters` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}` |
| List | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/clusters` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}` |
| Kubeconfig | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/kubeconfig` |
| _(no Update on cluster itself; scale via NodePool)_ | | |

#### Create Request Body

```json
{
  "name":        "prod-rke2",          // REQUIRED — DNS-label pattern, max 32 chars
  "k8s_version": "v1.33.10+rke2r3",   // REQUIRED
  "image_name":  "rancher-infra/rke2-ubuntu-22-04", // REQUIRED — "namespace/resource-name"

  "system_pool": {                     // REQUIRED
    "size":    "large",                // REQUIRED — enum: "small"|"medium"|"large"|"xlarge"
    "count":   3,                      // REQUIRED — enum: 1|3|5 (etcd quorum sizes only)
    "disk_gb": 100                     // OPTIONAL — min 40
  },

  "worker_pools": [                    // OPTIONAL — max 10 elements
    {
      "name":       "gpu-pool",        // REQUIRED — DNS-label, not "system"
      "size":       "xlarge",          // REQUIRED
      "count":      2,                 // REQUIRED — 1-50
      "disk_gb":    200,               // OPTIONAL — min 40
      "image_name": "rancher-infra/rke2-ubuntu-22-04", // OPTIONAL
      "taints": [                      // OPTIONAL — max 10
        {
          "key":    "nvidia.com/gpu",  // REQUIRED
          "value":  "true",            // OPTIONAL
          "effect": "NoSchedule"       // REQUIRED — "NoSchedule"|"PreferNoSchedule"|"NoExecute"
        }
      ],
      "labels": {                      // OPTIONAL — max 50 entries
        "pool-type": "gpu"
      }
    }
  ],

  // Network — mutually exclusive:
  "network_name": "iaas/vm-network-001",  // OPTIONAL (legacy bridge mode)
  "vnet_id":      "550e8400-e29b-...",    // OPTIONAL (VPC mode)
  "subnet_id":    "660e8400-e29b-..."     // OPTIONAL (VPC mode; required with vnet_id)
}
```

#### Create Response Body (202)

```json
{
  "resource": {
    "id":               "880e8400-e29b-...",
    "name":             "prod-rke2",
    "status":           "PENDING",
    "tenant_id":        "my-org",
    "provider_type":    "rancher",
    "system_pool": {
      "id":         "990e8400-e29b-...",
      "name":       "system",
      "role":       "system",
      "size":       "large",
      "count":      3,
      "disk_gb":    100,
      "taints":     [],
      "labels":     {},
      "status":     "provisioning",
      "message":    "",
      "created_at": "2026-05-27T10:00:00Z"
    },
    "worker_pool_count": 1,
    "total_node_count":  5,
    "message":           "",
    "created_at":        "2026-05-27T10:00:00Z"
  },
  "note": "Poll GET /clusters/{id} for status"
}
```

#### Kubeconfig Response (200, `Content-Type: text/plain`)

Raw kubeconfig YAML string. Only available when cluster `status` = `"ACTIVE"`. Returns 409 Conflict if the cluster is not yet ready.

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable) |
| `k8s_version` | USER_REQUIRED (immutable; no upgrade endpoint) |
| `image_name` | USER_REQUIRED (immutable) |
| `system_pool.size` | USER_REQUIRED (immutable) |
| `system_pool.count` | USER_REQUIRED (immutable at cluster level; scalable via NodePool PATCH) |
| `system_pool.disk_gb` | USER_OPTIONAL (immutable) |
| `worker_pools` | USER_OPTIONAL (immutable at cluster create; manage via NodePool resource) |
| `network_name` | COMPUTED_OPTIONAL (immutable) |
| `vnet_id` | COMPUTED_OPTIONAL (immutable) |
| `subnet_id` | COMPUTED_OPTIONAL (immutable) |
| `id` | COMPUTED |
| `status` | COMPUTED |
| `tenant_id` | COMPUTED |
| `provider_type` | COMPUTED |
| `worker_pool_count` | COMPUTED |
| `total_node_count` | COMPUTED |
| `message` | COMPUTED |
| `created_at` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY (inside `"resource"` object) |

#### Async

| Operation | Behaviour |
|---|---|
| Create | ASYNC — poll `status`; `"ACTIVE"` = success, `"FAILED"` = error |
| Delete | ASYNC — poll until 404 |

#### Dependencies

| Field | References |
|---|---|
| `image_name` | `Image.id` |
| `network_name` | `Network.id` (legacy) |
| `vnet_id` | `VNet.id` |
| `subnet_id` | `Subnet.id` |
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: NodePool

Sub-resource of Cluster.

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/node-pools` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/node-pools/{pool_name}` |
| List | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/node-pools` |
| Update | PATCH | `/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/node-pools/{pool_name}` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/node-pools/{pool_name}` |

> `{pool_name}` is the string name, **not a UUID**.

#### Create Request Body

```json
{
  "name":       "gpu-pool",   // REQUIRED — DNS-label, must not be "system"
  "size":       "xlarge",     // REQUIRED — enum: "small"|"medium"|"large"|"xlarge"
  "count":      2,            // REQUIRED — 1-50
  "disk_gb":    200,          // OPTIONAL — min 40
  "image_name": "rancher-infra/rke2-ubuntu-22-04",  // OPTIONAL
  "taints": [                 // OPTIONAL — max 10
    {
      "key":    "nvidia.com/gpu",  // REQUIRED
      "value":  "true",            // OPTIONAL — max 63 chars
      "effect": "NoSchedule"       // REQUIRED — "NoSchedule"|"PreferNoSchedule"|"NoExecute"
    }
  ],
  "labels": {                 // OPTIONAL — map[string]string, max 50 entries
    "pool-type": "gpu"
  }
}
```

#### Create Response Body (202)

```json
{
  "id":         "aa0e8400-e29b-...",  // COMPUTED — UUID4 (internal)
  "name":       "gpu-pool",           // USER_SET — used as API key for subsequent calls
  "role":       "worker",             // COMPUTED
  "size":       "xlarge",
  "count":      2,
  "disk_gb":    200,
  "taints":     [...],
  "labels":     {...},
  "status":     "provisioning",       // COMPUTED
  "message":    "",
  "created_at": "2026-05-27T10:00:00Z"
}
```

#### Update Request Body (PATCH) — all optional

```json
{
  "count":  4,        // OPTIONAL — scale up/down; 1-50
  "taints": [...],    // OPTIONAL — FULL REPLACE of taints array; refused on system pool
  "labels": {...}     // OPTIONAL — FULL REPLACE of labels map; refused on system pool
}
```

Node pool statuses: `"provisioning"` | `"ready"` | `"scaling"` | `"deleting"` | `"failed"`

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable — used as API path key) |
| `size` | USER_REQUIRED (immutable) |
| `count` | USER_UPDATABLE |
| `disk_gb` | USER_OPTIONAL (immutable) |
| `image_name` | USER_OPTIONAL (immutable) |
| `taints` | USER_UPDATABLE (full replace on PATCH) |
| `labels` | USER_UPDATABLE (full replace on PATCH) |
| `id` | COMPUTED |
| `role` | COMPUTED |
| `status` | COMPUTED |
| `message` | COMPUTED |
| `created_at` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `name` (string, e.g. `"gpu-pool"`) |
| ID source | USER_DEFINED |
| ID location | RESPONSE_BODY |

#### Async

| Operation | Behaviour |
|---|---|
| Create | ASYNC — poll `GET /{pool_name}`; `"ready"` = success, `"failed"` = error |
| Update | ASYNC — poll until `"ready"` |
| Delete | ASYNC — poll until 404 on the pool (or 404 on the cluster if cluster was deleted) |

#### Dependencies

| Field | References |
|---|---|
| `cluster_id` (path `{id}`) | `Cluster.id` |
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: VNet

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}` |
| List | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}` |
| _(no Update endpoint)_ | | |

#### Create Request Body

```json
{
  "name":          "prod-vpc",        // REQUIRED — regex: [a-z][a-z0-9-]{0,61}[a-z0-9]
  "address_space": ["10.1.0.0/16"],  // REQUIRED — array of RFC1918 CIDRs, /8-/28; min 1, max 5
  "region":        "lk-dev",         // REQUIRED — must match a region slug in DC-API regions table
  "description":   "Production VPC"  // OPTIONAL — max 256 chars
}
```

#### Create Response Body (202)

```json
{
  "resource": {
    "id":            "bb0e8400-e29b-...",
    "tenant_id":     "my-org",
    "name":          "prod-vpc",
    "region":        "lk-dev",
    "address_space": ["10.1.0.0/16"],
    "description":   "Production VPC",
    "status":        "PENDING",
    "provider_type": "kubeovn",
    "message":       "",
    "created_at":    "2026-05-27T10:00:00Z",
    "updated_at":    "2026-05-27T10:00:00Z"
  },
  "note": "Poll GET /vnets/{vnet_id} for status"
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable) |
| `address_space` | USER_REQUIRED (immutable) |
| `region` | USER_REQUIRED (immutable) |
| `description` | USER_OPTIONAL (immutable) |
| `id` | COMPUTED |
| `tenant_id` | COMPUTED |
| `status` | COMPUTED |
| `provider_type` | COMPUTED |
| `message` | COMPUTED |
| `created_at` | COMPUTED |
| `updated_at` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY (inside `"resource"` object) |

#### Async

| Operation | Behaviour |
|---|---|
| Create | ASYNC — poll `status`; `"ACTIVE"` = success, `"FAILED"` = error |
| Delete | ASYNC — poll until 404. **Blocked (409) if any Subnets still exist** — delete all subnets first. |

#### Dependencies

| Field | References |
|---|---|
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: Subnet

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets/{subnet_id}` |
| List | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets/{subnet_id}` |

#### Create Request Body

```json
{
  "name":        "app-subnet",     // REQUIRED — unique within the VNet
  "cidr":        "10.1.1.0/24",   // REQUIRED — within parent VNet address_space; no sibling overlap; /8-/28
  "gateway":     "10.1.1.1",      // OPTIONAL — must be within cidr; defaults to first usable IP
  "description": "App tier"       // OPTIONAL — max 256 chars
}
```

#### Create Response Body (202)

```json
{
  "resource": {
    "id":            "cc0e8400-e29b-...",
    "vnet_id":       "bb0e8400-e29b-...",
    "tenant_id":     "my-org",
    "name":          "app-subnet",
    "cidr":          "10.1.1.0/24",
    "gateway":       "10.1.1.1",
    "description":   "App tier",
    "status":        "PENDING",
    "provider_type": "kubeovn",
    "message":       "",
    "created_at":    "2026-05-27T10:00:00Z",
    "updated_at":    "2026-05-27T10:00:00Z"
  },
  "note": "Poll GET /subnets/{subnet_id} for status"
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable) |
| `cidr` | USER_REQUIRED (immutable) |
| `gateway` | COMPUTED_OPTIONAL (immutable) |
| `description` | USER_OPTIONAL (immutable) |
| `id` | COMPUTED |
| `vnet_id` | COMPUTED (from path) |
| `tenant_id` | COMPUTED |
| `status` | COMPUTED |
| `provider_type` | COMPUTED |
| `message` | COMPUTED |
| `created_at` | COMPUTED |
| `updated_at` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY (inside `"resource"` object) |

#### Async

| Operation | Behaviour |
|---|---|
| Create | ASYNC — poll `status`; `"ACTIVE"` = success, `"FAILED"` = error |
| Delete | ASYNC — poll until 404. **Blocked (409) if NSGs are attached** — detach first. If this is the last subnet in the VNet, DC-API automatically tears down the per-VPC NAT gateway and DNS deployment first (adds latency — build extra timeout). |

#### Dependencies

| Field | References |
|---|---|
| `vnet_id` (path) | `VNet.id` |
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: RouteTable

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/route-tables` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/route-tables/{rt_id}` |
| List | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/route-tables` |
| Update (routes) | PUT | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/route-tables/{rt_id}` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/route-tables/{rt_id}` |

#### Create Request Body

```json
{
  "name":        "main-rt",   // REQUIRED — unique within VNet
  "description": "string",    // OPTIONAL — max 256
  "routes": [                 // OPTIONAL — default []
    {
      "name":             "to-internet",       // REQUIRED — unique within route table
      "destination_cidr": "0.0.0.0/0",         // REQUIRED
      "next_hop_type":    "internet",           // REQUIRED — "vnet_local"|"internet"|"virtual_appliance"|"none"
      "next_hop_ip":      ""                   // OPTIONAL — required when next_hop_type = "virtual_appliance"
    }
  ]
}
```

#### Create Response Body (201)

```json
{
  "id":            "dd0e8400-e29b-...",
  "vnet_id":       "bb0e8400-e29b-...",
  "tenant_id":     "my-org",
  "name":          "main-rt",
  "description":   "string",
  "routes":        [...],
  "status":        "ACTIVE",
  "provider_type": "kubeovn",
  "created_at":    "2026-05-27T10:00:00Z",
  "updated_at":    "2026-05-27T10:00:00Z"
}
```

#### Update Request Body (PUT) — **full replace of routes array**

```json
{
  "routes": [
    { "name": "...", "destination_cidr": "...", "next_hop_type": "...", "next_hop_ip": "..." }
  ]
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable) |
| `description` | USER_OPTIONAL (immutable) |
| `routes` | USER_UPDATABLE (full replace via PUT) |
| `id` | COMPUTED |
| `vnet_id` | COMPUTED (from path) |
| `tenant_id` | COMPUTED |
| `status` | COMPUTED |
| `provider_type` | COMPUTED |
| `created_at` | COMPUTED |
| `updated_at` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY |

#### Async

| Operation | Behaviour |
|---|---|
| Create | SYNC (201) |
| Update | SYNC (200) |
| Delete | SYNC (204) |

#### Dependencies

| Field | References |
|---|---|
| `vnet_id` (path) | `VNet.id` |
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: RouteTableAssociation

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/route-tables/{rt_id}/associations` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/route-tables/{rt_id}/associations/{assoc_id}` |
| _(no Read, List, or Update — association state embedded in RouteTable GET)_ | | |

#### Create Request Body

```json
{
  "subnet_id": "cc0e8400-e29b-..."  // REQUIRED — UUID of subnet to associate
}
```

#### Create Response Body (201)

```json
{
  "id":             "ee0e8400-e29b-...",  // COMPUTED — UUID4
  "route_table_id": "dd0e8400-e29b-...", // COMPUTED
  "subnet_id":      "cc0e8400-e29b-...", // USER_SET
  "created_at":     "2026-05-27T10:00:00Z",
  "warning":        "string"             // COMPUTED — may be present: "per-subnet routing not yet enforced"
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `subnet_id` | USER_REQUIRED (immutable) |
| `id` | COMPUTED |
| `route_table_id` | COMPUTED |
| `created_at` | COMPUTED |
| `warning` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY |

#### Async

| Operation | Behaviour |
|---|---|
| Create | SYNC (201) |
| Delete | SYNC (204) |

#### Dependencies

| Field | References |
|---|---|
| `route_table_id` (path `{rt_id}`) | `RouteTable.id` |
| `subnet_id` | `Subnet.id` |
| `vnet_id` (path) | `VNet.id` |

---

### RESOURCE: NetworkSecurityGroup

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/security-groups` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/security-groups/{sg_id}` |
| List | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/security-groups` |
| Update (rules) | PUT | `/v1/tenants/{tenant_id}/projects/{project_id}/security-groups/{sg_id}/rules` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/security-groups/{sg_id}` |

#### Create Request Body

```json
{
  "name":        "web-sg",   // REQUIRED — unique within tenant
  "description": "string",   // OPTIONAL — max 256
  "rules": [                 // OPTIONAL — default []
    {
      "name":                       "allow-https",  // REQUIRED — unique within NSG
      "direction":                  "inbound",      // REQUIRED — "inbound"|"outbound"
      "priority":                   100,            // REQUIRED — 100-4096; unique per direction per NSG
      "protocol":                   "tcp",          // REQUIRED — "tcp"|"udp"|"icmp"|"*"
      "source_address_prefix":      "*",            // REQUIRED — CIDR or "*"
      "source_port_range":          "*",            // REQUIRED — port, range, or "*"
      "destination_address_prefix": "*",            // REQUIRED
      "destination_port_range":     "443",          // REQUIRED
      "action":                     "allow"         // REQUIRED — "allow"|"deny"
    }
  ]
}
```

#### Create Response Body (201)

```json
{
  "id":           "ff0e8400-e29b-...",
  "tenant_id":    "my-org",
  "name":         "web-sg",
  "description":  "string",
  "rules":        [...],
  "attachments":  [],          // COMPUTED — empty at creation; populated by NSGAttachment
  "status":       "ACTIVE",
  "provider_type": "kubeovn",
  "created_at":   "2026-05-27T10:00:00Z",
  "updated_at":   "2026-05-27T10:00:00Z"
}
```

#### Update Request Body (PUT `/rules`) — **full replace of rules array**

```json
{
  "rules": [
    { "name": "...", "direction": "...", "priority": 100, ... }
  ]
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable) |
| `description` | USER_OPTIONAL (immutable) |
| `rules` | USER_UPDATABLE (full replace via PUT `/rules`) |
| `id` | COMPUTED |
| `tenant_id` | COMPUTED |
| `attachments` | COMPUTED (managed by NSGAttachment resource) |
| `status` | COMPUTED |
| `provider_type` | COMPUTED |
| `created_at` | COMPUTED |
| `updated_at` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY |

#### Async

| Operation | Behaviour |
|---|---|
| Create | SYNC (201) |
| Update | SYNC (200) |
| Delete | SYNC (204). **Blocked (409) if NSG has active attachments** — detach all first. |

#### Dependencies

| Field | References |
|---|---|
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: NSGAttachment

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/security-groups/{sg_id}/attachments` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/security-groups/{sg_id}/attachments/{attachment_id}` |
| _(no Read, List, or Update — attachment state is embedded in NSG GET response)_ | | |

#### Create Request Body

```json
{
  "target_type": "subnet",              // REQUIRED — only "subnet" supported in M2
  "target_id":   "cc0e8400-e29b-..."   // REQUIRED — UUID of subnet to attach
}
```

#### Create Response Body (201)

```json
{
  "id":          "110e8400-e29b-...",  // COMPUTED — UUID4; use this as {attachment_id} for delete
  "sg_id":       "ff0e8400-e29b-...", // COMPUTED
  "target_type": "subnet",
  "target_id":   "cc0e8400-e29b-...",
  "created_at":  "2026-05-27T10:00:00Z"
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `target_type` | USER_REQUIRED (immutable; only `"subnet"` valid) |
| `target_id` | USER_REQUIRED (immutable) |
| `id` | COMPUTED |
| `sg_id` | COMPUTED |
| `created_at` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY |

#### Async

| Operation | Behaviour |
|---|---|
| Create | SYNC (201) |
| Delete | SYNC (204) |

#### Dependencies

| Field | References |
|---|---|
| `sg_id` (path `{sg_id}`) | `NetworkSecurityGroup.id` |
| `target_id` | `Subnet.id` (when `target_type = "subnet"`) |

---

### RESOURCE: VNetPeering

> Peerings are **directional**. Creating one peering only adds routes in one direction. For full bidirectional peering, create two `VNetPeering` resources (one on each VNet pointing at the other).

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/peerings` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/peerings/{peering_id}` |
| List | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/peerings` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/peerings/{peering_id}` |

#### Create Request Body

```json
{
  "name":                    "prod-to-staging",    // REQUIRED — unique within the requesting VNet
  "peer_vnet_id":            "220e8400-e29b-...", // REQUIRED — UUID of peer VNet (same tenant, same region)
  "allow_forwarded_traffic": false                 // OPTIONAL — default false
}
```

Constraints:
- `peer_vnet_id` must be same tenant and same region.
- VNet address spaces must not overlap with peer address spaces.
- Self-peering (`vnet_id == peer_vnet_id`) is rejected.
- Duplicate peering between the same pair is rejected.

#### Create Response Body (202)

```json
{
  "resource": {
    "id":                     "330e8400-e29b-...",
    "vnet_id":                "bb0e8400-e29b-...",
    "peer_vnet_id":           "220e8400-e29b-...",
    "tenant_id":              "my-org",
    "name":                   "prod-to-staging",
    "allow_forwarded_traffic": false,
    "status":                 "PENDING",
    "provider_type":          "kubeovn",
    "message":                "",
    "created_at":             "2026-05-27T10:00:00Z",
    "updated_at":             "2026-05-27T10:00:00Z",
    "warning":                ""
  },
  "note": "Poll GET /peerings/{peering_id} for status"
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable) |
| `peer_vnet_id` | USER_REQUIRED (immutable) |
| `allow_forwarded_traffic` | USER_OPTIONAL (immutable) |
| `id` | COMPUTED |
| `vnet_id` | COMPUTED (from path) |
| `tenant_id` | COMPUTED |
| `status` | COMPUTED |
| `provider_type` | COMPUTED |
| `message` | COMPUTED |
| `warning` | COMPUTED |
| `created_at` | COMPUTED |
| `updated_at` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY (inside `"resource"` object) |

#### Async

| Operation | Behaviour |
|---|---|
| Create | ASYNC — poll `status`; `"ACTIVE"` = success, `"FAILED"` = error |
| Delete | ASYNC — poll until 404 |

#### Dependencies

| Field | References |
|---|---|
| `vnet_id` (path) | `VNet.id` |
| `peer_vnet_id` | `VNet.id` |
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: PrivateDnsZone

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/dns-zones` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/dns-zones/{zone_id}` |
| List | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/dns-zones` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/dns-zones/{zone_id}` |

#### Create Request Body

```json
{
  "name":        "internal.example.com",  // REQUIRED — valid DNS label sequence
  "description": "string"              // OPTIONAL — max 256
}
```

#### Create Response Body (202)

```json
{
  "resource": {
    "id":            "440e8400-e29b-...",
    "vnet_id":       "bb0e8400-e29b-...",
    "tenant_id":     "my-org",
    "name":          "internal.example.com",
    "description":   "string",
    "status":        "PENDING",
    "provider_type": "kubeovn",
    "message":       "",
    "created_at":    "2026-05-27T10:00:00Z",
    "updated_at":    "2026-05-27T10:00:00Z"
  },
  "note": "Poll GET /dns-zones/{zone_id} for status"
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable) |
| `description` | USER_OPTIONAL (immutable) |
| `id` | COMPUTED |
| `vnet_id` | COMPUTED (from path) |
| `tenant_id` | COMPUTED |
| `status` | COMPUTED |
| `provider_type` | COMPUTED |
| `message` | COMPUTED |
| `created_at` | COMPUTED |
| `updated_at` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY (inside `"resource"` object) |

#### Async

| Operation | Behaviour |
|---|---|
| Create | ASYNC — poll `status`; `"ACTIVE"` = success, `"FAILED"` = error |
| Delete | ASYNC — poll until 404 |

#### Dependencies

| Field | References |
|---|---|
| `vnet_id` (path) | `VNet.id` |
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: DnsRecord

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create (upsert) | POST | `/v1/.../dns-zones/{zone_id}/records` |
| Read | GET | `/v1/.../dns-zones/{zone_id}/records/{record_id}` |
| List | GET | `/v1/.../dns-zones/{zone_id}/records` |
| Update | PUT | `/v1/.../dns-zones/{zone_id}/records/{record_id}` |
| Delete | DELETE | `/v1/.../dns-zones/{zone_id}/records/{record_id}` |

> POST is an **upsert** — creates a new record, or updates an existing one matching the same `name`+`type` within the zone. PUT updates by explicit record ID.

#### Create/Upsert Request Body

```json
{
  "name":   "www",        // REQUIRED — relative record name within zone (e.g. "www")
  "type":   "A",          // REQUIRED — "A"|"AAAA"|"CNAME"|"SRV"|"TXT"|"MX"
  "values": ["10.1.1.5"], // REQUIRED — min 1 value
  "ttl":    300           // OPTIONAL — 30-86400 seconds; default 300
}
```

#### Create Response Body (201)

```json
{
  "id":         "550e8400-e29b-...",
  "zone_id":    "440e8400-e29b-...",
  "tenant_id":  "my-org",
  "type":       "A",
  "name":       "www",
  "values":     ["10.1.1.5"],
  "ttl":        300,
  "created_at": "2026-05-27T10:00:00Z"
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable — part of upsert identity) |
| `type` | USER_REQUIRED (immutable — part of upsert identity) |
| `values` | USER_UPDATABLE (full replace via PUT) |
| `ttl` | USER_UPDATABLE |
| `id` | COMPUTED |
| `zone_id` | COMPUTED (from path) |
| `tenant_id` | COMPUTED |
| `created_at` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY |

#### Async

| Operation | Behaviour |
|---|---|
| Create | SYNC (201) |
| Update | SYNC (200) |
| Delete | SYNC (204) |

#### Dependencies

| Field | References |
|---|---|
| `zone_id` (path) | `PrivateDnsZone.id` |
| `vnet_id` (path) | `VNet.id` |
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: KeyVault

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}` |
| List | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}` |
| Get credentials | GET | `/v1/.../keyvaults/{id}/credentials` |
| Rotate credentials | POST | `/v1/.../keyvaults/{id}/credentials/rotate` |

> The credentials endpoint returns **410 Gone** after the first successful call. See implementation notes.

#### Create Request Body

```json
{
  "name":             "prod-secrets",  // REQUIRED — 3..63 chars, DNS label, starts with letter
  "soft_delete_days": 30               // OPTIONAL — 7..90; default 30
}
```

#### Create Response Body (201)

```json
{
  "id":               "660e8400-e29b-...",
  "tenant_id":        "my-org",
  "name":             "prod-secrets",
  "soft_delete_days": 30,
  "status":           "PENDING",
  "message":          "",
  "mount_path":       "",              // COMPUTED — populated when ACTIVE
  "endpoint_address": "",              // COMPUTED — in-cluster OpenBao address; populated when ACTIVE
  "endpoint_port":    "",              // COMPUTED — typically "8200"; populated when ACTIVE
  "created_at":       "2026-05-27T10:00:00Z",
  "updated_at":       "2026-05-27T10:00:00Z"
}
```

#### Credentials Response (200 — first call only; 410 on subsequent calls)

```json
{
  "role_id":        "stable-approle-id",           // COMPUTED — stable; same on re-read if not yet consumed
  "secret_id":      "one-time-secret-id",           // COMPUTED — SHOWN ONCE; sensitive
  "mount_path":     "tenant-my-org/prod-secrets",     // COMPUTED
  "backend_address": "openbao.example.svc",   // COMPUTED
  "backend_port":   "8200"                          // COMPUTED
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable) |
| `soft_delete_days` | USER_OPTIONAL (immutable — cannot change policy after create) |
| `id` | COMPUTED |
| `tenant_id` | COMPUTED |
| `status` | COMPUTED |
| `message` | COMPUTED |
| `mount_path` | COMPUTED |
| `endpoint_address` | COMPUTED |
| `endpoint_port` | COMPUTED |
| `created_at` | COMPUTED |
| `updated_at` | COMPUTED |
| `credentials.role_id` | COMPUTED |
| `credentials.secret_id` | COMPUTED (sensitive; shown once — store in Terraform state) |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY |

#### Async

| Operation | Behaviour |
|---|---|
| Create | SYNC (201) — but status may stay `PENDING` while KVI operator provisions. Poll GET until `"ACTIVE"` before calling `/credentials`. |
| Delete | SYNC (204) |

#### Dependencies

| Field | References |
|---|---|
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: PrivateEndpoint

> These routes return **501 Not Implemented** if the endpoint provisioner is not enabled on the DC-API instance.

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{kv_id}/private-endpoints` |
| Read | GET | `/v1/.../keyvaults/{kv_id}/private-endpoints/{ep_id}` |
| List | GET | `/v1/.../keyvaults/{kv_id}/private-endpoints` |
| Delete | DELETE | `/v1/.../keyvaults/{kv_id}/private-endpoints/{ep_id}` |

#### Create Request Body

```json
{
  "name":      "prod-secrets-ep",      // REQUIRED — 3..63 chars, DNS label, starts with letter
  "vnet_id":   "bb0e8400-e29b-...",   // REQUIRED — UUID of VNet where endpoint is reachable
  "subnet_id": "cc0e8400-e29b-..."    // REQUIRED — UUID of Subnet within that VNet
}
```

#### Create Response Body (201)

```json
{
  "id":          "770e8400-e29b-...",
  "tenant_id":   "my-org",
  "target_type": "key_vault",          // COMPUTED
  "target_id":   "660e8400-e29b-...", // COMPUTED — UUID of parent KeyVault
  "vnet_id":     "bb0e8400-e29b-...",
  "subnet_id":   "cc0e8400-e29b-...",
  "name":        "prod-secrets-ep",
  "ip_address":  "10.1.1.200",        // COMPUTED — VIP from subnet CIDR
  "hostname":    "prod-secrets.tenant-my-org.svc",  // COMPUTED — DNS-resolvable within VPC only
  "status":      "ACTIVE",
  "message":     "",
  "created_at":  "2026-05-27T10:00:00Z",
  "updated_at":  "2026-05-27T10:00:00Z"
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable) |
| `vnet_id` | USER_REQUIRED (immutable) |
| `subnet_id` | USER_REQUIRED (immutable) |
| `id` | COMPUTED |
| `tenant_id` | COMPUTED |
| `target_type` | COMPUTED |
| `target_id` | COMPUTED |
| `ip_address` | COMPUTED |
| `hostname` | COMPUTED |
| `status` | COMPUTED |
| `message` | COMPUTED |
| `created_at` | COMPUTED |
| `updated_at` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY |

#### Async

| Operation | Behaviour |
|---|---|
| Create | SYNC (201) |
| Delete | SYNC (204) |

#### Dependencies

| Field | References |
|---|---|
| `kv_id` (path) | `KeyVault.id` |
| `vnet_id` | `VNet.id` |
| `subnet_id` | `Subnet.id` |
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

### RESOURCE: TenantMember

> Requires `owner` role. A last-owner guard prevents removing the final owner.

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/members` |
| List | GET | `/v1/tenants/{tenant_id}/members` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/members/{principal_id}` |
| _(no Read-by-id, no Update — to change role, delete and re-invite)_ | | |

> `{principal_id}` in the delete path is the **OIDC sub string**, not the role_assignment UUID.

#### Create Request Body

```json
{
  "user_sub":      "auth0|abc123",  // REQUIRED — OIDC subject claim of user to invite
  "role":          "member",        // REQUIRED — "owner"|"member"|"viewer"
  "display_alias": "Alice"          // OPTIONAL — human-readable label; max 256 chars
}
```

#### Create Response Body (201)

```json
{
  "id":             "880e8400-e29b-...",  // COMPUTED — UUID4 of role_assignment row
  "principal_type": "user",              // COMPUTED
  "principal_id":   "auth0|abc123",      // COMPUTED — echoes user_sub
  "scope_type":     "tenant",            // COMPUTED
  "scope_id":       "my-org",             // COMPUTED — tenant slug
  "role":           "member",
  "granted_at":     "2026-05-27T10:00:00Z",
  "granted_by":     "auth0|owner123",   // COMPUTED — caller's sub
  "display_alias":  "Alice"
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `user_sub` | USER_REQUIRED (immutable — becomes `principal_id`) |
| `role` | USER_REQUIRED (immutable; re-create to change) |
| `display_alias` | USER_OPTIONAL (immutable) |
| `id` | COMPUTED |
| `principal_type` | COMPUTED |
| `principal_id` | COMPUTED |
| `scope_type` | COMPUTED |
| `scope_id` | COMPUTED |
| `granted_at` | COMPUTED |
| `granted_by` | COMPUTED |

#### Identity

| Field | Value |
|---|---|
| ID for state | `id` (UUID4 of role_assignment row) |
| ID for DELETE path | `principal_id` (OIDC sub string — **not** the UUID) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY |

#### Async

| Operation | Behaviour |
|---|---|
| Create | SYNC (201) |
| Delete | SYNC (204) |

#### Dependencies

| Field | References |
|---|---|
| `tenant_id` (path) | `Tenant.id` |

---

### RESOURCE: ServiceAccount

> Requires `owner` role to create or delete.

#### Endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/v1/tenants/{tenant_id}/projects/{project_id}/service-accounts` |
| Read | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/service-accounts/{sa_id}` |
| List | GET | `/v1/tenants/{tenant_id}/projects/{project_id}/service-accounts` |
| Delete | DELETE | `/v1/tenants/{tenant_id}/projects/{project_id}/service-accounts/{sa_id}` |

#### Create Request Body

```json
{
  "name":        "ci-pipeline",      // REQUIRED — regex: [a-z0-9][a-z0-9-]{0,61}[a-z0-9]
  "role":        "member",           // REQUIRED — "owner"|"member"|"viewer"
  "description": "GitHub Actions SA" // OPTIONAL — max 256 chars
}
```

#### Create Response Body (201) — includes token

```json
{
  "id":          "990e8400-e29b-...",
  "tenant_id":   "my-org",
  "name":        "ci-pipeline",
  "role":        "member",
  "description": "GitHub Actions SA",
  "created_at":  "2026-05-27T10:00:00Z",
  "token":       "dcapi_sa_abc123_xYzSECRET"  // COMPUTED — SHOWN ONCE; use as Bearer token
}
```

> **`token` is returned exactly once and never stored server-side.** Store in Terraform state as sensitive. If lost, the SA must be deleted and recreated.

#### Read Response Body (200) — token NOT included

```json
{
  "id":          "990e8400-e29b-...",
  "tenant_id":   "my-org",
  "name":        "ci-pipeline",
  "role":        "member",
  "description": "GitHub Actions SA",
  "created_at":  "2026-05-27T10:00:00Z",
  "last_used":   "2026-05-27T12:00:00Z"  // COMPUTED — nullable RFC3339
}
```

#### Field Classification

| Field | Classification |
|---|---|
| `name` | USER_REQUIRED (immutable) |
| `role` | USER_REQUIRED (immutable; no update endpoint) |
| `description` | USER_OPTIONAL (immutable) |
| `id` | COMPUTED |
| `tenant_id` | COMPUTED |
| `created_at` | COMPUTED |
| `last_used` | COMPUTED |
| `token` | COMPUTED (sensitive; shown once on create only) |

#### Identity

| Field | Value |
|---|---|
| ID field | `id` (UUID4) |
| ID source | API_GENERATED |
| ID location | RESPONSE_BODY |

#### Async

| Operation | Behaviour |
|---|---|
| Create | SYNC (201) |
| Delete | SYNC (204) |

#### Dependencies

| Field | References |
|---|---|
| `tenant_id` (path) | `Tenant.id` |
| `project_id` (path) | `Project.id` |

---

## Data Sources

These are read-only endpoints with no Create/Delete lifecycle. Implement as Terraform data sources.

### DATA SOURCE: Image

```
LIST:   GET /v1/tenants/{tenant_id}/images
CREATE: POST /v1/tenants/{tenant_id}/images  (registers by URL — optional managed resource)
```

List response item:

```json
{
  "id":           "rancher-infra/ubuntu-22-04",  // format: "namespace/resource-name"
  "display_name": "Ubuntu 22.04",
  "namespace":    "rancher-infra"
}
```

Use `id` as the value for `image_name` in VM and Cluster resources.

### DATA SOURCE: Network (Legacy)

```
LIST: GET /v1/tenants/{tenant_id}/networks
```

List response item:

```json
{
  "id":           "iaas/vm-network-001",  // format: "namespace/resource-name"
  "display_name": "VM Network 001",
  "namespace":    "iaas"
}
```

Use `id` as the value for `network_name` in VM resources (legacy bridge mode only).

### DATA SOURCE: TenantCapUsage

```
READ: GET /v1/tenants/{tenant_id}/cap-usage
```

Response:

```json
{
  "cap":       { "cpu_cores": 80,  "memory_gb": 256, "storage_gb": 2000 },
  "allocated": { "cpu_cores": 20,  "memory_gb": 64,  "storage_gb": 500  },
  "available": { "cpu_cores": 60,  "memory_gb": 192, "storage_gb": 1500 }
}
```

---

## Key Vault Secrets

Pass-through proxy to OpenBao. The parent KeyVault must be `ACTIVE`. These routes return **501 Not Implemented** if the KVI provisioner is not enabled.

```
LIST:    GET    /v1/.../keyvaults/{id}/secrets
READ:    GET    /v1/.../keyvaults/{id}/secrets/{key}
WRITE:   PUT    /v1/.../keyvaults/{id}/secrets/{key}   (create or update)
DELETE:  DELETE /v1/.../keyvaults/{id}/secrets/{key}   (soft-delete)
RESTORE: POST   /v1/.../keyvaults/{id}/secrets/{key}/restore
```

Secret key constraint: pattern `^[a-z0-9._-]{1,256}$`

**Write request body:**

```json
{
  "value":    "super-secret-value",  // REQUIRED — UTF-8 string; max 1 MiB
  "metadata": { "env": "prod" }      // OPTIONAL — map[string]string; max 64 pairs
}
```

**Read response (200)** / **410 Gone** (if soft-deleted):

```json
{
  "key":        "db-password",
  "value":      "super-secret-value",
  "version":    3,
  "metadata":   { "env": "prod" },
  "created_at": "2026-05-27T10:00:00Z",
  "deleted_at": null
}
```

**List response** (cursor-paginated):

```json
{
  "items": [
    {
      "name":           "db-password",
      "latest_version": 3,
      "created_at":     "2026-05-27T10:00:00Z",
      "updated_at":     "2026-05-27T11:00:00Z",
      "deleted_at":     null
    }
  ],
  "next_cursor":  null,
  "total_count":  1
}
```

Pass `?cursor=<next_cursor>` to paginate. `next_cursor = null` means last page.

---

## VM Size Catalog

The `size` field on VMs, Bastions, and Cluster node pools maps to fixed CPU/RAM bundles:

| Size | vCPU | RAM |
|---|---|---|
| `small` | 2 | 8 GB |
| `medium` | 4 | 16 GB |
| `large` | 8 | 32 GB |
| `xlarge` | 16 | 64 GB |

Disk size is not bundled — specify `disk_gb` explicitly. Minimum is 10 GB for VMs, 40 GB for cluster nodes.

---

## Cross-Resource Dependency Graph

```
Tenant
  └── Project
        ├── VirtualMachine ──→ Image (image_name)
        │                  ──→ Network (network_name, legacy bridge mode)
        │                  ──→ VNet (vnet_id, VPC mode)
        │                  ──→ Subnet (subnet_id, VPC mode)
        │
        ├── Bastion        ──→ VNet (vnet_id)
        │                  ──→ Subnet (subnet_id)
        │
        ├── Cluster        ──→ Image (image_name)
        │     └── NodePool ──→ Image (image_name, optional)
        │                  ──→ VNet (vnet_id, VPC mode)
        │                  ──→ Subnet (subnet_id, VPC mode)
        │
        ├── VNet
        │     ├── Subnet
        │     │     ← NSGAttachment.target_id (when target_type="subnet")
        │     │     ← RouteTableAssociation.subnet_id
        │     ├── RouteTable
        │     │     └── RouteTableAssociation ──→ Subnet
        │     ├── VNetPeering ──→ VNet (peer_vnet_id)
        │     └── PrivateDnsZone
        │           └── DnsRecord
        │
        ├── NetworkSecurityGroup
        │     └── NSGAttachment ──→ Subnet (target_id)
        │
        └── KeyVault
              └── PrivateEndpoint ──→ VNet (vnet_id)
                                  ──→ Subnet (subnet_id)

TenantMember  ──→ Tenant (tenant-scoped)
ServiceAccount ──→ Project (project-scoped)
```

---

## Terraform Provider Implementation Notes

### 1. Shown-once fields

`private_key`, `console_password` (VMs and Bastions), `token` (ServiceAccounts), and `secret_id` (KeyVault credentials) are returned **exactly once** by the API and never stored server-side. Mark these as `Sensitive: true` in the schema and store them in Terraform state during Create. If state is lost:

| Resource | Recovery |
|---|---|
| VirtualMachine / Bastion | Delete and recreate |
| ServiceAccount | Delete and recreate |
| KeyVault credentials | Call `POST /credentials/rotate` to mint a new `secret_id` |

### 2. Async polling

For all `202 Accepted` operations, implement a `resource.StateChangeConf` (or equivalent) polling `GET /{id}` every 15–30 seconds. Surface the `message` field in error diagnostics when `status` transitions to `"FAILED"`.

Recommended timeouts:

| Resource | Create | Delete |
|---|---|---|
| VirtualMachine | 15 min | 10 min |
| Bastion | 15 min | 10 min |
| Cluster | 30 min | 20 min |
| NodePool | 15 min | 10 min |
| VNet | 5 min | 5 min |
| Subnet | 5 min | 10 min (last subnet in VNet: 15 min) |
| VNetPeering | 5 min | 5 min |
| PrivateDnsZone | 5 min | 5 min |

### 3. Delete polling

After issuing DELETE, poll `GET /{id}`. Treat `404` as terminal success. For resources with intermediate `"DELETING"` status, keep polling until 404.

### 4. 404 = drift

On every Read (refresh), if the API returns 404, call `d.SetId("")` to remove the resource from state. This is the correct Terraform pattern for externally-deleted resources.

### 5. Full-replace update semantics

`PUT /rules` on NSG and `PUT` on RouteTable **replace the entire collection**. Your provider must send the complete desired state, not a partial diff. Implement by always sending all current rules/routes when updating.

### 6. Project ID is a user-defined slug

The `id` field on a Project is a user-provided slug (e.g. `"infra"`), not a UUID. Use the slug in API paths. Store `project_uuid` for internal tracking — it is immutable even if the project is deleted and recreated with the same slug.

### 7. Cluster worker pools

`worker_pools` in `CreateClusterRequest` atomically creates the cluster and initial pools. After creation, manage node pools via the `NodePool` resource. You may choose to either inline worker pools in the cluster resource (and ignore drift on post-create pool changes) or model them purely as separate `NodePool` resources. The separate resource approach is cleaner for plan/apply cycles.

### 8. Peering is directional

One `VNetPeering` resource only provisions routes in one direction. For bidirectional connectivity:

```hcl
resource "dcapi_vnet_peering" "a_to_b" {
  vnet_id      = dcapi_vnet.a.id
  peer_vnet_id = dcapi_vnet.b.id
  ...
}

resource "dcapi_vnet_peering" "b_to_a" {
  vnet_id      = dcapi_vnet.b.id
  peer_vnet_id = dcapi_vnet.a.id
  ...
}
```

### 9. Subnet delete ordering

When a VNet is destroyed, subnets must be deleted before the VNet. Within subnet deletion: if any NSGAttachments exist, detach first. If deleting the last subnet in a VNet, DC-API automatically tears down the per-VPC NAT gateway and CoreDNS pods before proceeding — allow extra timeout (15 min) for this specific case.

### 10. KeyVault credentials lifecycle

```
Create KeyVault → poll until ACTIVE
→ GET /credentials  (call exactly once; store role_id + secret_id in state)
→ GET /credentials again → 410 Gone (expected; do not error)
→ POST /credentials/rotate → new secret_id returned once; old secret_id invalidated
```

On provider Read, do not re-call `GET /credentials` — the 410 response does not indicate the KeyVault is gone. Use `GET /keyvaults/{id}` for status checks and treat 410 on the credentials sub-resource as "already consumed, no action needed."

### 11. NSG attachment ordering

An NSG must exist before attaching. A subnet must exist before attaching. The attachment must be deleted before deleting either the NSG or the subnet. Encode this in `depends_on` or use resource references to let Terraform infer it automatically.

### 12. Provider authentication

Create a dedicated ServiceAccount with `member` role for the provider's own API calls. Store the token in the provider configuration block and pass it as `Authorization: Bearer <token>`. The token does not expire unless the SA is deleted.

```hcl
provider "dcapi" {
  endpoint   = "https://dcapi.example.com"
  token      = var.dcapi_sa_token   # dcapi_sa_<lookup_id>_<secret>
  tenant_id  = "my-org"
  project_id = "my-project"
}
```
