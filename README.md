# terraform-provider-dcapi

The Terraform provider for **DC-API**, the Open Cloud Datacenter control plane.
It lets users manage tenants, projects, VMs, clusters, networks, and key vaults
declaratively — the same resources `dcctl` and `cloud-ui` drive, exposed as
Terraform resources and data sources.

> **This is an incubation branch, not a release.** The provider lives here as an
> orphan branch (same standalone-tree model as the `terraform` module catalog and
> `operators` branches) until it earns its own repository. See
> [Future: standalone repo](#future-standalone-repo).

---

## The one rule: never copy the OpenAPI spec

**`dc-api/openapi.yaml` on the `controlplane` branch is the single source of
truth.** It is the contract for every client — `cloud-ui`, `dcctl`, and this
provider. There is exactly **one** copy of it, and it does not live here.

Do **not** vendor, fork, or paste a copy of `openapi.yaml` into this branch. A
second copy is a second source of truth, and it *will* drift.

Instead, follow the same pattern the in-tree clients use: **generate the Go
client from the controlplane spec and commit only the generated output.** The
only thing that differs here is that, being out-of-tree, we reference the spec by
URL instead of by relative path.

| Client | Where it lives | How it sources the spec | What it commits |
|---|---|---|---|
| `cloud-ui` | `controlplane` | `openapi-typescript ../dc-api/openapi.yaml` | `src/api/generated/types.ts` |
| `dcctl` | `controlplane` | `oapi-codegen ... ../../../../dc-api/openapi.yaml` | `internal/client/generated/dcapi.gen.go` |
| **this provider** | here (out-of-tree) | `oapi-codegen ... <raw URL to controlplane spec>` | the generated client package |

### Regenerating the client

Day to day, generate against the **latest** controlplane spec:

```bash
oapi-codegen -config oapi.cfg.yaml \
  https://raw.githubusercontent.com/wso2/open-cloud-datacenter/controlplane/dc-api/openapi.yaml
```

Wire this as a `//go:generate` directive next to the generated package (mirror
`dcctl/internal/client/generated/gen.go`) so `go generate ./...` refreshes it.
Commit the generated `.go` file; never commit the YAML.

> **At release time, pin instead of tracking latest.** Swap `controlplane` in the
> URL for a tag or commit SHA so a published provider version is reproducibly
> built against a known API version. Record that ref in the changelog. Tracking
> `controlplane` is for development; a release pins.

If `go generate` produces a diff, the API changed under you — review it, adapt the
provider, and commit the regenerated client together with the provider changes in
one PR. (A CI job that regenerates against `controlplane` and fails on an
unexpected diff gives the same early-warning the contract tests give the in-tree
clients — add it once the provider stabilizes.)

---

## Layout

Standard Terraform provider layout. Suggested shape — adapt to what the code
actually needs:

```
terraform-provider-dcapi/
├── main.go                  # provider entry point (plugin serve)
├── go.mod                   # module: github.com/wso2/terraform-provider-dcapi
├── oapi.cfg.yaml            # oapi-codegen config (see dcctl's for reference)
├── internal/
│   ├── client/generated/    # GENERATED from the controlplane spec — do not hand-edit
│   └── provider/            # resources & data sources (dcapi_vm, dcapi_cluster, …)
├── examples/                # example .tf for the registry + docs
├── docs/                    # tfplugindocs output
└── .github/workflows/       # build, acceptance tests, (later) release
```

## Build & test

```bash
go generate ./...    # refresh the client from the controlplane spec
go build ./...
go test ./...        # unit tests
# acceptance tests hit a real dc-api and create real resources:
TF_ACC=1 DCAPI_ENDPOINT=... go test ./internal/provider/ -run TestAcc -v
```

Spin up a local dc-api for acceptance tests with `scripts/dev-up.sh local-stack`
on the `controlplane` branch.

---

## Releases

Git tags are repo-global, so — exactly like the `terraform` branch tags its
releases `terraform/vX.Y.Z` — releases from this branch are namespaced:

```
terraform-provider-dcapi/vX.Y.Z
```

This keeps them clear of `controlplane`'s bare `vX.Y.Z` tags. Do **not** cut a
bare `vX.Y.Z` from this branch.

---

## Future: standalone repo

The end state is a dedicated **`terraform-provider-dcapi`** repository so it can
publish to the Terraform Registry (which requires a repo named
`terraform-provider-<name>` with its own `vX.Y.Z` release tags and GoReleaser +
GPG signing). The orphan-branch layout here is a deliberate dress rehearsal for
that: because this branch is already a standalone tree with no shared history,
extraction is a clean

```bash
git push git@github.com:wso2/terraform-provider-dcapi.git terraform-provider-dcapi:main
```

— no `git filter-repo` surgery, full history preserved. At that point the raw-URL
spec reference keeps working unchanged; only the tag prefix is dropped.

---

## Maintaining the provider

This provider is a pure client of DC-API: it exposes only what the API contract
offers, and every resource and data source maps to a `dc-api` endpoint. Provider
craft — resource schemas, plan/apply semantics, acceptance tests,
`tfplugindocs`, and release packaging — follows the upstream Terraform plugin
conventions. When the API contract changes, regenerate the client (see above)
and adapt the affected resources in the same change. All contract questions trace
back to `dc-api/openapi.yaml` on `controlplane`.
