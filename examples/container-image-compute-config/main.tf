# This example shows the two most common ways to get a container image ready for use on
# Anyscale, side by side with a compute config that will run workloads on that image.
#
# IMPORTANT: Terraform does not link these resources together, and that's intentional.
# There is no attribute on anyscale_compute_config that references a container image, and
# no depends_on below -- a container image and a compute config are both just *inputs* you
# hand to a job or service when you submit it (via the Anyscale CLI, SDK, or web console).
# That submission step happens outside Terraform. See the "Container Images" guide
# (docs/guides/container-images.md) for the full explanation.

# --- Option 1: build a new image from a Containerfile -----------------------------------

resource "anyscale_container_image_build" "example" {
  name       = var.image_name
  project_id = var.project_id

  containerfile = <<-EOT
    FROM anyscale/ray:2.9.0-py310
    RUN pip install --no-cache-dir pandas scikit-learn
  EOT

  timeouts {
    create = "30m"
    update = "30m"
  }
}

# --- Option 2: register an image that already exists in your own registry --------------
# Uncomment this block to register a pre-built image instead of building one from source,
# then use its outputs instead of the build's in the outputs below. Registering skips the
# build step entirely -- Anyscale validates that the image is pullable and Ray-compatible,
# then makes it available immediately.
#
# resource "anyscale_container_image_registry" "example" {
#   image_uri = "docker.io/my-org/my-ray-image:2.9.0"
#   name      = var.image_name
#
#   # Only needed for private registries. Omit entirely for public images.
#   # registry_login_secret = var.registry_login_secret
# }

# --- A compute config that will run workloads on the image above ------------------------
# This is a completely independent resource. Anyscale does not require -- or support --
# tying a compute config to a specific container image; the pairing happens at job/service
# submission time, not in Terraform.

resource "anyscale_compute_config" "example" {
  name     = var.compute_config_name
  cloud_id = var.cloud_id

  head_node = {
    instance_type = var.head_node_instance_type
  }

  worker_nodes = [
    {
      instance_type = var.worker_instance_type
      min_nodes     = var.worker_min_nodes
      max_nodes     = var.worker_max_nodes
    }
  ]
}
