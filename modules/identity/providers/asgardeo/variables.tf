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
