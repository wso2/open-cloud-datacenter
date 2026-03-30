variable "cluster_name" {
  type        = string
  description = "Name of the RKE2 cluster (used as the ServiceAccount name and in resource naming)"

  validation {
    condition = (
      can(regex("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", var.cluster_name)) &&
      length(var.cluster_name) <= 253 &&
      length("harvesterconfig-${var.cluster_name}") <= 253 &&
      length("${var.cluster_name}-harvester-csi-token") <= 253
    )
    error_message = "cluster_name must be a valid Kubernetes DNS-1123 label (lowercase alphanumerics and hyphens only, starting and ending with an alphanumeric) and short enough that derived resource names do not exceed 253 characters."
  }
}

variable "vm_namespace" {
  type        = string
  description = "Harvester namespace where the cluster's VMs run. The ServiceAccount is created here."
}

variable "harvester_api_server" {
  type        = string
  description = "Direct Harvester Kubernetes API server URL used inside the generated kubeconfig (e.g. https://192.168.10.100:6443). This is the address CSI driver pods use from inside RKE2 cluster nodes — the Harvester VIP at port 6443, not the Rancher proxy URL."
}
