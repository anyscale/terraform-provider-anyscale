package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// runProjectsDataSourceRead drives ProjectsDataSource's real Read() method
// end-to-end, the same construction pattern as runProjectDataSourceRead.
func runProjectsDataSourceRead(t *testing.T, d *ProjectsDataSource, model ProjectsDataSourceModel) (ProjectsDataSourceModel, diag.Diagnostics) {
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
		State: tfsdk.State(config),
	}
	d.Read(ctx, datasource.ReadRequest{Config: config}, readResp)

	if readResp.Diagnostics.HasError() {
		return ProjectsDataSourceModel{}, readResp.Diagnostics
	}

	var result ProjectsDataSourceModel
	getDiags := readResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, readResp.Diagnostics
}

// TestProjectsDataSourceRead_Filters replaces the old
// TestProjectsFilterParameterBuilding, which re-implemented the query-param
// construction inline instead of calling Read(). This captures the actual
// query string Read() sends to /api/v2/projects for real.
func TestProjectsDataSourceRead_Filters(t *testing.T) {
	tests := []struct {
		name           string
		config         ProjectsDataSourceModel
		wantParams     map[string]string
		wantNotPresent []string
	}{
		{
			name:   "name_contains filter",
			config: ProjectsDataSourceModel{NameContains: types.StringValue("prod")},
			wantParams: map[string]string{
				"name_contains":    "prod",
				"include_defaults": "true",
			},
		},
		{
			name:   "creator_id filter",
			config: ProjectsDataSourceModel{CreatorID: types.StringValue("user_123")},
			wantParams: map[string]string{
				"creator_id":       "user_123",
				"include_defaults": "true",
			},
		},
		{
			name:   "cloud_id filter",
			config: ProjectsDataSourceModel{CloudID: types.StringValue("cld_456")},
			wantParams: map[string]string{
				"parent_cloud_id":  "cld_456",
				"include_defaults": "true",
			},
		},
		{
			name:   "include_defaults false",
			config: ProjectsDataSourceModel{IncludeDefaults: types.BoolValue(false)},
			wantParams: map[string]string{
				"include_defaults": "false",
			},
		},
		{
			name: "multiple filters combined",
			config: ProjectsDataSourceModel{
				NameContains:    types.StringValue("test"),
				CreatorID:       types.StringValue("user_789"),
				CloudID:         types.StringValue("cld_abc"),
				IncludeDefaults: types.BoolValue(true),
			},
			wantParams: map[string]string{
				"name_contains":    "test",
				"creator_id":       "user_789",
				"parent_cloud_id":  "cld_abc",
				"include_defaults": "true",
			},
		},
		{
			name:           "no filters set means no filter params sent, only include_defaults",
			config:         ProjectsDataSourceModel{},
			wantParams:     map[string]string{"include_defaults": "true"},
			wantNotPresent: []string{"name_contains", "creator_id", "parent_cloud_id"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedQuery map[string][]string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedQuery = map[string][]string(r.URL.Query())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(ProjectsListResponse{Results: []ProjectResult{}})
			}))
			defer server.Close()

			d := &ProjectsDataSource{client: NewClientWithToken(server.URL, "test-token")}
			_, diags := runProjectsDataSourceRead(t, d, tt.config)
			if diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}

			for key, want := range tt.wantParams {
				got := ""
				if vals := capturedQuery[key]; len(vals) > 0 {
					got = vals[0]
				}
				if got != want {
					t.Errorf("query param %s = %q, want %q (full query: %v)", key, got, want, capturedQuery)
				}
			}
			for _, key := range tt.wantNotPresent {
				if _, present := capturedQuery[key]; present {
					t.Errorf("query param %s should not be present, got %v (full query: %v)", key, capturedQuery[key], capturedQuery)
				}
			}
		})
	}
}

// TestProjectsDataSourceRead_FieldMapping replaces the old tautological
// TestProjectSummaryMapping/TestProjectSummaryNullableFields/
// TestProjectsResultList/TestProjectsNoCollaborators (which built
// ProjectSummaryModel structs by hand and checked they held what they were
// given). This drives the real Read() against a mock API and checks what it
// actually produces.
func TestProjectsDataSourceRead_FieldMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ProjectsListResponse{Results: []ProjectResult{
			{
				ID:              "prj_1",
				Name:            "production",
				Description:     strPtr("Production project"),
				ParentCloudID:   strPtr("cld_def"),
				CreatorID:       strPtr("user_123"),
				CreatedAt:       "2024-01-01T00:00:00Z",
				LastUsedCloudID: strPtr("cld_def"),
				IsDefault:       false,
				DirectoryName:   "production-dir",
			},
			{
				// A legacy/default project shape with the optional pointer
				// fields absent, to prove they come back null rather than a
				// zero-valued/empty string.
				ID:            "prj_2",
				Name:          "default",
				ParentCloudID: strPtr("cld_def"),
				CreatedAt:     "2023-01-01T00:00:00Z",
				IsDefault:     true,
				DirectoryName: "default-dir",
			},
		}})
	}))
	defer server.Close()

	d := &ProjectsDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runProjectsDataSourceRead(t, d, ProjectsDataSourceModel{CloudID: types.StringValue("cld_def")})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if len(result.Projects) != 2 {
		t.Fatalf("projects count = %d, want 2", len(result.Projects))
	}

	p1 := result.Projects[0]
	if p1.ID.ValueString() != "prj_1" || p1.Name.ValueString() != "production" {
		t.Errorf("projects[0] id/name = %q/%q, want prj_1/production", p1.ID.ValueString(), p1.Name.ValueString())
	}
	if p1.Description.ValueString() != "Production project" {
		t.Errorf("projects[0] description = %q, want %q", p1.Description.ValueString(), "Production project")
	}
	if p1.IsDefault.ValueBool() {
		t.Error("projects[0] is_default = true, want false")
	}

	p2 := result.Projects[1]
	if !p2.Description.IsNull() {
		t.Errorf("projects[1] description should be null when absent from the API, got %q", p2.Description.ValueString())
	}
	if !p2.CreatorID.IsNull() {
		t.Errorf("projects[1] creator_id should be null when absent from the API, got %q", p2.CreatorID.ValueString())
	}
	if !p2.LastUsedCloudID.IsNull() {
		t.Errorf("projects[1] last_used_cloud_id should be null when absent from the API, got %q", p2.LastUsedCloudID.ValueString())
	}
	if !p2.IsDefault.ValueBool() {
		t.Error("projects[1] is_default = false, want true")
	}
}

// TestProjectsDataSourceRead_NullParentCloudID is the DS-PROJ-1 mutation-proof
// regression guard for the plural data source - same shape as
// TestProjectDataSourceRead_NullParentCloudID in data_source_project_test.go.
// Uses a raw JSON body (rather than a ProjectResult struct literal, which
// cannot express "null" for the plain-string ParentCloudID field as it stands
// today) to prove a nil parent_cloud_id currently comes back "" not null.
func TestProjectsDataSourceRead_NullParentCloudID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [
			{
				"id": "prj_no_cloud",
				"name": "no-cloud-project",
				"parent_cloud_id": null,
				"created_at": "2024-01-01T00:00:00Z",
				"is_default": false,
				"directory_name": "no-cloud-project-dir"
			}
		], "metadata": {"next_paging_token": null}}`)
	}))
	defer server.Close()

	d := &ProjectsDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runProjectsDataSourceRead(t, d, ProjectsDataSourceModel{})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if len(result.Projects) != 1 {
		t.Fatalf("projects count = %d, want 1", len(result.Projects))
	}
	if !result.Projects[0].CloudID.IsNull() {
		t.Errorf("projects[0].cloud_id = %#v, want null for a nil parent_cloud_id, got a non-null value (likely \"\")", result.Projects[0].CloudID)
	}
}

// TestProjectsDataSourceRead_IncludeDefaultsDefaultsToTrue replaces the old
// TestProjectsIncludeDefaults, which only checked a bool literal against
// itself. This proves Read() actually sends include_defaults=true when the
// attribute is left unset in config, via the real request.
func TestProjectsDataSourceRead_IncludeDefaultsDefaultsToTrue(t *testing.T) {
	var capturedValue string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedValue = r.URL.Query().Get("include_defaults")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ProjectsListResponse{Results: []ProjectResult{}})
	}))
	defer server.Close()

	d := &ProjectsDataSource{client: NewClientWithToken(server.URL, "test-token")}
	_, diags := runProjectsDataSourceRead(t, d, ProjectsDataSourceModel{})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if capturedValue != "true" {
		t.Errorf("include_defaults sent = %q, want %q when left unset in config", capturedValue, "true")
	}
}

// TestProjectsDataSourceFetchProjects_PagesBeyondFirstPage proves fetchProjects
// itself correctly wires PaginatedRequest for the ProjectsListResponse shape
// (distinct from the generic PaginatedRequest coverage in
// api_helpers_test.go, and distinct from the old duplicate
// TestProjectsPagination, which only re-checked nextToken != nil logic in
// isolation and never called fetchProjects at all).
func TestProjectsDataSourceFetchProjects_PagesBeyondFirstPage(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if requestCount == 1 {
			_ = json.NewEncoder(w).Encode(ProjectsListResponse{
				Results: []ProjectResult{{ID: "prj_1", Name: "one", DirectoryName: "one-dir"}},
				Metadata: struct {
					Total           int     `json:"total"`
					NextPagingToken *string `json:"next_paging_token"`
				}{Total: 2, NextPagingToken: strPtr("page2")},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(ProjectsListResponse{
			Results: []ProjectResult{{ID: "prj_2", Name: "two", DirectoryName: "two-dir"}},
		})
	}))
	defer server.Close()

	d := &ProjectsDataSource{client: NewClientWithToken(server.URL, "test-token")}
	projects, err := d.fetchProjects(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects across both pages, got %d", len(projects))
	}
	if projects[1].ID.ValueString() != "prj_2" {
		t.Errorf("second project id = %q, want %q", projects[1].ID.ValueString(), "prj_2")
	}
	if requestCount != 2 {
		t.Errorf("expected 2 requests (one per page), got %d", requestCount)
	}
}
