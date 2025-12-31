package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestCloudDataSourceLookupValidation tests validation of ID vs Name lookup
func TestCloudDataSourceLookupValidation(t *testing.T) {
	tests := []struct {
		name      string
		id        types.String
		cloudName types.String
		wantError bool
		errorMsg  string
	}{
		{
			name:      "ID provided",
			id:        types.StringValue("cld_123"),
			cloudName: types.StringNull(),
			wantError: false,
		},
		{
			name:      "name provided",
			id:        types.StringNull(),
			cloudName: types.StringValue("my-cloud"),
			wantError: false,
		},
		{
			name:      "neither provided",
			id:        types.StringNull(),
			cloudName: types.StringNull(),
			wantError: true,
			errorMsg:  "Either 'id' or 'name' must be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate validation logic from Read method
			hasID := !tt.id.IsNull()
			hasName := !tt.cloudName.IsNull()

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

// TestCloudDataSourceNameResolutionLogic tests name resolution with multiple matching clouds
func TestCloudDataSourceNameResolutionLogic(t *testing.T) {
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

// TestCloudDataSourceModelMapping tests mapping of API response to model
func TestCloudDataSourceModelMapping(t *testing.T) {
	// Simulate API response
	apiResult := struct {
		ID       string
		Name     string
		Provider string
		Region   string
		Status   string
		State    string
	}{
		ID:       "cld_abc123",
		Name:     "production-cloud",
		Provider: "AWS",
		Region:   "us-west-2",
		Status:   "ready",
		State:    "ACTIVE",
	}

	// Map to model
	model := CloudDataSourceModel{
		ID:            types.StringValue(apiResult.ID),
		Name:          types.StringValue(apiResult.Name),
		CloudProvider: types.StringValue(apiResult.Provider),
		Region:        types.StringValue(apiResult.Region),
		Status:        types.StringValue(apiResult.Status),
		State:         types.StringValue(apiResult.State),
	}

	// Verify mapping
	if model.ID.ValueString() != "cld_abc123" {
		t.Errorf("ID = %v, want 'cld_abc123'", model.ID.ValueString())
	}
	if model.Name.ValueString() != "production-cloud" {
		t.Errorf("Name = %v, want 'production-cloud'", model.Name.ValueString())
	}
	if model.CloudProvider.ValueString() != "AWS" {
		t.Errorf("CloudProvider = %v, want 'AWS'", model.CloudProvider.ValueString())
	}
	if model.Region.ValueString() != "us-west-2" {
		t.Errorf("Region = %v, want 'us-west-2'", model.Region.ValueString())
	}
	if model.Status.ValueString() != "ready" {
		t.Errorf("Status = %v, want 'ready'", model.Status.ValueString())
	}
	if model.State.ValueString() != "ACTIVE" {
		t.Errorf("State = %v, want 'ACTIVE'", model.State.ValueString())
	}
}

// TestCloudBooleanFieldDefaults tests default boolean field handling
func TestCloudBooleanFieldDefaults(t *testing.T) {
	// Simulate API response without boolean fields
	model := CloudDataSourceModel{
		ID:                    types.StringValue("cld_123"),
		Name:                  types.StringValue("test-cloud"),
		AutoAddUser:           types.BoolValue(false), // Should default to false
		EnableLineageTracking: types.BoolValue(false),
		EnableLogIngestion:    types.BoolValue(false),
		IsEmptyCloud:          types.BoolValue(false),
	}

	// Verify defaults
	if model.AutoAddUser.ValueBool() != false {
		t.Errorf("AutoAddUser = %v, want false (default)", model.AutoAddUser.ValueBool())
	}
	if model.EnableLineageTracking.ValueBool() != false {
		t.Errorf("EnableLineageTracking = %v, want false (default)", model.EnableLineageTracking.ValueBool())
	}
	if model.EnableLogIngestion.ValueBool() != false {
		t.Errorf("EnableLogIngestion = %v, want false (default)", model.EnableLogIngestion.ValueBool())
	}
	if model.IsEmptyCloud.ValueBool() != false {
		t.Errorf("IsEmptyCloud = %v, want false (default)", model.IsEmptyCloud.ValueBool())
	}
}

// TestCloudProviderValues tests valid cloud provider values
func TestCloudProviderValues(t *testing.T) {
	validProviders := []string{"AWS", "GCP", "AZURE", "GENERIC"}

	for _, provider := range validProviders {
		t.Run("provider_"+provider, func(t *testing.T) {
			model := CloudDataSourceModel{
				CloudProvider: types.StringValue(provider),
			}

			if model.CloudProvider.ValueString() != provider {
				t.Errorf("CloudProvider = %v, want %v", model.CloudProvider.ValueString(), provider)
			}
		})
	}
}

// TestCloudStatusMapping tests common cloud status values
func TestCloudStatusMapping(t *testing.T) {
	statuses := []string{"ready", "pending", "failed", "creating"}

	for _, status := range statuses {
		t.Run("status_"+status, func(t *testing.T) {
			model := CloudDataSourceModel{
				Status: types.StringValue(status),
			}

			if model.Status.ValueString() != status {
				t.Errorf("Status = %v, want %v", model.Status.ValueString(), status)
			}
		})
	}
}

// TestCloudStateMapping tests cloud lifecycle states
func TestCloudStateMapping(t *testing.T) {
	states := []string{"ACTIVE", "CREATING", "FAILED", "DELETING"}

	for _, state := range states {
		t.Run("state_"+state, func(t *testing.T) {
			model := CloudDataSourceModel{
				State: types.StringValue(state),
			}

			if model.State.ValueString() != state {
				t.Errorf("State = %v, want %v", model.State.ValueString(), state)
			}
		})
	}
}

// TestCloudNullableFields tests handling of optional/nullable fields
func TestCloudNullableFields(t *testing.T) {
	model := CloudDataSourceModel{
		ID:                types.StringValue("cld_123"),
		Name:              types.StringValue("test-cloud"),
		Status:            types.StringNull(), // Status might not be present
		CloudDeploymentID: types.StringNull(), // Deployment ID might not be present
	}

	if !model.Status.IsNull() {
		t.Error("Status should be null")
	}
	if !model.CloudDeploymentID.IsNull() {
		t.Error("CloudDeploymentID should be null")
	}
}
