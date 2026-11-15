package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// policyMockServer is a stateful fake of the role-binding policy API: generate,
// apply (with dry_run), GET /role_bindings/{id}, the roles probe, userinfo, and
// the project and cloud reads Read uses after a 403.
//
// Statuses and detail texts are the ones measured on a converted premerge org
// (2026-11-04), verbatim, including the roles[i].principals[j] prefix and a dry
// run's added entries carrying no id. Three are traced, not observed, because no
// test org can produce them: the frozen-binding 409 and the admin-group 409s.
//
// Semantics, as the backend's apply_policy implements them: bindings compare by
// fact (principal, role, scope), order-blind; a held fact keeps its id; a new
// grant gets a new id; per-element checks run first, then revocations in held
// order (oldest first), and the first refusal stops the call with nothing
// written. A role entry with no principals is a silent no-op.
type policyMockServer struct {
	*httptest.Server
	t *testing.T

	mu       sync.Mutex
	seq      int
	bindings []*policyMockBinding
	roles    map[string]bool
	groups   map[string]bool
	// scopes are the "<type>/<id>" scopes that exist.
	scopes map[string]bool
	// noIAM are existing scopes the caller does not manage IAM on.
	noIAM       map[string]bool
	flagOff     bool
	unconverted bool
	// adminGroup makes any apply that revokes a binding answer the admin
	// group 409, as revoking one of that group's bindings would.
	adminGroup bool
	// generateStatus, when non-zero, is the status every generate answers
	// with: a read-back failing after a successful apply.
	generateStatus int
	// getDenials is how many GETs of each rb_ id answer the propagation 403
	// before it reads, as a just-granted binding does for a few seconds
	// (measured); getCounts counts them per id.
	getDenials int
	getCounts  map[string]int
	// requests records every request as "METHOD path?query body".
	requests []string
}

type policyMockBinding struct {
	ID            string
	PrincipalType string
	PrincipalID   string
	RoleID        string
	ResourceType  string
	ResourceID    string
	Frozen        bool
	Origin        string
}

const policyMockCaller = "usr_policycaller00000000000000"

func newPolicyMockServer(t *testing.T) *policyMockServer {
	t.Helper()
	m := &policyMockServer{
		t:      t,
		roles:  map[string]bool{},
		groups: map[string]bool{},
		scopes: map[string]bool{},
		noIAM:  map[string]bool{},
	}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.Close)
	return m
}

func (m *policyMockServer) providerBlock() string {
	return fmt.Sprintf(`
provider "anyscale" {
  api_url = %q
  token   = "mock-token"
}
`, m.URL)
}

func (m *policyMockServer) with(f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f()
}

func (m *policyMockServer) addRole(id string)  { m.with(func() { m.roles[id] = true }) }
func (m *policyMockServer) addGroup(id string) { m.with(func() { m.groups[id] = true }) }
func (m *policyMockServer) addScope(scopeType, id string) {
	m.with(func() { m.scopes[scopeType+"/"+id] = true })
}
func (m *policyMockServer) deleteScope(scopeType, id string) {
	m.with(func() { delete(m.scopes, scopeType+"/"+id) })
}
func (m *policyMockServer) setNoIAM(scopeType, id string, on bool) {
	m.with(func() { m.noIAM[scopeType+"/"+id] = on })
}
func (m *policyMockServer) setFlagOff(on bool)     { m.with(func() { m.flagOff = on }) }
func (m *policyMockServer) setUnconverted(on bool) { m.with(func() { m.unconverted = on }) }
func (m *policyMockServer) setAdminGroup(on bool)  { m.with(func() { m.adminGroup = on }) }
func (m *policyMockServer) setGetDenials(n int)    { m.with(func() { m.getDenials = n }) }
func (m *policyMockServer) setGenerateStatus(status int) {
	m.with(func() { m.generateStatus = status })
}

// seed grants a binding outside Terraform and returns its id.
func (m *policyMockServer) seed(principalType, principalID, roleID, scopeType, scopeID string) string {
	var id string
	m.with(func() { id = m.insert(principalType, principalID, roleID, scopeType, scopeID, "imperative").ID })
	return id
}

// seedOrigin grants a binding outside Terraform with this origin ("" for
// none) and returns its id.
func (m *policyMockServer) seedOrigin(principalType, principalID, roleID, scopeType, scopeID, origin string) string {
	var id string
	m.with(func() { id = m.insert(principalType, principalID, roleID, scopeType, scopeID, origin).ID })
	return id
}

func (m *policyMockServer) seedFrozen(principalType, principalID, roleID, scopeType, scopeID string) string {
	var id string
	m.with(func() {
		b := m.insert(principalType, principalID, roleID, scopeType, scopeID, "imperative")
		b.Frozen = true
		id = b.ID
	})
	return id
}

func (m *policyMockServer) unfreezeAll() {
	m.with(func() {
		for _, b := range m.bindings {
			b.Frozen = false
		}
	})
}

// revoke deletes a binding outside Terraform.
func (m *policyMockServer) revoke(id string) {
	m.with(func() {
		for i, b := range m.bindings {
			if b.ID == id {
				m.bindings = append(m.bindings[:i], m.bindings[i+1:]...)
				return
			}
		}
		m.t.Fatalf("mock: revoke of unknown binding %s", id)
	})
}

// facts lists the scope's bindings as "role/principal_type/principal_id",
// sorted.
func (m *policyMockServer) facts(scopeType, scopeID string) []string {
	var out []string
	m.with(func() {
		for _, b := range m.bindings {
			if b.ResourceType == scopeType && b.ResourceID == scopeID {
				out = append(out, b.RoleID+"/"+b.PrincipalType+"/"+b.PrincipalID)
			}
		}
	})
	sort.Strings(out)
	return out
}

// takeRequests returns and clears the request log.
func (m *policyMockServer) takeRequests() []string {
	var out []string
	m.with(func() { out, m.requests = m.requests, nil })
	return out
}

// requestsMatching returns the logged requests that start with prefix,
// without clearing the log.
func (m *policyMockServer) requestsMatching(prefix string) []string {
	var out []string
	m.with(func() {
		for _, r := range m.requests {
			if strings.HasPrefix(r, prefix) {
				out = append(out, r)
			}
		}
	})
	return out
}

func (m *policyMockServer) insert(principalType, principalID, roleID, scopeType, scopeID, origin string) *policyMockBinding {
	m.seq++
	b := &policyMockBinding{
		ID: fmt.Sprintf("rb_%026d", m.seq), PrincipalType: principalType, PrincipalID: principalID,
		RoleID: roleID, ResourceType: scopeType, ResourceID: scopeID, Origin: origin,
	}
	m.bindings = append(m.bindings, b)
	return b
}

// json renders a binding; an empty Origin is omitted, as the API omits a
// binding's origin when it has none.
func (b *policyMockBinding) json() map[string]any {
	out := map[string]any{
		"id": b.ID, "principal_type": b.PrincipalType, "principal_id": b.PrincipalID,
		"role_id": b.RoleID, "resource_type": b.ResourceType, "resource_id": b.ResourceID,
		"created_by": policyMockCaller, "created_at": "2026-11-04T12:00:00.000000+00:00",
	}
	if b.Origin != "" {
		out["origin"] = b.Origin
	}
	return out
}

func (m *policyMockServer) reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func (m *policyMockServer) fail(w http.ResponseWriter, status int, detail string) {
	m.reply(w, status, map[string]any{"error": map[string]any{"detail": detail}})
}

func (m *policyMockServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		entry += "?" + r.URL.RawQuery
	}
	if len(body) > 0 {
		entry += " " + strings.TrimSpace(string(body))
	}
	m.requests = append(m.requests, entry)

	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == "/api/v2/userinfo" && r.Method == http.MethodGet:
		m.reply(w, http.StatusOK, map[string]any{"result": map[string]any{
			"id": policyMockCaller, "email": "caller@example.com", "name": "Caller", "username": "caller",
			"organization_ids": []string{"org_policymock000000000000000"},
			"organizations":    []any{map[string]any{"id": "org_policymock000000000000000", "name": "mock"}},
		}})
		return
	case strings.HasPrefix(path, "/api/v2/projects/") && r.Method == http.MethodGet:
		m.serveScopeRead(w, "project", strings.TrimPrefix(path, "/api/v2/projects/"))
		return
	case strings.HasPrefix(path, "/api/v2/clouds/") && r.Method == http.MethodGet:
		m.serveScopeRead(w, "cloud", strings.TrimPrefix(path, "/api/v2/clouds/"))
		return
	}

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
		m.reply(w, http.StatusOK, map[string]any{
			"results":  []any{map[string]any{"id": "rol_probe", "name": "probe", "built_in": true}},
			"metadata": map[string]any{"total": 1, "next_paging_token": nil},
		})
		return
	}

	rest := strings.Split(strings.TrimPrefix(path, rbBase+"/"), "/")
	switch {
	case len(rest) == 4 && rest[2] == "policy" && rest[3] == "generate" && r.Method == http.MethodGet:
		if m.refuseIAM(w, rest[0], rest[1]) {
			return
		}
		if m.generateStatus != 0 {
			m.fail(w, m.generateStatus, "mock generate failure")
			return
		}
		m.serveGenerate(w, rest[0], rest[1])
	case len(rest) == 3 && rest[2] == "policy" && r.Method == http.MethodPut:
		// The conversion refusal is a router dependency, so it answers first.
		if m.unconverted {
			m.fail(w, http.StatusConflict, roleBindingMockNotConvertedDetail)
			return
		}
		if m.refuseIAM(w, rest[0], rest[1]) {
			return
		}
		m.serveApply(w, rest[0], rest[1], body, r.URL.Query().Get("dry_run") == "true")
	case len(rest) == 1 && strings.HasPrefix(rest[0], "rb_") && r.Method == http.MethodGet:
		if m.getCounts == nil {
			m.getCounts = map[string]int{}
		}
		m.getCounts[rest[0]]++
		if m.getCounts[rest[0]] <= m.getDenials {
			m.fail(w, http.StatusForbidden, fmt.Sprintf("You may not read role binding '%s'.", rest[0]))
			return
		}
		for _, b := range m.bindings {
			if b.ID == rest[0] {
				m.reply(w, http.StatusOK, map[string]any{"result": b.json()})
				return
			}
		}
		m.fail(w, http.StatusForbidden, fmt.Sprintf("You may not read role binding '%s'.", rest[0]))
	default:
		m.t.Errorf("mock: unhandled role_bindings route %s %s", r.Method, r.URL.Path)
		m.fail(w, http.StatusNotFound, "unhandled route")
	}
}

func (m *policyMockServer) serveScopeRead(w http.ResponseWriter, scopeType, id string) {
	if !m.scopes[scopeType+"/"+id] {
		m.fail(w, http.StatusForbidden, "Permission denied")
		return
	}
	m.reply(w, http.StatusOK, map[string]any{"result": map[string]any{"id": id}})
}

// refuseIAM answers the manage-IAM 403, which a deleted or nonexistent scope
// gets too.
func (m *policyMockServer) refuseIAM(w http.ResponseWriter, scopeType, id string) bool {
	key := scopeType + "/" + id
	if m.scopes[key] && !m.noIAM[key] {
		return false
	}
	m.fail(w, http.StatusForbidden, fmt.Sprintf("You do not manage IAM on %s '%s'.", scopeType, id))
	return true
}

func (m *policyMockServer) serveGenerate(w http.ResponseWriter, scopeType, scopeID string) {
	var order []string
	byRole := map[string][]any{}
	for _, b := range m.bindings {
		if b.ResourceType != scopeType || b.ResourceID != scopeID {
			continue
		}
		if _, ok := byRole[b.RoleID]; !ok {
			order = append(order, b.RoleID)
		}
		byRole[b.RoleID] = append(byRole[b.RoleID], map[string]any{"principal_type": b.PrincipalType, "principal_id": b.PrincipalID})
	}
	roles := []any{}
	for _, id := range order {
		roles = append(roles, map[string]any{"role_id": id, "principals": byRole[id]})
	}
	m.reply(w, http.StatusOK, map[string]any{"result": map[string]any{"roles": roles}})
}

type policyMockKey struct{ PrincipalType, PrincipalID, RoleID string }

func (m *policyMockServer) serveApply(w http.ResponseWriter, scopeType, scopeID string, body []byte, dryRun bool) {
	var req struct {
		Roles *[]struct {
			RoleID     string `json:"role_id"`
			Principals []struct {
				PrincipalType string `json:"principal_type"`
				PrincipalID   string `json:"principal_id"`
			} `json:"principals"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Roles == nil {
		m.fail(w, http.StatusUnprocessableEntity, "roles: Field required")
		return
	}

	stated := map[policyMockKey]bool{}
	held := map[policyMockKey]*policyMockBinding{}
	for _, b := range m.bindings {
		if b.ResourceType == scopeType && b.ResourceID == scopeID {
			held[policyMockKey{b.PrincipalType, b.PrincipalID, b.RoleID}] = b
		}
	}
	var granted []policyMockKey
	for i, role := range *req.Roles {
		for j, p := range role.Principals {
			at := fmt.Sprintf("roles[%d].principals[%d]: ", i, j)
			if !m.roles[role.RoleID] {
				m.fail(w, http.StatusNotFound, at+"Role not found.")
				return
			}
			if p.PrincipalType == "user_group" && !m.groups[p.PrincipalID] {
				m.fail(w, http.StatusNotFound, fmt.Sprintf("%sUser group '%s' not found.", at, p.PrincipalID))
				return
			}
			key := policyMockKey{p.PrincipalType, p.PrincipalID, role.RoleID}
			if _, ok := held[key]; !ok {
				if p.PrincipalType == "user" && p.PrincipalID == policyMockCaller {
					m.fail(w, http.StatusForbidden, at+"You cannot assign a role to yourself directly. Assign it to a user group you belong to.")
					return
				}
				granted = append(granted, key)
			}
			stated[key] = true
		}
	}
	var revoked []*policyMockBinding
	for _, b := range m.bindings {
		if b.ResourceType == scopeType && b.ResourceID == scopeID && !stated[policyMockKey{b.PrincipalType, b.PrincipalID, b.RoleID}] {
			revoked = append(revoked, b)
		}
	}
	for _, b := range revoked {
		if b.Frozen {
			m.fail(w, http.StatusConflict, fmt.Sprintf("This policy would revoke the frozen role binding '%s'. Name it in the policy to keep it.", b.ID))
			return
		}
		if b.PrincipalType == "user" && b.PrincipalID == policyMockCaller {
			m.fail(w, http.StatusForbidden, fmt.Sprintf("This policy would revoke your own role binding '%s'. Name it in the policy to keep it.", b.ID))
			return
		}
	}
	if m.adminGroup && len(revoked) > 0 {
		m.fail(w, http.StatusConflict, roleBindingMockAdminGroupRemovalDetail)
		return
	}

	removed := []any{}
	for _, b := range revoked {
		removed = append(removed, b.json())
	}
	added := []any{}
	if dryRun {
		for _, k := range granted {
			added = append(added, map[string]any{
				"principal_type": k.PrincipalType, "principal_id": k.PrincipalID, "role_id": k.RoleID,
				"resource_type": scopeType, "resource_id": scopeID,
			})
		}
		m.reply(w, http.StatusOK, map[string]any{"result": map[string]any{"added": added, "removed": removed}})
		return
	}
	gone := map[*policyMockBinding]bool{}
	for _, b := range revoked {
		gone[b] = true
	}
	kept := m.bindings[:0]
	for _, b := range m.bindings {
		if !gone[b] {
			kept = append(kept, b)
		}
	}
	m.bindings = kept
	for _, k := range granted {
		added = append(added, m.insert(k.PrincipalType, k.PrincipalID, k.RoleID, scopeType, scopeID, "declarative").json())
	}
	m.reply(w, http.StatusOK, map[string]any{"result": map[string]any{"added": added, "removed": removed}})
}
