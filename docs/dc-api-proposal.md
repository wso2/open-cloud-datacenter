# WSO2 Infrastructure Platform Control Plane
## Strategic Proposal — April 2026

**Prepared by:** IaaS Team &nbsp;|&nbsp; **Audience:** Engineering Leadership, CTO

---

## Executive Summary

The WSO2 LK Datacenter has substantial physical compute capacity — hardware we already own and pay to operate. Most teams that could be running workloads here are instead paying AWS or Azure, because cloud providers offer something we currently don't: a simple, self-service developer experience.

This proposal requests alignment on a strategic direction: evolving from a **Terraform module vendor** into a **Cloud Provider**. The DC-API is the middleware layer that hides Rancher, Harvester, and any future backends behind a clean HTTP API — giving teams a `dcctl create vm` experience backed entirely by our sovereign on-premises infrastructure.

The alternative path — continuing to productise Terraform modules that expose Rancher and Harvester directly to downstream teams — has a hard ceiling. This document explains where that ceiling is, why it exists, and why DC-API is the only path that scales beyond it.

**Current status:** DC-API is built and end-to-end tested. VM provisioning, RKE2 cluster creation, OIDC authentication, and quota enforcement are all live against our LK dev environment.

---

## 1. The Opportunity: Our Datacenter as a Cloud

The reason teams choose AWS or Azure is not hardware — we have equivalent compute. The reason is **developer experience**:

- Self-service, with no ticket to the infra team
- A CLI that works in minutes (`aws ec2 run-instances`, `az vm create`)
- Quotas and costs that are visible and predictable
- Familiar patterns (REST API, kubeconfig, SSH keys on create)

DC-API closes every one of those gaps. Once `dcctl create vm` is as fast and simple as `aws ec2 run-instances`, teams have no technical reason to choose cloud for workloads that don't specifically require it.

**The repatriation economics:** Every workload moved off cloud reduces our monthly bill while increasing utilisation of hardware whose capital cost is already sunk. The marginal cost of an additional workload on hardware we already run is a fraction of cloud hourly rates — and that advantage compounds with every team that repatriates.

Secondary benefits: data sovereignty, in-country compliance, no egress charges, predictable pricing, lower latency between services.

---

## 2. The Current State and Its Limits

### 2.1 What Rancher and Harvester provide natively

Rancher and Harvester are capable platforms with meaningful built-in features, and it is important to be accurate about what they offer before explaining why they are insufficient as a *product surface* for downstream teams.

**RBAC:** Rancher has a three-tier role model — Cluster Owner/Member/Viewer at the cluster level, and Project Owner/Member/Read-Only at the project level. Custom roles can be created, combining Kubernetes RBAC verbs (get, create, delete) on specific resource types. A global admin can define a "VM-operator" role targeting `kubevirt.io/virtualmachines` resources specifically.

**Quotas:** Rancher Projects support resource quotas covering CPU, memory, pods, services, configmaps, and persistent volume claims. Harvester namespaces accept standard Kubernetes `ResourceQuota` objects for CPU, memory, and storage.

**Projects as tenant boundaries:** A Rancher Project maps to a set of Kubernetes namespaces and can serve as an isolation boundary between teams. At small scale (5–20 tenants), one project per tenant is a workable arrangement.

### 2.2 Where the direct approach breaks down

Despite the above, directly exposing Rancher and Harvester to downstream teams has hard constraints that cannot be worked around without essentially building DC-API yourself.

**RBAC flexibility stops at global admin.** Custom roles can only be created by Rancher global administrators — tenant teams cannot define sub-roles for their own members. More importantly, Rancher's permission model has no concept of "tenant-scoped role creation." An Azure-style model — where a Subscription owner defines custom roles for their own team — is not possible. Every new permission variant requires a global admin to intervene, which does not scale.

**Project quotas cover compute, not cloud resources.** Rancher's quota system covers Kubernetes primitives (pods, CPU, memory). It has no notion of "maximum 5 VMs per tenant," "maximum 2 RKE2 clusters," or "maximum 10 TB storage." Enforcing these requires either a custom admission controller (significant operational complexity) or an application layer — which is exactly what DC-API provides.

**Identity is Rancher-configured, not code-controlled.** Rancher has one global OIDC provider and manages group-to-role mapping inside its own configuration — not in code we own or can extend. Adding a new role tier, changing how groups map to permissions, or plugging in additional authentication logic all require Rancher configuration changes, not application-layer changes. There is no API surface for this.

**Project scale has a practical ceiling.** Community experience and our own environment confirm that Rancher's UI and API become noticeably slower beyond ~100 projects, due to etcd overhead and dashboard polling behaviour. At 50+ projects, UI operations that should be sub-second take 3–5 seconds. This is not a hard limit but it is a real operational constraint for a multi-tenant platform that expects to grow.

**Managed services are impossible without leaking internals.** If a team needs a managed PostgreSQL database, the only option via Terraform modules is to expose CloudNativePG or Percona operator internals to them. There is no clean abstraction boundary. Every new service type adds more Harvester/Rancher-specific knowledge that downstream teams must acquire.

**Backend migration breaks every caller.** If we move from Harvester to another hypervisor, every team's Terraform must change. The infrastructure details leak through every layer.

---

## 3. DC-API: The Control Plane Approach

DC-API is a REST API that sits in front of Harvester and Rancher and exposes a cloud-provider-style interface. Teams interact only with DC-API — they never touch Rancher or Harvester.

```
Developer / CI pipeline
        │
      dcctl
        │
        ▼
     DC-API ──────── PostgreSQL  (state · quotas · audit log)
        │
        ├──────────► Harvester   (VMs via KubeVirt CRDs)
        └──────────► Rancher     (RKE2 clusters via provisioning v2 API)
```

**What it looks like to a developer:**

```bash
dcctl create vm \
  --name web-01 --size medium \
  --image default/ubuntu-24-04 \
  --network default/vm-net-100
# → PENDING. Polls until ACTIVE.
# → IP: 192.168.10.42. Private key saved to ~/.ssh/web-01.pem.

ssh -i ~/.ssh/web-01.pem ubuntu@192.168.10.42

dcctl create cluster \
  --name prod-k8s-01 --size large --nodes 3 \
  --image default/ubuntu-24-04 --network default/vm-net-100
# → Waits 5-15 min, then ACTIVE.

dcctl kubeconfig prod-k8s-01 --file ~/.kube/prod-k8s-01.yaml
kubectl get nodes
```

No Terraform. No Harvester knowledge. No Rancher login. No infrastructure team involvement.

### Identity: Asgardeo as the authentication layer

All DC-API authentication is handled by **Asgardeo** — WSO2's own cloud IAM product. This is intentional and worth stating explicitly: we are dogfooding a WSO2 product in a WSO2-operated WSO2 Infrastructure Platform offering, which strengthens the internal case for Asgardeo and lets us surface real usage feedback directly to that team.

The design is a **single Asgardeo organisation for all tenants**. This is the right model — not a compromise. AWS doesn't let each customer bring their own identity plane; authentication is standardised at the platform level. Within that single org, tenant isolation is enforced via Asgardeo groups (`dc-tenant-teamalpha`), which DC-API's middleware maps to tenant boundaries and roles entirely in code we control.

If a tenant's engineers need to authenticate with their corporate Microsoft or Google accounts, Asgardeo's enterprise SSO federation handles that at the IdP layer. From DC-API's perspective, it always validates an Asgardeo-issued JWT — the upstream federation is invisible. This is exactly the capability Asgardeo is designed for.

---

## 4. Head-to-Head Comparison

| Dimension | Direct Terraform on Rancher/Harvester | DC-API |
|---|---|---|
| **Developer entry bar** | Must learn Terraform, Harvester, Rancher, RKE2 | `dcctl create vm` |
| **Custom RBAC** | Global admin only; tenants cannot define sub-roles | Any model we define |
| **VM/cluster count quotas** | Not natively supported; needs custom admission controller | Enforced in DB before every request |
| **Identity** | One global IdP for all of Rancher | Standardised on Asgardeo (WSO2 IAM); enterprise federation handled at IdP layer, not API layer |
| **Managed services** | Requires exposing operator internals | Clean API — tenant never sees the backend |
| **Tenant scale** | Degrades noticeably at 100+ Rancher projects | Scales to DB capacity |
| **Audit trail** | Rancher internal logs; not queryable by tenant | Every operation logged, queryable, exportable |
| **Backend migration** | Breaks every team's Terraform | Invisible — new provider, same API |
| **Availability zones** | Manual, per-team Terraform configuration | Abstracted — `--zone lk-a` selects the right cluster |

### On what DC-API reuses (not replaces)

DC-API does not rebuild Rancher or Harvester. Internally, we still use Rancher's provisioning API to create RKE2 clusters, and Harvester's KubeVirt CRDs to create VMs. The Rancher project quota system can serve as a *secondary enforcement layer* under DC-API — belt and suspenders for compute limits. What DC-API replaces is the **direct tenant-facing surface**, not the backend machinery.

---

## 5. What DC-API Unlocks That Direct Exposure Cannot

### 5.1 Managed Services — the long-term differentiator

Because DC-API owns the abstraction layer completely, we can expose any infrastructure component as a managed service, indistinguishable from what a cloud provider offers. The tenant makes an API call; what happens behind it is our concern.

| Tenant calls | DC-API does internally |
|---|---|
| `POST /v1/databases` | Deploys a PostgreSQL cluster via CloudNativePG operator on Harvester |
| `POST /v1/caches` | Deploys Valkey (Redis-compatible) via Helm operator |
| `POST /v1/registries` | Creates a Harbor project and robot account |
| `POST /v1/load-balancers` | Allocates a MetalLB IP from our pool |
| `POST /v1/dns-records` | Writes a record via VyOS REST API |
| `POST /v1/certificates` | Issues a certificate via cert-manager against our internal CA |

The tenant never knows whether PostgreSQL runs on Harvester, on dedicated database hardware, or in a future hybrid model — partially on-prem, partially delegated to a cloud-managed service for overflow. The API stays identical. This is how AWS RDS works: the caller does not know (or need to know) what's under the hood.

This is categorically impossible via Terraform modules without the same abstraction layer, because modules are thin wrappers — they always expose the backend API they wrap.

### 5.2 Custom RBAC at any granularity

We define the role model. Today it is `tenant-owner` / `tenant-member` / `viewer`. We can add any role the business requires — `billing-viewer` (can see cost data, cannot provision), `cluster-admin-only` (can manage clusters but not VMs), `ci-service-account` (read-only, non-human, scoped to one tenant), `auditor` (cross-tenant read, no write). None of these require touching Rancher. They are rows in our `tenant_roles` table and conditions in our middleware.

### 5.3 Availability Zones and Multi-Region

DC-API can abstract multiple Harvester clusters or datacenter locations as **zones** and **regions** — concepts familiar to any developer who has used AWS or Azure.

```bash
dcctl create vm --name db-replica --size large --zone lk-a
dcctl create vm --name db-primary --size large --zone lk-b
```

Behind the scenes, `lk-a` and `lk-b` map to different Harvester clusters or network segments. The tenant never configures this directly. This makes it straightforward to express concepts like:

- **Anti-affinity**: place the primary and replica in different zones
- **Disaster recovery**: replicate a cluster across two physical locations
- **Compliance**: route certain workloads to a specific facility

Multi-region (if we operate datacenters in more than one country) follows the same pattern — a `region` parameter on the resource, resolved by DC-API to the appropriate cluster. Rancher supports multiple downstream clusters, and DC-API's provider factory can route to the correct one based on region/zone metadata.

This is infrastructure we already have (multiple physical network segments, potential for multiple sites) — DC-API is what makes it explorable to tenants without infrastructure team involvement.

### 5.4 Cloud Repatriation at Scale

Direct Terraform modules require every team to manage their own state, understand provider quirks, and keep up with module updates. The operational overhead means teams default to cloud for new workloads even when on-prem would be cheaper. DC-API removes that overhead entirely — provisioning a VM is the same UX regardless of whether the physical hardware is in Sri Lanka or on AWS.

---

## 6. Architecture

**Core components:**

| Component | Role |
|---|---|
| **Auth middleware** | Validates Asgardeo JWT; maps groups to tenant and role |
| **Quota engine** | Checks resource limits in PostgreSQL before any provisioning |
| **Resource registry** | PostgreSQL — canonical state for all resources, independent of provider |
| **Provider interface** | Go interface satisfied by Harvester and Rancher drivers |
| **Reconciler** | Goroutine polling PENDING/DELETING resources; syncs actual state back to DB |
| **Audit log** | Immutable event stream per resource (who, what, when, status transitions) |

**Provider abstraction:** The Rancher and Harvester drivers are the only code that knows about those systems. Every other component — handlers, quotas, auth, audit — is provider-agnostic. Adding a new backend (OpenStack, Proxmox, a second datacenter) means one new Go file implementing the provider interface. Nothing else changes.

---

## 7. Service Roadmap

| Milestone | Theme | Scope | Target |
|---|---|---|---|
| **M1 — Foundation** ✅ | Compute + Clusters | VM and RKE2 cluster CRUD, OIDC, quotas, `dcctl` CLI | Q2 2026 — Done |
| **M1.5 — RBAC** | Access Control | Owner/member/viewer roles per tenant; service accounts for CI | Q2 2026 |
| **M2 — Storage & Networking** | Infrastructure | Volumes, snapshots, MetalLB load balancers, network listing | Q3 2026 |
| **M3 — Managed Services** | Platform | PostgreSQL, Valkey, Harbor registry, DNS records, TLS certificates | Q4 2026 |
| **M4 — Portal & GitOps** | Self-Service | Web UI, DC-API Terraform provider, cost dashboard | Q1 2027 |
| **M5 — Org Hierarchy** | Enterprise | Organization → Subscription → Resource Group (Azure-like), multi-region | Q2 2027 |

---

## 8. Success Metrics

| Metric | Baseline (Today) | Target (M1 Complete) |
|---|---|---|
| Time to provision a VM | ~25 min (clone repo, configure vars, run `terraform apply`) | < 3 min (`dcctl create vm`) |
| Infrastructure knowledge required | High (Terraform, Harvester, Rancher, RKE2) | None — CLI flags only |
| Time to onboard a new tenant | ~1 hour (Asgardeo group, Rancher project, TF creds) | < 10 min (Asgardeo group + quota row in DB) |
| Quota violations detectable | No — unenforceable | Yes — blocked at API before provisioning |
| Audit trail | None | 100% of operations logged with actor, timestamp, and diff |
| Backend migration effort | Full rewrite of all team Terraform | One new Go file implementing the provider interface |

---

## 9. Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| DC-API becomes a single point of failure | Medium | High | 2-replica Kubernetes Deployment; PostgreSQL with persistent volumes; existing Terraform modules as emergency fallback |
| Harvester or Rancher API changes break the provider driver | Low | Medium | Provider interface tests against a pinned version in CI; Kubernetes CRD API is stable across minor versions |
| State drift between DC-API DB and provider | Medium | Medium | Reconciler polls every 60s; planned Prometheus drift alerts |
| Asgardeo availability affects all API access | Low | High | JWKS public keys cached with 1-hour TTL; tokens remain valid until expiry if Asgardeo is unreachable |
| Team bandwidth — single-engineer delivery | High | Medium | Milestone-based: M1 alone delivers substantial value; CLI and API are independent codebases developed in parallel |

---

## 10. Ask

1. **Strategic alignment** — agreement that DC-API is the direction for all developer-facing datacenter access, and that Terraform-module-as-product is being wound down in favour of this approach.

2. **Dedicated team capacity** — focused IaaS team time to reach M3 (managed services) by end of 2026. M1 was delivered alongside other work; M2 and M3 need sustained focus.

3. **A pilot tenant** — one internal team onboarded to DC-API in Q2 2026 for real-world feedback ahead of wider rollout.

---

*DC-API source: `HiranAdikari/sovereign-cloud` — `dc-api/` (Go REST server) + `dcctl/` (CLI). Currently live against the LK dev Harvester + Rancher environment.*
