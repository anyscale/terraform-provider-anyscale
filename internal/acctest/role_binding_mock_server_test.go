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

// roleBindingMockServer is a stateful fake of the role_bindings and roles APIs.
//
// Shapes and semantics are traced from the backend source; none were recorded
// live, because no test org had the feature enabled when this was written:
//   - Every route is gated by the role-definition-as-data flag and answers
//     404 "Not found." when it is off. flagOff models that on every route.
//   - GET and DELETE /role_bindings/{id} check the caller's permission on the
//     binding object before the service runs. A revoked or never-issued id
//     has no object, so both answer 403, never 404.
//   - The resource-scoped principal listing checks manage_iam on the scope
//     first, so a deleted cloud or project answers 403 (deleteScope). A group
//     that holds nothing there, or does not exist, gets an empty page.
//   - A duplicate grant (same principal, role and resource) is 409.
//   - Deleting a group cascades its bindings.
//   - Listings page by offset: paging_token is the next offset, count is
//     capped at 50.
//   - The roles listing's name filter is a case-insensitive "contains".
type roleBindingMockServer struct {
	*httptest.Server
	t *testing.T

	mu       sync.Mutex
	seq      int
	flagOff  bool
	bindings map[string]*roleBindingMockBinding
	roles    map[string]*roleBindingMockRole
	groups   map[string]bool
	// goneScopes are "<type>/<id>" scopes deleted out of band.
	goneScopes map[string]bool
	// deleteMode simulates DELETE races: "revoked" revokes the binding and
	// answers 403, as a concurrent revoke between refresh and delete would;
	// "denied" answers 403 and keeps it, as a lost permission would.
	deleteMode string
	// groupGone404 makes the principal listings answer 404 for a group that
	// does not exist, instead of the empty page the service documents. Both
	// readings of the source are plausible, so the provider must remove state
	// under either.
	groupGone404 bool
	// rolesStatus, when non-zero, is the status every roles listing answers
	// with while the flag is on: the provider's 404 probe failing some other way.
	rolesStatus int
	// listingStatus, when non-zero, is the status every principal listing
	// answers with while the flag is on.
	listingStatus int
	// writes records every mutating request in order.
	writes []string
}

type roleBindingMockBinding struct {
	ID            string
	PrincipalType string
	PrincipalID   string
	RoleID        string
	ResourceType  string
	ResourceID    string
	Origin        *string
	CreatedBy     *string
	CreatedAt     time.Time
}

type roleBindingMockRole struct {
	ID          string
	Name        string
	Description *string
	BuiltIn     bool
	Allowed     []string
	Denied      []string
	Archived    bool
}

const roleBindingMockCaller = "usr_mockcaller0000000000000000"

func newRoleBindingMockServer(t *testing.T) *roleBindingMockServer {
	t.Helper()
	m := &roleBindingMockServer{
		t:          t,
		bindings:   map[string]*roleBindingMockBinding{},
		roles:      map[string]*roleBindingMockRole{},
		groups:     map[string]bool{},
		goneScopes: map[string]bool{},
	}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.Close)
	return m
}

func (m *roleBindingMockServer) providerBlock() string {
	return fmt.Sprintf(`
provider "anyscale" {
  api_url = %q
  token   = "mock-token"
}
`, m.URL)
}

func (m *roleBindingMockServer) setFlagOff(off bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flagOff = off
}

// addGroup registers a group ID so grants to it are accepted.
func (m *roleBindingMockServer) addGroup(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.groups[id] = true
}

// deleteGroup deletes a group and, as the backend does, its bindings.
func (m *roleBindingMockServer) deleteGroup(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.groups, id)
	for bid, b := range m.bindings {
		if b.PrincipalType == "user_group" && b.PrincipalID == id {
			delete(m.bindings, bid)
		}
	}
}

func (m *roleBindingMockServer) deleteScope(resourceType, resourceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.goneScopes[resourceType+"/"+resourceID] = true
}

func (m *roleBindingMockServer) restoreScope(resourceType, resourceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.goneScopes, resourceType+"/"+resourceID)
}

func (m *roleBindingMockServer) setGroupGone404(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.groupGone404 = on
}

func (m *roleBindingMockServer) setRolesStatus(status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rolesStatus = status
}

func (m *roleBindingMockServer) setListingStatus(status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listingStatus = status
}

func (m *roleBindingMockServer) setDeleteMode(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteMode = mode
}

func (m *roleBindingMockServer) addRole(r roleBindingMockRole) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := r
	m.roles[r.ID] = &cp
}

// seedBinding inserts a binding outside Terraform and returns its ID.
func (m *roleBindingMockServer) seedBinding(principalType, principalID, roleID, resourceType, resourceID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.insert(principalType, principalID, roleID, resourceType, resourceID).ID
}

// revoke deletes a binding out of band.
func (m *roleBindingMockServer) revoke(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bindings, id)
}

func (m *roleBindingMockServer) bindingExists(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.bindings[id]
	return ok
}

func (m *roleBindingMockServer) bindingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.bindings)
}

func (m *roleBindingMockServer) takeWrites() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.writes
	m.writes = nil
	return w
}

func roleBindingMockID(n int) string {
	return fmt.Sprintf("rb_%026d", n)
}

func (m *roleBindingMockServer) insert(principalType, principalID, roleID, resourceType, resourceID string) *roleBindingMockBinding {
	m.seq++
	origin, by := "imperative", roleBindingMockCaller
	b := &roleBindingMockBinding{
		ID: roleBindingMockID(m.seq), PrincipalType: principalType, PrincipalID: principalID,
		RoleID: roleID, ResourceType: resourceType, ResourceID: resourceID,
		Origin: &origin, CreatedBy: &by, CreatedAt: time.Date(2026, 10, 2, 12, 0, m.seq, 0, time.UTC),
	}
	m.bindings[b.ID] = b
	return b
}

func (b *roleBindingMockBinding) json() map[string]any {
	return map[string]any{
		"id": b.ID, "principal_type": b.PrincipalType, "principal_id": b.PrincipalID,
		"role_id": b.RoleID, "resource_type": b.ResourceType, "resource_id": b.ResourceID,
		"created_by": b.CreatedBy, "created_at": b.CreatedAt.Format("2006-01-02T15:04:05.000000+00:00"),
		"origin": b.Origin,
	}
}

// summaryJSON is RoleBindingSummary, what the principal listings return.
func (b *roleBindingMockBinding) summaryJSON() map[string]any {
	return map[string]any{
		"id": b.ID, "role_id": b.RoleID, "resource_type": b.ResourceType,
		"resource_id": b.ResourceID, "origin": b.Origin,
	}
}

func (r *roleBindingMockRole) json(withPerms bool) map[string]any {
	out := map[string]any{
		"id": r.ID, "name": r.Name, "description": r.Description, "built_in": r.BuiltIn,
		"is_frozen": false, "created_by": nil, "created_at": "2026-09-01T00:00:00.000000+00:00",
		"archived_at": nil, "allowed_permissions": nil, "denied_permissions": nil,
	}
	if r.Archived {
		out["archived_at"] = "2026-09-15T00:00:00.000000+00:00"
	}
	if withPerms {
		out["allowed_permissions"] = append([]string{}, r.Allowed...)
		out["denied_permissions"] = append([]string{}, r.Denied...)
	}
	return out
}

func (m *roleBindingMockServer) reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func (m *roleBindingMockServer) fail(w http.ResponseWriter, status int, detail string) {
	m.reply(w, status, map[string]any{"error": map[string]any{"detail": detail}})
}

// page slices items by the request's offset paging.
func (m *roleBindingMockServer) page(w http.ResponseWriter, r *http.Request, items []map[string]any) {
	count := 10
	if c := r.URL.Query().Get("count"); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil || n < 0 || n > 50 {
			m.fail(w, http.StatusUnprocessableEntity, "count must be between 0 and 50")
			return
		}
		count = n
	}
	offset := 0
	if p := r.URL.Query().Get("paging_token"); p != "" {
		offset, _ = strconv.Atoi(p)
	}
	end := min(offset+count, len(items))
	if offset > len(items) {
		offset = end
	}
	var next any
	if end < len(items) {
		next = strconv.Itoa(end)
	}
	m.reply(w, http.StatusOK, map[string]any{
		"results":  items[offset:end],
		"metadata": map[string]any{"total": len(items), "next_paging_token": next},
	})
}

func (m *roleBindingMockServer) serve(w http.ResponseWriter, r *http.Request) {
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

	path := strings.TrimSuffix(r.URL.Path, "/")
	const rbBase, rolesBase = "/api/v2/role_bindings", "/api/v2/roles"
	if !strings.HasPrefix(path, rbBase) && !strings.HasPrefix(path, rolesBase) {
		m.t.Errorf("mock: unexpected route %s %s", r.Method, r.URL.Path)
		m.fail(w, http.StatusNotFound, "unexpected route")
		return
	}
	if m.flagOff {
		m.fail(w, http.StatusNotFound, "Not found.")
		return
	}

	if path == rolesBase && r.Method == http.MethodGet {
		if m.rolesStatus != 0 {
			m.fail(w, m.rolesStatus, "mock roles failure")
			return
		}
		m.serveRoles(w, r)
		return
	}
	if strings.HasPrefix(path, rolesBase+"/") {
		m.t.Errorf("mock: GET /roles/{id} cannot fetch a built-in promoted elsewhere; use the ?ids= listing")
		m.fail(w, http.StatusNotFound, "Role not found.")
		return
	}

	rest := strings.Split(strings.TrimPrefix(strings.TrimPrefix(path, rbBase), "/"), "/")
	switch {
	case path == rbBase && r.Method == http.MethodPost:
		m.serveCreate(w, body)
	case len(rest) == 1 && rest[0] == "managed_resources" && r.Method == http.MethodGet:
		// The provider probes GET /roles/?count=1 on a 404, not this route.
		m.t.Errorf("mock: provider probed managed_resources; it should probe GET /roles/?count=1")
		m.reply(w, http.StatusOK, map[string]any{"results": []any{}, "metadata": map[string]any{"total": 0, "next_paging_token": nil}})
	case len(rest) == 3 && rest[0] == "principals" && r.Method == http.MethodGet:
		if m.listingOverride(w, rest[1], rest[2]) {
			return
		}
		m.page(w, r, m.summaries("", "", rest[1], rest[2]))
	case len(rest) == 5 && rest[2] == "principals" && r.Method == http.MethodGet:
		if !validResourceType(rest[0]) || !validPrincipalType(rest[3]) {
			m.fail(w, http.StatusUnprocessableEntity, "invalid path parameter")
			return
		}
		if m.goneScopes[rest[0]+"/"+rest[1]] {
			m.fail(w, http.StatusForbidden, fmt.Sprintf("You do not manage IAM on %s '%s'.", rest[0], rest[1]))
			return
		}
		if m.listingOverride(w, rest[3], rest[4]) {
			return
		}
		m.page(w, r, m.summaries(rest[0], rest[1], rest[3], rest[4]))
	case len(rest) == 1 && strings.HasPrefix(rest[0], "rb_") && r.Method == http.MethodGet:
		b, ok := m.bindings[rest[0]]
		if !ok {
			m.fail(w, http.StatusForbidden, fmt.Sprintf("You may not read role binding '%s'.", rest[0]))
			return
		}
		m.reply(w, http.StatusOK, map[string]any{"result": b.json()})
	case len(rest) == 1 && strings.HasPrefix(rest[0], "rb_") && r.Method == http.MethodDelete:
		b, ok := m.bindings[rest[0]]
		denied := !ok || m.goneScopes[b.ResourceType+"/"+b.ResourceID] || m.deleteMode == "denied"
		if ok && m.deleteMode == "revoked" {
			delete(m.bindings, rest[0])
			denied = true
		}
		if denied {
			m.fail(w, http.StatusForbidden, fmt.Sprintf("You may not revoke role binding '%s'.", rest[0]))
			return
		}
		delete(m.bindings, rest[0])
		w.WriteHeader(http.StatusNoContent)
	default:
		m.t.Errorf("mock: unhandled role_bindings route %s %s", r.Method, r.URL.Path)
		m.fail(w, http.StatusNotFound, "unhandled route")
	}
}

func (m *roleBindingMockServer) listingOverride(w http.ResponseWriter, principalType, principalID string) bool {
	if m.listingStatus != 0 {
		m.fail(w, m.listingStatus, "mock listing failure")
		return true
	}
	if !m.groupGone404 || principalType != "user_group" || m.groups[principalID] {
		return false
	}
	m.fail(w, http.StatusNotFound, fmt.Sprintf("User group '%s' not found.", principalID))
	return true
}

func validResourceType(s string) bool {
	return s == "organization" || s == "cloud" || s == "project"
}

func validPrincipalType(s string) bool {
	return s == "user" || s == "user_group"
}

func (m *roleBindingMockServer) serveCreate(w http.ResponseWriter, body []byte) {
	var req struct {
		PrincipalType string `json:"principal_type"`
		PrincipalID   string `json:"principal_id"`
		RoleID        string `json:"role_id"`
		ResourceType  string `json:"resource_type"`
		ResourceID    string `json:"resource_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil || !validPrincipalType(req.PrincipalType) || !validResourceType(req.ResourceType) || req.PrincipalID == "" || req.RoleID == "" || req.ResourceID == "" {
		m.fail(w, http.StatusUnprocessableEntity, "invalid role binding request")
		return
	}
	if m.goneScopes[req.ResourceType+"/"+req.ResourceID] {
		m.fail(w, http.StatusForbidden, fmt.Sprintf("You do not manage IAM on %s '%s'.", req.ResourceType, req.ResourceID))
		return
	}
	if req.PrincipalType == "user_group" && !m.groups[req.PrincipalID] {
		m.fail(w, http.StatusNotFound, fmt.Sprintf("User group '%s' not found.", req.PrincipalID))
		return
	}
	role, ok := m.roles[req.RoleID]
	if !ok {
		m.fail(w, http.StatusNotFound, "Role not found.")
		return
	}
	if role.Archived {
		m.fail(w, http.StatusBadRequest, "This role is archived and cannot be newly assigned.")
		return
	}
	for _, b := range m.bindings {
		if b.PrincipalType == req.PrincipalType && b.PrincipalID == req.PrincipalID && b.RoleID == req.RoleID && b.ResourceType == req.ResourceType && b.ResourceID == req.ResourceID {
			m.fail(w, http.StatusConflict, "The principal already holds this role on this resource.")
			return
		}
	}
	b := m.insert(req.PrincipalType, req.PrincipalID, req.RoleID, req.ResourceType, req.ResourceID)
	m.reply(w, http.StatusCreated, map[string]any{"result": b.json()})
}

// summaries lists one principal's bindings, optionally within one scope,
// ordered by ID so paging is stable.
func (m *roleBindingMockServer) summaries(resourceType, resourceID, principalType, principalID string) []map[string]any {
	var ids []string
	for id, b := range m.bindings {
		if b.PrincipalType != principalType || b.PrincipalID != principalID {
			continue
		}
		if resourceType != "" && (b.ResourceType != resourceType || b.ResourceID != resourceID) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, m.bindings[id].summaryJSON())
	}
	return out
}

func (m *roleBindingMockServer) serveRoles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ids := q["ids"]
	if len(ids) > 50 {
		m.fail(w, http.StatusBadRequest, "Ask for at most 50 ids at a time.")
		return
	}
	name := strings.ToLower(q.Get("name"))
	includeArchived := q.Get("include_archived") == "true"
	includeBuiltIn := q.Get("include_built_in") != "false"
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var keys []string
	for id, role := range m.roles {
		if len(want) > 0 && !want[id] {
			continue
		}
		if name != "" && !strings.Contains(strings.ToLower(role.Name), name) {
			continue
		}
		if role.Archived && !includeArchived {
			continue
		}
		if role.BuiltIn && !includeBuiltIn {
			continue
		}
		keys = append(keys, id)
	}
	sort.Strings(keys)
	items := make([]map[string]any, 0, len(keys))
	for _, id := range keys {
		items = append(items, m.roles[id].json(len(want) > 0))
	}
	m.page(w, r, items)
}
