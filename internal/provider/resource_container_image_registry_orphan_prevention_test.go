package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestContainerImageRegistryCreate_BuildReadFailure_WarnsAndStateHoldsTemplate covers
// the window between Create's template POST (which creates the cluster environment and
// its first build) and the GETs that read that build back. If a GET fails or finds no
// build, Create must warn rather than error: an error taints the resource, and the
// replace the next apply plans would archive the environment and then fail to recreate
// it under the same name. State must still hold the template id, so Delete can archive
// it, with the build attributes null for Read to fill. Create must never POST a build.
//
// This drives the resource's real Create() and Delete() methods directly as plain Go
// calls because ContainerImageRegistryResource.client is unexported, so the test lives
// in package provider rather than internal/acctest.
func TestContainerImageRegistryCreate_BuildReadFailure_WarnsAndStateHoldsTemplate(t *testing.T) {
	const templateID = "apptemp_orphan_risk"
	const buildID = "bld_orphan_risk"
	const imageURI = "docker.io/example/orphan-test:v1"
	const name = "tfacc-orphan-prevention-test"

	cases := []struct {
		name          string
		templateGET   func(w http.ResponseWriter)
		buildGET      func(w http.ResponseWriter)
		wantLastError bool
	}{
		{
			name: "template GET fails",
			templateGET: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error": {"detail": "simulated template read failure"}}`))
			},
			wantLastError: true,
		},
		{
			name:        "build GET fails",
			templateGET: templateWithLatestBuild(templateID, name, buildID),
			buildGET: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error": {"detail": "simulated build read failure"}}`))
			},
			wantLastError: true,
		},
		{
			name: "template has no build",
			templateGET: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{
					Result: ApplicationTemplateResult{ID: templateID, Name: name, CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
				})
			},
			wantLastError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var archiveCalls, buildPOSTs int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/v2/application_templates/byod":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{
						Result: ApplicationTemplateResult{ID: templateID, Name: name, CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
					})
				case r.Method == http.MethodGet && r.URL.Path == "/api/v2/application_templates/"+templateID:
					tc.templateGET(w)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v2/builds/"+buildID && tc.buildGET != nil:
					tc.buildGET(w)
				case r.Method == http.MethodPost && r.URL.Path == "/api/v2/builds/byod":
					buildPOSTs++
					w.WriteHeader(http.StatusInternalServerError)
				case r.Method == http.MethodPost && r.URL.Path == "/api/v2/application_templates/"+templateID+"/archive":
					archiveCalls++
					w.Header().Set("Content-Type", "application/json")
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

			// The real runtime pre-populates CreateResponse.State from CreateRequest.Plan.
			createResp := &resource.CreateResponse{State: tfsdk.State(plan)}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, createResp)

			if createResp.Diagnostics.HasError() {
				t.Fatalf("Create reported an error, want only a warning (an error taints the resource): %v", createResp.Diagnostics)
			}
			warnings := createResp.Diagnostics.Warnings()
			if len(warnings) != 1 || warnings[0].Summary() != "Container Image Build Not Found Yet" {
				t.Fatalf("warnings = %v, want one 'Container Image Build Not Found Yet'", warnings)
			}
			if got := strings.Contains(warnings[0].Detail(), "Last error: "); got != tc.wantLastError {
				t.Errorf("warning detail carries 'Last error: ' = %v, want %v: %q", got, tc.wantLastError, warnings[0].Detail())
			}
			if buildPOSTs != 0 {
				t.Errorf("Create POSTed %d builds, want 0: the template POST already created the build", buildPOSTs)
			}

			var state ContainerImageRegistryResourceModel
			if diags := createResp.State.Get(ctx, &state); diags.HasError() {
				t.Fatalf("failed to decode post-create state: %v", diags)
			}
			if state.ID.ValueString() != templateID {
				t.Errorf("state.ID = %q, want template id %q -- Delete() keys on it", state.ID.ValueString(), templateID)
			}
			for attr, v := range map[string]types.String{
				"build_id":     state.BuildID,
				"build_status": state.BuildStatus,
				"created_at":   state.CreatedAt,
				"name_version": state.NameVersion,
				"digest":       state.Digest,
				"ray_version":  state.RayVersion,
			} {
				if !v.IsNull() {
					t.Errorf("state.%s = %v, want null until Read finds the build", attr, v)
				}
			}
			if state.Revision.IsNull() || state.Revision.ValueInt64() != 0 {
				t.Errorf("state.Revision = %v, want 0", state.Revision)
			}

			// Delete must archive the template from exactly this state: no orphan.
			deleteResp := &resource.DeleteResponse{State: createResp.State}
			r.Delete(ctx, resource.DeleteRequest{State: createResp.State}, deleteResp)
			if deleteResp.Diagnostics.HasError() {
				t.Fatalf("Delete reported an error cleaning up the template: %v", deleteResp.Diagnostics)
			}
			if archiveCalls != 1 {
				t.Fatalf("archive calls = %d, want 1 -- the template may be left orphaned", archiveCalls)
			}
		})
	}
}

// templateWithLatestBuild answers a template GET with latest_build set to buildID.
func templateWithLatestBuild(templateID, name, buildID string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result": {"id": "` + templateID + `", "name": "` + name + `", "creator_id": "user_1",
			"created_at": "2024-01-01T00:00:00Z", "latest_build": {"id": "` + buildID + `", "revision": 1, "status": "succeeded"}}}`))
	}
}
