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

// runContainerImageDataSourceRead drives the data source's real Read() method
// end-to-end against a model representing the user's config.
//
// tfsdk.Config has no Set method (unlike tfsdk.Plan/tfsdk.State), so there is
// no direct way to build a ReadRequest.Config fixture from a Go struct. This
// works around that by building the raw value through a throwaway
// tfsdk.State.Set (which does have Set) and converting it to a tfsdk.Config --
// both types share the same underlying {Raw, Schema} shape, and Get() decodes
// identically regardless of which wrapper carries it.
func runContainerImageDataSourceRead(t *testing.T, d *ContainerImageDataSource, model ContainerImageDataSourceModel) (ContainerImageDataSourceModel, diag.Diagnostics) {
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
		return ContainerImageDataSourceModel{}, readResp.Diagnostics
	}

	var result ContainerImageDataSourceModel
	getDiags := readResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, readResp.Diagnostics
}

// newContainerImageDataSourceTestServer serves the by-ID template lookup and,
// when build is non-nil, the build detail fetch -- the two real HTTP calls
// Read() makes on its success path. Any other request fails the test.
func newContainerImageDataSourceTestServer(t *testing.T, template ApplicationTemplateResult, build *BuildResult) (*httptest.Server, *int) {
	t.Helper()
	buildRequests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/application_templates/"+template.ID:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{Result: template})
		case build != nil && r.Method == http.MethodGet && r.URL.Path == "/api/v2/builds/"+build.ID:
			buildRequests++
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(BuildResponse{Result: *build})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return server, &buildRequests
}

// TestContainerImageDataSourceRead_NeitherIDNorNameErrors drives the real
// Read() with neither id nor name set, replacing
// TestContainerImageDataSourceLookupValidation (which only reimplemented the
// hasID/hasName check inline and asserted against its own reimplementation).
// The mock server's catch-all failure handler proves the validation error
// fires before any HTTP call is attempted.
func TestContainerImageDataSourceRead_NeitherIDNorNameErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s -- neither id nor name was set, Read must error before calling the API", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	d := &ContainerImageDataSource{client: NewClientWithToken(server.URL, "fake-token-neither")}
	_, diags := runContainerImageDataSourceRead(t, d, ContainerImageDataSourceModel{})

	if !diags.HasError() {
		t.Fatal("expected an error when neither id nor name is set, got none")
	}
	found := false
	for _, d := range diags {
		if d.Summary() == "Missing Required Attribute" && d.Detail() == "Either 'id' or 'name' must be specified." {
			found = true
		}
	}
	if !found {
		t.Errorf("diagnostics = %v, want a %q error with detail %q", diags, "Missing Required Attribute", "Either 'id' or 'name' must be specified.")
	}
}

// TestContainerImageDataSourceRead_IDTakesPriorityOverName replaces
// TestContainerImageDataSourceIDLookupPriority (which only reimplemented the
// hasID/hasName priority check inline). This drives the real Read() with
// both id and name set; the mock server's catch-all fails the test on any
// request other than the by-ID lookup, proving the name-search path is never
// even attempted.
func TestContainerImageDataSourceRead_IDTakesPriorityOverName(t *testing.T) {
	const templateID = "apptemp_priority"
	template := ApplicationTemplateResult{ID: templateID, Name: "actual-name-from-id-lookup", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"}

	var idRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/application_templates/"+templateID:
			idRequests++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{Result: template})
		default:
			t.Errorf("unexpected request: %s %s -- id must take priority, the name search must never be called", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &ContainerImageDataSource{client: NewClientWithToken(server.URL, "fake-token-priority")}
	result, diags := runContainerImageDataSourceRead(t, d, ContainerImageDataSourceModel{
		ID:   types.StringValue(templateID),
		Name: types.StringValue("some-other-name-that-must-be-ignored"),
	})

	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if idRequests != 1 {
		t.Fatalf("expected exactly 1 by-ID request, got %d", idRequests)
	}
	if result.ID.ValueString() != templateID {
		t.Errorf("result ID = %q, want %q", result.ID.ValueString(), templateID)
	}
	if result.Name.ValueString() != template.Name {
		t.Errorf("result Name = %q, want %q -- name should come from the id-keyed template, not the config's name value", result.Name.ValueString(), template.Name)
	}
}

// TestContainerImageDataSourceRead_MapsBuildFields replaces
// TestContainerImageDataSourceModelMapping and
// TestContainerImageDataSourceBYODvsBuilt, both of which hand-copied Read()'s
// field-mapping logic and asserted the copy against itself rather than
// exercising the real method. This drives the real Read() against a mock
// server for both a built-from-containerfile image and a BYOD image,
// asserting the full output field set each time.
func TestContainerImageDataSourceRead_MapsBuildFields(t *testing.T) {
	tests := []struct {
		name  string
		build BuildResult
	}{
		{
			name: "built from containerfile",
			build: BuildResult{
				ID: "bld_built", Status: "succeeded", Revision: 2, IsBYOD: false,
				RayVersion:      strPtr("2.9.0"),
				DockerImageName: strPtr("anyscale/my-custom-image:v1"),
			},
		},
		{
			name: "BYOD image",
			build: BuildResult{
				ID: "bld_byod", Status: "succeeded", Revision: 5, IsBYOD: true,
				RayVersion:      strPtr("2.10.0"),
				DockerImageName: strPtr("docker.io/myorg/myimage:latest"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const templateID = "apptemp_mapfields"
			template := ApplicationTemplateResult{
				ID: templateID, Name: "my-custom-image", CreatorID: "user_456", CreatedAt: "2024-01-01T00:00:00Z",
				LatestBuild: &MiniBuildResult{ID: tt.build.ID, Revision: tt.build.Revision, Status: tt.build.Status},
			}

			server, buildRequests := newContainerImageDataSourceTestServer(t, template, &tt.build)
			defer server.Close()

			d := &ContainerImageDataSource{client: NewClientWithToken(server.URL, "fake-token-mapfields")}
			result, diags := runContainerImageDataSourceRead(t, d, ContainerImageDataSourceModel{ID: types.StringValue(templateID)})
			if diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}

			if *buildRequests != 1 {
				t.Fatalf("expected exactly 1 build request, got %d", *buildRequests)
			}
			if result.ID.ValueString() != templateID {
				t.Errorf("ID = %q, want %q", result.ID.ValueString(), templateID)
			}
			if result.Name.ValueString() != template.Name {
				t.Errorf("Name = %q, want %q", result.Name.ValueString(), template.Name)
			}
			if result.BuildID.ValueString() != tt.build.ID {
				t.Errorf("BuildID = %q, want %q", result.BuildID.ValueString(), tt.build.ID)
			}
			if result.BuildStatus.ValueString() != tt.build.Status {
				t.Errorf("BuildStatus = %q, want %q", result.BuildStatus.ValueString(), tt.build.Status)
			}
			if result.ImageURI.ValueString() != *tt.build.DockerImageName {
				t.Errorf("ImageURI = %q, want %q", result.ImageURI.ValueString(), *tt.build.DockerImageName)
			}
			if result.RayVersion.ValueString() != *tt.build.RayVersion {
				t.Errorf("RayVersion = %q, want %q", result.RayVersion.ValueString(), *tt.build.RayVersion)
			}
			if result.IsBYOD.ValueBool() != tt.build.IsBYOD {
				t.Errorf("IsBYOD = %v, want %v", result.IsBYOD.ValueBool(), tt.build.IsBYOD)
			}
			if result.Revision.ValueInt64() != int64(tt.build.Revision) {
				t.Errorf("Revision = %v, want %v", result.Revision.ValueInt64(), tt.build.Revision)
			}
			wantNameVersion := fmt.Sprintf("%s:%d", template.Name, tt.build.Revision)
			if result.NameVersion.ValueString() != wantNameVersion {
				t.Errorf("NameVersion = %q, want %q", result.NameVersion.ValueString(), wantNameVersion)
			}
			if result.CreatedAt.ValueString() != template.CreatedAt {
				t.Errorf("CreatedAt = %q, want %q", result.CreatedAt.ValueString(), template.CreatedAt)
			}
			if result.CreatorID.ValueString() != template.CreatorID {
				t.Errorf("CreatorID = %q, want %q", result.CreatorID.ValueString(), template.CreatorID)
			}
		})
	}
}

// TestContainerImageDataSourceRead_NoLatestBuild_BuildFieldsAreNull replaces
// TestContainerImageDataSourceNullBuildHandling (which hand-copied the
// nil-LatestBuild branch and asserted the copy against itself). The mock
// server exposes only the by-ID template endpoint; any call to the builds
// endpoint fails the test, proving Read() skips the build fetch entirely
// rather than nulling fields after a call.
func TestContainerImageDataSourceRead_NoLatestBuild_BuildFieldsAreNull(t *testing.T) {
	const templateID = "apptemp_nobuild"
	template := ApplicationTemplateResult{ID: templateID, Name: "empty-image", CreatorID: "user_456", CreatedAt: "2024-01-01T00:00:00Z"}

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/application_templates/"+templateID:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{Result: template})
		default:
			t.Errorf("unexpected request: %s %s -- Read must not call the builds endpoint when there is no latest build", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &ContainerImageDataSource{client: NewClientWithToken(server.URL, "fake-token-nobuild")}
	result, diags := runContainerImageDataSourceRead(t, d, ContainerImageDataSourceModel{ID: types.StringValue(templateID)})

	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if requestCount != 1 {
		t.Fatalf("expected exactly 1 request (template only), got %d", requestCount)
	}
	if !result.BuildID.IsNull() {
		t.Errorf("BuildID = %q, want null", result.BuildID.ValueString())
	}
	if !result.BuildStatus.IsNull() {
		t.Errorf("BuildStatus = %q, want null", result.BuildStatus.ValueString())
	}
	if !result.ImageURI.IsNull() {
		t.Errorf("ImageURI = %q, want null", result.ImageURI.ValueString())
	}
	if !result.RayVersion.IsNull() {
		t.Errorf("RayVersion = %q, want null", result.RayVersion.ValueString())
	}
	if !result.IsBYOD.IsNull() {
		t.Errorf("IsBYOD = %v, want null", result.IsBYOD)
	}
	if !result.Revision.IsNull() {
		t.Errorf("Revision = %v, want null", result.Revision)
	}
	if !result.NameVersion.IsNull() {
		t.Errorf("NameVersion = %q, want null", result.NameVersion.ValueString())
	}
}

// TestContainerImageDataSourceRead_BuildFetchFails_BuildFieldsAreNullButNoError
// covers a branch none of the original 7 tests (real or placebo) exercised:
// the template has a latest_build reference, but the detail fetch for that
// build fails (e.g. the build was hard-deleted after the template last
// referenced it). Read() must degrade gracefully -- warn, null the
// build-detail fields, but still resolve build_id from the template's own
// reference and report no error -- rather than failing the whole read. This
// is exactly the kind of behavior a later refactor could silently invert
// (e.g. by promoting the getBuild error to a hard failure), so it's worth its
// own proof independent of the placebo cleanup.
func TestContainerImageDataSourceRead_BuildFetchFails_BuildFieldsAreNullButNoError(t *testing.T) {
	const templateID = "apptemp_buildfetchfails"
	const buildID = "bld_missing"
	template := ApplicationTemplateResult{
		ID: templateID, Name: "flaky-build-image", CreatorID: "user_456", CreatedAt: "2024-01-01T00:00:00Z",
		LatestBuild: &MiniBuildResult{ID: buildID, Revision: 1, Status: "succeeded"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/application_templates/"+templateID:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{Result: template})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/builds/"+buildID:
			// Simulates e.g. a build that was hard-deleted server-side after
			// the template's latest_build reference was set.
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &ContainerImageDataSource{client: NewClientWithToken(server.URL, "fake-token-buildfetchfails")}
	result, diags := runContainerImageDataSourceRead(t, d, ContainerImageDataSourceModel{ID: types.StringValue(templateID)})

	if diags.HasError() {
		t.Fatalf("Read must degrade gracefully (warn + null build-detail fields) when the build fetch fails, not propagate an error: %v", diags)
	}
	if result.BuildID.ValueString() != buildID {
		t.Errorf("BuildID = %q, want %q -- the template's own latest_build id must survive even though the detail fetch failed", result.BuildID.ValueString(), buildID)
	}
	if !result.BuildStatus.IsNull() {
		t.Errorf("BuildStatus = %q, want null", result.BuildStatus.ValueString())
	}
	if !result.ImageURI.IsNull() {
		t.Errorf("ImageURI = %q, want null", result.ImageURI.ValueString())
	}
	if !result.RayVersion.IsNull() {
		t.Errorf("RayVersion = %q, want null", result.RayVersion.ValueString())
	}
	if !result.IsBYOD.IsNull() {
		t.Errorf("IsBYOD = %v, want null", result.IsBYOD)
	}
	if !result.Revision.IsNull() {
		t.Errorf("Revision = %v, want null", result.Revision)
	}
	if !result.NameVersion.IsNull() {
		t.Errorf("NameVersion = %q, want null", result.NameVersion.ValueString())
	}
}

// TestContainerImageDataSourceNameResolutionLogic tests the real filterExactApplicationTemplateMatches
// helper used by getApplicationTemplateByName, covering exact-name and archived filtering together.
func TestContainerImageDataSourceNameResolutionLogic(t *testing.T) {
	// Simulate a name_contains search response with multiple application templates sharing a name.
	deletedAt := "2024-01-03T00:00:00Z"
	templates := []ApplicationTemplateResult{
		{
			ID:        "apptemp_123",
			Name:      "test-image",
			CreatedAt: "2024-01-01T00:00:00Z",
			DeletedAt: nil, // Not archived
		},
		{
			ID:        "apptemp_456",
			Name:      "test-image",
			CreatedAt: "2024-01-02T00:00:00Z", // More recent
			DeletedAt: nil,                    // Not archived
		},
		{
			ID:        "apptemp_789",
			Name:      "test-image",
			CreatedAt: "2024-01-03T00:00:00Z",
			DeletedAt: &deletedAt, // Archived - should be filtered out
		},
		{
			ID:        "apptemp_abc",
			Name:      "other-image",
			CreatedAt: "2024-01-04T00:00:00Z",
			DeletedAt: nil, // Not archived, but a different name - should be filtered out
		},
	}

	matches := filterExactApplicationTemplateMatches(templates, "test-image")

	// Should find 2 non-archived exact-name matches
	if len(matches) != 2 {
		t.Errorf("matches count = %v, want 2", len(matches))
	}

	// First match should be used
	if matches[0].ID != "apptemp_123" {
		t.Errorf("first match ID = %v, want 'apptemp_123'", matches[0].ID)
	}
}

// TestApplicationTemplateResultIsArchived tests the IsArchived() method
func TestApplicationTemplateResultIsArchived(t *testing.T) {
	deletedAt := "2024-01-01T00:00:00Z"
	emptyDeletedAt := ""

	tests := []struct {
		name       string
		deletedAt  *string
		isArchived bool
	}{
		{
			name:       "nil DeletedAt - not archived",
			deletedAt:  nil,
			isArchived: false,
		},
		{
			name:       "empty DeletedAt - not archived",
			deletedAt:  &emptyDeletedAt,
			isArchived: false,
		},
		{
			name:       "non-empty DeletedAt - archived",
			deletedAt:  &deletedAt,
			isArchived: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplicationTemplateResult{
				ID:        "apptemp_123",
				DeletedAt: tt.deletedAt,
			}

			if result.IsArchived() != tt.isArchived {
				t.Errorf("IsArchived() = %v, want %v", result.IsArchived(), tt.isArchived)
			}
		})
	}
}
