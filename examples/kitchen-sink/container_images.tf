# --- anyscale_container_image_build + anyscale_container_image_registry ------------------------
# Two distinct images, not two ways to make the same one: a training image built from source, and
# a pre-built base image registered from an existing public registry. Neither references
# anyscale_compute_config above -- Anyscale doesn't tie a compute config to a specific image at the
# infrastructure level, you pick both at job/service submission time. See
# docs/guides/container-images.md for the full explanation.

resource "anyscale_container_image_build" "training" {
  name       = "${var.name_prefix}-training-image"
  project_id = anyscale_project.a.id

  containerfile = <<-EOT
    FROM anyscale/ray:2.9.0-py310
    RUN pip install --no-cache-dir pandas scikit-learn
  EOT

  build_timeout = "30m"
}

resource "anyscale_container_image_registry" "base" {
  name      = "${var.name_prefix}-base-image"
  image_uri = var.registry_image_uri
}
