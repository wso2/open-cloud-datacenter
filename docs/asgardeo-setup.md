# Asgardeo Setup Guide

How to configure a **personal Asgardeo account** as the identity provider for DC-API and dcctl.

Asgardeo is WSO2's cloud IAM. The free tier is sufficient for development and personal use.

> **Provider-agnostic note**: DC-API uses standard OIDC. If you prefer Keycloak,
> Okta, or another IdP, the API server config is identical — only the issuer URL and
> client IDs change. This guide covers Asgardeo specifically.

---

## Overview of what we're creating

```
Asgardeo organization: "myorg"
│
├── Application: "DC-API Resource"       ← The resource server (validates tokens)
│   └── API Resource with identifier "dc-api"
│
├── Application: "dcctl"                  ← The CLI client (public, PKCE)
│   └── Redirect URI: http://localhost:8085/callback
│
├── Groups
│   ├── dc-admin                          ← Platform admins
│                                          (tenants live in dc-api's DB, not in groups)
│
└── User: you
    └── Member of: dc-admin
```

---

## Step 1: Create an Asgardeo account

1. Go to **[https://asgardeo.io](https://asgardeo.io)** and click **Sign Up**.
2. Complete registration. You'll be prompted to create an **organization name** — choose something short and lowercase, e.g., `hirandc`. This becomes part of your issuer URL.
3. Note your organization name — you'll use it everywhere below as `<org>`.

**Your issuer URL**: `https://api.asgardeo.io/t/<org>`

---

## Step 2: Create the DC-API application (Resource Server)

This application represents DC-API in Asgardeo. Tokens issued by dcctl will have this application's identifier as their audience (`aud` claim), which DC-API validates.

1. In the Asgardeo Console, go to **Applications** → **New Application**.
2. Choose **Standard-Based Application**.
3. Fill in:
   - **Name**: `DC-API`
   - **Protocol**: `OAuth2 / OpenID Connect`
4. Click **Register**.
5. On the application's **Protocol** tab:
   - Under **Allowed Grant Types**, enable **Client Credentials** (for M2M) and **JWT Bearer**.
   - Under **Access Token**, set **Token type** to `JWT`.
6. Go to the **Info** tab and note the **Client ID** — this is your `DCAPI_OIDC_AUDIENCE`.
7. Click **Save**.

> **Why a separate DC-API app?** It acts as the resource server identifier. When dcctl
> gets a token, it requests the `dc-api` audience so that DC-API knows the token was
> intended for it (not for some other service). This is standard OAuth2 audience restriction.

---

## Step 3: Create the dcctl application (CLI client)

This is a **public client** — it has no client secret. dcctl uses PKCE to prove it's the same process that started the login flow.

1. Go to **Applications** → **New Application**.
2. Choose **Mobile Application** (this maps to "native/public client" in OAuth2 terms).
3. Fill in:
   - **Name**: `dcctl`
4. Click **Register**.
5. On the **Protocol** tab:
   - **Allowed Grant Types**: enable **Authorization Code** only.
   - **Redirect URIs**: add `http://localhost:8085/callback`
   - **PKCE**: set to **Mandatory** (this is the security mechanism for public clients).
   - Under **Access Token** → **Token Binding**: leave as default.
   - Under **Requested Audience**: add the **Client ID** of your `DC-API` application from Step 2.
     - This ensures dcctl's tokens have `aud: <dc-api-client-id>`, which DC-API validates.
6. On the **Info** tab, note the **Client ID** — this is your dcctl `client_id` config value.
7. Click **Save**.

---

## Step 4: Configure the groups claim in tokens

By default, Asgardeo does not include group membership in tokens. We need to enable this so DC-API can map your groups to a tenant.

1. Go to **Applications** → select **dcctl**.
2. Go to the **User Attributes** tab.
3. Under **User Attribute**, click **Add User Attributes**.
4. Search for **groups** and add it.
5. Check the **Requested** checkbox next to `groups`.
6. Under **Mandated** — leave unchecked (it's fine to have it as optional).
7. Click **Save**.

Repeat this for the **DC-API** application (Step 2) if you plan to use client credentials flow later.

---

## Step 5: Create groups

1. Go to **User Management** → **Groups** → **New Group**.
2. Create the following groups:

   | Group Name              | Purpose                        |
   |-------------------------|--------------------------------|
   | `dc-admin`              | Platform administrators        |


3. Click **Save** for each group.

---

## Step 6: Assign yourself to groups

1. Go to **User Management** → **Users**.
2. Click on your user account.
3. Go to the **Groups** tab.
4. Assign yourself to `dc-admin`. (This is the ONLY group dc-api interprets — tenants are registered via `POST /v1/admin/tenants` / `dcctl admin tenant create`, and members are invited by email.)
5. Click **Save**.

---

## Step 7: Configure DC-API

Update your DC-API environment variables:

```bash
# Your Asgardeo issuer
export DCAPI_OIDC_ISSUER="https://api.asgardeo.io/t/<org>"

# Client ID from the DC-API application (Step 2)
export DCAPI_OIDC_AUDIENCE="<dc-api-client-id>"

# Group mapping (defaults match what we created above — no change needed)
# export DCAPI_ADMIN_GROUP="dc-admin"
```

---

## Step 8: Configure dcctl

```bash
cat > ~/.dcctl/config.yaml <<EOF
dcapi_url: "http://localhost:8080"
oidc_issuer: "https://api.asgardeo.io/t/<org>"
client_id: "<dcctl-client-id-from-step-3>"
callback_port: 8085
EOF
```

---

## Step 9: Test login

```bash
dcctl login
# Opens browser → Asgardeo login page
# After login:
# Login successful!
#   User:   <your-asgardeo-sub>
#   Tenant: <yourname>
```

If `GET /v1/tenants` comes back empty, you haven't been invited to any tenant yet (or, as an admin, none are registered) — register a tenant and invite members.

---

## Troubleshooting

### "OIDC discovery failed"
- Check that `DCAPI_OIDC_ISSUER` exactly matches your org name (case-sensitive).
- Test manually: `curl https://api.asgardeo.io/t/<org>/.well-known/openid-configuration`

### "Unauthorized: invalid token"
- The token audience doesn't match `DCAPI_OIDC_AUDIENCE`.
- Verify that the dcctl app has `DC-API`'s Client ID in its **Requested Audience** list (Step 3, point 5).

### Groups claim missing from token
- Revisit Step 4 — the `groups` attribute must be added to both the dcctl application's User Attributes.
- After saving, log out and run `dcctl login` again (cached tokens don't have the new claim).

### Token expired
- dcctl stores a refresh token and auto-refreshes. If it fails, run `dcctl login` again.

---

## Using a different identity provider

To use Keycloak, Okta, or any other OIDC provider instead of Asgardeo:

1. Create a **public client** (Authorization Code + PKCE) for dcctl.
2. Create an **API resource** or **audience** for DC-API.
3. Create a `dc-admin` group for platform admins (or change `DCAPI_ADMIN_GROUP`
   to match your convention). No other groups are needed — tenant membership is
   managed inside dc-api via invites.
4. Update the two environment variables:
   ```bash
   export DCAPI_OIDC_ISSUER="<your-idp-issuer>"
   export DCAPI_OIDC_AUDIENCE="<dc-api-audience-identifier>"
   ```

No code changes needed.
