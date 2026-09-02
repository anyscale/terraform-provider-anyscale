# ---------------------------------------------------------------------------------------------------------------------
# Anyscale Operator identity (EKS Pod Identity)
#
# The Operator authenticates to AWS as a POD, not an EC2 instance, so it needs its own IAM role reachable via EKS Pod
# Identity -- reusing the node group's role (module.eks.eks_managed_node_groups["default"].iam_role_arn) does not
# work: that role's trust policy only allows the ec2.amazonaws.com principal (EC2 instance-profile assumption), not
# pods.eks.amazonaws.com. Without a matching aws_eks_pod_identity_association, the Operator pod falls through Pod
# Identity, then IMDS (which the node group's hop-limit=1 hardening blocks for pods anyway), and fails to start:
# "no EC2 IMDS role found ... GetMetadata, canceled, context deadline exceeded". Confirmed against a real cluster.
# ---------------------------------------------------------------------------------------------------------------------

resource "aws_iam_role" "anyscale_operator" {
  name = "${var.eks_cluster_name}-operator"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "pods.eks.amazonaws.com" }
      Action    = ["sts:AssumeRole", "sts:TagSession"]
    }]
  })

  tags = var.tags
}

# Minimum the Operator has been confirmed to need at startup: it lists/reads/writes the
# object_storage bucket registered on the cloud (anyscale_cloud.primary.object_storage) as part of
# its own cloud-verify check.
resource "aws_iam_role_policy" "anyscale_operator_s3" {
  name = "${var.eks_cluster_name}-operator-s3"
  role = aws_iam_role.anyscale_operator.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "s3:ListBucket",
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject",
      ]
      Resource = [
        module.anyscale_s3.s3_bucket_arn,
        "${module.anyscale_s3.s3_bucket_arn}/*",
      ]
    }]
  })
}

resource "aws_eks_pod_identity_association" "anyscale_operator" {
  cluster_name    = module.eks.cluster_name
  namespace       = "anyscale-operator"
  service_account = "anyscale-operator"
  role_arn        = aws_iam_role.anyscale_operator.arn

  # Requires the eks-pod-identity-agent addon, already part of module.eks's addon set below.
  depends_on = [module.eks]
}
