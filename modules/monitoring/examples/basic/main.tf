# Example: Deploy the monitoring stack for a Harvester + Rancher environment.
#
# Prerequisites:
#   - rancher-monitoring (kube-prometheus-stack) deployed on the Harvester cluster.
#     Verify: kubectl get pods -n cattle-monitoring-system
#   - Harvester kubeconfig downloaded (Harvester UI → Support → Download KubeConfig).
#   - Google Chat incoming webhook URL created
#     (Chat Space → Apps & Integrations → Webhooks → Add webhook).
#
# Apply:
#   export TF_VAR_google_chat_webhook_url="https://chat.googleapis.com/..."
#   terraform init && terraform apply

terraform {
  required_version = ">= 1.5"

  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 3.0"
    }
  }
}

provider "kubernetes" {
  config_path    = var.kubeconfig_path
  config_context = var.kubeconfig_context
}

module "monitoring" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/monitoring?ref=v0.3.0"

  # Identifiers
  environment        = "lk"
  kubeconfig_path    = var.kubeconfig_path
  kubeconfig_context = var.kubeconfig_context

  # Notification
  google_chat_webhook_url = var.google_chat_webhook_url

  # Thresholds (optional — all have sensible defaults)
  disk_usage_warning_pct        = 80
  disk_usage_critical_pct       = 90
  replica_rebuild_warning_count = 5
  node_cpu_warning_pct          = 85
  node_cpu_critical_pct         = 95
  node_memory_warning_pct       = 85
  virt_launcher_stuck_for       = "5m"
  hp_volume_stuck_for           = "3m"

  # Runbook base URL (optional)
  runbook_base_url = "https://wiki.internal/runbooks/harvester"
}

output "monitoring_resources" {
  value = {
    prometheus_rule_storage = module.monitoring.prometheus_rule_storage_name
    prometheus_rule_vm      = module.monitoring.prometheus_rule_vm_name
    prometheus_rule_node    = module.monitoring.prometheus_rule_node_name
    alertmanager_config     = module.monitoring.alertmanager_config_name
  }
}

variable "kubeconfig_path" {
  type    = string
  default = "~/.kube/harvester-lk.yaml"
}

variable "kubeconfig_context" {
  type    = string
  default = "local"
}

variable "google_chat_webhook_url" {
  type      = string
  sensitive = true
}
