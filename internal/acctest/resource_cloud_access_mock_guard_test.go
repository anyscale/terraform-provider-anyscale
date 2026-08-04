package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestCloudAccessMockRefusesUnrevokableRevoke guards the `unrevokable` entry in
// resource_cloud_access_import_acc_test.go's mock, which is load-bearing and
// would otherwise be entirely unexercised.
//
// That fixture's experiment is "the backend holds a member the config does not
// declare". Create is AUTHORITATIVE, so it revokes undeclared members - remove
// the unrevokable entry and the seeded member is deleted in step 1, leaving
// every later step running against a backend that matches the config. The
// fixture would still PASS, having stopped testing anything: the placebo shape
// its own header warns about. It cannot self-check that, because it stays
// skipped until the resource exists, so this drives the mock's HTTP surface
// directly with no provider involved.
//
// Asserts the BEHAVIORAL chain only (revoke fails, member survives), never the
// status or reason text - see cloudAccessMockUnrevokableReason.
//
// Not a TestAcc* name on purpose: no provider, no Terraform, no credentials.
func TestCloudAccessMockRefusesUnrevokableRevoke(t *testing.T) {
	server := newMockCloudAccessServer(t)

	revoke := func(t *testing.T, email string) int {
		t.Helper()
		req, err := http.NewRequest(
			http.MethodDelete,
			fmt.Sprintf("%s/api/v2/clouds/%s/collaborators/%s", server.URL, cloudAccessMockCloudID, email),
			nil,
		)
		if err != nil {
			t.Fatalf("build revoke request: %v", err)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("revoke %s: %v", email, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// listMembers reads back through the real list route the resource uses, so
	// this observes the mock the same way the provider would rather than
	// reaching into its internals.
	listMembers := func(t *testing.T) map[string]bool {
		t.Helper()
		resp, err := server.Client().Post(
			fmt.Sprintf("%s/api/v2/clouds/%s/collaborators/users/search", server.URL, cloudAccessMockCloudID),
			"application/json",
			strings.NewReader(`{}`),
		)
		if err != nil {
			t.Fatalf("list members: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list members: status %d, want 200", resp.StatusCode)
		}
		var page struct {
			Results []struct {
				Value struct {
					Email string `json:"email"`
				} `json:"value"`
			} `json:"results"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			t.Fatalf("decode member list: %v", err)
		}
		emails := make(map[string]bool, len(page.Results))
		for _, r := range page.Results {
			emails[strings.ToLower(r.Value.Email)] = true
		}
		return emails
	}

	if got := listMembers(t); !got[cloudAccessMockImplicitMember] {
		t.Fatalf("precondition failed: %s is not seeded in the mock's member list (got %v) - the import fixture's premise depends on it being there", cloudAccessMockImplicitMember, got)
	}

	// The refusal itself, asserted twice over: non-2xx AND still present.
	// Either alone would be a weaker claim - a mock could return an error and
	// delete anyway, or delete silently and report success.
	if status := revoke(t, cloudAccessMockImplicitMember); status >= 200 && status < 300 {
		t.Errorf("revoke of %s returned %d (a success status) - it must be refused, or an authoritative Create silently deletes it and the import fixture stops testing anything", cloudAccessMockImplicitMember, status)
	}
	if got := listMembers(t); !got[cloudAccessMockImplicitMember] {
		t.Fatalf("%s was deleted by a revoke that should have been refused - the import fixture's undeclared-member premise is gone and its steps would pass while proving nothing", cloudAccessMockImplicitMember)
	}

	// Repeated attempts must keep failing. An authoritative reconcile retries
	// the revoke on every apply, so a mock that refuses once and then yields
	// would break the fixture on its second step rather than its first.
	if status := revoke(t, cloudAccessMockImplicitMember); status >= 200 && status < 300 {
		t.Errorf("second revoke of %s returned %d - refusal must be durable across repeated attempts, not one-shot", cloudAccessMockImplicitMember, status)
	}
	if got := listMembers(t); !got[cloudAccessMockImplicitMember] {
		t.Fatalf("%s survived the first refused revoke but not the second - refusal is not durable", cloudAccessMockImplicitMember)
	}

	// CONTROL, and it is what makes the assertions above mean something: an
	// ordinary member must still be revokable. Without this, a mock that
	// refused EVERY revoke would satisfy every check above while being just
	// as broken - the resource could never revoke anyone.
	const ordinary = "alice@example.com"
	grant, err := server.Client().Do(mustNewRequest(t,
		http.MethodPut,
		fmt.Sprintf("%s/api/v2/clouds/%s/collaborators/users/usr_alice/roles", server.URL, cloudAccessMockCloudID),
		`{"email":"`+ordinary+`","permission_level":"write"}`,
	))
	if err != nil {
		t.Fatalf("grant %s: %v", ordinary, err)
	}
	_, _ = io.Copy(io.Discard, grant.Body)
	_ = grant.Body.Close()
	if grant.StatusCode != http.StatusOK {
		t.Fatalf("grant %s: status %d, want 200", ordinary, grant.StatusCode)
	}
	if got := listMembers(t); !got[ordinary] {
		t.Fatalf("control setup failed: %s was not added by the grant call", ordinary)
	}
	if status := revoke(t, ordinary); status < 200 || status >= 300 {
		t.Errorf("revoke of ordinary member %s returned %d, want 2xx - only the registered unrevokable member may be refused", ordinary, status)
	}
	if got := listMembers(t); got[ordinary] {
		t.Errorf("ordinary member %s survived its revoke - the mock must actually delete revokable members, or the fixture cannot detect a resource that fails to revoke", ordinary)
	}
}

func mustNewRequest(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s request: %v", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}
