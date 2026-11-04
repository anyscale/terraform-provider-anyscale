package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The primary-replace guard refuses, at plan time, a change that would replace a cloud's
// primary anyscale_cloud_resource. Destroying a primary resource tears down the cloud's
// service load balancers and marks the cloud DELETING, which cannot be undone, so replacing
// one would start deleting the cloud. Anyscale cannot change these fields on a primary
// resource without a new cloud.
//
// It wraps each replace-forcing plan modifier instead of checking in ModifyPlan, because
// resource-level ModifyPlan never sees which attributes require replacement: the framework
// starts its RequiresReplace empty and merges the attribute modifiers' paths afterwards. The
// wrapper runs the inner modifier unchanged and only adds the error, so the replace decision
// (including the semantic-equivalence modifiers) stays in one place. Descriptions pass through
// unchanged, so schema contract tests that identify a modifier by its description still work.

// primaryReplaceGuarded reports whether a replacement of this resource must be refused: an
// update (prior state and plan both present) of a resource whose prior state says it is its
// cloud's primary, and whose cloud is not itself being replaced (cloud_id known in the plan).
func primaryReplaceGuarded(ctx context.Context, state tfsdk.State, plan tfsdk.Plan) bool {
	if state.Raw.IsNull() || plan.Raw.IsNull() {
		return false
	}
	var isDefault types.Bool
	if diags := state.GetAttribute(ctx, path.Root("is_default"), &isDefault); diags.HasError() {
		return false
	}
	if !isDefault.ValueBool() {
		return false
	}
	var cloudID types.String
	if diags := plan.GetAttribute(ctx, path.Root("cloud_id"), &cloudID); diags.HasError() {
		return false
	}
	return !cloudID.IsUnknown()
}

// replaceIsReal filters out the replacement a RequiresReplace modifier reports for an
// Optional+Computed attribute the config leaves unset: during any update its plan value is
// still unknown when RequiresReplace runs, and a later UseStateForUnknown restores the prior
// value, so Terraform drops that replacement (the planned value equals the prior one). Without
// this, every update of a primary resource would be refused. An unknown that comes from the
// config (a value known only after apply) is still treated as a real replacement.
func replaceIsReal(configNull, planUnknown bool) bool {
	return !configNull || !planUnknown
}

func addPrimaryReplaceError(diags *diag.Diagnostics, p path.Path) {
	diags.AddAttributeError(p, "Cannot Replace a Primary Cloud Resource", fmt.Sprintf(
		"%s cannot be changed on a cloud's primary resource without replacing the cloud. "+
			"Replacing a primary resource would mark its cloud DELETING, which cannot be undone. "+
			"Replace the cloud instead (for example, terraform apply -replace on the anyscale_cloud), "+
			"or revert this change.", p))
}

type primaryReplaceGuardString struct{ inner planmodifier.String }

func guardPrimaryString(inner planmodifier.String) planmodifier.String {
	return primaryReplaceGuardString{inner: inner}
}

func (m primaryReplaceGuardString) Description(ctx context.Context) string {
	return m.inner.Description(ctx)
}

func (m primaryReplaceGuardString) MarkdownDescription(ctx context.Context) string {
	return m.inner.MarkdownDescription(ctx)
}

func (m primaryReplaceGuardString) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	m.inner.PlanModifyString(ctx, req, resp)
	if resp.RequiresReplace && replaceIsReal(req.ConfigValue.IsNull(), resp.PlanValue.IsUnknown()) &&
		primaryReplaceGuarded(ctx, req.State, req.Plan) {
		addPrimaryReplaceError(&resp.Diagnostics, req.Path)
	}
}

type primaryReplaceGuardBool struct{ inner planmodifier.Bool }

func guardPrimaryBool(inner planmodifier.Bool) planmodifier.Bool {
	return primaryReplaceGuardBool{inner: inner}
}

func (m primaryReplaceGuardBool) Description(ctx context.Context) string {
	return m.inner.Description(ctx)
}

func (m primaryReplaceGuardBool) MarkdownDescription(ctx context.Context) string {
	return m.inner.MarkdownDescription(ctx)
}

func (m primaryReplaceGuardBool) PlanModifyBool(ctx context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	m.inner.PlanModifyBool(ctx, req, resp)
	if resp.RequiresReplace && replaceIsReal(req.ConfigValue.IsNull(), resp.PlanValue.IsUnknown()) &&
		primaryReplaceGuarded(ctx, req.State, req.Plan) {
		addPrimaryReplaceError(&resp.Diagnostics, req.Path)
	}
}

type primaryReplaceGuardList struct{ inner planmodifier.List }

func guardPrimaryList(inner planmodifier.List) planmodifier.List {
	return primaryReplaceGuardList{inner: inner}
}

func (m primaryReplaceGuardList) Description(ctx context.Context) string {
	return m.inner.Description(ctx)
}

func (m primaryReplaceGuardList) MarkdownDescription(ctx context.Context) string {
	return m.inner.MarkdownDescription(ctx)
}

func (m primaryReplaceGuardList) PlanModifyList(ctx context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	m.inner.PlanModifyList(ctx, req, resp)
	if resp.RequiresReplace && replaceIsReal(req.ConfigValue.IsNull(), resp.PlanValue.IsUnknown()) &&
		primaryReplaceGuarded(ctx, req.State, req.Plan) {
		addPrimaryReplaceError(&resp.Diagnostics, req.Path)
	}
}

type primaryReplaceGuardMap struct{ inner planmodifier.Map }

func guardPrimaryMap(inner planmodifier.Map) planmodifier.Map {
	return primaryReplaceGuardMap{inner: inner}
}

func (m primaryReplaceGuardMap) Description(ctx context.Context) string {
	return m.inner.Description(ctx)
}

func (m primaryReplaceGuardMap) MarkdownDescription(ctx context.Context) string {
	return m.inner.MarkdownDescription(ctx)
}

func (m primaryReplaceGuardMap) PlanModifyMap(ctx context.Context, req planmodifier.MapRequest, resp *planmodifier.MapResponse) {
	m.inner.PlanModifyMap(ctx, req, resp)
	if resp.RequiresReplace && replaceIsReal(req.ConfigValue.IsNull(), resp.PlanValue.IsUnknown()) &&
		primaryReplaceGuarded(ctx, req.State, req.Plan) {
		addPrimaryReplaceError(&resp.Diagnostics, req.Path)
	}
}
