# Build a container image from an inline Containerfile
resource "anyscale_container_image_build" "inline" {
  name = "my-custom-image"

  containerfile = <<-EOT
    FROM anyscale/ray:2.9.0-py310
    RUN pip install --no-cache-dir pandas scikit-learn
  EOT

  build_timeout = "30m"
}

# Build from a Containerfile checked into the repo, scoped to a project.
# Updating the file's contents triggers a new build revision.
resource "anyscale_container_image_build" "from_file" {
  name               = "training-image"
  containerfile_path = "${path.module}/Containerfile"
  project_id         = "prj_abc123"
}

# Outputs
output "build_image_uri" {
  value       = anyscale_container_image_build.inline.image_uri
  description = "The URI of the built container image, for use in compute configs or job submissions"
}

output "build_status" {
  value       = anyscale_container_image_build.inline.build_status
  description = "The current status of the build (pending, in_progress, succeeded, failed, pending_cancellation, canceled)"
}
