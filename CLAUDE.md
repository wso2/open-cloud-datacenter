# CLAUDE.md — Open Cloud Datacenter Terraform Modules

> Documents the **`terraform`** branch of `open-cloud-datacenter`. Derived by
> reading the module tree (`modules/`), `README.md`, `docs/architecture.md`, the
> CI workflows, and the pre-PR review gate. Every claim below is traceable to a
> file in this branch — keep it that way when you edit it.

## THE BIG PICTURE (read this first, every time)

This branch is the **Open Cloud Datacenter (OCDC) Terraform module catalog** —
reusable, single-concern modules that stand up a self-hosted, cloud-style datacenter
on **Harvester HCI** (KubeVirt hypervisor) + **Rancher** (Kubernetes management
plane), with tenant RKE2 clusters running as VMs inside Harvester. It is a **source
catalog, not a deployable environment**: no root module, no backend, no
`terraform.tfvars`, no live state here.

```text
            open-cloud-datacenter @ terraform   (THIS branch — reusable modules)
            ┌──────────────────────────────────────────────────────────┐
            │  modules/{platform,tenancy,operators,cloud,addons}         │
            └──────────────────────────────────────────────────────────┘
                       ▲  source = "…?ref=terraform/vX.Y.Z"  (git + tag pin)
            ┌──────────┴───────────────────────────────────────────────┐
            │  a consumer / instance repo (per-environment)             │
            │  layered TF that pins these modules by tag and owns the    │
            │  backend, state, secrets, and all env values (hosts/IPs)   │
            └────────────────────────────────────────────────────────────┘
```

The **source-vs-consumer split** is the central idea: this repo holds the modules;
**per-environment Terraform lives in separate consumer repos** that reference them by
a branch-scoped release tag and supply every environment-specific value. Changing
module code here changes the public contract every consumer depends on.

**When you go down a rabbit hole**, return to: *does this keep the module reusable
and its public surface stable?* The deep dive — per-module purpose, provider
versions, dependency graph, deploy phases — is in
[`docs/architecture.md`](docs/architecture.md).

---

## Repositories & branches

| Repo / branch | Purpose |
|---|---|
| `open-cloud-datacenter` @ **`terraform`** (this branch) | The Terraform module catalog: `platform/`, `tenancy/`, `operators/`, `cloud/`, `addons/`. Tagged `terraform/vX.Y.Z`. |
| `open-cloud-datacenter` @ **`controlplane`** | The control-plane services — dc-api (Go REST API) + dcctl (CLI) + cloud-ui (React). The `cloud/` modules here deploy those images. |
| `open-cloud-datacenter` @ **`operators`** | Operator source code (kubebuilder projects). The `operators/` modules here re-express that source's `config/` kustomize output as typed Terraform. |
| `open-cloud-datacenter` @ **`main`** | Legacy unified module layout, frozen at `?ref=v0.4.5`. New consumers pin `terraform/*` tags instead. |
| A consumer / instance repo | Per-environment layered Terraform + GitOps overlays that pin these modules by tag and own the state + secrets. |

Each branch is a long-lived development line with its own root tree and **its own
namespaced tags** (`terraform/v0.1.0`, `operators/v0.1.0`, `controlplane/v0.1.0`).
The `terraform/` portion is a branch namespace; the `vX.Y.Z` portion is strict
SemVer (the `0.x` line means "pre-stable — surface may change"). See `README.md`
§Releases & versioning.

---

## Module catalog (`modules/`)

Five purpose-driven families — grouped by *when you need a module*, not who built
it. You can stop at any layer and still have a useful system.

```text
modules/
├── platform/                  Foundation — run once
│   ├── rancher/               Bootstrap RKE2 + Rancher VM(s) on Harvester via cloud-init; LB + IP pool; storage class/network
│   ├── harvester-integration/ Register Harvester into Rancher; UI extension; cloud credential; registration manifest
│   ├── networking/            VLAN-backed harvester_network resources (one per entry in var.vlans)
│   ├── storage/               Download + register OS images into Harvester (harvester_image)
│   ├── monitoring/            Calert + Google Chat alert routing + curated dashboards on top of rancher-monitoring
│   ├── nginx-lb-vm/           Standalone nginx LoadBalancer VM (stable join/VIP address for HA bootstrap)
│   └── identity/
│       ├── rancher-oidc/      Point Rancher auth at any generic OIDC provider
│       └── providers/{asgardeo,azure-ad}/   Per-IdP presets that emit OIDC endpoints for rancher-oidc
│
├── tenancy/                   Day-2 tenant operations
│   ├── tenant-space/          Full onboarding bundle: Rancher project + namespaces + quotas + RBAC + optional VLAN/VyOS; composes the sub-modules below
│   ├── rbac/                  Bulk-create Rancher projects + namespaces with CPU/memory/storage quotas
│   ├── cluster-roles/         Custom Rancher role templates (e.g. vm-manager, vm-metrics-observer, cluster-reader)
│   ├── vm/                    Provision a standalone Harvester VM (multi-disk, cloud-init, custom networks)
│   ├── k8s-cluster/           Provision a tenant RKE2 cluster via Rancher machine provisioning (multi-pool)
│   ├── vyos-tenant/           Per-tenant VyOS vif/DHCP/NAT config (sub-module of tenant-space)
│   ├── tenant-cloud-config/   Node cloud-init template ConfigMap for a tenant common namespace (sub-module)
│   └── rancher-bot-user/      Per-tenant CI/machine identity + token (sub-module of tenant-space)
│
├── operators/                 Managed-service operator deployments (typed TF mirror of each operator's kustomize output)
│   ├── dc-webhook/            KubeOVN/MAC-pinning mutating admission webhook for KubeVirt VMs
│   ├── keyvault/              keyvault-operator (OpenBao HA) controller + CRDs
│   └── database/              dbaas-operator (RDS-style managed PostgreSQL on KubeVirt) controller + CRDs
│
├── cloud/                     DC-API self-service layer (optional)
│   ├── dc-controlplane/       3-node HA RKE2 cluster hosting DC-API (composes tenancy/tenant-space + tenancy/k8s-cluster)
│   └── dc-services/           DC-API runtime on that cluster: Postgres + dc-api + cloud-ui + ingress + GitHub Actions runner
│
└── addons/                    Niche glue that papers over vanilla Rancher+Harvester gaps — may be deprecated as gaps close
    ├── namespace-credentials/      Long-running reconciler: auto-issue scoped SA + kubeconfig Secret per tenant namespace
    ├── harvester-cloud-credential/ Materialize a Harvester kubeconfig Secret for use as a Rancher cloud credential
    └── harvester-vm-access/        Namespace-scoped SA + kubeconfig for delegated tenant access to Harvester VMs
```

> The `README.md`/`docs/architecture.md` tables describe `operators/keyvault` and
> `operators/database` as "coming" — that prose lags the tree; both modules exist
> with real code today (their READMEs pin `?ref=terraform/v0.2.0`). Trust the tree.

The full deploy ordering (Phase 0 bootstrap → 1 Rancher auth → 2 platform →
3 tenancy → 4 operators → 5 cloud → 6 addons), per-module outputs consumed
downstream, and the dependency graph live in [`docs/architecture.md`](docs/architecture.md).

---

## Conventions & patterns (confirmed from the code — match them)

**Module file layout.** Each module is a directory with `main.tf` (+ `variables.tf`,
`outputs.tf`, `versions.tf` where present) and its own `README.md`. Bigger modules
add `templates/` (cloud-init / config `.tpl`/`.tftpl`), `crds/` (operators), and an
`examples/basic/` (7 modules ship one — `rancher`, `networking`, `storage`,
`monitoring`, `harvester-integration`, `rbac`, `k8s-cluster`).

**Providers are declared, not configured, in modules.** Every module pins its
providers in a `terraform { required_providers { … } }` block (`versions.tf` or top
of `main.tf`) but **declares no `provider "…" {}` config and no backend** — all
provider auth and the backend live in the calling consumer layer. Stated explicitly
at the top of several modules (e.g. `cloud/dc-services/main.tf`,
`operators/dc-webhook/main.tf`). Provider pins vary by module (e.g.
`harvester/harvester` ranges `~> 0.6.0` to `~> 1.7`; `rancher/rancher2 ~> 13.1`;
`hashicorp/kubernetes ~> 2.30`); the source of truth is each module's `versions.tf`,
summarized in `docs/architecture.md` §Provider version summary.

**Typed `kubernetes_*` resources for native objects.** Namespaces, Secrets,
Deployments, Services, Ingress, RBAC, StatefulSets are typed throughout (see all of
`cloud/dc-services/main.tf`) — better diffing, no CRD needed at plan time.
`kubernetes_manifest` is used for CRDs and custom resources Helm/typed resources
don't cover — operator CRDs + `MutatingWebhookConfiguration` + ServiceMonitors in
`operators/*`, PrometheusRules / Grafana-dashboard CRs in `platform/monitoring`, a
`ScheduledVMBackup` CR in `tenancy/vm`. (`monitoring` also expresses its Calert
Deployment/Service as `kubernetes_manifest` — a localized exception, not the norm.)

**Helm is invoked via the `helm` CLI, NOT the `helm_release` resource.** There are
**zero `helm_release` resources** in the catalog. Off-the-shelf charts are installed
with `null_resource` + `local-exec` running `helm upgrade --install …
oci://…`. `cloud/dc-services/main.tf` documents *why*: the TF helm provider fails
posting release Secrets through Rancher's proxy chain ("request body too large"),
while the helm CLI through the same proxy works. If you add a chart, follow that
pattern (materialize the kubeconfig with `local_sensitive_file`, trigger on
`chart_version`/values SHA, and add an idempotent `when = destroy` `helm uninstall`).

**Secrets.**
- Generated secrets use `random_password` and never leave the module (e.g. the
  Postgres password and RKE2 join token in `cloud/dc-services` and `platform/rancher`).
- Charts/Deployments consume secrets by **referencing a pre-created
  `kubernetes_secret` by name**, not by `--set` literals (e.g. the ARC runner's
  `githubConfigSecret`, the dc-api env `secret_key_ref`s). Decouples rotation from
  the release.
- Secret-bearing **variables are marked `sensitive = true`** (`vm_password`,
  `rancher_admin_password`, `harvester_kubeconfig`, `db_password`, …). Supply them
  from the consumer layer (env vars / `*.secret.tfvars` / a secrets manager) —
  `*.tfvars` is git-ignored here; **never commit a secret value.** (`docs/architecture.md`
  §Security considerations.)

**Two-phase apply where a name/endpoint is unknown at first plan.** Most modules are
single-phase. The exception is `tenancy/k8s-cluster`: when a node pool isn't in
`machine_config_overrides`, `rancher2_machine_config_v2` gets a provider-generated
random name the cluster can't reference in one plan — so apply in two phases (see the
comment at `modules/tenancy/k8s-cluster/main.tf`):
```bash
terraform apply -target=module.<name>.rancher2_machine_config_v2.pool   # phase 1
terraform apply -target=module.<name>                                   # phase 2
```
`operators/dc-webhook`'s README explicitly notes it is **single-phase** (cluster
endpoint known before apply). Document the apply order in any module that needs it.

**Module composition is by relative `source = "../…"`, not remote state.** Higher
modules call lower ones directly: `tenancy/tenant-space` composes `vyos-tenant`,
`tenant-cloud-config`, and `rancher-bot-user`; `cloud/dc-controlplane` composes
`tenancy/tenant-space` + `tenancy/k8s-cluster`, passing the Harvester k8s provider
through with `providers = { kubernetes.harvester = kubernetes.harvester }`.
`data "terraform_remote_state"` appears only as **consumer-side guidance** in
`platform/storage`'s README/outputs (how a downstream layer reads `image_ids`) — it
is for consumer layers composing module outputs across state, not for wiring modules
to each other inside this repo.

**Operator modules mirror upstream kustomize as typed TF.** `operators/{database,keyvault}`
re-express the operator's `config/default` kustomize output as typed `kubernetes_*`
resources (apply needs no kustomize) and vendor the CRD YAML under `crds/`. Their
headers give a "refresh from upstream" procedure: rebuild `kubectl kustomize …`, diff
each kind+name against the TF, reconcile, and **bump `crds/*.yaml` alongside the
operator image tag** (schema travels with the image).

**Input validation lives in the module.** Modules guard inputs with `check {}`
blocks and `lifecycle { precondition { … } }` (e.g. `platform/rancher`,
`tenancy/tenant-space`) and use `lifecycle { ignore_changes = [...] }` for fields a
controller/CI owns (CI-rolled container `image`, Rancher-set `container_resource_limit`,
auto-managed annotations / cloudinit-disk size). Preserve these — they are what keeps
re-applies and brownfield imports diff-clean.

**Backward-compat is a contract.** A module's `variables` + `outputs` are the public
surface every consumer pins against. Renaming/removing/retyping one, or changing a
default, is a **breaking change**: call it out and drive the SemVer bump (MAJOR for
breaking, MINOR for additive-with-safe-defaults, PATCH for fixes/docs — `README.md`
§Releases & versioning). The LLM review's most valuable lens here is exactly this
contract check.

**Formatting & validation.** `terraform fmt -recursive` must be clean. Run
`terraform validate` inside a module/example where providers can init. The scan gate
(below) runs an **init-free** `terraform fmt -check` + tflint + checkov + trivy; it
deliberately does **not** run `terraform validate` (that needs provider downloads).

**IaC policy / `.checkov.yaml`.** Checkov reads the repo-root `.checkov.yaml`, which
skips exactly one rule: **`CKV_TF_1`** — because example/doc module sources pin a
semantic-version tag (`?ref=terraform/vX.Y.Z`) for readability rather than a
commit-hash (commit-hash pinning is recommended for production consumers but not
enforced in this catalog's own examples). Honor that file; don't re-skip inline.

**PRs.** Fill in `pull_request_template.md`. File an issue first
(`issue_template.md`), branch `feature/<id>-<desc>` or `fix/<id>-<desc>` off
`terraform`, open the PR **against `terraform`**, and link `Closes #ID`. CodeRabbit
auto-reviews this branch (opted in via `.coderabbit.yaml`, anchored to `^terraform$`
because the default branch's config does not reach this root tree). Full guidelines:
[`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md).

---

## CI & quality gates

PRs against this branch run two GitHub checks (`.github/workflows/`):

| Workflow | Trigger | What it does |
|---|---|---|
| `terraform-scan.yml` | PR touching `*.tf` / `*.tfvars` / `*.hcl` | Trivy IaC misconfig scan; results posted as a PR comment. |
| `linter.yml` | PR + on approved review | Super-Linter over Terraform / YAML / Markdown / Shell (diff-scoped). |

---

## Before pushing — run the pre-PR review gate

A local, two-layer self-review that front-runs CodeRabbit, defined in
[`docs/pr-review-gate.md`](docs/pr-review-gate.md). This is the Terraform module
catalog, so the gate is Terraform-first.

```bash
make scan        # Layer 1 — deterministic scanners on your diff (the OSS analyzers
                 # CodeRabbit runs): terraform fmt-check, tflint, checkov, trivy on
                 # changed *.tf; plus gitleaks, hadolint, actionlint, shellcheck,
                 # yamllint, markdownlint, semgrep when matching files change.
                 # checkov auto-picks up .checkov.yaml (so CKV_TF_1 stays skipped).
```

- **One-time:** `make scan-tools` installs the analyzers (best-effort Homebrew);
  `make hooks` wires an advisory pre-push hook (prints findings; **hard-blocks only
  on a detected secret** — override once with `OCD_SKIP_PRESCAN=1 git push`).
- **Claude Code users:** for a feature-sized change (a new module, or a changed
  public variable/output), also invoke the **`pr-review`** skill
  (`.claude/skills/pr-review/`) — the LLM multi-lens review layered on the scanners.
  Its key lens for a module catalog is **contract/backward-compat**: only it reasons
  about a renamed/removed variable or output being a breaking change.

Fix what the gate flags, then open the PR — the same scan files are portable and can
be copied into the consumer repos where most day-to-day `*.tf` work happens.

---

## Hard rules

- **This repo is the *source*, not an environment.** Don't add a backend, a root
  module, `terraform.tfvars`, or environment values (hostnames/IPs/credentials) to a
  module. Those belong in the consumer repo. `*.tfvars` / `*.tfstate*` /
  `.terraform/` are git-ignored here.
- **Never commit secrets.** Use `random_password` for generated values; mark
  secret-bearing variables `sensitive = true`; reference pre-created
  `kubernetes_secret`s by name. The pre-push hook hard-blocks on a detected secret.
- **A module's variables/outputs are a public contract.** Treat any rename, removal,
  retype, or default change as breaking; bump the `terraform/vX.Y.Z` tag accordingly
  and note it in the PR.
- **Match the established pattern, don't introduce a competing one.** Typed
  `kubernetes_*` for native objects; `kubernetes_manifest` only for CRDs/custom
  resources; Helm via `null_resource`+CLI (not `helm_release`); composition via
  relative `source`. If you must deviate, say why in a code comment (the existing
  Helm-CLI and `ignore_changes` comments are the model).
- **Public repo — keep it generic.** No internal environment/host/org/board names in
  module code, comments, examples, or docs you add. Run `make scan` (gitleaks)
  before pushing.
- **Run `terraform fmt -recursive` and `make scan` after every non-trivial change.**

---

## Reference

| For | See |
|---|---|
| Public layout, mental model, deploy phases, SemVer/versioning, contributing | [`README.md`](README.md) |
| Per-module purpose, provider versions, dependency graph, network/security notes | [`docs/architecture.md`](docs/architecture.md) |
| The pre-PR review gate (scanners + LLM lenses) and how to widen coverage | [`docs/pr-review-gate.md`](docs/pr-review-gate.md) |
| Branching, commit/PR conventions, disclosure | [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md) |
| The control-plane services these `cloud/` modules deploy | `open-cloud-datacenter` @ `controlplane` |
| The operator source the `operators/` modules mirror | `open-cloud-datacenter` @ `operators` |
| A worked, layered consumer of these modules | a consumer / instance repo (separate, per-environment) |
