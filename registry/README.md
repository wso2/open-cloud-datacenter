# registry

A Kubernetes operator that gives every namespace a private container registry,
backed by one Harbor deployment per tenant on a Harvester HCI cluster.

A tenant is a Harvester project. Applying a `Registry` in any namespace of that
project provisions Harbor for the tenant if it has none yet, then carves out a
Harbor project and push credentials for the namespace that asked.

Tested on **Harvester 1.7.1** (RKE2 v1.34.3).

## Using it

```yaml
apiVersion: registry.opencloud.wso2.com/v1alpha1
kind: Registry
metadata:
  name: web
  namespace: acme-project-1
spec:
  plan: starter
  reclaimPolicy: Retain
```

```sh
kubectl apply -f registry.yaml
kubectl get registries -n acme-project-1 -w
```

Once `Ready`, the Secret named in `.status.credentialsSecretName` holds
`robot_username`, `robot_secret`, `registry_url`, and `project` — everything a
CI pipeline needs to log in and push.

The tenant comes from the namespace's Harvester project, so a `Registry` names
neither a tenant nor a Harbor deployment. A namespace that belongs to no project
has no tenant, and its `Registry` waits rather than resolving to one.

## API

Group `registry.opencloud.wso2.com/v1alpha1`.

| Kind | Scope | Created by | Purpose |
|:---|:---|:---|:---|
| `Registry` | Namespaced | tenants | One Harbor project plus its robot credentials. `plan` sets the storage quota; `reclaimPolicy` decides whether images survive deletion. |
| `RegistryBackend` | Cluster | the operator | One tenant's Harbor deployment, shared by every `Registry` in that tenant. Holds deployment sizing and lifecycle policy. |

Several `Registry` objects may exist per namespace. Each maps to the Harbor
project `<namespace>-<name>`, so no two can resolve to the same project.

## How it works

A controller-runtime operator with two reconcilers. The Custom Resource is the
single source of truth, all work happens inside the reconcile loop, and slow
external steps are polled with `RequeueAfter`. Leader election is on, so extra
replicas act as hot standbys.

**Registry** resolves its tenant from the namespace, binds to that tenant's
`RegistryBackend` — creating it when the tenant has none — and once Harbor is
ready creates the project, converges its storage quota, mints a project-scoped
robot account exactly once, and writes the credentials Secret next to the
`Registry`. The backend name is derived from the tenant, so simultaneous first
requests converge on one deployment.

**RegistryBackend** creates the tenant's Harbor namespace inside the tenant's
Harvester project, generates and pins every Harbor credential into a Secret
beside the pods that read it, installs Harbor with Helm, expands
plan-controlled volumes, waits for the API, applies system configuration, and
reports `Ready` with the URL.

**Sizing** starts at the smallest plan and grows on its own. Harbor reports both
the storage it has promised to projects and what they consume, so the operator
raises the deployment size once commitments approach provisioned capacity —
`spec.plan` is a floor an administrator can raise, `status.effectivePlan` is
what is deployed. Growth is permanent, because expanding a PersistentVolumeClaim
cannot be undone.

**Lifecycle** is phase-based (`Provisioning → Ready → Failed / Terminating`,
empty until the first reconcile) with standard conditions,
`observedGeneration`, and Events. Finalizers guarantee cleanup: a `Registry`
with `reclaimPolicy: Delete` holds its finalizer until the Harbor project is
really gone, and a `RegistryBackend` refuses to delete while any `Registry`
still depends on it. Removing the last `Registry` leaves the deployment running
and marks it idle, so shared image data is never destroyed as a side effect.

**TLS** for each tenant's Harbor is issued by cert-manager through ingress
annotations in the rendered Helm values.

**Access control** is plain Kubernetes RBAC. `registry-admin/editor/viewer`
ClusterRoles are meant to be bound inside a tenant's namespaces;
`registrybackend-admin/editor/viewer` are for platform administrators.

## Quickstart

```sh
make docker-build docker-push IMG=<registry>/registry-provisioner:<tag>
KUBECONFIG=<kubeconfig> make install
KUBECONFIG=<kubeconfig> make deploy IMG=<registry>/registry-provisioner:<tag>

kubectl apply -k config/samples/
kubectl get registries -A -w
```

Set `BASE_DOMAIN`, `STORAGE_CLASS`, and `CERT_ISSUER` for your cluster in
`config/manager/manager.yaml`, or layer `config/local` on top of it — see
`config/local/manager_local_patch.yaml.example` — to keep real values out of
the tracked defaults.

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
Datacenter](https://github.com/wso2/open-cloud-datacenter) initiative,
providing managed container registry services on Harvester HCI.

## License

Apache-2.0
