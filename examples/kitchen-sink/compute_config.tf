# --- anyscale_compute_config: one per targeting pattern ----------------------------------------
# worker_nodes is always listed explicitly below on every compute config -- omitting it silently
# launches a cluster with ZERO workers (unless auto_select_worker_config is set), a known prior bug
# this example exists partly to guard against regressing on by example.

resource "anyscale_compute_config" "cc_a_default" {
  name     = "${var.name_prefix}-cc-a-default"
  cloud_id = anyscale_cloud.a.id
  # No cloud_resource set -- defaults to Cloud A's primary resource. a_vm is primary because
  # cloud_a.tf's depends_on makes it the first resource created on Cloud A.

  head_node = {
    instance_type = var.head_node_instance_type
  }

  worker_nodes = [
    {
      name          = "workers"
      instance_type = var.worker_instance_type
      min_nodes     = 0
      max_nodes     = 5
    }
  ]

  idle_termination_minutes = 30
}

resource "anyscale_compute_config" "cc_a_eks" {
  name     = "${var.name_prefix}-cc-a-eks"
  cloud_id = anyscale_cloud.a.id
  # cloud_resource is a NAME (attribute reference, not a hardcoded string), which gives Terraform a
  # dependency edge onto a_eks -- this is what requirement (c) (compute configs tied to a specific
  # cloud resource, not just the cloud) is actually exercising.
  cloud_resource = anyscale_cloud_resource.a_eks.name

  # NOTE: infra_eks.tf's single node group is sized for cluster system components, not Ray
  # workloads -- this compute config demonstrates the attachment/targeting pattern (a
  # cloud_resource-scoped compute config resolving and applying cleanly), not a verified-working K8S
  # workload. See README.md and docs/guides/cloud-resources.md's Kubernetes section.
  head_node = {
    instance_type = var.head_node_instance_type
  }

  worker_nodes = [
    {
      name          = "workers"
      instance_type = var.worker_instance_type
      min_nodes     = 0
      max_nodes     = 5
    }
  ]

  idle_termination_minutes = 30
}

resource "anyscale_compute_config" "cc_b" {
  name     = "${var.name_prefix}-cc-b"
  cloud_id = anyscale_cloud.b.id

  head_node = {
    instance_type = var.head_node_instance_type
  }

  worker_nodes = [
    {
      name          = "workers"
      instance_type = var.worker_instance_type
      min_nodes     = 0
      max_nodes     = 5
    }
  ]

  idle_termination_minutes = 30
}
