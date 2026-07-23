package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

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
	ID                     types.String `tfsdk:"id"`
	Name                   types.String `tfsdk:"name"`
	CloudProvider          types.String `tfsdk:"cloud_provider"`
	ComputeStack           types.String `tfsdk:"compute_stack"`
	Region                 types.String `tfsdk:"region"`
	Status                 types.String `tfsdk:"status"`
	State                  types.String `tfsdk:"state"`
	CreatedAt              types.String `tfsdk:"created_at"`
	CreatorID              types.String `tfsdk:"creator_id"`
	IsDefault              types.Bool   `tfsdk:"is_default"`
	IsK8s                  types.Bool   `tfsdk:"is_k8s"`
	IsPrivateCloud         types.Bool   `tfsdk:"is_private_cloud"`
	AutoAddUser            types.Bool   `tfsdk:"auto_add_user"`
	LineageTrackingEnabled types.Bool   `tfsdk:"lineage_tracking_enabled"`
	AggregatedLogsEnabled  types.Bool   `tfsdk:"aggregated_logs_enabled"`

	// DS-CLOUD-5 (Phase B), via cloudSharedAttributes.
	AvailabilityZones types.List   `tfsdk:"availability_zones"`
	Version           types.String `tfsdk:"version"`
	ExternalID        types.String `tfsdk:"external_id"`
}

// Metadata returns the data source type name.
func (d *CloudsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_clouds"
}

// Schema defines the schema for the data source.
func (d *CloudsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	itemAttributes := cloudSharedAttributes()
	itemAttributes["id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The unique identifier of the cloud.",
	}
	itemAttributes["name"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The name of the cloud.",
	}
	itemAttributes["is_k8s"] = schema.BoolAttribute{
		Computed:            true,
		MarkdownDescription: "Whether this cloud uses Kubernetes.",
	}
	// Uniform <noun>_enabled naming, shared with the singular anyscale_cloud
	// data source and the anyscale_cloud resource. lineage_tracking_enabled
	// already matched this plural data source's pre-existing name (both
	// previously called it enable_lineage_tracking on the resource/singular
	// DS). aggregated_logs_enabled is a rename on THIS plural data source
	// too, as of this release - it previously matched the backend's own
	// is_aggregated_logs_enabled (the resource/singular DS instead called it
	// enable_log_ingestion), but the backend-exact name became the lone
	// is_-prefixed outlier once the other two surfaces unified on
	// <noun>_enabled, so all three surfaces adopt aggregated_logs_enabled
	// together in this same release. See CHANGELOG.md and
	// schema_shared_attributes.go's cloudSharedAttributes doc comment for
	// the naming-unification history.
	itemAttributes["lineage_tracking_enabled"] = schema.BoolAttribute{
		Computed:            true,
		MarkdownDescription: "Whether lineage tracking is enabled for this cloud.",
	}
	itemAttributes["aggregated_logs_enabled"] = schema.BoolAttribute{
		Computed:            true,
		MarkdownDescription: "Whether aggregated log ingestion is enabled for this cloud. Renamed from is_aggregated_logs_enabled in this release for uniform <noun>_enabled naming with lineage_tracking_enabled - see CHANGELOG.md for the migration note.",
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists and filters Anyscale Clouds. This data source returns a list of clouds with summary information. The per-cloud `cloud_resource_id` is deliberately omitted here to avoid an extra API call per cloud in the list - use the `anyscale_cloud` data source or the `anyscale_cloud`/`anyscale_cloud_resource` resources to look it up.",

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
					Attributes: itemAttributes,
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

	// DS-CLOUD-1: GET /api/v2/clouds only accepts "name" (substring) server-side -
	// there is no provider or region query param on this endpoint at all, so those
	// two are applied as client-side post-filters below instead, over every page.
	params := url.Values{}
	if !config.NameContains.IsNull() {
		params.Add("name", config.NameContains.ValueString())
	}

	tflog.Debug(ctx, "Fetching clouds with filters", map[string]any{
		"filters": params.Encode(),
	})

	// Fetch clouds
	clouds, err := d.fetchClouds(ctx, params, config.CloudProvider.ValueString(), config.Region.ValueString())
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

// fetchClouds fetches clouds matching the server-side name substring filter (params), handling
// pagination, then applies cloudProvider/region as client-side post-filters (DS-CLOUD-1: the
// backend has no provider/region query params on this endpoint). Either post-filter is skipped
// when its argument is "".
func (d *CloudsDataSource) fetchClouds(ctx context.Context, params url.Values, cloudProvider, region string) ([]CloudSummaryModel, error) {
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

	// Convert CloudResults to CloudSummaryModels, applying the provider/region
	// post-filters as we go. cloud_provider matches case-insensitively (PR #91
	// fixed the same case-sensitivity trap for addProviderConfig; a user typing
	// "aws" should match a stored "AWS" here too).
	allClouds := make([]CloudSummaryModel, 0, len(cloudResults))
	for _, cloud := range cloudResults {
		if cloudProvider != "" && !strings.EqualFold(cloud.Provider, cloudProvider) {
			continue
		}
		if region != "" && cloud.Region != region {
			continue
		}

		azList, azDiags := types.ListValueFrom(ctx, types.StringType, cloud.AvailabilityZones)
		if azDiags.HasError() {
			return nil, fmt.Errorf("failed to convert availability_zones for cloud %s: %v", cloud.ID, azDiags)
		}

		cloudModel := CloudSummaryModel{
			ID:            types.StringValue(cloud.ID),
			Name:          types.StringValue(cloud.Name),
			CloudProvider: types.StringValue(cloud.Provider),
			ComputeStack:  types.StringValue(cloud.ComputeStack),
			Region:        types.StringValue(cloud.Region),
			// DS-CLOUD-3: align with the singular's null-guarding. status/state
			// are non-Optional enum fields on the backend Cloud model today (always
			// populated, per backend/server/api/base/models/clouds.py), so this
			// currently never observes a real empty value - kept for defensive
			// parity with the singular DS's identical guard, and so both DS behave
			// the same way if that ever changes.
			Status:                 stringOrNull(cloud.Status),
			State:                  stringOrNull(cloud.State),
			CreatedAt:              types.StringValue(cloud.CreatedAt),
			CreatorID:              types.StringValue(cloud.CreatorID),
			IsDefault:              types.BoolValue(cloud.IsDefault),
			IsK8s:                  types.BoolValue(cloud.IsK8s),
			IsPrivateCloud:         types.BoolValue(cloud.IsPrivateCloud),
			AutoAddUser:            types.BoolValue(cloud.AutoAddUser),
			LineageTrackingEnabled: types.BoolValue(cloud.LineageTrackingEnabled),
			AggregatedLogsEnabled:  types.BoolValue(cloud.IsAggregatedLogsEnabled),
			AvailabilityZones:      azList,
			Version:                types.StringValue(cloud.Version),
			ExternalID:             types.StringPointerValue(cloud.ExternalID),
		}
		allClouds = append(allClouds, cloudModel)
	}

	return allClouds, nil
}
