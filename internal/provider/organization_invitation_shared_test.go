package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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

			_, err := createOrganizationInvitation(context.Background(), client, "invitee@example.com")

			if tc.wantPosted {
				if err != nil {
					t.Fatalf("sso_mode=%q must not block inviting, got: %v", tc.ssoMode, err)
				}
				if !wasPosted() {
					t.Errorf("sso_mode=%q must send the invitation, but no POST was made", tc.ssoMode)
				}
				return
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

	if organizationRequiresSSO(context.Background(), NewClientWithToken(server.URL, "test-token")) {
		t.Error("an unreadable userinfo must not be treated as SSO-required - a transient failure would " +
			"otherwise block every invitation")
	}
}
