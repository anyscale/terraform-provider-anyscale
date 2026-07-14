# Kitchen Sink: every resource and data source this provider registers, wired together into one
# coherent, applyable configuration. See README.md for what this creates and the two gotchas
# (a real email invitation, and why anyscale_organization_collaborator is commented out below).
#
# provider "anyscale" {} needs no arguments here -- token resolution falls through
# ANYSCALE_CLI_TOKEN / ~/.anyscale/credentials.json. See examples/provider/ for the explicit form.
provider "anyscale" {}

# --- anyscale_cloud + anyscale_cloud_resource (split deployment pattern) ----------------------
# An empty cloud, then a separate resource deployment attached to it. This exercises both
# resource types; the all-in-one alternative (aws_config embedded directly on anyscale_cloud) is
# already covered by examples/resources/anyscale_cloud/resource.tf and examples/aws-vm-basic/.
resource "anyscale_cloud" "main" {
  name           = "${var.name_prefix}-cloud"
  cloud_provider = "AWS"
  region         = var.aws_region
}

resource "anyscale_cloud_resource" "main" {
  cloud_id      = anyscale_cloud.main.id
  name          = "${var.name_prefix}-cloud-resource"
  compute_stack = "VM"

  aws_config {
    vpc_id                    = var.aws_vpc_id
    subnet_ids_to_az          = var.aws_subnet_ids_to_az
    security_group_ids        = var.aws_security_group_ids
    controlplane_iam_role_arn = var.aws_controlplane_iam_role_arn
    dataplane_iam_role_arn    = var.aws_dataplane_iam_role_arn
    external_id               = var.aws_external_id
  }

  object_storage {
    bucket_name = var.object_storage_bucket_name
    region      = var.aws_region
  }
}
