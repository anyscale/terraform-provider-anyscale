package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// This file previously held seven tests that built their inputs and outputs from
// local Go literals (BuildResult{...}, ContainerImageRegistryResourceModel{...},
// CreateBYODApplicationTemplateRequest{...}) and asserted the literals matched
// themselves -- none of them called the resource's real Create(), so none could
// catch a regression in it. Two had gone further than merely untested: they
// hand-asserted `id == build_id` ("Resource ID should be build ID for registry
// resources"), which F3 made false -- id has keyed on the application template
// (cluster environment) id since then, precisely so a superseded/failed build
// never orphans the resource's identity (see the comment on Create(), around the
// early resp.State.Set call, and on ContainerImageRegistryResourceModel.ID).
//
// The two tests below replace all seven. They drive the resource's real Create()
// directly as plain Go calls (not through resource.Test/terraform apply) because
// ContainerImageRegistryResource.client is unexported, so a test that constructs
// the resource directly against a mock server must live in package provider
// rather than internal/acctest -- the same constraint and pattern as
// resource_container_image_registry_orphan_prevention_test.go. The former
// TestImageURIValidation is deleted outright rather than replaced: it asserted
// `imageURI != ""`, a check that exists only in that test, not in the resource --
// there is no real validator to point a real test at.

// TestContainerImageRegistryCreate_MapsFieldsAndKeysIDOnTemplate replaces
// TestContainerImageRegistryModelMapping, TestBuildResultToBuildIDMapping,
// TestCreateBYODApplicationTemplateRequestStructure, TestBYODFlagHandling,
// TestRegistryResourceOptionalFields, and TestRegisteredImageBuildStatus.
//
// It runs Create() against a mock server that deliberately returns DISTINCT
// template and build ids, so the money assertion -- state.ID must be the
// template id, never the build id -- can actually fail if Create() regresses,
// rather than passing by coincidence the way it would if both fixture ids
// happened to share a value. It also captures the real request bodies Create()
// sends, to salvage the genuine intent of the deleted request-structure and
// optional-field tests against what Create() actually puts on the wire instead
// of a hand-built literal.
func TestContainerImageRegistryCreate_MapsFieldsAndKeysIDOnTemplate(t *testing.T) {
	tests := []struct {
		name        string
		loginSecret types.String
	}{
		{name: "without registry_login_secret", loginSecret: types.StringNull()},
		{name: "with registry_login_secret", loginSecret: types.StringValue("my-ecr-secret")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const (
				templateID   = "apptemp_create_test_456" // deliberately distinct from buildID
				buildID      = "bld_create_test_123"
				resourceName = "tfacc-registry-create-test"
				imageURI     = "123456789012.dkr.ecr.us-west-2.amazonaws.com/my-ray-image:latest"
				rayVersion   = "2.9.0"
				digest       = "sha256:createtestdigest00000000000000000000000000000000000000000000"
				revision     = 3
				createdAt    = "2024-06-01T00:00:00Z"
			)

			var templateReq CreateBYODApplicationTemplateRequest
			var buildReq CreateBYODBuildRequest

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read request body: %v", err)
				}

				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/v2/application_templates/byod":
					if err := json.Unmarshal(body, &templateReq); err != nil {
						t.Fatalf("failed to decode template request: %v", err)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{
						Result: ApplicationTemplateResult{
							ID:        templateID,
							Name:      resourceName,
							CreatorID: "user_1",
							CreatedAt: createdAt,
						},
					})
				case r.Method == http.MethodPost && r.URL.Path == "/api/v2/builds/byod":
					if err := json.Unmarshal(body, &buildReq); err != nil {
						t.Fatalf("failed to decode build request: %v", err)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(BuildResponse{
						Result: BuildResult{
							ID:                    buildID,
							ApplicationTemplateID: templateID,
							DockerImageName:       strPtr(imageURI),
							RayVersion:            strPtr(rayVersion),
							Revision:              revision,
							CreatorID:             "user_1",
							Status:                "succeeded",
							CreatedAt:             createdAt,
							LastModifiedAt:        createdAt,
							IsBYOD:                true,
							Digest:                strPtr(digest),
						},
					})
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			r := &ContainerImageRegistryResource{client: NewClientWithToken(server.URL, "fake-token-create-test")}
			ctx := context.Background()

			var schemaResp resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
			if schemaResp.Diagnostics.HasError() {
				t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
			}

			plan := tfsdk.Plan{Schema: schemaResp.Schema}
			planDiags := plan.Set(ctx, &ContainerImageRegistryResourceModel{
				Name:                types.StringValue(resourceName),
				ImageURI:            types.StringValue(imageURI),
				RayVersion:          types.StringValue(rayVersion),
				RegistryLoginSecret: tt.loginSecret,
			})
			if planDiags.HasError() {
				t.Fatalf("failed to build plan: %v", planDiags)
			}

			createResp := &resource.CreateResponse{
				// The real runtime pre-populates CreateResponse.State from CreateRequest.Plan.
				State: tfsdk.State(plan),
			}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, createResp)

			if createResp.Diagnostics.HasError() {
				t.Fatalf("Create reported an unexpected error: %v", createResp.Diagnostics)
			}

			// Real request-body assertions against what Create() actually sent.
			if templateReq.Name != resourceName {
				t.Errorf("call 1 request Name = %q, want %q", templateReq.Name, resourceName)
			}
			if templateReq.Anonymous {
				t.Error("call 1 request Anonymous = true, want false")
			}
			if templateReq.ConfigJSON.DockerImage != imageURI {
				t.Errorf("call 1 request ConfigJSON.DockerImage = %q, want %q", templateReq.ConfigJSON.DockerImage, imageURI)
			}
			if templateReq.ConfigJSON.RayVersion != rayVersion {
				t.Errorf("call 1 request ConfigJSON.RayVersion = %q, want %q", templateReq.ConfigJSON.RayVersion, rayVersion)
			}
			if buildReq.ApplicationTemplateID != templateID {
				t.Errorf("call 2 request ApplicationTemplateID = %q, want %q -- Create() must chain call 1's response id into call 2", buildReq.ApplicationTemplateID, templateID)
			}
			if buildReq.ConfigJSON.DockerImage != imageURI {
				t.Errorf("call 2 request ConfigJSON.DockerImage = %q, want %q", buildReq.ConfigJSON.DockerImage, imageURI)
			}
			if buildReq.ConfigJSON.RayVersion != rayVersion {
				t.Errorf("call 2 request ConfigJSON.RayVersion = %q, want %q", buildReq.ConfigJSON.RayVersion, rayVersion)
			}

			if tt.loginSecret.IsNull() {
				if templateReq.ConfigJSON.RegistryLoginSecret != nil {
					t.Errorf("call 1 request RegistryLoginSecret = %v, want nil", *templateReq.ConfigJSON.RegistryLoginSecret)
				}
				if buildReq.ConfigJSON.RegistryLoginSecret != nil {
					t.Errorf("call 2 request RegistryLoginSecret = %v, want nil", *buildReq.ConfigJSON.RegistryLoginSecret)
				}
			} else {
				want := tt.loginSecret.ValueString()
				if templateReq.ConfigJSON.RegistryLoginSecret == nil || *templateReq.ConfigJSON.RegistryLoginSecret != want {
					t.Errorf("call 1 request RegistryLoginSecret = %v, want %q", templateReq.ConfigJSON.RegistryLoginSecret, want)
				}
				if buildReq.ConfigJSON.RegistryLoginSecret == nil || *buildReq.ConfigJSON.RegistryLoginSecret != want {
					t.Errorf("call 2 request RegistryLoginSecret = %v, want %q", buildReq.ConfigJSON.RegistryLoginSecret, want)
				}
			}

			var state ContainerImageRegistryResourceModel
			getDiags := createResp.State.Get(ctx, &state)
			if getDiags.HasError() {
				t.Fatalf("failed to decode final state: %v", getDiags)
			}

			// The money assertion: id must be the TEMPLATE id, never the build id --
			// this is what F3 changed and what TestContainerImageRegistryModelMapping /
			// TestBuildResultToBuildIDMapping got backwards. templateID and buildID are
			// deliberately distinct fixture values above so this assertion can actually
			// fail on a regression instead of passing by coincidence.
			if state.ID.ValueString() != templateID {
				t.Errorf("state.ID = %q, want template id %q (NOT build id %q)", state.ID.ValueString(), templateID, buildID)
			}
			if state.BuildID.ValueString() != buildID {
				t.Errorf("state.BuildID = %q, want %q", state.BuildID.ValueString(), buildID)
			}
			if state.BuildStatus.ValueString() != "succeeded" {
				t.Errorf("state.BuildStatus = %q, want %q", state.BuildStatus.ValueString(), "succeeded")
			}
			if state.CreatedAt.ValueString() != createdAt {
				t.Errorf("state.CreatedAt = %q, want %q", state.CreatedAt.ValueString(), createdAt)
			}
			if !state.IsBYOD.ValueBool() {
				t.Error("state.IsBYOD = false, want true")
			}
			if state.Revision.ValueInt64() != int64(revision) {
				t.Errorf("state.Revision = %d, want %d", state.Revision.ValueInt64(), revision)
			}
			if state.Digest.ValueString() != digest {
				t.Errorf("state.Digest = %q, want %q", state.Digest.ValueString(), digest)
			}
			if state.RayVersion.ValueString() != rayVersion {
				t.Errorf("state.RayVersion = %q, want %q", state.RayVersion.ValueString(), rayVersion)
			}
			wantNameVersion := fmt.Sprintf("%s:%d", resourceName, revision)
			if state.NameVersion.ValueString() != wantNameVersion {
				t.Errorf("state.NameVersion = %q, want %q", state.NameVersion.ValueString(), wantNameVersion)
			}
		})
	}
}

// TestContainerImageRegistryCreate_GeneratesNameWhenOmitted covers a real Create()
// branch none of the seven deleted placebo tests exercised: when the config omits
// `name`, Create() must generate one via sanitizeImageURIForName(imageURI) plus a
// timestamp suffix (name must match ^[A-Za-z0-9._-]+$, so the raw image URI itself
// is not a legal name) rather than send the template create request without one.
// The mock server echoes back whatever name it actually receives, so this test
// stays deterministic without needing to predict the timestamp itself.
func TestContainerImageRegistryCreate_GeneratesNameWhenOmitted(t *testing.T) {
	const (
		templateID = "apptemp_generated_name_test"
		buildID    = "bld_generated_name_test"
		imageURI   = "123456789012.dkr.ecr.us-west-2.amazonaws.com/my-ray-image:latest"
		rayVersion = "2.9.0"
	)
	wantPrefix := sanitizeImageURIForName(imageURI) + "-"

	var templateReq CreateBYODApplicationTemplateRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/application_templates/byod":
			if err := json.Unmarshal(body, &templateReq); err != nil {
				t.Fatalf("failed to decode template request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{
				// Echo back whatever name Create() actually generated, exactly as the
				// real API would -- this is what keeps the test deterministic despite
				// the generated name embedding a live timestamp.
				Result: ApplicationTemplateResult{ID: templateID, Name: templateReq.Name, CreatorID: "user_1", CreatedAt: "2024-06-01T00:00:00Z"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/builds/byod":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(BuildResponse{
				Result: BuildResult{
					ID: buildID, ApplicationTemplateID: templateID, RayVersion: strPtr(rayVersion),
					Revision: 1, CreatorID: "user_1", Status: "succeeded",
					CreatedAt: "2024-06-01T00:00:00Z", LastModifiedAt: "2024-06-01T00:00:00Z", IsBYOD: true,
					// Already-settled digest -- this test is about name generation, not
					// the digest-settle wait, so give waitForBuildDigest its fast path.
					Digest: strPtr("sha256:generatednametestdigest0000000000000000000000000000000000"),
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := &ContainerImageRegistryResource{client: NewClientWithToken(server.URL, "fake-token-generated-name-test")}
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	planDiags := plan.Set(ctx, &ContainerImageRegistryResourceModel{
		// Name deliberately omitted -- this is the whole point of the test. Unknown, not the
		// zero-value Null: name is Optional+Computed, and that is what a real Core plan hands
		// the provider for an omitted Computed attribute on a fresh Create (no prior state for
		// UseStateForUnknown to carry forward). tfsdk.Plan.Set() does not run Core's plan
		// pipeline, so this must be set explicitly to match - a bare zero-value field here would
		// silently test an input shape Create can never actually receive from real Terraform.
		Name:       types.StringUnknown(),
		ImageURI:   types.StringValue(imageURI),
		RayVersion: types.StringValue(rayVersion),
	})
	if planDiags.HasError() {
		t.Fatalf("failed to build plan: %v", planDiags)
	}

	createResp := &resource.CreateResponse{State: tfsdk.State(plan)}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create reported an unexpected error: %v", createResp.Diagnostics)
	}

	if !strings.HasPrefix(templateReq.Name, wantPrefix) {
		t.Fatalf("call 1 request Name = %q, want prefix %q", templateReq.Name, wantPrefix)
	}
	suffix := strings.TrimPrefix(templateReq.Name, wantPrefix)
	if _, err := strconv.ParseInt(suffix, 10, 64); err != nil {
		t.Errorf("call 1 request Name = %q, want a numeric timestamp suffix after %q, got %q (%v)", templateReq.Name, wantPrefix, suffix, err)
	}

	var state ContainerImageRegistryResourceModel
	getDiags := createResp.State.Get(ctx, &state)
	if getDiags.HasError() {
		t.Fatalf("failed to decode final state: %v", getDiags)
	}

	if state.ID.ValueString() != templateID {
		t.Errorf("state.ID = %q, want template id %q", state.ID.ValueString(), templateID)
	}
	// name is Optional+Computed: Create() must write the generated name back into state now
	// (it is the only place this value will ever be known if the practitioner never sets it),
	// and it must be the exact value that was actually sent to the backend, not a second,
	// independently-regenerated guess.
	if state.Name.IsNull() {
		t.Error("state.Name = null, want the backend-generated name Create() actually sent")
	} else if state.Name.ValueString() != templateReq.Name {
		t.Errorf("state.Name = %q, want %q (the exact name sent in the create request)", state.Name.ValueString(), templateReq.Name)
	}
}

// TestContainerImageRegistryRead_NotFoundRemovesFromState proves the observable contract for a
// not-found read: the real Read() against a real 404 removes the resource from state and raises
// no error diagnostic - not merely that some helper's error satisfies errors.Is.
//
// It does NOT guard the errors.Is(err, ErrNotFound) check itself. Reverting that check to
// strings.Contains(err.Error(), "404") still passes this mock, because the legacy phrase
// survives in DoRequestRaw's wrapped text for a genuine 404. The test below is what guards it.
func TestContainerImageRegistryRead_NotFoundRemovesFromState(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/application_templates/cenv_gone" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "not found"}}`)
			return
		}
		t.Errorf("unexpected request: %s", r.URL.Path)
	}))
	defer server.Close()

	r := &ContainerImageRegistryResource{client: NewClientWithToken(server.URL, "test-token")}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	priorState := ContainerImageRegistryResourceModel{
		ID:                  types.StringValue("cenv_gone"),
		Name:                types.StringNull(),
		ImageURI:            types.StringValue("docker.io/anyscale/ray:latest"),
		RayVersion:          types.StringNull(),
		RegistryLoginSecret: types.StringNull(),
		BuildID:             types.StringValue("bld_1"),
		BuildStatus:         types.StringValue("succeeded"),
		CreatedAt:           types.StringValue("2026-01-01T00:00:00Z"),
		IsBYOD:              types.BoolValue(true),
		Revision:            types.Int64Value(1),
		Digest:              types.StringValue("sha256:deadbeef"),
		NameVersion:         types.StringValue("cenv_gone:1"),
	}
	tfState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := tfState.Set(ctx, &priorState); diags.HasError() {
		t.Fatalf("failed to build state fixture: %v", diags)
	}

	readResp := &resource.ReadResponse{State: tfState}
	r.Read(ctx, resource.ReadRequest{State: tfState}, readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read produced diagnostics errors for a real 404, want a clean removal: %v", readResp.Diagnostics)
	}
	if !readResp.State.Raw.IsNull() {
		t.Error("Read did not remove the resource from state for a real 404")
	}
}

// TestContainerImageRegistryRead_ArchivedRemovesFromState proves Read's other removal path: a
// template archived out of band (console, or another Terraform run) returns 200 with archived_at
// populated, not a 404 - deleted_at stays null even though the template is gone from the user's
// perspective, matching a real GET on a template archived weeks earlier (see IsArchived's doc
// comment, models.go). Before ApplicationTemplateResult grew ArchivedAt, IsArchived() checked only
// DeletedAt and so structurally never fired here: this exact 200-with-archived_at response left the
// resource in state forever, with no way for a later apply to self-heal it.
func TestContainerImageRegistryRead_ArchivedRemovesFromState(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/application_templates/cenv_archived" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"result": {
				"id": "cenv_archived", "name": "tfacc-archived-elsewhere",
				"creator_id": "usr_1", "created_at": "2026-01-01T00:00:00Z",
				"anonymous": false, "is_default": false,
				"deleted_at": null, "archived_at": "2026-01-02T00:00:00Z"
			}}`)
			return
		}
		t.Errorf("unexpected request: %s", r.URL.Path)
	}))
	defer server.Close()

	r := &ContainerImageRegistryResource{client: NewClientWithToken(server.URL, "test-token")}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	priorState := ContainerImageRegistryResourceModel{
		ID:                  types.StringValue("cenv_archived"),
		Name:                types.StringNull(),
		ImageURI:            types.StringValue("docker.io/anyscale/ray:latest"),
		RayVersion:          types.StringNull(),
		RegistryLoginSecret: types.StringNull(),
		BuildID:             types.StringValue("bld_1"),
		BuildStatus:         types.StringValue("succeeded"),
		CreatedAt:           types.StringValue("2026-01-01T00:00:00Z"),
		IsBYOD:              types.BoolValue(true),
		Revision:            types.Int64Value(1),
		Digest:              types.StringValue("sha256:deadbeef"),
		NameVersion:         types.StringValue("cenv_archived:1"),
	}
	tfState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := tfState.Set(ctx, &priorState); diags.HasError() {
		t.Fatalf("failed to build state fixture: %v", diags)
	}

	readResp := &resource.ReadResponse{State: tfState}
	r.Read(ctx, resource.ReadRequest{State: tfState}, readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read produced diagnostics errors for an archived template, want a clean removal: %v", readResp.Diagnostics)
	}
	if !readResp.State.Raw.IsNull() {
		t.Error("Read did not remove the resource from state for a template archived out of band (200 with archived_at set)")
	}
}

// TestContainerImageRegistryRead_UnrelatedErrorTextNotTreatedAsNotFound is what actually guards
// Read's errors.Is(err, ErrNotFound) check (see the limit noted on the test above). It forces a
// 500 whose unrelated body text happens to contain "not found" - the exact false positive the old
// substring check admitted, and which errors.Is rules out by construction, since the sentinel is
// only ever attached when the real HTTP status was 404. Getting this wrong swallows a live
// backend error as "already gone" and silently drops a still-live resource from state.
func TestContainerImageRegistryRead_UnrelatedErrorTextNotTreatedAsNotFound(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/application_templates/cenv_live" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "dependency not found in upstream registry"}}`)
			return
		}
		t.Errorf("unexpected request: %s", r.URL.Path)
	}))
	defer server.Close()

	r := &ContainerImageRegistryResource{client: NewClientWithToken(server.URL, "test-token")}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	priorState := ContainerImageRegistryResourceModel{
		ID:                  types.StringValue("cenv_live"),
		Name:                types.StringNull(),
		ImageURI:            types.StringValue("docker.io/anyscale/ray:latest"),
		RayVersion:          types.StringNull(),
		RegistryLoginSecret: types.StringNull(),
		BuildID:             types.StringValue("bld_1"),
		BuildStatus:         types.StringValue("succeeded"),
		CreatedAt:           types.StringValue("2026-01-01T00:00:00Z"),
		IsBYOD:              types.BoolValue(true),
		Revision:            types.Int64Value(1),
		Digest:              types.StringValue("sha256:deadbeef"),
		NameVersion:         types.StringValue("cenv_live:1"),
	}
	tfState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := tfState.Set(ctx, &priorState); diags.HasError() {
		t.Fatalf("failed to build state fixture: %v", diags)
	}

	readResp := &resource.ReadResponse{State: tfState}
	r.Read(ctx, resource.ReadRequest{State: tfState}, readResp)

	if readResp.State.Raw.IsNull() {
		t.Fatal("Read removed a still-live resource from state because an unrelated 500's body text happened to contain \"not found\"")
	}
	if !readResp.Diagnostics.HasError() {
		t.Error("Read produced no diagnostics for a genuine 500 - the error should have surfaced, not been silently swallowed")
	}
}
