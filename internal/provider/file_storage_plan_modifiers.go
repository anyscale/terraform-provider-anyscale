package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// fileStorageIDUnchanged reports whether file_storage.file_storage_id is the
// same in plan and state, with null equal to null. It is false when either side
// is unknown, or when the attribute cannot be read from either, because then
// there is no prior value that could still describe the planned file system.
func fileStorageIDUnchanged(ctx context.Context, plan tfsdk.Plan, state tfsdk.State) bool {
	idPath := path.Root("file_storage").AtName("file_storage_id")

	var planID, stateID types.String
	if diags := plan.GetAttribute(ctx, idPath, &planID); diags.HasError() {
		return false
	}
	if diags := state.GetAttribute(ctx, idPath, &stateID); diags.HasError() {
		return false
	}
	if planID.IsUnknown() || stateID.IsUnknown() {
		return false
	}
	// Both null is unchanged: a file_storage with no ID (a PVC or CSI volume) has nothing the
	// backend derives from it.
	return planID.Equal(stateID)
}

// carryFileStorageDerivedValue reports whether a backend-derived file_storage
// value (mount_targets, mount_path) may be carried forward from state.
//
// A derived value describes one file system, so it is carried only while
// file_storage_id is unchanged. Once the ID changes, carrying it would send the
// old file system's address alongside the new ID, and the backend stores the
// mismatched pair. On a change the slot stays unknown and Update resolves it
// from the live resource after the write.
func carryFileStorageDerivedValue(ctx context.Context, plan tfsdk.Plan, state tfsdk.State) bool {
	if state.Raw.IsNull() || plan.Raw.IsNull() {
		return false
	}
	return fileStorageIDUnchanged(ctx, plan, state)
}

// fileStorageDerivedModifierDescription is the description both modifiers report;
// the schema contract tests identify the modifiers by it.
const fileStorageDerivedModifierDescription = "Uses the prior state value while file_storage_id is unchanged; otherwise the value stays unknown until apply."

type fileStorageDerivedListModifier struct{}

func (fileStorageDerivedListModifier) Description(context.Context) string {
	return fileStorageDerivedModifierDescription
}

func (m fileStorageDerivedListModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (fileStorageDerivedListModifier) PlanModifyList(ctx context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	if !req.PlanValue.IsUnknown() || req.ConfigValue.IsUnknown() {
		return
	}
	if carryFileStorageDerivedValue(ctx, req.Plan, req.State) {
		resp.PlanValue = req.StateValue
	}
}

type fileStorageDerivedStringModifier struct{}

func (fileStorageDerivedStringModifier) Description(context.Context) string {
	return fileStorageDerivedModifierDescription
}

func (m fileStorageDerivedStringModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (fileStorageDerivedStringModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.PlanValue.IsUnknown() || req.ConfigValue.IsUnknown() {
		return
	}
	if carryFileStorageDerivedValue(ctx, req.Plan, req.State) {
		resp.PlanValue = req.StateValue
	}
}
