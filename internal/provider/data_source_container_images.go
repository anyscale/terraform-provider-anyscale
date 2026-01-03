package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
							MarkdownDescription: "The status of the latest build (`pending`, `in_progress`, `succeeded`, `failed`, `cancelled`).",
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

	// Build search query request body
	query := ClusterEnvironmentsSearchQuery{
		IncludeArchived:  false,
		IncludeAnonymous: false,
		Paging: PageQuery{
			Count: 100,
		},
	}

	if !config.NameContains.IsNull() && config.NameContains.ValueString() != "" {
		query.Name = &TextQuery{
			Contains: config.NameContains.ValueString(),
		}
	}

	if !config.CreatorID.IsNull() && config.CreatorID.ValueString() != "" {
		creatorID := config.CreatorID.ValueString()
		query.CreatorID = &creatorID
	}

	if !config.ProjectID.IsNull() && config.ProjectID.ValueString() != "" {
		projectID := config.ProjectID.ValueString()
		query.ProjectID = &projectID
	}

	// Set include_archived (defaults to false if not specified)
	if !config.IncludeArchived.IsNull() {
		query.IncludeArchived = config.IncludeArchived.ValueBool()
	}

	tflog.Debug(ctx, "Fetching container images with search query", map[string]any{
		"include_archived": query.IncludeArchived,
	})

	// Fetch container images
	containerImages, err := d.fetchContainerImages(ctx, query)
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

// fetchContainerImages fetches container images using GET /api/v2/application_templates/, handling pagination automatically.
// Note: We use GET instead of POST /search because GET returns DecoratedApplicationTemplate with latest_build info.
func (d *ContainerImagesDataSource) fetchContainerImages(ctx context.Context, query ClusterEnvironmentsSearchQuery) ([]ContainerImageSummaryModel, error) {
	var allResults []ClusterEnvironmentResult

	// Build query string for GET endpoint
	baseURL := "/api/v2/application_templates/"
	params := make([]string, 0)
	params = append(params, fmt.Sprintf("count=%d", query.Paging.Count))

	if query.Name != nil && query.Name.Contains != "" {
		params = append(params, fmt.Sprintf("name_contains=%s", url.QueryEscape(query.Name.Contains)))
	}
	if query.CreatorID != nil && *query.CreatorID != "" {
		params = append(params, fmt.Sprintf("creator_id=%s", url.QueryEscape(*query.CreatorID)))
	}
	if query.ProjectID != nil && *query.ProjectID != "" {
		params = append(params, fmt.Sprintf("project_id=%s", url.QueryEscape(*query.ProjectID)))
	}
	if query.IncludeArchived {
		params = append(params, "include_archived=true")
	}

	// Handle pagination
	for {
		queryString := baseURL
		currentParams := params
		if query.Paging.PagingToken != nil && *query.Paging.PagingToken != "" {
			currentParams = append(currentParams, fmt.Sprintf("paging_token=%s", url.QueryEscape(*query.Paging.PagingToken)))
		}
		if len(currentParams) > 0 {
			queryString = fmt.Sprintf("%s?%s", baseURL, strings.Join(currentParams, "&"))
		}

		listResp, err := DoRequestAndParse[ClusterEnvironmentsListResponse](
			ctx,
			d.client,
			"GET",
			queryString,
			nil,
			http.StatusOK,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to list cluster environments: %w", err)
		}

		allResults = append(allResults, listResp.Results...)

		// Check for next page
		if listResp.Metadata.NextPagingToken == nil || *listResp.Metadata.NextPagingToken == "" {
			break
		}

		// Update paging token for next request
		query.Paging.PagingToken = listResp.Metadata.NextPagingToken
	}

	// Fetch build details to get revision numbers
	allImages := make([]ContainerImageSummaryModel, 0, len(allResults))
	for _, env := range allResults {
		imageModel := ContainerImageSummaryModel{
			ID:         types.StringValue(env.ID),
			Name:       types.StringValue(env.Name),
			CreatedAt:  types.StringValue(env.CreatedAt),
			IsArchived: types.BoolValue(env.IsArchived),
		}

		if env.CreatorID != "" {
			imageModel.CreatorID = types.StringValue(env.CreatorID)
		} else {
			imageModel.CreatorID = types.StringNull()
		}

		// Get build ID from nested latest_build object or legacy field
		var buildID string
		if env.LatestBuild != nil && env.LatestBuild.ID != "" {
			buildID = env.LatestBuild.ID
		} else if env.LatestBuildID != nil && *env.LatestBuildID != "" {
			buildID = *env.LatestBuildID
		}

		if buildID != "" {
			imageModel.LatestBuildID = types.StringValue(buildID)

			// Set status from nested object if available
			if env.LatestBuild != nil {
				imageModel.LatestBuildStatus = types.StringValue(env.LatestBuild.Status)
			} else if env.LatestBuildStatus != nil {
				imageModel.LatestBuildStatus = types.StringValue(*env.LatestBuildStatus)
			} else {
				imageModel.LatestBuildStatus = types.StringNull()
			}

			// Fetch build details to get revision (or use from nested object)
			if env.LatestBuild != nil && env.LatestBuild.Revision > 0 {
				imageModel.Revision = types.Int64Value(int64(env.LatestBuild.Revision))
				imageModel.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", env.Name, env.LatestBuild.Revision))
			} else {
				build, err := d.getBuild(ctx, buildID)
				if err != nil {
					tflog.Warn(ctx, "Failed to get build details for revision", map[string]any{
						"build_id": buildID,
						"error":    err.Error(),
					})
					imageModel.Revision = types.Int64Null()
					imageModel.NameVersion = types.StringNull()
				} else {
					imageModel.Revision = types.Int64Value(int64(build.Revision))
					imageModel.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", env.Name, build.Revision))
				}
			}
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

// getBuild fetches build details by ID.
func (d *ContainerImagesDataSource) getBuild(ctx context.Context, buildID string) (*BuildResult, error) {
	// Note: The Anyscale API returns 201 for GET build endpoints
	buildResp, err := DoRequestAndParse[BuildResponse](
		ctx,
		d.client,
		"GET",
		fmt.Sprintf("/api/v2/builds/%s", buildID),
		nil,
		http.StatusOK,
		http.StatusCreated,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get build %s: %w", buildID, err)
	}

	return &buildResp.Result, nil
}
