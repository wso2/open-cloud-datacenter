locals {
  base_url       = "https://api.asgardeo.io/t/${var.org_name}"
  issuer_url     = "${local.base_url}/oauth2/token"
  auth_endpoint  = "${local.base_url}/oauth2/authorize"
  token_endpoint = "${local.base_url}/oauth2/token"
  jwks_url       = "${local.base_url}/oauth2/jwks"
  discovery_url  = "${local.base_url}/oauth2/token/.well-known/openid-configuration"
}

resource "asgardeo_application" "this" {
  name        = var.app_name
  description = var.description
  access_url  = var.access_url

  oidc {
    grant_types          = ["authorization_code", "refresh_token"]
    callback_urls        = var.callback_urls
    allowed_origins      = var.allowed_origins
    logout_redirect_urls = var.logout_redirect_urls

    pkce {
      mandatory                         = false
      support_plain_transform_algorithm = false
    }

    access_token {
      type                             = "JWT"
      user_access_token_expiry_seconds = 3600
    }

    refresh_token {
      expiry_seconds      = 86400
      renew_refresh_token = true
    }
  }

  advanced {
    skip_login_consent  = var.skip_consent
    skip_logout_consent = var.skip_consent
  }

  claim_configuration {
    dynamic "requested_claims" {
      for_each = var.requested_claims
      content {
        uri       = requested_claims.value.uri
        mandatory = requested_claims.value.mandatory
      }
    }
  }
}
