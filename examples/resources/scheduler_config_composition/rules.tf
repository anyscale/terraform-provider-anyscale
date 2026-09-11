# scheduling_rules is evaluated top to bottom and the first match wins, so -
# like flavors.tf and unlike queues.tf - it stays a plain ordered list built
# with concat(), never values() or any other map iteration that would reorder
# it. Once any rule exists, a workload matching none of them is rejected, so
# the last rule below is a deliberate catch-all.
locals {
  interactive_rules = [
    {
      selector = [
        {
          key      = "workload-type"
          operator = "in"
          values   = ["notebook", "workspace"]
        },
      ]
      # Referenced through local.queues rather than the bare string
      # "interactive" - a typo here fails at plan with a source location
      # instead of a server error mid-apply.
      resource_queue = local.queues["interactive"].name

      priority_policy = {
        default      = 100
        min          = 50
        max          = 200
        on_violation = "reject"
      }
    },
  ]

  batch_rules = [
    {
      resource_queue = local.queues["batch"].name
    },
  ]

  rules = concat(local.interactive_rules, local.batch_rules)
}
