# Key Decisions

Foundational decisions for dc-api. Don't re-debate these without a good reason —
each one is here because the alternative was tried, considered, or has a known
failure mode.

1. **Async provisioning.** `POST /v1/.../virtual-machines` returns `202 Accepted`
   immediately; the caller polls `GET /{id}` for status. VM creation takes
   2–5 minutes — a synchronous call would time out.

2. **dc-api generates SSH keys.** The caller never provides a public key. dc-api
   generates an ECDSA P-256 keypair, injects the public key via cloud-init, and
   returns the private key **once** in the create response. It is never stored
   server-side.

3. **PKCE, no client secret in dcctl.** The CLI binary is a public client.
   Embedding a secret in a distributed binary is insecure, so dcctl uses the
   OAuth Authorization Code + PKCE flow.

4. **Rancher REST API directly** (not the rancher2 Terraform provider). The TF
   provider has had RKE2 cluster-creation bugs; the REST v3 API is what the
   Rancher UI itself uses.

5. **Harvester via the Kubernetes dynamic client** (not an HTTP API). Harvester
   VMs are KubeVirt CRDs — applying CRs through the Kubernetes API is the
   native interface.

6. **One PostgreSQL row per resource.** State is owned by dc-api, not by
   Harvester/Rancher. Having a canonical registry is what makes drift detection
   and quota enforcement possible.

7. **Go module path `github.com/wso2/dc-api`.** The logical module name is fixed
   independent of where the repo is hosted; change it with
   `go mod edit -module` only if the canonical import path changes.
