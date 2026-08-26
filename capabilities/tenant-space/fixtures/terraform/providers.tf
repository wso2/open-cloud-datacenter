provider "rancher2" {
  api_url   = var.rancher_url
  token_key = var.rancher_api_token
  insecure  = false
}

provider "harvester" {
  kubeconfig = var.harvester_kubeconfig_path
}

provider "kubernetes" {
  alias       = "harvester"
  config_path = var.harvester_kubeconfig_path
}
