package acctest

// Read must remove a genuinely-gone config from state, and ImportState must
// adopt an existing config by organization ID, rejecting a mismatched ID, a
// missing config, and a capability-gated organization with clear diagnostics
// rather than an empty phantom resource. ImportState has no fail-open
// carve-out - unlike Read's 403 handling, an import that cannot see the
// config must hard-fail.

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const schedulerReadImportTestConfig = `{"resource_flavors":[{"name":"default"}]}`

func schedulerReadImportHCL(serverURL string) string {
	return testAccProviderBlock(serverURL) + `
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    { name = "default" },
  ]
}
`
}

// TestAccSchedulerConfigResource_ReadRemovesOnGenuine404_MockServer: once the
// organization's config genuinely disappears (mock GET returns 404, matching
// the real "no active scheduler config" body), the next refresh must remove
// the resource from state rather than report it healthy - the next plan then
// shows a create, not a no-op.
func TestAccSchedulerConfigResource_ReadRemovesOnGenuine404_MockServer(t *testing.T) {
	server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig: schedulerReadImportTestConfig,
	})
	defer server.Close()
	config := schedulerReadImportHCL(server.URL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				// PlanOnly: applying this step would recreate the config,
				// which a real backend would then serve on GET - the mock's
				// override is sticky by design (see f3NotFoundMockServer's
				// nextGetStatus) and does not model that self-healing, so a
				// full apply here would fail for a reason unrelated to what
				// this test checks. Read removing the resource is fully
				// observable from the plan alone.
				PreConfig: func() {
					mock.SetGetResponse(http.StatusNotFound, `{"error":{"detail":"No active scheduler config found."}}`)
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccSchedulerConfigResource_ImportAdoptsActiveConfig_MockServer: the
// happy path - importing by the token's own organization ID adopts the
// already-active config, recovering its document and computed metadata with
// no preceding local Create.
func TestAccSchedulerConfigResource_ImportAdoptsActiveConfig_MockServer(t *testing.T) {
	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig:     schedulerReadImportTestConfig,
		InitialVersion: 7,
	})
	defer server.Close()
	config := schedulerReadImportHCL(server.URL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_scheduler_config.test",
				ImportState:        true,
				ImportStateId:      "org_mocktestorganizationid00",
				ImportStatePersist: true,
				Config:             config,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						t.Fatalf("expected 1 imported state, got %d", len(states))
					}
					attrs := states[0].Attributes
					if got := attrs["version"]; got != "7" {
						t.Errorf("version = %q, want %q", got, "7")
					}
					if got := attrs["resource_flavors.0.name"]; got != "default" {
						t.Errorf("resource_flavors.0.name = %q, want %q", got, "default")
					}
					if got := attrs["creator_id"]; got != "usr_mock" {
						t.Errorf("creator_id = %q, want %q", got, "usr_mock")
					}
					return nil
				},
			},
			{
				// Self-heal bar: the same config plans clean against what
				// import recovered.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// TestAccSchedulerConfigResource_ImportOrgIDMismatch_MockServer: the mismatch
// case - an import ID that is not the token's own organization must fail
// clearly rather than silently importing the wrong org's config.
func TestAccSchedulerConfigResource_ImportOrgIDMismatch_MockServer(t *testing.T) {
	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig:     schedulerReadImportTestConfig,
		InitialVersion: 1,
	})
	defer server.Close()
	config := schedulerReadImportHCL(server.URL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:  "anyscale_scheduler_config.test",
				ImportState:   true,
				ImportStateId: "org_someotherorganizationid",
				Config:        config,
				ExpectError:   regexp.MustCompile(`Organization ID Mismatch`),
			},
		},
	})
}

// TestAccSchedulerConfigResource_ImportNoActiveConfig_MockServer: the
// not-found case - importing an organization with no active scheduler config
// (mock never seeded a version) must fail clearly, not import an empty
// phantom resource.
func TestAccSchedulerConfigResource_ImportNoActiveConfig_MockServer(t *testing.T) {
	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{})
	defer server.Close()
	config := schedulerReadImportHCL(server.URL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:  "anyscale_scheduler_config.test",
				ImportState:   true,
				ImportStateId: "org_mocktestorganizationid00",
				Config:        config,
				ExpectError:   regexp.MustCompile(`No Active Anyscale Scheduler Config to Import`),
			},
		},
	})
}

// TestAccSchedulerConfigResource_ImportSchedulerNotEnabled_MockServer: the
// capability-gate case - ImportState has no fail-open carve-out, so a 403
// admission-gate response must hard-fail the import, unlike Read's
// warn-and-retain behavior for the same condition.
func TestAccSchedulerConfigResource_ImportSchedulerNotEnabled_MockServer(t *testing.T) {
	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		GetStatus: http.StatusForbidden,
		GetBody:   `{"error":{"detail":"Anyscale Scheduler is not enabled for this organization."}}`,
	})
	defer server.Close()
	config := schedulerReadImportHCL(server.URL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:  "anyscale_scheduler_config.test",
				ImportState:   true,
				ImportStateId: "org_mocktestorganizationid00",
				Config:        config,
				ExpectError:   regexp.MustCompile(`Unable to Read Anyscale Scheduler Config`),
			},
		},
	})
}
