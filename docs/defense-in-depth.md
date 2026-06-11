# Defense-in-depth for WSO2 Infrastructure Platform Tenancy

**Status**: design / proposal. None of the phases below are deployed yet
(except where marked "today").
**Last updated**: 2026-05-20

This document captures the tenancy isolation + quota threat model for the
WSO2 Infrastructure Platform Control Plane and lays out a phased hardening plan. Every
phase below is **additive and non-breaking** — you can ship them
independently and stop whenever the residual risk is acceptable. The
ordering is by leverage (cheap + high value first), not by dependency.

---

## 1. Today's setup (Phase 0)

```
            ┌───────────────────┐
   user ──▶ │   dc-api          │ ── single Rancher admin token
            │   (cluster-admin) │ ── single Harvester kubeconfig
            └─────┬─────────────┘    (both have cluster-admin scope)
                  │
                  ├─▶ Rancher  (RKE2 cluster create/delete/kubeconfig)
                  └─▶ Harvester (KubeVirt VMs, KubeOVN VPCs, PVCs, NADs, …)
                       │
                       ▼
            ┌─────────────────────────────────────────────────────┐
            │   dc-api-controlled namespaces on harvester-dev:    │
            │     dc-tenant-<id>  ← one per tenant (today's plan) │
            │     dc-api           ← infra: dcapi VMs, NAT pods    │
            └─────────────────────────────────────────────────────┘
```

What's already protecting tenants:

- **dc-api enforces RBAC** via `role_assignments`:
  `requireTenantRole(owner|member|viewer)` on every write path.
- **TenantContext middleware** sets `tenant_id` from the URL path and
  refuses any request whose JWT/SA doesn't have a role row for that
  tenant.
- **dc-api `quotas` table** caps each tenant's VM/cluster/cpu/memory
  before talking to Harvester.
- **KubeOVN per-tenant VPC** = real L2/L3 isolation between tenant
  networks. Cross-VPC traffic requires explicit peering.
- **KubeVirt VMs are real VMs** — kernel is per-VM, so no shared-kernel
  side channels (Spectre-class) between tenants colocated on the same
  Harvester node.
- **VM root disks are on Longhorn** (1-replica strict-local for the
  controlplane, 2-replica for tenant disks — see
  `docs/dev/handoff-3node-ha-dc-controlplane.md`).

What this does NOT defend against:

- A **bug in dc-api** that uses the wrong `tenant_id`, skips a quota
  check, or creates a cluster-scoped resource from a tenant action.
  Every Harvester action is performed as cluster-admin, so kube-apiserver
  trusts dc-api completely.
- A **compromised dc-api process** (memory dump, container escape on
  the dcapi cluster) gives the attacker the master Harvester kubeconfig
  → full cluster compromise.
- A tenant **issuing API calls fast enough** to monopolize dc-api's
  upstream apiserver connection (no per-tenant rate limit yet).
- A KubeOVN **routing misconfiguration** would silently bypass VPC
  isolation — there's no Kubernetes NetworkPolicy belt for KubeOVN's
  braces.
- A tenant who gets shell on one of their own VMs and **pivots through
  the VPC NAT** to attack `192.168.10.0/24` infra or `dc-postgres`.
- **Audit obscurity**: every Kubernetes audit log entry shows
  `system:serviceaccount:cattle-system:rancher` or the kubeconfig user
  — you cannot tell which tenant requested the action by reading audit
  logs alone.

---

## 2. Threat model

| # | Threat | Impact | Today's mitigation | Today's gap |
|---|---|---|---|---|
| T1 | dc-api bug uses wrong tenant_id | Cross-tenant resource creation/deletion | Code review, tests | Bug = real impact |
| T2 | dc-api skips quota check | One tenant exhausts cluster CPU/RAM/storage | dc-api `quotas` table | Single check, no belt |
| T3 | Compromised dc-api process | Full cluster compromise | None | Master creds in memory |
| T4 | Tenant pivots via VPC NAT to infra | Lateral access to management network | None | NAT egress unfirewalled |
| T5 | API request flood from one tenant | dc-api or apiserver DOS | None | No per-tenant rate limit |
| T6 | Audit log doesn't show tenant | Can't investigate incidents | None | All actions = "dc-api" |
| T7 | KubeOVN VPC misconfigured | Cross-tenant pod-to-pod traffic | KubeOVN design | No second layer |
| T8 | Stale JWT after role revoke | Revoked user keeps access until JWT expiry (~1h) | Short TTL | No revocation list |
| T9 | Storage cost-DOS (snapshots, PVC growth) | Longhorn / disk exhaustion | None | No per-tenant storage cap |
| T10 | Tenant creates cluster-scoped resource | Privilege escalation | dc-api code path doesn't expose | No admission belt |
| T11 | Privileged container / hostPath in VMI pod | Container-escape style break-out | KubeVirt VMI default | No PodSecurityStandards |
| T12 | DNS exfiltration via per-VPC CoreDNS | Data leak through DNS upstream | None | CoreDNS upstream is open |

The single most leveraged threat is **T1 + T3 + T6 together**: dc-api is
the trust singularity. Every other gap is meaningfully worse when dc-api
is the only enforcement layer AND the credential holder AND the audit
identity.

---

## 3. Phased hardening

Each phase ships independently. Pick what fits your appetite — the
sequence below is sorted by leverage, not dependency.

### Phase 1 — Cheap structural wins (~1 day total)

Three small, fully additive changes that each tighten one threat.

#### 1a. Kubernetes `ResourceQuota` per tenant namespace ⭐
Closes: **T2**

For each `dc-tenant-<id>` namespace on harvester-dev, write a
`ResourceQuota` mirroring the dc-api `quotas` table:
```yaml
apiVersion: v1
kind: ResourceQuota
metadata:
  name: dc-tenant-quota
  namespace: dc-tenant-foo
spec:
  hard:
    count/virtualmachines.kubevirt.io: "20"
    count/persistentvolumeclaims: "40"
    requests.cpu: "32"
    requests.memory: 128Gi
    requests.storage: 1Ti
    services.loadbalancers: "5"
```
**Even if dc-api forgets to check, kube-apiserver rejects creation when
over quota.** The dc-api reconciler keeps the K8s ResourceQuota in sync
with the dc-api `quotas` row.

Not breaking — adding ResourceQuota to namespaces only fails creates that
would have been over-limit anyway.

#### 1b. Per-tenant API rate limit in dc-api (~half day)
Closes: **T5**

Token-bucket per `tenant_id` in the dc-api middleware chain. Conservative
default: 30 req/min sustained, 100 req burst per tenant. Buckets keyed by
the resolved `tenant_id`, not the IP (tenants share IPs through dcctl
and cloud-ui).

Returns `429 Too Many Requests` with `Retry-After`. Doesn't affect any
existing client at normal usage.

#### 1c. Default-deny NetworkPolicy per tenant namespace (~1 hour)
Closes: **T7** (partly)

Each `dc-tenant-<id>` namespace gets a default-deny ingress + egress
`NetworkPolicy`, plus explicit allows for: cluster DNS, the per-VPC
CoreDNS pod, ingress-nginx for return-path. Belt over KubeOVN's braces —
if KubeOVN VPC routing is ever misconfigured, NetworkPolicy still
rejects cross-namespace pod-to-pod traffic at the kube-apiserver-enforced
layer.

Non-breaking if the allow list matches today's flows.

---

### Phase 2 — Admission policy (Kyverno or OPA Gatekeeper, ~1 week)
Closes: **T1, T10, T11** (and reduces blast radius of T3)

Install a cluster-wide admission engine on harvester-dev with policies
that **enforce dc-api's invariants at apiserver time, independent of
dc-api code**. Examples (rules a dc-api bug shouldn't be able to violate):

- Every `VirtualMachine`, `PersistentVolumeClaim`, `NetworkAttachmentDefinition`,
  `Service` created in a `dc-tenant-*` namespace MUST carry
  `dc-api.io/tenant=<id>` label, and the label must equal the namespace
  suffix (e.g. label=foo for namespace dc-tenant-foo).
- No tenant resource may set `spec.priorityClassName: system-*`.
- Tenant PVCs may only reference an approved StorageClass set.
- VMI pods may not set `hostNetwork: true`, `hostPID`, `hostIPC`, or
  privileged securityContext.
- VirtualMachineImages may not source from arbitrary URLs (whitelist
  Ubuntu cloud-images mirror + internal registry).
- Custom: cloud-init userData must not contain regex patterns matching
  known infra secrets (e.g. `lk1@03az`, `${DCAPI_*}`).

Phased rollout:
1. Install Kyverno in audit-only mode → see what would have been blocked
2. Tune rules until audit log is clean
3. Flip to enforce

Kyverno is the friendlier of the two (YAML, no Rego). ~10 rules to start.
Adding rules later is non-breaking; only the policy that says "must have
tenant label" risks rejecting an old object — handle by backfilling
labels first.

---

### Phase 3 — Per-tenant Service Account impersonation (~2 weeks) ⭐⭐
Closes: **T1, T3, T6** — the structural fix

The biggest single architectural improvement. dc-api stops using the
master Harvester kubeconfig for tenant-scoped writes. Instead, for any
operation on tenant X, dc-api **impersonates** a tenant-scoped
ServiceAccount via Kubernetes user-impersonation
(`Impersonate-User`/`Impersonate-Group` headers, or via the `--as` flag
to kubectl, supported by both client-go and Rancher's auth proxy).

Per-tenant ServiceAccount:
```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: tenant-provisioner
  namespace: dc-tenant-foo
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: tenant-provisioner
  namespace: dc-tenant-foo
roleRef:
  kind: ClusterRole
  name: dc-tenant-provisioner   # least-privilege role; namespaced verbs only
subjects:
  - kind: ServiceAccount
    name: tenant-provisioner
    namespace: dc-tenant-foo
```

dc-api still has cluster-admin (to set up the SA + impersonate), but
every actual CREATE/UPDATE/DELETE is performed as the tenant's SA.

What this fixes:
- **T1**: a dc-api bug that targets the wrong namespace gets a clean
  403 from kube-apiserver — the impersonated SA doesn't have rights
  outside its namespace.
- **T3**: a compromised dc-api can still impersonate any tenant SA, BUT
  cross-tenant escalation requires multiple impersonations, each
  audit-logged. Single-tenant blast radius is what tenants already have.
- **T6**: audit logs now show `user=system:serviceaccount:dc-tenant-foo:tenant-provisioner`
  — incident response can pinpoint which tenant.

Tradeoffs:
- Real refactor: every Harvester driver call site wraps the dynamic
  client with an impersonation config built from the tenant_id in
  context. ~2 weeks because there are ~30 call sites.
- Cluster-scoped operations (rare; mostly Rancher cluster create/delete,
  IPPool patches, KubeOVN VPC create) STAY on the master cred — those
  are tracked as a small "trusted operations" allowlist and audit-logged
  separately.

This is the change that makes every other defense layer meaningful. With
this in place, even if Phase 2 admission policies have a hole, the SA
itself constrains the action.

---

### Phase 4 — Operational visibility (~3 days, parallel-able)
Closes: **T6, T4** — turns on the lights

#### 4a. kube-apiserver audit policy on harvester-dev
Enable an audit policy that captures every dc-api originated write at
`RequestResponse` level, every read at `Metadata` level, and ship the
log to a central store (Loki / Elastic / S3). Redact `userData`,
secret payloads, and PEM blocks from the audit log via a redaction
transform — without this, the operator password we inject into
cloud-init shows up in plaintext on every VM create.

#### 4b. Per-tenant alerts on cluster-scoped or cross-namespace actions
A SIEM rule: any action against a `dc-tenant-X` namespace performed by a
principal whose SA is `dc-tenant-Y:tenant-provisioner` (i.e. cross-tenant
operation) pages on-call. Catches the worst case of T1 happening
in production.

#### 4c. KubeOVN VPC egress firewall via VpcNatGateway iptables rules
Closes: **T4**

Each tenant VPC's NAT gateway gets a default-deny egress chain to:
- `192.168.10.0/24` (mgmt network: Harvester, Rancher, dc-api, dc-postgres)
- Other tenant external IPs
- Any non-internet destination

Tenant VMs can still reach the public internet (the whole point of NAT),
but they can't pivot to attack infra. Implemented as iptables rules on
the `VpcNatGateway` pod's egress chain; KubeOVN exposes
`IptablesEgressIP`/`IptablesSnatRule` CRDs that dc-api can stamp out.

---

### Phase 5 — Storage + secrets hygiene (when scaled, ~ongoing)

| Concern | Mitigation |
|---|---|
| Storage cost-DOS (T9) | Per-tenant Longhorn snapshot count cap; Harvester storage quota in tenant namespace |
| PSS / VMI hardening (T11) | PodSecurityStandards Restricted profile on tenant namespaces (verify KubeVirt VMI compat first) |
| DNS exfiltration (T12) | Restrict per-VPC CoreDNS upstream to a known-good resolver; deny tenant DNS queries to internal-only zones (e.g. `*.internal.wso2.com`) |
| Stale JWT after revoke (T8) | Shorten access-token TTL to 15min, or maintain a per-sub revocation set in dc-api consulted by the auth middleware (small Redis or in-process set) |
| Master credential rotation | Quarterly rotation of the Rancher admin token + Harvester kubeconfig; today there's no automation. |

These are operational disciplines, not single deliverables — they layer
in over time.

---

## 4. Pragmatic recommendation

If you do nothing else, do **Phase 1a (ResourceQuota)** and
**Phase 3 (impersonation)**. The first is a half-day change that closes
the most realistic accidental-DOS path. The second is the structural
move that makes every other defense matter.

If you can also fit it in: **Phase 2 (Kyverno)** lands in a week and
gives you policy-as-code enforcement that is independent of dc-api
correctness.

Phases 4 and 5 are slower-burn operational items — important, but the
control plane works without them. Run them concurrently with the bigger
shifts.

## 5. Open questions (defer until needed)

- Do we ever want **tenant-scoped Rancher tokens**? Right now Rancher
  cluster-create is a cluster-scoped operation; per-tenant impersonation
  for Rancher would require Rancher's RBAC layer to grow per-tenant
  projects. Probably not until M2 multi-region exposes the need.
- **Resource group / subscription scopes** (M5 RBAC plan) — currently
  every role is tenant-scoped. When sub-tenant scopes ship, the
  per-tenant SA model needs to extend OR the admission policies become
  the enforcement boundary for sub-tenant separation.
- **Per-VPC bandwidth caps** — KubeOVN exposes
  `QoSPolicy` and Subnet ratelimit fields. Useful when tenants start
  sharing the same uplink heavily. Defer until observed contention.

---

## 6. Tenant lifecycle

Today dc-api can **register** a tenant (`POST /v1/admin/tenants`) but
cannot **delete** one. The asymmetry causes two real problems, addressed
in two sub-sections below.

### 6a. UUID-keyed identity (shipped 2026-05-20)

**Problem.** Before Phase 6a, every per-tenant row referenced its tenant
by slug (`tenant_id TEXT`). If a tenant `cs-team` was destroyed by hand
(we wiped `tenants` + `role_assignments` but not the resource tables)
and the slug was re-registered via `POST /v1/admin/tenants`, the new
tenant immediately "owned" every orphan vnet/subnet/VM/KV/etc. row
keyed to `tenant_id='cs-team'`. Not a cross-tenant bug (queries were
correctly filtered) but a "ghost data" hazard the user observed in
the wild on 2026-05-20.

**Fix.** Every tenant gets an immutable `tenant_uuid UUID` (default
`gen_random_uuid()`, UNIQUE). Every per-tenant table — `resources`,
`vnets`, `subnets`, `route_tables`, `route_table_associations`,
`network_security_groups`, `nsg_attachments`, `peerings`,
`private_dns_zones`, `dns_records`, `service_accounts`, `key_vaults`,
`private_endpoints`, `quotas` — carries a `tenant_uuid` column
populated on every INSERT. `role_assignments` carries `scope_uuid`
for the same reason (M5 will reuse it for subscription/resource-group
scopes). Filters switch from `WHERE tenant_id = $slug` to
`WHERE tenant_uuid = $uuid`. The slug stays for display and URL
ergonomics — it is now a renameable handle, not an ownership reference.

The plumbing:

- **`TenantContext` middleware** resolves the URL slug to its
  `tenant_uuid` once per request and stashes both in context. A slug
  with no `tenants` row gets a 404 — uniformly, including for
  platform admins. This forces re-registration of a recycled slug
  through `POST /v1/admin/tenants` before any tenant-scoped request
  can succeed (and that POST necessarily mints a fresh `tenant_uuid`,
  so orphans are invisible to the new tenant).
- **Repo methods** take `tenantUUID uuid.UUID` and filter on
  `tenant_uuid`. `GetX(ctx, id, tenantUUID)` enforces tenant
  isolation at the DB layer, eliminating the old
  `if x.TenantID != tenantID` post-fetch check in handlers (which
  was the exact spot the bug hid). Methods called from the reconciler
  with no tenant context are split off as `GetXInternal`.
- **Backfill** runs on every dc-api boot
  (`internal/db/migrate.go::backfillTenantUUIDs`): UPSERTs any orphan
  slugs found in per-tenant tables into `tenants`, then UPDATEs every
  per-tenant table's `tenant_uuid` via JOIN to `tenants`. Idempotent
  (`tenant_uuid IS NULL` gate) — a re-boot on a fully-populated DB
  affects zero rows.
- **Integration test:**
  `test/integration/phase_6a_slug_recycle_test.go` seeds Tenant A
  (slug + UUID), creates a VNet, deletes the tenant, re-registers
  the same slug with a fresh UUID (Tenant B), and asserts Tenant B's
  `GET /v1/.../vnets` and `GET /v1/.../vnets/{A-vnet-id}` cannot see
  the orphan. This is the load-bearing test for 6a.

**Operational note.** This is a process-only fix for the
slug-recycle hazard; orphan rows still sit in the DB and orphan
KubeOVN/Rancher state still sits on the cluster until 6b ships.
The win is that the next "register cs-team again" no longer hands
ghost resources to the new tenant — it just looks empty, as it
should.

### 6b. Cascade-delete (pending)

The remaining gap: **resources outlive their tenant on harvester-dev.**
The reconciler only processes `PENDING`/`DELETING` — it never
garbage-collects a row that's `ACTIVE` in dc-api but missing in the
provider, nor the inverse. So SQL-deleting tenant rows leaves orphan
KubeOVN VPCs, NAT gateways, CoreDNS pods, OpenBao secrets, Rancher
clusters, ARC runners, etc. piling up on the live cluster indefinitely.

After 6a these orphans are invisible to any re-registered tenant, but
they still consume storage, IPs, and provider quota — so a proper
cascade-delete pipeline is still required.

A proper `DELETE /v1/admin/tenants/{id}` endpoint must walk the full
dependency tree:

```
tenant
 ├─ resources (VMs, bastions, clusters)
 │   └─ cluster → Rancher RKE2 cluster + cattle-cluster-agent in cluster
 ├─ vnets
 │   ├─ subnets
 │   │   └─ NetworkAttachmentDefinition (Multus)
 │   ├─ route_tables
 │   ├─ network_security_groups
 │   ├─ peerings        ← cross-tenant! cleanup of A's peering also
 │   │                     mutates B's view; needs the peer-owner's
 │   │                     consent OR admin override
 │   ├─ private_dns_zones
 │   ├─ private_endpoints
 │   └─ on harvester-dev: KubeOVN VPC + NAT gateway pod + per-VPC
 │                        CoreDNS Deployment + namespace
 ├─ key_vaults          ← backing OpenBao mount / Vault secrets
 ├─ role_assignments    ← all tenant-scoped membership
 ├─ service_accounts    ← revoke their tokens before deletion
 └─ tenants registry row
```

### Design rules a cascade implementation has to honour

- **Order matters.** VMs before subnets; subnets before VNets; per-VPC
  pods (NAT gateway, CoreDNS) before the subnet they're attached to.
  The same "wait for pods to drain before deleting the subnet"
  pattern in `internal/api/handlers/subnet.go` (F26) generalises here
  — every dependency layer needs a deterministic poll for "is the
  thing actually gone on the provider side?" before moving on.
- **Cross-tenant peering is the trap.** Deleting tenant A while A has
  an outstanding peering with B mutates B's network topology. Two
  reasonable policies: (1) refuse delete and ask the admin to break
  peerings first; (2) treat admin tenant-delete as authoritative and
  break peerings unilaterally with audit log entries on B's side.
  Pick one explicitly; don't let it be a runtime accident.
- **Failure modes need idempotent retry.** A provider DELETE can
  return 404 (already gone — treat as success), 5xx (retryable), or
  500-because-stuck (e.g. a VPC with an undrained NAT pod). The
  cascade handler must be re-runnable from any partial state.
- **Operator escape hatch.** Add a `?force=true` query param that
  skips dependency cleanup and only nukes dc-api state. Logs as
  `cascade-skipped`. For tenants where one resource is permanently
  un-deletable (e.g. a Longhorn volume in a stuck DELETING state),
  an operator needs a path forward that doesn't require
  surgery on every dependent row.
- **Audit each step.** Every cascade step appends an audit_events
  row. A multi-failure cascade should still leave a clear chain of
  what succeeded and what's pending.
- **Don't fire-and-forget.** Cascade can take minutes (cluster
  deletes especially). Return 202 with a polling URL, OR mark the
  tenant `DELETING` and let the reconciler walk the dependency tree
  asynchronously like every other resource. The reconciler path is
  the better fit because tenant deletion is "a state machine over
  many providers" — the same shape the reconciler was built for.
- **Slug recycling protection.** Once a tenant is deleted, the slug
  should be tombstoned (a `deleted_tenants` table or a `deleted_at`
  column with a UNIQUE index on `(id) WHERE deleted_at IS NULL`).
  Re-registering the same id requires either a long delay (e.g. 30
  days) OR an explicit `--purge-tombstone` admin gesture. This is
  what stops the "ghost data" replay scenario in problem 1.

### Estimated effort

- **Walker + DELETE endpoint + first-cut dependency tree**: ~2 days
- **Cross-resource integration tests** (one per dependency type + a
  full-cascade happy path + an interrupted-mid-cascade resume): ~1 day
- **Operator UX**: cloud-ui "Delete tenant" confirm dialog with the
  full dependency preview (count of VMs, vnets, KVs etc. about to
  go); `dcctl delete tenant <id>` command. ~half day.
- **Tombstone + slug recycling rules**: ~half day including migration.

Realistic landing window: 3–4 days for the backend cascade + tests;
another 1–2 days for the operator UX. Plan for it as a **single
focused chunk**, not interleaved with other work — the dependency
ordering is fiddly and benefits from sustained attention.

### Sequencing relative to other phases

Cascade-delete is logically **Phase 6** in this doc — independent of
the security tiers above but blocked on the same architectural
foundation (Phase 3 per-tenant SAs). If you ship cascade-delete
**before** Phase 3, the provider-side deletes still use the master
cred — no security regression vs today, just a missed opportunity
to scope blast radius further. Either ordering works. The pragmatic
order: do **Phase 1 + Phase 2** first (cheap + medium wins on
quotas and admission), then choose between **Phase 3 (impersonation)**
and **Phase 6 (tenant lifecycle)** based on which blocks you more.
For a dev-stage cloud growing tenants weekly, Phase 6 is the more
operationally pressing one. For a multi-team prod cloud, Phase 3
is the bigger risk.

---

## 7. Tracking

When you start any phase, copy it into FOLLOWUPS.md as a tracked feature
with the same numbering. The phase-by-phase format above is intended to
let you ship 1a today without committing to anything else.
