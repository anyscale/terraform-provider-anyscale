# Import using the service ID. Find it via `anyscale service status` or
# the anyscale_service data source's `id` attribute.
terraform import anyscale_service.example service2_abc123

# Import reads ray_serve_config from the running version, so the first
# plan may show Anyscale's normalized copy differing from your HCL.
# rollout_strategy and max_surge_percent cannot be read back, so the
# first plan updates them in place; applying that records them in state
# without redeploying.
