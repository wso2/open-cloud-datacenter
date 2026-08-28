# registry

A Kubernetes operator that gives every namespace a private container registry,
backed by one Harbor deployment per namespace.

Applying a `Registry` in a namespace provisions Harbor into that namespace if it
has none yet, then carves out a Harbor project and push credentials for the
`Registry` that asked. Later `Registry` objects in the same namespace reuse that
Harbor and only add a project to it.

Runs on any conformant Kubernetes cluster — it uses no vendor APIs. Tested on
RKE2 v1.34.3 and kind v1.36.1.

## Requirements

| Requirement | Notes |
|:---|:---|
| An `IngressClass` | Harbor is exposed through it; name it with `INGRESS_CLASS` |
| A `StorageClass` | Must set `allowVolumeExpansion: true` for plan upgrades to grow storage |
| Egress from the operator and Harbor pods | The Harbor chart is pulled from `helm.goharbor.io`, images from their upstream registries |
| A cert-manager `ClusterIssuer` | Issues per-namespace TLS for Harbor's ingress. The operator verifies Harbor's certificate, so the issuer's CA must be trusted by the manager pod |

## Using it

```yaml
apiVersion: registry.opencloud.wso2.com/v1alpha1
kind: Registry
metadata:
  name: web
spec:
  plan: starter
```

```sh
kubectl apply -n acme-project-1 -f registry.yaml
kubectl get registries -n acme-project-1 -w
```

The namespace is not part of the manifest — every namespace is treated alike, so
`-n` alone decides which one gets the Harbor.

Once `Ready`, the Secret named in `.status.credentialsSecretName` holds
`robot_username`, `robot_secret`, `registry_url`, and `project` — everything a
CI pipeline needs to log in and push.

A `Registry` never names a Harbor deployment: the one serving it is always the
one in its own namespace. Nothing has to be prepared first — no label, no
annotation, no pre-created object — because the namespace already exists by the
time a `Registry` can be created in it.

## API

Group `registry.opencloud.wso2.com/v1alpha1`.

| Kind | Scope | Created by | Purpose |
|:---|:---|:---|:---|
| `Registry` | Namespaced | users | One Harbor project plus its robot credentials. `plan` sets the project's storage quota |
| `RegistryBackend` | Namespaced | the operator | One namespace's Harbor deployment, shared by every `Registry` in that namespace. Always named `harbor`. Holds deployment sizing |

Several `Registry` objects may exist per namespace, each mapping to the Harbor
project of the same name. Since a Harbor serves exactly one namespace and
Kubernetes forbids duplicate names within one, no two can collide.

## Configuration

Set on the manager Deployment (`config/manager/manager.yaml`), or layer
`config/local` over it — see `config/local/manager_local_patch.yaml.example` —
to keep cluster-specific values out of the tracked defaults.

| Variable | Required | Default | Purpose |
|:---|:---:|:---|:---|
| `BASE_DOMAIN` | ✅ | — | Registry URLs are `registry.<namespace>.<BASE_DOMAIN>` |
| `STORAGE_CLASS` | | `longhorn` | StorageClass for Harbor's volumes |
| `INGRESS_CLASS` | | `nginx` | IngressClass for Harbor's ingress |
| `CERT_ISSUER` | | `letsencrypt-prod` | cert-manager `ClusterIssuer` |
| `HARBOR_CHART_VERSION` | | `1.19.2` | Harbor chart version. Do not set below `1.14.1` |
| `HARBOR_HELM_REPO` | | `https://helm.goharbor.io` | Chart repository |

> With **nip.io**, use the dash-separated form (`10-0-0-5.nip.io`). nip.io finds
> the address by scanning for four dot-separated octets anywhere in the name, so
> a namespace ending in a digit merges into that scan and resolves elsewhere.

## How it works

A controller-runtime operator with two reconcilers. The Custom Resource is the
single source of truth, all work happens inside the reconcile loop, and slow
external steps are polled with `RequeueAfter`. Leader election is on, so extra
replicas act as hot standbys.

**Registry** binds to the `RegistryBackend` in its own namespace — creating it
when the namespace has none — and once Harbor is ready creates the project,
converges its storage quota, mints a project-scoped robot account exactly once,
and writes the credentials Secret next to the `Registry`. Every `Registry` in a
namespace targets the same fixed backend name, so simultaneous first requests
converge on one deployment: they attempt the identical object and the API
server's uniqueness constraint settles the race.

**RegistryBackend** generates and pins every Harbor credential into a Secret
beside the pods that read it, installs Harbor with Helm into its own namespace,
expands plan-controlled volumes, waits for the API, applies system
configuration, and reports `Ready` with the URL. It never creates a namespace.
Because Harbor shares the namespace with the user's own workloads, every object
it manages there is selected by the Helm release labels, never by namespace
alone.

**Sizing** starts at the smallest plan and grows on its own. Harbor reports both
the storage it has promised to projects and what they consume, so the operator
raises the deployment size once commitments approach provisioned capacity —
`spec.plan` is a floor an administrator can raise, `status.effectivePlan` is
what is deployed. Growth is permanent, because expanding a PersistentVolumeClaim
cannot be undone.

**Garbage collection** is scheduled on every reconcile. Deleting a project
removes its manifests but leaves the blobs on disk, and those orphans belong to
no project's quota — so without a sweep they are invisible to the capacity
measurement above while still consuming the volume.

**Vulnerability scanning** runs daily against Trivy, an hour before the sweep. A
scan records what was known when it ran, so a repeating pass is what surfaces a
CVE published after an image was already pushed.

**Deletion** destroys data, and the guard rather than a policy field is what
protects it. Deleting a `Registry` removes its Harbor project and every image in
it, emptying the repositories first because Harbor refuses to delete a non-empty
project. Deleting a `RegistryBackend` is **refused** while any `Registry` exists
in its namespace; overriding that takes a deliberate annotation, which then
cascades to those Registries first — leaving them behind would let them
recreate the backend they depend on.

```sh
kubectl -n <ns> annotate registrybackend harbor registry.opencloud.wso2.com/force=true
```

**Upgrades** run Harbor's schema migration as a Helm pre-upgrade hook, so it
completes before any pod rolls and a failure aborts the upgrade rather than
half-applying it. Changing `HARBOR_CHART_VERSION` upgrades existing deployments
on their next reconcile. Harbor states that a version upgrade migrates the
schema and cannot be done without downtime, and that it cannot be rolled back.

**Lifecycle** is phase-based (`Provisioning → Ready → Failed / Terminating`,
empty until the first reconcile) with standard conditions, `observedGeneration`,
and Events. Finalizers guarantee cleanup: a `Registry` holds its finalizer until
Harbor confirms the project is gone, so an unreachable Harbor is never mistaken
for a completed deletion.

**TLS** for each Harbor is issued by cert-manager through ingress annotations in
the rendered Helm values. Each namespace's Harbor is reachable at
`registry.<namespace>.<BASE_DOMAIN>`.

**Access control** is plain Kubernetes RBAC. `registry-admin/editor/viewer`
ClusterRoles are meant to be bound inside a user's namespaces;
`registrybackend-admin/editor/viewer` are for platform administrators.

## Quickstart

```sh
make docker-build docker-push IMG=<registry>/registry-provisioner:<tag>
KUBECONFIG=<kubeconfig> make install
KUBECONFIG=<kubeconfig> make deploy IMG=<registry>/registry-provisioner:<tag>

kubectl apply -n <your-namespace> -k config/samples/
kubectl get registries -A -w
```

## Build / test / develop

```sh
make manifests generate fmt vet build   # regenerate CRDs + DeepCopy, build binary
make test                               # unit tests
make docker-build IMG=...               # build image
make install                            # apply CRDs using current kubeconfig
make deploy IMG=...                     # apply manager + RBAC
```

## Part of Open Cloud Datacenter

This component lives in the [WSO2 Open Cloud
Datacenter](https://github.com/wso2/open-cloud-datacenter) initiative, providing
managed container registry services.

## License

Apache-2.0
