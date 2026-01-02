package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &GlobalResourceSchedulersDataSource{}
	_ datasource.DataSourceWithConfigure = &GlobalResourceSchedulersDataSource{}
)

// NewGlobalResourceSchedulersDataSource creates a new global resource schedulers data source.
func NewGlobalResourceSchedulersDataSource() datasource.DataSource {
	return &GlobalResourceSchedulersDataSource{}
}

// GlobalResourceSchedulersDataSource defines the data source implementation.
type GlobalResourceSchedulersDataSource struct {
	client *Client
}

// GlobalResourceSchedulersDataSourceModel describes the data source data model.
type GlobalResourceSchedulersDataSourceModel struct {
	// Filter inputs (all optional)
	NameContains types.String `tfsdk:"name_contains"`

	// Computed output
	MachinePools []MachinePoolSummaryModel `tfsdk:"machine_pools"`
}

// MachinePoolSummaryModel represents a global resource scheduler in the list.
type MachinePoolSummaryModel struct {
	ID                            types.String `tfsdk:"id"`
	Name                          types.String `tfsdk:"name"`
	OrganizationID                types.String `tfsdk:"organization_id"`
	EnableRootlessDataplaneConfig types.Bool   `tfsdk:"enable_rootless_dataplane_config"`
	CloudIDs                      types.List   `tfsdk:"cloud_ids"`
}

// Metadata returns the data source type name.
func (d *GlobalResourceSchedulersDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_global_resource_schedulers"
}

// Schema defines the schema for the data source.
func (d *GlobalResourceSchedulersDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists and filters Anyscale Global Resource Schedulers. This data source returns a list of global resource schedulers without detailed spec for performance.",

		Attributes: map[string]schema.Attribute{
			"name_contains": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter global resource schedulers by partial name match.",
			},
			"machine_pools": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "List of global resource schedulers matching the filters.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The unique identifier of the global resource scheduler.",
						},
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The name of the global resource scheduler.",
						},
						"organization_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The organization ID that owns the global resource scheduler.",
						},
						"enable_rootless_dataplane_config": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "Whether rootless dataplane configuration is enabled.",
						},
						"cloud_ids": schema.ListAttribute{
							Computed:            true,
							ElementType:         types.StringType,
							MarkdownDescription: "List of cloud IDs attached to this global resource scheduler.",
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *GlobalResourceSchedulersDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Read refreshes the Terraform state with the latest data.
func (d *GlobalResourceSchedulersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config GlobalResourceSchedulersDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Listing global resource schedulers", map[string]any{
		"name_contains": config.NameContains.ValueString(),
	})

	// List all global resource schedulers
	listResp, err := DoRequestAndParse[ListMachinePoolsResponse](
		ctx,
		d.client,
		"GET",
		"/api/v2/machine_pools/",
		nil,
		http.StatusOK,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "list global resource schedulers", err)
		return
	}

	// Filter and convert results
	machinePools := make([]MachinePoolSummaryModel, 0, len(listResp.Result.MachinePools))

	nameFilter := config.NameContains.ValueString()

	for _, pool := range listResp.Result.MachinePools {
		// Apply name filter if specified
		if nameFilter != "" && !strings.Contains(pool.MachinePoolName, nameFilter) {
			continue
		}

		// Convert cloud IDs to list
		cloudIDs := make([]string, len(pool.CloudIDs))
		copy(cloudIDs, pool.CloudIDs)
		cloudIDsList, diags := types.ListValueFrom(ctx, types.StringType, cloudIDs)
		if diags.HasError() {
			resp.Diagnostics.Append(diags...)
			return
		}

		poolModel := MachinePoolSummaryModel{
			ID:                            types.StringValue(pool.MachinePoolID),
			Name:                          types.StringValue(pool.MachinePoolName),
			OrganizationID:                types.StringValue(pool.OrganizationID),
			EnableRootlessDataplaneConfig: types.BoolValue(pool.EnableRootlessDataplaneConfig),
			CloudIDs:                      cloudIDsList,
		}

		machinePools = append(machinePools, poolModel)
	}

	tflog.Info(ctx, "Machine pools listed", map[string]any{
		"count": len(machinePools),
	})

	// Populate config
	config.MachinePools = machinePools

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
