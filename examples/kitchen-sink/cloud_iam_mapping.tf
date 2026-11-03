# --- anyscale_cloud_iam_mapping (Cloud B) --------------------------------------------------------
# Routes Cloud B's workloads to IAM identities by selector; the first matching rule wins. This
# resource owns the cloud's whole mapping: applying it replaces whatever is there, and destroying
# it clears the mapping. An empty mapping is a no-op, so this declares one real rule.
#
# The rule sends jobs and services to Cloud B's own node role, the same identity they would get
# by default, so it changes nothing at runtime. On AWS, a rule value is an IAM role name with an
# instance profile of the same name; the IAM module creates both with one name.

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
