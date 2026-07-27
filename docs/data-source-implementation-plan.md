# Implementation Plan: Terraform Data Sources for DC-API Provider

## 1. Problem statement

Today the provider only exposes `resource "dcapi_*"` blocks, all of which assume
Terraform owns the full create/read/delete lifecycle of the object. There is no
way to *reference* a DC-API object whose lifecycle is managed outside the current
Terraform root module — e.g. a shared VNet, key vault, or DNS zone provisioned by
platform admins or a different Terraform state. Users currently have to either
hardcode UUIDs or import objects as resources they don't actually own (risking
accidental deletion/drift correction).

Terraform's `data "dcapi_*"` blocks solve this: read-only lookups, no create/update/
delete, safe to point at objects this config does not manage.

## 2. Candidate resources for data sources

Based on `docs/Open-api-sprc.yaml` (every collection has a `list*` endpoint) and
which objects are realistically shared/pre-existing rather than provisioned fresh
per Terraform run:

### Good candidates (implement)

| Data source | DC-API list endpoint | Rationale |
|---|---|---|
| `dcapi_tenant` | `listTenants` | Org-level, provisioned once, referenced by many configs |
| `dcapi_project` | `listProjects` | Often pre-provisioned by platform admins |
| `dcapi_vnet` | `listVNets` | Shared network infra, referenced by name across teams |
| `dcapi_subnet` | `listSubnets` | Same as VNet — looked up within a known VNet |
| `dcapi_route_table` | `listRouteTables` | Shared routing config, reused across subnets |
| `dcapi_network_security_group` | `listSecurityGroups` | Shared NSGs applied across multiple workloads |
| `dcapi_key_vault` | `listKeyVaults` | Central secrets store, often owned by a separate security team's config |
| `dcapi_private_dns_zone` | `listDNSZones` | Shared DNS zones spanning multiple projects |
| `dcapi_dns_record` | `listDNSRecords` | Lookup of existing records (e.g. to build dependent resources) |
| `dcapi_vnet_peering` | `listPeerings` | Inspect an existing peering's status/state |
| `dcapi_region` (new) | `listRegions` | Platform-wide, never created by users |
| `dcapi_image` (new) | `listImages` | Platform-wide, never created by users |

### Should stay resource-only (do not implement as data sources)

- `dcapi_virtual_machine`, `dcapi_bastion`, `dcapi_cluster`, `dcapi_node_pool` — actively
  provisioned compute; no realistic "reference an existing one" use case for this provider today.
- `dcapi_service_account` — secrets are only returned at creation time; a lookup
  would expose no usable credential and could mislead users into thinking one exists.
- `dcapi_nsg_attachment`, `dcapi_route_table_association` — pure join records, no
  independent identity worth looking up.
- `dcapi_private_endpoint` — tightly coupled child of a specific key vault; not
  independently referenced.

## 3. Architecture changes required

### 3.1 Client layer (`internal/client/`)

No `List*` methods exist today — every client method requires the object's UUID
already known (`GetVNet(ctx, tenantID, projectID, vnetID)`). Data sources need to
resolve a human-readable `name` to that UUID, so each targeted entity file needs a
new list method:

```go
// internal/client/vnet.go
func (c *DCAPIClient) ListVNets(ctx context.Context, tenantID, projectID string) ([]VNetResponse, error)
```

- Reuses the existing `doRequest` chokepoint — same auth/error handling, just a
  different path and a `[]XResponse` decode target instead of a single object.
  The DC-API list endpoints appear to return the full collection for the given
  tenant/project scope (no server-side name filter documented in the spec), so
  **filtering by name happens client-side**, inside each data source's read
  function, not inside the client method.
- Add one `List*` method per entity in section 2's "Good candidates" table, in
  its existing client file (`internal/client/vnet.go`, `subnet.go`, `key_vault.go`,
  `dns_record.go`, `private_dns_zone.go`, `vnet_peering.go`, `nsg.go`,
  `route_table.go`, `project.go`, `tenant.go`).
- New client files needed for entities with no resource today: `internal/client/region.go`,
  `internal/client/image.go` — read-only structs + `List*` only, no Create/Get/Delete.

### 3.2 Data source implementations (`internal/datasources/` — new package)

Create a new package parallel to `internal/resources/`, one file per data source,
mirroring the resource pattern but with only a read function:

```go
// internal/datasources/vnet.go
func DataSourceVNet() *schema.Resource {
    return &schema.Resource{
        ReadContext: dataSourceVNetRead,
        Schema: map[string]*schema.Schema{
            "tenant_id":  {Type: schema.TypeString, Required: true},
            "project_id": {Type: schema.TypeString, Required: true},
            "name":       {Type: schema.TypeString, Required: true},
            // computed, mirrors resourceVNet's computed fields:
            "vnet_uuid":     {Type: schema.TypeString, Computed: true},
            "address_space": {Type: schema.TypeString, Computed: true},
            "region":        {Type: schema.TypeString, Computed: true},
            "status":        {Type: schema.TypeString, Computed: true},
        },
    }
}

func dataSourceVNetRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
    c := meta.(*client.DCAPIClient)
    tenantID := d.Get("tenant_id").(string)
    projectID := d.Get("project_id").(string)
    name := d.Get("name").(string)

    vnets, err := c.ListVNets(ctx, tenantID, projectID)
    if err != nil {
        return diag.FromErr(err)
    }
    for _, v := range vnets {
        if v.Name == name {
            d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, v.ID))
            // appendSet(...) each computed field, same helper as resources
            return nil
        }
    }
    return diag.Errorf("no vnet named %q found in project %s", name, projectID)
}
```

- Reuse `internal/resources/helpers.go`'s `appendSet` helper (or move it to a
  shared location, e.g. `internal/common/`, since both `resources` and
  `datasources` packages need it — see section 3.4).
- State ID convention matches existing resources (`tenant/project/uuid`) so any
  computed reference (e.g. passing `data.dcapi_vnet.example.id` into a subnet
  resource's `vnet_id` field) stays consistent with how resources already
  cross-reference each other.
- Lookup key is `name` for all entities except `dcapi_dns_record` (likely needs
  `zone_id` + `name` + `record_type` as the natural unique key — confirm against
  spec) and `dcapi_region`/`dcapi_image` (likely just `name`, no tenant/project scoping).

### 3.3 Provider registration (`internal/provider/provider.go`)

The `schema.Provider` struct currently has no `DataSourcesMap` field at all. Add it
alongside the existing `ResourcesMap`:

```go
func New() *schema.Provider {
    return &schema.Provider{
        Schema: map[string]*schema.Schema{ /* unchanged */ },
        ResourcesMap: map[string]*schema.Resource{ /* unchanged */ },
        DataSourcesMap: map[string]*schema.Resource{
            "dcapi_tenant":                 datasources.DataSourceTenant(),
            "dcapi_project":                datasources.DataSourceProject(),
            "dcapi_vnet":                   datasources.DataSourceVNet(),
            "dcapi_subnet":                 datasources.DataSourceSubnet(),
            "dcapi_route_table":            datasources.DataSourceRouteTable(),
            "dcapi_network_security_group": datasources.DataSourceNSG(),
            "dcapi_key_vault":              datasources.DataSourceKeyVault(),
            "dcapi_private_dns_zone":       datasources.DataSourcePrivateDNSZone(),
            "dcapi_dns_record":             datasources.DataSourceDNSRecord(),
            "dcapi_vnet_peering":           datasources.DataSourceVNetPeering(),
            "dcapi_region":                 datasources.DataSourceRegion(),
            "dcapi_image":                  datasources.DataSourceImage(),
        },
        ConfigureContextFunc: configureProvider, // unchanged — same *client.DCAPIClient meta
    }
}
```

No change needed to `ConfigureContextFunc` — the same `*client.DCAPIClient` meta
object is reused by data source `ReadContext` functions exactly as resources do.

### 3.4 Shared helper extraction

`appendSet` currently lives in `internal/resources/helpers.go`. Since
`internal/datasources` needs the identical helper and Go doesn't allow importing
a sibling package's unexported-adjacent internals cleanly, either:
- Export it as-is and import `internal/resources` from `internal/datasources`
  (simplest, but creates a cross-dependency between two otherwise-parallel packages), or
- Move `appendSet` into a new `internal/common/` package and update both
  `internal/resources` and `internal/datasources` to import from there (cleaner,
  slightly more churn — touches every existing resource file's import).

Recommend the `internal/common/` extraction since it keeps `resources` and
`datasources` decoupled long-term.

## 4. Step-by-step implementation order

1. Create `internal/common/helpers.go`, move `appendSet` there, update all
   existing `internal/resources/*.go` imports (mechanical, low risk).
2. Add `List*` client methods for the 10 "good candidate" entities that already
   have resources (section 3.1), one entity at a time, each verified against
   `docs/Open-api-sprc.yaml`'s list endpoint response schema.
3. Add new read-only client files for `region` and `image` (no existing resource
   to pattern-match against, so these need direct spec reading).
4. Implement `internal/datasources/*.go` for each entity, in the same priority
   order as section 2's table (start with `vnet` and `subnet` — most likely to be
   exercised first, and `vnet` is the best-understood resource per the earlier
   codebase survey).
5. Wire `DataSourcesMap` into `internal/provider/provider.go`.
6. Add manual smoke-test blocks to `test/main.tf` for each new data source,
   following `docs/manual-testing-plan.md`'s existing structure.
7. Add `examples/<name>_lookup/main.tf` for each data source (mirrors the existing
   `examples/<resource_name>/main.tf` convention).
8. Update `README.md`: add a `### dcapi_vnet (data source)` — style subsection per
   entity (mirroring the existing per-resource sections), and extend the "State ID
   Encoding" table to note data sources reuse the same ID format.

## 5. Open questions to confirm against the OpenAPI spec before coding

- Whether DC-API list endpoints support pagination — if so, `List*` client
  methods need to page through results before client-side name-filtering, not
  just decode a single response body.
- Whether `dcapi_dns_record`'s natural lookup key should be `(zone_id, name)` or
  `(zone_id, name, record_type)` — a zone can have multiple record types for the
  same name (e.g. A and TXT).
- Whether `dcapi_region` / `dcapi_image` responses are truly tenant-agnostic
  (global) or still require a tenant/project scope in the URL path per the spec.
