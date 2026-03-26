# modules/monitoring

Terraform module that wires up a complete monitoring stack on top of the
`rancher-monitoring` (kube-prometheus-stack) add-on that ships with every
Harvester cluster. A single `module` block deploys PrometheusRules,
Alertmanager configuration, a Google Chat notification relay, and Grafana
dashboards — all configurable via input variables.

## Prerequisites

- `rancher-monitoring` add-on installed on the Harvester cluster
- Google Chat Space with an incoming webhook URL
- `kubectl` available in the Terraform execution environment (used by
  `null_resource` provisioners to patch the Alertmanager Secret)

## Usage

```hcl
module "monitoring" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/monitoring?ref=v0.4.0"

  environment             = "lk"
  kubeconfig_path         = "/path/to/harvester.kubeconfig"
  kubeconfig_context      = "local"
  google_chat_webhook_url = var.google_chat_webhook_url

  # Optional — show a "View Alert" deep-link button in each notification card.
  # Find this URL: Harvester UI → Add-ons → rancher-monitoring → alert-manager
  # alertmanager_url = "https://<harvester-ip>/api/v1/namespaces/cattle-monitoring-system/services/http:rancher-monitoring-alertmanager:9093/proxy"
}
```

---

## Architecture

```
Prometheus (rancher-monitoring)
   │  evaluates PrometheusRule CRDs labelled release=rancher-monitoring
   ▼
Alertmanager (rancher-monitoring)
   │  matches severity label → route → receiver "google-chat"
   │  webhook_configs: http://calert.cattle-monitoring-system:6000/create
   ▼
calert  (Deployment — ghcr.io/mr-karan/calert)
   │  accepts Alertmanager webhook POST, renders Google Chat Cards v2
   ▼
Google Chat Space (incoming webhook)
```

### Resources created

| Resource | Kubernetes kind | Name pattern | Namespace |
|---|---|---|---|
| Alertmanager config | Secret | `alertmanager-rancher-monitoring-alertmanager` | `cattle-monitoring-system` |
| calert config + template | Secret | `calert-config` | `cattle-monitoring-system` |
| calert | Deployment + Service | `calert` | `cattle-monitoring-system` |
| Storage alerts | PrometheusRule | `{env}-harvester-storage-alerts` | `cattle-monitoring-system` |
| VM alerts | PrometheusRule | `{env}-harvester-vm-alerts` | `cattle-monitoring-system` |
| Node alerts | PrometheusRule | `{env}-harvester-node-alerts` | `cattle-monitoring-system` |
| Storage dashboard | ConfigMap | `{env}-harvester-storage-dashboard` | `cattle-dashboards` |
| VM dashboard | ConfigMap | `{env}-harvester-vm-dashboard` | `cattle-dashboards` |
| Node dashboard | ConfigMap | `{env}-harvester-node-dashboard` | `cattle-dashboards` |

---

## Design decisions

### Why direct Secret injection instead of AlertmanagerConfig CRD

`AlertmanagerConfig` v1alpha1 silently drops any field it does not recognise —
including `googleChatConfigs`. The module therefore patches the
`alertmanager-rancher-monitoring-alertmanager` Secret directly using a
`null_resource` + `kubectl apply`. Prometheus Operator watches the Secret and
hot-reloads Alertmanager within ~30 s of any change.

The `kubernetes_manifest` resource is not used for this Secret because
rancher-monitoring Helm pre-creates it; a `kubernetes_manifest` would fail
with "already exists" on the first `terraform apply`.

### calert as a Google Chat relay

Google Chat does not have a native Alertmanager receiver. calert
(`ghcr.io/mr-karan/calert`) is a purpose-built relay that accepts the standard
Alertmanager webhook payload and reformats it into Google Chat Cards v2 JSON.

### Hot-reload

The calert Deployment carries a `checksum/config` annotation computed from the
rendered config and message template. Any `terraform apply` that changes the
template or config content automatically triggers a rolling restart — no manual
pod deletion required.

### PrometheusRule label selector

Prometheus Operator discovers PrometheusRule CRDs via its `ruleSelector`. The
rancher-monitoring Helm chart configures this selector to match
`release=rancher-monitoring`. Every PrometheusRule created by this module uses
`local.rule_labels`, which merges `release=rancher-monitoring` with the common
`managed_by` and `environment` labels. **Omitting `local.rule_labels` from a
PrometheusRule will cause Prometheus to ignore the rule entirely.**

---

## Alert inventory

### Storage (`prometheus_rule_storage`)

| Alert name | Severity | Condition |
|---|---|---|
| `LonghornVolumeFaulted` | critical | Volume state = Faulted |
| `LonghornVolumeDegradedWarning` | warning | Volume degraded for 15 m |
| `LonghornVolumeDegradedCritical` | critical | Volume degraded for 60 m |
| `LonghornVolumeReplicaCountLow` | warning | Healthy replica count < expected |
| `LonghornReplicaRebuildBacklog` | warning | Concurrent rebuilds per node > threshold |
| `LonghornEvictionWithDegradedVolumes` | critical | Disk eviction active + volumes degraded |
| `LonghornDiskUsageHigh` | warning / critical | Disk usage % above configurable threshold |

### VM / KubeVirt (`prometheus_rule_vm`)

| Alert name | Severity | Condition |
|---|---|---|
| `VirtLauncherPodStuck` | critical | virt-launcher pod Pending > `virt_launcher_stuck_for` |
| `VirtLauncherContainerCreating` | critical | virt-launcher stuck in ContainerCreating |
| `VirtLauncherCrashLoop` | critical | ≥ 3 restarts in 15 m |
| `HpVolumePodNotRunning` | critical | hotplug volume pod not Running > `hp_volume_stuck_for` |
| `HpVolumeMapDeviceFailed` | critical | exit status 32 (NFS/Block mode conflict) |
| `StaleVolumeAttachmentBlocking` | warning | CSI blocked by stale VolumeAttachment |

### Node (`prometheus_rule_node`)

| Alert name | Severity | Condition |
|---|---|---|
| `NodeCpuHigh` | warning / critical | CPU utilisation > configurable threshold |
| `NodeMemoryHigh` | warning | Memory utilisation > configurable threshold |

---

## How to add a new alert

### Option A — extend an existing rule group

This is the fastest path when the new alert belongs to an existing category
(storage, VM, or node).

1. Open [main.tf](main.tf) and locate the matching `kubernetes_manifest` block:
   - `prometheus_rule_storage` — Longhorn / disk
   - `prometheus_rule_vm` — KubeVirt / virt-launcher
   - `prometheus_rule_node` — node CPU / memory

2. Add a new map to the `rules` list inside the relevant group:

   ```hcl
   {
     alert = "MyNewAlert"           # PascalCase, [A-Za-z0-9_] only
     expr  = "my_metric > 0"        # valid PromQL — test in Grafana Explore first
     for   = "5m"                   # omit for instant (stateless) alerts
     labels = {
       severity = "warning"         # "warning" or "critical" — Alertmanager routes on this
     }
     annotations = {
       summary     = "One-line description shown in the card"
       description = "Detail with template vars: instance={{ $labels.instance }}, value={{ $value }}"
       runbook_url = "${var.runbook_base_url}/MyNewAlert"
     }
   }
   ```

3. Apply:

   ```bash
   terraform plan   # verify the rule diff looks correct
   terraform apply
   ```

### Option B — add a new PrometheusRule resource

Use this when the alert belongs to a distinct new category that warrants its
own Kubernetes object and Grafana dashboard.

```hcl
resource "kubernetes_manifest" "prometheus_rule_myapp" {
  manifest = {
    apiVersion = "monitoring.coreos.com/v1"
    kind       = "PrometheusRule"
    metadata = {
      name      = "${var.environment}-harvester-myapp-alerts"
      namespace = var.monitoring_namespace
      labels    = local.rule_labels   # required — carries release=rancher-monitoring
    }
    spec = {
      groups = [
        {
          name = "harvester.myapp"
          rules = [
            {
              alert = "MyAppDown"
              expr  = "up{job=\"myapp\"} == 0"
              for   = "2m"
              labels      = { severity = "critical" }
              annotations = {
                summary     = "MyApp is unreachable"
                description = "Instance {{ $labels.instance }} has been down for 2 m."
                runbook_url = "${var.runbook_base_url}/MyAppDown"
              }
            }
          ]
        }
      ]
    }
  }
}
```

Add a corresponding output in [outputs.tf](outputs.tf) and expose it through
the environment layer's `outputs.tf`.

### Verifying a rule was picked up

```bash
# List all PrometheusRule objects
kubectl get prometheusrules -n cattle-monitoring-system

# Confirm the rule name appears in Prometheus's loaded rule set
kubectl exec -n cattle-monitoring-system \
  $(kubectl get pod -n cattle-monitoring-system -l app=rancher-monitoring-prometheus -o name | head -1) \
  -- wget -qO- localhost:9090/api/v1/rules \
  | jq '.data.groups[].rules[].name' | grep MyNewAlert
```

### Test-firing an alert

```bash
# Port-forward Alertmanager
kubectl port-forward -n cattle-monitoring-system \
  svc/rancher-monitoring-alertmanager 9093:9093

# POST a synthetic alert
curl -s -X POST http://localhost:9093/api/v2/alerts \
  -H 'Content-Type: application/json' \
  -d '[{
    "labels":      { "alertname": "MyNewAlert", "severity": "warning" },
    "annotations": { "summary": "Test fire", "description": "Synthetic test" }
  }]'
```

A Google Chat card should appear within a few seconds.

---

## Notification card anatomy

Each alert produces one card. The card structure is defined in a Go template
stored in the `calert-config` Secret and rendered by calert at runtime.

```
┌─────────────────────────────────────────────────────┐
│ (WARNING) LonghornDiskUsageHigh | Firing             │  ← header
├─────────────────────────────────────────────────────┤
│ Summary: Disk usage on node-1 is 87%                 │  ┐
│ Description: Longhorn disk sdb on node-1 …           │  │ one decoratedText
│ Runbook: https://wiki.internal/runbooks/…            │  │ widget per annotation
├─────────────────────────────────────────────────────┤  ┘
│ ▶ Alert Details  (collapsible)                       │  ← all labels
├─────────────────────────────────────────────────────┤
│  [View Alert]   [View in Prometheus]                 │  ← buttons (optional)
└─────────────────────────────────────────────────────┘
```

**"View Alert" button** is only rendered when `alertmanager_url` is set. It
links to:
```
<alertmanager_url>/#/alerts?filter={alertname="<name>"}
```

**"View in Prometheus" button** is only rendered when the alert carries a
`GeneratorURL` (set automatically by Prometheus when a rule fires for real;
absent in synthetic test-fires).

### Template evaluation: Terraform vs Go

The card template is a Go template evaluated by calert at runtime — but it
lives inside a Terraform heredoc and is written to a Kubernetes Secret at
`terraform apply` time. This means two template engines interact:

| Syntax | Evaluated by | When |
|---|---|---|
| `${var.alertmanager_url}` | Terraform | at `terraform apply` |
| `%{~ if var.alertmanager_url != "" ~}` | Terraform | at `terraform apply` |
| `{{.Labels.alertname}}` | calert (Go template) | at alert runtime |
| `{{.Annotations.SortedPairs}}` | calert (Go template) | at alert runtime |

Terraform bakes the base URL as a literal string into the template file.
calert then substitutes the per-alert `alertname` at runtime. Both coexist
safely in the same heredoc because Terraform ignores `{{ }}` delimiters and
calert ignores `${ }` delimiters.

---

## Variable reference

### Required

| Name | Type | Description |
|---|---|---|
| `environment` | string | Short environment identifier used in resource names (`lk`, `prod`, …) |
| `kubeconfig_path` | string | Path to the Harvester kubeconfig file |
| `kubeconfig_context` | string | kubectl context within the kubeconfig |
| `google_chat_webhook_url` | string (sensitive) | Google Chat incoming webhook URL |

### Optional

| Name | Type | Default | Description |
|---|---|---|---|
| `alertmanager_url` | string | `""` | Alertmanager UI base URL — enables "View Alert" button. Leave empty to omit. |
| `monitoring_namespace` | string | `cattle-monitoring-system` | Namespace where rancher-monitoring runs |
| `dashboards_namespace` | string | `cattle-dashboards` | Namespace where Grafana picks up dashboard ConfigMaps |
| `runbook_base_url` | string | `https://wiki.internal/runbooks/harvester` | Base URL prepended to each alert's `runbook_url` annotation |
| `disk_usage_warning_pct` | number | `80` | Longhorn disk usage % — warning threshold |
| `disk_usage_critical_pct` | number | `90` | Longhorn disk usage % — critical threshold |
| `replica_rebuild_warning_count` | number | `5` | Concurrent Longhorn rebuilds per node before warning |
| `node_cpu_warning_pct` | number | `85` | Node CPU utilisation % — warning threshold |
| `node_cpu_critical_pct` | number | `95` | Node CPU utilisation % — critical threshold |
| `node_memory_warning_pct` | number | `85` | Node memory utilisation % — warning threshold |
| `virt_launcher_stuck_for` | string | `"5m"` | Duration virt-launcher must be Pending/ContainerCreating before alerting |
| `hp_volume_stuck_for` | string | `"3m"` | Duration hp-volume pod must be non-Running before alerting |

### Outputs

| Name | Description |
|---|---|
| `prometheus_rule_storage_name` | Name of the storage PrometheusRule |
| `prometheus_rule_vm_name` | Name of the VM PrometheusRule |
| `prometheus_rule_node_name` | Name of the node PrometheusRule |
| `alertmanager_config_name` | Name of the Alertmanager config Secret |
| `grafana_dashboard_storage_name` | Name of the storage Grafana dashboard ConfigMap |
| `grafana_dashboard_vm_name` | Name of the VM Grafana dashboard ConfigMap |
| `grafana_dashboard_node_name` | Name of the node Grafana dashboard ConfigMap |
| `monitoring_namespace` | Namespace all monitoring resources were deployed into |
