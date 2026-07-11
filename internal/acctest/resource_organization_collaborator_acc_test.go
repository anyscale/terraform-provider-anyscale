package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccOrganizationCollaboratorResource_CreateFails(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// No CheckDestroy: direct create is rejected, so nothing is ever created
		// to verify the destroy of.
		Steps: []resource.TestStep{
			{
				Config:      testAccOrganizationCollaboratorResourceConfig("collaborator"),
				ExpectError: regexp.MustCompile("Direct Creation Not Supported"),
			},
		},
	})
}

// warnDestructiveCollaboratorTest logs a loud, explicit warning before any
// test that imports a real organization_collaborator via resource.Test.
//
// resource.Test ALWAYS calls the resource's real Delete() at teardown,
// whether the test passes or fails — CheckDestroy only controls whether
// there's a post-destroy verification, not whether destroy itself runs. For
// this resource, Delete() calls DELETE /api/v2/organization_collaborators/{id},
// which genuinely removes that identity from the organization. There is no
// undo: restoring a removed collaborator requires re-inviting and
// re-accepting from scratch.
//
// This test class is gated behind ANYSCALE_TEST_USER_IDENTITY_ID specifically
// so it stays opt-in, but the destructive-teardown behavior itself is not
// obvious from the env var name alone — a real, shared test-org identity
// (brent+testtfprovider@anyscale.com) was deprovisioned this way during
// development because that risk wasn't stated loudly enough. Point this env
// var only at a genuinely disposable identity you can afford to lose; the
// default, CI-safe coverage for this resource is the mocked httptest-based
// unit tests, which consume nothing real.
func warnDestructiveCollaboratorTest(t *testing.T, identityID string) {
	t.Helper()
	t.Logf("WARNING: this test imports identity %s and resource.Test WILL delete it from the "+
		"organization at teardown, pass or fail — there is no undo. Only point "+
		"ANYSCALE_TEST_USER_IDENTITY_ID at a disposable identity you can afford to lose.", identityID)
}

func TestAccOrganizationCollaboratorResource_Import(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	// This test requires an existing user identity_id
	testIdentityID := os.Getenv("ANYSCALE_TEST_USER_IDENTITY_ID")
	if testIdentityID == "" {
		t.Skip("ANYSCALE_TEST_USER_IDENTITY_ID not set, skipping import test")
	}
	warnDestructiveCollaboratorTest(t, testIdentityID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// No CheckDestroy: the API has no GET-by-ID endpoint for collaborators
		// (only list-and-filter). CheckDestroy would only verify what happens
		// after destroy, not prevent it — see warnDestructiveCollaboratorTest:
		// destroy WILL remove this identity from the org for real.
		Steps: []resource.TestStep{
			// Import existing collaborator. ImportStateVerify is NOT usable here:
			// it verifies import against an *already-established* prior resource
			// state from an earlier step (normally created via Create()), but
			// Create() is intentionally blocked for this resource — there is no
			// "old" state to compare against, so ImportStateVerify would always
			// fail with "Failed state verification, resource with ID ... not
			// found" regardless of whether import itself actually worked. This
			// went uncaught for as long as it did purely because the test always
			// skipped (no ANYSCALE_TEST_USER_IDENTITY_ID in CI) until a real
			// identity was provided. ImportStateCheck verifies the imported
			// values directly instead, which is the documented alternative for
			// exactly this situation.
			{
				Config:        testAccOrganizationCollaboratorResourceConfig("collaborator"),
				ResourceName:  "anyscale_organization_collaborator.test",
				ImportState:   true,
				ImportStateId: testIdentityID,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported resource, got %d", len(states))
					}
					s := states[0]
					if s.Attributes["id"] != testIdentityID {
						return fmt.Errorf("imported id = %q, want %q", s.Attributes["id"], testIdentityID)
					}
					if s.Attributes["email"] == "" {
						return fmt.Errorf("imported email is empty, want it populated from the API")
					}
					if s.Attributes["permission_level"] == "" {
						return fmt.Errorf("imported permission_level is empty, want it populated from the API")
					}
					if s.Attributes["created_at"] == "" {
						return fmt.Errorf("imported created_at is empty, want it populated from the API")
					}
					return nil
				},
			},
		},
	})
}

func TestAccOrganizationCollaboratorResource_UpdatePermission(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	// This test requires an existing user identity_id that can be safely modified
	testIdentityID := os.Getenv("ANYSCALE_TEST_USER_IDENTITY_ID")
	if testIdentityID == "" {
		t.Skip("ANYSCALE_TEST_USER_IDENTITY_ID not set, skipping update test")
	}
	warnDestructiveCollaboratorTest(t, testIdentityID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// No CheckDestroy: API has no GET-by-ID for collaborators. See
		// warnDestructiveCollaboratorTest above — destroy DOES remove this
		// identity from the org for real; that is not something CheckDestroy
		// could prevent even if it verified against it.
		Steps: []resource.TestStep{
			// Import as collaborator. ImportStatePersist is required here: without
			// it, an import step's result is only checked in isolation and does
			// NOT become the "current" state subsequent steps build on — since
			// Create() is intentionally blocked for this resource, the next step
			// would otherwise see no existing resource and attempt to create one
			// from Config, hitting "Direct Creation Not Supported". This went
			// uncaught for as long as it did purely because the test always
			// skipped (no ANYSCALE_TEST_USER_IDENTITY_ID in CI) until a real
			// identity was provided.
			{
				Config:             testAccOrganizationCollaboratorResourceConfig("collaborator"),
				ResourceName:       "anyscale_organization_collaborator.test",
				ImportState:        true,
				ImportStateId:      testIdentityID,
				ImportStatePersist: true,
			},
			// Verify initial state
			{
				Config: testAccOrganizationCollaboratorResourceConfig("collaborator"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_organization_collaborator.test", "permission_level", "collaborator"),
					resource.TestCheckResourceAttrSet("anyscale_organization_collaborator.test", "email"),
					resource.TestCheckResourceAttrSet("anyscale_organization_collaborator.test", "created_at"),
				),
			},
			// Update to owner
			{
				Config: testAccOrganizationCollaboratorResourceConfig("owner"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_organization_collaborator.test", "permission_level", "owner"),
					testAccCheckCollaboratorPermissionInAPI("anyscale_organization_collaborator.test", "owner"),
				),
			},
			// Update back to collaborator
			{
				Config: testAccOrganizationCollaboratorResourceConfig("collaborator"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_organization_collaborator.test", "permission_level", "collaborator"),
					testAccCheckCollaboratorPermissionInAPI("anyscale_organization_collaborator.test", "collaborator"),
				),
			},
		},
	})
}

func TestAccOrganizationCollaboratorResource_Delete(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	// This test requires a test user that can be safely removed
	// Skip this test by default to avoid accidentally removing real users
	testIdentityID := os.Getenv("ANYSCALE_TEST_USER_IDENTITY_ID_DELETABLE")
	if testIdentityID == "" {
		t.Skip("ANYSCALE_TEST_USER_IDENTITY_ID_DELETABLE not set, skipping delete test")
	}
	warnDestructiveCollaboratorTest(t, testIdentityID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// No CheckDestroy: API has no GET-by-ID for collaborators; the inline
		// testAccCheckCollaboratorDoesNotExist below covers post-destroy state.
		Steps: []resource.TestStep{
			// Import collaborator
			{
				Config:        testAccOrganizationCollaboratorResourceConfig("collaborator"),
				ResourceName:  "anyscale_organization_collaborator.test",
				ImportState:   true,
				ImportStateId: testIdentityID,
			},
			// Verify it exists
			{
				Config: testAccOrganizationCollaboratorResourceConfig("collaborator"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckCollaboratorExistsInAPI("anyscale_organization_collaborator.test"),
				),
			},
			// Delete by removing config
			{
				Config: "# Collaborator removed",
				Check: resource.ComposeAggregateTestCheckFunc(
					// Collaborator should be removed from organization
					testAccCheckCollaboratorDoesNotExist(testIdentityID),
				),
			},
		},
	})
}

// Helper functions

func testAccOrganizationCollaboratorResourceConfig(permissionLevel string) string {
	return fmt.Sprintf(`
resource "anyscale_organization_collaborator" "test" {
  permission_level = %[1]q
}
`, permissionLevel)
}

func testAccCheckCollaboratorExistsInAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("Not found: %s", resourceName)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("No collaborator ID is set")
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("Failed to get test client: %w", err)
		}

		identityID := rs.Primary.ID
		collaborators, err := listAllCollaboratorsForTest(context.Background(), client)
		if err != nil {
			return fmt.Errorf("Error fetching collaborators: %w", err)
		}

		for _, c := range collaborators {
			if c.ID == identityID {
				return nil
			}
		}

		return fmt.Errorf("collaborator %s not found in organization_collaborators list (%d entries)", identityID, len(collaborators))
	}
}

func testAccCheckCollaboratorPermissionInAPI(resourceName string, expectedPermission string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("Not found: %s", resourceName)
		}

		// Verify the permission level in state matches expected.
		actualPermission := rs.Primary.Attributes["permission_level"]
		if actualPermission != expectedPermission {
			return fmt.Errorf("expected permission %s in state, got %s", expectedPermission, actualPermission)
		}

		// Also verify the API's own value, since this check is named InAPI
		// and a state-only comparison alone cannot tell a real update from a
		// plan that merely looks right locally.
		identityID := rs.Primary.ID
		if identityID == "" {
			return fmt.Errorf("no collaborator ID is set")
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		collaborators, err := listAllCollaboratorsForTest(context.Background(), client)
		if err != nil {
			return fmt.Errorf("error fetching collaborators: %w", err)
		}

		for _, c := range collaborators {
			if c.ID == identityID {
				if c.PermissionLevel != expectedPermission {
					return fmt.Errorf("expected permission %s in API, got %s", expectedPermission, c.PermissionLevel)
				}
				return nil
			}
		}

		return fmt.Errorf("collaborator %s not found in organization_collaborators list while checking permission", identityID)
	}
}

func testAccCheckCollaboratorDoesNotExist(identityID string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("Failed to get test client: %w", err)
		}

		collaborators, err := listAllCollaboratorsForTest(context.Background(), client)
		if err != nil {
			return fmt.Errorf("Error fetching collaborators: %w", err)
		}

		for _, c := range collaborators {
			if c.ID == identityID {
				return fmt.Errorf("collaborator %s still present in organization_collaborators list after destroy", identityID)
			}
		}

		return nil
	}
}

// listAllCollaboratorsForTest pages through /api/v2/organization_collaborators
// via the shared PaginatedRequest helper, mirroring
// listAllOrganizationCollaborators in internal/provider (unexported there and
// so not reusable across packages, hence duplicated here rather than shared).
func listAllCollaboratorsForTest(ctx context.Context, client *provider.Client) ([]provider.OrganizationCollaboratorResult, error) {
	return provider.PaginatedRequest(ctx, client, "/api/v2/organization_collaborators", url.Values{"count": []string{"50"}},
		func(body []byte) ([]provider.OrganizationCollaboratorResult, *string, error) {
			var page provider.OrganizationCollaboratorsListResponse
			if err := json.Unmarshal(body, &page); err != nil {
				return nil, nil, fmt.Errorf("parse collaborators response: %w", err)
			}
			return page.Results, page.Metadata.NextPagingToken, nil
		},
	)
}

// collaboratorState hand-builds the exact terraform.State shape the
// collaborator CheckDestroy/exists functions see, matching the pattern
// established in helpers_checkdestroy_test.go for the same reason: these
// TestCheckFuncs can be called directly against a fake state and a mock
// server without ever running a real resource.Test apply.
func collaboratorState(identityID, permissionLevel string) *terraform.State {
	return &terraform.State{
		Modules: []*terraform.ModuleState{
			{
				Path: []string{"root"},
				Resources: map[string]*terraform.ResourceState{
					"anyscale_organization_collaborator.test": {
						Type: "anyscale_organization_collaborator",
						Primary: &terraform.InstanceState{
							ID: identityID,
							Attributes: map[string]string{
								"permission_level": permissionLevel,
							},
						},
					},
				},
			},
		},
	}
}

// collaboratorsListServer starts a mock /api/v2/organization_collaborators
// endpoint returning exactly the given collaborators (single page).
func collaboratorsListServer(t *testing.T, collaborators []provider.OrganizationCollaboratorResult) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(provider.OrganizationCollaboratorsListResponse{Results: collaborators})
	}))
	t.Cleanup(server.Close)
	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-collaborator-checks")
	return server
}

// TestCollaboratorExistsInAPI_SucceedsWhenPresent is the positive control:
// the identity IS in the mocked list, so the check must pass.
func TestCollaboratorExistsInAPI_SucceedsWhenPresent(t *testing.T) {
	const identityID = "ident_present"
	collaboratorsListServer(t, []provider.OrganizationCollaboratorResult{
		{ID: identityID, Email: "present@example.com", PermissionLevel: "collaborator"},
	})

	if err := testAccCheckCollaboratorExistsInAPI("anyscale_organization_collaborator.test")(collaboratorState(identityID, "collaborator")); err != nil {
		t.Fatalf("expected success for a collaborator present in the API list, got: %v", err)
	}
}

// TestCollaboratorExistsInAPI_FailsWhenAbsent is the mutation proof this
// check is no longer a placebo: before the fix, this exact scenario (the
// identity is genuinely absent from the API) still returned nil because the
// old code never parsed the response at all. It must now fail loudly.
func TestCollaboratorExistsInAPI_FailsWhenAbsent(t *testing.T) {
	const identityID = "ident_absent"
	collaboratorsListServer(t, []provider.OrganizationCollaboratorResult{
		{ID: "ident_someone_else", Email: "other@example.com", PermissionLevel: "collaborator"},
	})

	err := testAccCheckCollaboratorExistsInAPI("anyscale_organization_collaborator.test")(collaboratorState(identityID, "collaborator"))
	if err == nil {
		t.Fatal("expected an error when the collaborator is absent from the API list, got nil (this is the exact placebo behavior being fixed)")
	}
}

// TestCollaboratorDoesNotExist_SucceedsWhenAbsent is the positive control for
// the post-destroy check: the identity is genuinely gone, so it must pass.
func TestCollaboratorDoesNotExist_SucceedsWhenAbsent(t *testing.T) {
	const identityID = "ident_destroyed"
	collaboratorsListServer(t, []provider.OrganizationCollaboratorResult{
		{ID: "ident_someone_else", Email: "other@example.com", PermissionLevel: "collaborator"},
	})

	if err := testAccCheckCollaboratorDoesNotExist(identityID)(collaboratorState(identityID, "collaborator")); err != nil {
		t.Fatalf("expected success when the collaborator is genuinely absent, got: %v", err)
	}
}

// TestCollaboratorDoesNotExist_FailsWhenPresent is the mutation proof for the
// post-destroy check: before the fix, a collaborator that was NOT actually
// removed (still present in the API) still passed silently. It must now fail.
func TestCollaboratorDoesNotExist_FailsWhenPresent(t *testing.T) {
	const identityID = "ident_leaked"
	collaboratorsListServer(t, []provider.OrganizationCollaboratorResult{
		{ID: identityID, Email: "leaked@example.com", PermissionLevel: "collaborator"},
	})

	err := testAccCheckCollaboratorDoesNotExist(identityID)(collaboratorState(identityID, "collaborator"))
	if err == nil {
		t.Fatal("expected an error when the collaborator is still present after destroy, got nil (this is the exact placebo behavior being fixed)")
	}
}

// TestCollaboratorPermissionInAPI_FailsOnMismatch is the mutation proof for
// the renamed-to-be-accurate InAPI permission check: a state attribute that
// looks right but an API value that has actually diverged must be caught,
// not just the state-only comparison the old version silently relied on.
func TestCollaboratorPermissionInAPI_FailsOnMismatch(t *testing.T) {
	const identityID = "ident_permission_drift"
	collaboratorsListServer(t, []provider.OrganizationCollaboratorResult{
		{ID: identityID, Email: "drift@example.com", PermissionLevel: "owner"},
	})

	err := testAccCheckCollaboratorPermissionInAPI("anyscale_organization_collaborator.test", "collaborator")(collaboratorState(identityID, "collaborator"))
	if err == nil {
		t.Fatal("expected an error when state says collaborator but the API says owner, got nil")
	}
}
