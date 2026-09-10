package acctest

// Plan-time validation of the scheduler config document: enum values,
// declaration order, and declared-but-empty sections.
//
// All three criteria here concern what happens at PLAN, not at apply. That
// distinction is the whole point - a config the schema can reject must never
// reach the API - so every test that claims it asserts it, either with a
// PlanOnly step (which fails the test if the error only arrives during apply)
// or with an explicit plan-action check.

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// Criterion 13: a value outside an enumerated attribute's allowed set fails at
// plan, with the schema's own OneOf diagnostic, and never reaches apply.
//
// PlanOnly is what makes this a claim about plan rather than about the config
// being rejected somewhere. Without it a step that errored only during apply
// would look identical from the outside. The zero-write assertion is the
// second half: the mock counts every POST, so a build that let the value
// through to the API would be caught even if some later diagnostic still
// failed the step.
func TestAccSchedulerConfigResourceRejectsInvalidEnumAtPlan(t *testing.T) {
	cases := []struct {
		name       string
		config     string
		expectErrs *regexp.Regexp
	}{
		{
			name: "match expression operator",
			config: `
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    {
      name = "cpu-standard"
      selector = [
        { key = "node.kubernetes.io/instance-type", operator = "matches", values = ["m5.large"] },
      ]
    },
  ]
}
`,
			expectErrs: regexp.MustCompile(`(?s)Invalid Attribute Value Match.*operator`),
		},
		{
			name: "priority policy on_violation",
			config: `
resource "anyscale_scheduler_config" "test" {
  scheduling_rules = [
    {
      resource_queue  = "default"
      priority_policy = { on_violation = "explode" }
    },
  ]
}
`,
			expectErrs: regexp.MustCompile(`(?s)Invalid Attribute Value Match.*on_violation`),
		},
		{
			name: "queue preemption reclaim_within_cohort",
			config: `
resource "anyscale_scheduler_config" "test" {
  resource_queues = [
    {
      name       = "default"
      preemption = { reclaim_within_cohort = "sometimes" }
    },
  ]
}
`,
			expectErrs: regexp.MustCompile(`(?s)Invalid Attribute Value Match.*reclaim_within_cohort`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{})
			defer server.Close()

			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:      testAccProviderBlock(server.URL) + tc.config,
						PlanOnly:    true,
						ExpectError: tc.expectErrs,
					},
				},
			})

			if writes := mock.WriteCount(); writes != 0 {
				t.Fatalf("expected an invalid enum value to be rejected at plan with zero API writes, got %d write(s)", writes)
			}
		})
	}
}

// Criterion 16: a section declared as an empty list fails at plan, and the
// diagnostic says to omit the section rather than merely stating a size
// constraint.
//
// The guidance is part of the behavior under test, not decoration, and the
// reason is the inverse of the obvious one: the section fields are Go slices
// tagged omitempty, so an empty list and an absent one serialize identically.
// Accepting `[]` would silently mean "unset". That also rules out testing this
// at the wire level - the two documents are byte-identical, so a body
// assertion would pass against any build. Plan-time diagnostic is the only
// place the behavior exists. The regexp deliberately requires the
// "Omit the ... entirely" sentence: the stock
// listvalidator.SizeAtLeast(1) message ("list must contain at least 1
// elements") passes a size assertion but not this one, which is exactly the
// mutation this test defends against.
func TestAccSchedulerConfigResourceRejectsEmptySectionAtPlan(t *testing.T) {
	for _, attr := range []string{"resource_flavors", "resource_queues", "scheduling_rules"} {
		t.Run(attr, func(t *testing.T) {
			server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{})
			defer server.Close()

			config := `
resource "anyscale_scheduler_config" "test" {
  ` + attr + ` = []
}
`

			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:      testAccProviderBlock(server.URL) + config,
						PlanOnly:    true,
						ExpectError: regexp.MustCompile(`(?s)Empty ` + attr + ` Section.*Omit the.*entirely`),
					},
				},
			})

			if writes := mock.WriteCount(); writes != 0 {
				t.Fatalf("expected an empty %s to be rejected at plan with zero API writes, got %d write(s)", attr, writes)
			}
		})
	}
}

// Criterion 14: the declared order of list sections round-trips exactly, and a
// read-back whose order differs from the config produces a non-empty plan.
//
// Order is significant to the scheduler (flavors are tried in the order
// written), so a build that reordered on the way out or normalized on the way
// back would be a real defect - and an invisible one, since every element
// would still be present.
//
// The read-back documents below are hand-written JSON literals, and the mock
// is locked to them via ReadConfig/SetReadConfig. They are NOT derived from
// what was applied: a helper that rebuilds the document by marshaling Go maps
// cannot represent key or element ordering at all, so a reorder test built on
// one would pass against a build that reorders wrongly. The placebo is
// specific and easy to reintroduce - the auto-derivation path is the obvious
// tool for the job here - which is why the literals are spelled out.
func TestAccSchedulerConfigResourcePreservesDeclaredOrder(t *testing.T) {
	const orderedReadConfig = `{"resource_flavors":[{"name":"cpu-standard"},{"name":"gpu-a10"},{"name":"gpu-h100"}]}`
	const reorderedReadConfig = `{"resource_flavors":[{"name":"gpu-h100"},{"name":"cpu-standard"},{"name":"gpu-a10"}]}`

	server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig:        orderedReadConfig,
		EchoAppliedConfig: true,
	})
	defer server.Close()

	const config = `
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    { name = "cpu-standard" },
    { name = "gpu-a10" },
    { name = "gpu-h100" },
  ]
}
`

	const resourceName = "anyscale_scheduler_config.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Create. State must carry the config's own order, index for
				// index - not merely the same set of names.
				Config: testAccProviderBlock(server.URL) + config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.#", "3"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.name", "cpu-standard"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.1.name", "gpu-a10"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.2.name", "gpu-h100"),
				),
			},
			{
				// Re-plan against a read-back in the declared order: exact
				// round-trip, so no diff.
				Config: testAccProviderBlock(server.URL) + config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Same config, but the server now reports the same three
				// flavors in a different order. Terraform lists are
				// order-sensitive, so this must plan an update - a build that
				// treated the two orders as equal would silently accept a
				// document the practitioner did not write.
				//
				// This step applies rather than planning only: PlanOnly and
				// ConfigPlanChecks.PreApply are mutually exclusive in
				// terraform-plugin-testing, and the plan-action check is the
				// assertion that carries the criterion - "non-empty" alone
				// would also be satisfied by a spurious replace.
				//
				// Nothing restores the ordered read-back by hand: the mock
				// echoes the posted bytes, so the corrective apply settles on
				// its own. An earlier version of this test restored it in
				// Check, which passes only because the harness happens to run
				// Check before its own post-apply refresh plan - undocumented
				// internal ordering, and load-bearing enough that removing the
				// restore without the echo turns the step red.
				PreConfig: func() { mock.SetReadConfig(reorderedReadConfig) },
				Config:    testAccProviderBlock(server.URL) + config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				// The update must have converged: same config, and the
				// server - now returning what the previous apply posted -
				// produces no diff. This is the half a reorder test usually
				// omits; detecting the drift proves nothing if the correction
				// does not settle.
				Config: testAccProviderBlock(server.URL) + config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.name", "cpu-standard"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.1.name", "gpu-a10"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.2.name", "gpu-h100"),
				),
			},
		},
	})
}
