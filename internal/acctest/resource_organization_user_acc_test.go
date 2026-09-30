package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Helper functions

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
