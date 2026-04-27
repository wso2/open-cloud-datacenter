# ── Required ──────────────────────────────────────────────────────────────────

variable "environment" {
  type        = string
  description = "Environment name used for resource naming (e.g. \"lk\")."
}

variable "kubeconfig_path" {
  type        = string
  description = "Path to the Harvester kubeconfig file."
}

variable "google_chat_webhook_url" {
  type        = string
  sensitive   = true
  description = "Google Chat incoming webhook URL for alert notifications."
}

variable "rancher_url" {
  type        = string
  default     = ""
  description = "Base URL of the Rancher server (e.g. https://rancher.example.com). Combined with harvester_cluster_id to build Rancher-authenticated proxy URLs for the 'View Alert' and 'View in Prometheus' buttons. Leave empty to omit both buttons."
}

variable "harvester_cluster_id" {
  type        = string
  default     = ""
  description = "Rancher cluster ID for the Harvester cluster (e.g. c-v7gvt). Found in Rancher UI → Cluster Management → cluster row. Required when rancher_url is set."
}

# ── Optional (monitoring namespaces) ─────────────────────────────────────────

variable "monitoring_namespace" {
  type        = string
  default     = "cattle-monitoring-system"
  description = "Namespace where rancher-monitoring (kube-prometheus-stack) runs."
}

variable "dashboards_namespace" {
  type        = string
  default     = "cattle-dashboards"
  description = "Namespace where Grafana picks up dashboard ConfigMaps (label grafana_dashboard=1)."
}

# ── Optional (alert thresholds) ───────────────────────────────────────────────

variable "runbook_base_url" {
  type        = string
  default     = "https://wiki.internal/runbooks/harvester"
  description = "Base URL prepended to each alert's runbook_url annotation."
}

variable "disk_usage_warning_pct" {
  type        = number
  default     = 80
  description = "Longhorn disk usage percentage that triggers a warning alert."
}

variable "disk_usage_critical_pct" {
  type        = number
  default     = 90
  description = "Longhorn disk usage percentage that triggers a critical alert."
}

variable "replica_rebuild_warning_count" {
  type        = number
  default     = 5
  description = "Number of concurrent replica rebuilds per node that triggers a warning."
}

variable "node_cpu_warning_pct" {
  type        = number
  default     = 85
  description = "Node CPU utilisation percentage that triggers a warning alert."
}

variable "node_cpu_critical_pct" {
  type        = number
  default     = 95
  description = "Node CPU utilisation percentage that triggers a critical alert."
}

variable "node_memory_warning_pct" {
  type        = number
  default     = 85
  description = "Node memory utilisation percentage that triggers a warning alert."
}

variable "virt_launcher_stuck_for" {
  type        = string
  default     = "5m"
  description = "Duration a virt-launcher pod must be Pending/ContainerCreating before alerting."
}

variable "hp_volume_stuck_for" {
  type        = string
  default     = "3m"
  description = "Duration an hp-volume pod must be non-Running before alerting."
}
