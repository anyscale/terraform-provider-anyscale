package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// defaultCloudIAMMappingTimeout bounds one Create/Update/Delete
// read-modify-write, including the bounded retry
// doCloudIAMMappingConfigRequestRetry performs. 2 minutes is real headroom
// for a handful of retried transient failures on a single-field PUT, without
// masking a genuinely stuck request behind a long default.
const defaultCloudIAMMappingTimeout = 2 * time.Minute

// anyscale_cloud_iam_mapping manages the ENTIRE dataplane IAM mapping of one
// cloud deployment: an authoritative singleton, not a set of independently
// addressable rules. See docs/decisions/cloud-iam-mapping/README.md for the
// full contract - this file implements it; it does not re-derive it.
//
// WHY A SEPARATE RESOURCE AND NOT A BLOCK ON anyscale_cloud_resource.
// anyscale_cloud_resource's config blocks (aws_config, gcp_config,
// kubernetes_config, ...) are deliberately not Read-refreshed, to avoid the
// C12 "provider produced inconsistent result after apply" regression. IAM
// mapping is exactly the kind of setting that gets changed out-of-band
// (console, CLI), so a surface that cannot show it drifted would be worse
// than no surface. This resource's Read is a real GET every time - that is
// the entire reason it exists as its own resource.
//
// WHY cloud_provider/compute_stack NEVER APPEAR HERE. The PUT requires both
// in the body and validates them, then throws them away -
// cloud_config_service.go's write only ever assigns DataplaneIamMapping and
// UserTagAnnotationPrefix. Exposing them as schema attributes would let a
// practitioner set a value that can never take effect. Every write below
// re-reads them fresh from the current config instead.
//
// THE ONE-FIELD BLAST RADIUS. The same write assigns UserTagAnnotationPrefix
// unconditionally from the request, so a PUT that omits it wipes an
// unrelated cloud setting. Every write path here reads the current value
// first and resends it unchanged - see writeCloudIAMMapping.
var (
	_ resource.Resource                   = &CloudIAMMappingResource{}
	_ resource.ResourceWithConfigure      = &CloudIAMMappingResource{}
	_ resource.ResourceWithImportState    = &CloudIAMMappingResource{}
	_ resource.ResourceWithValidateConfig = &CloudIAMMappingResource{}
)

// NewCloudIAMMappingResource creates a new cloud IAM mapping resource.
func NewCloudIAMMappingResource() resource.Resource {
	return &CloudIAMMappingResource{}
}

// CloudIAMMappingResource manages one cloud deployment's IAM mapping.
type CloudIAMMappingResource struct {
	client *Client
}

// CloudIAMMappingResourceModel maps the resource schema.
type CloudIAMMappingResourceModel struct {
	ID              types.String   `tfsdk:"id"`
	CloudID         types.String   `tfsdk:"cloud_id"`
	CloudResourceID types.String   `tfsdk:"cloud_resource_id"`
	Rules           types.List     `tfsdk:"rules"`
	FallbackRule    types.String   `tfsdk:"fallback_rule"`
	Mode            types.String   `tfsdk:"mode"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

func (r *CloudIAMMappingResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_iam_mapping"
}

func (r *CloudIAMMappingResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the **complete** dataplane IAM mapping of one Anyscale cloud deployment: " +
			"the ordered rules that route a launching workload to a cloud IAM identity based on its user, project, " +
			"or workload type, plus the fallback behavior when no rule matches. Public docs and the console call " +
			"this \"Cloud IAM mapping\"; the CLI reaches the same value through `anyscale cloud config get/update`.\n\n" +
			"This is an **authoritative singleton per cloud deployment** - the write path replaces the mapping " +
			"wholesale. Two instances of this resource targeting the same `cloud_id`/`cloud_resource_id` pair will " +
			"overwrite each other on every apply and produce a permanently non-empty plan; only ever declare one.\n\n" +
			"This resource owns nothing else about the deployment - `anyscale_cloud` and `anyscale_cloud_resource` " +
			"remain the owners of everything else, including the `cloud_provider` and `compute_stack` this " +
			"resource's writes must resend to the API but never exposes as configuration (required by the wire " +
			"format, validated, then discarded server-side).\n\n" +
			"Destroying this resource is a real revert: it clears the cloud's mapping rather than merely removing " +
			"it from state.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`<cloud_id>/<cloud_resource_id>`.",
			},
			"cloud_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The cloud this mapping applies to. Identity - changing it means a " +
					"different object, so any change replaces this resource. To reference a cloud by name instead " +
					"of by id, look it up with the [`anyscale_cloud` data source](../data-sources/cloud.md).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cloud_resource_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "The cloud deployment (what the API calls `cloud_deployment_id`; this " +
					"provider renamed the concept to \"cloud resource\" in v0.13.0) this mapping applies to. When " +
					"omitted, resolves to the cloud's primary deployment (`is_default`) and is written back to " +
					"state - omitting it never produces a diff against the resolved value. Identity, along with " +
					"`cloud_id` - changing it replaces this resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"rules": schema.ListNestedAttribute{
				Optional: true,
				MarkdownDescription: "The mapping's rules, evaluated in order - **the first matching rule wins, " +
					"so order is semantic configuration, not incidental**. Reordering two rules in configuration " +
					"produces a real plan diff, and that is intentional: it can change which IAM identity a " +
					"workload receives. An empty or omitted list clears the mapping (equivalent to destroying this " +
					"resource without removing it from configuration).",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"selector": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "A Kubernetes label-selector expression matched against the " +
								"launching workload, e.g. `workload-type=job`, `project=my-project`, or " +
								"`user=alice@example.com`. Only the keys `workload-type`, `project`, and `user` " +
								"are accepted; `workload-type` values are further restricted to `job`, `service`, " +
								"or `workspace`. Validated at plan time using the same selector grammar the API " +
								"enforces server-side.",
						},
						"value": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "The cloud IAM identity this rule assigns when its selector " +
								"matches. Meaning depends on the cloud: an AWS VM cloud expects an IAM **role " +
								"name** with an identically-named instance profile (not an ARN), a GCP VM cloud " +
								"expects a service account email, and a Kubernetes cloud (EKS/AKS/GKE) expects a " +
								"Kubernetes service account name.",
							Validators: []validator.String{
								stringvalidator.LengthAtLeast(1),
							},
						},
					},
				},
			},
			"fallback_rule": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "What to do when no rule in `rules` matches: `CLOUD_DEFAULT` falls back to " +
					"the cloud's default IAM role, `FAIL` rejects the workload launch. **Required when `rules` is " +
					"non-empty, and must be omitted when `rules` is empty** - the API silently ignores it in the " +
					"empty-rules case rather than erroring, which this provider instead rejects at plan time (an " +
					"absorbed value would hide from you that your setting does nothing).",
				Validators: []validator.String{
					stringvalidator.OneOf("CLOUD_DEFAULT", "FAIL"),
				},
			},
			"mode": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The mapping's server-derived mode. Currently always `CUSTOMER_MANAGED` " +
					"once any rule exists, and `null` when `rules` is empty - never settable, and reproduced here " +
					"only because a second mode value exists in the underlying data model even though this " +
					"endpoint cannot currently produce it.",
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
				CreateDescription: "Maximum time to wait for a create's write to finish, including every " +
					"retried transient failure (e.g. `30s`, `2m`). Defaults to `2m`.",
				UpdateDescription: "Maximum time to wait for an update's write to finish. Same default and rationale as `create`.",
				DeleteDescription: "Maximum time to wait for destroy's revert write to finish. Same default and rationale as `create`.",
			}),
		},
	}
}

func (r *CloudIAMMappingResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	r.client = client
}

func (r *CloudIAMMappingResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config CloudIAMMappingResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.Rules.IsUnknown() {
		return
	}

	rulesEmpty := config.Rules.IsNull() || len(config.Rules.Elements()) == 0

	if rulesEmpty {
		if !config.FallbackRule.IsNull() && !config.FallbackRule.IsUnknown() {
			resp.Diagnostics.AddAttributeError(
				path.Root("fallback_rule"),
				"fallback_rule requires a non-empty rules list",
				"The API silently ignores fallback_rule when rules is empty - it would be dropped server-side "+
					"and Read would return it unset, which surfaces as \"provider produced inconsistent result "+
					"after apply\". Remove fallback_rule, or add at least one rule.",
			)
		}
		return
	}

	if config.FallbackRule.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("fallback_rule"),
			"fallback_rule is required when rules is non-empty",
			"The API requires fallback_rule whenever rules is non-empty and rejects the write otherwise. Set "+
				"fallback_rule to \"CLOUD_DEFAULT\" or \"FAIL\".",
		)
	}

	var rules []CloudIAMMappingRuleModel
	resp.Diagnostics.Append(config.Rules.ElementsAs(ctx, &rules, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	for i, rule := range rules {
		if rule.Selector.IsNull() || rule.Selector.IsUnknown() {
			continue
		}
		if problem := validateIAMMappingSelector(rule.Selector.ValueString()); problem != "" {
			resp.Diagnostics.AddAttributeError(
				path.Root("rules").AtListIndex(i).AtName("selector"),
				"Invalid IAM mapping rule selector",
				problem,
			)
		}
	}
}

// Create resolves cloud_resource_id if unset, then performs the same
// read-modify-write PUT as Update - there is no distinct create semantic,
// the endpoint is an upsert.
func (r *CloudIAMMappingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan CloudIAMMappingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultCloudIAMMappingTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cloudID := plan.CloudID.ValueString()
	cloudResourceID := plan.CloudResourceID.ValueString()
	if plan.CloudResourceID.IsNull() || plan.CloudResourceID.IsUnknown() {
		resolved, err := resolvePrimaryCloudResourceID(ctx, r.client, cloudID)
		if err != nil {
			resp.Diagnostics.AddError("Unable to resolve primary cloud resource", err.Error())
			return
		}
		cloudResourceID = resolved
	}

	mode, diags := r.writeCloudIAMMapping(ctx, cloudID, cloudResourceID, plan.Rules, plan.FallbackRule)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = types.StringValue(cloudID + "/" + cloudResourceID)
	plan.CloudResourceID = types.StringValue(cloudResourceID)
	plan.Mode = mode

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read is a real GET on every refresh - the entire reason this is a
// separate resource rather than a block on anyscale_cloud_resource. rules
// and fallback_rule are non-Computed, so what Read puts in state is what
// the next plan diffs against config: an out-of-band change shows up as
// drift, exactly as intended.
func (r *CloudIAMMappingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state CloudIAMMappingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := state.CloudID.ValueString()
	cloudResourceID := state.CloudResourceID.ValueString()

	current, err := getCloudDeploymentConfig(ctx, r.client, cloudID, cloudResourceID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		AddAPIError(&resp.Diagnostics, "read cloud IAM mapping", err)
		return
	}

	rulesList, diags := dataplaneIAMMappingRulesToState(current.DataplaneIAMMapping.Rules)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Rules = rulesList
	state.FallbackRule = stringOrNull(current.DataplaneIAMMapping.FallbackRule)
	state.Mode = stringOrNull(current.DataplaneIAMMapping.Mode)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *CloudIAMMappingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan CloudIAMMappingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, defaultCloudIAMMappingTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cloudID := plan.CloudID.ValueString()
	cloudResourceID := plan.CloudResourceID.ValueString()

	mode, diags := r.writeCloudIAMMapping(ctx, cloudID, cloudResourceID, plan.Rules, plan.FallbackRule)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = types.StringValue(cloudID + "/" + cloudResourceID)
	plan.Mode = mode

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete is a real revert, not a state-only removal: a PUT whose spec omits
// rules nils the stored mapping (cloud_config_model.go only assigns
// DataplaneIamMapping inside the len(rules)>0 branch). The spec sent MUST be
// non-empty (cloud_provider/compute_stack populated) - an empty spec
// short-circuits to a no-op server-side (cloud_config_service.go:33-37) and
// would make this destroy silently do nothing while reporting success.
func (r *CloudIAMMappingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state CloudIAMMappingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := state.Timeouts.Delete(ctx, defaultCloudIAMMappingTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cloudID := state.CloudID.ValueString()
	cloudResourceID := state.CloudResourceID.ValueString()

	current, err := getCloudDeploymentConfig(ctx, r.client, cloudID, cloudResourceID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Already gone - nothing to revert.
			return
		}
		AddAPIError(&resp.Diagnostics, "read cloud deployment config before destroy", err)
		return
	}

	desired := CloudDeploymentConfigSpec{
		CloudProvider:           current.CloudProvider,
		ComputeStack:            current.ComputeStack,
		UserTagAnnotationPrefix: current.UserTagAnnotationPrefix,
		// DataplaneIAMMapping deliberately left zero-valued: rules omitted
		// nils the stored mapping. See the trap this function's doc comment
		// names - the spec above must still be non-empty as a whole.
	}

	if _, err := putCloudDeploymentConfig(ctx, r.client, cloudID, cloudResourceID, desired); err != nil {
		if errors.Is(err, ErrNotFound) {
			return
		}
		// A 500 here does not prove nothing happened - the PUT commits the
		// write before propagating to machine pools, and a downstream RPC
		// failure raises 500 after that commit. Report the failure plainly;
		// do not claim the mapping is still present.
		AddAPIError(&resp.Diagnostics, "clear cloud IAM mapping", err)
		return
	}
}

// ImportState accepts "<cloud_id>/<cloud_resource_id>" or a bare
// "<cloud_id>", resolving the primary deployment in the latter case (and
// whenever the literal legacy "default" alias is given, rather than sending
// that alias to the API - it is server-resolved with a deprecation warning
// this provider should never trigger deliberately). Resolution errors
// explicitly on zero or multiple primaries rather than guessing, matching
// the CLI's own behavior. The framework runs Read after this to populate
// rules/fallback_rule/mode from the live config.
func (r *CloudIAMMappingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			"Import anyscale_cloud_iam_mapping using '<cloud_id>/<cloud_resource_id>', or a bare '<cloud_id>' to "+
				"resolve the cloud's primary deployment, for example "+
				"'terraform import anyscale_cloud_iam_mapping.example cld_abc123/cldrsrc_def456'.",
		)
		return
	}

	cloudID := id
	cloudResourceID := ""
	if idx := strings.Index(id, "/"); idx >= 0 {
		cloudID = id[:idx]
		cloudResourceID = id[idx+1:]
		if cloudID == "" || cloudResourceID == "" {
			resp.Diagnostics.AddError(
				"Invalid Import ID",
				fmt.Sprintf("Expected '<cloud_id>/<cloud_resource_id>' or a bare '<cloud_id>', got %q.", id),
			)
			return
		}
	}

	if cloudResourceID == "" || cloudResourceID == "default" {
		resolved, err := resolvePrimaryCloudResourceID(ctx, r.client, cloudID)
		if err != nil {
			resp.Diagnostics.AddError("Unable to resolve primary cloud resource", err.Error())
			return
		}
		cloudResourceID = resolved
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), types.StringValue(cloudID+"/"+cloudResourceID))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cloud_id"), types.StringValue(cloudID))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cloud_resource_id"), types.StringValue(cloudResourceID))...)
}

// writeCloudIAMMapping performs the read-modify-write PUT shared by Create
// and Update: GET the current config to obtain cloud_provider/compute_stack
// (required-but-discarded) and user_tag_annotation_prefix (collateral - a
// PUT omitting it wipes it), splice in the desired mapping, and PUT.
//
// rules/fallbackRule are deliberately not echoed back into state by the
// caller from this function's return value - only mode is Computed. Core
// requires a non-Computed attribute's post-apply state to equal the plan
// exactly, so the caller must set state.Rules/FallbackRule from plan, never
// from the PUT response.
func (r *CloudIAMMappingResource) writeCloudIAMMapping(
	ctx context.Context, cloudID, cloudResourceID string, rules types.List, fallbackRule types.String,
) (types.String, diag.Diagnostics) {
	var diags diag.Diagnostics

	current, err := getCloudDeploymentConfig(ctx, r.client, cloudID, cloudResourceID)
	if err != nil {
		diags.AddError("Unable to read current cloud deployment config", err.Error())
		return types.StringNull(), diags
	}

	wireRules, d := dataplaneIAMMappingRulesFromState(ctx, rules)
	diags.Append(d...)
	if diags.HasError() {
		return types.StringNull(), diags
	}

	desired := CloudDeploymentConfigSpec{
		CloudProvider:           current.CloudProvider,
		ComputeStack:            current.ComputeStack,
		UserTagAnnotationPrefix: current.UserTagAnnotationPrefix,
		DataplaneIAMMapping: DataplaneIAMMappingWireSpec{
			Rules:        wireRules,
			FallbackRule: fallbackRule.ValueString(),
		},
	}

	updated, err := putCloudDeploymentConfig(ctx, r.client, cloudID, cloudResourceID, desired)
	if err != nil {
		diags.AddError("Unable to write cloud IAM mapping", err.Error())
		return types.StringNull(), diags
	}

	return stringOrNull(updated.DataplaneIAMMapping.Mode), diags
}
