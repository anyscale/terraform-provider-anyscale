terraform {
  required_version = ">= 1.0"
  required_providers {
    anyscale = {
      source  = "terraform-providers/anyscale"
      version = "0.0.1" # version is ignored by dev_overrides
    }

    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
  }
}

provider "google" {
  project = var.google_project_id
  region  = var.google_region
}
