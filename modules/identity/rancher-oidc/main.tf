resource "rancher2_auth_config_generic_oidc" "this" {
  client_id            = var.client_id
  client_secret        = var.client_secret
  issuer               = var.issuer_url
  rancher_url          = var.rancher_callback_url
  auth_endpoint        = var.auth_endpoint
  token_endpoint       = var.token_endpoint
  jwks_url             = var.jwks_url
  scopes               = var.scopes
  group_search_enabled = var.group_search_enabled
  access_mode          = var.access_mode
  enabled              = var.enabled
}
