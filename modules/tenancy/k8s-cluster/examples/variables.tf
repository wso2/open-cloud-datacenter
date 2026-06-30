variable "rancher_api_url" {
  type        = string
  description = "Base URL of the Rancher instance"
}

variable "rancher_api_token" {
  type        = string
  sensitive   = true
  description = "Rancher API token. Format: token-xxxxx:xxxxxxxxxx. Get from Rancher UI → Account & API Keys."
}

variable "harvester_cluster_name" {
  type        = string
  description = "Name of the upstream Harvester cluster registered in Rancher."
}

variable "node_password" {
  type        = string
  sensitive   = true
  description = "Password for the ubuntu user on every cluster node VM."
}

variable "ssh_authorized_keys" {
  type        = list(string)
  description = "SSH public keys injected into every node VM."
  default     = []
}

variable "ntp_server" {
  type        = string
  description = "NTP server address for node time synchronisation."
  default     = "pool.ntp.org"
}
