package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &SchedulerConfigResource{}
	_ resource.ResourceWithConfigure   = &SchedulerConfigResource{}
	_ resource.ResourceWithImportState = &SchedulerConfigResource{}
	_ resource.ResourceWithModifyPlan  = &SchedulerConfigResource{}
)

// NewSchedulerConfigResource creates the Anyscale Scheduler config resource.
func NewSchedulerConfigResource() resource.Resource {
	return &SchedulerConfigResource{}
}

// SchedulerConfigResource manages the organization's scheduler config document.
type SchedulerConfigResource struct {
	client *Client
}

// ---------------------------------------------------------------------------
// Terraform models
//
// These mirror the wire structs in scheduler_api.go one-for-one. They are kept
// separate rather than reusing those structs because the two have different
// jobs: the wire structs distinguish "unset" from "explicit zero" with
// pointers and omitempty, while these carry Terraform's null/unknown semantics.
// Collapsing them would make it impossible to express an explicit quota of 0,
// which the backend reads as "block this resource entirely" - the opposite of
// the unset meaning, "unlimited".
// ---------------------------------------------------------------------------

type schedulerMatchExpressionModel struct {
	Key      types.String   `tfsdk:"key"`
	Operator types.String   `tfsdk:"operator"`
	Values   []types.String `tfsdk:"values"`
}

type schedulerResourceFlavorModel struct {
	Name                   types.String                    `tfsdk:"name"`
	Selector               []schedulerMatchExpressionModel `tfsdk:"selector"`
	AdvancedInstanceConfig jsontypes.Normalized            `tfsdk:"advanced_instance_config"`
}

type schedulerResourceQuotaSpecModel struct {
	Name           types.String  `tfsdk:"name"`
	NominalQuota   types.Float64 `tfsdk:"nominal_quota"`
	LendingLimit   types.Float64 `tfsdk:"lending_limit"`
	BorrowingLimit types.Float64 `tfsdk:"borrowing_limit"`
}

type schedulerFlavorQuotaModel struct {
	Name      types.String                      `tfsdk:"name"`
	Resources []schedulerResourceQuotaSpecModel `tfsdk:"resources"`
}

type schedulerResourceGroupModel struct {
	CoveredResources []types.String              `tfsdk:"covered_resources"`
	Flavors          []schedulerFlavorQuotaModel `tfsdk:"flavors"`
}

type schedulerPreemptionModel struct {
	ReclaimWithinCohort types.String `tfsdk:"reclaim_within_cohort"`
	BorrowWithinCohort  types.String `tfsdk:"borrow_within_cohort"`
	WithinResourceQueue types.String `tfsdk:"within_resource_queue"`
}

type schedulerResourceQueueModel struct {
	Name           types.String                  `tfsdk:"name"`
	CohortName     types.String                  `tfsdk:"cohort_name"`
	Preemption     *schedulerPreemptionModel     `tfsdk:"preemption"`
	ResourceGroups []schedulerResourceGroupModel `tfsdk:"resource_groups"`
}

type schedulerPriorityPolicyModel struct {
	Default     types.Int64  `tfsdk:"default"`
	Min         types.Int64  `tfsdk:"min"`
	Max         types.Int64  `tfsdk:"max"`
	OnViolation types.String `tfsdk:"on_violation"`
}

type schedulerSchedulingRuleModel struct {
	Selector       []schedulerMatchExpressionModel `tfsdk:"selector"`
	ResourceQueue  types.String                    `tfsdk:"resource_queue"`
	PriorityPolicy *schedulerPriorityPolicyModel   `tfsdk:"priority_policy"`
}

type schedulerRecyclePolicyModel struct {
	RotationInterval types.String `tfsdk:"rotation_interval"`
	MaxWorkloads     types.Int64  `tfsdk:"max_workloads"`
	MaxIdleDuration  types.String `tfsdk:"max_idle_duration"`
}

// SchedulerConfigResourceModel is the whole document plus the API's metadata
// about the version this state represents.
type SchedulerConfigResourceModel struct {
	ResourceFlavors []schedulerResourceFlavorModel `tfsdk:"resource_flavors"`
	ResourceQueues  []schedulerResourceQueueModel  `tfsdk:"resource_queues"`
	SchedulingRules []schedulerSchedulingRuleModel `tfsdk:"scheduling_rules"`
	RecyclePolicy   *schedulerRecyclePolicyModel   `tfsdk:"recycle_policy"`

	Version   types.Int64  `tfsdk:"version"`
	CreatedAt types.String `tfsdk:"created_at"`
	CreatorID types.String `tfsdk:"creator_id"`
}

func (r *SchedulerConfigResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_scheduler_config"
}

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

func schedulerMatchExpressionAttributes(what string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"key": schema.StringAttribute{
			Required:            true,
			MarkdownDescription: "Label key to match on " + what + ".",
		},
		"operator": schema.StringAttribute{
			Required:            true,
			Validators:          []validator.String{stringvalidator.OneOf(schedulerOperators...)},
			MarkdownDescription: "How `key` is compared: `in` and `not_in` test membership in `values`; `exists` and `does_not_exist` test only for the key's presence and take no `values`.",
		},
		"values": schema.ListAttribute{
			Optional:            true,
			ElementType:         types.StringType,
			MarkdownDescription: "Values compared against `key`. Required for `in` and `not_in`; must be omitted for `exists` and `does_not_exist`.",
		},
	}
}

// nonEmptyListValidator rejects a section that is declared but empty, and
// names the fix a practitioner actually wants: omit the section entirely.
//
// The reason to reject the empty list is that it is INDISTINGUISHABLE from
// omission, not that it differs. All three sections are Go slices tagged
// omitempty (see SchedulerConfig in scheduler_api.go), and encoding/json drops
// a zero-length slice, so `section = []` and no section at all serialize to the
// same bytes. Accepting the empty form would therefore mean silently treating
// "I declared this section" as "leave this section unset". Two consequences,
// both inviting wrong next moves: the guard is not redundant (there is no empty
// array on the wire to be harmless), and no wire-level assertion can test it
// (byte-identical documents, so such a test passes against any build). The
// behavior exists only as a plan-time diagnostic, which is where it is tested.
type nonEmptyListValidator struct {
	attrName string
}

func (v nonEmptyListValidator) Description(ctx context.Context) string {
	return v.MarkdownDescription(ctx)
}

func (v nonEmptyListValidator) MarkdownDescription(_ context.Context) string {
	return fmt.Sprintf("if `%s` is declared it must contain at least one element; omit it entirely to leave it unset", v.attrName)
}

func (v nonEmptyListValidator) ValidateList(_ context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if len(req.ConfigValue.Elements()) > 0 {
		return
	}
	resp.Diagnostics.AddAttributeError(
		req.Path,
		fmt.Sprintf("Empty %s Section", v.attrName),
		fmt.Sprintf(
			"`%s` was declared as an empty list. Omit the `%s` attribute entirely to leave the section unset - "+
				"an empty list is sent to the Anyscale API as no section at all, so declaring one would quietly "+
				"mean the opposite of what it looks like.",
			v.attrName, v.attrName,
		),
	)
}

func (r *SchedulerConfigResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: 0,
		MarkdownDescription: "Manages the [Anyscale Scheduler](https://docs.anyscale.com/scheduler) configuration for your organization: the resource flavors, resource queues, scheduling rules, and recycle policy that govern how workloads are admitted and where they run.\n\n" +
			"~> **Beta.** The Anyscale Scheduler is a beta product and its configuration fields may change. Applying a scheduler config changes how *every* workload in the organization is admitted - a scheduling rule that matches no queue, or a `nominal_quota` of `0`, can prevent workloads from starting. Review [the product documentation](https://docs.anyscale.com/scheduler) before your first apply.\n\n" +
			"~> **Singleton, authoritative-write.** An organization has at most one active scheduler config, and this resource owns the whole document - there is no per-cloud variant and no user-chosen key. Two Terraform configurations that both declare `anyscale_scheduler_config` for the same organization will silently fight, each apply reverting the other. Declare it in exactly one place. Changes made outside Terraform (CLI or console) appear as drift and are reverted by the next apply.\n\n" +
			"~> **`terraform destroy` does not clear the configuration.** The Anyscale API has no delete operation for scheduler configs, so destroying this resource removes it from Terraform state while the organization's config stays active. See the warning emitted at destroy time.\n\n" +
			"Requires the Anyscale Scheduler to be enabled for your organization; if it is disabled, the provider translates the raw permission error into an actionable message rather than passing it through. `terraform plan` completes with a warning rather than failing outright - naming validation as skipped for a config not yet in state, or leaving prior state untouched and unrefreshed for one already applied. `terraform apply` still fails, either on the disabled scheduler itself or on a genuinely invalid document, so nothing applies silently.\n\n" +
			"-> **A note on naming.** You may also see this called the **Global Resource Scheduler (GRS)** - the Anyscale CLI help text and some API error messages still use that name. They are the same product.",
		Attributes: map[string]schema.Attribute{
			"version": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Version number of the scheduler config this state represents. Config documents are immutable and monotonically versioned: every apply mints a new version rather than editing the previous one.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When this config version was applied, RFC 3339.",
			},
			"creator_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the user that applied this config version. May be `null` for versions applied by an automated process that the API does not attribute to a user.",
			},
			"resource_flavors": schema.ListNestedAttribute{
				Optional:            true,
				Validators:          []validator.List{nonEmptyListValidator{attrName: "resource_flavors"}},
				MarkdownDescription: "Named hardware profiles that queues allocate quota against. Order is significant: when a workload can run on more than one flavor, flavors are tried in the order written here. If declared, it must contain at least one element - omit the attribute entirely, not `[]`, to leave the section unset.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Name of the flavor, referenced by `resource_queues[].resource_groups[].flavors[].name`.",
						},
						"selector": schema.ListNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Node label expressions selecting the machines this flavor covers. All expressions must match (AND).",
							NestedObject:        schema.NestedAttributeObject{Attributes: schedulerMatchExpressionAttributes("the node")},
						},
						"advanced_instance_config": schema.StringAttribute{
							Optional:   true,
							CustomType: jsontypes.NormalizedType{},
							MarkdownDescription: "Cloud-provider-specific instance configuration applied to machines in this flavor, as a JSON string. Use `jsonencode()` for HCL objects. " +
								"This can't be a native/dynamic value: it lives inside a list, and Terraform doesn't support a dynamic type nested inside a list - the same constraint that shapes `advanced_instance_config` on `anyscale_compute_config`. " +
								"Compared semantically rather than by text, so key order and whitespace never produce a diff - `{\"a\":1,\"b\":2}` and `{ \"b\":2, \"a\":1 }` are the same value. Numbers are compared as written, so `1` and `1.0` are *not* the same value; `jsonencode()` avoids the question entirely. " +
								"A mismatch here never resolves itself - the same plan reappears on every apply, and because there is no delete route and no content dedupe, each apply mints another permanent config version.",
						},
					},
				},
			},
			"resource_queues": schema.ListNestedAttribute{
				Optional:            true,
				Validators:          []validator.List{nonEmptyListValidator{attrName: "resource_queues"}},
				MarkdownDescription: "Queues that workloads are admitted into, each carrying its own quota and preemption policy. If declared, it must contain at least one element - omit the attribute entirely, not `[]`, to leave the section unset.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Name of the queue, referenced by `scheduling_rules[].resource_queue`.",
						},
						"cohort_name": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Cohort this queue belongs to. Queues in the same cohort can lend and borrow unused quota from one another; a queue with no cohort never shares quota.",
						},
						"preemption": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "When workloads in this queue may be preempted to make room for others.",
							Attributes: map[string]schema.Attribute{
								"reclaim_within_cohort": schema.StringAttribute{
									Optional:            true,
									Validators:          []validator.String{stringvalidator.OneOf(schedulerReclaimPolicies...)},
									MarkdownDescription: "Whether this queue may preempt workloads in other queues of its cohort to reclaim quota it lent out: `never`, `lower_priority`, or `any`.",
								},
								"borrow_within_cohort": schema.StringAttribute{
									Optional:            true,
									Validators:          []validator.String{stringvalidator.OneOf(schedulerBorrowPolicies...)},
									MarkdownDescription: "Whether this queue may preempt workloads in other queues of its cohort in order to borrow quota beyond its own: `never` or `lower_priority`.",
								},
								"within_resource_queue": schema.StringAttribute{
									Optional:            true,
									Validators:          []validator.String{stringvalidator.OneOf(schedulerWithinQueuePolic...)},
									MarkdownDescription: "Whether a workload in this queue may preempt another workload in the same queue: `never` or `lower_priority`.",
								},
							},
						},
						"resource_groups": schema.ListNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Quota for this queue, grouped by the resource types each group governs.",
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"covered_resources": schema.ListAttribute{
										Required:            true,
										ElementType:         types.StringType,
										MarkdownDescription: "Resource types this group governs, lowercase (`cpu`, `gpu`, `memory_gb`, `tpu` at the time of writing; accelerator names are added over time, so this list is not fixed here). An unsupported value is reported at plan time by the Anyscale API, which returns the authoritative list.",
									},
									"flavors": schema.ListNestedAttribute{
										Required:            true,
										MarkdownDescription: "Per-flavor quota within this group. Order is significant - flavors are tried in the order written.",
										NestedObject: schema.NestedAttributeObject{
											Attributes: map[string]schema.Attribute{
												"name": schema.StringAttribute{
													Required:            true,
													MarkdownDescription: "Name of a flavor declared in `resource_flavors`.",
												},
												"resources": schema.ListNestedAttribute{
													Optional:            true,
													MarkdownDescription: "Quota for each covered resource type on this flavor.",
													NestedObject: schema.NestedAttributeObject{
														Attributes: map[string]schema.Attribute{
															"name": schema.StringAttribute{
																Required:            true,
																MarkdownDescription: "Resource type this quota applies to. Must be one of the enclosing group's `covered_resources`.",
															},
															"nominal_quota": schema.Float64Attribute{
																Optional:            true,
																MarkdownDescription: "Quota guaranteed to this queue for this resource. Omit for unlimited. **`0` is not the same as omitting it**: an explicit `0` blocks this resource entirely for this queue.",
															},
															"lending_limit": schema.Float64Attribute{
																Optional:            true,
																MarkdownDescription: "Most of `nominal_quota` this queue will lend to other queues in its cohort. Omit for no limit; `0` lends nothing.",
															},
															"borrowing_limit": schema.Float64Attribute{
																Optional:            true,
																MarkdownDescription: "Most this queue may borrow from its cohort beyond `nominal_quota`. Omit for no limit; `0` borrows nothing.",
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			"scheduling_rules": schema.ListNestedAttribute{
				Optional:            true,
				Validators:          []validator.List{nonEmptyListValidator{attrName: "scheduling_rules"}},
				MarkdownDescription: "Rules mapping workloads to queues. **First match wins, top to bottom**, so order is significant. Once any rule exists, a workload matching no rule is rejected rather than run unscheduled - keep a catch-all rule last unless that is what you intend. If declared, it must contain at least one element - omit the attribute entirely, not `[]`, to leave the section unset.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"resource_queue": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Name of the queue matching workloads are admitted into. Must name a queue declared in `resource_queues`.",
						},
						"selector": schema.ListNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Workload label expressions this rule matches on. All expressions must match (AND). Omit for a catch-all rule.",
							NestedObject:        schema.NestedAttributeObject{Attributes: schedulerMatchExpressionAttributes("the workload")},
						},
						"priority_policy": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Priority bounds applied to workloads matched by this rule.",
							Attributes: map[string]schema.Attribute{
								"default": schema.Int64Attribute{
									Optional:            true,
									MarkdownDescription: "Priority assigned to a matching workload that requests none.",
								},
								"min": schema.Int64Attribute{
									Optional:            true,
									MarkdownDescription: "Lowest priority a matching workload may request.",
								},
								"max": schema.Int64Attribute{
									Optional:            true,
									MarkdownDescription: "Highest priority a matching workload may request.",
								},
								"on_violation": schema.StringAttribute{
									Optional:            true,
									Validators:          []validator.String{stringvalidator.OneOf(schedulerOnViolationActs...)},
									MarkdownDescription: "What to do with a workload requesting a priority outside `min`/`max`: `reject` it, or `force_update` its priority to the nearest bound.",
								},
							},
						},
					},
				},
			},
			"recycle_policy": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "When the scheduler retires and replaces the machines it manages. Stored and returned by the API, but no Anyscale component acts on it yet, so setting it changes no scheduling behavior today. Omit the attribute entirely, not `{}`, to leave the section unset: unlike the list sections, where `[]` is dropped from the request, an empty object here is sent to the API as an empty policy.",
				Attributes: map[string]schema.Attribute{
					"rotation_interval": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "How long a machine may serve before being rotated out, as a duration string (for example `24h`).",
					},
					"max_workloads": schema.Int64Attribute{
						Optional:            true,
						MarkdownDescription: "How many workloads a machine may run before being rotated out.",
					},
					"max_idle_duration": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "How long a machine may sit idle before being reclaimed, as a duration string (for example `10m`).",
					},
				},
			},
		},
	}
}

func (r *SchedulerConfigResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ---------------------------------------------------------------------------
// Model <-> wire conversion
// ---------------------------------------------------------------------------

func stringPtrOrNil(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

func float64PtrOrNil(v types.Float64) *float64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	f := v.ValueFloat64()
	return &f
}

func int64PtrOrNil(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	i := v.ValueInt64()
	return &i
}

func expandStringSlice(values []types.String) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, v.ValueString())
	}
	return out
}

func flattenStringSlice(values []string) []types.String {
	if values == nil {
		return nil
	}
	out := make([]types.String, 0, len(values))
	for _, v := range values {
		out = append(out, types.StringValue(v))
	}
	return out
}

func expandSchedulerSelector(in []schedulerMatchExpressionModel) []SchedulerMatchExpression {
	if in == nil {
		return nil
	}
	out := make([]SchedulerMatchExpression, 0, len(in))
	for _, e := range in {
		out = append(out, SchedulerMatchExpression{
			Key:      e.Key.ValueString(),
			Operator: e.Operator.ValueString(),
			Values:   expandStringSlice(e.Values),
		})
	}
	return out
}

func flattenSchedulerSelector(in []SchedulerMatchExpression) []schedulerMatchExpressionModel {
	if in == nil {
		return nil
	}
	out := make([]schedulerMatchExpressionModel, 0, len(in))
	for _, e := range in {
		out = append(out, schedulerMatchExpressionModel{
			Key:      types.StringValue(e.Key),
			Operator: types.StringValue(e.Operator),
			Values:   flattenStringSlice(e.Values),
		})
	}
	return out
}

// expandSchedulerConfig converts the Terraform model into the wire document.
// Every optional scalar goes through a *PtrOrNil helper so a field the
// practitioner never wrote is omitted rather than sent as a zero - for the
// quota fields that distinction is the difference between "unlimited" and
// "blocked".
func expandSchedulerConfig(model *SchedulerConfigResourceModel, diags *[]string) SchedulerConfig {
	cfg := SchedulerConfig{}

	for _, f := range model.ResourceFlavors {
		flavor := SchedulerResourceFlavor{
			Name:     f.Name.ValueString(),
			Selector: expandSchedulerSelector(f.Selector),
		}
		if !f.AdvancedInstanceConfig.IsNull() && !f.AdvancedInstanceConfig.IsUnknown() {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(f.AdvancedInstanceConfig.ValueString()), &parsed); err != nil {
				*diags = append(*diags, fmt.Sprintf("resource_flavors[%q].advanced_instance_config is not a valid JSON object: %s", flavor.Name, err))
			} else {
				flavor.AdvancedInstanceConfig = parsed
			}
		}
		cfg.ResourceFlavors = append(cfg.ResourceFlavors, flavor)
	}

	for _, q := range model.ResourceQueues {
		queue := SchedulerResourceQueue{
			Name:       q.Name.ValueString(),
			CohortName: stringPtrOrNil(q.CohortName),
		}
		if q.Preemption != nil {
			queue.Preemption = &SchedulerPreemptionPolicy{
				ReclaimWithinCohort: stringPtrOrNil(q.Preemption.ReclaimWithinCohort),
				BorrowWithinCohort:  stringPtrOrNil(q.Preemption.BorrowWithinCohort),
				WithinResourceQueue: stringPtrOrNil(q.Preemption.WithinResourceQueue),
			}
		}
		for _, g := range q.ResourceGroups {
			group := SchedulerResourceGroup{
				CoveredResources: expandStringSlice(g.CoveredResources),
			}
			for _, fl := range g.Flavors {
				fq := SchedulerFlavorQuota{Name: fl.Name.ValueString()}
				for _, res := range fl.Resources {
					fq.Resources = append(fq.Resources, SchedulerResourceQuotaSpec{
						Name:           res.Name.ValueString(),
						NominalQuota:   float64PtrOrNil(res.NominalQuota),
						LendingLimit:   float64PtrOrNil(res.LendingLimit),
						BorrowingLimit: float64PtrOrNil(res.BorrowingLimit),
					})
				}
				group.Flavors = append(group.Flavors, fq)
			}
			queue.ResourceGroups = append(queue.ResourceGroups, group)
		}
		cfg.ResourceQueues = append(cfg.ResourceQueues, queue)
	}

	for _, ru := range model.SchedulingRules {
		rule := SchedulerSchedulingRule{
			ResourceQueue: ru.ResourceQueue.ValueString(),
			Selector:      expandSchedulerSelector(ru.Selector),
		}
		if ru.PriorityPolicy != nil {
			rule.PriorityPolicy = &SchedulerPriorityPolicy{
				Default:     int64PtrOrNil(ru.PriorityPolicy.Default),
				Min:         int64PtrOrNil(ru.PriorityPolicy.Min),
				Max:         int64PtrOrNil(ru.PriorityPolicy.Max),
				OnViolation: stringPtrOrNil(ru.PriorityPolicy.OnViolation),
			}
		}
		cfg.SchedulingRules = append(cfg.SchedulingRules, rule)
	}

	if model.RecyclePolicy != nil {
		cfg.RecyclePolicy = &SchedulerRecyclePolicy{
			RotationInterval: stringPtrOrNil(model.RecyclePolicy.RotationInterval),
			MaxWorkloads:     int64PtrOrNil(model.RecyclePolicy.MaxWorkloads),
			MaxIdleDuration:  stringPtrOrNil(model.RecyclePolicy.MaxIdleDuration),
		}
	}

	return cfg
}

// flattenAdvancedInstanceConfig renders the API's object back to a JSON string.
// No prior-state comparison is needed: the attribute is a
// jsontypes.Normalized, which compares unmarshalled values, so the API's key
// reordering and its widening of 1 to 1.0 are both equal to what was written.
func flattenAdvancedInstanceConfig(apiValue map[string]any) (jsontypes.Normalized, error) {
	if len(apiValue) == 0 {
		return jsontypes.NewNormalizedNull(), nil
	}
	encoded, err := json.Marshal(apiValue)
	if err != nil {
		return jsontypes.NewNormalizedNull(), err
	}
	return jsontypes.NewNormalizedValue(string(encoded)), nil
}

// flattenSchedulerConfig maps the wire document onto the Terraform model.
//
// An absent section maps to nil, i.e. Terraform null - never to an empty list
// or an allocated empty struct. Unlike the omit half on the request side, this
// really is the implementation's to keep for every section: the sections are
// Optional and not Computed, so a config that omits one holds the provider to
// null, and [] or {} is a different value that diffs forever against it. The
// list fields append into nil slices and recycle_policy is only allocated when
// the wire carries it; do not pre-allocate either to "simplify" this loop.
//
// The server collapsing an empty array to an absent key is why an absent key is
// reachable here at all, and so why this function has to handle the case. It is
// not the reason null is the right mapping - that is the Terraform fact above,
// and it would still hold if the server echoed [] back verbatim.
func flattenSchedulerConfig(cfg SchedulerConfig) (*SchedulerConfigResourceModel, error) {
	model := &SchedulerConfigResourceModel{}

	for _, f := range cfg.ResourceFlavors {
		advanced, err := flattenAdvancedInstanceConfig(f.AdvancedInstanceConfig)
		if err != nil {
			return nil, fmt.Errorf("re-encoding advanced_instance_config for resource flavor %q: %w", f.Name, err)
		}
		model.ResourceFlavors = append(model.ResourceFlavors, schedulerResourceFlavorModel{
			Name:                   types.StringValue(f.Name),
			Selector:               flattenSchedulerSelector(f.Selector),
			AdvancedInstanceConfig: advanced,
		})
	}

	for _, q := range cfg.ResourceQueues {
		queue := schedulerResourceQueueModel{
			Name:       types.StringValue(q.Name),
			CohortName: types.StringPointerValue(q.CohortName),
		}
		if q.Preemption != nil {
			queue.Preemption = &schedulerPreemptionModel{
				ReclaimWithinCohort: types.StringPointerValue(q.Preemption.ReclaimWithinCohort),
				BorrowWithinCohort:  types.StringPointerValue(q.Preemption.BorrowWithinCohort),
				WithinResourceQueue: types.StringPointerValue(q.Preemption.WithinResourceQueue),
			}
		}
		for _, g := range q.ResourceGroups {
			group := schedulerResourceGroupModel{
				CoveredResources: flattenStringSlice(g.CoveredResources),
			}
			for _, fl := range g.Flavors {
				fq := schedulerFlavorQuotaModel{Name: types.StringValue(fl.Name)}
				for _, res := range fl.Resources {
					fq.Resources = append(fq.Resources, schedulerResourceQuotaSpecModel{
						Name:           types.StringValue(res.Name),
						NominalQuota:   types.Float64PointerValue(res.NominalQuota),
						LendingLimit:   types.Float64PointerValue(res.LendingLimit),
						BorrowingLimit: types.Float64PointerValue(res.BorrowingLimit),
					})
				}
				group.Flavors = append(group.Flavors, fq)
			}
			queue.ResourceGroups = append(queue.ResourceGroups, group)
		}
		model.ResourceQueues = append(model.ResourceQueues, queue)
	}

	for _, ru := range cfg.SchedulingRules {
		rule := schedulerSchedulingRuleModel{
			ResourceQueue: types.StringValue(ru.ResourceQueue),
			Selector:      flattenSchedulerSelector(ru.Selector),
		}
		if ru.PriorityPolicy != nil {
			rule.PriorityPolicy = &schedulerPriorityPolicyModel{
				Default:     types.Int64PointerValue(ru.PriorityPolicy.Default),
				Min:         types.Int64PointerValue(ru.PriorityPolicy.Min),
				Max:         types.Int64PointerValue(ru.PriorityPolicy.Max),
				OnViolation: types.StringPointerValue(ru.PriorityPolicy.OnViolation),
			}
		}
		model.SchedulingRules = append(model.SchedulingRules, rule)
	}

	if cfg.RecyclePolicy != nil {
		model.RecyclePolicy = &schedulerRecyclePolicyModel{
			RotationInterval: types.StringPointerValue(cfg.RecyclePolicy.RotationInterval),
			MaxWorkloads:     types.Int64PointerValue(cfg.RecyclePolicy.MaxWorkloads),
			MaxIdleDuration:  types.StringPointerValue(cfg.RecyclePolicy.MaxIdleDuration),
		}
	}

	return model, nil
}

// ---------------------------------------------------------------------------
// Plan-time validation
// ---------------------------------------------------------------------------

// ModifyPlan sends the planned document to POST /config/validate so a document
// the schema cannot reject - a scheduling rule naming a queue that does not
// exist, a flavor quota for a resource its group does not cover - fails at plan
// with the server's own sentence instead of as a 400 or 422 mid-apply.
//
// This runs in ModifyPlan rather than ValidateConfig deliberately: the
// ValidateResourceConfig RPC happens before the provider is configured, so
// there is no API client to call at that point.
func (r *SchedulerConfigResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		// Destroy. Nothing is being applied, so there is nothing to validate.
		return
	}
	if r.client == nil {
		return
	}
	if !req.Plan.Raw.IsFullyKnown() {
		// Values that resolve during apply cannot be serialized yet. Skipping
		// is the honest outcome: validating a partial document would report
		// errors about unknowns rather than about the practitioner's config.
		tflog.Debug(ctx, "skipping scheduler config plan-time validation: plan contains unknown values")
		return
	}

	var plan SchedulerConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var encodeErrs []string
	config := expandSchedulerConfig(&plan, &encodeErrs)
	for _, e := range encodeErrs {
		resp.Diagnostics.AddError("Invalid Scheduler Configuration", e)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	if err := validateSchedulerConfig(ctx, r.client, config); err != nil {
		if errors.Is(err, ErrSchedulerValidationUnavailable) {
			// The check could not run. Failing the plan here would make
			// `terraform plan` impossible for a reason unrelated to the
			// config - an expired token, a 5xx, a network blip, or the
			// organization's scheduler admission flag - and would report the
			// config as rejected when nothing ever read it. Warn, name why,
			// and let the plan proceed; a genuinely invalid document still
			// fails at apply with the server's own message.
			resp.Diagnostics.AddWarning(
				"Anyscale Scheduler Configuration Not Validated",
				fmt.Sprintf(
					"Terraform could not reach the Anyscale scheduler validation endpoint, so this config was "+
						"NOT checked before planning. This is not a statement about whether the config is valid.\n\n"+
						"Reason: %s\n\n"+
						"The plan continues. If the config turns out to be invalid, the apply will fail with the "+
						"API's own error message.",
					err,
				),
			)
			return
		}
		resp.Diagnostics.AddError(
			"Invalid Anyscale Scheduler Configuration",
			fmt.Sprintf("The Anyscale API rejected this scheduler config: %s", err),
		)
	}
}

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

// applyAndRefresh performs the shared Create/Update path: POST the document,
// then GET it back for the computed metadata. Create and Update are the same
// call on the wire - there is no create/update distinction for an immutable,
// versioned document, and this resource does not pretend otherwise.
func (r *SchedulerConfigResource) applyAndRefresh(ctx context.Context, plan *SchedulerConfigResourceModel, diags *diag.Diagnostics) {
	var encodeErrs []string
	config := expandSchedulerConfig(plan, &encodeErrs)
	for _, e := range encodeErrs {
		diags.AddError("Invalid Scheduler Configuration", e)
	}
	if diags.HasError() {
		return
	}

	version, err := applySchedulerConfig(ctx, r.client, config)
	if err != nil {
		diags.AddError("Unable to Apply Anyscale Scheduler Config", err.Error())
		return
	}
	tflog.Info(ctx, "applied Anyscale scheduler config", map[string]any{"version": version})

	current, err := getActiveSchedulerConfig(ctx, r.client)
	if err != nil {
		diags.AddError(
			"Unable to Read Anyscale Scheduler Config After Apply",
			fmt.Sprintf("Version %d was applied successfully, but reading it back failed: %s", version, err),
		)
		return
	}

	// The declared document stays exactly as planned; only the API's own
	// metadata is taken from the response. Overwriting the document with the
	// server's copy here would risk "provider produced inconsistent result
	// after apply" on any field the server normalizes - real drift belongs to
	// Read, which is allowed to differ from config.
	plan.Version = types.Int64Value(current.Result.Version)
	plan.CreatedAt = types.StringValue(current.Result.CreatedAt)
	plan.CreatorID = stringOrNull(current.Result.CreatorID)
}

func (r *SchedulerConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SchedulerConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.applyAndRefresh(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SchedulerConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SchedulerConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	current, err := getActiveSchedulerConfig(ctx, r.client)
	if err != nil {
		if errors.Is(err, ErrSchedulerConfigNotFound) {
			// The organization has no active config any more. Removing from
			// state - rather than erroring - lets the next apply recreate it.
			tflog.Warn(ctx, "no active Anyscale scheduler config; removing resource from state")
			resp.State.RemoveResource(ctx)
			return
		}
		if errors.Is(err, ErrSchedulerNotEnabled) {
			// The capability gate is closed, so this token cannot look at the
			// config. "Cannot look" is not evidence of "gone": erroring would
			// fail the whole plan on a flag flip, and removing from state
			// would plan a create for a config that still exists - which,
			// under an append-only API with no content dedupe, would mint a
			// duplicate version. Keep the prior state untouched and say so.
			//
			// Deliberately narrow: this is the only Read failure that fails
			// open. A transport error or a 5xx stays a hard error, because a
			// failed plan on a transient blip is ordinary behavior and
			// self-resolves, while silently trusting stale state is not.
			resp.Diagnostics.AddWarning(
				"Anyscale Scheduler Config Not Refreshed",
				fmt.Sprintf(
					"Terraform could not read the organization's scheduler config, so the values in state "+
						"were kept as-is and may be out of date.\n\n"+
						"Reason: %s\n\n"+
						"State was NOT removed: the config still exists, and removing it would plan a "+
						"redundant apply. Re-run once the Anyscale Scheduler is enabled to refresh it.",
					err,
				),
			)
			return
		}
		resp.Diagnostics.AddError("Unable to Read Anyscale Scheduler Config", err.Error())
		return
	}

	refreshed, err := flattenSchedulerConfig(current.Result.Config)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Anyscale Scheduler Config", err.Error())
		return
	}
	refreshed.Version = types.Int64Value(current.Result.Version)
	refreshed.CreatedAt = types.StringValue(current.Result.CreatedAt)
	refreshed.CreatorID = stringOrNull(current.Result.CreatorID)

	resp.Diagnostics.Append(resp.State.Set(ctx, refreshed)...)
}

func (r *SchedulerConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan SchedulerConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.applyAndRefresh(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the resource from Terraform state without calling the API.
// The Anyscale API exposes no delete operation for scheduler configs, so there
// is nothing to call. Applying an empty document instead was considered and
// rejected: a destroy must not write organization-wide state. Removing one
// resource from one Terraform configuration would mint a new active config
// version affecting every workload in the organization, and that write is
// permanent - there is no operation to undo it. Whether an empty document is
// a safe thing to apply is a separate question from whether a destroy may
// apply one on the practitioner's behalf; only the second is decided here.
func (r *SchedulerConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SchedulerConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddWarning(
		"Anyscale Scheduler Config Was Not Cleared",
		fmt.Sprintf(
			"The anyscale_scheduler_config resource was removed from Terraform state, but the organization's "+
				"scheduler config was NOT cleared and remains in effect (version %d, as last observed by Terraform). "+
				"The Anyscale API exposes no delete operation for scheduler configs.\n\n"+
				"To return scheduling to the unrestricted admission an organization has with no config at all, "+
				"declare an anyscale_scheduler_config with every section omitted (an empty document) and apply, "+
				"or manage it through the Anyscale CLI or console. Applying an empty document supersedes the "+
				"current config rather than removing it, so the API still reports an active version afterwards.",
			state.Version.ValueInt64(),
		),
	)
}

// ImportState adopts the organization's active scheduler config. The import ID
// is the organization ID: a singleton has no natural key, and requiring the org
// ID makes the import state which organization is being adopted rather than
// silently taking whatever the ambient token points at.
func (r *SchedulerConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org, err := fetchCurrentOrganization(ctx, r.client)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Identify Organization for Import", err.Error())
		return
	}
	if req.ID != org.ID.ValueString() {
		resp.Diagnostics.AddError(
			"Organization ID Mismatch",
			fmt.Sprintf(
				"anyscale_scheduler_config is imported by organization ID, and %q is not the organization this "+
					"provider's token authenticates to (%s). Check that the token and the import ID refer to the "+
					"same organization - the anyscale_organization data source reports the token's own organization.",
				req.ID, org.ID.ValueString(),
			),
		)
		return
	}

	// Every failure below is terminal, and deliberately so. Import runs with no
	// prior state: there is no previously-observed config to keep, so there is
	// nothing to fall back to and no way to proceed on a partial answer. A
	// closed capability gate means this token cannot read the config, and a 404
	// means there is no config to adopt - in both cases the only honest outcome
	// is to stop, because the alternative is writing state that was never read
	// from the API.
	current, err := getActiveSchedulerConfig(ctx, r.client)
	if err != nil {
		if errors.Is(err, ErrSchedulerConfigNotFound) {
			resp.Diagnostics.AddError(
				"No Active Anyscale Scheduler Config to Import",
				fmt.Sprintf("Organization %s has no active scheduler config. Apply one before importing it.", org.ID.ValueString()),
			)
			return
		}
		resp.Diagnostics.AddError("Unable to Read Anyscale Scheduler Config", err.Error())
		return
	}

	// There is no attribute to pass the import ID through to - organization
	// identity is connection-level and lives in the anyscale_organization data
	// source, not on this resource - so full state is set here directly.
	imported, err := flattenSchedulerConfig(current.Result.Config)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Anyscale Scheduler Config", err.Error())
		return
	}
	imported.Version = types.Int64Value(current.Result.Version)
	imported.CreatedAt = types.StringValue(current.Result.CreatedAt)
	imported.CreatorID = stringOrNull(current.Result.CreatorID)

	resp.Diagnostics.Append(resp.State.Set(ctx, imported)...)
}
