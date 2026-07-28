package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestParseCloudUserRoleID_Valid confirms the composite cloud_id/email import
// format parses cleanly.
func TestParseCloudUserRoleID_Valid(t *testing.T) {
	cloudID, email, err := parseCloudUserRoleID("cld_abc123/analyst@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cloudID != "cld_abc123" {
		t.Errorf("cloudID = %q, want %q", cloudID, "cld_abc123")
	}
	if email != "analyst@example.com" {
		t.Errorf("email = %q, want %q", email, "analyst@example.com")
	}
}

// TestParseCloudUserRoleID_InvalidFormats covers every malformed shape this
// parser must reject rather than silently misinterpret.
func TestParseCloudUserRoleID_InvalidFormats(t *testing.T) {
	for _, id := range []string{"", "no-slash-at-all", "/missing-cloud@example.com", "cld_missing_email/", "cld_only"} {
		if _, _, err := parseCloudUserRoleID(id); err == nil {
			t.Errorf("parseCloudUserRoleID(%q) = nil error, want an error", id)
		}
	}
}

// TestParseCloudUserRoleID_ExtraSlashGoesToEmail confirms parsing keeps
// everything after the first slash together - relevant since an email address
// itself never contains "/", but the parser should not assume that.
func TestParseCloudUserRoleID_ExtraSlashGoesToEmail(t *testing.T) {
	cloudID, email, err := parseCloudUserRoleID("cld_abc/plus+alias/example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cloudID != "cld_abc" || email != "plus+alias/example.com" {
		t.Errorf("got cloudID=%q email=%q, want cloudID=%q email=%q", cloudID, email, "cld_abc", "plus+alias/example.com")
	}
}

// TestDenyRolesToList_EmptyAPIResultPreservesPriorNull is the mutation-proof
// case for the null-vs-empty-list contract: deny_roles is Optional-not-Computed,
// and the wire response cannot distinguish "config omitted it" from "config
// set it to []" (this resource always sends [] for both) - so when the API
// genuinely reports zero deny roles, state must reproduce whatever shape prior
// state already had, not unconditionally collapse to one or the other.
func TestDenyRolesToList_EmptyAPIResultPreservesPriorNull(t *testing.T) {
	got, diags := denyRolesToList(context.Background(), []string{}, types.ListNull(types.StringType))
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if !got.IsNull() {
		t.Errorf("expected null preserved when prior state was null and API reports zero deny roles, got %v", got)
	}
}

// TestDenyRolesToList_EmptyAPIResultPreservesPriorEmptyList is the other half
// of the same contract: if prior state was an explicit empty list (not null),
// an empty API result should stay an explicit empty list, not collapse to null.
func TestDenyRolesToList_EmptyAPIResultPreservesPriorEmptyList(t *testing.T) {
	priorEmpty, diags := types.ListValueFrom(context.Background(), types.StringType, []string{})
	if diags.HasError() {
		t.Fatalf("failed to build fixture: %v", diags)
	}

	got, diags := denyRolesToList(context.Background(), []string{}, priorEmpty)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if got.IsNull() {
		t.Error("expected an explicit empty list preserved (not collapsed to null) when prior state was already an explicit empty list")
	}
	if length := len(got.Elements()); length != 0 {
		t.Errorf("expected zero elements, got %d", length)
	}
}

// TestDenyRolesToList_NonEmptyAPIResultAlwaysWins confirms a real, non-empty
// API result overrides prior state regardless of what shape prior state had -
// this is the one case where the null-vs-empty ambiguity does not apply, since
// the API is unambiguously telling us something.
func TestDenyRolesToList_NonEmptyAPIResultAlwaysWins(t *testing.T) {
	got, diags := denyRolesToList(context.Background(), []string{"cloud_read_only"}, types.ListNull(types.StringType))
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if got.IsNull() {
		t.Fatal("expected a populated list, got null")
	}
	var out []string
	if diags := got.ElementsAs(context.Background(), &out, false); diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if len(out) != 1 || out[0] != "cloud_read_only" {
		t.Errorf("got %v, want [cloud_read_only]", out)
	}
}

// mockCloudUserRoleServer is a scripted stand-in for the org-wide
// organization_collaborators list plus the cloud-scoped collaborators/roles
// APIs, tracking one mutable cloud's bootstrap membership and roles plus an
// ordered call log so H21/H22's strictly-ordered, always-unconditional Create
// sequence can be asserted directly rather than merely assumed from the code
// reading correctly.
type mockCloudUserRoleServer struct {
	mu sync.Mutex

	cloudID string

	// orgMembers: email (lowercased) -> {identity_id, user_id}
	orgMembers map[string]orgMemberFixture
	// cloudPermissions: user_id -> permission_level, present once the legacy
	// bootstrap (or a directly-simulated external grant) has run.
	cloudPermissions map[string]string
	// roles: user_id -> {base_roles, deny_roles}
	roles map[string]CloudUserRolesResult

	callLog []string

	failNextRolesPUT   bool
	failNextLegacyPOST *failureInjection
	failDelete         *failureInjection
}

type orgMemberFixture struct {
	identityID string
	userID     string
}

type failureInjection struct {
	status int
	detail string
}

func newMockCloudUserRoleServer(t *testing.T) (*httptest.Server, *mockCloudUserRoleServer) {
	t.Helper()
	s := &mockCloudUserRoleServer{
		cloudID:          "cld_test",
		orgMembers:       map[string]orgMemberFixture{},
		cloudPermissions: map[string]string{},
		roles:            map[string]CloudUserRolesResult{},
	}
	server := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(server.Close)
	return server, s
}

// seedOrgMember registers an org-wide collaborator resolveIdentityForEmail
// can resolve - every test that exercises Create/Import must seed the target
// email here first, exactly like a real organization member would need to
// exist before any cloud role work is possible.
func (s *mockCloudUserRoleServer) seedOrgMember(email, identityID, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orgMembers[strings.ToLower(email)] = orgMemberFixture{identityID: identityID, userID: userID}
}

// seedExternalGrant simulates a role granted OUTSIDE this resource entirely
// (the H22/G8 scenario): a role exists but no legacy bootstrap ever ran, so
// there is no entry in cloudPermissions for this user - only in roles.
func (s *mockCloudUserRoleServer) seedExternalGrant(userID string, baseRoles, denyRoles []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[userID] = CloudUserRolesResult{UserID: userID, BaseRoles: baseRoles, DenyRoles: denyRoles}
}

func (s *mockCloudUserRoleServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	path := r.URL.Path
	base := fmt.Sprintf("/api/v2/clouds/%s", s.cloudID)

	switch {
	case r.Method == http.MethodGet && path == "/api/v2/organization_collaborators":
		s.callLog = append(s.callLog, "GET org-collaborators")
		s.handleOrgList(w, r)
	case r.Method == http.MethodPost && path == base+"/collaborators/users":
		s.callLog = append(s.callLog, "POST legacy-create")
		s.handleLegacyCreate(w, r)
	case r.Method == http.MethodPut && strings.HasPrefix(path, base+"/collaborators/users/") && strings.HasSuffix(path, "/roles"):
		s.callLog = append(s.callLog, "PUT roles")
		s.handleSetRoles(w, r)
	case r.Method == http.MethodGet && path == base+"/collaborators/roles":
		s.callLog = append(s.callLog, "GET roles")
		s.handleListRoles(w)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, base+"/collaborators/"):
		s.callLog = append(s.callLog, "DELETE legacy")
		s.handleDelete(w, path)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":"not found: %s %s"}}`, r.Method, path)
	}
}

func (s *mockCloudUserRoleServer) handleOrgList(w http.ResponseWriter, r *http.Request) {
	emailFilter := strings.ToLower(r.URL.Query().Get("email"))
	results := make([]OrganizationCollaboratorResult, 0, len(s.orgMembers))
	for email, m := range s.orgMembers {
		if emailFilter != "" && email != emailFilter {
			continue
		}
		userID := m.userID
		results = append(results, OrganizationCollaboratorResult{ID: m.identityID, Email: email, UserID: &userID})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(OrganizationCollaboratorsListResponse{Results: results})
}

func (s *mockCloudUserRoleServer) handleLegacyCreate(w http.ResponseWriter, r *http.Request) {
	var body CreateCloudCollaboratorRequest
	_ = json.NewDecoder(r.Body).Decode(&body)

	if s.failNextLegacyPOST != nil {
		fi := s.failNextLegacyPOST
		s.failNextLegacyPOST = nil
		w.WriteHeader(fi.status)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":%q}}`, fi.detail)
		return
	}

	member, ok := s.orgMembers[strings.ToLower(body.Email)]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":"User with email %s not found."}}`, body.Email)
		return
	}
	if _, alreadyHas := s.cloudPermissions[member.userID]; alreadyHas {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":"User with email %s already has permissions for this cloud."}}`, body.Email)
		return
	}

	s.cloudPermissions[member.userID] = body.PermissionLevel
	w.WriteHeader(http.StatusNoContent)
}

func (s *mockCloudUserRoleServer) handleSetRoles(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, fmt.Sprintf("/api/v2/clouds/%s/collaborators/users/", s.cloudID)), "/roles")

	if s.failNextRolesPUT {
		s.failNextRolesPUT = false
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":{"detail":"injected roles PUT failure"}}`)
		return
	}

	var body SetCloudRolesRequest
	_ = json.NewDecoder(r.Body).Decode(&body)

	s.roles[userID] = CloudUserRolesResult{UserID: userID, BaseRoles: []string{body.BaseRole}, DenyRoles: body.DenyRoles}
	// Reflect real backend behavior: a real permissions row also gets a
	// legacy permission_level, whether from a prior bootstrap or one this
	// call itself now implies exists.
	if _, hasPerm := s.cloudPermissions[userID]; !hasPerm {
		s.cloudPermissions[userID] = "write"
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *mockCloudUserRoleServer) handleListRoles(w http.ResponseWriter) {
	results := make([]CloudUserRolesResult, 0, len(s.roles))
	for _, role := range s.roles {
		results = append(results, role)
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(CloudUserRolesListResponse{Results: results})
}

func (s *mockCloudUserRoleServer) handleDelete(w http.ResponseWriter, path string) {
	if s.failDelete != nil {
		w.WriteHeader(s.failDelete.status)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":%q}}`, s.failDelete.detail)
		return
	}

	identityID := path[len(fmt.Sprintf("/api/v2/clouds/%s/collaborators/", s.cloudID)):]

	var userID string
	for email, m := range s.orgMembers {
		if m.identityID == identityID {
			userID = m.userID
			_ = email
			break
		}
	}
	if userID == "" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":"User with identity %s is not a member of clouds: {%s}."}}`, identityID, s.cloudID)
		return
	}
	if _, hasPerm := s.cloudPermissions[userID]; !hasPerm {
		// H22/G8: a permissions row (or role) may exist with no membership
		// edge - modeled here as "no cloudPermissions entry recorded via the
		// legacy bootstrap", exactly the trap state.
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":"User with identity %s is not a member of clouds: {%s}."}}`, identityID, s.cloudID)
		return
	}

	delete(s.cloudPermissions, userID)
	delete(s.roles, userID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *mockCloudUserRoleServer) callLogSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.callLog))
	copy(out, s.callLog)
	return out
}

// indexOfCall returns the first index of call in log, or -1 if absent. Used
// to assert relative ordering between two calls (H21/H22) rather than
// assuming either one is the very first call overall - email/identity
// resolution legitimately happens before either.
func indexOfCall(log []string, call string) int {
	for i, c := range log {
		if c == call {
			return i
		}
	}
	return -1
}

func (s *mockCloudUserRoleServer) hasCloudPermission(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.cloudPermissions[userID]
	return ok
}

// runCloudUserRoleCreate drives CloudUserRoleResource's real Create() method
// end to end, the same pattern established for organization_user's own tests.
func runCloudUserRoleCreate(t *testing.T, r *CloudUserRoleResource, plan CloudUserRoleResourceModel) (CloudUserRoleResourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	tfPlan := tfsdk.Plan{Schema: schemaResp.Schema}
	if diags := tfPlan.Set(ctx, &plan); diags.HasError() {
		t.Fatalf("failed to build plan fixture: %v", diags)
	}
	tfState := tfsdk.State{Schema: schemaResp.Schema}

	createResp := &resource.CreateResponse{State: tfState}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan}, createResp)

	if createResp.Diagnostics.HasError() {
		return CloudUserRoleResourceModel{}, createResp.Diagnostics
	}

	var result CloudUserRoleResourceModel
	getDiags := createResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, createResp.Diagnostics
}

// runCloudUserRoleDelete drives CloudUserRoleResource's real Delete() method
// end to end against a state model.
func runCloudUserRoleDelete(t *testing.T, r *CloudUserRoleResource, state CloudUserRoleResourceModel) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	tfState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := tfState.Set(ctx, &state); diags.HasError() {
		t.Fatalf("failed to build state fixture: %v", diags)
	}

	deleteResp := &resource.DeleteResponse{State: tfState}
	r.Delete(ctx, resource.DeleteRequest{State: tfState}, deleteResp)
	return deleteResp.Diagnostics
}

func denyRolesNull() types.List {
	return types.ListNull(types.StringType)
}

// TestCloudUserRoleCreate_BootstrapAlwaysRunsFirstThenRolesPUT is H21/H22's
// central regression test: the legacy bootstrap POST must run first and
// unconditionally, before the roles PUT, every time - never skipped based on
// a pre-check, since a conditional bootstrap is exactly the shape of bug that
// can walk a user into the H22 unrepairable state.
func TestCloudUserRoleCreate_BootstrapAlwaysRunsFirstThenRolesPUT(t *testing.T) {
	httpServer, mock := newMockCloudUserRoleServer(t)
	r := &CloudUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
	mock.seedOrgMember("analyst@example.com", "ide_analyst", "usr_analyst")

	plan := CloudUserRoleResourceModel{
		CloudID:   types.StringValue("cld_test"),
		Email:     types.StringValue("analyst@example.com"),
		BaseRole:  types.StringValue("compute_config_viewer"),
		DenyRoles: denyRolesNull(),
	}

	result, diags := runCloudUserRoleCreate(t, r, plan)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	log := mock.callLogSnapshot()
	createIdx := indexOfCall(log, "POST legacy-create")
	rolesIdx := indexOfCall(log, "PUT roles")
	if createIdx == -1 {
		t.Fatalf("expected the legacy create to run unconditionally, but it never happened - call log: %v", log)
	}
	if rolesIdx == -1 || rolesIdx < createIdx {
		t.Fatalf("expected the legacy create to run strictly before the roles PUT (H21/H22 ordering), got call log: %v", log)
	}
	foundRolesPUTAfterCreate := false
	for _, call := range log[createIdx+1:] {
		if call == "PUT roles" {
			foundRolesPUTAfterCreate = true
			break
		}
	}
	if !foundRolesPUTAfterCreate {
		t.Fatalf("expected a roles PUT after the legacy create, got call log: %v", log)
	}

	if result.ID.ValueString() != "cld_test/analyst@example.com" {
		t.Errorf("id = %q, want composite cloud_id/email", result.ID.ValueString())
	}
	if result.UserID.ValueString() != "usr_analyst" {
		t.Errorf("user_id = %q, want %q (resolved from email)", result.UserID.ValueString(), "usr_analyst")
	}
	if result.IdentityID.ValueString() != "ide_analyst" {
		t.Errorf("identity_id = %q, want %q (resolved from email)", result.IdentityID.ValueString(), "ide_analyst")
	}
	if result.BaseRole.ValueString() != "compute_config_viewer" {
		t.Errorf("base_role = %q, want %q", result.BaseRole.ValueString(), "compute_config_viewer")
	}
}

// TestCloudUserRoleCreate_AlreadyHasPermissions409IsExpectedNotFatal is H22's
// central regression test: the bootstrap runs unconditionally even when the
// user already has cloud permissions, and its resulting 409 ("already has
// permissions for this cloud") must be treated as an expected, non-fatal
// outcome that simply skips ahead to the role grant - not a Create failure.
func TestCloudUserRoleCreate_AlreadyHasPermissions409IsExpectedNotFatal(t *testing.T) {
	httpServer, mock := newMockCloudUserRoleServer(t)
	r := &CloudUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
	mock.seedOrgMember("existing@example.com", "ide_existing", "usr_existing")
	mock.cloudPermissions["usr_existing"] = "write" // already a collaborator before this Create runs

	plan := CloudUserRoleResourceModel{
		CloudID:   types.StringValue("cld_test"),
		Email:     types.StringValue("existing@example.com"),
		BaseRole:  types.StringValue("writer"),
		DenyRoles: denyRolesNull(),
	}

	result, diags := runCloudUserRoleCreate(t, r, plan)
	if diags.HasError() {
		t.Fatalf("expected the already-has-permissions 409 to be treated as non-fatal, got: %v", diags)
	}
	if result.BaseRole.ValueString() != "writer" {
		t.Errorf("base_role = %q, want %q", result.BaseRole.ValueString(), "writer")
	}

	log := mock.callLogSnapshot()
	if indexOfCall(log, "POST legacy-create") == -1 {
		t.Fatalf("expected the legacy create to still be attempted unconditionally even though the user already had permissions, got call log: %v", log)
	}
}

// TestCloudUserRoleCreate_RollsBackMembershipItCreatedOnRolesPUTFailure is
// H21's most important regression test: if this Create call itself
// establishes cloud membership and the immediately-following roles PUT then
// fails, the resource must roll back the membership it just created rather
// than silently leaving the user holding the bridge permission_level.
func TestCloudUserRoleCreate_RollsBackMembershipItCreatedOnRolesPUTFailure(t *testing.T) {
	httpServer, mock := newMockCloudUserRoleServer(t)
	r := &CloudUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
	mock.seedOrgMember("flaky@example.com", "ide_flaky", "usr_flaky")
	mock.failNextRolesPUT = true

	plan := CloudUserRoleResourceModel{
		CloudID:   types.StringValue("cld_test"),
		Email:     types.StringValue("flaky@example.com"),
		BaseRole:  types.StringValue("owner"),
		DenyRoles: denyRolesNull(),
	}

	_, diags := runCloudUserRoleCreate(t, r, plan)
	if !diags.HasError() {
		t.Fatal("expected an error when the roles PUT fails, got none")
	}

	if mock.hasCloudPermission("usr_flaky") {
		t.Error("expected the cloud membership this Create created to be rolled back after the roles PUT failed, but the permission is still present")
	}

	foundRollbackMention := false
	for _, d := range diags {
		if strings.Contains(d.Detail(), "rolled back") && strings.Contains(d.Detail(), "no unintended access") {
			foundRollbackMention = true
		}
	}
	if !foundRollbackMention {
		t.Errorf("expected a diagnostic confirming the rollback succeeded and no unintended access was left behind, got: %v", diags)
	}
}

// TestCloudUserRoleCreate_DoesNotRollBackPreexistingMembershipOnRolesPUTFailure
// is the other side of the same hazard: if the user was ALREADY a cloud
// collaborator before this Create ran (the bootstrap 409'd as expected), a
// roles-PUT failure must NOT delete their pre-existing cloud membership -
// this Create did not create it and has no business removing it.
func TestCloudUserRoleCreate_DoesNotRollBackPreexistingMembershipOnRolesPUTFailure(t *testing.T) {
	httpServer, mock := newMockCloudUserRoleServer(t)
	r := &CloudUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
	mock.seedOrgMember("existing@example.com", "ide_existing", "usr_existing")
	mock.cloudPermissions["usr_existing"] = "write"
	mock.failNextRolesPUT = true

	plan := CloudUserRoleResourceModel{
		CloudID:   types.StringValue("cld_test"),
		Email:     types.StringValue("existing@example.com"),
		BaseRole:  types.StringValue("writer"),
		DenyRoles: denyRolesNull(),
	}

	_, diags := runCloudUserRoleCreate(t, r, plan)
	if !diags.HasError() {
		t.Fatal("expected an error when the roles PUT fails, got none")
	}

	if !mock.hasCloudPermission("usr_existing") {
		t.Fatal("expected the pre-existing cloud membership to be left untouched after a roles PUT failure this Create did not cause, but it was removed")
	}

	for _, d := range diags {
		if strings.Contains(d.Detail(), "NOT modified or removed") {
			return
		}
	}
	t.Errorf("expected a diagnostic stating the pre-existing collaborator relationship was not modified or removed, got: %v", diags)
}

// TestCloudUserRoleCreate_SelfModify403GetsNamedDiagnostic is H23's
// regression test: the roles PUT 403ing because the target is the caller's
// own identity must get a clear, specific diagnostic, not a raw passthrough.
func TestCloudUserRoleCreate_SelfModify403GetsNamedDiagnostic(t *testing.T) {
	_, mock := newMockCloudUserRoleServer(t)
	r := &CloudUserRoleResource{}
	mock.seedOrgMember("me@example.com", "ide_me", "usr_me")

	plan := CloudUserRoleResourceModel{
		CloudID:   types.StringValue("cld_test"),
		Email:     types.StringValue("me@example.com"),
		BaseRole:  types.StringValue("owner"),
		DenyRoles: denyRolesNull(),
	}

	// Swap in a thin wrapper server that answers the roles PUT with the exact
	// self-modify 403 body, delegating everything else to the real mock.
	selfModifyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPut && strings.Contains(req.URL.Path, "/roles") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"error":{"detail":"You cannot modify your own role."}}`)
			return
		}
		mock.handle(w, req)
	}))
	defer selfModifyServer.Close()
	r.client = NewClientWithToken(selfModifyServer.URL, "test-token")

	_, diags := runCloudUserRoleCreate(t, r, plan)
	if !diags.HasError() {
		t.Fatal("expected an error for a self-modify 403, got none")
	}
	for _, d := range diags {
		if strings.Contains(d.Detail(), "own identity") {
			return
		}
	}
	t.Errorf("expected a diagnostic explaining the self-modify restriction, got: %v", diags)
}

// TestCloudUserRoleDelete_UnrepairableStateGetsExplanatoryDiagnosticNotRawPassthrough
// is H22/G8's central regression test, and the one architect specified as
// mutation-proof: the legacy DELETE 404ing (the only way the unrepairable
// state - a role granted outside Terraform without ever running the
// bootstrap - can ever surface) must produce the exact three-part explanation
// (granted outside Terraform, no API sequence can repair it, terraform state
// rm is the only clean exit), never a raw API error passthrough.
func TestCloudUserRoleDelete_UnrepairableStateGetsExplanatoryDiagnosticNotRawPassthrough(t *testing.T) {
	httpServer, mock := newMockCloudUserRoleServer(t)
	r := &CloudUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
	mock.seedOrgMember("orphan@example.com", "ide_orphan", "usr_orphan")
	mock.seedExternalGrant("usr_orphan", []string{"compute_config_viewer"}, []string{})
	// Deliberately no cloudPermissions entry - the H22/G8 trap state: a role
	// exists with no bootstrap-created membership edge.

	// PLACEBO GUARD, not a redundant check (architect ruling, correcting an
	// earlier standalone-delete proposal from assayer): the assertion below
	// only proves the diagnostic CONTAINS the explanatory phrases. It says
	// nothing about whether the mock's own RAW error text already happens to
	// contain them - a different, independent fact. If this exact fixture
	// string ever drifted to include wording like "state rm" or "granted
	// outside Terraform" (a careless copy-paste from a real API message,
	// say), a regressed raw-passthrough implementation would produce a
	// diagnostic that still contains the phrase by accident, and the
	// assertion below would pass vacuously - this test would become a silent
	// placebo without anyone touching its own logic. This precondition is
	// what keeps that from happening: it fails loudly if the fixture itself
	// ever starts overlapping with what only the real wrapping logic should
	// be adding.
	mockRawDetail := fmt.Sprintf("User with identity %s is not a member of clouds: {%s}.", "ide_orphan", "cld_test")
	if strings.Contains(mockRawDetail, "granted outside Terraform") || strings.Contains(mockRawDetail, "state rm") {
		t.Fatal("precondition failed: this mock's raw 404 text already contains wording the real explanatory diagnostic is supposed to add - the test below would no longer prove anything if this fires")
	}

	state := CloudUserRoleResourceModel{
		ID:         types.StringValue("cld_test/orphan@example.com"),
		CloudID:    types.StringValue("cld_test"),
		Email:      types.StringValue("orphan@example.com"),
		UserID:     types.StringValue("usr_orphan"),
		IdentityID: types.StringValue("ide_orphan"),
		BaseRole:   types.StringValue("compute_config_viewer"),
		DenyRoles:  denyRolesNull(),
	}

	diags := runCloudUserRoleDelete(t, r, state)
	if !diags.HasError() {
		t.Fatal("expected an error for the unrepairable state, got none")
	}
	for _, d := range diags {
		detail := d.Detail()
		if strings.Contains(detail, "granted outside Terraform") && strings.Contains(detail, "No API sequence") == false && strings.Contains(detail, "no API sequence") == false {
			continue
		}
		if strings.Contains(detail, "granted outside Terraform") &&
			(strings.Contains(detail, "No API sequence") || strings.Contains(detail, "no API sequence")) &&
			strings.Contains(detail, "state rm") {
			return
		}
	}
	t.Errorf("expected a diagnostic explaining the assignment was granted outside Terraform, that no API sequence can repair it, and that terraform state rm is the only clean exit - got: %v", diags)
}

// TestCloudUserRoleDelete_SurfacesAutoAddUser409AsNamedDiagnostic is H17's
// regression test: the raw 409 from the backend must be translated into a
// diagnostic that names auto_add_user specifically, not left as an opaque
// wrapped status code. Uses the exact live-confirmed detail text.
func TestCloudUserRoleDelete_SurfacesAutoAddUser409AsNamedDiagnostic(t *testing.T) {
	httpServer, mock := newMockCloudUserRoleServer(t)
	r := &CloudUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
	mock.seedOrgMember("t@example.com", "ide_target", "usr_target")
	mock.failDelete = &failureInjection{status: http.StatusConflict, detail: "Users cannot be removed from clouds which have auto add users enabled."}

	state := CloudUserRoleResourceModel{
		ID:         types.StringValue("cld_test/t@example.com"),
		CloudID:    types.StringValue("cld_test"),
		Email:      types.StringValue("t@example.com"),
		UserID:     types.StringValue("usr_target"),
		IdentityID: types.StringValue("ide_target"),
		BaseRole:   types.StringValue("writer"),
		DenyRoles:  denyRolesNull(),
	}

	diags := runCloudUserRoleDelete(t, r, state)
	if !diags.HasError() {
		t.Fatal("expected an error, got none")
	}
	for _, d := range diags {
		if strings.Contains(d.Detail(), "auto_add_user") {
			return
		}
	}
	t.Errorf("expected a diagnostic naming auto_add_user specifically, got: %v", diags)
}

// TestCloudUserRoleDelete_SurfacesSelfRemoval403AsNamedDiagnostic confirms the
// cloud-specific self-removal 403 ("cannot remove yourself from the cloud",
// distinct wording from the org-level equivalent this provider already
// handles elsewhere) gets a clear, actionable diagnostic too.
func TestCloudUserRoleDelete_SurfacesSelfRemoval403AsNamedDiagnostic(t *testing.T) {
	httpServer, mock := newMockCloudUserRoleServer(t)
	r := &CloudUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
	mock.seedOrgMember("self@example.com", "ide_self", "usr_self")
	mock.failDelete = &failureInjection{status: http.StatusForbidden, detail: "You cannot remove yourself from the cloud."}

	state := CloudUserRoleResourceModel{
		ID:         types.StringValue("cld_test/self@example.com"),
		CloudID:    types.StringValue("cld_test"),
		Email:      types.StringValue("self@example.com"),
		UserID:     types.StringValue("usr_self"),
		IdentityID: types.StringValue("ide_self"),
		BaseRole:   types.StringValue("owner"),
		DenyRoles:  denyRolesNull(),
	}

	diags := runCloudUserRoleDelete(t, r, state)
	if !diags.HasError() {
		t.Fatal("expected an error, got none")
	}
	for _, d := range diags {
		if strings.Contains(d.Detail(), "own identity") {
			return
		}
	}
	t.Errorf("expected a diagnostic explaining the self-removal restriction, got: %v", diags)
}

// TestCloudUserRoleDelete_SucceedsForAProperlyBootstrappedRole confirms the
// ordinary, healthy path still works: a role this resource's own Create (or
// an equivalent legacy bootstrap) established deletes cleanly.
func TestCloudUserRoleDelete_SucceedsForAProperlyBootstrappedRole(t *testing.T) {
	httpServer, mock := newMockCloudUserRoleServer(t)
	r := &CloudUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
	mock.seedOrgMember("healthy@example.com", "ide_healthy", "usr_healthy")
	mock.cloudPermissions["usr_healthy"] = "readonly"
	mock.roles["usr_healthy"] = CloudUserRolesResult{UserID: "usr_healthy", BaseRoles: []string{"writer"}, DenyRoles: []string{}}

	state := CloudUserRoleResourceModel{
		ID:         types.StringValue("cld_test/healthy@example.com"),
		CloudID:    types.StringValue("cld_test"),
		Email:      types.StringValue("healthy@example.com"),
		UserID:     types.StringValue("usr_healthy"),
		IdentityID: types.StringValue("ide_healthy"),
		BaseRole:   types.StringValue("writer"),
		DenyRoles:  denyRolesNull(),
	}

	diags := runCloudUserRoleDelete(t, r, state)
	if diags.HasError() {
		t.Fatalf("expected a clean delete for a properly bootstrapped role, got: %v", diags)
	}
	if mock.hasCloudPermission("usr_healthy") {
		t.Error("expected the cloud permission to actually be removed")
	}
}

// TestReadCloudUserRoleIntoModelIfPresent_LenNotOneLeavesModelUntouched is
// H4's regression test: an anomalous 0 or 2+ base_roles result must never
// crash or silently pick an element - it must leave whatever was already in
// the model alone and surface a warning instead.
func TestReadCloudUserRoleIntoModelIfPresent_LenNotOneLeavesModelUntouched(t *testing.T) {
	for _, tc := range []struct {
		name      string
		baseRoles []string
	}{
		{"zero roles", []string{}},
		{"two roles", []string{"owner", "writer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			httpServer, mock := newMockCloudUserRoleServer(t)
			client := NewClientWithToken(httpServer.URL, "test-token")

			mock.roles["usr_drifted"] = CloudUserRolesResult{UserID: "usr_drifted", BaseRoles: tc.baseRoles, DenyRoles: []string{}}

			model := CloudUserRoleResourceModel{
				BaseRole:  types.StringValue("sentinel-prior-value"),
				DenyRoles: denyRolesNull(),
			}

			found, diags := readCloudUserRoleIntoModelIfPresent(context.Background(), client, "cld_test", "usr_drifted", &model)
			if !found {
				t.Fatal("expected found=true (the user has SOME role entry), got false")
			}
			if diags.WarningsCount() == 0 {
				t.Errorf("expected a warning diagnostic for an unexpected role count, got: %v", diags)
			}
			if model.BaseRole.ValueString() != "sentinel-prior-value" {
				t.Errorf("expected base_role left untouched at the prior value, got %q", model.BaseRole.ValueString())
			}
		})
	}
}

// TestReadCloudUserRoleIntoModelIfPresent_NotFoundReturnsFalse confirms a
// user with no role entry at all on this cloud is reported as not found,
// rather than as a zero-length-roles drift case - a genuinely different state.
func TestReadCloudUserRoleIntoModelIfPresent_NotFoundReturnsFalse(t *testing.T) {
	httpServer, _ := newMockCloudUserRoleServer(t)
	client := NewClientWithToken(httpServer.URL, "test-token")

	var model CloudUserRoleResourceModel
	found, diags := readCloudUserRoleIntoModelIfPresent(context.Background(), client, "cld_test", "usr_never_existed", &model)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if found {
		t.Error("expected found=false for a user with no role entry at all")
	}
}

// TestResolveIdentityForEmail_ResolvesBothIDs confirms the org-wide,
// email-keyed resolution this resource depends on for everything (Create,
// Update via state, Import) returns both identity_id and user_id from one
// call, case-insensitively.
func TestResolveIdentityForEmail_ResolvesBothIDs(t *testing.T) {
	httpServer, mock := newMockCloudUserRoleServer(t)
	client := NewClientWithToken(httpServer.URL, "test-token")
	mock.seedOrgMember("Mixed.Case@Example.com", "ide_mixed", "usr_mixed")

	identityID, userID, err := resolveIdentityForEmail(context.Background(), client, "mixed.case@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if identityID != "ide_mixed" || userID != "usr_mixed" {
		t.Errorf("got identityID=%q userID=%q, want identityID=%q userID=%q", identityID, userID, "ide_mixed", "usr_mixed")
	}
}

// TestResolveIdentityForEmail_NotFoundErrors confirms a clean error (not a
// panic or a silently empty result) for an email with no matching org member.
func TestResolveIdentityForEmail_NotFoundErrors(t *testing.T) {
	httpServer, _ := newMockCloudUserRoleServer(t)
	client := NewClientWithToken(httpServer.URL, "test-token")

	_, _, err := resolveIdentityForEmail(context.Background(), client, "nobody@example.com")
	if err == nil {
		t.Fatal("expected an error for an email with no matching org member, got nil")
	}
}
