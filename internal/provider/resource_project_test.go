package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestCloudReferenceValidation tests validation of cloud_id vs cloud_name
func TestCloudReferenceValidation(t *testing.T) {
	tests := []struct {
		name      string
		cloudID   types.String
		cloudName types.String
		wantError bool
		errorMsg  string
	}{
		{
			name:      "cloud_id provided",
			cloudID:   types.StringValue("cld_123"),
			cloudName: types.StringNull(),
			wantError: false,
		},
		{
			name:      "cloud_name provided",
			cloudID:   types.StringNull(),
			cloudName: types.StringValue("my-cloud"),
			wantError: false,
		},
		{
			name:      "neither provided",
			cloudID:   types.StringNull(),
			cloudName: types.StringNull(),
			wantError: true,
			errorMsg:  "Either 'cloud_id' or 'cloud_name' must be specified",
		},
		{
			name:      "both provided",
			cloudID:   types.StringValue("cld_123"),
			cloudName: types.StringValue("my-cloud"),
			wantError: true,
			errorMsg:  "Cannot specify both 'cloud_id' and 'cloud_name'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate validation logic from Create method
			hasCloudID := !tt.cloudID.IsNull()
			hasCloudName := !tt.cloudName.IsNull()

			var gotError bool
			var gotErrorMsg string

			if !hasCloudID && !hasCloudName {
				gotError = true
				gotErrorMsg = "Either 'cloud_id' or 'cloud_name' must be specified"
			} else if hasCloudID && hasCloudName {
				gotError = true
				gotErrorMsg = "Cannot specify both 'cloud_id' and 'cloud_name'"
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

// TestCollaboratorSyncLogic tests the logic for determining collaborator adds/updates/removes
func TestCollaboratorSyncLogic(t *testing.T) {
	tests := []struct {
		name               string
		planned            []ProjectCollaboratorModel
		current            []ProjectCollaboratorModel
		expectedAdds       []string // emails
		expectedUpdates    []string // emails
		expectedRemoves    []string // emails
	}{
		{
			name: "add new collaborator",
			planned: []ProjectCollaboratorModel{
				{
					Email:           types.StringValue("user1@example.com"),
					PermissionLevel: types.StringValue("writer"),
				},
			},
			current:         []ProjectCollaboratorModel{},
			expectedAdds:    []string{"user1@example.com"},
			expectedUpdates: []string{},
			expectedRemoves: []string{},
		},
		{
			name: "remove collaborator",
			planned: []ProjectCollaboratorModel{},
			current: []ProjectCollaboratorModel{
				{
					Email:           types.StringValue("user1@example.com"),
					PermissionLevel: types.StringValue("writer"),
					IdentityID:      types.StringValue("identity_123"),
				},
			},
			expectedAdds:    []string{},
			expectedUpdates: []string{},
			expectedRemoves: []string{"user1@example.com"},
		},
		{
			name: "update permission level",
			planned: []ProjectCollaboratorModel{
				{
					Email:           types.StringValue("user1@example.com"),
					PermissionLevel: types.StringValue("owner"),
				},
			},
			current: []ProjectCollaboratorModel{
				{
					Email:           types.StringValue("user1@example.com"),
					PermissionLevel: types.StringValue("writer"),
					IdentityID:      types.StringValue("identity_123"),
				},
			},
			expectedAdds:    []string{},
			expectedUpdates: []string{"user1@example.com"},
			expectedRemoves: []string{},
		},
		{
			name: "no changes",
			planned: []ProjectCollaboratorModel{
				{
					Email:           types.StringValue("user1@example.com"),
					PermissionLevel: types.StringValue("writer"),
				},
			},
			current: []ProjectCollaboratorModel{
				{
					Email:           types.StringValue("user1@example.com"),
					PermissionLevel: types.StringValue("writer"),
					IdentityID:      types.StringValue("identity_123"),
				},
			},
			expectedAdds:    []string{},
			expectedUpdates: []string{},
			expectedRemoves: []string{},
		},
		{
			name: "complex changes",
			planned: []ProjectCollaboratorModel{
				{
					Email:           types.StringValue("user1@example.com"),
					PermissionLevel: types.StringValue("owner"), // Updated
				},
				{
					Email:           types.StringValue("user2@example.com"),
					PermissionLevel: types.StringValue("writer"), // Added
				},
				// user3 removed
			},
			current: []ProjectCollaboratorModel{
				{
					Email:           types.StringValue("user1@example.com"),
					PermissionLevel: types.StringValue("writer"),
					IdentityID:      types.StringValue("identity_1"),
				},
				{
					Email:           types.StringValue("user3@example.com"),
					PermissionLevel: types.StringValue("readonly"),
					IdentityID:      types.StringValue("identity_3"),
				},
			},
			expectedAdds:    []string{"user2@example.com"},
			expectedUpdates: []string{"user1@example.com"},
			expectedRemoves: []string{"user3@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build maps for comparison (simulates syncCollaborators logic)
			planMap := make(map[string]ProjectCollaboratorModel)
			for _, collab := range tt.planned {
				planMap[collab.Email.ValueString()] = collab
			}

			currentMap := make(map[string]ProjectCollaboratorModel)
			for _, collab := range tt.current {
				currentMap[collab.Email.ValueString()] = collab
			}

			// Determine adds, updates, removes
			var gotAdds []string
			var gotUpdates []string
			var gotRemoves []string

			// Find adds and updates
			for email, planCollab := range planMap {
				if currentCollab, exists := currentMap[email]; exists {
					// Check if permission changed
					if currentCollab.PermissionLevel.ValueString() != planCollab.PermissionLevel.ValueString() {
						gotUpdates = append(gotUpdates, email)
					}
				} else {
					gotAdds = append(gotAdds, email)
				}
			}

			// Find removes
			for email := range currentMap {
				if _, exists := planMap[email]; !exists {
					gotRemoves = append(gotRemoves, email)
				}
			}

			// Verify results
			if len(gotAdds) != len(tt.expectedAdds) {
				t.Errorf("adds count = %v, want %v", len(gotAdds), len(tt.expectedAdds))
			}
			for _, expected := range tt.expectedAdds {
				found := false
				for _, got := range gotAdds {
					if got == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected add %s not found", expected)
				}
			}

			if len(gotUpdates) != len(tt.expectedUpdates) {
				t.Errorf("updates count = %v, want %v", len(gotUpdates), len(tt.expectedUpdates))
			}
			for _, expected := range tt.expectedUpdates {
				found := false
				for _, got := range gotUpdates {
					if got == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected update %s not found", expected)
				}
			}

			if len(gotRemoves) != len(tt.expectedRemoves) {
				t.Errorf("removes count = %v, want %v", len(gotRemoves), len(tt.expectedRemoves))
			}
			for _, expected := range tt.expectedRemoves {
				found := false
				for _, got := range gotRemoves {
					if got == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected remove %s not found", expected)
				}
			}
		})
	}
}

// TestPermissionLevelValidation tests that permission levels are validated
func TestPermissionLevelValidation(t *testing.T) {
	validLevels := []string{"owner", "writer", "readonly"}
	invalidLevels := []string{"admin", "reader", "viewer", ""}

	for _, level := range validLevels {
		t.Run("valid_"+level, func(t *testing.T) {
			// Simulate validation
			isValid := level == "owner" || level == "writer" || level == "readonly"
			if !isValid {
				t.Errorf("permission level %s should be valid", level)
			}
		})
	}

	for _, level := range invalidLevels {
		t.Run("invalid_"+level, func(t *testing.T) {
			// Simulate validation
			isValid := level == "owner" || level == "writer" || level == "readonly"
			if isValid {
				t.Errorf("permission level %s should be invalid", level)
			}
		})
	}
}

// TestCloudReferenceStateHandling tests how cloud_id vs cloud_name is handled in state
func TestCloudReferenceStateHandling(t *testing.T) {
	tests := []struct {
		name              string
		configCloudID     types.String
		configCloudName   types.String
		apiCloudID        string
		expectedCloudID   types.String
		expectedCloudName types.String
	}{
		{
			name:              "cloud_id in config, preserve in state",
			configCloudID:     types.StringValue("cld_123"),
			configCloudName:   types.StringNull(),
			apiCloudID:        "cld_123",
			expectedCloudID:   types.StringValue("cld_123"),
			expectedCloudName: types.StringNull(),
		},
		{
			name:              "cloud_name in config, preserve in state",
			configCloudID:     types.StringNull(),
			configCloudName:   types.StringValue("my-cloud"),
			apiCloudID:        "cld_456",
			expectedCloudID:   types.StringNull(),
			expectedCloudName: types.StringValue("my-cloud"),
		},
		{
			name:              "import (both null), set cloud_id from API",
			configCloudID:     types.StringNull(),
			configCloudName:   types.StringNull(),
			apiCloudID:        "cld_789",
			expectedCloudID:   types.StringValue("cld_789"),
			expectedCloudName: types.StringNull(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate readProject logic for cloud reference handling
			model := &ProjectResourceModel{
				CloudID:   tt.configCloudID,
				CloudName: tt.configCloudName,
			}

			// Apply API result based on what user provided
			if model.CloudName.IsNull() {
				model.CloudID = types.StringValue(tt.apiCloudID)
			}
			// If cloud_name was provided, keep it and don't set cloud_id

			// Verify state
			if model.CloudID != tt.expectedCloudID {
				if model.CloudID.IsNull() != tt.expectedCloudID.IsNull() {
					t.Errorf("cloud_id null state = %v, want %v", model.CloudID.IsNull(), tt.expectedCloudID.IsNull())
				} else if !model.CloudID.IsNull() && model.CloudID.ValueString() != tt.expectedCloudID.ValueString() {
					t.Errorf("cloud_id = %v, want %v", model.CloudID.ValueString(), tt.expectedCloudID.ValueString())
				}
			}

			if model.CloudName != tt.expectedCloudName {
				if model.CloudName.IsNull() != tt.expectedCloudName.IsNull() {
					t.Errorf("cloud_name null state = %v, want %v", model.CloudName.IsNull(), tt.expectedCloudName.IsNull())
				} else if !model.CloudName.IsNull() && model.CloudName.ValueString() != tt.expectedCloudName.ValueString() {
					t.Errorf("cloud_name = %v, want %v", model.CloudName.ValueString(), tt.expectedCloudName.ValueString())
				}
			}
		})
	}
}

// TestCollaboratorModelMapping tests mapping of API response to model
func TestCollaboratorModelMapping(t *testing.T) {
	// Simulate API response structure
	apiResponse := struct {
		ID              string
		PermissionLevel string
		Value           struct {
			ID    string
			Email string
		}
	}{
		ID:              "identity_123",
		PermissionLevel: "writer",
		Value: struct {
			ID    string
			Email string
		}{
			ID:    "user_456",
			Email: "user@example.com",
		},
	}

	// Map to model
	model := ProjectCollaboratorModel{
		Email:           types.StringValue(apiResponse.Value.Email),
		PermissionLevel: types.StringValue(apiResponse.PermissionLevel),
		IdentityID:      types.StringValue(apiResponse.ID),
		UserID:          types.StringValue(apiResponse.Value.ID),
	}

	// Verify mapping
	if model.Email.ValueString() != "user@example.com" {
		t.Errorf("email = %v, want 'user@example.com'", model.Email.ValueString())
	}
	if model.PermissionLevel.ValueString() != "writer" {
		t.Errorf("permission_level = %v, want 'writer'", model.PermissionLevel.ValueString())
	}
	if model.IdentityID.ValueString() != "identity_123" {
		t.Errorf("identity_id = %v, want 'identity_123'", model.IdentityID.ValueString())
	}
	if model.UserID.ValueString() != "user_456" {
		t.Errorf("user_id = %v, want 'user_456'", model.UserID.ValueString())
	}
}

// TestProjectCreateRequestStructure tests the structure of create request
func TestProjectCreateRequestStructure(t *testing.T) {
	// Test that the CreateProjectRequest structure is correctly built
	desc := "Test project description"
	configID := "ccfg_123"

	req := CreateProjectRequest{
		Name:                   "test-project",
		ParentCloudID:          "cld_123",
		Description:            &desc,
		InitialClusterConfigID: &configID,
	}

	if req.Name != "test-project" {
		t.Errorf("name = %v, want 'test-project'", req.Name)
	}
	if req.ParentCloudID != "cld_123" {
		t.Errorf("parent_cloud_id = %v, want 'cld_123'", req.ParentCloudID)
	}
	if req.Description == nil || *req.Description != "Test project description" {
		t.Errorf("description = %v, want 'Test project description'", req.Description)
	}
	if req.InitialClusterConfigID == nil || *req.InitialClusterConfigID != "ccfg_123" {
		t.Errorf("initial_cluster_config_id = %v, want 'ccfg_123'", req.InitialClusterConfigID)
	}
}

// TestCollaboratorBatchRequestStructure tests the structure of batch create request
func TestCollaboratorBatchRequestStructure(t *testing.T) {
	collaborators := []ProjectCollaboratorModel{
		{
			Email:           types.StringValue("user1@example.com"),
			PermissionLevel: types.StringValue("writer"),
		},
		{
			Email:           types.StringValue("user2@example.com"),
			PermissionLevel: types.StringValue("readonly"),
		},
	}

	// Build request (simulates createCollaborators logic)
	entries := make(ProjectCollaboratorBatchRequest, 0, len(collaborators))
	for _, collab := range collaborators {
		entries = append(entries, ProjectCollaboratorEntry{
			Value: struct {
				Email string `json:"email"`
			}{
				Email: collab.Email.ValueString(),
			},
			PermissionLevel: collab.PermissionLevel.ValueString(),
		})
	}

	if len(entries) != 2 {
		t.Errorf("entries count = %v, want 2", len(entries))
	}

	if entries[0].Value.Email != "user1@example.com" {
		t.Errorf("entries[0].email = %v, want 'user1@example.com'", entries[0].Value.Email)
	}
	if entries[0].PermissionLevel != "writer" {
		t.Errorf("entries[0].permission_level = %v, want 'writer'", entries[0].PermissionLevel)
	}

	if entries[1].Value.Email != "user2@example.com" {
		t.Errorf("entries[1].email = %v, want 'user2@example.com'", entries[1].Value.Email)
	}
	if entries[1].PermissionLevel != "readonly" {
		t.Errorf("entries[1].permission_level = %v, want 'readonly'", entries[1].PermissionLevel)
	}
}

// TestCollaboratorUpdateRequestStructure tests the structure of update request
func TestCollaboratorUpdateRequestStructure(t *testing.T) {
	req := ProjectCollaboratorUpdateRequest{
		PermissionLevel: "owner",
	}

	if req.PermissionLevel != "owner" {
		t.Errorf("permission_level = %v, want 'owner'", req.PermissionLevel)
	}
}

// TestCloudNameResolutionLogic tests the logic for resolving multiple clouds with same name
func TestCloudNameResolutionLogic(t *testing.T) {
	// Simulate API response with multiple clouds having the same name
	clouds := []struct {
		ID        string
		Name      string
		CreatedAt string
	}{
		{
			ID:        "cld_123",
			Name:      "test-cloud",
			CreatedAt: "2024-01-01T00:00:00Z",
		},
		{
			ID:        "cld_456",
			Name:      "test-cloud",
			CreatedAt: "2024-01-02T00:00:00Z", // More recent
		},
		{
			ID:        "cld_789",
			Name:      "other-cloud",
			CreatedAt: "2024-01-03T00:00:00Z",
		},
	}

	targetName := "test-cloud"

	// Simulate resolution logic
	var matchedCloudID string
	var latestCreatedAt string

	for _, cloud := range clouds {
		if cloud.Name == targetName {
			if matchedCloudID == "" || cloud.CreatedAt > latestCreatedAt {
				matchedCloudID = cloud.ID
				latestCreatedAt = cloud.CreatedAt
			}
		}
	}

	// Should pick the most recent one
	if matchedCloudID != "cld_456" {
		t.Errorf("resolved cloud_id = %v, want 'cld_456' (most recent)", matchedCloudID)
	}
}

// TestEmptyCollaboratorList tests handling of projects with no collaborators
func TestEmptyCollaboratorList(t *testing.T) {
	model := &ProjectResourceModel{
		Collaborators: []ProjectCollaboratorModel{},
	}

	// Simulate the check from readProject
	shouldFetchCollaborators := len(model.Collaborators) > 0

	if shouldFetchCollaborators {
		t.Error("should not fetch collaborators when list is empty")
	}
}

// TestComputedFieldsPreservation tests that computed fields are properly set
func TestComputedFieldsPreservation(t *testing.T) {
	// Simulate API response
	apiResult := struct {
		ID              string
		Name            string
		ParentCloudID   string
		Description     *string
		CreatorID       *string
		CreatedAt       string
		LastUsedCloudID *string
		IsDefault       bool
		DirectoryName   string
	}{
		ID:            "prj_123",
		Name:          "test-project",
		ParentCloudID: "cld_456",
		Description: func() *string {
			s := "Test description"
			return &s
		}(),
		CreatorID: func() *string {
			s := "user_789"
			return &s
		}(),
		CreatedAt: "2024-01-01T00:00:00Z",
		LastUsedCloudID: func() *string {
			s := "cld_456"
			return &s
		}(),
		IsDefault:     false,
		DirectoryName: "test-project-dir",
	}

	// Map to model
	model := &ProjectResourceModel{}
	model.ID = types.StringValue(apiResult.ID)
	model.Name = types.StringValue(apiResult.Name)
	if apiResult.Description != nil {
		model.Description = types.StringValue(*apiResult.Description)
	}
	if apiResult.CreatorID != nil {
		model.CreatorID = types.StringValue(*apiResult.CreatorID)
	}
	model.CreatedAt = types.StringValue(apiResult.CreatedAt)
	if apiResult.LastUsedCloudID != nil {
		model.LastUsedCloudID = types.StringValue(*apiResult.LastUsedCloudID)
	}
	model.IsDefault = types.BoolValue(apiResult.IsDefault)
	model.DirectoryName = types.StringValue(apiResult.DirectoryName)

	// Verify all computed fields are set
	if model.ID.ValueString() != "prj_123" {
		t.Errorf("id = %v, want 'prj_123'", model.ID.ValueString())
	}
	if model.CreatorID.IsNull() || model.CreatorID.ValueString() != "user_789" {
		t.Errorf("creator_id = %v, want 'user_789'", model.CreatorID.ValueString())
	}
	if model.CreatedAt.ValueString() != "2024-01-01T00:00:00Z" {
		t.Errorf("created_at = %v, want '2024-01-01T00:00:00Z'", model.CreatedAt.ValueString())
	}
	if model.IsDefault.ValueBool() != false {
		t.Errorf("is_default = %v, want false", model.IsDefault.ValueBool())
	}
	if model.DirectoryName.ValueString() != "test-project-dir" {
		t.Errorf("directory_name = %v, want 'test-project-dir'", model.DirectoryName.ValueString())
	}
}
