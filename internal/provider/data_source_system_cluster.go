package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &SystemClusterDataSource{}
	_ datasource.DataSourceWithConfigure = &SystemClusterDataSource{}
)

// NewSystemClusterDataSource creates a new System Cluster data source.
func NewSystemClusterDataSource() datasource.DataSource {
	return &SystemClusterDataSource{}
}

// SystemClusterDataSource defines the data source implementation.
type SystemClusterDataSource struct {
	client *Client
}

// SystemClusterDataSourceModel describes the data source data model. This is
// a genuinely new shape for this provider: a required parent ID (cloud_id)
// that resolves to exactly one child object, rather than a zero-argument
// connection-level singleton (anyscale_organization/anyscale_user) or an
// either/or self-identified lookup (anyscale_cloud's id/name).
type SystemClusterDataSourceModel struct {
	CloudID            types.String `tfsdk:"cloud_id"`
	ClusterID          types.String `tfsdk:"cluster_id"`
	State              types.String `tfsdk:"state"`
	IsEnabled          types.Bool   `tfsdk:"is_enabled"`
	WorkloadServiceURL types.String `tfsdk:"workload_service_url"`
}

func (d *SystemClusterDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_system_cluster"
}

func (d *SystemClusterDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: `Reads the current status of an Anyscale Cloud's System Cluster - the always-on cluster that backs the task and actor observability dashboards. Read-only and always side-effect-free: unlike a naive status check built directly on the describe endpoint, this data source never creates, starts, or otherwise provisions anything as a side effect of being read, regardless of whether the System Cluster has ever been created (it first confirms existence via a side-effect-free existence check, and only asks for detailed status once that existence is confirmed).

If the cloud has no System Cluster yet (never enabled, or enabled but never started), this data source returns ` + "`cluster_id = null`" + `, ` + "`state = null`" + `, ` + "`is_enabled = null`" + `, and ` + "`workload_service_url = null`" + ` rather than an error - a cloud without a System Cluster is an expected, valid state, not a misconfiguration.

Companion to the ` + "`anyscale_system_cluster`" + ` resource, which shares this data source's computed field shapes and is the only way to enable and start a System Cluster through this provider.`,
		Attributes: map[string]schema.Attribute{
			"cloud_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The ID of the Anyscale Cloud whose System Cluster to look up.",
			},
			"cluster_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The System Cluster's own identifier, or `null` if the cloud has no System Cluster yet.",
			},
			"state": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The System Cluster's current status (e.g. `Running`, `StartingUp`, `Terminated`, `Terminating`, `StartupErrored`), or `null` if the cloud has no System Cluster yet. Ships as a plain string with no client-side enum validation, matching this provider's convention of not hand-maintaining a copy of the backend's enum list.",
			},
			"is_enabled": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the System Cluster is enabled for this cloud, or `null` if the cloud has no System Cluster yet - confirming a true `false` in that case would require a call with a real side effect, which this data source deliberately never makes.",
			},
			"workload_service_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The URL the task and actor observability dashboards use to reach this System Cluster's workload service, or `null` if the cloud has no System Cluster yet.",
			},
		},
	}
}

func (d *SystemClusterDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = client
}

// Read is the same side-effect-free two-call oracle+status flow as the
// resource's Read (see resource_system_cluster.go) - existence via
// findSystemWorkloadCluster first, describeSystemWorkload only once that
// confirms a cluster exists. A not-found result is a clean, valid "no
// System Cluster" answer, not an error - never RemoveResource-equivalent
// behavior here, since a data source has no prior state to remove.
func (d *SystemClusterDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config SystemClusterDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := config.CloudID.ValueString()

	found, err := findSystemWorkloadCluster(ctx, d.client, cloudID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "check for system cluster", err)
		return
	}
	if found == nil {
		config.ClusterID = types.StringNull()
		config.State = types.StringNull()
		config.IsEnabled = types.BoolNull()
		config.WorkloadServiceURL = types.StringNull()
		resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
		return
	}

	described, err := describeSystemWorkload(ctx, d.client, cloudID, false)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read system cluster", err)
		return
	}

	config.ClusterID = types.StringPointerValue(described.ClusterID)
	config.State = types.StringPointerValue(described.Status)
	config.IsEnabled = types.BoolValue(described.IsEnabled)
	config.WorkloadServiceURL = types.StringPointerValue(described.WorkloadServiceURL)
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
