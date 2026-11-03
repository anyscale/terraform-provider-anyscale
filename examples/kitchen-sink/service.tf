# --- anyscale_runtime_env_package + anyscale_service (Cloud B) -----------------------------------
# A Ray Serve hello world: ./app is packaged and uploaded to Cloud B's storage, and the service runs
# it as working_dir on the image built in container_images.tf. Apply waits until the service is
# RUNNING; destroy terminates it. Editing a file under ./app uploads a new package and rolls out
# the service.

resource "anyscale_runtime_env_package" "app" {
  source_dir = "${path.module}/app"
  # The cloud of the compute config the service runs on.
  cloud_id = anyscale_cloud.b.id
}

resource "anyscale_service" "hello" {
  name       = "${var.name_prefix}-hello"
  project_id = anyscale_project.b.id

  build_id = anyscale_container_image_build.training.build_id
  # The compute config's config_id (a specific version), not its id.
  compute_config_id = anyscale_compute_config.cc_b.config_id

  ray_serve_config = {
    applications = [
      {
        name         = "hello"
        route_prefix = "/"
        import_path  = "main:app"
        runtime_env = {
          working_dir = anyscale_runtime_env_package.app.uri
        }
      }
    ]
  }

  # Cloud B's nodes read the package from the shared bucket through this policy.
  depends_on = [aws_iam_role_policy_attachment.roles_b_node_s3]
}
