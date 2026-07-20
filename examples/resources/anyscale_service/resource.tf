# Deploy a Ray Serve application as an Anyscale Service. ray_serve_config uses native HCL
# syntax (no jsonencode needed) - the same types.Dynamic convention anyscale_compute_config's
# advanced_instance_config/flags use for similarly open-ended, schemaless config.
#
# A change to ray_serve_config, build_id, or compute_config_id rolls out a new version
# automatically and terraform apply waits for it to become healthy - see this resource's own
# description for the full rollout/destroy behavior.
resource "anyscale_service" "example" {
  name        = "my-service"
  description = "Serves the fraud-detection model"
  project_id  = "prj_abc123" # optional - omit to use your org's default project

  build_id = "cenv_abc123" # from anyscale_container_image_build, or an existing build

  # from anyscale_compute_config - use its config_id output, NOT id. id is the stable
  # name (unchanged across versions); config_id is the version-specific API id this
  # field actually needs. Easy to get backwards since it is not obvious from the name
  # alone - anyscale_compute_config.example.config_id, not .example.id.
  compute_config_id = "cpt_abc123" # also determines the cloud

  ray_serve_config = {
    applications = [
      {
        name         = "fraud_detection"
        route_prefix = "/"
        import_path  = "fraud_detection.main:app"
        runtime_env = {
          working_dir = "https://github.com/my-org/my-repo/archive/refs/heads/main.zip"
        }
        deployments = [
          {
            name         = "Model"
            num_replicas = 2
            ray_actor_options = {
              num_cpus = 1
            }
          }
        ]
      }
    ]
  }

  # Rollout pacing - both optional. rollout_strategy defaults to ROLLOUT (new cluster, shift
  # traffic, converge to 100%). IN_PLACE upgrades the existing cluster instead (faster), but the
  # backend then only permits ray_serve_config to change - a plan changing build_id,
  # compute_config_id, or connection_ids together with rollout_strategy = "IN_PLACE" is rejected
  # at plan time rather than left to fail during apply.
  rollout_strategy  = "ROLLOUT"
  max_surge_percent = 25 # paces the rollout only - it still converges to 100%, never holds at a canary percent
  rollout_timeout   = "30m"

  tags = {
    team        = "ml-platform"
    environment = "production"
  }
}

# Minimal service - only the required inputs. project_id, tags, and the rollout knobs all take
# sensible defaults (project_id resolves to your org's default project for the compute config's
# cloud; rollout_strategy defaults to ROLLOUT; rollout_timeout defaults to 30m).
resource "anyscale_service" "minimal" {
  name     = "minimal-service"
  build_id = "cenv_def456"
  # anyscale_compute_config's config_id output, not its id output - see the note above.
  compute_config_id = "cpt_def456"

  ray_serve_config = {
    applications = [
      {
        name        = "default"
        import_path = "main:app"
      }
    ]
  }
}

# Outputs
output "service_base_url" {
  value       = anyscale_service.example.base_url
  description = "The base URL clients use to reach this service"
}

output "service_state" {
  value       = anyscale_service.example.current_state
  description = "Current lifecycle state, e.g. RUNNING, UNHEALTHY, TERMINATED"
}

# canary_version is null except while a rollout is actively in progress - check for null before
# reading fields off it. Under this resource's declarative auto-rollout, a rollout always
# converges to 100% and terraform apply already waits for RUNNING, so this is mainly useful for
# observing an in-progress rollout from a separate `terraform plan`/refresh, not for gating on.
output "service_is_rolling_out" {
  value       = anyscale_service.example.canary_version != null
  description = "Whether a canary rollout is currently in progress"
}

# primary_version.ray_serve_config is the server's own live copy (a JSON string, decode with
# jsondecode()) - compare it against the ray_serve_config you authored above if you suspect the
# backend has normalized or enriched it; the two are never reconciled automatically (see this
# resource's ray_serve_config description).
output "service_live_ray_serve_config" {
  value       = jsondecode(anyscale_service.example.primary_version.ray_serve_config)
  description = "The primary version's Ray Serve config as last observed from the server"
}
