package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/dynamicplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Rollout strategies accepted by the backend's ApplyProductionServiceV2Model. MULTI_VERSION
// exists upstream but is out of scope for v1 (see
// .crystl/quest/CONTRACT_anyscale_service_resource.md section 1) so it is deliberately not a
// valid value here.
const (
	serviceRolloutStrategyRollout = "ROLLOUT"
	serviceRolloutStrategyInPlace = "IN_PLACE"
)

const defaultServiceRolloutTimeout = "30m"

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &ServiceResource{}
	_ resource.ResourceWithConfigure   = &ServiceResource{}
	_ resource.ResourceWithImportState = &ServiceResource{}
	_ resource.ResourceWithModifyPlan  = &ServiceResource{}
)

// NewServiceResource creates a new service resource.
func NewServiceResource() resource.Resource {
	return &ServiceResource{}
}

// ServiceResource defines the resource implementation.
type ServiceResource struct {
	client *Client
}

// ServiceResourceModel describes the resource data model. See
// .crystl/quest/CONTRACT_anyscale_service_resource.md for the full design contract.
type ServiceResourceModel struct {
	// Required inputs
	Name            types.String  `tfsdk:"name"`
	RayServeConfig  types.Dynamic `tfsdk:"ray_serve_config"`
	BuildID         types.String  `tfsdk:"build_id"`
	ComputeConfigID types.String  `tfsdk:"compute_config_id"`

	// Optional service-level inputs
	Description types.String `tfsdk:"description"`
	ProjectID   types.String `tfsdk:"project_id"`
	Tags        types.Map    `tfsdk:"tags"`

	// Optional rollout inputs
	RolloutStrategy types.String `tfsdk:"rollout_strategy"`
	MaxSurgePercent types.Int64  `tfsdk:"max_surge_percent"`
	ConnectionIDs   types.List   `tfsdk:"connection_ids"`
	RolloutTimeout  types.String `tfsdk:"rollout_timeout"`

	// Computed outputs. The four nested-object fields are typed as types.Object rather than the
	// concrete ServiceObservabilityURLsModel/ServiceVersionModel/ServiceStatusChecklistModel
	// structs the data sources use for the SAME shape (P0, contract section P0): a plain Go
	// struct/pointer cannot represent Unknown, and on Create (and on Update/ModifyPlan before an
	// apply has run) these Computed-only nested objects are genuinely Unknown in the plan -
	// req.Plan.Get decoding them into a bare struct field crashes with "Value Conversion Error"
	// on every single create (confirmed by actually running one against a mock server, not just
	// reading the code). types.Object/types.List natively hold Unknown/Null/Known, so decoding
	// works at every call site (Plan.Get, State.Get, State.Set) with no other change needed.
	// Converted to/from the reused ServiceObservabilityURLsModel/ServiceVersionModel/
	// ServiceStatusChecklistModel structs (Addendum A reuse preserved) via the attr-type maps
	// below and populateServiceResourceModelComputed - the shared service_conversion.go mapping
	// helpers themselves are untouched.
	ID                       types.String `tfsdk:"id"`
	CloudID                  types.String `tfsdk:"cloud_id"`
	Hostname                 types.String `tfsdk:"hostname"`
	BaseURL                  types.String `tfsdk:"base_url"`
	CurrentState             types.String `tfsdk:"current_state"`
	GoalState                types.String `tfsdk:"goal_state"`
	CreatorID                types.String `tfsdk:"creator_id"`
	CreatedAt                types.String `tfsdk:"created_at"`
	EndedAt                  types.String `tfsdk:"ended_at"`
	IsMultiVersion           types.Bool   `tfsdk:"is_multi_version"`
	AutoRolloutEnabled       types.Bool   `tfsdk:"auto_rollout_enabled"`
	ErrorMessage             types.String `tfsdk:"error_message"`
	ServiceObservabilityURLs types.Object `tfsdk:"service_observability_urls"`
	PrimaryVersion           types.Object `tfsdk:"primary_version"`
	CanaryVersion            types.Object `tfsdk:"canary_version"`
	ServiceStatusChecklist   types.Object `tfsdk:"service_status_checklist"`
}

// Attr-type maps mirroring the wire shape of the four nested Computed objects above -
// service_status_checklist_item's shape is shared by both the "shared" and "items" (within
// per_version) lists, matching serviceStatusChecklistItemResourceAttributes(). Kept in sync by
// hand with serviceVersionResourceAttributes()/serviceStatusChecklistItemResourceAttributes()
// (the schema.Attribute definitions) since attr.Type and schema.Attribute are different framework
// types with no automatic conversion between them.
var serviceObservabilityURLsAttrTypes = map[string]attr.Type{
	"service_dashboard_url":                    types.StringType,
	"service_dashboard_embedding_url":          types.StringType,
	"serve_deployment_dashboard_url":           types.StringType,
	"serve_deployment_dashboard_embedding_url": types.StringType,
}

var serviceVersionAttrTypes = map[string]attr.Type{
	"id":                 types.StringType,
	"created_at":         types.StringType,
	"version":            types.StringType,
	"current_state":      types.StringType,
	"weight":             types.Int64Type,
	"current_weight":     types.Int64Type,
	"target_weight":      types.Int64Type,
	"build_id":           types.StringType,
	"compute_config_id":  types.StringType,
	"production_job_ids": types.ListType{ElemType: types.StringType},
	"connection_ids":     types.ListType{ElemType: types.StringType},
	"ray_serve_config":   types.StringType,
}

var serviceStatusChecklistItemAttrTypes = map[string]attr.Type{
	"kind":        types.StringType,
	"label":       types.StringType,
	"state":       types.StringType,
	"message":     types.StringType,
	"version_id":  types.StringType,
	"observed_at": types.StringType,
}

var serviceVersionChecklistAttrTypes = map[string]attr.Type{
	"version_id": types.StringType,
	"items":      types.ListType{ElemType: types.ObjectType{AttrTypes: serviceStatusChecklistItemAttrTypes}},
}

var serviceStatusChecklistAttrTypes = map[string]attr.Type{
	"shared":      types.ListType{ElemType: types.ObjectType{AttrTypes: serviceStatusChecklistItemAttrTypes}},
	"per_version": types.ListType{ElemType: types.ObjectType{AttrTypes: serviceVersionChecklistAttrTypes}},
}

// Metadata returns the resource type name.
func (r *ServiceResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

// Schema defines the schema for the resource.
func (r *ServiceResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: `Deploys an Anyscale Service and rolls out new versions on config change. Companion to the read-only ` + "`anyscale_service`" + `/` + "`anyscale_services`" + ` data sources, which share this resource's computed field shapes.

A change to ` + "`ray_serve_config`" + `, ` + "`build_id`" + `, or ` + "`compute_config_id`" + ` rolls out a new version automatically (declarative auto-rollout: the new version always rolls to 100%, so ` + "`terraform apply`" + ` converges to a steady RUNNING state rather than holding at a partial canary). ` + "`max_surge_percent`" + ` only paces that rollout; it does not hold it. Staged/manual canary (hold at a partial percent, explicit promote/rollback) is intentionally not supported by this resource in this version - it does not fit a converging declarative model and may arrive later as separate provider actions layered on top, not as a change to this resource's lifecycle.

~> **Note:** ` + "`terraform destroy`" + ` terminates the service and then deletes the record entirely (the backend requires a service to be terminated before it can be deleted, so destroy performs both steps and waits for termination in between). This is a gone-for-good delete, not an archive - there is no Terraform-managed way to recover a destroyed service's history afterward.`,

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the service.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			// ─── Required inputs ───
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The name of the service. Unique only within `project_id`, not organization-wide. Immutable: the backend has no rename endpoint, so changing this replaces the resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"ray_serve_config": schema.DynamicAttribute{
				Required: true,
				MarkdownDescription: "The Ray Serve application config, as a native HCL object (supports nested objects and mixed types) - the same `types.Dynamic` convention `anyscale_compute_config`'s `advanced_instance_config`/`flags` use for similarly open-ended, schemaless config. " +
					"This value is never refreshed from the server on a normal read: the backend may enrich or reorder the stored copy, and echoing that back would produce a spurious diff against your own config on every plan even with no real change. What you last applied is what stays authoritative in state; the live server-side copy (potentially normalized) is visible read-only via `primary_version.ray_serve_config`, so you can compare if you suspect drift. " +
					"After `terraform import`, this field is seeded once from the server's stored version - the first `terraform plan` afterward may show a normalization diff that you resolve by reconciling your HCL with the imported value (a known limitation of importing any open/schemaless config).",
				PlanModifiers: []planmodifier.Dynamic{
					dynamicplanmodifier.UseStateForUnknown(),
				},
			},
			"build_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The ID of the container image build (cluster environment build) this service runs, e.g. from `anyscale_container_image_build`. Changing this rolls out a new service version; it is not a replace.",
			},
			"compute_config_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The ID of the compute config this service uses, e.g. from `anyscale_compute_config`. Pins the cloud the service runs in - there is no separate top-level `cloud_id` argument because it is fully determined by this value. Changing this rolls out a new service version; it is not a replace.",
			},

			// ─── Optional service-level inputs ───
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Description of the service. Null if not set.",
			},
			"project_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "The ID of the project this service starts clusters in. If omitted, the backend resolves your organization's default project for the compute config's cloud - that resolved value is written back here, so leaving this unset does not produce a diff on later plans. " +
					"Immutable: the backend has no move-project endpoint, so changing this (including changing which project an omitted value would resolve to, e.g. by changing `compute_config_id` to a different cloud) replaces the resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tags": schema.MapAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Tags to associate with the service. The backend stores tags through a separate system from the service record itself (`/api/v2/tags/resource`, not the service GET/apply endpoints), so this resource makes its own extra calls to keep it a true round trip: read back on refresh, and - since that system's write endpoint only upserts (adds/updates) rather than replaces - any key removed from this map is explicitly deleted so it does not linger on the backend forever.",
			},

			// ─── Optional rollout inputs ───
			"rollout_strategy": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(serviceRolloutStrategyRollout),
				MarkdownDescription: "Either `ROLLOUT` (default) or `IN_PLACE`. `ROLLOUT` deploys the new version on a newly started cluster and shifts traffic over, then converges to RUNNING. `IN_PLACE` upgrades the existing cluster in place - faster, but the backend permits changing only `ray_serve_config` under it; changing `build_id`, `compute_config_id`, or `connection_ids` in the same apply as `rollout_strategy = \"IN_PLACE\"` is rejected at plan time (see this resource's plan-time validation) rather than left to fail opaquely at apply. " +
					"Not readable back from the API, so - like `tags` - drift on this attribute is never detected; it is a pure rollout directive re-sent on every apply.",
				Validators: []validator.String{
					stringvalidator.OneOf(serviceRolloutStrategyRollout, serviceRolloutStrategyInPlace),
				},
			},
			"max_surge_percent": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Pacing knob only (0-100): how much excess capacity to allocate during the rollout. The rollout always still converges to 100% - this does not hold the rollout at a partial percent. Null lets the backend pick its own pacing. Not readable back from the API (see `rollout_strategy`).",
				Validators: []validator.Int64{
					int64validator.Between(0, 100),
				},
			},
			"connection_ids": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				MarkdownDescription: "Connection IDs to associate with the new service version. Wire semantics matter here: leaving this null preserves whatever connections are already attached (the common case), while explicitly setting `[]` removes all of them - so this is modeled as a nullable list rather than defaulting to empty. " +
					"For the same reason, this is never refreshed from the server on read (mirroring `ray_serve_config`'s treatment): the server's actual current connections are visible read-only via `primary_version.connection_ids`, but copying them into this null-preserving directive would silently turn a future \"don't touch connections\" apply into \"remove every connection that's actually attached.\"",
			},
			"rollout_timeout": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(defaultServiceRolloutTimeout),
				MarkdownDescription: "Maximum time to wait for a create or update rollout to reach `RUNNING`, or for destroy to wait for termination to reach `TERMINATED` before deleting (e.g. `30m`, `1h`). Defaults to `30m`.",
			},

			// ─── Computed outputs (reused from the anyscale_service data source's model - see
			// service_conversion.go and populateServiceResourceModelComputed below) ───
			"cloud_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The ID of the cloud this service runs in, derived from `compute_config_id`. Exposed for parity with the `anyscale_service`/`anyscale_services` data sources.",
			},
			"hostname": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The hostname of the service.",
			},
			"base_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The base URL of this service.",
			},
			"current_state": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The current state of this service (e.g. `RUNNING`, `UNHEALTHY`, `TERMINATED`). Ships as a plain string with no client-side enum validation, matching this provider's convention of not hand-maintaining a copy of the backend's enum list.",
			},
			"goal_state": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The goal state of this service (`RUNNING` or `TERMINATED`).",
			},
			"creator_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The ID of the user who created the service.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the service was created.",
			},
			"ended_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the service was terminated. Null while the service is active.",
			},
			"is_multi_version": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this service is a multi-version service (multiple active versions with no single canary). Always false for services managed by this resource in this version - multi-version is out of scope (see this resource's top-level description).",
			},
			"auto_rollout_enabled": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this service uses automatic rollout.",
			},
			"error_message": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Error message from processing the most recent API request against this service, if any. Null otherwise.",
			},
			"service_observability_urls": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Dashboard URLs for this service. Each URL is null if the backend has none to report (e.g. before the service's first successful deploy).",
				Attributes: map[string]schema.Attribute{
					"service_dashboard_url": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "URL to a dashboard with graphs about the entire service.",
					},
					"service_dashboard_embedding_url": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "Embeddable variant of `service_dashboard_url`.",
					},
					"serve_deployment_dashboard_url": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "URL to a dashboard with graphs about a single deployment or replica of the service.",
					},
					"serve_deployment_dashboard_embedding_url": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "Embeddable variant of `serve_deployment_dashboard_url`.",
					},
				},
			},
			"primary_version": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The primary (currently active) version of this service, as last observed from the server - including its own `ray_serve_config`/`connection_ids`, which reflect the live server-side state rather than this resource's authored `ray_serve_config`/`connection_ids` inputs above. Compare the two if you suspect drift.",
				Attributes:          serviceVersionResourceAttributes(),
			},
			"canary_version": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The canary version of this service. Null unless the service is currently rolling out.",
				Attributes:          serviceVersionResourceAttributes(),
			},
			"service_status_checklist": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Per-component status breakdown derived from the most recent reconciler snapshot. Null for terminated services and during the brief window before the reconciler's first tick on a brand-new service.",
				Attributes: map[string]schema.Attribute{
					"shared": schema.ListNestedAttribute{
						Computed:            true,
						MarkdownDescription: "Components shared across all versions (load balancer, listener rule, DNS, TLS certificate). Empty (not null) if none.",
						NestedObject: schema.NestedAttributeObject{
							Attributes: serviceStatusChecklistItemResourceAttributes(),
						},
					},
					"per_version": schema.ListNestedAttribute{
						Computed:            true,
						MarkdownDescription: "Per-version components (cluster, application, target group), one entry per active service version. Empty (not null) if none.",
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"version_id": schema.StringAttribute{
									Computed:            true,
									MarkdownDescription: "The service version these checklist items belong to.",
								},
								"items": schema.ListNestedAttribute{
									Computed:            true,
									MarkdownDescription: "Per-component statuses for this version. Empty (not null) if none.",
									NestedObject: schema.NestedAttributeObject{
										Attributes: serviceStatusChecklistItemResourceAttributes(),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// serviceVersionResourceAttributes returns the resource-schema equivalent of
// schema_shared_attributes.go's serviceVersionAttributes(). Framework resource and data source
// schema attributes are distinct Go types (resource/schema.Attribute vs
// datasource/schema.Attribute), so the data sources' map cannot be reused directly here even
// though every field/description is identical - kept byte-for-byte in sync with that function
// by hand since there is no framework-level sharing mechanism across the two schema packages.
// Returns a fresh map on every call for the same reason serviceVersionAttributes does (primary_
// version and canary_version each need their own independent map value).
func serviceVersionResourceAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The unique identifier of this service version.",
		},
		"created_at": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Timestamp when this version was created.",
		},
		"version": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The version string identifier for this version.",
		},
		"current_state": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The current state of this service version (e.g. `RUNNING`, `STARTING`, `UNHEALTHY`).",
		},
		"weight": schema.Int64Attribute{
			Computed:            true,
			MarkdownDescription: "The configured traffic weight currently stored for this version, 0-100. During rollouts this may be an intermediate desired load-balancer weight rather than the live figure - see `current_weight`.",
		},
		"current_weight": schema.Int64Attribute{
			Computed:            true,
			MarkdownDescription: "The current percentage of live traffic observed for this version, 0-100. Null if not currently observed.",
		},
		"target_weight": schema.Int64Attribute{
			Computed:            true,
			MarkdownDescription: "The intended final traffic weight for this version, 0-100, when known. Null otherwise.",
		},
		"build_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The ID of the cluster environment build (container image) this version runs.",
		},
		"compute_config_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The ID of the compute configuration this version uses.",
		},
		"production_job_ids": schema.ListAttribute{
			ElementType:         types.StringType,
			Computed:            true,
			MarkdownDescription: "The production job IDs associated with this service version. Empty (not null) if none.",
		},
		"connection_ids": schema.ListAttribute{
			ElementType:         types.StringType,
			Computed:            true,
			MarkdownDescription: "The connection IDs associated with this service version. Null if the API does not report connections for this version.",
		},
		"ray_serve_config": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The Ray Serve config for this version, as a JSON string. This is a dynamic, open-ended structure upstream with no fixed schema in this provider - use `jsondecode()` in HCL to access individual fields. Always present (required upstream), even when its contents are trivial.",
		},
	}
}

// serviceStatusChecklistItemResourceAttributes is the resource-schema equivalent of
// schema_shared_attributes.go's serviceStatusChecklistItemAttributes() - see
// serviceVersionResourceAttributes' doc comment for why this cannot be shared directly.
func serviceStatusChecklistItemResourceAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"kind": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The kind of resource this checklist row represents (e.g. `SERVICE`, `SERVICE_VERSION`, `LOAD_BALANCER`). Ships as a plain string with no client-side enum validation, matching this provider's convention of not hand-maintaining a copy of the backend's enum list.",
		},
		"label": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "User-facing label for this resource (e.g. `Cluster`, `Load Balancer`).",
		},
		"state": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The state of this resource (e.g. `RUNNING`, `UNHEALTHY`, `STARTING`).",
		},
		"message": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Reconciler-provided message describing the current state. Empty string, not null, when the backend has no message to report.",
		},
		"version_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The service version this item belongs to. Null for items shared across versions (e.g. cloud networking).",
		},
		"observed_at": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "When this row's state was last observed by the cluster manager. Null when the snapshot is missing a timestamp (older event).",
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *ServiceResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		AddConfigError(&resp.Diagnostics, "Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}

	r.client = client
}

// ModifyPlan enforces the backend's IN_PLACE invariant at plan time rather than letting a
// PUT /apply 422 (traced: services_dao.upgrade_service_in_place hash-compares the whole version
// config excluding ray_serve_config) opaquely surface during apply. Only relevant to Update -
// Create has no prior state to compare against, and Destroy has no planned new state.
func (r *ServiceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var state, plan ServiceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.RolloutStrategy.ValueString() != serviceRolloutStrategyInPlace {
		return
	}

	// The frozen set for v1: every version-defining field besides ray_serve_config. Framed as
	// the invariant (what CAN change under IN_PLACE), not as an allowlist of these three names,
	// so it stays correct if a future version-level field is added to the schema.
	var changed []string
	if !plan.BuildID.Equal(state.BuildID) {
		changed = append(changed, "build_id")
	}
	if !plan.ComputeConfigID.Equal(state.ComputeConfigID) {
		changed = append(changed, "compute_config_id")
	}
	if !plan.ConnectionIDs.Equal(state.ConnectionIDs) {
		changed = append(changed, "connection_ids")
	}

	if len(changed) > 0 {
		AddConfigError(&resp.Diagnostics, "Invalid Change Under IN_PLACE Rollout Strategy",
			fmt.Sprintf(
				"rollout_strategy = \"IN_PLACE\" permits changing only ray_serve_config. The following attribute(s) changed: %s. "+
					"Use rollout_strategy = \"ROLLOUT\" (the default) to change them, or revert this change.",
				strings.Join(changed, ", "),
			))
	}
}

// Create creates the service and sets the initial Terraform state.
func (r *ServiceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ServiceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, err := time.ParseDuration(plan.RolloutTimeout.ValueString())
	if err != nil {
		AddConfigError(&resp.Diagnostics, "Invalid Rollout Timeout", err.Error())
		return
	}

	name := plan.Name.ValueString()
	projectID := plan.ProjectID.ValueString()

	// Create-adoption guard: PUT /apply is create-or-update keyed by (name, project_id) with
	// no id in the path, so without this check a fresh `terraform apply` would silently take
	// over an unrelated pre-existing service rather than creating a new one. Same
	// error-on-collision stance as findCloudByName's adopt-path ruling, but stricter: ANY
	// match errors here (not just 2+), since adopting even a single match risks reconfiguring
	// someone else's running service, unlike a cloud recovering its own interrupted create.
	existingMatches, err := findExistingServiceIDs(ctx, r.client, name, projectID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "check for existing service", err)
		return
	}
	if len(existingMatches) > 0 {
		var collisions []string
		for _, m := range existingMatches {
			collisions = append(collisions, fmt.Sprintf("%s (project %s)", m.ID, m.ProjectID))
		}

		detail := fmt.Sprintf(
			"A service named %q already exists: %s. PUT /apply is create-or-update by (name, project_id), "+
				"so creating this resource would silently take over that service's existing configuration rather "+
				"than creating a new one.",
			name, strings.Join(collisions, ", "),
		)
		if projectID == "" {
			// F5 (contract section F): project_id was omitted, so this search could only check
			// by name across every project the token can see - broader than what the backend
			// will actually collide against once it resolves a specific default project (see
			// findExistingServiceIDs' doc comment). Name the fix for that specific case: set
			// project_id to a project that does NOT already have this name, alongside the
			// general import escape hatch.
			detail += fmt.Sprintf(
				" If you intended a different project than the one(s) listed, set project_id explicitly. "+
					"If this is the service you intend to manage, import it instead: "+
					"terraform import anyscale_service.<resource_name> %s",
				existingMatches[0].ID,
			)
		} else {
			detail += fmt.Sprintf(
				" If this is the service you intend to manage, import it instead: "+
					"terraform import anyscale_service.<resource_name> %s",
				existingMatches[0].ID,
			)
		}

		AddConfigError(&resp.Diagnostics, "Service Already Exists", detail)
		return
	}

	applyBody, err := buildApplyServiceRequest(ctx, &plan)
	if err != nil {
		AddConfigError(&resp.Diagnostics, "Invalid Service Configuration", err.Error())
		return
	}

	reqBody, err := MarshalRequestBody(applyBody)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "service apply request", err)
		return
	}

	tflog.Info(ctx, "Applying service", map[string]any{"name": name})

	applyResp, err := DoRequestAndParse[ServiceResponse](
		ctx, r.client, "PUT", "/api/v2/services-v2/apply", reqBody, http.StatusAccepted,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "apply service", err)
		return
	}

	service := applyResp.Result
	plan.ID = types.StringValue(service.ID)
	// project_id is Optional+Computed: when the user omits it, the plan value is unknown until
	// resolved here from the backend's default-project pick (traced: check_and_get_resources /
	// get_default_project). Must be set to a known value before any State.Set below.
	plan.ProjectID = types.StringValue(service.ProjectID)

	// Persist state now that the service exists remotely, before doing anything else that could
	// fail (tags sync below, then the potentially long-running rollout wait) - without this, a
	// later failure would leave the service orphaned in the backend with no Terraform record to
	// destroy it (G2, contract section H). Mirrors resource_container_image_build.go's Create.
	diags := populateServiceResourceModelComputed(ctx, &plan, &service)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// tags always go through the dedicated tags endpoints (see syncServiceTags), never through
	// apply - there is nothing previously configured to remove on a fresh Create, so this is
	// upsert-only in practice, but calling the same shared helper keeps Create and Update
	// consistent rather than each hand-rolling its own tags write. Deliberately after the
	// persist above, not before: the service is already safely tracked in state by this point,
	// so a tags failure here surfaces as a plain error rather than orphaning anything.
	newTags, tagsDiags := tagsMapFromModel(ctx, plan.Tags)
	resp.Diagnostics.Append(tagsDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := syncServiceTags(ctx, r.client, service.ID, map[string]string{}, newTags); err != nil {
		AddAPIError(&resp.Diagnostics, "sync service tags", err)
		return
	}

	finalService, err := waitForServiceState(ctx, r.client, service.ID, serviceStateRunning, timeout)
	if finalService != nil {
		diags := populateServiceResourceModelComputed(ctx, &plan, finalService)
		resp.Diagnostics.Append(diags...)
		refreshServiceTagsIntoModel(ctx, r.client, finalService.ID, &plan.Tags)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		AddAPIError(&resp.Diagnostics, "wait for service rollout", err)
		return
	}
}

// Read refreshes the Terraform state with the latest data.
func (r *ServiceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ServiceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	serviceID := state.ID.ValueString()

	service, err := getServiceByID(ctx, r.client, serviceID)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			tflog.Warn(ctx, "Service not found, removing from state", map[string]any{"service_id": serviceID})
			resp.State.RemoveResource(ctx)
			return
		}
		AddAPIError(&resp.Diagnostics, "read service", err)
		return
	}

	diags := populateServiceResourceModelComputed(ctx, &state, service)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Refresh the plain, un-normalized inputs the API can meaningfully report drift for.
	// Deliberately NOT refreshed here (state keeps whatever was last applied/imported):
	//   - ray_serve_config, connection_ids: see their schema MarkdownDescriptions - the server's
	//     live copies are visible via primary_version instead, refreshing the authoring copies
	//     would either fight config with a spurious diff (ray_serve_config) or silently flip a
	//     null "preserve" directive into an active "remove everything" one (connection_ids).
	//   - rollout_strategy, max_surge_percent: not returned by this endpoint (or any endpoint) at
	//     all, nothing to refresh from - see their schema MarkdownDescriptions.
	//   - rollout_timeout: purely local to this provider, never sent to or read from the API.
	state.Name = types.StringValue(service.Name)
	state.Description = types.StringPointerValue(service.Description)
	state.BuildID = types.StringValue(service.PrimaryVersion.BuildID)
	state.ComputeConfigID = types.StringValue(service.PrimaryVersion.ComputeConfigID)
	// H3 (contract section H): project_id must be refreshed here even though it is
	// RequiresReplace and Create/Update already set it - ImportState seeds only id and
	// ray_serve_config, so without this the post-import Read would leave project_id null.
	// Writing the real project_id into config afterward would then read as null->value, which
	// RequiresReplace turns into a destroy+recreate of the service that was just imported.
	state.ProjectID = types.StringValue(service.ProjectID)

	// tags lives entirely outside the service GET response (a separate system - see
	// fetchServiceTags), so refreshing it needs its own call.
	refreshServiceTagsIntoModel(ctx, r.client, serviceID, &state.Tags)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// serviceDeployFieldsChanged reports whether any field that requires a new PUT /apply + rollout
// wait differs between plan and state (H2, contract section H). rollout_timeout (purely local
// to this provider) and tags (its own always-run sync via syncServiceTags, independent of
// whether a deploy happens) are deliberately excluded: neither has a version/rollout concept,
// so changing only one of them must not redeploy an otherwise-unchanged, healthy running
// service. name/project_id are RequiresReplace and so cannot differ here at all (Update is
// never called with either changed - that goes through Create+Delete instead).
func serviceDeployFieldsChanged(plan, state *ServiceResourceModel) bool {
	return !plan.RayServeConfig.Equal(state.RayServeConfig) ||
		!plan.BuildID.Equal(state.BuildID) ||
		!plan.ComputeConfigID.Equal(state.ComputeConfigID) ||
		!plan.ConnectionIDs.Equal(state.ConnectionIDs) ||
		!plan.RolloutStrategy.Equal(state.RolloutStrategy) ||
		!plan.MaxSurgePercent.Equal(state.MaxSurgePercent) ||
		!plan.Description.Equal(state.Description)
}

// Update applies a new service version and waits for it to become healthy - but only when a
// deploy-affecting field actually changed (see serviceDeployFieldsChanged). Tags are synced
// unconditionally beforehand since they carry no version/rollout concept of their own.
func (r *ServiceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ServiceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	serviceID := state.ID.ValueString()
	plan.ID = state.ID

	oldTags, oldDiags := tagsMapFromModel(ctx, state.Tags)
	resp.Diagnostics.Append(oldDiags...)
	newTags, newDiags := tagsMapFromModel(ctx, plan.Tags)
	resp.Diagnostics.Append(newDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := syncServiceTags(ctx, r.client, serviceID, oldTags, newTags); err != nil {
		AddAPIError(&resp.Diagnostics, "sync service tags", err)
		return
	}

	if !serviceDeployFieldsChanged(&plan, &state) {
		// H2 (contract section H): nothing that requires a new version changed - e.g. only
		// rollout_timeout, or only tags (already synced above) - so skip the PUT /apply +
		// rollout wait against an otherwise-unchanged, healthy running service.
		//
		// H5 (contract section H): still need a fresh GET to populate the computed outputs
		// before persisting. Every Computed attribute without a UseStateForUnknown plan
		// modifier (cloud_id, hostname, current_state, primary_version, etc. - everything
		// except id/ray_serve_config/project_id) is Unknown in this plan, since no apply ran
		// to resolve it. Setting state with those still-Unknown values would error post-apply
		// ("provider produced inconsistent result... value is unknown"). Deliberately NOT
		// fixed by adding UseStateForUnknown to those attributes instead: current_state,
		// goal_state, primary_version, etc. are volatile and server-controlled, so pinning them
		// to prior state would hide real drift rather than report it (the Computed+
		// UseStateForUnknown volatile hazard) - a fresh read is the correct fix, not a plan
		// modifier that dodges the symptom.
		service, err := getServiceByID(ctx, r.client, serviceID)
		if err != nil {
			AddAPIError(&resp.Diagnostics, "read service", err)
			return
		}
		diags := populateServiceResourceModelComputed(ctx, &plan, service)
		resp.Diagnostics.Append(diags...)
		refreshServiceTagsIntoModel(ctx, r.client, serviceID, &plan.Tags)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	timeout, err := time.ParseDuration(plan.RolloutTimeout.ValueString())
	if err != nil {
		AddConfigError(&resp.Diagnostics, "Invalid Rollout Timeout", err.Error())
		return
	}

	applyBody, err := buildApplyServiceRequest(ctx, &plan)
	if err != nil {
		AddConfigError(&resp.Diagnostics, "Invalid Service Configuration", err.Error())
		return
	}

	reqBody, err := MarshalRequestBody(applyBody)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "service apply request", err)
		return
	}

	tflog.Info(ctx, "Applying service update", map[string]any{
		"service_id": serviceID,
		"name":       plan.Name.ValueString(),
	})

	applyResp, err := DoRequestAndParse[ServiceResponse](
		ctx, r.client, "PUT", "/api/v2/services-v2/apply", reqBody, http.StatusAccepted,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "apply service update", err)
		return
	}

	service := applyResp.Result
	plan.ID = types.StringValue(service.ID)
	plan.ProjectID = types.StringValue(service.ProjectID)

	// Persist immediately for the same orphan-avoidance reason as Create.
	diags := populateServiceResourceModelComputed(ctx, &plan, &service)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	finalService, err := waitForServiceState(ctx, r.client, service.ID, serviceStateRunning, timeout)
	if finalService != nil {
		diags := populateServiceResourceModelComputed(ctx, &plan, finalService)
		resp.Diagnostics.Append(diags...)
		refreshServiceTagsIntoModel(ctx, r.client, finalService.ID, &plan.Tags)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		AddAPIError(&resp.Diagnostics, "wait for service rollout", err)
		return
	}
}

// Delete terminates the service, waits for termination to land, then deletes the record.
// Traced (services_dao.delete_service): DELETE /{id} 400s unless the service is already
// TERMINATED, and initiate_termination is itself an async transition - so a bare
// terminate-then-immediate-DELETE would fail in practice. initiate_termination is also
// idempotent on an already-terminated goal_state (traced), so re-running Delete after a
// partial failure is safe.
func (r *ServiceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ServiceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	serviceID := state.ID.ValueString()

	timeout, err := time.ParseDuration(state.RolloutTimeout.ValueString())
	if err != nil {
		timeout, _ = time.ParseDuration(defaultServiceRolloutTimeout)
	}

	tflog.Info(ctx, "Terminating service", map[string]any{"service_id": serviceID})

	// H1 (contract section H): StatusNotFound must NOT be in the accepted list here - if it
	// were, a 404 would make err nil (api_helpers.go's isStatusExpected treats any listed
	// status as success), making the strings.Contains(err, "404") guard below unreachable dead
	// code. That would fall through to waitForServiceState against an already-gone service,
	// which 404s there instead and turns an idempotent "already deleted" destroy into a
	// failure. Only StatusAccepted is a real success here; a 404 must produce a real error for
	// the guard below to catch.
	_, err = DoRequestRaw(
		ctx, r.client, "POST", fmt.Sprintf("/api/v2/services-v2/%s/terminate", serviceID), nil,
		http.StatusAccepted,
	)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			tflog.Info(ctx, "Service already gone", map[string]any{"service_id": serviceID})
			return
		}
		AddAPIError(&resp.Diagnostics, "terminate service", err)
		return
	}

	if _, err := waitForServiceState(ctx, r.client, serviceID, serviceStateTerminated, timeout); err != nil {
		AddAPIError(&resp.Diagnostics, "wait for service termination", err)
		return
	}

	tflog.Info(ctx, "Deleting service", map[string]any{"service_id": serviceID})

	_, err = DoRequestRaw(
		ctx, r.client, "DELETE", fmt.Sprintf("/api/v2/services-v2/%s", serviceID), nil,
		http.StatusNoContent, http.StatusNotFound,
	)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return
		}
		AddAPIError(&resp.Diagnostics, "delete service", err)
		return
	}
}

// ImportState imports the resource by service ID.
func (r *ServiceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	if resp.Diagnostics.HasError() {
		return
	}

	// ray_serve_config is Required but Read deliberately never refreshes it (see its schema
	// MarkdownDescription) - on a fresh import there is no prior applied value to preserve at
	// all, so it must be seeded here, once, from the server's stored version. Mirrors
	// compute_config's CC12 exception, which special-cases its own Dynamic fields
	// (flags/advanced_instance_config) in ImportState for the identical reason.
	service, err := getServiceByID(ctx, r.client, req.ID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read service for import", err)
		return
	}

	var rsConfigMap map[string]interface{}
	if err := json.Unmarshal(service.PrimaryVersion.RayServeConfig, &rsConfigMap); err != nil {
		AddJSONError(&resp.Diagnostics, "unmarshal", "imported ray_serve_config", err)
		return
	}

	rsConfigDynamic, err := InterfaceToDynamic(ctx, rsConfigMap)
	if err != nil {
		AddConfigError(&resp.Diagnostics, "Failed to Convert Imported ray_serve_config", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("ray_serve_config"), rsConfigDynamic)...)
}

// Helper functions

// applyServiceRequest mirrors the wire fields of the backend's ApplyProductionServiceV2Model
// this resource actually sends. Fields deliberately not modeled (config, ray_gcs_external_storage_config,
// tracing_config, canary_percent, auto_complete_rollout, traffic_percent, version) are omitted
// from the request entirely - matching Go's zero-value/omitempty behavior - so the backend
// applies its own defaults, rather than this provider re-deriving or freezing them. See
// .crystl/quest/CONTRACT_anyscale_service_resource.md section 1 for the documented scope.
//
// tags is deliberately NOT one of these fields even though the backend model accepts one: this
// resource always syncs tags through the dedicated /api/v2/tags/resource endpoints instead (see
// syncServiceTags), independent of whether a deploy happens at all - see serviceDeployFieldsChanged's
// doc comment (H2, contract section H) for why a tags-only change must not go through apply.
type applyServiceRequest struct {
	Name            string          `json:"name"`
	Description     *string         `json:"description,omitempty"`
	ProjectID       *string         `json:"project_id,omitempty"`
	RayServeConfig  json.RawMessage `json:"ray_serve_config"`
	BuildID         string          `json:"build_id"`
	ComputeConfigID string          `json:"compute_config_id"`
	RolloutStrategy string          `json:"rollout_strategy,omitempty"`
	MaxSurgePercent *int            `json:"max_surge_percent,omitempty"`
	// *[]string (not []string): wire semantics are null=preserve existing connections,
	// []=explicitly remove all - a plain slice cannot represent that distinction once
	// marshaled, and omitempty on a non-nil pointer to an empty slice still emits "[]".
	ConnectionIDs *[]string `json:"connection_ids,omitempty"`
}

// buildApplyServiceRequest converts a plan/config model into the PUT /apply request body shared
// by Create and Update.
func buildApplyServiceRequest(ctx context.Context, plan *ServiceResourceModel) (*applyServiceRequest, error) {
	rsConfig, err := DynamicToInterface(ctx, plan.RayServeConfig)
	if err != nil {
		return nil, fmt.Errorf("invalid ray_serve_config: %w", err)
	}
	rsConfigJSON, err := json.Marshal(rsConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ray_serve_config: %w", err)
	}

	body := &applyServiceRequest{
		Name:            plan.Name.ValueString(),
		RayServeConfig:  rsConfigJSON,
		BuildID:         plan.BuildID.ValueString(),
		ComputeConfigID: plan.ComputeConfigID.ValueString(),
	}

	if !plan.Description.IsNull() {
		desc := plan.Description.ValueString()
		body.Description = &desc
	}

	// Unknown when Optional+Computed and omitted by the user (not yet resolved by the
	// backend) - send nothing and let the backend pick its default project, same as a
	// genuinely-omitted config value. Known on every call after Create resolves it once.
	if !plan.ProjectID.IsNull() && !plan.ProjectID.IsUnknown() {
		projectID := plan.ProjectID.ValueString()
		body.ProjectID = &projectID
	}

	if !plan.RolloutStrategy.IsNull() {
		body.RolloutStrategy = plan.RolloutStrategy.ValueString()
	}

	if !plan.MaxSurgePercent.IsNull() {
		v := int(plan.MaxSurgePercent.ValueInt64())
		body.MaxSurgePercent = &v
	}

	connIDs, diags := connectionIDsToAPI(ctx, plan.ConnectionIDs)
	if diags.HasError() {
		return nil, fmt.Errorf("invalid connection_ids: %v", diags)
	}
	body.ConnectionIDs = connIDs

	return body, nil
}

// connectionIDsToAPI converts the connection_ids attribute to the *[]string the wire format
// needs, preserving null (preserve existing connections) vs non-null-empty (remove all) - see
// connection_ids' schema MarkdownDescription.
func connectionIDsToAPI(ctx context.Context, list types.List) (*[]string, diag.Diagnostics) {
	if list.IsNull() || list.IsUnknown() {
		return nil, nil
	}

	// Initialized non-nil so an empty HCL list ([]) still marshals to "[]", not "null" -
	// ElementsAs on a zero-element list otherwise leaves a nil slice untouched.
	ids := []string{}
	diags := list.ElementsAs(ctx, &ids, false)
	return &ids, diags
}

// resourceTagsResponse mirrors GET /api/v2/tags/resource's Response[ResourceTagsList] wrapper.
type resourceTagsResponse struct {
	Result struct {
		Tags []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"tags"`
	} `json:"result"`
}

// deleteResourceTagsRequest mirrors DeleteResourceTagsRequest, the body DELETE
// /api/v2/tags/resource expects.
type deleteResourceTagsRequest struct {
	ResourceType string   `json:"resource_type"`
	ResourceID   string   `json:"resource_id"`
	Keys         []string `json:"keys"`
}

// serviceTagsResourceType is the ResourceTagResourceType enum value for services ("service"),
// traced against resource_tags_dao.py - the generic tags system's own resource-type discriminator,
// unrelated to this provider's ServiceResult.
const serviceTagsResourceType = "service"

// tagsMapFromModel converts the tags attribute to a plain Go map, treating null/unknown as
// empty - used for diffing prior vs desired tags in Update, where "no tags configured" and
// "tags explicitly cleared" both mean "nothing should remain".
func tagsMapFromModel(ctx context.Context, m types.Map) (map[string]string, diag.Diagnostics) {
	if m.IsNull() || m.IsUnknown() {
		return map[string]string{}, nil
	}
	tags := make(map[string]string, len(m.Elements()))
	diags := m.ElementsAs(ctx, &tags, false)
	return tags, diags
}

// fetchServiceTags reads a service's tags from the generic tags system (`/api/v2/tags/resource`)
// - a completely separate endpoint from the service GET/list this provider otherwise reads from,
// since the Service read model itself omits tags entirely (traced: BaseProductionServiceV2Model
// has no tags field). Confirmed real and current-generation (api/v2), not assumed.
func fetchServiceTags(ctx context.Context, client *Client, serviceID string) (map[string]string, error) {
	path := fmt.Sprintf("/api/v2/tags/resource?resource_type=%s&resource_id=%s",
		serviceTagsResourceType, url.QueryEscape(serviceID))

	tagsResp, err := DoRequestAndParse[resourceTagsResponse](ctx, client, "GET", path, nil, http.StatusOK)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch service tags: %w", err)
	}

	tags := make(map[string]string, len(tagsResp.Result.Tags))
	for _, t := range tagsResp.Result.Tags {
		tags[t.Key] = t.Value
	}
	return tags, nil
}

// removedTagKeys returns the keys present in oldTags but absent from newTags - a pure,
// independently-testable predicate for computeServiceTagsToDelete's caller, mirroring
// evaluateServiceState's split between predicate and effectful caller.
func removedTagKeys(oldTags, newTags map[string]string) []string {
	var removed []string
	for k := range oldTags {
		if _, stillPresent := newTags[k]; !stillPresent {
			removed = append(removed, k)
		}
	}
	return removed
}

// upsertResourceTagsRequest mirrors UpsertResourceTagsRequest, the body PUT
// /api/v2/tags/resource expects.
type upsertResourceTagsRequest struct {
	ResourceType string            `json:"resource_type"`
	ResourceID   string            `json:"resource_id"`
	Tags         map[string]string `json:"tags"`
}

// reconcileServiceTags deletes any tag key present in oldTags but absent from newTags - the
// delete half of syncServiceTags, split out because it is independently useful (e.g. Create
// calling it with an empty oldTags is always a no-op, cheap to keep symmetric with Update).
//
// This half exists because the tags system has no replace-all endpoint (traced:
// ResourceTagsDAO.upsert_tags/upsert_resource_tags only add/update keys present in the request -
// `if not tags: return []` short-circuits an empty map as a no-op rather than clearing existing
// tags). Without it, removing a key from `tags` in HCL would upsert nothing, Read would keep
// observing the stale key via fetchServiceTags, and the plan would want to remove it again on
// every subsequent apply - a permanent, unfixable-via-apply diff, which is worse than not
// tracking tags at all.
func reconcileServiceTags(ctx context.Context, client *Client, serviceID string, oldTags, newTags map[string]string) error {
	keys := removedTagKeys(oldTags, newTags)
	if len(keys) == 0 {
		return nil
	}

	body, err := MarshalRequestBody(deleteResourceTagsRequest{
		ResourceType: serviceTagsResourceType,
		ResourceID:   serviceID,
		Keys:         keys,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal tag deletion request: %w", err)
	}

	if _, err := DoRequestRaw(ctx, client, "DELETE", "/api/v2/tags/resource", body, http.StatusOK); err != nil {
		return fmt.Errorf("failed to delete removed tags %v: %w", keys, err)
	}
	return nil
}

// upsertServiceTagsMap adds/updates every key in tags (a harmless no-op for any key whose value
// is unchanged) - the upsert half of syncServiceTags. A no-op on an empty map (nothing to add).
func upsertServiceTagsMap(ctx context.Context, client *Client, serviceID string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}

	body, err := MarshalRequestBody(upsertResourceTagsRequest{
		ResourceType: serviceTagsResourceType,
		ResourceID:   serviceID,
		Tags:         tags,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal tag upsert request: %w", err)
	}

	if _, err := DoRequestRaw(ctx, client, "PUT", "/api/v2/tags/resource", body, http.StatusOK, http.StatusNoContent); err != nil {
		return fmt.Errorf("failed to upsert tags: %w", err)
	}
	return nil
}

// syncServiceTags reconciles a service's tags to exactly match newTags: deletes anything
// removed, then upserts everything desired. Called independently of whether a deploy happens
// (H2, contract section H) - unlike ray_serve_config/build_id/compute_config_id, tags live in a
// system with no version/rollout concept at all, so a tags-only change must not trigger a
// PUT /apply + rollout wait on an otherwise-unchanged, healthy running service.
func syncServiceTags(ctx context.Context, client *Client, serviceID string, oldTags, newTags map[string]string) error {
	if err := reconcileServiceTags(ctx, client, serviceID, oldTags, newTags); err != nil {
		return err
	}
	return upsertServiceTagsMap(ctx, client, serviceID, newTags)
}

// refreshServiceTagsIntoModel fetches a service's current tags and overwrites *target with them
// - but only when the fetch found at least one tag. An empty result is genuinely ambiguous
// between "no tags were ever configured" (target should stay null) and "tags were explicitly
// set to {}" (target should stay a real, empty, non-null map) - the tags system has no way to
// distinguish those two states, so this leaves *target untouched rather than collapsing a
// legitimate empty map to null or vice versa. A fetch failure is logged and non-fatal (tolerates
// this secondary call failing without losing the rest of a Create/Read/Update), matching this
// provider's tolerant-secondary-fetch convention elsewhere.
func refreshServiceTagsIntoModel(ctx context.Context, client *Client, serviceID string, target *types.Map) {
	tags, err := fetchServiceTags(ctx, client, serviceID)
	if err != nil {
		tflog.Warn(ctx, "Failed to refresh service tags, leaving prior value", map[string]any{
			"service_id": serviceID, "error": err.Error(),
		})
		return
	}

	if len(tags) == 0 {
		// H4 (contract section H): an empty *fetch* is still ambiguous (see doc comment above),
		// but *target already resolves that ambiguity for us - if it was null (never
		// configured), leave it null; otherwise it was a real (possibly non-empty) map, so
		// collapse it to a real empty map rather than leaving a stale non-empty value in place.
		// This is what catches full-removal-of-all-tags-out-of-band as drift; a partial removal
		// was already caught by the non-empty branch below.
		if target.IsNull() {
			return
		}
		emptyValue, diags := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		if diags.HasError() {
			tflog.Warn(ctx, "Failed to build empty tags map, leaving prior value", map[string]any{"service_id": serviceID})
			return
		}
		*target = emptyValue
		return
	}

	tagsValue, diags := types.MapValueFrom(ctx, types.StringType, tags)
	if diags.HasError() {
		tflog.Warn(ctx, "Failed to convert fetched service tags, leaving prior value", map[string]any{"service_id": serviceID})
		return
	}
	*target = tagsValue
}

// findExistingServiceIDs lists services matching (name, project_id) for the Create-adoption
// guard. Unlike ServiceDataSource.findServiceByName, this returns every matching ID rather than
// resolving/erroring internally - Create decides that any match at all is a hard stop.
//
// If projectID is empty (the user omitted project_id, letting the backend resolve a default -
// see project_id's schema MarkdownDescription), this can only filter by name across every
// project the token can see, which is broader than what the backend will actually collide
// against once it resolves a specific default project. This over-blocks rather than
// under-checks: a false-positive block on an unrelated same-named service in another project is
// judged safer than silently adopting a real collision, consistent with why this guard exists.
// existingServiceMatch identifies one Create-adoption-guard collision - both fields are named
// in the resulting error (F5, contract section F) so the fix is self-service even in the
// broader, project_id-omitted search (see findExistingServiceIDs' own doc comment).
type existingServiceMatch struct {
	ID        string
	ProjectID string
}

func findExistingServiceIDs(ctx context.Context, client *Client, name, projectID string) ([]existingServiceMatch, error) {
	params := url.Values{}
	params.Add("name", name)
	if projectID != "" {
		params.Add("project_id", projectID)
	}

	results, err := PaginatedRequest(ctx, client, "/api/v2/services-v2", params,
		func(body []byte) ([]ServiceResult, *string, error) {
			var servicesResp ServicesListResponse
			if err := json.Unmarshal(body, &servicesResp); err != nil {
				return nil, nil, err
			}
			return servicesResp.Results, servicesResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list services: %w", err)
	}

	var matches []existingServiceMatch
	for _, s := range results {
		if s.Name != name {
			continue
		}
		if projectID != "" && s.ProjectID != projectID {
			continue
		}
		matches = append(matches, existingServiceMatch{ID: s.ID, ProjectID: s.ProjectID})
	}
	return matches, nil
}

// populateServiceResourceModelComputed maps a ServiceResult into the resource model's computed
// output fields ONLY (not the authored inputs - callers handle those). Delegates to the SAME
// service_conversion.go helpers the anyscale_service/anyscale_services data sources use
// (serviceVersionResultToModel, serviceObservabilityURLsToModel, serviceStatusChecklistToModel)
// rather than re-implementing the mapping, so this resource inherits their null-vs-empty and
// enum discipline instead of re-litigating it.
func populateServiceResourceModelComputed(ctx context.Context, m *ServiceResourceModel, s *ServiceResult) diag.Diagnostics {
	var diags diag.Diagnostics

	m.ID = types.StringValue(s.ID)
	m.CloudID = types.StringValue(s.CloudID)
	m.Hostname = types.StringValue(s.Hostname)
	m.BaseURL = types.StringValue(s.BaseURL)
	m.CurrentState = types.StringValue(s.CurrentState)
	m.GoalState = types.StringValue(s.GoalState)
	m.CreatorID = types.StringValue(s.CreatorID)
	m.CreatedAt = types.StringValue(s.CreatedAt)
	m.EndedAt = types.StringPointerValue(s.EndedAt)
	m.IsMultiVersion = types.BoolValue(s.IsMultiVersion)
	m.AutoRolloutEnabled = types.BoolValue(s.AutoRolloutEnabled)
	m.ErrorMessage = types.StringPointerValue(s.ErrorMessage)

	obsURLs := serviceObservabilityURLsToModel(s.ServiceObservabilityURLs)
	obsURLsObj, obsDiags := types.ObjectValueFrom(ctx, serviceObservabilityURLsAttrTypes, obsURLs)
	diags.Append(obsDiags...)
	m.ServiceObservabilityURLs = obsURLsObj

	primaryVersion, vDiags := serviceVersionResultToModel(ctx, s.PrimaryVersion)
	diags.Append(vDiags...)
	primaryObj, pObjDiags := types.ObjectValueFrom(ctx, serviceVersionAttrTypes, primaryVersion)
	diags.Append(pObjDiags...)
	m.PrimaryVersion = primaryObj

	if s.CanaryVersion != nil {
		canaryVersion, cDiags := serviceVersionResultToModel(ctx, *s.CanaryVersion)
		diags.Append(cDiags...)
		canaryObj, cObjDiags := types.ObjectValueFrom(ctx, serviceVersionAttrTypes, canaryVersion)
		diags.Append(cObjDiags...)
		m.CanaryVersion = canaryObj
	} else {
		m.CanaryVersion = types.ObjectNull(serviceVersionAttrTypes)
	}

	if checklist := serviceStatusChecklistToModel(s.ServiceStatusChecklist); checklist != nil {
		checklistObj, chkDiags := types.ObjectValueFrom(ctx, serviceStatusChecklistAttrTypes, *checklist)
		diags.Append(chkDiags...)
		m.ServiceStatusChecklist = checklistObj
	} else {
		m.ServiceStatusChecklist = types.ObjectNull(serviceStatusChecklistAttrTypes)
	}

	return diags
}
