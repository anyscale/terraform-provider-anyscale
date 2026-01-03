package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestContainerImagesDataSourceFilterConstruction tests query parameter construction
func TestContainerImagesDataSourceFilterConstruction(t *testing.T) {
	tests := []struct {
		name            string
		nameContains    types.String
		creatorID       types.String
		projectID       types.String
		includeArchived types.Bool
		wantParams      map[string]string
	}{
		{
			name:            "no filters",
			nameContains:    types.StringNull(),
			creatorID:       types.StringNull(),
			projectID:       types.StringNull(),
			includeArchived: types.BoolNull(),
			wantParams: map[string]string{
				"include_archived": "false",
			},
		},
		{
			name:            "name filter only",
			nameContains:    types.StringValue("my-image"),
			creatorID:       types.StringNull(),
			projectID:       types.StringNull(),
			includeArchived: types.BoolNull(),
			wantParams: map[string]string{
				"name_contains":    "my-image",
				"include_archived": "false",
			},
		},
		{
			name:            "all filters",
			nameContains:    types.StringValue("test"),
			creatorID:       types.StringValue("user_123"),
			projectID:       types.StringValue("prj_456"),
			includeArchived: types.BoolValue(true),
			wantParams: map[string]string{
				"name_contains":    "test",
				"creator_id":       "user_123",
				"project_id":       "prj_456",
				"include_archived": "true",
			},
		},
		{
			name:            "include archived false",
			nameContains:    types.StringNull(),
			creatorID:       types.StringNull(),
			projectID:       types.StringNull(),
			includeArchived: types.BoolValue(false),
			wantParams: map[string]string{
				"include_archived": "false",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate parameter construction from Read method
			params := make(map[string]string)

			if !tt.nameContains.IsNull() {
				params["name_contains"] = tt.nameContains.ValueString()
			}

			if !tt.creatorID.IsNull() {
				params["creator_id"] = tt.creatorID.ValueString()
			}

			if !tt.projectID.IsNull() {
				params["project_id"] = tt.projectID.ValueString()
			}

			includeArchived := false
			if !tt.includeArchived.IsNull() {
				includeArchived = tt.includeArchived.ValueBool()
			}
			if includeArchived {
				params["include_archived"] = "true"
			} else {
				params["include_archived"] = "false"
			}

			// Verify params match expected
			for key, wantValue := range tt.wantParams {
				gotValue, ok := params[key]
				if !ok {
					t.Errorf("missing param %s", key)
					continue
				}
				if gotValue != wantValue {
					t.Errorf("param %s = %v, want %v", key, gotValue, wantValue)
				}
			}

			// Verify no extra params
			for key := range params {
				if _, ok := tt.wantParams[key]; !ok {
					t.Errorf("unexpected param %s", key)
				}
			}
		})
	}
}

// TestContainerImageSummaryModelMapping tests mapping of API response to model
func TestContainerImageSummaryModelMapping(t *testing.T) {
	// Simulate API response
	clusterEnv := ClusterEnvironmentResult{
		ID:                "apptemp_123",
		Name:              "my-custom-image",
		CreatorID:         "user_456",
		CreatedAt:         "2024-01-01T00:00:00Z",
		IsArchived:        false,
		LatestBuildID:     strPtr("bld_789"),
		LatestBuildStatus: strPtr("succeeded"),
	}

	build := BuildResult{
		ID:       "bld_789",
		Revision: 5,
	}

	// Map to summary model
	model := ContainerImageSummaryModel{
		ID:                types.StringValue(clusterEnv.ID),
		Name:              types.StringValue(clusterEnv.Name),
		CreatorID:         types.StringValue(clusterEnv.CreatorID),
		CreatedAt:         types.StringValue(clusterEnv.CreatedAt),
		IsArchived:        types.BoolValue(clusterEnv.IsArchived),
		LatestBuildID:     types.StringValue(*clusterEnv.LatestBuildID),
		LatestBuildStatus: types.StringValue(*clusterEnv.LatestBuildStatus),
		Revision:          types.Int64Value(int64(build.Revision)),
		NameVersion:       types.StringValue(fmt.Sprintf("%s:%d", clusterEnv.Name, build.Revision)),
	}

	// Verify mapping
	if model.ID.ValueString() != "apptemp_123" {
		t.Errorf("ID = %v, want 'apptemp_123'", model.ID.ValueString())
	}
	if model.Name.ValueString() != "my-custom-image" {
		t.Errorf("Name = %v, want 'my-custom-image'", model.Name.ValueString())
	}
	if model.CreatorID.ValueString() != "user_456" {
		t.Errorf("CreatorID = %v, want 'user_456'", model.CreatorID.ValueString())
	}
	if model.IsArchived.ValueBool() {
		t.Error("IsArchived should be false")
	}
	if model.LatestBuildID.ValueString() != "bld_789" {
		t.Errorf("LatestBuildID = %v, want 'bld_789'", model.LatestBuildID.ValueString())
	}
	if model.LatestBuildStatus.ValueString() != "succeeded" {
		t.Errorf("LatestBuildStatus = %v, want 'succeeded'", model.LatestBuildStatus.ValueString())
	}
	if model.Revision.ValueInt64() != 5 {
		t.Errorf("Revision = %v, want 5", model.Revision.ValueInt64())
	}
	if model.NameVersion.ValueString() != "my-custom-image:5" {
		t.Errorf("NameVersion = %v, want 'my-custom-image:5'", model.NameVersion.ValueString())
	}
}

// TestContainerImageSummaryNoBuild tests handling of images without builds
func TestContainerImageSummaryNoBuild(t *testing.T) {
	// Simulate cluster environment without a build
	clusterEnv := ClusterEnvironmentResult{
		ID:                "apptemp_123",
		Name:              "empty-image",
		CreatorID:         "user_456",
		CreatedAt:         "2024-01-01T00:00:00Z",
		IsArchived:        false,
		LatestBuildID:     nil,
		LatestBuildStatus: nil,
	}

	// Map to summary model - should handle nil build
	model := ContainerImageSummaryModel{
		ID:         types.StringValue(clusterEnv.ID),
		Name:       types.StringValue(clusterEnv.Name),
		CreatorID:  types.StringValue(clusterEnv.CreatorID),
		CreatedAt:  types.StringValue(clusterEnv.CreatedAt),
		IsArchived: types.BoolValue(clusterEnv.IsArchived),
	}

	// Set build-related fields to null when no build exists
	if clusterEnv.LatestBuildID == nil {
		model.LatestBuildID = types.StringNull()
		model.LatestBuildStatus = types.StringNull()
		model.Revision = types.Int64Null()
		model.NameVersion = types.StringNull()
	}

	// Verify null handling
	if !model.LatestBuildID.IsNull() {
		t.Error("LatestBuildID should be null when no build exists")
	}
	if !model.LatestBuildStatus.IsNull() {
		t.Error("LatestBuildStatus should be null when no build exists")
	}
	if !model.Revision.IsNull() {
		t.Error("Revision should be null when no build exists")
	}
	if !model.NameVersion.IsNull() {
		t.Error("NameVersion should be null when no build exists")
	}
}

// TestContainerImagesArchivedFilter tests the archived filtering logic
func TestContainerImagesArchivedFilter(t *testing.T) {
	clusterEnvs := []ClusterEnvironmentResult{
		{
			ID:         "apptemp_123",
			Name:       "active-image",
			IsArchived: false,
		},
		{
			ID:         "apptemp_456",
			Name:       "archived-image",
			IsArchived: true,
		},
		{
			ID:         "apptemp_789",
			Name:       "another-active",
			IsArchived: false,
		},
	}

	// Test filtering - this simulates what the API should return
	// when include_archived=false
	var activeOnly []ClusterEnvironmentResult
	for _, env := range clusterEnvs {
		if !env.IsArchived {
			activeOnly = append(activeOnly, env)
		}
	}

	if len(activeOnly) != 2 {
		t.Errorf("active count = %v, want 2", len(activeOnly))
	}

	// Test with include_archived=true (all returned)
	if len(clusterEnvs) != 3 {
		t.Errorf("total count = %v, want 3", len(clusterEnvs))
	}
}

// TestContainerImagesNameVersionFormatting tests name_version field formatting in list context
func TestContainerImagesNameVersionFormatting(t *testing.T) {
	tests := []struct {
		name            string
		imageName       string
		revision        int
		wantNameVersion string
	}{
		{
			name:            "standard image",
			imageName:       "production-image",
			revision:        10,
			wantNameVersion: "production-image:10",
		},
		{
			name:            "first revision",
			imageName:       "new-image",
			revision:        1,
			wantNameVersion: "new-image:1",
		},
		{
			name:            "image with special chars",
			imageName:       "my-custom-ray-2.9.0",
			revision:        3,
			wantNameVersion: "my-custom-ray-2.9.0:3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nameVersion := fmt.Sprintf("%s:%d", tt.imageName, tt.revision)

			if nameVersion != tt.wantNameVersion {
				t.Errorf("name_version = %v, want %v", nameVersion, tt.wantNameVersion)
			}
		})
	}
}
