package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func init() {
	resource.AddTestSweepers("anyscale_organization_invitation", &resource.Sweeper{
		Name: "anyscale_organization_invitation",
		F:    sweepInvitations,
	})
}

// sweepInvitationEmailPrefix matches the email format used by invitation
// acceptance tests. The legacy cloud/project prefixes don't apply here —
// invitations are addressed by email, not resource name.
const sweepInvitationEmailPrefix = "tfacc-invite-"

const sweepInvitationDefaultMinAge = 2 * time.Hour

type sweepInvitationResult struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
}

type sweepInvitationListResponse struct {
	Results  []sweepInvitationResult `json:"results"`
	Metadata struct {
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

func sweepInvitations(_ string) error {
	client, err := GetTestClient()
	if err != nil {
		log.Printf("[sweep:anyscale_organization_invitation] skipping: %v", err)
		return nil
	}

	minAge, err := resolveSweepMinAge(sweepInvitationDefaultMinAge)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-minAge)

	ctx := context.Background()
	invitations, err := listAllInvitationsForSweep(ctx, client)
	if err != nil {
		return err
	}

	log.Printf("[sweep:anyscale_organization_invitation] listed %d invitation(s); min-age=%s", len(invitations), minAge)

	var failures []string
	swept := 0
	for _, inv := range invitations {
		if !strings.HasPrefix(inv.Email, sweepInvitationEmailPrefix) {
			continue
		}

		createdAt, perr := time.Parse(time.RFC3339, inv.CreatedAt)
		if perr != nil {
			log.Printf("[sweep:anyscale_organization_invitation] KEEP %s (%s): unparseable created_at %q: %v", inv.ID, sweepRedactEmail(inv.Email), inv.CreatedAt, perr)
			continue
		}
		if createdAt.After(cutoff) {
			log.Printf("[sweep:anyscale_organization_invitation] KEEP %s (%s): too young (created %s)", inv.ID, sweepRedactEmail(inv.Email), inv.CreatedAt)
			continue
		}

		if derr := sweepInvalidateInvitation(ctx, client, inv); derr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", inv.ID, derr))
			continue
		}
		swept++
	}

	log.Printf("[sweep:anyscale_organization_invitation] swept=%d failed=%d", swept, len(failures))
	if len(failures) > 0 {
		return fmt.Errorf("invitation sweep had %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func listAllInvitationsForSweep(ctx context.Context, client *provider.Client) ([]sweepInvitationResult, error) {
	return provider.PaginatedRequest(ctx, client, "/api/v2/organization_invitations", url.Values{},
		func(body []byte) ([]sweepInvitationResult, *string, error) {
			var page sweepInvitationListResponse
			if err := json.Unmarshal(body, &page); err != nil {
				return nil, nil, fmt.Errorf("parse invitations response: %w", err)
			}
			return page.Results, page.Metadata.NextPagingToken, nil
		},
	)
}

// TestListAllInvitationsForSweep_MultiPage proves listAllInvitationsForSweep
// actually follows next_paging_token across pages instead of silently
// truncating to page one - a truncation bug here would not error, it would
// just make the sweeper blind to leaked invitations on later pages.
func TestListAllInvitationsForSweep_MultiPage(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			if got := r.URL.Query().Get("paging_token"); got != "" {
				t.Errorf("first request should not carry a paging_token, got %q", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"results":[{"id":"i1","email":"tfacc-invite-one@example.com","created_at":"2020-01-01T00:00:00Z"}],"metadata":{"next_paging_token":"page2"}}`)
			return
		}
		if got := r.URL.Query().Get("paging_token"); got != "page2" {
			t.Errorf("second request should carry paging_token=page2 as a query param, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results":[{"id":"i2","email":"tfacc-invite-two@example.com","created_at":"2020-01-01T00:00:00Z"}],"metadata":{"next_paging_token":null}}`)
	}))
	defer server.Close()

	client := provider.NewClientWithToken(server.URL, "test-token")
	invitations, err := listAllInvitationsForSweep(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("expected 2 requests (one per page), got %d", requestCount)
	}
	if len(invitations) != 2 {
		t.Fatalf("expected 2 invitations across both pages, got %d (silent truncation would show up as a short result here)", len(invitations))
	}
	if invitations[0].ID != "i1" || invitations[1].ID != "i2" {
		t.Fatalf("expected [i1, i2] in page order, got %+v", invitations)
	}
}

func sweepInvalidateInvitation(ctx context.Context, client *provider.Client, inv sweepInvitationResult) error {
	if isSweepDryRun() {
		log.Printf("[sweep:anyscale_organization_invitation] DRY-RUN would INVALIDATE %s (%s)", inv.ID, sweepRedactEmail(inv.Email))
		return nil
	}

	// Invitations are invalidated via POST, not DELETE; the resource Delete
	// implementation calls the same endpoint.
	resp, err := client.DoRequest(ctx, "POST", fmt.Sprintf("/api/v2/organization_invitations/%s/invalidate", inv.ID), nil)
	if err != nil {
		log.Printf("[sweep:anyscale_organization_invitation] DELETE FAILED %s: %v", inv.ID, err)
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	switch resp.StatusCode {
	case 200, 202, 204, 404:
		log.Printf("[sweep:anyscale_organization_invitation] DELETE OK %s (%s): status=%d", inv.ID, sweepRedactEmail(inv.Email), resp.StatusCode)
		return nil
	default:
		log.Printf("[sweep:anyscale_organization_invitation] DELETE FAILED %s (%s): status=%d body=%s", inv.ID, sweepRedactEmail(inv.Email), resp.StatusCode, truncateBody(string(body), 256))
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncateBody(string(body), 256))
	}
}

// sweepRedactEmail keeps the prefix segment for triage but drops the full
// address so logs don't leak invitee PII even when the email is synthetic.
func sweepRedactEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 0 {
		return "[redacted]"
	}
	local := email[:at]
	if len(local) > 16 {
		local = local[:16] + "..."
	}
	return local + "@..."
}
