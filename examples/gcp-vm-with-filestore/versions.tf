# Configure the Anyscale Provider
terraform {
  required_version = ">= 1.9"
  required_providers {
    # anyscale provider uses dev_overrides - no lock file entry needed
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
    time = {
      source  = "hashicorp/time"
      version = ">= 0.9.0"
    }
    anyscale = {
      source  = "terraform-providers/anyscale"
      version = "0.0.1"
    }
  }
}

provider "google" {
  region = var.gcp_region
}
