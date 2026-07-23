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

    # Installs Envoy Gateway + the Anyscale Operator, and creates the Gateway
    # API objects that wire them together. See gateway_operator.tf.
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

provider "aws" {
  region = var.aws_region
}

# Exec-plugin token idiom, not data.aws_eks_cluster_auth: that data source
# mints a ~15min-lived token once, early in the graph, and a long apply
# (cluster + node groups + multiple helm installs) can outrun it and fail
# mid-apply with Unauthorized. The exec plugin re-fetches a token at the
# moment kubernetes/helm actually need one. The AWS CLI is already a hard
# prerequisite for this example (you cannot create the cluster without it),
# so this costs nothing extra to require.
locals {
  eks_auth_exec = {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name, "--region", var.aws_region]
  }
}

provider "kubernetes" {
  host                   = module.eks.cluster_endpoint
  cluster_ca_certificate = base64decode(module.eks.cluster_certificate_authority_data)
  exec {
    api_version = local.eks_auth_exec.api_version
    command     = local.eks_auth_exec.command
    args        = local.eks_auth_exec.args
  }
}

provider "helm" {
  kubernetes {
    host                   = module.eks.cluster_endpoint
    cluster_ca_certificate = base64decode(module.eks.cluster_certificate_authority_data)
    exec {
      api_version = local.eks_auth_exec.api_version
      command     = local.eks_auth_exec.command
      args        = local.eks_auth_exec.args
    }
  }
}
