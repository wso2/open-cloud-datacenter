# SUSE Support Scope: KubeOVN on Harvester

## Context & Determination

We evaluated the support status of running upstream KubeOVN v1.15 as an additive secondary Multus CNI on a Harvester cluster to provide tenant-isolated overlay networking. On 2026-06-18, following our support engagement on this exact integration pattern, SUSE published a public Knowledge Base article, **"Guidelines, Limitations, and Support Scope for Installing Custom Applications and Controllers on Harvester"** — [support.scc.suse.com KB](https://support.scc.suse.com/s/kb/Guidelines-Limitations-and-Support-Scope-for-Installing-Custom-Applications-and-Controllers-on-Harvester?language=en_US).

SUSE explicitly confirmed our architecture is a **supported customization** — not outside support scope. Our usage fits the "Standard Extension Model" described in their KB with a small number of documented deviations driven entirely by upstream KubeOVN and Kubernetes design constraints, not by our implementation choices.

**Key implementation facts:**
- dc-api creates standard `kubeovn.io` CRDs (Vpc, Subnet, ProviderNetwork, Vlan, VpcNatGateway, IptablesEIP, IptablesSnatRule) to model tenant VPCs, subnets, and NAT.
- VMs attach via standard KubeVirt `spec.template.spec.networks` + Multus NetworkAttachmentDefinition annotations.
- Harvester's bundled KubeOVN/VPC addon remains disabled to prevent controller fights over the same CRDs.
- All dc-api-created KubeOVN resources carry the label `dc-api/managed: "true"` for lifecycle isolation and RCA identification.

---

## Adherence Matrix: SUSE's Six Guidelines

The Standard Extension Model defines six design requirements for custom controllers/CNIs. Our status against each.

**How we classify a non-PASS.** A guideline is marked anything other than PASS *only* when the gap is dictated by upstream KubeOVN or Kubernetes **design** and is therefore outside our control to change. We do **not** record a reversible choice of ours as a "deviation" — anything we are technically able to bring into compliance is brought into compliance and marked PASS. Every non-PASS below states explicitly whether closing it is **within our control**; if it is, it is logged as a tracked remediation, never as a permanent exemption.

| Guideline | SUSE Intent | Our Status | Evidence |
|-----------|-----------|-----------|----------|
| **(a) Custom resources should be namespace-scoped** to avoid cluster-wide configuration pollution and respect Kubernetes admission policy intent. | Prevent custom resources from polluting cluster-wide configuration, and their CRDs from colliding with future GA features via name collisions. | **DEVIATION — by design, not within our control.** | KubeOVN's Vpc, Subnet, ProviderNetwork, Vlan, VpcNatGateway, IptablesEIP, IptablesSnatRule are cluster-scoped custom resources in upstream KubeOVN; there is **no namespaced variant** to switch to. Not a choice of ours. We mitigate with ownership labels. See deviation #1. |
| **(b) Custom controllers must run independently** and not interfere with native Harvester controllers. | Isolation: no shared state, no competing watches, no patches to system controllers. | **PASS** — kube-ovn-controller runs standalone in kube-system as a separate Deployment. | Single hard requirement: Harvester's bundled KubeOVN/VPC addon must be disabled in the Harvester configuration before our overlay is installed. Two controllers reconciling the same `kubeovn.io` CRDs would cause churn and state corruption. |
| **(c) Integrate only via public, stable, documented Kubernetes & Harvester APIs.** | No private endpoints, no undocumented hooks, no monkey-patching of shared objects. | **PASS** — we use only upstream-blessed, documented APIs. | KubeVirt `VirtualMachine` spec; Multus `NetworkAttachmentDefinition`; upstream `kubeovn.io` API group; Kubernetes `Subnet`, `Deployment`, `ConfigMap`, `ServiceAccount`, `Pod` APIs. All documented in upstream specs. |
| **(d) Manage VM lifecycle through standard Kubernetes/Harvester APIs** (no hypervisor-level scripting). | VMs declared in Kubernetes; no direct QEMU/libvirt/host escapes. | **PASS** — dc-api creates standard `VirtualMachine` CRs; KubeVirt schedules virt-launcher pods; Harvester reconciles them to QEMU. | `internal/providers/harvester/client.go::CreateVM` builds a KubeVirt `VirtualMachine` object with `spec.template.spec.networks` listing Multus attachments. No hypervisor bypass. |
| **(e) No modifications to Harvester core** (system containers, bundled controllers, shipped CRDs, Helm chart values, webhooks). | Harvester's integrity: upgrades stay clean, SUSE can RCA without discovering operator-patched artifacts. | **PASS (strongest)** — we ship zero patches to Harvester system components. | We READ Harvester-managed labels (e.g., `network.harvesterhci.io/ready` on NADs) and wait for them; we do not MODIFY them or any harvester-system/cattle-system resource. |
| **(f) No privileged host-level changes** (SLE Micro OS kernel, sysctl, host networking, mount mods). | CNI inherently manages per-node networking, but must not add host-level tuning beyond standard CNI setup. | **PARTIAL — by design, not within our control.** | A CNI must program host networking by definition; KubeOVN's per-node DaemonSets manage OVS bridges and CNI state. The residual is the **irreducible upstream baseline** — we pin to the same version Harvester bundles and add **zero** extra host/sysctl/kernel tuning beyond the upstream Helm chart defaults, so our host-level behavior is identical in kind to what SUSE already validates and ships. There is no host-level change of ours to remove. |

---

## Documented Deviations

Each item states whether closing the gap is **within our control**. Items #1 and #2 are dictated by upstream KubeOVN / Kubernetes design and cannot be changed by us — they are permanent, by-design deviations. Item #3 has one part that is by-design and one part that is a reversible choice of ours, so that part is logged as a **tracked remediation**, not an exemption.

### 1. Cluster-Scoped kubeovn.io Custom Resources

**Within our control? No — by upstream design.** Upstream KubeOVN ships no namespaced variant of these resources; we cannot change their scope.

**What:** Vpc, Subnet, ProviderNetwork, Vlan, VpcNatGateway, IptablesEIP, IptablesSnatRule are cluster-scoped in upstream KubeOVN; there is no namespaced variant. They live under the `kubeovn.io` API group.

**Why it's unavoidable:** This is upstream KubeOVN's architectural decision. A Vpc and its subnets form a logical, cluster-wide routing domain; scoping them to a namespace would fragment the data model in ways OVN's control plane doesn't support.

**How we mitigate:**
- Every dc-api-created object of these types carries the label `dc-api/managed: "true"` (plus `dc-api/vpc-name: "<vpc-uid>"` where relevant).
- A single label selector (`dc-api/managed=true`) identifies all dc-api-owned KubeOVN resources for isolation, deletion, or RCA.
- See `dc-api/internal/providers/kubeovn/nat.go` (lines ~165–167, ~196–198, ~215–217) and `dns.go` for label application on Create operations.

**Support implication:** SUSE understands this is upstream-design-driven. The labels provide RCA isolation: if KubeOVN's controller experiences a bug, SUSE can identify which objects are ours vs. system-generated.

---

### 2. vpc-nat-gw Workloads in kube-system — KubeOVN's Decision, Not Ours

**Within our control? No — by upstream design.** KubeOVN's controller decides where these workloads land, the resource names are hardcoded inside kube-ovn-controller, and Harvester's network webhook enforces the same constraints. We cannot relocate or rename them.

**What:** When dc-api creates a `VpcNatGateway` CR, KubeOVN's controller automatically creates a corresponding StatefulSet named `vpc-nat-gw-<name>` and pods in the `kube-system` namespace. These are NOT created by dc-api directly.

**Why it's unavoidable:**
- The external subnet name `ovn-vpc-external-network` is hardcoded inside kube-ovn-controller's VPC NAT gateway reconciler — we have no control over the name.
- KubeOVN's reconciler watches `VpcNatGateway` CRs cluster-wide and creates the StatefulSet in `kube-system` by default (upstream convention for cluster-critical infrastructure).
- Harvester's network webhook enforces that the external subnet uses a 3-dot provider format (`<name>.<namespace>.ovn`) and validates that a matching NetworkAttachmentDefinition exists — the bootstrap has to be in `kube-system` to satisfy these hard-coded checks.

**How we mitigate:**
- dc-api still owns the `VpcNatGateway` CR itself (which carries `dc-api/managed: "true"` label).
- Deleting the VPC causes the VpcNatGateway CR to be deleted, which triggers KubeOVN's controller to tear down the StatefulSet and pods.
- The NAT bootstrap resources (ProviderNetwork, Vlan, external Subnet, external NAD) that dc-api does create directly all carry `dc-api/managed: "true"` labels.

**Evidence:** `dc-api/internal/providers/kubeovn/nat.go` lines ~176 (VpcNatGateway Create) and ~109–126, ~419–511 (NAT bootstrap). See comments at lines ~413–421 for the hardcoded-name gotcha.

---

### 3. per-VPC CoreDNS — One By-Design Constraint, One Reversible Choice

This item has two parts; they must not be conflated.

**Part A — owning our own CoreDNS instead of KubeOVN's `VpcDns` CRD.**
**Within our control? No — by design.** Harvester's kube-ovn-controller runs with `--enable-lb=false` (confirmed in `dns.go` header, gotcha #3), which makes the `kubeovn.io` `VpcDns` CRD structurally unavailable — creating one fails. We therefore run a small forward-only CoreDNS Deployment per VPC (Approach C.2). This is forced by how KubeOVN is configured on Harvester, not a preference.

**Part B — the CoreDNS Deployment currently lives in `kube-system`.**
**Within our control? Yes — reversible choice, tracked for remediation.** The *only* reason it sits in `kube-system` is that it reuses the built-in `system-cluster-critical` PriorityClass, which Kubernetes confines to the `kube-system` namespace by admission policy. That is a convenience, not a necessity: we can define our own high-value PriorityClass (e.g. `dc-vpc-critical`) and run the pod in a dedicated dc-owned namespace (e.g. `dc-system`), retaining near-equivalent eviction protection while keeping our workload out of Harvester's system namespace. Doing so brings this fully inside SUSE's "isolate in dedicated namespaces" intent. **This is the one gap in this document that is ours to close**, so it is logged as a remediation — not claimed as an unavoidable deviation. Until it is done, every object still carries `dc-api/managed: "true"` plus `app: vpc-dns, vpc: <vpc-uid>` labels for unambiguous RCA isolation.

**Evidence:** `dc-api/internal/providers/kubeovn/dns.go` — `dnsBootstrapNS = "kube-system"` const (~lines 57–61); `EnsureVpcDNSBootstrap` (~108–134); `priorityClassName: system-cluster-critical` applied at Deployment build time (~line 290).

---

## Operational Commitments

We honor SUSE's three operational cautions (support-boundary isolation, upgrade compatibility, control-plane overhead) through the following discipline:

### Version Parity
- KubeOVN is version-pinned to the release bundled in Harvester (currently v1.15.4).
- On every Harvester release, re-pin our KubeOVN chart to the new bundled version and re-validate against a staging cluster that mirrors the prod topology.
- Add zero extra host tuning beyond the upstream Helm chart defaults.

### Staged Upgrades
- Every Harvester upgrade is validated on a staging cluster **before** rolling to production.
- The staging cluster is seeded with the same KubeOVN overlay, tenant VMs, and VPC topology as production.
- Re-validate the SLE Micro + RKE2 + KubeVirt + KubeOVN matrix before declaring the upgrade safe.

### Control-Plane Monitoring
- **Clarification:** KubeOVN's OVN logical topology (flows, routes) lives in the OVN northbound/southbound OVSDB (Raft-clustered in the ovn-central container), **not** in Kubernetes etcd.
- The real control-plane load on Harvester's etcd comes from Kubernetes-facing CRD churn: chiefly `ips.kubeovn.io` objects (one per pod/VM IP) and `subnet` status reconciles.
- Load scales with VM/IP count and reconcile frequency, not OVN topology complexity.
- **Baseline and alert on:**
  - etcd database size, fsync/commit latency, write rate
  - Kubernetes API server request latency and in-flight request count
  - kube-ovn-controller reconcile loop latency (the controller's worker-count / resync-interval flags can be tuned down if churn becomes a problem; confirm exact flag names against the pinned KubeOVN version before changing).

### RCA Quiesce
- Maintain a runbook to gracefully scale-down or pause the upstream kube-ovn-controller Deployment and our per-VPC CoreDNS Deployments.
- **Importantly:** pausing the controller does NOT delete tenant VPC state from the OVN databases; tenant network packets continue to forward through existing OVN flows.
- This lets SUSE's RCA run without destroying tenant connectivity, and allows us to comply with an isolation request fast (e.g., "disable this layer while we diagnose the cluster").

### Native-VPC GA Collision Plan
- **Forward risk:** Harvester's native VPC feature is also KubeOVN and uses the same `kubeovn.io` CRD group.
- If native VPC reaches GA and becomes non-optional, CRD name collision / competing controllers becomes a literal risk, not hypothetical.
- **Mitigation:** Our `NetworkProvider` interface (Strategy Pattern) allows swapping to the bundled VPC driver as a pure implementation change with no public-API changes.
- **Action:** Monitor Harvester release notes. If native VPC moves toward GA or becomes mandatory, file a task to swap the KubeOVN provider to the bundled one and validate the change on staging.
- Until then, keep the bundled addon disabled.

---

## Bottom Line

We are a **supported customization** under SUSE's Standard Extension Model. Two deviations are genuinely by-design and outside our control — cluster-scoped `kubeovn.io` custom resources (#1) and the irreducible host-networking footprint of any CNI (#f). The KubeOVN-controller-owned `kube-system` workloads (#2) are likewise not ours to move. Exactly **one** gap is ours to close: relocating the per-VPC CoreDNS Deployment out of `kube-system` into a dc-owned namespace with a custom PriorityClass (#3, Part B) — tracked as a remediation, not excused as a deviation. Everything we touch is labeled `dc-api/managed: "true"` for RCA isolation. Continue with discipline on version parity, staged upgrades, control-plane monitoring, and forward-watching native VPC GA. The architecture is sound and defensible in a support conversation.
