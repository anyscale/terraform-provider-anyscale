package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// userOrganizationObjectType mirrors the object type Read() builds inline via
// types.ListValueFrom for the "organizations" attribute.
func userOrganizationObjectType() types.ObjectType {
	return types.ObjectType{
		AttrTypes: map[string]attr.Type{
			"id":                types.StringType,
			"name":              types.StringType,
			"public_identifier": types.StringType,
			"default_cloud_id":  types.StringType,
		},
	}
}

// runUserDataSourceRead drives UserDataSource's real Read() method end-to-end,
// same construction pattern as runProjectsDataSourceRead in
// data_source_projects_test.go. Replaces the old data_source_user_test.go,
// whose ~13 tests all constructed a UserDataSourceModel/OrganizationModel
// literal directly and asserted it against itself - none called Read(), none
// used a mock server, none exercised any real conversion logic, so none of
// them could ever have caught DS-USER-1/2/3.
func runUserDataSourceRead(t *testing.T, d *UserDataSource) (UserDataSourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	// UserDataSourceModel's list-typed fields are all raw types.List, which need
	// an explicit, correctly-typed null value before state.Set can reconcile a
	// zero-value model against the schema (an untyped zero-value types.List
	// otherwise fails with a "MISSING TYPE" conversion error). This data source
	// takes no config arguments, so this covers every case.
	model := UserDataSourceModel{
		OrganizationIDs: types.ListNull(types.StringType),
		Organizations:   types.ListNull(userOrganizationObjectType()),
		CloudIDs:        types.ListNull(types.StringType),
		UserGroupIDs:    types.ListNull(types.StringType),
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	setDiags := state.Set(ctx, &model)
	if setDiags.HasError() {
		t.Fatalf("failed to build config fixture: %v", setDiags)
	}
	config := tfsdk.Config(state)

	readResp := &datasource.ReadResponse{
		State: tfsdk.State(config),
	}
	d.Read(ctx, datasource.ReadRequest{Config: config}, readResp)

	if readResp.Diagnostics.HasError() {
		return UserDataSourceModel{}, readResp.Diagnostics
	}

	var result UserDataSourceModel
	getDiags := readResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, readResp.Diagnostics
}

// userInfoMockServer builds an httptest server that serves the given userinfo
// and user_groups JSON bodies (both already-JSON-encoded strings) for
// /api/v2/userinfo, /api/v2/clouds, and /api/v2/user_groups respectively -
// the three endpoints UserDataSource.Read calls.
func userInfoMockServer(t *testing.T, userInfoJSON, cloudsJSON, userGroupsJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/userinfo":
			_, _ = fmt.Fprint(w, userInfoJSON)
		case "/api/v2/clouds":
			_, _ = fmt.Fprint(w, cloudsJSON)
		case "/api/v2/user_groups":
			_, _ = fmt.Fprint(w, userGroupsJSON)
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

const emptyCloudsResponse = `{"results": [], "metadata": {"next_paging_token": null}}`

// TestUserDataSourceRead_FieldMapping is a real, Read()-driven replacement for
// the old file's ~6 single-field tautologies (TestUserDataSourceFieldMapping,
// TestUserOrganizationIDs, TestUserCloudIDs, TestOrganizationModelMapping,
// TestUserAPIResponseStructure, TestUserMultipleOrganizations): one mock
// userinfo response, asserting every top-level and nested field actually maps
// through the real Read() method.
func TestUserDataSourceRead_FieldMapping(t *testing.T) {
	userInfoJSON := `{
		"result": {
			"id": "usr_abc123",
			"email": "user@example.com",
			"name": "John Doe",
			"username": "johndoe",
			"organization_permission_level": "owner",
			"organization_ids": ["org_1", "org_2"],
			"organizations": [
				{"id": "org_1", "name": "Org One", "public_identifier": "org-one", "default_cloud_id": "cld_1"},
				{"id": "org_2", "name": "Org Two", "public_identifier": "org-two", "default_cloud_id": "cld_2"}
			]
		}
	}`
	cloudsJSON := `{"results": [{"id": "cld_1"}, {"id": "cld_2"}, {"id": "cld_3"}], "metadata": {"next_paging_token": null}}`
	userGroupsJSON := `{"results": [{"id": "grp_1"}], "metadata": {"next_paging_token": null}}`

	server := userInfoMockServer(t, userInfoJSON, cloudsJSON, userGroupsJSON)
	defer server.Close()

	d := &UserDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runUserDataSourceRead(t, d)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if got := result.ID.ValueString(); got != "usr_abc123" {
		t.Errorf("ID = %q, want usr_abc123", got)
	}
	if got := result.Email.ValueString(); got != "user@example.com" {
		t.Errorf("Email = %q, want user@example.com", got)
	}
	if got := result.Name.ValueString(); got != "John Doe" {
		t.Errorf("Name = %q, want John Doe", got)
	}
	if got := result.Username.ValueString(); got != "johndoe" {
		t.Errorf("Username = %q, want johndoe", got)
	}
	if got := result.OrganizationPermissionLevel.ValueString(); got != "owner" {
		t.Errorf("OrganizationPermissionLevel = %q, want owner", got)
	}

	var orgIDs []string
	if diags := result.OrganizationIDs.ElementsAs(context.Background(), &orgIDs, false); diags.HasError() {
		t.Fatalf("failed to decode organization_ids: %v", diags)
	}
	if len(orgIDs) != 2 || orgIDs[0] != "org_1" || orgIDs[1] != "org_2" {
		t.Errorf("OrganizationIDs = %v, want [org_1 org_2]", orgIDs)
	}

	var cloudIDs []string
	if diags := result.CloudIDs.ElementsAs(context.Background(), &cloudIDs, false); diags.HasError() {
		t.Fatalf("failed to decode cloud_ids: %v", diags)
	}
	if len(cloudIDs) != 3 {
		t.Errorf("CloudIDs count = %d, want 3", len(cloudIDs))
	}

	var userGroupIDs []string
	if diags := result.UserGroupIDs.ElementsAs(context.Background(), &userGroupIDs, false); diags.HasError() {
		t.Fatalf("failed to decode user_group_ids: %v", diags)
	}
	if len(userGroupIDs) != 1 || userGroupIDs[0] != "grp_1" {
		t.Errorf("UserGroupIDs = %v, want [grp_1]", userGroupIDs)
	}

	type orgOut struct {
		ID               types.String `tfsdk:"id"`
		Name             types.String `tfsdk:"name"`
		PublicIdentifier types.String `tfsdk:"public_identifier"`
		DefaultCloudID   types.String `tfsdk:"default_cloud_id"`
	}
	var orgs []orgOut
	if diags := result.Organizations.ElementsAs(context.Background(), &orgs, false); diags.HasError() {
		t.Fatalf("failed to decode organizations: %v", diags)
	}
	if len(orgs) != 2 {
		t.Fatalf("Organizations count = %d, want 2", len(orgs))
	}
	if orgs[0].ID.ValueString() != "org_1" || orgs[0].DefaultCloudID.ValueString() != "cld_1" {
		t.Errorf("Organizations[0] = %+v, want id=org_1 default_cloud_id=cld_1", orgs[0])
	}
	if orgs[1].ID.ValueString() != "org_2" || orgs[1].DefaultCloudID.ValueString() != "cld_2" {
		t.Errorf("Organizations[1] = %+v, want id=org_2 default_cloud_id=cld_2", orgs[1])
	}
}

// TestUserDataSourceRead_NullDefaultCloudID is the DS-USER-1 mutation-proof
// regression guard. The real /api/v2/userinfo response can and does return
// default_cloud_id: null for an organization with no default cloud set
// (confirmed live against the connected test org's own account, not just
// hypothesized) - Read()'s anonymous response struct types DefaultCloudID as
// a plain string, so JSON null silently decodes to the Go zero value ""
// before any application code even runs, and types.StringValue("") is what
// lands in state. This currently FAILS (state comes back "" not null) - that
// failure is the mutation-proof evidence. Must pass once DefaultCloudID (both
// the anonymous struct field and OrganizationModel) is *string +
// types.StringPointerValue.
func TestUserDataSourceRead_NullDefaultCloudID(t *testing.T) {
	userInfoJSON := `{
		"result": {
			"id": "usr_abc123",
			"email": "user@example.com",
			"name": "John Doe",
			"username": "johndoe",
			"organization_permission_level": "owner",
			"organization_ids": ["org_1"],
			"organizations": [
				{"id": "org_1", "name": "Org One", "public_identifier": "org-one", "default_cloud_id": null}
			]
		}
	}`

	server := userInfoMockServer(t, userInfoJSON, emptyCloudsResponse, emptyCloudsResponse)
	defer server.Close()

	d := &UserDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runUserDataSourceRead(t, d)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	type orgOut struct {
		ID               types.String `tfsdk:"id"`
		Name             types.String `tfsdk:"name"`
		PublicIdentifier types.String `tfsdk:"public_identifier"`
		DefaultCloudID   types.String `tfsdk:"default_cloud_id"`
	}
	var orgs []orgOut
	if diags := result.Organizations.ElementsAs(context.Background(), &orgs, false); diags.HasError() {
		t.Fatalf("failed to decode organizations: %v", diags)
	}
	if len(orgs) != 1 {
		t.Fatalf("Organizations count = %d, want 1", len(orgs))
	}
	if !orgs[0].DefaultCloudID.IsNull() {
		t.Errorf("organizations[0].default_cloud_id = %#v, want null for a nil API value, got a non-null value (likely \"\")", orgs[0].DefaultCloudID)
	}
}

// TestUserDataSourceRead_NullOrganizationPermissionLevel is the DS-USER-2
// mutation-proof regression guard - same shape as DS-USER-1 above, for the
// top-level organization_permission_level field (Optional/unassigned upstream
// -> null). This currently FAILS the same way.
func TestUserDataSourceRead_NullOrganizationPermissionLevel(t *testing.T) {
	userInfoJSON := `{
		"result": {
			"id": "usr_abc123",
			"email": "user@example.com",
			"name": "John Doe",
			"username": "johndoe",
			"organization_permission_level": null,
			"organization_ids": [],
			"organizations": []
		}
	}`

	server := userInfoMockServer(t, userInfoJSON, emptyCloudsResponse, emptyCloudsResponse)
	defer server.Close()

	d := &UserDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runUserDataSourceRead(t, d)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if !result.OrganizationPermissionLevel.IsNull() {
		t.Errorf("organization_permission_level = %#v, want null for a nil API value, got a non-null value (likely \"\")", result.OrganizationPermissionLevel)
	}
}

// TestUserDataSourceRead_UserGroupIDsPagesBeyondFirstPage is the DS-USER-3
// mutation-proof regression guard. Read() fetches /api/v2/user_groups via a
// single raw DoRequest call rather than PaginatedRequest, and an inline
// comment in the production code claims (incorrectly - the real endpoint's
// response does carry a next_paging_token field) that the endpoint is
// non-paginated. A user group past page 1 is silently dropped. This currently
// FAILS - the mock's second page is never requested - which is the
// mutation-proof evidence. Must pass once Read() pages through
// /api/v2/user_groups via PaginatedRequest.
func TestUserDataSourceRead_UserGroupIDsPagesBeyondFirstPage(t *testing.T) {
	userInfoJSON := `{
		"result": {
			"id": "usr_abc123",
			"email": "user@example.com",
			"name": "John Doe",
			"username": "johndoe",
			"organization_permission_level": "owner",
			"organization_ids": [],
			"organizations": []
		}
	}`

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/userinfo":
			_, _ = fmt.Fprint(w, userInfoJSON)
		case "/api/v2/clouds":
			_, _ = fmt.Fprint(w, emptyCloudsResponse)
		case "/api/v2/user_groups":
			requestCount++
			if requestCount == 1 {
				_, _ = fmt.Fprint(w, `{"results": [{"id": "grp_1"}], "metadata": {"next_paging_token": "page2"}}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"results": [{"id": "grp_2"}], "metadata": {"next_paging_token": null}}`)
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &UserDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runUserDataSourceRead(t, d)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	var userGroupIDs []string
	if diags := result.UserGroupIDs.ElementsAs(context.Background(), &userGroupIDs, false); diags.HasError() {
		t.Fatalf("failed to decode user_group_ids: %v", diags)
	}
	if len(userGroupIDs) != 2 {
		t.Fatalf("UserGroupIDs = %v, want 2 groups across both pages (grp_1, grp_2)", userGroupIDs)
	}
	if requestCount != 2 {
		t.Errorf("expected 2 requests to /api/v2/user_groups (one per page), got %d", requestCount)
	}
}
