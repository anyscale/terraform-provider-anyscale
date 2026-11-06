package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A newly granted binding answers GET and DELETE by ID with 403 for a few
// seconds while its permission propagates (measured), with the same detail a
// revoked ID gets. These tests drive the real Delete and ImportState against
// that text, verbatim.

const retryBindingID = "rb_retry0000000000000000000"

type byIDRetryServer struct {
	mu sync.Mutex
	// failures is how many by-ID calls answer 403 before succeeding; -1 is
	// every call.
	failures int
	// detail overrides the 403 detail (default: the propagation text).
	detail string
	// listed is whether the principal listing still shows the binding.
	listed  bool
	deletes int
	gets    int
}

func (s *byIDRetryServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		deny := func(n int, verb string) bool {
			if s.failures != -1 && n > s.failures {
				return false
			}
			detail := s.detail
			if detail == "" {
				detail = "You may not " + verb + " role binding '" + retryBindingID + "'."
			}
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"detail": detail}})
			return true
		}
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v2/role_bindings/"+retryBindingID:
			s.deletes++
			if !deny(s.deletes, "revoke") {
				w.WriteHeader(http.StatusNoContent)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/role_bindings/"+retryBindingID:
			s.gets++
			if !deny(s.gets, "read") {
				_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
					"id": retryBindingID, "principal_type": "user_group", "principal_id": regrantGroup,
					"role_id": regrantRole, "resource_type": "cloud", "resource_id": regrantCloud,
					"created_by": nil, "created_at": regrantCreated, "origin": "imperative",
				}})
			}
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/principals/"):
			results := []map[string]any{}
			if s.listed {
				results = append(results, map[string]any{"id": retryBindingID, "role_id": regrantRole,
					"resource_type": "cloud", "resource_id": regrantCloud, "origin": "imperative"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results, "metadata": map[string]any{"next_paging_token": nil}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func newByIDRetryResource(t *testing.T, s *byIDRetryServer) *IAMRoleGrantResource {
	t.Helper()
	fastRoleBindingRetry(t)
	server := httptest.NewServer(s.handler(t))
	t.Cleanup(server.Close)
	return &IAMRoleGrantResource{client: NewClientWithToken(server.URL, "test-token")}
}

func runByIDRetryDelete(t *testing.T, s *byIDRetryServer, createdAt time.Time) *resource.DeleteResponse {
	t.Helper()
	r := newByIDRetryResource(t, s)
	state := forbiddenStateFixture(t, r, map[string]any{
		"id":            types.StringValue(retryBindingID),
		"user_group_id": types.StringValue(regrantGroup),
		"role_id":       types.StringValue(regrantRole),
		"cloud_id":      types.StringValue(regrantCloud),
		"created_at":    types.StringValue(createdAt.UTC().Format("2006-01-02T15:04:05.000000+00:00")),
	})
	resp := &resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)
	return resp
}

func TestRoleBindingDelete_PropagationRetry(t *testing.T) {
	recent, old := time.Now().Add(-10*time.Second), time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name        string
		server      *byIDRetryServer
		createdAt   time.Time
		wantErr     bool
		wantDeletes func(int) bool
	}{
		{name: "recent and listed: retried until it succeeds", server: &byIDRetryServer{failures: 2, listed: true},
			createdAt: recent, wantDeletes: func(n int) bool { return n == 3 }},
		{name: "recent and listed, never succeeds: bounded, then an error", server: &byIDRetryServer{failures: -1, listed: true},
			createdAt: recent, wantErr: true, wantDeletes: func(n int) bool { return n > 1 }},
		{name: "old and listed: a real refusal, not retried", server: &byIDRetryServer{failures: -1, listed: true},
			createdAt: old, wantErr: true, wantDeletes: func(n int) bool { return n == 1 }},
		{name: "recent but no longer listed: already revoked", server: &byIDRetryServer{failures: -1, listed: false},
			createdAt: recent, wantDeletes: func(n int) bool { return n == 1 }},
		{name: "a different 403 is never retried", server: &byIDRetryServer{failures: -1, listed: true, detail: "You cannot revoke your own role binding."},
			createdAt: recent, wantErr: true, wantDeletes: func(n int) bool { return n == 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.server
			resp := runByIDRetryDelete(t, s, tc.createdAt)
			if got := resp.Diagnostics.HasError(); got != tc.wantErr {
				t.Errorf("error = %v, want %v: %v", got, tc.wantErr, resp.Diagnostics)
			}
			if !tc.wantDeletes(s.deletes) {
				t.Errorf("DELETEs = %d", s.deletes)
			}
		})
	}
}

func TestRoleBindingImport_PropagationRetry(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failures int
		wantErr  bool
	}{
		{name: "403 twice, then readable", failures: 2},
		{name: "always 403: bounded, then not found", failures: -1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &byIDRetryServer{failures: tc.failures}
			r := newByIDRetryResource(t, s)
			resp := &resource.ImportStateResponse{State: forbiddenStateFixture(t, r, nil)}
			r.ImportState(context.Background(), resource.ImportStateRequest{ID: retryBindingID}, resp)
			if got := resp.Diagnostics.HasError(); got != tc.wantErr {
				t.Fatalf("error = %v, want %v: %v", got, tc.wantErr, resp.Diagnostics)
			}
			if !tc.wantErr {
				var id types.String
				resp.State.GetAttribute(context.Background(), path.Root("id"), &id)
				if id.ValueString() != retryBindingID || s.gets != 3 {
					t.Errorf("id = %q after %d GETs, want %q after 3", id.ValueString(), s.gets, retryBindingID)
				}
			} else if s.gets < 2 {
				t.Errorf("GETs = %d, want the propagation 403 retried", s.gets)
			}
		})
	}
}
