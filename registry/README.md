# registry

A Kubernetes operator that provisions managed Harbor container registries as
tenant-isolated deployments on a Harvester HCI cluster. A `RegistryBackend`
custom resource maps to one Harbor Helm release; a `RegistryInstance` maps to
one Harbor project with scoped robot-account credentials.

Tested on **Harvester 1.7.1** (RKE2 v1.34.3).

## What it does

- **API**: `RegistryBackend` (one shared Harbor per tenant) and
  `RegistryInstance` (one Harbor project + robot account per registry) in group
  `registry.opencloud.wso2.com/v1alpha1`.
- **Pattern**: a pure controller-runtime operator. The Custom Resource is the
  single source of truth — there is **no database, no background worker, and no
  HTTP gateway**. Both reconcilers do all their work in the reconcile loop,
  polling slow external steps with `RequeueAfter`. Leader election is enabled,
  so running multiple replicas yields exactly one active reconciler (hot
  standby for failover).
- **RegistryBackend reconciler**: generates Harbor's admin + database passwords
  into an owned Secret, ensures the tenant's `<tenantID>-management` namespace
  exists (creating it if absent), installs Harbor via Helm into it, polls the
  Harbor API until it is ready, applies system configuration, and reports
  `Ready` with the registry URL.
- **RegistryInstance reconciler**: waits for the backend to be `Ready`, creates
  a Harbor project, mints a project-scoped robot account **once**, and writes
  the robot credentials into an **owner-referenced K8s Secret**
  (`registry-credentials-<name>`) that dc-api reads directly.
- **Lifecycle**: phase-based (`Pending → Provisioning → Ready →
  Failed / Terminating`) with standard status conditions, `observedGeneration`,
  and Kubernetes Events. Finalizers guarantee cleanup; a Backend refuses
  deletion while `RegistryInstance`s still reference it. `spec.reclaimPolicy`
  (`Retain` | `Delete`) controls whether Harbor's PVCs / project are destroyed
  on delete.
- **TLS**: per-tenant Harbor TLS issued via cert-manager (ingress annotations
  in the rendered Helm values).
- **Access control**: scaffolded `registryinstance-admin/editor/viewer` and
  `registrybackend-admin/editor/viewer` ClusterRoles. Authorization is pure
  Kubernetes RBAC.

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
