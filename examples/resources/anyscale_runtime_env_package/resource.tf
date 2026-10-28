# Package ./app and deploy it as a service's working_dir. Editing a
# file under ./app plans an update to the package and a rollout of the
# service; an unchanged tree (even a fresh clone) plans nothing.
resource "anyscale_runtime_env_package" "app" {
  source_dir = "${path.module}/app"
  # The cloud of the compute config the service runs on.
  cloud_id = "cld_abc123"

  # Added to .git, .venv, venv, __pycache__ and any .gitignore rules.
  excludes = ["tests/"]
}

resource "anyscale_service" "example" {
  name              = "my-service"
  build_id          = "cenv_abc123"
  compute_config_id = "cpt_abc123"

  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
        runtime_env = {
          working_dir = anyscale_runtime_env_package.app.uri
        }
      }
    ]
  }
}

output "package_uri" {
  value       = anyscale_runtime_env_package.app.uri
  description = "Storage URI of the uploaded package"
}
