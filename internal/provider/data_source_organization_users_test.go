package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// organizationUserObjectType mirrors the object type Read() builds inline via
// types.ListValueFrom - needed here so a zero-value OrganizationUsersDataSourceModel
// has a properly-typed null Users list instead of an untyped one the framework
// cannot reconcile against the schema.
func organizationUserObjectType() types.ObjectType {
	return types.ObjectType{
		AttrTypes: map[string]attr.Type{
			"id":               types.StringType,
			"user_id":          types.StringType,
			"name":             types.StringType,
			"email":            types.StringType,
			"permission_level": types.StringType,
			"created_at":       types.StringType,
			"base_role":        types.StringType,
			"additional_roles": types.ListType{ElemType: types.StringType},
		},
	}
}

// runOrganizationUsersDataSourceRead drives OrganizationUsersDataSource's real
// Read() method end-to-end, same construction pattern as
// runProjectsDataSourceRead in data_source_projects_test.go.
func runOrganizationUsersDataSourceRead(t *testing.T, d *OrganizationUsersDataSource, model OrganizationUsersDataSourceModel) (OrganizationUsersDataSourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	if model.Users.ElementType(ctx) == nil {
		model.Users = types.ListNull(organizationUserObjectType())
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
		return OrganizationUsersDataSourceModel{}, readResp.Diagnostics
	}

	var result OrganizationUsersDataSourceModel
	getDiags := readResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, readResp.Diagnostics
}

// TestOrganizationUsersDataSource_NullName is the DS-OU-1 mutation-proof
// regression guard for the plural data source (no unit test existed for this
// data source at all before this file). Same bug as the singular's
// organizationCollaboratorToUserModel: the Read loop collapses a nil API name
// into "" instead of Terraform null. This currently FAILS - that failure is
// the mutation-proof evidence. Must pass once the Read loop uses
// types.StringPointerValue for Name.
func TestOrganizationUsersDataSource_NullName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [
				{"id": "identity-1", "email": "svc@example.com", "name": null, "permission_level": "collaborator", "created_at": "2024-01-01T00:00:00Z"}
			],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	d := &OrganizationUsersDataSource{client: NewClientWithToken(server.URL, "test-token")}

	result, diags := runOrganizationUsersDataSourceRead(t, d, OrganizationUsersDataSourceModel{})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	users := make([]OrganizationUserModel, 0)
	if diags := result.Users.ElementsAs(context.Background(), &users, false); diags.HasError() {
		t.Fatalf("failed to decode users list: %v", diags)
	}

	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if !users[0].Name.IsNull() {
		t.Errorf("Name = %#v, want null for a nil API name, got a non-null value (likely \"\")", users[0].Name)
	}
}

// TestOrganizationUsersDataSource_NonNullName guards the other side of the
// same fix: a real name must still come through as its exact value.
func TestOrganizationUsersDataSource_NonNullName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [
				{"id": "identity-1", "email": "ada@example.com", "name": "Ada Lovelace", "permission_level": "owner", "created_at": "2024-01-01T00:00:00Z"}
			],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	d := &OrganizationUsersDataSource{client: NewClientWithToken(server.URL, "test-token")}

	result, diags := runOrganizationUsersDataSourceRead(t, d, OrganizationUsersDataSourceModel{})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	users := make([]OrganizationUserModel, 0)
	if diags := result.Users.ElementsAs(context.Background(), &users, false); diags.HasError() {
		t.Fatalf("failed to decode users list: %v", diags)
	}

	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].Name.IsNull() {
		t.Fatal("Name = null, want the real name to come through")
	}
	if got := users[0].Name.ValueString(); got != "Ada Lovelace" {
		t.Errorf("Name = %q, want %q", got, "Ada Lovelace")
	}
}

// TestOrganizationUsersDataSource_FiltersForwardedAndRolesBackfilled is architect's assigned
// verification for the list data source's read-path fix: the email/name/is_service_account
// filters must keep working exactly as before (list-and-filter stays primary per the ruling
// that POST /search cannot replace it - search has no is_service_account and only a combined
// name_or_email field), AND additional_roles must now be genuinely backfilled per result via the
// singular per-user GET (hydrateCollaboratorRoles), not list's hardcoded-empty value. This
// currently fails to even compile against the unmodified data source - hydrateCollaboratorRoles
// doesn't exist yet in this worktree - which is itself the fail-without-fix proof; it must
// compile and pass once forge's fix is integrated.
func TestOrganizationUsersDataSource_FiltersForwardedAndRolesBackfilled(t *testing.T) {
	var sawListParams url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/organization_collaborators":
			sawListParams = r.URL.Query()
			_, _ = fmt.Fprint(w, `{
				"results": [
					{"id": "identity-1", "email": "populated@example.com", "name": "Has Roles", "permission_level": "owner", "base_role": "owner", "additional_roles": [], "created_at": "2024-01-01T00:00:00Z", "user_id": "usr_populated"},
					{"id": "identity-2", "email": "empty@example.com", "name": "No Extra Roles", "permission_level": "collaborator", "base_role": "collaborator", "additional_roles": [], "created_at": "2024-01-01T00:00:00Z", "user_id": "usr_empty"},
					{"id": "identity-3", "email": "nouserid@example.com", "name": "Service-ish", "permission_level": "collaborator", "base_role": "collaborator", "additional_roles": [], "created_at": "2024-01-01T00:00:00Z"}
				],
				"metadata": {"total": 3, "next_paging_token": null}
			}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/organization_collaborators/usr_populated":
			_, _ = fmt.Fprint(w, `{"result":{"id":"identity-1","email":"populated@example.com","name":"Has Roles","permission_level":"owner","base_role":"owner","additional_roles":["image_reader"],"user_id":"usr_populated"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/organization_collaborators/usr_empty":
			_, _ = fmt.Fprint(w, `{"result":{"id":"identity-2","email":"empty@example.com","name":"No Extra Roles","permission_level":"collaborator","base_role":"collaborator","additional_roles":[],"user_id":"usr_empty"}}`)
		default:
			t.Errorf("unexpected request (identity-3 has no user_id, its singular GET must never be attempted): %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &OrganizationUsersDataSource{client: NewClientWithToken(server.URL, "test-token")}

	result, diags := runOrganizationUsersDataSourceRead(t, d, OrganizationUsersDataSourceModel{
		Email:            types.StringValue("example.com"),
		Name:             types.StringValue("Has"),
		IsServiceAccount: types.BoolValue(false),
	})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	// Filters must still be forwarded server-side exactly as before - this is
	// the "did not lose a capability while fixing the bug" half of the check.
	if sawListParams.Get("email") != "example.com" {
		t.Errorf("email filter = %q, want %q forwarded to the list endpoint", sawListParams.Get("email"), "example.com")
	}
	if sawListParams.Get("name") != "Has" {
		t.Errorf("name filter = %q, want %q forwarded to the list endpoint", sawListParams.Get("name"), "Has")
	}
	if sawListParams.Get("is_service_account") != "false" {
		t.Errorf("is_service_account filter = %q, want %q forwarded to the list endpoint", sawListParams.Get("is_service_account"), "false")
	}

	users := make([]OrganizationUserModel, 0)
	if diags := result.Users.ElementsAs(context.Background(), &users, false); diags.HasError() {
		t.Fatalf("failed to decode users list: %v", diags)
	}
	if len(users) != 3 {
		t.Fatalf("expected 3 users, got %d", len(users))
	}

	byEmail := make(map[string]OrganizationUserModel, len(users))
	for _, u := range users {
		byEmail[u.Email.ValueString()] = u
	}

	populated := byEmail["populated@example.com"]
	if populated.AdditionalRoles.IsNull() {
		t.Error("populated user: additional_roles = null, want a populated, non-null list (backfilled from the singular GET)")
	}
	var populatedRoles []string
	if diags := populated.AdditionalRoles.ElementsAs(context.Background(), &populatedRoles, false); diags.HasError() || len(populatedRoles) != 1 || populatedRoles[0] != "image_reader" {
		t.Errorf("populated user: additional_roles = %v, want [image_reader]", populatedRoles)
	}

	empty := byEmail["empty@example.com"]
	if empty.AdditionalRoles.IsNull() {
		t.Error("empty user: additional_roles = null, want a non-null EMPTY list (queried and genuinely none, not the same as undetermined)")
	}
	if !empty.AdditionalRoles.IsNull() {
		var emptyRoles []string
		if diags := empty.AdditionalRoles.ElementsAs(context.Background(), &emptyRoles, false); diags.HasError() || len(emptyRoles) != 0 {
			t.Errorf("empty user: additional_roles = %v, want an empty (not null, not populated) list", emptyRoles)
		}
	}

	noUserID := byEmail["nouserid@example.com"]
	if !noUserID.AdditionalRoles.IsNull() {
		t.Errorf("no-user_id user: additional_roles = %v, want null (undetermined - the singular GET is user_id-keyed and can never be reached for this collaborator)", noUserID.AdditionalRoles)
	}
}
