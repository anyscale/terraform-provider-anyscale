# The Anyscale Scheduler configuration is a single document per organization.
# Declare it in exactly one Terraform configuration: this resource owns the
# whole document, so a second configuration managing it would revert the first.

resource "anyscale_scheduler_config" "this" {
  # Named hardware profiles that queues allocate quota against.
  resource_flavors = [
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

  # Queues that workloads are admitted into. Both queues share a cohort, so
  # unused quota in one is available to the other.
  resource_queues = [
    {
      name        = "batch"
      cohort_name = "shared"

      preemption = {
        within_resource_queue = "lower_priority"
        reclaim_within_cohort = "any"
        borrow_within_cohort  = "lower_priority"
      }

      resource_groups = [
        {
          covered_resources = ["cpu", "memory_gb"]
          flavors = [
            {
              name = "cpu-standard"
              resources = [
                { name = "cpu", nominal_quota = 256 },
                { name = "memory_gb", nominal_quota = 1024 },
              ]
            },
          ]
        },
        {
          covered_resources = ["gpu"]
          flavors = [
            {
              name = "gpu-a10"
              # Omitting nominal_quota means unlimited. An explicit 0 would
              # block GPUs for this queue entirely - the two are not the same.
              resources = [
                { name = "gpu", borrowing_limit = 8 },
              ]
            },
          ]
        },
      ]
    },
    {
      name        = "interactive"
      cohort_name = "shared"

      resource_groups = [
        {
          covered_resources = ["cpu", "memory_gb"]
          flavors = [
            {
              name = "cpu-standard"
              resources = [
                { name = "cpu", nominal_quota = 64, lending_limit = 32 },
                { name = "memory_gb", nominal_quota = 256 },
              ]
            },
          ]
        },
      ]
    },
  ]

  # Rules are evaluated top to bottom and the first match wins. Once any rule
  # exists, a workload matching none of them is rejected, so the last rule here
  # is a deliberate catch-all.
  scheduling_rules = [
    {
      selector = [
        {
          key      = "workload-type"
          operator = "in"
          values   = ["notebook", "workspace"]
        },
      ]
      resource_queue = "interactive"

      priority_policy = {
        default      = 100
        min          = 50
        max          = 200
        on_violation = "reject"
      }
    },
    {
      resource_queue = "batch"
    },
  ]

  # Stored and returned by the API, but the scheduler does not act on it yet.
  recycle_policy = {
    rotation_interval = "24h"
    max_idle_duration = "10m"
  }
}
