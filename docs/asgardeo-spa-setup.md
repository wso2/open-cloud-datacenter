# Asgardeo SPA Setup Guide

> **⚠️ Known limitation (2026-05-10):** Asgardeo strips the `groups` claim
> from tokens issued to public clients regardless of every config knob
> (template, `mandatory`, `skip_login_consent`, etc.). dc-api requires
> `groups` to derive `tenantID`, so cloud-ui sign-in via this SPA flow
> currently 403s on every API call. Tracked as **F7** in `FOLLOWUPS.md`
> with a backend-for-frontend (BFF) fix planned. Until then, cloud-ui
> uses `VITE_DEV_TOKEN` (a `dcctl login` token) for development. The TF
> resources described below are still useful — they'll be repurposed
> when F7 lands. Operators setting up a fresh environment can skip
> Steps 6-7 for now.

How to register the **`cloud-ui`** browser app as a Single-Page Application
(SPA) in your Asgardeo organisation, so users can sign in with their WSO2
identity from the web console.

This is a sibling to [`asgardeo-setup.md`](./asgardeo-setup.md) — that one
covers the DC-API resource server + the dcctl CLI client (already done).
This one covers the **third client**: the web UI.

> Read the existing setup guide first if you haven't done so. This file
> assumes the DC-API resource application and your tenant groups already
> exist.

---

## Why a separate Asgardeo client for the SPA?

The web app and the CLI both authenticate the same human, but they need
different OAuth client configurations:

| | dcctl (CLI) | cloud-ui (browser) |
|---|---|---|
| Asgardeo app type | Standard-Based / Public | **Single-Page Application** |
| Redirect URI | `http://localhost:8085/callback` | `http://localhost:5173/callback` (dev) + `https://cloud.lk.internal.wso2.com/callback` (prod) |
| Allowed origin (CORS) | n/a | dev + prod hosts above |
| Grant type | Authorization Code + PKCE | Authorization Code + PKCE |
| Client secret | none (public) | none (public) |
| Token storage | `~/.dcctl/credentials.json` | `sessionStorage` (in-memory effectively) |
| Silent renewal | refresh token in file | refresh-token rotation via iframe |

Same human, same OIDC issuer, different redirect URIs. They cannot share a
client because Asgardeo (and any sane IdP) ties redirect URIs to a single
client registration.

---

## What you'll register

```
Asgardeo organization: "<org>"
│
├── Application: "DC-API"                 ← already exists (resource server)
├── Application: "dcctl"                  ← already exists (CLI client)
├── Application: "cloud-ui"               ← NEW (browser SPA client) ← this guide
│   └── Redirect URIs: localhost:5173/callback + cloud.lk.internal.wso2.com/callback
└── (groups + users unchanged)
```

---

## Step 1: Create the SPA application

1. Asgardeo Console → **Applications** → **New Application**.
2. Pick the **Single-Page Application** template (NOT Standard-Based).
   This template defaults to public client, PKCE-required, no client secret —
   exactly what we want.
3. Fill in:
   - **Name**: `cloud-ui` (or `WSO2 Infrastructure Platform Web` if you prefer
     human-readable; the technical name in `cloud-ui/.env` will reference
     the resulting `client_id`, not this name).
   - **Authorized redirect URLs** — add both:
     - `http://localhost:5173/callback`
     - `https://cloud.lk.internal.wso2.com/callback`
4. Click **Register**.

---

## Step 2: Configure the protocol

On the new app's **Protocol** tab:

1. **Allowed Grant Types**: only **Authorization Code** should be checked.
   (No client credentials, no implicit, no refresh-token grant if you don't
   need long sessions in-browser. PKCE is implicit in the SPA template.)
2. **Public client**: should already be ON because of the SPA template —
   verify.
3. **PKCE**: **mandatory** — verify.
4. **Access Token** section:
   - **Token type**: **JWT** (DC-API only validates JWTs).
   - **User access token expiry**: 3600s (1 hour) is fine for a browser
     session. Refresh tokens handle the rest.
   - **Application access token expiry**: doesn't apply for SPAs (no client
     credentials).
5. **Refresh Token**:
   - **Renew refresh token**: ON (rotates on each silent renew).
   - **Refresh token expiry**: 86400s (24h) is reasonable.
6. **ID Token**:
   - **ID token expiry**: 3600s.
   - **Audience**: leave empty. Asgardeo will issue tokens whose `aud`
     claim is the SPA's own Client ID — that's fine because dc-api
     accepts a list of allowed audiences (see Step 7 below). No
     per-client Asgardeo audience config needed.
7. **Logout URLs** (optional but recommended): same hosts as redirect URIs,
   path `/logout`. Keeps `Single Logout` working if you ever wire it.

Click **Save**.

---

## Step 3: Configure CORS

The browser will make XHR requests from the SPA's origin to the Asgardeo
token endpoint. Asgardeo needs to allow the SPA's origin:

1. App page → **Info** tab → look for the section listing **Allowed
   Origins** (or sometimes called "Web origin" / "CORS origins").
2. Add **both**:
   - `http://localhost:5173`
   - `https://cloud.lk.internal.wso2.com`

Asgardeo also accepts wildcard subdomains in some plans; stick to exact
hosts for security.

---

## Step 4: Note the values for `cloud-ui` configuration

From the **Info** tab of the newly created `cloud-ui` application:

- **Client ID** (you'll see it labelled "Client ID" or "Consumer key")

The other two values you need are constant per organisation:

- **Issuer URL**: `https://api.asgardeo.io/t/<org>`
- **Discovery endpoint** (the SPA will use this to auto-fetch endpoints):
  `https://api.asgardeo.io/t/<org>/oauth2/token/.well-known/openid-configuration`

Hand these to the cloud-ui developer to drop into `cloud-ui/.env.local`:

```
VITE_OIDC_AUTHORITY=https://api.asgardeo.io/t/<org>
VITE_OIDC_CLIENT_ID=<the client ID from step 4>
VITE_OIDC_REDIRECT_URI=http://localhost:5173/callback
VITE_OIDC_SCOPE=openid profile email groups
VITE_API_BASE=http://localhost:8080
```

For production deployment behind `cloud.lk.internal.wso2.com`:

```
VITE_OIDC_REDIRECT_URI=https://cloud.lk.internal.wso2.com/callback
VITE_API_BASE=https://dcapi.lk.internal.wso2.com
```

---

## Step 5: Verify the group claim

DC-API reads exactly one group from the JWT: the platform-admin group
(`DCAPI_ADMIN_GROUP`, default `dc-admin`). Tenant membership comes from
DC-API's own role_assignments registry, never from groups. The SPA needs
the `groups` claim present in its tokens so admins are recognised.

1. App **Protocol** tab → **OpenID Connect** subsection → **Scopes** →
   ensure `groups` is checked.
2. App **User Attributes & Stores** tab → **OIDC Scopes** → ensure
   `Groups` is mapped under `groups` scope — **requested, not mandatory**:
   a mandatory `groups` attribute makes Asgardeo block group-less users at
   login with a "complete your profile" prompt, and most users hold no
   groups at all in this model.

The SPA only requests this scope; the DC-API server-side validation is
unchanged.

---

## Step 6: Tell dc-api to accept tokens from this SPA

Asgardeo issues tokens whose `aud` claim equals the requesting client's
own Client ID. So tokens minted for cloud-ui will have
`aud = <cloud-ui SPA Client ID>`, while tokens minted for dcctl have
`aud = <dcctl Client ID>`. dc-api's audience check is multi-valued —
`DCAPI_OIDC_AUDIENCE` is a comma-separated list — so we just add this
SPA's Client ID to the list.

> **Important:** all dc-api configuration on the live cluster is managed
> by Terraform in the `wso2-datacenter-project` repo. Do **not**
> `kubectl edit secret dc-api-secrets` directly — Terraform will overwrite
> your edit on the next apply. See the "Deployment is Terraform-driven"
> section in this repo's CLAUDE.md.

If the cloud-ui SPA was already added to `03-asgardeo-auth` (it ought to
be — that's how its `client_id` is created and why
`outputs.cloud_ui_client_id` exists), the audience plumbing reads it
automatically:

```hcl
# environments/lk-dev/02-dc-controlplane-services/dc-api.tf
oidc_audience = [
  data.terraform_remote_state.asgardeo_auth.outputs.client_id,           # dcctl / dc-api resource
  data.terraform_remote_state.asgardeo_auth.outputs.cloud_ui_client_id,  # cloud-ui SPA
]
```

Then apply:

```bash
cd wso2-datacenter-project/environments/lk-dev/02-dc-controlplane-services
terraform fmt
terraform validate
terraform plan      # expect: ~ kubernetes_secret.dc_api will be updated in-place
terraform apply
```

The `kubernetes_secret` change triggers an automatic rollout of the
`dc-api` Deployment (it references the secret via `envFrom`). Within
~30 seconds the new pod is up with both audiences honoured.

If `terraform_remote_state.asgardeo_auth.outputs.cloud_ui_client_id`
doesn't exist yet, add the SPA application + an output for it in
`03-asgardeo-auth/` first; that layer's `terraform apply` is a
prerequisite.

Without this step, cloud-ui logs in successfully with Asgardeo but
**every dc-api call returns 401 "Unauthorized: audience mismatch"**.
This is the most common stumbling block for this guide — if anything
goes wrong on the API side, check this first.

---

## Step 7: Sanity check (dev only — once cloud-ui implements login)

```bash
cd cloud-ui
cp .env.example .env.local      # fill in values
pnpm dev
# open http://localhost:5173, click "Sign in with Asgardeo"
# you should be redirected to Asgardeo, complete the flow, and land back
```

DevTools → Application → SessionStorage should show the OIDC client's
storage entries. The Network tab should show a `POST` to
`/oauth2/token` with `grant_type=authorization_code` and a PKCE
`code_verifier`.

If you see a CORS error, double-check Step 3.
If you see `invalid redirect_uri`, double-check Step 1.

---

## What NOT to do

- Don't reuse the `dcctl` client — its redirect URI is `localhost:8085`,
  Asgardeo will reject the browser callback as `invalid redirect_uri`.
- Don't use the **Standard-Based / Web Application** template — that's
  for confidential clients with a server holding a client secret.
  Browser apps are public.
- Don't store tokens in `localStorage`. The cloud-ui code uses
  `sessionStorage` (per `oidc-client-ts` defaults) so XSS can't survive
  a tab close.
- Don't enable the **Implicit grant** under any circumstance — it's
  deprecated and insecure for public clients.

---

## When EU/US datacenters come online

Add their hosts (e.g. `cloud.eu.internal.wso2.com`, plus the matching
`/callback`) to **both** the redirect URIs (Step 1) and the allowed origins
(Step 3). One Asgardeo client handles all regions; only the
`VITE_API_BASE` per-region build artifact differs.
