package acctest

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// Criteria 6 and 7: create sets every declared section and version, and an
// identical re-apply of that same config plans empty.
//
// Empty-plan is asserted explicitly via plancheck.ExpectEmptyPlan() rather
// than trusting step success alone: on an append-only API with no content
// dedupe, a provider bug that re-sends the config on every plan looks
// identical to success right up until the version counter climbs on every
// apply of unchanged config.
func TestAccSchedulerConfigResourceLifecycleCreateAndNoOpReplan(t *testing.T) {
	readConfig := `{"resource_flavors":[{"name":"cpu-standard","selector":[{"key":"node.kubernetes.io/instance-type","operator":"in","values":["m5.xlarge"]}]}],"resource_queues":[{"name":"default","cohort_name":"prod","preemption":{"reclaim_within_cohort":"lower_priority","borrow_within_cohort":"never","within_resource_queue":"never"},"resource_groups":[{"covered_resources":["cpu","memory_gb"],"flavors":[{"name":"cpu-standard","resources":[{"name":"cpu","nominal_quota":16,"lending_limit":4,"borrowing_limit":8}]}]}]}],"scheduling_rules":[{"resource_queue":"default","selector":[{"key":"team","operator":"in","values":["ml-platform"]}],"priority_policy":{"default":100,"min":0,"max":200,"on_violation":"force_update"}}],"recycle_policy":{"rotation_interval":"24h","max_workloads":50,"max_idle_duration":"10m"}}`

	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{ReadConfig: readConfig})
	defer server.Close()

	const config = `
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    {
      name = "cpu-standard"
      selector = [
        {
          key      = "node.kubernetes.io/instance-type"
          operator = "in"
          values   = ["m5.xlarge"]
        },
      ]
    },
  ]

  resource_queues = [
    {
      name        = "default"
      cohort_name = "prod"
      preemption = {
        reclaim_within_cohort = "lower_priority"
        borrow_within_cohort  = "never"
        within_resource_queue = "never"
      }
      resource_groups = [
        {
          covered_resources = ["cpu", "memory_gb"]
          flavors = [
            {
              name = "cpu-standard"
              resources = [
                {
                  name            = "cpu"
                  nominal_quota   = 16
                  lending_limit   = 4
                  borrowing_limit = 8
                },
              ]
            },
          ]
        },
      ]
    },
  ]

  scheduling_rules = [
    {
      resource_queue = "default"
      selector = [
        {
          key      = "team"
          operator = "in"
          values   = ["ml-platform"]
        },
      ]
      priority_policy = {
        default      = 100
        min          = 0
        max          = 200
        on_violation = "force_update"
      }
    },
  ]

  recycle_policy = {
    rotation_interval = "24h"
    max_workloads     = 50
    max_idle_duration = "10m"
  }
}
`

	resourceName := "anyscale_scheduler_config.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Criterion 6: every declared field is readable back, and
				// version is set by the first apply.
				Config: testAccProviderBlock(server.URL) + config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "version", "1"),
					resource.TestCheckResourceAttrSet(resourceName, "created_at"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.#", "1"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.name", "cpu-standard"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.selector.0.key", "node.kubernetes.io/instance-type"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.selector.0.operator", "in"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.selector.0.values.0", "m5.xlarge"),
					resource.TestCheckResourceAttr(resourceName, "resource_queues.#", "1"),
					resource.TestCheckResourceAttr(resourceName, "resource_queues.0.name", "default"),
					resource.TestCheckResourceAttr(resourceName, "resource_queues.0.cohort_name", "prod"),
					resource.TestCheckResourceAttr(resourceName, "resource_queues.0.preemption.reclaim_within_cohort", "lower_priority"),
					resource.TestCheckResourceAttr(resourceName, "resource_queues.0.resource_groups.0.covered_resources.#", "2"),
					resource.TestCheckResourceAttr(resourceName, "resource_queues.0.resource_groups.0.flavors.0.resources.0.nominal_quota", "16"),
					resource.TestCheckResourceAttr(resourceName, "scheduling_rules.#", "1"),
					resource.TestCheckResourceAttr(resourceName, "scheduling_rules.0.resource_queue", "default"),
					resource.TestCheckResourceAttr(resourceName, "scheduling_rules.0.priority_policy.on_violation", "force_update"),
					resource.TestCheckResourceAttr(resourceName, "recycle_policy.rotation_interval", "24h"),
					resource.TestCheckResourceAttr(resourceName, "recycle_policy.max_workloads", "50"),
				),
			},
			{
				// Criterion 7: replanning identical config against the state
				// the first apply produced must be a true no-op.
				Config: testAccProviderBlock(server.URL) + config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// Criterion 8: changing one field updates in place - no replacement - and
// mints a new version. resource_flavors carries no RequiresReplace plan
// modifier anywhere in the schema (the whole document is one authoritative,
// in-place-writable API object), so an update here must never destroy first.
//
// The mock is deliberately left in auto-derive mode (no ReadConfig override)
// so GET reflects whatever was actually last posted, the way a real backend
// would. An earlier version of this test pre-set the second version's
// read-back via a PreConfig hook before step 2's plan was computed - that
// mutation lands before Terraform's own pre-plan refresh, so refresh already
// matched the new config and the step spuriously planned no-op instead of
// update. Reproduced by actually running the test, not assumed from reading
// the code.
func TestAccSchedulerConfigResourceLifecycleUpdateInPlace(t *testing.T) {
	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{})
	defer server.Close()

	const configV1 = `
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    { name = "cpu-standard" },
  ]
}
`
	const configV2 = `
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    { name = "cpu-large" },
  ]
}
`

	resourceName := "anyscale_scheduler_config.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + configV1,
				Check:  resource.TestCheckResourceAttr(resourceName, "version", "1"),
			},
			{
				Config: testAccProviderBlock(server.URL) + configV2,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.name", "cpu-large"),
					resource.TestCheckResourceAttr(resourceName, "version", "2"),
				),
			},
		},
	})
}

// Criterion 18: a section omitted entirely by the API - not sent as an empty
// list or empty object, genuinely absent from the JSON - must read back as
// Terraform null, not as an empty collection.
//
// This is the plan/apply half of the twin null/omit constraint forge's unit
// tests exercise at the Go level only (expand omits a null section on write,
// flatten appends into nil slices on read). What a Go-level unit test cannot
// see is Core rejecting or rewriting an inconsistent shape at plan time -
// this resource.Test is the only thing that can.
//
// The mock document below deliberately omits resource_queues,
// scheduling_rules, and recycle_policy - it does not send them as `[]`/`{}`.
// A fixture that "fills them in" empty would let a broken flatten (nil slice
// vs. explicit empty list) pass silently, which is exactly the failure shape
// that shipped the mount_targets import bug this repo has seen before.
//
// This test covers only the flatten half of the twin null/omit contract -
// criterion 17's raw-body assertion is the only detector for the expand half,
// per the sharpened contract: a broken expand that emits `[]` instead of
// omitting the key can round-trip invisibly if the server collapses an empty
// array back to an absent key on its own.
//
// Mutation-proof (reverted, byte-clean): materializing
// model.ResourceQueues = []schedulerResourceQueueModel{} when the section is
// absent, instead of leaving it nil, fails this test - not with Core's
// "provider produced inconsistent result after apply" as first predicted, but
// with terraform-plugin-testing's own post-apply refresh-consistency check
// ("the refresh plan was not empty"), because resource_queues is Optional and
// not Computed so Core accepts the returned `[]` as the new value and only
// the following idempotency replan surfaces the drift against the
// null-implying config. Either way the mutation cannot pass silently.
func TestAccSchedulerConfigResourceLifecycleOmittedSectionsReadAsNull(t *testing.T) {
	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig: `{"resource_flavors":[{"name":"cpu-standard"}]}`,
	})
	defer server.Close()

	const config = `
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    { name = "cpu-standard" },
  ]
}
`

	resourceName := "anyscale_scheduler_config.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "resource_queues.#"),
					resource.TestCheckNoResourceAttr(resourceName, "scheduling_rules.#"),
					resource.TestCheckNoResourceAttr(resourceName, "recycle_policy.%"),
				),
			},
		},
	})
}
