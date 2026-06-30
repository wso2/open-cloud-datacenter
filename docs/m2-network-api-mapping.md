# M2 Network API — Cloud Concepts ↔ KubeOVN Mapping

**Status:** design notes. **Spike completed 2026-05-06 with all 5 gates green** —
self-managed upstream KubeOVN as multus secondary on Harvester is viable for
M2. This doc informs the `NetworkProvider` interface and the kubeovn driver
under `dc-api/internal/providers/kubeovn/`.

**Audience:** DC-API engineers designing the network API and the kubeovn
driver. Not user-facing — users see only Azure-shaped resources.

---

## The headline insight

KubeOVN models a VPC as an **OVN logical router**, not as an address space.
This is why `Vpc` CRDs do not have a `cidrBlock` — a router doesn't have
one; only its interfaces (subnets) do.

Cloud providers (Azure VNet, AWS VPC) attach an address space to the VPC
itself for three reasons:

1. **Billing / quota** — the address space is reserved capacity.
2. **Peering validation** — peerings reject overlapping CIDRs at create time.
3. **Subnet containment** — subnet CIDRs must be subsets of the VNet CIDR;
   prevents sprawl and surprises.

KubeOVN only enforces (3) loosely — "no two subnets in the same VPC overlap"
— and does not enforce (1) or (2). DC-API adds these constraints at the API
layer:

- `vnets` row carries an `address_space TEXT[]` column (one or more CIDRs).
- `POST /v1/vnets/{id}/subnets` rejects subnet CIDRs not contained in any of
  the VNet's address-space entries (HTTP 400, clear message).
- `POST /v1/vnets/{id}/peerings` rejects overlapping address spaces between
  the two VNets.

The kubeovn driver constructs the KubeOVN `Vpc` CRD without a CIDR (because
the CRD has no field for it) and persists `address_space` only in DC-API's
own DB. KubeOVN never sees it; only DC-API enforces against it.

---

## Resource mapping

| DC-API (public, Azure-shaped) | KubeOVN primitive | Semantic owner | Notes |
|---|---|---|---|
| `VNet` with `address_space: [...]` | `Vpc` CRD | DC-API + KubeOVN | KubeOVN handles the logical router. DC-API holds the CIDR constraint and validates it. |
| `Subnet` (CIDR, gateway, dhcp options) | `Subnet` CRD | KubeOVN | KubeOVN enforces no-overlap within the same VPC. DC-API enforces containment in the VNet's address space. |
| `RouteTable` (rules: dest CIDR → next hop) | `Vpc.spec.staticRoutes` + `Vpc.spec.policyRoutes` | DC-API surfaces, driver flattens | Cloud users expect a route table as an object. KubeOVN puts routes on the VPC. The driver appends to the VPC's route lists at create time, removes at delete time. |
| `NetworkSecurityGroup` (rules + associations) | Either `Subnet.spec.acls` *or* `SecurityGroup` CRD | DC-API decides per association | If the NSG is associated with a subnet → subnet ACL. If associated with a NIC/VM → SecurityGroup CRD attached via pod annotation. The driver picks based on the `target` field in the API request. |
| `VNetPeering` | `VpcPeering` CRD + reciprocal routes on both VPCs | DC-API + driver | Driver creates the peering CRD and adds the reciprocal `staticRoutes` entries. DC-API enforces no-overlap and same-subscription scoping. |
| `NatGateway` (public IP, SNAT) | `VpcNatGateway` + `IptablesEip` / `OvnEip` | KubeOVN | EIP allocation comes from a pre-configured external IP pool. DC-API tracks ownership, KubeOVN handles dataplane. |
| `PrivateDnsZone` | `VpcDns` CRD | KubeOVN | Per-VPC DNS resolver. DC-API exposes Azure-style zones; driver translates. |
| `InternalLoadBalancer` | `SwitchLBRule` | KubeOVN | Per-subnet L4 load-balancing. |
| `PublicIp` (standalone) | `OvnEip` (or `IptablesEip` for legacy NAT mode) | KubeOVN | Allocated from external pool, attachable to NICs / NAT gateways. |
| `VpnGateway` (site-to-site IPsec) | Tenant-deployable VyOS VM (M3+) | Tenant resource | NOT native in KubeOVN. VyOS provides IPsec, IKEv2, route-based VPN. M3+ feature; treat like a managed compute resource that happens to terminate VPN. |
| `BgpPeering` (e.g. corporate uplink) | Tenant-deployable VyOS VM (M3+) | Tenant resource | NOT native in KubeOVN. VyOS does BGP termination cleanly. M3+ feature. |
| `FlowLog` | (none yet — KubeOVN supports OVS port mirroring; needs custom tap pipeline) | DC-API + future driver work | Defer past M2. |

---

## Zone placement and inheritance

Every VNet carries an immutable `zone` attribute assigned at creation. If omitted, it defaults to the control plane's local zone (the zone where dc-api itself runs). Child resources—subnets, VMs, clusters, route tables, NAT gateways—inherit their parent VNet's zone and cannot override it. This ensures all resources in a VNet are co-located in the same Harvester cluster.

**Rules:**

1. **VNet creation** — `POST /v1/vnets` accepts an optional `zone` field. If omitted, defaults to `DCAPI_LOCAL_ZONE`. Once assigned, the zone is immutable (not updateable via PATCH).
2. **Subnet creation** — `POST /v1/vnets/{id}/subnets` does not accept a zone field. The subnet inherits the parent VNet's zone.
3. **VM/Cluster creation** — When attaching a VM or cluster to a VNet/subnet, the compute resource inherits the zone. Zone is validated at create time (must match the VNet's zone).
4. **Peering** — Both VNets must have the same zone. Cross-zone peering is rejected with `422 Unprocessable Entity`.
5. **Backend isolation** — The kubeovn driver is instantiated per-zone. A VNet in zone A always routes to the Harvester cluster and KubeOVN instance in zone A, never elsewhere.

**Example zone inheritance chain:**

```
VNet (zone: zone-1)
  ├─ Subnet-1 (inherits: zone-1)
  │   ├─ VM-1 (inherits: zone-1)
  │   └─ VM-2 (inherits: zone-1)
  └─ Subnet-2 (inherits: zone-1)
      └─ VM-3 (inherits: zone-1)
```

All compute and network operations route through the zone's Harvester cluster. No cross-zone traffic within a VNet.

---

## NSG model — design call

Azure NSG combines two patterns that AWS splits:

- **Subnet-attached** → AWS NACL-style (stateless, applies to all traffic
  in/out of the subnet).
- **NIC-attached** → AWS Security Group-style (stateful, applies to a single
  instance's traffic).

KubeOVN gives us both primitives:

- `Subnet.spec.acls` — stateless, per-subnet, OVN ACL syntax (e.g.
  `match: 'ip4.dst == 10.99.2.0/24', action: drop`).
- `SecurityGroup` CRD — pod/VM-level, attached via annotation
  (`ovn.kubernetes.io/security_groups: <sg-name>`), supports ingress/egress
  rules with stateful matching.

DC-API exposes a single `NetworkSecurityGroup` resource. The
`associations: [{type, target_id}]` field decides:

- `type: subnet` → the driver writes the rule set as `Subnet.spec.acls`.
- `type: nic` → the driver creates a `SecurityGroup` CRD and annotates the
  target VM/pod.

This keeps the user-facing API simple (one NSG resource type) while letting
the driver pick the right OVN primitive for the binding target.

**No VyOS needed for NSG.** VyOS is reserved for things KubeOVN cannot do
(VPN, BGP) — see the `VpnGateway` and `BgpPeering` rows above.

---

## Route table — design call

Cloud route tables are first-class objects:

```hcl
resource "azurerm_route_table" "rt1" {
  name = "internal-rt"
  route { ... }
}
resource "azurerm_subnet_route_table_association" "a" {
  subnet_id      = ...
  route_table_id = ...
}
```

KubeOVN puts routes directly on the VPC:

```yaml
apiVersion: kubeovn.io/v1
kind: Vpc
metadata:
  name: ...
spec:
  staticRoutes:
    - cidr: 10.99.5.0/24
      nextHopIP: 10.99.1.5
      policy: policySrc
  policyRoutes:
    - match: ip4.src == 10.99.1.0/24
      action: reroute
      nextHopIP: 10.99.1.99
      priority: 1000
```

DC-API exposes a separate `RouteTable` resource for Azure-parity. The driver
flattens it onto the parent VPC at create time. Multiple Route Tables on the
same VPC concatenate their entries; deletion removes only that table's
entries (the driver tags entries with the route-table ID in a comment field
so it can match on delete).

Limitation: KubeOVN's logical router is per-VPC, so two subnets in the same
VPC cannot have *different* route tables in any meaningful sense. The driver
should warn (or reject) if the user tries to associate different route
tables to subnets within the same VNet — same constraint exists in some
cloud providers.

---

## VPC peering — design call

KubeOVN's `VpcPeering` CRD only creates the OVN-side peering construct. The
driver also adds **reciprocal `staticRoutes` entries** on both VPCs — without
those, the peering exists but routes nothing.

DC-API enforces:

1. Both VNets in the same Subscription. (Cross-subscription peering is
   explicitly not supported — same as Azure's original VNet peering scope.)
2. Address spaces don't overlap. Reject at create time, HTTP 400.
3. Audit log entry on both sides.

Cross-VPC isolation guarantees (already documented in MILESTONES.md M2):
encapsulation tunnel ID, separate logical routers, default-deny ACLs, and
DC-API peering enforcement — four independent layers, defeating any one is
not enough to break across.

---

## NAT gateway — design call

KubeOVN supports EIPs in two modes:

- `IptablesEip` — uses iptables NAT (legacy). Works without `non-primary CNI mode`.
- `OvnEip` — uses OVN-native NAT (preferred since KubeOVN v1.15 / Harvester v1.8).
  Requires KubeOVN to be running in non-primary CNI mode (which we are).

DC-API uses `OvnEip` exclusively. The driver allocates from a pre-configured
external IP pool (defined per-region by the operator); EIPs are attached to
`VpcNatGateway` for SNAT, or directly to a NIC for 1:1 DNAT.

---

## DNS — design call

Azure has Private DNS Zones scoped to a VNet (or linked across VNets).
KubeOVN has `VpcDns`, which provides a per-VPC DNS resolver (a CoreDNS pod
per VPC or a shared one with VPC-aware records).

DC-API exposes `PrivateDnsZone` as a first-class resource. Records (`A`,
`CNAME`, `SRV`, etc.) are sub-resources. The driver maps to `VpcDns` plus
ConfigMap-based records.

---

## What's deferred past M2

- **`VpnGateway`** — VyOS-based, M3+.
- **`BgpPeering`** — VyOS-based, M3+.
- **`FlowLog`** — needs a tap pipeline; defer past M2.
- **`ExpressRoute`-equivalent** — same as VPN/BGP; M3+ via VyOS or future
  Megaport-style integration.
- **Multi-region peering** — depends on a cross-DC fabric (overlay or
  underlay tunnels between regions). Out of scope for M2 (single-region).

---

## Spike findings — gotchas the kubeovn driver MUST handle

These were discovered during the 2026-05-06 spike and are non-obvious. The
production kubeovn driver implementation must encode each one or VMs will
silently fail.

### 1. MAC pinning is mandatory for KubeVirt VMs on KubeOVN

OVN logical-switch ports have **port-security** enabled by default. KubeOVN
registers each LSP with a single allowed source MAC. KubeVirt's `bridge: {}`
binding mode generates a *random* virtio MAC for the VM that does **not**
match the MAC kube-ovn registered on the LSP — so OVN silently drops every
frame from the VM.

**Fix (must be on every VM the driver creates):**

```yaml
metadata:
  annotations:
    # Pin the MAC kube-ovn writes onto the OVN logical-switch-port allow list.
    # IMPORTANT: annotation key uses `.ovn.kubernetes.io/` (with `.ovn.`).
    # The form without `.ovn.` is silently ignored.
    <nad>.<namespace>.ovn.kubernetes.io/mac_address: "<mac>"
spec:
  template:
    spec:
      domain:
        devices:
          interfaces:
            - name: ovn
              bridge: {}
              macAddress: "<mac>"   # MUST equal the annotation above
```

**MAC generation strategy in the driver:** generate a stable, locally-administered
MAC from the resource UUID at create time (e.g. `02:` + first 5 bytes of the
hash), persist it on the DC-API resource row, set both annotation and
interface field to that value. Locally-administered (`02:`-prefix) avoids
collision with real hardware MACs.

### 2. Live migration requires homogeneous CPU OR explicit `cpu.model`

KubeVirt's migration check refuses target nodes lacking the source CPU's
features. In a heterogeneous cluster (mixed CPU generations) migrations
fail with `migrationTargetPodUnschedulable`.

**Fix in driver:** when the cluster has mixed CPUs, set `domain.cpu.model`
to the highest common denominator across the cluster's nodes. The driver
should:
- Query `cpu-model.node.kubevirt.io/*=true` labels across all Harvester nodes
- Compute the intersection
- Pick the most modern model in that intersection (e.g. IvyBridge if that's
  the LCD)
- Set `domain.cpu.model` on every VM

For homogeneous clusters this can be omitted (KubeVirt will pick optimally).

### 3. VM affinity must be `preferred`, not `required`

If the VM spec uses `requiredDuringSchedulingIgnoredDuringExecution`, the
migration target pod can't schedule on a *different* node from the source.
The driver must use `preferredDuringSchedulingIgnoredDuringExecution` for
any soft node placement (e.g. anti-affinity, zone preference).

### 4. ACL changes must be patches, not file deletes

Removing an ACL is a `spec.acls` field-level update on an existing Subnet
CRD, NOT a delete of the CRD (which would tear down the whole subnet and
all bound LSPs, blocked indefinitely by the kube-ovn finalizer because of
running consumers).

The driver's `RemoveSecurityGroup`/equivalent operation must:
1. Read the current Subnet's `spec.acls`
2. Build a new array with the matching entry filtered out (the entry's
   `match` field is the natural identifier)
3. PATCH the Subnet with the filtered array
4. NEVER `kubectl delete subnet ...` as part of normal ACL lifecycle

Same principle applies to route entries (`Vpc.spec.staticRoutes`), peerings,
NAT rules, etc. — every kube-ovn CRD has dependencies that finalize on
delete; we always patch fields, never delete the parent.

### 5. KubeOVN tracks every cluster pod in `ips.kubeovn.io`, not just OVN-attached pods

In multus-secondary mode, kube-ovn-controller creates an `ips.kubeovn.io`
CRD for every pod created in the cluster — even ones using only Canal.
Those entries are **phantom allocations** in `ovn-default` (10.16.0.0/16);
the pod doesn't actually use them, real packet flow goes through Canal.

**Operational implications:**
- `kubectl get ip` looks like every pod is on KubeOVN — it isn't. Compare
  the IP CRD's `.spec.ipAddress` against the pod's `.status.podIP`. If they
  differ, the kube-ovn entry is a phantom.
- Cleanup is automatic on pod delete.
- Document this in the operator runbook so engineers don't waste time chasing it.

### 6. Multus annotation `default-network` doesn't override the pod's primary CNI

The annotation `v1.multus-cni.io/default-network: <ns>/<nad>` is *supposed*
to make the named NAD the pod's only network. In practice, on Harvester's
RKE2 with Canal+Multus, Canal still attaches eth0 anyway. Pods end up
dual-homed (Canal + KubeOVN). For VMs this doesn't matter (KubeVirt's
domain only attaches the multus interface). For test pods, expect Canal
to be the default route — explicitly add `ip route` for KubeOVN subnets
when running connectivity probes.

### 7. OVN port rebinding has a brief stabilization window after live migration

After a successful KubeVirt live migration, kube-ovn must update which
chassis hosts the LSP and update OVS flows on the new node. There's a
~5-30 second window where the VM is "moved" but inbound traffic from
peers hasn't caught up. This is normal OVN behavior and **the same as
production live-migration semantics on AWS/Azure**. DC-API's API should
NOT report a VM as "ACTIVE on new node" until the OVN port-binding has
landed — otherwise callers might assume connectivity is restored when it
isn't. The driver should wait on `Port_Binding`'s `chassis` field in
ovn-sb before declaring migration complete.

---

---

## Driver implementation notes (2026-05-06)

This section records findings made while implementing
`dc-api/internal/providers/kubeovn/client.go` (issue #149).  Update this
section if any future driver work contradicts the assumptions below.

### GVR plurals confirmed on KubeOVN v1.15 (lk, 2026-05-06)

| CRD kind | Plural used in code | Notes |
|---|---|---|
| `Vpc` | `vpcs` | cluster-scoped |
| `Subnet` | `subnets` | cluster-scoped |
| `VpcPeering` | `vpc-peerings` | cluster-scoped; note hyphen — NOT "vpcpeerings" |
| `VpcDns` | `vpcdnses` | cluster-scoped; availability probed at startup |
| `Ip` | `ips` | cluster-scoped; phantom detection helper added |
| `NetworkAttachmentDefinition` | `network-attachment-definitions` | namespaced (tenant ns) |

The driver probes for `vpcdnses` at construction time (`Client.vpcDnsAvailable`
field).  If the CRD is absent, DNS zone management falls back to a ConfigMap
named `dc-dns-<sanitized-zone-name>` in the tenant namespace (CoreDNS
file-plugin format).

### VpcDns record management

KubeOVN v1.15's `VpcDns` CRD spec does not expose per-record fields — it
references a CoreDNS ConfigMap internally.  For M2, record-level operations
(`UpsertDnsRecord`, `DeleteDnsRecord`) always use the ConfigMap path, even
when VpcDns mode is active.  The VpcDns CRD is created to set the VPC scope;
records go directly into the ConfigMap.  Issue #153 tracks upgrading to a
native record API when KubeOVN exposes one.

### Route entry tagging field

The driver uses the KubeOVN `staticRoutes` entry's `routeTable` field (a
string) to embed the owning resource tag (`routetable-<UUID>` or
`peering-<name>`).  This field is not in KubeOVN's official schema but is
passed through as-is in the unstructured object and persisted in the CRD.
On a future KubeOVN upgrade, verify this field is still round-tripped by
`kubectl apply` without being stripped.

### NSG backendUID encoding

Because the `NetworkProvider` interface's `UpdateNSGRules(ctx, backendUID,
rules)` receives only one string for the backend UID, the handler must encode
both the NSG UUID and the list of attached subnet UIDs into a single string:

```
"<nsgUUID>|<subnetUID1>|<subnetUID2>|..."
```

The driver's `parseNSGBackendUID` splits on `"|"`.  If no subnets are
attached (no `"|"` present), `UpdateNSGRules` is a no-op: rules are buffered
in the DC-API DB only and applied the next time a subnet is attached.

### RouteTable backendUID encoding

`UpdateRouteTableRoutes(ctx, backendUID, routes)` receives the backendUID as
`"<vnetUID>/<routeTableUUID>"`.  The handler constructs this composite key
after creating the DB row (which provides the routeTableUUID) and after
`CreateRouteTable` returns the vnetUID as BackendUID.  The separator `/` was
chosen because neither vnetUID nor route-table UUIDs contain `/`.

### Peering route next-hop

For VpcPeering-owned static routes, `nextHopIP` is set to `"0.0.0.0"`.
KubeOVN resolves the real next-hop internally via the `VpcPeering` object.
Setting `"0.0.0.0"` avoids routing loops while still letting the entry be
tagged for clean removal.  Confirmed working in spike gate #2.

### Subnet ACL tag format

NSG-owned ACL entries have their `name` field set to `"nsg-<nsgUUID>/<ruleName>"`.
Filtering uses `strings.HasPrefix(name, tag+"/")`.  Rule names must not contain
`/` — validated by the handler before reaching the driver.

### Tenant namespace creation

`CreateVNet` calls `ensureNamespace` before creating the VPC, so the first
network object created by a tenant automatically provisions the `dc-<tenantID>`
namespace.  This mirrors the harvester driver's behaviour for VMs.

## API surface summary (for `NetworkProvider` interface)

```go
type NetworkProvider interface {
    // VNet
    CreateVNet(ctx, tenantID, spec VNetSpec) (BackendUID, error)
    GetVNet(ctx, BackendUID) (VNet, error)
    DeleteVNet(ctx, BackendUID) error

    // Subnet
    CreateSubnet(ctx, vnetUID, spec SubnetSpec) (BackendUID, error)
    GetSubnet(ctx, BackendUID) (Subnet, error)
    DeleteSubnet(ctx, BackendUID) error

    // Route Table
    CreateRouteTable(ctx, vnetUID, spec RouteTableSpec) (BackendUID, error)
    DeleteRouteTable(ctx, BackendUID) error

    // NSG
    CreateNSG(ctx, spec NSGSpec) (BackendUID, error)
    AttachNSG(ctx, nsgUID, target NSGAttachment) error
    DetachNSG(ctx, nsgUID, target NSGAttachment) error

    // Peering
    CreatePeering(ctx, vnetA, vnetB BackendUID) (BackendUID, error)
    DeletePeering(ctx, BackendUID) error

    // NAT
    CreateNatGateway(ctx, vnetUID, spec NatGatewaySpec) (BackendUID, error)
    DeleteNatGateway(ctx, BackendUID) error

    // DNS
    CreatePrivateDnsZone(ctx, vnetUID, spec DnsZoneSpec) (BackendUID, error)
    DeletePrivateDnsZone(ctx, BackendUID) error
}
```

The driver implementing this interface is `internal/providers/kubeovn/`.
A future swap to Harvester's bundled KubeOVN (Model A) reuses this exact
interface; only the driver changes.
