# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Terraform provider (built with `terraform-plugin-sdk/v2`) for WSO2's internal DC-API cloud platform ("Sovereign Cloud"). It exposes tenants, projects, networking (vnet/subnet/nsg/route tables/peering/private endpoints/DNS), compute (VM, cluster, node pool, bastion), and secrets (key vault) as Terraform resources and data sources, talking to DC-API over a bearer-token-authenticated HTTP/HTTPS API.

Module path: `terraform-provider-dcapi` (Go 1.21).

## Commands

```bash
make build     # go build -o terraform-provider-dcapi .
make install   # build + copy into ~/.terraform.d/plugins/registry.terraform.io/wso2/dcapi/0.1.0/<OS>_<ARCH>/
make clean     # remove compiled binary
```

There is no test suite (`go test`) in this repo currently. To exercise a change manually, `make install` and run Terraform against an example under `examples/<resource>/main.tf` (each has a real DC-API endpoint/token expected via `DCAPI_ENDPOINT`/`DCAPI_TOKEN` env vars).

`go build ./...` / `go vet ./...` are the fastest correctness checks when adding or editing a resource.

## Architecture

Three-package structure, one file per API object in each:

- `internal/client/` — one `*.go` file per DC-API object (e.g. `vnet.go`, `subnet.go`, `vm.go`). All HTTP traffic funnels through the single `doRequest` method on `DCAPIClient` in `client.go` — no other file makes HTTP calls directly. `doRequest` handles auth headers, JSON encoding, and translates non-2xx responses into errors (with special-cased parsing for HTTP 400 `quota_exceeded` bodies to surface cap/allocated/available/requested numbers).
- `internal/resources/` — one `*.go` file per Terraform resource, each exposing a `Resource<Name>() *schema.Resource` factory with `CreateContext`/`ReadContext`/`UpdateContext`/`DeleteContext`. Resources with all-immutable fields omit `UpdateContext` (e.g. `vnet.go`, `subnet.go`) and rely on `ForceNew: true` per field instead.
- `internal/datasources/` — read-only counterparts; `DataSource<Name>() *schema.Resource` with only `ReadContext`. Deliberately duplicates the tiny `appendSet` helper from `internal/resources/helpers.go` rather than sharing a common package (not worth the import indirection for a 6-line function) — keep that duplication if you touch either helper.
- `internal/provider/provider.go` — wires everything together: provider schema (`endpoint`, `token`, both fall back to `DCAPI_ENDPOINT`/`DCAPI_TOKEN` env vars), `ResourcesMap`, `DataSourcesMap`, and `configureProvider` which builds the shared `*DCAPIClient` passed to every resource/datasource as the `meta interface{}` parameter.
- `main.go` — plugin server entry point, calls `plugin.Serve` with `provider.New`.

### Adding a new resource

1. Add API call methods to a new file in `internal/client/`, following the existing per-object file pattern.
2. Implement the Terraform resource in `internal/resources/` with `CreateContext`/`ReadContext`/`UpdateContext`/`DeleteContext` (omit Update if all fields are `ForceNew`).
3. Register it in `internal/provider/provider.go` under `ResourcesMap` (and `DataSourcesMap` if a read-only lookup makes sense).
4. Add an example under `examples/<resource>/main.tf`.

### State ID encoding

Each resource's Terraform state ID is a composite path mirroring the DC-API URL structure, since Read/Delete must rebuild the full URL from the ID alone:

| Resource | State ID format | Example |
|---|---|---|
| `dcapi_tenant` | `slug` | `wso2` |
| `dcapi_project` | `tenant/project` | `wso2/infra` |
| `dcapi_vnet` | `tenant/project/vnet_uuid` | `wso2/infra/bb0e8400-...` |
| `dcapi_subnet` | `tenant/project/vnet_uuid/subnet_uuid` | `wso2/infra/bb0e8400-.../cc0e8400-...` |
| `dcapi_virtual_machine` | `tenant/project/vm_uuid` | `wso2/infra/dd0e8400-...` |

Other resources follow the same convention of encoding parent path segments before the resource's own UUID.

### Async operation lifecycle

Most create/delete calls return HTTP 202 Accepted with a `PENDING`/`PROVISIONING` status. Resource `CreateContext`/`DeleteContext` implementations poll via `helper/resource.StateChangeConf` (typically every 15s) until the object reaches a terminal state (`ACTIVE`, deleted, etc.), governed by the `Timeouts` block on the resource. Follow this pattern (see `internal/resources/vnet.go`) for any new resource whose API is asynchronous.

### Resource dependency order

Resources must be created bottom-up: `dcapi_tenant` → `dcapi_project` → `dcapi_vnet` → `dcapi_subnet` → (`dcapi_virtual_machine` | `dcapi_cluster` | ...). Child resources take the parent's slug/UUID as required, `ForceNew` input attributes (not computed from a implicit relationship), so parent creation order matters when writing example `.tf` files.

## Notable behaviors to know about

- `dcapi_tenant` delete is a no-op against the API — it only removes the resource from Terraform state.
- `dcapi_project` delete returns HTTP 409 if child VNets/VMs still exist — they must be destroyed first.
- `dcapi_virtual_machine` returns `private_key` and `console_password` only at creation time; they're stored as sensitive state values and never re-fetched on `Read`.
- Deleting the last subnet in a VNet triggers NAT/CoreDNS cleanup, adding 10–15 minutes to the delete operation.
- `dcapi_virtual_machine` supports two mutually exclusive networking modes: VPC mode (`vnet_id`/`subnet_id`) or legacy bridge mode (`network_name`) — don't set both.

## CodeRabbit

This branch (`terraform-provider-dcapi`) is a separate long-lived line with its own root tree, so `.coderabbit.yaml` explicitly opts it into auto-review via an anchored `base_branches` regex — the default branch's CodeRabbit config does not apply here.
