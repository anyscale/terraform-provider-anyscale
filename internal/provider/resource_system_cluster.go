package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// defaultSystemClusterStartTimeout is sized against assayer's live smoke
// test (~49s to reach Running on the static fixture cloud) with generous
// real-world headroom - see start_timeout's MarkdownDescription.
const defaultSystemClusterStartTimeout = "20m"

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &SystemClusterResource{}
	_ resource.ResourceWithConfigure   = &SystemClusterResource{}
	_ resource.ResourceWithImportState = &SystemClusterResource{}
)

// NewSystemClusterResource creates a new System Cluster resource. Named
// SystemClusterResource/SystemClusterResourceModel (not a bare
// "SystemCluster*" prefix) to stay clearly distinct from
// CloudResourceModel's EnableSystemCluster-shaped history - see the design
// record for the naming-collision ruling.
func NewSystemClusterResource() resource.Resource {
	return &SystemClusterResource{}
}

// SystemClusterResource defines the resource implementation.
type SystemClusterResource struct {
	client *Client
}

// SystemClusterResourceModel describes the resource data model. Shared with
// the data source (data_source_system_cluster.go) since both surface the
// same observed fields.
type SystemClusterResourceModel struct {
	ID                 types.String `tfsdk:"id"`
	CloudID            types.String `tfsdk:"cloud_id"`
	ClusterID          types.String `tfsdk:"cluster_id"`
	State              types.String `tfsdk:"state"`
	IsEnabled          types.Bool   `tfsdk:"is_enabled"`
	WorkloadServiceURL types.String `tfsdk:"workload_service_url"`
	StartTimeout       types.String `tfsdk:"start_timeout"`
}

func (r *SystemClusterResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_system_cluster"
}

func (r *SystemClusterResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: `Ensures the System Cluster for an Anyscale Cloud - the always-on cluster that backs the task and actor observability dashboards - is enabled and running. Declarative: applying this resource enables the System Cluster if it is not already enabled, starts it if it is terminated, and waits until it reaches a healthy ` + "`RUNNING`" + ` state. Re-applying against an already-running cluster is a no-op.

A cloud's System Cluster is tied to that cloud's primary (default) ` + "`anyscale_cloud_resource`" + ` - a secondary cloud resource on the same cloud never gets a working System Cluster of its own today. Anyscale engineering is actively working on multi-resource System Cluster support; this limitation is not enforced by this resource (the Anyscale API currently exposes no way to detect or check it), so it is a documentation caveat rather than a plan/apply-time guard.

~> **Note:** ` + "`terraform destroy`" + ` only removes this resource from Terraform state. It does not stop, disable, or terminate the underlying System Cluster, which keeps running afterward. This provider does not support stopping or disabling the System Cluster from this resource - use the Anyscale console's Clouds > Settings > Observability page, the ` + "`anyscale cloud terminate-system-cluster`" + ` CLI command, or the ` + "`anyscale.cloud.terminate_system_cluster`" + ` SDK call to do so directly. A future stop/restart capability may arrive later as a separate provider Action layered on top of this resource, not as a change to this resource's converging lifecycle (the same design choice this provider already made for ` + "`anyscale_service`" + `'s canary promote/rollback).`,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				MarkdownDescription: "Mirrors `cloud_id`. Present for import/tooling convention; use `cloud_id` for the real identity.",
			},
			"cloud_id": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				MarkdownDescription: "The ID of the Anyscale Cloud whose System Cluster this resource manages. A cloud's System Cluster is always tied to its primary (default) `anyscale_cloud_resource`, never a secondary one - see the top-level description. Changing this value replaces the resource (it targets a different Cloud's System Cluster, not an update to this one).",
			},
			"cluster_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The System Cluster's own identifier. Null until the cluster has been created at least once (which happens automatically as part of this resource's `Create`) - a cloud that has never had its System Cluster started has no `cluster_id` yet.",
			},
			"state": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The System Cluster's current status (e.g. `Running`, `StartingUp`, `Terminated`, `Terminating`, `StartupErrored`). Ships as a plain string with no client-side enum validation, matching this provider's convention of not hand-maintaining a copy of the backend's enum list. Refreshed on every `terraform plan`/`apply` - if the cluster is terminated outside Terraform (e.g. from the console, or by the backend's own idle auto-termination), this reflects `Terminated` rather than hiding the change; re-run `terraform apply -replace` against this resource to start it again (this resource does not auto-recreate on an observed termination).",
			},
			"is_enabled": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the System Cluster is enabled for this cloud. Always `true` once this resource has been successfully created or imported - `Create`/`Update` unconditionally ensure the System Cluster is enabled before starting it, so there is no separate user-facing toggle on this resource for enable-vs-disable (unlike the removed `anyscale_cloud.enable_system_cluster`, which this resource supersedes). Exposed read-only for observability: `is_enabled = true` together with a non-running `state` is a real, valid combination (e.g. the cluster is enabled but has since been terminated), not something this attribute collapses away.",
			},
			"workload_service_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The URL the task and actor observability dashboards use to reach this System Cluster's workload service. Null until the cluster has been created and reaches a state where this URL is populated.",
			},
			"start_timeout": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(defaultSystemClusterStartTimeout),
				MarkdownDescription: "Maximum time to wait for the System Cluster to reach `Running` after a create or update triggers a start (e.g. `10m`, `30m`). Defaults to `20m` - a real start observed on a live cloud took about 49 seconds, so this default carries substantial real-world headroom rather than just the minimum a fast environment needs. Purely local to this provider - never sent to or read from the Anyscale API.",
			},
		},
	}
}

func (r *SystemClusterResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create ensures the System Cluster is enabled and running: enable-then-start
// (order-dependent, per assayer's Q5 trace - starting a disabled cluster is a
// silent no-op), persist state immediately once cluster_id is known (before
// the poll, so a later timeout/failure never orphans a real backend cluster
// with zero Terraform record - mirrors resource_service.go's Create), then
// poll to Running.
func (r *SystemClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SystemClusterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, err := time.ParseDuration(plan.StartTimeout.ValueString())
	if err != nil {
		AddConfigError(&resp.Diagnostics, "Invalid Start Timeout", err.Error())
		return
	}

	cloudID := plan.CloudID.ValueString()

	tflog.Info(ctx, "Enabling System Cluster", map[string]any{"cloud_id": cloudID})
	if err := enableSystemCluster(ctx, r.client, cloudID, true); err != nil {
		AddAPIError(&resp.Diagnostics, "enable system cluster", err)
		return
	}

	tflog.Info(ctx, "Starting System Cluster", map[string]any{"cloud_id": cloudID})
	started, err := describeSystemWorkload(ctx, r.client, cloudID, true)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "start system cluster", err)
		return
	}

	populateSystemClusterResourceModel(&plan, started)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	final, err := waitForSystemClusterState(ctx, r.client, cloudID, systemClusterStateRunning, timeout)
	if final != nil {
		populateSystemClusterResourceModel(&plan, final)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		AddAPIError(&resp.Diagnostics, "wait for system cluster to start", err)
		return
	}
}

// Read is the side-effect-free two-call oracle+status flow: decorated_sessions
// (via findSystemWorkloadCluster) confirms existence without ever risking the
// create-on-read hazard (which only fires when no cluster yet exists);
// describeSystemWorkload with startCluster=false is then safe unconditionally,
// since existence is already confirmed. Not-found removes the resource from
// state, mirroring resource_service.go's 404 handling - a System Cluster
// whose underlying session is gone entirely (not just Terminated, which
// decorated_sessions still finds) is a heavier drift than this resource's
// reflect-only design covers.
func (r *SystemClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SystemClusterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := state.CloudID.ValueString()

	found, err := findSystemWorkloadCluster(ctx, r.client, cloudID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "check for system cluster", err)
		return
	}
	if found == nil {
		tflog.Warn(ctx, "System Cluster not found, removing from state", map[string]any{"cloud_id": cloudID})
		resp.State.RemoveResource(ctx)
		return
	}

	described, err := describeSystemWorkload(ctx, r.client, cloudID, false)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read system cluster", err)
		return
	}

	populateSystemClusterResourceModel(&state, described)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only ever runs for a start_timeout-only change (cloud_id is
// RequiresReplace, and no other attribute is configurable) - per the design
// record, this needs no enable/start call, just an accurate refresh of the
// Computed fields alongside adopting the new timeout value.
func (r *SystemClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan SystemClusterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := plan.CloudID.ValueString()

	found, err := findSystemWorkloadCluster(ctx, r.client, cloudID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "check for system cluster", err)
		return
	}
	if found == nil {
		AddAPIError(&resp.Diagnostics, "update system cluster", fmt.Errorf("system cluster for cloud %s was not found - it may have been removed outside Terraform; re-import or replace this resource", cloudID))
		return
	}

	described, err := describeSystemWorkload(ctx, r.client, cloudID, false)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read system cluster", err)
		return
	}

	populateSystemClusterResourceModel(&plan, described)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes this resource from Terraform state only. It never calls
// terminate/disable - see the schema's top-level Note. This resource's
// entire purpose is ensuring the System Cluster runs; nothing about
// removing Terraform's record of that should stop it.
func (r *SystemClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SystemClusterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "Removing System Cluster from Terraform state only (not stopping the real cluster)", map[string]any{"cloud_id": state.CloudID.ValueString()})
}

// ImportState imports by cloud_id alone - the resource's real identity
// (assayer's Q3: DB-unique exactly-one-per-cloud). No compound ID needed,
// unlike anyscale_cloud_resource's cloud_id:name form.
func (r *SystemClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("cloud_id"), req, resp)
}

// populateSystemClusterResourceModel writes a describeSystemWorkload result
// into the model's Computed fields, including id (always mirrors cloud_id -
// see the schema doc). Shared by Create/Read/Update so all three stay
// consistent - a divergence here (e.g. Read forgetting to set id, which
// Create originally did inline) is exactly the kind of easy-to-miss bug that
// only surfaces later as an ImportStateVerify mismatch, confirmed by
// TestAccSystemClusterResource_ImportByCloudID failing before this fix.
func populateSystemClusterResourceModel(m *SystemClusterResourceModel, d *DescribeSystemWorkloadResult) {
	m.ID = m.CloudID
	m.ClusterID = types.StringPointerValue(d.ClusterID)
	m.State = types.StringPointerValue(d.Status)
	m.IsEnabled = types.BoolValue(d.IsEnabled)
	m.WorkloadServiceURL = types.StringPointerValue(d.WorkloadServiceURL)
}
