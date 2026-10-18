variable "cluster_name" {
  type        = string
  description = "Name of the downstream RKE2 cluster in Rancher"
}

variable "kubernetes_version" {
  type        = string
  description = "RKE2 Kubernetes version (e.g. v1.32.13+rke2r1)"
}

variable "cloud_credential_id" {
  type        = string
  sensitive   = true
  description = "Harvester cloud credential secret name (cattle-global-data:cc-xxxx)"
  default     = ""
}

variable "cni" {
  type        = string
  description = "CNI plugin for the cluster"
  default     = "cilium"
}

variable "ingress_controller" {
  type        = string
  description = "Ingress controller to use. Accepted values: ingress-nginx (RKE2 default), traefik, none. Ignored when machine_global_config is set explicitly."
  default     = "ingress-nginx"

  validation {
    condition     = contains(["traefik", "ingress-nginx", "none"], var.ingress_controller)
    error_message = "ingress_controller must be one of: traefik, ingress-nginx, none."
  }
}

variable "machine_global_config" {
  type        = string
  description = "Full machine_global_config YAML for the cluster. When set, takes full precedence and ingress_controller is ignored. When null (default), the module generates a config from cni and ingress_controller."
  default     = null
}

variable "registries" {
  type = object({
    configs = optional(list(object({
      hostname        = string
      insecure        = optional(bool, false)
      ca_bundle       = optional(string)
      tls_secret_name = optional(string)

      # For new clusters: provide credentials directly and the module creates
      # the auth secret in fleet-default automatically.
      # Both username and password must be set together — neither can be omitted
      # when the other is present. Mutually exclusive with auth_config_secret_name.
      username = optional(string)
      password = optional(string)

      # For brownfield clusters whose auth secret was created outside Terraform
      # (e.g. via Rancher UI): reference the existing secret name directly.
      # Mutually exclusive with username/password.
      auth_config_secret_name = optional(string)
    })), [])
    mirrors = optional(list(object({
      hostname  = string
      endpoints = list(string)
    })), [])
  })
  description = "Private registry configurations for the cluster. For new clusters supply username/password and the module creates the auth secret. For brownfield clusters whose secret was created outside Terraform, supply auth_config_secret_name instead. Set to null to configure no registries."
  default     = null

  validation {
    condition = var.registries == null ? true : (
      # Hostnames must be unique (case-insensitive)
      length(var.registries.configs) == length(distinct([
        for c in var.registries.configs : lower(trimspace(c.hostname))
      ])) &&
      alltrue([
        for c in var.registries.configs : (
          # hostname must be non-empty
          trimspace(c.hostname) != "" &&
          # username and password must both be set or both be null
          (c.username != null) == (c.password != null) &&
          # when set, username and password must be non-empty
          (c.username == null || trimspace(c.username) != "") &&
          (c.password == null || trimspace(c.password) != "") &&
          # when set, auth_config_secret_name must be non-empty
          (c.auth_config_secret_name == null || trimspace(c.auth_config_secret_name) != "") &&
          # inline credentials and pre-existing secret name are mutually exclusive
          !(c.username != null && c.auth_config_secret_name != null)
        )
      ])
    )
    error_message = "Each registry config must have a unique non-empty hostname; username and password must be set together and non-empty; auth_config_secret_name must be non-empty and cannot be combined with username/password."
  }
}

# ── Machine pools ─────────────────────────────────────────────────────────────
# Each entry produces one rancher2_machine_config_v2 + one pool in the cluster.
# Use a single combined pool for small clusters; separate control-plane / worker
# entries for larger ones.
variable "machine_pools" {
  type = list(object({
    name         = string
    vm_namespace = string
    quantity     = number
    cpu_count    = string # string expected by Harvester API e.g. "4"
    memory_size  = string # GiB as string e.g. "12"
    disk_size    = number # GiB as integer
    image_name   = string # "namespace/image-id"

    # ── Network interfaces ────────────────────────────────────────────────────
    # Simple path (new): declare vm_network and/or storage_network as single refs.
    #   vm_network      → first interface (primary NIC, gets default route)
    #   storage_network → last interface (storage NIC, use-routes: false via cloud-init)
    #   networks        → any additional interfaces inserted between the two above
    #
    # Legacy path (backward compat): set networks with the full list in order.
    #   networks = ["ns/vm-nad", "ns/storage-nad", ...]
    #
    # Final interface order: [vm_network, ...networks, storage_network]
    vm_network      = optional(string)           # primary VM network ref e.g. "kasun-test-net/kasun-test-vlan601"
    storage_network = optional(string)           # storage network ref   e.g. "kasun-test-net/kasun-test-strg-vlan698"
    networks        = optional(list(string), []) # additional or legacy full network list

    control_plane = bool
    etcd          = bool
    worker        = bool
    # machine_labels are applied to Kubernetes nodes (RKEMachinePool.spec.labels).
    # Use these for node selectors and scheduling decisions (e.g. nodepool=build).
    # Note: machine_pools.labels in rancher2 v13 targets MachineDeployment metadata,
    # not the nodes themselves — this variable maps to machine_labels on the resource.
    machine_labels = optional(map(string), {})
    taints = optional(list(object({
      key    = string
      value  = string
      effect = string # NoSchedule | PreferNoSchedule | NoExecute
    })), [])
    # Optional per-pool cloud-init override (full replacement, plain YAML or
    # base64). When set and non-empty, takes precedence over the module-level
    # user_data and the generated template for VMs in this pool only.
    # Resolution: pool user_data → var.user_data → generated template.
    # Not allowed on gvisor = true pools (they need the generated cloud-init).
    # Required for HA control-plane setups where each node needs its own
    # static IP pinned at boot time.
    user_data = optional(string)
    # Optional Kubernetes StorageClass for the VM root disk. When unset or
    # empty, Harvester uses the host cluster's default StorageClass (typically
    # harvester-longhorn-2r). Set this to an SC tuned for low fsync latency
    # (numberOfReplicas=1, dataLocality=strict-local) for clusters whose
    # bootstrap and steady-state are bottlenecked on replicated-write
    # latency, e.g. control-plane clusters that run etcd on Longhorn-backed
    # disks. The StorageClass must already exist on the Harvester host
    # cluster — this module does NOT create it.
    storage_class_name = optional(string)
    # Optional gVisor sandbox runtime for this pool. When true, the generated
    # cloud-init installs the pinned gVisor release (see var.gvisor), registers
    # the runsc handler with RKE2's containerd, and the pool is labelled and
    # (by default) tainted so only RuntimeClass-selected pods land on it.
    # Requires the module-generated cloud-init (var.user_data must be empty).
    gvisor = optional(bool, false)
    # Optional per-pool gVisor release override (apt suite, e.g. "20261012").
    # Falls back to var.gvisor.release. Useful for canarying a new release on
    # a second gVisor pool before rolling it everywhere.
    gvisor_release = optional(string)
  }))
  # Defaults to empty for brownfield callers (manage_rke_config = false).
  # A precondition on the cluster resource enforces at least one pool when
  # manage_rke_config = true.
  default = []

  validation {
    condition = length(var.machine_pools) == length(distinct([for p in var.machine_pools : p.name])) && alltrue([
      for p in var.machine_pools :
      p.quantity > 0 &&
      floor(p.quantity) == p.quantity &&
      p.disk_size > 0 &&
      floor(p.disk_size) == p.disk_size &&
      (p.control_plane || p.etcd || p.worker)
    ])
    error_message = "Each machine pool must have a unique name, integer quantity/disk_size > 0, and at least one role (control_plane, etcd, or worker) enabled."
  }

  validation {
    condition = alltrue([
      for p in var.machine_pools : alltrue([
        for t in p.taints : contains(["NoSchedule", "PreferNoSchedule", "NoExecute"], t.effect)
      ])
    ])
    error_message = "Each taint effect must be one of: NoSchedule, PreferNoSchedule, NoExecute."
  }

  validation {
    condition = alltrue([
      for p in var.machine_pools : (
        (p.vm_network == null || trimspace(p.vm_network) != "") &&
        (p.storage_network == null || trimspace(p.storage_network) != "") &&
        alltrue([for n in p.networks : trimspace(n) != ""])
      )
    ])
    error_message = "Each pool's network refs (vm_network, storage_network, and entries in networks) must be non-empty strings."
  }

  validation {
    condition = alltrue([
      for p in var.machine_pools :
      p.gvisor_release == null || can(regex("^[A-Za-z0-9._-]+$", p.gvisor_release))
    ])
    error_message = "gvisor_release must be an apt suite name such as \"20261012\" (letters, digits, '.', '_' or '-')."
  }
}

# ── gVisor sandbox runtime ────────────────────────────────────────────────────
# Shared settings for every pool with gvisor = true. Has no effect unless at
# least one pool opts in. Follows the upstream recipe: gVisor is installed from
# its signed apt repository pinned to a dated release suite (and apt-mark held),
# RKE2's containerd template extends {{ template "base" . }} with a runsc
# handler, and a RuntimeClass maps workloads to the labelled/tainted pool.
variable "gvisor" {
  type = object({
    # Dated apt suite of the gVisor release every gVisor node installs. Pinned
    # by default; override with the release you have tested. Never use
    # "release"/"latest" in production — every new node would install whatever
    # is newest that day.
    release = optional(string, "20261012")

    apt_repo_url = optional(string, "https://storage.googleapis.com/gvisor/releases")
    apt_key_url  = optional(string, "https://gvisor.dev/archive.key")

    # containerd config template version. 3 = containerd 2.x (RKE2 >= v1.31
    # with embedded containerd 2.x) → config-v3.toml.tmpl + io.containerd.cri.v1.runtime.
    # 2 = containerd 1.7 → config.toml.tmpl + io.containerd.grpc.v1.cri.
    containerd_config_version = optional(number, 3)

    # containerd runtime handler name; the RuntimeClass handler must match.
    runtime_handler = optional(string, "runsc")

    # runsc_config flags written to /etc/containerd/runsc.toml.
    #   platform: systrap works inside Harvester VMs without nested virt; kvm needs nested virt.
    #   systemd_cgroup: must match RKE2's containerd SystemdCgroup setting.
    platform       = optional(string, "systrap")
    systemd_cgroup = optional(bool, true)
    # Extra runsc_config flags (e.g. { debug = "true" }). Overrides the two above on key clash.
    runsc_config = optional(map(string), {})

    # Node label applied to gVisor pools; the RuntimeClass nodeSelector uses it.
    node_label_key   = optional(string, "sandbox.gvisor.dev/runtime")
    node_label_value = optional(string, "true")
    # Taint (key/value = node label) that keeps ordinary pods off gVisor nodes.
    taint        = optional(bool, true)
    taint_effect = optional(string, "NoSchedule")

    # Deliver a cluster-scoped RuntimeClass through rke_config.additional_manifest.
    create_runtime_class = optional(bool, true)
    runtime_class_name   = optional(string, "gvisor")
    # Optional RuntimeClass overhead.podFixed (e.g. { memory = "64Mi", cpu = "50m" }).
    runtime_class_overhead = optional(map(string))

    # Tolerate every NoSchedule taint on the harvester-csi-driver chart so its
    # node plugin DaemonSet also runs on gVisor nodes (PVCs hang otherwise).
    # Only applied when the module controls chart_values on cluster create.
    harvester_csi_tolerations = optional(bool, true)
  })
  description = "gVisor (runsc) settings shared by machine pools with gvisor = true. See the module README §gVisor node pools."
  default     = {}

  validation {
    condition     = can(regex("^[A-Za-z0-9._-]+$", var.gvisor.release))
    error_message = "gvisor.release must be an apt suite name such as \"20261012\" (letters, digits, '.', '_' or '-')."
  }

  validation {
    condition     = contains([2, 3], var.gvisor.containerd_config_version)
    error_message = "gvisor.containerd_config_version must be 2 (containerd 1.7) or 3 (containerd 2.x)."
  }

  validation {
    condition     = contains(["systrap", "kvm", "ptrace"], var.gvisor.platform)
    error_message = "gvisor.platform must be one of: systrap, kvm, ptrace."
  }

  validation {
    condition     = contains(["NoSchedule", "PreferNoSchedule", "NoExecute"], var.gvisor.taint_effect)
    error_message = "gvisor.taint_effect must be one of: NoSchedule, PreferNoSchedule, NoExecute."
  }

  validation {
    condition = can(regex("^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$", var.gvisor.runtime_handler)) && alltrue([
      for k, v in var.gvisor.runsc_config : can(regex("^[A-Za-z0-9_.-]+$", k)) && !can(regex("[\"\\\\\n]", v))
    ])
    error_message = "gvisor.runtime_handler must be an RFC 1123 label (lowercase letters, digits and '-', max 63 chars), and runsc_config keys must be [A-Za-z0-9_.-] with values free of quotes, backslashes and newlines."
  }
}

variable "additional_manifest" {
  type        = string
  description = "Extra Kubernetes manifests (multi-document YAML) Rancher applies to the cluster via rke_config.additional_manifest. The gVisor RuntimeClass is appended automatically when a gVisor pool exists."
  default     = ""
}

# ── Node cloud-init ───────────────────────────────────────────────────────────
variable "node_password" {
  type        = string
  sensitive   = true
  description = "Password for ssh_user on every node, injected via chpasswd.list. Only used when user_data is not set. Leave null to disable password auth."
  default     = null
}

variable "ssh_authorized_keys" {
  type        = list(string)
  description = "SSH public keys to inject into ssh_user on every node. Only used when user_data is not set."
  default     = []
}

variable "ntp_server" {
  type        = string
  description = "NTP server written into /etc/systemd/timesyncd.conf on every node. Only used when user_data is not set."
  default     = "pool.ntp.org"
}

variable "user_data" {
  type        = string
  sensitive   = true
  description = "Full cloud-init user-data override applied to every node VM (plain YAML or base64) whose pool does not set its own user_data. When set, node_password/ssh_authorized_keys/ntp_server are ignored for those pools."
  default     = ""
}

variable "ssh_user" {
  type        = string
  description = "SSH username for the VM OS"
  default     = "ubuntu"
}

# ── Harvester cloud provider ──────────────────────────────────────────────────
variable "enable_harvester_cloud_provider" {
  type        = bool
  description = "When true, configures machine_selector_config with cloud-provider-name: harvester so Rancher deploys the Harvester CSI driver on every node. Pair this with the harvester-cloud-credential module (or an existing harvesterconfig* secret) to supply the credential. Set false only for clusters not running on Harvester infrastructure."
  default     = true
}

variable "cloud_provider_config_secret" {
  type        = string
  description = "harvesterconfig* secret name in fleet-default. For new Terraform-provisioned clusters pass the harvester-cloud-credential module's secret_name output — this preserves the dependency edge so credential creation is ordered before cluster provisioning. For brownfield clusters whose credentials were created outside Terraform (via Rancher UI or manually), provide the existing secret name directly."
  default     = ""
}

# ── Brownfield skip flag ──────────────────────────────────────────────────────
variable "manage_rke_config" {
  type        = bool
  description = "Create/manage machine configs and rke_config block. Set false for brownfield clusters where machine configs cannot be imported."
  default     = true
}

variable "machine_config_overrides" {
  type = map(object({
    kind = string
    name = string
  }))
  description = "Existing machine config kind/name keyed by pool name. When a pool name is present here, no rancher2_machine_config_v2 is created for it and the provided kind/name are used directly. Use this for brownfield pools whose machine configs already exist in Rancher and cannot be imported."
  default     = {}
}

# ── etcd S3 backup (optional) ─────────────────────────────────────────────────
variable "etcd_s3" {
  type = object({
    bucket              = string
    folder              = string
    region              = string
    cloud_credential_id = string
    snapshot_retention  = optional(number, 3)
    snapshot_schedule   = optional(string, "5 23 * * *")
  })
  default     = null
  description = "S3 etcd backup config. Set to null to disable."

  validation {
    condition = var.etcd_s3 == null || (
      trimspace(var.etcd_s3.bucket) != "" &&
      trimspace(var.etcd_s3.region) != "" &&
      trimspace(var.etcd_s3.cloud_credential_id) != "" &&
      try(var.etcd_s3.snapshot_retention, 3) > 0 &&
      floor(try(var.etcd_s3.snapshot_retention, 3)) == try(var.etcd_s3.snapshot_retention, 3) &&
      trimspace(try(var.etcd_s3.snapshot_schedule, "")) != ""
    )
    error_message = "When etcd_s3 is set, bucket/region/cloud_credential_id must be non-empty, snapshot_retention must be a positive integer, and snapshot_schedule must be non-empty."
  }
}

# ── Dynamic Cloud Credential variables ───────────────────────────────────────
variable "create_cloud_credential" {
  type        = bool
  description = "When true, dynamically provisions rancher2_cloud_credential.harvester and fetches cloud provider config using Rancher API rather than requiring predefined inputs."
  default     = false

  validation {
    condition = (
      !var.create_cloud_credential || (
        trimspace(var.rancher_api_url) != "" &&
        trimspace(var.rancher_api_token) != "" &&
        (trimspace(var.harvester_cluster_id) != "" || trimspace(var.harvester_cluster_name) != "")
      )
    )
    error_message = "When create_cloud_credential is true, rancher_api_url and rancher_api_token must be non-empty, and at least one of harvester_cluster_id or harvester_cluster_name must be non-empty."
  }
}

variable "harvester_cluster_id" {
  type        = string
  description = "Upstream host Harvester cluster ID (optional when create_cloud_credential = true, preferred over harvester_cluster_name)"
  default     = ""
}

variable "harvester_cluster_name" {
  type        = string
  description = "Upstream host Harvester cluster name (required when create_cloud_credential = true)"
  default     = ""
}

variable "rancher_api_url" {
  type        = string
  description = "Rancher API Server URL (required when create_cloud_credential = true)"
  default     = ""
}

variable "rancher_api_token" {
  type        = string
  sensitive   = true
  description = "Rancher API bearer token key (required when create_cloud_credential = true)"
  default     = ""
}

variable "harvester_vm_namespace" {
  type        = string
  description = "Harvester host cluster VM namespace where the guest VMs reside (optional when create_cloud_credential = true, defaults to guest cluster name)"
  default     = ""
}

variable "harvester_service_account_name" {
  type        = string
  description = "ServiceAccount name to be created/used in Harvester for cloud provider (optional when create_cloud_credential = true, defaults to Harvester cluster name)"
  default     = ""
}

variable "chart_values" {
  type        = string
  description = "Custom Helm chart values to pass to RKE2"
  default     = ""
}

variable "harvester_credential_rotation" {
  type        = number
  description = <<-EOT
    Bump this number to renew the Harvester cloud credential's kubeconfig token. A fresh
    kubeconfig is fetched dynamically from Rancher (the same mechanism used on initial
    creation) and applied to the existing credential in place — you never need to supply
    kubeconfig content yourself. Only used when create_cloud_credential = true; changing
    it on brownfield clusters (create_cloud_credential = false) has no effect.
  EOT
  default     = 0
}


# ── Cluster membership ────────────────────────────────────────────────────────
# Optional list of users/groups to bind to this cluster after provisioning.
# Defaults to [] — omitting it leaves no additional bindings and does not
# affect any existing clusters.
#
# Identity fields (set exactly one per entry):
#
#   email              — User email address resolved via data "rancher2_principal"
#                        with type = "user". Cleaner than name when you know the
#                        identity is a user and have their email address.
#                        Same caveat as name: unreliable if the account has never
#                        logged in or if multiple Rancher accounts share the email.
#                        Example: "dev@wso2.com"
#
#   name               — Generic display name / email resolved via data "rancher2_principal".
#                        Use with type = "group" for group lookups.
#                        Example: "platform-team"  (with type = "group")
#
#   user_id            — Bare Rancher user ID (no scheme prefix). Most reliable
#                        for local users — immune to duplicate-account ambiguity.
#                        Find it in Rancher UI → Users & Authentication → user row,
#                        or from: rancher2_user.this.id
#                        Example: "u-427g5iiyyg"
#
#   user_principal_id  — Full Rancher principal ID for a user account.
#                        Local Rancher users:  "local://u-427g5iiyyg"
#                        OIDC / SAML users:    "genericoidc_user://email@example.com"
#
#   group_principal_id — Full Rancher principal ID for a group.
#                        OIDC / SAML groups:   "genericoidc_group://my-group-name"
#                        AD groups:            "activedirectory_group://CN=my-group,DC=corp"
#
# role defaults to "cluster-member". Use "cluster-owner" to grant full
# cluster admin. Any custom role template ID is also accepted.
#
# Examples:
#   cluster_members = [
#     # Email lookup — convenient when one Rancher account exists for the address
#     { email = "dev@wso2.com" },
#
#     # Bare user ID — avoids lookup, immune to duplicate accounts
#     { user_id = "u-427g5iiyyg", role = "cluster-owner" },
#
#     # Full user principal ID — local Rancher user
#     { user_principal_id = "local://u-427g5iiyyg" },
#
#     # OIDC user by full principal ID
#     { user_principal_id = "genericoidc_user://dev@wso2.com", role = "cluster-owner" },
#
#     # Group by full principal ID
#     { group_principal_id = "genericoidc_group://platform-team", role = "cluster-owner" },
#
#     # Group name lookup
#     { name = "platform-team", type = "group", role = "cluster-owner" },
#   ]

variable "cluster_members" {
  type = list(object({
    email              = optional(string)         # user email — resolved via data source (type = "user")
    name               = optional(string)         # display name — resolved via data source, use with type
    type               = optional(string, "user") # "user" or "group" — only meaningful with name
    user_id            = optional(string)         # bare user ID, e.g. "u-427g5iiyyg"
    user_principal_id  = optional(string)         # full user principal, e.g. "local://u-427g5iiyyg"
    group_principal_id = optional(string)         # full group principal, e.g. "genericoidc_group://my-group"
    role               = optional(string, "cluster-member")
  }))
  default     = []
  description = "Optional cluster role bindings. Set exactly one of: email, name, user_id, user_principal_id, or group_principal_id. role defaults to cluster-member."

  validation {
    condition = alltrue([
      for m in var.cluster_members :
      length([
        for v in [m.email, m.name, m.user_id, m.user_principal_id, m.group_principal_id] :
        v if v != null && trimspace(v) != ""
      ]) == 1
    ])
    error_message = "Each cluster_members entry must set exactly one of: email, name, user_id, user_principal_id, or group_principal_id (non-empty, non-whitespace)."
  }

  validation {
    condition = alltrue([
      for m in var.cluster_members :
      m.name == null ? m.type == "user" : contains(["user", "group"], m.type)
    ])
    error_message = "When name is set, type must be \"user\" or \"group\". For email, user_id, user_principal_id, or group_principal_id entries, omit type (defaults to \"user\")."
  }
}
