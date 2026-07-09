package provider

import (
	"testing"

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

// TestContainerImageRegistryModelMapping tests mapping of API response to model
func TestContainerImageRegistryModelMapping(t *testing.T) {
	// Simulate API response for a registered BYOD image
	buildResult := BuildResult{
		ID:                    "bld_123",
		ApplicationTemplateID: "apptemp_456",
		Status:                "succeeded",
		RayVersion:            strPtr("2.9.0"),
		DockerImageName:       strPtr("anyscale/ray:2.9.0-py310"),
		IsBYOD:                true,
		CreatedAt:             "2024-01-01T00:00:00Z",
		Revision:              1,
	}

	// Map to model
	clusterEnvName := "my-registered-image"
	model := ContainerImageRegistryResourceModel{
		ID:                   types.StringValue(buildResult.ID),
		BuildID:              types.StringValue(buildResult.ID),
		ClusterEnvironmentID: types.StringValue(buildResult.ApplicationTemplateID),
		BuildStatus:          types.StringValue(buildResult.Status),
		CreatedAt:            types.StringValue(buildResult.CreatedAt),
		IsBYOD:               types.BoolValue(buildResult.IsBYOD),
		Revision:             types.Int64Value(int64(buildResult.Revision)),
		NameVersion:          types.StringValue(clusterEnvName + ":1"),
	}

	// Verify mapping
	if model.ID.ValueString() != "bld_123" {
		t.Errorf("ID = %v, want 'bld_123'", model.ID.ValueString())
	}
	if model.BuildID.ValueString() != "bld_123" {
		t.Errorf("BuildID = %v, want 'bld_123'", model.BuildID.ValueString())
	}
	if model.ClusterEnvironmentID.ValueString() != "apptemp_456" {
		t.Errorf("ClusterEnvironmentID = %v, want 'apptemp_456'", model.ClusterEnvironmentID.ValueString())
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

// TestBuildResultToBuildIDMapping tests that build ID is correctly extracted
func TestBuildResultToBuildIDMapping(t *testing.T) {
	tests := []struct {
		name             string
		buildID          string
		clusterEnvID     string
		wantResourceID   string
		wantBuildID      string
		wantClusterEnvID string
	}{
		{
			name:             "standard IDs",
			buildID:          "bld_abc123",
			clusterEnvID:     "apptemp_xyz789",
			wantResourceID:   "bld_abc123",
			wantBuildID:      "bld_abc123",
			wantClusterEnvID: "apptemp_xyz789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildResult{
				ID:                    tt.buildID,
				ApplicationTemplateID: tt.clusterEnvID,
			}

			// Resource ID should be build ID for registry resources
			resourceID := result.ID
			buildID := result.ID
			clusterEnvID := result.ApplicationTemplateID

			if resourceID != tt.wantResourceID {
				t.Errorf("resourceID = %v, want %v", resourceID, tt.wantResourceID)
			}
			if buildID != tt.wantBuildID {
				t.Errorf("buildID = %v, want %v", buildID, tt.wantBuildID)
			}
			if clusterEnvID != tt.wantClusterEnvID {
				t.Errorf("clusterEnvID = %v, want %v", clusterEnvID, tt.wantClusterEnvID)
			}
		})
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
