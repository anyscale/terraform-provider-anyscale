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
  enable_system_cluster   = false
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
    # Optional: a Redis endpoint reachable from the data plane, used for Ray
    # GCS fault tolerance. Available on any K8S cloud, not AWS-specific.
    redis_endpoint = "redis.ray-system.svc.cluster.local:6379"
  }

  object_storage {
    bucket_name = "my-eks-bucket"
    region      = "us-west-2"
  }

  # Optional: shared file storage for the Ray cluster. mount_targets (EFS)
  # below is one option; persistent_volume_claim and csi_ephemeral_volume_driver
  # are alternatives to it, not additions - set only one (see the GKE example
  # below for the persistent_volume_claim form).
  file_storage {
    file_storage_id = "fs-0abc123def456789"

    mount_targets {
      address = "fs-0abc123def456789.efs.us-west-2.amazonaws.com"
      zone    = "us-west-2a"
    }

    # persistent_volume_claim     = "my-shared-storage-pvc"
    # csi_ephemeral_volume_driver = "csi.example.com"
  }
}

# GCP GKE (Kubernetes)
resource "anyscale_cloud" "gke_example" {
  name           = "my-gke-cloud"
  cloud_provider = "GCP"
  region         = "us-central1"
  compute_stack  = "K8S"

  kubernetes_config {
    anyscale_operator_iam_identity = "gke-nodes@my-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
    redis_endpoint                 = "redis.ray-system.svc.cluster.local:6379"
  }

  object_storage {
    # Include the gs:// prefix explicitly for GCP, same as the GCP VM example
    # above.
    bucket_name = "gs://my-gke-bucket"
  }

  # Optional: a pre-existing PersistentVolumeClaim for shared storage, as an
  # alternative to EFS/Filestore-style mount_targets (see the EKS example
  # above) - set only one of persistent_volume_claim, csi_ephemeral_volume_driver,
  # or mount_targets.
  file_storage {
    persistent_volume_claim = "my-shared-storage-pvc"
  }
}

# Azure AKS (Kubernetes)
#
# Azure is Kubernetes-only: Anyscale does not support Azure VM clouds, so
# compute_stack must be "K8S" - anything else (including the default when
# omitted) is a plan-time error. Unlike aws_config/gcp_config, azure_config
# takes only tenant_id: AKS setup creates no VNet/subnet resources of its own,
# and authentication is operator workload-identity federation, not network or
# IAM-role wiring.
#
# This example is schema- and mock-validated, not validated against a real AKS
# cluster the way the EKS and GKE examples above are - validate it against
# your own Azure subscription before relying on it.
resource "anyscale_cloud" "aks_example" {
  name           = "my-aks-cloud"
  cloud_provider = "AZURE"
  region         = "eastus2"
  compute_stack  = "K8S"

  azure_config {
    tenant_id = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
  }

  kubernetes_config {
    # The managed identity's PRINCIPAL ID, not its client ID - the reference
    # AKS setup flow uses principal ID here and client ID only in the
    # operator's own values.yaml.
    anyscale_operator_iam_identity = "11111111-2222-3333-4444-555555555555"
    # Azure availability zones are plain digits, unlike AWS/GCP's region-suffixed names.
    zones          = ["1", "2"]
    redis_endpoint = "redis.ray-system.svc.cluster.local:6379"
  }

  object_storage {
    # Azure uses its own abfss:// URI, never s3:// or gs:// - passed through
    # verbatim with no prefix rewriting. Must include the full
    # container@account.dfs.core.windows.net form, not just a bucket name.
    bucket_name = "abfss://ray-storage@anyscalestorageacct.dfs.core.windows.net"
  }
}

# Empty Cloud (Multi-Resource Cloud Pattern)
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

output "cloud_name" {
  value       = anyscale_cloud.aws_example.name
  description = "The name of the cloud"
}

output "cloud_is_default" {
  value       = anyscale_cloud.aws_example.is_default
  description = "Whether this cloud is the organization's default cloud (read-only, managed by Anyscale)"
}

output "is_empty_cloud" {
  value       = anyscale_cloud.empty.is_empty_cloud
  description = "Whether the cloud is empty (no embedded configuration)"
}
