# Shared AWS foundation: ONE VPC and ONE S3 bucket, reused by every Anyscale cloud/cloud-resource in
# this example (Cloud A's VM leg, Cloud A's EKS leg, and Cloud B) -- these are the slow/expensive
# layer of the whole apply. IAM roles are split two ways instead of shared; see the comment above
# module.anyscale_iam_roles_a below for why. infra_eks.tf builds the EKS cluster inside this same
# VPC. If you'd rather point this example at AWS infrastructure you already have instead of building
# it fresh, these two files (infra_aws.tf/infra_eks.tf) are the only ones to swap out -- replace the
# module.* references below with data sources or variables for your existing VPC/subnet/IAM/S3 IDs
# (no worked example of that swap exists in this repo yet -- every example here, including
# examples/aws-vm-basic-resource, builds this infrastructure fresh via modules too).

data "aws_caller_identity" "current" {}

locals {
  public_subnets  = ["172.24.101.0/24", "172.24.102.0/24"]
  private_subnets = ["172.24.20.0/24", "172.24.21.0/24"]

  tags = merge(
    { anyscale-deploy-environment = var.anyscale_deploy_env },
    var.tags,
  )
}

module "anyscale_vpc" {
  #checkov:skip=CKV_TF_1: Example code should use the latest version of the module
  #checkov:skip=CKV_TF_2: Example code should use the latest version of the module
  # tflint-ignore: terraform_module_pinned_source
  source = "github.com/anyscale/terraform-aws-anyscale-cloudfoundation-modules//modules/aws-anyscale-vpc"

  anyscale_vpc_name = "anyscale-${var.name_prefix}"
  cidr_block        = "172.24.0.0/16"

  public_subnets  = local.public_subnets
  private_subnets = local.private_subnets

  public_subnet_tags = {
    "kubernetes.io/role/elb" = "1"
  }
  private_subnet_tags = {
    "kubernetes.io/role/internal-elb" = "1"
  }
}

#trivy:ignore:avd-aws-0132
module "anyscale_s3" {
  #checkov:skip=CKV_TF_1: Example code should use the latest version of the module
  #checkov:skip=CKV_TF_2: Example code should use the latest version of the module
  # tflint-ignore: terraform_module_pinned_source
  source = "github.com/anyscale/terraform-aws-anyscale-cloudfoundation-modules//modules/aws-anyscale-s3"

  module_enabled = true

  # global, not the module's own account-regional default: this example names the bucket explicitly
  # (prefix + region + account id) rather than letting AWS generate an account-regional-namespaced
  # name, and the AWS provider rejects an explicit name that doesn't already carry the required
  # -{account_id}-{region}-an suffix under account-regional. Baking the account id into the name
  # itself instead makes it globally unique by design.
  bucket_namespace     = "global"
  anyscale_bucket_name = "${var.name_prefix}-${var.aws_region}-${data.aws_caller_identity.current.account_id}"
  force_destroy        = var.anyscale_s3_force_destroy

  tags = local.tags
}

# The VPC submodule above creates no security group of its own (VPC/subnets/routing only) -- this
# is the one every cloud resource in this example shares: all traffic within the VPC (needed for
# EKS node-to-node and control-plane traffic) plus the ports a VM leg needs from outside it.
#trivy:ignore:avd-aws-0104
resource "aws_security_group" "shared" {
  #checkov:skip=CKV2_AWS_5: "Ensure that Security Groups are attached to another resource"
  #checkup:skip=CKV_AWS_382: "Egress is allowed to the internet for the Anyscale Control Plane and other services."
  name        = "${var.name_prefix}-shared"
  description = "Shared security group for the kitchen-sink example: all traffic within the VPC, plus SSH/HTTPS from customer_ingress_cidr_ranges for the VM legs."
  vpc_id      = module.anyscale_vpc.vpc_id

  ingress {
    description = "Allow all traffic from within the VPC"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = [module.anyscale_vpc.vpc_cidr_block]
  }

  ingress {
    description = "HTTPS from customer_ingress_cidr_ranges, for the VM legs"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = split(",", var.customer_ingress_cidr_ranges)
  }

  ingress {
    description = "SSH from customer_ingress_cidr_ranges, for the VM legs"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = split(",", var.customer_ingress_cidr_ranges)
  }

  egress {
    description = "Allow all traffic to the internet"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = local.tags
}

# TWO IAM role sets, not one -- verified against the backend (backend/db/migrations,
# clouds.external_id has a UNIQUE INDEX per non-deleted row within an org: two `anyscale_cloud`s
# cannot share the same external_id, even though the S3 bucket and IAM role ARNs themselves have no
# such constraint and CAN be reused across clouds). Cloud A's a_vm and Cloud B are both aws_config
# (VM) resources that each set external_id, so each needs its own role/external_id pair; Cloud A's
# a_eks uses kubernetes_config instead, which has no external_id field at all, so it just borrows
# roles_a's S3 policy below. Both role sets are cheap/fast IAM resources (seconds, not the
# VPC/EKS/S3 minutes-long layer), so duplicating them doesn't undercut "one shared VPC."
#trivy:ignore:avd-aws-0342
module "anyscale_iam_roles_a" {
  #checkov:skip=CKV_TF_1: Example code should use the latest version of the module
  #checkov:skip=CKV_TF_2: Example code should use the latest version of the module
  # tflint-ignore: terraform_module_pinned_source
  source = "github.com/anyscale/terraform-aws-anyscale-cloudfoundation-modules//modules/aws-anyscale-iam"

  module_enabled = true

  create_anyscale_access_role          = true
  create_cluster_node_instance_profile = true
  create_iam_s3_policy                 = true

  anyscale_org_id        = var.anyscale_org_id
  anyscale_external_id   = "${var.anyscale_external_id}-a"
  anyscale_s3_bucket_arn = module.anyscale_s3.s3_bucket_arn

  tags = local.tags
}

#trivy:ignore:avd-aws-0342
module "anyscale_iam_roles_b" {
  #checkov:skip=CKV_TF_1: Example code should use the latest version of the module
  #checkov:skip=CKV_TF_2: Example code should use the latest version of the module
  # tflint-ignore: terraform_module_pinned_source
  source = "github.com/anyscale/terraform-aws-anyscale-cloudfoundation-modules//modules/aws-anyscale-iam"

  module_enabled = true

  create_anyscale_access_role          = true
  create_cluster_node_instance_profile = true
  create_iam_s3_policy                 = false # roles_a's S3 policy already covers the shared bucket

  anyscale_org_id        = var.anyscale_org_id
  anyscale_external_id   = "${var.anyscale_external_id}-b"
  anyscale_s3_bucket_arn = module.anyscale_s3.s3_bucket_arn

  tags = local.tags
}
