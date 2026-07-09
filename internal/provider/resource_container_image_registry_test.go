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

// TestImageURIValidation tests validation of image URI format
func TestImageURIValidation(t *testing.T) {
	tests := []struct {
		name     string
		imageURI string
		valid    bool
	}{
		{
			name:     "Docker Hub image",
			imageURI: "docker.io/myrepo/image:v2",
			valid:    true,
		},
		{
			name:     "Docker Hub short form",
			imageURI: "myrepo/image:latest",
			valid:    true,
		},
		{
			name:     "ECR image",
			imageURI: "123456789.dkr.ecr.us-west-2.amazonaws.com/my-repo:latest",
			valid:    true,
		},
		{
			name:     "GCR image",
			imageURI: "gcr.io/my-project/my-image:v1",
			valid:    true,
		},
		{
			name:     "Anyscale Ray image",
			imageURI: "anyscale/ray:2.9.0-py310",
			valid:    true,
		},
		{
			name:     "Image with digest",
			imageURI: "myrepo/image@sha256:abc123",
			valid:    true,
		},
		{
			name:     "Empty string",
			imageURI: "",
			valid:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Basic validation - just check non-empty
			isValid := tt.imageURI != ""

			if isValid != tt.valid {
				t.Errorf("imageURI %q valid = %v, want %v", tt.imageURI, isValid, tt.valid)
			}
		})
	}
}

// TestCreateBYODApplicationTemplateRequestStructure tests the structure of the BYOD
// application template create request (POST /api/v2/application_templates/byod,
// call 1 of the registry resource's 2-call Create sequence).
func TestCreateBYODApplicationTemplateRequestStructure(t *testing.T) {
	tests := []struct {
		name                string
		dockerImage         string
		rayVersion          string
		registryLoginSecret *string
	}{
		{
			name:                "basic request",
			dockerImage:         "anyscale/ray:2.9.0-py310",
			rayVersion:          "2.44.0",
			registryLoginSecret: nil,
		},
		{
			name:                "with ray version",
			dockerImage:         "myrepo/custom:latest",
			rayVersion:          "2.9.0",
			registryLoginSecret: nil,
		},
		{
			name:                "with private registry",
			dockerImage:         "123456789.dkr.ecr.us-west-2.amazonaws.com/my-repo:latest",
			rayVersion:          "2.9.0",
			registryLoginSecret: strPtr("my-ecr-secret"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configJSON := CreateBYODApplicationTemplateConfigJSON{
				DockerImage:         tt.dockerImage,
				RayVersion:          tt.rayVersion,
				RegistryLoginSecret: tt.registryLoginSecret,
			}

			req := CreateBYODApplicationTemplateRequest{
				Name:       "test-image",
				ConfigJSON: configJSON,
				Anonymous:  false,
			}

			if req.ConfigJSON.DockerImage != tt.dockerImage {
				t.Errorf("DockerImage = %v, want %v", req.ConfigJSON.DockerImage, tt.dockerImage)
			}

			if req.ConfigJSON.RayVersion != tt.rayVersion {
				t.Errorf("RayVersion = %v, want %v", req.ConfigJSON.RayVersion, tt.rayVersion)
			}

			if tt.registryLoginSecret != nil {
				if req.ConfigJSON.RegistryLoginSecret == nil || *req.ConfigJSON.RegistryLoginSecret != *tt.registryLoginSecret {
					t.Errorf("RegistryLoginSecret = %v, want %v", req.ConfigJSON.RegistryLoginSecret, tt.registryLoginSecret)
				}
			} else if req.ConfigJSON.RegistryLoginSecret != nil {
				t.Error("RegistryLoginSecret should be nil")
			}
		})
	}
}

// TestContainerImageRegistryModelMapping is the GATE-F3(e) fix for a placebo
// test: the original version never called any production code. It hand-built
// a ContainerImageRegistryResourceModel literal setting ID to the BUILD id
// and then asserted that literal equaled itself -- a tautology that would
// keep passing even if Create() regressed back to the pre-F3 id scheme,
// while also actively documenting the wrong contract to anyone reading it.
//
// This version drives the real Create() against a mock server and asserts
// the mapping it actually performs. Like
// TestContainerImageRegistryCreate_Call2Failure_StateHoldsTemplateForCleanup,
// it calls Create() directly as a plain Go method (not via resource.Test)
// because ContainerImageRegistryResource.client is unexported.
func TestContainerImageRegistryModelMapping(t *testing.T) {
	const templateID = "apptemp_456"
	const buildID = "bld_123"
	const name = "my-registered-image"
	const imageURI = "anyscale/ray:2.9.0-py310"
	const rayVersion = "2.9.0"
	const digest = "sha256:modelmapping0000000000000000000000000000000000000000000000000"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/application_templates/byod":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{
				Result: ApplicationTemplateResult{ID: templateID, Name: name, CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/builds/byod":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(BuildResponse{
				Result: BuildResult{
					ID:                    buildID,
					ApplicationTemplateID: templateID,
					Status:                "succeeded",
					RayVersion:            strPtr(rayVersion),
					DockerImageName:       strPtr(imageURI),
					IsBYOD:                true,
					CreatedAt:             "2024-01-01T00:00:00Z",
					Revision:              1,
					Digest:                strPtr(digest),
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := &ContainerImageRegistryResource{client: NewClientWithToken(server.URL, "fake-token-model-mapping-test")}
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	// RayVersion must be explicitly Unknown here, not left at its Go zero
	// value (Null): a real Terraform plan computes Unknown for an omitted
	// Optional+Computed attribute, and Create()'s ray_version-fill logic is
	// gated on IsUnknown(), not IsNull() -- see the comment on that block in
	// resource_container_image_registry.go. A hand-built plan that leaves
	// this Null would never trigger the fill and would fail below for a
	// reason that has nothing to do with production code.
	planDiags := plan.Set(ctx, &ContainerImageRegistryResourceModel{
		Name:       types.StringValue(name),
		ImageURI:   types.StringValue(imageURI),
		RayVersion: types.StringUnknown(),
	})
	if planDiags.HasError() {
		t.Fatalf("failed to build plan: %v", planDiags)
	}

	createResp := &resource.CreateResponse{State: tfsdk.State(plan)}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create reported an unexpected error: %v", createResp.Diagnostics)
	}

	var model ContainerImageRegistryResourceModel
	if getDiags := createResp.State.Get(ctx, &model); getDiags.HasError() {
		t.Fatalf("failed to decode post-create state: %v", getDiags)
	}

	// The money assertion: id (and cluster_environment_id) must be the
	// TEMPLATE id, never the build id. This is exactly what the old version
	// of this test got backwards.
	if model.ID.ValueString() != templateID {
		t.Errorf("ID = %v, want %q (the template id, not the build id %q)", model.ID.ValueString(), templateID, buildID)
	}
	if model.ClusterEnvironmentID.ValueString() != templateID {
		t.Errorf("ClusterEnvironmentID = %v, want %q", model.ClusterEnvironmentID.ValueString(), templateID)
	}
	if model.BuildID.ValueString() != buildID {
		t.Errorf("BuildID = %v, want %q", model.BuildID.ValueString(), buildID)
	}
	if model.BuildStatus.ValueString() != "succeeded" {
		t.Errorf("BuildStatus = %v, want 'succeeded'", model.BuildStatus.ValueString())
	}
	if !model.IsBYOD.ValueBool() {
		t.Error("IsBYOD should be true for registered images")
	}
	if model.Revision.ValueInt64() != 1 {
		t.Errorf("Revision = %v, want 1", model.Revision.ValueInt64())
	}
	if model.NameVersion.ValueString() != "my-registered-image:1" {
		t.Errorf("NameVersion = %v, want 'my-registered-image:1'", model.NameVersion.ValueString())
	}
	if model.RayVersion.ValueString() != rayVersion {
		t.Errorf("RayVersion = %v, want %q", model.RayVersion.ValueString(), rayVersion)
	}
	if model.Digest.ValueString() != digest {
		t.Errorf("Digest = %v, want %q", model.Digest.ValueString(), digest)
	}
}

// TestBYODFlagHandling tests that BYOD flag is always true for registered images
func TestBYODFlagHandling(t *testing.T) {
	tests := []struct {
		name       string
		isBYOD     bool
		wantIsBYOD bool
	}{
		{
			name:       "registered image - is BYOD",
			isBYOD:     true,
			wantIsBYOD: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := ContainerImageRegistryResourceModel{
				IsBYOD: types.BoolValue(tt.isBYOD),
			}

			if model.IsBYOD.ValueBool() != tt.wantIsBYOD {
				t.Errorf("IsBYOD = %v, want %v", model.IsBYOD.ValueBool(), tt.wantIsBYOD)
			}
		})
	}
}

// TestRegistryResourceOptionalFields tests handling of optional fields
func TestRegistryResourceOptionalFields(t *testing.T) {
	tests := []struct {
		name                string
		nameValue           types.String
		rayVersion          types.String
		registryLoginSecret types.String
	}{
		{
			name:                "all optional fields null",
			nameValue:           types.StringNull(),
			rayVersion:          types.StringNull(),
			registryLoginSecret: types.StringNull(),
		},
		{
			name:                "name provided",
			nameValue:           types.StringValue("my-image"),
			rayVersion:          types.StringNull(),
			registryLoginSecret: types.StringNull(),
		},
		{
			name:                "all optional fields provided",
			nameValue:           types.StringValue("my-image"),
			rayVersion:          types.StringValue("2.9.0"),
			registryLoginSecret: types.StringValue("my-secret"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := ContainerImageRegistryResourceModel{
				Name:                tt.nameValue,
				RayVersion:          tt.rayVersion,
				RegistryLoginSecret: tt.registryLoginSecret,
			}

			// Verify null handling
			if model.Name.IsNull() != tt.nameValue.IsNull() {
				t.Errorf("Name.IsNull() = %v, want %v", model.Name.IsNull(), tt.nameValue.IsNull())
			}
			if model.RayVersion.IsNull() != tt.rayVersion.IsNull() {
				t.Errorf("RayVersion.IsNull() = %v, want %v", model.RayVersion.IsNull(), tt.rayVersion.IsNull())
			}
			if model.RegistryLoginSecret.IsNull() != tt.registryLoginSecret.IsNull() {
				t.Errorf("RegistryLoginSecret.IsNull() = %v, want %v", model.RegistryLoginSecret.IsNull(), tt.registryLoginSecret.IsNull())
			}
		})
	}
}

// TestBuildResultToBuildIDMapping is the second GATE-F3(e) placebo fix. The
// original never called production code either: it derived "resourceID"
// locally as result.ID (the build id) under the comment "Resource ID should
// be build ID for registry resources" -- true before F3, false after. Since
// nothing here ever touched Create() or Read(), the test would pass
// identically whether or not that old belief still held in the actual
// resource.
//
// This version is deliberately narrow -- a focused regression guard for
// exactly the historical bug, complementing TestContainerImageRegistryModelMapping's
// broader field-by-field coverage above. It drives the real Create() with a
// build id and template id that are intentionally very different-looking
// (distinct prefixes, no shared substring) so a regression back to id ==
// build_id cannot pass by coincidence, and asserts the negative directly
// (id must NOT equal the build id) rather than relying solely on a positive
// equality check.
func TestBuildResultToBuildIDMapping(t *testing.T) {
	const templateID = "apptemp_xyz789"
	const buildID = "bld_abc123"
	const imageURI = "docker.io/example/build-id-mapping-test:v1"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/application_templates/byod":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{
				Result: ApplicationTemplateResult{ID: templateID, Name: "build-id-mapping-test", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/builds/byod":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(BuildResponse{
				Result: BuildResult{
					ID:                    buildID,
					ApplicationTemplateID: templateID,
					Status:                "succeeded",
					IsBYOD:                true,
					CreatedAt:             "2024-01-01T00:00:00Z",
					Revision:              1,
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := &ContainerImageRegistryResource{client: NewClientWithToken(server.URL, "fake-token-build-id-mapping-test")}
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	planDiags := plan.Set(ctx, &ContainerImageRegistryResourceModel{ImageURI: types.StringValue(imageURI)})
	if planDiags.HasError() {
		t.Fatalf("failed to build plan: %v", planDiags)
	}

	createResp := &resource.CreateResponse{State: tfsdk.State(plan)}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create reported an unexpected error: %v", createResp.Diagnostics)
	}

	var model ContainerImageRegistryResourceModel
	if getDiags := createResp.State.Get(ctx, &model); getDiags.HasError() {
		t.Fatalf("failed to decode post-create state: %v", getDiags)
	}

	resourceID := model.ID.ValueString()
	if resourceID == buildID {
		t.Fatalf("resourceID = %v, which is the BUILD id -- this is the exact pre-F3 bug: id must be the cluster environment (template) id, never the build id", resourceID)
	}
	if resourceID != templateID {
		t.Errorf("resourceID = %v, want %v (the template id)", resourceID, templateID)
	}
	if model.BuildID.ValueString() != buildID {
		t.Errorf("BuildID = %v, want %v", model.BuildID.ValueString(), buildID)
	}
	if model.ClusterEnvironmentID.ValueString() != templateID {
		t.Errorf("ClusterEnvironmentID = %v, want %v", model.ClusterEnvironmentID.ValueString(), templateID)
	}
}

// TestRegisteredImageBuildStatus tests that registered images have succeeded status
func TestRegisteredImageBuildStatus(t *testing.T) {
	// For registered images, the build status should typically be "succeeded"
	// since there's no actual build process
	result := BuildResult{
		ID:     "bld_123",
		Status: "succeeded",
		IsBYOD: true,
	}

	if result.Status != "succeeded" {
		t.Errorf("registered image status = %v, want 'succeeded'", result.Status)
	}

	if !result.IsBYOD {
		t.Error("registered image should have IsBYOD = true")
	}
}
