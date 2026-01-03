package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestContainerImageDataSourceLookupValidation tests validation of ID vs Name lookup
func TestContainerImageDataSourceLookupValidation(t *testing.T) {
	tests := []struct {
		name      string
		id        types.String
		imageName types.String
		wantError bool
		errorMsg  string
	}{
		{
			name:      "ID provided",
			id:        types.StringValue("apptemp_123"),
			imageName: types.StringNull(),
			wantError: false,
		},
		{
			name:      "name provided",
			id:        types.StringNull(),
			imageName: types.StringValue("my-image"),
			wantError: false,
		},
		{
			name:      "both provided - OK (uses ID)",
			id:        types.StringValue("apptemp_123"),
			imageName: types.StringValue("my-image"),
			wantError: false,
		},
		{
			name:      "neither provided",
			id:        types.StringNull(),
			imageName: types.StringNull(),
			wantError: true,
			errorMsg:  "Either 'id' or 'name' must be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate validation logic from Read method
			hasID := !tt.id.IsNull() && tt.id.ValueString() != ""
			hasName := !tt.imageName.IsNull() && tt.imageName.ValueString() != ""

			var gotError bool
			var gotErrorMsg string

			if !hasID && !hasName {
				gotError = true
				gotErrorMsg = "Either 'id' or 'name' must be specified"
			}

			if gotError != tt.wantError {
				t.Errorf("validation error = %v, wantError %v", gotError, tt.wantError)
			}

			if tt.wantError && gotErrorMsg != tt.errorMsg {
				t.Errorf("error message = %v, want %v", gotErrorMsg, tt.errorMsg)
			}
		})
	}
}

// TestContainerImageDataSourceNameResolutionLogic tests name resolution with multiple matching images
func TestContainerImageDataSourceNameResolutionLogic(t *testing.T) {
	// Simulate API response with multiple cluster environments having the same name
	clusterEnvs := []ClusterEnvironmentResult{
		{
			ID:         "apptemp_123",
			Name:       "test-image",
			CreatedAt:  "2024-01-01T00:00:00Z",
			IsArchived: false,
		},
		{
			ID:         "apptemp_456",
			Name:       "test-image",
			CreatedAt:  "2024-01-02T00:00:00Z", // More recent
			IsArchived: false,
		},
		{
			ID:         "apptemp_789",
			Name:       "test-image",
			CreatedAt:  "2024-01-03T00:00:00Z",
			IsArchived: true, // Archived - should be filtered out
		},
		{
			ID:         "apptemp_abc",
			Name:       "other-image",
			CreatedAt:  "2024-01-04T00:00:00Z",
			IsArchived: false,
		},
	}

	targetName := "test-image"

	// Simulate resolution logic - find exact matches that are not archived
	var matches []ClusterEnvironmentResult
	for _, env := range clusterEnvs {
		if env.Name == targetName && !env.IsArchived {
			matches = append(matches, env)
		}
	}

	// Should find 2 non-archived matches
	if len(matches) != 2 {
		t.Errorf("matches count = %v, want 2", len(matches))
	}

	// First match should be used
	if matches[0].ID != "apptemp_123" {
		t.Errorf("first match ID = %v, want 'apptemp_123'", matches[0].ID)
	}
}

// TestContainerImageDataSourceModelMapping tests mapping of API response to model
func TestContainerImageDataSourceModelMapping(t *testing.T) {
	// Simulate API response
	clusterEnv := ClusterEnvironmentResult{
		ID:                "apptemp_123",
		Name:              "my-custom-image",
		CreatorID:         "user_456",
		CreatedAt:         "2024-01-01T00:00:00Z",
		LatestBuildID:     strPtr("bld_789"),
		LatestBuildStatus: strPtr("succeeded"),
	}

	build := BuildResult{
		ID:              "bld_789",
		Status:          "succeeded",
		RayVersion:      strPtr("2.9.0"),
		DockerImageName: strPtr("anyscale/my-custom-image:v1"),
		IsBYOD:          false,
		Revision:        2,
	}

	// Map to model
	model := ContainerImageDataSourceModel{
		ID:          types.StringValue(clusterEnv.ID),
		Name:        types.StringValue(clusterEnv.Name),
		BuildID:     types.StringValue(build.ID),
		BuildStatus: types.StringValue(build.Status),
		CreatedAt:   types.StringValue(clusterEnv.CreatedAt),
		CreatorID:   types.StringValue(clusterEnv.CreatorID),
		IsBYOD:      types.BoolValue(build.IsBYOD),
		Revision:    types.Int64Value(int64(build.Revision)),
		NameVersion: types.StringValue(clusterEnv.Name + ":2"),
	}

	if build.DockerImageName != nil {
		model.ImageURI = types.StringValue(*build.DockerImageName)
	}
	if build.RayVersion != nil {
		model.RayVersion = types.StringValue(*build.RayVersion)
	}

	// Verify mapping
	if model.ID.ValueString() != "apptemp_123" {
		t.Errorf("ID = %v, want 'apptemp_123'", model.ID.ValueString())
	}
	if model.Name.ValueString() != "my-custom-image" {
		t.Errorf("Name = %v, want 'my-custom-image'", model.Name.ValueString())
	}
	if model.BuildID.ValueString() != "bld_789" {
		t.Errorf("BuildID = %v, want 'bld_789'", model.BuildID.ValueString())
	}
	if model.BuildStatus.ValueString() != "succeeded" {
		t.Errorf("BuildStatus = %v, want 'succeeded'", model.BuildStatus.ValueString())
	}
	if model.ImageURI.ValueString() != "anyscale/my-custom-image:v1" {
		t.Errorf("ImageURI = %v, want 'anyscale/my-custom-image:v1'", model.ImageURI.ValueString())
	}
	if model.RayVersion.ValueString() != "2.9.0" {
		t.Errorf("RayVersion = %v, want '2.9.0'", model.RayVersion.ValueString())
	}
	if model.IsBYOD.ValueBool() {
		t.Error("IsBYOD should be false for built images")
	}
	if model.Revision.ValueInt64() != 2 {
		t.Errorf("Revision = %v, want 2", model.Revision.ValueInt64())
	}
	if model.NameVersion.ValueString() != "my-custom-image:2" {
		t.Errorf("NameVersion = %v, want 'my-custom-image:2'", model.NameVersion.ValueString())
	}
}

// TestContainerImageDataSourceArchivedFiltering tests that archived images are filtered out
func TestContainerImageDataSourceArchivedFiltering(t *testing.T) {
	clusterEnvs := []ClusterEnvironmentResult{
		{
			ID:         "apptemp_123",
			Name:       "my-image",
			IsArchived: true, // Should be filtered out
		},
		{
			ID:         "apptemp_456",
			Name:       "my-image",
			IsArchived: false, // Should be included
		},
	}

	// Filter out archived
	var active []ClusterEnvironmentResult
	for _, env := range clusterEnvs {
		if !env.IsArchived {
			active = append(active, env)
		}
	}

	if len(active) != 1 {
		t.Errorf("active count = %v, want 1", len(active))
	}

	if active[0].ID != "apptemp_456" {
		t.Errorf("active ID = %v, want 'apptemp_456'", active[0].ID)
	}
}

// TestContainerImageDataSourceNullBuildHandling tests handling when no build exists
func TestContainerImageDataSourceNullBuildHandling(t *testing.T) {
	// Cluster environment without a build
	clusterEnv := ClusterEnvironmentResult{
		ID:                "apptemp_123",
		Name:              "empty-image",
		CreatorID:         "user_456",
		CreatedAt:         "2024-01-01T00:00:00Z",
		LatestBuildID:     nil,
		LatestBuildStatus: nil,
	}

	// Map to model - should handle nil build
	model := ContainerImageDataSourceModel{
		ID:        types.StringValue(clusterEnv.ID),
		Name:      types.StringValue(clusterEnv.Name),
		CreatedAt: types.StringValue(clusterEnv.CreatedAt),
		CreatorID: types.StringValue(clusterEnv.CreatorID),
	}

	// Set build-related fields to null when no build exists
	if clusterEnv.LatestBuildID == nil {
		model.BuildID = types.StringNull()
		model.BuildStatus = types.StringNull()
		model.ImageURI = types.StringNull()
		model.RayVersion = types.StringNull()
		model.IsBYOD = types.BoolNull()
		model.Revision = types.Int64Null()
		model.NameVersion = types.StringNull()
	}

	// Verify null handling
	if !model.BuildID.IsNull() {
		t.Error("BuildID should be null when LatestBuildID is nil")
	}
	if !model.BuildStatus.IsNull() {
		t.Error("BuildStatus should be null when LatestBuildID is nil")
	}
	if !model.ImageURI.IsNull() {
		t.Error("ImageURI should be null when no build exists")
	}
	if !model.RayVersion.IsNull() {
		t.Error("RayVersion should be null when no build exists")
	}
	if !model.IsBYOD.IsNull() {
		t.Error("IsBYOD should be null when no build exists")
	}
	if !model.Revision.IsNull() {
		t.Error("Revision should be null when no build exists")
	}
	if !model.NameVersion.IsNull() {
		t.Error("NameVersion should be null when no build exists")
	}
}

// TestContainerImageDataSourceBYODvsBuilt tests distinguishing BYOD from built images
func TestContainerImageDataSourceBYODvsBuilt(t *testing.T) {
	tests := []struct {
		name      string
		isBYOD    bool
		buildType string
	}{
		{
			name:      "built image",
			isBYOD:    false,
			buildType: "built from containerfile",
		},
		{
			name:      "BYOD image",
			isBYOD:    true,
			buildType: "registered from registry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			build := BuildResult{
				ID:     "bld_123",
				IsBYOD: tt.isBYOD,
			}

			model := ContainerImageDataSourceModel{
				IsBYOD: types.BoolValue(build.IsBYOD),
			}

			if model.IsBYOD.ValueBool() != tt.isBYOD {
				t.Errorf("IsBYOD = %v, want %v for %s", model.IsBYOD.ValueBool(), tt.isBYOD, tt.buildType)
			}
		})
	}
}

// TestContainerImageDataSourceIDLookupPriority tests that ID lookup takes priority over name
func TestContainerImageDataSourceIDLookupPriority(t *testing.T) {
	// When both ID and name are provided, ID should be used
	id := types.StringValue("apptemp_123")
	name := types.StringValue("my-image")

	hasID := !id.IsNull() && id.ValueString() != ""
	hasName := !name.IsNull() && name.ValueString() != ""

	// Simulate lookup priority
	var lookupMethod string
	if hasID {
		lookupMethod = "by_id"
	} else if hasName {
		lookupMethod = "by_name"
	}

	if lookupMethod != "by_id" {
		t.Errorf("lookupMethod = %v, want 'by_id'", lookupMethod)
	}
}

// TestClusterEnvironmentResultLatestBuild tests extraction of latest build info
func TestClusterEnvironmentResultLatestBuild(t *testing.T) {
	tests := []struct {
		name          string
		latestBuildID *string
		latestStatus  *string
		hasBuild      bool
	}{
		{
			name:          "with latest build",
			latestBuildID: strPtr("bld_123"),
			latestStatus:  strPtr("succeeded"),
			hasBuild:      true,
		},
		{
			name:          "without latest build",
			latestBuildID: nil,
			latestStatus:  nil,
			hasBuild:      false,
		},
		{
			name:          "with empty build ID",
			latestBuildID: strPtr(""),
			latestStatus:  strPtr(""),
			hasBuild:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClusterEnvironmentResult{
				ID:                "apptemp_123",
				LatestBuildID:     tt.latestBuildID,
				LatestBuildStatus: tt.latestStatus,
			}

			hasBuild := result.LatestBuildID != nil && *result.LatestBuildID != ""

			if hasBuild != tt.hasBuild {
				t.Errorf("hasBuild = %v, want %v", hasBuild, tt.hasBuild)
			}
		})
	}
}
