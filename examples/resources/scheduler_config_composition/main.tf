# Composing anyscale_scheduler_config across files instead of one large embedded
# block. See flavors.tf, queues.tf, and rules.tf for the pattern; the
# accompanying guide (Scheduler Configuration Guide) explains why each section
# is built the way it is.
#
# This is still one resource, one plan, one state address - splitting the
# authoring across files does not give any section its own lifecycle, and it
# does not change the one-document-per-apply behavior described on the
# resource page.

terraform {
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }
  }
}

resource "anyscale_scheduler_config" "this" {
  resource_flavors = local.flavors
  resource_queues  = values(local.queues)
  scheduling_rules = local.rules
}
