package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A role-binding policy apply that revokes and re-grants a binding gives the
// same grant a new ID (observed on a real API: origin "declarative"). These
// tests drive the real Read against the wire shapes of the principal listing
// (RoleBindingSummary: id, role_id, resource_type, resource_id, origin) and of
// GET /role_bindings/{id}, which adds the principal, created_by and created_at.

const (
	regrantOldID   = "rb_old00000000000000000000000"
	regrantNewID   = "rb_new00000000000000000000000"
	regrantGroup   = "ug_regrant000000000000000000"
	regrantRole    = "rol_regrant00000000000000000"
	regrantCloud   = "cld_regrant00000000000000000"
	regrantOther   = "cld_other0000000000000000000"
	regrantCreated = "2026-11-04T12:00:00.000000+00:00"
)

type regrantServer struct {
	mu sync.Mutex
	// summaries is what the principal listing on regrantCloud returns.
	summaries []map[string]any
	// getStatus, when non-zero, is what GET /role_bindings/{id} answers.
	getStatus int
	origin    string
	gets      []string
}

func (s *regrantServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		listing := "/api/v2/role_bindings/cloud/" + regrantCloud + "/principals/user_group/" + regrantGroup
		switch {
		case r.Method == http.MethodGet && strings.TrimSuffix(r.URL.Path, "/") == listing:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results":  s.summaries,
				"metadata": map[string]any{"total": len(s.summaries), "next_paging_token": nil},
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v2/role_bindings/rb_"):
			id := strings.TrimPrefix(r.URL.Path, "/api/v2/role_bindings/")
			s.gets = append(s.gets, id)
			if s.getStatus != 0 {
				w.WriteHeader(s.getStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"detail": "You may not read role binding '" + id + "'."}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
				"id": id, "principal_type": "user_group", "principal_id": regrantGroup,
				"role_id": regrantRole, "resource_type": "cloud", "resource_id": regrantCloud,
				"created_by": "usr_policy00000000000000000", "created_at": regrantCreated, "origin": s.origin,
			}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func regrantSummary(id, resourceID, origin string) map[string]any {
	return map[string]any{"id": id, "role_id": regrantRole, "resource_type": "cloud", "resource_id": resourceID, "origin": origin}
}

func runRegrantRead(t *testing.T, s *regrantServer) (*resource.ReadResponse, []string) {
	t.Helper()
	server := httptest.NewServer(s.handler(t))
	t.Cleanup(server.Close)
	r := &RoleBindingResource{client: NewClientWithToken(server.URL, "test-token")}
	state := forbiddenStateFixture(t, r, map[string]any{
		"id":            types.StringValue(regrantOldID),
		"user_group_id": types.StringValue(regrantGroup),
		"role_id":       types.StringValue(regrantRole),
		"cloud_id":      types.StringValue(regrantCloud),
		"origin":        types.StringValue("imperative"),
		"created_by":    types.StringValue("usr_terraform000000000000000"),
		"created_at":    types.StringValue("2026-11-01T00:00:00.000000+00:00"),
	})
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	s.mu.Lock()
	defer s.mu.Unlock()
	return resp, append([]string(nil), s.gets...)
}

func regrantStateString(t *testing.T, resp *resource.ReadResponse, attr string) string {
	t.Helper()
	var v types.String
	if diags := resp.State.GetAttribute(context.Background(), path.Root(attr), &v); diags.HasError() {
		t.Fatalf("reading %s from state: %v", attr, diags)
	}
	return v.ValueString()
}

func regrantPolicyWarnings(diags diag.Diagnostics) int {
	n := 0
	for _, d := range diags.Warnings() {
		if strings.Contains(d.Summary(), "Role-Binding Policy") {
			n++
		}
	}
	return n
}

func TestRoleBindingRead_RegrantedUnderNewIDIsAdopted(t *testing.T) {
	for _, tc := range []struct {
		origin       string
		wantWarnings int
	}{
		{origin: "declarative", wantWarnings: 1},
		{origin: "imperative", wantWarnings: 0},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			s := &regrantServer{origin: tc.origin, summaries: []map[string]any{regrantSummary(regrantNewID, regrantCloud, tc.origin)}}
			resp, gets := runRegrantRead(t, s)
			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected error: %v", resp.Diagnostics)
			}
			if resp.State.Raw.IsNull() {
				t.Fatal("binding was removed from state; want the re-granted ID adopted")
			}
			if got := regrantStateString(t, resp, "id"); got != regrantNewID {
				t.Errorf("id = %q, want %q", got, regrantNewID)
			}
			if got := regrantStateString(t, resp, "origin"); got != tc.origin {
				t.Errorf("origin = %q, want %q", got, tc.origin)
			}
			if got := regrantStateString(t, resp, "created_at"); got != regrantCreated {
				t.Errorf("created_at = %q, want %q (from the full GET, not the stale state)", got, regrantCreated)
			}
			if len(gets) != 1 || gets[0] != regrantNewID {
				t.Errorf("GETs = %v, want exactly one GET of %s", gets, regrantNewID)
			}
			if got := regrantPolicyWarnings(resp.Diagnostics); got != tc.wantWarnings {
				t.Errorf("policy warnings = %d, want %d: %v", got, tc.wantWarnings, resp.Diagnostics)
			}
		})
	}
}

func TestRoleBindingRead_SameIDNeedsNoGet(t *testing.T) {
	s := &regrantServer{summaries: []map[string]any{
		regrantSummary(regrantNewID, regrantCloud, "declarative"),
		regrantSummary(regrantOldID, regrantCloud, "imperative"),
	}}
	resp, gets := runRegrantRead(t, s)
	if resp.Diagnostics.HasError() || len(resp.Diagnostics.Warnings()) != 0 {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if got := regrantStateString(t, resp, "id"); got != regrantOldID {
		t.Errorf("id = %q, want the ID match %q", got, regrantOldID)
	}
	if len(gets) != 0 {
		t.Errorf("GETs = %v, want none when the ID still matches", gets)
	}
}

func TestRoleBindingRead_RegrantRevokedAgainIsRemoved(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		s := &regrantServer{getStatus: status, summaries: []map[string]any{regrantSummary(regrantNewID, regrantCloud, "declarative")}}
		resp, _ := runRegrantRead(t, s)
		if resp.Diagnostics.HasError() {
			t.Fatalf("GET %d: unexpected error: %v", status, resp.Diagnostics)
		}
		if !resp.State.Raw.IsNull() {
			t.Errorf("GET %d: binding kept in state; want it removed", status)
		}
	}
}

func TestRoleBindingRead_RegrantGetFailureErrors(t *testing.T) {
	s := &regrantServer{getStatus: http.StatusInternalServerError, summaries: []map[string]any{regrantSummary(regrantNewID, regrantCloud, "declarative")}}
	resp, _ := runRegrantRead(t, s)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error when the re-granted binding cannot be read for a reason other than revocation")
	}
}

func TestRoleBindingRead_OtherScopeRowIsNotAdopted(t *testing.T) {
	s := &regrantServer{summaries: []map[string]any{regrantSummary(regrantNewID, regrantOther, "imperative")}}
	resp, gets := runRegrantRead(t, s)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("adopted a binding held on another resource; want the binding removed")
	}
	if len(gets) != 0 {
		t.Errorf("GETs = %v, want none", gets)
	}
}
