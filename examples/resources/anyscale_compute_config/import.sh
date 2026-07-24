# Import using the version-specific config ID (not the name).
# Find it via `anyscale compute-config get <name>` or the anyscale_compute_config
# data source's `config_id` attribute.
terraform import anyscale_compute_config.example cpt_abc123

# Or import using name:version - resolved to the matching config_id at import
# time, then behaves identically to importing by config_id. The name must not
# contain a colon. If it matches more than one cloud, import by config_id
# instead and specify which cloud you mean.
terraform import anyscale_compute_config.example my-compute-config:3
