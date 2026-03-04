variable "tenant_id" {
  type        = string
  description = "Azure AD tenant ID (Directory ID). Used to construct OIDC endpoints."
}

# ── Manual credentials (until azuread_application is implemented) ─────────────

variable "client_id" {
  type        = string
  description = "Client ID of the Azure AD app registration. Create manually in the Azure portal until automated app registration is implemented."
}

variable "client_secret" {
  type        = string
  description = "Client secret of the Azure AD app registration."
  sensitive   = true
}

# ── Future: passed to azuread_application once implemented ───────────────────

variable "app_name" {
  type        = string
  description = "Display name for the Azure AD application (used when automated registration is implemented)."
  default     = ""
}

variable "callback_urls" {
  type        = list(string)
  description = "OAuth2 redirect URIs (used when automated registration is implemented)."
  default     = []
}

variable "allowed_origins" {
  type        = list(string)
  description = "CORS allowed origins (used when automated registration is implemented)."
  default     = []
}

variable "logout_redirect_urls" {
  type        = list(string)
  description = "Post-logout redirect URIs (used when automated registration is implemented)."
  default     = []
}
