package acctest

// The Anyscale API has no delete operation for scheduler configs, so
// Destroy must be state-only - it removes the resource from Terraform state
// but never calls the API. The write-call count proves that: an errant call
// here would look identical to a "successful" destroy from Terraform's own
// diagnostics, since the resource is state-only either way - only counting
// real requests against the mock catches it.

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccSchedulerConfigResource_DestroyIsStateOnly_MockServer(t *testing.T) {
	server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig: schedulerReadImportTestConfig,
	})
	defer server.Close()
	config := schedulerReadImportHCL(server.URL)

	var writesAfterCreate int
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
	if writesAfterCreate != 1 {
		t.Fatalf("sanity check failed: expected exactly 1 write call from Create's own apply, got %d", writesAfterCreate)
	}
}
