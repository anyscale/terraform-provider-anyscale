# Import using the cloud ID you assert is currently the org default - the API has no separate
# identifier for "the org default pointer" itself, so the asserted cloud_id doubles as the
# import key. Import fails cleanly if that cloud is not actually the current org default.
terraform import anyscale_organization_default_cloud.this cld_abc123
