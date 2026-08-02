package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// warnDestructiveCollaboratorTest logs a loud, explicit warning before any
// test that imports a real organization user via resource.Test.
//
// resource.Test ALWAYS calls the resource's real Delete() at teardown,
// whether the test passes or fails — CheckDestroy only controls whether
// there's a post-destroy verification, not whether destroy itself runs. For
// this resource, Delete() calls DELETE /api/v2/organization_collaborators/{id},
// which genuinely removes that identity from the organization. There is no
// undo: restoring a removed member requires re-inviting and re-accepting from
// scratch.
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

func TestAccOrganizationUserResource_Import(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	// Keyed on email since the re-key - identity_id no longer imports.
	testUserEmail := os.Getenv("ANYSCALE_TEST_USER_EMAIL")
	if testUserEmail == "" {
		t.Skip("ANYSCALE_TEST_USER_EMAIL not set, skipping import test")
	}
	warnDestructiveCollaboratorTest(t, testUserEmail)

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
				Config:        testAccOrganizationUserResourceConfig(testUserEmail),
				ResourceName:  "anyscale_organization_user.test",
				ImportState:   true,
				ImportStateId: testUserEmail,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported resource, got %d", len(states))
					}
					s := states[0]
					// id holds the EMAIL since the re-key, not the identity_id.
					if s.Attributes["id"] != testUserEmail {
						return fmt.Errorf("imported id = %q, want the email %q", s.Attributes["id"], testUserEmail)
					}
					if s.Attributes["email"] == "" {
						return fmt.Errorf("imported email is empty, want it populated from the API")
					}
					if s.Attributes["created_at"] == "" {
						return fmt.Errorf("imported created_at is empty, want it populated from the API")
					}
					// Role fields are deliberately NOT asserted here: this
					// resource manages membership only, and base_role /
					// additional_roles / permission_level live on
					// anyscale_organization_user_role and the
					// organization_user(s) data sources instead.
					return nil
				},
			},
		},
	})
}

func TestAccOrganizationUserResource_Delete(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	// Since the re-key this test asserts the OPPOSITE of what it used to:
	// destroy must LEAVE the member in place. It is still env-gated because it
	// imports and destroys against a real identity, and a regression here would
	// evict a real person - the failure mode is exactly why the gate stays.
	testUserEmail := os.Getenv("ANYSCALE_TEST_USER_EMAIL")
	if testUserEmail == "" {
		t.Skip("ANYSCALE_TEST_USER_EMAIL not set, skipping destroy test")
	}
	warnDestructiveCollaboratorTest(t, testUserEmail)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// No CheckDestroy: API has no GET-by-ID for collaborators; the inline
		// testAccCheckCollaboratorDoesNotExist below covers post-destroy state.
		Steps: []resource.TestStep{
			// Import collaborator
			{
				Config:        testAccOrganizationUserResourceConfig(testUserEmail),
				ResourceName:  "anyscale_organization_user.test",
				ImportState:   true,
				ImportStateId: testUserEmail,
			},
			// Verify it exists
			{
				Config: testAccOrganizationUserResourceConfig(testUserEmail),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckCollaboratorExistsInAPI("anyscale_organization_user.test"),
				),
			},
			// Removing the resource from config destroys it - and the member must
			// SURVIVE. This assertion is inverted from what it used to be: destroy
			// no longer evicts anyone, so a regression that restored eviction would
			// remove a real human here. That is why this stays env-gated.
			{
				Config: "# resource removed from configuration",
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckCollaboratorExistsInAPIByEmail(testUserEmail),
				),
			},
		},
	})
}

// Helper functions

// testAccOrganizationUserResourceConfig builds the resource block. email is
// REQUIRED since the re-key - it is the resource's key, and the identifier that
// exists at every point in the lifecycle. Callers pass the address they seeded.
func testAccOrganizationUserResourceConfig(email string) string {
	return fmt.Sprintf(`
resource "anyscale_organization_user" "test" {
  email = %q
}
`, email)
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

// testAccCheckCollaboratorExistsInAPIByEmail asserts the member is STILL in the
// organization. Since the re-key, destroy does not evict anyone, so this is the
// post-destroy assertion - the inverse of the one it replaced. A regression that
// restored eviction removes a real human, which is why the test that calls this
// is env-gated.
func testAccCheckCollaboratorExistsInAPIByEmail(email string) resource.TestCheckFunc {
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
			if strings.EqualFold(c.Email, email) {
				return nil
			}
		}
		return fmt.Errorf("member %s is GONE from the organization after destroy - destroying this resource must "+
			"never evict a human", email)
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
//
// Only the identity ID is modeled: the checks below key off rs.Primary.ID
// alone, and the resource itself now carries no writable attribute (roles moved
// to anyscale_organization_user_role).
func collaboratorState(identityID string) *terraform.State {
	return &terraform.State{
		Modules: []*terraform.ModuleState{
			{
				Path: []string{"root"},
				Resources: map[string]*terraform.ResourceState{
					"anyscale_organization_user.test": {
						Type: "anyscale_organization_user",
						Primary: &terraform.InstanceState{
							ID:         identityID,
							Attributes: map[string]string{},
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

	if err := testAccCheckCollaboratorExistsInAPI("anyscale_organization_user.test")(collaboratorState(identityID)); err != nil {
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

	err := testAccCheckCollaboratorExistsInAPI("anyscale_organization_user.test")(collaboratorState(identityID))
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

	if err := testAccCheckCollaboratorDoesNotExist(identityID)(collaboratorState(identityID)); err != nil {
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

	err := testAccCheckCollaboratorDoesNotExist(identityID)(collaboratorState(identityID))
	if err == nil {
		t.Fatal("expected an error when the collaborator is still present after destroy, got nil (this is the exact placebo behavior being fixed)")
	}
}
