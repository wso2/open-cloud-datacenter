# CLAUDE.md — Open Cloud Datacenter Control Plane

You're on the **`controlplane`** branch of `open-cloud-datacenter`. This branch
holds the control-plane services — **dc-api** (Go REST API), **dcctl** (CLI), and
**cloud-ui** (React web app) — that turn raw Harvester + Rancher infrastructure
into a self-service Cloud Provider experience.

## THE BIG PICTURE (read this first, every time)

The goal: replace raw Terraform module delivery with a **Cloud Provider
experience** — developers use `dcctl` (or cloud-ui) like they use `aws` or `az`,
with no Terraform knowledge required. The earlier model delivered infrastructure
as Terraform modules, which leaked Harvester/Rancher/RKE2/TF-state abstractions to
tenants, made quotas unenforceable, and had no audit trail. dc-api is the answer:
one REST API that hides backend complexity, enforces quotas, owns state, and gives
a real developer experience.

```
dcctl / cloud-ui  →  OIDC (any provider)  →  DC-API  →  PostgreSQL (state)
                                                     →  Harvester (VMs via KubeVirt CRDs)
                                                     →  Rancher   (RKE2 clusters via REST v3)
                                                     →  KubeOVN   (tenant VPC networking)
```

**When you go down a rabbit hole** (debugging, testing, fixing a specific thing),
return to: *Does this unblock the current goal?* For the technical deep dive see
[`docs/dc-api-architecture.md`](docs/dc-api-architecture.md).

---

## Repositories

| Repo / branch | Purpose |
|---|---|
| `open-cloud-datacenter` @ **`controlplane`** (this branch) | dc-api + dcctl + cloud-ui — the control plane (monorepo) |
| `open-cloud-datacenter` @ **`terraform`** | Shared Terraform module catalog (`platform/`, `tenancy/`, `cloud/`, `operators/`) that stands up Harvester + Rancher + networking |
| A consumer / instance repo | Per-environment Terraform + Flux overlays that deploy this control plane onto a cluster |

This is a **two-repo split**: the *source* (modules + control-plane code here) vs
the *consumer* (per-environment instances). Before making changes that touch the
`terraform` branch or a consumer repo, read the `CLAUDE.md` in each first — they
establish branching policy, cross-repo SemVer versioning, and the
`terraform fmt`/`validate` commands.

---

## Project Structure

```
open-cloud-datacenter (controlplane)/
├── dc-api/                         Go REST API server
│   ├── cmd/dc-api/main.go          Entry point — wires everything, graceful shutdown
│   ├── go.mod                      module: github.com/wso2/dc-api, go 1.26
│   ├── Dockerfile                  Multi-stage; ships distroless/static:nonroot
│   ├── openapi.yaml                The API contract (source of truth — see below)
│   ├── deploy/                     Legacy K8s skeletons — NOT applied (see "Deployment")
│   └── internal/
│       ├── config/config.go        Env-var config (DCAPI_* prefix, 12-factor)
│       ├── models/                 Domain types: Resource, VMSpec, ClusterSpec, Quota, RBAC
│       ├── db/
│       │   ├── schema.sql          PostgreSQL DDL (idempotent — see docs/lessons-learned.md)
│       │   ├── db.go               ResourceRepository (Repository Pattern)
│       │   └── migrate.go          Applies schema on startup
│       ├── providers/
│       │   ├── interface.go        Compute/Cluster/Network provider interfaces (Strategy)
│       │   ├── factory.go          NewComputeProvider / NewClusterProvider (Factory)
│       │   ├── harvester/          Harvester driver — dynamic client, KubeVirt CRDs
│       │   ├── rancher/            Rancher driver — REST v3
│       │   └── kubeovn/            KubeOVN driver — tenant VPC networking
│       ├── api/
│       │   ├── middleware/         OIDC JWT validation → tenant/principal in context
│       │   ├── handlers/           vm, cluster, project, member, network, keyvault, …
│       │   └── router.go           Chi router composition root (Dependency Injection)
│       └── reconciler/             Background goroutine: PENDING/DELETING → real state
│
├── dcctl/                          Cobra CLI (go 1.22; OIDC Authorization Code + PKCE)
│   ├── cmd/                        login, logout, create/get/list/delete, kubeconfig, tenant, project
│   └── internal/{auth,config,client}
│
├── cloud-ui/                       React + Fluent UI web app (Vite + pnpm)
├── flux/                           GitOps deployment recipes (infrastructure/ + platform/)
├── examples/consumer/flux/         Per-environment overlay template
├── scripts/                        bootstrap.sh, dev-up.sh (local dev loop), init-flux.sh
├── .github/workflows/              dc-api.yaml, cloud-ui.yaml, contract.yaml
├── docs/                           Architecture, runbooks, decisions, lessons learned
└── CLAUDE.md                       This file
```

---

## Design Patterns (understand before editing)

| Pattern | File | Rule |
|---|---|---|
| **Strategy** | `providers/interface.go` | Handlers only see the interface. Never import `harvester`/`rancher`/`kubeovn` packages from handlers. |
| **Factory** | `providers/factory.go` | Adding a provider = new case here + new `internal/providers/<name>/`. Nothing else changes. |
| **Repository** | `db/db.go` | All SQL lives in the db package. Handlers call `repo.Create()`, never `pool.Query()` directly. |
| **Dependency Injection** | `router.go` → `handlers/` | Handlers receive `repo` and `provider` via constructor. Never instantiate them inside a handler. |
| **Middleware Chain** | `middleware/auth.go` | Auth runs before every `/v1/*` handler. Handlers read tenant/user from `context`, never from the token. |

---

## API contract — `dc-api/openapi.yaml` is the source of truth

Every public endpoint of dc-api is described in `dc-api/openapi.yaml` (OpenAPI
3.0.3). The spec is **the contract** between dc-api and every client. Rules:

1. **Spec and handler ship together.** A PR that changes a handler's path, method,
   request/response shape, status code, or auth requirement MUST update
   `openapi.yaml` in the same commit. Reviewers reject PRs that don't.
2. **The spec leads, the code follows.** Write the spec entry first (or
   concurrently) when designing a new endpoint — that's what `api-designer` is for.
3. **Validate before merge.** Run `npx @redocly/cli lint dc-api/openapi.yaml`
   after edits.
4. **Don't add schemas you can't use.** An unreferenced `components/schemas/X` is
   noise — wire the endpoint that returns it or remove it until you need it.

### Consumers of the spec (so you know what breaks)

| Consumer | How it consumes the spec | What breaks if you change the spec without updating it |
|---|---|---|
| **cloud-ui** | `pnpm gen:api` → `src/api/generated/types.ts` (auto-runs on `predev`/`prebuild`); `openapi-fetch` for runtime calls | TypeScript build fails — exactly the early warning we want |
| **dcctl** | `oapi-codegen` Go client at `dcctl/internal/client/generated/`; hand-written wrappers add convenience helpers | Go compile error against the regenerated typed methods |
| **terraform-provider-dcapi** (planned) | Same Go codegen as dcctl | n/a yet |
| **Contract tests** | Schemathesis hits an in-process dc-api with nopped backends; runs on push + PR via `.github/workflows/contract.yaml`, tag-scoped in `dc-api/test/contract/contract_test.go` | CI failure on the contract job |

### Adding a new endpoint, end to end

1. Design the request/response with `api-designer`; add it to `openapi.yaml`; lint.
2. Implement the Go handler; `go build ./...`.
3. Wire it into `router.go`.
4. Write the integration test under `dc-api/test/integration/`.
5. Bump cloud-ui types: from `cloud-ui/`, `pnpm gen:api && pnpm exec tsc --noEmit
   && pnpm lint`. If the UI uses the endpoint, write the calling code now.
6. If dcctl exposes it too, update `dcctl/internal/client/`.
7. Single PR, all of it.

---

## Core Architectural Principles (never violate these)

1. **dc-api's hierarchy is independent of Rancher and Harvester.** Users see
   `Tenant → Project → Resource` (GCP-like). Rancher "projects", Harvester
   "namespaces", KubeVirt — internal plumbing. They must never appear in the
   public API or CLI output.

2. **Rancher and Harvester credentials never leave dc-api.** dc-api holds one
   master Harvester kubeconfig and one Rancher admin token. Teams get dc-api roles
   (owner/member/viewer at tenant/project scope). No team ever gets direct Rancher
   or Harvester access via dc-api.

3. **Tenant + project isolation is multi-layer.** Every per-tenant DB row carries
   both `tenant_uuid` and `project_uuid` — the immutable filters in every repo
   query. Slugs are the human handle; UUIDs gate access. Within a project,
   capacity and object counts are enforced at the dc-api layer AND mirrored as a
   Kubernetes `ResourceQuota` (defense-in-depth). RBAC is per-role via
   `role_assignments` with a polymorphic `scope_type`. See
   [`docs/rbac.md`](docs/rbac.md).

4. **Tenant capacity ceilings live on the tenant row.**
   `tenants.{cpu_cores_cap, memory_gb_cap, storage_gb_cap}` is the platform-admin
   ceiling; owners distribute it across projects. Project create/PATCH validates
   `sum(project quotas) ≤ tenant cap` inside a transaction with `SELECT FOR UPDATE`
   on the tenants row. Admin PATCH refuses a shrink below already-allocated sums.
   See `internal/db/projects.go::CreateProject` and `tenants.go::UpdateTenantCap`.

5. **Swapping a backend must not change the API.** Replace Harvester with
   OpenStack? New provider struct, zero API changes. That's why the Strategy
   Pattern exists. Don't couple handler code to providers.

---

## Build & Run

```bash
# DC-API
cd dc-api && go mod tidy && go build ./cmd/dc-api/ && ./dc-api   # needs DCAPI_* env

# dcctl
cd dcctl && go mod tidy && go build -o ~/bin/dcctl .
```

- **Full local dev loop** (Postgres + dc-api + cloud-ui): `scripts/dev-up.sh
  local-stack`. See [`docs/local-dev.md`](docs/local-dev.md) for the walk-through.
- **Config:** `DCAPI_*` env vars. [`.env.example`](.env.example) is the source of
  truth and documents every variable; the README has a grouped reference table.

---

## Deployment is GitOps-driven (Flux)

dc-api and cloud-ui are deployed by **Flux GitOps**, not by hand and not by
`kubectl apply`. The deployment recipes live in **[`flux/`](flux)**
(`infrastructure/` = cert-manager/ingress-nginx/sealed-secrets; `platform/` =
dc-api/cloud-ui/dc-postgres/…). A consumer repo Kustomize-references
`flux/platform/` as a remote base and overlays its environment (hostnames,
sealed secrets, image pins). `ImageUpdateAutomation` rolls new images as CI
publishes them. See [`flux/DEPLOY.md`](flux/DEPLOY.md) and
[`flux/README.md`](flux/README.md).

- The `dc-api/deploy/*.yaml` and `cloud-ui/deploy/*.yaml` files are **legacy
  skeletons** — they are NOT applied to any cluster; editing them changes nothing.
- **Don't hand-edit live Secrets or manifests** (`kubectl edit secret …`). Change
  them through the consumer's Flux overlay (sealed secrets) — a manual edit is
  reverted on the next reconcile.
- CI (`.github/workflows/{dc-api,cloud-ui}.yaml`) builds and publishes
  `ghcr.io/wso2/{dc-api,dc-api-webhook,cloud-ui}` on every merge to `controlplane`.

---

## Before pushing changes

CI runs unit tests on every push, but **integration tests are not gated in CI**
(they need a live Harvester + KubeOVN cluster). Before pushing a change that
touches the KubeOVN driver, network handlers, the DB layer, or the auth
middleware, run the integration suite locally against your dev cluster:

```bash
cd dc-api
KUBECONFIG=$HOME/.kube/config KUBE_CONTEXT=<your-harvester-context> \
  go test -tags integration -timeout 20m ./test/integration/...
```

See `dc-api/test/integration/README.md` for coverage. For changes that don't touch
those areas (CLI, docs, manifests), `go test ./...` (unit tests) is enough.

**Spec-diff check for CR-building code.** Whenever dc-api generates a Kubernetes
resource that mirrors a proven live object (Rancher `Cluster`, `HarvesterConfig`,
KubeOVN `Vpc`/`Subnet`, `NetworkAttachmentDefinition`, per-VPC CoreDNS, …), the PR
MUST include a structural diff of the generated object against a known-working
reference from the cluster. Unit tests pass on the shape you *think* is right; the
live cluster doesn't lie. This costs ~5 minutes and catches whole classes of "CR
looks right but isn't" bugs.

---

## What exists today

Shipped and running: **M1** core (domain models, Postgres schema + repository,
Harvester + Rancher drivers, OIDC auth middleware, async VM/cluster handlers, the
reconciler, dcctl). **M1.5** RBAC (role assignments, service accounts, member
management). **M2** networking (VPC/Subnet/NSG/Peering/RouteTable/PrivateDnsZone on
self-managed KubeOVN, per-VPC NAT gateway + CoreDNS, bastions). **M2.5** the
`Tenant → Project → Resource` hierarchy with hybrid quota caps, across dc-api +
dcctl + cloud-ui. **M3** Key Vault (chunks 1–2 shipped; per-tenant OpenBao HA
spiked — see [`docs/spike-m3-keyvault-openbao-ha.md`](docs/spike-m3-keyvault-openbao-ha.md)).

In flight / next: M2 storage (volumes, snapshots, LB IPAM); the managed-services
framework ([`docs/managed-services-integration.md`](docs/managed-services-integration.md),
[`docs/managed-services-framework.md`](docs/managed-services-framework.md)); the
Terraform provider; and defense-in-depth hardening
([`docs/defense-in-depth.md`](docs/defense-in-depth.md)).

---

## Agent Usage — Mandatory

Specialized agents exist for this project. Check the available agent list and
invoke them based on context. Do not wait to be told. Agents save context, run
focused work in isolation, and protect the main conversation from noise.

**When to invoke without being asked:**

| Situation | Agent |
|---|---|
| Any API endpoint or CLI command changed | `docs-writer` — update reference docs |
| Making any claim about Rancher/Harvester capabilities | `rancher-harvester-specialist` — verify first |
| Kubernetes manifests, Helm, RBAC, ingress, or storage on Harvester-hosted clusters | `rancher-harvester-specialist` — covers the full Harvester+k8s stack |
| Implementing a new handler, provider method, or DB query | `test-engineer` — write tests |
| Designing a new API resource, endpoint shape, or naming | `api-designer` — review before implementing |
| Implementing Go business logic or DB layer changes | `backend-developer` — delegate the implementation |
| Building or changing CLI commands or output formatting | `cli-developer` — delegate the implementation |
| Any UI design or web-app work (mockups, React/TS, Fluent UI) | `frontend-developer` — owns the web UI end-to-end |
| Any Terraform / IaC work (layer code, modules, helm_release modelling, secrets, two-phase applies) | `terraform-specialist` — owns the IaC craft |
| Broad codebase exploration spanning multiple files/packages | `Explore` — faster than manual grep loops |

These are not optional steps. A task is not complete until the relevant agents
have run. If skipped, state why explicitly.

**When you create a NEW agent** under `.claude/agents/`, you MUST also register it
in the table above with a clear trigger phrase and a one-line scope. This is what
makes auto-invocation work in future sessions — without the table entry, the next
session won't know when to spawn it. It's part of the agent-creation task itself.

---

## Hard rules

- **Never `sed -i` (or any `-i` in-place stream editor) on macOS.** It has
  silently truncated tracked files to 0 bytes here — twice. Use the `Edit` tool
  (or `Write` for full rewrites). Read-only `sed`/`awk` to stdout is fine.
- **Never `git commit -am` while sub-agents may have written to the working tree.**
  `-a` auto-stages everything and has swept up untested agent edits. Use
  `git add <specific files>` and review the staged set first.
- **Verify sub-agent test claims yourself.** "All clean" / "53/0/1 PASS" is
  untested code until you read the actual output. Run the suite, count
  PASS/FAIL/SKIP against the expected total, then mark the chunk done.
- **Explain non-trivial changes in plain English in the PR description** — 2–5
  sentences on what it does, why, and any operational side-effect a reviewer would
  notice. Reviewers may not be Go developers. Skip for one-liners and typo fixes.

These are the operational guardrails. The full project-specific traps (KubeOVN
bridge outages, KubeVirt DHCP races, CDI prime-PVC quota, ARC runner orphans, …)
are in [`docs/lessons-learned.md`](docs/lessons-learned.md) — read them before
touching networking, the DB layer, or CI.

---

## Reference

| For | See |
|---|---|
| Why each design choice was made | [`docs/decisions.md`](docs/decisions.md) |
| Project-specific traps already paid for | [`docs/lessons-learned.md`](docs/lessons-learned.md) |
| Every `DCAPI_*` env var | [`.env.example`](.env.example) + the README reference table |
| Technical architecture deep dive | [`docs/dc-api-architecture.md`](docs/dc-api-architecture.md) |
| RBAC model | [`docs/rbac.md`](docs/rbac.md) |
| Local development walk-through | [`docs/local-dev.md`](docs/local-dev.md) |
| GitOps deployment | [`flux/DEPLOY.md`](flux/DEPLOY.md) |
