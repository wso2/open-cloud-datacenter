# Module: management/harvester-integration

Registers the Harvester HCI cluster into Rancher by enabling the Harvester feature flag, installing the UI extension, creating a cloud credential, patching CoreDNS, and applying the registration manifest.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| rancher/rancher2 | ~> 8.0.0 |
| harvester/harvester | ~> 0.6.0 |
| hashicorp/kubernetes | ~> 2.30.0 |

## Usage

```hcl
module "harvester_integration" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/harvester-integration?ref=v0.1.0"

  harvester_kubeconfig  = file("~/.kube/harvester-config")
  rancher_hostname      = "rancher.example.internal"
  rancher_lb_ip         = "192.168.10.10"
  harvester_cluster_name = "harvester-hci"
}
```

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| harvester_kubeconfig | Content of the Harvester kubeconfig file | `string` | n/a | yes |
| harvester_cluster_name | Name for the Harvester cluster in Rancher | `string` | `"harvester-hci"` | no |
| rancher_hostname | The FQDN of the Rancher server | `string` | n/a | yes |
| rancher_lb_ip | The IP address of the Rancher LoadBalancer | `string` | n/a | yes |

## Outputs

This module does not define explicit outputs. Key resources created include:

- `rancher2_cluster.harvester_hci` — the imported cluster entry in Rancher
- `rancher2_cloud_credential.harvester` — cloud credential for provisioning workload clusters
- `harvester_setting.registration_url` — the cluster registration URL applied to Harvester
