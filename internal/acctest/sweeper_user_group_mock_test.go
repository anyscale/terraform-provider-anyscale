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

// userGroupSweepMock serves the user_groups wire shapes the live API returns
// (results+metadata on the list, result.groups on memberships, an error.detail
// body on 404) and records every request in order.
type userGroupSweepMock struct {
	mu       sync.Mutex
	pages    []string
	members  string
	requests []string
	// memberDeleteStatus/Body override DELETE /{id}/members for one group.
	memberDeleteStatus map[string]int
	memberDeleteBody   map[string]string
}

func (m *userGroupSweepMock) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		entry := r.Method + " " + r.URL.Path
		if len(body) > 0 {
			entry += " " + string(body)
		}
		m.requests = append(m.requests, entry)
		m.mu.Unlock()

		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v2/user_groups/":
			if got := r.URL.Query().Get("count"); got != "50" {
				t.Errorf("list should request count=50, got %q", got)
			}
			idx := 0
			if tok := r.URL.Query().Get("paging_token"); tok != "" {
				_, _ = fmt.Sscanf(tok, "p%d", &idx)
			}
			_, _ = fmt.Fprint(w, m.pages[idx])
		case r.Method == "GET" && r.URL.Path == "/api/v2/user_groups/memberships/list":
			_, _ = fmt.Fprint(w, m.members)
		case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/members"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v2/user_groups/"), "/members")
			if s, ok := m.memberDeleteStatus[id]; ok {
				w.WriteHeader(s)
				_, _ = fmt.Fprint(w, m.memberDeleteBody[id])
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v2/user_groups/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
}

func (m *userGroupSweepMock) mutations() []string {
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

// Timestamps use the live wire format (microseconds, +00:00 offset).
const userGroupSweepOld = "2020-01-01T00:00:00.123456+00:00"

func userGroupSweepJSON(id, name, source, createdAt string) string {
	src := "null"
	if source != "" {
		src = fmt.Sprintf("%q", source)
	}
	return fmt.Sprintf(`{"id":%q,"name":%q,"org_id":"org_x","created_at":%q,"updated_at":%q,"deleted_at":null,"organization_permissions":null,"source":%s}`,
		id, name, createdAt, createdAt, src)
}

func runUserGroupSweep(t *testing.T, m *userGroupSweepMock) error {
	t.Helper()
	server := httptest.NewServer(m.handler(t))
	defer server.Close()
	client := provider.NewClientWithToken(server.URL, "test-token")
	return sweepUserGroupsWithClient(context.Background(), client, time.Now().Add(-2*time.Hour))
}

// TestSweepUserGroups_RemovesMembersBeforeDelete guards the one ordering that
// keeps a swept group from leaking access: per the backend source, group
// delete does not revoke its members' grants, so members are removed first.
func TestSweepUserGroups_RemovesMembersBeforeDelete(t *testing.T) {
	t.Setenv("ANYSCALE_SWEEP_DRY_RUN", "")
	m := &userGroupSweepMock{
		pages: []string{`{"results":[` + userGroupSweepJSON("ug_a", "tfacc-ug-a", "user", userGroupSweepOld) + `],"metadata":{"total":1,"next_paging_token":null}}`},
		members: `{"result":{"groups":[{"group_id":"ug_a","group_name":"tfacc-ug-a","members":[` +
			`{"user_id":"usr_2","user_email":"b@example.com","user_name":"B"},` +
			`{"user_id":"usr_1","user_email":"a@example.com","user_name":"A"}]}]}}`,
	}
	if err := runUserGroupSweep(t, m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := m.mutations()
	want := []string{
		`DELETE /api/v2/user_groups/ug_a/members {"user_ids":["usr_2","usr_1"]}`,
		`DELETE /api/v2/user_groups/ug_a`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mutations:\n got  %q\n want %q", got, want)
	}
}

// TestSweepUserGroups_EmptyGroupSkipsMemberCall: the memberships listing
// includes empty groups with members:[], and an empty user_ids body is not a
// call worth sending.
func TestSweepUserGroups_EmptyGroupSkipsMemberCall(t *testing.T) {
	t.Setenv("ANYSCALE_SWEEP_DRY_RUN", "")
	m := &userGroupSweepMock{
		pages:   []string{`{"results":[` + userGroupSweepJSON("ug_e", "tfacc-ug-e", "user", userGroupSweepOld) + `],"metadata":{"total":1,"next_paging_token":null}}`},
		members: `{"result":{"groups":[{"group_id":"ug_e","group_name":"tfacc-ug-e","members":[]}]}}`,
	}
	if err := runUserGroupSweep(t, m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := m.mutations(); len(got) != 1 || got[0] != "DELETE /api/v2/user_groups/ug_e" {
		t.Fatalf("expected only the group DELETE, got %q", got)
	}
}

// TestSweepUserGroups_FilterGuards: only old, non-synced, sweepable-prefix
// groups are touched. A null source (groups created before the source column)
// is not synced. The positive control (ug_ok) proves the filter is not simply
// rejecting everything.
func TestSweepUserGroups_FilterGuards(t *testing.T) {
	t.Setenv("ANYSCALE_SWEEP_DRY_RUN", "")
	young := time.Now().UTC().Format("2006-01-02T15:04:05.000000-07:00")
	m := &userGroupSweepMock{
		pages: []string{`{"results":[` + strings.Join([]string{
			userGroupSweepJSON("ug_scim", "tfacc-ug-scim", "scim", userGroupSweepOld),
			userGroupSweepJSON("ug_young", "tfacc-ug-young", "user", young),
			userGroupSweepJSON("ug_prod", "platform-team", "user", userGroupSweepOld),
			userGroupSweepJSON("ug_ok", "tfacc-ug-ok", "", userGroupSweepOld),
		}, ",") + `],"metadata":{"total":4,"next_paging_token":null}}`},
		members: `{"result":{"groups":[]}}`,
	}
	if err := runUserGroupSweep(t, m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := m.mutations(); len(got) != 1 || got[0] != "DELETE /api/v2/user_groups/ug_ok" {
		t.Fatalf("expected only ug_ok to be deleted, got %q", got)
	}
}

// TestSweepUserGroups_PagesAndDedupes: the list pages by offset over
// created_at desc, so a group can appear on two pages. It must be followed to
// page two and deleted once.
func TestSweepUserGroups_PagesAndDedupes(t *testing.T) {
	t.Setenv("ANYSCALE_SWEEP_DRY_RUN", "")
	a := userGroupSweepJSON("ug_a", "tfacc-ug-a", "user", userGroupSweepOld)
	b := userGroupSweepJSON("ug_b", "tfacc-ug-b", "user", userGroupSweepOld)
	m := &userGroupSweepMock{
		pages: []string{
			`{"results":[` + a + `],"metadata":{"total":2,"next_paging_token":"p1"}}`,
			`{"results":[` + a + `,` + b + `],"metadata":{"total":2,"next_paging_token":null}}`,
		},
		members: `{"result":{"groups":[]}}`,
	}
	if err := runUserGroupSweep(t, m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"DELETE /api/v2/user_groups/ug_a", "DELETE /api/v2/user_groups/ug_b"}
	if got := m.mutations(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mutations:\n got  %q\n want %q", got, want)
	}
}

// TestSweepUserGroups_MemberRemovalFailureKeepsGroup: if members cannot be
// removed, deleting the group anyway would leak their access permanently, so
// the group stays for the next sweep and the job fails. A "User IDs not found"
// 404 is a member problem, not "group already gone".
func TestSweepUserGroups_MemberRemovalFailureKeepsGroup(t *testing.T) {
	t.Setenv("ANYSCALE_SWEEP_DRY_RUN", "")
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, `{"error":{"detail":"boom"}}`},
		{"user ids not found", http.StatusNotFound, `{"error":{"detail":"User IDs not found: usr_1"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &userGroupSweepMock{
				pages:              []string{`{"results":[` + userGroupSweepJSON("ug_a", "tfacc-ug-a", "user", userGroupSweepOld) + `],"metadata":{"total":1,"next_paging_token":null}}`},
				members:            `{"result":{"groups":[{"group_id":"ug_a","group_name":"tfacc-ug-a","members":[{"user_id":"usr_1","user_email":"a@example.com","user_name":"A"}]}]}}`,
				memberDeleteStatus: map[string]int{"ug_a": tc.status},
				memberDeleteBody:   map[string]string{"ug_a": tc.body},
			}
			err := runUserGroupSweep(t, m)
			if err == nil || !strings.Contains(err.Error(), "ug_a") {
				t.Fatalf("expected a sweep failure naming ug_a, got %v", err)
			}
			for _, r := range m.mutations() {
				if r == "DELETE /api/v2/user_groups/ug_a" {
					t.Fatalf("group was deleted after member removal failed: %q", m.mutations())
				}
			}
		})
	}
}

// TestSweepUserGroups_GroupGoneDuringMemberRemoval: a "User group ... not
// found" 404 on member removal means a concurrent destroy got there first.
func TestSweepUserGroups_GroupGoneDuringMemberRemoval(t *testing.T) {
	t.Setenv("ANYSCALE_SWEEP_DRY_RUN", "")
	m := &userGroupSweepMock{
		pages:              []string{`{"results":[` + userGroupSweepJSON("ug_a", "tfacc-ug-a", "user", userGroupSweepOld) + `],"metadata":{"total":1,"next_paging_token":null}}`},
		members:            `{"result":{"groups":[{"group_id":"ug_a","group_name":"tfacc-ug-a","members":[{"user_id":"usr_1","user_email":"a@example.com","user_name":"A"}]}]}}`,
		memberDeleteStatus: map[string]int{"ug_a": http.StatusNotFound},
		memberDeleteBody:   map[string]string{"ug_a": `{"error":{"detail":"User group with id 'ug_a' not found or does not belong to your organization."}}`},
	}
	if err := runUserGroupSweep(t, m); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

// TestSweepUserGroups_DryRunSendsNoMutations: dry-run must not send either
// DELETE.
func TestSweepUserGroups_DryRunSendsNoMutations(t *testing.T) {
	t.Setenv("ANYSCALE_SWEEP_DRY_RUN", "1")
	m := &userGroupSweepMock{
		pages:   []string{`{"results":[` + userGroupSweepJSON("ug_a", "tfacc-ug-a", "user", userGroupSweepOld) + `],"metadata":{"total":1,"next_paging_token":null}}`},
		members: `{"result":{"groups":[{"group_id":"ug_a","group_name":"tfacc-ug-a","members":[{"user_id":"usr_1","user_email":"a@example.com","user_name":"A"}]}]}}`,
	}
	if err := runUserGroupSweep(t, m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := m.mutations(); len(got) != 0 {
		t.Fatalf("dry-run sent mutations: %q", got)
	}
}
