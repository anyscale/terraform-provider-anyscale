# Configure the Anyscale Provider
terraform {
  required_version = ">= 1.10"
  required_providers {

    anyscale = {
      source = "anyscale/anyscale"
    }

    aws = {
      source  = "hashicorp/aws"
      version = ">= 6.52.0, < 7.0.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}
