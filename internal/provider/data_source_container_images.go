package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &ContainerImagesDataSource{}
	_ datasource.DataSourceWithConfigure = &ContainerImagesDataSource{}
)

// NewContainerImagesDataSource creates a new container images data source.
func NewContainerImagesDataSource() datasource.DataSource {
	return &ContainerImagesDataSource{}
}

// ContainerImagesDataSource defines the data source implementation.
type ContainerImagesDataSource struct {
	client *Client
}

// ContainerImagesDataSourceModel describes the data source data model.
type ContainerImagesDataSourceModel struct {
	// Filter inputs (all optional)
	NameContains    types.String `tfsdk:"name_contains"`
	CreatorID       types.String `tfsdk:"creator_id"`
	ProjectID       types.String `tfsdk:"project_id"`
	IncludeArchived types.Bool   `tfsdk:"include_archived"`

	// Computed output
	ContainerImages []ContainerImageSummaryModel `tfsdk:"container_images"`
}

// ContainerImageSummaryModel represents a container image in the list.
type ContainerImageSummaryModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	CreatorID         types.String `tfsdk:"creator_id"`
	CreatedAt         types.String `tfsdk:"created_at"`
	IsArchived        types.Bool   `tfsdk:"is_archived"`
	LatestBuildID     types.String `tfsdk:"latest_build_id"`
	LatestBuildStatus types.String `tfsdk:"latest_build_status"`
	Revision          types.Int64  `tfsdk:"revision"`
	NameVersion       types.String `tfsdk:"name_version"`
}

// Metadata returns the data source type name.
func (d *ContainerImagesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_images"
}

// Schema defines the schema for the data source.
func (d *ContainerImagesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists and filters Anyscale container images (cluster environments). This data source returns a list of container images with their latest build information.",

		Attributes: map[string]schema.Attribute{
			"name_contains": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter container images by partial name match.",
			},
			"creator_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter container images by creator ID.",
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter container images by project ID.",
			},
			"include_archived": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether to include archived container images in results. Defaults to false.",
			},
			"container_images": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "List of container images matching the filters.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The unique identifier of the cluster environment.",
						},
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The name of the container image.",
						},
						"creator_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The ID of the user who created this container image.",
						},
						"created_at": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Timestamp when the container image was created.",
						},
						"is_archived": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "Whether this container image is archived.",
						},
						"latest_build_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The ID of the latest build for this container image.",
						},
						"latest_build_status": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The status of the latest build (`pending`, `in_progress`, `succeeded`, `failed`, `pending_cancellation`, `canceled`).",
						},
						"revision": schema.Int64Attribute{
							Computed:            true,
							MarkdownDescription: "The revision number of the latest build.",
						},
						"name_version": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The name and revision formatted as `name:revision` for use with Anyscale APIs.",
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *ContainerImagesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		AddConfigError(&resp.Diagnostics, "Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}

	d.client = client
}

// Read refreshes the Terraform state with the latest data.
func (d *ContainerImagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config ContainerImagesDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build query parameters for GET /api/v2/application_templates/
	params := url.Values{}

	if !config.NameContains.IsNull() && config.NameContains.ValueString() != "" {
		params.Set("name_contains", config.NameContains.ValueString())
	}

	if !config.CreatorID.IsNull() && config.CreatorID.ValueString() != "" {
		params.Set("creator_id", config.CreatorID.ValueString())
	}

	if !config.ProjectID.IsNull() && config.ProjectID.ValueString() != "" {
		params.Set("project_id", config.ProjectID.ValueString())
	}

	// include_archived defaults to false if not specified
	includeArchived := !config.IncludeArchived.IsNull() && config.IncludeArchived.ValueBool()
	params.Set("include_archived", strconv.FormatBool(includeArchived))

	tflog.Debug(ctx, "Fetching container images", map[string]any{
		"include_archived": includeArchived,
	})

	// Fetch container images
	containerImages, err := d.fetchContainerImages(ctx, params)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "list container images", err)
		return
	}

	tflog.Info(ctx, "Container images fetched successfully", map[string]any{"count": len(containerImages)})

	// Populate config
	config.ContainerImages = containerImages

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// Helper functions

// fetchContainerImages fetches container images from GET /api/v2/application_templates/, handling pagination automatically.
// Each result's latest build summary (id/revision/status) is embedded directly on the decorated
// application template, so no per-item build lookup is required.
func (d *ContainerImagesDataSource) fetchContainerImages(ctx context.Context, params url.Values) ([]ContainerImageSummaryModel, error) {
	results, err := PaginatedRequest(ctx, d.client, "/api/v2/application_templates/", params,
		func(body []byte) ([]ApplicationTemplateResult, *string, error) {
			var listResp ApplicationTemplatesListResponse
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, err
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list application templates: %w", err)
	}

	allImages := make([]ContainerImageSummaryModel, 0, len(results))
	for _, tmpl := range results {
		imageModel := ContainerImageSummaryModel{
			ID:         types.StringValue(tmpl.ID),
			Name:       types.StringValue(tmpl.Name),
			CreatedAt:  types.StringValue(tmpl.CreatedAt),
			IsArchived: types.BoolValue(tmpl.IsArchived()),
		}

		if tmpl.CreatorID != "" {
			imageModel.CreatorID = types.StringValue(tmpl.CreatorID)
		} else {
			imageModel.CreatorID = types.StringNull()
		}

		if tmpl.LatestBuild != nil {
			imageModel.LatestBuildID = types.StringValue(tmpl.LatestBuild.ID)
			imageModel.LatestBuildStatus = types.StringValue(tmpl.LatestBuild.Status)
			imageModel.Revision = types.Int64Value(int64(tmpl.LatestBuild.Revision))
			imageModel.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", tmpl.Name, tmpl.LatestBuild.Revision))
		} else {
			imageModel.LatestBuildID = types.StringNull()
			imageModel.LatestBuildStatus = types.StringNull()
			imageModel.Revision = types.Int64Null()
			imageModel.NameVersion = types.StringNull()
		}

		allImages = append(allImages, imageModel)
	}

	return allImages, nil
}
