package acctest

// Fail-open behavior: a scheduler call that could not be performed must not
// block `terraform plan`.
//
// The distinction these tests protect is "the server evaluated your document
// and rejected it" versus "nothing ever read your document." Only the first is
// a statement about the config. Hard-failing on the second makes plan
// impossible for reasons unrelated to what the practitioner wrote - an expired
// token, a 5xx, or an organization that simply does not have the scheduler
// admission flag turned on.
//
// Each test asserts that the step COMPLETES. That is the load-bearing
// assertion, and it is deliberately not "a warning was emitted": a build that
// hard-errors emits diagnostics too, and a warning-presence check alone would
// pass one.

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// schedulerNotEnabledBody is the shape of the organization admission gate's
// 403. The provider keys on the detail sentence, not on any product
// abbreviation, so this body is written the way the backend sends it.
const schedulerNotEnabledBody = `{"error":{"detail":"GRS is not enabled for this organization."}}`

const schedulerFailOpenConfig = `
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    { name = "cpu-standard" },
  ]
}
`

const schedulerFailOpenReadConfig = `{"resource_flavors":[{"name":"cpu-standard"}]}`

// TestAccSchedulerConfigResourceValidationUnreachableDoesNotBlockPlan: an
// unreachable validation endpoint does not block plan.
//
// The mock answers 503 on /config/validate - the server never got as far as
// reading the document - while the apply path itself stays healthy. A build
// that treated "could not validate" as "invalid" would fail this step outright.
func TestAccSchedulerConfigResourceValidationUnreachableDoesNotBlockPlan(t *testing.T) {
	server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig:     schedulerFailOpenReadConfig,
		ValidateStatus: 503,
		ValidateBody:   `{"error":{"detail":"upstream unavailable"}}`,
	})
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + schedulerFailOpenConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_scheduler_config.test", "version", "1"),
				),
			},
		},
	})

	// The plan proceeding is not the same as the validate call being skipped:
	// it was attempted, it failed, and the provider carried on.
	if mock.WriteCount() != 1 {
		t.Fatalf("expected the apply to proceed to exactly 1 write despite an unreachable validate endpoint, got %d", mock.WriteCount())
	}
}

// TestAccSchedulerConfigResourceAdmissionFlag403DoesNotBlockPlan: the
// organization-level admission-flag 403 does not block plan either.
//
// This is the same fail-open path as the unreachable-validation case above,
// but the one a practitioner is far more likely to hit: an org without the
// scheduler enabled. Blocking plan here would make the whole workspace
// unplannable, not just this resource.
func TestAccSchedulerConfigResourceAdmissionFlag403DoesNotBlockPlan(t *testing.T) {
	server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig:     schedulerFailOpenReadConfig,
		ValidateStatus: 403,
		ValidateBody:   schedulerNotEnabledBody,
	})
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + schedulerFailOpenConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_scheduler_config.test", "version", "1"),
				),
			},
		},
	})

	if mock.WriteCount() != 1 {
		t.Fatalf("expected the apply to proceed to exactly 1 write despite an admission-flag 403 on validate, got %d", mock.WriteCount())
	}
}

// TestAccSchedulerConfigResourceRefreshForbiddenRetainsState: a 403 on
// refresh, with the resource already in state, completes the plan AND
// retains the state.
//
// Two independent things can go wrong here and one assertion cannot catch
// both:
//
//   - The refresh could hard-error, failing the plan. Caught by the step
//     completing at all.
//   - The refresh could fail open on the diagnostic but still drop the
//     resource from state. That build "completes" too - and then silently
//     plans a create for a config that already exists, which on this resource
//     mints another permanent config version. Caught only by the plan-action
//     check: a dropped resource plans a Create, not a no-op.
//
// So the empty-plan check is not a stylistic tightening of the completion
// assertion. It is the second half of what this test proves.
func TestAccSchedulerConfigResourceRefreshForbiddenRetainsState(t *testing.T) {
	server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig: schedulerFailOpenReadConfig,
	})
	defer server.Close()

	const resourceName = "anyscale_scheduler_config.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Create against a healthy server, so there is real state for
				// the next step's refresh to fail against.
				Config: testAccProviderBlock(server.URL) + schedulerFailOpenConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "version", "1"),
				),
			},
			{
				// GET now answers with the admission-gate 403. Refresh cannot
				// read the live document, so the provider keeps what it has
				// and says so - it must not conclude the config is gone.
				PreConfig: func() { mock.SetGetResponse(403, schedulerNotEnabledBody) },
				Config:    testAccProviderBlock(server.URL) + schedulerFailOpenConfig,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "version", "1"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.name", "cpu-standard"),
				),
			},
		},
	})

	// A retained-state refresh must not have written anything to recover: the
	// create is the only legitimate write in this test.
	if mock.WriteCount() != 1 {
		t.Fatalf("expected exactly 1 write (the create), got %d - a refresh that fails open must not re-apply", mock.WriteCount())
	}
}
