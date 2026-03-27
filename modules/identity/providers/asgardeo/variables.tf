variable "org_name" {
  type        = string
  description = "Asgardeo organisation name (the subdomain of your Asgardeo tenant)."
}

variable "app_name" {
  type        = string
  description = "Display name for the OIDC application in Asgardeo."
}

variable "description" {
  type        = string
  description = "Human-readable description of the application."
  default     = "Managed by Terraform."
}

variable "access_url" {
  type        = string
  description = "URL users are redirected to after authentication (e.g. https://app.example.com/dashboard)."
}

variable "callback_urls" {
  type        = list(string)
  description = "OAuth2 redirect URIs that Asgardeo will accept after authentication."
}

variable "allowed_origins" {
  type        = list(string)
  description = "Origins permitted to make CORS requests to the Asgardeo token endpoint."
}

variable "logout_redirect_urls" {
  type        = list(string)
  description = "URLs to redirect to after logout."
}

variable "skip_consent" {
  type        = bool
  description = "Skip login and logout consent screens for seamless SSO."
  default     = true
}

variable "requested_claims" {
  type = list(object({
    uri       = string
    mandatory = optional(bool, false)
  }))
  description = <<-EOT
    Local claim URIs to include in the OIDC token. Controls which user attributes
    the relying party (e.g. Rancher) can read from the ID token.

    Common Asgardeo claim URIs:
      - "http://wso2.org/claims/emailaddress"  → email
      - "http://wso2.org/claims/username"      → username (shown as display name)
      - "http://wso2.org/claims/groups"        → group memberships (required for Rancher RBAC)
      - "http://wso2.org/claims/givenname"     → first name
      - "http://wso2.org/claims/lastname"      → last name

    Defaults to email + username + groups, which is the minimum needed for readable
    display names and group-based RBAC in Rancher.
  EOT
  default = [
    { uri = "http://wso2.org/claims/emailaddress", mandatory = false },
    { uri = "http://wso2.org/claims/username", mandatory = false },
    { uri = "http://wso2.org/claims/groups", mandatory = true },
  ]
}
