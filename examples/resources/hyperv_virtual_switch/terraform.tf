# Provider declaration for example validation. tfplugindocs renders only
# resource.tf in the generated documentation, so this file is invisible to
# Registry users -- it exists so `terraform validate` can resolve the
# xeitu/hyperv source against a dev_overrides binary in CI.
terraform {
  required_providers {
    hyperv = {
      source  = "xeitu/hyperv"
      version = "~> 0.3"
    }
  }
}
