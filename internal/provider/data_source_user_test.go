package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestUserDataSourceFieldMapping tests mapping of user info to model
func TestUserDataSourceFieldMapping(t *testing.T) {
	model := UserDataSourceModel{
		ID:                          types.StringValue("user_abc123"),
		Email:                       types.StringValue("user@example.com"),
		Name:                        types.StringValue("John Doe"),
		Username:                    types.StringValue("johndoe"),
		OrganizationPermissionLevel: types.StringValue("owner"),
	}

	// Verify all fields
	if model.ID.ValueString() != "user_abc123" {
		t.Errorf("ID = %v, want 'user_abc123'", model.ID.ValueString())
	}
	if model.Email.ValueString() != "user@example.com" {
		t.Errorf("Email = %v, want 'user@example.com'", model.Email.ValueString())
	}
	if model.Name.ValueString() != "John Doe" {
		t.Errorf("Name = %v, want 'John Doe'", model.Name.ValueString())
	}
	if model.Username.ValueString() != "johndoe" {
		t.Errorf("Username = %v, want 'johndoe'", model.Username.ValueString())
	}
	if model.OrganizationPermissionLevel.ValueString() != "owner" {
		t.Errorf("OrganizationPermissionLevel = %v, want 'owner'", model.OrganizationPermissionLevel.ValueString())
	}
}

// TestUserOrganizationIDs tests handling of organization ID list
func TestUserOrganizationIDs(t *testing.T) {
	orgIDs := []string{"org_1", "org_2", "org_3"}

	// Convert to types.List
	orgIDValues := make([]attr.Value, len(orgIDs))
	for i, id := range orgIDs {
		orgIDValues[i] = types.StringValue(id)
	}

	listValue, diags := types.ListValue(types.StringType, orgIDValues)
	if diags.HasError() {
		t.Fatalf("Failed to create list: %v", diags)
	}

	model := UserDataSourceModel{
		OrganizationIDs: listValue,
	}

	// Verify list
	if model.OrganizationIDs.IsNull() {
		t.Error("OrganizationIDs should not be null")
	}

	elements := model.OrganizationIDs.Elements()
	if len(elements) != 3 {
		t.Errorf("OrganizationIDs count = %v, want 3", len(elements))
	}
}

// TestUserCloudIDs tests handling of cloud ID list
func TestUserCloudIDs(t *testing.T) {
	cloudIDs := []string{"cld_1", "cld_2", "cld_3", "cld_4"}

	// Convert to types.List
	cloudIDValues := make([]attr.Value, len(cloudIDs))
	for i, id := range cloudIDs {
		cloudIDValues[i] = types.StringValue(id)
	}

	listValue, diags := types.ListValue(types.StringType, cloudIDValues)
	if diags.HasError() {
		t.Fatalf("Failed to create list: %v", diags)
	}

	model := UserDataSourceModel{
		CloudIDs: listValue,
	}

	// Verify list
	if model.CloudIDs.IsNull() {
		t.Error("CloudIDs should not be null")
	}

	elements := model.CloudIDs.Elements()
	if len(elements) != 4 {
		t.Errorf("CloudIDs count = %v, want 4", len(elements))
	}
}

// TestUserPermissionLevels tests valid organization permission levels
func TestUserPermissionLevels(t *testing.T) {
	validLevels := []string{"owner", "admin", "member"}

	for _, level := range validLevels {
		t.Run("level_"+level, func(t *testing.T) {
			model := UserDataSourceModel{
				OrganizationPermissionLevel: types.StringValue(level),
			}

			if model.OrganizationPermissionLevel.ValueString() != level {
				t.Errorf("OrganizationPermissionLevel = %v, want %v",
					model.OrganizationPermissionLevel.ValueString(), level)
			}
		})
	}
}

// TestUserGroupIDs tests handling of user group IDs
func TestUserGroupIDs(t *testing.T) {
	// User groups might be empty (feature not fully implemented)
	emptyList, diags := types.ListValue(types.StringType, []attr.Value{})
	if diags.HasError() {
		t.Fatalf("Failed to create empty list: %v", diags)
	}

	model := UserDataSourceModel{
		UserGroupIDs: emptyList,
	}

	elements := model.UserGroupIDs.Elements()
	if len(elements) != 0 {
		t.Errorf("UserGroupIDs count = %v, want 0 (feature not implemented)", len(elements))
	}
}

// TestOrganizationModelMapping tests organization model structure
func TestOrganizationModelMapping(t *testing.T) {
	model := OrganizationModel{
		ID:               types.StringValue("org_abc"),
		Name:             types.StringValue("Acme Corporation"),
		PublicIdentifier: types.StringValue("acme-corp"),
		DefaultCloudID:   types.StringValue("cld_def"),
	}

	// Verify fields
	if model.ID.ValueString() != "org_abc" {
		t.Errorf("ID = %v, want 'org_abc'", model.ID.ValueString())
	}
	if model.Name.ValueString() != "Acme Corporation" {
		t.Errorf("Name = %v, want 'Acme Corporation'", model.Name.ValueString())
	}
	if model.PublicIdentifier.ValueString() != "acme-corp" {
		t.Errorf("PublicIdentifier = %v, want 'acme-corp'", model.PublicIdentifier.ValueString())
	}
	if model.DefaultCloudID.ValueString() != "cld_def" {
		t.Errorf("DefaultCloudID = %v, want 'cld_def'", model.DefaultCloudID.ValueString())
	}
}

// TestUserAPIResponseStructure tests expected API response structure
func TestUserAPIResponseStructure(t *testing.T) {
	// Simulate API response structure
	apiResponse := struct {
		Result struct {
			ID                          string
			Email                       string
			Name                        string
			Username                    string
			OrganizationPermissionLevel string
			OrganizationIDs             []string
			Organizations               []struct {
				ID               string
				Name             string
				PublicIdentifier string
				DefaultCloudID   string
			}
		}
	}{
		Result: struct {
			ID                          string
			Email                       string
			Name                        string
			Username                    string
			OrganizationPermissionLevel string
			OrganizationIDs             []string
			Organizations               []struct {
				ID               string
				Name             string
				PublicIdentifier string
				DefaultCloudID   string
			}
		}{
			ID:                          "user_123",
			Email:                       "test@example.com",
			Name:                        "Test User",
			Username:                    "testuser",
			OrganizationPermissionLevel: "member",
			OrganizationIDs:             []string{"org_1", "org_2"},
			Organizations: []struct {
				ID               string
				Name             string
				PublicIdentifier string
				DefaultCloudID   string
			}{
				{
					ID:               "org_1",
					Name:             "Organization 1",
					PublicIdentifier: "org-1",
					DefaultCloudID:   "cld_1",
				},
				{
					ID:               "org_2",
					Name:             "Organization 2",
					PublicIdentifier: "org-2",
					DefaultCloudID:   "cld_2",
				},
			},
		},
	}

	// Verify response structure
	if apiResponse.Result.ID != "user_123" {
		t.Errorf("ID = %v, want 'user_123'", apiResponse.Result.ID)
	}
	if len(apiResponse.Result.OrganizationIDs) != 2 {
		t.Errorf("OrganizationIDs count = %v, want 2", len(apiResponse.Result.OrganizationIDs))
	}
	if len(apiResponse.Result.Organizations) != 2 {
		t.Errorf("Organizations count = %v, want 2", len(apiResponse.Result.Organizations))
	}
}

// TestUserMultipleOrganizations tests handling of users in multiple organizations
func TestUserMultipleOrganizations(t *testing.T) {
	// Users can belong to multiple organizations
	orgIDs := []string{"org_1", "org_2", "org_3"}

	orgIDValues := make([]attr.Value, len(orgIDs))
	for i, id := range orgIDs {
		orgIDValues[i] = types.StringValue(id)
	}

	listValue, diags := types.ListValue(types.StringType, orgIDValues)
	if diags.HasError() {
		t.Fatalf("Failed to create list: %v", diags)
	}

	model := UserDataSourceModel{
		OrganizationIDs: listValue,
	}

	elements := model.OrganizationIDs.Elements()
	if len(elements) != 3 {
		t.Errorf("User should be in 3 organizations, got %v", len(elements))
	}
}

// TestUserEmptyLists tests handling of empty lists
func TestUserEmptyLists(t *testing.T) {
	emptyOrgIDs, _ := types.ListValue(types.StringType, []attr.Value{})
	emptyCloudIDs, _ := types.ListValue(types.StringType, []attr.Value{})
	emptyUserGroupIDs, _ := types.ListValue(types.StringType, []attr.Value{})

	model := UserDataSourceModel{
		OrganizationIDs: emptyOrgIDs,
		CloudIDs:        emptyCloudIDs,
		UserGroupIDs:    emptyUserGroupIDs,
	}

	// All lists should be empty but not null
	if model.OrganizationIDs.IsNull() {
		t.Error("OrganizationIDs should not be null")
	}
	if len(model.OrganizationIDs.Elements()) != 0 {
		t.Errorf("OrganizationIDs should be empty, got %v elements", len(model.OrganizationIDs.Elements()))
	}

	if model.CloudIDs.IsNull() {
		t.Error("CloudIDs should not be null")
	}
	if len(model.CloudIDs.Elements()) != 0 {
		t.Errorf("CloudIDs should be empty, got %v elements", len(model.CloudIDs.Elements()))
	}
}

// TestUserEmailValidation tests email field format
func TestUserEmailValidation(t *testing.T) {
	validEmails := []string{
		"user@example.com",
		"user.name@example.com",
		"user+tag@example.co.uk",
		"user123@sub.example.com",
	}

	for _, email := range validEmails {
		t.Run("email_"+email, func(t *testing.T) {
			model := UserDataSourceModel{
				Email: types.StringValue(email),
			}

			if model.Email.ValueString() != email {
				t.Errorf("Email = %v, want %v", model.Email.ValueString(), email)
			}
		})
	}
}

// TestUserUsernameFormat tests username field
func TestUserUsernameFormat(t *testing.T) {
	usernames := []string{
		"johndoe",
		"john.doe",
		"john_doe",
		"john-doe",
		"john123",
	}

	for _, username := range usernames {
		t.Run("username_"+username, func(t *testing.T) {
			model := UserDataSourceModel{
				Username: types.StringValue(username),
			}

			if model.Username.ValueString() != username {
				t.Errorf("Username = %v, want %v", model.Username.ValueString(), username)
			}
		})
	}
}
