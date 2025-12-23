# Configure the Anyscale Provider
terraform {
  required_version = ">= 1.9"
  required_providers {

    anyscale = {
      source  = "terraform-providers/anyscale"
      version = "0.0.1" # or whatever; version is ignored by dev_overrides
    }

    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}

# Configure the Anyscale Provider
# provider "anyscale" {
#   token = var.anyscale_token
#   # base_url = var.anyscale_base_url
# }
