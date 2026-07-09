package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestContainerImageRegistryCreate_Call2Failure_StateHoldsTemplateForCleanup
// is the GATE proof for the registry resource's 2-call BYOD orphan-prevention
// design (see the comment above the early resp.State.Set call in Create(),
// resource_container_image_registry.go). Unlike the old atomic
// /ext/v0/cluster_environments/byod endpoint, api/v2 splits BYOD registration
// into two calls -- POST /api/v2/application_templates/byod (creates the
// template) followed by POST /api/v2/builds/byod (creates the build) -- which
// opens a partial-failure window the old single call never had: if call 2
// fails, the template already exists remotely. Without the defensive early
// State.Set before call 2, Terraform would have no record of that template
// and it would orphan in the backend forever.
//
// This drives the resource's real Create() and Delete() methods directly as
// plain Go calls (not through resource.Test/terraform apply) because
// ContainerImageRegistryResource.client is unexported, so a test that
// constructs the resource directly against a mock server must live in
// package provider rather than internal/acctest.
func TestContainerImageRegistryCreate_Call2Failure_StateHoldsTemplateForCleanup(t *testing.T) {
	const templateID = "apptemp_orphan_risk"
	const imageURI = "docker.io/example/orphan-test:v1"
	const name = "tfacc-orphan-prevention-test"

	var archiveCalls int
	var archivedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/application_templates/byod":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{
				Result: ApplicationTemplateResult{ID: templateID, Name: name, CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/builds/byod":
			// Call 2 fails: simulates e.g. a quota or validation error on the
			// build side, after the template already exists remotely.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "simulated build failure"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/application_templates/"+templateID+"/archive":
			archiveCalls++
			archivedPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"archived_at": "2024-01-01T00:00:01Z"}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := &ContainerImageRegistryResource{client: NewClientWithToken(server.URL, "fake-token-orphan-test")}
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	planDiags := plan.Set(ctx, &ContainerImageRegistryResourceModel{
		Name:     types.StringValue(name),
		ImageURI: types.StringValue(imageURI),
	})
	if planDiags.HasError() {
		t.Fatalf("failed to build plan: %v", planDiags)
	}

	createResp := &resource.CreateResponse{
		// The real runtime pre-populates CreateResponse.State from CreateRequest.Plan.
		State: tfsdk.State(plan),
	}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected Create to report a diagnostic error for the failed call 2, got none")
	}

	// The defensive early State.Set before call 2 must have survived: state
	// holds the template's own id under id (Delete keys directly on ID since
	// V1(c) removed the separate cluster_environment_id attribute; ID carries
	// the same value ID always did), with every field that only the build
	// call would have populated left null.
	var stateAfterFailure ContainerImageRegistryResourceModel
	getDiags := createResp.State.Get(ctx, &stateAfterFailure)
	if getDiags.HasError() {
		t.Fatalf("failed to decode post-failure state: %v", getDiags)
	}

	if stateAfterFailure.ID.ValueString() != templateID {
		t.Errorf("state.ID = %q, want template id %q -- Delete() keys on this field directly; a wrong value here means Delete() can't clean up", stateAfterFailure.ID.ValueString(), templateID)
	}
	if !stateAfterFailure.BuildID.IsNull() {
		t.Errorf("state.BuildID = %q, want null -- call 2 never succeeded, there is no build", stateAfterFailure.BuildID.ValueString())
	}
	if !stateAfterFailure.BuildStatus.IsNull() {
		t.Errorf("state.BuildStatus = %q, want null", stateAfterFailure.BuildStatus.ValueString())
	}
	if !stateAfterFailure.CreatedAt.IsNull() {
		t.Errorf("state.CreatedAt = %q, want null", stateAfterFailure.CreatedAt.ValueString())
	}
	if !stateAfterFailure.NameVersion.IsNull() {
		t.Errorf("state.NameVersion = %q, want null", stateAfterFailure.NameVersion.ValueString())
	}
	if stateAfterFailure.IsBYOD.IsNull() || !stateAfterFailure.IsBYOD.ValueBool() {
		t.Errorf("state.IsBYOD = %v, want true", stateAfterFailure.IsBYOD)
	}
	if stateAfterFailure.Revision.IsNull() || stateAfterFailure.Revision.ValueInt64() != 0 {
		t.Errorf("state.Revision = %v, want 0", stateAfterFailure.Revision)
	}

	// Now prove Delete() uses exactly this state to clean up the template --
	// no orphan. Thread createResp.State straight into Delete()'s request,
	// the same tfsdk.State a real Terraform Core run would carry forward into
	// a subsequent destroy of a tainted resource.
	deleteResp := &resource.DeleteResponse{State: createResp.State}
	r.Delete(ctx, resource.DeleteRequest{State: createResp.State}, deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete reported an unexpected error cleaning up the partially-created template: %v", deleteResp.Diagnostics)
	}
	if archiveCalls != 1 {
		t.Fatalf("expected exactly 1 archive call, got %d -- template may be left orphaned", archiveCalls)
	}
	wantPath := "/api/v2/application_templates/" + templateID + "/archive"
	if archivedPath != wantPath {
		t.Errorf("archived path = %q, want %q", archivedPath, wantPath)
	}
}
