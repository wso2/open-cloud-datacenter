## What is the problem we are trying to solve?

The WSO2 LK Datacenter was stood up recently. As the IaaS team, we are still defining how downstream teams should access and provision their resources. Our current approach — delivering infrastructure via Terraform modules that wrap the Harvester and Rancher providers directly (`open-cloud-datacenter`) — is a starting point, not an end goal.

The problem is that Terraform modules are a thin wrapper around provider APIs. They reduce repetition but do not raise the abstraction level. Downstream teams must still understand Harvester namespaces, Rancher project semantics, two separate credential lifecycles (one for VMs, one for Kubernetes clusters), and how to manage and rotate those credentials safely. The more teams onboard, the more that burden falls back on the IaaS team to set up and maintain access manually.

This is not a gap that better module design closes, and it is not a path we want to scale across multiple datacenters — with EU and US deployments planned, we need a model where teams can self-serve without becoming infrastructure experts.

## And why should it be solved now?

- **Cloud spend is growing while datacenter utilisation is underused.** The primary barrier to repatriation is developer experience, not capability. DC-API removes that barrier, making the cost differential actionable.

- **The Terraform module path has a ceiling.** It works for 5–10 teams. At 25+ teams, RBAC becomes manual per-tenant work, quota enforcement is impossible, and every new service type requires exposing another backend system. The longer we invest in that path, the more migration cost accumulates.

- **M1 is already built and working.** VM provisioning, RKE2 cluster creation, OIDC auth via Asgardeo, and quota enforcement are end-to-end tested against our LK dev environment today. This is not a greenfield proposal — it is a request for strategic commitment to a working foundation.

- **WSO2 products fit naturally here.** Asgardeo is the authentication layer for the entire platform. This is WSO2 dogfooding its own IAM product in a WSO2 Infrastructure Platform offering — a natural alignment and a source of real-world product feedback.

- **The window for managed services is open now.** Once DC-API owns the abstraction layer, we can expose PostgreSQL, Valkey, Harbor, DNS, and TLS as managed services with no additional learning curve for tenants. Waiting means teams build their own fragile self-hosted solutions instead.

## Who are we solving the problem for?

Internal WSO2 development teams who need compute, Kubernetes clusters, and eventually platform services (databases, registries, certificates) without becoming infrastructure experts. Secondary beneficiaries are the IaaS team — who currently spend significant time assisting teams through Terraform setup — and engineering leadership, who gain quota visibility, audit trails, and a repatriation lever against cloud spend.

## Solution

A **WSO2 Infrastructure Platform Control Plane**: a REST API (`DC-API`) that exposes our LK Datacenter as a self-service cloud, backed by Harvester and Rancher but hiding both entirely from tenants. The API is the single source of truth — everything else (CLI, Terraform provider, web UI) is a client on top of it.

#### [1. Developer Experience — Three Access Paths]{.mark}

[The REST API unlocks three ways for teams to interact with the datacenter, serving different workflows without any team needing to touch Harvester or Rancher.]{.mark}

**CLI (`dcctl`) — available now.** The immediate interface for engineers and CI pipelines:

```
dcctl create vm --name web-01 --size medium \
  --image default/ubuntu-24-04 --network default/vm-net-100
→ ACTIVE in ~3 min. SSH key returned on first create.

dcctl create cluster --name prod-k8s-01 --size large --nodes 3 \
  --image default/ubuntu-24-04 --network default/vm-net-100
→ ACTIVE in ~10 min. Kubeconfig retrievable immediately after.
```

**Terraform Provider (`terraform-provider-dcapi`) — near term.** Teams who already use Terraform for application-layer infrastructure can manage DC resources in the same workflow, without any knowledge of the underlying Harvester or Rancher providers. The provider calls DC-API — Harvester and Rancher are completely hidden.

```hcl
resource "dcapi_virtual_machine" "web" {
  name    = "web-01"
  size    = "medium"
  image   = "default/ubuntu-24-04"
  network = "default/vm-net-100"
}
```

**Web Portal — near term.** A self-service UI for teams who prefer point-and-click over a terminal. Resource listing, quota visibility, and one-click provisioning — all backed by the same DC-API. With AI-assisted tooling, this is achievable alongside the Terraform provider without significant additional investment.

#### 2. Identity — Asgardeo as the Single Auth Layer

All authentication flows through a single Asgardeo organisation. Tenant isolation is enforced via Asgardeo groups (`dc-tenant-<name>`) mapped to DC-API roles entirely in code we control — not in Rancher configuration. Enterprise tenants whose engineers need corporate SSO (Microsoft, Google) use Asgardeo's federation capability; DC-API always validates one trusted issuer.

#### 3. Quota and RBAC — Owned by DC-API, Not Rancher

Every provisioning request is checked against a per-tenant quota in PostgreSQL before any backend call. VM count, cluster count, CPU, and memory are all enforceable. The role model (owner / member / viewer, with service accounts for CI) is defined in our code, not in Rancher's configuration — meaning any role or permission the business needs can be added without touching the underlying platform.

#### [4. Managed Services — The Long-Term Differentiator]{.mark}

[Because DC-API owns the abstraction layer, any infrastructure component can be exposed as a managed service. The tenant makes one API call; the backend is our concern.]{.mark}

| Tenant calls | DC-API does internally |
|---|---|
| `POST /v1/databases` | CloudNativePG cluster on Harvester |
| `POST /v1/caches` | Valkey (Redis-compatible) via operator |
| `POST /v1/registries` | Harbor project + robot account |
| `POST /v1/load-balancers` | MetalLB IP allocation |
| `POST /v1/dns-records` | VyOS REST API |
| `POST /v1/certificates` | cert-manager + internal CA |

This is impossible via Terraform modules without the same abstraction layer.

#### 5. Roadmap

| Milestone | Scope | Target |
|---|---|---|
| **M1 — Compute & Clusters** ✅ | VM + RKE2 CRUD, quotas, Asgardeo auth, dcctl CLI | Q2 2026 — Done |
| **M1.5 — RBAC** | Owner/member/viewer roles, CI service accounts | Q2 2026 |
| **M2 — Storage & Networking** | Volumes, snapshots, load balancers | Q3 2026 |
| **M3 — Managed Services** | PostgreSQL, Valkey, Harbor, DNS, TLS | Q4 2026 |
| **M4 — Terraform Provider & Portal** | `terraform-provider-dcapi`, web UI, cost dashboard | Q1 2027 |

## Sample Workflow

The following flow shows how a team onboards and provisions a full environment without any Terraform or infrastructure knowledge.

```
1.  IaaS team adds engineer to Asgardeo group:  dc-tenant-teamalpha
    (one-time, ~2 minutes)

2.  Engineer on day one:
    dcctl login
    → Browser opens, Asgardeo login, token stored locally.

    dcctl create vm --name api-server --size medium \
      --image default/ubuntu-24-04 --network default/vm-net-100
    → VM ACTIVE in 3 min. SSH key auto-generated and saved.

    dcctl create cluster --name k8s-prod --size large --nodes 3 \
      --image default/ubuntu-24-04 --network default/vm-net-100
    → Cluster ACTIVE in ~12 min.

    dcctl kubeconfig k8s-prod --file ~/.kube/k8s-prod.yaml
    kubectl get nodes
    → Ready. No Rancher login ever touched.

3.  Future (M3):
    dcctl create database --name orders-db --engine postgres --size medium
    → Managed PostgreSQL. Connection string returned. No operator knowledge needed.
```
