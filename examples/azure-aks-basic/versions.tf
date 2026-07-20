terraform {
  required_version = ">= 1.9"
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }

    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

provider "azurerm" {
  features {}
  subscription_id = var.azure_subscription_id
}
