# Deploy a Ray Serve application as an Anyscale Service. A change to a
# deploy input (ray_serve_config, the image, compute_config_id, ...)
# rolls out a new version, and terraform apply waits for it.
resource "anyscale_service" "example" {
  name        = "my-service"
  description = "Serves the fraud-detection model"
  # Optional: omit to use your org's default project.
  project_id = "prj_abc123"

  # An Anyscale base image or a registry URI, resolved to a build at
  # apply time. Use build_id instead for an image Terraform manages.
  image_uri = "anyscale/ray:2.56.1-slim-py312-cu129"

  # anyscale_compute_config's config_id output, not its id.
  compute_config_id = "cpt_abc123"

  ray_serve_config = {
    applications = [
      {
        name         = "fraud_detection"
        route_prefix = "/"
        import_path  = "fraud_detection.main:app"
        runtime_env = {
          working_dir = "https://github.com/my-org/my-repo/archive/main.zip"
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

# Minimal service running a build from anyscale_container_image_build
# or anyscale_container_image_registry.
resource "anyscale_service" "minimal" {
  name              = "minimal-service"
  build_id          = "cenv_def456"
  compute_config_id = "cpt_def456"

  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
      }
    ]
  }
}

output "service_base_url" {
  value = anyscale_service.example.base_url
}
