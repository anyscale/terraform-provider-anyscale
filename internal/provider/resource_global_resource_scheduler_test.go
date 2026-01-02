package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestSpecToAPI tests converting a Terraform spec model to API format
func TestSpecToAPI(t *testing.T) {
	ctx := context.Background()
	r := &GlobalResourceSchedulerResource{}

	tests := []struct {
		name    string
		spec    []GlobalResourceSchedulerSpecModel
		wantNil bool
	}{
		{
			name:    "nil spec returns nil",
			spec:    nil,
			wantNil: true,
		},
		{
			name:    "empty spec returns nil",
			spec:    []GlobalResourceSchedulerSpecModel{},
			wantNil: true,
		},
		{
			name: "spec with machine types",
			spec: []GlobalResourceSchedulerSpecModel{
				{
					MachineTypes: []MachineTypeModel{},
				},
			},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.specToAPI(ctx, tt.spec)

			if tt.wantNil {
				if got != nil {
					t.Errorf("specToAPI() = %v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Errorf("specToAPI() = nil, want non-nil")
				return
			}

			// All global resource schedulers should have kind = ANYSCALE_MANAGED (hardcoded)
			if kind, ok := got["kind"].(string); ok {
				if kind != "ANYSCALE_MANAGED" {
					t.Errorf("specToAPI() kind = %v, want ANYSCALE_MANAGED", kind)
				}
			} else {
				t.Errorf("specToAPI() missing kind field")
			}
		})
	}
}

// TestSpecToAPIWithMachineTypes tests spec conversion with machine types
func TestSpecToAPIWithMachineTypes(t *testing.T) {
	ctx := context.Background()
	r := &GlobalResourceSchedulerResource{}

	zonesList, _ := types.ListValueFrom(ctx, types.StringType, []string{"us-west-2a", "us-west-2b"})

	spec := []GlobalResourceSchedulerSpecModel{
		{
			MachineTypes: []MachineTypeModel{
				{
					Name: types.StringValue("RES-8CPU-32GB"),
					LaunchTemplates: []LaunchTemplateModel{
						{
							InstanceType:           types.StringValue("m5.2xlarge"),
							MarketType:             types.StringValue("ON_DEMAND"),
							Zones:                  zonesList,
							AdvancedInstanceConfig: types.MapNull(types.StringType),
						},
						{
							InstanceType:           types.StringValue("m5.2xlarge"),
							MarketType:             types.StringValue("SPOT"),
							Zones:                  types.ListNull(types.StringType),
							AdvancedInstanceConfig: types.MapNull(types.StringType),
						},
					},
					RecyclePolicy: []RecyclePolicyModel{
						{
							MaxWorkloads:     types.Int64Value(100),
							RotationInterval: types.StringValue("24h"),
							MaxIdleDuration:  types.StringValue("60m"),
						},
					},
					Partitions: []PartitionModel{
						{
							Name: types.StringValue("default"),
							Size: types.Int64Value(10),
							Rules: []RuleModel{
								{
									Selector: types.StringValue("workload-type in (job)"),
									Priority: types.Int64Value(100),
									Quota:    types.Int64Null(),
								},
							},
						},
					},
				},
			},
		},
	}

	got := r.specToAPI(ctx, spec)

	if got == nil {
		t.Fatal("specToAPI() returned nil")
	}

	// Verify kind is always ANYSCALE_MANAGED
	if got["kind"] != "ANYSCALE_MANAGED" {
		t.Errorf("specToAPI() kind = %v, want ANYSCALE_MANAGED", got["kind"])
	}

	// Verify machine_types exists
	machineTypes, ok := got["machine_types"].([]map[string]any)
	if !ok {
		t.Fatal("specToAPI() missing or invalid machine_types")
	}

	if len(machineTypes) != 1 {
		t.Errorf("specToAPI() machine_types length = %d, want 1", len(machineTypes))
	}

	mt := machineTypes[0]

	// Verify machine type name
	if mt["machine_type"] != "RES-8CPU-32GB" {
		t.Errorf("specToAPI() machine_type = %v, want RES-8CPU-32GB", mt["machine_type"])
	}

	// Verify launch templates
	templates, ok := mt["launch_templates"].([]map[string]any)
	if !ok {
		t.Fatal("specToAPI() missing or invalid launch_templates")
	}
	if len(templates) != 2 {
		t.Errorf("specToAPI() launch_templates length = %d, want 2", len(templates))
	}

	// Verify first launch template
	if templates[0]["instance_type"] != "m5.2xlarge" {
		t.Errorf("specToAPI() launch_template[0].instance_type = %v, want m5.2xlarge", templates[0]["instance_type"])
	}
	if templates[0]["market_type"] != "ON_DEMAND" {
		t.Errorf("specToAPI() launch_template[0].market_type = %v, want ON_DEMAND", templates[0]["market_type"])
	}

	// Verify zones on first template
	zones, ok := templates[0]["zones"].([]string)
	if !ok {
		t.Fatal("specToAPI() missing or invalid zones")
	}
	if len(zones) != 2 {
		t.Errorf("specToAPI() zones length = %d, want 2", len(zones))
	}

	// Verify recycle policy
	rp, ok := mt["recycle_policy"].(map[string]any)
	if !ok {
		t.Fatal("specToAPI() missing or invalid recycle_policy")
	}
	if rp["max_workloads"] != int64(100) {
		t.Errorf("specToAPI() recycle_policy.max_workloads = %v, want 100", rp["max_workloads"])
	}
	if rp["rotation_interval"] != "24h" {
		t.Errorf("specToAPI() recycle_policy.rotation_interval = %v, want 24h", rp["rotation_interval"])
	}

	// Verify partitions
	partitions, ok := mt["partitions"].([]map[string]any)
	if !ok {
		t.Fatal("specToAPI() missing or invalid partitions")
	}
	if len(partitions) != 1 {
		t.Errorf("specToAPI() partitions length = %d, want 1", len(partitions))
	}
	if partitions[0]["name"] != "default" {
		t.Errorf("specToAPI() partition[0].name = %v, want default", partitions[0]["name"])
	}
	if partitions[0]["size"] != int64(10) {
		t.Errorf("specToAPI() partition[0].size = %v, want 10", partitions[0]["size"])
	}

	// Verify rules
	rules, ok := partitions[0]["rules"].([]map[string]any)
	if !ok {
		t.Fatal("specToAPI() missing or invalid rules")
	}
	if len(rules) != 1 {
		t.Errorf("specToAPI() rules length = %d, want 1", len(rules))
	}
	if rules[0]["selector"] != "workload-type in (job)" {
		t.Errorf("specToAPI() rule[0].selector = %v, want 'workload-type in (job)'", rules[0]["selector"])
	}
	if rules[0]["priority"] != int64(100) {
		t.Errorf("specToAPI() rule[0].priority = %v, want 100", rules[0]["priority"])
	}
}

// TestSpecFromAPI tests converting API spec to Terraform model format
func TestSpecFromAPI(t *testing.T) {
	ctx := context.Background()
	r := &GlobalResourceSchedulerResource{}

	tests := []struct {
		name    string
		apiSpec map[string]any
		wantNil bool
	}{
		{
			name:    "nil spec returns nil",
			apiSpec: nil,
			wantNil: true,
		},
		{
			name:    "empty spec returns nil",
			apiSpec: map[string]any{},
			wantNil: true,
		},
		{
			name: "spec with kind",
			apiSpec: map[string]any{
				"kind": "ANYSCALE_MANAGED",
			},
			wantNil: false,
		},
		{
			name: "spec without kind still works",
			apiSpec: map[string]any{
				"machine_types": []any{},
			},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.specFromAPI(ctx, tt.apiSpec)
			if err != nil {
				t.Fatalf("specFromAPI() error = %v", err)
			}

			if tt.wantNil {
				if got != nil {
					t.Errorf("specFromAPI() = %v, want nil", got)
				}
				return
			}

			if len(got) == 0 {
				t.Errorf("specFromAPI() = nil or empty, want non-empty")
				return
			}
		})
	}
}

// TestSpecFromAPIWithMachineTypes tests spec conversion from API with machine types
func TestSpecFromAPIWithMachineTypes(t *testing.T) {
	ctx := context.Background()
	r := &GlobalResourceSchedulerResource{}

	apiSpec := map[string]any{
		"kind": "ANYSCALE_MANAGED",
		"machine_types": []any{
			map[string]any{
				"machine_type": "RES-8CPU-32GB",
				"launch_templates": []any{
					map[string]any{
						"instance_type": "m5.2xlarge",
						"market_type":   "ON_DEMAND",
						"zones":         []any{"us-west-2a", "us-west-2b"},
					},
				},
				"recycle_policy": map[string]any{
					"max_workloads":     float64(100),
					"rotation_interval": "24h",
					"max_idle_duration": "60m",
				},
				"partitions": []any{
					map[string]any{
						"name": "default",
						"size": float64(10),
						"rules": []any{
							map[string]any{
								"selector": "workload-type in (job)",
								"priority": float64(100),
								"quota":    float64(5),
							},
						},
					},
				},
			},
		},
	}

	got, err := r.specFromAPI(ctx, apiSpec)
	if err != nil {
		t.Fatalf("specFromAPI() error = %v", err)
	}

	if len(got) == 0 {
		t.Fatal("specFromAPI() returned nil or empty")
	}

	spec := got[0]

	// Verify machine types
	if len(spec.MachineTypes) != 1 {
		t.Fatalf("specFromAPI() machine_types length = %d, want 1", len(spec.MachineTypes))
	}

	mt := spec.MachineTypes[0]

	// Verify machine type name
	if mt.Name.ValueString() != "RES-8CPU-32GB" {
		t.Errorf("specFromAPI() machine_type name = %v, want RES-8CPU-32GB", mt.Name.ValueString())
	}

	// Verify launch templates
	if len(mt.LaunchTemplates) != 1 {
		t.Fatalf("specFromAPI() launch_templates length = %d, want 1", len(mt.LaunchTemplates))
	}

	lt := mt.LaunchTemplates[0]
	if lt.InstanceType.ValueString() != "m5.2xlarge" {
		t.Errorf("specFromAPI() instance_type = %v, want m5.2xlarge", lt.InstanceType.ValueString())
	}
	if lt.MarketType.ValueString() != "ON_DEMAND" {
		t.Errorf("specFromAPI() market_type = %v, want ON_DEMAND", lt.MarketType.ValueString())
	}

	// Verify recycle policy
	if len(mt.RecyclePolicy) != 1 {
		t.Fatalf("specFromAPI() recycle_policy length = %d, want 1", len(mt.RecyclePolicy))
	}
	rp := mt.RecyclePolicy[0]
	if rp.MaxWorkloads.ValueInt64() != 100 {
		t.Errorf("specFromAPI() max_workloads = %v, want 100", rp.MaxWorkloads.ValueInt64())
	}
	if rp.RotationInterval.ValueString() != "24h" {
		t.Errorf("specFromAPI() rotation_interval = %v, want 24h", rp.RotationInterval.ValueString())
	}

	// Verify partitions
	if len(mt.Partitions) != 1 {
		t.Fatalf("specFromAPI() partitions length = %d, want 1", len(mt.Partitions))
	}
	p := mt.Partitions[0]
	if p.Name.ValueString() != "default" {
		t.Errorf("specFromAPI() partition name = %v, want default", p.Name.ValueString())
	}
	if p.Size.ValueInt64() != 10 {
		t.Errorf("specFromAPI() partition size = %v, want 10", p.Size.ValueInt64())
	}

	// Verify rules
	if len(p.Rules) != 1 {
		t.Fatalf("specFromAPI() rules length = %d, want 1", len(p.Rules))
	}
	rule := p.Rules[0]
	if rule.Selector.ValueString() != "workload-type in (job)" {
		t.Errorf("specFromAPI() rule selector = %v, want 'workload-type in (job)'", rule.Selector.ValueString())
	}
	if rule.Priority.ValueInt64() != 100 {
		t.Errorf("specFromAPI() rule priority = %v, want 100", rule.Priority.ValueInt64())
	}
	if rule.Quota.ValueInt64() != 5 {
		t.Errorf("specFromAPI() rule quota = %v, want 5", rule.Quota.ValueInt64())
	}
}

// TestSpecRoundTrip tests that spec can be converted to API and back without data loss
// Note: This test simulates the JSON round-trip that happens in real API calls
func TestSpecRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := &GlobalResourceSchedulerResource{}

	// Create an API spec directly (simulating what comes from the API)
	// This avoids the type assertion issues with []map[string]any vs []any
	apiSpec := map[string]any{
		"kind": "ANYSCALE_MANAGED",
		"machine_types": []any{
			map[string]any{
				"machine_type": "RES-4CPU-16GB",
				"launch_templates": []any{
					map[string]any{
						"instance_type": "m5.xlarge",
						"market_type":   "SPOT",
						"zones":         []any{"us-west-2a"},
					},
				},
				"partitions": []any{
					map[string]any{
						"name": "test-partition",
						"size": float64(5),
						"rules": []any{
							map[string]any{
								"selector": "env in (test)",
								"priority": float64(50),
							},
						},
					},
				},
			},
		},
	}

	// Convert from API to model
	result, err := r.specFromAPI(ctx, apiSpec)
	if err != nil {
		t.Fatalf("specFromAPI() error = %v", err)
	}

	if len(result) == 0 {
		t.Fatal("specFromAPI() returned nil or empty")
	}

	// Verify machine types preserved
	if len(result[0].MachineTypes) != 1 {
		t.Fatalf("Round-trip machine_types length = %d, want 1", len(result[0].MachineTypes))
	}

	mt := result[0].MachineTypes[0]
	if mt.Name.ValueString() != "RES-4CPU-16GB" {
		t.Errorf("Round-trip machine_type name = %v, want RES-4CPU-16GB", mt.Name.ValueString())
	}

	// Convert back to API format
	backToAPI := r.specToAPI(ctx, result)
	if backToAPI == nil {
		t.Fatal("specToAPI() returned nil on round-trip")
	}

	// Verify kind is always ANYSCALE_MANAGED (hardcoded)
	if backToAPI["kind"] != "ANYSCALE_MANAGED" {
		t.Errorf("Round-trip API kind = %v, want ANYSCALE_MANAGED", backToAPI["kind"])
	}

	machineTypes, ok := backToAPI["machine_types"].([]map[string]any)
	if !ok {
		t.Fatal("Round-trip API machine_types is invalid type")
	}
	if len(machineTypes) != 1 {
		t.Errorf("Round-trip API machine_types length = %d, want 1", len(machineTypes))
	}
	if machineTypes[0]["machine_type"] != "RES-4CPU-16GB" {
		t.Errorf("Round-trip API machine_type = %v, want RES-4CPU-16GB", machineTypes[0]["machine_type"])
	}
}

// TestMarketTypeValues tests the valid market type values
func TestMarketTypeValues(t *testing.T) {
	validTypes := []string{"ON_DEMAND", "SPOT"}

	for _, mt := range validTypes {
		t.Run(mt, func(t *testing.T) {
			// Verify these are the expected market types
			if mt != "ON_DEMAND" && mt != "SPOT" {
				t.Errorf("Unexpected market type: %s", mt)
			}
		})
	}
}
