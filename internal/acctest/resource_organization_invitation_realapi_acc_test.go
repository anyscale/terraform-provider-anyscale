package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccOrganizationInvitationResource_RealAPI sends, re-plans, imports and
// revokes a real organization invitation.
//
// The invitee is a fresh plus-address on ANYSCALE_TEST_USER_EMAIL's mailbox
// (local+tfacc-inv-<rand>@domain), so the email lands in an inbox the tester
// controls and no real person is invited. It does send one email and spends
// one of the organization's 20 invitations per 24 hours. An organization that
// requires single sign-on refuses to invite; run this against one that does
// not.
//
// Every attribute but email is Computed and an email change replaces the
// invitation, so there is no in-place update. The second step changes only the
// email's letter case and asserts a no-op plan instead: the backend treats
// case variants as one invitation, so replacing would revoke and re-send for
// nothing.
//
// Destroy invalidates the invitation. The row is never deleted, so
// CheckDestroy reads it back by ID and requires it gone or expired.
//
// Run: TF_ACC=1 ANYSCALE_TEST_USER_EMAIL=<mailbox you control> ANYSCALE_TEST_ORG_NAME=<org> go test ./internal/acctest -run '^TestAccOrganizationInvitationResource_RealAPI$' -v -count=1
func TestAccOrganizationInvitationResource_RealAPI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	base := strings.TrimSpace(os.Getenv("ANYSCALE_TEST_USER_EMAIL"))
	if base == "" {
		t.Skip("ANYSCALE_TEST_USER_EMAIL not set - skipping real-API invitation test. The invitee is a " +
			"plus-address derived from it, so set it to a mailbox you control.")
	}
	PreCheck(t)
	requireTestOrgName(t, "this test sends a real invitation")

	email, err := plusAddress(base, UniqueName(t, "inv"))
	if err != nil {
		t.Fatalf("ANYSCALE_TEST_USER_EMAIL: %s", err)
	}
	t.Logf("inviting %s", email)

	var invitationID string
	// Backstop if the run dies before destroy: revoke the invitation if it is
	// still pending. Idempotent; 404 is fine.
	t.Cleanup(func() {
		if invitationID == "" {
			return
		}
		if err := checkInvitationRevoked(invitationID); err == nil {
			return
		}
		status, body, err := orgAPI("POST", "/api/v2/organization_invitations/"+invitationID+"/invalidate", nil)
		if err != nil || (status >= 300 && status != http.StatusNotFound) {
			t.Errorf("CLEANUP FAILED: invitation %s for %s may still be pending (HTTP %d, %v): %s. "+
				"Revoke it in the console.", invitationID, email, status, err, truncateBody(string(body), 256))
		}
	})

	const addr = "anyscale_organization_invitation.test"
	config := func(e string) string {
		return realInfraProviderBlock() + fmt.Sprintf(`
resource "anyscale_organization_invitation" "test" {
  email = %q
}
`, e)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if invitationID == "" {
				return fmt.Errorf("no invitation ID was captured, so revocation cannot be checked")
			}
			return checkInvitationRevoked(invitationID)
		},
		Steps: []resource.TestStep{
			{
				Config: config(email),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "email", email),
					resource.TestCheckResourceAttr(addr, "status", "pending"),
					resource.TestCheckNoResourceAttr(addr, "accepted_at"),
					resource.TestCheckResourceAttrSet(addr, "organization_id"),
					resource.TestCheckResourceAttrSet(addr, "expires_at"),
					CaptureResourceAttr(addr, "id", &invitationID),
					func(*terraform.State) error { return checkInvitationPending(invitationID, email) },
				),
			},
			{
				Config: config(strings.ToUpper(email)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionNoop)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					// State keeps the original casing and the same invitation.
					resource.TestCheckResourceAttr(addr, "email", email),
					VerifyResourceAttrUnchanged(addr, "id", &invitationID),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// plusAddress returns local+tag@domain for base. An existing +tag on base is
// replaced, so the result always routes to the base mailbox. The result is
// lower-cased because the backend stores addresses lower-cased and import
// writes the API's copy.
func plusAddress(base, tag string) (string, error) {
	at := strings.LastIndex(base, "@")
	if at <= 0 || at == len(base)-1 {
		return "", fmt.Errorf("%q is not an email address", base)
	}
	local, domain := base[:at], base[at+1:]
	if plus := strings.Index(local, "+"); plus >= 0 {
		local = local[:plus]
	}
	return strings.ToLower(local + "+" + tag + "@" + domain), nil
}

type liveInvitation struct {
	Email      string  `json:"email"`
	ExpiresAt  string  `json:"expires_at"`
	AcceptedAt *string `json:"accepted_at"`
}

// readLiveInvitation fetches one invitation by ID; found=false on 404.
func readLiveInvitation(id string) (inv liveInvitation, found bool, err error) {
	status, body, err := orgAPI("GET", "/api/v2/organization_invitations/"+id, nil)
	if err != nil {
		return inv, false, err
	}
	if status == http.StatusNotFound {
		return inv, false, nil
	}
	if status != http.StatusOK {
		return inv, false, fmt.Errorf("GET invitation %s: HTTP %d: %s", id, status, truncateBody(string(body), 256))
	}
	var parsed struct {
		Result liveInvitation `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return inv, false, fmt.Errorf("parse invitation %s: %w", id, err)
	}
	return parsed.Result, true, nil
}

func checkInvitationPending(id, email string) error {
	inv, found, err := readLiveInvitation(id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("API has no invitation %s after create", id)
	}
	if !strings.EqualFold(inv.Email, email) {
		return fmt.Errorf("API invitation %s is for %q, want %q", id, inv.Email, email)
	}
	expires, err := parseInvitationTime(inv.ExpiresAt)
	if err != nil {
		return err
	}
	if inv.AcceptedAt != nil || !time.Now().Before(expires) {
		return fmt.Errorf("API invitation %s is not pending (accepted_at=%v, expires_at=%s)", id, inv.AcceptedAt, inv.ExpiresAt)
	}
	return nil
}

// checkInvitationRevoked passes when the invitation is gone or its expiry is
// in the past: invalidating moves expires_at rather than deleting the row.
func checkInvitationRevoked(id string) error {
	inv, found, err := readLiveInvitation(id)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if inv.AcceptedAt != nil {
		return fmt.Errorf("invitation %s was ACCEPTED (accepted_at=%s); a plus-address invitee should never accept", id, *inv.AcceptedAt)
	}
	expires, err := parseInvitationTime(inv.ExpiresAt)
	if err != nil {
		return err
	}
	if time.Now().Before(expires) {
		return fmt.Errorf("invitation %s is still pending after destroy (expires_at=%s): it was not revoked", id, inv.ExpiresAt)
	}
	return nil
}

func parseInvitationTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05"} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse invitation timestamp %q", s)
}
