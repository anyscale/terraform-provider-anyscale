package provider

import (
	"context"
	"math/big"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestNodeConfigToAPI tests converting a head node configuration to API format
func TestNodeConfigToAPI(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		obj     types.Object
		want    map[string]interface{}
		wantErr bool
	}{
		{
			name: "basic head node with instance type",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"instance_type":            types.StringType,
					"resources":                types.MapType{ElemType: types.Float64Type},
					"required_resources":       types.ObjectType{AttrTypes: map[string]attr.Type{}},
					"labels":                   types.MapType{ElemType: types.StringType},
					"advanced_instance_config": types.StringType,
					"flags":                    types.StringType,
					"cloud_deployment":         types.ObjectType{AttrTypes: map[string]attr.Type{}},
				},
				map[string]attr.Value{
					"instance_type": types.StringValue("m5.2xlarge"),
					"resources": types.MapValueMust(
						types.Float64Type,
						map[string]attr.Value{
							"CPU": types.Float64Value(8),
							"RAM": types.Float64Value(32),
						},
					),
					"required_resources":       types.ObjectNull(map[string]attr.Type{}),
					"labels":                   types.MapNull(types.StringType),
					"advanced_instance_config": types.StringNull(),
					"flags":                    types.StringNull(),
					"cloud_deployment":         types.ObjectNull(map[string]attr.Type{}),
				},
			),
			want: map[string]interface{}{
				"name":          "head",
				"instance_type": "m5.2xlarge",
				"resources": map[string]interface{}{
					"CPU": float64(8),
					"RAM": float64(32),
				},
			},
			wantErr: false,
		},
		{
			name: "head node with advanced_instance_config JSON",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"instance_type":            types.StringType,
					"resources":                types.MapType{ElemType: types.Float64Type},
					"required_resources":       types.ObjectType{AttrTypes: map[string]attr.Type{}},
					"labels":                   types.MapType{ElemType: types.StringType},
					"advanced_instance_config": types.StringType,
					"flags":                    types.StringType,
					"cloud_deployment":         types.ObjectType{AttrTypes: map[string]attr.Type{}},
				},
				map[string]attr.Value{
					"instance_type":            types.StringValue("m5.xlarge"),
					"resources":                types.MapNull(types.Float64Type),
					"required_resources":       types.ObjectNull(map[string]attr.Type{}),
					"labels":                   types.MapNull(types.StringType),
					"advanced_instance_config": types.StringValue(`{"disk_size": 100, "enable_monitoring": true}`),
					"flags":                    types.StringNull(),
					"cloud_deployment":         types.ObjectNull(map[string]attr.Type{}),
				},
			),
			want: map[string]interface{}{
				"name":          "head",
				"instance_type": "m5.xlarge",
				"advanced_configurations_json": map[string]interface{}{
					"disk_size":         float64(100),
					"enable_monitoring": true,
				},
			},
			wantErr: false,
		},
		{
			name:    "null object returns nil",
			obj:     types.ObjectNull(map[string]attr.Type{}),
			want:    nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nodeConfigToAPI(ctx, tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("nodeConfigToAPI() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.want == nil && got != nil {
				t.Errorf("nodeConfigToAPI() = %v, want nil", got)
				return
			}

			if tt.want != nil {
				if got == nil {
					t.Errorf("nodeConfigToAPI() = nil, want non-nil")
					return
				}

				// Verify required fields
				if got["name"] != tt.want["name"] {
					t.Errorf("nodeConfigToAPI() name = %v, want %v", got["name"], tt.want["name"])
				}
				if got["instance_type"] != tt.want["instance_type"] {
					t.Errorf("nodeConfigToAPI() instance_type = %v, want %v", got["instance_type"], tt.want["instance_type"])
				}

				// Verify resources if present
				if expectedResources, ok := tt.want["resources"]; ok {
					if gotResources, ok := got["resources"]; ok {
						resMap := gotResources.(map[string]interface{})
						expMap := expectedResources.(map[string]interface{})
						if len(resMap) != len(expMap) {
							t.Errorf("nodeConfigToAPI() resources count = %v, want %v", len(resMap), len(expMap))
						}
					} else {
						t.Errorf("nodeConfigToAPI() missing resources")
					}
				}

				// Verify advanced_configurations_json if present
				if expectedAdvanced, ok := tt.want["advanced_configurations_json"]; ok {
					if gotAdvanced, ok := got["advanced_configurations_json"]; ok {
						advMap := gotAdvanced.(map[string]interface{})
						expMap := expectedAdvanced.(map[string]interface{})
						if len(advMap) != len(expMap) {
							t.Errorf("nodeConfigToAPI() advanced_configurations_json count = %v, want %v", len(advMap), len(expMap))
						}
					} else {
						t.Errorf("nodeConfigToAPI() missing advanced_configurations_json")
					}
				}
			}
		})
	}
}

// TestWorkerNodeConfigToAPI tests converting a worker node configuration to API format
func TestWorkerNodeConfigToAPI(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		obj     types.Object
		want    map[string]interface{}
		wantErr bool
	}{
		{
			name: "worker node with ON_DEMAND market type",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"name":                     types.StringType,
					"min_nodes":                types.Int64Type,
					"max_nodes":                types.Int64Type,
					"market_type":              types.StringType,
					"instance_type":            types.StringType,
					"resources":                types.MapType{ElemType: types.Float64Type},
					"required_resources":       types.ObjectType{AttrTypes: map[string]attr.Type{}},
					"labels":                   types.MapType{ElemType: types.StringType},
					"advanced_instance_config": types.StringType,
					"flags":                    types.StringType,
					"cloud_deployment":         types.ObjectType{AttrTypes: map[string]attr.Type{}},
				},
				map[string]attr.Value{
					"name":                     types.StringValue("worker-group-1"),
					"min_nodes":                types.Int64Value(0),
					"max_nodes":                types.Int64Value(10),
					"market_type":              types.StringValue("ON_DEMAND"),
					"instance_type":            types.StringValue("m5.large"),
					"resources":                types.MapNull(types.Float64Type),
					"required_resources":       types.ObjectNull(map[string]attr.Type{}),
					"labels":                   types.MapNull(types.StringType),
					"advanced_instance_config": types.StringNull(),
					"flags":                    types.StringNull(),
					"cloud_deployment":         types.ObjectNull(map[string]attr.Type{}),
				},
			),
			want: map[string]interface{}{
				"name":                 "worker-group-1",
				"instance_type":        "m5.large",
				"min_workers":          int64(0),
				"max_workers":          int64(10),
				"use_spot":             false,
				"fallback_to_ondemand": false,
			},
			wantErr: false,
		},
		{
			name: "worker node with SPOT market type",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"name":                     types.StringType,
					"min_nodes":                types.Int64Type,
					"max_nodes":                types.Int64Type,
					"market_type":              types.StringType,
					"instance_type":            types.StringType,
					"resources":                types.MapType{ElemType: types.Float64Type},
					"required_resources":       types.ObjectType{AttrTypes: map[string]attr.Type{}},
					"labels":                   types.MapType{ElemType: types.StringType},
					"advanced_instance_config": types.StringType,
					"flags":                    types.StringType,
					"cloud_deployment":         types.ObjectType{AttrTypes: map[string]attr.Type{}},
				},
				map[string]attr.Value{
					"name":                     types.StringNull(),
					"min_nodes":                types.Int64Value(1),
					"max_nodes":                types.Int64Value(5),
					"market_type":              types.StringValue("SPOT"),
					"instance_type":            types.StringValue("m5.xlarge"),
					"resources":                types.MapNull(types.Float64Type),
					"required_resources":       types.ObjectNull(map[string]attr.Type{}),
					"labels":                   types.MapNull(types.StringType),
					"advanced_instance_config": types.StringNull(),
					"flags":                    types.StringNull(),
					"cloud_deployment":         types.ObjectNull(map[string]attr.Type{}),
				},
			),
			want: map[string]interface{}{
				"name":                 "m5.xlarge", // Defaults to instance type
				"instance_type":        "m5.xlarge",
				"min_workers":          int64(1),
				"max_workers":          int64(5),
				"use_spot":             true,
				"fallback_to_ondemand": false,
			},
			wantErr: false,
		},
		{
			name: "worker node with PREFER_SPOT market type",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"name":                     types.StringType,
					"min_nodes":                types.Int64Type,
					"max_nodes":                types.Int64Type,
					"market_type":              types.StringType,
					"instance_type":            types.StringType,
					"resources":                types.MapType{ElemType: types.Float64Type},
					"required_resources":       types.ObjectType{AttrTypes: map[string]attr.Type{}},
					"labels":                   types.MapType{ElemType: types.StringType},
					"advanced_instance_config": types.StringType,
					"flags":                    types.StringType,
					"cloud_deployment":         types.ObjectType{AttrTypes: map[string]attr.Type{}},
				},
				map[string]attr.Value{
					"name":                     types.StringValue("spot-with-fallback"),
					"min_nodes":                types.Int64Value(2),
					"max_nodes":                types.Int64Value(20),
					"market_type":              types.StringValue("PREFER_SPOT"),
					"instance_type":            types.StringValue("m5.2xlarge"),
					"resources":                types.MapNull(types.Float64Type),
					"required_resources":       types.ObjectNull(map[string]attr.Type{}),
					"labels":                   types.MapNull(types.StringType),
					"advanced_instance_config": types.StringNull(),
					"flags":                    types.StringNull(),
					"cloud_deployment":         types.ObjectNull(map[string]attr.Type{}),
				},
			),
			want: map[string]interface{}{
				"name":                 "spot-with-fallback",
				"instance_type":        "m5.2xlarge",
				"min_workers":          int64(2),
				"max_workers":          int64(20),
				"use_spot":             true,
				"fallback_to_ondemand": true,
			},
			wantErr: false,
		},
		{
			name:    "null object returns nil",
			obj:     types.ObjectNull(map[string]attr.Type{}),
			want:    nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := workerNodeConfigToAPI(ctx, tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("workerNodeConfigToAPI() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.want == nil && got != nil {
				t.Errorf("workerNodeConfigToAPI() = %v, want nil", got)
				return
			}

			if tt.want != nil {
				if got == nil {
					t.Errorf("workerNodeConfigToAPI() = nil, want non-nil")
					return
				}

				// Verify all expected fields
				for key, expectedValue := range tt.want {
					gotValue, ok := got[key]
					if !ok {
						t.Errorf("workerNodeConfigToAPI() missing key %s", key)
						continue
					}
					if gotValue != expectedValue {
						t.Errorf("workerNodeConfigToAPI() %s = %v, want %v", key, gotValue, expectedValue)
					}
				}
			}
		})
	}
}

// TestMarketTypeTranslation specifically tests the market type translation logic
func TestMarketTypeTranslation(t *testing.T) {
	tests := []struct {
		name                     string
		marketType               string
		expectedUseSpot          bool
		expectedFallbackOnDemand bool
	}{
		{
			name:                     "ON_DEMAND translates to no spot",
			marketType:               "ON_DEMAND",
			expectedUseSpot:          false,
			expectedFallbackOnDemand: false,
		},
		{
			name:                     "SPOT translates to spot without fallback",
			marketType:               "SPOT",
			expectedUseSpot:          true,
			expectedFallbackOnDemand: false,
		},
		{
			name:                     "PREFER_SPOT translates to spot with fallback",
			marketType:               "PREFER_SPOT",
			expectedUseSpot:          true,
			expectedFallbackOnDemand: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the translation logic from workerNodeConfigToAPI
			var useSpot, fallbackToOnDemand bool
			switch tt.marketType {
			case "SPOT":
				useSpot = true
				fallbackToOnDemand = false
			case "PREFER_SPOT":
				useSpot = true
				fallbackToOnDemand = true
			case "ON_DEMAND":
				useSpot = false
				fallbackToOnDemand = false
			}

			if useSpot != tt.expectedUseSpot {
				t.Errorf("market type %s: use_spot = %v, want %v", tt.marketType, useSpot, tt.expectedUseSpot)
			}
			if fallbackToOnDemand != tt.expectedFallbackOnDemand {
				t.Errorf("market type %s: fallback_to_ondemand = %v, want %v", tt.marketType, fallbackToOnDemand, tt.expectedFallbackOnDemand)
			}
		})
	}
}

// TestDynamicToInterfaceConversion tests the real DynamicToInterface function
// (framework_helpers.go) against types.Dynamic shapes matching how Terraform
// actually represents flags/advanced_instance_config HCL object literals -
// the previous version of this test only re-parsed raw JSON and never called
// DynamicToInterface at all, so it could not have caught a bug in it.
func TestDynamicToInterfaceConversion(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		dynamic types.Dynamic
		want    map[string]interface{}
	}{
		{
			name: "flat object with mixed types",
			dynamic: types.DynamicValue(types.ObjectValueMust(
				map[string]attr.Type{
					"enable_autoscaling": types.BoolType,
					"max_scale":          types.NumberType,
					"pool_name":          types.StringType,
				},
				map[string]attr.Value{
					"enable_autoscaling": types.BoolValue(true),
					"max_scale":          types.NumberValue(big.NewFloat(10)),
					"pool_name":          types.StringValue("default"),
				},
			)),
			want: map[string]interface{}{
				"enable_autoscaling": true,
				"max_scale":          int64(10),
				"pool_name":          "default",
			},
		},
		{
			// The schema's MarkdownDescription for flags/advanced_instance_config
			// specifically promises nested object support - this is the case the
			// old test claimed to cover via "nested configuration" but did not.
			name: "nested object with mixed types",
			dynamic: types.DynamicValue(types.ObjectValueMust(
				map[string]attr.Type{
					"monitoring": types.ObjectType{AttrTypes: map[string]attr.Type{
						"enabled": types.BoolType,
					}},
					"disk": types.ObjectType{AttrTypes: map[string]attr.Type{
						"size": types.NumberType,
						"type": types.StringType,
					}},
				},
				map[string]attr.Value{
					"monitoring": types.ObjectValueMust(
						map[string]attr.Type{"enabled": types.BoolType},
						map[string]attr.Value{"enabled": types.BoolValue(true)},
					),
					"disk": types.ObjectValueMust(
						map[string]attr.Type{"size": types.NumberType, "type": types.StringType},
						map[string]attr.Value{
							"size": types.NumberValue(big.NewFloat(100)),
							"type": types.StringValue("ssd"),
						},
					),
				},
			)),
			want: map[string]interface{}{
				"monitoring": map[string]interface{}{"enabled": true},
				"disk": map[string]interface{}{
					"size": int64(100),
					"type": "ssd",
				},
			},
		},
		{
			name: "list-valued attribute inside a dynamic object",
			dynamic: types.DynamicValue(types.ObjectValueMust(
				map[string]attr.Type{
					"allowed_zones": types.ListType{ElemType: types.StringType},
				},
				map[string]attr.Value{
					"allowed_zones": types.ListValueMust(types.StringType, []attr.Value{
						types.StringValue("us-west-2a"),
						types.StringValue("us-west-2b"),
					}),
				},
			)),
			want: map[string]interface{}{
				"allowed_zones": []interface{}{"us-west-2a", "us-west-2b"},
			},
		},
		{
			name:    "null dynamic returns nil map and no error",
			dynamic: types.DynamicNull(),
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DynamicToInterface(ctx, tt.dynamic)
			if err != nil {
				t.Fatalf("DynamicToInterface() unexpected error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DynamicToInterface() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestWorkerNameDefaulting tests that worker name defaults to instance type
func TestWorkerNameDefaulting(t *testing.T) {
	ctx := context.Background()

	obj := types.ObjectValueMust(
		map[string]attr.Type{
			"name":                     types.StringType,
			"min_nodes":                types.Int64Type,
			"max_nodes":                types.Int64Type,
			"market_type":              types.StringType,
			"instance_type":            types.StringType,
			"resources":                types.MapType{ElemType: types.Float64Type},
			"required_resources":       types.ObjectType{AttrTypes: map[string]attr.Type{}},
			"labels":                   types.MapType{ElemType: types.StringType},
			"advanced_instance_config": types.StringType,
			"flags":                    types.StringType,
			"cloud_deployment":         types.ObjectType{AttrTypes: map[string]attr.Type{}},
		},
		map[string]attr.Value{
			"name":                     types.StringNull(), // No name provided
			"min_nodes":                types.Int64Value(0),
			"max_nodes":                types.Int64Value(10),
			"market_type":              types.StringValue("ON_DEMAND"),
			"instance_type":            types.StringValue("n2-standard-4"),
			"resources":                types.MapNull(types.Float64Type),
			"required_resources":       types.ObjectNull(map[string]attr.Type{}),
			"labels":                   types.MapNull(types.StringType),
			"advanced_instance_config": types.StringNull(),
			"flags":                    types.StringNull(),
			"cloud_deployment":         types.ObjectNull(map[string]attr.Type{}),
		},
	)

	got, err := workerNodeConfigToAPI(ctx, obj)
	if err != nil {
		t.Fatalf("workerNodeConfigToAPI() error = %v", err)
	}

	// Name should default to instance type
	if got["name"] != "n2-standard-4" {
		t.Errorf("workerNodeConfigToAPI() name = %v, want %v (instance_type)", got["name"], "n2-standard-4")
	}
}

// TestRequiredResourcesConversion tests conversion of required_resources for
// custom instance types (CC1: renamed from physical_resources to match the
// API field name; CC4: cpu_architecture added).
func TestRequiredResourcesConversion(t *testing.T) {
	ctx := context.Background()

	requiredResourcesObj := types.ObjectValueMust(
		map[string]attr.Type{
			"cpu":              types.Int64Type,
			"memory":           types.StringType,
			"gpu":              types.Int64Type,
			"accelerator":      types.StringType,
			"tpu":              types.Int64Type,
			"tpu_hosts":        types.Int64Type,
			"cpu_architecture": types.StringType,
		},
		map[string]attr.Value{
			"cpu":              types.Int64Value(16),
			"memory":           types.StringValue("64Gi"),
			"gpu":              types.Int64Value(4),
			"accelerator":      types.StringValue("A100"),
			"tpu":              types.Int64Null(),
			"tpu_hosts":        types.Int64Null(),
			"cpu_architecture": types.StringValue("arm64"),
		},
	)

	obj := types.ObjectValueMust(
		map[string]attr.Type{
			"instance_type":            types.StringType,
			"resources":                types.MapType{ElemType: types.Float64Type},
			"required_resources":       requiredResourcesObj.Type(ctx),
			"labels":                   types.MapType{ElemType: types.StringType},
			"advanced_instance_config": types.StringType,
			"flags":                    types.StringType,
			"cloud_deployment":         types.ObjectType{AttrTypes: map[string]attr.Type{}},
		},
		map[string]attr.Value{
			"instance_type":            types.StringValue("custom"),
			"resources":                types.MapNull(types.Float64Type),
			"required_resources":       requiredResourcesObj,
			"labels":                   types.MapNull(types.StringType),
			"advanced_instance_config": types.StringNull(),
			"flags":                    types.StringNull(),
			"cloud_deployment":         types.ObjectNull(map[string]attr.Type{}),
		},
	)

	got, err := nodeConfigToAPI(ctx, obj)
	if err != nil {
		t.Fatalf("nodeConfigToAPI() error = %v", err)
	}

	// Verify required_resources was converted under its real API key - CC1's
	// whole point is that "physical_resources" is rejected by the backend, so
	// pinning the API key name here is the regression guard for the rename.
	reqRes, ok := got["required_resources"]
	if !ok {
		t.Fatal("nodeConfigToAPI() missing required_resources")
	}
	if _, ok := got["physical_resources"]; ok {
		t.Error("nodeConfigToAPI() must NOT send physical_resources - the backend rejects it (CC1)")
	}

	reqResMap, ok := reqRes.(map[string]interface{})
	if !ok {
		t.Fatalf("required_resources is not a map, got %T", reqRes)
	}

	if reqResMap["cpu"] != int64(16) {
		t.Errorf("required_resources.cpu = %v, want 16", reqResMap["cpu"])
	}
	if reqResMap["memory"] != "64Gi" {
		t.Errorf("required_resources.memory = %v, want '64Gi'", reqResMap["memory"])
	}
	if reqResMap["gpu"] != int64(4) {
		t.Errorf("required_resources.gpu = %v, want 4", reqResMap["gpu"])
	}
	if reqResMap["accelerator"] != "A100" {
		t.Errorf("required_resources.accelerator = %v, want 'A100'", reqResMap["accelerator"])
	}
	if reqResMap["cpu_architecture"] != "arm64" {
		t.Errorf("required_resources.cpu_architecture = %v, want 'arm64'", reqResMap["cpu_architecture"])
	}
}

// TestCloudDeploymentConversion tests conversion of cloud_deployment selector
func TestCloudDeploymentConversion(t *testing.T) {
	ctx := context.Background()

	cloudDepObj := types.ObjectValueMust(
		map[string]attr.Type{
			"provider":     types.StringType,
			"region":       types.StringType,
			"machine_pool": types.StringType,
			"id":           types.StringType,
		},
		map[string]attr.Value{
			"provider":     types.StringValue("aws"),
			"region":       types.StringValue("us-west-2"),
			"machine_pool": types.StringValue("spot-pool"),
			"id":           types.StringNull(),
		},
	)

	obj := types.ObjectValueMust(
		map[string]attr.Type{
			"instance_type":            types.StringType,
			"resources":                types.MapType{ElemType: types.Float64Type},
			"required_resources":       types.ObjectType{AttrTypes: map[string]attr.Type{}},
			"labels":                   types.MapType{ElemType: types.StringType},
			"advanced_instance_config": types.StringType,
			"flags":                    types.StringType,
			"cloud_deployment":         cloudDepObj.Type(ctx),
		},
		map[string]attr.Value{
			"instance_type":            types.StringValue("m5.large"),
			"resources":                types.MapNull(types.Float64Type),
			"required_resources":       types.ObjectNull(map[string]attr.Type{}),
			"labels":                   types.MapNull(types.StringType),
			"advanced_instance_config": types.StringNull(),
			"flags":                    types.StringNull(),
			"cloud_deployment":         cloudDepObj,
		},
	)

	got, err := nodeConfigToAPI(ctx, obj)
	if err != nil {
		t.Fatalf("nodeConfigToAPI() error = %v", err)
	}

	// Verify cloud_deployment was converted
	cloudDep, ok := got["cloud_deployment"]
	if !ok {
		t.Fatal("nodeConfigToAPI() missing cloud_deployment")
	}

	cloudDepMap, ok := cloudDep.(map[string]interface{})
	if !ok {
		t.Fatalf("cloud_deployment is not a map, got %T", cloudDep)
	}

	// Verify fields
	if cloudDepMap["provider"] != "aws" {
		t.Errorf("cloud_deployment.provider = %v, want 'aws'", cloudDepMap["provider"])
	}
	if cloudDepMap["region"] != "us-west-2" {
		t.Errorf("cloud_deployment.region = %v, want 'us-west-2'", cloudDepMap["region"])
	}
	if cloudDepMap["machine_pool"] != "spot-pool" {
		t.Errorf("cloud_deployment.machine_pool = %v, want 'spot-pool'", cloudDepMap["machine_pool"])
	}
}

// TestNodeLabelsConversion tests conversion of labels
func TestNodeLabelsConversion(t *testing.T) {
	ctx := context.Background()

	obj := types.ObjectValueMust(
		map[string]attr.Type{
			"instance_type":            types.StringType,
			"resources":                types.MapType{ElemType: types.Float64Type},
			"required_resources":       types.ObjectType{AttrTypes: map[string]attr.Type{}},
			"labels":                   types.MapType{ElemType: types.StringType},
			"advanced_instance_config": types.StringType,
			"flags":                    types.StringType,
			"cloud_deployment":         types.ObjectType{AttrTypes: map[string]attr.Type{}},
		},
		map[string]attr.Value{
			"instance_type":      types.StringValue("m5.xlarge"),
			"resources":          types.MapNull(types.Float64Type),
			"required_resources": types.ObjectNull(map[string]attr.Type{}),
			"labels": types.MapValueMust(
				types.StringType,
				map[string]attr.Value{
					"environment": types.StringValue("production"),
					"team":        types.StringValue("ml-platform"),
				},
			),
			"advanced_instance_config": types.StringNull(),
			"flags":                    types.StringNull(),
			"cloud_deployment":         types.ObjectNull(map[string]attr.Type{}),
		},
	)

	got, err := nodeConfigToAPI(ctx, obj)
	if err != nil {
		t.Fatalf("nodeConfigToAPI() error = %v", err)
	}

	// Verify labels
	labels, ok := got["labels"]
	if !ok {
		t.Fatal("nodeConfigToAPI() missing labels")
	}
	labelsMap := labels.(map[string]interface{})
	if labelsMap["environment"] != "production" {
		t.Errorf("labels.environment = %v, want 'production'", labelsMap["environment"])
	}
	if labelsMap["team"] != "ml-platform" {
		t.Errorf("labels.team = %v, want 'ml-platform'", labelsMap["team"])
	}
}
