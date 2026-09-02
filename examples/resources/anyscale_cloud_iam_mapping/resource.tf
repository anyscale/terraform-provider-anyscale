# anyscale_cloud_iam_mapping is AUTHORITATIVE and a SINGLETON per cloud deployment: applying this
# configuration replaces the cloud's entire dataplane IAM mapping. Only ever declare one instance
# per cloud_id/cloud_resource_id pair -- a second instance targeting the same deployment will fight
# this one and produce a permanently non-empty plan.
#
# rules is ORDER-SENSITIVE: the first matching rule wins. Reordering rules below is a real
# behavior change, not just a cosmetic diff -- it can change which IAM identity a workload
# receives.
resource "anyscale_cloud_iam_mapping" "production" {
  cloud_id = "cld_00000000000000000000000000"

  rules = [
    {
      # Workloads launched under this project assume a dedicated, tightly-scoped role.
      selector = "project=my-restricted-project"
      value    = "my-restricted-project-role" # AWS: an IAM role name, not an ARN.
    },
    {
      # Everything else launched by this service account still needs its own identity.
      selector = "user=ci-bot@example.com"
      value    = "ci-bot-role"
    },
  ]

  # Required whenever rules is non-empty: what happens when nothing above matches.
  # CLOUD_DEFAULT falls back to the cloud's default IAM role; FAIL rejects the launch outright.
  fallback_rule = "CLOUD_DEFAULT"
}

# Destroying anyscale_cloud_iam_mapping is a REAL REVERT, not a state-only removal -- it clears
# the cloud's mapping via the same API this resource writes through, reverting every launching
# workload to the cloud's single default IAM role.
