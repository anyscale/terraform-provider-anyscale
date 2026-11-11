package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// In-place description updates, per the 2026-11-09 Gate 1 result: production
// serves PATCH /api/v2/projects/{id} with exactly {"description": <string>},
// so a description change must plan as Update - never a replace - issue
// exactly one PATCH, no POST, no DELETE, and a 405 (control planes predating
// the route) must fail with the "cannot be updated" diagnostic leaving state
// untouched, never fall back to replacement.
//
// These run against the mockProjectServer in
// resource_project_lifecycle_acc_test.go, extended with a PATCH handler that
// enforces the confirmed wire shape (extra key 422, missing 422, null 422
// string_type, unknown id 404).

func TestAccProjectResource_UpdateDescriptionPlansUpdate_MockServer(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	server, mock := newMockProjectServerWithCounts(t)
	const name = "project-desc-update-mock"

	step1Config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = %[1]q
  cloud_id    = "cld_mock"
  description = "first description"
}
`, name)

	step2Config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = %[1]q
  cloud_id    = "cld_mock"
  description = "second description"
}
`, name)

	var step1ID atomic.Value

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: step1Config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "description", "first description"),
					captureProjectID(&step1ID),
					func(s *terraform.State) error {
						posts, patches, deletes := mock.RequestCounts()
						if posts != 1 {
							return fmt.Errorf("step 1 should issue exactly one POST, got %d", posts)
						}
						if patches != 0 || deletes != 0 {
							return fmt.Errorf("step 1 should issue no PATCH (%d) and no DELETE (%d)", patches, deletes)
						}
						return nil
					},
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// The core contract: a description change plans an in-place
				// Update. Mutation-proofed against a build with
				// RequiresReplaceIfConfigured restored on the description
				// attribute: this plan check fails there (ResourceActionReplace
				// != ResourceActionUpdate), and the request-count assertions
				// below fail too (replacement issues POST + DELETE, no PATCH).
				Config: step2Config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_project.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "description", "second description"),
					captureProjectIDMatches(&step1ID, "the project must not have been replaced: the id changed"),
					func(s *terraform.State) error {
						posts, patches, deletes := mock.RequestCounts()
						if patches != 1 {
							return fmt.Errorf("step 2 should issue exactly one PATCH total, got %d", patches)
						}
						if posts != 1 {
							return fmt.Errorf("step 2 must issue no POST (a replacement would), got %d total", posts)
						}
						if deletes != 0 {
							return fmt.Errorf("step 2 must issue no DELETE (a replacement would), got %d", deletes)
						}
						return nil
					},
				),
			},
		},
	})
}

// Omitting the description after it was set must be a no-op: no PATCH, no
// replace, and the stored value stays in state. Core plans an omitted
// Optional+Computed attribute from prior state; this test pins that
// (mutation check below). If the remote value ever drifted out of band,
// refresh would surface it as an ordinary in-place update - covered by the
// explicit-change tests above, not here.
func TestAccProjectResource_OmitDescriptionKeepsValue_MockServer(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	server, mock := newMockProjectServerWithCounts(t)
	const name = "project-desc-omit-mock"

	step1Config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = %[1]q
  cloud_id    = "cld_mock"
  description = "first description"
}
`, name)

	var step1ID atomic.Value

	step2Config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_project" "test" {
  name      = %[1]q
  cloud_id  = "cld_mock"
}
`, name)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: step1Config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "description", "first description"),
					captureProjectID(&step1ID),
				),
			},
			{
				// Mutation-proofed against a build with the description
				// attribute's Computed:true removed: the omitted value
				// plans as null against state "first description", the
				// plan becomes a non-empty update, and the PreApply
				// ExpectEmptyPlan below fails there. Dropping
				// UseStateForUnknown does not: Core already plans the
				// omitted value from state, so the modifier never runs
				// in this step.
				Config: step2Config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "description", "first description"),
					captureProjectIDMatches(&step1ID, "the project must not have been replaced: the id changed"),
					func(s *terraform.State) error {
						posts, patches, deletes := mock.RequestCounts()
						if patches != 0 {
							return fmt.Errorf("step 2 must issue no PATCH (an omitted description must not be sent), got %d", patches)
						}
						if posts != 1 {
							return fmt.Errorf("step 2 must issue no further POST, got %d total", posts)
						}
						if deletes != 0 {
							return fmt.Errorf("step 2 must issue no DELETE, got %d", deletes)
						}
						return nil
					},
				),
			},
		},
	})
}

// new405OnPatchServer delegates everything to a real mockProjectServer
// except PATCH, which answers 405 - the deployment whose behavior this
// exercises predates the PATCH route.
func new405OnPatchServer(t *testing.T) (*httptest.Server, *mockProjectServer) {
	t.Helper()
	_, mock := newMockProjectServerWithCounts(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v2/projects/") {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"detail":"Method Not Allowed"}`))
			return
		}
		mock.handle(w, r)
	}))
	t.Cleanup(server.Close)
	return server, mock
}

func TestAccProjectResource_UpdateDescription405ErrorsWithoutReplacing_MockServer(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	server, _ := new405OnPatchServer(t)
	const name = "project-desc-405-mock"

	step1Config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = %[1]q
  cloud_id    = "cld_mock"
  description = "kept description"
}
`, name)

	step2Config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = %[1]q
  cloud_id    = "cld_mock"
  description = "attempted description"
}
`, name)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: step1Config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "description", "kept description"),
				),
			},
			{
				// The 405 diagnostic - contract wording, matched at a length
				// Terraform's rendered output will not wrap mid-phrase - with
				// the failed apply persisting nothing, so no replacement is
				// possible.
				Config:      step2Config,
				ExpectError: regexp.MustCompile(`Project Description Cannot Be Updated`),
			},
			{
				// State unchanged: reverting the config to step 1's shape
				// must plan no change at all - the failed step never
				// persisted the new description, and the project cannot
				// have been replaced (Step 2's apply never completed).
				Config: step1Config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_project.test", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "description", "kept description"),
				),
			},
		},
	})
}

func captureProjectID(dest *atomic.Value) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources["anyscale_project.test"]
		if !ok {
			return fmt.Errorf("anyscale_project.test not found in state")
		}
		dest.Store(rs.Primary.ID)
		return nil
	}
}

func captureProjectIDMatches(dest *atomic.Value, message string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		previous, ok := dest.Load().(string)
		if !ok {
			return fmt.Errorf("no prior id captured to compare with")
		}
		rs, ok := s.RootModule().Resources["anyscale_project.test"]
		if !ok {
			return fmt.Errorf("anyscale_project.test not found in state")
		}
		if rs.Primary.ID != previous {
			return fmt.Errorf("%s: %q != %q", message, rs.Primary.ID, previous)
		}
		return nil
	}
}
