# ---------------------------------------------------------------------------------------------------------------------
# Anyscale Operator identity (EKS Pod Identity) for Cloud A's EKS leg (a_eks in cloud_a.tf).
#
# The Operator authenticates to AWS as a POD, not an EC2 instance, so it needs its own IAM role reachable via EKS Pod
# Identity -- reusing the node group's role (module.eks.eks_managed_node_groups["default"].iam_role_arn) does not
# work: that role's trust policy only allows the ec2.amazonaws.com principal (EC2 instance-profile assumption), not
# pods.eks.amazonaws.com. Without a matching aws_eks_pod_identity_association, the Operator pod falls through Pod
# Identity, then IMDS (which the node group's hop-limit=1 hardening blocks for pods anyway), and fails to start:
# "no EC2 IMDS role found ... GetMetadata, canceled, context deadline exceeded". Confirmed against a real cluster
# (see examples/aws-eks-basic/anyscale_operator_iam.tf, same finding).
# ---------------------------------------------------------------------------------------------------------------------

resource "aws_iam_role" "anyscale_operator" {
  name = "${var.name_prefix}-operator"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "pods.eks.amazonaws.com" }
      Action    = ["sts:AssumeRole", "sts:TagSession"]
    }]
  })

  tags = local.tags
}

# Minimum the Operator has been confirmed to need at startup: it lists/reads/writes the
# object_storage bucket registered on the a_eks cloud resource as part of its own cloud-verify check.
# A self-contained policy here rather than reusing anyscale_iam_roles_a's S3 policy output, so this
# fix doesn't depend on that module exposing a stable policy-ARN output.
resource "aws_iam_role_policy" "anyscale_operator_s3" {
  name = "${var.name_prefix}-operator-s3"
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

  # Requires the eks-pod-identity-agent addon, already part of module.eks's addon set in infra_eks.tf.
  depends_on = [module.eks]
}
