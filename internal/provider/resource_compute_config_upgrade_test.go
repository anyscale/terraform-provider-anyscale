package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// This file is CC1's state-upgrade regression: physical_resources was
// renamed to required_resources, with a MANDATORY schema-version bump plus
// UpgradeState, because prior state can already hold a physical_resources
// block from a successful apply (the only shape that was ever accepted is
// null, or present with every inner field null - the backend rejects any
// non-empty value, per the Platform model). Without a real upgrader, that
// prior state would fail to decode against the new schema on the very next
// plan, for every existing user, independent of whether physical_resources
// ever did anything useful.
//
// This is a genuine unit test, not a "fake" one: it drives forge's actual
// exported UpgradeState() map and the real upgradeComputeConfigStateV0toV1
// function through the real tfsdk.State encode/decode path (the same
// mechanism the Terraform Core <-> plugin protocol uses), not a hand-rolled
// reimplementation of the upgrade logic. What it does NOT cover - the
// framework's own version-mismatch detection that routes to this upgrader in
// the first place - is HashiCorp's own tested machinery, not this provider's.

func physicalResourcesAttrTypesV0() map[string]attr.Type {
	return map[string]attr.Type{
		"cpu":         types.Int64Type,
		"memory":      types.StringType,
		"gpu":         types.Int64Type,
		"accelerator": types.StringType,
		"tpu":         types.Int64Type,
		"tpu_hosts":   types.Int64Type,
	}
}

func nodeAttrTypesV0() map[string]attr.Type {
	return map[string]attr.Type{
		"instance_type":            types.StringType,
		"resources":                types.MapType{ElemType: types.Float64Type},
		"physical_resources":       types.ObjectType{AttrTypes: physicalResourcesAttrTypesV0()},
		"labels":                   types.MapType{ElemType: types.StringType},
		"advanced_instance_config": types.StringType,
		"flags":                    types.StringType,
		"cloud_deployment":         cloudDeploymentObjectType(),
	}
}

func workerNodeAttrTypesV0() map[string]attr.Type {
	t := nodeAttrTypesV0()
	t["name"] = types.StringType
	t["min_nodes"] = types.Int64Type
	t["max_nodes"] = types.Int64Type
	t["market_type"] = types.StringType
	return t
}

// buildV0NodeObject builds a v0-shaped head_node object (no worker-only
// fields). physicalResources may be types.ObjectNull(physicalResourcesAttrTypesV0())
// (never configured) or a present-but-all-fields-null object (the one shape
// shipwright specifically checked the backend never rejects, confirmed by
// forge against the Platform model) - both must upgrade cleanly.
func buildV0NodeObject(t *testing.T, instanceType string, physicalResources types.Object) types.Object {
	t.Helper()
	return buildV0NodeObjectWithTypes(t, nodeAttrTypesV0(), nil, instanceType, physicalResources)
}

// buildV0WorkerNodeObject is buildV0NodeObject's worker_nodes[] analogue,
// including the worker-only fields (name/min_nodes/max_nodes/market_type).
func buildV0WorkerNodeObject(t *testing.T, extra map[string]attr.Value, instanceType string, physicalResources types.Object) types.Object {
	t.Helper()
	return buildV0NodeObjectWithTypes(t, workerNodeAttrTypesV0(), extra, instanceType, physicalResources)
}

func buildV0NodeObjectWithTypes(t *testing.T, attrTypes map[string]attr.Type, extra map[string]attr.Value, instanceType string, physicalResources types.Object) types.Object {
	t.Helper()
	for k := range extra {
		if _, ok := attrTypes[k]; !ok {
			t.Fatalf("buildV0NodeObjectWithTypes: extra key %q not in the given v0 attr types", k)
		}
	}

	values := map[string]attr.Value{
		"instance_type":            types.StringValue(instanceType),
		"resources":                types.MapNull(types.Float64Type),
		"physical_resources":       physicalResources,
		"labels":                   types.MapNull(types.StringType),
		"advanced_instance_config": types.StringNull(),
		"flags":                    types.StringNull(),
		"cloud_deployment":         types.ObjectNull(cloudDeploymentAttrTypes()),
	}
	for k, v := range extra {
		values[k] = v
	}

	obj, diags := types.ObjectValue(attrTypes, values)
	if diags.HasError() {
		t.Fatalf("buildV0NodeObjectWithTypes: %v", diags)
	}
	return obj
}

// upgradeV0Fixture upgrades a manufactured v0 state through the real
// tfsdk.State encode/decode path and returns the resulting v1 model.
func upgradeV0Fixture(t *testing.T, v0Model computeConfigResourceModelV0) ComputeConfigResourceModel {
	t.Helper()
	ctx := context.Background()

	r := &ComputeConfigResource{}
	upgraders := r.UpgradeState(ctx)
	upgrader, ok := upgraders[0]
	if !ok {
		t.Fatalf("UpgradeState() has no entry for schema version 0")
	}

	v0Schema := computeConfigSchemaV0()
	priorState := &tfsdk.State{
		Schema: *v0Schema,
		Raw:    tftypes.NewValue(v0Schema.Type().TerraformType(ctx), nil),
	}
	diags := priorState.Set(ctx, &v0Model)
	if diags.HasError() {
		t.Fatalf("failed to build v0 prior state fixture: %v", diags)
	}

	// resp.State must start initialized against the CURRENT (v1) schema, the
	// same way the real framework runtime primes it before calling the
	// upgrader - mirrors newImportStateResponse's pattern in
	// resource_cloud_c3_test.go for the analogous ImportState case.
	var v1SchemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &v1SchemaResp)
	if v1SchemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build v1 schema: %v", v1SchemaResp.Diagnostics)
	}

	req := resource.UpgradeStateRequest{State: priorState}
	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{
			Schema: v1SchemaResp.Schema,
			Raw:    tftypes.NewValue(v1SchemaResp.Schema.Type().TerraformType(ctx), nil),
		},
	}

	upgrader.StateUpgrader(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgradeComputeConfigStateV0toV1() diagnostics: %v", resp.Diagnostics)
	}

	var v1Model ComputeConfigResourceModel
	diags = resp.State.Get(ctx, &v1Model)
	if diags.HasError() {
		t.Fatalf("failed to decode upgraded v1 state: %v", diags)
	}
	return v1Model
}

func TestComputeConfigStateUpgradeV0toV1(t *testing.T) {
	t.Run("head_node physical_resources present but all-fields-null upgrades to an equally-empty required_resources", func(t *testing.T) {
		// The one shape confirmed to survive a real create (backend rejects
		// any non-empty physical_resources value), per shipwright's review.
		allNullPhysRes := types.ObjectValueMust(physicalResourcesAttrTypesV0(), map[string]attr.Value{
			"cpu": types.Int64Null(), "memory": types.StringNull(), "gpu": types.Int64Null(),
			"accelerator": types.StringNull(), "tpu": types.Int64Null(), "tpu_hosts": types.Int64Null(),
		})
		headNode := buildV0NodeObject(t, "m5.large", allNullPhysRes)

		v0Model := minimalV0Model(t, headNode, types.ListNull(types.ObjectType{AttrTypes: workerNodeAttrTypesV0()}))
		v1Model := upgradeV0Fixture(t, v0Model)

		if v1Model.HeadNode.IsNull() {
			t.Fatal("head_node must not be null after upgrade")
		}
		attrs := v1Model.HeadNode.Attributes()
		if _, present := attrs["physical_resources"]; present {
			t.Error("upgraded head_node must not carry a physical_resources attribute")
		}
		reqRes, ok := attrs["required_resources"].(types.Object)
		if !ok {
			t.Fatalf("upgraded head_node.required_resources is not types.Object (got %T)", attrs["required_resources"])
		}
		if reqRes.IsNull() {
			t.Error("required_resources must be an equally-empty object (all fields null), not null itself - " +
				"this was the exact shape shipwright checked the upgrader against by hand")
		}
		reqAttrs := reqRes.Attributes()
		for _, field := range []string{"cpu", "memory", "gpu", "accelerator", "tpu", "tpu_hosts"} {
			if v, ok := reqAttrs[field]; !ok || !v.IsNull() {
				t.Errorf("required_resources.%s = %v, want null (carried over unchanged from physical_resources)", field, v)
			}
		}
		if v, ok := reqAttrs["cpu_architecture"]; !ok {
			t.Error("required_resources must include the new cpu_architecture field (CC4)")
		} else if !v.IsNull() {
			t.Errorf("required_resources.cpu_architecture = %v, want null (nothing to migrate under v0)", v)
		}
	})

	t.Run("head_node physical_resources entirely null upgrades to required_resources entirely null", func(t *testing.T) {
		headNode := buildV0NodeObject(t, "m5.large", types.ObjectNull(physicalResourcesAttrTypesV0()))
		v0Model := minimalV0Model(t, headNode, types.ListNull(types.ObjectType{AttrTypes: workerNodeAttrTypesV0()}))
		v1Model := upgradeV0Fixture(t, v0Model)

		reqRes, ok := v1Model.HeadNode.Attributes()["required_resources"].(types.Object)
		if !ok {
			t.Fatalf("required_resources is not types.Object (got %T)", v1Model.HeadNode.Attributes()["required_resources"])
		}
		if !reqRes.IsNull() {
			t.Errorf("required_resources = %v, want null (never configured under v0)", reqRes)
		}
	})

	t.Run("worker_nodes upgrade elementwise, each renaming its own physical_resources", func(t *testing.T) {
		w1 := buildV0WorkerNodeObject(t, map[string]attr.Value{
			"name": types.StringValue("worker-a"), "min_nodes": types.Int64Value(0),
			"max_nodes": types.Int64Value(5), "market_type": types.StringValue("ON_DEMAND"),
		}, "m5.large", types.ObjectNull(physicalResourcesAttrTypesV0()))
		w2 := buildV0WorkerNodeObject(t, map[string]attr.Value{
			"name": types.StringValue("worker-b"), "min_nodes": types.Int64Value(1),
			"max_nodes": types.Int64Value(3), "market_type": types.StringValue("SPOT"),
		}, "m5.xlarge", types.ObjectValueMust(physicalResourcesAttrTypesV0(), map[string]attr.Value{
			"cpu": types.Int64Null(), "memory": types.StringNull(), "gpu": types.Int64Null(),
			"accelerator": types.StringNull(), "tpu": types.Int64Null(), "tpu_hosts": types.Int64Null(),
		}))

		workerNodes, diags := types.ListValue(types.ObjectType{AttrTypes: workerNodeAttrTypesV0()}, []attr.Value{w1, w2})
		if diags.HasError() {
			t.Fatalf("failed to build v0 worker_nodes fixture: %v", diags)
		}

		headNode := buildV0NodeObject(t, "m5.large", types.ObjectNull(physicalResourcesAttrTypesV0()))
		v0Model := minimalV0Model(t, headNode, workerNodes)
		v1Model := upgradeV0Fixture(t, v0Model)

		if v1Model.WorkerNodes.IsNull() {
			t.Fatal("worker_nodes must not be null after upgrade")
		}
		elems := v1Model.WorkerNodes.Elements()
		if len(elems) != 2 {
			t.Fatalf("worker_nodes has %d elements, want 2", len(elems))
		}
		for i, elem := range elems {
			obj, ok := elem.(types.Object)
			if !ok {
				t.Fatalf("worker_nodes[%d] is not types.Object (got %T)", i, elem)
			}
			attrs := obj.Attributes()
			if _, present := attrs["physical_resources"]; present {
				t.Errorf("worker_nodes[%d] must not carry physical_resources after upgrade", i)
			}
			if _, ok := attrs["required_resources"]; !ok {
				t.Errorf("worker_nodes[%d] must carry required_resources after upgrade", i)
			}
			nameAttr, ok := attrs["name"].(types.String)
			if !ok || nameAttr.IsNull() {
				t.Errorf("worker_nodes[%d].name lost during upgrade: %v", i, attrs["name"])
			}
		}
		// Worker-specific fields must pass through untouched by the shared
		// upgradeNodeV0toV1 function (it must not clobber name/min_nodes/
		// max_nodes/market_type while renaming physical_resources).
		firstAttrs := elems[0].(types.Object).Attributes()
		if v, ok := firstAttrs["name"].(types.String); !ok || v.ValueString() != "worker-a" {
			t.Errorf("worker_nodes[0].name = %v, want worker-a", firstAttrs["name"])
		}
	})

	t.Run("idle_termination_minutes and maximum_uptime_minutes are null (CC2: nothing to migrate)", func(t *testing.T) {
		headNode := buildV0NodeObject(t, "m5.large", types.ObjectNull(physicalResourcesAttrTypesV0()))
		v0Model := minimalV0Model(t, headNode, types.ListNull(types.ObjectType{AttrTypes: workerNodeAttrTypesV0()}))
		v1Model := upgradeV0Fixture(t, v0Model)

		if !v1Model.IdleTerminationMinutes.IsNull() {
			t.Errorf("IdleTerminationMinutes = %v, want null (new in v1, nothing to migrate)", v1Model.IdleTerminationMinutes)
		}
		if !v1Model.MaximumUptimeMinutes.IsNull() {
			t.Errorf("MaximumUptimeMinutes = %v, want null (new in v1, nothing to migrate)", v1Model.MaximumUptimeMinutes)
		}
	})

	t.Run("unrelated top-level fields pass through unchanged", func(t *testing.T) {
		headNode := buildV0NodeObject(t, "m5.large", types.ObjectNull(physicalResourcesAttrTypesV0()))
		v0Model := minimalV0Model(t, headNode, types.ListNull(types.ObjectType{AttrTypes: workerNodeAttrTypesV0()}))
		v0Model.CloudName = types.StringValue("my-cloud")
		v0Model.Zones = types.ListValueMust(types.StringType, []attr.Value{types.StringValue("us-west-2a")})
		v0Model.EnableCrossZoneScaling = types.BoolValue(true)

		v1Model := upgradeV0Fixture(t, v0Model)

		if v1Model.Name.ValueString() != v0Model.Name.ValueString() {
			t.Errorf("Name = %v, want %v", v1Model.Name, v0Model.Name)
		}
		if v1Model.CloudID.ValueString() != v0Model.CloudID.ValueString() {
			t.Errorf("CloudID = %v, want %v", v1Model.CloudID, v0Model.CloudID)
		}
		if v1Model.CloudName.ValueString() != "my-cloud" {
			t.Errorf("CloudName = %v, want my-cloud", v1Model.CloudName)
		}
		if !v1Model.EnableCrossZoneScaling.ValueBool() {
			t.Error("EnableCrossZoneScaling = false, want true (must pass through unchanged)")
		}
		if v1Model.Zones.IsNull() || len(v1Model.Zones.Elements()) != 1 {
			t.Errorf("Zones = %v, want a single-element list carried over from v0", v1Model.Zones)
		}
	})
}

// minimalV0Model returns a fully-populated, otherwise-uninteresting v0 model
// with the given head_node/worker_nodes, so each test case only has to build
// the node shape it actually cares about.
func minimalV0Model(t *testing.T, headNode types.Object, workerNodes types.List) computeConfigResourceModelV0 {
	t.Helper()
	return computeConfigResourceModelV0{
		ID:                     types.StringValue("my-config"),
		ConfigID:               types.StringValue("cpt_v0_abc123"),
		NameVersion:            types.StringValue("my-config:1"),
		Name:                   types.StringValue("my-config"),
		CloudID:                types.StringValue("cld_abc123"),
		CloudName:              types.StringNull(),
		CloudResource:          types.StringNull(),
		Zones:                  types.ListNull(types.StringType),
		MinResources:           types.MapNull(types.Float64Type),
		MaxResources:           types.MapNull(types.Float64Type),
		EnableCrossZoneScaling: types.BoolValue(false),
		AdvancedInstanceConfig: types.DynamicNull(),
		AutoSelectWorkerConfig: types.BoolValue(false),
		Flags:                  types.DynamicNull(),
		Version:                types.Int64Value(1),
		CreatedAt:              types.StringValue("2024-01-01T00:00:00Z"),
		LastModifiedAt:         types.StringValue("2024-01-01T00:00:00Z"),
		HeadNode:               headNode,
		WorkerNodes:            workerNodes,
	}
}
