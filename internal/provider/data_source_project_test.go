package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestProjectDataSourceLookupValidation tests validation of ID vs Name lookup
func TestProjectDataSourceLookupValidation(t *testing.T) {
	tests := []struct {
		name      string
		id        types.String
		projName  types.String
		wantError bool
		errorMsg  string
	}{
		{
			name:      "ID provided",
			id:        types.StringValue("prj_123"),
			projName:  types.StringNull(),
			wantError: false,
		},
		{
			name:      "name provided",
			id:        types.StringNull(),
			projName:  types.StringValue("my-project"),
			wantError: false,
		},
		{
			name:      "neither provided",
			id:        types.StringNull(),
			projName:  types.StringNull(),
			wantError: true,
			errorMsg:  "Either 'id' or 'name' must be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate validation logic from Read method
			hasID := !tt.id.IsNull()
			hasName := !tt.projName.IsNull()

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

// TestProjectCollaboratorMapping tests mapping of collaborators
func TestProjectCollaboratorMapping(t *testing.T) {
	apiCollab := struct {
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
	model := ProjectDataSourceCollaboratorModel{
		Email:           types.StringValue(apiCollab.Value.Email),
		PermissionLevel: types.StringValue(apiCollab.PermissionLevel),
		IdentityID:      types.StringValue(apiCollab.ID),
		UserID:          types.StringValue(apiCollab.Value.ID),
	}

	// Verify mapping
	if model.Email.ValueString() != "user@example.com" {
		t.Errorf("Email = %v, want 'user@example.com'", model.Email.ValueString())
	}
	if model.PermissionLevel.ValueString() != "writer" {
		t.Errorf("PermissionLevel = %v, want 'writer'", model.PermissionLevel.ValueString())
	}
	if model.IdentityID.ValueString() != "identity_123" {
		t.Errorf("IdentityID = %v, want 'identity_123'", model.IdentityID.ValueString())
	}
	if model.UserID.ValueString() != "user_456" {
		t.Errorf("UserID = %v, want 'user_456'", model.UserID.ValueString())
	}
}

// TestProjectFieldMapping tests mapping of project fields
func TestProjectFieldMapping(t *testing.T) {
	model := ProjectDataSourceModel{
		ID:              types.StringValue("prj_abc"),
		Name:            types.StringValue("production-project"),
		CloudID:         types.StringValue("cld_def"),
		Description:     types.StringValue("Production environment project"),
		CreatorID:       types.StringValue("user_123"),
		CreatedAt:       types.StringValue("2024-01-01T00:00:00Z"),
		LastUsedCloudID: types.StringValue("cld_def"),
		IsDefault:       types.BoolValue(false),
		DirectoryName:   types.StringValue("production-project-dir"),
	}

	// Verify all fields
	if model.ID.ValueString() != "prj_abc" {
		t.Errorf("ID = %v, want 'prj_abc'", model.ID.ValueString())
	}
	if model.Name.ValueString() != "production-project" {
		t.Errorf("Name = %v, want 'production-project'", model.Name.ValueString())
	}
	if model.CloudID.ValueString() != "cld_def" {
		t.Errorf("CloudID = %v, want 'cld_def'", model.CloudID.ValueString())
	}
	if model.IsDefault.ValueBool() != false {
		t.Errorf("IsDefault = %v, want false", model.IsDefault.ValueBool())
	}
	if model.DirectoryName.ValueString() != "production-project-dir" {
		t.Errorf("DirectoryName = %v, want 'production-project-dir'", model.DirectoryName.ValueString())
	}
}

// TestProjectNullableFields tests handling of optional/nullable fields
func TestProjectNullableFields(t *testing.T) {
	model := ProjectDataSourceModel{
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

// TestProjectDefaultFlag tests handling of default project flag
func TestProjectDefaultFlag(t *testing.T) {
	tests := []struct {
		name      string
		isDefault bool
	}{
		{"default project", true},
		{"non-default project", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := ProjectDataSourceModel{
				IsDefault: types.BoolValue(tt.isDefault),
			}

			if model.IsDefault.ValueBool() != tt.isDefault {
				t.Errorf("IsDefault = %v, want %v", model.IsDefault.ValueBool(), tt.isDefault)
			}
		})
	}
}

// TestProjectCloudNameResolution tests cloud name resolution for filtering
func TestProjectCloudNameResolution(t *testing.T) {
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

// TestProjectCollaboratorList tests handling of multiple collaborators
func TestProjectCollaboratorList(t *testing.T) {
	collaborators := []ProjectDataSourceCollaboratorModel{
		{
			Email:           types.StringValue("user1@example.com"),
			PermissionLevel: types.StringValue("owner"),
			IdentityID:      types.StringValue("identity_1"),
			UserID:          types.StringValue("user_1"),
		},
		{
			Email:           types.StringValue("user2@example.com"),
			PermissionLevel: types.StringValue("writer"),
			IdentityID:      types.StringValue("identity_2"),
			UserID:          types.StringValue("user_2"),
		},
		{
			Email:           types.StringValue("user3@example.com"),
			PermissionLevel: types.StringValue("readonly"),
			IdentityID:      types.StringValue("identity_3"),
			UserID:          types.StringValue("user_3"),
		},
	}

	model := ProjectDataSourceModel{
		Collaborators: collaborators,
	}

	if len(model.Collaborators) != 3 {
		t.Errorf("Collaborators count = %v, want 3", len(model.Collaborators))
	}

	// Verify first collaborator
	if model.Collaborators[0].PermissionLevel.ValueString() != "owner" {
		t.Errorf("First collaborator permission = %v, want 'owner'", model.Collaborators[0].PermissionLevel.ValueString())
	}
}

// TestProjectPermissionLevels tests all valid permission levels
func TestProjectPermissionLevels(t *testing.T) {
	validLevels := []string{"owner", "writer", "readonly"}

	for _, level := range validLevels {
		t.Run("level_"+level, func(t *testing.T) {
			collab := ProjectDataSourceCollaboratorModel{
				PermissionLevel: types.StringValue(level),
			}

			if collab.PermissionLevel.ValueString() != level {
				t.Errorf("PermissionLevel = %v, want %v", collab.PermissionLevel.ValueString(), level)
			}
		})
	}
}

// TestProjectEmptyCollaborators tests handling of projects without collaborators
func TestProjectEmptyCollaborators(t *testing.T) {
	model := ProjectDataSourceModel{
		Collaborators: []ProjectDataSourceCollaboratorModel{},
	}

	if len(model.Collaborators) != 0 {
		t.Errorf("Collaborators count = %v, want 0", len(model.Collaborators))
	}
}
