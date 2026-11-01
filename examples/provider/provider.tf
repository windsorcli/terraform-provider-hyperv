terraform {
  required_providers {
    hyperv = {
      source  = "windsorcli/hyperv"
      version = "~> 0.3"
    }
  }
}

# All provider attributes are optional; environment variables (HYPERV_*) supply
# defaults. See the documentation for the full configuration reference and
# precedence rules.
provider "hyperv" {}

# WinRM against a stock Windows Server host. max_shells keeps the provider
# under the host's MaxShellsPerUser quota (5 by default) when Terraform runs
# resources in parallel. The password comes from HYPERV_PASSWORD.
provider "hyperv" {
  alias    = "winrm"
  backend  = "winrm"
  host     = "hv01.example.com"
  username = "Administrator"

  winrm = {
    max_shells = 3
  }
}
