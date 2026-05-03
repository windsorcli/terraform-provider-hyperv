terraform {
  required_providers {
    hyperv = {
      source  = "windsorcli/hyperv"
      version = "~> 0.0"
    }
  }
}

provider "hyperv" {
  # Reads HYPERV_HOST / HYPERV_USERNAME / HYPERV_PASSWORD / HYPERV_BACKEND
  # etc. from .env.local. See ../../../.env.example for the full list.
}
