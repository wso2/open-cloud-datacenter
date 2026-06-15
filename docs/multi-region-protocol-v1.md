---
title: "Agent Protocol v1 — The Command Channel (M-A)"
author: WSO2 IaaS Team
date: 2026-06-15
---

# 1. Purpose & Scope

Protocol v0 (shipped in #160) gives the control plane a *presence* channel: an
agent dials out over WSS, says `hello`, and heartbeats with `ping`/`pong`. The
control plane learns a zone is reachable and healthy — and nothing more.

This document specifies **protocol v1 (milestone M-A)**: the request/response
layer that lets dc-api ask a connected agent to *do work* in its zone over the
same single WebSocket. It is **dark and non-breaking** — v0 frames are unchanged,
no provider is moved off the direct Harvester/Rancher path yet, and an old agent
keeps working against a new server (and vice versa). M-A delivers one read-only
verb end to end (`get_inventory`); the mutating verbs (`apply`/`delete`) reuse the
exact same machinery and land in M-B.

This branch (`feat/agent-protocol-v1`) is based on the merged `controlplane`
foundation and is independent of the open PRs #161/#267.

See [`multi-region.md`](multi-region.md) for the region→zone model and the v0
transport this extends, and [discussion #157](https://github.com/wso2/open-cloud-datacenter/discussions/157)
for the milestone roadmap.

---

# 2. Design Constraints

1. **Backward compatible.** v0 (`hello`/`hello_ack`/`ping`/`pong`) is untouched.
   Both ends already `Decode` unknown `type`s to an `Unknown` frame and ignore
   them, so adding frame types can never break an older peer.
2. **One connection, multiplexed.** A zone has exactly one agent and one socket.
   Multiple requests may be in flight concurrently; responses can return out of
   order. Correlation is by an `id` on every request/response.
3. **Server-initiated in M-A.** dc-api is the requester; the agent executes
   locally and replies. The envelope is symmetric (either side *could* send a
   `req`), but agent→server requests are out of scope until there's a need.
4. **Single writer per side.** `coder/websocket` writes are not concurrency-safe.
   Each side serializes writes (a write mutex or a single writer goroutine), so a
   ping and a response never interleave on the wire.
5. **The credential is authoritative.** As in v0, the agent token's `(region,
   zone)` — not anything in a frame — decides which zone a session serves.

---

# 3. Frame Additions

All frames stay JSON text messages discriminated by `type`. v1 adds three:

```jsonc
// request  (dc-api → agent)
{ "type": "req", "id": "<uuid>", "op": "get_inventory", "params": { } }

// terminal response (agent → dc-api) — ok
{ "type": "res", "id": "<uuid>", "ok": true, "result": { } }

// terminal response — error
{ "type": "res", "id": "<uuid>", "ok": false,
  "error": { "code": "EXEC_ERROR", "message": "…" } }

// optional progress (agent → dc-api), zero or more before the terminal res
{ "type": "progress", "id": "<uuid>", "stage": "applying", "detail": "…" }
```

- `id` — correlation id (UUID) minted by the requester, echoed on every
  `progress` and the terminal `res`.
- `op` — the operation name. Adding a verb is a new `op` string plus a handler,
  never new envelope or routing code.
- `params` / `result` — op-specific JSON objects (see §6).
- Exactly **one** terminal `res` ends a request; `progress` frames are advisory
  and may be dropped by a receiver that doesn't care.

**Why a generic `req`/`res` envelope rather than a distinct `type` per verb**
(which the v0 doc-comment hinted at): one correlation mechanism and one dispatch
path serve every verb. The `Decode` switch stays at six arms forever; the verb
set grows in a registry, not in the wire grammar.

**Version negotiation.** v1 extends `hello` with the ops the agent supports:

```jsonc
{ "type": "hello", "region": "…", "zone": "…", "version": "…",
  "ops": ["get_inventory"] }
```

The field is optional and absent on a v0 agent (→ treated as "no v1 ops"). The
server checks `ops` before issuing a `req`, so a verb an agent can't do returns a
clean `OP_UNSUPPORTED` error instead of a timeout.

---

# 4. Server-Side RPC Machinery (dc-api)

```
HTTP handler ──Call(zone, op, params)──▶ SessionRegistry ──▶ Session ──write req──▶ socket
                                                                │
serve() read loop ──res/progress──▶ route by id ──▶ pending[id] channel ──▶ Call returns
```

- **`Session`** (one per connected agent): owns the `*websocket.Conn`, a write
  mutex, and `pending map[uuid]chan *Response`. Methods:
  - `Call(ctx, op, params) (result json.RawMessage, err error)` — mint an `id`,
    register a channel, write the `req` under the write lock, then wait for the
    terminal `res` or `ctx` deadline; always clean up `pending[id]`.
  - `writeFrame(...)` — existing helper, now guarded by the write mutex.
- **`SessionRegistry`**: `region/zone → *Session`, registered when the handshake
  completes and removed on disconnect. HTTP handlers resolve a zone's session
  here; a missing entry is `AGENT_UNAVAILABLE`.
- **`serve()` read loop** gains routing: a `res`/`progress` frame is dispatched
  to `pending[id]`; `ping` still pongs; `hello`/unknown unchanged. Liveness
  (`TouchAgent`) still bumps on every inbound frame.

The session loop stays the *only* reader of the socket — correlation removes the
need for any other goroutine to read.

---

# 5. Agent-Side Dispatch (dc-agent)

- The read loop in `internal/conn` gains a `req` branch: decode, look the `op` up
  in an **executor registry**, run the handler in a goroutine (so a slow op never
  blocks pings or other requests), and write the `res` (and any `progress`) under
  the agent's write mutex.
- **`Executor` interface** — the local-cluster boundary:

  ```go
  type Executor interface {
      GetInventory(ctx context.Context) (Inventory, error)
      // M-B: Apply(ctx, manifest) (...) ; Delete(ctx, ref) (...)
  }
  ```

  The executor holds the zone-local Kubernetes client (in-cluster ServiceAccount
  in production; a kubeconfig for local dev). This is where "dc-api stops holding
  the cluster credential" becomes literally true — the credential lives here, in
  the zone, and never travels.

---

# 6. First Slice — `get_inventory`

A read-only verb proves the whole channel before anything mutates a cluster.

```jsonc
// req params: {}  (room later for {"include": ["nodes","vms"]})
// res result:
{
  "nodes": [
    { "name": "…", "ready": true,
      "cpu_allocatable_m": 64000, "cpu_used_m": 18000,
      "mem_allocatable_mb": 257000, "mem_used_mb": 96000 }
  ],
  "vm_count": 12
}
```

- **Agent**: the executor reads nodes from the local Kubernetes API and counts
  KubeVirt `VirtualMachine`s. No mutation, no provider coupling.
- **dc-api**: `GET /v1/regions/{region}/zones/{zone}/inventory` →
  `registry.Call("get_inventory")` → returns the result (or maps the error per
  §7). **Admin-only** — node and capacity figures are Harvester-internal and
  must never reach a tenant (see §11). Kept off `GET /v1/regions` so listing
  never blocks on a live RPC.
- **cloud-ui**: the *admin* region card upgrades from "up" to "up · 3 nodes ·
  48 vCPU free" when inventory is available; falls back to plain health when it
  isn't. Tenants never see this card — §11 covers what they do see.

---

# 7. Error Model

Errors are structured `{ code, message }`. dc-api maps them to HTTP:

| code               | meaning                                   | class     | HTTP |
|--------------------|-------------------------------------------|-----------|------|
| `AGENT_UNAVAILABLE`| no live session for the zone              | transient | 503  |
| `TIMEOUT`          | no `res` before the deadline              | transient | 504  |
| `OP_UNSUPPORTED`   | agent didn't advertise the op (`hello.ops`)| terminal | 501  |
| `BAD_REQUEST`      | malformed params                          | terminal  | 400  |
| `EXEC_ERROR`       | the executor failed (detail in `message`) | terminal  | 502  |

Transient errors are safe for the caller to retry; terminal ones aren't. Retry
ownership sits with the dc-api caller, not the channel.

---

# 8. Idempotency & Delivery (forward note for M-B)

`get_inventory` is read-only, so M-A needs none of this. When `apply`/`delete`
land: each request carries an op id the agent dedupes within a bounded window
(at-least-once delivery, exactly-once effect), and `apply` is intrinsically
idempotent via server-side apply (declared state, not deltas). Captured here so
the envelope (`id` already present) doesn't need to change for it.

---

# 9. Open Questions (resolved as M-B is built, not now)

- **`WatchStatus` vs. polling.** `progress` frames cover in-flight streaming; for
  steady-state resource status, decide between a long-lived `watch` op and dc-api
  polling `get_status`. Defer until `apply` exists to watch.
- **Intent authorization.** Does the agent blindly trust dc-api, or enforce a
  capability allowlist on `op` + manifest kind it will apply? Leaning allowlist
  (M-B), so a compromised control plane can't make an agent apply anything.
- **Version skew beyond ops.** `hello.ops` handles "agent too old for a verb";
  revisit if a verb's *params* ever change shape.

---

# 10. Rollout (M-A implementation order)

1. **Protocol package** — add `req`/`res`/`progress` + `ops` on `hello`;
   `Encode`/`Decode` both sides; round-trip + unknown-tolerance tests. *(this PR)*
2. **Server machinery** — `Session` + `SessionRegistry` + `Call`; route
   `res`/`progress` in `serve()`; in-process WebSocket tests.
3. **Agent dispatch** — executor registry + read-loop `req` branch; a real
   `GetInventory` against the local kube client; tests.
4. **dc-api endpoint** — `GET …/zones/{zone}/inventory` (admin-only) +
   `openapi.yaml` + integration test.
5. **cloud-ui** — surface inventory on the *admin* region card.
6. **Dark PR off `controlplane`.**

(The tenant-facing placement filter in §11 is a separate, smaller change that
rides the existing `GET /v1/regions` status — it does not depend on the command
channel — and is sequenced with M-C, not M-A.)

---

# 11. Visibility & Placement (admin vs. tenant)

Regions surface very differently to the two audiences, per the core principle
that Harvester/Rancher internals never reach a tenant.

**Admin** sees the full picture — every region/zone, live health, and (via
`get_inventory`) node counts and capacity. This is the operator dashboard: the
rich region card. The inventory endpoint is admin-gated.

**Tenants** never see capacity, node counts, agent versions, or a "regions
dashboard". To a tenant a region is only a *placement target*:

- Every create flow (VNet, VM, cluster, bastion) lists only **placeable**
  regions in its selector; a non-placeable region is filtered out or shown
  disabled as "unavailable", so a tenant can't target a region that can't serve
  the request.
- Placeable is `up`, and only `up`: a `down` region (agent was connected, now
  stale) and an `unknown` region (no agent yet) are both non-placeable.
- Enforced **server-side**, not just in the UI: the create handlers reject
  placement into a non-placeable region (409/422). UI filtering is UX; the
  handler guard is the actual rule, so the API and CLI honour it too.
- Existing resources in a region that goes down are **never** hidden or deleted.
  They stay listed and report their own degraded/down status (operations against
  them fail while the region is down). No special handling — this falls out of
  per-resource status.

**Sequencing caveat — don't gate on live agent status prematurely.** Today
provisioning still runs direct-to-Harvester; the agent is not yet in the
provisioning path, so a region's live `status` means "an agent is connected", not
"this region can provision". Gating placement on agent-`up` *now* would block
provisioning that currently works (in production no agent runs yet, so regions
read `unknown`). Placeability therefore separates two signals:

- **Enabled** — an admin "this region is open for business" flag (near-term
  placeability, independent of the agent), and
- **Healthy path** — the provisioning path is up (today: always, via the direct
  driver; post-M-C: the agent is `up`).

`placeable = enabled AND healthy-path`. The live-agent gate switches on with M-C,
when the agent becomes the provisioning path; until then placeability rides the
admin enabled flag, so nothing that works today breaks.
