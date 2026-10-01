package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The framework pairs a list element's nested attributes with prior state by
// index. When a worker group is removed from or inserted into worker_nodes,
// every later group shifts to a new index, so a plain UseStateForUnknown on a
// worker's Computed attribute copies the value of whichever group used to sit
// at that index - and Create/Update then send it as if configured.
//
// These modifiers reuse the prior value only when the prior element at the same
// index is the same worker group: the same configured name, or, for an unnamed
// group, the same instance_type. Otherwise the value stays unknown and resolves
// from the API response.

// workerResourcesUseStateForSameWorker is UseStateForUnknown for a worker's
// resources map, limited to the same worker group (see above).
func workerResourcesUseStateForSameWorker() planmodifier.Map {
	return workerResourcesModifier{}
}

type workerResourcesModifier struct{}

func (m workerResourcesModifier) Description(_ context.Context) string {
	return "Keeps the prior resources value when the worker group at this position is unchanged."
}

func (m workerResourcesModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m workerResourcesModifier) PlanModifyMap(ctx context.Context, req planmodifier.MapRequest, resp *planmodifier.MapResponse) {
	if req.StateValue.IsUnknown() || !req.PlanValue.IsUnknown() || req.ConfigValue.IsUnknown() {
		return
	}
	if !samePriorWorker(ctx, req.Path, req.Config, req.State) {
		return
	}
	resp.PlanValue = req.StateValue
}

// workerNameUseStateForSameWorker is UseNonNullStateForUnknown for a worker's
// name, limited to the same worker group (see above). A brand-new element has
// no prior value at its index, so it stays unknown, as with
// UseNonNullStateForUnknown.
func workerNameUseStateForSameWorker() planmodifier.String {
	return workerNameModifier{}
}

type workerNameModifier struct{}

func (m workerNameModifier) Description(_ context.Context) string {
	return "Keeps the prior name when the worker group at this position is unchanged."
}

func (m workerNameModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m workerNameModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() || !req.PlanValue.IsUnknown() || req.ConfigValue.IsUnknown() {
		return
	}
	if !samePriorWorker(ctx, req.Path, req.Config, req.State) {
		return
	}
	resp.PlanValue = req.StateValue
}

// samePriorWorker reports whether the prior-state element owning attrPath is
// the same worker group as the configured element at that path. A configured
// name must match the prior name; an unnamed group must match on
// instance_type. Any value that cannot be read counts as "not the same", which
// leaves the attribute unknown - the safe outcome.
func samePriorWorker(ctx context.Context, attrPath path.Path, config tfsdk.Config, state tfsdk.State) bool {
	if state.Raw.IsNull() {
		return false
	}
	element := attrPath.ParentPath()

	var priorName, priorInstanceType, configName, configInstanceType types.String
	if diags := state.GetAttribute(ctx, element.AtName("name"), &priorName); diags.HasError() {
		return false
	}
	if diags := state.GetAttribute(ctx, element.AtName("instance_type"), &priorInstanceType); diags.HasError() {
		return false
	}
	if diags := config.GetAttribute(ctx, element.AtName("name"), &configName); diags.HasError() {
		return false
	}
	if diags := config.GetAttribute(ctx, element.AtName("instance_type"), &configInstanceType); diags.HasError() {
		return false
	}

	if !configName.IsNull() {
		return !configName.IsUnknown() && configName.Equal(priorName)
	}
	return !configInstanceType.IsUnknown() && !priorInstanceType.IsNull() && configInstanceType.Equal(priorInstanceType)
}
