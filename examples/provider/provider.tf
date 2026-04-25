terraform {
  required_providers {
    hyperv = {
      source  = "windsorcli/hyperv"
      version = "~> 0.0"
    }
  }
}

# All provider attributes are optional; environment variables (HYPERV_*) supply
# defaults. See the documentation for the full configuration reference and
# precedence rules.
provider "hyperv" {}
