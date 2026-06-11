---
title: "M1.5 RBAC — Membership and Roles"
author: WSO2 IaaS Team
date: 2026-05-09
---

# M1.5 RBAC — Membership and Roles

> **Note:** Role vocabulary has moved to RBAC v2. See [rbac-v2.md](rbac-v2.md) for the current action-based model and role catalog. This document covers M1.5 semantics and the autoprovision / member-invite workflow; it uses examples from the v1 era and will be superseded when v2 fully ships.

**Related design doc:** [MILESTONES.md § M1.5](../MILESTONES.md#m15--full-rbac-before-m2-after-m1-e2e-test-passes)

## What This Is

DC-API has its own membership and role system. **Identity still comes from Asgardeo** (via OIDC JWT), but **access control lives inside DC-API**. This lets you:

- Assign different roles to different users within the same tenant (owner vs member vs viewer)
- Issue long-lived tokens to CI/CD systems without giving them Asgardeo credentials
- Audit who granted access to whom, and when
- Extend to finer-grained scopes in M5 (subscriptions, resource groups, individual resources) without a schema rewrite

The mental model: Asgardeo answers *"who are you?"*. DC-API answers *"what can you do?"*.

## The Model in One Diagram

```
    ┌─────────────────────────────────────┐
    │     Asgardeo                        │
    │  (OIDC JWT with groups)             │
    └────────────────┬────────────────────┘
                     │
                     ▼
         ┌───────────────────────┐
         │    DC-API Auth        │
         │  (JWT validation)     │
         └────────────┬──────────┘
                      │
          ┌───────────┴───────────┐
          ▼                       ▼
    ┌──────────────┐      ┌──────────────┐
    │  role_       │      │ is_admin     │
    │  assignments │      │ (dc-admin    │
    │  table       │      │ group check) │
    └────────┬─────┘      └──────┬───────┘
             │                   │
             └───────────┬───────┘
                         ▼
              ┌────────────────────┐
              │  Access decision:  │
              │  allowed / 403     │
              └────────────────────┘
```

When a user logs in via `dcctl login`, Asgardeo issues a JWT. DC-API validates the signature and extracts the user's email and groups. Then:

1. **If the user is in the `dc-admin` group** → they're a platform admin, can do anything
2. **If the user is NOT a member of any tenant in DC-API** → depends on `DCAPI_RBAC_AUTOPROVISION`:
   - `true` (dev default): auto-grant `member` role if the JWT contains a `dc-tenant-<name>` group
   - `false` (prod recommended): reject with 403; they must be invited explicitly
3. **If the user IS a member** → look up their role(s) and enforce per-operation

This design is **forward-compatible with M5**: when the hierarchy lands, the table schema stays the same, just with more scope types.

## Roles

| Role | Can create | Can read | Can update | Can delete | Can invite/remove members | Notes |
|---|---|---|---|---|---|---|
| `owner` | Yes | Yes | Yes | Yes | Yes | Full control within scope; can manage team membership |
| `member` | Yes | Yes | Yes | No | No | Day-to-day developer; can't delete or manage people |
| `viewer` | No | Yes | No | No | No | Read-only; auditors, CI dashboards, approval flows |
| `platform-admin` | Yes | Yes | Yes | Yes | Yes | Via Asgardeo `dc-admin` group; never stored in `role_assignments` |

**Effective role** is the most permissive role a principal holds across the scope chain. In M1.5 the chain is always one element (the tenant). In M5 it grows to resource → resource group → subscription → tenant, and you take the max permission across all four.

## Scopes

Today: `tenant` only. Example:

```sql
INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, role, granted_by)
VALUES ('user', 'user-123@acme.com', 'tenant', 'acme', 'member', 'admin@acme.com');
```

This user can now act on all resources in the `acme` tenant.

**In M5** these will be valid scope types:
- `subscription` — a team's quota boundary
- `resource_group` — logical grouping within a subscription (dev/staging/prod)
- `resource` — individual VM, cluster, volume (fine-grained)

**Your M1.5 membership rows are compatible with M5** — they stay exactly as-is. New rows will simply have different `scope_type` values. No migration, no data loss.

## Adding a Human to a Tenant

### Step 1: Create the Asgardeo group

The user must be in an Asgardeo group named `dc-tenant-<tenant-slug>`. For tenant `acme`:

1. Open Asgardeo console → Groups
2. Create group `dc-tenant-acme`
3. Add the user to the group

See [asgardeo-setup.md](asgardeo-setup.md) for detailed steps.

### Step 2: First login

When the user runs `dcctl login` for the first time:

```bash
dcctl login
# Browser opens, user authenticates in Asgardeo
# JWT contains groups: ["dc-tenant-acme"]
```

**If autoprovision is ON** (`DCAPI_RBAC_AUTOPROVISION=true`, dev default):
- DC-API checks: does this user have a `dc-tenant-acme` group?
- If yes: inserts a `member` role assignment row automatically
- User is now a member of the `acme` tenant

**If autoprovision is OFF** (`DCAPI_RBAC_AUTOPROVISION=false`, prod recommended):
- DC-API checks: does this user exist in the `role_assignments` table?
- If no: returns 403, tells them to ask an owner to invite them
- No auto-insert happens; an existing `owner` must run the next step

### Step 3: Manual invite (optional, prod only)

If autoprovision is off, an existing tenant owner invites the user:

```bash
dcctl tenant member create alice@acme.com --role Contributor --tenant acme
```

This inserts a `role_assignments` row even if the user has never logged in. When they do log in later, they'll have access.

**Email-based invites** require the directory provider to be configured (all four `DCAPI_IDP_*` environment variables must be set; see the "IdP Directory Configuration" section below). If the directory is not configured, or if the email does not resolve to exactly one user, the API returns 422; in that case, invite the user by their OIDC `sub` instead:

```bash
dcctl tenant member create 01abc123-0000-0000-0000-user000000001 --role Contributor --tenant acme
```

## Autoprovision Explained

### Dev mode (autoprovision ON)

**Environment:** `DCAPI_RBAC_AUTOPROVISION=true` (default)

**Behavior:** First login with a valid `dc-tenant-<x>` group → instant `member` role.

**Pros:**
- Low friction for local dev and testing
- Developers can self-onboard into shared dev/test tenants
- Matches M1 behavior (backward compatible)

**Cons:**
- Not suitable for production (no explicit approval)
- Creates membership rows even for typos in group names

**When to use:** Local dev, CI integration tests, pre-prod staging.

### Prod mode (autoprovision OFF)

**Environment:** `DCAPI_RBAC_AUTOPROVISION=false`

**Behavior:** First login without an existing `role_assignments` row → 403. Owner must explicitly invite.

**Pros:**
- Explicit audit trail for every access grant
- Prevents accidental over-provisioning
- Complies with zero-trust onboarding policies

**Cons:**
- Higher overhead (owner must run `dcctl tenant member create` for each hire)
- Requires coordination between teams

**When to use:** Production, regulated industries, multi-tenant platforms.

### Migrating from dev to prod

When you flip `DCAPI_RBAC_AUTOPROVISION=false` for the first time:

1. All existing `role_assignments` rows stay in the database — no data loss
2. New first-time users will be rejected with 403 instead of auto-provisioned
3. Existing users who already have rows are unaffected
4. Owners must now explicitly invite new hires

**No migration script needed** — the row format is unchanged.

## Roles in Practice

### Add a contributor

```bash
dcctl tenant member create bob@acme.com --role Contributor --tenant acme
```

Output:
```
Granted Contributor to bob@acme.com on tenant acme (principal 01abc123-0000-0000-0000-user000000002).
```

What it does:
- Inserts a `role_assignments` row with `scope_type=tenant`, `scope_uuid=<acme-uuid>`, `role_definition=Contributor`
- If a row with the same (principal_id, scope_type, scope_uuid, role_definition) already exists: returns 200 (idempotent, no error)
- Appends an audit event: actor=you, action=ROLE_ASSIGNMENT_CREATED
- Prints the principal ID (the OIDC `sub` if granted by email, or the `user_sub` directly)

A Contributor can create, read, and update resources but cannot delete them or manage access:
- `dcctl create vm ...` ✓
- `dcctl get vm <id>` ✓
- `dcctl list vms` ✓
- `dcctl delete vm <id>` ✗ (403)
- `dcctl tenant member create ...` ✗ (403)

### Add a reader

```bash
dcctl tenant member create audit@acme.com --role Reader --tenant acme
```

A Reader can inspect resources but cannot create, update, or delete:
- `dcctl get vm <id>` ✓
- `dcctl list vms` ✓
- `dcctl create vm ...` ✗ (403)
- `dcctl delete vm <id>` ✗ (403)

### Add an owner

```bash
dcctl tenant member create cto@acme.com --role Owner --tenant acme
```

Owners can invite/remove members, manage all resources, delete resources, etc. Only other owners can invite a new owner (one owner can always do what other owners can do).

### List members

```bash
dcctl tenant member list --tenant acme
```

Output:
```
PRINCIPAL_ID                        ALIAS          ROLE           GRANTED_AT            GRANTED_BY
01abc123-0000-0000-0000-user000000001  alice          Contributor    2026-05-09T10:00:00Z  admin@wso2.com
01abc123-0000-0000-0000-user000000002  bob            Contributor    2026-05-09T10:02:00Z  alice@acme.com
01abc123-0000-0000-0000-user000000003  CTO (external) Owner          2026-05-09T10:05:00Z  admin@wso2.com
```

Service accounts also appear here if they're assigned roles:

```
PRINCIPAL_ID                        ALIAS            ROLE         GRANTED_AT            GRANTED_BY
550e8400-e29b-41d4-a716-446655440000  ci-deployer    Contributor  2026-05-09T11:00:00Z  cto@acme.com
```

## Removing Members

```bash
dcctl tenant member delete 01abc123-0000-0000-0000-user000000001 --tenant acme
```

This deletes **all** `role_assignments` rows for that principal in that tenant scope (they might have held multiple roles; now they're all gone). Rows are deleted, audit event is appended. The argument is the principal's `sub` (the opaque OIDC subject returned when the member was invited).

### The "last owner" guard

You cannot remove the last owner of a tenant. If only one principal holds an Owner role at tenant scope:

```bash
dcctl tenant member delete 01abc123-0000-0000-0000-cto000000001 --tenant acme
# Error: cannot remove the last principal holding authorization/roleAssignments/write at tenant scope
```

**Rationale:** prevents locking out the entire tenant. If you need to transfer ownership, add a new owner first, then remove the old one.

## Service Accounts

### What they are

DC-API-issued long-lived tokens for non-human callers: CI/CD pipelines, webhooks, automation scripts. They authenticate with a token instead of an OIDC JWT.

They are **not** Kubernetes service accounts or Asgardeo service accounts. They live entirely in DC-API and are scoped to a tenant.

### Token format

```
dcapi_sa_<lookup_id>_<secret>
         ↑↑↑↑↑↑↑↑↑↑↑↑↑ ↑↑↑↑↑↑↑↑↑↑↑↑↑↑↑↑
         12 chars       32 chars (random)
         plaintext      secret (bcrypt-hashed in DB)
```

Example:
```
dcapi_sa_a7f3b2c1d9e8_gX9kL2mN5pQ8rT1uV4wX7yZ0aB3cD6eF9
```

The lookup ID is stored plaintext for efficient indexed lookups. Only the secret portion is bcrypt-hashed. This avoids O(N) bcrypt comparisons on every request while keeping the security of cryptographic hashing.

### Token shown once

When you create a service account:

```bash
dcctl tenant create-service-account ci-deployer --role member --tenant acme
```

Output:
```
Created service account: ci-deployer

Token (shown once, save it now):
dcapi_sa_a7f3b2c1d9e8_gX9kL2mN5pQ8rT1uV4wX7yZ0aB3cD6eF9

Expires: never (tokens are long-lived; revoke via delete)
```

**This is the only time the raw token is printed.** It is NOT stored in the database — only the bcrypt hash is saved. If you lose it, delete the SA and create a new one.

### Using the token

In a GitHub Actions workflow:

```yaml
- name: Deploy to DC
  env:
    DCAPI_TOKEN: ${{ secrets.DCAPI_TOKEN }}
  run: |
    dcctl create vm --name web-01 --size medium --image default/img \
      --network default/net --token "$DCAPI_TOKEN"
```

Or as an HTTP header:

```bash
curl -H "Authorization: Bearer dcapi_sa_a7f3b2c1d9e8_gX9kL2mN5pQ8rT1uV4wX7yZ0aB3cD6eF9" \
  https://dcapi.lk.internal.wso2.com/v1/virtual-machines
```

### Token rotation

Service account tokens are long-lived by design (no expiry). To rotate:

1. Create a new SA
2. Update your automation to use the new token
3. Delete the old SA

```bash
dcctl tenant create-service-account ci-deployer-v2 --role member --tenant acme
# Update your CI secrets...
dcctl tenant delete-service-account ci-deployer --tenant acme
```

### List service accounts

```bash
dcctl tenant list-service-accounts --tenant acme
```

Output:
```
NAME               ID                                    ROLE     CREATED_AT            LAST_USED
ci-deployer        550e8400-e29b-41d4-a716-446655440000 member   2026-05-08T15:00:00Z  2026-05-09T10:32:14Z
slack-webhook      550e8400-e29b-41d4-a716-446655440001 viewer   2026-05-01T08:00:00Z  2026-05-09T09:45:00Z
```

The `last_used` timestamp is updated fire-and-forget on the auth path (non-blocking). It's operational information only, not security-critical.

### Delete service account

```bash
dcctl tenant delete-service-account ci-deployer --tenant acme
```

The token is immediately revoked. Any request with the deleted SA's token will get a 401.

## Platform Admin

Members of the Asgardeo group `dc-admin` are **platform admins**. They bypass all role assignment checks:

```bash
# In Asgardeo, add admin@wso2.com to the dc-admin group.
# Then, optionally (for audit purposes), grant them an explicit Owner role:
dcctl tenant member create admin@wso2.com --role Owner --tenant acme
# But if admin@wso2.com is in dc-admin group, the role row is optional.
# They have access to all tenants regardless.
```

**Key point:** Admin access is determined by **group membership in Asgardeo, not by DC-API roles**. DC-API checks the JWT's `groups` claim for the configured admin group (default: `dc-admin`).

**Do not make this implicit.** The `dc-admin` group must be managed in Asgardeo, and membership is auditable there. The only place DC-API surfaces this is in context (the `is_admin` flag).

**Admins can still appear in `role_assignments`** — there's no harm in it. But the rows are not consulted if `dc-admin` is present in the JWT.

## Forward Compatibility (M5 Preview)

Your M1.5 membership rows will work unchanged in M5. Here's why:

**Today's schema:**
```sql
INSERT INTO role_assignments
  (principal_type, principal_id, scope_type, scope_id, role, granted_by)
VALUES
  ('user', 'alice@acme.com', 'tenant', 'acme', 'member', 'admin@wso2.com');
```

**In M5, when subscriptions exist:**
```sql
INSERT INTO role_assignments
  (principal_type, principal_id, scope_type, scope_id, role, granted_by)
VALUES
  ('user', 'alice@acme.com', 'subscription', 'sub-123', 'member', 'admin@wso2.com');
```

Both rows are valid in the same database. The middleware walks the scope chain (narrow to broad) and picks the most permissive role. A user with a `member` role on a subscription and a `viewer` role on the parent tenant gets `member` access.

**No migration needed.** The schema design anticipated this from the start. New rows just have new scope types.

## Gotchas

### Removed from Asgardeo group

If an employee leaves and you remove them from the `dc-tenant-acme` group in Asgardeo, their JWT will no longer contain that group. On their next login, DC-API sees a JWT without the group — but **the `role_assignments` row still exists** (deliberately, for audit history).

**What happens:**
- If the JWT doesn't contain the `dc-tenant-acme` group, the user's effective role is empty
- They get 403 on any `dcctl` command
- An owner can explicitly remove them with `dcctl tenant member delete <sub>`

This is by design: the removal audit trail is permanent.

### Token rotation with same secret

Don't try to reuse a deleted SA's name and expect the old token to work. Once deleted, the old token is gone. The bcrypt hash is deleted from the database. Even if you create a new SA with the same name, it gets a different secret and a different lookup_id.

### Removing a user from Asgardeo doesn't auto-remove role assignments

Once you remove someone from the IdP, revoke their DC-API membership explicitly:

```bash
dcctl tenant member delete 01abc123-0000-0000-0000-user000000001 --tenant acme
```

If you don't, the row sits in the audit trail forever (which is fine — it documents they once had access). It just doesn't grant access anymore because the JWT won't authenticate.

### Existing `tenant_id` column on resources is unchanged

All your VMs, clusters, and other resources still have a `tenant_id` column. RBAC is a **layer on top**, not a replacement:

- Every resource checks `tenant_id` first (one VM can't cross tenants)
- Then it checks RBAC roles on that tenant (can this user act on resources in this tenant?)

Existing tenant isolation guarantees remain. RBAC is purely about controlling *who* can do *what* within their tenant.

## Audit Trail

Every role-assignment mutation is logged to `audit_events`:

```sql
SELECT action, actor_id, created_at FROM audit_events
  WHERE action IN ('MEMBER_ADDED', 'MEMBER_REMOVED')
  ORDER BY created_at DESC;
```

Output:
```
action         actor_id              created_at
MEMBER_REMOVED alice@acme.com        2026-05-09T11:02:00Z
MEMBER_ADDED   cto@acme.com          2026-05-09T11:00:00Z
```

Service account creation, deletion, and token usage are also audited. The `last_used` timestamp on service accounts is a convenience; the authoritative audit trail is `audit_events`.

## Environment Variables (DC-API)

### RBAC Core

| Variable | Default | Notes |
|---|---|---|
| `DCAPI_RBAC_AUTOPROVISION` | `true` | `true` = auto-grant member on first login; `false` = require explicit invite |
| `DCAPI_TENANT_GROUP_PREFIX` | `dc-tenant-` | IdP group prefix that identifies tenants (e.g. `dc-tenant-acme` maps to tenant `acme`) |
| `DCAPI_ADMIN_GROUP` | `dc-admin` | IdP group name for platform admins |

Change these if your IdP uses different naming conventions.

### IdP Directory Configuration (Optional)

When all four of these variables are set, dc-api enables live directory browsing for invite pickers and email-based role assignments:

| Variable | Required | Notes |
|---|---|---|
| `DCAPI_IDP_SCIM_BASE_URL` | If any are set | SCIM2 endpoint URL of your IdP (e.g. `https://api.asgardeo.io/t/<org>/scim2`). The four `*_BASE_URL`/`*_TOKEN_URL`/`*_CLIENT_ID`/`*_CLIENT_SECRET` variables must be set together or all unset; partial sets cause startup failure. |
| `DCAPI_IDP_TOKEN_URL` | If any are set | OAuth2 token endpoint for the machine-to-machine credential (e.g. `https://api.asgardeo.io/t/<org>/oauth2/token`). |
| `DCAPI_IDP_CLIENT_ID` | If any are set | Client ID of a machine-to-machine OAuth2 app with user/group VIEW scopes only (never write). |
| `DCAPI_IDP_CLIENT_SECRET` | If any are set | Client secret for the above app. |
| `DCAPI_IDP_SCOPES` | For Asgardeo / WSO2 IS | OAuth2 scopes requested with the client_credentials grant (space- or comma-separated). **Required for Asgardeo and WSO2 Identity Server**, which only attach SCIM permissions to the token when scopes are explicitly requested — without them every SCIM call returns 403. Other IdPs may need none. Optional and excluded from the all-or-nothing startup check. Grant LIST/VIEW scopes ONLY. Asgardeo value: `internal_user_mgt_list internal_user_mgt_view internal_group_mgt_view`. |
| `DCAPI_IDP_USERSTORE_DOMAIN` | No (default `DEFAULT`) | Restricts directory reads to one userstore via the WSO2 SCIM2 `domain` query parameter. Without it the IdP returns every account in the organization — including console administrators and collaborators, who are not invite candidates. `DEFAULT` is Asgardeo's consumer-user store; on-prem WSO2 Identity Server typically wants `PRIMARY`. Set explicitly empty to list all stores; non-WSO2 SCIM servers ignore the parameter. |

**Granting the SCIM scopes (Asgardeo console):** open your M2M app → **API Authorization** → authorize the **SCIM2 Users API** (`internal_user_mgt_list`, `internal_user_mgt_view`) and the **SCIM2 Groups API** (`internal_group_mgt_view`), then set those three on `DCAPI_IDP_SCOPES`. Do not authorize any create/update/delete SCIM scopes — dc-api never writes to the IdP.

**What this enables:**
- `GET /v1/tenants/{tenant_id}/directory/users` — searchable user listing (for invite-by-email type-ahead in cloud-ui and dcctl)
- `GET /v1/tenants/{tenant_id}/directory/groups` — group listing
- Email-based invites: `dcctl tenant member create alice@example.com --role Contributor` — the email is resolved to an OIDC `sub` at invite time via SCIM2

**When disabled** (all four unset): the feature is dark. Directory endpoints return `501 Not Implemented`, and role assignment requests must use `user_sub` instead of `user_email`.

**Guardrails:** dc-api proxies SCIM2 reads live and stores nothing. Only the OIDC `sub` (and an inviter-provided `display_alias`) are persisted. See [decisions.md decision #8](decisions.md).

## See Also

- [MILESTONES.md § M1.5](../MILESTONES.md#m15--full-rbac-before-m2-after-m1-e2e-test-passes) — design rationale and scope-polymorphic schema
- [asgardeo-setup.md](asgardeo-setup.md) — step-by-step Asgardeo group creation
- [dc-api-architecture.md](dc-api-architecture.md) — how auth fits into the bigger picture
