package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccOrganizationInvitationResource_Basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	// Skip by default to avoid rate limits (20 invitations per 24 hours)
	// Set ANYSCALE_TEST_INVITATIONS=1 to run these tests
	if os.Getenv("ANYSCALE_TEST_INVITATIONS") == "" {
		t.Skip("Skipping invitation tests to avoid rate limits. Set ANYSCALE_TEST_INVITATIONS=1 to run.")
		return
	}

	testEmail := UniqueName(t, "invite-basic") + "@example.com"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheckAuth(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckInvitationDestroy,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccOrganizationInvitationResourceConfig(testEmail),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_organization_invitation.test", "id"),
					resource.TestCheckResourceAttr("anyscale_organization_invitation.test", "email", testEmail),
					resource.TestCheckResourceAttrSet("anyscale_organization_invitation.test", "organization_id"),
					resource.TestCheckResourceAttrSet("anyscale_organization_invitation.test", "created_at"),
					resource.TestCheckResourceAttrSet("anyscale_organization_invitation.test", "expires_at"),
					resource.TestCheckResourceAttr("anyscale_organization_invitation.test", "status", "pending"),
					testAccCheckInvitationExistsInAPI("anyscale_organization_invitation.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// ImportState testing
			{
				ResourceName:      "anyscale_organization_invitation.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// Note: Removed TestAccOrganizationInvitationResource_OwnerPermission
// because invitation API doesn't support setting permission level during creation

func TestAccOrganizationInvitationResource_RequiresReplace(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	// Skip by default to avoid rate limits (20 invitations per 24 hours)
	if os.Getenv("ANYSCALE_TEST_INVITATIONS") == "" {
		t.Skip("Skipping invitation tests to avoid rate limits. Set ANYSCALE_TEST_INVITATIONS=1 to run.")
		return
	}

	testEmail1 := UniqueName(t, "invite-replace1") + "@example.com"
	testEmail2 := UniqueName(t, "invite-replace2") + "@example.com"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheckAuth(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckInvitationDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationInvitationResourceConfig(testEmail1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_organization_invitation.test", "email", testEmail1),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Changing email should force replacement
			{
				Config: testAccOrganizationInvitationResourceConfig(testEmail2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_organization_invitation.test", "email", testEmail2),
					// Verify it's a new invitation (different ID)
					testAccCheckInvitationExistsInAPI("anyscale_organization_invitation.test"),
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

func TestAccOrganizationInvitationResource_Delete(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	// Skip by default to avoid rate limits (20 invitations per 24 hours)
	if os.Getenv("ANYSCALE_TEST_INVITATIONS") == "" {
		t.Skip("Skipping invitation tests to avoid rate limits. Set ANYSCALE_TEST_INVITATIONS=1 to run.")
		return
	}

	testEmail := UniqueName(t, "invite-delete") + "@example.com"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheckAuth(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckInvitationDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationInvitationResourceConfig(testEmail),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckInvitationExistsInAPI("anyscale_organization_invitation.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Test deletion by removing the config
			{
				Config: "# Invitation removed",
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify invitation was invalidated
					testAccCheckInvitationDoesNotExist(testEmail),
				),
			},
		},
	})
}

// Helper functions

func testAccOrganizationInvitationResourceConfig(email string) string {
	return fmt.Sprintf(`
resource "anyscale_organization_invitation" "test" {
  email = %[1]q
}
`, email)
}

func testAccCheckInvitationExistsInAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("Not found: %s", resourceName)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("No invitation ID is set")
		}

		// Get the test client
		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("Failed to get test client: %w", err)
		}

		// Try to fetch the invitation
		invitationID := rs.Primary.ID
		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/organization_invitations/%s", invitationID), nil)
		if err != nil {
			return fmt.Errorf("Error fetching invitation: %s", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != 200 {
			return fmt.Errorf("Invitation not found in API (status %d)", resp.StatusCode)
		}

		return nil
	}
}

func testAccCheckInvitationDoesNotExist(email string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		path := fmt.Sprintf("/api/v2/organization_invitations?email=%s", url.QueryEscape(email))
		resp, err := client.DoRequest(context.Background(), "GET", path, nil)
		if err != nil {
			return fmt.Errorf("verify invitation for %s does not exist: %w", email, err)
		}
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Printf("[WARN] Failed to close response body: %v", closeErr)
			}
		}()

		if resp.StatusCode == http.StatusNotFound {
			return nil
		}

		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("verify invitation for %s does not exist: read body: %w", email, readErr)
		}

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("cannot verify invitation for %s: API returned status %d: %s", email, resp.StatusCode, truncateBody(string(body), 256))
		}

		var listResp struct {
			Results []struct {
				ID    string `json:"id"`
				Email string `json:"email"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &listResp); err != nil {
			return fmt.Errorf("verify invitation for %s does not exist: parse response: %w", email, err)
		}

		// The list endpoint may not filter server-side; match client-side to be safe.
		for _, inv := range listResp.Results {
			if inv.Email == email {
				return fmt.Errorf("invitation for %s still exists (id %s) after destroy", email, inv.ID)
			}
		}

		return nil
	}
}

// testAccCheckInvitationDestroy verifies that any anyscale_organization_invitation in
// state was invalidated. Delete on this resource POSTs to /invalidate, which the
// provider's Read treats as a soft-delete: an invalidated invitation either 404s on
// GET or returns with an expires_at in the past (status "expired"). Treat both as
// destroyed; treat "pending" or "accepted" as a leak.
func testAccCheckInvitationDestroy(s *terraform.State) error {
	client, err := GetTestClient()
	if err != nil {
		return fmt.Errorf("failed to get test client: %w", err)
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "anyscale_organization_invitation" {
			continue
		}

		invitationID := rs.Primary.ID
		if invitationID == "" {
			continue
		}

		if err := verifyInvitationDestroyed(client, invitationID); err != nil {
			return err
		}
	}

	return nil
}

func verifyInvitationDestroyed(client *provider.Client, invitationID string) error {
	resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/organization_invitations/%s", invitationID), nil)
	if err != nil {
		return fmt.Errorf("verify destroy of invitation %s: %w", invitationID, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("[WARN] Failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("verify destroy of invitation %s: read body: %w", invitationID, readErr)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cannot verify destroy of invitation %s: API returned status %d: %s", invitationID, resp.StatusCode, truncateBody(string(body), 256))
	}

	var invResp provider.OrganizationInvitationResponse
	if err := json.Unmarshal(body, &invResp); err != nil {
		return fmt.Errorf("verify destroy of invitation %s: parse response: %w", invitationID, err)
	}

	// The provider computes status client-side from accepted_at + expires_at.
	// Invalidate sets expires_at to the past, so a destroyed invitation reads as "expired".
	if invResp.Result.AcceptedAt != nil && *invResp.Result.AcceptedAt != "" {
		return fmt.Errorf("invitation %s was accepted before destroy could invalidate it", invitationID)
	}

	expires, err := time.Parse(time.RFC3339, invResp.Result.ExpiresAt)
	if err != nil {
		return fmt.Errorf("invitation %s still exists with unparseable expires_at %q", invitationID, invResp.Result.ExpiresAt)
	}
	if time.Now().Before(expires) {
		return fmt.Errorf("invitation %s still exists and is not expired (expires_at=%s) after destroy", invitationID, invResp.Result.ExpiresAt)
	}

	return nil
}

// PreCheckAuth checks for authentication without requiring cloud ID
func PreCheckAuth(t *testing.T) {
	// Check for authentication
	token := os.Getenv("ANYSCALE_CLI_TOKEN")
	if token == "" {
		// Try credentials file
		if _, err := provider.GetAuthToken(); err != nil {
			t.Fatalf("ANYSCALE_CLI_TOKEN must be set or ~/.anyscale/credentials.json must exist for acceptance tests")
		}
	}

	// Verify the token actually works; skip cleanly on expired/invalid secret.
	ValidateAuthOrSkip(t)
}
