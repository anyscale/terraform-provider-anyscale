package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
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

// newProjectCreateTestServer serves the endpoints ProjectResource.Create needs
// for a successful create-then-read cycle: optionally GET /api/v2/clouds (for
// cloud_name resolution), POST /api/v2/projects, then GET
// /api/v2/projects/{id} (Create always reads back after creating).
func newProjectCreateTestServer(t *testing.T, cloudID, cloudName string) *httptest.Server {
	t.Helper()
	const projectID = "prj_test123"

	respond := func(w http.ResponseWriter, status int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(ProjectResponse{Result: ProjectResult{
			ID:            projectID,
			Name:          "test-project",
			ParentCloudID: cloudID,
			CreatedAt:     "2024-01-01T00:00:00Z",
			IsDefault:     false,
			DirectoryName: "test-project-dir",
		}})
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/clouds" && cloudName != "":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(CloudsListResponse{
				Results: []CloudResult{{ID: cloudID, Name: cloudName, CreatedAt: "2024-01-01T00:00:00Z"}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/projects":
			respond(w, http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/"+projectID:
			respond(w, http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestProjectResourceCreate_CloudReferenceValidation replaces the old
// TestCloudReferenceValidation, which re-implemented the cloud_id/cloud_name
// branching inline instead of calling Create(). This drives the real Create()
// method for all four cases; the two error cases also prove the validation
// short-circuits before any HTTP call (the mock server fails the test if hit).
func TestProjectResourceCreate_CloudReferenceValidation(t *testing.T) {
	t.Run("neither cloud_id nor cloud_name set", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("unexpected request %s %s: validation must short-circuit before any API call", r.Method, r.URL.String())
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		_, diags := runProjectResourceCreate(t, r, ProjectResourceModel{
			Name:          types.StringValue("test-project"),
			Collaborators: []ProjectCollaboratorModel{},
		})

		if !diags.HasError() {
			t.Fatal("expected a diagnostic error, got none")
		}
		if !diagsContainSummary(diags, "Cloud Reference Required") {
			t.Errorf("expected 'Cloud Reference Required' diagnostic, got: %v", diags)
		}
	})

	t.Run("both cloud_id and cloud_name set", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("unexpected request %s %s: validation must short-circuit before any API call", r.Method, r.URL.String())
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		_, diags := runProjectResourceCreate(t, r, ProjectResourceModel{
			Name:          types.StringValue("test-project"),
			CloudID:       types.StringValue("cld_123"),
			CloudName:     types.StringValue("my-cloud"),
			Collaborators: []ProjectCollaboratorModel{},
		})

		if !diags.HasError() {
			t.Fatal("expected a diagnostic error, got none")
		}
		if !diagsContainSummary(diags, "Conflicting Cloud Reference") {
			t.Errorf("expected 'Conflicting Cloud Reference' diagnostic, got: %v", diags)
		}
	})

	t.Run("cloud_id only succeeds and is preserved", func(t *testing.T) {
		server := newProjectCreateTestServer(t, "cld_123", "")
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		result, diags := runProjectResourceCreate(t, r, ProjectResourceModel{
			Name:          types.StringValue("test-project"),
			CloudID:       types.StringValue("cld_123"),
			Collaborators: []ProjectCollaboratorModel{},
		})
		if diags.HasError() {
			t.Fatalf("unexpected error: %v", diags)
		}
		if result.CloudID.ValueString() != "cld_123" {
			t.Errorf("cloud_id = %q, want %q", result.CloudID.ValueString(), "cld_123")
		}
		if !result.CloudName.IsNull() {
			t.Errorf("cloud_name should stay null when cloud_id was used, got %q", result.CloudName.ValueString())
		}
	})

	t.Run("cloud_name only resolves and is preserved over cloud_id", func(t *testing.T) {
		server := newProjectCreateTestServer(t, "cld_456", "my-cloud")
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		result, diags := runProjectResourceCreate(t, r, ProjectResourceModel{
			Name:          types.StringValue("test-project"),
			CloudName:     types.StringValue("my-cloud"),
			Collaborators: []ProjectCollaboratorModel{},
		})
		if diags.HasError() {
			t.Fatalf("unexpected error: %v", diags)
		}
		if result.CloudName.ValueString() != "my-cloud" {
			t.Errorf("cloud_name = %q, want %q (should stay as configured)", result.CloudName.ValueString(), "my-cloud")
		}
		if !result.CloudID.IsNull() {
			t.Errorf("cloud_id should stay null when cloud_name was used (API's parent_cloud_id must not overwrite it), got %q", result.CloudID.ValueString())
		}
	})
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
				ID: "prj_test", Name: "test-project", ParentCloudID: "cld_123",
				CreatedAt: "2024-01-01T00:00:00Z", DirectoryName: "test-project-dir",
			}})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ProjectResponse{Result: ProjectResult{
			ID: "prj_test", Name: "test-project", ParentCloudID: "cld_123",
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
		Collaborators:          []ProjectCollaboratorModel{},
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

// TestProjectResourceSyncCollaborators replaces the old
// TestCollaboratorSyncLogic, which re-implemented the add/update/remove
// classification inline instead of calling syncCollaborators. This calls the
// real method against a mock server that records which HTTP calls actually
// happen, and decodes the real request bodies -- which also supersedes the
// old tautological TestCollaboratorBatchRequestStructure/
// TestCollaboratorUpdateRequestStructure struct-literal checks.
func TestProjectResourceSyncCollaborators(t *testing.T) {
	tests := []struct {
		name        string
		planned     []ProjectCollaboratorModel
		current     []ProjectCollaboratorModel
		wantAdds    []string
		wantUpdates map[string]string // identity_id -> new permission_level
		wantRemoves []string          // identity_ids
	}{
		{
			name: "add new collaborator",
			planned: []ProjectCollaboratorModel{
				{Email: types.StringValue("user1@example.com"), PermissionLevel: types.StringValue("write")},
			},
			wantAdds: []string{"user1@example.com"},
		},
		{
			name: "remove collaborator",
			current: []ProjectCollaboratorModel{
				{Email: types.StringValue("user1@example.com"), PermissionLevel: types.StringValue("write"), IdentityID: types.StringValue("identity_123")},
			},
			wantRemoves: []string{"identity_123"},
		},
		{
			name: "update permission level",
			planned: []ProjectCollaboratorModel{
				{Email: types.StringValue("user1@example.com"), PermissionLevel: types.StringValue("owner")},
			},
			current: []ProjectCollaboratorModel{
				{Email: types.StringValue("user1@example.com"), PermissionLevel: types.StringValue("write"), IdentityID: types.StringValue("identity_123")},
			},
			wantUpdates: map[string]string{"identity_123": "owner"},
		},
		{
			name: "no changes means no requests",
			planned: []ProjectCollaboratorModel{
				{Email: types.StringValue("user1@example.com"), PermissionLevel: types.StringValue("write")},
			},
			current: []ProjectCollaboratorModel{
				{Email: types.StringValue("user1@example.com"), PermissionLevel: types.StringValue("write"), IdentityID: types.StringValue("identity_123")},
			},
		},
		{
			name: "complex: one add, one update, one remove",
			planned: []ProjectCollaboratorModel{
				{Email: types.StringValue("user1@example.com"), PermissionLevel: types.StringValue("owner")},
				{Email: types.StringValue("user2@example.com"), PermissionLevel: types.StringValue("write")},
			},
			current: []ProjectCollaboratorModel{
				{Email: types.StringValue("user1@example.com"), PermissionLevel: types.StringValue("write"), IdentityID: types.StringValue("identity_1")},
				{Email: types.StringValue("user3@example.com"), PermissionLevel: types.StringValue("readonly"), IdentityID: types.StringValue("identity_3")},
			},
			wantAdds:    []string{"user2@example.com"},
			wantUpdates: map[string]string{"identity_1": "owner"},
			wantRemoves: []string{"identity_3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAdds []string
			gotUpdates := map[string]string{}
			var gotRemoves []string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/v2/projects/proj-1/collaborators/users/batch_create":
					var entries ProjectCollaboratorBatchRequest
					if err := json.NewDecoder(r.Body).Decode(&entries); err != nil {
						t.Fatalf("failed to decode batch_create body: %v", err)
					}
					for _, e := range entries {
						gotAdds = append(gotAdds, e.Value.Email)
					}
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v2/projects/proj-1/collaborators/"):
					identityID := strings.TrimPrefix(r.URL.Path, "/api/v2/projects/proj-1/collaborators/")
					var updateReq ProjectCollaboratorUpdateRequest
					if err := json.NewDecoder(r.Body).Decode(&updateReq); err != nil {
						t.Fatalf("failed to decode update body: %v", err)
					}
					gotUpdates[identityID] = updateReq.PermissionLevel
					w.WriteHeader(http.StatusOK)
				case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/projects/proj-1/collaborators/"):
					identityID := strings.TrimPrefix(r.URL.Path, "/api/v2/projects/proj-1/collaborators/")
					gotRemoves = append(gotRemoves, identityID)
					w.WriteHeader(http.StatusOK)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
			if err := r.syncCollaborators(context.Background(), "proj-1", tt.planned, tt.current); err != nil {
				t.Fatalf("syncCollaborators returned error: %v", err)
			}

			assertStringSliceUnordered(t, "adds", gotAdds, tt.wantAdds)
			assertStringSliceUnordered(t, "removes", gotRemoves, tt.wantRemoves)
			if len(gotUpdates) != len(tt.wantUpdates) {
				t.Errorf("updates = %v, want %v", gotUpdates, tt.wantUpdates)
			}
			for id, level := range tt.wantUpdates {
				if gotUpdates[id] != level {
					t.Errorf("update for %s = %q, want %q", id, gotUpdates[id], level)
				}
			}
		})
	}
}

func assertStringSliceUnordered(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", label, got, want)
		return
	}
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	for _, g := range got {
		if !wantSet[g] {
			t.Errorf("%s = %v, want %v", label, got, want)
			return
		}
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
			ParentCloudID:   "cld_789",
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
			t.Errorf("unexpected request (no collaborators configured, must not fetch them): %s %s", r.Method, r.URL.String())
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
		if !model.CloudName.IsNull() {
			t.Errorf("cloud_name should stay null, got %q", model.CloudName.ValueString())
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

	t.Run("cloud_name in config is preserved, cloud_id left untouched", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveFullProject(w)
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}
		model := &ProjectResourceModel{CloudName: types.StringValue("my-cloud")}
		if err := r.readProject(context.Background(), projectID, model); err != nil {
			t.Fatalf("readProject returned error: %v", err)
		}

		if model.CloudName.ValueString() != "my-cloud" {
			t.Errorf("cloud_name = %q, want %q", model.CloudName.ValueString(), "my-cloud")
		}
		if !model.CloudID.IsNull() {
			t.Errorf("cloud_id should stay untouched (null) when cloud_name drives the config, got %q", model.CloudID.ValueString())
		}
	})

	t.Run("import: both null, cloud_id populated from API", func(t *testing.T) {
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
		if !model.CloudName.IsNull() {
			t.Error("cloud_name should stay null on import")
		}
	})

	t.Run("collaborators are fetched only when already present in model", func(t *testing.T) {
		collaboratorRequests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/"+projectID:
				serveFullProject(w)
			case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/"+projectID+"/collaborators/users":
				collaboratorRequests++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(ProjectCollaboratorListResponse{
					Results: []ProjectCollaboratorResult{
						{
							ID:              "identity_1",
							PermissionLevel: "write",
							Value: struct {
								ID    string `json:"id"`
								Name  string `json:"name"`
								Email string `json:"email"`
							}{ID: "user_1", Name: "User One", Email: "user1@example.com"},
						},
					},
				})
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		r := &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}

		emptyModel := &ProjectResourceModel{Collaborators: []ProjectCollaboratorModel{}}
		if err := r.readProject(context.Background(), projectID, emptyModel); err != nil {
			t.Fatalf("readProject returned error: %v", err)
		}
		if collaboratorRequests != 0 {
			t.Errorf("expected 0 collaborator requests when none configured, got %d", collaboratorRequests)
		}

		withCollabModel := &ProjectResourceModel{
			Collaborators: []ProjectCollaboratorModel{
				{Email: types.StringValue("placeholder@example.com"), PermissionLevel: types.StringValue("write")},
			},
		}
		if err := r.readProject(context.Background(), projectID, withCollabModel); err != nil {
			t.Fatalf("readProject returned error: %v", err)
		}
		if collaboratorRequests != 1 {
			t.Errorf("expected 1 collaborator request when collaborators configured, got %d", collaboratorRequests)
		}
		if len(withCollabModel.Collaborators) != 1 {
			t.Fatalf("collaborators count = %d, want 1", len(withCollabModel.Collaborators))
		}
		got := withCollabModel.Collaborators[0]
		if got.Email.ValueString() != "user1@example.com" ||
			got.PermissionLevel.ValueString() != "write" ||
			got.IdentityID.ValueString() != "identity_1" ||
			got.UserID.ValueString() != "user_1" {
			t.Errorf("collaborator mapping = %+v, want email=user1@example.com permission_level=write identity_id=identity_1 user_id=user_1", got)
		}
	})
}

// TestProjectResourcePermissionLevelValidator replaces the old
// TestPermissionLevelValidation, which compared literal strings against
// themselves and could never fail. This pulls the real validator off the real
// schema and runs it, so a regression to the "writer" wire-mismatch bug (or
// any other drift in the accepted set) fails this test. Per the design
// contract (F1), the canonical set is {owner, write, readonly} -- "writer" is
// invalid and must stay invalid.
func TestProjectResourcePermissionLevelValidator(t *testing.T) {
	r := &ProjectResource{}
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	block, ok := schemaResp.Schema.Blocks["collaborator"].(schema.ListNestedBlock)
	if !ok {
		t.Fatalf("collaborator block missing or of unexpected type")
	}
	attr, ok := block.NestedObject.Attributes["permission_level"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("permission_level attribute missing or of unexpected type")
	}
	if len(attr.Validators) == 0 {
		t.Fatalf("permission_level has no validators configured")
	}

	tests := []struct {
		value     string
		wantError bool
	}{
		{"owner", false},
		{"write", false},
		{"readonly", false},
		{"writer", true}, // F1: the wire-format bug this effort exists to fix must stay fixed.
		{"admin", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			for _, v := range attr.Validators {
				req := validator.StringRequest{ConfigValue: types.StringValue(tt.value)}
				resp := &validator.StringResponse{}
				v.ValidateString(ctx, req, resp)
				if gotError := resp.Diagnostics.HasError(); gotError != tt.wantError {
					t.Errorf("value %q: validator error = %v, want %v (diags: %v)", tt.value, gotError, tt.wantError, resp.Diagnostics)
				}
			}
		})
	}
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
// shipwright's find on commit 78527ab/492de9a: Delete's 409 detection must
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
// milliseconds instead of really sleeping for the production ~60s ceiling - per assayer's
// test-speed note. The relative shape (ramp then cap, same doubling) is preserved exactly, just
// scaled down, so the subtests below exercise the identical control flow as production.
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
// Retry timing is capped-exponential (ramp 1/2/4/8s then held at the 8s cap, to a 60s ceiling
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
