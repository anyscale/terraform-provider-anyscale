# Package ./app and deploy it as the service's working_dir. Editing a
# file under ./app uploads a new package and rolls out the service.
resource "anyscale_runtime_env_package" "app" {
  source_dir = "${path.module}/app"
  # The cloud of the service's compute config.
  cloud_id = "cld_abc123"
}

# A change to a deploy input (ray_serve_config, the image,
# compute_config_id, ...) rolls out a new version, and terraform apply
# waits for it.
resource "anyscale_service" "example" {
  name        = "my-service"
  description = "Serves the fraud-detection model"
  # Optional: omit to use your org's default project.
  project_id = "prj_abc123"

  # An Anyscale base image or a registry URI, resolved to a build at
  # apply time. Use build_id instead for an image Terraform manages.
  image_uri = "anyscale/ray:2.59.0-slim-py313-cu129"

  # anyscale_compute_config's config_id output, not its id.
  compute_config_id = "cpt_abc123"

  ray_serve_config = {
    applications = [
      {
        name         = "fraud_detection"
        route_prefix = "/"
        import_path  = "fraud_detection.main:app"
        runtime_env = {
          working_dir = anyscale_runtime_env_package.app.uri
        }
      }
    ]
  }

  # IN_PLACE upgrades the running cluster but permits changing only
  # ray_serve_config.
  rollout_strategy = "ROLLOUT"

  # true turns the service off but keeps its id and hostname; false
  # (the default) restarts it.
  stopped = false

  tags = {
    team = "ml-platform"
  }
}

output "service_base_url" {
  value       = anyscale_service.example.base_url
  description = "The base URL clients use to reach the service"
}
