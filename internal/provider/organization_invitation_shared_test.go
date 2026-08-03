package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// newSSOModeServer serves userinfo with a given sso_mode and records whether an
// invitation was ever POSTed. The POST record is the assertion that matters: the
// guard's whole purpose is that no email is sent, and only the request log shows
// that. Comparing returned errors would pass against a guard that refused AFTER
// sending.
func newSSOModeServer(t *testing.T, ssoMode string) (*httptest.Server, func() bool) {
	t.Helper()
	var mu sync.Mutex
	posted := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/userinfo":
			w.WriteHeader(http.StatusOK)
			// sso_required is deliberately reported as TRUE for every mode here,
			// including "off". The real API derives it as (sso_mode != off), so it
			// is true for optional AND required - a guard keyed on it would refuse
			// in optional orgs that work fine. Sending an always-true value means
			// any implementation that reads sso_required fails these tests.
			_, _ = fmt.Fprintf(w, `{"result":{"organizations":[{"sso_mode":%q,"sso_required":true}]}}`, ssoMode)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/organization_invitations":
			mu.Lock()
			posted = true
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"result":{"id":"inv_mock"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	return server, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return posted
	}
}

// TestCreateOrganizationInvitation_SSOModeGuard pins which sso_mode values block
// an invitation, and it is the "optional" row that carries the real risk.
//
// The backend's own `sso_required` field is derived as (sso_mode != off), so it
// is true for optional as well as required. Keying the guard on it would refuse
// in optional-mode organizations, which accept invitations perfectly well -
// blocking a working configuration to prevent a problem those orgs do not have.
// Only `required` trips the accept-side 403.
func TestCreateOrganizationInvitation_SSOModeGuard(t *testing.T) {
	for _, tc := range []struct {
		ssoMode    string
		wantPosted bool
	}{
		{ssoMode: "off", wantPosted: true},
		// The row that matters: optional must still invite.
		{ssoMode: "optional", wantPosted: true},
		{ssoMode: "required", wantPosted: false},
	} {
		t.Run("sso_mode="+tc.ssoMode, func(t *testing.T) {
			server, wasPosted := newSSOModeServer(t, tc.ssoMode)
			client := NewClientWithToken(server.URL, "test-token")

			var diags diag.Diagnostics
			_, err := createOrganizationInvitation(context.Background(), client, "invitee@example.com", &diags)

			if tc.wantPosted {
				if err != nil {
					t.Fatalf("sso_mode=%q must not block inviting, got: %v", tc.ssoMode, err)
				}
				if !wasPosted() {
					t.Errorf("sso_mode=%q must send the invitation, but no POST was made", tc.ssoMode)
				}
				// A SUCCESSFUL read must be silent. The warning exists for the case
				// where the check could not run; firing it here would put a warning
				// on every apply in every ordinary organization, and a warning that
				// always fires is one people learn to scroll past - which costs the
				// rare real one its only job.
				if diags.WarningsCount() != 0 {
					t.Errorf("sso_mode=%q read cleanly, so nothing was skipped and nothing should be warned "+
						"about; got: %v", tc.ssoMode, diags.Warnings())
				}
				return
			}
			if diags.WarningsCount() != 0 {
				t.Errorf("sso_mode=required is REFUSED, not skipped - the error carries the explanation and a "+
					"warning alongside it is noise; got: %v", diags.Warnings())
			}

			if err == nil {
				t.Fatal("sso_mode=required must refuse - the emailed link cannot be used and sending one " +
					"consumes a rate-limited invitation")
			}
			if wasPosted() {
				t.Error("sso_mode=required must refuse BEFORE sending. A POST was made, so an email went out " +
					"with a dead link and burned one of the 20 daily invitations")
			}
			if !strings.Contains(err.Error(), "single sign-on") || !strings.Contains(err.Error(), "import") {
				t.Errorf("the refusal must explain WHY and what to do instead (log in via the IdP, then "+
					"import); got: %v", err)
			}
		})
	}
}

// TestOrganizationRequiresSSO_UnreadableDefaultsToAllowing pins the safe
// direction when userinfo cannot be read.
//
// Failing to determine sso_mode is not evidence the org requires SSO. Refusing
// on a transient error would convert a blip into a blocked apply - worse than
// the wasted email this guard exists to prevent.
func TestOrganizationRequiresSSO_UnreadableDefaultsToAllowing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	required, determined := organizationRequiresSSO(context.Background(), NewClientWithToken(server.URL, "test-token"))
	if required {
		t.Error("an unreadable userinfo must not be treated as SSO-required - a transient failure would " +
			"otherwise block every invitation")
	}
	if determined {
		t.Error("an unreadable userinfo must report the check as UNDETERMINED, so the caller can warn. " +
			"Failing open silently gives an SSO-required org the dead-link email with no indication why")
	}

	// And the warning must actually reach the practitioner.
	var diags diag.Diagnostics
	_, _ = createOrganizationInvitation(context.Background(), NewClientWithToken(server.URL, "test-token"),
		"invitee@example.com", &diags)
	if diags.WarningsCount() == 0 {
		t.Error("failing open must emit a WARNING - an unrun check reported as a clean pass is exactly the " +
			"failure this guard exists to prevent")
	}
}

// newUnreadableSSOModeServer serves the invitation POST but makes the SSO
// preflight unreadable, so the guard takes its fail-open branch.
//
// The 500 is deliberate rather than a 404: a 404 is what a mock that simply
// forgot the route returns, and this test must not be satisfiable by that
// accident — see the doc comment on the test below.
func newUnreadableSSOModeServer(t *testing.T) (*httptest.Server, func() bool) {
	t.Helper()
	var mu sync.Mutex
	posted := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/userinfo":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":{"detail":"transient"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/organization_invitations":
			mu.Lock()
			posted = true
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"result":{"id":"invite_unreadable_probe"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, `{"error":{"detail":"unexpected %s %s"}}`, r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server, func() bool { mu.Lock(); defer mu.Unlock(); return posted }
}

// TestCreateOrganizationInvitation_SSOPreflightUnreadable covers the fail-open
// branch DELIBERATELY.
//
// WHY THIS EXISTS. Until 22a58be every mock-backed invitation test reached this
// branch BY ACCIDENT: none of those mocks served /api/v2/userinfo, so the
// preflight 404'd and the guard fell open. Coverage was inverted — the degraded
// path exercised by everything, the ordinary path by nothing — and 22a58be fixed
// it by giving those mocks a real userinfo handler.
//
// That fix removed the incidental coverage. This test replaces it deliberately:
// it makes userinfo unreadable explicitly, so it does not depend on a mock
// forgetting a route.
//
// BOTH ASSERTIONS ARE LOAD-BEARING and they are separable. Fail-open means the
// invitation is still sent; loudly means a warning says the check did not run.
// A mutation that keeps the send and drops the warning leaves the action
// byte-identical — a test asserting only "the invitation went out" passes it
// (that is exactly what M2 demonstrated against the guard's own test). So the
// warning is asserted, not assumed.
func TestCreateOrganizationInvitation_SSOPreflightUnreadable(t *testing.T) {
	server, wasPosted := newUnreadableSSOModeServer(t)
	client := NewClientWithToken(server.URL, "test-token")

	var diags diag.Diagnostics
	_, err := createOrganizationInvitation(context.Background(), client, "invitee@example.com", &diags)

	// FAIL OPEN: a failed lookup is not evidence that SSO is required, so the
	// invitation must still go out rather than turning a transient error into a
	// failed apply.
	if err != nil {
		t.Fatalf("an unreadable SSO preflight must not block inviting, got: %v", err)
	}
	if !wasPosted() {
		t.Error("an unreadable SSO preflight must still send the invitation, but no POST was made")
	}

	// LOUDLY: without this, a user in an SSO-required org gets the dead-link
	// email the guard exists to prevent, with nothing indicating the guard did
	// not run. Silent fail-open is indistinguishable, from the user's side, from
	// the defect being unfixed.
	if diags.WarningsCount() != 1 {
		t.Fatalf("an unreadable SSO preflight must emit exactly one warning saying the check did not run; got %d: %v",
			diags.WarningsCount(), diags.Warnings())
	}
	if summary := diags.Warnings()[0].Summary(); !strings.Contains(summary, "Single Sign-On") {
		t.Errorf("the warning must name what did not run; summary was %q", summary)
	}
}
