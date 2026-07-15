# The EKS cluster for Cloud A's K8S cloud resource, built inside the shared VPC from infra_aws.tf.
# Trimmed to a single managed node group -- this is a kitchen sink for the ANYSCALE PROVIDER's
# resource/data-source surface, not an EKS reference architecture, so the GPU/spot node-group matrix
# examples/aws-eks-basic demonstrates is out of scope here. See that example if you need those.
#
# NOTE: workload execution on this cluster is not exercised by this example -- see
# docs/guides/cloud-resources.md's Kubernetes section and README.md for what "attachment only" means
# here. The single node group below is sized for cluster system components, not Ray workloads.

#trivy:ignore:avd-aws-0038
#trivy:ignore:avd-aws-0040
#trivy:ignore:avd-aws-0041
#trivy:ignore:avd-aws-0104
module "eks" {
  #checkov:skip=CKV_TF_1: Use the given version of the module
  source  = "terraform-aws-modules/eks/aws"
  version = "21.24.0"

  name               = "${var.name_prefix}-eks"
  kubernetes_version = var.eks_cluster_version

  addons = {
    coredns    = {}
    kube-proxy = {}
    # before_compute: install ahead of node join. v21 hardcodes
    # bootstrap_self_managed_addons=false, so without these the cluster gets no CNI and every node
    # stays NotReady.
    eks-pod-identity-agent = { before_compute = true }
    vpc-cni                = { before_compute = true }
  }

  endpoint_public_access = true

  authentication_mode                      = "API_AND_CONFIG_MAP"
  enable_cluster_creator_admin_permissions = true

  vpc_id                   = module.anyscale_vpc.vpc_id
  control_plane_subnet_ids = module.anyscale_vpc.public_subnet_ids
  subnet_ids               = module.anyscale_vpc.private_subnet_ids

  node_security_group_additional_rules = {
    anyscale_ingress_nodes = {
      description = "Node to node ingress - Anyscale ports"
      protocol    = "tcp"
      from_port   = 80
      to_port     = 65535
      type        = "ingress"
      self        = true
    }
  }

  eks_managed_node_groups = {
    # Management components (CoreDNS, Anyscale Operator, etc.) -- see the file header for why this
    # is the only node group in this example, unlike aws-eks-basic's fuller matrix.
    default = {
      ami_type       = "BOTTLEROCKET_x86_64"
      instance_types = ["t3.medium"]

      min_size     = 1
      max_size     = 10
      desired_size = 2

      # NOTE: v21's node-group IMDS hop limit default is 1 (was 2 in v20), so pods can no longer
      # reach these policies via IMDS node-role inheritance; the Anyscale operator already uses the
      # pod identity agent addon above, so it's unaffected.
      iam_role_additional_policies = {
        anyscale_s3_policy = module.anyscale_iam_roles_a.anyscale_iam_s3_policy_arn
      }
    }
  }

  tags = local.tags
}
