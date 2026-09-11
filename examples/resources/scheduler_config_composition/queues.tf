# resource_queues makes no ordering claim, so - unlike flavors.tf - the map IS
# the source of truth here, keyed by queue name. main.tf turns it into the
# list the API expects with values(local.queues); iteration order is
# lexicographic by key, which is harmless only because this section carries no
# ordering semantics. Do not copy that values() call onto flavors or
# scheduling_rules, both of which are ordered.
locals {
  queues = {
    batch = {
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
              # Referenced through flavor_by_name rather than the bare string
              # "cpu-standard" - a typo here fails at plan with a source
              # location instead of a server error mid-apply.
              name = local.flavor_by_name["cpu-standard"].name
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
              name = local.flavor_by_name["gpu-a10"].name
              # Omitting nominal_quota means unlimited. An explicit 0 would
              # block GPUs for this queue entirely - the two are not the same.
              resources = [
                { name = "gpu", borrowing_limit = 8 },
              ]
            },
          ]
        },
      ]
    }

    interactive = {
      name        = "interactive"
      cohort_name = "shared"

      resource_groups = [
        {
          covered_resources = ["cpu", "memory_gb"]
          flavors = [
            {
              name = local.flavor_by_name["cpu-standard"].name
              resources = [
                { name = "cpu", nominal_quota = 64, lending_limit = 32 },
                { name = "memory_gb", nominal_quota = 256 },
              ]
            },
          ]
        },
      ]
    }
  }
}
