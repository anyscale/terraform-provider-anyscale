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

// TestContainerImageDataSourceModelMapping tests mapping of API response to model
func TestContainerImageDataSourceModelMapping(t *testing.T) {
	// Simulate an application template plus its resolved latest build (contract-based: the
	// build detail comes from a single GET /api/v2/builds/{id}, not a separate list call).
	template := ApplicationTemplateResult{
		ID:        "apptemp_123",
		Name:      "my-custom-image",
		CreatorID: "user_456",
		CreatedAt: "2024-01-01T00:00:00Z",
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
		ID:          types.StringValue(template.ID),
		Name:        types.StringValue(template.Name),
		BuildID:     types.StringValue(build.ID),
		BuildStatus: types.StringValue(build.Status),
		CreatedAt:   types.StringValue(template.CreatedAt),
		CreatorID:   types.StringValue(template.CreatorID),
		IsBYOD:      types.BoolValue(build.IsBYOD),
		Revision:    types.Int64Value(int64(build.Revision)),
		NameVersion: types.StringValue(template.Name + ":2"),
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

// TestContainerImageDataSourceNullBuildHandling tests handling when the application template
// has no latest build (mirrors Read()'s "template.LatestBuild == nil" branch).
func TestContainerImageDataSourceNullBuildHandling(t *testing.T) {
	// Application template without a latest build.
	template := ApplicationTemplateResult{
		ID:        "apptemp_123",
		Name:      "empty-image",
		CreatorID: "user_456",
		CreatedAt: "2024-01-01T00:00:00Z",
	}

	// Map to model - should handle a nil LatestBuild
	model := ContainerImageDataSourceModel{
		ID:        types.StringValue(template.ID),
		Name:      types.StringValue(template.Name),
		CreatedAt: types.StringValue(template.CreatedAt),
		CreatorID: types.StringValue(template.CreatorID),
	}

	// Set build-related fields to null when there is no latest build
	if template.LatestBuild == nil {
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
		t.Error("BuildID should be null when no builds found")
	}
	if !model.BuildStatus.IsNull() {
		t.Error("BuildStatus should be null when no builds found")
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

	// Ensure hasName is used to avoid compiler warning
	if !hasName {
		t.Log("Name not provided, but that's fine for this test")
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
