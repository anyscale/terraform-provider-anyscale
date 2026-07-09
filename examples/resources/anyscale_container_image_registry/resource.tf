# Register a public image from Docker Hub
resource "anyscale_container_image_registry" "public" {
  name        = "public-ray-image"
  image_uri   = "docker.io/anyscale/ray:2.9.0-py310"
  ray_version = "2.9.0"
}

# Register a private image from Amazon ECR. The registry_login_secret must
# reference credentials already configured for your cloud (e.g. via IAM
# roles/instance profiles or a secret Anyscale can use to pull the image).
resource "anyscale_container_image_registry" "private_ecr" {
  name                  = "internal-training-image"
  image_uri             = "123456789012.dkr.ecr.us-west-2.amazonaws.com/my-repo:latest"
  registry_login_secret = "ecr-pull-secret"
}

# Outputs
output "registry_id" {
  value       = anyscale_container_image_registry.public.id
  description = "The durable ID of the cluster environment (app config) created to hold this image"
}

output "registry_image_digest" {
  value       = anyscale_container_image_registry.public.digest
  description = "The content digest of this image's current latest successful build (e.g. sha256:...); like build_id, revision, and name_version, it can change on a later refresh if a new build supersedes this one"
}
