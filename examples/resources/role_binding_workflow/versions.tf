# Role bindings need Terraform 1.10 or later (the provider's floor).
# The commented import block in step6_adopt_existing.tf uses for_each,
# which needs Terraform 1.7 or later, so 1.10 covers it.
terraform {
  required_version = ">= 1.10"

  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }
  }
}

# Credentials come from ANYSCALE_CLI_TOKEN or
# ~/.anyscale/credentials.json (run `anyscale login`). The token decides
# which organization this configuration changes.
provider "anyscale" {}
