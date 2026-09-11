---
page_title: "Anyscale Scheduler: Per-Identity Rules and Deny-by-Default"
subcategory: "Behavior & Limitations"
description: |-
  A worked example of per-identity priority tiers on one shared queue, and the deny-by-default consequence of omitting a catch-all scheduling rule from anyscale_scheduler_config.
---

# Anyscale Scheduler: Per-Identity Rules and Deny-by-Default

The [`anyscale_scheduler_config`](../resources/scheduler_config.md) resource's own schema
documentation already states the rule that matters here: once `scheduling_rules` contains any
entries, a workload matching none of them is rejected rather than run unscheduled. That is a single
sentence in a schema description, and it is easy to read past it while writing `scheduling_rules` -
the config shown below is the shape that puts it into practice on purpose. Read this before you
write a `scheduling_rules` list keyed on user identity rather than workload type: getting the
selectors right is not enough on its own, because the rules you *don't* write are doing just as
much of the work as the ones you do.

## Per-user priority tiers on a single shared queue

This example gives every workload a shared pool of GPU, CPU, and memory quota, then uses
`scheduling_rules` selectors on a `user` label to give each person a different priority band and a
different set of allowed workload types - all in the same `shared` queue, rather than one queue per
person.

```hcl
resource "anyscale_scheduler_config" "this" {
  # A single catch-all flavor - this example's quota isn't split by hardware
  # type, so one flavor with no selector covers every node.
  resource_flavors = [
    {
      name = "any"
    },
  ]

  resource_queues = [
    {
      name = "shared"

      preemption = {
        within_resource_queue = "lower_priority"
      }

      resource_groups = [
        {
          covered_resources = ["gpu", "cpu", "memory_gb"]
          flavors = [
            {
              name = "any"
              resources = [
                { name = "gpu", nominal_quota = 64 },
                { name = "cpu", nominal_quota = 512 },
                { name = "memory_gb", nominal_quota = 2048 },
              ]
            },
          ]
        },
      ]
    },
  ]

  # Priority bands referenced below: Low = 0-99, Medium = 100-499, High = 500-1000.
  #
  # No catch-all rule at the end of this list is deliberate, not an oversight -
  # see "Deny-by-default" below before copying this pattern.
  scheduling_rules = [
    {
      # alice: every workload type, Medium or High priority.
      selector = [
        { key = "user", operator = "in", values = ["alice"] },
        { key = "workload-type", operator = "in", values = ["job", "service", "workspace"] },
      ]
      resource_queue = "shared"
      priority_policy = {
        min          = 100
        max          = 1000
        default      = 200
        on_violation = "reject"
      }
    },
    {
      # bob: every workload type, Medium priority only.
      selector = [
        { key = "user", operator = "in", values = ["bob"] },
        { key = "workload-type", operator = "in", values = ["job", "service", "workspace"] },
      ]
      resource_queue = "shared"
      priority_policy = {
        min          = 100
        max          = 499
        default      = 200
        on_violation = "reject"
      }
    },
    {
      # carol: jobs and workspaces only, Low priority only. No "service" here,
      # and no rule anywhere below matches it for her - see "Deny-by-default".
      selector = [
        { key = "user", operator = "in", values = ["carol"] },
        { key = "workload-type", operator = "in", values = ["job", "workspace"] },
      ]
      resource_queue = "shared"
      priority_policy = {
        min          = 0
        max          = 99
        default      = 10
        on_violation = "reject"
      }
    },
  ]
}
```

## Deny-by-default: what the missing catch-all rule actually does

Nothing in the HCL above says "reject everyone else" - there is no rule that says that, and that is
exactly the point. The scheduler evaluates `scheduling_rules` top to bottom and admits a workload
under the **first** rule whose selector matches it. Once the list is non-empty, a workload that
matches **none** of the rules is rejected outright rather than falling through to run unscheduled.
With no catch-all rule at the end - one with no `selector`, or one whose selector matches everything
- there is nothing left for an unmatched workload to fall through to. In this configuration that
means, concretely:

- A workload submitted by anyone other than `alice`, `bob`, or `carol` is rejected. There is no
  fourth rule to catch them.
- A `service` workload submitted by `carol` is rejected too, even though `carol` is a name this
  configuration clearly knows about - her selector only lists `job` and `workspace`, so her own
  rule doesn't match it, and neither does any other rule.

Both of those are denials, not accidents, and that is the reason this example has no catch-all:
it's modeling an allowlist of exactly three people and, for one of them, a narrower set of workload
types than the other two. If you want unmatched workloads to run unscheduled instead of being
rejected, either add a final rule with no `selector` (or with a selector broad enough to match
everything you want to allow through), or don't reach for a per-identity selector shape like this
one in the first place - see the `resource.tf` example on the
[`anyscale_scheduler_config` resource page](../resources/scheduler_config.md) for the catch-all-last
form. There is no configuration flag that reverts this behavior; the only lever is which rules you
write, and in what order.

## Why per-identity selectors instead of one queue per person

Everyone here draws from the same `shared` resource queue and its single quota pool - this pattern
gives each person a different **priority band** for preemption purposes, not a separate slice of
capacity. `alice`'s workloads can preempt `bob`'s or `carol`'s once their priority is high enough
(`within_resource_queue = "lower_priority"`), but nobody gets guaranteed hardware nobody else can
touch. If you instead want hard capacity isolation per person or team, give each its own
`resource_queue` (or its own cohort) with its own `resource_groups`, the way the two-queue
`batch`/`interactive` example on the resource page does it - that trades this pattern's flexible
sharing for a guarantee that one person's workloads can never crowd out another's.

## Composing large configs across files

`resource_flavors`, `resource_queues`, and `scheduling_rules` are typed HCL list attributes, not
JSON blobs, so Terraform validates and tab-completes them regardless of size - but a platform team
with a dozen queues and rules per team will still outgrow a single resource block. The fix is
authoring, not schema: pull each section into its own file as locals, and shrink the resource block
to reference them. This is still one resource, one plan, one state address - splitting the
authoring across files does not give any section its own lifecycle or its own apply. (Splitting
`anyscale_scheduler_config` itself into per-section resources was evaluated and rejected: the API
takes one write for the whole document, so positional ordering has no way to be expressed across
separate resources.)

The three sections don't compose the same way, because they don't all order the same way:

- **`resource_flavors`** is ordered - the scheduler tries flavors within a `resource_group` in the
  list's order - and it declares the names other sections reference. Keep the list as the source of
  truth and compose it with `concat()` across files; derive a lookup map only when you need one,
  with `{ for f in local.flavors : f.name => f }`. Never author this section as a map keyed by
  name - iterating a map is lexicographic, not insertion order, so a map-first flavors section
  still plans and applies cleanly while silently reordering which flavor the scheduler tries first.
  This particular `for` expression does buy one thing for free: a duplicate flavor name is a hard
  `Duplicate object key` error at `terraform plan` - a property of this expression, not a guarantee
  every map-shaped local in this guide shares.
- **`resource_queues`** makes no ordering claim, so a map keyed by queue name can be the source of
  truth here. Build the document's list from it (`[for k, q in local.queues : q]` or
  `values(local.queues)` - equivalent, pick whichever reads better) rather than writing the list
  by hand alongside the map.
- **`scheduling_rules`**, and the `flavors` nested inside a `resource_group`, are ordered but
  reference names rather than declaring them. Keep these as plain lists composed with `concat()`,
  and point each name field through the map it refers to -
  `resource_queue = local.queues["batch"].name` - rather than the bare string `"batch"`. A typo in
  the map key fails before any request is made - at the expression's source location, not as a
  400 from the API mid-apply.

If the same map is fed to `for_each` to generate separate resource instances, rather than into a
`for` or `concat()` expression inside one resource body, ordering is gone entirely - resource
instances have no sequence. The same data behaves differently depending on which idiom consumes it.

See [`examples/resources/scheduler_config_composition`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/resources/scheduler_config_composition)
for a complete, runnable version of this pattern.

## Out-of-band changes show up as a plan diff, not a silent drop

`anyscale_scheduler_config` writes the whole document on every apply - there is no per-section
update. Because of that, anything set outside Terraform (console, API, another tool) shows up in
the next plan as a removal, for any of the four sections, not just `scheduling_rules`: read the
plan before applying whenever the console or API might also be writing this config.
