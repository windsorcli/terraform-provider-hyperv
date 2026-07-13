terraform {
  required_providers {
    hyperv = {
      source  = "xeitu/hyperv"
      version = "~> 0.3"
    }
  }
}

# All provider attributes are optional; environment variables (HYPERV_*) supply
# defaults. See the documentation for the full configuration reference and
# precedence rules.
provider "hyperv" {
  backend  = "ssh"
  host     = "192.168.90.50"
  username = "terraform"

  # private_key_path is local to the Terraform runner. All paths used by
  # Hyper-V resources are Windows paths on the remote host.
  ssh = {
    private_key_path = pathexpand("~/.ssh/hyperv_terraform")
    known_hosts_path = pathexpand("~/.ssh/known_hosts")
  }
}
