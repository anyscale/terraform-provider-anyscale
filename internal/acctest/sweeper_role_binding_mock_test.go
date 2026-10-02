package acctest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

// roleBindingSweepMock serves the group listing, the roles probe and the
// per-group role binding listing, and records every request.
type roleBindingSweepMock struct {
	mu          sync.Mutex
	rolesStatus int
	groups      []string
	// bindings maps a group ID to its binding IDs.
	bindings     map[string][]string
	deleteStatus map[string]int
	requests     []string
}

func (m *roleBindingSweepMock) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.requests = append(m.requests, r.Method+" "+r.URL.Path)
		const byGroup = "/api/v2/role_bindings/principals/user_group/"
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v2/roles/":
			if m.rolesStatus != http.StatusOK {
				w.WriteHeader(m.rolesStatus)
				_, _ = fmt.Fprint(w, `{"error":{"detail":"Not found."}}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"results":[],"metadata":{"total":0,"next_paging_token":null}}`)
		case r.Method == "GET" && r.URL.Path == "/api/v2/user_groups/":
			_, _ = fmt.Fprintf(w, `{"results":[%s],"metadata":{"total":%d,"next_paging_token":null}}`, strings.Join(m.groups, ","), len(m.groups))
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, byGroup):
			var items []string
			for _, id := range m.bindings[strings.TrimPrefix(r.URL.Path, byGroup)] {
				items = append(items, fmt.Sprintf(`{"id":%q,"role_id":"rol_x","resource_type":"cloud","resource_id":"cld_x","origin":"imperative"}`, id))
			}
			_, _ = fmt.Fprintf(w, `{"results":[%s],"metadata":{"total":%d,"next_paging_token":null}}`, strings.Join(items, ","), len(items))
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v2/role_bindings/rb_"):
			id := strings.TrimPrefix(r.URL.Path, "/api/v2/role_bindings/")
			if s, ok := m.deleteStatus[id]; ok {
				w.WriteHeader(s)
				_, _ = fmt.Fprintf(w, `{"error":{"detail":"You may not revoke role binding '%s'."}}`, id)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
}

func (m *roleBindingSweepMock) mutations() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, r := range m.requests {
		if !strings.HasPrefix(r, "GET ") {
			out = append(out, r)
		}
	}
	return out
}

func runRoleBindingSweep(t *testing.T, m *roleBindingSweepMock) error {
	t.Helper()
	server := httptest.NewServer(m.handler(t))
	defer server.Close()
	client := provider.NewClientWithToken(server.URL, "test-token")
	return sweepRoleBindingsWithClient(context.Background(), client, time.Now().Add(-2*time.Hour))
}

func newRoleBindingSweepMock() *roleBindingSweepMock {
	return &roleBindingSweepMock{
		rolesStatus: http.StatusOK,
		groups: []string{
			userGroupSweepJSON("ug_old", "tfacc-rb-old", "user", userGroupSweepOld),
			userGroupSweepJSON("ug_human", "platform-admins", "user", userGroupSweepOld),
			userGroupSweepJSON("ug_young", "tfacc-rb-young", "user", time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00")),
		},
		bindings: map[string][]string{
			"ug_old":   {"rb_1", "rb_2"},
			"ug_human": {"rb_3"},
			"ug_young": {"rb_4"},
		},
	}
}

// Only bindings of sweepable, old-enough groups are revoked.
func TestSweepRoleBindings_RevokesOnlySweepableGroups(t *testing.T) {
	m := newRoleBindingSweepMock()
	if err := runRoleBindingSweep(t, m); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got := strings.Join(m.mutations(), "; ")
	want := "DELETE /api/v2/role_bindings/rb_1; DELETE /api/v2/role_bindings/rb_2"
	if got != want {
		t.Errorf("mutations = %q, want %q", got, want)
	}
}

// With the feature off the roles probe 404s and nothing is listed or revoked.
func TestSweepRoleBindings_FlagOffSweepsNothing(t *testing.T) {
	m := newRoleBindingSweepMock()
	m.rolesStatus = http.StatusNotFound
	if err := runRoleBindingSweep(t, m); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(m.requests) != 1 {
		t.Errorf("want only the roles probe, got %q", m.requests)
	}
}

// Any other probe failure is an error, not a silent skip.
func TestSweepRoleBindings_ProbeErrorFails(t *testing.T) {
	m := newRoleBindingSweepMock()
	m.rolesStatus = http.StatusInternalServerError
	if err := runRoleBindingSweep(t, m); err == nil {
		t.Fatal("want an error when the roles probe answers 500")
	}
	if len(m.mutations()) != 0 {
		t.Errorf("mutations = %q, want none", m.mutations())
	}
}

// A refused revoke is reported, not counted as already gone, and the rest
// of the group's bindings are still revoked.
func TestSweepRoleBindings_RefusedRevokeIsReported(t *testing.T) {
	m := newRoleBindingSweepMock()
	m.deleteStatus = map[string]int{"rb_1": http.StatusForbidden}
	err := runRoleBindingSweep(t, m)
	if err == nil || !strings.Contains(err.Error(), "rb_1") {
		t.Fatalf("want an error naming rb_1, got %v", err)
	}
	if !strings.Contains(strings.Join(m.mutations(), ";"), "rb_2") {
		t.Errorf("rb_2 was not revoked after rb_1 failed: %q", m.mutations())
	}
}

func TestSweepRoleBindings_DryRunSendsNoMutations(t *testing.T) {
	t.Setenv("ANYSCALE_SWEEP_DRY_RUN", "1")
	m := newRoleBindingSweepMock()
	if err := runRoleBindingSweep(t, m); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(m.mutations()) != 0 {
		t.Errorf("dry run sent %q", m.mutations())
	}
}
