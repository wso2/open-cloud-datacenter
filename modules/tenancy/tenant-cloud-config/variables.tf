variable "namespace" {
  type        = string
  description = "Kubernetes namespace in the Harvester cluster where the cloud-init ConfigMap is created. Typically the common namespace output from the tenant-space module (e.g. <project_name>-common)."
  validation {
    condition     = trimspace(var.namespace) != ""
    error_message = "namespace must be a non-empty string."
  }
}

variable "ssh_authorized_keys" {
  type        = list(string)
  description = "SSH public keys to inject into the cloud-init template's ssh_authorized_keys field. Defaults to an empty list (no keys injected)."
  default     = []
}

variable "ntp_server" {
  type        = string
  description = "NTP server address written into /etc/systemd/timesyncd.conf. When null (default), the timesyncd config file and NTP runcmds are omitted. Set per-environment via a local (e.g. 192.168.8.254 for LK, pool.ntp.org for US)."
  default     = null
  validation {
    condition     = var.ntp_server == null || trimspace(var.ntp_server) != ""
    error_message = "ntp_server must be null or a non-empty string."
  }
}
