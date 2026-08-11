# Import by "<cloud_id>/<cloud_resource_id>" -- the same compound id the resource's `id`
# attribute reads back as. Do this before your first `apply` against a cloud that already has a
# mapping configured outside Terraform: Create is just as authoritative as Update, so applying
# against a cloud with real, undeclared rules replaces them exactly as an Update would.
terraform import anyscale_cloud_iam_mapping.production cld_00000000000000000000000000/cldrsrc_00000000000000000000000000

# A bare cloud_id is also accepted -- it resolves to the cloud's primary (is_default) deployment.
# This errors explicitly if the cloud has zero or more than one primary deployment, rather than
# guessing.
terraform import anyscale_cloud_iam_mapping.production cld_00000000000000000000000000

# Note: importing a cloud that has no mapping configured at all succeeds -- rules reads back as
# null. That is not an error; it is a legitimate starting point for declaring a mapping on a cloud
# that has never had one.
