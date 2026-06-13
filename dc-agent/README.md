# dc-agent

The regional agent for the DC control plane. One `dc-agent` runs in each
region (one per zone, eventually) and **dials out** to the control plane over
WebSocket-over-TLS on 443 — nothing ever connects *into* a datacenter. This is
the same topology Rancher's cattle-agent, Azure Arc, and GitHub runners use:
each datacenter only needs to allow one outbound HTTPS connection.

Two properties fall out of this design:

- **Credential locality.** The region's infrastructure credentials (Harvester
  kubeconfig, Rancher token) stay inside the datacenter, held by the agent.
  They never sit in the control-plane database. The only credential that
  travels is the agent's own bearer token.
- **Symmetry.** The control plane's *local* region runs the identical agent,
  connecting to the same endpoint as every remote region. There is no
  privileged in-cluster path, so relocating the control plane is a redeploy,
  not a redesign.

## Scope today: comms only

This binary currently implements **protocol v0** — connection lifecycle only:

1. Dial `DCAGENT_ENDPOINT` with `Authorization: Bearer <token>`.
2. Send `hello` (region, zone, version); await `hello_ack` (agent ID).
3. Keepalive `ping` every 30s; the server answers `pong` and enforces a ~120s
   read deadline.
4. On any disconnect, reconnect forever with exponential backoff + jitter
   (1s → 60s cap).

Agent liveness doubles as the region/zone health signal surfaced by
`GET /v1/regions`. Executing desired-state operations against the local
Harvester/Rancher/KubeOVN (`Apply` / `Delete` / `GetStatus` / `WatchStatus`)
— and therefore any Kubernetes client dependency — is a later phase. See
[`docs/multi-region.md`](../docs/multi-region.md) for the full design and
[discussion #157](https://github.com/wso2/open-cloud-datacenter/discussions/157)
for its history.

## Configuration

All configuration comes from `DCAGENT_*` environment variables (12-factor,
same convention as dc-api's `DCAPI_*`). The agent fails fast at startup,
reporting every problem at once.

| Variable | Required | Default | Description |
|---|---|---|---|
| `DCAGENT_ENDPOINT` | yes | — | Control-plane agent-channel URL, e.g. `wss://connect.<domain>/v1/agent/ws`. This is the dedicated **connect** host (not the human/API host), which must not sit behind a bot/JS challenge. Scheme must be `wss` (`ws` allowed for local dev). |
| `DCAGENT_TOKEN` | yes | — | Agent bearer token minted by the control plane — `dcctl admin agent-token create --region <r> --zone <z>` (or the cloud-ui admin page). Must start with `dcagent_`. |
| `DCAGENT_REGION` | yes | — | Region this agent serves, e.g. `lk`. Sent in the `hello` frame. |
| `DCAGENT_ZONE` | yes | — | Zone within the region, e.g. `zone-1`. Required even while regions have one zone — the region → zone model is first-class from day one. |
| `DCAGENT_LOG_LEVEL` | no | `info` | `trace`, `debug`, `info`, `warn`, or `error`. |

## Running locally against a dev control plane

With a dc-api dev instance listening on `localhost:8080` (see
[`docs/local-dev.md`](../docs/local-dev.md)) and an agent token in hand:

```bash
cd dc-agent
go build -o dc-agent .

DCAGENT_ENDPOINT=ws://localhost:8080/v1/agent/ws \
DCAGENT_TOKEN=dcagent_<your-dev-token> \
DCAGENT_REGION=lk \
DCAGENT_ZONE=zone-1 \
DCAGENT_LOG_LEVEL=debug \
./dc-agent
```

At `debug` level you'll see the `hello`/`hello_ack` handshake, each `ping`
sent, and each `pong` received. Stop with Ctrl-C — the agent closes the
WebSocket cleanly on SIGINT/SIGTERM.

Run the tests:

```bash
go test ./...
```

Build the container image:

```bash
docker build --build-arg VERSION=$(git describe --tags --always) -t dc-agent:dev .
```

## Module layout

```
dc-agent/                      Separate Go module: github.com/wso2/dc-agent
├── main.go                    Config (DCAGENT_*), logging, signal handling
├── internal/conn/             Dial → hello → ping loop; reconnect w/ backoff+jitter
├── internal/protocol/         Protocol v0 frames — the wire contract with dc-api
└── Dockerfile                 Multi-stage → distroless/static:nonroot
```

Like `crds/keyvault`, this is a standalone module within the monorepo: the
agent ships to datacenters independently of dc-api and must keep its
dependency tree minimal (currently just `coder/websocket` + `zerolog`).

## Roadmap

| Step | What |
|---|---|
| now | Channel + liveness (this binary) |
| next | Bootstrap-token exchange for a long-lived identity; protocol v1 manifest primitives (`Apply`, `Delete`, `GetStatus`, `WatchStatus`) with a local Kubernetes client |
| later | Operator delivery through the agent; per-zone agents; see the phasing table in [`docs/multi-region.md`](../docs/multi-region.md) |
