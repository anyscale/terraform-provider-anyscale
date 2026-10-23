# Build a container image from an inline Containerfile
resource "anyscale_container_image_build" "inline" {
  name = "my-custom-image"

  containerfile = <<-EOT
    FROM anyscale/ray:2.9.0-py310
    RUN pip install --no-cache-dir pandas scikit-learn
  EOT

  timeouts {
    create = "30m"
    update = "30m"
  }
}

# Build from a Containerfile checked into the repo, scoped to a project.
# Editing the file's contents does not rebuild; changing the path does.
# To rebuild on content changes, use `containerfile =
# file("${path.module}/Containerfile")` instead.
resource "anyscale_container_image_build" "from_file" {
  name               = "training-image"
  containerfile_path = "${path.module}/Containerfile"
  project_id         = "prj_abc123"
}

# Outputs
output "build_image_uri" {
  value       = anyscale_container_image_build.inline.image_uri
  description = "URI of the built image, for compute configs or jobs"
}

output "build_status" {
  value       = anyscale_container_image_build.inline.build_status
  description = "Current build status (e.g. pending, succeeded)"
}

output "build_image_digest" {
  value       = anyscale_container_image_build.inline.digest
  description = "Digest of the built image; changes on a new revision"
}
