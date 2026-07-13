terraform {
  required_providers {
    hyperv = {
      source  = "xeitu/hyperv"
      version = "~> 0.3"
    }
  }
}

provider "hyperv" {
  # Reads HYPERV_HOST / HYPERV_USERNAME / HYPERV_PASSWORD / HYPERV_BACKEND
  # etc. from .env.local. See ../../../.env.example for the full list.
}
