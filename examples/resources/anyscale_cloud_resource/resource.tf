# AWS VM Cloud Resource
resource "anyscale_cloud_resource" "aws_vm" {
  cloud_id      = "cld_abc123"
  resource_name = "vm-aws-us-east-2"
  compute_stack = "VM"

  aws_config {
    vpc_id = "vpc-0343edeee0eab27c3"
    subnet_ids_to_az = {
      "subnet-086ac7bba68e3c1c3" = "us-east-2a"
      "subnet-08a309019a027ec72" = "us-east-2b"
    }
    security_group_ids        = ["sg-064dac0ed5cffc779"]
    controlplane_iam_role_arn = "arn:aws:iam::xxx:role/crossacct"
    dataplane_iam_role_arn    = "arn:aws:iam::xxx:role/cluster-node"
  }

  object_storage {
    bucket_name = "my-bucket"
    region      = "us-east-2"
  }
}

# GCP VM Cloud Resource
resource "anyscale_cloud_resource" "gcp_vm" {
  cloud_id      = "cld_xyz789"
  resource_name = "vm-gcp-us-central1"
  compute_stack = "VM"

  gcp_config {
    project_id                     = "my-project-123"
    provider_name                  = "projects/123/locations/global/workloadIdentityPools/anyscale/providers/anyscale"
    vpc_name                       = "anyscale-vpc"
    subnet_names                   = ["anyscale-subnet-us-central1"]
    anyscale_service_account_email = "anyscale@my-project.iam.gserviceaccount.com"
    cluster_service_account_email  = "cluster@my-project.iam.gserviceaccount.com"
  }

  object_storage {
    bucket_name = "my-gcs-bucket"
  }
}

# AWS K8S Cloud Resource with File Storage
resource "anyscale_cloud_resource" "eks_with_efs" {
  cloud_id      = "cld_k8s123"
  resource_name = "k8s-aws-us-west-2"
  compute_stack = "K8S"

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::367974485317:role/anyscale-eks-operator"
    zones                          = ["us-west-2a", "us-west-2b"]
  }

  object_storage {
    bucket_name = "my-eks-bucket"
    region      = "us-west-2"
  }

  file_storage {
    file_storage_id = "fs-0abc123def456789"
    mount_path      = "/mnt/cluster_storage"
    mount_targets = [
      {
        address = "fs-0abc123def456789.efs.us-west-2.amazonaws.com"
        zone    = "us-west-2a"
      }
    ]
  }
}

# Import example: Use composite ID format cloud_id:resource_name
# terraform import anyscale_cloud_resource.aws_vm "cld_abc123:vm-aws-us-east-2"
