# Open Cloud Datacenter — Control Plane

> **You're on the `controlplane` branch of `open-cloud-datacenter`.**
> This branch holds the DC-API control-plane services that turn the raw
> infrastructure stood up by the [`terraform` branch](https://github.com/wso2/open-cloud-datacenter/tree/terraform)
> into a self-service Cloud Provider experience. See
> [OCDC-RESTRUCTURE.md](https://github.com/wso2/open-cloud-datacenter#branch-model)
> for the full branch model.

A self-hosted cloud provider experience built on [Harvester](https://harvesterhci.io) and [Rancher](https://rancher.com). Provision VMs and Kubernetes clusters with a single CLI command — no Terraform knowledge required.

## What's on this branch

| Path | What it is |
|---|---|
| [`dc-api/`](./dc-api) | Go REST API server — the canonical control plane. |
| [`cloud-ui/`](./cloud-ui) | React + Fluent UI web app — the browser-shaped user surface. |
| [`dcctl/`](./dcctl) | Cobra CLI — the kubectl-shaped user surface. |
| [`flux/`](./flux) | **Flux GitOps deployment recipes** — `infrastructure/` (cert-manager, ingress-nginx, sealed-secrets) + `platform/` (dc-api, cloud-ui, dc-postgres, ARC bases, Image{Repository,Policy,UpdateAutomation}). Consumers Kustomize-reference this tree as a remote base. |
| [`examples/consumer/flux/`](./examples/consumer/flux) | Per-environment overlay template the bootstrap wizard renders into a consumer repo. |
| [`scripts/init-flux.sh`](./scripts/init-flux.sh) | Bootstrap wizard — preps a consumer for first Flux apply on a fresh cluster. |
| [`ci/`](./ci) | Shared CI tooling (the self-hosted runner image used by sovereign-cloud's CI; legacy from before this branch's workflows moved to GitHub-hosted runners — see `.github/workflows/`). |
| [`crds/`](./crds) | Note: managed-service operator source moved to the [`operators` branch](https://github.com/wso2/open-cloud-datacenter/tree/operators). Whatever's left here is legacy or example. |
| [`docs/`](./docs) | Architecture, ops runbooks, API reference. |
| [`.github/workflows/`](./.github/workflows) | CI: `dc-api.yaml`, `cloud-ui.yaml`, `contract.yaml` — build/test/publish on push; PR-time gates. Images publish to `ghcr.io/wso2/{dc-api, dc-api-webhook, cloud-ui}`. |

## Consuming this branch

A consumer who wants their own self-hosted cloud:

1. **Run the prerequisites from the [`terraform`](https://github.com/wso2/open-cloud-datacenter/tree/terraform) branch** — modules under `platform/` and `tenancy/` stand up Rancher + Harvester + the underlying networking/storage. This branch assumes that's done.
2. **(Optional) Build your own images.** This branch's `.github/workflows/` publishes `ghcr.io/wso2/{dc-api, dc-api-webhook, cloud-ui}:latest` on every merge to `controlplane`. If you'd rather have your own org's images, fork this repo, let your fork's workflows publish to `ghcr.io/<your-org>/*`, and retarget your Flux overlay there.
3. **Bootstrap Flux on your cluster.** Run [`scripts/init-flux.sh init`](./scripts/init-flux.sh) — the wizard prompts for your environment specifics (hostnames, GHCR org, OIDC issuer), renders [`examples/consumer/flux/`](./examples/consumer/flux) into your consumer repo as `environments/<env>/flux/`, and sets up the GitRepository + Kustomization pointing back at this branch's `flux/platform/` as a remote base.
4. **Seal your secrets.** `scripts/init-flux.sh seal` prompts for the 5 cluster-side secrets (postgres password, OIDC client secret, TLS cert, GHCR pull token, GitHub PAT for ARC), seals them via the cluster's sealed-secrets controller, and commits them to your consumer repo.
5. **Apply.** Flux reconciles. dc-api + cloud-ui come up. ImageUpdateAutomation auto-rolls new images as they're published by this branch.

After bootstrap, ongoing operation is hands-off: every merge to this branch's `controlplane` → image published → Flux ImagePolicy sees the new build → IUA bumps the consumer repo's image pin → Flux Kustomization rolls the cluster.

```
dcctl create vm --name web-01 --size medium \
  --image default/image-rflb5 --network default/vm-net-100 \
  --save-key ~/.ssh/web-01.pem
```

> **Looking for the technical deep dive?** See [`docs/dc-api-architecture.md`](docs/dc-api-architecture.md) — components, sequence diagrams, deployment topology, code structure, the lot.

---

## Architecture

```
┌──────────┐   OIDC/PKCE   ┌──────────────┐
│  dcctl   │ ─────────────▶│  Identity    │
│  (CLI)   │               │  Provider    │
└────┬─────┘   JWT token   │  (any OIDC)  │
     │ ◀────────────────── └──────────────┘
     │
     │  Bearer token
     ▼
┌────────────────────────────────────────┐
│              DC-API  :8080             │
│                                        │
│  ┌──────────┐   ┌────────────────┐    │
│  │  Auth    │   │  Quota Engine  │    │
│  │Middleware│   │  (per-tenant)  │    │
│  └──────────┘   └────────────────┘    │
│                                        │
│  ┌──────────────────────────────────┐ │
│  │      Resource Registry           │ │
│  │         (PostgreSQL)             │ │
│  └──────────────────────────────────┘ │
│                                        │
│  ┌─────────────┐  ┌────────────────┐  │
│  │  Harvester  │  │    Rancher     │  │
│  │   Driver    │  │    Driver      │  │
│  │(KubeVirt CRD│  │  (REST v3 API) │  │
│  └──────┬──────┘  └───────┬────────┘  │
└─────────┼─────────────────┼───────────┘
          │                 │
          ▼                 ▼
    ┌──────────┐      ┌──────────┐
    │ Harvester│      │ Rancher  │
    │  (VMs)   │      │(Clusters)│
    └──────────┘      └──────────┘
```

**Request flow**: `dcctl login` authenticates with your OIDC provider and stores a token locally. Every `dcctl` command sends that token to DC-API as a Bearer token. DC-API validates the token, checks the tenant's quota, creates a record in PostgreSQL, and calls Harvester or Rancher to provision the resource asynchronously.

---

## Pluggable Authentication

DC-API uses standard OIDC — **it is not tied to any specific identity provider**. Any OIDC-compliant IdP works by changing two environment variables:

| Provider   | `DCAPI_OIDC_ISSUER`                              |
|------------|--------------------------------------------------|
| Asgardeo   | `https://api.asgardeo.io/t/<your-org>`           |
| Keycloak   | `https://keycloak.example.com/realms/<realm>`    |
| Okta       | `https://<domain>.okta.com/oauth2/default`       |
| Auth0      | `https://<domain>.auth0.com/`                    |
| Dex        | `https://dex.example.com`                        |

Group-to-tenant mapping is also configurable:

| Variable                    | Default        | Purpose                              |
|-----------------------------|----------------|--------------------------------------|
| `DCAPI_TENANT_GROUP_PREFIX` | `dc-tenant-`   | Group prefix that identifies tenants |
| `DCAPI_ADMIN_GROUP`         | `dc-admin`     | Group name for platform admins       |

A user in group `dc-tenant-teamalpha` gets tenant ID `teamalpha`. A user in `dc-admin` gets admin access. No code changes needed for different naming conventions.

---

## Quick Start

> **For a step-by-step walk-through with every variable explained, see [`docs/local-dev.md`](docs/local-dev.md).**
> The summary below assumes you already have a Harvester + Rancher dev cluster you can point at.

### Prerequisites

What you need on your workstation:

| Tool       | Min version | Why                                                                          |
|------------|-------------|------------------------------------------------------------------------------|
| Go         | 1.26+       | Builds dc-api. dcctl alone works with Go 1.22+.                              |
| Docker     | 24+         | Runs PostgreSQL via `docker compose` for local dev.                          |
| `base64`   | any         | Encoding the Harvester kubeconfig for `DCAPI_HARVESTER_KUBECONFIG`.          |
| `openssl`  | any         | Generating the BFF session secret (optional — only if you enable cloud-ui).  |
| `psql`     | any         | *Optional.* Useful for inspecting the dev database.                          |
| `kubectl`  | any         | *Optional.* Useful for dumping a Harvester kubeconfig or sanity-checking KubeOVN. |

Install commands:

| Platform        | Command                                                                              |
|-----------------|--------------------------------------------------------------------------------------|
| macOS           | `brew install go docker base64 openssl postgresql kubernetes-cli jq`                 |
| Debian / Ubuntu | `sudo apt update && sudo apt install -y golang-go docker.io docker-compose-plugin postgresql-client kubectl jq` |
| Fedora / RHEL   | `sudo dnf install -y golang docker docker-compose-plugin postgresql kubectl jq`      |
| Arch            | `sudo pacman -S go docker docker-compose postgresql kubectl jq`                      |

What you need to bring with you (not installable from a package manager):

- **Harvester cluster** with KubeOVN installed and a kubeconfig you can copy.
- **Rancher** pointed at that Harvester, with a User API token and a Harvester
  cloud credential (`cattle-global-data:cc-xxxxx`) created in the UI.
- **OIDC identity provider** — Asgardeo (free), Keycloak, Okta, etc. See the
  [Asgardeo Setup Guide](docs/asgardeo-setup.md) for the fastest path.

### 1. Bootstrap

One command checks prerequisites, starts PostgreSQL, builds `dc-api` and `dcctl`, and copies `.env.example` → `.env`:

```bash
git clone -b controlplane https://github.com/wso2/open-cloud-datacenter.git
cd open-cloud-datacenter

./scripts/bootstrap.sh        # or: make bootstrap
```

To re-check prerequisites only (no side effects): `./scripts/bootstrap.sh check` (or `make check`).

### 2. Fill in `.env`

Open `.env` and replace every placeholder. The required variables are:

| Variable                              | What to put in it                                                                   |
|---------------------------------------|-------------------------------------------------------------------------------------|
| `DCAPI_OIDC_ISSUER`                   | Your IdP issuer URL, e.g. `https://api.asgardeo.io/t/<org>`.                        |
| `DCAPI_OIDC_AUDIENCE`                 | Comma-separated client IDs whose tokens dc-api accepts (dcctl, cloud-ui, …).        |
| `DCAPI_HARVESTER_KUBECONFIG`          | `base64 -i ~/.kube/harvester.yaml \| tr -d '\n'` (macOS) or `base64 -w0` (Linux).    |
| `DCAPI_RANCHER_URL`                   | Your Rancher server URL.                                                            |
| `DCAPI_RANCHER_TOKEN`                 | Rancher → User Settings → API Keys → Create (no scope, no expiry).                  |
| `DCAPI_RANCHER_HARVESTER_CREDENTIAL`  | Cloud-credential secret name from Rancher → Cluster Management → Cloud Credentials. |
| `DCAPI_VPC_EXTERNAL_BRIDGE`           | Linux bridge on Harvester hosts (e.g. `mgmt-br`).                                   |
| `DCAPI_VPC_EXTERNAL_CIDR`             | CIDR of that external network (e.g. `192.168.10.0/24`).                             |
| `DCAPI_VPC_EXTERNAL_GATEWAY`          | Upstream gateway IP inside the CIDR.                                                |

`.env.example` documents every other variable with a comment. See [`docs/local-dev.md`](docs/local-dev.md) for the rationale behind each one.

> **Why the KubeOVN VPC vars are required:** dc-api defaults to KubeOVN as the
> network provider (`DCAPI_NETWORK_PROVIDER=kubeovn`) and runs an F15 bootstrap
> at startup that creates a cluster-scoped ProviderNetwork / Vlan / Subnet / NAD
> on Harvester. Without `DCAPI_VPC_EXTERNAL_*`, this fails fast and the process
> exits. If your Harvester does NOT have KubeOVN installed yet, install it
> before running dc-api — see [`docs/local-dev.md`](docs/local-dev.md) §5.

### 3. Run DC-API

```bash
make run

# Expected log lines:
# INF DC-API starting listen=:8080 log_level=debug
# INF connected to PostgreSQL
# INF compute provider ready provider=harvester
# INF cluster provider ready provider=rancher
# INF network provider ready provider=kubeovn
# INF kubeovn: F15 external network bootstrap verified
# INF kubeovn: F20 per-VPC DNS bootstrap verified
# INF OIDC middleware ready issuer=...
# INF HTTP server listening addr=:8080
```

Open the live API documentation at <http://localhost:8080/docs>.

### 4. Bootstrap a tenant and project

`dcctl` config defaults point at the production cluster. Override for local use:

```bash
cat > ~/.dcctl/config.yaml <<EOF
dcapi_url: "http://localhost:8080"
oidc_issuer: "https://api.asgardeo.io/t/<your-org>"
client_id: "<dcctl-client-id>"
callback_port: 8085
EOF

dcctl login                          # opens a browser; PKCE flow

# Platform-admin one-time tenant register:
dcctl admin tenant create acme --cpu 32 --memory 64 --storage 500

dcctl tenant list
dcctl tenant set acme

dcctl project create dev --cpu 8 --memory 16 --storage 100
dcctl project set dev

dcctl project current                # dev
```

### 5. Create a VM

```bash
# First create a VNet + subnet (M2 — required for non-bridge VMs):
dcctl vnet create app-net --cidr 10.10.0.0/16
dcctl subnet create app-sub --vnet app-net --cidr 10.10.0.0/24

# Then a VM on that subnet:
dcctl vm create \
  --name web-01 --size medium \
  --image rancher-infra/ubuntu-22-04 \
  --vnet app-net --subnet app-sub \
  --save-key ~/.ssh/web-01.pem

dcctl vm list
dcctl vm get <id>
```

For RKE2 clusters, key vaults, bastions, and peerings, run `dcctl <resource> --help`.

---

## Environment Variables Reference

`.env.example` is the source of truth and documents every variable with a
comment. The summary below groups them by purpose. **Required** variables
make `dc-api` exit at startup if missing or invalid.

### DC-API (`DCAPI_*`)

| Variable                              | Required | Default                  | Purpose                                                                 |
|---------------------------------------|----------|--------------------------|-------------------------------------------------------------------------|
| **Database**                          |          |                          |                                                                         |
| `DCAPI_DB_URL`                        | ✅       |                          | PostgreSQL DSN.                                                         |
| **OIDC**                              |          |                          |                                                                         |
| `DCAPI_OIDC_ISSUER`                   | ✅       |                          | Issuer URL of your IdP (Asgardeo, Keycloak, Okta, …).                   |
| `DCAPI_OIDC_AUDIENCE`                 | ✅       |                          | Comma-separated accepted client IDs.                                    |
| `DCAPI_TENANT_GROUP_PREFIX`           |          | `dc-tenant-`             | IdP group prefix that identifies tenants.                               |
| `DCAPI_ADMIN_GROUP`                   |          | `dc-admin`               | IdP group that maps to platform admin.                                  |
| `DCAPI_PLATFORM_ADMIN_SUBS`           |          |                          | Comma-separated `sub` values for break-glass platform admins.           |
| `DCAPI_RBAC_AUTOPROVISION`            |          | `false`                  | Auto-grant `member` on first login with a valid tenant group.           |
| **IdP Directory (optional)**          |          |                          | Leave all four unset to disable. When set, enable live directory browsing and email-based invites (see [rbac.md](docs/rbac.md#idp-directory-configuration-optional)). |
| `DCAPI_IDP_SCIM_BASE_URL`             |          |                          | SCIM2 endpoint URL of your IdP. The four required `DCAPI_IDP_*` vars must be set together or all unset. |
| `DCAPI_IDP_TOKEN_URL`                 |          |                          | OAuth2 token endpoint for m2m credential (read-only user/group VIEW scopes). |
| `DCAPI_IDP_CLIENT_ID`                 |          |                          | Client ID of machine-to-machine OAuth2 app. |
| `DCAPI_IDP_CLIENT_SECRET`             |          |                          | Client secret for the m2m app. |
| `DCAPI_IDP_SCOPES`                    |          |                          | OAuth2 scopes for the client_credentials grant (space/comma-separated). Required for Asgardeo/WSO2 IS (else SCIM 403s); optional elsewhere. LIST/VIEW only. Asgardeo: `internal_user_mgt_list internal_user_mgt_view internal_group_mgt_view`. |
| **Harvester**                         |          |                          |                                                                         |
| `DCAPI_HARVESTER_KUBECONFIG`          | ✅       |                          | Base64-encoded Harvester kubeconfig.                                    |
| `DCAPI_HARVESTER_NAMESPACE`           |          | `default`                | Fallback Harvester namespace.                                           |
| **Rancher**                           |          |                          |                                                                         |
| `DCAPI_RANCHER_URL`                   | ✅       |                          | Rancher server URL.                                                     |
| `DCAPI_RANCHER_TOKEN`                 | ✅       |                          | Rancher API token.                                                      |
| `DCAPI_RANCHER_INSECURE`              |          | `false`                  | Skip TLS verify (dev clusters with self-signed certs).                  |
| `DCAPI_RANCHER_HARVESTER_CREDENTIAL`  | ✅       |                          | Rancher cloud-credential secret name (`cattle-global-data:cc-xxxxx`).   |
| **Operator break-glass**              |          |                          |                                                                         |
| `DCAPI_OPERATOR_SSH_KEY`              |          |                          | Public key injected into every cluster node via cloud-init.             |
| `DCAPI_OPERATOR_PASSWORD`             |          |                          | Recovery-console password for cluster nodes.                            |
| **Provider selection**                |          |                          |                                                                         |
| `DCAPI_VM_PROVIDER`                   |          | `harvester`              | VM backend.                                                             |
| `DCAPI_CLUSTER_PROVIDER`              |          | `rancher`                | Cluster backend.                                                        |
| `DCAPI_NETWORK_PROVIDER`              |          | `kubeovn`                | SDN backend (only kubeovn supported in M2).                             |
| `DCAPI_KUBEOVN_NAMESPACE`             |          | `kube-ovn`               | Namespace where KubeOVN daemons live.                                   |
| **KubeOVN VPC external network (F15)**|          |                          | Required when `DCAPI_NETWORK_PROVIDER=kubeovn` (the default).            |
| `DCAPI_VPC_EXTERNAL_BRIDGE`           | ✅       |                          | Linux bridge on Harvester hosts.                                        |
| `DCAPI_VPC_EXTERNAL_CIDR`             | ✅       |                          | CIDR of that external network.                                          |
| `DCAPI_VPC_EXTERNAL_GATEWAY`          | ✅       |                          | Upstream gateway IP inside the CIDR.                                    |
| `DCAPI_VPC_EXTERNAL_RESERVED_IPS`     |          |                          | Comma-separated IPs KubeOVN's IPAM must skip.                           |
| `DCAPI_VPC_EXTERNAL_VLAN_ID`          |          | `0`                      | 0 = untagged.                                                           |
| **Per-VPC CoreDNS (F20)**             |          |                          |                                                                         |
| `DCAPI_VPC_DNS_FORWARDERS`            |          | `1.1.1.1,8.8.8.8`        | Upstream resolvers for per-VPC CoreDNS.                                 |
| `DCAPI_VPC_DNS_IMAGE`                 |          | auto-detect              | CoreDNS image to run.                                                   |
| `DCAPI_VPC_DNS_SEARCH_DOMAIN`         |          |                          | Optional DNS search domain injected into every VPC VM.                  |
| **Cluster + bastion management NIC**  |          |                          |                                                                         |
| `DCAPI_CLUSTER_MGMT_NAD`              |          | `iaas/vm-network-001`    | NAD for the outbound NIC on RKE2 cluster nodes.                         |
| `DCAPI_BASTION_MGMT_NAD`              |          | `iaas/vm-network-001`    | NAD for the bastion's operator-reachable NIC.                           |
| `DCAPI_BASTION_IMAGE`                 |          | `rancher-infra/ubuntu-22-04` | VM image used for bastions.                                         |
| `DCAPI_INFRA_RESERVED_NADS`           |          | `dc-api/dc-api-mgmt`     | NADs tenant VMs MUST NOT attach to.                                     |
| **M3 Key Vault**                      |          |                          |                                                                         |
| `DCAPI_KV_BACKEND_ADDR`               |          | `openbao.dc-api-vault.svc.cluster.local` | OpenBao Service backing Key Vault endpoints.            |
| `DCAPI_KV_BACKEND_PORT`               |          | `8200`                   | OpenBao port.                                                           |
| **BFF (cloud-ui session login)**      |          |                          | Leave empty to disable; dcctl never needs these.                        |
| `DCAPI_BFF_CLIENT_ID`                 |          |                          | Asgardeo confidential client ID for the BFF.                            |
| `DCAPI_BFF_CLIENT_SECRET`             |          |                          | Asgardeo client secret.                                                 |
| `DCAPI_BFF_SESSION_SECRET`            |          |                          | Base64-encoded 32-byte AES key (`openssl rand -base64 32`).             |
| `DCAPI_BFF_REDIRECT_URL`              |          |                          | Allowed redirect URI registered on the BFF Asgardeo app.                |
| `DCAPI_BFF_POST_LOGIN_REDIRECT`       |          |                          | cloud-ui URL the browser lands on after callback.                       |
| `DCAPI_BFF_POST_LOGOUT_REDIRECT`      |          |                          | Browser bounce-back after Asgardeo `end_session`.                       |
| `DCAPI_BFF_COOKIE_DOMAIN`             |          |                          | Domain attribute on the session cookie.                                 |
| `DCAPI_BFF_COOKIE_SECURE`             |          | `true`                   | Flip to `false` for plain-`http` localhost dev.                         |
| **Server**                            |          |                          |                                                                         |
| `DCAPI_LOG_LEVEL`                     |          | `info`                   | `debug` / `info` / `warn` / `error`.                                    |
| `DCAPI_LISTEN_ADDR`                   |          | `:8080`                  | HTTP listen address.                                                    |

### dcctl (`~/.dcctl/config.yaml` or `DCCTL_*` env vars)

| Key             | Default                                    | Description                      |
|-----------------|--------------------------------------------|----------------------------------|
| `dcapi_url`     | `https://dc-api.internal.wso2.com`         | DC-API base URL                  |
| `oidc_issuer`   | `https://api.asgardeo.io/t/wso2`           | OIDC issuer URL                  |
| `client_id`     | `dcctl-public-client`                      | dcctl OIDC client ID             |
| `callback_port` | `8085`                                     | Local OIDC callback port         |

Active tenant and project are stored in `~/.dcctl/context.yaml` and managed
with `dcctl tenant set` / `dcctl project set`. Per-command overrides:
`--tenant`, `--project`, `DCCTL_TENANT`, `DCCTL_PROJECT`.

---

## API Reference

All endpoints require `Authorization: Bearer <token>`. The full machine-readable
spec is at [`dc-api/openapi.yaml`](dc-api/openapi.yaml) and rendered live at
<http://localhost:8080/docs> when dc-api is running.

DC-API uses a `Tenant → Project → Resource` hierarchy (M2.5). Most resources
live under `/v1/tenants/{tenant_id}/projects/{project_id}/...`.

### Tenant + project management

| Method   | Path                                                | Description                                        |
|----------|-----------------------------------------------------|----------------------------------------------------|
| `GET`    | `/v1/tenants`                                       | List tenants the caller can access.                |
| `POST`   | `/v1/admin/tenants`                                 | Platform-admin: register a tenant with caps.       |
| `PATCH`  | `/v1/admin/tenants/{tenant_id}`                     | Platform-admin: adjust tenant caps.                |
| `POST`   | `/v1/tenants/{tenant_id}/projects`                  | Create a project inside a tenant.                  |
| `GET`    | `/v1/tenants/{tenant_id}/projects`                  | List projects in a tenant.                         |
| `GET`    | `/v1/tenants/{tenant_id}/projects/{project_id}`     | Get a project.                                     |
| `PATCH`  | `/v1/tenants/{tenant_id}/projects/{project_id}`     | Update project quotas.                             |
| `DELETE` | `/v1/tenants/{tenant_id}/projects/{project_id}`     | Delete a project (cascade cleanup).                |
| `GET`    | `/v1/tenants/{tenant_id}/cap-usage`                 | Tenant cap vs allocated project quotas.            |
| `POST`   | `/v1/tenants/{tenant_id}/members`                   | Invite a user (owner only).                        |
| `GET`    | `/v1/tenants/{tenant_id}/members`                   | List members.                                      |
| `DELETE` | `/v1/tenants/{tenant_id}/members/{principal_id}`    | Remove a member (owner only).                      |

### Project-scoped resources

Each path below is rooted at `/v1/tenants/{tenant_id}/projects/{project_id}`.

| Resource             | CRUD paths under that root                                                    |
|----------------------|-------------------------------------------------------------------------------|
| Virtual machines     | `POST/GET /virtual-machines`, `GET/DELETE /virtual-machines/{id}`             |
| Clusters (RKE2)      | `POST/GET /clusters`, `GET/DELETE /clusters/{id}`, `GET /clusters/{id}/kubeconfig`, node pools under `/clusters/{id}/node-pools/...` |
| Bastions             | `POST/GET /bastions`, `GET/DELETE /bastions/{id}`                             |
| VNets                | `POST/GET /vnets`, `GET/DELETE /vnets/{id}`                                   |
| Subnets              | `POST/GET /vnets/{vnet_id}/subnets`, `GET/DELETE /vnets/{vnet_id}/subnets/{id}` |
| Route tables         | `POST/GET/PUT/DELETE /vnets/{vnet_id}/route-tables/...`                       |
| Peerings             | `POST/GET /vnets/{vnet_id}/peerings`, `GET/DELETE /vnets/{vnet_id}/peerings/{id}` |
| DNS zones + records  | `/vnets/{vnet_id}/dns-zones/...`                                              |
| Security groups      | `POST/GET /security-groups`, rules + attachments under `/security-groups/{id}/...` |
| Key Vaults           | `/keyvaults/...`, secrets at `/keyvaults/{id}/secrets/...`, private endpoints at `/keyvaults/{id}/private-endpoints/...` |
| Service accounts     | `POST/GET /service-accounts`, `GET/DELETE /service-accounts/{id}`             |

### Tenant-scoped helpers

| Method | Path                                            | Description                                  |
|--------|-------------------------------------------------|----------------------------------------------|
| `GET`  | `/v1/tenants/{tenant_id}/images`                | List available VM images.                    |
| `POST` | `/v1/tenants/{tenant_id}/images`                | Register a new image (provider downloads it).|
| `GET`  | `/v1/tenants/{tenant_id}/networks`              | List legacy bridge networks (pre-VNet).      |

### Auth + meta

| Method | Path                  | Auth   | Description                                       |
|--------|-----------------------|--------|---------------------------------------------------|
| `GET`  | `/healthz`            | None   | Liveness probe.                                   |
| `GET`  | `/openapi.yaml`       | None   | Raw OpenAPI 3.0.3 spec.                           |
| `GET`  | `/docs`               | None   | Redoc HTML rendering of the spec.                 |
| `GET`  | `/v1/auth/login`      | None   | BFF login (enabled when `DCAPI_BFF_CLIENT_ID` is set). |
| `GET`  | `/v1/auth/callback`   | None   | BFF OAuth callback.                               |
| `POST` | `/v1/auth/logout`     | Cookie | BFF logout.                                       |
| `GET`  | `/v1/auth/me`         | Cookie | BFF: who am I.                                    |

---

## Project Structure

```
sovereign-cloud/
├── dc-api/                       Go REST API server (Go 1.26)
│   ├── cmd/dc-api/main.go        Entry point — env loading, signals, graceful shutdown
│   ├── Dockerfile                Multi-stage build → distroless/static:nonroot
│   ├── deploy/                   Kubernetes manifests for in-cluster deploy
│   └── internal/
│       ├── config/               Env-var configuration (DCAPI_* prefix)
│       ├── models/               Domain types
│       ├── db/                   PostgreSQL repository + schema + migration runner
│       ├── providers/            ComputeProvider + ClusterProvider interfaces
│       │   ├── harvester/        Harvester driver (KubeVirt CRDs via dynamic client)
│       │   └── rancher/          Rancher driver (REST v3)
│       ├── api/
│       │   ├── middleware/       JWT auth (OIDC, provider-agnostic)
│       │   ├── handlers/         vm.go, cluster.go (also handles images/networks)
│       │   └── router.go         Chi router wiring
│       └── reconciler/           Background goroutine — syncs PENDING → ACTIVE
│
├── dcctl/                        Cobra CLI (Go 1.22)
│   ├── cmd/                      login, logout, create, get, list, delete, kubeconfig
│   └── internal/                 PKCE auth flow, config, HTTP client
│
├── .github/workflows/deploy.yaml CI/CD via in-cluster GitHub Actions runner (ARC)
│
└── docs/
    ├── dc-api-architecture.md    ⭐ Technical architecture & component overview
    ├── dc-api-internal-proposal.md  The "why" — internal proposal document
    ├── ops-bootstrap.md          Step-by-step runbook for new datacenter regions
    ├── asgardeo-setup.md         Identity-provider setup guide
    └── *.docx                    Pandoc-rendered copies for Google Docs
```

---

## Contributing

See [CLAUDE.md](CLAUDE.md) for architecture decisions and design patterns.
See [MILESTONES.md](MILESTONES.md) for the delivery roadmap.

This project is not yet on the Terraform Registry or published as a release.
Build from source using the steps above.

---

## License

[Apache 2.0](LICENSE) — planned when open-sourced.
