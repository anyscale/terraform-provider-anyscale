package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHydrateCollaboratorRoles_TriStates(t *testing.T) {
	userID := "usr_test123"

	t.Run("populated - singular GET returns real additional roles", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v2/organization_collaborators/"+userID {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, `{"result":{"id":"identity-1","email":"a@example.com","permission_level":"owner","base_role":"owner","additional_roles":["image_reader"],"user_id":"usr_test123"}}`)
				return
			}
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		fromList := OrganizationCollaboratorResult{ID: "identity-1", UserID: &userID, BaseRole: "owner"}
		got := hydrateCollaboratorRoles(context.Background(), NewClientWithToken(server.URL, "test-token"), fromList)

		if got.AdditionalRoles == nil {
			t.Fatal("AdditionalRoles = nil, want a populated, non-nil slice")
		}
		if len(got.AdditionalRoles) != 1 || got.AdditionalRoles[0] != "image_reader" {
			t.Errorf("AdditionalRoles = %v, want [image_reader]", got.AdditionalRoles)
		}
	})

	// list-vs-singular disagree on base_role - shipwright's mutation-proof catch
	// (2026-07-15): the sub-case above uses base_role "owner" on BOTH the list
	// input and the mocked singular response, so it cannot distinguish correct
	// (list wins) from the original bug (singular overwrites list) - reverting
	// the fix and rerunning the suite left every sub-case green. This is the
	// scenario that actually proves it: list says one thing, the singular GET
	// (live-proven to be a real, permanently stale SpiceDB source - see the
	// hydrateCollaboratorRoles doc comment) says another, and the result must
	// report the LIST value, since that is the one guaranteed to agree with
	// permission_level. Confirmed this sub-case alone fails if the old
	// fromList.BaseRole = singular.Result.BaseRole line is restored, and passes
	// with it removed - the mutation-proof discipline shipwright applied to the
	// suite as a whole, now applied to this specific fix.
	t.Run("list and singular disagree on base_role - list wins", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v2/organization_collaborators/"+userID {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, `{"result":{"id":"identity-1","email":"a@example.com","permission_level":"owner","base_role":"owner","additional_roles":[],"user_id":"usr_test123"}}`)
				return
			}
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		fromList := OrganizationCollaboratorResult{ID: "identity-1", UserID: &userID, BaseRole: "collaborator"}
		got := hydrateCollaboratorRoles(context.Background(), NewClientWithToken(server.URL, "test-token"), fromList)

		if got.BaseRole != "collaborator" {
			t.Errorf("BaseRole = %q, want the list-derived %q preserved even though the singular GET (a real, permanently stale SpiceDB source) says %q - list is the one guaranteed to agree with permission_level", got.BaseRole, "collaborator", "owner")
		}
	})

	t.Run("empty - singular GET queried and genuinely reports none", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v2/organization_collaborators/"+userID {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, `{"result":{"id":"identity-1","email":"a@example.com","permission_level":"owner","base_role":"owner","additional_roles":[],"user_id":"usr_test123"}}`)
				return
			}
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		fromList := OrganizationCollaboratorResult{ID: "identity-1", UserID: &userID, BaseRole: "owner"}
		got := hydrateCollaboratorRoles(context.Background(), NewClientWithToken(server.URL, "test-token"), fromList)

		if got.AdditionalRoles == nil {
			t.Fatal("AdditionalRoles = nil, want a non-nil empty slice (queried and genuinely none is not the same as undetermined)")
		}
		if len(got.AdditionalRoles) != 0 {
			t.Errorf("AdditionalRoles = %v, want empty", got.AdditionalRoles)
		}
	})

	t.Run("null - no user_id, singular GET never even called", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("unexpected request for a null user_id collaborator: %s %s - the singular GET is user_id-keyed and must never be attempted", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		fromList := OrganizationCollaboratorResult{ID: "identity-2", UserID: nil, BaseRole: "collaborator"}
		got := hydrateCollaboratorRoles(context.Background(), NewClientWithToken(server.URL, "test-token"), fromList)

		if got.AdditionalRoles != nil {
			t.Errorf("AdditionalRoles = %v, want nil (undetermined) for a collaborator with no user_id", got.AdditionalRoles)
		}
		if got.BaseRole != "collaborator" {
			t.Errorf("BaseRole = %q, want the list-derived value %q preserved (base_role is always derivable from the list alone)", got.BaseRole, "collaborator")
		}
	})

	t.Run("null - singular GET fails, degrades gracefully rather than erroring", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":{"detail":"internal error"}}`)
		}))
		defer server.Close()

		fromList := OrganizationCollaboratorResult{ID: "identity-1", UserID: &userID, BaseRole: "owner"}
		got := hydrateCollaboratorRoles(context.Background(), NewClientWithToken(server.URL, "test-token"), fromList)

		if got.AdditionalRoles != nil {
			t.Errorf("AdditionalRoles = %v, want nil (undetermined) when the singular GET itself fails", got.AdditionalRoles)
		}
		if got.BaseRole != "owner" {
			t.Errorf("BaseRole = %q, want the list-derived value %q preserved on a hydration failure", got.BaseRole, "owner")
		}
	})
}
