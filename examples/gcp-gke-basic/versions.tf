terraform {
  required_version = ">= 1.0"
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
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
