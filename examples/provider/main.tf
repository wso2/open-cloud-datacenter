terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

# Option 1: explicit values (local testing only — never commit real tokens)
# provider "dcapi" {
#   endpoint = "https://dcapi.example.com"
#   token    = "dcapi_sa_xxxxx_yyyyy"
# }

# Option 2: environment variables (recommended)
#   export DCAPI_ENDPOINT="https://dcapi.example.com"
#   export DCAPI_TOKEN="dcapi_sa_xxxxx_yyyyy"
provider "dcapi" {}
