# Module: workloads/harvester-vm-access

Provisions a namespace-scoped ServiceAccount on Harvester and outputs a kubeconfig that
a consumer team can use to configure the `harvester` Terraform provider. Enables the team
to provision VMs inside their assigned namespace **without** needing cluster-admin credentials.

Run this module **once per consumer team** from the infra/platform layer after `tenant-space`
has created the namespace. Hand the `kubeconfig` output to the consumer team securely (e.g.
a shared password manager entry or a secrets manager secret).

## Permissions granted

The ServiceAccount receives the minimum set of namespace-scoped role bindings needed to
create and manage VMs:

| Binding | Scope | Grants |
|---------|-------|--------|
| `edit` (Kubernetes built-in) | consumer namespace | kubevirt VMs, PVCs, Secrets, ConfigMaps |
| `harvesterhci.io:edit` | consumer namespace | Harvester keypairs, VM backups, NADs |
| `harvesterhci.io:view` | `default` namespace | read shared OS images |
| `harvesterhci.io:view` | `harvester-public` namespace | read public OS images |

No cluster-wide write access is granted. The consumer cannot modify infrastructure outside
their namespace.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.7 |
| hashicorp/kubernetes | ~> 2.35 |

## Usage (infra/platform layer)

```hcl
module "asgardeo_vm_access" {
  source = "github.com/wso2/open-cloud-datacenter//modules/workloads/harvester-vm-access?ref=v0.8.0"

  providers = {
    kubernetes.harvester = kubernetes.harvester
  }

  vm_namespace         = "asgardeo-dev"
  consumer_name        = "asgardeo-dev"
  harvester_api_server = "https://192.168.10.100:6443"
}

# Write kubeconfig to a local file for hand-off — gitignore this file.
resource "local_sensitive_file" "consumer_kubeconfig" {
  content  = module.asgardeo_vm_access.kubeconfig
  filename = "${path.module}/asgardeo-dev.kubeconfig.secret"
}
```

Or expose the kubeconfig as a root output and retrieve it:

```hcl
output "asgardeo_dev_kubeconfig" {
  value     = module.asgardeo_vm_access.kubeconfig
  sensitive = true
}
```

```bash
terraform output -raw asgardeo_dev_kubeconfig > asgardeo-dev.kubeconfig.secret
```

## Inputs

| Name | Description | Type | Required |
|------|-------------|------|----------|
| `vm_namespace` | Harvester namespace assigned to the consumer team | `string` | yes |
| `consumer_name` | Short identifier for the team (lowercase, hyphen-separated) | `string` | yes |
| `harvester_api_server` | Direct Harvester kube-apiserver URL (port 6443, not Rancher proxy) | `string` | yes |

## Outputs

| Name | Description | Sensitive |
|------|-------------|-----------|
| `kubeconfig` | Kubeconfig YAML for the consumer team's Harvester provider | yes |
| `service_account_name` | ServiceAccount name created in the consumer namespace | no |

## Notes

- `harvester_api_server` must be the **direct** Harvester kube-apiserver URL (port 6443).
  Do not use the Rancher proxy URL (`https://<rancher>/k8s/clusters/local`, port 443) — tenant
  nodes may not have port 443 access to the management VIP.
- The output kubeconfig contains a long-lived ServiceAccount token. Treat it as a secret.
  Do not commit it to version control. Write to a gitignored file or a secrets manager.
- To rotate the credential: `terraform taint module.<name>.kubernetes_secret_v1.token`
  then `terraform apply`. Taint only marks the resource for replacement — the old token
  remains valid until `terraform apply` deletes and recreates the Secret.
- This module does **not** grant Rancher project access. The consumer still needs a Rancher
  API token (personal or M2M) for the `rancher2` provider, which the Rancher admin provisions
  separately via the Rancher UI.
