# AWS VM Cloud Resource
resource "anyscale_cloud_resource" "aws_vm" {
  cloud_id      = "cld_abc123"
  name          = "vm-aws-us-east-2"
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

    # Optional: only needed if your IAM tooling names the cluster node's
    # instance profile differently from its role (dataplane_iam_role_arn
    # above). Defaults to the same name as dataplane_iam_role_arn when unset.
    # cluster_instance_profile_id = "arn:aws:iam::xxx:instance-profile/cluster-node"
  }

  object_storage {
    bucket_name = "my-bucket"
    region      = "us-east-2"
  }
}

# GCP VM Cloud Resource
resource "anyscale_cloud_resource" "gcp_vm" {
  cloud_id      = "cld_xyz789"
  name          = "vm-gcp-us-central1"
  compute_stack = "VM"

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

# AWS K8S Cloud Resource with File Storage
resource "anyscale_cloud_resource" "eks_with_efs" {
  cloud_id       = "cld_k8s123"
  name           = "k8s-aws-us-west-2"
  cloud_provider = "AWS" # required here: no aws_config block below to infer it from
  compute_stack  = "K8S"

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::367974485317:role/anyscale-eks-operator"
    zones                          = ["us-west-2a", "us-west-2b"]
    # Optional: a Redis endpoint reachable from the data plane, used for Ray
    # GCS fault tolerance. Available on any K8S cloud, not AWS-specific.
    redis_endpoint = "redis.ray-system.svc.cluster.local:6379"
  }

  object_storage {
    bucket_name = "my-eks-bucket"
    region      = "us-west-2"
  }

  file_storage {
    file_storage_id = "fs-0abc123def456789"

    mount_targets {
      address = "fs-0abc123def456789.efs.us-west-2.amazonaws.com"
      zone    = "us-west-2a"
    }

    # Alternatives to EFS mount targets above, for a pre-existing Kubernetes
    # volume instead: a PersistentVolumeClaim by name, or a CSI ephemeral
    # inline volume driver. Both are Kubernetes-only, like this whole block.
    # persistent_volume_claim     = "my-shared-storage-pvc"
    # csi_ephemeral_volume_driver = "csi.example.com"
  }
}

# GCP K8S Cloud Resource with a CSI ephemeral inline volume
resource "anyscale_cloud_resource" "gke_with_csi" {
  cloud_id       = "cld_k8s456"
  name           = "k8s-gcp-us-central1"
  cloud_provider = "GCP" # required here: no gcp_config block below to infer it from
  compute_stack  = "K8S"

  kubernetes_config {
    anyscale_operator_iam_identity = "gke-nodes@my-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
    redis_endpoint                 = "redis.ray-system.svc.cluster.local:6379"
  }

  object_storage {
    # Include the gs:// prefix explicitly for GCP - see the "Cloud Resources"
    # guide for why.
    bucket_name = "gs://my-gke-bucket"
  }

  # A CSI ephemeral inline volume driver, as an alternative to the EFS-style
  # mount_targets shown on the AWS example above - set only one of
  # persistent_volume_claim, csi_ephemeral_volume_driver, or mount_targets.
  file_storage {
    csi_ephemeral_volume_driver = "csi.example.com"

    # persistent_volume_claim = "my-shared-storage-pvc"
  }
}

# Kubernetes Anyscale Operator health, once it has reported in (null for VM,
# and null for a K8S resource whose operator hasn't reported yet)
output "eks_operator_status" {
  value       = anyscale_cloud_resource.eks_with_efs.operator_status
  description = "Health status reported by the Anyscale Operator running in the cluster"
}

output "eks_operator_version" {
  value       = anyscale_cloud_resource.eks_with_efs.operator_version
  description = "Version of the Anyscale Operator that last reported status"
}

output "eks_operator_reported_at" {
  value       = anyscale_cloud_resource.eks_with_efs.reported_at
  description = "Timestamp when the Anyscale Operator last reported status"
}
