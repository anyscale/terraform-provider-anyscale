package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// runProjectResourceCreate drives ProjectResource's real Create() method
// end-to-end against a plan model, the same pattern used in
// resource_container_image_registry_orphan_prevention_test.go: build the real
// schema, set a tfsdk.Plan fixture from it, then call Create() directly. This
// exercises the actual validation/request/response code, not a re-implemented
// copy of it.
func runProjectResourceCreate(t *testing.T, r *ProjectResource, plan ProjectResourceModel) (ProjectResourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	tfPlan := tfsdk.Plan{Schema: schemaResp.Schema}
	planDiags := tfPlan.Set(ctx, &plan)
	if planDiags.HasError() {
		t.Fatalf("failed to build plan fixture: %v", planDiags)
	}

	createResp := &resource.CreateResponse{
		// The real runtime pre-populates CreateResponse.State from CreateRequest.Plan.
		State: tfsdk.State(tfPlan),
	}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan}, createResp)

	if createResp.Diagnostics.HasError() {
		return ProjectResourceModel{}, createResp.Diagnostics
	}

	var result ProjectResourceModel
	getDiags := createResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, createResp.Diagnostics
}

func diagsContainSummary(diags diag.Diagnostics, summary string) bool {
	for _, d := range diags {
		if d.Summary() == summary {
			return true
		}
	}
	return false
}

// TestProjectResourceCreate_RequestBody replaces the old
// TestProjectCreateRequestStructure, which only checked that a hand-built
// CreateProjectRequest{} struct literal held the fields it was given -- a
// tautology that could never catch a real regression. This captures the
// actual JSON Create() sends over the wire.
func TestProjectResourceCreate_RequestBody(t *testing.T) {
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v2/projects" {
			if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
				t.Fatalf("failed to decode create request body: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ProjectResponse{Result: ProjectResult{
				ID: "prj_test", Name: "test-project", ParentCloudID: strPtr("cld_123"),
				CreatedAt: "2024-01-01T00:00:00Z", DirectoryName: "test-project-dir",
			}})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ProjectResponse{Result: ProjectResult{
			ID: "prj_test", Name: "test-project", ParentCloudID: strPtr("cld_123"),
			CreatedAt: "2024-01-01T00:00:00Z", DirectoryName: "test-project-dir",
		}})
	}))
	defer server.Close()

	r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
	configID := "ccfg_123"
	_, diags := runProjectResourceCreate(t, r, ProjectResourceModel{
		Name:                   types.StringValue("test-project"),
		CloudID:                types.StringValue("cld_123"),
		Description:            types.StringValue("a description"),
		InitialClusterConfigID: types.StringValue(configID),
	})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if capturedBody["name"] != "test-project" {
		t.Errorf("wire name = %v, want %q", capturedBody["name"], "test-project")
	}
	if capturedBody["parent_cloud_id"] != "cld_123" {
		t.Errorf("wire parent_cloud_id = %v, want %q", capturedBody["parent_cloud_id"], "cld_123")
	}
	if capturedBody["description"] != "a description" {
		t.Errorf("wire description = %v, want %q", capturedBody["description"], "a description")
	}
	// The Go field is InitialClusterConfigID but the wire key is
	// cluster_config -- the API's own name for it (see models.go).
	if capturedBody["cluster_config"] != configID {
		t.Errorf("wire cluster_config = %v, want %q (API field is cluster_config, not initial_cluster_config_id)", capturedBody["cluster_config"], configID)
	}
}

// TestProjectResourceReadProject replaces the old TestCloudReferenceStateHandling,
// TestComputedFieldsPreservation, TestEmptyCollaboratorList, and
// TestCollaboratorModelMapping, all of which either re-implemented readProject's
// branching inline or just checked Go struct-literal assignment. This calls the
// real readProject against a mock API.
func TestProjectResourceReadProject(t *testing.T) {
	const projectID = "prj_456"
	serveFullProject := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ProjectResponse{Result: ProjectResult{
			ID:              projectID,
			Name:            "test-project",
			Description:     strPtr("a description"),
			ParentCloudID:   strPtr("cld_789"),
			CreatorID:       strPtr("user_789"),
			CreatedAt:       "2024-01-01T00:00:00Z",
			LastUsedCloudID: strPtr("cld_789"),
			IsDefault:       false,
			DirectoryName:   "test-project-dir",
		}})
	}

	t.Run("cloud_id in config is preserved, all computed fields populated", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/"+projectID {
				serveFullProject(w)
				return
			}
			t.Errorf("unexpected request (readProject must issue exactly one GET and nothing else): %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		model := &ProjectResourceModel{CloudID: types.StringValue("cld_789")}
		if err := r.readProject(context.Background(), projectID, model); err != nil {
			t.Fatalf("readProject returned error: %v", err)
		}

		if model.CloudID.ValueString() != "cld_789" {
			t.Errorf("cloud_id = %q, want %q", model.CloudID.ValueString(), "cld_789")
		}
		if model.CreatorID.ValueString() != "user_789" {
			t.Errorf("creator_id = %q, want %q", model.CreatorID.ValueString(), "user_789")
		}
		if model.CreatedAt.ValueString() != "2024-01-01T00:00:00Z" {
			t.Errorf("created_at = %q, want %q", model.CreatedAt.ValueString(), "2024-01-01T00:00:00Z")
		}
		if model.LastUsedCloudID.ValueString() != "cld_789" {
			t.Errorf("last_used_cloud_id = %q, want %q", model.LastUsedCloudID.ValueString(), "cld_789")
		}
		if model.IsDefault.ValueBool() {
			t.Error("is_default = true, want false")
		}
		if model.DirectoryName.ValueString() != "test-project-dir" {
			t.Errorf("directory_name = %q, want %q", model.DirectoryName.ValueString(), "test-project-dir")
		}
	})

	t.Run("import: cloud_id populated from API", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveFullProject(w)
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		model := &ProjectResourceModel{}
		if err := r.readProject(context.Background(), projectID, model); err != nil {
			t.Fatalf("readProject returned error: %v", err)
		}

		if model.CloudID.ValueString() != "cld_789" {
			t.Errorf("cloud_id = %q, want %q (must be populated from the API on import)", model.CloudID.ValueString(), "cld_789")
		}
	})

	// t.Run("cloud_id is null when parent_cloud_id is null") is the resource-side
	// DS-PROJ-1 mutation-proof regression guard. Same bug as the two data
	// sources: ProjectResult.ParentCloudID is a plain string, so a nil API
	// parent_cloud_id could silently decode to "" before readProject ever runs
	// and get written straight into state as a non-null empty string instead
	// of null. Uses a raw JSON body since ParentCloudID's current type cannot
	// express JSON null in a struct literal fixture. Proves readProject uses
	// types.StringPointerValue (not types.StringValue) for cloud_id, matching
	// the two data sources' own ParentCloudID handling.
	t.Run("cloud_id is null when parent_cloud_id is null", func(t *testing.T) {
		const noCloudProjectID = "prj_no_cloud"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/"+noCloudProjectID {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result": {
					"id": "` + noCloudProjectID + `",
					"name": "no-cloud-project",
					"parent_cloud_id": null,
					"created_at": "2024-01-01T00:00:00Z",
					"is_default": false,
					"directory_name": "no-cloud-project-dir"
				}}`))
				return
			}
			t.Errorf("unexpected request (readProject must issue exactly one GET and nothing else): %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		// CloudID starts null (e.g. an import), matching the "both null" branch
		// in readProject's comment that sets cloud_id from the API response.
		model := &ProjectResourceModel{}
		if err := r.readProject(context.Background(), noCloudProjectID, model); err != nil {
			t.Fatalf("readProject returned error: %v", err)
		}

		if !model.CloudID.IsNull() {
			t.Errorf("cloud_id = %#v, want null for a nil parent_cloud_id, got a non-null value (likely \"\")", model.CloudID)
		}
	})
}

// runProjectResourceDelete drives ProjectResource's real Delete() method
// end-to-end against a state model, the same construction pattern as
// runProjectResourceCreate.
func runProjectResourceDelete(t *testing.T, r *ProjectResource, state ProjectResourceModel) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	tfState := tfsdk.State{Schema: schemaResp.Schema}
	setDiags := tfState.Set(ctx, &state)
	if setDiags.HasError() {
		t.Fatalf("failed to build state fixture: %v", setDiags)
	}

	deleteResp := &resource.DeleteResponse{State: tfState}
	r.Delete(ctx, resource.DeleteRequest{State: tfState}, deleteResp)
	return deleteResp.Diagnostics
}

// TestProjectResourceDelete_409Detection is the regression test for
// the bug found on commit 78527ab/492de9a: Delete's 409 detection must
// match the specific "status 409" substring DoRequestRaw's error formatting
// produces (`unexpected status %d: %s`), not a bare "409" search -- otherwise
// an unrelated failure whose response body happens to contain that digit
// sequence elsewhere would be misreported as "Project Has Active Resources".
func TestProjectResourceDelete_409Detection(t *testing.T) {
	t.Run("real 409 gets the specific diagnostic", func(t *testing.T) {
		const projectID = "prj_409"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"detail":"Project has active clusters"}`))
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		diags := runProjectResourceDelete(t, r, ProjectResourceModel{ID: types.StringValue(projectID)})

		if !diags.HasError() {
			t.Fatal("expected a diagnostic error, got none")
		}
		if !diagsContainSummary(diags, "Project Has Active Resources") {
			t.Errorf("expected 'Project Has Active Resources' diagnostic, got: %v", diags)
		}
	})

	t.Run("unrelated failure with a decoy 409 substring in the body does not false-positive", func(t *testing.T) {
		const projectID = "prj_decoy"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			// Body deliberately contains the literal digits "409" as part of an
			// unrelated error code, but the status is 500 and the string
			// "status 409" never appears -- this is the exact shape the old
			// bare strings.Contains(err.Error(), "409") check would have
			// misclassified.
			_, _ = w.Write([]byte(`{"error_code":"ERR_409_LEGACY","message":"internal failure"}`))
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		diags := runProjectResourceDelete(t, r, ProjectResourceModel{ID: types.StringValue(projectID)})

		if !diags.HasError() {
			t.Fatal("expected a diagnostic error, got none")
		}
		if diagsContainSummary(diags, "Project Has Active Resources") {
			t.Errorf("false-positived 'Project Has Active Resources' on a decoy 409 substring, diags: %v", diags)
		}
	})
}

// withFastRetryTiming overrides the three retry-timing vars to millisecond-scale values for the
// duration of a test (saving and defer-restoring the real production values), so the
// exhaust-path and multi-attempt-then-succeed subtests below run in low-single-digit
// milliseconds instead of really sleeping for the production ~90s ceiling - a deliberate
// test-speed optimization. The relative shape (ramp then cap, same doubling) is preserved
// exactly, just scaled down, so the subtests below exercise the identical control flow as
// production.
func withFastRetryTiming(t *testing.T) {
	t.Helper()
	origInitial, origMax, origWait := deleteProjectRetryInitialInterval, deleteProjectRetryMaxInterval, deleteProjectRetryMaxWait
	deleteProjectRetryInitialInterval = 1 * time.Millisecond
	deleteProjectRetryMaxInterval = 4 * time.Millisecond
	deleteProjectRetryMaxWait = 15 * time.Millisecond
	t.Cleanup(func() {
		deleteProjectRetryInitialInterval, deleteProjectRetryMaxInterval, deleteProjectRetryMaxWait = origInitial, origMax, origWait
	})
}

// TestProjectResourceDelete_403Retry covers the bounded, age-scoped retry for
// the known delete-time permission-check consistency race (see the
// project-delete-403 investigation notes): a project's own created_at is the
// ONLY signal that gates the retry, deliberately not "are we in a test" or
// any message-content check, since the race and a genuine long-standing
// denial produce the identical bare "Permission denied" 403.
//
// Retry timing is capped-exponential (ramp 1/2/4/8s then held at the 8s cap, to a 90s ceiling
// in production); withFastRetryTiming scales that down to 1/2/4ms held at a 4ms cap to a 15ms
// ceiling for these subtests, so the exact request counts below are derived from that scaled
// schedule, not the production one - see withFastRetryTiming's doc comment for why that's safe.
func TestProjectResourceDelete_403Retry(t *testing.T) {
	withFastRetryTiming(t)

	t.Run("recently created project retries a 403 across multiple ramp steps and succeeds", func(t *testing.T) {
		const projectID = "prj_recent_retry_succeeds"
		var requestCount int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			if requestCount < 4 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"detail":"Permission denied"}}`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		diags := runProjectResourceDelete(t, r, ProjectResourceModel{
			ID:        types.StringValue(projectID),
			CreatedAt: types.StringValue(time.Now().Format(time.RFC3339)),
		})

		if diags.HasError() {
			t.Fatalf("expected the retry to eventually succeed, got diagnostics: %v", diags)
		}
		// 3 failures (exercising the 1ms, 2ms, and capped-4ms steps of the ramp) + 1 success.
		if requestCount != 4 {
			t.Fatalf("expected exactly 4 requests (3 failed across the ramp + 1 success), got %d", requestCount)
		}
	})

	t.Run("old project does NOT retry a 403 - surfaces immediately", func(t *testing.T) {
		const projectID = "prj_old_no_retry"
		var requestCount int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"detail":"Permission denied"}}`))
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		diags := runProjectResourceDelete(t, r, ProjectResourceModel{
			ID:        types.StringValue(projectID),
			CreatedAt: types.StringValue(time.Now().Add(-1 * time.Hour).Format(time.RFC3339)),
		})

		if !diags.HasError() {
			t.Fatal("expected a diagnostic error for an old project's genuine-looking 403, got none")
		}
		if requestCount != 1 {
			t.Fatalf("an old project's 403 must NOT be retried (a real, long-standing permission denial must surface immediately) - expected exactly 1 request, got %d", requestCount)
		}
	})

	t.Run("recently created project with active jobs/services does NOT retry a 403 - surfaces immediately", func(t *testing.T) {
		// Parallels the old-project anti-masking assertion above: this 403 is on a project well
		// within the recently-created window (so eligible would otherwise be true), but its body
		// matches a known non-transient message (deleteProjectNonTransient403Messages), so it
		// must still fail fast rather than riding out the full 90s ceiling - see
		// isKnownNonTransient403's doc comment for why this exclusion is safe.
		const projectID = "prj_recent_active_jobs"
		var requestCount int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"detail":"You cannot delete a project unless it has no jobs or services"}}`))
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		diags := runProjectResourceDelete(t, r, ProjectResourceModel{
			ID:        types.StringValue(projectID),
			CreatedAt: types.StringValue(time.Now().Format(time.RFC3339)),
		})

		if !diagsContainSummary(diags, "Project Has Active Resources") {
			t.Errorf("expected the friendly 'Project Has Active Resources' diagnostic for a known active-jobs 403, got: %v", diags)
		}
		if requestCount != 1 {
			t.Fatalf("a known non-transient 403 (active jobs/services) must NOT be retried even on a recently-created project - expected exactly 1 request, got %d", requestCount)
		}
	})

	t.Run("recently created project exhausts retries and still surfaces the error", func(t *testing.T) {
		const projectID = "prj_recent_exhausts"
		var requestCount int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"detail":"Permission denied"}}`))
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		diags := runProjectResourceDelete(t, r, ProjectResourceModel{
			ID:        types.StringValue(projectID),
			CreatedAt: types.StringValue(time.Now().Format(time.RFC3339)),
		})

		if !diags.HasError() {
			t.Fatal("expected the error to still surface once retries are exhausted, got none")
		}
		// Derived from withFastRetryTiming's 1ms/4ms-cap/15ms-ceiling schedule: elapsed reaches
		// 0,1,3,7,11,15ms across attempts 1-6, and the 6th check (elapsed 15 >= ceiling 15) stops
		// the loop without a further sleep. This is a direct trace of the production loop in
		// deleteProjectWithRetry, just at millisecond instead of second scale - see that
		// function's ceiling check (elapsed >= deleteProjectRetryMaxWait) before each sleep.
		const wantRequests = 6
		if requestCount != wantRequests {
			t.Fatalf("expected exactly %d requests (retries exhausted), got %d", wantRequests, requestCount)
		}
	})

	t.Run("missing created_at fails closed - no retry", func(t *testing.T) {
		const projectID = "prj_missing_created_at"
		var requestCount int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"detail":"Permission denied"}}`))
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		diags := runProjectResourceDelete(t, r, ProjectResourceModel{
			ID: types.StringValue(projectID),
			// CreatedAt deliberately left unset (empty string).
		})

		if !diags.HasError() {
			t.Fatal("expected a diagnostic error, got none")
		}
		if requestCount != 1 {
			t.Fatalf("an unparseable/missing created_at must fail closed to no-retry, expected exactly 1 request, got %d", requestCount)
		}
	})

	t.Run("a 409 is still detected correctly and never retried", func(t *testing.T) {
		const projectID = "prj_409_recent"
		var requestCount int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"detail":"Project has active clusters"}`))
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		diags := runProjectResourceDelete(t, r, ProjectResourceModel{
			ID:        types.StringValue(projectID),
			CreatedAt: types.StringValue(time.Now().Format(time.RFC3339)),
		})

		if !diagsContainSummary(diags, "Project Has Active Resources") {
			t.Errorf("expected 'Project Has Active Resources' diagnostic even for a recently-created project, got: %v", diags)
		}
		if requestCount != 1 {
			t.Fatalf("a 409 must never be retried by the 403-specific retry, expected exactly 1 request, got %d", requestCount)
		}
	})

	t.Run("clean success on a recently created project needs no retry", func(t *testing.T) {
		const projectID = "prj_recent_clean"
		var requestCount int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		diags := runProjectResourceDelete(t, r, ProjectResourceModel{
			ID:        types.StringValue(projectID),
			CreatedAt: types.StringValue(time.Now().Format(time.RFC3339)),
		})

		if diags.HasError() {
			t.Fatalf("expected a clean delete, got diagnostics: %v", diags)
		}
		if requestCount != 1 {
			t.Fatalf("a clean success must not trigger any retry, expected exactly 1 request, got %d", requestCount)
		}
	})
}
