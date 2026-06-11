# Managed Services Integration Contract

**Version:** 0.1 (draft — pre-implementation)
**Status:** Binding design; implementations MUST conform before dc-api wires them in.
**Audience:** Teams building Kubernetes operators for managed services that plug into
the WSO2 Infrastructure Platform Control Plane (dc-api). Database, cache, registry, keyvault,
and any future service type.

---

## 1. Why a contract exists

The WSO2 Infrastructure Platform platform hosts multiple managed-service operators built by
independent teams on different schedules. Without a shared contract, each operator
invents its own CRD shape, status conventions, networking assumptions, and secret
format — dc-api would need bespoke integration code for every service, and changes
to one operator's API surface would silently break callers. This document is the
single interface definition all operators must conform to. A conformant operator
can be wired into dc-api with approximately 50 lines of framework registration code
and zero bespoke handler logic.

---

## 2. The model

Managed services integrate through the Kubernetes operator pattern.

1. A user calls a dc-api endpoint (`POST /v1/tenants/{tid}/projects/{pid}/<resource-name>`).
2. dc-api authenticates the user, checks quotas, and creates a **Resource CR** in the
   target project namespace.
3. If the service uses a shared **Backend** (see below) and one does not yet exist for
   the tenant, dc-api also creates a Backend CR and waits for it to be Ready.
4. The operator's controller reconciles the Resource CR — provisioning whatever it
   needs: VMs, pods, PVCs, sub-objects inside the Backend, etc.
5. When the Resource is ready, the controller sets `status.phase: Ready` and populates
   `status.endpoint`.
6. dc-api's reconciler detects the phase transition (via a Kubernetes informer),
   updates its internal record, and exposes the endpoint to the caller via GET.
7. The caller retrieves credentials via a one-shot `GET .../{id}/credentials` call.
8. On deletion, dc-api issues a `DELETE` on the Resource CR and waits for the
   finalizer chain to complete before removing its own record.

### Backend vs Resource — the two-layer model

Every managed service is modelled as up to two CRDs:

| Layer | What it represents | Visibility | Lifecycle |
|---|---|---|---|
| **Resource** | The user-facing thing the caller asked for — a database, a cache, a key vault, a registry project. Always exists. | Always exposed at `/v1/tenants/{tid}/projects/{pid}/<resource-name>`. | Created per user request; deleted when the user deletes the resource. |
| **Backend** | The long-lived server/cluster that hosts Resources. Optional. | Internal only — never exposed in dc-api URLs or CLI. | Created lazily by dc-api when the first Resource needs it; lives in `dc-tenant-<slug>` or `dc-system`; may host many Resources. |

Cardinality patterns:

- **1 Resource → 1 Backend** (no shared Backend, `Backend = nil` in the framework
  registration): databases, caches. Each user request gets its own VM/pod set.
- **N Resources → 1 Backend per tenant** (`Backend.Placement = Tenant`): key vaults
  inside a shared per-tenant secret-store cluster, where each Resource is a mount or
  namespace inside the running Backend.
- **N Resources → 1 Backend platform-wide** (`Backend.Placement = Platform`): registry
  projects inside a shared registry cluster.

**Users never see the Backend.** They always create, list, and delete Resources at
the project-scoped URL. dc-api handles Backend lifecycle silently. From the user's
perspective every Resource is independent; the fact that two of their key vaults
share an underlying OpenBao StatefulSet is an implementation detail.

**dc-api owns the user-facing API, credentials flow, and Backend lifecycle. The
operator owns the infrastructure lifecycle of its own CRDs.** Neither crosses that
boundary.

---

## 3. Namespace placement

dc-api places CRs in namespaces according to a three-tier scheme. Operators do NOT
choose placement — dc-api resolves the target namespace from the service registration
and the caller's tenant/project context.

| Tier | Namespace pattern | What lives here |
|---|---|---|
| **Platform** | `dc-system` | Backends shared across the entire platform (e.g. a single container-registry cluster). Operator controller deployments. |
| **Tenant** | `dc-tenant-<tenant-slug>` | Backends shared across a tenant's projects (e.g. a per-tenant secret-store cluster). One Backend CR per (tenant, service-type). |
| **Project** | `dc-<tenant-slug>-<project-slug>` | All Resource CRs. Per-instance Backends for 1:1 services (databases, caches). VMs, PVCs, Services, NADs, and proxy pods. |

**Resources always live in the project tier.** This is non-negotiable — the user's
URL and the K8s namespace agree. Backends live in tenant or platform tier when they
are shared; in the project tier when they are 1:1 with a Resource.

The platform tier namespace (`dc-system`) is created at cluster bootstrap.
The tenant tier namespace (`dc-tenant-<slug>`) MUST be created eagerly when
`POST /v1/admin/tenants` runs — not lazily on first service use.
The project tier namespace (`dc-<tenant>-<project>`) is created at project creation time.

dc-api's `internal/providers/common.NamespaceForProject` derives project-tier names.
Tenant-tier names follow the pattern `dc-tenant-<tenant-slug>` (lowercase, hyphens only,
≤ 56 characters including the prefix).

---

## 4. Required CRD shape

Operators define **one or two CRDs** per service type:

- **Resource CRD** — required. Models a single user-facing instance. Kind name is
  `<Service>Instance` (e.g. `DatabaseInstance`, `KeyVault`, `CacheInstance`).
- **Backend CRD** — required only when the service uses a shared Backend across
  multiple Resources. Kind name is `<Service>Backend` (e.g. `KeyVaultBackend`,
  `RegistryBackend`). For 1:1 services (one Resource per Backend), omit this CRD
  and embed the Backend's lifecycle into the Resource CR itself.

### 4.1 Metadata (Resource CRD)

```yaml
apiVersion: <service>.opencloud.wso2.com/v1alpha1
kind: <ServiceName>Instance        # OR a shorter idiomatic name like DBInstance
```

- `<service>` is a short lowercase identifier unique to the operator (e.g. `database`,
  `cache`, `registry`, `keyvault`, `dbaas`).
- Kind is conventionally `<ServiceName>Instance`, but a shorter idiomatic
  alternative (e.g. `DBInstance`, `Cache`) is acceptable as long as the printer
  columns and `shortNames` make it discoverable. dc-api binds via GVR, so the
  Kind spelling is operator-facing, not user-facing.
- The CRD is **namespaced** (not cluster-scoped). dc-api places it in the resolved
  namespace (see section 3 — always project tier for Resources).
- Short name (encouraged): lowercase abbreviated (e.g. `kv`, `db`, `dbi`, `cache`)
  so `kubectl get <short>` works for operators triaging incidents.

### 4.1a Metadata (Backend CRD, when present)

```yaml
apiVersion: <service>.opencloud.wso2.com/v1alpha1
kind: <ServiceName>Backend
```

- Same group as the Resource CRD; same `v1alpha1` versioning policy.
- Namespaced; placed in `dc-tenant-<slug>` or `dc-system` per the Service's
  Backend placement.
- The Backend CR's `metadata.name` derives from tenant UUID (for tenant-tier) or is
  a fixed singleton name (for platform-tier). dc-api sets this — operators do not.
- The Resource CRs that depend on a Backend reference it via a `spec.backendRef`
  field (see 4.2 below).

### 4.2 Resource spec fields

The contract is deliberately spec-shape-flexible. Two operators in different
service families will reasonably choose different idioms (RDS-style class
strings vs explicit cpu/memory; flat fields vs grouped engine config; struct
vs string network refs). dc-api absorbs the variation in its per-service
adapter (the framework's `BuildSpec` callback — see
`docs/managed-services-framework.md` §2). What matters for the contract is
that the operator picks ONE idiom and is consistent within a single CRD; the
patterns below are both valid.

#### Sizing — pick ONE pattern, document it in the CRD comment

**Pattern A — explicit dimensions (recommended for services without an
established naming convention):**

```yaml
spec:
  cpu: "2"            # Kubernetes quantity string
  memoryGB: 4         # int
  storageGB: 20       # int
```

**Pattern B — size-class string (recommended when the service has an
established public class catalog the user already understands, e.g.
RDS-style `db.t3.medium`):**

```yaml
spec:
  instanceClass: "db.t3.medium"   # opaque string the operator resolves
  allocatedStorage: 50            # int GiB
```

When using Pattern B, the operator MUST ship an in-code class catalog (a
`map[string]{CPUCores, MemoryMB}` style structure) and document it in the
CRD's godoc so dc-api's per-service adapter can translate user-supplied
cpu/memory back to a class name. dc-api's quota model always thinks in
cpu/memoryGB/storageGB — the framework's `CapCost` callback is responsible
for projecting whichever spec shape the operator chose onto the canonical
quota dimensions.

#### Engine-specific configuration

Engine fields (PostgreSQL version, cache eviction policy, mount path) may
appear EITHER grouped under their own struct OR flat at the top level of
spec, whichever reads better for the service. The operator's controller is
the only consumer; the framework does not care.

**Pattern A — grouped (cleaner for services with many engine fields):**

```yaml
spec:
  engineConfig:
    version: "16"
    parameterGroup: "default.postgres16"
```

**Pattern B — flat (cleaner for services with 2-3 engine fields):**

```yaml
spec:
  engineVersion: "16"
  dbName: "myapp"
  masterUsername: "dbadmin"
```

#### Backend reference

```yaml
spec:
  # Required when the service uses a shared Backend (section 4.1a).
  # dc-api sets this to point at the Backend CR for the caller's tenant or
  # the platform-wide singleton. Omit entirely for 1:1 services.
  backendRef:
    name: "keyvault-backend-acme"
```

#### Network reference — pick ONE form, document it

`networkRef` is required only for Resources that need their own
customer-VPC endpoint (typical for VM-backed services like databases;
unnecessary for in-cluster pod-backed services like key vaults that bind
to a shared Backend's Service IP). When present, the operator picks one
shape:

**Pattern A — struct (more typed, harder to mistype):**

```yaml
networkRef:
  namespace: "dc-mycompany-myproject"
  name: "subnet-abc12345-def67890"
```

**Pattern B — string `<namespace>/<name>` (shorter, widely used in Multus
ecosystem):**

```yaml
networkRef: "dc-mycompany-myproject/subnet-abc12345-def67890"
```

Whichever shape the operator chooses, dc-api's adapter constructs the value
from the user's `vnet_id` + `subnet_id` — see §7.1 for the resolution rule.
The operator never sees the user's VPC/subnet IDs directly.

#### General rules

- Fields documented as "NOT YET IMPLEMENTED" in the operator's CRD comments
  MUST be accepted in spec without error. The operator silently ignores them
  until implemented. This preserves forward-compatible schema evolution.
- No spec field may be required if the operator can derive a safe default.
  dc-api will not always supply all fields.
- An operator may expose `spec.deletionProtection: bool` to refuse deletes
  via the finalizer. When it does, dc-api pre-checks the flag at the
  handler before issuing DELETE on the CR and returns 409 with a clear
  message — see §10.

### 4.2a Backend spec fields (when present)

```yaml
spec:
  # Capacity for the SHARED Backend — sized by dc-api as a function of how
  # many Resources are bound to it, OR fixed at a tenant-cap-derived value.
  # The Service's framework registration controls how this is computed.
  cpu: "4"
  memoryGB: 8
  storageGB: 100

  # Backend-specific config (HA replica count, audit destination, etc.)
  engineConfig:
    haReplicas: 3
```

The Backend's `status` follows the same shape as section 5. Its `status.endpoint`
is the in-cluster Service the operator's controller (and proxy pods) hit; it is
NOT user-facing.

### 4.3 Immutable fields

The following spec fields MUST be marked immutable in the CRD validation
(via `x-kubernetes-validations: rule: "self == oldSelf"`):
- `networkRef` — changing the VPC attachment post-provisioning is destructive.
- `backendRef` — a Resource cannot migrate between Backends in place.
- **Any spec field that maps to a dc-api quota dimension** (typically `cpu`,
  `memoryGB`, `storageGB` when present) — resize is deferred to v1beta1.
  Services that do not expose these to users (e.g. key vaults) are unaffected
  by this rule.

---

## 5. Required status shape

The operator is solely responsible for writing `status`. dc-api reads it via
the Kubernetes API; it does not mutate status directly.

### 5.1 The five-state lifecycle dc-api needs

The framework needs to know which of five abstract states the Resource is
in: **Pending / Provisioning / Ready / Failed / Terminating**. The operator
MAY encode that state in any of three ways — the per-service adapter
(`MapStatus` callback) translates the operator's chosen encoding into the
framework's view.

| Pattern | What the CR carries | When to use |
|---|---|---|
| **A. Canonical single field** | `status.phase: Ready` using the five literal values above | New services with no public-API legacy |
| **B. Operator-native single field** | `status.phase: available` (operator picks idiomatic values, e.g. RDS-style lowercase) | Services modelling an established cloud API (RDS, ElastiCache) where users expect those phase strings |
| **C. Two-axis field pair** | `status.phase` (user-facing) + `status.provisioningPhase` (controller's internal step) | Services with long multi-step provisioning where exposing the substep helps UX |

For Patterns B and C the operator MUST document the value enum in the CRD
godoc so the adapter can translate. The framework's `MapStatus(crStatus
map[string]any) ServiceStatus` callback runs per-GET and on every informer
event — it reads whichever fields the operator uses and emits one of the
five canonical phases plus a free-form `Message` (which is what surfaces to
the dc-api caller on GET).

### 5.2 Common shape

```yaml
status:
  # See §5.1 — phase value follows whichever pattern the operator picked.
  phase: Ready

  # Required. Standard Kubernetes condition list.
  conditions:
    - type: Ready
      status: "True"
      lastTransitionTime: "2026-05-21T08:00:00Z"
      reason: InstanceProvisioned
      message: "Instance is accepting connections."

  # Required when the instance exposes a network endpoint.
  endpoint:
    address: "10.150.0.42"    # IP or hostname reachable from the customer VPC
    port: 5432
    # OPTIONAL — credential Secret may live here OR elsewhere; see §5.3.
    secretRef:
      name: "myservice-abc12345-creds"

  # Required. Tracks every Kubernetes object the operator created as a child
  # of this CR. Used for idempotency: on reconcile, the operator checks this
  # list before creating resources so retries are safe.
  #
  # Two shapes are accepted:
  #   - canonical: list of {group, version, kind, namespace, name} entries
  #   - named: a struct of typed string fields, one per child resource kind
  #     (e.g. status.resources.vmName, status.resources.pvcName, ...)
  # The named shape is fine when the set of child kinds is fixed at compile
  # time. Pick whichever fits the controller's reconcile loop.
  resources:
    - group: ""
      version: "v1"
      kind: "PersistentVolumeClaim"
      namespace: "dc-mycompany-myproject"
      name: "data-myservice-abc12345"
    - group: "kubevirt.io"
      version: "v1"
      kind: "VirtualMachine"
      namespace: "dc-mycompany-myproject"
      name: "vm-abc12345"

  # Optional but strongly encouraged. Human-readable explanation of the
  # current phase. Surfaced verbatim to the dc-api caller on GET. When the
  # operator uses Pattern C and leaves message blank during long provisions,
  # the adapter falls back to status.provisioningPhase so the caller sees
  # progress instead of an empty message.
  message: ""
```

### 5.3 Credential Secret reference — declare the path

The reference to the credential Secret is conventionally at
`status.endpoint.secretRef.name`, but established cloud APIs sometimes put
it elsewhere (e.g. `status.masterUserSecret.name` in RDS-shaped services).
Both placements are accepted. The operator declares the JSON path to the
Secret name in its framework registration (the `SecretRefPath` field on
the Service definition — see framework doc §2) so dc-api looks in the right
spot without per-operator handler logic.

dc-api ALSO needs to know the Secret's data-key conventions (`username` +
`password`, or `role_id` + `secret_id`, or a single `token`, …). These are
declared in the per-service `BuildCredential` callback, not in the contract
— each managed-service type ships its own decoder.

### 5.4 Power-cycle (stop/start)

Services that support pausing a Resource (keeping storage but not running
the workload — common for databases and caches) MUST expose stop/start as
dedicated dc-api action endpoints, NOT as additional status enum values:

```
POST .../resources/{id}/stop
POST .../resources/{id}/start
```

The action endpoint updates a `spec.running: false/true` (or equivalent
mutable spec field) on the CR; the operator reacts to that field. The
dc-api status enum stays at five values. The Resource is reported as
`ACTIVE` in both running and stopped states, with `status.message`
explaining "Stopped — POST .../start to resume". Adding STOPPED to the
canonical phase enum would force every consumer (UI, CLI, tf provider) to
add a special case for one service.

---

## 6. Labels — operator MUST propagate

dc-api applies seven standard labels to every CR it creates (from
`internal/providers/common.StandardLabels`). The operator controller MUST
copy all seven labels from the CR's `metadata.labels` to every child resource
it creates — VMs, Secrets, PVCs, Services, ConfigMaps, and any other object.

The seven label keys (use these exact strings):

```
dc-api.wso2.com/tenant
dc-api.wso2.com/project
dc-api.wso2.com/tenant-uuid
dc-api.wso2.com/project-uuid
dc-api.wso2.com/resource-uuid
dc-api.wso2.com/resource-kind
dc-api.wso2.com/resource-name
```

These labels enable:
- **Blast-radius queries** — `kubectl get all -l dc-api.wso2.com/tenant=mycompany`
- **Cost attribution** — filter cloud billing or Longhorn storage by project
- **Quota auditing** — verify all storage/CPU belongs to a live project
- **Cascade cleanup** — find orphans when a tenant is deleted

Propagation is non-negotiable. The operator MAY also stamp its own labels
(e.g. `<service>.opencloud.wso2.com/instance: <id>`) on top — those are
fine and useful for the operator's own selectors — but the seven
`dc-api.wso2.com/*` labels MUST be present in addition.

### 6.1 Propagation pattern

Operators conventionally implement a single helper that's called from every
child-resource builder:

```go
// copyDCAPILabels merges the seven dc-api.wso2.com/* labels from the
// parent CR into the child's label map. Call this for every child:
// VMs, Secrets, PVCs, Services, ConfigMaps, ServiceMonitors, NADs.
func copyDCAPILabels(parent metav1.Object, child metav1.Object) {
    src := parent.GetLabels()
    dst := child.GetLabels()
    if dst == nil { dst = map[string]string{} }
    for _, k := range []string{
        "dc-api.wso2.com/tenant",
        "dc-api.wso2.com/project",
        "dc-api.wso2.com/tenant-uuid",
        "dc-api.wso2.com/project-uuid",
        "dc-api.wso2.com/resource-uuid",
        "dc-api.wso2.com/resource-kind",
        "dc-api.wso2.com/resource-name",
    } {
        if v, ok := src[k]; ok { dst[k] = v }
    }
    child.SetLabels(dst)
}
```

A child-creation call site then looks like:

```go
vm := &kubevirtv1.VirtualMachine{
    ObjectMeta: metav1.ObjectMeta{
        Name:      vmName,
        Namespace: parentCR.Namespace,
        Labels:    map[string]string{
            "<service>.opencloud.wso2.com/instance": parentCR.Name, // operator-private
        },
        OwnerReferences: []metav1.OwnerReference{ownerRefFor(parentCR)},
    },
    // ... spec ...
}
copyDCAPILabels(parentCR, vm)  // ← adds the seven dc-api.wso2.com/* labels
```

### 6.2 PR gate

A PR to wire an operator into dc-api MUST demonstrate propagation with a
test that asserts the seven labels exist on (a) the credential Secret and
(b) at least one child PVC and at least one child compute object (VM, Pod,
or StatefulSet, whichever the service emits). A test that only checks
labels on the parent CR does NOT satisfy this rule — the parent labels
are stamped by dc-api itself; the contract is about the operator
propagating them downward.

---

## 7. Networking

**Operators do not create networks. Operators do not create proxy pods either.**

dc-api owns the customer-facing network exposure of every managed service via
a **PrivateEndpoint** primitive — a generic 2-NIC proxy pod (cluster CNI + Multus
into a chosen VPC) that bridges any service into any of the tenant's VPCs.
Users create PrivateEndpoints separately from the underlying Resource; one
Resource can have many PrivateEndpoints, each exposing it into a different VPC.

The operator's job is to expose the Resource at some **in-cluster address** —
typically a Kubernetes Service or, for VM-backed services, the VM's IP on its
Multus NAD. The operator declares this address in
`status.endpoint.address` + `status.endpoint.port`. dc-api's PrivateEndpoint
handler reads those, plugs them into a per-service `BackendResolver`, and
configures the proxy pod's upstream accordingly.

This means:

- For pod/StatefulSet-backed services (key vault, cache, registry):
  `status.endpoint.address` is the in-cluster Service DNS name.
- For VM-backed services (database): `status.endpoint.address` is the VM's
  Multus IP (which is already in some VPC; PrivateEndpoints expose it into
  other VPCs).
- For mount/path-based services (key vault where each Resource is a mount path
  inside a shared Backend): `status.endpoint.address` points at the Backend's
  Service; the user's URL includes the mount path. dc-api can return this in
  GET responses.

**The operator never reaches into a customer VPC.** If a Resource needs to be
reachable from outside the cluster, that's exclusively a PrivateEndpoint concern.
The contract requires `status.endpoint.address` and `port` to be honest about
where the operator placed the service; everything downstream is dc-api's job.

The NAD referenced in `spec.networkRef` (when present on Resource or Backend)
already exists when the CR is created — dc-api provisions it at subnet
creation time via the KubeOVN provider.

### 7.1 How `spec.networkRef` is resolved

dc-api's user-facing API accepts a `vnet_id` + `subnet_id` (both UUIDs) on
the create request. The framework's per-service `BuildSpec` callback never
sees those IDs directly — by the time the callback is invoked, dc-api has
resolved them to the NAD identity. The callback receives:

```go
req.NetworkRef = &NetworkRef{
    Namespace: "dc-mycompany-myproject",  // project-tier namespace
    Name:      "subnet-abc12345",          // KubeOVN-backed NAD name
}
```

`BuildSpec` plumbs that into the CR spec at whatever path the operator
expects — `spec.networkRef` as a struct, or
`<Namespace>/<Name>` as a single string, or whatever the operator's CRD
documents (see §4.2 Pattern A vs Pattern B for networkRef shape).

**The operator does NOT need to know how the NAD was produced.** dc-api's
KubeOVN provider creates a Multus NAD for every subnet at subnet creation
time; the NAD speaks the standard Multus interface regardless of whether
it backs a tenant VPC, a legacy bridge VLAN, or a future SDN. Operators
that already worked against a flat bridge-NAD test environment integrate
into a VPC-backed environment with zero code changes — the only thing
that differs is the namespace/name dc-api passes in.

For tests in the operator's own dev cluster where there is no dc-api, the
operator team can hand-create a NAD (`kubectl apply -f` a
`NetworkAttachmentDefinition` of any flavour) and reference it. The
production wiring is then a no-op rename.

### 7.2 Attaching pods or VMs to the customer VPC

```yaml
# Pod annotation (Multus):
annotations:
  k8s.v1.cni.cncf.io/networks: '[{"name":"<nad-name>","namespace":"<project-ns>"}]'
```

Customer-VPC IP addresses come from the NAD's IPAM configuration — the operator
does not specify IPs. The operator MUST NOT request a static IP unless it has
received explicit confirmation from the dc-api team that the subnet's IPAM
supports static allocation for that kind.

When a service needs a second NIC to bridge between the cluster network and the
customer VPC (e.g. a proxy pod), use the following NIC layout:
- `eth0` — default cluster CNI (Calico or equivalent); use this for all
  in-cluster traffic (reaching operator-owned Services, control-plane APIs).
- `net1` — Multus NIC into the customer VPC subnet; use this for the address
  the customer sees.

This layout is the same pattern dc-api uses for other per-project proxy pods.
Both NICs are inside the project namespace, so subnet teardown cleanly removes
the pod's logical switch ports — the operator does not need to participate in
subnet-delete cleanup.

---

## 8. Authentication and secrets

dc-api authenticates the user. Operators MUST NOT implement their own
authentication for user requests — dc-api is the authentication boundary.

Credentials generated during instance provisioning (passwords, tokens, TLS
certs) MUST be stored in a Kubernetes Secret in the same namespace as the
CR. The Secret name MUST be referenced from the CR's status; the JSON path
to the name is declared in the framework registration (default
`status.endpoint.secretRef.name` — see §5.3).

Rules for the credential Secret:
- Name must be derived from the resource UUID (e.g. `<service>-<8-char-uuid>-creds`).
  Must not include the tenant slug, user email, or any PII in the Secret name or
  any label key or value.
- Owned by the CR (owner reference set) so it is cleaned up when the CR is deleted.
- The seven `dc-api.wso2.com/*` labels MUST be propagated to the Secret
  (see §6) — dc-api needs them to filter Secret-listing operations.
- RBAC: the operator's ServiceAccount needs read access to Secrets in the project
  and tenant namespaces it manages. No other ServiceAccount in the cluster should
  have read access to these Secrets by default.

dc-api exposes credentials via a one-shot endpoint:

```
GET /v1/tenants/{tid}/projects/{pid}/<resource-name>/{id}/credentials
```

The first call after the Resource reaches `status.phase: Ready` returns the
credential bundle (extracted from the Secret). dc-api marks the credential
as consumed in its own database; subsequent calls return `410 Gone`. There
is no server-side storage of the credential beyond the operator's own
Secret — which the operator's rotation mechanism is responsible for.

### 8.1 Secret data keys are per-service — declare them

The keys inside `secret.data` are entirely service-specific. dc-api's
per-service registration (see framework doc §2 `CredentialSecretKeys`)
maps canonical fields (`username`, `password`, `token`, `ca_cert`) to the
operator's actual keys. Mismatch surfaces as a 503 on
`GET .../credentials` because the framework reads empty strings and
refuses to mark the credential consumed — the request retries are idempotent
and the user sees a clear message rather than a half-populated bundle.

**Operators MUST document the Secret data-keys in their CRD's godoc.**
Examples of legitimate variation:

| Service kind | Canonical bundle | Actual Secret data keys |
|---|---|---|
| Token-style (e.g. key vault AppRole) | `role_id`, `secret_id`, `vault_path` | `role_id`, `secret_id`, `mount_path` |
| User/password (canonical-named) | `username`, `password`, `ca_cert` | `username`, `password`, `ca_cert` |
| User/password (RDS-style) | `username`, `password`, `ca_cert` | `admin_user`, `admin_password`, `ca_cert` |
| Multi-role database | `username`, `password`, …+repl+exporter | `admin_user`, `admin_password`, `repl_password`, `exporter_password`, `ca_cert`, `server_cert`, `server_key` |

Whichever keys the operator picks, they MUST be stable for the lifetime
of the v1alpha1 API. Changing key names in a controller upgrade
silently breaks every existing Resource's credentials retrieval. Treat
key names like API fields — additive only, deprecate via v1beta1.

---

## 9. Async reconciliation

All provisioning operations MUST be non-blocking from the controller's perspective:
start work, update `status.phase`, return. Do not block the reconcile loop on
long-running operations.

**Idempotency is required.** The controller will be reconciled multiple times for
the same CR (controller restarts, watch re-syncs, etc.). Use `status.resources`
to track what has already been created. On each reconcile pass, check the list
before creating any child resource. If the child already exists and matches the
desired spec, skip creation.

**Finalizers.** The operator MUST register a finalizer on every CR it manages
(e.g. `<service>.opencloud.wso2.com/cleanup`). On CR deletion:
1. Set `status.phase: Terminating`.
2. Delete all child resources in reverse creation order.
3. Wait for child resources to be gone (poll or watch; never use fixed sleeps).
4. Remove the finalizer.

dc-api only deletes its internal record after the CR's finalizer has completed
and the CR row is gone from the Kubernetes API.

**Drift detection.** If the underlying infrastructure diverges from the desired
state (e.g. a VM is deleted externally), the operator MUST detect this and either
reconcile back to `Ready` or set `status.phase: Failed` with a descriptive message.
dc-api surfaces `status.phase` directly to callers via `GET` — there is no separate
health check endpoint.

---

## 10. Quotas

The project namespace has a Kubernetes `ResourceQuota` object provisioned by
dc-api at project creation time. This quota enforces `cpu`, `memory`, and `storage`
limits as a backstop against over-provisioning.

Every pod the operator creates in the project namespace MUST have `resources.requests`
and `resources.limits` set. Pods without resource requests will be rejected by
the admission controller when the namespace has a ResourceQuota that covers the
`requests.cpu` or `requests.memory` fields.

For VMs provisioned via KubeVirt + CDI:
- Factor in CDI's image import DataVolume: CDI requires approximately 2x the
  uncompressed disk size during import (scratch space + final PVC). A 20 GB
  image import will briefly claim ~40 GB of storage against the quota. Design
  the quota check accordingly.
- The VM's root disk PVC must have `resources.requests.storage` set.

For services in the tenant-tier namespace (`dc-tenant-<slug>`), there is no
pre-provisioned ResourceQuota by default. The operator is responsible for
ensuring it does not over-provision beyond what the tenant's capacity cap permits.
dc-api performs a cap pre-check (via the framework's `CapCost` callback) before
creating the CR.

---

## 11. Operator deployment conventions

- Deploy the controller in the **`dc-system`** namespace — not in a per-service
  namespace such as `<service>-system`. Kubebuilder-scaffolded projects default
  their kustomize bundle to `<projectName>-system` (e.g. `dbaas-system`,
  `cache-system`); the consumer's deployment overlay MUST override that
  namespace, OR the operator team should re-bundle their default manifest
  with `namespace: dc-system` baked in. Operators that ship with the
  scaffold-default namespace and document `make deploy` without an overlay
  step put the burden of remembering this on the operator's user.
- Package as a Helm chart with:
  - CRDs under `crds/` (not in `templates/` — prevents accidental deletion on
    `helm uninstall`).
  - Controller Deployment, ClusterRole, ClusterRoleBinding, and ServiceAccount
    under `templates/`.
- ClusterRole MUST be restricted to:
  - Full CRUD on its own CRD group (e.g. `database.opencloud.wso2.com`).
  - Read access to Namespaces (for listing tenant/project namespaces).
  - Create/Get/Delete access to Secrets, PVCs, Services, and any other child
    resource types in the namespaces it manages.
  - No permissions on CRD groups belonging to other operators.
- RBAC rules MUST use `resourceNames` where possible to restrict access to
  named objects rather than entire resource types.
- The operator MUST expose a `/healthz` endpoint for Kubernetes liveness and
  readiness probes.
- The operator MUST log in structured JSON to stdout. Log line fields: `level`,
  `time` (RFC3339), `namespace`, `name`, `phase`, `msg`. No multi-line log entries.

---

## 12. Forward compatibility

Schema evolution rules:

- **Never remove a spec field** without a new API version (`v1beta1`). Mark
  deprecated fields with a comment `// Deprecated: will be removed in v1beta1`.
- **Never change a spec field's type** (e.g. `int` to `string`) in the same
  API version.
- **New optional spec fields** may be added at any time. They must default safely
  (the operator fills a sensible default when the field is absent).
- **Fields reserved for future use** MUST be accepted in spec without triggering
  validation errors. Use OpenAPI `x-kubernetes-preserve-unknown-fields: false`
  only on the specific struct containing the reserved fields, with a comment
  listing which fields are reserved.
- **NOT YET IMPLEMENTED fields** MUST be documented in the CRD's OpenAPI
  description (e.g. `// +kubebuilder:validation:Optional // NOT YET IMPLEMENTED`).
  The operator MUST accept these fields and ignore them until implemented.

---

## 13. How dc-api integrates

dc-api's `internal/managedservice` package provides a registration framework that
wraps the CR lifecycle described above. To integrate a new operator:

1. The operator team provides:
   - A conformant CRD (this document).
   - A Go `managedservice.Service` struct describing GVK, namespace placement,
     a `BuildSpec` callback (transforms the dc-api `CreateRequest` into the CR
     spec), a `MapStatus` callback (maps `status.phase` to dc-api's status enum),
     a `BuildCredential` callback (extracts credentials from the credential Secret),
     and a `CapCost` callback (returns the resource cost for quota pre-checking).

2. Register the service in dc-api's router with a single call:

   ```go
   managedservice.Register(router,
       "/v1/tenants/{tid}/projects/{pid}/databases",
       myservice.ServiceDefinition(),
       managedservice.Deps{
           DynClient: dynClient,
           Repo:      repo,
           Log:       log,
       },
   )
   ```

3. dc-api auto-generates `POST/GET/LIST/DELETE` endpoints under the given path
   with the async 202 pattern, status polling, credential-shown-once response,
   and tenant-cap pre-check — all from the callbacks.

See `docs/managed-services-framework.md` for the full Go interface specification
that the `Service` struct and callbacks must satisfy.

---

## Appendix A — Example conformant CR

```yaml
apiVersion: database.opencloud.wso2.com/v1alpha1
kind: DatabaseInstance
metadata:
  name: db-a1b2c3d4
  namespace: dc-mycompany-myproject
  labels:
    dc-api.wso2.com/tenant: mycompany
    dc-api.wso2.com/project: myproject
    dc-api.wso2.com/tenant-uuid: "550e8400-e29b-41d4-a716-446655440000"
    dc-api.wso2.com/project-uuid: "550e8400-e29b-41d4-a716-446655440001"
    dc-api.wso2.com/resource-uuid: "a1b2c3d4-0000-0000-0000-000000000000"
    dc-api.wso2.com/resource-kind: database
    dc-api.wso2.com/resource-name: my-postgres
  finalizers:
    - database.opencloud.wso2.com/cleanup
spec:
  cpu: "2"
  memoryGB: 4
  storageGB: 20
  engineConfig:
    version: "16"
    multiAZ: false               # NOT YET IMPLEMENTED
    backupRetentionDays: 7       # NOT YET IMPLEMENTED
  networkRef:
    namespace: dc-mycompany-myproject
    name: subnet-a1b2c3d4-e5f6a7b8
status:
  phase: Ready
  conditions:
    - type: Ready
      status: "True"
      lastTransitionTime: "2026-05-21T08:00:00Z"
      reason: InstanceProvisioned
      message: "Database accepting connections."
  endpoint:
    address: "10.150.0.42"
    port: 5432
    secretRef:
      name: "db-a1b2c3d4-creds"
  resources:
    - group: ""
      version: "v1"
      kind: "PersistentVolumeClaim"
      namespace: dc-mycompany-myproject
      name: data-db-a1b2c3d4
    - group: "kubevirt.io"
      version: "v1"
      kind: "VirtualMachine"
      namespace: dc-mycompany-myproject
      name: vm-a1b2c3d4
  message: ""
```

## Appendix B — Credential Secret shape

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: db-a1b2c3d4-creds
  namespace: dc-mycompany-myproject
  ownerReferences:
    - apiVersion: database.opencloud.wso2.com/v1alpha1
      kind: DatabaseInstance
      name: db-a1b2c3d4
      uid: <cr-uid>
      blockOwnerDeletion: true
      controller: true
  labels:
    # All seven dc-api.wso2.com/* labels propagated from the parent CR.
    dc-api.wso2.com/tenant: mycompany
    dc-api.wso2.com/project: myproject
    dc-api.wso2.com/tenant-uuid: "550e8400-e29b-41d4-a716-446655440000"
    dc-api.wso2.com/project-uuid: "550e8400-e29b-41d4-a716-446655440001"
    dc-api.wso2.com/resource-uuid: "a1b2c3d4-0000-0000-0000-000000000000"
    dc-api.wso2.com/resource-kind: database
    dc-api.wso2.com/resource-name: my-postgres
type: Opaque
data:
  username: <base64>
  password: <base64>
  ca_cert:  <base64>   # when TLS is used
```
