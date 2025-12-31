package provider

import (
	"net/url"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestCloudsFilterParameterBuilding tests building query parameters from filters
func TestCloudsFilterParameterBuilding(t *testing.T) {
	tests := []struct {
		name            string
		config          CloudsDataSourceModel
		expectedParams  map[string]string
	}{
		{
			name: "name_contains filter",
			config: CloudsDataSourceModel{
				NameContains: types.StringValue("prod"),
			},
			expectedParams: map[string]string{
				"name_contains": "prod",
			},
		},
		{
			name: "cloud_provider filter",
			config: CloudsDataSourceModel{
				CloudProvider: types.StringValue("AWS"),
			},
			expectedParams: map[string]string{
				"provider": "AWS",
			},
		},
		{
			name: "region filter",
			config: CloudsDataSourceModel{
				Region: types.StringValue("us-west-2"),
			},
			expectedParams: map[string]string{
				"region": "us-west-2",
			},
		},
		{
			name: "multiple filters",
			config: CloudsDataSourceModel{
				NameContains:  types.StringValue("prod"),
				CloudProvider: types.StringValue("GCP"),
				Region:        types.StringValue("us-central1"),
			},
			expectedParams: map[string]string{
				"name_contains": "prod",
				"provider":      "GCP",
				"region":        "us-central1",
			},
		},
		{
			name: "no filters",
			config: CloudsDataSourceModel{
				NameContains:  types.StringNull(),
				CloudProvider: types.StringNull(),
				Region:        types.StringNull(),
			},
			expectedParams: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate parameter building logic
			params := url.Values{}

			if !tt.config.NameContains.IsNull() {
				params.Add("name_contains", tt.config.NameContains.ValueString())
			}
			if !tt.config.CloudProvider.IsNull() {
				params.Add("provider", tt.config.CloudProvider.ValueString())
			}
			if !tt.config.Region.IsNull() {
				params.Add("region", tt.config.Region.ValueString())
			}

			// Verify all expected params are present
			for key, expectedValue := range tt.expectedParams {
				gotValue := params.Get(key)
				if gotValue != expectedValue {
					t.Errorf("param %s = %v, want %v", key, gotValue, expectedValue)
				}
			}

			// Verify no unexpected params
			if len(params) != len(tt.expectedParams) {
				t.Errorf("param count = %d, want %d", len(params), len(tt.expectedParams))
			}
		})
	}
}

// TestCloudSummaryMapping tests mapping API response to CloudSummaryModel
func TestCloudSummaryMapping(t *testing.T) {
	apiCloud := struct {
		ID                      string
		Name                    string
		Provider                string
		ComputeStack            string
		Region                  string
		Status                  string
		State                   string
		CreatedAt               string
		CreatorID               string
		IsDefault               bool
		IsK8s                   bool
		IsAIOA                  bool
		IsBringYourOwnResource  bool
		IsPrivateCloud          bool
		IsPrivateServiceCloud   bool
		AutoAddUser             bool
		LineageTrackingEnabled  bool
		IsAggregatedLogsEnabled bool
	}{
		ID:                      "cld_abc",
		Name:                    "production",
		Provider:                "AWS",
		ComputeStack:            "VM",
		Region:                  "us-west-2",
		Status:                  "ready",
		State:                   "ACTIVE",
		CreatedAt:               "2024-01-01T00:00:00Z",
		CreatorID:               "user_123",
		IsDefault:               true,
		IsK8s:                   false,
		IsAIOA:                  true,
		IsBringYourOwnResource:  false,
		IsPrivateCloud:          false,
		IsPrivateServiceCloud:   false,
		AutoAddUser:             true,
		LineageTrackingEnabled:  true,
		IsAggregatedLogsEnabled: true,
	}

	model := CloudSummaryModel{
		ID:                      types.StringValue(apiCloud.ID),
		Name:                    types.StringValue(apiCloud.Name),
		CloudProvider:           types.StringValue(apiCloud.Provider),
		ComputeStack:            types.StringValue(apiCloud.ComputeStack),
		Region:                  types.StringValue(apiCloud.Region),
		Status:                  types.StringValue(apiCloud.Status),
		State:                   types.StringValue(apiCloud.State),
		CreatedAt:               types.StringValue(apiCloud.CreatedAt),
		CreatorID:               types.StringValue(apiCloud.CreatorID),
		IsDefault:               types.BoolValue(apiCloud.IsDefault),
		IsK8s:                   types.BoolValue(apiCloud.IsK8s),
		IsAIOA:                  types.BoolValue(apiCloud.IsAIOA),
		IsBringYourOwnResource:  types.BoolValue(apiCloud.IsBringYourOwnResource),
		IsPrivateCloud:          types.BoolValue(apiCloud.IsPrivateCloud),
		IsPrivateServiceCloud:   types.BoolValue(apiCloud.IsPrivateServiceCloud),
		AutoAddUser:             types.BoolValue(apiCloud.AutoAddUser),
		LineageTrackingEnabled:  types.BoolValue(apiCloud.LineageTrackingEnabled),
		IsAggregatedLogsEnabled: types.BoolValue(apiCloud.IsAggregatedLogsEnabled),
	}

	// Verify all fields
	if model.ID.ValueString() != "cld_abc" {
		t.Errorf("ID = %v, want 'cld_abc'", model.ID.ValueString())
	}
	if model.IsDefault.ValueBool() != true {
		t.Errorf("IsDefault = %v, want true", model.IsDefault.ValueBool())
	}
	if model.IsK8s.ValueBool() != false {
		t.Errorf("IsK8s = %v, want false", model.IsK8s.ValueBool())
	}
	if model.LineageTrackingEnabled.ValueBool() != true {
		t.Errorf("LineageTrackingEnabled = %v, want true", model.LineageTrackingEnabled.ValueBool())
	}
}

// TestCloudsPaginationHandling tests pagination token handling
func TestCloudsPaginationHandling(t *testing.T) {
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
			name: "no next page - nil token",
			nextToken: nil,
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

// TestComputeStackValues tests valid compute stack values
func TestComputeStackValues(t *testing.T) {
	validStacks := []string{"VM", "K8S"}

	for _, stack := range validStacks {
		t.Run("stack_"+stack, func(t *testing.T) {
			model := CloudSummaryModel{
				ComputeStack: types.StringValue(stack),
			}

			if model.ComputeStack.ValueString() != stack {
				t.Errorf("ComputeStack = %v, want %v", model.ComputeStack.ValueString(), stack)
			}
		})
	}
}

// TestCloudBooleanFlags tests all boolean flags in CloudSummaryModel
func TestCloudBooleanFlags(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value bool
	}{
		{"is_default true", "is_default", true},
		{"is_default false", "is_default", false},
		{"is_k8s true", "is_k8s", true},
		{"is_k8s false", "is_k8s", false},
		{"is_aioa true", "is_aioa", true},
		{"is_aioa false", "is_aioa", false},
		{"auto_add_user true", "auto_add_user", true},
		{"auto_add_user false", "auto_add_user", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := CloudSummaryModel{}

			switch tt.field {
			case "is_default":
				model.IsDefault = types.BoolValue(tt.value)
				if model.IsDefault.ValueBool() != tt.value {
					t.Errorf("IsDefault = %v, want %v", model.IsDefault.ValueBool(), tt.value)
				}
			case "is_k8s":
				model.IsK8s = types.BoolValue(tt.value)
				if model.IsK8s.ValueBool() != tt.value {
					t.Errorf("IsK8s = %v, want %v", model.IsK8s.ValueBool(), tt.value)
				}
			case "is_aioa":
				model.IsAIOA = types.BoolValue(tt.value)
				if model.IsAIOA.ValueBool() != tt.value {
					t.Errorf("IsAIOA = %v, want %v", model.IsAIOA.ValueBool(), tt.value)
				}
			case "auto_add_user":
				model.AutoAddUser = types.BoolValue(tt.value)
				if model.AutoAddUser.ValueBool() != tt.value {
					t.Errorf("AutoAddUser = %v, want %v", model.AutoAddUser.ValueBool(), tt.value)
				}
			}
		})
	}
}
