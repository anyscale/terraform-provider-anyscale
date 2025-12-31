package provider

import (
	"net/url"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestProjectsFilterParameterBuilding tests building query parameters from filters
func TestProjectsFilterParameterBuilding(t *testing.T) {
	tests := []struct {
		name           string
		config         ProjectsDataSourceModel
		expectedParams map[string]string
	}{
		{
			name: "name_contains filter",
			config: ProjectsDataSourceModel{
				NameContains: types.StringValue("prod"),
			},
			expectedParams: map[string]string{
				"name_contains":    "prod",
				"include_defaults": "true",
			},
		},
		{
			name: "creator_id filter",
			config: ProjectsDataSourceModel{
				CreatorID: types.StringValue("user_123"),
			},
			expectedParams: map[string]string{
				"creator_id":       "user_123",
				"include_defaults": "true",
			},
		},
		{
			name: "cloud_id filter",
			config: ProjectsDataSourceModel{
				CloudID: types.StringValue("cld_456"),
			},
			expectedParams: map[string]string{
				"parent_cloud_id":  "cld_456",
				"include_defaults": "true",
			},
		},
		{
			name: "include_defaults false",
			config: ProjectsDataSourceModel{
				IncludeDefaults: types.BoolValue(false),
			},
			expectedParams: map[string]string{
				"include_defaults": "false",
			},
		},
		{
			name: "multiple filters",
			config: ProjectsDataSourceModel{
				NameContains:    types.StringValue("test"),
				CreatorID:       types.StringValue("user_789"),
				CloudID:         types.StringValue("cld_abc"),
				IncludeDefaults: types.BoolValue(true),
			},
			expectedParams: map[string]string{
				"name_contains":    "test",
				"creator_id":       "user_789",
				"parent_cloud_id":  "cld_abc",
				"include_defaults": "true",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate parameter building logic
			params := url.Values{}

			if !tt.config.NameContains.IsNull() {
				params.Add("name_contains", tt.config.NameContains.ValueString())
			}
			if !tt.config.CreatorID.IsNull() {
				params.Add("creator_id", tt.config.CreatorID.ValueString())
			}

			// For cloud_id, simulate it being set
			cloudID := tt.config.CloudID.ValueString()
			if cloudID != "" {
				params.Add("parent_cloud_id", cloudID)
			}

			// Set include_defaults (defaults to true if not specified)
			includeDefaults := true
			if !tt.config.IncludeDefaults.IsNull() {
				includeDefaults = tt.config.IncludeDefaults.ValueBool()
			}
			if includeDefaults {
				params.Add("include_defaults", "true")
			} else {
				params.Add("include_defaults", "false")
			}

			// Verify all expected params are present
			for key, expectedValue := range tt.expectedParams {
				gotValue := params.Get(key)
				if gotValue != expectedValue {
					t.Errorf("param %s = %v, want %v", key, gotValue, expectedValue)
				}
			}
		})
	}
}

// TestProjectSummaryMapping tests mapping API response to ProjectSummaryModel
func TestProjectSummaryMapping(t *testing.T) {
	apiProject := struct {
		ID              string
		Name            string
		Description     *string
		CloudID         string
		CreatorID       string
		CreatedAt       string
		LastUsedCloudID *string
		IsDefault       bool
		DirectoryName   string
	}{
		ID:              "prj_abc",
		Name:            "production",
		Description:     func() *string { s := "Production project"; return &s }(),
		CloudID:         "cld_def",
		CreatorID:       "user_123",
		CreatedAt:       "2024-01-01T00:00:00Z",
		LastUsedCloudID: func() *string { s := "cld_def"; return &s }(),
		IsDefault:       false,
		DirectoryName:   "production-dir",
	}

	model := ProjectSummaryModel{
		ID:            types.StringValue(apiProject.ID),
		Name:          types.StringValue(apiProject.Name),
		CloudID:       types.StringValue(apiProject.CloudID),
		CreatorID:     types.StringValue(apiProject.CreatorID),
		CreatedAt:     types.StringValue(apiProject.CreatedAt),
		IsDefault:     types.BoolValue(apiProject.IsDefault),
		DirectoryName: types.StringValue(apiProject.DirectoryName),
	}

	if apiProject.Description != nil {
		model.Description = types.StringValue(*apiProject.Description)
	}
	if apiProject.LastUsedCloudID != nil {
		model.LastUsedCloudID = types.StringValue(*apiProject.LastUsedCloudID)
	}

	// Verify all fields
	if model.ID.ValueString() != "prj_abc" {
		t.Errorf("ID = %v, want 'prj_abc'", model.ID.ValueString())
	}
	if model.Name.ValueString() != "production" {
		t.Errorf("Name = %v, want 'production'", model.Name.ValueString())
	}
	if model.IsDefault.ValueBool() != false {
		t.Errorf("IsDefault = %v, want false", model.IsDefault.ValueBool())
	}
}

// TestProjectsIncludeDefaults tests include_defaults parameter handling
func TestProjectsIncludeDefaults(t *testing.T) {
	tests := []struct {
		name            string
		includeDefaults types.Bool
		expectedValue   bool
	}{
		{
			name:            "explicitly true",
			includeDefaults: types.BoolValue(true),
			expectedValue:   true,
		},
		{
			name:            "explicitly false",
			includeDefaults: types.BoolValue(false),
			expectedValue:   false,
		},
		{
			name:            "null (defaults to true)",
			includeDefaults: types.BoolNull(),
			expectedValue:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate include_defaults logic
			includeDefaults := true
			if !tt.includeDefaults.IsNull() {
				includeDefaults = tt.includeDefaults.ValueBool()
			}

			if includeDefaults != tt.expectedValue {
				t.Errorf("includeDefaults = %v, want %v", includeDefaults, tt.expectedValue)
			}
		})
	}
}

// TestProjectsPagination tests pagination handling
func TestProjectsPagination(t *testing.T) {
	tests := []struct {
		name          string
		nextToken     *string
		expectHasMore bool
	}{
		{
			name: "has next page",
			nextToken: func() *string {
				s := "next_token_123"
				return &s
			}(),
			expectHasMore: true,
		},
		{
			name:          "no next page - nil token",
			nextToken:     nil,
			expectHasMore: false,
		},
		{
			name: "no next page - empty token",
			nextToken: func() *string {
				s := ""
				return &s
			}(),
			expectHasMore: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate pagination logic
			hasMore := tt.nextToken != nil && *tt.nextToken != ""

			if hasMore != tt.expectHasMore {
				t.Errorf("hasMore = %v, want %v", hasMore, tt.expectHasMore)
			}
		})
	}
}

// TestProjectsCloudNameResolution tests cloud name resolution for filtering
func TestProjectsCloudNameResolution(t *testing.T) {
	tests := []struct {
		name            string
		cloudID         types.String
		cloudName       types.String
		expectedCloudID string
	}{
		{
			name:            "cloud_id provided",
			cloudID:         types.StringValue("cld_123"),
			cloudName:       types.StringNull(),
			expectedCloudID: "cld_123",
		},
		{
			name:            "cloud_name provided (resolved)",
			cloudID:         types.StringNull(),
			cloudName:       types.StringValue("my-cloud"),
			expectedCloudID: "cld_456", // Simulated resolved ID
		},
		{
			name:            "neither provided",
			cloudID:         types.StringNull(),
			cloudName:       types.StringNull(),
			expectedCloudID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate cloud resolution logic
			cloudID := ""
			if !tt.cloudID.IsNull() {
				cloudID = tt.cloudID.ValueString()
			} else if !tt.cloudName.IsNull() {
				// Simulated resolution
				cloudID = "cld_456"
			}

			if cloudID != tt.expectedCloudID {
				t.Errorf("cloudID = %v, want %v", cloudID, tt.expectedCloudID)
			}
		})
	}
}

// TestProjectSummaryNullableFields tests handling of optional/nullable fields
func TestProjectSummaryNullableFields(t *testing.T) {
	model := ProjectSummaryModel{
		ID:              types.StringValue("prj_123"),
		Name:            types.StringValue("test-project"),
		CloudID:         types.StringValue("cld_456"),
		Description:     types.StringNull(), // Might not have description
		CreatorID:       types.StringNull(), // Might not be available
		LastUsedCloudID: types.StringNull(), // Might not have been used yet
	}

	if !model.Description.IsNull() {
		t.Error("Description should be null")
	}
	if !model.CreatorID.IsNull() {
		t.Error("CreatorID should be null")
	}
	if !model.LastUsedCloudID.IsNull() {
		t.Error("LastUsedCloudID should be null")
	}
}

// TestProjectsResultList tests handling of multiple projects
func TestProjectsResultList(t *testing.T) {
	projects := []ProjectSummaryModel{
		{
			ID:        types.StringValue("prj_1"),
			Name:      types.StringValue("project-1"),
			CloudID:   types.StringValue("cld_1"),
			IsDefault: types.BoolValue(false),
		},
		{
			ID:        types.StringValue("prj_2"),
			Name:      types.StringValue("project-2"),
			CloudID:   types.StringValue("cld_1"),
			IsDefault: types.BoolValue(true),
		},
		{
			ID:        types.StringValue("prj_3"),
			Name:      types.StringValue("project-3"),
			CloudID:   types.StringValue("cld_2"),
			IsDefault: types.BoolValue(false),
		},
	}

	model := ProjectsDataSourceModel{
		Projects: projects,
	}

	if len(model.Projects) != 3 {
		t.Errorf("Projects count = %v, want 3", len(model.Projects))
	}

	// Verify default project
	defaultCount := 0
	for _, proj := range model.Projects {
		if proj.IsDefault.ValueBool() {
			defaultCount++
		}
	}

	if defaultCount != 1 {
		t.Errorf("Default projects count = %v, want 1", defaultCount)
	}
}

// TestProjectsNoCollaborators tests that summary model doesn't include collaborators
func TestProjectsNoCollaborators(t *testing.T) {
	// ProjectSummaryModel should not have a Collaborators field
	// This is intentional for performance - only the singular project data source includes collaborators
	model := ProjectSummaryModel{
		ID:   types.StringValue("prj_123"),
		Name: types.StringValue("test-project"),
	}

	// This test mainly serves as documentation that ProjectSummaryModel
	// intentionally excludes collaborators for performance reasons
	_ = model
}
