---
title: "RBAC v2 — Fine-Grained, Scope-Inherited Authorization"
status: DRAFT / for review
supersedes: docs/rbac.md (the v1 owner/member/viewer model)
date: 2026-05-30
---

# RBAC v2 — Fine-Grained, Scope-Inherited Authorization

> **One sentence:** replace the three coarse roles (`owner`/`member`/`viewer`)
> with an **action-based** model — a catalog of built-in roles (including
> per-resource-type roles like *Virtual Machine Contributor*) that can be
> **granted at any scope (tenant → project → resource) and inherit downward**,
> on a foundation that supports **user-defined custom roles** later.

This is the Azure ARM RBAC model, adapted to DC-API's resource catalog and our
`Tenant → Project → Resource` hierarchy. It is deliberately faithful to Azure
because "we do what Azure does, for the same reasons" is the most defensible
answer to anyone who challenges the model.

> **Clean break.** There are no production users yet, so this is a **one-time
> breaking change taken on purpose** — the moment to break the model so future
> improvements don't have to. No back-compat shims, no transition columns; we may
> reset dev data freely.

---

## 1. Goals & non-goals

**Goals**

1. A **catalog of roles**, not three ranks. Cross-cutting roles (Reader,
   Contributor, Owner) *and* per-resource-type roles (VM Contributor, Network
   Contributor, Key Vault Secrets User, …).
2. **Assign a role at any scope** — tenant, project, or an individual resource
   (where applicable) — with **downward inheritance**. A grant at the tenant
   applies to every project and every resource beneath it.
3. **Additive, allow-wins** evaluation: effective permission is the union of
   everything granted at or above the target.
4. **Control-plane vs data-plane** separation (managing a vault ≠ reading its
   secrets).
5. A **custom-role-ready foundation**: built-in and user-defined roles are the
   same shape (a set of actions), stored and evaluated identically. Shipping
   custom roles later is a UI + a DB table, **not** an engine change.

**Non-goals (deferred, but not designed-out)**

- A custom-role **editor UX** (the engine and schema are ready; the picker ships
  in a later phase).
- Azure-style standalone **deny assignments** (we use `notActions` on a role
  instead — see §11).
- **ABAC / conditions** (attribute predicates on assignments). Out of scope.
- Surfacing **resource-scope grants in the UI** for structural resource types
  (subnets, route tables) — the engine supports it; the UX only exposes it where
  it's meaningful (§5.3).

---

## 2. Vocabulary

| Term | Meaning |
|---|---|
| **Principal** | Who is acting: a `user` (Asgardeo `sub`) or a `service_account`. Platform admins are a short-circuit, not a principal kind. |
| **Action** | A namespaced control-plane operation string, e.g. `compute/virtualMachines/write`. |
| **DataAction** | An operation on a resource's *contents*, e.g. reading a secret value or pulling a kubeconfig. Evaluated against a separate list. |
| **Role definition** | A named set of `actions`, `notActions`, `dataActions`, `notDataActions`. Either **built-in** (system-owned, defined in code) or **custom** (tenant-owned, in the DB). Same shape. |
| **Role assignment** | A binding `(principal, roleDefinition, scope)`. The atom of access. |
| **Scope** | A node in the hierarchy: `tenant:<uuid>`, `project:<uuid>`, or `resource:<uuid>`. |
| **Effective permission** | For a given request, the union of all actions from all assignments whose scope is an ancestor-or-self of the target. |

---

## 3. Action taxonomy

### 3.1 Naming

```
<provider>/<resourceType>[/<subType>]/<verb>
```

- Lowercase provider, camelCase types, lowercase verb. Examples:
  `compute/virtualMachines/write`, `network/vnets/read`,
  `keyvault/vaults/secrets/read` (a DataAction).
- This is the same idea as Azure's `Microsoft.Compute/virtualMachines/write`,
  trimmed of the vendor prefix.

### 3.2 Wildcards

A role lists **patterns**; a request presents a **concrete** action. `*` in a
pattern matches any sequence of characters (including `/`).

| Pattern | Matches |
|---|---|
| `*` | everything (this is what makes Owner "everything") |
| `compute/*` | every action in the compute provider |
| `compute/virtualMachines/*` | every verb on VMs |
| `*/read` | the read verb on every type |

An action is **granted** iff some `actions` pattern matches it **and** no
`notActions` pattern matches it. DataActions are matched the same way against the
`dataActions` / `notDataActions` lists. Control and data lists never cross —
`*/read` in `actions` does **not** grant `keyvault/vaults/secrets/read` (a
DataAction).

### 3.3 The registry (current resources)

This table is the **source of truth** a future custom-role picker enumerates.
**Granularity is deliberately coarse** (decision #3): `read | write | delete`
plus a few specials, grown by *adding* strings, never renaming. `(data)` =
DataAction.

| Provider | Resource type | Verbs / special actions |
|---|---|---|
| `compute` | `virtualMachines` | read, write, delete, **start**, **stop**, **restart** |
| `compute` | `clusters` | read, write, delete, **kubeconfig/read (data)** |
| `compute` | `bastions` | read, write, delete |
| `compute` | `images` | read, write, delete |
| `network` | `vnets` | read, write, delete |
| `network` | `subnets` | read, write, delete |
| `network` | `nsgs` | read, write, delete |
| `network` | `peerings` | read, write, delete |
| `network` | `routeTables` | read, write, delete |
| `network` | `dnsZones` | read, write, delete |
| `network` | `privateEndpoints` | read, write, delete |
| `network` | `providerNetworks` | read *(the tenant network catalog)* |
| `keyvault` | `vaults` | read, write, delete |
| `keyvault` | `vaults/secrets` | **read (data)**, **write (data)**, **delete (data)**, `readMetadata` (control read) |
| `database` | `servers` | read, write, delete |
| `database` | `servers/credentials` | **read (data)** |
| `authorization` | `roleAssignments` | read, write, delete |
| `authorization` | `roleDefinitions` | read, write, delete |
| `authorization` | `serviceAccounts` | read, write, delete |
| `resourcemanager` | `tenants` | read, write, delete *(platform-admin surface)* |
| `resourcemanager` | `projects` | read, write, delete |
| `resourcemanager` | `quotas` | read, write |
| `resourcemanager` | `capUsage` | read |

> **Stability rule:** action strings are an API contract. Once shipped, an action
> string is never renamed or repurposed — only added or deprecated. The registry
> is versioned with the handlers (same PR adds the handler, its action, and the
> built-in roles that include it).

---

## 4. Built-in role catalog

Defined **in code** (a Go registry), immutable, assignable at any scope. Keys are
reserved PascalCase identifiers (custom roles get UUIDs, so they can never
collide).

| Key | Grants (plain English) | `actions` | `notActions` | `dataActions` |
|---|---|---|---|---|
| **Owner** | Everything, incl. granting access and reading data | `*` | — | `*` |
| **Contributor** | Full CRUD on all resources; **cannot** grant access; **cannot** read data | `*` | `authorization/roleAssignments/write`, `authorization/roleAssignments/delete`, `authorization/roleDefinitions/write`, `authorization/roleDefinitions/delete` | — |
| **Reader** | Read all metadata; no writes; no data | `*/read` | — | — |
| **User Access Administrator** | Manage members & assignments only (+ read) | `*/read`, `authorization/*` | — | — |
| **Virtual Machine Contributor** | Full control of all VMs (+ read their deps) | `compute/virtualMachines/*`, `compute/images/read`, `network/*/read` | — | — |
| **Cluster Contributor** | Manage RKE2 clusters; pull kubeconfig | `compute/clusters/*`, `compute/images/read`, `network/*/read` | — | `compute/clusters/kubeconfig/read` |
| **Network Contributor** | Manage all networking | `network/*` | — | — |
| **Key Vault Administrator** | Manage vaults **and** their data | `keyvault/*` | — | `keyvault/*` |
| **Key Vault Secrets Officer** | CRUD secret values (not vault lifecycle) | `keyvault/vaults/read` | — | `keyvault/vaults/secrets/*` |
| **Key Vault Secrets User** | Read secret values only | `keyvault/vaults/read` | — | `keyvault/vaults/secrets/read` |
| **Key Vault Reader** | Vault + secret **metadata**, no values | `keyvault/vaults/read`, `keyvault/vaults/secrets/readMetadata` | — | — |
| **Database Contributor** | Manage database servers | `database/*` | — | — |
| **Database Reader** | Read database metadata | `database/servers/read` | — | — |

The catalog is open-ended: a new "Bastion Operator" or "DNS Zone Contributor" is
one more row, no engine change. (`Owner` includes `dataActions: *` per decision
#1 — an Owner reads everything. Kept simple, revisitable.)

### 4.1 The current three roles map onto this

| v1 role | v2 built-in | Net effect of the cut-over |
|---|---|---|
| `viewer` | **Reader** | Loses the ability to read **secret values** (that's a DataAction now, only in the Key Vault data roles) — a deliberate hardening. |
| `member` | **Contributor** | **Gains uniform delete** across all resource types — fixes today's "can delete DBs but not VMs" inconsistency. |
| `owner` | **Owner** | Unchanged. |
| `dc-admin` group | platform-admin | Unchanged — still the IdP-group break-glass short-circuit. |

So v2 **auto-resolves both inconsistencies** flagged in the v1 review, rather
than patching them by hand. That's a sign the model is more coherent, not just
bigger.

---

## 5. Scopes, assignment & inheritance

### 5.1 Scope identity is a UUID, never a slug

A scope is `(scope_type, scope_uuid)`. **Critical:** project slugs are only
unique *within* a tenant (`demo` can exist in two tenants), so the v1 engine's
slug-based matching (`rbac/rbac.go` matches on `scope_id`) would leak a project
grant across tenants the moment project scope goes live. v2 matches on
`scope_uuid` (`tenant_uuid` / `project_uuid` / resource UUID — all already in the
DB since Phase 6a / M2.5). Slugs become display-only.

**Confirmed against the live schema (2026-05-30):** same-named projects across
tenants is *correct and intended*, not a bug. `projects` is
`PRIMARY KEY (tenant_id, id)` — slugs are unique **within** a tenant, so two
tenants each having a `prod` is allowed (exactly like Azure resource groups in
different subscriptions), while a duplicate *within* one tenant is blocked by the
PK (race-safe). Every resource table foreign-keys to the globally-unique
`projects.project_uuid`, and every lookup is tenant-qualified — there is no
"find project by slug" path and no cross-tenant bleed today. The *only* place it
could bite is RBAC scope-matching, which is exactly why scopes key on
`project_uuid`. We deliberately do **not** add a global-unique project-name
constraint — that would wrongly forbid two tenants from both having a `prod`.

### 5.2 Inheritance — the worked example

Effective permission = union over every assignment whose scope is an
**ancestor-or-self** of the target.

- Grant **Virtual Machine Contributor** to Alice at **tenant `acme`** → Alice can
  do anything to **every VM in every project** of `acme`, and read the
  images/networks they depend on — nothing else.
- Add **Network Contributor** at **project `acme/prod`** → in `prod` only, Alice
  now also manages networking. Other projects: unchanged.
- Grant Bob **VM Contributor** at **resource scope** on one VM → Bob manages only
  that VM.

To *confine* someone, you grant **narrow and never broad** — never by adding a
"deny". Access is always the sum of grants you can point to.

### 5.3 Which resource types get resource-scope grants

The engine supports `resource` scope universally. The **UI/CLI** only surfaces
per-resource assignment for types with an individual identity and lifetime worth
delegating (decision #4): **VMs, clusters, key vaults, databases**.
Structural/network types (subnets, route tables, peerings) are managed at project
scope. ("Where applicable.")

### 5.4 Assignment authority & anti-escalation (defensibility core)

- To **create/delete a role assignment at scope S** you need
  `authorization/roleAssignments/write` (or `/delete`) **at S** — held by `Owner`
  and `User Access Administrator`. Because grants inherit, a tenant Owner can
  manage assignments in every project automatically. A project Owner can manage
  only their project. The delegation rule is *free* from inheritance.
- **No privilege escalation:** you may only grant a role whose action set is a
  **subset of your own effective actions at that scope** — unless you hold
  `Owner` (full `*`). This stops a `User Access Administrator` from minting
  themselves Owner. (Stronger than Azure's historical default; very defensible.)
- **Scope can't be widened:** the project-assignments endpoint only ever writes
  `scope_type=project` for *that* project; a project Owner has no endpoint that
  writes a tenant-scoped grant.

### 5.5 Orphan protection

- **Tenant** must always retain ≥1 principal holding `authorization/roleAssignments/write`
  at tenant scope (i.e. an Owner/UAA). This is the v1 "last owner guard",
  generalized. Enforced on assignment delete.
- **Projects can't orphan:** a tenant Owner always inherits write authority into
  every project, so removing a project's last project-Owner is safe — no
  project-level last-owner guard needed.

---

## 6. Evaluation engine

Pure function, no HTTP/SQL (mirrors today's `internal/rbac` package boundary).

```text
Authorize(principal, action, isDataAction, scopeChain) -> ALLOW | DENY:
  if principal.isPlatformAdmin: return ALLOW           # break-glass short-circuit

  assignments = repo.ListRoleAssignmentsForPrincipal(principal)   # 1 DB call
  applicable  = [a for a in assignments
                   if (a.scope_type, a.scope_uuid) in scopeChain]  # ancestor-or-self

  for a in applicable:
      def = resolve(a.role_definition)          # built-in registry first, else DB
      if def.permits(action, isDataAction):     # allow-match AND not notActions, WITHIN this role
          return ALLOW
  return DENY

# def.permits(action) = (some Actions pattern matches) AND (no NotActions pattern
# matches), on the data plane when isDataAction. NotActions are per-role
# SUBTRACTIONS, not global deny rules: a permission one role subtracts can still
# be granted by another role the principal holds (Azure semantics). e.g. a
# Contributor (notActions: authorization/*) who is ALSO User Access Administrator
# CAN manage access — the UAA grant is not cancelled by Contributor's subtraction.
```

**The scope chain** is built per request from context (UUIDs are already injected
by `TenantContext` / `ProjectContext` middleware):

| Request | scopeChain (narrow → broad) |
|---|---|
| Create a VM in a project | `[project:<uuid>, tenant:<uuid>]` |
| Act on an existing VM | `[resource:<vm-uuid>, project:<uuid>, tenant:<uuid>]` |
| Read tenant catalog (images/networks/cap-usage) | `[tenant:<uuid>]` + *subtree-read* (§7) |

**Performance:** one indexed query per request (`idx_role_assignments_principal`),
then in-memory set work over O(handful) assignments — identical cost profile to
v1. If a principal ever holds thousands of assignments we push the scope filter
into SQL; not a concern at current scale.

---

## 7. Enforcement integration

- Replace the single `requireTenantRole(min)` helper with
  `requireAction(w, r, action)` (and a data variant). Each handler **names the
  action it performs** instead of a minimum rank — e.g. VM create asserts
  `compute/virtualMachines/write`; cluster kubeconfig asserts the DataAction
  `compute/clusters/kubeconfig/read`.
- The helper builds the scope chain from context (resource UUID when the URL
  targets one, else project, else tenant).
- **Subtree read:** tenant-level read endpoints (images, provider networks,
  cap-usage) and `GET /v1/tenants` accept a principal who holds **any** role
  anywhere in the tenant subtree — so a project-only member can still list the
  images they need and see the tenant in their switcher. (Fixes the v1
  `tenants.go:93` filter that hides project-only members.)
- An **endpoint→action map** (one table, generated/asserted in a test) keeps the
  OpenAPI spec, handlers, and role catalog coherent. This is where `api-designer`
  and `docs-writer` plug in during implementation.

---

## 8. Data model

### 8.1 Role definitions

Built-ins live **in code** (versioned, immutable). Custom roles live in a new
table — same shape, so the engine resolves both uniformly:

```sql
-- Custom (tenant-owned) role definitions. Built-ins are NOT stored here;
-- they live in the Go registry and are resolved by reserved key first.
CREATE TABLE IF NOT EXISTS role_definitions (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_uuid       UUID NOT NULL,                 -- owning tenant
    name              TEXT NOT NULL,
    description       TEXT,
    actions           JSONB NOT NULL DEFAULT '[]',
    not_actions       JSONB NOT NULL DEFAULT '[]',
    data_actions      JSONB NOT NULL DEFAULT '[]',
    not_data_actions  JSONB NOT NULL DEFAULT '[]',
    assignable_scopes JSONB NOT NULL DEFAULT '[]',   -- e.g. ["tenant:<uuid>","project:<uuid>"]
    created_by        TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_uuid, name)
);
```

**Custom-role guardrails** (enforced at the API when the editor ships):

- `actions`/`dataActions` must be a **subset of the creator's own effective
  actions** at the target scope (no escalation — §5.4).
- A custom role's `assignable_scopes` confine where it can be bound.
- Built-in keys are reserved; a custom role can't shadow `Owner`, `Reader`, etc.

### 8.2 Role assignments

**Clean break — no live users, so no migration burden.** We cut over in one step
rather than carrying a transition `role` column:

```sql
-- Replace the rank column with a role-definition reference.
ALTER TABLE role_assignments ADD COLUMN IF NOT EXISTS role_definition TEXT;
--   built-in -> reserved key   e.g. 'VirtualMachineContributor'
--   custom   -> role_definitions.id (uuid as text)

-- Convert any rows present in a dev DB, then drop the old rank column.
UPDATE role_assignments SET role_definition = CASE role
    WHEN 'owner' THEN 'Owner' WHEN 'member' THEN 'Contributor' WHEN 'viewer' THEN 'Reader'
END WHERE role_definition IS NULL;
ALTER TABLE role_assignments DROP COLUMN IF EXISTS role;

-- scope_type gains 'resource'; scope_uuid then holds the resource's UUID.
-- (scope_type is already free TEXT; scope_uuid already exists — no other DDL.)
```

The `UNIQUE (…, scope_id, role)` constraint becomes `(…, scope_id, role_definition)`.
Wiping dev data entirely is equally fine — there's nothing to preserve. All
statements stay idempotent per the `schema.sql` rule in CLAUDE.md.

---

## 9. API surface

Generalize "members" into "role assignments"; a friendly members view is an
optional convenience over the same data (not a back-compat requirement).

```
# Role catalog (read-only until custom roles ship)
GET    /v1/role-definitions                                   # built-ins + caller's tenant customs
GET    /v1/role-definitions/{key}

# Role assignments — the general primitive
GET    /v1/tenants/{tid}/role-assignments?scope=&principal=
POST   /v1/tenants/{tid}/role-assignments                     # {principal, roleDefinition, scope:{type,id}}
DELETE /v1/tenants/{tid}/role-assignments/{id}
GET    /v1/tenants/{tid}/projects/{pid}/role-assignments      # project-scoped binds
POST   /v1/tenants/{tid}/projects/{pid}/role-assignments
DELETE /v1/tenants/{tid}/projects/{pid}/role-assignments/{id}

# Convenience views (over role-assignments)
GET    /v1/tenants/{tid}/members                              # humans at tenant scope (optional sugar)
GET    /v1/auth/me                                            # caller identity + assignments (§10.1)

# Capability probe (the UI's gate — §10.1)
POST   /v1/permissions:check                                  # [{action, scope}] -> [{allowed}]

# Custom roles (LATER phase; schema ready now)
POST   /v1/tenants/{tid}/role-definitions
PATCH  /v1/tenants/{tid}/role-definitions/{id}
DELETE /v1/tenants/{tid}/role-definitions/{id}
```

**No more "PATCH a member's role".** In an additive model a principal simply
holds a set of assignments — changing access = add one / remove one. The v1
remove-then-re-add dance (and its access-gap window) disappears by construction.

### 9.1 Invite by email

The role-assignment POST endpoints accept **either** `user_sub` **or** `user_email`, but not both:

```json
{
  "user_email": "alice@example.com",
  "role_definition": "Contributor"
}
```

When `user_email` is supplied, dc-api resolves it to an OIDC `sub` via the optional SCIM2 directory provider (all four `DCAPI_IDP_*` environment variables must be configured). If the directory is not configured, or if the email does not match exactly one user, the API returns `422 Unprocessable Entity`.

Fallback when email invite fails: grant by raw `user_sub` instead, which always works.

**Error responses:**
- `422` — directory not configured, or email is ambiguous / not found
- `501` — directory provider is disabled (feature dark)
- `502` — directory provider is configured but the IdP upstream request failed

The directory endpoints themselves (`GET /v1/tenants/{tenant_id}/directory/users` and `.../directory/groups`) are gated to principals holding `authorization/roleAssignments/write` — the inviter roles (Owner, User Access Administrator). See [rbac.md IdP Directory Configuration](rbac.md#idp-directory-configuration-optional) for the full setup guide.

---

## 10. How this wires to the UI (and CLI)

The UI is the part most affected, so the plan matters. **The one rule that keeps
it sane: the front-end must never re-implement the authorization matcher.** The
engine lives in dc-api; the UI asks dc-api what the caller can do.

### 10.1 The capability contract (the UI's only new dependency — design in P1)

Today cloud-ui derives `isOwner` from `GET /v1/tenants[].roles` and gates buttons
on it. With an action-based model "can I create a VM in this project?" is no
longer a single-role check. So dc-api exposes capability hints:

- **`GET /v1/auth/me`** → the caller's identity + their raw role assignments
  (principal, role, scope) + `is_admin`.
- **Per-object hints** — list/detail responses carry a small `_can` block for the
  caller, e.g. a project returns
  `{"_can":["compute/virtualMachines/write","network/vnets/read", …]}`. The UI
  gates the "Create VM" button on the hint, not on a role name.
- **Batch probe** — `POST /v1/permissions:check` with `[{action, scope}]` for
  screens that need many checks at once.

This is exactly how the Azure portal works (it calls a "check access" API rather
than guessing from role names). It is the single new contract P2 depends on, so
we nail it in P1.

### 10.2 Screens

- **Access control (tenant)** — today's `MembersPage` generalizes from "members"
  to "role assignments": a table of *Principal · Role (from the catalog) · Scope ·
  Inherited? · Granted by/at · ⋯*. The "Add" dialog becomes **pick principal →
  pick role (searchable, grouped by tier from `GET /v1/role-definitions`) → pick
  scope**.
- **Access control (project)** — a new page under the project nav, same
  component, scoped to the project. It lists assignments granted *here* **plus**
  the ones **inherited from the tenant** (read-only, badged "Inherited") — the
  Azure "Role assignments" experience.
- **Resource "Access control" tab** *(end of P2)* — on VM / cluster / key-vault /
  database detail pages, the same component scoped to that resource.
- **Custom-role builder** *(P3)* — action checkboxes grouped by the §3.3 registry,
  writing a `role_definitions` row. The registry being a real enumerable list is
  what makes this a straightforward form, not a bespoke build.

### 10.3 Is it an overhaul?

Honest answer: **moderate, not a rewrite.** `MembersPage` → a generalized
`AccessControlPage`, one new project-level page, role/scope pickers, and swapping
the `isOwner` gate for capability hints. The `RoleBadge` / `RolePill` and the
tenant switcher's role display survive with a wider enum. The biggest single
change is *gating-by-capability instead of by-role-name* — which is why the §10.1
contract is the thing to get right early. Everything else is incremental, and can
be tackled when we reach P2.

### 10.4 CLI

`dcctl iam {assign,list,revoke}` with `--role`, `--scope tenant|project|resource`,
`--assignee`; `dcctl role-definition list`; and a real `dcctl whoami` over
`GET /v1/auth/me` that prints the caller's assignments and effective scopes.

---

## 11. Defensibility / design rationale

| Decision | Why it withstands scrutiny |
|---|---|
| **Action-based, not rank-based** | Industry standard (Azure ARM, AWS IAM, GCP IAM). "More roles" is an emergent property of composing actions, not a special case. |
| **Additive, allow-wins** | Access = the sum of explicit grants you can point at. Auditable ("she has it via this assignment"), unlike deny-based ("denied by a rule three scopes up"). |
| **`notActions`, not deny assignments** | Subtractive needs are expressed *inside a role*; we avoid Azure's separately-managed deny assignments, the part of Azure even Azure admins find confusing. |
| **Control vs data split** | "Manage the vault" ≠ "read the secrets". A read-only auditor (Reader) cannot exfiltrate secret values. |
| **UUID-keyed scopes** | Slugs aren't globally unique; UUID keying closes a cross-tenant leak before it can exist. |
| **Subset-only delegation** | You can't grant — or author a role granting — what you don't hold. No self-escalation by an access admin. |
| **Built-ins in code, customs in DB, one engine** | Built-ins are versioned and immutable; customs are tenant data; both evaluate identically, so custom roles never become a forked code path. |
| **Least privilege defaults** | Autoprovision stays **off** in prod; new principals get nothing until explicitly granted. |
| **Platform admin is IdP-managed & audited** | Break-glass lives in Asgardeo group membership (auditable there), surfaced only as `is_admin`. |

---

## 12. Phased rollout

| Phase | Content | Risk |
|---|---|---|
| **P0 — engine, semantics-preserving** | Action registry + built-in role registry + wildcard matcher (pure package, fully unit-tested). Then schema cut-over (`role_definition`, `role` dropped) and rewire `requireTenantRole` to the new engine with the 3 mapped roles. With only Owner/Contributor/Reader wired at tenant scope, the **allow/deny matrix is identical to today** — a verified refactor before any new capability lands. | Low |
| **P1 — scope + per-type roles** | Action-tag all ~18 handlers (`requireAction`); enforce project & resource scope; subtree-read; ship the per-resource-type built-ins; role-assignment API; capability probe (§10.1); `/members` becomes a view. | Medium |
| **P2 — UX** | cloud-ui scope+role pickers, inherited-vs-direct, capability-gated buttons; `dcctl iam` + `whoami`. | Low |
| **P3 — custom roles** | role-definition CRUD API + subset/guardrail validation + UI builder. Schema already in place. | Medium |
| **P4 — docs & tests** | Rewrite `docs/rbac.md`; integration matrix (inherit / confine / escalate-blocked / data-plane). | Low |

P0's pure engine package is the keystone: a self-contained, fully-tested unit
with no HTTP/DB/handler dependencies, so it can be proven correct in isolation
before anything is wired to it.

---

## 13. Resolved decisions

All forks are decided (2026-05-30):

1. **Owner includes data actions** (`dataActions: *`). Simple; revisitable later.
2. **Custom-role-ready foundation** — built-ins in code, customs in DB, one
   engine (§4, §8.1). Users will later compose actions (e.g. `vm/write` without
   `vm/delete`) into their own role.
3. **Action granularity: coarse.** `read/write/delete` plus a few specials —
   `vm/start|stop|restart`, `clusters/kubeconfig` (data), the secret data verbs.
   Grow by adding strings, never renaming (§3.3).
4. **Resource-scope set: VMs, clusters, key vaults, databases.** Engine supports
   resource scope universally; UI/CLI only surface it for these (§5.3).
5. **`whoami` / capability shape:** `GET /v1/auth/me` returns the caller's raw
   assignments; a separate **capability probe** (`POST /v1/permissions:check`)
   plus per-object `_can` hints answer "can I do X at scope Y?" so neither CLI
   nor UI re-implements the matcher (§10.1).
6. **Custom-role authoring: subset-of-creator** (see rationale below).
7. **Clean break.** No live users → no migration burden. Cut `role` →
   `role_definition` in one step; reset dev data if convenient. This is the
   moment to break it so the *next* changes don't have to (§8.2).

### Why subset-of-creator (decision 6), explained

Two ways to gate who may author a custom role:

- **Owner-only** — only a tenant Owner can create custom roles. Dead simple, but
  needlessly restrictive: a project Owner can't even compose a role out of powers
  they already hold and use it inside their own project.
- **Subset-of-creator** *(chosen)* — anyone holding
  `authorization/roleDefinitions/write` may author a custom role, **but its
  action set must be a subset of the author's own effective actions at the target
  scope**. You can never mint a role granting powers you don't have, so there's no
  escalation path; yet a project Owner can delegate a slice of their own
  authority. This is the same principle AWS IAM enforces with permissions
  boundaries, and it's the same invariant v2 already applies to *assigning* roles
  (§5.4): **you can't hand out what you don't hold** — one rule for both authoring
  and assigning.
