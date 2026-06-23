# registry

A Kubernetes operator that provisions managed Harbor container registries as
tenant-isolated deployments on a Harvester HCI cluster. One `RegistryInstance`
custom resource maps to one Harbor Helm release, a TLS-secured ingress, scoped
robot account credentials, and optional Prometheus monitoring.

Tested on **Harvester 1.7.1** (RKE2 v1.34.3).

## What it does

- **API**: `RegistryInstance` and `RegistryBackend` in group
  `registry.opencloud.wso2.com/v1alpha1`, namespaced. `RegistryBackend`
  represents the shared Helm/infra config; `RegistryInstance` represents one
  tenant's Harbor deployment.
- **Reconciler**: phase-based state machine (`Pending → Provisioning → Ready →
  Failed / Terminating`); idempotent and crash-safe via a Postgres-backed
  worker. Status surfaces the `loginServer`, `harborProject`, and robot account
  credentials through a K8s Secret read by dc-api.
- **Deploy worker**: a background goroutine pulls pending deployments from
  Postgres, installs Harbor via Helm into the tenant's management namespace
  (`<tenantID>-management`), polls until all Deployments are ready, bootstraps
  the Harbor API (configure → create project → create robot account), then
  writes a credentials Secret for dc-api.
- **HTTP gateway**: a thin REST layer (`/api/v1/registries`) that dc-api calls
  to trigger provisioning and fetch status — authenticated by forwarding the
  caller's bearer token to the K8s API server (same authn/RBAC/audit path as
  `kubectl`).
- **TLS**: per-tenant Harbor TLS cert issued from an `internal-ca`
  ClusterIssuer (cert-manager). The CA PEM travels with the robot credentials
  so clients can trust the registry without a public CA.
- **Tenant isolation**: each Harbor runs in its own namespace with a
  `deny-cross-tenant` NetworkPolicy. A hard-delete annotation
  (`registry.opencloud.wso2.com/hard-delete: "true"`) controls whether PVCs
  are removed on CR deletion.
- **Access control**: scaffolded `registryinstance-admin/editor/viewer` and
  `registrybackend-admin/editor/viewer` ClusterRoles aggregate into the
  built-in K8s roles. Authorization is pure Kubernetes RBAC.

## Quickstart

```sh
make docker-build docker-push IMG=<registry>/registry-provisioner:<tag>
KUBECONFIG=<harvester-kubeconfig> make install
KUBECONFIG=<harvester-kubeconfig> make deploy IMG=<registry>/registry-provisioner:<tag>

kubectl apply -k config/samples/
kubectl get registryinstances -A -w
```

## Build / test / develop

```sh
make manifests generate fmt vet build   # regenerate CRD + DeepCopy, build binary
make test                               # unit tests
make docker-build IMG=...              # build image
make install                            # apply CRDs using current kubeconfig
make deploy IMG=...                     # apply manager + RBAC
```

## Part of Open Cloud Datacenter

This component lives in the [WSO2 Open Cloud
Datacenter](https://github.com/wso2/open-cloud-datacenter) initiative,
providing managed container registry services on Harvester HCI.

## License

Apache-2.0
