# First-touch bootstrap of a freshly installed Harvester cluster: logs in with
# the initial admin password, sets a new one, and writes an admin kubeconfig to
# kubeconfig_path for every later layer to use.
#
# harvester_bootstrap only works when the caller configures the provider in
# bootstrap mode — this module declares no provider config (see README.md):
#
#   provider "harvester" {
#     bootstrap = true
#   }
#
# Every argument is ForceNew, and the resource has no real delete, so changing
# any input re-runs the login/password-change against the cluster.
resource "harvester_bootstrap" "this" {
  api_url          = var.api_url
  initial_password = var.initial_password
  password         = var.password
  kubeconfig       = var.kubeconfig_path
}
