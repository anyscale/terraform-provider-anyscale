package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// userGroupMockServer is a stateful fake of the user_groups API and the
// organization_collaborators listing the provider resolves emails through.
//
// Shapes and semantics follow the backend: the group routes as recorded
// against the test org, the member and collaborator routes as traced in source:
//   - IDs are ug_ + 26 chars; source is "user" for API-created groups.
//   - Create and rename strip the name; a name taken by a live group is 409
//     "A user group with the name '<n>' already exists in this organization."
//   - The not-found 404 detail differs by route: GET says "... not found or
//     does not belong to your organization.", PATCH/DELETE say "... not
//     found.". Both start "User group with id".
//   - Member routes 404 "User IDs not found: <ids>" for a non-member user ID,
//     409 on a scim group, and write only the delta.
//   - DELETE of a scim group is not rejected.
//   - /memberships/list returns every live group including empty ones, sorted
//     by name with members sorted by email, and drops members who are no
//     longer active org users.
//   - Collaborator emails are lowercase (the backend lowercases them when it
//     creates users).
type userGroupMockServer struct {
	*httptest.Server
	t *testing.T

	mu     sync.Mutex
	seq    int
	groups map[string]*userGroupMockGroup
	// users are the active org members, keyed by usr_ ID.
	users map[string]userGroupMockUser
	// listPageSize, when >0, pages the group list by this many rows and repeats
	// the last row of each page at the top of the next, the way an insert
	// between two offset-page requests shifts a row onto the next page.
	listPageSize int
	// writes records every mutating request in order.
	writes []string
}

type userGroupMockGroup struct {
	ID        string
	Name      string
	Source    *string
	CreatedAt time.Time
	Members   map[string]bool
	OrgPerms  map[string]any
}

type userGroupMockUser struct {
	UserID     string
	IdentityID string
	Email      string
	Name       string
}

func newUserGroupMockServer(t *testing.T, users ...userGroupMockUser) *userGroupMockServer {
	t.Helper()
	m := &userGroupMockServer{t: t, groups: map[string]*userGroupMockGroup{}, users: map[string]userGroupMockUser{}}
	for _, u := range users {
		u.Email = strings.ToLower(u.Email)
		m.users[u.UserID] = u
	}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.Close)
	return m
}

func userGroupMockID(n int) string {
	return fmt.Sprintf("ug_%026d", n)
}

func (m *userGroupMockServer) providerBlock() string {
	return fmt.Sprintf(`
provider "anyscale" {
  api_url = %q
  token   = "mock-token"
}
`, m.URL)
}

// seedGroup inserts a group directly, outside Terraform.
func (m *userGroupMockServer) seedGroup(name string, source *string, memberIDs ...string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	g := &userGroupMockGroup{ID: userGroupMockID(m.seq), Name: name, Source: source, CreatedAt: time.Now().Add(time.Duration(m.seq) * time.Second), Members: map[string]bool{}}
	for _, id := range memberIDs {
		g.Members[id] = true
	}
	m.groups[g.ID] = g
	return g.ID
}

func (m *userGroupMockServer) setMember(groupID, userID string, present bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.groups[groupID]
	if !ok {
		m.t.Fatalf("mock: no group %s", groupID)
	}
	if present {
		g.Members[userID] = true
	} else {
		delete(g.Members, userID)
	}
}

func (m *userGroupMockServer) deleteGroup(groupID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.groups, groupID)
}

func (m *userGroupMockServer) removeUserFromOrg(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.users, userID)
}

// memberIDs returns a group's member user IDs, sorted, and whether it exists.
func (m *userGroupMockServer) memberIDs(groupID string) ([]string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.groups[groupID]
	if !ok {
		return nil, false
	}
	var out []string
	for id := range g.Members {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, true
}

func (m *userGroupMockServer) groupIDByName(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.groups {
		if g.Name == name {
			return g.ID
		}
	}
	return ""
}

func (m *userGroupMockServer) groupCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.groups)
}

func (m *userGroupMockServer) takeWrites() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.writes
	m.writes = nil
	return w
}

func (g *userGroupMockGroup) json(withPerms bool) map[string]any {
	ts := g.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000-07:00")
	out := map[string]any{
		"id": g.ID, "name": g.Name, "org_id": "org_mockorg00000000000000000000",
		"created_at": ts, "updated_at": ts, "deleted_at": nil,
		"organization_permissions": nil, "source": g.Source,
	}
	if withPerms && g.OrgPerms != nil {
		out["organization_permissions"] = g.OrgPerms
	}
	return out
}

func (m *userGroupMockServer) reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func (m *userGroupMockServer) fail(w http.ResponseWriter, status int, detail string) {
	m.reply(w, status, map[string]any{"error": map[string]any{"detail": detail}})
}

func (m *userGroupMockServer) nameTaken(name, exceptID string) bool {
	for _, g := range m.groups {
		if g.Name == name && g.ID != exceptID {
			return true
		}
	}
	return false
}

func (m *userGroupMockServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Method != http.MethodGet {
		entry := r.Method + " " + r.URL.Path
		if len(body) > 0 {
			entry += " " + strings.TrimSpace(string(body))
		}
		m.writes = append(m.writes, entry)
	}

	const base = "/api/v2/user_groups"
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/api/v2/organization_collaborators") && r.Method == http.MethodGet:
		m.serveCollaborators(w, r)
	case path == base+"/" && r.Method == http.MethodGet:
		m.serveList(w, r)
	case path == base+"/" && r.Method == http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(body, &req)
		name := strings.TrimSpace(req.Name)
		if name == "" {
			m.fail(w, http.StatusUnprocessableEntity, "name must not be blank")
			return
		}
		if m.nameTaken(name, "") {
			m.fail(w, http.StatusConflict, fmt.Sprintf("A user group with the name '%s' already exists in this organization.", name))
			return
		}
		m.seq++
		src := "user"
		g := &userGroupMockGroup{ID: userGroupMockID(m.seq), Name: name, Source: &src, CreatedAt: time.Now().Add(time.Duration(m.seq) * time.Second), Members: map[string]bool{}}
		m.groups[g.ID] = g
		m.reply(w, http.StatusCreated, map[string]any{"result": g.json(false)})
	case path == base+"/memberships/list" && r.Method == http.MethodGet:
		m.serveMemberships(w)
	case strings.HasPrefix(path, base+"/") && strings.HasSuffix(path, "/members"):
		m.serveMembers(w, r, strings.TrimSuffix(strings.TrimPrefix(path, base+"/"), "/members"), body)
	case strings.HasPrefix(path, base+"/"):
		m.serveGroup(w, r, strings.TrimPrefix(path, base+"/"), body)
	default:
		m.t.Errorf("mock: unexpected request %s %s", r.Method, r.URL.String())
		m.fail(w, http.StatusNotFound, "Not Found")
	}
}

func (m *userGroupMockServer) serveList(w http.ResponseWriter, r *http.Request) {
	if c := r.URL.Query().Get("count"); c != "50" {
		m.t.Errorf("mock: group list requested count=%q, want 50 (the API maximum; default is 10)", c)
	}
	all := make([]*userGroupMockGroup, 0, len(m.groups))
	for _, g := range m.groups {
		all = append(all, g)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })

	start := 0
	if tok := r.URL.Query().Get("paging_token"); tok != "" {
		start, _ = strconv.Atoi(tok)
	}
	end := len(all)
	var next any
	if m.listPageSize > 0 && start+m.listPageSize < len(all) {
		end = start + m.listPageSize
		// The next page starts one row early, duplicating this page's last row.
		next = strconv.Itoa(end - 1)
	}
	results := []any{}
	for _, g := range all[start:end] {
		results = append(results, g.json(false))
	}
	m.reply(w, http.StatusOK, map[string]any{"results": results, "metadata": map[string]any{"total": len(all), "next_paging_token": next}})
}

func (m *userGroupMockServer) serveMemberships(w http.ResponseWriter) {
	// The backend sorts groups by name and each group's members by email
	// (user_groups_service.py list_user_group_memberships).
	all := make([]*userGroupMockGroup, 0, len(m.groups))
	for _, g := range m.groups {
		all = append(all, g)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	groups := []any{}
	for _, g := range all {
		var active []userGroupMockUser
		for uid := range g.Members {
			if u, ok := m.users[uid]; ok {
				active = append(active, u)
			}
		}
		sort.Slice(active, func(i, j int) bool { return active[i].Email < active[j].Email })
		members := []any{}
		for _, u := range active {
			members = append(members, map[string]any{"user_id": u.UserID, "user_email": u.Email, "user_name": u.Name})
		}
		groups = append(groups, map[string]any{"group_id": g.ID, "group_name": g.Name, "members": members})
	}
	m.reply(w, http.StatusOK, map[string]any{"result": map[string]any{"groups": groups}})
}

func (m *userGroupMockServer) serveGroup(w http.ResponseWriter, r *http.Request, id string, body []byte) {
	g, ok := m.groups[id]
	switch r.Method {
	case http.MethodGet:
		if !ok {
			m.fail(w, http.StatusNotFound, fmt.Sprintf("User group with id '%s' not found or does not belong to your organization.", id))
			return
		}
		m.reply(w, http.StatusOK, map[string]any{"result": g.json(true)})
	case http.MethodPatch:
		if !ok {
			m.fail(w, http.StatusNotFound, fmt.Sprintf("User group with id '%s' not found.", id))
			return
		}
		if g.Source != nil && *g.Source == "scim" {
			m.fail(w, http.StatusConflict, fmt.Sprintf("User group '%s' is synced from your identity provider; its name can only be changed there.", id))
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(body, &req)
		name := strings.TrimSpace(req.Name)
		if m.nameTaken(name, id) {
			m.fail(w, http.StatusConflict, fmt.Sprintf("A user group with the name '%s' already exists in this organization.", name))
			return
		}
		g.Name = name
		m.reply(w, http.StatusOK, map[string]any{"result": g.json(false)})
	case http.MethodDelete:
		if !ok {
			m.fail(w, http.StatusNotFound, fmt.Sprintf("User group with id '%s' not found.", id))
			return
		}
		delete(m.groups, id)
		m.reply(w, http.StatusNoContent, nil)
	default:
		m.t.Errorf("mock: unexpected %s %s", r.Method, r.URL.Path)
		m.fail(w, http.StatusMethodNotAllowed, "Method Not Allowed")
	}
}

func (m *userGroupMockServer) serveMembers(w http.ResponseWriter, r *http.Request, id string, body []byte) {
	g, ok := m.groups[id]
	if !ok {
		m.fail(w, http.StatusNotFound, fmt.Sprintf("User group with id '%s' not found.", id))
		return
	}
	if g.Source != nil && *g.Source == "scim" {
		m.fail(w, http.StatusConflict, fmt.Sprintf("User group '%s' is synced from your identity provider; its members can only be changed there.", id))
		return
	}
	var req struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.UserIDs) == 0 {
		m.fail(w, http.StatusUnprocessableEntity, "user_ids must contain at least one item")
		return
	}
	var missing []string
	for _, uid := range req.UserIDs {
		if _, active := m.users[uid]; !active {
			missing = append(missing, uid)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		m.fail(w, http.StatusNotFound, "User IDs not found: "+strings.Join(missing, ", "))
		return
	}
	for _, uid := range req.UserIDs {
		if r.Method == http.MethodPost {
			g.Members[uid] = true
		} else {
			delete(g.Members, uid)
		}
	}
	m.reply(w, http.StatusNoContent, nil)
}

func (m *userGroupMockServer) serveCollaborators(w http.ResponseWriter, r *http.Request) {
	ids := make([]string, 0, len(m.users))
	for id := range m.users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	results := []any{}
	for _, id := range ids {
		u := m.users[id]
		results = append(results, map[string]any{
			"base_role": "collaborator", "additional_roles": []any{}, "permission_level": "collaborator",
			"id": u.IdentityID, "name": u.Name, "created_at": "2026-01-01T00:00:00.000000+00:00",
			"email": u.Email, "user_id": u.UserID, "is_service_account": false,
		})
	}
	m.reply(w, http.StatusOK, map[string]any{"results": results, "metadata": map[string]any{"total": len(results), "next_paging_token": nil}})
}
