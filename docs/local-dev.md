# Local Development Setup

A step-by-step walk-through for running `dc-api` and `dcctl` on your laptop,
pointed at an existing Harvester + Rancher dev cluster.

Audience: anyone who needs a working `dc-api` to develop against —
contributors hacking on dc-api itself, frontend devs working on cloud-ui,
people writing client libraries (e.g. a future Terraform provider).

---

## 0. The shape of the system

```
              your laptop                              dev cluster
  ┌──────────────────────────────────┐        ┌──────────────────────────┐
  │                                  │        │                          │
  │   docker compose                 │        │   Harvester              │
  │   └─ dc-postgres :5432           │        │   ├─ KubeVirt VMs        │
  │                                  │        │   ├─ KubeOVN CRDs ◀──────┼── DCAPI_HARVESTER_KUBECONFIG
  │   go run ./dc-api  :8080  ───────┼────────┤   └─ ResourceQuotas      │
  │   │                              │        │                          │
  │   ├─ pgx → dc-postgres           │        │   Rancher                │
  │   ├─ k8s client → Harvester      │        │   ├─ provisioning.cattle ◀ DCAPI_RANCHER_URL
  │   ├─ REST   → Rancher            │        │   │  /Cluster CRD        │
  │   └─ OIDC   → IdP                │        │   └─ Cloud credentials   │
  │                                  │        │                          │
  │   dcctl   ──── Bearer ──▶ dc-api │        │                          │
  └──────────────────────────────────┘        └──────────────────────────┘
                              │
                              └────── OIDC ──▶ Asgardeo / Keycloak / Okta
```

`dc-api` is a single Go process. Everything backend-y is reached through
clients it constructs on startup:

- **PostgreSQL** — local Docker container; holds the canonical resource registry.
- **Harvester** — reached via `DCAPI_HARVESTER_KUBECONFIG` (Kubernetes dynamic client). KubeVirt VMs and KubeOVN networking CRDs are both on this cluster.
- **Rancher** — reached via REST v3 + the Steve `provisioning.cattle.io/Cluster` CRD.
- **OIDC IdP** — reached at startup for OIDC discovery; thereafter only JWKS lookups.

Local-dev does **not** require a separate KubeOVN cluster — the kubeovn driver
reuses the same Harvester kubeconfig because KubeOVN runs on Harvester.

---

## 1. Prerequisites

### On your workstation

| Tool       | Min version | Why                                                                          |
|------------|-------------|------------------------------------------------------------------------------|
| Go         | 1.26+       | Builds dc-api. dcctl alone works with Go 1.22+.                              |
| Docker     | 24+         | Runs PostgreSQL via `docker compose`.                                        |
| `base64`   | any         | Encoding the Harvester kubeconfig.                                           |
| `openssl`  | any         | Generating the BFF session secret (only if you enable cloud-ui).             |
| `psql`     | any         | *Optional.* Useful for inspecting the dev database.                          |
| `kubectl`  | any         | *Optional.* Useful for sanity-checking KubeOVN on Harvester.                 |
| `jq`       | any         | *Optional.* Pretty-prints API responses.                                     |

Install in one go:

| Platform        | Command                                                                              |
|-----------------|--------------------------------------------------------------------------------------|
| macOS           | `brew install go docker base64 openssl postgresql kubernetes-cli jq`                 |
| Debian / Ubuntu | `sudo apt update && sudo apt install -y golang-go docker.io docker-compose-plugin postgresql-client kubectl jq` |
| Fedora / RHEL   | `sudo dnf install -y golang docker docker-compose-plugin postgresql kubectl jq`      |
| Arch            | `sudo pacman -S go docker docker-compose postgresql kubectl jq`                      |

On macOS, the easiest Docker install is Docker Desktop
(<https://www.docker.com/products/docker-desktop/>); it ships `docker compose` v2.

On Linux, after installing the docker engine, add your user to the `docker`
group so you don't have to `sudo` everything: `sudo usermod -aG docker $USER`,
then log out and back in.

### What you need from the dev cluster

You'll be pointing your local dc-api at an existing Harvester+Rancher pair.
Before starting, make sure you have:

| Artifact                                                | Where to get it                                                                  |
|--------------------------------------------------------|----------------------------------------------------------------------------------|
| Harvester kubeconfig                                    | Harvester UI → Support → Download KubeConfig (or `kubectl config view --raw`).   |
| Rancher API token                                       | Rancher UI → User Settings → API Keys → Create. No scope, no expiry.             |
| Rancher Harvester cloud credential                      | Rancher UI → Cluster Management → Cloud Credentials → Create → Harvester. Paste the Harvester kubeconfig. Copy the resulting `cattle-global-data:cc-xxxxx` name. |
| Network reachability                                    | Your workstation must be able to reach both the Harvester and Rancher API endpoints. For LK dev, that's VPN. |
| KubeOVN installed on Harvester                          | Check with `kubectl get crd subnets.kubeovn.io` against the Harvester kubeconfig. If the CRD is not present, install KubeOVN before continuing (Helm chart docs at <https://kubeovn.github.io/docs/>). |
| One free external bridge for VPC SNAT                   | A Linux bridge on Harvester hosts connected to the upstream router (e.g. `mgmt-br`). dc-api will claim it via `ProviderNetwork`. See §5 for the precondition check. |

### What you need from an identity provider

dc-api is OIDC-only — you'll need *some* IdP. The fastest path is a free Asgardeo
account (see [`docs/asgardeo-setup.md`](asgardeo-setup.md)). Keycloak, Okta,
Auth0 and Dex all work with the same env vars.

You'll need, at a minimum:

- An **issuer URL** (e.g. `https://api.asgardeo.io/t/<org>`).
- A **dcctl public client** (Authorization Code + PKCE, redirect `http://localhost:8085/callback`).
- A **DC-API resource server / audience identifier** that the dcctl client requests.
- Yourself in the `dc-admin` group (or change the name via
  `DCAPI_ADMIN_GROUP`) — the only IdP group dc-api interprets. Tenants and
  members are created through the API (`dcctl admin tenant create`, invites).

---

## 2. Bootstrap

Clone, then run the bootstrap script:

```bash
git clone https://github.com/HiranAdikari/sovereign-cloud.git
cd sovereign-cloud

./scripts/bootstrap.sh        # equivalent to: make bootstrap
```

What it does, in order:

1. Verifies every prerequisite from §1 and prints actionable install hints for anything missing.
2. Copies `.env.example` → `.env` if `.env` does not exist yet (it won't overwrite).
3. Runs `docker compose up -d postgres` and waits for the DB to accept connections.
4. Builds `dc-api/dc-api` and `~/bin/dcctl` (set `DCCTL_OUT=/some/path/dcctl` to override).

Re-run any time — it's idempotent. To verify prerequisites only with no side
effects: `./scripts/bootstrap.sh check` (or `make check`).

---

## 3. Fill in `.env`

Open the `.env` the bootstrap script created and replace every placeholder.
The complete list with comments is in `.env.example`; this section explains
where each value comes from.

### Database (already correct)

```bash
DCAPI_DB_URL="postgres://dc_api:dc_dev_password@localhost:5432/dc_api?sslmode=disable"
```

Matches the `docker-compose.yaml` defaults. Don't change unless you customised the password.

### OIDC

```bash
DCAPI_OIDC_ISSUER="https://api.asgardeo.io/t/<org>"
DCAPI_OIDC_AUDIENCE="<dcctl-client-id>,<cloud-ui-client-id>"
```

`DCAPI_OIDC_AUDIENCE` accepts a comma-separated list of every client whose
tokens dc-api should honour. Add the BFF client and the future Terraform
provider client here when you create them — a token is accepted when at least
one of its audience values matches one entry.

### Harvester

```bash
# macOS
DCAPI_HARVESTER_KUBECONFIG="$(base64 -i ~/.kube/harvester.yaml | tr -d '\n')"
# Linux
DCAPI_HARVESTER_KUBECONFIG="$(base64 -w0 < ~/.kube/harvester.yaml)"
```

The kubeconfig must be **base64-encoded**, on a single line, not the raw YAML.

### Rancher

```bash
DCAPI_RANCHER_URL="https://rancher.example.com"
DCAPI_RANCHER_TOKEN="token-xxxxx:yyyyyyyyyyyyyy"
DCAPI_RANCHER_INSECURE="true"          # dev clusters typically have self-signed certs
DCAPI_RANCHER_HARVESTER_CREDENTIAL="cattle-global-data:cc-xxxxx"
```

The cloud-credential value is the **secret name** shown in Rancher's UI after
you create the Harvester cloud credential, formatted as `<namespace>:<name>`.
Without this, RKE2 cluster create fails because Rancher has nothing to use
for VM provisioning.

### KubeOVN VPC external network (F15)

**Required when `DCAPI_NETWORK_PROVIDER=kubeovn` (the default).** dc-api's F15
bootstrap creates a `ProviderNetwork`, `Vlan`, `Subnet`, and
`NetworkAttachmentDefinition` on Harvester at startup; missing or invalid
values trip the `config.ValidateF15` fail-fast and the process exits.

```bash
DCAPI_VPC_EXTERNAL_BRIDGE="mgmt-br"          # bridge on Harvester hosts
DCAPI_VPC_EXTERNAL_CIDR="192.168.10.0/24"    # CIDR of that bridge's network
DCAPI_VPC_EXTERNAL_GATEWAY="192.168.10.254"  # upstream router IP, must be in CIDR
DCAPI_VPC_EXTERNAL_RESERVED_IPS="192.168.10.6,192.168.10.37"   # IPs IPAM must skip
DCAPI_VPC_EXTERNAL_VLAN_ID="0"               # 0 = untagged
```

To find the right values for your dev cluster:

```bash
# Bridges on a Harvester node (SSH to any node)
ip -br link | grep -E ' br[a-z0-9-]+'

# CIDR + gateway are whatever your upstream router serves on that VLAN.
# Reserved IPs MUST include:
#   - every Harvester node's external IP
#   - the Harvester VIP
#   - any ingress LB VIPs
#   - any other host already pinned on that subnet
```

### Operator break-glass (optional)

```bash
DCAPI_OPERATOR_SSH_KEY="$(cat ~/.ssh/id_ed25519.pub)"
DCAPI_OPERATOR_PASSWORD=""    # leave blank if SSH key alone is enough
```

If set, every RKE2 cluster node gets these credentials injected via cloud-init,
so the IaaS team can SSH or use the recovery console when Rancher is down.

### BFF (optional, only for running cloud-ui locally)

dcctl never needs the BFF — it speaks Bearer-token directly. Leave the
`DCAPI_BFF_*` block as empty defaults unless you're running cloud-ui locally
and want session-cookie login.

If you do enable it:

```bash
DCAPI_BFF_CLIENT_ID="<bff-confidential-client-id>"
DCAPI_BFF_CLIENT_SECRET="<bff-secret>"
DCAPI_BFF_SESSION_SECRET="$(openssl rand -base64 32)"
DCAPI_BFF_REDIRECT_URL="http://localhost:8080/v1/auth/callback"
DCAPI_BFF_POST_LOGIN_REDIRECT="http://localhost:5173/"
DCAPI_BFF_POST_LOGOUT_REDIRECT="http://localhost:5173/"
DCAPI_BFF_COOKIE_DOMAIN=""       # empty = host-only cookie, safe for localhost
DCAPI_BFF_COOKIE_SECURE="false"  # http://localhost can't carry Secure cookies
```

Make sure the BFF redirect URI is registered on the confidential Asgardeo app.

---

## 4. Sanity-check the cluster preconditions

Before booting dc-api, prove the cluster is ready:

```bash
export KUBECONFIG=~/.kube/harvester.yaml   # or whatever path you base64'd

# KubeOVN is installed and reachable
kubectl get crd subnets.kubeovn.io vpcs.kubeovn.io providernetworks.kubeovn.io

# The bridge name you put in DCAPI_VPC_EXTERNAL_BRIDGE actually exists on at
# least one Harvester node. dc-api won't tell you this — the bootstrap call
# silently produces a broken ProviderNetwork.
kubectl get nodes -o yaml | grep -A2 'topology.kubernetes.io/zone'   # node list
# then SSH to a node and run:
ip -br link | grep mgmt-br

# Rancher token works
curl -sk -u "$DCAPI_RANCHER_TOKEN" "$DCAPI_RANCHER_URL/v3/users?me=true" | jq '.data[0].id'
```

If any of these fail, fix them before running dc-api.

---

## 5. Run dc-api

```bash
make run        # loads .env, then runs ./dc-api/dc-api
```

What you should see (paraphrased):

```
INF DC-API starting listen=:8080 log_level=debug
INF connected to PostgreSQL
INF compute provider ready provider=harvester
INF cluster provider ready provider=rancher
INF F32: cluster provisioner wired with Harvester SA bootstrap
INF network provider ready provider=kubeovn
INF kubeovn: F15 external network bootstrap verified cidr=192.168.10.0/24
INF kubeovn: F20 per-VPC DNS bootstrap verified
INF NAT backfill: no VNets found — nothing to backfill
INF DNS backfill: no VNets found — nothing to backfill
INF OIDC middleware ready issuer=https://api.asgardeo.io/...
INF BFF auth disabled (DCAPI_BFF_CLIENT_ID unset)
INF HTTP server listening addr=:8080
```

Smoke checks:

```bash
curl -s http://localhost:8080/healthz                          # 200 OK
curl -s http://localhost:8080/openapi.yaml | head -5           # OpenAPI 3.0.3
open http://localhost:8080/docs                                # Redoc UI (macOS)
xdg-open http://localhost:8080/docs                            # Redoc UI (Linux)
```

To re-run on code changes, Ctrl-C and `make build-api && make run`.

To wipe and start over with a clean database: `make db-reset && make run`
(the schema is re-applied automatically on next startup).

---

## 6. Bootstrap a tenant and project with dcctl

`dcctl` ships with production defaults — override them for local-dev:

```bash
cat > ~/.dcctl/config.yaml <<EOF
dcapi_url: "http://localhost:8080"
oidc_issuer: "https://api.asgardeo.io/t/<your-org>"
client_id: "<dcctl-client-id-from-asgardeo-setup>"
callback_port: 8085
EOF
```

Log in (opens a browser, completes the PKCE flow, drops tokens at
`~/.dcctl/credentials.json`):

```bash
dcctl login
```

If you are in the `dc-admin` group on the IdP, you can now register a
tenant + caps:

```bash
dcctl admin tenant create acme \
  --cpu 32 --memory 64 --storage 500 \
  --max-vnets 5 --max-clusters 3 --max-volumes 20

dcctl tenant list
dcctl tenant set acme
```

If you are NOT a platform admin, someone with admin rights has to register
your tenant first; you can then `dcctl tenant set <id>` once you appear in
`dcctl tenant list`.

Create a project to hold resources:

```bash
dcctl project create dev --cpu 8 --memory 16 --storage 100
dcctl project set dev
dcctl project current      # → dev
```

You are now ready to create resources. Sample first VM:

```bash
dcctl vnet create app-net --cidr 10.10.0.0/16
dcctl subnet create app-sub --vnet app-net --cidr 10.10.0.0/24

dcctl vm create \
  --name web-01 --size medium \
  --image rancher-infra/ubuntu-22-04 \
  --vnet app-net --subnet app-sub \
  --save-key ~/.ssh/web-01.pem

dcctl vm list
```

For the full noun-chain, run `dcctl --help` and `dcctl <noun> --help`.

---

## 7. Hitting the API directly

Anyone writing a non-dcctl client (e.g. a Terraform provider) talks to dc-api
over plain HTTP with a Bearer token:

```bash
TOKEN=$(jq -r .access_token ~/.dcctl/credentials.json)
TENANT=acme
PROJECT=dev
BASE=http://localhost:8080

# Tenant + project listing
curl -sH "Authorization: Bearer $TOKEN" \
  "$BASE/v1/tenants" | jq

curl -sH "Authorization: Bearer $TOKEN" \
  "$BASE/v1/tenants/$TENANT/projects" | jq

# List VMs in the active project
curl -sH "Authorization: Bearer $TOKEN" \
  "$BASE/v1/tenants/$TENANT/projects/$PROJECT/virtual-machines" | jq

# Create a VM (async — returns 202; poll the GET endpoint until status=ACTIVE)
curl -sH "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{
    "name": "api-vm-01",
    "size": "medium",
    "image_name": "rancher-infra/ubuntu-22-04",
    "vnet_id":   "<vnet-uuid>",
    "subnet_id": "<subnet-uuid>"
  }' \
  "$BASE/v1/tenants/$TENANT/projects/$PROJECT/virtual-machines" | jq
```

For long-lived non-interactive clients (CI, Terraform provider tests), prefer
a **service account token** over a user JWT — they don't expire on user
session timeout:

```bash
dcctl tenant service-account create ci-bot
# → prints "dcapi_sa_..." token ONCE. Save it.
```

Use `Authorization: Bearer dcapi_sa_xxxxx` for every request. The
`middleware/serviceaccount.go` chain recognises the prefix and validates
against `service_accounts` in PostgreSQL — no IdP round-trip.

The machine-readable contract is `dc-api/openapi.yaml`. Generate a client
in any language from it:

```bash
# Go
go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
oapi-codegen -config dcctl/internal/client/generated/oapi-codegen.yaml \
  dc-api/openapi.yaml

# TypeScript (used by cloud-ui)
cd cloud-ui && pnpm gen:api
```

---

## 8. Troubleshooting startup failures

| Symptom                                                                          | Likely cause                                                                  | Fix                                                                                                                                                  |
|----------------------------------------------------------------------------------|-------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| `failed to load configuration — DCAPI_VPC_EXTERNAL_BRIDGE missing`               | Missing F15 vars and `NETWORK_PROVIDER` defaults to `kubeovn`.                | Fill in all `DCAPI_VPC_EXTERNAL_*` vars, or set `DCAPI_NETWORK_PROVIDER` to something else (not yet supported — install KubeOVN instead).            |
| `F15 VPC external network config is invalid — DCAPI_VPC_EXTERNAL_GATEWAY is not inside CIDR` | Gateway IP outside the CIDR (typo in either var).                             | Re-check `ip route show default` on a Harvester node.                                                                                                |
| `failed to bootstrap external network resources` (`ProviderNetwork/Vlan/Subnet/NAD`) | KubeOVN not installed on Harvester, or the bridge name is wrong.              | Run §4 sanity checks. Install KubeOVN if the CRDs are absent.                                                                                        |
| `failed to initialise OIDC auth middleware` / `oidc: issuer did not match`       | `DCAPI_OIDC_ISSUER` does not exactly match the IdP's published issuer.        | `curl <issuer>/.well-known/openid-configuration` and copy the `issuer` field verbatim. Trailing slashes matter.                                       |
| `Unauthorized: invalid token` on dcctl login                                     | Token audience doesn't match `DCAPI_OIDC_AUDIENCE`.                           | Add the dcctl client ID to the audience list. For Asgardeo, also ensure the DC-API resource is in the dcctl app's Requested Audience.                |
| `GET /v1/tenants` returns `[]` after login                                       | The user has no role_assignments rows yet.                                    | Invite the user to a tenant (`dcctl tenant member create <email>`), or as admin register a tenant first.                                              |
| `connect: connection refused` on first request                                   | dc-api never made it past startup.                                            | Re-read the dc-api logs — every fatal exits with a descriptive `Fatal` line.                                                                         |
| `dc-postgres` won't start                                                        | Port 5432 already in use by another local Postgres.                           | `lsof -iTCP:5432` and stop the conflicting process, or edit `docker-compose.yaml` to remap the port and update `DCAPI_DB_URL`.                       |
| `failed to connect to PostgreSQL`                                                | Wrong password / wrong port / container not ready.                            | `docker compose ps` and `docker compose logs postgres`. Reset: `make db-reset`.                                                                       |
| `failed to initialise cluster provider`                                          | Rancher unreachable or token invalid.                                         | `curl -ksu "$DCAPI_RANCHER_TOKEN" $DCAPI_RANCHER_URL/v3/users?me=true` should return JSON.                                                            |
| `RANCHER_HARVESTER_CREDENTIAL missing — empty string`                            | Forgot to set it.                                                             | Create a Harvester cloud credential in Rancher UI; copy the `cattle-global-data:cc-xxxxx` name into `.env`.                                          |
| dc-api boots but cluster creates hang forever                                    | The cloud credential references a kubeconfig Rancher can't reach.             | Open the cloud credential in Rancher UI, click "Refresh" — it must show "active". If Rancher is in a different network zone than Harvester, fix that. |

For deeper debugging, set `DCAPI_LOG_LEVEL=debug` and re-run.

---

## 9. Tear-down

```bash
make down              # stop postgres, keep data volume
docker compose down -v # stop postgres, wipe data
rm -rf ~/.dcctl        # discard dcctl tokens and context
```

The Harvester / Rancher state created during local runs (KubeOVN VPCs, RKE2
clusters, VMs) is **not** torn down automatically — dc-api owns the registry,
and once the local PostgreSQL is wiped, the backend objects are orphaned.
Either delete them through `dcctl` before tearing down, or clean up the
cluster manually (`kubectl delete vpc/...`, `kubectl delete cluster.cattle.io/...`).
