package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// runProjectDataSourceRead drives ProjectDataSource's real Read() method
// end-to-end against a config model, the same pattern used in
// data_source_container_image_test.go's runContainerImageDataSourceRead.
//
// tfsdk.Config has no Set method (unlike tfsdk.Plan/tfsdk.State), so this
// builds the raw value through a throwaway tfsdk.State.Set and converts it to
// a tfsdk.Config -- both types share the same underlying {Raw, Schema} shape.
func runProjectDataSourceRead(t *testing.T, d *ProjectDataSource, model ProjectDataSourceModel) (ProjectDataSourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	setDiags := state.Set(ctx, &model)
	if setDiags.HasError() {
		t.Fatalf("failed to build config fixture: %v", setDiags)
	}
	config := tfsdk.Config(state)

	readResp := &datasource.ReadResponse{
		// The real runtime pre-populates ReadResponse.State from ReadRequest.Config.
		State: tfsdk.State(config),
	}
	d.Read(ctx, datasource.ReadRequest{Config: config}, readResp)

	if readResp.Diagnostics.HasError() {
		return ProjectDataSourceModel{}, readResp.Diagnostics
	}

	var result ProjectDataSourceModel
	getDiags := readResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, readResp.Diagnostics
}

// TestProjectDataSourceRead_LookupValidation replaces the old
// TestProjectDataSourceLookupValidation, which re-implemented the id/name
// requirement inline instead of calling Read(). This also proves the
// validation short-circuits before any HTTP call.
func TestProjectDataSourceRead_LookupValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s: validation must short-circuit before any API call", r.Method, r.URL.String())
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	d := &ProjectDataSource{client: NewClientWithToken(server.URL, "test-token")}
	_, diags := runProjectDataSourceRead(t, d, ProjectDataSourceModel{})

	if !diags.HasError() {
		t.Fatal("expected a diagnostic error, got none")
	}
	if !diagsContainSummary(diags, "Missing Required Attribute") {
		t.Errorf("expected 'Missing Required Attribute' diagnostic, got: %v", diags)
	}
}

// TestProjectDataSourceRead_ByID replaces the old tautological
// TestProjectFieldMapping/TestProjectCollaboratorMapping/
// TestProjectCollaboratorList (which built model structs and checked they
// held the fields they were given). This drives the real Read() against a
// mock API and checks the fields it actually produces, including collaborator
// mapping and ordering.
func TestProjectDataSourceRead_ByID(t *testing.T) {
	const projectID = "prj_abc"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/"+projectID:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ProjectResponse{Result: ProjectResult{
				ID:              projectID,
				Name:            "production-project",
				Description:     strPtr("Production environment project"),
				ParentCloudID:   strPtr("cld_def"),
				CreatorID:       strPtr("user_123"),
				CreatedAt:       "2024-01-01T00:00:00Z",
				LastUsedCloudID: strPtr("cld_def"),
				IsDefault:       false,
				DirectoryName:   "production-project-dir",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/"+projectID+"/collaborators/users":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ProjectCollaboratorListResponse{
				Results: []ProjectCollaboratorResult{
					{
						ID:              "identity_1",
						PermissionLevel: "owner",
						Value: struct {
							ID    string `json:"id"`
							Name  string `json:"name"`
							Email string `json:"email"`
						}{ID: "user_1", Name: "User One", Email: "user1@example.com"},
					},
					{
						ID:              "identity_2",
						PermissionLevel: "write",
						Value: struct {
							ID    string `json:"id"`
							Name  string `json:"name"`
							Email string `json:"email"`
						}{ID: "user_2", Name: "User Two", Email: "user2@example.com"},
					},
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &ProjectDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runProjectDataSourceRead(t, d, ProjectDataSourceModel{ID: types.StringValue(projectID)})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if result.Name.ValueString() != "production-project" {
		t.Errorf("name = %q, want %q", result.Name.ValueString(), "production-project")
	}
	if result.CloudID.ValueString() != "cld_def" {
		t.Errorf("cloud_id = %q, want %q", result.CloudID.ValueString(), "cld_def")
	}
	if result.Description.ValueString() != "Production environment project" {
		t.Errorf("description = %q, want %q", result.Description.ValueString(), "Production environment project")
	}
	if result.IsDefault.ValueBool() {
		t.Error("is_default = true, want false")
	}
	if len(result.Collaborators) != 2 {
		t.Fatalf("collaborators count = %d, want 2", len(result.Collaborators))
	}
	if result.Collaborators[0].Email.ValueString() != "user1@example.com" || result.Collaborators[0].PermissionLevel.ValueString() != "owner" ||
		result.Collaborators[0].IdentityID.ValueString() != "identity_1" || result.Collaborators[0].UserID.ValueString() != "user_1" {
		t.Errorf("collaborators[0] = %+v, want email=user1@example.com permission_level=owner identity_id=identity_1 user_id=user_1", result.Collaborators[0])
	}
	if result.Collaborators[1].Email.ValueString() != "user2@example.com" || result.Collaborators[1].PermissionLevel.ValueString() != "write" {
		t.Errorf("collaborators[1] = %+v, want email=user2@example.com permission_level=write", result.Collaborators[1])
	}
}

// TestProjectDataSourceRead_NullableFieldsAndDefaultFlag replaces the old
// TestProjectNullableFields/TestProjectDefaultFlag/TestProjectEmptyCollaborators,
// which all built model structs by hand instead of exercising Read(). A
// project with none of the optional fields set (legacy/default-project shape)
// must come back with those fields null, not zero-valued/empty-string.
func TestProjectDataSourceRead_NullableFieldsAndDefaultFlag(t *testing.T) {
	const projectID = "prj_legacy_default"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/"+projectID:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ProjectResponse{Result: ProjectResult{
				ID:            projectID,
				Name:          "default-project",
				ParentCloudID: strPtr("cld_def"),
				CreatedAt:     "2024-01-01T00:00:00Z",
				IsDefault:     true,
				DirectoryName: "default-project-dir",
				// Description, CreatorID, LastUsedCloudID intentionally absent.
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/"+projectID+"/collaborators/users":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ProjectCollaboratorListResponse{Results: []ProjectCollaboratorResult{}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &ProjectDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runProjectDataSourceRead(t, d, ProjectDataSourceModel{ID: types.StringValue(projectID)})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if !result.Description.IsNull() {
		t.Errorf("description should be null when absent from the API, got %q", result.Description.ValueString())
	}
	if !result.CreatorID.IsNull() {
		t.Errorf("creator_id should be null when absent from the API, got %q", result.CreatorID.ValueString())
	}
	if !result.LastUsedCloudID.IsNull() {
		t.Errorf("last_used_cloud_id should be null when absent from the API, got %q", result.LastUsedCloudID.ValueString())
	}
	if !result.IsDefault.ValueBool() {
		t.Error("is_default = false, want true")
	}
	if len(result.Collaborators) != 0 {
		t.Errorf("collaborators count = %d, want 0", len(result.Collaborators))
	}
}

// TestProjectDataSourceRead_NullParentCloudID is the DS-PROJ-1 mutation-proof
// regression guard. ProjectResult.ParentCloudID (models.go) is a plain string,
// not *string like its Description/CreatorID/LastUsedCloudID siblings, even
// though the real parent_cloud_id API field is Optional[str] - a JSON null
// silently decodes to the Go zero value "" before any application code runs,
// so no nil-guard in Read() could catch this even if one existed. This uses a
// raw JSON response body (rather than building a ProjectResult literal, which
// cannot express "null" for a plain string field) to prove it: this currently
// FAILS - cloud_id comes back "" not null - which is the mutation-proof
// evidence. Must pass once ParentCloudID is *string + StringPointerValue.
func TestProjectDataSourceRead_NullParentCloudID(t *testing.T) {
	const projectID = "prj_no_cloud"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/"+projectID:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": {
				"id": "` + projectID + `",
				"name": "no-cloud-project",
				"parent_cloud_id": null,
				"created_at": "2024-01-01T00:00:00Z",
				"is_default": false,
				"directory_name": "no-cloud-project-dir"
			}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/"+projectID+"/collaborators/users":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ProjectCollaboratorListResponse{Results: []ProjectCollaboratorResult{}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &ProjectDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runProjectDataSourceRead(t, d, ProjectDataSourceModel{ID: types.StringValue(projectID)})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if !result.CloudID.IsNull() {
		t.Errorf("cloud_id = %#v, want null for a nil parent_cloud_id, got a non-null value (likely \"\")", result.CloudID)
	}
}

// TestProjectDataSourceRead_ByName exercises findProjectByName for real,
// including the multiple-matches-picks-most-recent behavior, which was
// previously only checked for cloud names (TestProjectCloudNameResolution
// duplicated logic already covered for real by
// cloud_helpers_test.go:TestResolveCloudNameToID and never actually tested
// the project-lookup-by-name path at all).
func TestProjectDataSourceRead_ByName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ProjectsListResponse{Results: []ProjectResult{
				{ID: "prj_old", Name: "my-project", ParentCloudID: strPtr("cld_1"), CreatedAt: "2023-01-01T00:00:00Z", DirectoryName: "d1"},
				{ID: "prj_new", Name: "my-project", ParentCloudID: strPtr("cld_1"), CreatedAt: "2024-06-01T00:00:00Z", DirectoryName: "d2"},
				{ID: "prj_other", Name: "other-project", ParentCloudID: strPtr("cld_1"), CreatedAt: "2024-01-01T00:00:00Z", DirectoryName: "d3"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/prj_new":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ProjectResponse{Result: ProjectResult{
				ID: "prj_new", Name: "my-project", ParentCloudID: strPtr("cld_1"), CreatedAt: "2024-06-01T00:00:00Z", DirectoryName: "d2",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/prj_new/collaborators/users":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ProjectCollaboratorListResponse{Results: []ProjectCollaboratorResult{}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &ProjectDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runProjectDataSourceRead(t, d, ProjectDataSourceModel{Name: types.StringValue("my-project")})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if result.ID.ValueString() != "prj_new" {
		t.Errorf("id = %q, want %q (most recently created project with a matching name)", result.ID.ValueString(), "prj_new")
	}
}
