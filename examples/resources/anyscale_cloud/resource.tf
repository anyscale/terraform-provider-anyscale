# AWS VM Cloud (All-in-One Pattern)
resource "anyscale_cloud" "aws_example" {
  name           = "my-terraform-cloud"
  cloud_provider = "AWS"
  region         = "us-east-2"
  compute_stack  = "VM"

  # Cloud-level settings
  auto_add_user           = false
  enable_lineage_tracking = false
  enable_log_ingestion    = false
  is_private_cloud        = false

  # AWS-specific configuration
  aws_config {
    vpc_id = "vpc-0343edeee0eab27c3"
    subnet_ids_to_az = {
      "subnet-086ac7bba68e3c1c3" = "us-east-2a"
      "subnet-08a309019a027ec72" = "us-east-2b"
      "subnet-06a825a292bd4d476" = "us-east-2c"
    }
    security_group_ids        = ["sg-064dac0ed5cffc779"]
    controlplane_iam_role_arn = "arn:aws:iam::367974485317:role/anyscale-crossacct-role"
    dataplane_iam_role_arn    = "arn:aws:iam::367974485317:role/anyscale-cluster-node-role"
    external_id               = "org_abc123-external-id"
  }

  # Object storage configuration
  object_storage {
    bucket_name = "my-anyscale-bucket" # s3:// prefix added automatically
    region      = "us-east-2"
  }
}

# GCP VM Cloud
resource "anyscale_cloud" "gcp_example" {
  name           = "my-gcp-cloud"
  cloud_provider = "GCP"
  region         = "us-central1"
  compute_stack  = "VM"

  gcp_config {
    project_id                         = "my-project-123"
    provider_name                      = "projects/123/locations/global/workloadIdentityPools/anyscale/providers/anyscale"
    vpc_name                           = "anyscale-vpc"
    subnet_names                       = ["anyscale-subnet-us-central1"]
    controlplane_service_account_email = "anyscale@my-project.iam.gserviceaccount.com"
    dataplane_service_account_email    = "cluster@my-project.iam.gserviceaccount.com"
  }

  object_storage {
    # Include the gs:// prefix explicitly for GCP (unlike AWS, where a bare
    # bucket name is fine either way) - see the "Cloud Resources" guide for why.
    bucket_name = "gs://my-gcs-bucket"
  }
}

# AWS EKS (Kubernetes)
resource "anyscale_cloud" "eks_example" {
  name           = "my-eks-cloud"
  cloud_provider = "AWS"
  region         = "us-west-2"
  compute_stack  = "K8S"

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::367974485317:role/anyscale-eks-operator-role"
    zones                          = ["us-west-2a", "us-west-2b"]
  }

  object_storage {
    bucket_name = "my-eks-bucket"
    region      = "us-west-2"
  }
}

# Empty Cloud (Split Deployment Pattern)
resource "anyscale_cloud" "empty" {
  name           = "my-empty-cloud"
  cloud_provider = "AWS"
  region         = "us-east-2"

  # No config blocks = empty cloud
  # Resources added via anyscale_cloud_resource
}

# Outputs
output "cloud_id" {
  value       = anyscale_cloud.aws_example.id
  description = "The unique identifier for the cloud"
}

output "is_empty_cloud" {
  value       = anyscale_cloud.empty.is_empty_cloud
  description = "Whether the cloud is empty (no embedded configuration)"
}
