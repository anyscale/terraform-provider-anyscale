# Cloud A: BYOC/split pattern, hosting TWO cloud resources on the shared VPC -- a VM leg and an EKS
# (K8S) leg. This is what actually satisfies "mix a VM cloud and a K8s cloud, same provider is fine."
# Only a BYOC/split cloud accepts a second anyscale_cloud_resource at all; an Anyscale-managed
# all-in-one cloud rejects one with a 400. See docs/guides/cloud-resources.md's "Multiple resource
# deployments on one cloud" section for the cardinality rules this file follows.

resource "anyscale_cloud" "a" {
  name           = "${var.name_prefix}-cloud-a"
  cloud_provider = "AWS"
  region         = var.aws_region

  # compute_stack intentionally OMITTED: an empty/split cloud derives its compute stack from
  # whichever resource(s) end up attached to it. Setting it explicitly here would produce a plan
  # inconsistency the moment a_vm/a_eks below report their own (different) values back.
}

resource "anyscale_cloud_resource" "a_vm" {
  cloud_id      = anyscale_cloud.a.id
  name          = "${var.name_prefix}-cloud-a-vm"
  region        = var.aws_region
  compute_stack = "VM"

  aws_config {
    vpc_id             = module.anyscale_vpc.vpc_id
    subnet_ids_to_az   = module.anyscale_vpc.public_subnet_ids_az_map
    security_group_ids = [aws_security_group.shared.id]

    controlplane_iam_role_arn = module.anyscale_iam_roles_a.iam_anyscale_access_role_arn
    dataplane_iam_role_arn    = module.anyscale_iam_roles_a.iam_cluster_node_role_arn
    external_id               = module.anyscale_iam_roles_a.iam_anyscale_access_role_external_id
  }

  object_storage {
    bucket_name = module.anyscale_s3.s3_bucket_id
    region      = var.aws_region
  }
}

resource "anyscale_cloud_resource" "a_eks" {
  # is_default is assigned by the backend to whichever cloud resource was created first (it isn't a
  # settable field) -- this ordering keeps a_vm the default/primary resource on Cloud A, and
  # cc_a_default in compute_config.tf relies on that to land on the VM leg without naming it.
  depends_on = [anyscale_cloud_resource.a_vm]

  cloud_id = anyscale_cloud.a.id
  name     = "${var.name_prefix}-cloud-a-eks"
  region   = var.aws_region
  # No aws_config/gcp_config on this block to infer a region or cloud_provider from, so both are
  # required explicitly -- an omitted region fails at plan time rather than sending an empty one.
  cloud_provider = "AWS"
  compute_stack  = "K8S"

  kubernetes_config {
    anyscale_operator_iam_identity = module.eks.eks_managed_node_groups["default"].iam_role_arn
    zones                          = module.anyscale_vpc.availability_zones
  }

  object_storage {
    bucket_name = module.anyscale_s3.s3_bucket_id
    region      = var.aws_region
  }
}
