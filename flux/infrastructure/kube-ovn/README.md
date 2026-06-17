# KubeOVN (host-cluster SDN) — Flux base

Installs **upstream KubeOVN** (`kubeovn/kube-ovn`, pinned `v1.15.4`) as a
**Multus secondary CNI** on the Harvester host cluster. This is the SDN dc-api
drives for tenant VPC networking — it creates `Vpc` / `Subnet` /
`VpcNatGateway` / `NetworkAttachmentDefinition` CRDs against this install at
runtime.

This base is for a **host cluster** (the one running KubeVirt VMs), not the
control-plane workload cluster. A host-cluster Flux overlay references it; the
control-plane cluster's `flux/infrastructure/kustomization.yaml` does **not**.

## Single owner — disable the Harvester bundled add-on first

KubeOVN's CRDs are cluster-scoped and the OVN central DB is a singleton. Two
installs **fight** regardless of namespace. Harvester ships a bundled
`kubeovn-operator` **Addon** (plus a Rancher Fleet `managedchart` for its CRDs)
that renders its own kube-ovn into `kube-system`. Before this chart reconciles,
that bundled install MUST be removed on the host cluster:

```bash
# Disable + delete the Harvester Addon (stops its helm-controller reconcile)
kubectl -n kube-system patch addon kubeovn-operator --type=merge -p '{"spec":{"enabled":false}}'
kubectl -n kube-system delete addon kubeovn-operator
# Remove the Fleet-managed CRD chart
kubectl -n fleet-local delete managedchart kubeovn-operator-crd
# Uninstall whatever the operator/Fleet left behind, then this chart installs clean
helm -n kube-system uninstall kubeovn-operator kubeovn-operator-crd 2>/dev/null || true
```

(If a prior **manual** `helm install kube-ovn` exists, uninstall it too — this
Flux release replaces it as the single manager.)

## What a consumer overlay MUST set

The base omits two site-specific values; patch the `kube-ovn` HelmRelease in
your overlay:

| Value | What | Example |
|---|---|---|
| `spec.values.MASTER_NODES` | comma-separated control-plane node IP(s) for ovn-central | `"10.0.0.10"` |
| `spec.values.ipv4.SVC_CIDR` | the cluster's REAL Kubernetes service CIDR | `"10.43.0.0/16"` |

```yaml
# infrastructure-overlay/kustomization.yaml
resources:
  - https://github.com/wso2/open-cloud-datacenter//flux/infrastructure/kube-ovn?ref=controlplane
patches:
  - patch: |-
      apiVersion: helm.toolkit.fluxcd.io/v2
      kind: HelmRelease
      metadata:
        name: kube-ovn
        namespace: kube-ovn
      spec:
        values:
          MASTER_NODES: "10.0.0.10"
          ipv4:
            SVC_CIDR: "10.43.0.0/16"
```

## The external (SNAT/EIP) network is NOT here

The `ProviderNetwork` + `Vlan` + `ovn-vpc-external-network` Subnet that VPC
EIPs are SNAT'd through are created by **dc-api at runtime**
(`EnsureExternalNetworkBootstrap`), driven by `DCAPI_VPC_EXTERNAL_*` config.
This chart only installs the kube-ovn control/data plane. After a fresh
install, restart dc-api so it re-creates the external network. Ensure
`DCAPI_VPC_EXTERNAL_RESERVED_IPS` lists the host node IPs + any VIPs so
kube-ovn's IPAM never hands them out.
