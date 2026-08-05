package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// These tests cover the reconcile, which is the only code in this provider that
// removes a real person's access to real infrastructure. Every case here is one
// where the wrong behavior is silent: access left behind that the configuration
// says is gone, access removed that nobody asked to remove, or an operator
// locked out of the cloud they are managing.
//
// The mock records every request in order, because ORDER is part of the
// contract - grants must precede revokes - and an assertion on the final state
// alone cannot see it.

// cloudAccessReconcileMock is a whole backend for the reconcile: the identity
// endpoints it reads, the cloud settings it preflights, and the three write
// endpoints it calls.
type cloudAccessReconcileMock struct {
	callerUserID string
	callerEmail  string

	// members is the cloud's current collaborator list, keyed by email.
	members map[string]cloudAccessMockMember
	// orgMembers maps email to (identity_id, user_id) for the organization lookup.
	orgMembers map[string]cloudAccessMockIdentity

	autoAddUser bool
	// cloudGetStatus, when non-zero, is returned by the cloud GET instead of a
	// body - used to prove the auto_add_user preflight fails closed.
	cloudGetStatus int
	// rolesReadStatus, when non-zero, is returned by the roles GET instead of a
	// body - used to prove a 501 (the read-side feature flag) on that endpoint
	// surfaces the two-flag diagnostic rather than a generic read-failure one.
	rolesReadStatus int
	// rolesWriteStatus, when non-zero, is returned by every roles PUT instead of
	// writing anything - used to prove a 501 (the write-side feature flag) on
	// that endpoint surfaces the two-flag diagnostic rather than a generic
	// set-role-failure one.
	rolesWriteStatus int

	// groupPolicyBindingCount, when > 0, makes GET /api/v2/policy/cloud/{id}
	// return that many bindings (Present). groupPolicyStatusOverride, when
	// non-zero, makes it return that raw status instead (e.g. 403, to prove
	// Undetermined). groupPolicyRegisterEmpty forces the route to register and
	// return a 200 with zero bindings (the OTHER Absent shape) even though that
	// is indistinguishable, by count alone, from "don't register." With none of
	// the three set, the route is left unregistered and ServeMux's own 404
	// fires - proving the common Absent shape needs no special mock support at
	// all, which is the ordinary case in production too.
	groupPolicyBindingCount     int
	groupPolicyStatusOverride   int
	groupPolicyRegisterEmpty    bool
	groupPolicyRequestsRecorded int

	// failRoleWriteFor / failRevokeFor are emails whose write fails.
	failRoleWriteFor map[string]bool
	failRevokeFor    map[string]string

	// roleReadOverrideDenyRoles, keyed by user_id, makes the roles GET report a
	// DIFFERENT deny_roles value than what m.members actually holds - standing
	// in for a stale/eventually-consistent read-back rather than a write bug.
	// Used only by the read-back-must-not-touch-member regression guard: if
	// applyCloudAccess ever read this response back into state.Member, the
	// override value (not the plan's) would show up, and the guard would fail.
	roleReadOverrideDenyRoles map[string][]string

	// calls is every mutating request, in order, as "VERB path".
	calls []string
	// roleWriteBodies is the RAW body of each roles PUT. Kept raw because the
	// distinction that matters - deny_roles sent as [] versus as null - vanishes
	// once decoded into a Go slice, so a decoded assertion cannot see it.
	roleWriteBodies []string
}

type cloudAccessMockMember struct {
	IdentityID string
	UserID     string
	BaseRoles  []string
	DenyRoles  []string
}

type cloudAccessMockIdentity struct {
	IdentityID string
	UserID     string
	// PermissionLevel is the organization-scope permission_level reported by
	// GET /api/v2/organization_collaborators - "owner" or "collaborator".
	// Defaults to "collaborator" when unset, so only a test that cares about
	// J.22's org-admin guard needs to set it.
	PermissionLevel string
}

func (m *cloudAccessReconcileMock) record(r *http.Request) {
	m.calls = append(m.calls, r.Method+" "+r.URL.Path)
}

func (m *cloudAccessReconcileMock) handler(t *testing.T, cloudID string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()

	writeJSON := func(w http.ResponseWriter, payload interface{}) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Fatalf("encoding the mock response failed: %v", err)
		}
	}
	emptyMeta := map[string]interface{}{"next_paging_token": nil}

	mux.HandleFunc("/api/v2/userinfo", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"result": map[string]interface{}{
			"id":    m.callerUserID,
			"email": m.callerEmail,
			"name":  "Caller",
			"organizations": []map[string]interface{}{
				{"id": "org_1", "name": "Test", "public_identifier": "test", "default_cloud_id": nil},
			},
		}})
	})

	mux.HandleFunc("/api/v2/organization_collaborators", func(w http.ResponseWriter, r *http.Request) {
		results := []map[string]interface{}{}
		for email, id := range m.orgMembers {
			userID := id.UserID
			permissionLevel := id.PermissionLevel
			if permissionLevel == "" {
				permissionLevel = "collaborator"
			}
			results = append(results, map[string]interface{}{
				"id": id.IdentityID, "email": email, "user_id": userID, "name": "Test",
				"permission_level": permissionLevel,
			})
		}
		writeJSON(w, map[string]interface{}{"results": results, "metadata": emptyMeta})
	})

	// The singular GET, keyed by user_id - what cloudAccessLikelyOrgAdmin (J.22)
	// actually reads base_role from. Kept driven by the same PermissionLevel
	// field as the list handler above: for this mock's purposes, one signal per
	// test identity is enough to say "this person is an org admin", even though
	// the two endpoints have different real-world staleness properties.
	mux.HandleFunc("/api/v2/organization_collaborators/", func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimPrefix(r.URL.Path, "/api/v2/organization_collaborators/")
		for _, id := range m.orgMembers {
			if id.UserID != userID {
				continue
			}
			baseRole := id.PermissionLevel
			if baseRole == "" {
				baseRole = "collaborator"
			}
			writeJSON(w, map[string]interface{}{"result": map[string]interface{}{
				"base_role": baseRole, "additional_roles": []string{},
			}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	mux.HandleFunc(fmt.Sprintf("/api/v2/clouds/%s/collaborators/users/search", cloudID),
		func(w http.ResponseWriter, r *http.Request) {
			results := []map[string]interface{}{}
			for email, mem := range m.members {
				results = append(results, map[string]interface{}{
					"id":               mem.IdentityID,
					"value":            map[string]interface{}{"id": mem.UserID, "name": "Test", "email": email},
					"permission_level": "readonly",
				})
			}
			writeJSON(w, map[string]interface{}{"results": results, "metadata": emptyMeta})
		})

	mux.HandleFunc(fmt.Sprintf("/api/v2/clouds/%s/collaborators/roles", cloudID),
		func(w http.ResponseWriter, r *http.Request) {
			if m.rolesReadStatus != 0 {
				w.WriteHeader(m.rolesReadStatus)
				writeJSON(w, map[string]interface{}{"error": map[string]interface{}{
					"detail": "the cloud roles feature is not enabled for this organization",
				}})
				return
			}
			results := []map[string]interface{}{}
			for _, mem := range m.members {
				if len(mem.BaseRoles) == 0 {
					continue
				}
				denyRoles := mem.DenyRoles
				if override, ok := m.roleReadOverrideDenyRoles[mem.UserID]; ok {
					denyRoles = override
				}
				results = append(results, map[string]interface{}{
					"user_id": mem.UserID, "base_roles": mem.BaseRoles, "deny_roles": denyRoles,
				})
			}
			writeJSON(w, map[string]interface{}{"results": results, "metadata": emptyMeta})
		})

	mux.HandleFunc(fmt.Sprintf("/api/v2/clouds/%s/collaborators/users", cloudID),
		func(w http.ResponseWriter, r *http.Request) {
			m.record(r)
			var body CreateCloudCollaboratorRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("the bootstrap POST sent an undecodable body: %v", err)
			}
			if _, exists := m.members[body.Email]; exists {
				// The real 409: nothing is written, which is what makes running the
				// bootstrap unconditionally safe for an existing member.
				w.WriteHeader(http.StatusConflict)
				writeJSON(w, map[string]interface{}{"error": map[string]interface{}{
					"detail": fmt.Sprintf("User with email %s already has permissions for this cloud.", body.Email),
				}})
				return
			}
			id := m.orgMembers[body.Email]
			m.members[body.Email] = cloudAccessMockMember{IdentityID: id.IdentityID, UserID: id.UserID}
			w.WriteHeader(http.StatusNoContent)
		})

	// The cloud GET, for the auto_add_user preflight. Registered on the exact
	// path so it cannot swallow the subtree patterns above.
	mux.HandleFunc(fmt.Sprintf("/api/v2/clouds/%s", cloudID), func(w http.ResponseWriter, r *http.Request) {
		if m.cloudGetStatus != 0 {
			w.WriteHeader(m.cloudGetStatus)
			return
		}
		writeJSON(w, map[string]interface{}{"result": map[string]interface{}{
			"id": cloudID, "name": "test", "provider": "AWS", "region": "us-east-1",
			"auto_add_user": m.autoAddUser,
		}})
	})

	// The roles PUT and the collaborator DELETE share a prefix with the reads
	// above, so they are dispatched by hand from one catch-all rather than by
	// pattern - ServeMux would otherwise pick the longest static prefix and never
	// reach them.
	mux.HandleFunc(fmt.Sprintf("/api/v2/clouds/%s/collaborators/", cloudID),
		func(w http.ResponseWriter, r *http.Request) {
			m.record(r)
			trailing := strings.TrimPrefix(r.URL.Path, fmt.Sprintf("/api/v2/clouds/%s/collaborators/", cloudID))

			if r.Method == http.MethodPut && strings.HasSuffix(trailing, "/roles") {
				if m.rolesWriteStatus != 0 {
					w.WriteHeader(m.rolesWriteStatus)
					writeJSON(w, map[string]interface{}{"error": map[string]interface{}{
						"detail": "the cloud roles feature is not enabled for this organization",
					}})
					return
				}
				userID := strings.TrimSuffix(strings.TrimPrefix(trailing, "users/"), "/roles")
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("reading the roles PUT body failed: %v", err)
				}
				m.roleWriteBodies = append(m.roleWriteBodies, string(raw))
				var body SetCloudRolesRequest
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatalf("the roles PUT sent an undecodable body: %v", err)
				}
				for email, mem := range m.members {
					if mem.UserID != userID {
						continue
					}
					if m.failRoleWriteFor[email] {
						w.WriteHeader(http.StatusInternalServerError)
						writeJSON(w, map[string]interface{}{"error": map[string]interface{}{"detail": "role write exploded"}})
						return
					}
					mem.BaseRoles = []string{body.BaseRole}
					mem.DenyRoles = body.DenyRoles
					m.members[email] = mem
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.WriteHeader(http.StatusNotFound)
				return
			}

			if r.Method == http.MethodDelete {
				for email, mem := range m.members {
					if mem.IdentityID != trailing {
						continue
					}
					if reason, fails := m.failRevokeFor[email]; fails {
						w.WriteHeader(http.StatusConflict)
						writeJSON(w, map[string]interface{}{"error": map[string]interface{}{"detail": reason}})
						return
					}
					delete(m.members, email)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.WriteHeader(http.StatusNotFound)
				return
			}

			t.Fatalf("unexpected request to the collaborators subtree: %s %s", r.Method, r.URL.Path)
		})

	// Only registered when a test opts in - see the fields' own doc comment for
	// why an unregistered route (the default) is itself the Absent case, not a
	// mock gap needing a stub.
	if m.groupPolicyBindingCount > 0 || m.groupPolicyStatusOverride != 0 || m.groupPolicyRegisterEmpty {
		mux.HandleFunc(fmt.Sprintf("/api/v2/policy/cloud/%s", cloudID), func(w http.ResponseWriter, r *http.Request) {
			m.groupPolicyRequestsRecorded++
			if m.groupPolicyStatusOverride != 0 {
				w.WriteHeader(m.groupPolicyStatusOverride)
				writeJSON(w, map[string]interface{}{"error": map[string]interface{}{"detail": "forbidden"}})
				return
			}
			bindings := make([]map[string]interface{}, m.groupPolicyBindingCount)
			for i := range bindings {
				bindings[i] = map[string]interface{}{"role_name": "write", "principals": []string{fmt.Sprintf("ug_%d", i)}}
			}
			writeJSON(w, map[string]interface{}{"result": map[string]interface{}{"bindings": bindings, "sync_status": "success"}})
		})
	}

	return mux
}

func (m *cloudAccessReconcileMock) memberEmails() []string {
	out := make([]string, 0, len(m.members))
	for email := range m.members {
		out = append(out, email)
	}
	return out
}

// cloudAccessApplyOptions is the option set an apply uses. Destroy differs on
// RefuseWhenAutoAddUserEnabled and has its own test below.
func cloudAccessApplyOptions() cloudAccessReconcileOptions {
	return cloudAccessReconcileOptions{
		RevokeUndeclared: true, RefuseWhenAutoAddUserEnabled: true, RefuseWhenGroupPolicyBound: true,
	}
}

func cloudAccessDesired(entries ...cloudAccessDesiredMember) map[string]cloudAccessDesiredMember {
	out := make(map[string]cloudAccessDesiredMember, len(entries))
	for _, e := range entries {
		out[cloudAccessFoldEmail(e.Email)] = e
	}
	return out
}

// TestReconcileCloudAccess_GrantsBeforeRevokes covers the ordering property. A
// configuration that replaces one owner with another must never pass through a
// moment where the cloud has no owner, and an apply that dies partway through
// should leave too much access rather than too little: too much shows up in the
// next plan, too little is an outage for a real person.
func TestReconcileCloudAccess_GrantsBeforeRevokes(t *testing.T) {
	const cloudID = "cld_order"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{
			"outgoing@example.com": {IdentityID: "idn_out", UserID: "usr_out", BaseRoles: []string{"owner"}},
		},
		orgMembers: map[string]cloudAccessMockIdentity{
			"incoming@example.com": {IdentityID: "idn_in", UserID: "usr_in"},
			"outgoing@example.com": {IdentityID: "idn_out", UserID: "usr_out"},
		},
	}

	result, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
		cloudAccessDesired(cloudAccessDesiredMember{Email: "incoming@example.com", BaseRole: "owner"}), cloudAccessApplyOptions())
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if len(result.Unmanaged) != 0 {
		t.Fatalf("nothing should have failed: %+v", result.Unmanaged)
	}

	firstDelete, lastGrant := -1, -1
	for i, call := range mock.calls {
		if strings.HasPrefix(call, "DELETE ") && firstDelete == -1 {
			firstDelete = i
		}
		if strings.HasPrefix(call, "POST ") || strings.HasPrefix(call, "PUT ") {
			lastGrant = i
		}
	}
	if firstDelete == -1 || lastGrant == -1 {
		t.Fatalf("expected both a grant and a revoke; calls were %v", mock.calls)
	}
	if firstDelete < lastGrant {
		t.Errorf("a revoke was issued before the last grant (calls: %v). Grants must complete first, so an apply that dies partway leaves too much access rather than too little", mock.calls)
	}

	if _, ok := mock.members["incoming@example.com"]; !ok {
		t.Errorf("the declared member was not granted access: %v", mock.memberEmails())
	}
	if _, ok := mock.members["outgoing@example.com"]; ok {
		t.Errorf("the undeclared member was not revoked: %v", mock.memberEmails())
	}
}

// TestReconcileCloudAccess_NeverRevokesTheCaller covers the exclusion that keeps
// an operator from locking themselves out. The token running Terraform is
// usually a collaborator on the cloud it manages and frequently its owner, so
// without this the FIRST apply of an authoritative resource removes the
// operator's own access.
func TestReconcileCloudAccess_NeverRevokesTheCaller(t *testing.T) {
	const cloudID = "cld_self"

	for _, tc := range []struct {
		name         string
		callerUserID string
		callerEmail  string
	}{
		{
			name:         "matched by user id",
			callerUserID: "usr_caller",
			callerEmail:  "someone-else@example.com",
		},
		{
			// The user id can be absent or differ by identity type, so the email is
			// checked too - and case-insensitively, since Anyscale treats email
			// identity as case-insensitive.
			name:         "matched by email in a different case",
			callerUserID: "",
			callerEmail:  "Operator@Example.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &cloudAccessReconcileMock{
				callerUserID: tc.callerUserID, callerEmail: tc.callerEmail,
				members: map[string]cloudAccessMockMember{
					"operator@example.com": {IdentityID: "idn_caller", UserID: "usr_caller", BaseRoles: []string{"owner"}},
				},
				orgMembers: map[string]cloudAccessMockIdentity{},
			}

			// An empty desired set: every existing member is undeclared, so the caller
			// is only spared by the exclusion.
			_, diags := reconcileCloudAccess(context.Background(),
				cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
				map[string]cloudAccessDesiredMember{}, cloudAccessApplyOptions())
			if diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}

			if _, ok := mock.members["operator@example.com"]; !ok {
				t.Fatalf("the reconcile revoked the calling identity's own access to the cloud it is managing")
			}
			for _, call := range mock.calls {
				if strings.HasPrefix(call, "DELETE") {
					t.Errorf("a revoke was attempted at all: %v", mock.calls)
				}
			}
		})
	}
}

// TestReconcileCloudAccess_AutoAddUserBlocksRevokes covers the preflight. While
// auto_add_user is on, a backend reconciler grants every organization member
// access to the cloud and refuses collaborator removal, so Terraform cannot be
// authoritative over the member list at all - that is impossible rather than
// degraded, and the apply must say so instead of attempting revokes that fail
// one at a time.
func TestReconcileCloudAccess_AutoAddUserBlocksRevokes(t *testing.T) {
	const cloudID = "cld_autoadd"

	newMock := func() *cloudAccessReconcileMock {
		return &cloudAccessReconcileMock{
			callerUserID: "usr_caller", callerEmail: "operator@example.com",
			autoAddUser: true,
			members: map[string]cloudAccessMockMember{
				"undeclared@example.com": {IdentityID: "idn_u", UserID: "usr_u", BaseRoles: []string{"writer"}},
			},
			orgMembers: map[string]cloudAccessMockIdentity{},
		}
	}

	t.Run("a reconcile that would revoke is refused with no writes", func(t *testing.T) {
		mock := newMock()
		_, diags := reconcileCloudAccess(context.Background(),
			cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
			map[string]cloudAccessDesiredMember{}, cloudAccessApplyOptions())

		if !diags.HasError() {
			t.Fatal("expected an error: with auto_add_user on, the member list cannot be authoritative and every revoke would fail")
		}
		if len(mock.calls) != 0 {
			t.Errorf("the refusal came after writes had already been made: %v - it must be a preflight so a refused apply changes nothing", mock.calls)
		}

		// The message has to explain itself, not just refuse. An operator cannot
		// derive "turn off a setting on a different resource" from a refusal, and the
		// revoke count is their whole organization, which looks like a provider bug
		// unless the message says why.
		d := cloudAccessFindError(diags, "Cloud Has auto_add_user Enabled")
		if d == nil {
			t.Fatalf("expected the auto_add_user error specifically: %v", diags)
		}
		for _, want := range []string{"auto_add_user = false", "retroactively", "cannot converge"} {
			if !strings.Contains(d.Detail(), want) {
				t.Errorf("the refusal detail does not mention %q, so the operator has no way to derive the remedy or to understand why the count is their whole organization: %s", want, d.Detail())
			}
		}
	})

	t.Run("a reconcile with nothing to revoke is allowed", func(t *testing.T) {
		// The rule is deliberately the narrowest one that is correct: the flag blocks
		// REMOVAL, so an apply that removes nobody is unaffected. Refusing on the
		// flag alone would block a legitimate grant for no safety benefit.
		mock := newMock()
		mock.orgMembers["undeclared@example.com"] = cloudAccessMockIdentity{IdentityID: "idn_u", UserID: "usr_u"}

		_, diags := reconcileCloudAccess(context.Background(),
			cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
			cloudAccessDesired(cloudAccessDesiredMember{Email: "undeclared@example.com", BaseRole: "writer"}), cloudAccessApplyOptions())
		if diags.HasError() {
			t.Fatalf("unexpected error: %v", diags)
		}
	})

	t.Run("a preflight that cannot answer fails closed", func(t *testing.T) {
		// A guard that treats its own failure as "probably fine" is not a guard.
		mock := newMock()
		mock.cloudGetStatus = http.StatusInternalServerError

		_, diags := reconcileCloudAccess(context.Background(),
			cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
			map[string]cloudAccessDesiredMember{}, cloudAccessApplyOptions())
		if !diags.HasError() {
			t.Fatal("expected an error: the auto_add_user check failed, so the reconcile does not know whether revoking is possible and must not proceed")
		}
		for _, call := range mock.calls {
			if strings.HasPrefix(call, "DELETE") {
				t.Errorf("a revoke was attempted after the guard failed to answer: %v", mock.calls)
			}
		}
	})
}

// TestReconcileCloudAccess_FailedRevokeConvergesIntoUnmanagedGrants covers the
// exception channel. A single unrevokable member must not block every other
// change to this cloud's members indefinitely, so the apply converges and
// records the failure - but silently converging is exactly the danger, which is
// why there is both an attribute to alert on and a warning.
func TestReconcileCloudAccess_FailedRevokeConvergesIntoUnmanagedGrants(t *testing.T) {
	const cloudID = "cld_stuck"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{
			"stuck@example.com":     {IdentityID: "idn_stuck", UserID: "usr_stuck", BaseRoles: []string{"writer"}},
			"removable@example.com": {IdentityID: "idn_ok", UserID: "usr_ok", BaseRoles: []string{"writer"}},
		},
		orgMembers: map[string]cloudAccessMockIdentity{},
		// A NEUTRAL failure detail on purpose. The auto_add_user 409's real envelope
		// has not been captured, and a fixture invented for a response nobody has
		// seen is what lets a broken mapping pass green. What is under test here is
		// converge-and-record, which does not depend on that envelope's wording;
		// the auto_add_user-specific message stays untested until the capture lands.
		failRevokeFor: map[string]string{"stuck@example.com": "revoke refused by the backend"},
	}

	result, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
		map[string]cloudAccessDesiredMember{}, cloudAccessApplyOptions())
	if diags.HasError() {
		t.Fatalf("a failed revoke must not fail the apply: %v", diags)
	}
	if diags.WarningsCount() == 0 {
		t.Error("a failed revoke converged with no warning at all; the attribute is the alarm but the apply output should still say something happened")
	}

	if len(result.Unmanaged) != 1 {
		t.Fatalf("got %d unmanaged grants, want 1: %+v", len(result.Unmanaged), result.Unmanaged)
	}
	got := result.Unmanaged[0]
	if got.Email.ValueString() != "stuck@example.com" {
		t.Errorf("the unmanaged grant names %q, want stuck@example.com", got.Email.ValueString())
	}
	if !got.ProjectID.IsNull() {
		t.Errorf("project_id is %q, want null: this is the cloud-level grant, not a grant on a project", got.ProjectID.ValueString())
	}
	if !strings.Contains(got.Reason.ValueString(), "revoke refused by the backend") {
		t.Errorf("the reason does not carry the API's own detail: %q - a reader has nothing else to go on", got.Reason.ValueString())
	}

	// The other member was still revoked: one stuck member must not stop the rest.
	if _, ok := mock.members["removable@example.com"]; ok {
		t.Errorf("a revokable member was left behind because another one failed: %v", mock.memberEmails())
	}
}

// TestReconcileCloudAccess_FailedGrantSuppressesRevokesNotRecords covers AC-35:
// a grant failure records into Ungranted and warns rather than erroring - the
// error would taint the Create/Update, and the next apply would then destroy
// and recreate the resource, revoking every member this apply DID manage to
// grant. What a grant failure DOES suppress is the destructive half of the
// SAME apply: the cloud-scope revoke phase, whose skipped entries must still
// surface, distinguishably, in Unmanaged rather than silently vanishing.
func TestReconcileCloudAccess_FailedGrantSuppressesRevokesNotRecords(t *testing.T) {
	const cloudID = "cld_grantfail"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{
			"undeclared@example.com": {IdentityID: "idn_u", UserID: "usr_u", BaseRoles: []string{"writer"}},
		},
		orgMembers: map[string]cloudAccessMockIdentity{
			"incoming@example.com": {IdentityID: "idn_in", UserID: "usr_in"},
		},
		failRoleWriteFor: map[string]bool{"incoming@example.com": true},
	}

	result, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
		cloudAccessDesired(cloudAccessDesiredMember{Email: "incoming@example.com", BaseRole: "owner"}), cloudAccessApplyOptions())

	// NOT an error: erroring here after any other member's grant had already
	// succeeded would taint the resource and schedule a destroy-then-recreate
	// that revokes everyone this apply DID manage to grant. The failure is
	// recorded and warned about instead.
	if diags.HasError() {
		t.Fatalf("a failed grant must not error - it must record into Ungranted and warn, or the resource taints: %v", diags)
	}
	if len(result.Ungranted) != 1 || result.Ungranted[0].Email.ValueString() != "incoming@example.com" {
		t.Fatalf("expected incoming@example.com in Ungranted, got: %+v", result.Ungranted)
	}

	if _, ok := mock.members["undeclared@example.com"]; !ok {
		t.Errorf("an undeclared member was revoked even though a grant failed: %v - the destructive half of the apply must be suppressed on any grant failure", mock.memberEmails())
	}
	// The skipped revoke must still be VISIBLE, not merely absent, so an
	// operator reading only Ungranted does not conclude the rest of the apply
	// completed.
	foundSkip := false
	for _, u := range result.Unmanaged {
		if u.Email.ValueString() == "undeclared@example.com" && strings.Contains(u.Reason.ValueString(), "skipped") {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Errorf("expected undeclared@example.com recorded in Unmanaged with a skipped-because-a-grant-failed reason: %+v", result.Unmanaged)
	}

	// Membership created by the failed grant is rolled back, so no unintended
	// bridge-level access is left behind.
	if _, ok := mock.members["incoming@example.com"]; ok {
		t.Errorf("the bootstrap membership created moments before the failed role write was not rolled back: %v", mock.memberEmails())
	}
}

// TestReconcileCloudAccess_GrantLoopContinuesPastAFailure covers the other
// half of AC-35: a grant failure must not stop the loop from attempting every
// OTHER declared member. Grants are additive and per-member independent, so
// skipping "b" because "a" failed would silently fail to attempt access the
// configuration asked for - forbidden by the same rule that requires revokes
// to keep going past one unrevokable member.
func TestReconcileCloudAccess_GrantLoopContinuesPastAFailure(t *testing.T) {
	const cloudID = "cld_grantfail_continue"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{},
		orgMembers: map[string]cloudAccessMockIdentity{
			"alice@example.com": {IdentityID: "idn_a", UserID: "usr_a"},
			"bob@example.com":   {IdentityID: "idn_b", UserID: "usr_b"},
		},
		failRoleWriteFor: map[string]bool{"alice@example.com": true},
	}

	result, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
		cloudAccessDesired(
			cloudAccessDesiredMember{Email: "alice@example.com", BaseRole: "writer"},
			cloudAccessDesiredMember{Email: "bob@example.com", BaseRole: "writer"},
		),
		cloudAccessApplyOptions())

	if diags.HasError() {
		t.Fatalf("a grant failure must not error: %v", diags)
	}
	if len(result.Ungranted) != 1 || result.Ungranted[0].Email.ValueString() != "alice@example.com" {
		t.Fatalf("expected only alice@example.com in Ungranted, got: %+v", result.Ungranted)
	}
	if mem, ok := mock.members["bob@example.com"]; !ok || len(mem.BaseRoles) == 0 || mem.BaseRoles[0] != "writer" {
		t.Errorf("bob's grant was not attempted after alice's failed: %v", mock.memberEmails())
	}
}

// TestReconcileCloudAccess_ProjectPassReadFailureConvergesNotErrors covers the
// gap this same round found and fixed: a read failure inside the project
// pass (most plausibly J.13's own timeout firing after cloud-scope grants
// already succeeded) must NOT error - it must record the shortfall via
// cloudAccessUnplannedProjectShortfall and converge, exactly like a grant
// failure does. The mock here has no project-scope routes at all, so the
// project-roles read hits a genuine 404 - proving the fallback fires on a
// real read failure, not a synthetic one.
func TestReconcileCloudAccess_ProjectPassReadFailureConvergesNotErrors(t *testing.T) {
	const cloudID = "cld_projectreadfail"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{},
		orgMembers: map[string]cloudAccessMockIdentity{
			"alice@example.com": {IdentityID: "idn_a", UserID: "usr_a"},
		},
	}

	result, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
		cloudAccessDesired(cloudAccessDesiredMember{
			Email: "alice@example.com", BaseRole: "writer", Projects: map[string]string{"prj_1": "write"},
		}),
		cloudAccessApplyOptions())

	if diags.HasError() {
		t.Fatalf("a project-pass read failure must not error, or a mid-reconcile timeout would taint the resource: %v", diags)
	}
	if len(result.Ungranted) != 1 || result.Ungranted[0].ProjectID.ValueString() != "prj_1" {
		t.Fatalf("expected the project grant recorded as ungranted, got: %+v", result.Ungranted)
	}
	// Alice's CLOUD-scope grant must still have succeeded - the project pass
	// read failure must not roll that back or prevent it.
	if mem, ok := mock.members["alice@example.com"]; !ok || len(mem.BaseRoles) == 0 || mem.BaseRoles[0] != "writer" {
		t.Errorf("alice's cloud-scope grant did not succeed: %v", mock.memberEmails())
	}
}

// TestReconcileCloudAccess_FeatureGateOnReadSurfacesTwoFlagMessage covers AC-5
// on the reconcile's own member-list read. Before this fix, a 501 here (the
// read-side flag) fell through to the generic "Could Not Read The Cloud's
// Current Members" message, which sends an operator looking at permissions or
// the network instead of at the two feature flags that actually gate this
// endpoint.
func TestReconcileCloudAccess_FeatureGateOnReadSurfacesTwoFlagMessage(t *testing.T) {
	const cloudID = "cld_gate_read"

	mock := &cloudAccessReconcileMock{
		callerUserID:    "usr_caller",
		callerEmail:     "operator@example.com",
		members:         map[string]cloudAccessMockMember{},
		orgMembers:      map[string]cloudAccessMockIdentity{},
		rolesReadStatus: http.StatusNotImplemented,
	}

	_, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
		cloudAccessDesired(), cloudAccessApplyOptions())

	if !diags.HasError() {
		t.Fatal("expected an error: the roles listing 501'd")
	}
	if d := cloudAccessFindError(diags, "Could Not Read The Cloud's Current Members"); d != nil {
		t.Fatalf("got the generic read-failure diagnostic instead of the feature-gate one: %s", d.Detail())
	}
	d := cloudAccessFindError(diags, cloudRolesDisabledSummary)
	if d == nil {
		t.Fatalf("expected the %q diagnostic: %v", cloudRolesDisabledSummary, diags)
	}
	if !strings.Contains(d.Detail(), "two separate feature flags") {
		t.Errorf("the detail does not name the two-flag ambiguity: %s", d.Detail())
	}
}

// TestReconcileCloudAccess_FeatureGateOnRoleWriteSurfacesTwoFlagMessage covers
// AC-5 on the OTHER half of the two-flag gate - the roles PUT, which is
// reachable only through a grant. Before this fix, a 501 here fell through to
// "Could Not Set Cloud Role", which is just as misleading as the read-side
// generic message and for the same reason.
func TestReconcileCloudAccess_FeatureGateOnRoleWriteSurfacesTwoFlagMessage(t *testing.T) {
	const cloudID = "cld_gate_write"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{},
		orgMembers: map[string]cloudAccessMockIdentity{
			"incoming@example.com": {IdentityID: "idn_in", UserID: "usr_in"},
		},
		rolesWriteStatus: http.StatusNotImplemented,
	}

	result, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
		cloudAccessDesired(cloudAccessDesiredMember{Email: "incoming@example.com", BaseRole: "owner"}),
		cloudAccessApplyOptions())

	// NOT an error: a grant failure is recorded into Ungranted and warned
	// about, never erroring - see TestReconcileCloudAccess_FailedGrantSuppressesRevokesNotRecords.
	if diags.HasError() {
		t.Fatalf("a gated role write must not error: %v", diags)
	}
	if cloudAccessFindError(diags, "Could Not Set Cloud Role") != nil {
		t.Fatalf("got the generic set-role-failure diagnostic: %v", diags)
	}
	if len(result.Ungranted) != 1 || result.Ungranted[0].Email.ValueString() != "incoming@example.com" {
		t.Fatalf("expected incoming@example.com in Ungranted, got: %+v", result.Ungranted)
	}
	reason := result.Ungranted[0].Reason.ValueString()
	if !strings.Contains(reason, "two separate feature flags") {
		t.Errorf("the recorded reason does not name the two-flag ambiguity: %s", reason)
	}

	// The bootstrap membership the failed role write left behind must still be
	// rolled back - the feature gate changes the message, never the cleanup.
	if _, ok := mock.members["incoming@example.com"]; ok {
		t.Errorf("the bootstrap membership was not rolled back after the gated role write: %v", mock.memberEmails())
	}
}

// TestReconcileCloudAccess_GroupPolicyBindingRefusesRevoke covers AC-32's
// Present branch: a cloud carrying a group policy binding refuses before any
// write, naming the binding, so this resource never revokes a member the
// async group reconciler could silently re-grant later (J.20).
func TestReconcileCloudAccess_GroupPolicyBindingRefusesRevoke(t *testing.T) {
	const cloudID = "cld_grouppolicy_present"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{
			"undeclared@example.com": {IdentityID: "idn_u", UserID: "usr_u", BaseRoles: []string{"writer"}},
		},
		orgMembers:              map[string]cloudAccessMockIdentity{},
		groupPolicyBindingCount: 1,
	}

	_, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID, cloudAccessDesired(), cloudAccessApplyOptions())

	d := cloudAccessFindError(diags, "Cloud Has A Group Policy Binding")
	if d == nil {
		t.Fatalf("expected the group-policy refusal: %v", diags)
	}
	if len(mock.calls) != 0 {
		t.Errorf("the refusal came after writes had already been made: %v - it must be a preflight so a refused apply "+
			"changes nothing", mock.calls)
	}
	if _, ok := mock.members["undeclared@example.com"]; !ok {
		t.Error("the member was revoked despite the group-policy refusal")
	}
}

// TestReconcileCloudAccess_GroupPolicyAbsentProceedsSilently covers AC-32's
// Absent branch, both of its two shapes: a 404 (no policy row at all - the
// ordinary case for nearly every cloud today) and a 200 with an empty bindings
// list. Neither should produce any diagnostic.
func TestReconcileCloudAccess_GroupPolicyAbsentProceedsSilently(t *testing.T) {
	for _, tc := range []struct {
		name          string
		registerEmpty bool
	}{
		{name: "no policy row at all (404, unregistered route)", registerEmpty: false},
		{name: "200 with an empty bindings list", registerEmpty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const cloudID = "cld_grouppolicy_absent"
			mock := &cloudAccessReconcileMock{
				callerUserID: "usr_caller", callerEmail: "operator@example.com",
				members: map[string]cloudAccessMockMember{
					"undeclared@example.com": {IdentityID: "idn_u", UserID: "usr_u", BaseRoles: []string{"writer"}},
				},
				orgMembers:               map[string]cloudAccessMockIdentity{},
				groupPolicyRegisterEmpty: tc.registerEmpty,
			}

			_, diags := reconcileCloudAccess(context.Background(),
				cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID, cloudAccessDesired(), cloudAccessApplyOptions())

			if diags.HasError() {
				t.Fatalf("expected no error: absent must proceed silently: %v", diags)
			}
			if d := cloudAccessFindWarning(diags, "Could Not Confirm This Cloud Has No Group Policy Binding"); d != nil {
				t.Errorf("expected no warning for the absent case: %s", d.Detail())
			}
			if _, ok := mock.members["undeclared@example.com"]; ok {
				t.Error("the ordinary revoke did not happen - absent must not block the reconcile")
			}
		})
	}
}

// TestReconcileCloudAccess_GroupPolicyUndeterminedWarnsAndProceeds covers
// AC-32's Undetermined branch: a 403 (the common case for a Terraform token
// that is not an organization admin) warns loudly but does not refuse, and the
// ordinary revoke still happens.
func TestReconcileCloudAccess_GroupPolicyUndeterminedWarnsAndProceeds(t *testing.T) {
	const cloudID = "cld_grouppolicy_undetermined"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{
			"undeclared@example.com": {IdentityID: "idn_u", UserID: "usr_u", BaseRoles: []string{"writer"}},
		},
		orgMembers:                map[string]cloudAccessMockIdentity{},
		groupPolicyStatusOverride: http.StatusForbidden,
	}

	_, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID, cloudAccessDesired(), cloudAccessApplyOptions())

	if diags.HasError() {
		t.Fatalf("expected no error: undetermined must warn and proceed, never refuse: %v", diags)
	}
	d := cloudAccessFindWarning(diags, "Could Not Confirm This Cloud Has No Group Policy Binding")
	if d == nil {
		t.Fatalf("expected the undetermined warning: %v", diags)
	}
	if _, ok := mock.members["undeclared@example.com"]; ok {
		t.Error("undetermined blocked the ordinary revoke - it must warn and proceed, not refuse")
	}
}

// TestReconcileCloudAccess_GroupPolicyCheckSkippedWithNoRevokePending covers
// the entry-point gating: an apply that revokes nobody must not call the
// Policy API at all, matching auto_add_user's own entry point (J.20,
// Amendment 1) - the check would be irrelevant noise on the common case.
func TestReconcileCloudAccess_GroupPolicyCheckSkippedWithNoRevokePending(t *testing.T) {
	const cloudID = "cld_grouppolicy_norevoke"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{
			"existing@example.com": {IdentityID: "idn_e", UserID: "usr_e", BaseRoles: []string{"writer"}},
		},
		orgMembers: map[string]cloudAccessMockIdentity{
			"existing@example.com": {IdentityID: "idn_e", UserID: "usr_e"},
		},
		groupPolicyBindingCount: 1,
	}

	// Desired matches the existing member exactly, so there is nothing to
	// revoke - the group-policy check must not even run.
	_, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
		cloudAccessDesired(cloudAccessDesiredMember{Email: "existing@example.com", BaseRole: "writer"}),
		cloudAccessApplyOptions())

	if diags.HasError() {
		t.Fatalf("expected no error: nothing to revoke, so the group-policy check should not even fire: %v", diags)
	}
	if mock.groupPolicyRequestsRecorded != 0 {
		t.Errorf("the group-policy check ran even though no revoke was pending: %d request(s)", mock.groupPolicyRequestsRecorded)
	}
}

// TestReconcileCloudAccess_ExistingMemberIsUpdatedNotDowngraded covers the
// unconditional bootstrap. It runs on every member, including existing ones, and
// that is only safe because the API answers an existing member with a 409 and
// writes nothing - so the bridge permission level cannot replace a real role.
// Skipping the bootstrap conditionally is what produces an unrepairable resource
// (a role row with no membership edge), so it cannot simply be made conditional.
func TestReconcileCloudAccess_ExistingMemberIsUpdatedNotDowngraded(t *testing.T) {
	const cloudID = "cld_existing"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{
			"existing@example.com": {IdentityID: "idn_e", UserID: "usr_e", BaseRoles: []string{"readonly"}},
		},
		orgMembers: map[string]cloudAccessMockIdentity{
			"existing@example.com": {IdentityID: "idn_e", UserID: "usr_e"},
		},
	}

	_, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
		cloudAccessDesired(cloudAccessDesiredMember{
			Email: "existing@example.com", BaseRole: "owner", DenyRoles: []string{cloudAccessReadOnlyDenyRole},
		}), cloudAccessApplyOptions())
	if diags.HasError() {
		t.Fatalf("the bootstrap 409 for an already-present member must be tolerated, not fatal: %v", diags)
	}

	got := mock.members["existing@example.com"]
	if len(got.BaseRoles) != 1 || got.BaseRoles[0] != "owner" {
		t.Errorf("base roles came out as %v, want [owner]", got.BaseRoles)
	}
	if len(got.DenyRoles) != 1 || got.DenyRoles[0] != cloudAccessReadOnlyDenyRole {
		t.Errorf("deny roles came out as %v, want [%s]", got.DenyRoles, cloudAccessReadOnlyDenyRole)
	}

	sawBootstrap := false
	for _, call := range mock.calls {
		if call == "POST /api/v2/clouds/"+cloudID+"/collaborators/users" {
			sawBootstrap = true
		}
	}
	if !sawBootstrap {
		t.Error("the bootstrap POST was skipped for an existing member. It must run unconditionally: a conditional skip is what leaves a role row with no membership edge, where the bootstrap 409s forever and the revoke 404s forever")
	}
}

// A deny_roles the configuration omitted is sent as an empty list, because the
// roles write is a SET. Leaving the field off would carry a stale restriction
// forward invisibly - and at cloud scope the schema's contract is that an
// omitted deny_roles means no restrictions.
func TestReconcileCloudAccess_OmittedDenyRolesClearsRestrictions(t *testing.T) {
	const cloudID = "cld_deny"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{
			"member@example.com": {
				IdentityID: "idn_m", UserID: "usr_m",
				BaseRoles: []string{"writer"}, DenyRoles: []string{cloudAccessReadOnlyDenyRole},
			},
		},
		orgMembers: map[string]cloudAccessMockIdentity{
			"member@example.com": {IdentityID: "idn_m", UserID: "usr_m"},
		},
	}

	_, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
		// DenyRoles left nil, as cloudAccessDesiredMembers produces for an omitted
		// attribute.
		cloudAccessDesired(cloudAccessDesiredMember{Email: "member@example.com", BaseRole: "writer"}), cloudAccessApplyOptions())
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if got := mock.members["member@example.com"].DenyRoles; len(got) != 0 {
		t.Errorf("deny roles came out as %v, want empty: an omitted deny_roles at cloud scope means no restrictions, and the roles write is a SET", got)
	}

	// Asserted on the RAW body: a nil Go slice marshals to null, not [], and the
	// two are different requests to an API whose field is required. A decoded
	// assertion cannot tell them apart, so it would pass against a write that
	// sends null.
	if len(mock.roleWriteBodies) != 1 {
		t.Fatalf("got %d role writes, want 1: %v", len(mock.roleWriteBodies), mock.roleWriteBodies)
	}
	if !strings.Contains(mock.roleWriteBodies[0], `"deny_roles":[]`) {
		t.Errorf("the roles PUT body was %s, want deny_roles sent as an empty array - the wire field is required, and null is not the same request", mock.roleWriteBodies[0])
	}
}

// TestReconcileCloudAccess_DestroyDoesNotRefuseOnAutoAddUser covers the one
// asymmetry between an apply and a destroy.
//
// An apply refuses outright on an auto_add_user cloud, because Terraform cannot
// be authoritative over such a cloud's members at all. A destroy must NOT refuse:
// refusing would strand the resource in state with no exit but 'terraform state
// rm', which is a worse position than a destroy that tries, fails on each member
// and says exactly what to do about it. An apply has somewhere to go back to; a
// destroy does not.
func TestReconcileCloudAccess_DestroyDoesNotRefuseOnAutoAddUser(t *testing.T) {
	const cloudID = "cld_destroy_autoadd"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		autoAddUser: true,
		members: map[string]cloudAccessMockMember{
			"member@example.com": {IdentityID: "idn_m", UserID: "usr_m", BaseRoles: []string{"writer"}},
		},
		orgMembers:    map[string]cloudAccessMockIdentity{},
		failRevokeFor: map[string]string{"member@example.com": "removal refused by the backend"},
	}

	result, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID,
		map[string]cloudAccessDesiredMember{},
		cloudAccessReconcileOptions{RevokeUndeclared: true, RefuseWhenAutoAddUserEnabled: false})

	if diags.HasError() {
		t.Fatalf("a destroy must not refuse on an auto_add_user cloud - refusing leaves no exit but 'terraform state rm': %v", diags)
	}

	sawAttempt := false
	for _, call := range mock.calls {
		if strings.HasPrefix(call, "DELETE") {
			sawAttempt = true
		}
	}
	if !sawAttempt {
		t.Errorf("the destroy did not attempt the revoke at all: %v", mock.calls)
	}
	if len(result.Unmanaged) != 1 {
		t.Errorf("got %d recorded failures, want 1: the destroy must record what it could not remove: %+v", len(result.Unmanaged), result.Unmanaged)
	}
}

// TestReconcileCloudAccess_DestroyDoesNotRefuseOnGroupPolicyBinding is
// TestReconcileCloudAccess_DestroyDoesNotRefuseOnAutoAddUser's twin for J.20's
// asymmetry: a destroy attempts every revoke on a cloud carrying a group
// policy binding rather than refusing, for the same reason - refusing would
// strand the resource in state with no exit but 'terraform state rm'.
func TestReconcileCloudAccess_DestroyDoesNotRefuseOnGroupPolicyBinding(t *testing.T) {
	const cloudID = "cld_destroy_grouppolicy"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{
			"member@example.com": {IdentityID: "idn_m", UserID: "usr_m", BaseRoles: []string{"writer"}},
		},
		orgMembers:              map[string]cloudAccessMockIdentity{},
		groupPolicyBindingCount: 1,
	}

	_, diags := reconcileCloudAccess(context.Background(),
		cloudAccessTestClient(t, mock.handler(t, cloudID)), cloudID, map[string]cloudAccessDesiredMember{},
		cloudAccessReconcileOptions{RevokeUndeclared: true, RefuseWhenGroupPolicyBound: false})

	if diags.HasError() {
		t.Fatalf("a destroy must not refuse on a group-policy-bound cloud: %v", diags)
	}
	if _, ok := mock.members["member@example.com"]; ok {
		t.Error("the destroy did not attempt the revoke - a group policy binding must not block destroy")
	}
}

// runCloudAccessModifyPlanWithClient is runCloudAccessModifyPlan with a
// configured client, so the plan-time checks that need an API call actually run.
// The nil-client variant exists too, and deliberately: ValidateConfig-adjacent
// hooks can run before the provider is configured.
func runCloudAccessModifyPlanWithClient(t *testing.T, client *Client, planned *CloudAccessResourceModel) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()

	r := &CloudAccessResource{client: client}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	rawType := schemaResp.Schema.Type().TerraformType(ctx)

	holder := tfsdk.Plan{Schema: schemaResp.Schema, Raw: tftypes.NewValue(rawType, nil)}
	if diags := holder.Set(ctx, planned); diags.HasError() {
		t.Fatalf("failed to build the plan fixture: %v", diags)
	}

	resp := &resource.ModifyPlanResponse{}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{
		// Null prior state: a create. The caller check runs on a create too, which
		// is the point of it being ahead of the empty-member-set guard.
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(rawType, nil)},
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: holder.Raw},
	}, resp)
	return resp.Diagnostics
}

// TestCloudAccessModifyPlan_RefusesDeclaringTheCaller covers the plan-time half
// of the caller exclusion.
//
// The caller is absent from read, plan and write alike. Read omits them because
// reporting them while the reconcile refuses to revoke them has no working
// outcome - the post-apply state either contradicts the plan or the next refresh
// proposes the same removal forever. Given that, a configuration DECLARING the
// caller would be proposed as an addition on every plan and never converge, so it
// has to be an error rather than a silent no-op.
func TestCloudAccessModifyPlan_RefusesDeclaringTheCaller(t *testing.T) {
	const cloudID = "cld_plan"

	newMock := func() *cloudAccessReconcileMock {
		return &cloudAccessReconcileMock{
			callerUserID: "usr_caller", callerEmail: "operator@example.com",
			members:    map[string]cloudAccessMockMember{},
			orgMembers: map[string]cloudAccessMockIdentity{},
		}
	}

	planWith := func(emails ...string) *CloudAccessResourceModel {
		entries := make(map[string]attr.Value, len(emails))
		for _, e := range emails {
			entries[e] = cloudAccessMember("writer", cloudAccessNoDenyRoles(), cloudAccessNoProjects())
		}
		m := cloudAccessConfigModel(cloudAccessMemberMap(entries))
		return &m
	}

	t.Run("declaring the caller is an error", func(t *testing.T) {
		mock := newMock()
		diags := runCloudAccessModifyPlanWithClient(t, cloudAccessTestClient(t, mock.handler(t, cloudID)),
			planWith("operator@example.com", "alice@example.com"))

		d := cloudAccessFindError(diags, "Cannot Manage Your Own Cloud Access Here")
		if d == nil {
			t.Fatalf("expected the error: a declared entry for the caller can never converge, because Read never reports them: %v", diags)
		}
		cloudAccessAssertErrorPath(t, d, path.Root("member").AtMapKey("operator@example.com"))
	})

	t.Run("matched case-insensitively", func(t *testing.T) {
		// Email identity is not case-sensitive to Anyscale, so a differently-cased
		// spelling is the same person and the same non-converging plan.
		mock := newMock()
		diags := runCloudAccessModifyPlanWithClient(t, cloudAccessTestClient(t, mock.handler(t, cloudID)),
			planWith("Operator@Example.com"))
		if cloudAccessFindError(diags, "Cannot Manage Your Own Cloud Access Here") == nil {
			t.Errorf("a differently-cased spelling of the caller's own address slipped past the check: %v", diags)
		}
	})

	t.Run("declaring other people is fine", func(t *testing.T) {
		mock := newMock()
		diags := runCloudAccessModifyPlanWithClient(t, cloudAccessTestClient(t, mock.handler(t, cloudID)),
			planWith("alice@example.com", "bob@example.com"))
		if diags.HasError() {
			t.Fatalf("unexpected error for a configuration that declares nobody's own access: %v", diags)
		}
	})

	t.Run("an unresolvable caller skips the check without failing the plan", func(t *testing.T) {
		// What actually prevents an operator locking themselves out is the
		// reconcile's own exclusion, which fails closed. All that is lost here is a
		// friendly message, so a transient userinfo failure must not make the
		// configuration unplannable.
		broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(broken.Close)
		client := &Client{BaseURL: broken.URL, Token: "test-token", HTTPClient: broken.Client()}

		diags := runCloudAccessModifyPlanWithClient(t, client, planWith("operator@example.com"))
		if diags.HasError() {
			t.Fatalf("an unresolvable caller must not make the plan fail: %v", diags)
		}
	})
}

// TestCloudAccessModifyPlan_RefusesDeclaringOrgAdmins covers J.22/AC-34: the
// cloud roles write refuses outright to change an organization admin's role,
// and discovering that mid-reconcile - after another member's grant already
// succeeded - is exactly the situation the never-error-after-a-successful-
// write rule forbids. So this is caught at plan time instead, same placement
// as the caller check.
func TestCloudAccessModifyPlan_RefusesDeclaringOrgAdmins(t *testing.T) {
	const cloudID = "cld_plan_orgadmin"

	newMock := func() *cloudAccessReconcileMock {
		return &cloudAccessReconcileMock{
			callerUserID: "usr_caller", callerEmail: "operator@example.com",
			members: map[string]cloudAccessMockMember{},
			orgMembers: map[string]cloudAccessMockIdentity{
				"admin@example.com":       {IdentityID: "idn_admin", UserID: "usr_admin", PermissionLevel: "owner"},
				"other-admin@example.com": {IdentityID: "idn_admin2", UserID: "usr_admin2", PermissionLevel: "owner"},
				"alice@example.com":       {IdentityID: "idn_alice", UserID: "usr_alice", PermissionLevel: "collaborator"},
			},
		}
	}

	planWith := func(emails ...string) *CloudAccessResourceModel {
		entries := make(map[string]attr.Value, len(emails))
		for _, e := range emails {
			entries[e] = cloudAccessMember("writer", cloudAccessNoDenyRoles(), cloudAccessNoProjects())
		}
		m := cloudAccessConfigModel(cloudAccessMemberMap(entries))
		return &m
	}

	t.Run("declaring an org admin is an error naming them", func(t *testing.T) {
		mock := newMock()
		diags := runCloudAccessModifyPlanWithClient(t, cloudAccessTestClient(t, mock.handler(t, cloudID)),
			planWith("admin@example.com", "alice@example.com"))

		d := cloudAccessFindError(diags, "This Member Appears To Be An Organization Admin")
		if d == nil {
			t.Fatalf("expected the org-admin refusal: %v", diags)
		}
		cloudAccessAssertErrorPath(t, d, path.Root("member").AtMapKey("admin@example.com"))
	})

	t.Run("every declared admin is reported, not just the first", func(t *testing.T) {
		mock := newMock()
		diags := runCloudAccessModifyPlanWithClient(t, cloudAccessTestClient(t, mock.handler(t, cloudID)),
			planWith("admin@example.com", "other-admin@example.com", "alice@example.com"))

		for _, email := range []string{"admin@example.com", "other-admin@example.com"} {
			found := false
			for _, d := range diags {
				if d.Severity() == diag.SeverityError && strings.Contains(d.Detail(), email) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected an error naming %s: %v", email, diags)
			}
		}
	})

	t.Run("matched case-insensitively", func(t *testing.T) {
		mock := newMock()
		diags := runCloudAccessModifyPlanWithClient(t, cloudAccessTestClient(t, mock.handler(t, cloudID)),
			planWith("Admin@Example.com"))
		if cloudAccessFindError(diags, "This Member Appears To Be An Organization Admin") == nil {
			t.Errorf("a differently-cased spelling of the admin's address slipped past the check: %v", diags)
		}
	})

	t.Run("declaring a non-admin is fine", func(t *testing.T) {
		mock := newMock()
		diags := runCloudAccessModifyPlanWithClient(t, cloudAccessTestClient(t, mock.handler(t, cloudID)),
			planWith("alice@example.com"))
		if diags.HasError() {
			t.Fatalf("unexpected error for a configuration that declares no organization admin: %v", diags)
		}
	})

	t.Run("an unresolvable admin list skips the check without failing the plan", func(t *testing.T) {
		broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(broken.Close)
		client := &Client{BaseURL: broken.URL, Token: "test-token", HTTPClient: broken.Client()}

		diags := runCloudAccessModifyPlanWithClient(t, client, planWith("admin@example.com"))
		if diags.HasError() {
			t.Fatalf("an unresolvable admin list must not make the plan fail: %v", diags)
		}
	})
}

// runCloudAccessRead drives Read against a mock backend and returns the state it
// wrote.
func runCloudAccessRead(t *testing.T, client *Client, priorState *CloudAccessResourceModel) (CloudAccessResourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	r := &CloudAccessResource{client: client}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	rawType := schemaResp.Schema.Type().TerraformType(ctx)

	holder := tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(rawType, nil)}
	if diags := holder.Set(ctx, priorState); diags.HasError() {
		t.Fatalf("failed to build the prior-state fixture: %v", diags)
	}

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: holder.Raw}}
	r.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: schemaResp.Schema, Raw: holder.Raw}}, resp)

	var got CloudAccessResourceModel
	if resp.State.Raw.IsNull() {
		return got, resp.Diagnostics
	}
	diags := resp.Diagnostics
	diags.Append(resp.State.Get(ctx, &got)...)
	return got, diags
}

// TestCloudAccessRead_OmitsTheCaller covers the read half of the caller
// exclusion, and it is the half with no safe fallback.
//
// If Read reports the caller while the reconcile refuses to revoke them, the
// post-apply state either contradicts the plan - which Core rejects as "provider
// produced inconsistent result after apply" - or omits them, and the next Read
// puts them back and proposes the same removal forever. There is no third
// outcome, which is why this is enforced in Read rather than left to the write
// path.
func TestCloudAccessRead_OmitsTheCaller(t *testing.T) {
	const cloudID = "cld_read"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{
			"operator@example.com": {IdentityID: "idn_caller", UserID: "usr_caller", BaseRoles: []string{"owner"}},
			"alice@example.com":    {IdentityID: "idn_a", UserID: "usr_a", BaseRoles: []string{"writer"}},
		},
		orgMembers: map[string]cloudAccessMockIdentity{},
	}

	prior := cloudAccessConfigModel(types.MapNull(cloudAccessMemberType()))
	prior.CloudID = types.StringValue(cloudID)
	prior.ID = types.StringValue(cloudID)

	got, diags := runCloudAccessRead(t, cloudAccessTestClient(t, mock.handler(t, cloudID)), &prior)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	keys := got.Member.Elements()
	if _, present := keys["operator@example.com"]; present {
		t.Errorf("Read reported the calling identity as a member. A configuration cannot declare them - that is a plan-time error - so reporting them makes every plan propose the same removal, which the reconcile then refuses to perform: %v", keys)
	}
	if _, present := keys["alice@example.com"]; !present {
		t.Errorf("Read dropped a member who is not the caller: %v", keys)
	}
	if len(keys) != 1 {
		t.Errorf("got %d members, want 1: %v", len(keys), keys)
	}
}

// Read must not fail the whole refresh for the ordinary reasons, but it MUST fail
// when it cannot identify the caller: an unidentified caller means the refresh
// cannot tell whether it is about to write the one member that must not appear in
// state, and a failed refresh is recoverable while a state that disagrees with
// the plan is not.
func TestCloudAccessRead_FailsWhenTheCallerCannotBeIdentified(t *testing.T) {
	const cloudID = "cld_noidentity"

	// Only userinfo fails; every other endpoint works. A mock that failed
	// everything would make Read error for a different reason entirely, and the
	// test would pass whether or not this branch exists at all.
	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{
			"alice@example.com": {IdentityID: "idn_a", UserID: "usr_a", BaseRoles: []string{"writer"}},
		},
		orgMembers: map[string]cloudAccessMockIdentity{},
	}
	backend := mock.handler(t, cloudID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/userinfo" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	prior := cloudAccessConfigModel(types.MapNull(cloudAccessMemberType()))
	prior.CloudID = types.StringValue(cloudID)
	prior.ID = types.StringValue(cloudID)

	_, diags := runCloudAccessRead(t,
		&Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}, &prior)

	// Asserted on the SPECIFIC summary, not merely that some error occurred: "an
	// error happened" is satisfied by any failure anywhere in Read and would not
	// notice this branch disappearing.
	if cloudAccessFindError(diags, "Could Not Identify The Calling Identity") == nil {
		t.Fatalf("expected the caller-identity error: without that identity this refresh cannot know which member to omit, and reporting everyone produces a state that contradicts the plan. Got: %v", diags)
	}
}

// TestCloudAccessRevokeFailureReason covers the text that lands in
// unmanaged_grants, which is the only thing an operator gets to act on when a
// revoke fails.
//
// The unrecognized case matters most. Two settings block collaborator removal
// outright - the cloud's auto_add_user, and an organization with directory sync
// plus at least one Policy API role binding - and both report a conflict, so they
// are distinguishable only by wording. A reworded message on either side falls
// through to the default, and a default that returned the bare detail would leave
// the operator with a conflict and no idea which of the two they hit.
func TestCloudAccessRevokeFailureReason(t *testing.T) {
	for _, tc := range []struct {
		name        string
		detail      string
		wantPhrases []string
	}{
		{
			name:        "auto_add_user names the cloud setting",
			detail:      "Users cannot be removed from clouds which have auto add users enabled.",
			wantPhrases: []string{"auto_add_user"},
		},
		{
			name:        "self-removal names who has to do it instead",
			detail:      "You cannot remove yourself from the cloud",
			wantPhrases: []string{"another cloud owner"},
		},
		{
			name:        "the Policy API case points at the CLI that owns it",
			detail:      "This organization uses the Policy API",
			wantPhrases: []string{"anyscale policy set"},
		},
		{
			name:   "an unrecognized failure names both structural blockers",
			detail: "some conflict nobody has seen before",
			wantPhrases: []string{
				"some conflict nobody has seen before",
				"auto_add_user",
				"directory sync",
				"Policy API",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := cloudAccessRevokeFailureReason(fmt.Errorf("unexpected status 409: %s", tc.detail))
			for _, want := range tc.wantPhrases {
				if !strings.Contains(got, want) {
					t.Errorf("the reason does not mention %q: %s", want, got)
				}
			}
		})
	}
}

// TestCloudAccessGrantFailure_NextRefreshSurfacesTheShortfall is AC-36:
// after a failed grant, state still claims the full planned map (AC-21a -
// `member` has no Computed sub-fields, so applyCloudAccess must write the
// plan verbatim even for a member whose grant failed). The failure must not
// stay invisible forever - the next refresh has to recover it, so the
// following plan re-proposes the grant rather than converging on a lie.
//
// Chains applyCloudAccess (the AC-35 scenario) into Read directly - the
// write gate stays closed throughout, since neither call goes through
// Create/Update's refuseWriteWhileReadOnly check. Asserting the refreshed
// member map omits the failed grant is a deliberate proxy for a real
// Terraform plan (which can't run while the gate is closed): it proves
// exactly what forces a non-empty plan - state no longer matching what the
// unchanged config declares.
func TestCloudAccessGrantFailure_NextRefreshSurfacesTheShortfall(t *testing.T) {
	const cloudID = "cld_grantfail_refresh"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{},
		orgMembers: map[string]cloudAccessMockIdentity{
			"alice@example.com": {IdentityID: "idn_a", UserID: "usr_a"},
			"bob@example.com":   {IdentityID: "idn_b", UserID: "usr_b"},
		},
		failRoleWriteFor: map[string]bool{"bob@example.com": true},
	}
	client := cloudAccessTestClient(t, mock.handler(t, cloudID))

	// STEP 1: apply a config declaring both alice and bob. alice's grant
	// succeeds against the backend; bob's fails and is rolled back (the same
	// AC-35 shape forge's reconcile-level tests already cover) - confirmed
	// against the backend below, not assumed.
	plan := &CloudAccessResourceModel{
		ID:                  types.StringValue(cloudID),
		CloudID:             types.StringValue(cloudID),
		AllowEmptyMemberSet: types.BoolValue(false),
		Timeouts:            timeouts.Value{Object: types.ObjectNull(map[string]attr.Type{"create": types.StringType, "update": types.StringType, "delete": types.StringType})},
		Member: cloudAccessMemberMap(map[string]attr.Value{
			"alice@example.com": cloudAccessMember("writer", cloudAccessNoDenyRoles(), cloudAccessNoProjects()),
			"bob@example.com":   cloudAccessMember("writer", cloudAccessNoDenyRoles(), cloudAccessNoProjects()),
		}),
	}
	declaredMember := plan.Member // saved before applyCloudAccess can touch plan

	r := &CloudAccessResource{client: client}
	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	applyState := tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}

	var applyDiags diag.Diagnostics
	r.applyCloudAccess(ctx, plan, types.MapNull(cloudAccessMemberObjectType()), &applyState, &applyDiags)
	if applyDiags.HasError() {
		t.Fatalf("the apply itself must not error on a grant failure (AC-35): %v", applyDiags)
	}
	if _, ok := mock.members["bob@example.com"]; ok {
		t.Fatalf("precondition failed: bob's grant should have failed and rolled back, but the backend has him: %v", mock.memberEmails())
	}
	if mem, ok := mock.members["alice@example.com"]; !ok || len(mem.BaseRoles) == 0 {
		t.Fatalf("precondition failed: alice's grant should have succeeded against the backend: %v", mock.memberEmails())
	}

	var postApply CloudAccessResourceModel
	if diags := applyState.Get(ctx, &postApply); diags.HasError() {
		t.Fatalf("failed to read back the post-apply state: %v", diags)
	}
	if _, ok := postApply.Member.Elements()["bob@example.com"]; !ok {
		t.Fatalf("AC-21a precondition failed: post-apply state must still claim bob (the full planned map), or this test isn't exercising the shortfall AC-36 is about")
	}

	// STEP 2: refresh. The backend genuinely never granted bob, so a
	// refresh reading the real member-search must recover THAT truth,
	// diverging from what the (still-error-free, per AC-21a) apply claimed.
	refreshed, readDiags := runCloudAccessRead(t, client, &postApply)
	if readDiags.HasError() {
		t.Fatalf("unexpected error refreshing: %v", readDiags)
	}

	if _, stillClaimed := refreshed.Member.Elements()["bob@example.com"]; stillClaimed {
		t.Errorf("the refresh still reports bob@example.com as a member - the failed grant's shortfall did not " +
			"surface, so a plan against the unchanged config (which still declares bob) would show no diff and " +
			"silently never retry the grant")
	}
	// Control: alice's real, successful grant must survive the refresh
	// unchanged - otherwise this test could not distinguish "the refresh
	// correctly reports reality" from "the refresh drops everyone".
	if _, present := refreshed.Member.Elements()["alice@example.com"]; !present {
		t.Fatalf("control failed: alice's real grant did not survive the refresh - this test's own assertion above proves nothing without it")
	}

	// The declared config (unchanged from step 1) still names bob - the
	// comparison that would drive a real plan's diff, made explicit here
	// since no live Terraform plan step can run while the write gate is
	// closed.
	if _, stillDeclared := declaredMember.Elements()["bob@example.com"]; !stillDeclared {
		t.Fatalf("test setup error: the declared config must still name bob for the re-propose claim to mean anything")
	}
}

// TestApplyCloudAccess_NeverWritesADifferentMemberThanPlanned is AC-21b, a
// regression guard rather than a diagnostic test (design record ed5a176):
// applyCloudAccess never reads `member` back on any path today, so its
// original purpose is moot, but nothing would fail if a future edit
// reintroduced one - this test is what would catch it.
//
// The mock's roles GET is overridden to report a deny_roles value different
// from the plan's - an EMPTY, DECLARED list, the narrowest input that
// distinguishes "wrote the plan verbatim" from "wrote something read back".
// If applyCloudAccess ever read that response into plan.Member, the
// override value would appear in state and the assertion below would fail.
//
// Mutation-proven: temporarily reintroducing a read-back write to
// plan.Member makes this test fail with the override's value instead of the
// plan's; reverting restores the pass.
func TestApplyCloudAccess_NeverWritesADifferentMemberThanPlanned(t *testing.T) {
	const cloudID = "cld_apply_guard"

	mock := &cloudAccessReconcileMock{
		callerUserID: "usr_caller", callerEmail: "operator@example.com",
		members: map[string]cloudAccessMockMember{},
		orgMembers: map[string]cloudAccessMockIdentity{
			"alice@example.com": {IdentityID: "ide_alice", UserID: "usr_alice"},
		},
		roleReadOverrideDenyRoles: map[string][]string{
			"usr_alice": {cloudAccessReadOnlyDenyRole},
		},
	}

	plan := &CloudAccessResourceModel{
		ID:                  types.StringValue(cloudID),
		CloudID:             types.StringValue(cloudID),
		AllowEmptyMemberSet: types.BoolValue(false),
		Timeouts:            timeouts.Value{Object: types.ObjectNull(map[string]attr.Type{"create": types.StringType, "update": types.StringType, "delete": types.StringType})},
		Member: cloudAccessMemberMap(map[string]attr.Value{
			"alice@example.com": cloudAccessMember("writer", cloudAccessDenyRoles(), cloudAccessNoProjects()),
		}),
	}

	client := cloudAccessTestClient(t, mock.handler(t, cloudID))
	r := &CloudAccessResource{client: client}

	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	rawType := schemaResp.Schema.Type().TerraformType(ctx)
	state := tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(rawType, nil)}

	var diags diag.Diagnostics
	r.applyCloudAccess(ctx, plan, types.MapNull(cloudAccessMemberObjectType()), &state, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected error applying a config this mock should accept cleanly: %v", diags)
	}

	var got CloudAccessResourceModel
	if diags := state.Get(ctx, &got); diags.HasError() {
		t.Fatalf("failed to read back the state applyCloudAccess wrote: %v", diags)
	}

	elems := got.Member.Elements()
	aliceVal, ok := elems["alice@example.com"]
	if !ok {
		t.Fatalf("state.Member is missing alice@example.com entirely: %v", elems)
	}
	aliceObj, ok := aliceVal.(types.Object)
	if !ok {
		t.Fatalf("alice@example.com is not an object: %#v", aliceVal)
	}
	denyRolesVal := aliceObj.Attributes()["deny_roles"]
	denyRolesList, ok := denyRolesVal.(types.List)
	if !ok {
		t.Fatalf("alice@example.com's deny_roles is not a list: %#v", denyRolesVal)
	}
	if denyRolesList.IsNull() {
		t.Errorf("alice@example.com's deny_roles came back null - the plan declared it as an empty list, not " +
			"never-declared, and a null here means SOMETHING wrote a value other than the plan's")
	}
	if got := len(denyRolesList.Elements()); got != 0 {
		t.Errorf("alice@example.com's deny_roles has %d entries, want 0 (the plan's empty list) - a non-empty "+
			"result matching the mock's override (%v) means applyCloudAccess read the roles GET back into "+
			"state instead of writing the plan verbatim, which is exactly the regression this guard exists to catch",
			got, []string{cloudAccessReadOnlyDenyRole})
	}
}
