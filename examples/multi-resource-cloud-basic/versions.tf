# Configure the Anyscale Provider
terraform {
  required_version = ">= 1.9"
  required_providers {

    anyscale = {
      source = "terraform-providers/anyscale"
    }

    aws = {
      source  = "hashicorp/aws"
      version = ">= 6.37.0, < 7.0.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}
