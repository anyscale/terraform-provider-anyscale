# Import using the version-specific config ID (not the name).
# Find it via `anyscale compute-config get <name>` or the anyscale_compute_config
# data source's `config_id` attribute.
terraform import anyscale_compute_config.example cpt_abc123
