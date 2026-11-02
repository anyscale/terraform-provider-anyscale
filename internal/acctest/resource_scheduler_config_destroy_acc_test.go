package acctest

// The Anyscale API has no delete operation for scheduler configs, so
// Destroy must be state-only - it removes the resource from Terraform state
// but never calls the API. The write-call count proves that: an errant call
// here would look identical to a "successful" destroy from Terraform's own
// diagnostics, since the resource is state-only either way - only counting
// real requests against the mock catches it.

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccSchedulerConfigResource_DestroyIsStateOnly_MockServer(t *testing.T) {
	server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig: schedulerReadImportTestConfig,
	})
	defer server.Close()
	config := schedulerReadImportHCL(server.URL)

	var writesAfterCreate, validatesAfterCreate int
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_scheduler_config.test", "version", "1"),
					func(_ *terraform.State) error {
						writesAfterCreate = mock.WriteCount()
						return nil
					},
				),
				// The step plans twice more after Check (the post-apply
				// empty-plan checks), and each of those plans validates. A
				// PostApplyPostRefresh plan check runs after the step's last
				// plan, so the count captured here is the baseline the
				// implicit destroy starts from.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						captureValidateHits{mock: mock, into: &validatesAfterCreate},
					},
				},
			},
		},
	})

	// resource.Test always runs an implicit destroy after the last step, and
	// that destroy has completed by the time resource.Test returns. Terraform
	// still issues a legitimate Read (GET) refresh before planning the
	// destroy, so RequestCount alone would not isolate a real regression -
	// but the only mutating call the API exposes is POST, and Delete must
	// never make one.
	writesAfterDestroy := mock.WriteCount()
	if writesAfterDestroy != writesAfterCreate {
		t.Fatalf("expected Destroy to make zero write (POST) calls, but write count went from %d (after create) to %d (after implicit destroy)", writesAfterCreate, writesAfterDestroy)
	}
	// ModifyPlan must also short-circuit on a destroy plan: a destroy's null
	// config is fully known, so the IsFullyKnown gate does not stop it.
	// Today, removing the destroy guard alone is caught across the suite,
	// because Config.Get into the model fails on a null object. That
	// coincidence goes away if the config is ever read null-tolerantly: the
	// destroy plan then POSTs an empty document to /config/validate with no
	// error at all. This assertion is the one that catches that case.
	// WriteCount cannot (it counts only POST /config).
	if validatesAfter := mock.ValidateHits(); validatesAfter != validatesAfterCreate {
		t.Fatalf("expected the destroy plan to make zero /config/validate calls, but validate count went from %d (after create) to %d (after implicit destroy)", validatesAfterCreate, validatesAfter)
	}
	if validatesAfterCreate == 0 {
		t.Fatalf("sanity check failed: expected Create's plan to call /config/validate at least once, got 0")
	}
	if writesAfterCreate != 1 {
		t.Fatalf("sanity check failed: expected exactly 1 write call from Create's own apply, got %d", writesAfterCreate)
	}
}

// captureValidateHits records the mock's /config/validate count at the moment
// a plan check runs. It never fails the plan.
type captureValidateHits struct {
	mock *schedulerConfigServer
	into *int
}

func (c captureValidateHits) CheckPlan(_ context.Context, _ plancheck.CheckPlanRequest, _ *plancheck.CheckPlanResponse) {
	*c.into = c.mock.ValidateHits()
}
