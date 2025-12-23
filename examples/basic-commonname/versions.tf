# Configure the Anyscale Provider
terraform {
  required_version = ">= 1.9"
  required_providers {
    anyscale = {
      source = "github.com/brent/anyscale"
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
provider "anyscale" {
  # Token can be set here, or will be read from ANYSCALE_CLI_TOKEN env var or ~/.anyscale/credentials.json
  token = var.anyscale_token

  # API URL defaults to https://console.anyscale.com
  # api_url = "https://console.anyscale.com"
}
