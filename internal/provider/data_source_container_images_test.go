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

// This file previously held three tests (TestContainerImagesDataSourceFilterConstruction,
// TestContainerImageSummaryModelMapping, TestContainerImageSummaryNoBuild) that hand-copied
// Read()'s query-parameter construction and fetchContainerImages()'s field-mapping logic into
// local Go literals/maps and asserted the copies against themselves -- none of them called the
// data source's real Read(), so none could catch a regression in it. They are replaced below
// with tests that drive the real Read() against a mock server, following the same pattern as
// data_source_container_image_test.go (the singular sibling): package provider because
// ContainerImagesDataSource.client is unexported, real Schema()-built tfsdk.Config/State, real
// datasource.ReadRequest/ReadResponse.
//
// TestContainerImagesArchivedFilter and TestContainerImagesNameVersionFormatting are left as-is:
// the former already calls the real ApplicationTemplateResult.IsArchived() method rather than a
// hand-copy, and the latter re-derives a bare fmt.Sprintf format string with no API shape to get
// wrong -- neither matches the hand-copied-production-logic defect shape targeted here.
//
// Multi-page/paging_token coverage lives in data_source_container_images_pagination_test.go and
// is deliberately not duplicated here.

// runContainerImagesDataSourceRead drives the data source's real Read() method end-to-end
// against a model representing the user's config, mirroring
// runContainerImageDataSourceRead in the singular sibling test file.
func runContainerImagesDataSourceRead(t *testing.T, d *ContainerImagesDataSource, model ContainerImagesDataSourceModel) (ContainerImagesDataSourceModel, diag.Diagnostics) {
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
		return ContainerImagesDataSourceModel{}, readResp.Diagnostics
	}

	var result ContainerImagesDataSourceModel
	getDiags := readResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, readResp.Diagnostics
}

// TestContainerImagesDataSourceRead_SendsFilterParams replaces
// TestContainerImagesDataSourceFilterConstruction, which hand-built a map via its own copy of
// Read()'s if/else chain and asserted the copy against itself -- a bug in the real query-building
// code (e.g. a wrong key, a dropped filter, an inverted include_archived default) would never
// have shown up. This drives the real Read() and asserts on the actual URL query string the mock
// server receives.
func TestContainerImagesDataSourceRead_SendsFilterParams(t *testing.T) {
	tests := []struct {
		name            string
		nameContains    types.String
		creatorID       types.String
		projectID       types.String
		includeArchived types.Bool
		wantQuery       map[string]string
	}{
		{
			name:            "no filters defaults include_archived to false",
			nameContains:    types.StringNull(),
			creatorID:       types.StringNull(),
			projectID:       types.StringNull(),
			includeArchived: types.BoolNull(),
			wantQuery: map[string]string{
				"include_archived": "false",
			},
		},
		{
			name:            "name filter only",
			nameContains:    types.StringValue("my-image"),
			creatorID:       types.StringNull(),
			projectID:       types.StringNull(),
			includeArchived: types.BoolNull(),
			wantQuery: map[string]string{
				"name_contains":    "my-image",
				"include_archived": "false",
			},
		},
		{
			name:            "all filters set",
			nameContains:    types.StringValue("test"),
			creatorID:       types.StringValue("user_123"),
			projectID:       types.StringValue("prj_456"),
			includeArchived: types.BoolValue(true),
			wantQuery: map[string]string{
				"name_contains":    "test",
				"creator_id":       "user_123",
				"project_id":       "prj_456",
				"include_archived": "true",
			},
		},
		{
			name:            "include_archived explicitly false",
			nameContains:    types.StringNull(),
			creatorID:       types.StringNull(),
			projectID:       types.StringNull(),
			includeArchived: types.BoolValue(false),
			wantQuery: map[string]string{
				"include_archived": "false",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotQuery map[string]string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v2/application_templates/" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
					w.WriteHeader(http.StatusNotFound)
					return
				}

				gotQuery = make(map[string]string)
				for key, values := range r.URL.Query() {
					if len(values) > 0 {
						gotQuery[key] = values[0]
					}
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(ApplicationTemplatesListResponse{Results: []ApplicationTemplateResult{}})
			}))
			defer server.Close()

			d := &ContainerImagesDataSource{client: NewClientWithToken(server.URL, "fake-token-filterparams")}
			_, diags := runContainerImagesDataSourceRead(t, d, ContainerImagesDataSourceModel{
				NameContains:    tt.nameContains,
				CreatorID:       tt.creatorID,
				ProjectID:       tt.projectID,
				IncludeArchived: tt.includeArchived,
			})
			if diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}

			for key, want := range tt.wantQuery {
				got, ok := gotQuery[key]
				if !ok {
					t.Errorf("request query missing param %q, want %q", key, want)
					continue
				}
				if got != want {
					t.Errorf("request query param %q = %q, want %q", key, got, want)
				}
			}
			for key := range gotQuery {
				if key == "paging_token" {
					// PaginatedRequest's own concern, not this filter-construction test's.
					continue
				}
				if _, ok := tt.wantQuery[key]; !ok {
					t.Errorf("request query has unexpected param %q=%q", key, gotQuery[key])
				}
			}
		})
	}
}

// TestContainerImagesDataSourceRead_MapsBuildFields replaces TestContainerImageSummaryModelMapping,
// which hand-built a ContainerImageSummaryModel literal duplicating fetchContainerImages()'s
// mapping logic and asserted the copy against itself. This drives the real Read() against a mock
// server returning a decorated application template with a LatestBuild, and asserts every mapped
// output field against what the mock server actually returned.
func TestContainerImagesDataSourceRead_MapsBuildFields(t *testing.T) {
	const templateID = "apptemp_123"
	template := ApplicationTemplateResult{
		ID:        templateID,
		Name:      "my-custom-image",
		CreatorID: "user_456",
		CreatedAt: "2024-01-01T00:00:00Z",
		DeletedAt: nil,
		LatestBuild: &MiniBuildResult{
			ID:       "bld_789",
			Status:   "succeeded",
			Revision: 5,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/application_templates/" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ApplicationTemplatesListResponse{Results: []ApplicationTemplateResult{template}})
	}))
	defer server.Close()

	d := &ContainerImagesDataSource{client: NewClientWithToken(server.URL, "fake-token-mapfields")}
	result, diags := runContainerImagesDataSourceRead(t, d, ContainerImagesDataSourceModel{})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if len(result.ContainerImages) != 1 {
		t.Fatalf("got %d container_images, want 1", len(result.ContainerImages))
	}
	image := result.ContainerImages[0]

	if image.ID.ValueString() != templateID {
		t.Errorf("ID = %q, want %q", image.ID.ValueString(), templateID)
	}
	if image.Name.ValueString() != template.Name {
		t.Errorf("Name = %q, want %q", image.Name.ValueString(), template.Name)
	}
	if image.CreatorID.ValueString() != template.CreatorID {
		t.Errorf("CreatorID = %q, want %q", image.CreatorID.ValueString(), template.CreatorID)
	}
	if image.CreatedAt.ValueString() != template.CreatedAt {
		t.Errorf("CreatedAt = %q, want %q", image.CreatedAt.ValueString(), template.CreatedAt)
	}
	if image.IsArchived.ValueBool() {
		t.Error("IsArchived = true, want false (DeletedAt is nil)")
	}
	if image.LatestBuildID.ValueString() != template.LatestBuild.ID {
		t.Errorf("LatestBuildID = %q, want %q", image.LatestBuildID.ValueString(), template.LatestBuild.ID)
	}
	if image.LatestBuildStatus.ValueString() != template.LatestBuild.Status {
		t.Errorf("LatestBuildStatus = %q, want %q", image.LatestBuildStatus.ValueString(), template.LatestBuild.Status)
	}
	if image.Revision.ValueInt64() != int64(template.LatestBuild.Revision) {
		t.Errorf("Revision = %d, want %d", image.Revision.ValueInt64(), template.LatestBuild.Revision)
	}
	wantNameVersion := fmt.Sprintf("%s:%d", template.Name, template.LatestBuild.Revision)
	if image.NameVersion.ValueString() != wantNameVersion {
		t.Errorf("NameVersion = %q, want %q", image.NameVersion.ValueString(), wantNameVersion)
	}
}

// TestContainerImagesDataSourceRead_NoLatestBuild_BuildFieldsAreNull replaces
// TestContainerImageSummaryNoBuild, which hand-simulated the nil-LatestBuild branch inline and
// asserted its own copy rather than exercising the real mapping code. This drives the real
// Read() against a mock server returning a template with no LatestBuild at all, proving the four
// build-derived fields come back null through the real code path.
func TestContainerImagesDataSourceRead_NoLatestBuild_BuildFieldsAreNull(t *testing.T) {
	const templateID = "apptemp_123"
	template := ApplicationTemplateResult{
		ID:        templateID,
		Name:      "empty-image",
		CreatorID: "user_456",
		CreatedAt: "2024-01-01T00:00:00Z",
		DeletedAt: nil,
		// LatestBuild deliberately omitted (nil) -- this is the whole point of the test.
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/application_templates/" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ApplicationTemplatesListResponse{Results: []ApplicationTemplateResult{template}})
	}))
	defer server.Close()

	d := &ContainerImagesDataSource{client: NewClientWithToken(server.URL, "fake-token-nobuild")}
	result, diags := runContainerImagesDataSourceRead(t, d, ContainerImagesDataSourceModel{})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if len(result.ContainerImages) != 1 {
		t.Fatalf("got %d container_images, want 1", len(result.ContainerImages))
	}
	image := result.ContainerImages[0]

	if image.ID.ValueString() != templateID {
		t.Errorf("ID = %q, want %q", image.ID.ValueString(), templateID)
	}
	if !image.LatestBuildID.IsNull() {
		t.Errorf("LatestBuildID = %q, want null", image.LatestBuildID.ValueString())
	}
	if !image.LatestBuildStatus.IsNull() {
		t.Errorf("LatestBuildStatus = %q, want null", image.LatestBuildStatus.ValueString())
	}
	if !image.Revision.IsNull() {
		t.Errorf("Revision = %v, want null", image.Revision)
	}
	if !image.NameVersion.IsNull() {
		t.Errorf("NameVersion = %q, want null", image.NameVersion.ValueString())
	}
}

// TestContainerImagesArchivedFilter tests the archived filtering logic
func TestContainerImagesArchivedFilter(t *testing.T) {
	deletedAt := "2024-01-01T00:00:00Z"
	templates := []ApplicationTemplateResult{
		{
			ID:        "apptemp_123",
			Name:      "active-image",
			DeletedAt: nil, // Not archived
		},
		{
			ID:        "apptemp_456",
			Name:      "archived-image",
			DeletedAt: &deletedAt, // Archived
		},
		{
			ID:        "apptemp_789",
			Name:      "another-active",
			DeletedAt: nil, // Not archived
		},
	}

	// Test filtering - this simulates what the API should return
	// when include_archived=false
	var activeOnly []ApplicationTemplateResult
	for _, tmpl := range templates {
		if !tmpl.IsArchived() {
			activeOnly = append(activeOnly, tmpl)
		}
	}

	if len(activeOnly) != 2 {
		t.Errorf("active count = %v, want 2", len(activeOnly))
	}

	// Test with include_archived=true (all returned)
	if len(templates) != 3 {
		t.Errorf("total count = %v, want 3", len(templates))
	}
}

// TestContainerImagesNameVersionFormatting tests name_version field formatting in list context
func TestContainerImagesNameVersionFormatting(t *testing.T) {
	tests := []struct {
		name            string
		imageName       string
		revision        int
		wantNameVersion string
	}{
		{
			name:            "standard image",
			imageName:       "production-image",
			revision:        10,
			wantNameVersion: "production-image:10",
		},
		{
			name:            "first revision",
			imageName:       "new-image",
			revision:        1,
			wantNameVersion: "new-image:1",
		},
		{
			name:            "image with special chars",
			imageName:       "my-custom-ray-2.9.0",
			revision:        3,
			wantNameVersion: "my-custom-ray-2.9.0:3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nameVersion := fmt.Sprintf("%s:%d", tt.imageName, tt.revision)

			if nameVersion != tt.wantNameVersion {
				t.Errorf("name_version = %v, want %v", nameVersion, tt.wantNameVersion)
			}
		})
	}
}
