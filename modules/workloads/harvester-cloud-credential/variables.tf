variable "cluster_name" {
  type        = string
  description = "Name of the RKE2 cluster (used as the ServiceAccount name and in resource naming)"
}

variable "vm_namespace" {
  type        = string
  description = "Harvester namespace where the cluster's VMs run. The ServiceAccount is created here."
}

variable "harvester_api_server" {
  type        = string
  description = "Direct Harvester Kubernetes API server URL used inside the generated kubeconfig (e.g. https://192.168.10.100:6443). This is the address CSI driver pods use from inside RKE2 cluster nodes — the Harvester VIP at port 6443, not the Rancher proxy URL."
}
