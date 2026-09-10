# The scheduler config is a singleton per organization, so it is imported by
# organization ID. The ID must match the organization the provider's token
# authenticates to - the anyscale_organization data source reports it.
terraform import anyscale_scheduler_config.this org_abcdefghijklmnopqrstuvwxyz
