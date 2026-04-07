# ── Common locals ─────────────────────────────────────────────────────────────

locals {
  common_labels = {
    managed_by  = "terraform"
    environment = var.environment
  }

  # PrometheusRule resources must carry release=rancher-monitoring so that
  # rancher-monitoring's Prometheus picks them up via its ruleSelector.
  rule_labels = merge(local.common_labels, {
    release = "rancher-monitoring"
  })

  # AlertmanagerConfig and template resources go in the monitoring namespace.
  ns  = var.monitoring_namespace
  dns = var.dashboards_namespace

  # Rancher-authenticated proxy base for this Harvester cluster.
  # e.g. https://rancher.example.com/k8s/clusters/c-v7gvt
  # Both button URLs are derived from this so they pass through Rancher auth
  # rather than hitting the Harvester IP directly (which returns 403 to users
  # who are not separately authenticated to Harvester).
  rancher_proxy_base    = (var.rancher_url != "" && var.harvester_cluster_id != "") ? "${var.rancher_url}/k8s/clusters/${var.harvester_cluster_id}" : ""
  alertmanager_base_url = local.rancher_proxy_base != "" ? "${local.rancher_proxy_base}/api/v1/namespaces/${var.monitoring_namespace}/services/http:rancher-monitoring-alertmanager:9093/proxy" : ""
  prometheus_base_url   = local.rancher_proxy_base != "" ? "${local.rancher_proxy_base}/api/v1/namespaces/${var.monitoring_namespace}/services/http:rancher-monitoring-prometheus:9090/proxy" : ""
}

# ── Alertmanager config + calert (Google Chat webhook forwarder) ──────────────
# google_chat_configs is not a native Alertmanager receiver type. The standard
# pattern is: Alertmanager webhook_configs → calert → Google Chat API.
# calert (ghcr.io/mr-karan/calert) is a purpose-built forwarder that accepts
# the Alertmanager webhook payload and reformats it for Google Chat.
#
# Prometheus Operator watches the base alertmanager Secret and hot-reloads
# Alertmanager within ~30s of any change. The Secret is written via kubectl
# apply (idempotent create-or-update) since rancher-monitoring Helm pre-creates
# it and kubernetes_manifest would fail with "already exists" on fresh clusters.

locals {
  # calert config.toml — rendered with the webhook URL and stored in a Secret.
  calert_config_toml = <<-TOML
    [app]
    address = "0.0.0.0:6000"
    server_timeout = "30s"
    log = "info"

    [providers.gchat]
    type = "google_chat"
    endpoint = "${var.google_chat_webhook_url}"
    template = "/etc/calert/message.tmpl"
    timeout = "20s"
    thread_ttl = "12h"
    threaded_replies = false
    dry_run = false
  TOML

  # calert message template — Go template that produces a Google Chat Cards v2
  # JSON array. calert detects the named "cardsV2" block and uses it to build
  # a rich card instead of a plain text message.
  #
  # Template data (Alertmanager webhook payload):
  #   .Alerts[]        — slice of firing/resolved alerts
  #     .Fingerprint   — unique alert ID
  #     .Status        — "firing" | "resolved"
  #     .Labels        — map[string]string  (alertname, severity, …)
  #     .Annotations   — map[string]string  (summary, description, runbook_url, …)
  #     .GeneratorURL  — link to the Prometheus graph that fired
  #   .ExternalURL     — base URL of the Alertmanager instance
  # Template context: single alertmgrtmpl.Alert rendered per alert.
  # Output: single JSON object → chatv1.CardWithId (no array wrapper).
  # Section-level headers (strings) only — card-level header objects are not
  # supported by Google Chat webhooks and produce blank cards.
  # Available functions: toUpper, Title, SortedPairs (calert v2.3.0)
  # local.alertmanager_base_url / local.prometheus_base_url are Terraform
  # interpolations — baked in at apply time as literal strings. The Go template
  # vars ({{.Labels.alertname}} etc.) are resolved at runtime per alert.
  # Both coexist safely in the same heredoc.
  calert_message_tmpl = <<-TMPL
    {{- define "cardsV2" -}}
    {
      "card": {
        "sections": [
          {
            "header": "({{.Labels.severity | toUpper}}) {{.Labels.alertname | Title}} | {{.Status | Title}}",
            "widgets": [
              {{- range $i, $pair := .Annotations.SortedPairs -}}
              {{- if ne $i 0 -}},{{- end -}}
              {"decoratedText": {"text": "{{ $pair.Name | Title }}: {{ $pair.Value }}"}}
              {{- end -}}
              %{~if local.rancher_proxy_base != ""~}
              ,{"buttonList": {"buttons": [
                {"text": "View Alert", "onClick": {"openLink": {"url": "${local.alertmanager_base_url}/#/alerts?filter=%7Balertname%3D%22{{.Labels.alertname}}%22%7D"}}},
                {"text": "View in Prometheus", "onClick": {"openLink": {"url": "${local.prometheus_base_url}/alerts?search={{.Labels.alertname}}"}}}
              ]}}
              %{~endif~}
            ]
          },
          {
            "header": "Alert Details",
            "collapsible": true,
            "uncollapsibleWidgetsCount": 0,
            "widgets": [
              {{- range $i, $pair := .Labels.SortedPairs -}}
              {{- if ne $i 0 -}},{{- end -}}
              {"decoratedText": {"text": "{{ $pair.Name }}: {{ $pair.Value }}"}}
              {{- end -}}
            ]
          }
        ]
      }
    }
    {{- end -}}
  TMPL

  # Preserved from the rancher-monitoring Helm chart — used by any Slack/other
  # receivers configured via the Rancher UI. Keep it so those still work.
  rancher_defaults_tmpl = <<-TMPL
    {{- define "slack.rancher.text" -}}
    {{ template "rancher.text_multiple" . }}
    {{- end -}}

    {{- define "rancher.text_multiple" -}}
    *[GROUP - Details]*
    One or more alarms in this group have triggered a notification.

    {{- if gt (len .GroupLabels.Values) 0 }}
    *Group Labels:*
      {{- range .GroupLabels.SortedPairs }}
      • *{{ .Name }}:* `{{ .Value }}`
      {{- end }}
    {{- end }}
    {{- if .ExternalURL }}
    *Link to AlertManager:* {{ .ExternalURL }}
    {{- end }}

    {{- range .Alerts }}
    {{ template "rancher.text_single" . }}
    {{- end }}
    {{- end -}}

    {{- define "rancher.text_single" -}}
    {{- if .Labels.alertname }}
    *[ALERT - {{ .Labels.alertname }}]*
    {{- else }}
    *[ALERT]*
    {{- end }}
    {{- if .Labels.severity }}
    *Severity:* `{{ .Labels.severity }}`
    {{- end }}
    {{- if .Labels.cluster }}
    *Cluster:*  {{ .Labels.cluster }}
    {{- end }}
    {{- if .Annotations.summary }}
    *Summary:* {{ .Annotations.summary }}
    {{- end }}
    {{- if .Annotations.message }}
    *Message:* {{ .Annotations.message }}
    {{- end }}
    {{- if .Annotations.description }}
    *Description:* {{ .Annotations.description }}
    {{- end }}
    {{- if .Annotations.runbook_url }}
    *Runbook URL:* <{{ .Annotations.runbook_url }}|:spiral_note_pad:>
    {{- end }}
    {{- with .Labels }}
    {{- with .Remove (stringSlice "alertname" "severity" "cluster") }}
    {{- if gt (len .) 0 }}
    *Additional Labels:*
      {{- range .SortedPairs }}
      • *{{ .Name }}:* `{{ .Value }}`
      {{- end }}
    {{- end }}
    {{- end }}
    {{- end }}
    {{- with .Annotations }}
    {{- with .Remove (stringSlice "summary" "message" "description" "runbook_url") }}
    {{- if gt (len .) 0 }}
    *Additional Annotations:*
      {{- range .SortedPairs }}
      • *{{ .Name }}:* `{{ .Value }}`
      {{- end }}
    {{- end }}
    {{- end }}
    {{- end }}
    {{- end -}}
    TMPL

  alertmanager_config_yaml = yamlencode({
    global = {
      resolve_timeout = "5m"
    }

    route = {
      group_by        = ["alertname", "severity", "node", "volume"]
      group_wait      = "30s"
      group_interval  = "5m"
      repeat_interval = "12h"
      receiver        = "null"
      routes = [
        {
          matchers = ["alertname = \"Watchdog\""]
          receiver = "null"
        },
        {
          matchers        = ["severity = \"critical\""]
          receiver        = "google-chat-critical"
          repeat_interval = "1h"
        },
        {
          matchers        = ["severity = \"warning\""]
          receiver        = "google-chat-warning"
          repeat_interval = "4h"
        },
      ]
    }

    receivers = [
      { name = "null" },
      {
        name = "google-chat-critical"
        webhook_configs = [{
          url           = "http://calert.${local.ns}:6000/dispatch?room_name=gchat"
          send_resolved = true
        }]
      },
      {
        name = "google-chat-warning"
        webhook_configs = [{
          url           = "http://calert.${local.ns}:6000/dispatch?room_name=gchat"
          send_resolved = true
        }]
      },
    ]

    inhibit_rules = [
      # Rancher defaults — preserve existing rancher-monitoring behaviour.
      {
        source_matchers = ["severity = \"critical\""]
        target_matchers = ["severity =~ \"warning|info\""]
        equal           = ["namespace", "alertname"]
      },
      {
        source_matchers = ["severity = \"warning\""]
        target_matchers = ["severity = \"info\""]
        equal           = ["namespace", "alertname"]
      },
      {
        source_matchers = ["alertname = \"InfoInhibitor\""]
        target_matchers = ["severity = \"info\""]
        equal           = ["namespace"]
      },
      { target_matchers = ["alertname = \"InfoInhibitor\""] },
      # Suppress warning when critical fires for the same node.
      {
        source_matchers = ["severity = \"critical\""]
        target_matchers = ["severity = \"warning\""]
        equal           = ["node"]
      },
      # Suppress LonghornVolumeDegradedWarning when LonghornVolumeFaulted fires.
      {
        source_matchers = ["alertname = \"LonghornVolumeFaulted\""]
        target_matchers = ["alertname = \"LonghornVolumeDegradedWarning\""]
        equal           = ["volume"]
      },
      # Suppress VirtLauncherContainerCreating when LonghornVolumeFaulted fires.
      {
        source_matchers = ["alertname = \"LonghornVolumeFaulted\""]
        target_matchers = ["alertname = \"VirtLauncherContainerCreating\""]
        equal           = ["namespace"]
      },
    ]

    templates = ["/etc/alertmanager/config/*.tmpl"]
  })
}

# ── Alertmanager base config Secret ──────────────────────────────────────────
# Writes directly to the Secret that Prometheus Operator uses as the base
# Alertmanager configuration. The AlertmanagerConfig v1alpha1 CRD does not
# support googleChatConfigs (field silently dropped), so we bypass it entirely
# and write native Alertmanager YAML here. Prometheus Operator hot-reloads
# Alertmanager within ~30s of Secret changes.
#
# We use null_resource + kubectl apply instead of kubernetes_manifest because
# rancher-monitoring Helm pre-creates this Secret on every fresh cluster.
# kubernetes_manifest tries CREATE (POST) when a resource isn't in TF state,
# which fails with "already exists". kubectl apply is always create-or-update
# and is fully idempotent — no terraform import step required.

resource "null_resource" "alertmanager_base_config" {
  triggers = {
    config_hash = sha256(local.alertmanager_config_yaml)
    tmpl_hash   = sha256(local.rancher_defaults_tmpl)
  }

  provisioner "local-exec" {
    interpreter = ["/bin/bash", "-c"]
    environment = {
      KUBECONFIG    = var.kubeconfig_path
      AM_CONFIG_B64 = base64encode(local.alertmanager_config_yaml)
      AM_TMPL_B64   = base64encode(local.rancher_defaults_tmpl)
    }
    command = <<-BASH
      set -e
      kubectl create secret generic alertmanager-rancher-monitoring-alertmanager \
        --context '${var.kubeconfig_context}' \
        -n '${local.ns}' \
        --from-file=alertmanager.yaml=<(base64 -d <<< "$AM_CONFIG_B64") \
        --from-file=rancher_defaults.tmpl=<(base64 -d <<< "$AM_TMPL_B64") \
        --dry-run=client -o yaml \
      | kubectl apply \
          --context '${var.kubeconfig_context}' \
          -f -
    BASH
  }
}

# ── calert — Google Chat webhook forwarder ────────────────────────────────────
# calert accepts Alertmanager webhook_configs payloads and forwards them to
# Google Chat. Deployed as a single-replica Deployment in the monitoring
# namespace. Config is stored in a Secret (contains the webhook URL).

resource "null_resource" "calert_config" {
  triggers = {
    config_hash = sha256(local.calert_config_toml)
    tmpl_hash   = sha256(local.calert_message_tmpl)
  }

  provisioner "local-exec" {
    interpreter = ["/bin/bash", "-c"]
    environment = {
      KUBECONFIG = var.kubeconfig_path
      CONFIG_B64 = base64encode(local.calert_config_toml)
      TMPL_B64   = base64encode(local.calert_message_tmpl)
    }
    command = <<-BASH
      set -e
      kubectl create secret generic calert-config \
        --context '${var.kubeconfig_context}' \
        -n '${local.ns}' \
        --from-file=config.toml=<(base64 -d <<< "$CONFIG_B64") \
        --from-file=message.tmpl=<(base64 -d <<< "$TMPL_B64") \
        --dry-run=client -o yaml \
      | kubectl apply \
          --context '${var.kubeconfig_context}' \
          -f -
    BASH
  }
}

resource "kubernetes_manifest" "calert_deployment" {
  manifest = {
    apiVersion = "apps/v1"
    kind       = "Deployment"
    metadata = {
      name      = "calert"
      namespace = local.ns
      labels    = local.common_labels
    }
    spec = {
      replicas = 1
      selector = { matchLabels = { app = "calert" } }
      template = {
        metadata = {
          labels = { app = "calert" }
          annotations = {
            # Changing config or template triggers a rolling restart automatically.
            "checksum/config" = sha256("${local.calert_config_toml}${local.calert_message_tmpl}")
          }
        }
        spec = {
          containers = [{
            name  = "calert"
            image = "ghcr.io/mr-karan/calert:v2.3.0"
            args  = ["--config=/etc/calert/config.toml"]
            ports = [{ containerPort = 6000 }]
            volumeMounts = [{
              name      = "config"
              mountPath = "/etc/calert"
              readOnly  = true
            }]
          }]
          volumes = [{
            name   = "config"
            secret = { secretName = "calert-config" }
          }]
        }
      }
    }
  }

  depends_on = [null_resource.calert_config]
}

resource "kubernetes_manifest" "calert_service" {
  manifest = {
    apiVersion = "v1"
    kind       = "Service"
    metadata = {
      name      = "calert"
      namespace = local.ns
      labels    = local.common_labels
    }
    spec = {
      selector = { app = "calert" }
      ports = [{
        name       = "http"
        port       = 6000
        targetPort = 6000
      }]
    }
  }
}

# ═══════════════════════════════════════════════════════════════════════════════
# PrometheusRule 1 — Longhorn / Storage alerts
# ═══════════════════════════════════════════════════════════════════════════════

resource "kubernetes_manifest" "prometheus_rule_storage" {
  manifest = {
    apiVersion = "monitoring.coreos.com/v1"
    kind       = "PrometheusRule"
    metadata = {
      name      = "${var.environment}-harvester-storage-alerts"
      namespace = local.ns
      labels    = local.rule_labels
    }
    spec = {
      groups = [
        {
          name = "longhorn.storage"
          rules = [
            {
              alert  = "LonghornVolumeFaulted"
              expr   = "longhorn_volume_robustness{robustness=\"faulted\"} == 1"
              for    = "2m"
              labels = { severity = "critical" }
              annotations = {
                summary     = "Longhorn volume {{ $labels.volume }} is faulted"
                description = "Volume {{ $labels.volume }} is faulted (zero healthy replicas). VMs using this volume will have I/O errors. If disk eviction was in progress, immediately set evictionRequested=false on the source disk — eviction stops source replicas before destination finishes rebuilding."
                runbook_url = "${var.runbook_base_url}/LonghornVolumeFaulted"
              }
            },
            {
              alert  = "LonghornVolumeDegradedWarning"
              expr   = "longhorn_volume_robustness{robustness=\"degraded\"} == 1"
              for    = "15m"
              labels = { severity = "warning" }
              annotations = {
                summary     = "Longhorn volume {{ $labels.volume }} degraded >15m"
                description = "Volume {{ $labels.volume }} has been degraded for 15+ min. Replica count below desired. Next disk failure risks data loss."
                runbook_url = "${var.runbook_base_url}/LonghornVolumeDegraded"
              }
            },
            {
              alert  = "LonghornVolumeDegradedCritical"
              expr   = "longhorn_volume_robustness{robustness=\"degraded\"} == 1"
              for    = "60m"
              labels = { severity = "critical" }
              annotations = {
                summary     = "Longhorn volume {{ $labels.volume }} degraded >1h"
                description = "Volume {{ $labels.volume }} degraded > 1h. Rebuild stalled or insufficient capacity. Check replica pod logs and disk schedulability."
                runbook_url = "${var.runbook_base_url}/LonghornVolumeDegraded"
              }
            },
            {
              alert  = "LonghornVolumeReplicaCountLow"
              expr   = "longhorn_volume_replicas_count < longhorn_volume_spec_replicas_count AND longhorn_volume_actual_size > 0"
              for    = "10m"
              labels = { severity = "warning" }
              annotations = {
                summary     = "Longhorn volume {{ $labels.volume }} has fewer replicas than configured"
                description = "Volume {{ $labels.volume }} has {{ $value }} running replicas, fewer than configured. Rebuild may be stalled. Check: kubectl get replicas.longhorn.io -n longhorn-system"
                runbook_url = "${var.runbook_base_url}/LonghornVolumeReplicaCountLow"
              }
            },
            {
              alert  = "LonghornReplicaRebuildBacklog"
              expr   = "sum by (node) (longhorn_replica_rebuilding) > ${var.replica_rebuild_warning_count}"
              for    = "5m"
              labels = { severity = "warning" }
              annotations = {
                summary     = "Node {{ $labels.node }} has {{ $value }} replicas rebuilding simultaneously"
                description = "Node {{ $labels.node }} has {{ $value }} replicas rebuilding simultaneously. Do NOT initiate disk evictions while this is firing — mass eviction with active rebuilds causes cascade failure (source replica stopped before destination is healthy)."
                runbook_url = "${var.runbook_base_url}/LonghornReplicaRebuildBacklog"
              }
            },
            {
              alert  = "LonghornEvictionWithDegradedVolumes"
              expr   = "longhorn_disk_eviction_requested == 1 and on() count(longhorn_volume_robustness{robustness=\"degraded\"}) > 0"
              for    = "5m"
              labels = { severity = "critical" }
              annotations = {
                summary     = "Longhorn disk eviction active on {{ $labels.node }}/{{ $labels.disk }}"
                description = "Disk eviction is active on {{ $labels.node }}/{{ $labels.disk }}. Eviction stops source replicas before destinations finish rebuilding. If any volumes are degraded (see LonghornVolumeDegradedWarning), pause immediately: kubectl patch nodes.longhorn.io <node> -n longhorn-system --type=json -p='[{\"op\":\"replace\",\"path\":\"/spec/disks/<disk>/evictionRequested\",\"value\":false}]'"
                runbook_url = "${var.runbook_base_url}/LonghornEvictionWithDegradedVolumes"
              }
            },
            {
              alert  = "LonghornDiskUsageHigh"
              expr   = "(longhorn_disk_usage_bytes / longhorn_disk_capacity_bytes) * 100 > ${var.disk_usage_warning_pct}"
              for    = "10m"
              labels = { severity = "warning" }
              annotations = {
                summary     = "Longhorn disk {{ $labels.disk }} on {{ $labels.node }} at {{ $value | printf \"%.1f\" }}%"
                description = "Disk {{ $labels.disk }} on {{ $labels.node }} at {{ $value | printf \"%.1f\" }}% capacity. New replica scheduling will fail if disk reaches 100%."
                runbook_url = "${var.runbook_base_url}/LonghornDiskUsageHigh"
              }
            },
            {
              alert  = "LonghornDiskUsageCritical"
              expr   = "(longhorn_disk_usage_bytes / longhorn_disk_capacity_bytes) * 100 > ${var.disk_usage_critical_pct}"
              for    = "5m"
              labels = { severity = "critical" }
              annotations = {
                summary     = "Longhorn disk {{ $labels.disk }} on {{ $labels.node }} critically full at {{ $value | printf \"%.1f\" }}%"
                description = "Disk {{ $labels.disk }} on {{ $labels.node }} critically full at {{ $value | printf \"%.1f\" }}%. Immediate action required: check for ghost replicas (stopped replicas with no data dir that inflate storageScheduled)."
                runbook_url = "${var.runbook_base_url}/LonghornDiskUsageCritical"
              }
            },
            {
              alert  = "LonghornDiskSchedulingDisabledLong"
              expr   = "longhorn_disk_schedulable == 0"
              for    = "60m"
              labels = { severity = "warning" }
              annotations = {
                summary     = "Longhorn disk {{ $labels.disk }} on {{ $labels.node }} has scheduling disabled >1h"
                description = "Longhorn disk {{ $labels.disk }} on {{ $labels.node }} has allowScheduling=false for > 1h. Confirm this is intentional maintenance. New replicas will not be placed here."
                runbook_url = "${var.runbook_base_url}/LonghornDiskSchedulingDisabled"
              }
            },
            {
              alert  = "LonghornReplicaUnhealthy"
              expr   = "count by (node) (longhorn_replica_state{state=~\"error|unknown\"} == 1) > 0"
              for    = "5m"
              labels = { severity = "warning" }
              annotations = {
                summary     = "{{ $value }} replicas in error/unknown state on {{ $labels.node }}"
                description = "{{ $value }} Longhorn replicas in error or unknown state on {{ $labels.node }} for 5+ min. These are genuinely unhealthy (stopped is normal for powered-off VMs). Check Longhorn UI for affected volumes and consider deleting and rebuilding the replica."
                runbook_url = "${var.runbook_base_url}/LonghornReplicaUnhealthy"
              }
            },
            {
              alert  = "LonghornShareManagerNotRunning"
              expr   = "kube_pod_status_phase{pod=~\"share-manager-.*\", namespace=\"longhorn-system\", phase!=\"Running\"} == 1"
              for    = "3m"
              labels = { severity = "critical" }
              annotations = {
                summary     = "Longhorn share-manager {{ $labels.pod }} is not Running"
                description = "Longhorn share-manager {{ $labels.pod }} is not Running. All RWX volumes it exports lose access. Harvester CSI translates ALL downstream PVCs to RWX+Block — this share-manager failure directly blocks hotplugged disk access for affected VMs."
                runbook_url = "${var.runbook_base_url}/LonghornShareManagerNotRunning"
              }
            },
          ]
        }
      ]
    }
  }
}

# ═══════════════════════════════════════════════════════════════════════════════
# PrometheusRule 2 — KubeVirt / VM alerts
# ═══════════════════════════════════════════════════════════════════════════════

resource "kubernetes_manifest" "prometheus_rule_vm" {
  manifest = {
    apiVersion = "monitoring.coreos.com/v1"
    kind       = "PrometheusRule"
    metadata = {
      name      = "${var.environment}-harvester-vm-alerts"
      namespace = local.ns
      labels    = local.rule_labels
    }
    spec = {
      groups = [
        {
          name = "kubevirt.vm"
          rules = [
            {
              alert  = "VirtLauncherPodStuck"
              expr   = "kube_pod_status_phase{pod=~\"virt-launcher-.*\", phase=\"Pending\"} == 1"
              for    = var.virt_launcher_stuck_for
              labels = { severity = "critical" }
              annotations = {
                summary     = "VM pod {{ $labels.pod }} in {{ $labels.namespace }} pending for >${var.virt_launcher_stuck_for}"
                description = "VM pod {{ $labels.pod }} in {{ $labels.namespace }} pending for > ${var.virt_launcher_stuck_for}. Likely cause: Longhorn volume not attaching. Check: kubectl get volumeattachment | grep <pvc-uuid>. A stale VolumeAttachment from a previous node (attached=true on wrong node) may be blocking CSI. Delete it ONLY after confirming the referencing pod is terminated."
                runbook_url = "${var.runbook_base_url}/VirtLauncherPodStuck"
              }
            },
            {
              alert  = "VirtLauncherContainerCreating"
              expr   = "kube_pod_container_status_waiting_reason{pod=~\"virt-launcher-.*\", reason=\"ContainerCreating\"} == 1"
              for    = var.virt_launcher_stuck_for
              labels = { severity = "critical" }
              annotations = {
                summary     = "VM pod {{ $labels.pod }} stuck in ContainerCreating"
                description = "VM pod {{ $labels.pod }} stuck in ContainerCreating. 1. Check pod events: kubectl describe pod <pod> -n <namespace>. 2. Look for FailedAttachVolume → stale VolumeAttachment. 3. Look for FailedMount exit status 32 → share-manager node conflict (see HP-volume alerts)."
                runbook_url = "${var.runbook_base_url}/VirtLauncherContainerCreating"
              }
            },
            {
              alert  = "VirtLauncherCrashLoop"
              expr   = "increase(kube_pod_container_status_restarts_total{pod=~\"virt-launcher-.*\"}[15m]) > 3"
              for    = "0m"
              labels = { severity = "critical" }
              annotations = {
                summary     = "VM pod {{ $labels.pod }} restarted {{ $value }} times in 15m"
                description = "VM pod {{ $labels.pod }} restarted {{ $value }} times in 15m. If LonghornVolumeFaulted is also firing, root cause is VM I/O errors from faulted backing volume forcing virt-launcher termination."
                runbook_url = "${var.runbook_base_url}/VirtLauncherCrashLoop"
              }
            },
            {
              alert  = "HpVolumePodNotRunning"
              expr   = "kube_pod_status_phase{pod=~\"hp-volume-.*\", namespace=\"cpd-dp\", phase!=\"Running\"} == 1"
              for    = var.hp_volume_stuck_for
              labels = { severity = "critical" }
              annotations = {
                summary     = "hp-volume pod {{ $labels.pod }} not Running for >${var.hp_volume_stuck_for}"
                description = "hp-volume pod {{ $labels.pod }} not Running for >${var.hp_volume_stuck_for}. ALL hotplugged disks for the target VM are now unavailable. Key failure modes: 1. exit status 32: Longhorn engine pinned to different node than hp-volume pod. Root cause: Harvester CSI RWX+Block share-manager conflict. Engine node = share-manager node (controlled by share-manager controller, cannot be overridden externally). Fix: delete + recreate the PVC with node scheduling controlled. 2. FailedAttachVolume: stale VolumeAttachment on wrong node. Delete it. Check: kubectl get events -n cpd-dp --field-selector involvedObject.name=<pod>"
                runbook_url = "${var.runbook_base_url}/HpVolumePodNotRunning"
              }
            },
            {
              alert  = "HpVolumeMapDeviceFailed"
              expr   = "kube_event_count{involvedObject_kind=\"Pod\", involvedObject_namespace=\"cpd-dp\", reason=\"FailedMount\"} > 0"
              for    = "0m"
              labels = { severity = "critical" }
              annotations = {
                summary     = "hp-volume pod reporting FailedMount (exit status 32)"
                description = "hp-volume pod reporting mount failure. exit status 32 = NFS/Block mode conflict: share-manager exported volume as NFS filesystem, but NodePublishVolume expects a block device file at staging path. Only fix: PVC recreation."
                runbook_url = "${var.runbook_base_url}/HpVolumeMapDeviceFailed"
              }
            },
            {
              alert  = "StaleVolumeAttachmentBlocking"
              expr   = "increase(kube_event_count{reason=\"FailedAttachVolume\", involvedObject_kind=\"Pod\"}[10m]) > 3"
              for    = "0m"
              labels = { severity = "critical" }
              annotations = {
                summary     = "Pod {{ $labels.involvedObject_name }} has >3 FailedAttachVolume events in 10m"
                description = "Pod {{ $labels.involvedObject_name }} has >3 FailedAttachVolume events in 10m. A stale VolumeAttachment from a previous node is blocking CSI. Steps: (1) kubectl get volumeattachment | grep <pvc-id> (2) Confirm the pod referencing the old VA is terminated (3) kubectl delete volumeattachment <va-name>. Note: deleting a VA while referencing pod exists causes immediate recreation."
                runbook_url = "${var.runbook_base_url}/StaleVolumeAttachmentBlocking"
              }
            },
          ]
        }
      ]
    }
  }
}

# ═══════════════════════════════════════════════════════════════════════════════
# PrometheusRule 3 — Harvester node alerts
# ═══════════════════════════════════════════════════════════════════════════════

resource "kubernetes_manifest" "prometheus_rule_node" {
  manifest = {
    apiVersion = "monitoring.coreos.com/v1"
    kind       = "PrometheusRule"
    metadata = {
      name      = "${var.environment}-harvester-node-alerts"
      namespace = local.ns
      labels    = local.rule_labels
    }
    spec = {
      groups = [
        {
          name = "harvester.node"
          rules = [
            {
              alert  = "HarvesterNodeNotReady"
              expr   = "kube_node_status_condition{condition=\"Ready\", status=\"true\"} == 0"
              for    = "1m"
              labels = { severity = "critical" }
              annotations = {
                summary     = "Harvester node {{ $labels.node }} NotReady"
                description = "Harvester node {{ $labels.node }} NotReady for >1m. All VMs on this node are at risk. Longhorn replicas on this node will fault within 60s if it remains offline."
                runbook_url = "${var.runbook_base_url}/HarvesterNodeNotReady"
              }
            },
            {
              alert  = "LonghornNodeOffline"
              expr   = "longhorn_node_status{condition=\"ready\"} == 0"
              for    = "2m"
              labels = { severity = "critical" }
              annotations = {
                summary     = "Longhorn reports {{ $labels.node }} offline"
                description = "Longhorn reports {{ $labels.node }} offline. Volumes with replicas only on this node will degrade immediately."
                runbook_url = "${var.runbook_base_url}/LonghornNodeOffline"
              }
            },
            {
              alert  = "NodeDiskPressure"
              expr   = "kube_node_status_condition{condition=\"DiskPressure\", status=\"true\"} == 1"
              for    = "2m"
              labels = { severity = "warning" }
              annotations = {
                summary     = "Node {{ $labels.node }} OS disk pressure"
                description = "Node {{ $labels.node }} OS disk pressure (not Longhorn — this is the root filesystem). Kubelet will begin evicting pods. Check: df -h on the node."
                runbook_url = "${var.runbook_base_url}/NodeDiskPressure"
              }
            },
            {
              alert  = "NodeMemoryPressure"
              expr   = "kube_node_status_condition{condition=\"MemoryPressure\", status=\"true\"} == 1"
              for    = "2m"
              labels = { severity = "warning" }
              annotations = {
                summary     = "Node {{ $labels.node }} memory pressure"
                description = "Node {{ $labels.node }} memory pressure. Pod evictions may begin."
                runbook_url = "${var.runbook_base_url}/NodeMemoryPressure"
              }
            },
            {
              alert  = "NodeHighCPU"
              expr   = "100 - (avg by (node) (rate(node_cpu_seconds_total{mode=\"idle\"}[5m])) * 100) > ${var.node_cpu_warning_pct}"
              for    = "15m"
              labels = { severity = "warning" }
              annotations = {
                summary     = "Node {{ $labels.node }} CPU at {{ $value | printf \"%.1f\" }}%"
                description = "Node {{ $labels.node }} CPU at {{ $value | printf \"%.1f\" }}% for 15m. Correlate with LonghornReplicaRebuildBacklog — rebuild storms saturate CPU/IO."
                runbook_url = "${var.runbook_base_url}/NodeHighCPU"
              }
            },
            {
              alert  = "NodeCPUCritical"
              expr   = "100 - (avg by (node) (rate(node_cpu_seconds_total{mode=\"idle\"}[5m])) * 100) > ${var.node_cpu_critical_pct}"
              for    = "10m"
              labels = { severity = "critical" }
              annotations = {
                summary     = "Node {{ $labels.node }} CPU critical at {{ $value | printf \"%.1f\" }}%"
                description = "Node {{ $labels.node }} CPU at {{ $value | printf \"%.1f\" }}% for 10m. Immediate investigation required."
                runbook_url = "${var.runbook_base_url}/NodeCPUCritical"
              }
            },
            {
              alert  = "NodeHighMemory"
              expr   = "(1 - (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)) * 100 > ${var.node_memory_warning_pct}"
              for    = "10m"
              labels = { severity = "warning" }
              annotations = {
                summary     = "Node {{ $labels.node }} memory at {{ $value | printf \"%.1f\" }}%"
                description = "Node {{ $labels.node }} memory at {{ $value | printf \"%.1f\" }}%."
                runbook_url = "${var.runbook_base_url}/NodeHighMemory"
              }
            },
            {
              alert  = "NodeRootFSLow"
              expr   = "(node_filesystem_avail_bytes{mountpoint=\"/\", fstype!=\"tmpfs\"} / node_filesystem_size_bytes{mountpoint=\"/\", fstype!=\"tmpfs\"}) * 100 < 15"
              for    = "10m"
              labels = { severity = "warning" }
              annotations = {
                summary     = "Root filesystem on {{ $labels.instance }} < 15% free"
                description = "Root filesystem on {{ $labels.instance }} < 15% free. This is NOT Longhorn disk space — affects kubelet, etcd, container images, logs."
                runbook_url = "${var.runbook_base_url}/NodeRootFSLow"
              }
            },
            {
              alert  = "NodeRootFSCritical"
              expr   = "(node_filesystem_avail_bytes{mountpoint=\"/\", fstype!=\"tmpfs\"} / node_filesystem_size_bytes{mountpoint=\"/\", fstype!=\"tmpfs\"}) * 100 < 5"
              for    = "5m"
              labels = { severity = "critical" }
              annotations = {
                summary     = "Root filesystem on {{ $labels.instance }} critically low (<5% free)"
                description = "Root filesystem on {{ $labels.instance }} < 5% free. Immediate action required — kubelet, etcd, and container runtime may fail."
                runbook_url = "${var.runbook_base_url}/NodeRootFSCritical"
              }
            },
          ]
        }
      ]
    }
  }
}

# ═══════════════════════════════════════════════════════════════════════════════
# AlertmanagerConfig — Google Chat routing
#
# Uses monitoring.coreos.com/v1alpha1 AlertmanagerConfig CRD.
# Requires Prometheus Operator >= 0.73 (kube-prometheus-stack >= 60.x,
# included in Rancher Monitoring 103.x / Rancher 2.13.x+).
#
# The AlertmanagerConfig is picked up by rancher-monitoring's Alertmanager via
# alertmanagerConfigSelector (matches all AlertmanagerConfigs in the monitoring
# namespace by default in Rancher Monitoring).
# ═══════════════════════════════════════════════════════════════════════════════

# AlertmanagerConfig CRD removed — routing and receivers are now in
# kubernetes_manifest.alertmanager_base_config (native Alertmanager YAML).

# ═══════════════════════════════════════════════════════════════════════════════
# Grafana Dashboards — ConfigMaps in cattle-dashboards namespace
# Labels: grafana_dashboard = "1"  (picked up by Grafana sidecar)
# ═══════════════════════════════════════════════════════════════════════════════

locals {
  # ── Shared datasource template variable ─────────────────────────────────────
  _ds_var = {
    current    = { selected = false, text = "Prometheus", value = "Prometheus" }
    hide       = 0
    includeAll = false
    multi      = false
    name       = "datasource"
    options    = []
    query      = "prometheus"
    refresh    = 1
    type       = "datasource"
    label      = "Datasource"
  }

  # ── Dashboard 1: Storage Health ──────────────────────────────────────────────
  dashboard_storage = {
    title         = "${var.environment} — Harvester Storage Health"
    uid           = "${var.environment}-harvester-storage"
    schemaVersion = 38
    refresh       = "30s"
    tags          = ["harvester", "storage", "longhorn", var.environment]
    time          = { from = "now-1h", to = "now" }
    timezone      = "browser"
    templating    = { list = [local._ds_var] }
    annotations   = { list = [] }
    panels = [
      {
        id         = 1
        title      = "Volume Robustness"
        type       = "table"
        gridPos    = { h = 8, w = 24, x = 0, y = 0 }
        datasource = { type = "prometheus", uid = "$datasource" }
        options = {
          sortBy = [{ displayName = "volume", desc = false }]
          footer = { show = false }
        }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "longhorn_volume_robustness"
          instant      = true
          legendFormat = "__auto"
          refId        = "A"
        }]
        transformations = [
          {
            id      = "labelsToFields"
            options = { mode = "columns" }
          },
          {
            id = "organize"
            options = {
              renameByName = { volume = "Volume", namespace = "Namespace", robustness = "Robustness", node = "Node", state = "State" }
            }
          }
        ]
        fieldConfig = {
          defaults = {}
          overrides = [
            {
              matcher = { id = "byName", options = "Robustness" }
              properties = [{
                id    = "custom.displayMode"
                value = "color-background"
                }, {
                id = "mappings"
                value = [
                  { type = "value", options = { healthy = { color = "green", index = 0 } } },
                  { type = "value", options = { degraded = { color = "orange", index = 1 } } },
                  { type = "value", options = { faulted = { color = "red", index = 2 } } },
                ]
              }]
            }
          ]
        }
      },
      {
        id         = 2
        title      = "Active Replica Rebuilds per Node"
        type       = "timeseries"
        gridPos    = { h = 8, w = 12, x = 0, y = 8 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "sum by (node) (longhorn_replica_rebuilding)"
          legendFormat = "{{node}}"
          refId        = "A"
        }]
        fieldConfig = {
          defaults = {
            color = { mode = "palette-classic" }
            thresholds = {
              mode = "absolute"
              steps = [
                { color = "green", value = null },
                { color = "yellow", value = 3 },
                { color = "red", value = var.replica_rebuild_warning_count },
              ]
            }
          }
          overrides = []
        }
        options = { tooltip = { mode = "multi" } }
      },
      {
        id         = 3
        title      = "Disk Utilisation per Node/Disk"
        type       = "bargauge"
        gridPos    = { h = 8, w = 12, x = 12, y = 8 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "(longhorn_disk_usage_bytes / longhorn_disk_capacity_bytes) * 100"
          instant      = true
          legendFormat = "{{node}} / {{disk}}"
          refId        = "A"
        }]
        fieldConfig = {
          defaults = {
            unit = "percent"
            min  = 0
            max  = 100
            thresholds = {
              mode = "absolute"
              steps = [
                { color = "green", value = null },
                { color = "yellow", value = var.disk_usage_warning_pct },
                { color = "red", value = var.disk_usage_critical_pct },
              ]
            }
          }
          overrides = []
        }
        options = { orientation = "horizontal", reduceOptions = { calcs = ["lastNotNull"] } }
      },
      {
        id         = 4
        title      = "Disk Eviction State"
        type       = "stat"
        gridPos    = { h = 4, w = 8, x = 0, y = 16 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "longhorn_disk_eviction_requested"
          instant      = true
          legendFormat = "{{node}} / {{disk}}"
          refId        = "A"
        }]
        fieldConfig = {
          defaults = {
            mappings = [
              { type = "value", options = { "0" = { text = "No", color = "green" } } },
              { type = "value", options = { "1" = { text = "YES", color = "red" } } },
            ]
            thresholds = {
              mode  = "absolute"
              steps = [{ color = "green", value = null }, { color = "red", value = 1 }]
            }
          }
          overrides = []
        }
        options = { colorMode = "background", reduceOptions = { calcs = ["lastNotNull"] } }
      },
      {
        id         = 5
        title      = "Unhealthy Replica Count per Node (error/unknown)"
        type       = "stat"
        gridPos    = { h = 4, w = 8, x = 8, y = 16 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "count by (node) (longhorn_replica_state{state=~\"error|unknown\"} == 1)"
          instant      = true
          legendFormat = "{{node}}"
          refId        = "A"
        }]
        fieldConfig = {
          defaults = {
            thresholds = {
              mode = "absolute"
              steps = [
                { color = "green", value = null },
                { color = "orange", value = 5 },
                { color = "red", value = 10 },
              ]
            }
          }
          overrides = []
        }
        options = { colorMode = "background", reduceOptions = { calcs = ["lastNotNull"] } }
      },
      {
        id         = 6
        title      = "Share-Manager Pod Status"
        type       = "table"
        gridPos    = { h = 4, w = 8, x = 16, y = 16 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "kube_pod_status_phase{pod=~\"share-manager-.*\", namespace=\"longhorn-system\"} == 1"
          instant      = true
          legendFormat = "__auto"
          refId        = "A"
        }]
        transformations = [
          { id = "labelsToFields", options = { mode = "columns" } },
          { id = "organize", options = { renameByName = { pod = "Pod", node = "Node", phase = "Phase" } } }
        ]
        fieldConfig = {
          defaults = {}
          overrides = [
            {
              matcher = { id = "byName", options = "Phase" }
              properties = [{
                id    = "custom.displayMode"
                value = "color-background"
                }, {
                id = "mappings"
                value = [
                  { type = "value", options = { Running = { color = "green", index = 0 } } },
                  { type = "value", options = { Pending = { color = "orange", index = 1 } } },
                  { type = "value", options = { Failed = { color = "red", index = 2 } } },
                ]
              }]
            }
          ]
        }
        options = { footer = { show = false } }
      },
    ]
  }

  # ── Dashboard 2: VM Health ───────────────────────────────────────────────────
  dashboard_vm = {
    title         = "${var.environment} — Harvester VM Health"
    uid           = "${var.environment}-harvester-vm"
    schemaVersion = 38
    refresh       = "30s"
    tags          = ["harvester", "kubevirt", "vm", var.environment]
    time          = { from = "now-1h", to = "now" }
    timezone      = "browser"
    templating    = { list = [local._ds_var] }
    annotations   = { list = [] }
    panels = [
      {
        id         = 1
        title      = "virt-launcher Pod Phase Breakdown"
        type       = "stat"
        gridPos    = { h = 6, w = 12, x = 0, y = 0 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "count by (phase) (kube_pod_status_phase{pod=~\"virt-launcher-.*\"} == 1)"
          instant      = true
          legendFormat = "{{phase}}"
          refId        = "A"
        }]
        fieldConfig = {
          defaults = {
            mappings   = []
            thresholds = { mode = "absolute", steps = [{ color = "green", value = null }] }
          }
          overrides = []
        }
        options = { colorMode = "background", reduceOptions = { calcs = ["lastNotNull"] } }
      },
      {
        id         = 2
        title      = "Pods NOT Running (virt-launcher + hp-volume)"
        type       = "table"
        gridPos    = { h = 6, w = 12, x = 12, y = 0 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "kube_pod_status_phase{pod=~\"virt-launcher-.*|hp-volume-.*\", phase!~\"Running|Succeeded\"} == 1"
          instant      = true
          legendFormat = "__auto"
          refId        = "A"
        }]
        transformations = [
          { id = "labelsToFields", options = { mode = "columns" } },
          { id = "organize", options = { renameByName = { pod = "Pod", namespace = "Namespace", phase = "Phase" } } }
        ]
        fieldConfig = {
          defaults = {}
          overrides = [
            {
              matcher = { id = "byName", options = "Phase" }
              properties = [{ id = "custom.displayMode", value = "color-background" }, {
                id = "mappings", value = [
                  { type = "value", options = { Pending = { color = "orange", index = 0 } } },
                  { type = "value", options = { Failed = { color = "red", index = 1 } } },
                  { type = "value", options = { Unknown = { color = "red", index = 2 } } },
                ]
              }]
            }
          ]
        }
        options = { footer = { show = false } }
      },
      {
        id         = 3
        title      = "VolumeAttachment Count per Node (spike = stale VA)"
        type       = "timeseries"
        gridPos    = { h = 6, w = 12, x = 0, y = 6 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "count by (node) (kube_volumeattachment_info)"
          legendFormat = "{{node}}"
          refId        = "A"
        }]
        fieldConfig = {
          defaults  = { color = { mode = "palette-classic" } }
          overrides = []
        }
        options = { tooltip = { mode = "multi" } }
      },
      {
        id         = 4
        title      = "hp-volume Pod Status"
        type       = "table"
        gridPos    = { h = 6, w = 12, x = 12, y = 6 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [
          {
            datasource   = { type = "prometheus", uid = "$datasource" }
            expr         = "kube_pod_status_phase{pod=~\"hp-volume-.*\", namespace=\"cpd-dp\"} == 1"
            instant      = true
            legendFormat = "__auto"
            refId        = "A"
          },
          {
            datasource   = { type = "prometheus", uid = "$datasource" }
            expr         = "kube_pod_container_status_restarts_total{pod=~\"hp-volume-.*\", namespace=\"cpd-dp\"}"
            instant      = true
            legendFormat = "__auto"
            refId        = "B"
          },
        ]
        transformations = [
          { id = "labelsToFields", options = { mode = "columns" } },
          { id = "organize", options = { renameByName = { pod = "Pod", node = "Node", phase = "Phase" } } }
        ]
        fieldConfig = {
          defaults  = {}
          overrides = []
        }
        options = { footer = { show = false } }
      },
      {
        id         = 5
        title      = "virt-launcher Restart Rate (15m window)"
        type       = "timeseries"
        gridPos    = { h = 6, w = 24, x = 0, y = 12 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "increase(kube_pod_container_status_restarts_total{pod=~\"virt-launcher-.*\"}[15m]) > 0"
          legendFormat = "{{pod}} ({{namespace}})"
          refId        = "A"
        }]
        fieldConfig = {
          defaults = {
            color = { mode = "palette-classic" }
            thresholds = {
              mode  = "absolute"
              steps = [{ color = "green", value = null }, { color = "red", value = 3 }]
            }
          }
          overrides = []
        }
        options = { tooltip = { mode = "multi" } }
      },
    ]
  }

  # ── Dashboard 3: Node Health ─────────────────────────────────────────────────
  dashboard_node = {
    title         = "${var.environment} — Harvester Node Health"
    uid           = "${var.environment}-harvester-node"
    schemaVersion = 38
    refresh       = "30s"
    tags          = ["harvester", "node", var.environment]
    time          = { from = "now-1h", to = "now" }
    timezone      = "browser"
    templating    = { list = [local._ds_var] }
    annotations   = { list = [] }
    panels = [
      {
        id         = 1
        title      = "Node Ready Status"
        type       = "stat"
        gridPos    = { h = 4, w = 24, x = 0, y = 0 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "kube_node_status_condition{condition=\"Ready\", status=\"true\"}"
          instant      = true
          legendFormat = "{{node}}"
          refId        = "A"
        }]
        fieldConfig = {
          defaults = {
            mappings = [
              { type = "value", options = { "0" = { text = "NOT READY", color = "red" } } },
              { type = "value", options = { "1" = { text = "Ready", color = "green" } } },
            ]
            thresholds = {
              mode  = "absolute"
              steps = [{ color = "red", value = null }, { color = "green", value = 1 }]
            }
          }
          overrides = []
        }
        options = { colorMode = "background", reduceOptions = { calcs = ["lastNotNull"] }, orientation = "horizontal" }
      },
      {
        id         = 2
        title      = "CPU Utilisation per Node"
        type       = "timeseries"
        gridPos    = { h = 8, w = 12, x = 0, y = 4 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "100 - (avg by (node) (rate(node_cpu_seconds_total{mode=\"idle\"}[5m])) * 100)"
          legendFormat = "{{node}}"
          refId        = "A"
        }]
        fieldConfig = {
          defaults = {
            unit = "percent"
            min  = 0
            max  = 100
            thresholds = {
              mode = "absolute"
              steps = [
                { color = "green", value = null },
                { color = "yellow", value = var.node_cpu_warning_pct },
                { color = "red", value = var.node_cpu_critical_pct },
              ]
            }
            custom = { lineWidth = 2 }
          }
          overrides = []
        }
        options = { tooltip = { mode = "multi" } }
      },
      {
        id         = 3
        title      = "Memory Utilisation per Node"
        type       = "timeseries"
        gridPos    = { h = 8, w = 12, x = 12, y = 4 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "(1 - (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)) * 100"
          legendFormat = "{{node}}"
          refId        = "A"
        }]
        fieldConfig = {
          defaults = {
            unit = "percent"
            min  = 0
            max  = 100
            thresholds = {
              mode = "absolute"
              steps = [
                { color = "green", value = null },
                { color = "yellow", value = var.node_memory_warning_pct },
                { color = "red", value = 95 },
              ]
            }
            custom = { lineWidth = 2 }
          }
          overrides = []
        }
        options = { tooltip = { mode = "multi" } }
      },
      {
        id         = 4
        title      = "Root Filesystem Free % per Node"
        type       = "bargauge"
        gridPos    = { h = 6, w = 12, x = 0, y = 12 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "(node_filesystem_avail_bytes{mountpoint=\"/\", fstype!=\"tmpfs\"} / node_filesystem_size_bytes{mountpoint=\"/\", fstype!=\"tmpfs\"}) * 100"
          instant      = true
          legendFormat = "{{instance}}"
          refId        = "A"
        }]
        fieldConfig = {
          defaults = {
            unit = "percent"
            min  = 0
            max  = 100
            thresholds = {
              mode = "absolute"
              steps = [
                { color = "red", value = null },
                { color = "yellow", value = 15 },
                { color = "green", value = 25 },
              ]
            }
          }
          overrides = []
        }
        options = { orientation = "horizontal", reduceOptions = { calcs = ["lastNotNull"] } }
      },
      {
        id         = 5
        title      = "Active Alert Count by Severity"
        type       = "timeseries"
        gridPos    = { h = 6, w = 12, x = 12, y = 12 }
        datasource = { type = "prometheus", uid = "$datasource" }
        targets = [{
          datasource   = { type = "prometheus", uid = "$datasource" }
          expr         = "count by (severity) (ALERTS{alertstate=\"firing\"})"
          legendFormat = "{{severity}}"
          refId        = "A"
        }]
        fieldConfig = {
          defaults = { color = { mode = "palette-classic" }, custom = { lineWidth = 2 } }
          overrides = [
            { matcher = { id = "byName", options = "critical" }, properties = [{ id = "color", value = { fixedColor = "red", mode = "fixed" } }] },
            { matcher = { id = "byName", options = "warning" }, properties = [{ id = "color", value = { fixedColor = "yellow", mode = "fixed" } }] },
          ]
        }
        options = { tooltip = { mode = "multi" } }
      },
    ]
  }
}

# ── Grafana ConfigMaps ────────────────────────────────────────────────────────

resource "kubernetes_manifest" "grafana_dashboard_storage" {
  manifest = {
    apiVersion = "v1"
    kind       = "ConfigMap"
    metadata = {
      name      = "${var.environment}-harvester-storage-health"
      namespace = local.dns
      labels = merge(local.common_labels, {
        grafana_dashboard = "1"
      })
    }
    data = {
      # Key name becomes the filename on disk in Grafana's sidecar — must be unique
      # across all ConfigMaps, otherwise dashboards overwrite each other.
      "${var.environment}-harvester-storage-health.json" = jsonencode(local.dashboard_storage)
    }
  }
}

resource "kubernetes_manifest" "grafana_dashboard_vm" {
  manifest = {
    apiVersion = "v1"
    kind       = "ConfigMap"
    metadata = {
      name      = "${var.environment}-harvester-vm-health"
      namespace = local.dns
      labels = merge(local.common_labels, {
        grafana_dashboard = "1"
      })
    }
    data = {
      "${var.environment}-harvester-vm-health.json" = jsonencode(local.dashboard_vm)
    }
  }
}

resource "kubernetes_manifest" "grafana_dashboard_node" {
  manifest = {
    apiVersion = "v1"
    kind       = "ConfigMap"
    metadata = {
      name      = "${var.environment}-harvester-node-health"
      namespace = local.dns
      labels = merge(local.common_labels, {
        grafana_dashboard = "1"
      })
    }
    data = {
      "${var.environment}-harvester-node-health.json" = jsonencode(local.dashboard_node)
    }
  }
}
