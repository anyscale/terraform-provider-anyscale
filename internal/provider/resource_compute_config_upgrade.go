package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure ComputeConfigResource satisfies the state-upgrade interface.
var _ resource.ResourceWithUpgradeState = &ComputeConfigResource{}

// UpgradeState implements the v0 -> v1 migration for CC1: physical_resources
// was renamed to required_resources on head_node and worker_nodes, since the
// Anyscale API has always rejected physical_resources outright on any
// non-empty value (the only state that can exist under v0 has it null, or
// present with every inner field null). This is this provider's first state
// upgrader; there is no other in-repo example to mirror.
func (r *ComputeConfigResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema:   computeConfigSchemaV0(),
			StateUpgrader: upgradeComputeConfigStateV0toV1,
		},
	}
}

// computeConfigResourceModelV0 is the v0 (pre-CC1/CC2) resource model. Only
// the top level needs its own type: head_node/worker_nodes stay generic
// types.Object/types.List containers here exactly as in the current model,
// since decoding the outer struct does not inspect what is nested inside
// them. The v0-specific nested shape (physical_resources, no
// cpu_architecture) is handled separately in upgradeNodeV0toV1 by editing the
// attribute map directly, the same way maskNodeFromPrior already does,
// instead of declaring a second set of nested Go structs.
type computeConfigResourceModelV0 struct {
	ID                     types.String  `tfsdk:"id"`
	ConfigID               types.String  `tfsdk:"config_id"`
	NameVersion            types.String  `tfsdk:"name_version"`
	Name                   types.String  `tfsdk:"name"`
	CloudID                types.String  `tfsdk:"cloud_id"`
	CloudName              types.String  `tfsdk:"cloud_name"`
	CloudResource          types.String  `tfsdk:"cloud_resource"`
	Zones                  types.List    `tfsdk:"zones"`
	MinResources           types.Map     `tfsdk:"min_resources"`
	MaxResources           types.Map     `tfsdk:"max_resources"`
	EnableCrossZoneScaling types.Bool    `tfsdk:"enable_cross_zone_scaling"`
	AdvancedInstanceConfig types.Dynamic `tfsdk:"advanced_instance_config"`
	AutoSelectWorkerConfig types.Bool    `tfsdk:"auto_select_worker_config"`
	Flags                  types.Dynamic `tfsdk:"flags"`
	Version                types.Int64   `tfsdk:"version"`
	CreatedAt              types.String  `tfsdk:"created_at"`
	LastModifiedAt         types.String  `tfsdk:"last_modified_at"`
	HeadNode               types.Object  `tfsdk:"head_node"`
	WorkerNodes            types.List    `tfsdk:"worker_nodes"`
}

// computeConfigSchemaV0 is a frozen copy of the schema as shipped before CC1
// (required_resources rename) and CC2 (idle/max-uptime) added: physical_resources
// instead of required_resources, no cpu_architecture, no
// idle_termination_minutes/maximum_uptime_minutes. It exists solely so
// UpgradeState can decode v0 state; do not evolve it going forward -- it is a
// historical snapshot, not a second copy of the live schema.
func computeConfigSchemaV0() *schema.Schema {
	physicalResourcesAttrsV0 := map[string]schema.Attribute{
		"cpu":         schema.Int64Attribute{Optional: true},
		"memory":      schema.StringAttribute{Optional: true},
		"gpu":         schema.Int64Attribute{Optional: true},
		"accelerator": schema.StringAttribute{Optional: true},
		"tpu":         schema.Int64Attribute{Optional: true},
		"tpu_hosts":   schema.Int64Attribute{Optional: true},
	}
	cloudDeploymentAttrsV0 := map[string]schema.Attribute{
		"provider":     schema.StringAttribute{Optional: true},
		"region":       schema.StringAttribute{Optional: true},
		"machine_pool": schema.StringAttribute{Optional: true},
		"id":           schema.StringAttribute{Optional: true},
	}
	nodeAttrsV0 := map[string]schema.Attribute{
		"instance_type": schema.StringAttribute{Required: true},
		"resources": schema.MapAttribute{
			ElementType: types.Float64Type,
			Optional:    true,
			Computed:    true,
		},
		"physical_resources": schema.SingleNestedAttribute{
			Optional:   true,
			Attributes: physicalResourcesAttrsV0,
		},
		"labels":                   schema.MapAttribute{ElementType: types.StringType, Optional: true},
		"advanced_instance_config": schema.StringAttribute{Optional: true},
		"flags":                    schema.StringAttribute{Optional: true},
		"cloud_deployment": schema.SingleNestedAttribute{
			Optional:   true,
			Attributes: cloudDeploymentAttrsV0,
		},
	}

	workerNodeAttrsV0 := make(map[string]schema.Attribute, len(nodeAttrsV0)+4)
	for k, v := range nodeAttrsV0 {
		workerNodeAttrsV0[k] = v
	}
	workerNodeAttrsV0["name"] = schema.StringAttribute{Optional: true, Computed: true}
	workerNodeAttrsV0["min_nodes"] = schema.Int64Attribute{Optional: true, Computed: true}
	workerNodeAttrsV0["max_nodes"] = schema.Int64Attribute{Optional: true, Computed: true}
	workerNodeAttrsV0["market_type"] = schema.StringAttribute{Optional: true, Computed: true}

	return &schema.Schema{
		Version: 0,
		Attributes: map[string]schema.Attribute{
			"id":                        schema.StringAttribute{Computed: true},
			"config_id":                 schema.StringAttribute{Computed: true},
			"name_version":              schema.StringAttribute{Computed: true},
			"name":                      schema.StringAttribute{Required: true},
			"cloud_id":                  schema.StringAttribute{Optional: true, Computed: true},
			"cloud_name":                schema.StringAttribute{Optional: true},
			"cloud_resource":            schema.StringAttribute{Optional: true},
			"zones":                     schema.ListAttribute{ElementType: types.StringType, Optional: true},
			"min_resources":             schema.MapAttribute{ElementType: types.Float64Type, Optional: true},
			"max_resources":             schema.MapAttribute{ElementType: types.Float64Type, Optional: true},
			"enable_cross_zone_scaling": schema.BoolAttribute{Optional: true, Computed: true},
			"advanced_instance_config":  schema.DynamicAttribute{Optional: true},
			"auto_select_worker_config": schema.BoolAttribute{Optional: true, Computed: true},
			"flags":                     schema.DynamicAttribute{Optional: true},
			"version":                   schema.Int64Attribute{Computed: true},
			"created_at":                schema.StringAttribute{Computed: true},
			"last_modified_at":          schema.StringAttribute{Computed: true},
			"head_node": schema.SingleNestedAttribute{
				Required:   true,
				Attributes: nodeAttrsV0,
			},
			"worker_nodes": schema.ListNestedAttribute{
				Optional:     true,
				NestedObject: schema.NestedAttributeObject{Attributes: workerNodeAttrsV0},
			},
		},
	}
}

// upgradeComputeConfigStateV0toV1 renames physical_resources to
// required_resources inside head_node and every worker_nodes element, and
// fills the two attributes added by CC2 with null (there is nothing to
// migrate for them -- they never existed under v0).
func upgradeComputeConfigStateV0toV1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 0 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState computeConfigResourceModelV0
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	upgradedHeadNode, headDiags := upgradeNodeV0toV1(ctx, priorState.HeadNode, nodeConfigAttrTypes())
	resp.Diagnostics.Append(headDiags...)

	upgradedWorkerNodes, workerDiags := upgradeWorkerNodesV0toV1(ctx, priorState.WorkerNodes)
	resp.Diagnostics.Append(workerDiags...)

	if resp.Diagnostics.HasError() {
		return
	}

	newState := ComputeConfigResourceModel{
		ID:                     priorState.ID,
		ConfigID:               priorState.ConfigID,
		NameVersion:            priorState.NameVersion,
		Name:                   priorState.Name,
		CloudID:                priorState.CloudID,
		CloudName:              priorState.CloudName,
		CloudResource:          priorState.CloudResource,
		Zones:                  priorState.Zones,
		MinResources:           priorState.MinResources,
		MaxResources:           priorState.MaxResources,
		EnableCrossZoneScaling: priorState.EnableCrossZoneScaling,
		IdleTerminationMinutes: types.Int64Null(), // CC2: new in v1, nothing to migrate
		MaximumUptimeMinutes:   types.Int64Null(), // CC2: new in v1, nothing to migrate
		AdvancedInstanceConfig: priorState.AdvancedInstanceConfig,
		AutoSelectWorkerConfig: priorState.AutoSelectWorkerConfig,
		Flags:                  priorState.Flags,
		Version:                priorState.Version,
		CreatedAt:              priorState.CreatedAt,
		LastModifiedAt:         priorState.LastModifiedAt,
		HeadNode:               upgradedHeadNode,
		WorkerNodes:            upgradedWorkerNodes,
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// upgradeNodeV0toV1 renames the physical_resources attribute of a single v0
// node object (head_node, or one worker_nodes element) to required_resources,
// adding a null cpu_architecture (CC4: new in v1, nothing to migrate). Every
// other attribute -- including worker-only ones like name/min_nodes when
// called for a worker element -- passes through untouched, so the same
// function serves both head_node and worker_nodes callers; only the target
// attrTypes (and therefore which extra keys are expected) differs.
func upgradeNodeV0toV1(ctx context.Context, v0Node types.Object, attrTypes map[string]attr.Type) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics

	if v0Node.IsNull() || v0Node.IsUnknown() {
		return types.ObjectNull(attrTypes), diags
	}

	v0Attrs := v0Node.Attributes()
	newAttrs := make(map[string]attr.Value, len(attrTypes))
	for k, v := range v0Attrs {
		if k == "physical_resources" {
			continue
		}
		newAttrs[k] = v
	}

	requiredResources := types.ObjectNull(requiredResourcesAttrTypes())
	if physRes, ok := v0Attrs["physical_resources"].(types.Object); ok && !physRes.IsNull() && !physRes.IsUnknown() {
		physAttrs := physRes.Attributes()
		reqAttrs := make(map[string]attr.Value, len(requiredResourcesAttrTypes()))
		for k, v := range physAttrs {
			reqAttrs[k] = v
		}
		reqAttrs["cpu_architecture"] = types.StringNull()

		reqObj, reqDiags := types.ObjectValue(requiredResourcesAttrTypes(), reqAttrs)
		diags.Append(reqDiags...)
		requiredResources = reqObj
	}
	newAttrs["required_resources"] = requiredResources

	newObj, objDiags := types.ObjectValue(attrTypes, newAttrs)
	diags.Append(objDiags...)
	return newObj, diags
}

// upgradeWorkerNodesV0toV1 applies upgradeNodeV0toV1 elementwise across the
// worker_nodes list, mirroring maskWorkerNodesFromPrior's shape.
func upgradeWorkerNodesV0toV1(ctx context.Context, v0Workers types.List) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	newElemType := types.ObjectType{AttrTypes: workerNodeConfigAttrTypes()}

	if v0Workers.IsNull() || v0Workers.IsUnknown() {
		return types.ListNull(newElemType), diags
	}

	elems := v0Workers.Elements()
	newElems := make([]attr.Value, 0, len(elems))
	for _, e := range elems {
		obj, ok := e.(types.Object)
		if !ok {
			// Unreachable given PriorSchema declares worker_nodes as a list of
			// objects, but fail loudly rather than silently drop a worker group
			// out of state, matching nodeConfigToAPI's own worker-loop guard.
			diags.AddError("Unexpected Worker Node Type in Prior State",
				"A worker_nodes element in the prior state was not an object during the v0-to-v1 state upgrade. This is a bug in the provider; please report it.")
			return types.ListNull(newElemType), diags
		}
		upgraded, upgradedDiags := upgradeNodeV0toV1(ctx, obj, workerNodeConfigAttrTypes())
		diags.Append(upgradedDiags...)
		newElems = append(newElems, upgraded)
	}

	listVal, listDiags := types.ListValue(newElemType, newElems)
	diags.Append(listDiags...)
	return listVal, diags
}
