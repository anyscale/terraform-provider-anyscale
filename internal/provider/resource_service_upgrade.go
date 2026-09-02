package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/dynamicplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// UpgradeState is PR2's timeouts{} migration for anyscale_service - this
// resource's first-ever schema version bump. v0 (rollout_timeout as a flat,
// always-materialized Optional+Computed+Default string) drops that
// attribute and adopts the new (null-unless-set) timeouts{ create, update,
// delete } block, one value governing all three ops exactly as
// rollout_timeout did.
func (r *ServiceResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema:   serviceResourceSchemaV0(),
			StateUpgrader: upgradeServiceResourceStateV0toV1,
		},
	}
}

// serviceResourceModelV0 mirrors ServiceResourceModel exactly, but with the
// flat rollout_timeout string in place of the timeouts.Value block field -
// the only shape difference between v0 and the current (v1) schema.
type serviceResourceModelV0 struct {
	Name            types.String  `tfsdk:"name"`
	RayServeConfig  types.Dynamic `tfsdk:"ray_serve_config"`
	BuildID         types.String  `tfsdk:"build_id"`
	ComputeConfigID types.String  `tfsdk:"compute_config_id"`

	Description types.String `tfsdk:"description"`
	ProjectID   types.String `tfsdk:"project_id"`
	Tags        types.Map    `tfsdk:"tags"`

	RolloutStrategy types.String `tfsdk:"rollout_strategy"`
	MaxSurgePercent types.Int64  `tfsdk:"max_surge_percent"`
	ConnectionIDs   types.List   `tfsdk:"connection_ids"`
	RolloutTimeout  types.String `tfsdk:"rollout_timeout"`

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

// serviceResourceSchemaV0 is a frozen copy of anyscale_service's schema
// exactly as shipped through v0.19.0 (pre-PR2) - see cloudResourceSchemaV0's
// doc comment (resource_cloud_upgrade.go) for why flags don't need to match
// historical values exactly but names/types/structure must, and why this
// must not evolve alongside the live schema.
func serviceResourceSchemaV0() *schema.Schema {
	return &schema.Schema{
		Version: 0,
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
				Required: true,
				MarkdownDescription: "The ID of the compute config this service uses, e.g. from `anyscale_compute_config`. Pins the cloud the service runs in - there is no separate top-level `cloud_id` argument because it is fully determined by this value. Changing this rolls out a new service version; it is not a replace. " +
					"See `project_id` for the plan-time check that catches a mismatch between this value's cloud and an explicitly-set `project_id`'s cloud.",
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
					"Immutable: the backend has no move-project endpoint, so changing this (including changing which project an omitted value would resolve to, e.g. by changing `compute_config_id` to a different cloud) replaces the resource. " +
					"When explicitly set, its own cloud must match `compute_config_id`'s cloud - a plan-time check catches a mismatch here (naming both cloud IDs and a remedy) rather than letting the backend reject the cluster after apply.",
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
				MarkdownDescription: "Either `ROLLOUT` (default) or `IN_PLACE`. Controls how UPDATES roll in a new version - the initial create always performs a standard deploy, since there is no existing version yet to upgrade in place (the backend rejects `IN_PLACE` outright on a fresh create), so this attribute can be set from the start and left unchanged across create and every later update. `ROLLOUT` deploys the new version on a newly started cluster and shifts traffic over, then converges to RUNNING. `IN_PLACE` upgrades the existing cluster in place - faster, but the backend permits changing only `ray_serve_config` under it; changing `build_id`, `compute_config_id`, or `connection_ids` in the same apply as `rollout_strategy = \"IN_PLACE\"` is rejected at plan time (see this resource's plan-time validation) rather than left to fail opaquely at apply. " +
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
				Default:             stringdefault.StaticString("45m"),
				MarkdownDescription: "Maximum time to wait for a create or update rollout to reach `RUNNING`, or for destroy to wait for termination to reach `TERMINATED` before deleting (e.g. `30m`, `1h`). Defaults to `45m` - a standard `ROLLOUT` genuinely takes tens of minutes on real infra (a full second cluster spins up before the gradual canary traffic-shift even starts), so this default is sized with real-world headroom, not just the minimum a bare test app needs.",
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
				Computed: true,
				MarkdownDescription: "Dashboard URLs for this service. The whole block is null while a service is still being processed (a confirmed real transitional state, e.g. a not-yet-healthy service that has not finished its first reconcile). " +
					"Once present, each individual URL is separately null if the backend has none to report for it (e.g. before the service's first successful deploy).",
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
				MarkdownDescription: "The primary (currently active) version of this service, as last observed from the server - including its own `ray_serve_config`/`connection_ids`, which reflect the live server-side state rather than this resource's authored `ray_serve_config`/`connection_ids` inputs above. Compare the two if you suspect drift. Can be null if the backend has not returned it yet.",
				Attributes:          serviceVersionResourceAttributes(),
			},
			"canary_version": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The canary version of this service - a point-in-time snapshot of an in-progress rollout, present only while one is active and null once the service settles. Because it disappears predictably as soon as the rollout completes, treat it as observational (e.g. for dashboards or monitoring) rather than wiring it as a stable input elsewhere - anything that depends on it will see it go null on its own, with no configuration change on your end.",
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

// upgradeServiceResourceStateV0toV1 drops rollout_timeout and leaves the new
// timeouts block null - a null timeouts{} resolves to
// defaultServiceRolloutTimeout on the next apply via
// plan/state.Timeouts.Create/Update/Delete(ctx, default), exactly matching
// what an omitted rollout_timeout used to resolve to via its Default. No
// information is lost: a user who had customized rollout_timeout away from
// the old default would already see that as a real, non-null value here -
// see the state-upgrade test for the customized-value case, not just the
// default one.
func upgradeServiceResourceStateV0toV1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 0 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState serviceResourceModelV0
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState := ServiceResourceModel{
		Name:                     priorState.Name,
		RayServeConfig:           priorState.RayServeConfig,
		BuildID:                  priorState.BuildID,
		ComputeConfigID:          priorState.ComputeConfigID,
		Description:              priorState.Description,
		ProjectID:                priorState.ProjectID,
		Tags:                     priorState.Tags,
		RolloutStrategy:          priorState.RolloutStrategy,
		MaxSurgePercent:          priorState.MaxSurgePercent,
		ConnectionIDs:            priorState.ConnectionIDs,
		Timeouts:                 timeouts.Value{Object: types.ObjectNull(map[string]attr.Type{"create": types.StringType, "update": types.StringType, "delete": types.StringType})},
		ID:                       priorState.ID,
		CloudID:                  priorState.CloudID,
		Hostname:                 priorState.Hostname,
		BaseURL:                  priorState.BaseURL,
		CurrentState:             priorState.CurrentState,
		GoalState:                priorState.GoalState,
		CreatorID:                priorState.CreatorID,
		CreatedAt:                priorState.CreatedAt,
		EndedAt:                  priorState.EndedAt,
		IsMultiVersion:           priorState.IsMultiVersion,
		AutoRolloutEnabled:       priorState.AutoRolloutEnabled,
		ErrorMessage:             priorState.ErrorMessage,
		ServiceObservabilityURLs: priorState.ServiceObservabilityURLs,
		PrimaryVersion:           priorState.PrimaryVersion,
		CanaryVersion:            priorState.CanaryVersion,
		ServiceStatusChecklist:   priorState.ServiceStatusChecklist,
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}
