variable "image" {
  type        = string
  description = "Full container image reference for the keyvault-operator manager binary, including tag or digest. Example: ghcr.io/<owner>/keyvault-operator:v0.0.2"
}

variable "namespace" {
  type        = string
  description = "Kubernetes namespace where the operator's Deployment + RBAC live. CRDs are cluster-scoped and are NOT created in this namespace."
  default     = "keyvault-system"
}

variable "name_prefix" {
  type        = string
  description = "Prefix applied to every Kubernetes object name (ServiceAccount, ClusterRole, Role, Deployment, Service). Matches the kvi- prefix the operator's kustomize-default emits."
  default     = "kvi-"
}

variable "replicas" {
  type        = number
  description = "Number of operator manager replicas. Leader election is enabled, so only one is active at a time."
  default     = 1
}

variable "leader_elect" {
  type        = bool
  description = "Whether the manager runs with --leader-elect. Required when replicas > 1; harmless when replicas = 1."
  default     = true
}

variable "image_pull_secret" {
  type = object({
    server   = string
    username = string
    password = string
    email    = optional(string, "noreply@example.com")
  })
  description = "Optional docker-registry credentials for pulling the operator image from a private registry. When null, the module skips creating an image-pull Secret and the cluster relies on node-level pull credentials. When set, the Deployment references a Secret named <name_prefix>image-pull."
  default     = null
  sensitive   = true
}

variable "resources" {
  type = object({
    limits = object({
      cpu    = string
      memory = string
    })
    requests = object({
      cpu    = string
      memory = string
    })
  })
  description = "Compute resources for the manager container. Defaults match what the operator's kubebuilder-default scaffolding emits."
  default = {
    limits   = { cpu = "500m", memory = "128Mi" }
    requests = { cpu = "10m", memory = "64Mi" }
  }
}

variable "common_labels" {
  type        = map(string)
  description = "Labels merged into every object the module creates. Useful for tagging by environment, team, etc."
  default     = {}
}
