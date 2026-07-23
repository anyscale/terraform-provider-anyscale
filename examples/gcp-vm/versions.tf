# Configure the Anyscale Provider
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
  }
}

provider "google" {
  region = var.gcp_region
}
