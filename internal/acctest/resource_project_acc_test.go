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

// testAccDeleteProjectViaAPI deletes the project directly via the API so the next plan observes
// drift. Retries via provider.DeleteProjectWithRetry (same schedule as the resource's own Delete,
// since this provokes the same delete-time SpiceDB propagation race from outside the provider);
// time.Now() as createdAt trivially satisfies its age gate since the project was just created.
// If that ~90s schedule still exhausts, extendProjectDeleteRetry adds a small bounded second
// layer - see its doc comment.
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
// second, test-only retry layer that testAccDeleteProjectViaAPI falls back to once
// provider.DeleteProjectWithRetry's own ~90s schedule exhausts on an eligible 403. vars (not
// consts) so a unit test can shrink them, mirroring resource_project.go's
// deleteProjectRetryInitialInterval pattern. Unlike production Delete, this cleanup has no
// real-user latency cost, so it can afford to wait longer for the same SpiceDB propagation race.
//
// Deliberately small (was 96s against a 60s production ceiling; now 2 attempts, ~16s, against
// 90s): production's own real worst case at a 90s ceiling is already ~95s (its capped-exponential
// schedule overshoots the nominal ceiling by one more 8s step before giving up), which already
// exceeds the historically observed 87-94s recurrence range on its own. This extension is now
// pure margin for a slower-than-anything-yet-observed case, not something needed to reach the
// known tail. MaxWait is 2x the interval rather than an exact 1x match: a same-interval bound is
// latency-fragile in practice (confirmed empirically) - real request latency alone is enough to
// trip the elapsed-since-deadline check one attempt earlier than idealized arithmetic predicts,
// so an exact-multiple-of-one bound can silently give zero margin instead of one retry's worth.
//
// Handles two cases without distinguishing them: transient lag resolves within the longer bound;
// a permanently un-deletable project (Postgres write succeeded but its SpiceDB tuple never got
// written - see knownPermanentlyStuck403ProjectIDs in sweeper_project_test.go) still exhausts it
// and fails honestly rather than being silently tolerated. If this leaks a project anyway:
// lag-leaked ones sweep cleanly via the normal 2h age gate; missing-tuple ones don't and need
// manual triage, same as existing stuck specimens.
var (
	projectDisappearsExtendedRetryInterval = 8 * time.Second
	projectDisappearsExtendedRetryMaxWait  = 16 * time.Second
)

// projectDisappearsKnownNonTransient403Messages mirrors resource_project.go's
// deleteProjectNonTransient403Messages (duplicated rather than exported, to keep this a
// zero-provider-diff change) - keep the two lists in sync. A match here is a genuine rejection
// (active jobs/services), never the propagation race, so it must not be extended further.
var projectDisappearsKnownNonTransient403Messages = []string{
	"You cannot delete a project unless it has no jobs or services",
}

// isProjectDisappears403Extendable reports whether err is a 403 the extended retry should keep
// chasing - not a known non-transient rejection, and not an unrelated error (network, 409, etc).
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

// extendProjectDeleteRetry runs once provider.DeleteProjectWithRetry has already exhausted on an
// eligible 403 (initialErr; callers must confirm via isProjectDisappears403Extendable first). It
// retries the same DELETE at projectDisappearsExtendedRetryInterval until
// projectDisappearsExtendedRetryMaxWait elapses, logging every attempt via t.Logf (not tflog,
// which CI never captures since TF_LOG is unset in ci.yml) so a recurrence is diagnosable from
// the test output.
//
// Returns nil on success (200/204/404), or a wrapped error on exhaustion - never silently
// swallowed, so a permanent denial still fails the test honestly.
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

// withFastProjectDisappearsExtendedRetryTiming shrinks the extended-retry vars to millisecond
// scale for the test, restoring them via t.Cleanup - mirrors resource_project.go's
// withFastRetryTiming pattern so this proves the logic instantly instead of waiting real-time.
func withFastProjectDisappearsExtendedRetryTiming(t *testing.T) {
	t.Helper()
	origInterval, origMaxWait := projectDisappearsExtendedRetryInterval, projectDisappearsExtendedRetryMaxWait
	projectDisappearsExtendedRetryInterval = 1 * time.Millisecond
	projectDisappearsExtendedRetryMaxWait = 10 * time.Millisecond
	t.Cleanup(func() {
		projectDisappearsExtendedRetryInterval, projectDisappearsExtendedRetryMaxWait = origInterval, origMaxWait
	})
}

// TestIsProjectDisappears403Extendable proves the gate for extendProjectDeleteRetry: a bare
// "Permission denied" 403 is extendable; the known non-transient message, a 409, and nil are not.
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

// TestExtendProjectDeleteRetry covers the extended retry layer (see extendProjectDeleteRetry's
// doc comment). Mutation-tested (2026-07-20): forcing the loop to stop after 1 attempt failed the
// "succeeds on a later extended attempt" subtest as expected, then reverted clean.
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

// projectDisappearsState hand-builds the terraform.State shape testAccDeleteProjectViaAPI reads,
// mirroring helpers_checkdestroy_test.go's buildlessRegistryState. Only Primary.ID is read, so
// Attributes is empty.
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

// TestAccProjectResourceDisappearsRetryExtension is the real-time engagement proof for
// testAccDeleteProjectViaAPI/extendProjectDeleteRetry: it drives the real, un-shrinkable
// provider.DeleteProjectWithRetry schedule (~90s ceiling, ~15 requests) to genuine exhaustion
// against a mock server, then confirms the extension picks up and succeeds - the one thing
// TestExtendProjectDeleteRetry's ms-scale unit test can't prove, since it manipulates its own
// inputs rather than exercising the real call site (the same engagement-vs-logic gap the
// collaborator-retry bug slipped through).
//
// Deliberately slow (~105s): fails exactly the real ~15-request ceiling, so production provably
// exhausts (not just comes close), then succeeds on the 16th - the extension's first attempt.
// Gated on TF_ACC only (never the real-infra flag, which is unset in CI and would silently skip
// this), named to match the acctest-resource shard's ^TestAcc[A-Za-z]+Resource selector.
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
	if requestCount <= 15 {
		t.Fatalf("expected more requests than the production schedule's own ~15-attempt ceiling makes alone (got %d) - "+
			"this would mean the extension never actually engaged", requestCount)
	}
}
