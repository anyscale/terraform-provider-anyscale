package provider

import (
	"context"
	"testing"
)

// TestDataSourceSpecFromAPI tests converting API spec to data source model format
func TestDataSourceSpecFromAPI(t *testing.T) {
	ctx := context.Background()
	d := &GlobalResourceSchedulerDataSource{}

	tests := []struct {
		name     string
		apiSpec  map[string]any
		wantNil  bool
		wantKind string
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
			name: "ANYSCALE_MANAGED spec",
			apiSpec: map[string]any{
				"kind": "ANYSCALE_MANAGED",
			},
			wantNil:  false,
			wantKind: "ANYSCALE_MANAGED",
		},
		{
			name: "spec without kind defaults to ANYSCALE_MANAGED",
			apiSpec: map[string]any{
				"machine_types": []any{},
			},
			wantNil:  false,
			wantKind: "ANYSCALE_MANAGED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := d.specFromAPI(ctx, tt.apiSpec)
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

			if got[0].Kind.ValueString() != tt.wantKind {
				t.Errorf("specFromAPI() kind = %v, want %v", got[0].Kind.ValueString(), tt.wantKind)
			}
		})
	}
}

// TestDataSourceSpecFromAPIWithMachineTypes tests data source spec conversion with machine types
func TestDataSourceSpecFromAPIWithMachineTypes(t *testing.T) {
	ctx := context.Background()
	d := &GlobalResourceSchedulerDataSource{}

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
					map[string]any{
						"instance_type": "m5.2xlarge",
						"market_type":   "SPOT",
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
							},
							map[string]any{
								"selector": "workload-type in (service)",
								"priority": float64(200),
								"quota":    float64(5),
							},
						},
					},
				},
			},
			map[string]any{
				"machine_type": "RES-GPU-A10",
				"launch_templates": []any{
					map[string]any{
						"instance_type": "g5.2xlarge",
						"market_type":   "ON_DEMAND",
					},
				},
				"partitions": []any{
					map[string]any{
						"name": "gpu-partition",
						"size": float64(5),
					},
				},
			},
		},
	}

	got, err := d.specFromAPI(ctx, apiSpec)
	if err != nil {
		t.Fatalf("specFromAPI() error = %v", err)
	}

	if len(got) == 0 {
		t.Fatal("specFromAPI() returned nil or empty")
	}

	spec := got[0]

	// Verify kind
	if spec.Kind.ValueString() != "ANYSCALE_MANAGED" {
		t.Errorf("specFromAPI() kind = %v, want ANYSCALE_MANAGED", spec.Kind.ValueString())
	}

	// Verify machine types count
	if len(spec.MachineTypes) != 2 {
		t.Fatalf("specFromAPI() machine_types length = %d, want 2", len(spec.MachineTypes))
	}

	// Verify first machine type
	mt1 := spec.MachineTypes[0]
	if mt1.Name.ValueString() != "RES-8CPU-32GB" {
		t.Errorf("specFromAPI() mt[0].name = %v, want RES-8CPU-32GB", mt1.Name.ValueString())
	}

	// Verify launch templates for first machine type
	if len(mt1.LaunchTemplates) != 2 {
		t.Fatalf("specFromAPI() mt[0].launch_templates length = %d, want 2", len(mt1.LaunchTemplates))
	}

	lt1 := mt1.LaunchTemplates[0]
	if lt1.InstanceType.ValueString() != "m5.2xlarge" {
		t.Errorf("specFromAPI() lt[0].instance_type = %v, want m5.2xlarge", lt1.InstanceType.ValueString())
	}
	if lt1.MarketType.ValueString() != "ON_DEMAND" {
		t.Errorf("specFromAPI() lt[0].market_type = %v, want ON_DEMAND", lt1.MarketType.ValueString())
	}

	lt2 := mt1.LaunchTemplates[1]
	if lt2.MarketType.ValueString() != "SPOT" {
		t.Errorf("specFromAPI() lt[1].market_type = %v, want SPOT", lt2.MarketType.ValueString())
	}

	// Verify recycle policy
	if len(mt1.RecyclePolicy) != 1 {
		t.Fatalf("specFromAPI() mt[0].recycle_policy length = %d, want 1", len(mt1.RecyclePolicy))
	}
	rp := mt1.RecyclePolicy[0]
	if rp.MaxWorkloads.ValueInt64() != 100 {
		t.Errorf("specFromAPI() recycle_policy.max_workloads = %v, want 100", rp.MaxWorkloads.ValueInt64())
	}
	if rp.RotationInterval.ValueString() != "24h" {
		t.Errorf("specFromAPI() recycle_policy.rotation_interval = %v, want 24h", rp.RotationInterval.ValueString())
	}
	if rp.MaxIdleDuration.ValueString() != "60m" {
		t.Errorf("specFromAPI() recycle_policy.max_idle_duration = %v, want 60m", rp.MaxIdleDuration.ValueString())
	}

	// Verify partitions for first machine type
	if len(mt1.Partitions) != 1 {
		t.Fatalf("specFromAPI() mt[0].partitions length = %d, want 1", len(mt1.Partitions))
	}
	p1 := mt1.Partitions[0]
	if p1.Name.ValueString() != "default" {
		t.Errorf("specFromAPI() partition.name = %v, want default", p1.Name.ValueString())
	}
	if p1.Size.ValueInt64() != 10 {
		t.Errorf("specFromAPI() partition.size = %v, want 10", p1.Size.ValueInt64())
	}

	// Verify rules
	if len(p1.Rules) != 2 {
		t.Fatalf("specFromAPI() partition.rules length = %d, want 2", len(p1.Rules))
	}
	rule1 := p1.Rules[0]
	if rule1.Selector.ValueString() != "workload-type in (job)" {
		t.Errorf("specFromAPI() rule[0].selector = %v, want 'workload-type in (job)'", rule1.Selector.ValueString())
	}
	if rule1.Priority.ValueInt64() != 100 {
		t.Errorf("specFromAPI() rule[0].priority = %v, want 100", rule1.Priority.ValueInt64())
	}

	rule2 := p1.Rules[1]
	if rule2.Quota.ValueInt64() != 5 {
		t.Errorf("specFromAPI() rule[1].quota = %v, want 5", rule2.Quota.ValueInt64())
	}

	// Verify second machine type
	mt2 := spec.MachineTypes[1]
	if mt2.Name.ValueString() != "RES-GPU-A10" {
		t.Errorf("specFromAPI() mt[1].name = %v, want RES-GPU-A10", mt2.Name.ValueString())
	}
	if len(mt2.LaunchTemplates) != 1 {
		t.Errorf("specFromAPI() mt[1].launch_templates length = %d, want 1", len(mt2.LaunchTemplates))
	}
}

// TestDataSourceSpecFromAPINullValues tests handling of null/missing values
func TestDataSourceSpecFromAPINullValues(t *testing.T) {
	ctx := context.Background()
	d := &GlobalResourceSchedulerDataSource{}

	apiSpec := map[string]any{
		"kind": "ANYSCALE_MANAGED",
		"machine_types": []any{
			map[string]any{
				"machine_type": "RES-4CPU-16GB",
				"launch_templates": []any{
					map[string]any{
						"instance_type": "m5.xlarge",
						"market_type":   "ON_DEMAND",
						// No zones - should result in null list
					},
				},
				// No recycle_policy
				"partitions": []any{
					map[string]any{
						"name": "default",
						"size": float64(5),
						"rules": []any{
							map[string]any{
								"selector": "env in (test)",
								// No priority or quota
							},
						},
					},
				},
			},
		},
	}

	got, err := d.specFromAPI(ctx, apiSpec)
	if err != nil {
		t.Fatalf("specFromAPI() error = %v", err)
	}

	if len(got) == 0 {
		t.Fatal("specFromAPI() returned nil or empty")
	}

	mt := got[0].MachineTypes[0]

	// Verify launch template zones is null
	lt := mt.LaunchTemplates[0]
	if !lt.Zones.IsNull() {
		t.Errorf("specFromAPI() zones should be null when not provided")
	}

	// Verify recycle policy is empty (not set)
	if len(mt.RecyclePolicy) != 0 {
		t.Errorf("specFromAPI() recycle_policy should be empty when not provided, got length %d", len(mt.RecyclePolicy))
	}

	// Verify rule priority and quota are null
	rule := mt.Partitions[0].Rules[0]
	if !rule.Priority.IsNull() {
		t.Errorf("specFromAPI() rule.priority should be null when not provided")
	}
	if !rule.Quota.IsNull() {
		t.Errorf("specFromAPI() rule.quota should be null when not provided")
	}
}
