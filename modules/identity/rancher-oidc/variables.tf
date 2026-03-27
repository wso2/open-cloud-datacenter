# ── Standardized OIDC inputs (wire from any provider module) ─────────────────

variable "client_id" {
  type        = string
  description = "OAuth2 client ID from the identity provider."
}

variable "client_secret" {
  type        = string
  description = "OAuth2 client secret from the identity provider."
  sensitive   = true
}

variable "issuer_url" {
  type        = string
  description = "OIDC issuer URL."
}

variable "auth_endpoint" {
  type        = string
  description = "OAuth2 authorization endpoint."
}

variable "token_endpoint" {
  type        = string
  description = "OAuth2 token endpoint."
}

variable "jwks_url" {
  type        = string
  description = "JSON Web Key Set URL."
}

# ── Rancher-specific inputs ───────────────────────────────────────────────────

variable "rancher_callback_url" {
  type        = string
  description = "Full callback URL registered in the IdP, including /verify-auth path (e.g. https://rancher.example.com/verify-auth)."
}

variable "scopes" {
  type        = string
  description = "Space-separated OIDC scopes to request."
  default     = "openid profile email groups"
}

variable "group_search_enabled" {
  type        = bool
  description = "Enable group membership lookup via the IdP."
  default     = true
}

variable "groups_field" {
  type        = string
  description = "JWT claim name that contains the user's group memberships (e.g. \"groups\")."
  default     = "groups"
  validation {
    condition     = length(trimspace(var.groups_field)) > 0 && length(regexall("\\s", var.groups_field)) == 0
    error_message = "groups_field must be a non-empty JWT claim name without whitespace."
  }
}

variable "access_mode" {
  type        = string
  description = "Rancher access mode: 'unrestricted', 'restricted', or 'required'."
  default     = "unrestricted"
}

variable "enabled" {
  type        = bool
  description = "Whether to enable this auth provider in Rancher."
  default     = true
}
