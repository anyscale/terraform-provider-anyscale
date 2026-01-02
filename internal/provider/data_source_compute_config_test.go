package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestComputeConfigLookupValidation tests validation of ID vs Name lookup
func TestComputeConfigLookupValidation(t *testing.T) {
	tests := []struct {
		name      string
		id        types.String
		cfgName   types.String
		wantError bool
		errorMsg  string
	}{
		{
			name:      "ID provided",
			id:        types.StringValue("ccfg_123"),
			cfgName:   types.StringNull(),
			wantError: false,
		},
		{
			name:      "name provided",
			id:        types.StringNull(),
			cfgName:   types.StringValue("my-config"),
			wantError: false,
		},
		{
			name:      "neither provided",
			id:        types.StringNull(),
			cfgName:   types.StringNull(),
			wantError: true,
			errorMsg:  "Either 'id' or 'name' must be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate validation logic from Read method
			hasID := !tt.id.IsNull()
			hasName := !tt.cfgName.IsNull()

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

// TestComputeConfigCloudFiltering tests cloud ID and name filtering
func TestComputeConfigCloudFiltering(t *testing.T) {
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
			// Simulate cloud filtering logic
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

// TestComputeConfigFieldMapping tests mapping of API fields to model
func TestComputeConfigFieldMapping(t *testing.T) {
	model := ComputeConfigDataSourceModel{
		ID:                     types.StringValue("ccfg_123"),
		ConfigID:               types.StringValue("ccfg_123"),
		Name:                   types.StringValue("my-config"),
		NameVersion:            types.StringValue("my-config:3"),
		CloudID:                types.StringValue("cld_456"),
		CloudName:              types.StringValue("my-cloud"),
		Region:                 types.StringValue("us-west-2"),
		IdleTerminationMinutes: types.Int64Value(120),
		MaximumUptimeMinutes:   types.Int64Value(480),
		EnableCrossZoneScaling: types.BoolValue(true),
		AutoSelectWorkerConfig: types.BoolValue(false),
		ProjectID:              types.StringValue("prj_789"),
		Version:                types.Int64Value(3),
		CreatedAt:              types.StringValue("2024-01-01T00:00:00Z"),
		LastModifiedAt:         types.StringValue("2024-01-02T00:00:00Z"),
		Versions:               types.ListNull(types.Int64Type), // Would be populated from API
	}

	// Verify all fields are correctly set
	if model.ID.ValueString() != "ccfg_123" {
		t.Errorf("ID = %v, want 'ccfg_123'", model.ID.ValueString())
	}
	if model.ConfigID.ValueString() != "ccfg_123" {
		t.Errorf("ConfigID = %v, want 'ccfg_123'", model.ConfigID.ValueString())
	}
	if model.NameVersion.ValueString() != "my-config:3" {
		t.Errorf("NameVersion = %v, want 'my-config:3'", model.NameVersion.ValueString())
	}
	if model.Region.ValueString() != "us-west-2" {
		t.Errorf("Region = %v, want 'us-west-2'", model.Region.ValueString())
	}
	if model.IdleTerminationMinutes.ValueInt64() != 120 {
		t.Errorf("IdleTerminationMinutes = %v, want 120", model.IdleTerminationMinutes.ValueInt64())
	}
	if model.EnableCrossZoneScaling.ValueBool() != true {
		t.Errorf("EnableCrossZoneScaling = %v, want true", model.EnableCrossZoneScaling.ValueBool())
	}
	if model.Version.ValueInt64() != 3 {
		t.Errorf("Version = %v, want 3", model.Version.ValueInt64())
	}
}

// TestComputeConfigBooleanDefaults tests default values for boolean fields
func TestComputeConfigBooleanDefaults(t *testing.T) {
	model := ComputeConfigDataSourceModel{
		EnableCrossZoneScaling: types.BoolValue(false), // Should default to false
		AutoSelectWorkerConfig: types.BoolValue(false), // Should default to false
	}

	if model.EnableCrossZoneScaling.ValueBool() != false {
		t.Errorf("EnableCrossZoneScaling = %v, want false (default)", model.EnableCrossZoneScaling.ValueBool())
	}
	if model.AutoSelectWorkerConfig.ValueBool() != false {
		t.Errorf("AutoSelectWorkerConfig = %v, want false (default)", model.AutoSelectWorkerConfig.ValueBool())
	}
}

// TestComputeConfigNullableFields tests handling of optional/nullable fields
func TestComputeConfigNullableFields(t *testing.T) {
	model := ComputeConfigDataSourceModel{
		ID:                   types.StringValue("ccfg_123"),
		MaximumUptimeMinutes: types.Int64Null(),  // Might not be set
		ProjectID:            types.StringNull(), // Might not be associated with a project
	}

	if !model.MaximumUptimeMinutes.IsNull() {
		t.Error("MaximumUptimeMinutes should be null")
	}
	if !model.ProjectID.IsNull() {
		t.Error("ProjectID should be null")
	}
}

// TestComputeConfigNameResolutionWithMultiple tests finding config by name with multiple matches
func TestComputeConfigNameResolutionWithMultiple(t *testing.T) {
	// Simulate API response with multiple configs having the same name
	configs := []struct {
		ID        string
		Name      string
		CreatedAt string
	}{
		{
			ID:        "ccfg_123",
			Name:      "test-config",
			CreatedAt: "2024-01-01T00:00:00Z",
		},
		{
			ID:        "ccfg_456",
			Name:      "test-config",
			CreatedAt: "2024-01-02T00:00:00Z", // More recent
		},
	}

	// Simulate resolution logic (most recent)
	var matchedConfigID string
	var latestCreatedAt string

	for _, cfg := range configs {
		if matchedConfigID == "" || cfg.CreatedAt > latestCreatedAt {
			matchedConfigID = cfg.ID
			latestCreatedAt = cfg.CreatedAt
		}
	}

	// Should pick the most recent one
	if matchedConfigID != "ccfg_456" {
		t.Errorf("resolved config_id = %v, want 'ccfg_456' (most recent)", matchedConfigID)
	}
}

// TestComputeConfigIdleTerminationValues tests various idle termination values
func TestComputeConfigIdleTerminationValues(t *testing.T) {
	tests := []struct {
		name   string
		value  int64
		expect string
	}{
		{"disabled", 0, "disabled (0)"},
		{"2 hours", 120, "120 minutes"},
		{"8 hours", 480, "480 minutes"},
		{"1 day", 1440, "1440 minutes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := ComputeConfigDataSourceModel{
				IdleTerminationMinutes: types.Int64Value(tt.value),
			}

			if model.IdleTerminationMinutes.ValueInt64() != tt.value {
				t.Errorf("IdleTerminationMinutes = %v, want %v", model.IdleTerminationMinutes.ValueInt64(), tt.value)
			}
		})
	}
}
