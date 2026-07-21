package acctest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccProjectResource_Basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	projectName := UniqueName(t, "project-basic")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccProjectResourceBasicConfig(cloudID, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_project.test", "id"),
					resource.TestCheckResourceAttr("anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("anyscale_project.test", "cloud_id", cloudID),
					resource.TestCheckResourceAttrSet("anyscale_project.test", "created_at"),
					resource.TestCheckResourceAttrSet("anyscale_project.test", "creator_id"),
					resource.TestCheckResourceAttrSet("anyscale_project.test", "directory_name"),
					resource.TestCheckResourceAttr("anyscale_project.test", "is_default", "false"),
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// ImportState testing
			{
				ResourceName:      "anyscale_project.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"cloud_name", // input-only alias for cloud_id; project API stores only parent_cloud_id
					// F13: import now recovers the real collaborator list,
					// which always includes the API's auto-added
					// creator-owner even though this config never declares a
					// collaborator block. That's correct, honest behavior
					// (see PROJECT-API-SYNC-DESIGN.md F13) -- this config
					// just isn't the place asserting collaborator semantics;
					// TestAccProjectResource_WriteCollaboratorSymmetry does
					// that with no ignore on this attribute.
					"collaborator",
				},
			},
		},
	})
}

func TestAccProjectResource_WithDescription(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	projectName := UniqueName(t, "project-desc")
	description := "Test project with description"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccProjectResourceWithDescriptionConfig(cloudID, projectName, description),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("anyscale_project.test", "description", description),
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// regression test for task 452e7154: the API has no endpoint to update a
			// project's description in place, so an explicit, intentional description
			// change plans a replace (not a perpetual diff, not a silent no-op).
			{
				Config: testAccProjectResourceWithDescriptionConfig(cloudID, projectName, description+" (changed)"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "description", description+" (changed)"),
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_project.test", plancheck.ResourceActionReplace),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// TestAccProjectResource_DescriptionOmittedSurvivesUpdate is a regression test for
// task 452e7154: description is Optional+Computed and, when omitted from config,
// the framework's default proposed value for it goes unknown on ANY update to the
// resource (not just changes to description itself). A plain RequiresReplace can't
// tell "still omitted" apart from "changed" in that situation, so it used to force a
// full project replace on e.g. a collaborator-only edit even though description was
// never touched. This asserts that case is now a plain in-place update.
func TestAccProjectResource_DescriptionOmittedSurvivesUpdate(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	testEmail1 := os.Getenv("ANYSCALE_TEST_USER_EMAIL_1")
	testEmail2 := os.Getenv("ANYSCALE_TEST_USER_EMAIL_2")
	if testEmail1 == "" || testEmail2 == "" {
		t.Skip("ANYSCALE_TEST_USER_EMAIL_1 and ANYSCALE_TEST_USER_EMAIL_2 not set, skipping collaborator test")
	}

	projectName := UniqueName(t, "project-desc-omit")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccProjectResourceDescriptionOmittedConfig(cloudID, projectName, testEmail1, testEmail2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "2"),
					resource.TestCheckResourceAttrSet("anyscale_project.test", "description"),
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Collaborator-only edit, description still omitted from config: must be
			// a plain update, never a replace, and must not perpetually diff.
			{
				Config: testAccProjectResourceDescriptionOmittedUpdatedConfig(cloudID, projectName, testEmail1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "1"),
					resource.TestCheckResourceAttrSet("anyscale_project.test", "description"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_project.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

func TestAccProjectResource_WithCloudName(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudName := GetTestCloudName(t)

	projectName := UniqueName(t, "project-cloudname")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccProjectResourceWithCloudNameConfig(cloudName, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("anyscale_project.test", "cloud_name", cloudName),
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

func TestAccProjectResource_WithCollaborators(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	// Note: This test requires valid test user emails
	// Skip if not provided
	testEmail1 := os.Getenv("ANYSCALE_TEST_USER_EMAIL_1")
	testEmail2 := os.Getenv("ANYSCALE_TEST_USER_EMAIL_2")
	if testEmail1 == "" || testEmail2 == "" {
		t.Skip("ANYSCALE_TEST_USER_EMAIL_1 and ANYSCALE_TEST_USER_EMAIL_2 not set, skipping collaborator test")
	}

	projectName := UniqueName(t, "project-collab")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			// Create with collaborators
			{
				Config: testAccProjectResourceWithCollaboratorsConfig(cloudID, projectName, testEmail1, testEmail2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "2"),
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Update collaborators (remove one, add different permission)
			{
				Config: testAccProjectResourceWithUpdatedCollaboratorsConfig(cloudID, projectName, testEmail1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "1"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// Helper functions

func testAccCheckProjectExistsInAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}

		projectID := rs.Primary.Attributes["id"]
		if projectID == "" {
			return fmt.Errorf("project ID not set")
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/projects/%s", projectID), nil)
		if err != nil {
			return fmt.Errorf("failed to get project: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != 200 {
			return fmt.Errorf("project not found in API: status %d", resp.StatusCode)
		}

		return nil
	}
}

// Configuration templates

func testAccProjectResourceBasicConfig(cloudID, projectName string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project created by acceptance tests"
}
`, projectName, cloudID)
}

func testAccProjectResourceWithDescriptionConfig(cloudID, projectName, description string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = "%s"
  cloud_id    = "%s"
  description = "%s"
}
`, projectName, cloudID, description)
}

func testAccProjectResourceWithCloudNameConfig(cloudName, projectName string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = "%s"
  cloud_name  = "%s"
  description = "Test project using cloud_name"
}
`, projectName, cloudName)
}

func testAccProjectResourceWithCollaboratorsConfig(cloudID, projectName, email1, email2 string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project with collaborators"

  collaborator {
    email            = "%s"
    permission_level = "owner"
  }

  collaborator {
    email            = "%s"
    permission_level = "write"
  }
}
`, projectName, cloudID, email1, email2)
}

func testAccProjectResourceWithUpdatedCollaboratorsConfig(cloudID, projectName, email1 string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project with collaborators"

  collaborator {
    email            = "%s"
    permission_level = "write"
  }
}
`, projectName, cloudID, email1)
}

// testAccProjectResourceDescriptionOmittedConfig deliberately has no `description`
// argument at all: it must be left to the API-generated default, not just set to "".
func testAccProjectResourceDescriptionOmittedConfig(cloudID, projectName, email1, email2 string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name     = "%s"
  cloud_id = "%s"

  collaborator {
    email            = "%s"
    permission_level = "owner"
  }

  collaborator {
    email            = "%s"
    permission_level = "write"
  }
}
`, projectName, cloudID, email1, email2)
}

func testAccProjectResourceDescriptionOmittedUpdatedConfig(cloudID, projectName, email1 string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name     = "%s"
  cloud_id = "%s"

  collaborator {
    email            = "%s"
    permission_level = "write"
  }
}
`, projectName, cloudID, email1)
}

func TestAccProjectResource_WithUserDataSource(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	projectName := UniqueName(t, "project-datasource")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			// Create project with current user as collaborator using data source
			{
				Config: testAccProjectResourceWithUserDataSourceConfig(cloudID, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "1"),
					resource.TestCheckResourceAttrPair(
						"anyscale_project.test", "collaborator.0.email",
						"data.anyscale_user.current", "email",
					),
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.0.permission_level", "owner"),
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

func testAccProjectResourceWithUserDataSourceConfig(cloudID, projectName string) string {
	return fmt.Sprintf(`
data "anyscale_user" "current" {}

resource "anyscale_project" "test" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project using user data source"

  collaborator {
    email            = data.anyscale_user.current.email
    permission_level = "owner"
  }
}
`, projectName, cloudID)
}

// TestAccProjectResource_Disappears verifies that an out-of-band project
// deletion is detected by the next plan as drift.
func TestAccProjectResource_Disappears(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	projectName := UniqueName(t, "project-disappears")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccProjectResourceBasicConfig(cloudID, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
					testAccDeleteProjectViaAPI(t, "anyscale_project.test"),
				),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// testAccDeleteProjectViaAPI deletes the project directly via the Anyscale API so the next plan
// must observe drift. First retries via provider.DeleteProjectWithRetry, the exact same bounded
// capped-exponential schedule the provider's own Delete uses, since this helper provokes the
// identical delete-time permission-check consistency race from outside the provider (see that
// function's doc comment). The project was created moments ago by this same test step, so
// time.Now() is passed as createdAt; no separate no-retry code path is needed for "always
// eligible" since a fresh timestamp trivially satisfies the same age gate the resource itself
// uses.
//
// If that schedule's ~60s ceiling is still exhausted by an unusually long propagation delay (this
// has recurred a handful of times - see the project-delete-403 investigation notes - each
// observed case clearing well under 2 minutes), extends with a bounded, test-only-affordable
// second retry layer via extendProjectDeleteRetry - see that function's doc comment for why this
// is safe and what it does and does not prove about the underlying cause.
func testAccDeleteProjectViaAPI(t *testing.T, resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}

		projectID := rs.Primary.ID
		if projectID == "" {
			return fmt.Errorf("no Project ID is set for %s", resourceName)
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		err = provider.DeleteProjectWithRetry(context.Background(), client, projectID, time.Now().Format(time.RFC3339))
		if err == nil {
			return nil
		}
		if !isProjectDisappears403Extendable(err) {
			return fmt.Errorf("failed to delete project %s via API: %w", projectID, err)
		}
		return extendProjectDeleteRetry(t, client, projectID, err)
	}
}

// projectDisappearsExtendedRetryInterval and projectDisappearsExtendedRetryMaxWait bound a
// SECOND, test-only retry layer that testAccDeleteProjectViaAPI runs after
// provider.DeleteProjectWithRetry's own bounded ~60s schedule exhausts on an eligible 403 - see
// that function's doc comment for why its ceiling must stay bounded and cannot simply be raised
// for everyone. This layer is deliberately test-side-only, and vars (not consts) so a dedicated
// unit test can shrink them, mirroring resource_project.go's own
// deleteProjectRetryInitialInterval-family pattern.
//
// Unlike the production Delete a real user is waiting on, this cleanup step has no real-user
// latency cost, so it can afford to simply wait longer for the same known SpiceDB propagation
// race (see the project-delete-403 investigation notes) before giving up. This does NOT assume
// the tail is always transient lag rather than the separate, permanent missing-tuple case: a
// project can be created successfully (the Postgres write always happens) and still never get its
// SpiceDB tuple written, if the dual-write flag was off at that moment - the sweeper's own
// knownPermanentlyStuck403ProjectIDs allowlist (sweeper_project_test.go) contains exactly this
// shape of tfacc- test project. The extension is correct anyway because it handles BOTH cases
// without needing to distinguish them: a genuine lag resolves within the longer bound and the
// test proceeds as designed; a genuine missing-tuple case still exhausts the longer bound and
// fails honestly (see extendProjectDeleteRetry) rather than being silently tolerated.
//
// If this test still fails and leaks its project, what happens next depends on which case it
// was (confirmed by reading sweeper_project_test.go's sweepProjects/sweepDeleteProject, not
// changed by this hardening): a LAG-leaked project is swept cleanly by the next daily tfacc-
// sweeper run, since its 2h default age gate is vastly longer than any observed propagation
// delay (the worst real recurrence so far was 93.80s) - the race is long resolved by then, no
// manual cleanup needed. A MISSING-TUPLE-leaked project is NOT swept - it is permanently
// un-deletable by any client regardless of age, so it becomes a new entry the sweeper logs as an
// unrecognized permission-denied failure, the same shape as the existing
// knownPermanentlyStuck403ProjectIDs specimens, which would need manual triage to confirm and
// allowlist rather than disappearing on its own.
var (
	projectDisappearsExtendedRetryInterval = 8 * time.Second
	projectDisappearsExtendedRetryMaxWait  = 96 * time.Second
)

// projectDisappearsKnownNonTransient403Messages mirrors resource_project.go's
// deleteProjectNonTransient403Messages - keep these two lists in sync. Duplicated here rather than
// exported from the provider package, to keep this hardening a zero-provider-diff, test-only
// change. A 403 matching one of these is a genuine, non-transient rejection (the project still has
// active jobs/services), never the propagation race, so it must not be extended further -
// provider.DeleteProjectWithRetry has already correctly returned it immediately, without retrying
// at all.
var projectDisappearsKnownNonTransient403Messages = []string{
	"You cannot delete a project unless it has no jobs or services",
}

// isProjectDisappears403Extendable reports whether err is a 403-shaped error that the extended
// retry layer should keep chasing - i.e. not a known non-transient rejection, and not some
// unrelated error (network failure, a 409, etc).
func isProjectDisappears403Extendable(err error) bool {
	if err == nil || !strings.Contains(err.Error(), "status 403") {
		return false
	}
	for _, msg := range projectDisappearsKnownNonTransient403Messages {
		if strings.Contains(err.Error(), msg) {
			return false
		}
	}
	return true
}

// extendProjectDeleteRetry runs after provider.DeleteProjectWithRetry's own schedule has already
// exhausted on an eligible 403 (initialErr, only ever passed here when
// isProjectDisappears403Extendable already confirmed it). It keeps retrying the same DELETE at a
// fixed projectDisappearsExtendedRetryInterval cadence - continuing the same 8s cadence the
// exhausted schedule was already holding at - until projectDisappearsExtendedRetryMaxWait elapses.
//
// Every attempt logs via t.Logf rather than tflog: CI never sets TF_LOG (confirmed in the
// project-delete-403 investigation notes and still true), so tflog output is invisible in CI
// regardless of level, while t.Logf is captured in the test's own output unconditionally - this is
// what makes a future recurrence diagnosable directly from the captured CI log instead of only by
// duration.
//
// Returns nil on success (200/204/404 all count, matching DeleteProjectWithRetry's own accepted
// statuses), or a wrapped error naming both the original and final failures on exhaustion - never
// silently swallowed, so a genuine permanent denial (the missing-tuple case) still fails the test
// honestly rather than passing on a project that was never actually deleted.
func extendProjectDeleteRetry(t *testing.T, client *provider.Client, projectID string, initialErr error) error {
	t.Helper()
	t.Logf("testAccDeleteProjectViaAPI: initial retry schedule exhausted for project %s (%v); extending with a slower test-only round (interval=%s, max_wait=%s)",
		projectID, initialErr, projectDisappearsExtendedRetryInterval, projectDisappearsExtendedRetryMaxWait)

	deadline := time.Now().Add(projectDisappearsExtendedRetryMaxWait)
	attempt := 0
	lastErr := initialErr
	for {
		attempt++
		time.Sleep(projectDisappearsExtendedRetryInterval)

		_, err := provider.DoRequestRaw(
			context.Background(), client, "DELETE", fmt.Sprintf("/api/v2/projects/%s", projectID), nil,
			http.StatusOK, http.StatusNoContent, http.StatusNotFound,
		)
		if err == nil {
			t.Logf("testAccDeleteProjectViaAPI: extended attempt %d succeeded for project %s", attempt, projectID)
			return nil
		}
		lastErr = err
		t.Logf("testAccDeleteProjectViaAPI: extended attempt %d failed for project %s: %v", attempt, projectID, err)

		if !isProjectDisappears403Extendable(err) || time.Now().After(deadline) {
			break
		}
	}
	return fmt.Errorf("project %s still not deleted after the extended retry window (%d extra attempt(s) over %s): %w",
		projectID, attempt, projectDisappearsExtendedRetryMaxWait, lastErr)
}

// withFastProjectDisappearsExtendedRetryTiming shrinks projectDisappearsExtendedRetryInterval and
// projectDisappearsExtendedRetryMaxWait to millisecond scale for the duration of the test,
// restoring the originals via t.Cleanup - mirrors resource_project.go's own
// withFastRetryTiming/deleteProjectRetry* pattern so this test proves the LOGIC instantly instead
// of really waiting through the production-scale window.
func withFastProjectDisappearsExtendedRetryTiming(t *testing.T) {
	t.Helper()
	origInterval, origMaxWait := projectDisappearsExtendedRetryInterval, projectDisappearsExtendedRetryMaxWait
	projectDisappearsExtendedRetryInterval = 1 * time.Millisecond
	projectDisappearsExtendedRetryMaxWait = 10 * time.Millisecond
	t.Cleanup(func() {
		projectDisappearsExtendedRetryInterval, projectDisappearsExtendedRetryMaxWait = origInterval, origMaxWait
	})
}

// TestIsProjectDisappears403Extendable proves the gate that decides whether
// extendProjectDeleteRetry runs at all: a bare "Permission denied" 403 is extendable, the known
// non-transient active-jobs/services message is not, and neither a 409 nor a nil error is.
func TestIsProjectDisappears403Extendable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"bare permission denied 403", fmt.Errorf(`unexpected status 403: {"error":{"detail":"Permission denied"}}`), true},
		{"known non-transient active jobs/services 403", fmt.Errorf("unexpected status 403: You cannot delete a project unless it has no jobs or services"), false},
		{"409 conflict", fmt.Errorf("unexpected status 409: conflict"), false},
		{"network error", fmt.Errorf("dial tcp: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isProjectDisappears403Extendable(tc.err); got != tc.want {
				t.Errorf("isProjectDisappears403Extendable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestExtendProjectDeleteRetry covers the test-only extended retry layer testAccDeleteProjectViaAPI
// falls back to once provider.DeleteProjectWithRetry's own schedule has already exhausted on an
// eligible 403 - see extendProjectDeleteRetry's doc comment for the full rationale. Mutation-tested
// (2026-07-20): forcing the loop to stop after exactly 1 attempt made the "succeeds on a later
// extended attempt" subtest fail exactly as expected (wrong request count), then reverted clean -
// confirms this genuinely exercises the extension rather than passing regardless of it.
func TestExtendProjectDeleteRetry(t *testing.T) {
	withFastProjectDisappearsExtendedRetryTiming(t)
	initialErr := fmt.Errorf(`unexpected status 403: {"error":{"detail":"Permission denied"}}`)

	t.Run("succeeds on a later extended attempt", func(t *testing.T) {
		const projectID = "prj_extend_succeeds"
		var requestCount int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			if requestCount < 3 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"detail":"Permission denied"}}`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		client := provider.NewClientWithToken(server.URL, "test-token")
		if err := extendProjectDeleteRetry(t, client, projectID, initialErr); err != nil {
			t.Fatalf("expected the extended retry to eventually succeed, got: %v", err)
		}
		if requestCount != 3 {
			t.Fatalf("expected exactly 3 requests (2 failed + 1 success), got %d", requestCount)
		}
	})

	t.Run("exhausts the extended window and returns a non-swallowed error", func(t *testing.T) {
		const projectID = "prj_extend_exhausts"
		var requestCount int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"detail":"Permission denied"}}`))
		}))
		defer server.Close()

		client := provider.NewClientWithToken(server.URL, "test-token")
		err := extendProjectDeleteRetry(t, client, projectID, initialErr)
		if err == nil {
			t.Fatal("expected the extended retry to exhaust and surface an error, got nil - a genuine permanent denial must never be silently tolerated")
		}
		if requestCount == 0 {
			t.Fatal("expected at least one extended attempt to have been made")
		}
	})

	t.Run("a clean success needs only one extended attempt", func(t *testing.T) {
		const projectID = "prj_extend_clean"
		var requestCount int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		client := provider.NewClientWithToken(server.URL, "test-token")
		if err := extendProjectDeleteRetry(t, client, projectID, initialErr); err != nil {
			t.Fatalf("expected a clean delete, got: %v", err)
		}
		if requestCount != 1 {
			t.Fatalf("expected exactly 1 request, got %d", requestCount)
		}
	})
}

// projectDisappearsState hand-builds the terraform.State shape
// testAccDeleteProjectViaAPI reads - mirrors the established pattern in
// helpers_checkdestroy_test.go's buildlessRegistryState (see that function's doc comment for the
// precedent). Only Primary.ID is read by testAccDeleteProjectViaAPI, so Attributes is empty.
func projectDisappearsState(projectID string) *terraform.State {
	return &terraform.State{
		Modules: []*terraform.ModuleState{
			{
				Path: []string{"root"},
				Resources: map[string]*terraform.ResourceState{
					"anyscale_project.test": {
						Type: "anyscale_project",
						Primary: &terraform.InstanceState{
							ID:         projectID,
							Attributes: map[string]string{},
						},
					},
				},
			},
		},
	}
}

// TestAccProjectResourceDisappearsRetryExtension is the slow, deliberately real-time engagement
// proof for the hardening in testAccDeleteProjectViaAPI/extendProjectDeleteRetry: it drives the
// REAL, un-shrinkable-from-acctest provider.DeleteProjectWithRetry schedule (~60s, ~11 requests)
// to genuine exhaustion against a mock server, then confirms the extension picks up right where
// it left off and succeeds. This is the one thing TestExtendProjectDeleteRetry's ms-scale unit
// test structurally cannot prove: that testAccDeleteProjectViaAPI actually WIRES the real
// exhausted error into the extension, not just that the extension's own logic is correct in
// isolation - the exact "engagement, not logic" gap the collaborator-retry bug slipped through
// (see the project-delete-403 investigation notes and age-gated-retry-for-eventual-consistency-403
// memory: a unit test manipulating its own inputs missed a real non-engagement bug that only an
// end-to-end pass against the real call site caught).
//
// Deliberately slow: the mock fails the first 15 requests (comfortably more than the real
// schedule's ~11, so phase 1 is provably exhausted, not just close) and succeeds on the 16th (the
// extension's 5th attempt, so phase 2 provably made multiple attempts, not just one). Expect
// roughly 100s real wall-clock time (~60s for the production schedule + ~40s for 5 extension
// attempts at its 8s interval) - this is why it lives in the acctest-resource CI shard
// (SkipIfNotAcceptanceTest-gated, matching the C3 mock-server tests' convention) rather than the
// fast unit tier. Gated on TF_ACC only, never SkipIfNoRealInfra/ANYSCALE_TEST_REAL_INFRA (which is
// unset in ci.yml and would silently skip this in CI, defeating the entire point of an engagement
// proof) - and named to match the acctest-resource shard's ^TestAcc[A-Za-z]+Resource selector so
// it actually runs there instead of being silently skipped by name mismatch.
func TestAccProjectResourceDisappearsRetryExtension(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const projectID = "prj_disappears_engagement"
	const failCount = 15
	var requestCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount <= failCount {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"detail":"Permission denied"}}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-disappears-retry-extension")

	checkFn := testAccDeleteProjectViaAPI(t, "anyscale_project.test")
	if err := checkFn(projectDisappearsState(projectID)); err != nil {
		t.Fatalf("expected the real schedule to exhaust and the extension to then succeed, got: %v", err)
	}
	if requestCount != failCount+1 {
		t.Fatalf("expected exactly %d requests (%d failed across the real schedule + extension attempts, then 1 success), got %d",
			failCount+1, failCount, requestCount)
	}
	if requestCount <= 11 {
		t.Fatalf("expected more requests than the production schedule's own ~11-attempt ceiling makes alone (got %d) - "+
			"this would mean the extension never actually engaged", requestCount)
	}
}
