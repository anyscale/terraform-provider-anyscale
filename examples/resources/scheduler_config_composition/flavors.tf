# resource_flavors is an ORDERED list - the scheduler tries flavors within a
# resource_group in the order they appear. That ordering can only be expressed
# as a list, so a list stays the source of truth even when split across files;
# never rewrite this section as a map keyed by name, which would make the
# order a lexicographic accident that still plans and applies cleanly.
locals {
  cpu_flavors = [
    {
      name = "cpu-standard"
      selector = [
        {
          key      = "node.kubernetes.io/instance-type"
          operator = "in"
          values   = ["m5.4xlarge", "m5.8xlarge"]
        },
      ]
    },
  ]

  gpu_flavors = [
    {
      name = "gpu-a10"
      selector = [
        {
          key      = "accelerator"
          operator = "in"
          values   = ["A10G"]
        },
      ]
    },
  ]

  # concat() preserves order across files: cpu_flavors' entries still come
  # before gpu_flavors' entries in the document the API receives.
  flavors = concat(local.cpu_flavors, local.gpu_flavors)

  # Derived for by-name lookup only - never the source of truth for order.
  # Referenced from queues.tf via local.flavor_by_name[...], which also makes
  # this a free duplicate-name guard: a repeated flavor name here fails at
  # `terraform plan` with "Duplicate object key", not a silent drop.
  flavor_by_name = { for f in local.flavors : f.name => f }
}
