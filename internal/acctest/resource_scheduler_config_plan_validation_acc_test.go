package acctest

// Plan-time validation of the scheduler config document: enum values,
// declaration order, and declared-but-empty sections.
//
// All three checks here concern what happens at PLAN, not at apply. That
// distinction is the whole point - a config the schema can reject must never
// reach the API - so every test that claims it asserts it, either with a
// PlanOnly step (which fails the test if the error only arrives during apply)
// or with an explicit plan-action check.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// A value outside an enumerated attribute's allowed set fails at plan, with
// the schema's own OneOf diagnostic, and never reaches apply.
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

// A section declared as an empty list fails at plan, and the diagnostic says
// to omit the section rather than merely stating a size constraint.
//
// Why the empty form is rejected at all, and why no wire-level assertion could
// test it, is on nonEmptyListValidator's doc comment; it is not restated here.
//
// Test-specific: the regexp requires the "Omit the ... entirely" sentence
// rather than asserting a size constraint. The stock
// listvalidator.SizeAtLeast(1) message ("list must contain at least 1
// elements") satisfies a size assertion but not this one, and swapping to it is
// exactly the mutation this test defends against - the guidance is the behavior
// under test, not decoration.
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

// The cross-reference validate call actually runs on Create, not merely
// "would run if reached."
//
// ModifyPlan's precondition for calling /config/validate has to gate on
// something, because values that resolve during apply cannot be serialized
// into the request yet. The bug this test targets is which raw value that
// gate reads. This resource has three top-level Computed attributes -
// version, created_at, creator_id - with no UseStateForUnknown, so on Create
// the plan's raw value is never fully known: there is no prior state to
// carry them forward from, so they are Unknown in the plan by construction.
// A gate keyed on the PLAN's fully-known-ness is therefore false on every
// single Create, unconditionally, and the validate call it guards never
// fires - not "fires except in edge cases," never. A gate keyed on the
// CONFIG's fully-known-ness does not have this problem: config carries no
// Computed values at all, so it is fully known on an ordinary Create and the
// call proceeds.
//
// This is an assert-absence claim ("validate ran"), so it needs a positive
// control on the same path or a vacuous pass is indistinguishable from a
// real one - the same reasoning as the empty-plan checks above. The control
// here is the mock's validate endpoint answering 400, the status this
// provider treats as "the server evaluated the document and rejected it"
// (schedulerServerEvaluatedDocument). A build where the gate never fires
// cannot surface that rejection: nothing ever POSTs to /validate, so the
// 400 is never seen, and Create proceeds to write the (mock-)invalid
// document and succeeds. A build where the gate fires calls validate,
// receives the 400, and fails the plan before any write happens.
//
// Mutation-proof: reverting ModifyPlan's gate and expand calls from
// req.Config back to req.Plan (the shape this resource shipped with) turns
// this test red - Create succeeds and the write-count assertion fails,
// because the plan's Computed unknowns skip the call regardless of what the
// mock's validate endpoint would have said.
func TestAccSchedulerConfigResourceCrossReferenceValidationRunsOnCreate(t *testing.T) {
	const rejectDetail = "Scheduling rule #1 references unknown resource queue 'ghost-queue'."

	server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ValidateStatus: 400,
		ValidateBody:   fmt.Sprintf(`{"error":{"detail":%q}}`, rejectDetail),
	})
	defer server.Close()

	const config = `
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    { name = "cpu-standard" },
  ]
  scheduling_rules = [
    { resource_queue = "ghost-queue" },
  ]
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:   testAccProviderBlock(server.URL) + config,
				PlanOnly: true,
				// The CLI's own diagnostic renderer word-wraps long lines, so a
				// literal match on the server's detail sentence would break on
				// whichever space happens to fall at the wrap column. Every
				// run-of-spaces in the expected text becomes \s+ instead of a
				// literal match, tolerating the wrap without weakening the
				// assertion to "some error happened."
				ExpectError: regexp.MustCompile(`(?s)Invalid Anyscale Scheduler Configuration.*` +
					strings.Join(strings.Fields(regexp.QuoteMeta(rejectDetail)), `\s+`)),
			},
		},
	})

	// The write-count assertion is what makes this a claim about PLAN, not
	// about the resource eventually failing somewhere. A build that skips
	// the plan-time call but still fails Create some other way could pass
	// the ExpectError above; it cannot pass this.
	if writes := mock.WriteCount(); writes != 0 {
		t.Fatalf("expected the cross-reference rejection to be caught at plan with zero API writes, got %d write(s)", writes)
	}
}

// The declared order of list sections round-trips exactly, and a read-back
// whose order differs from the config produces a non-empty plan.
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
				// assertion that carries the claim - "non-empty" alone would
				// also be satisfied by a spurious replace.
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
