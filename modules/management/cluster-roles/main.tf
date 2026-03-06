# Cluster-level custom role templates.
# Apply once per Rancher instance; referenced by tenant-space role bindings.

# Grants read-only visibility into VM status and metrics for the Harvester
# dashboard. Intentionally excludes all mutating verbs (update, patch, delete)
# and subresources that control VM power state (start, stop, restart, migrate).
resource "rancher2_role_template" "vm_metrics_observer" {
  name        = "vm-metrics-observer"
  description = "Read-only access to VM status and metrics. Allows Harvester dashboard graphs without any control-plane permissions."
  context     = "project"

  # VirtualMachine and VirtualMachineInstance status — list/watch only
  rules {
    api_groups = ["kubevirt.io"]
    resources  = ["virtualmachines", "virtualmachineinstances"]
    verbs      = ["get", "list", "watch"]
  }

  # VM instance metrics subresource — required for Harvester dashboard graphs
  rules {
    api_groups = ["subresources.kubevirt.io"]
    resources  = ["virtualmachineinstances/metrics"]
    verbs      = ["get"]
  }

  # Service proxy — allows the Harvester UI to route metric scrape requests
  # through the kube-apiserver without direct pod access
  rules {
    api_groups = [""]
    resources  = ["services/proxy"]
    verbs      = ["get"]
  }
}
