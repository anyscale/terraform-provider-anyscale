# Import using the service ID. Find it via `anyscale service status` or the anyscale_service
# data source's `id` attribute.
terraform import anyscale_service.example service2_abc123

# ray_serve_config is seeded once from the server's stored version on import (it is otherwise
# never refreshed - see this resource's ray_serve_config description). The first `terraform plan`
# after an import may therefore show a normalization diff between the imported value and your own
# HCL, which you resolve by reconciling your configuration with the imported value - a known
# limitation of importing any open/schemaless config.
