terraform {
  required_version = ">= 1.10"
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }

    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }

    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.17"
    }

    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.35"
    }
  }
}

provider "google" {
  project = var.google_project_id
  region  = var.gcp_region
}
