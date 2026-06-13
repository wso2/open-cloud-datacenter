---
title: "Multi-Region: One Control Plane, Many Datacenters"
author: WSO2 IaaS Team
date: 2026-06-12
---

# 1. Document Purpose

This document is the in-repo design reference for taking DC-API from a single-site control plane to one that deploys and manages resources across many datacenters — the way a public cloud exposes regions. It consolidates the design discussed and agreed in [discussion #157](https://github.com/wso2/open-cloud-datacenter/discussions/157) (including the agent-scope addendum). For the current single-site architecture this design extends, see [`dc-api-architecture.md`](dc-api-architecture.md).

Status: design accepted; implementation phased (see §13). The `dc-agent/` module in this repo is the phase-2 transport skeleton — comms-only today.

---

# 2. Problem

DC-API today manages exactly one site: one Harvester cluster, one Rancher server, one KubeOVN fabric, configured by a single set of `DCAPI_*` env vars. A second datacenter is coming online, and the product goal is one control plane that deploys and manages resources across many datacenters without tenants ever caring where dc-api itself runs.

Two hard requirements shape the design:

1. **Symmetry.** The control plane is hosted *inside* one of the datacenters, but it must talk to its local region exactly the way it talks to every remote region. No in-cluster shortcuts, no privileged local path — relocating the control plane must be a redeploy, not a redesign.
2. **Zones from day one.** A region may eventually contain more than one Harvester cluster. The model is `region → zone`, where a zone is one Harvester (+ its Rancher); naming and schema assume this even while every region has exactly one zone.

---

# 3. Region → Zone Model

Regions and zones become first-class API objects:

- `GET /v1/regions` lists regions and their zones with health.
- Every *regional* resource (VNet, VM, cluster, bastion, volume) carries an immutable `region` (and eventually `zone`) attribute.
- Tenants and projects stay region-agnostic — only resources are placed.

A **zone** is one Harvester cluster plus its Rancher. A **region** is a datacenter containing one or more zones. The schema and naming assume multi-zone from the start so the model never needs a painful retrofit, even though every region ships with exactly one zone initially.

---

# 4. Containment Validation

Placement is validated by containment, not special-casing:

- A VM's region derives from its VNet; a cluster's from its VNet; a bastion's from its VNet.
- Creating a VM in region B against a VNet in region A is a `422` with a clear message — the parent resource's region is authoritative.
- Only **root resources** (VNets, key vaults) take region as a free choice at creation.
- VNet peering stays same-region until a cross-region fabric exists.

This keeps region handling out of individual handlers: resolve the parent, inherit its region, reject mismatches uniformly.

---

# 5. Provider Registry

The Strategy/Factory layer already isolates handlers from backends (see [`dc-api-architecture.md`](dc-api-architecture.md) §5). The singleton provider set becomes a registry keyed by region/zone:

```go
providers.For(region, zone)  // → the same Compute/Cluster/Network interfaces
```

- Handlers resolve the registry through the resource's region; nothing else about handler code changes.
- The reconciler runs **per-region loops**, so one region's outage cannot stall another region's reconciliation.
- The registry abstracts the transport: phase 1 may back it with direct connections over existing private connectivity; the agent transport (§6) slots in later *without API changes*.

---

# 6. Agent Transport: Outbound WSS on 443

Rather than routing the control plane into each datacenter's management network (site-to-site tunnels, O(n²) growth, inbound firewall holes), each region runs a small **dc-agent** that dials *out* to the control plane over WebSocket-over-TLS on 443 — internet-traversable, TLS end to end, nothing inbound to any datacenter. This is the same topology Rancher's cattle-agent, Azure Arc, and GitHub runners use, and the easier story to defend with security teams: each datacenter only ever opens an outbound HTTPS connection.

```
┌──────────────────────────────┐          ┌─────────────────────────────┐
│  Datacenter / Region A       │          │  Control plane (hosted in   │
│  ┌────────────┐              │   WSS    │  one of the regions)        │
│  │  dc-agent  │ ─────────────┼──443───► │  dc-api  /v1/agent/ws       │
│  └─────┬──────┘   dials OUT  │          │                             │
│        │ local creds only    │          │  PostgreSQL (no region      │
│  Harvester / Rancher / OVN   │          │  credentials stored)        │
└──────────────────────────────┘          └──────────────▲──────────────┘
┌──────────────────────────────┐                         │
│  Region B (local to the CP)  │   identical agent, same │
│  ┌────────────┐              │   endpoint — symmetry   │
│  │  dc-agent  │ ─────────────┼─────────────────────────┘
│  └────────────┘              │
└──────────────────────────────┘
```

Key properties:

- **Credentials stay regional.** The agent holds the region's credentials (Harvester kubeconfig, Rancher token) *locally* — they never leave the datacenter and never sit in the control-plane DB. This strengthens the existing "credentials never leave dc-api" principle into "credentials never leave their region".
- **Desired state flows down, status flows up.** The control plane sends desired-state operations down the established channel; the agent executes against its local Harvester/Rancher/KubeOVN and streams results back. dc-api's async 202-plus-reconciler model already absorbs the eventual consistency.
- **The local region runs the identical agent**, connecting to the control plane's service address like any other region — symmetry by construction.
- **Liveness is health.** Per-zone agents (or one agent per region managing its zones) keep the blast radius small; agent liveness doubles as the region/zone health signal surfaced in `GET /v1/regions` and the dashboard.

## 6.1 Region registration and bootstrap tokens

Admin-driven and API-managed:

1. `POST /v1/admin/regions` creates the region and mints a **one-time agent bootstrap token**.
2. The agent is deployed in the datacenter with that token and dials in.
3. The agent completes a token exchange and receives its long-lived identity (mTLS cert or rotating token).
4. Decommissioning a region = revoking the agent identity.

No region credentials are ever uploaded to the control plane.

---

# 7. Protocol v0

The implemented connection-lifecycle protocol between dc-agent and dc-api. JSON text frames over the WebSocket, discriminated by a `type` field; the JSON wire format is the compatibility contract between the two codebases (they share no Go package). The agent-side definitions live in `dc-agent/internal/protocol/`.

**Connection:** the agent dials `GET /v1/agent/ws` over TLS with header `Authorization: Bearer dcagent_<token>`.

| Direction | Frame | When |
|---|---|---|
| agent → server | `{"type":"hello","region":"…","zone":"…","version":"…"}` | First frame after connect |
| server → agent | `{"type":"hello_ack","agent_id":"<uuid>"}` | Reply to `hello` |
| agent → server | `{"type":"ping","ts":"<RFC3339>"}` | Every 30 seconds |
| server → agent | `{"type":"pong","ts":"<RFC3339>"}` | Reply to `ping` |

Liveness and reconnect semantics:

- The server enforces a **~120s read deadline**; the 30s ping cadence gives four chances per window before the server tears the connection down.
- On any disconnect, the agent reconnects with **exponential backoff + jitter (1s → 60s cap)**, forever.
- Receivers **tolerate unknown frame types** (log and ignore) so newer peers can introduce frames without breaking older ones.

## 7.1 Planned operation verbs (protocol v1)

Every provider operation the agent will execute is ultimately a Kubernetes CR manipulation (KubeVirt VMs, KubeOVN VPCs/Subnets, managed-service instance CRs), so the protocol's core verbs are **generic manifest primitives**, with typed provider operations riding on them:

| Verb | Semantics |
|---|---|
| `Apply(manifest)` | Server-side-apply a manifest against the agent's local cluster |
| `Delete(gvr, ns, name)` | Delete an object |
| `GetStatus(gvr, ns, name)` | Read an object's status once |
| `WatchStatus(gvr, ns, name)` | Stream status changes back up the channel |

These extend the `type` space of the v0 framing — the envelope and connection lifecycle do not change. This keeps the protocol stable as services are added: a new managed service is new CRs through the same four verbs, not new protocol.

---

# 8. Quotas and Placement

Tenant caps and project quotas become **per-region** — a region's capacity is physically its own, mirroring how public clouds scope quota by region. The existing hybrid quota model (dc-api-layer enforcement mirrored as Kubernetes `ResourceQuota`) applies per region.

Project presence in a region materializes **lazily**: the per-project namespace/quota mirror is provisioned in a region the first time the project deploys there, not eagerly in every region at project creation. Regions stay cheap to add; projects stay cheap to create.

---

# 9. Image Catalogs

Images are **per-region catalogs** — an image must exist on each region's Harvester to be usable there. Whether catalogs are centrally synced (a registry with per-region sync jobs) or per-region curated (with a naming convention) is an open question (§14).

---

# 10. DR and Data-Plane Independence

If the control plane's host datacenter is lost, **workloads in other regions keep running unmanaged** — the data plane does not depend on the control plane's availability. DR for the control plane is:

- a PostgreSQL replica/backup shipped to another region, plus
- a redeploy-from-GitOps runbook (the control plane is stateless apart from the DB; see [`flux/DEPLOY.md`](../flux/DEPLOY.md)).

The audit/activity framework and RBAC are region-agnostic already — events and grants reference resources, not sites.

---

# 11. Agent Scope: Manifest Primitives vs the Per-Service Operators

A scope question resolved in the discussion's addendum: since the agent is deployed onto each Harvester at bootstrap, could it also apply Kubernetes manifests on command — and eventually replace the separate per-service operators (key vault, database, …) running on every Harvester?

**1. Generic manifest primitives: yes, in protocol v1 — essentially free.** The agent must hold a local Kubernetes client regardless, because every provider operation it executes *is* a CR manipulation. So the core verbs are generic from day one (§7.1), and typed provider operations ride on them. This costs nothing extra and keeps the protocol stable as services are added.

**2. Operator delivery through the agent: yes, a near-term win.** Today each operator (and its CRDs) is delivered to each Harvester out-of-band. With manifest primitives, the control plane can ship and upgrade the operators *through the agent* at bootstrap — one delivery channel per datacenter, no per-operator installation plumbing, and operator versions become control-plane-managed facts.

**3. Replacing the operators with the agent: deferred, deliberately.** "Apply manifests" is not what the operators are for. The key-vault controller (~1,500 lines) watches CRs in a continuous reconcile loop with requeues, discovers the Raft leader pod among the vault replicas, drives the vault's own API (mounts, policies, AppRoles), handles unseal flows, and runs finalizer cleanup that connects *into* the service before letting CRs go. The database operator has the same shape per the managed-services framework contract. Replacing them means the agent would have to *host controller loops* (an embedded controller-runtime manager with per-service reconciler modules) — coherent as a future architecture ("one agent, many controllers" would collapse N operator Deployments per Harvester into one binary), but it couples agent releases to every service's controller code, unions their RBAC into one identity, and makes the agent the largest possible blast radius. That trade is not worth taking while the operator count is small.

**Revisit trigger for #3:** when the per-Harvester operator count or their upgrade orchestration becomes a real operational burden. The migration path is incremental — the agent already speaks the manifest/status/watch primitives, so individual operators can be absorbed as embedded reconciler modules one service at a time, without protocol changes.

---

# 12. What Does Not Change

- The public API hierarchy (`Tenant → Project → Resource`) and its independence from Rancher/Harvester concepts.
- The Strategy/Factory isolation of handlers from providers — the registry is a keying change, not a redesign.
- The async 202-plus-reconciler model — it already absorbs the eventual consistency the agent channel introduces.
- RBAC and audit — both reference resources, which now happen to carry a region attribute.

---

# 13. Phasing

| Phase | Scope |
|---|---|
| 0 — schema readiness | `region`/`zone` columns on regional tables (defaulted to the current site), read-only in API/UI. Cheap; protects against painful retrofits. |
| 1 — model + registry | Region/zone API objects, per-region provider registry, containment validation, region in create flows (CLI flag, UI selector), per-region reconcilers, per-region quotas. |
| 2 — agent transport | dc-agent (WSS dial-out, bootstrap-token registration, credential locality), control plane speaks to ALL regions — including local — through agents. Region health surfaced. |
| 3 — second region GA | Bootstrap the new datacenter via the agent, image catalog story, runbooks, dashboards. |
| later | Cross-region VNet peering, placement policies, DR automation, multi-zone scheduling within a region. |

The bias recorded in the discussion: ship multi-region *semantics* first behind the registry (phase 1, over existing private connectivity), so the agent slots in as a transport later without API changes.

---

# 14. Open Questions

1. **Agent protocol evolution:** the v0 hand-rolled WSS+JSON command stream is implemented; whether v1's request/response + watch semantics stay hand-rolled or move to gRPC streams remains open. Bias: smallest thing that works, versioned from day one.
2. **Agent granularity:** one agent per region managing N zones, or one per zone? (Per-zone keeps failure domains honest; per-region is less to operate.)
3. **Image catalogs:** central registry with per-region sync jobs, or per-region curation with a naming convention?
4. **Health depth:** how much of `GET /v1/regions` health is agent-liveness vs deeper probes (Harvester API reachability, capacity headroom)?

---

# 15. Where to Look Next

| For | See |
|---|---|
| The design's discussion history and feedback | [discussion #157](https://github.com/wso2/open-cloud-datacenter/discussions/157) |
| The agent implementation (protocol v0, comms-only) | [`dc-agent/README.md`](../dc-agent/README.md) |
| The single-site architecture this extends | [`dc-api-architecture.md`](dc-api-architecture.md) |
| The managed-services operator contract referenced in §11 | [`managed-services-framework.md`](managed-services-framework.md) |
