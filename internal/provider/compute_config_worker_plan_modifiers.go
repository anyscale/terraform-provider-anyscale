package provider

import (
	"context"
	"strconv"
	"strings"

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
// The same happens one level up: removing an additional_resources entry shifts
// every later entry, and each node inside it would inherit the removed entry's
// values.
//
// These modifiers reuse the prior value only when the prior element at the same
// index is the same node: the same additional_resources entry (same
// cloud_resource), and for a worker group the same configured name, or, for an
// unnamed group, the same instance_type with a prior name that was defaulted
// from it. Otherwise the value stays unknown and resolves from the API response.

// nodeResourcesUseStateForSameNode is UseStateForUnknown for a node's resources
// map, limited to the same node (see above). worker selects the worker-group
// check in addition to the additional_resources entry check.
func nodeResourcesUseStateForSameNode(worker bool) planmodifier.Map {
	return nodeResourcesModifier{worker: worker}
}

type nodeResourcesModifier struct{ worker bool }

func (m nodeResourcesModifier) Description(_ context.Context) string {
	return "Keeps the prior resources value when the node at this position is unchanged."
}

func (m nodeResourcesModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m nodeResourcesModifier) PlanModifyMap(ctx context.Context, req planmodifier.MapRequest, resp *planmodifier.MapResponse) {
	if req.StateValue.IsUnknown() || !req.PlanValue.IsUnknown() || req.ConfigValue.IsUnknown() {
		return
	}
	if !samePriorEntry(ctx, req.Path, req.Config, req.State) {
		return
	}
	if m.worker && !samePriorWorker(ctx, req.Path, req.Config, req.State) {
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
	if !samePriorEntry(ctx, req.Path, req.Config, req.State) || !samePriorWorker(ctx, req.Path, req.Config, req.State) {
		return
	}
	resp.PlanValue = req.StateValue
}

// samePriorEntry reports whether attrPath, if it is inside an
// additional_resources entry, belongs to the same entry in prior state and
// config (same cloud_resource). Paths outside additional_resources are always
// the same entry.
func samePriorEntry(ctx context.Context, attrPath path.Path, config tfsdk.Config, state tfsdk.State) bool {
	steps := attrPath.Steps()
	if len(steps) < 2 || steps[0] != path.PathStepAttributeName("additional_resources") {
		return true
	}
	index, ok := steps[1].(path.PathStepElementKeyInt)
	if !ok || state.Raw.IsNull() {
		return false
	}
	entry := path.Root("additional_resources").AtListIndex(int(index))
	var priorResource, configResource types.String
	if diags := state.GetAttribute(ctx, entry.AtName("cloud_resource"), &priorResource); diags.HasError() {
		return false
	}
	if diags := config.GetAttribute(ctx, entry.AtName("cloud_resource"), &configResource); diags.HasError() {
		return false
	}
	return !configResource.IsNull() && !configResource.IsUnknown() && configResource.Equal(priorResource)
}

// samePriorWorker reports whether the prior-state element owning attrPath is
// the same worker group as the configured element at that path. A configured
// name must match the prior name. An unnamed group must match on instance_type,
// and the prior name must be one defaulted from it ("<instance_type>" or
// "<instance_type>-<n>"), not a name another group was configured with. Any
// value that cannot be read counts as "not the same", which leaves the
// attribute unknown - the safe outcome.
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
	if configInstanceType.IsUnknown() || priorInstanceType.IsNull() || !configInstanceType.Equal(priorInstanceType) {
		return false
	}
	return isDefaultedWorkerName(priorName.ValueString(), priorInstanceType.ValueString())
}

// isDefaultedWorkerName reports whether name is what an unnamed worker group of
// instanceType is given: the instance type itself, or, when that collides, the
// instance type with a numeric suffix (disambiguateDefaultedWorkerNames).
func isDefaultedWorkerName(name, instanceType string) bool {
	if name == instanceType {
		return true
	}
	suffix, ok := strings.CutPrefix(name, instanceType+"-")
	if !ok || suffix == "" {
		return false
	}
	_, err := strconv.Atoi(suffix)
	return err == nil
}
