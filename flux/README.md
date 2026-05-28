# Flux GitOps — the OCD-shipped side

This directory is the **library**. It contains the shared shapes any
consumer of OCD can pull in and overlay:

```
flux/
├── infrastructure/        cluster add-ons shipped to every env
│   ├── sources/           HelmRepository sources
│   ├── sealed-secrets/    decrypts SealedSecret CRs
│   ├── cert-manager/      cert issuance for ingress
│   └── ingress-nginx/     ingress controller
└── platform/              dc-api stack — generic, placeholders only
    └── dc-api/base/       Deployment, Service, Ingress, ConfigMap, …
```

OCD does **not** ship a concrete cluster overlay. Each consumer keeps
their own per-environment overlay in their own repo (path convention:
`<consumer-repo>/environments/<env>/flux/`).

The consumer's overlay pulls these shared bits as a Kustomize remote
base pinned to an OCD release tag:

```yaml
# in the consumer repo's environments/<env>/flux/platform/kustomization.yaml
resources:
  - https://github.com/wso2/open-cloud-datacenter//flux/platform?ref=v0.9.0
  - ../sealed-asgardeo-m2m.yaml
  - ../sealed-rancher-token.yaml
patches:
  - target: { kind: ConfigMap, name: dc-api-config }
    patch: |
      - op: replace
        path: /data/DCAPI_OIDC_ISSUER
        value: "https://api.asgardeo.io/t/<your-org>/oauth2/token"
  …
```

For the boilerplate a new consumer copies into their own repo, see
[`examples/consumer/flux/`](../examples/consumer/flux/).

## How a change flows (consumer perspective)

1. Operator edits a YAML under their `environments/<env>/flux/`
   (or under `flux/platform/` in their OCD fork if they're contributing
   a generic improvement upstream).
2. Commit + push to their consumer repo.
3. Flux Source Controller polls their consumer repo every minute, sees
   the new commit.
4. Flux Kustomize Controller re-renders. If the overlay pulls OCD via
   a Kustomize remote base, Source Controller also keeps OCD's clone
   fresh (or pinned to the configured ref).
5. The delta gets applied to the cluster. ~30s.

## How an image rolls out

1. CI builds + pushes `ghcr.io/<consumer-org>/dc-api:main-<sha7>`.
2. Flux Image Reflector (running in the consumer's cluster) polls the
   registry. It only ever sees the consumer's image stream because the
   `ImageRepository` is overlayed with the consumer's org.
3. Flux Image Automation Controller writes the new tag back to the
   consumer's repo at the marked manifest. Auto-commit in dev,
   PR-gated in prod.
4. Source + Kustomize Controllers apply the bump. ~3 min.

The consumer never writes back to OCD. OCD-side image refs in
`flux/platform/dc-api/base/deployment.yaml` are pinned placeholders;
real image streams live in the consumer overlay.

## Versioning

Tag OCD releases (`v0.9.0`, `v0.10.0`, …). Consumers pin via
`?ref=vX.Y.Z` in their remote-base URL. To upgrade:

1. Bump `?ref=` to the new tag.
2. Read the OCD release notes.
3. Apply any breaking-change overlay patches in your consumer repo.
4. Commit + push. Flux applies.
