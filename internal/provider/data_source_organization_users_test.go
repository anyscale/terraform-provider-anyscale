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
