# Consumer Flux overlay (host cluster) — template

The **second** Flux overlay: this one runs on the **Harvester host cluster**,
the sibling of `../flux/` (which runs on the control-plane / management
cluster). It reconciles the OCD operators that belong on the host cluster —
the things that need in-cluster access to the nodes and KubeVirt VMs:
keyvault-operator today, and dc-agent / a database-operator next.

It is a dedicated Flux instance, **not** Rancher Fleet — the host cluster's
Fleet is left untouched. Bootstrap it with a `06-flux-bootstrap-harvester`
Terraform layer (`flux_bootstrap_git` pointed at the host cluster's local
kubeconfig), the analogue of the control-plane `06-flux-bootstrap`.

Copy this directory into your consumer repo at a per-env path:

```bash
cp -r examples/consumer/flux-harvester/ \
  ~/path/to/my-consumer-repo/environments/<env>/flux-harvester/
```

Then:

1. Replace every `CHANGE-ME` placeholder with your env's values (the env path
   in `platform.yaml` + `image-update-automation.yaml`, the commit author, and
   the image-tag pins in `platform-overlay/kustomization.yaml`).
2. Pin OCD to a release tag in `sources.yaml` + `platform-overlay/` (`?ref=`),
   in lock-step with `../flux/`.
3. Apply your `06-flux-bootstrap-harvester` TF layer (kubeconfig = the host
   cluster, NOT the Rancher proxy) — `flux_bootstrap_git` populates
   `flux-system/` and installs Flux on the host cluster.
4. Flux reconciles the operators from GHCR.

## What each file does

- `sources.yaml` — Flux `GitRepository` resources: OCD (the shared operator
  bases, pinned) and this consumer repo (the overlay + any sealed secrets).
- `platform.yaml` — Flux `Kustomization` reconciling `./platform-overlay/`.
- `platform-overlay/kustomization.yaml` — pulls each OCD operator base as a
  remote Kustomize base (one line per operator) and patches it for the host
  cluster.
- `image-update-automation.yaml` — commits operator image-tag bumps back to
  this repo as CI publishes new tags.
- `flux-system/` — populated by `flux_bootstrap_git` on apply
  (gotk-components / gotk-sync / kustomization). Empty until then; don't
  hand-edit. It **must** stay listed in `kustomization.yaml` or the root
  prune deletes Flux's own components.

## No infrastructure layer

Unlike `../flux/`, there is no `infrastructure.yaml`. The host cluster runs
operators, not ingress workloads — no cert-manager / ingress-nginx /
sealed-secrets needed. The operator images are public GHCR packages, so the
platform overlay patches out the bases' `ghcr-pull-secret` references rather
than sealing a pull secret. Add an `infrastructure.yaml` (pulling OCD's
`flux/infrastructure/sealed-secrets`) only if a future operator needs a sealed
secret — e.g. dc-agent, whose `dcagent_` channel token is sealed in.

## Adding an operator (e.g. dc-agent)

1. Add the operator's OCD base to `platform-overlay/kustomization.yaml`
   `resources:` (one line, `?ref=` matching `sources.yaml`).
2. Add a matching `images:` entry with the `{"$imagepolicy": ...:tag}` setter
   marker so image-automation bumps it.
3. If the operator needs a secret (dc-agent's token) or a private image, add an
   `infrastructure.yaml` for sealed-secrets and seal it into `platform-overlay/`.
