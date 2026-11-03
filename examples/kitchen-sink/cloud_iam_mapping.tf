# --- anyscale_cloud_iam_mapping (Cloud B) --------------------------------------------------------
# Routes Cloud B's workloads to IAM identities by selector; the first matching rule wins. This
# resource owns the cloud's whole mapping: apply replaces it, destroy clears it. An empty mapping
# is a no-op, so this declares one real rule.
#
# The rule sends jobs and services to Cloud B's node role, which they get by default anyway, so it
# changes nothing at runtime. On AWS a rule value is an IAM role name with a same-named instance
# profile; the IAM module creates both.

resource "anyscale_cloud_iam_mapping" "b" {
  cloud_id = anyscale_cloud.b.id

  rules = [
    {
      # A set-based selector: matches a job or a service.
      selector = "workload-type in (job, service)"
      value    = module.anyscale_iam_roles_b.iam_cluster_node_instance_profile_name
    },
  ]

  # What a workload no rule matches gets: the cloud's default role.
  fallback_rule = "CLOUD_DEFAULT"
}
