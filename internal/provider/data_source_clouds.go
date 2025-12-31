package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &CloudsDataSource{}
	_ datasource.DataSourceWithConfigure = &CloudsDataSource{}
)

// NewCloudsDataSource creates a new clouds data source.
func NewCloudsDataSource() datasource.DataSource {
	return &CloudsDataSource{}
}

// CloudsDataSource defines the data source implementation.
type CloudsDataSource struct {
	client *Client
}

// CloudsDataSourceModel describes the data source data model.
type CloudsDataSourceModel struct {
	// Filter inputs (all optional)
	NameContains  types.String `tfsdk:"name_contains"`
	CloudProvider types.String `tfsdk:"cloud_provider"`
	Region        types.String `tfsdk:"region"`

	// Computed output
	Clouds []CloudSummaryModel `tfsdk:"clouds"`
}

// CloudSummaryModel represents a cloud in the list.
type CloudSummaryModel struct {
	ID                      types.String `tfsdk:"id"`
	Name                    types.String `tfsdk:"name"`
	CloudProvider           types.String `tfsdk:"cloud_provider"`
	ComputeStack            types.String `tfsdk:"compute_stack"`
	Region                  types.String `tfsdk:"region"`
	Status                  types.String `tfsdk:"status"`
	State                   types.String `tfsdk:"state"`
	CreatedAt               types.String `tfsdk:"created_at"`
	CreatorID               types.String `tfsdk:"creator_id"`
	IsDefault               types.Bool   `tfsdk:"is_default"`
	IsK8s                   types.Bool   `tfsdk:"is_k8s"`
	IsAIOA                  types.Bool   `tfsdk:"is_aioa"`
	IsBringYourOwnResource  types.Bool   `tfsdk:"is_bring_your_own_resource"`
	IsPrivateCloud          types.Bool   `tfsdk:"is_private_cloud"`
	IsPrivateServiceCloud   types.Bool   `tfsdk:"is_private_service_cloud"`
	AutoAddUser             types.Bool   `tfsdk:"auto_add_user"`
	LineageTrackingEnabled  types.Bool   `tfsdk:"lineage_tracking_enabled"`
	IsAggregatedLogsEnabled types.Bool   `tfsdk:"is_aggregated_logs_enabled"`
}

// Metadata returns the data source type name.
func (d *CloudsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_clouds"
}

// Schema defines the schema for the data source.
func (d *CloudsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists and filters Anyscale Clouds. This data source returns a list of clouds with summary information.",

		Attributes: map[string]schema.Attribute{
			"name_contains": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter clouds by partial name match.",
			},
			"cloud_provider": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter clouds by provider (AWS, GCP, AZURE, GENERIC).",
			},
			"region": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter clouds by region.",
			},
			"clouds": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "List of clouds matching the filters.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The unique identifier of the cloud.",
						},
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The name of the cloud.",
						},
						"cloud_provider": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The cloud provider (AWS, GCP, AZURE, or GENERIC).",
						},
						"compute_stack": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The compute stack (VM or K8S).",
						},
						"region": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The region where the cloud is deployed.",
						},
						"status": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The operational status of the cloud.",
						},
						"state": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The lifecycle state of the cloud.",
						},
						"created_at": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Timestamp when the cloud was created.",
						},
						"creator_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The ID of the user who created the cloud.",
						},
						"is_default": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "Whether this is the default cloud for the organization.",
						},
						"is_k8s": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "Whether this cloud uses Kubernetes.",
						},
						"is_aioa": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "Whether this is an AIOA (Anyscale In Your Own Account) cloud.",
						},
						"is_bring_your_own_resource": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "Whether this cloud allows bringing your own resources.",
						},
						"is_private_cloud": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "Whether this is a private cloud.",
						},
						"is_private_service_cloud": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "Whether this is a private service cloud.",
						},
						"auto_add_user": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "Whether users are automatically added to this cloud.",
						},
						"lineage_tracking_enabled": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "Whether lineage tracking is enabled for this cloud.",
						},
						"is_aggregated_logs_enabled": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "Whether aggregated log ingestion is enabled for this cloud.",
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *CloudsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		AddConfigError(&resp.Diagnostics,
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = client
}

// Read refreshes the Terraform state with the latest data.
func (d *CloudsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config CloudsDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build query parameters
	params := url.Values{}

	if !config.NameContains.IsNull() {
		params.Add("name_contains", config.NameContains.ValueString())
	}

	if !config.CloudProvider.IsNull() {
		params.Add("provider", config.CloudProvider.ValueString())
	}

	if !config.Region.IsNull() {
		params.Add("region", config.Region.ValueString())
	}

	tflog.Debug(ctx, "Fetching clouds with filters", map[string]any{
		"filters": params.Encode(),
	})

	// Fetch clouds
	clouds, err := d.fetchClouds(ctx, params)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "list clouds", err)
		return
	}

	tflog.Info(ctx, "Clouds fetched successfully", map[string]any{"count": len(clouds)})

	// Populate config
	config.Clouds = clouds

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// fetchClouds fetches clouds with the given query parameters, handling pagination if needed.
func (d *CloudsDataSource) fetchClouds(ctx context.Context, params url.Values) ([]CloudSummaryModel, error) {
	// Use PaginatedRequest helper to fetch all clouds
	cloudResults, err := PaginatedRequest(ctx, d.client, "/api/v2/clouds", params,
		func(body []byte) ([]CloudResult, *string, error) {
			var cloudsResp CloudsListResponse
			if err := json.Unmarshal(body, &cloudsResp); err != nil {
				return nil, nil, fmt.Errorf("failed to parse clouds response: %w", err)
			}
			return cloudsResp.Results, cloudsResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return nil, err
	}

	// Convert CloudResults to CloudSummaryModels
	allClouds := make([]CloudSummaryModel, 0, len(cloudResults))
	for _, cloud := range cloudResults {
		cloudModel := CloudSummaryModel{
			ID:                      types.StringValue(cloud.ID),
			Name:                    types.StringValue(cloud.Name),
			CloudProvider:           types.StringValue(cloud.Provider),
			ComputeStack:            types.StringValue(cloud.ComputeStack),
			Region:                  types.StringValue(cloud.Region),
			Status:                  types.StringValue(cloud.Status),
			State:                   types.StringValue(cloud.State),
			CreatedAt:               types.StringValue(cloud.CreatedAt),
			CreatorID:               types.StringValue(cloud.CreatorID),
			IsDefault:               types.BoolValue(cloud.IsDefault),
			IsK8s:                   types.BoolValue(cloud.IsK8s),
			IsAIOA:                  types.BoolValue(cloud.IsAIOA),
			IsBringYourOwnResource:  types.BoolValue(cloud.IsBringYourOwnResource),
			IsPrivateCloud:          types.BoolValue(cloud.IsPrivateCloud),
			IsPrivateServiceCloud:   types.BoolValue(cloud.IsPrivateServiceCloud),
			AutoAddUser:             types.BoolValue(cloud.AutoAddUser),
			LineageTrackingEnabled:  types.BoolValue(cloud.LineageTrackingEnabled),
			IsAggregatedLogsEnabled: types.BoolValue(cloud.IsAggregatedLogsEnabled),
		}
		allClouds = append(allClouds, cloudModel)
	}

	return allClouds, nil
}
