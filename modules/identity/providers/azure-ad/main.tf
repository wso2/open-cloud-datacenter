# ── Azure AD OIDC endpoints (computed from tenant_id) ────────────────────────
locals {
  base_url       = "https://login.microsoftonline.com/${var.tenant_id}/v2.0"
  issuer_url     = local.base_url
  auth_endpoint  = "https://login.microsoftonline.com/${var.tenant_id}/oauth2/v2.0/authorize"
  token_endpoint = "https://login.microsoftonline.com/${var.tenant_id}/oauth2/v2.0/token"
  jwks_url       = "https://login.microsoftonline.com/${var.tenant_id}/discovery/v2.0/keys"
  discovery_url  = "${local.base_url}/.well-known/openid-configuration"
}

# ── TODO: Automated app registration ─────────────────────────────────────────
# The resources below are not yet implemented. The module currently operates
# in "bring your own app" mode: create the app registration in the Azure portal
# (or via az CLI) and pass client_id / client_secret as variables.
#
# When implementing, add:
#
# resource "azuread_application" "this" {
#   display_name = var.app_name
#   web {
#     redirect_uris = var.callback_urls
#     implicit_grant { access_token_issuance_enabled = false }
#   }
#   required_resource_access { ... }
# }
#
# resource "azuread_service_principal" "this" {
#   client_id = azuread_application.this.client_id
# }
#
# resource "azuread_application_password" "this" {
#   application_id = azuread_application.this.id
# }
#
# Then remove var.client_id / var.client_secret and source them from the
# resources instead.
