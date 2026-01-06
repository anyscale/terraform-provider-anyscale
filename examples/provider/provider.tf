terraform {
  required_providers {
    anyscale = {
      source  = "github.com/anyscale/terraform-provider-anyscale"
      version = "~> 0.1"
    }
  }
}

provider "anyscale" {
  # Optional: API URL (defaults to https://console.anyscale.com)
  # api_url = "https://console.anyscale.com"

  # Optional: API token
  # Can also be set via ANYSCALE_CLI_TOKEN environment variable
  # or read from ~/.anyscale/credentials.json
  # token = "your-token-here"
}
