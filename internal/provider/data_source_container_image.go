package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &ContainerImageDataSource{}
	_ datasource.DataSourceWithConfigure = &ContainerImageDataSource{}
)

// NewContainerImageDataSource creates a new container image data source.
func NewContainerImageDataSource() datasource.DataSource {
	return &ContainerImageDataSource{}
}

// ContainerImageDataSource defines the data source implementation.
type ContainerImageDataSource struct {
	client *Client
}

// ContainerImageDataSourceModel describes the data source data model.
type ContainerImageDataSourceModel struct {
	// Input attributes (one required)
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`

	// Output attributes
	BuildID     types.String `tfsdk:"build_id"`
	ImageURI    types.String `tfsdk:"image_uri"`
	RayVersion  types.String `tfsdk:"ray_version"`
	BuildStatus types.String `tfsdk:"build_status"`
	IsBYOD      types.Bool   `tfsdk:"is_byod"`
	CreatedAt   types.String `tfsdk:"created_at"`
	CreatorID   types.String `tfsdk:"creator_id"`
	Revision    types.Int64  `tfsdk:"revision"`
	NameVersion types.String `tfsdk:"name_version"` // Formatted as "name:revision" for use with Anyscale APIs
}

// Metadata returns the data source type name.
func (d *ContainerImageDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_image"
}

// Schema defines the schema for the data source.
func (d *ContainerImageDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Retrieves information about an existing Anyscale container image (cluster environment). Use this data source to look up container images by ID or name.",

		Attributes: map[string]schema.Attribute{
			// Input attributes
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The unique identifier of the cluster environment. Either `id` or `name` must be specified.",
				Validators: []validator.String{
					stringvalidator.AtLeastOneOf(
						path.MatchRoot("id"),
						path.MatchRoot("name"),
					),
				},
			},
			"name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The name of the cluster environment. Either `id` or `name` must be specified.",
			},

			// Output attributes
			"build_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the latest build for this cluster environment.",
			},
			"image_uri": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The URI of the container image.",
			},
			"ray_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The Ray version used in the build.",
			},
			"build_status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The status of the latest build (`pending`, `in_progress`, `succeeded`, `failed`, `cancelled`).",
			},
			"is_byod": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this is a BYOD (Bring Your Own Docker) image.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the cluster environment was created.",
			},
			"creator_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The ID of the user who created this cluster environment.",
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
	}
}

// Configure adds the provider configured client to the data source.
func (d *ContainerImageDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *ContainerImageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config ContainerImageDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var clusterEnv *ClusterEnvironmentResult
	var err error

	// Look up by ID or name
	if !config.ID.IsNull() && config.ID.ValueString() != "" {
		clusterEnv, err = d.getClusterEnvironmentByID(ctx, config.ID.ValueString())
	} else if !config.Name.IsNull() && config.Name.ValueString() != "" {
		clusterEnv, err = d.getClusterEnvironmentByName(ctx, config.Name.ValueString())
	} else {
		AddConfigError(&resp.Diagnostics, "Missing Required Attribute",
			"Either 'id' or 'name' must be specified.")
		return
	}

	if err != nil {
		AddAPIError(&resp.Diagnostics, "read container image", err)
		return
	}

	// Map cluster environment to model
	config.ID = types.StringValue(clusterEnv.ID)
	config.Name = types.StringValue(clusterEnv.Name)
	config.CreatedAt = types.StringValue(clusterEnv.CreatedAt)
	config.CreatorID = types.StringValue(clusterEnv.CreatorID)

	// Fetch the latest build for this cluster environment
	buildID, err := d.getLatestBuildID(ctx, clusterEnv.ID)
	if err != nil {
		tflog.Warn(ctx, "Failed to get latest build ID", map[string]any{
			"cluster_environment_id": clusterEnv.ID,
			"error":                  err.Error(),
		})
	}

	// Get build details if available
	if buildID != "" {
		config.BuildID = types.StringValue(buildID)

		// Get full build details
		build, err := d.getBuild(ctx, buildID)
		if err != nil {
			tflog.Warn(ctx, "Failed to get build details", map[string]any{
				"build_id": buildID,
				"error":    err.Error(),
			})
			config.Revision = types.Int64Null()
			config.NameVersion = types.StringNull()
		} else {
			config.BuildStatus = types.StringValue(build.Status)
			config.IsBYOD = types.BoolValue(build.IsBYOD)
			config.Revision = types.Int64Value(int64(build.Revision))
			config.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", clusterEnv.Name, build.Revision))

			if build.DockerImageName != nil {
				config.ImageURI = types.StringValue(*build.DockerImageName)
			} else {
				config.ImageURI = types.StringNull()
			}

			if build.RayVersion != nil {
				config.RayVersion = types.StringValue(*build.RayVersion)
			} else {
				config.RayVersion = types.StringNull()
			}
		}
	} else {
		config.BuildID = types.StringNull()
		config.BuildStatus = types.StringNull()
		config.ImageURI = types.StringNull()
		config.RayVersion = types.StringNull()
		config.IsBYOD = types.BoolNull()
		config.Revision = types.Int64Null()
		config.NameVersion = types.StringNull()
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// Helper functions

// getClusterEnvironmentByID fetches a cluster environment by ID.
func (d *ContainerImageDataSource) getClusterEnvironmentByID(ctx context.Context, id string) (*ClusterEnvironmentResult, error) {
	tflog.Debug(ctx, "Fetching cluster environment by ID", map[string]any{"id": id})

	clusterEnvResp, err := DoRequestAndParse[ClusterEnvironmentResponse](
		ctx,
		d.client,
		"GET",
		fmt.Sprintf("/ext/v0/cluster_environments/%s", id),
		nil,
		http.StatusOK,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster environment %s: %w", id, err)
	}

	return &clusterEnvResp.Result, nil
}

// getClusterEnvironmentByName fetches a cluster environment by name.
func (d *ContainerImageDataSource) getClusterEnvironmentByName(ctx context.Context, name string) (*ClusterEnvironmentResult, error) {
	tflog.Debug(ctx, "Fetching cluster environment by name", map[string]any{"name": name})

	// Search for cluster environment by name using POST /ext/v0/cluster_environments/search
	searchQuery := ClusterEnvironmentsSearchQuery{
		Name: &TextQuery{
			Contains: name,
		},
		Paging: PageQuery{
			Count: 100,
		},
		IncludeArchived:  false,
		IncludeAnonymous: false,
	}

	reqBody, err := MarshalRequestBody(searchQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal search query: %w", err)
	}

	clusterEnvsResp, err := DoRequestAndParse[ClusterEnvironmentsListResponse](
		ctx,
		d.client,
		"POST",
		"/ext/v0/cluster_environments/search",
		reqBody,
		http.StatusOK,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to search cluster environments: %w", err)
	}

	// Find exact match
	var matches []ClusterEnvironmentResult
	for _, env := range clusterEnvsResp.Results {
		if env.Name == name && !env.IsArchived() {
			matches = append(matches, env)
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("no cluster environment found with name '%s'", name)
	}

	if len(matches) > 1 {
		WarnIfMultipleMatches(ctx, "cluster environment", name, len(matches), matches[0].ID)
	}

	// Return the first match (or most recent if multiple)
	return &matches[0], nil
}

// getBuild fetches build details by ID.
func (d *ContainerImageDataSource) getBuild(ctx context.Context, buildID string) (*ClusterEnvironmentBuildResult, error) {
	// Note: The Anyscale API returns 201 for GET build endpoints
	buildResp, err := DoRequestAndParse[ClusterEnvironmentBuildResponse](
		ctx,
		d.client,
		"GET",
		fmt.Sprintf("/ext/v0/cluster_environment_builds/%s", buildID),
		nil,
		http.StatusOK,
		http.StatusCreated,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get build %s: %w", buildID, err)
	}

	return &buildResp.Result, nil
}

// getLatestBuildID fetches the latest build ID for a cluster environment.
func (d *ContainerImageDataSource) getLatestBuildID(ctx context.Context, clusterEnvID string) (string, error) {
	tflog.Debug(ctx, "Fetching latest build for cluster environment", map[string]any{"cluster_environment_id": clusterEnvID})

	buildsResp, err := DoRequestAndParse[ClusterEnvironmentBuildsListResponse](
		ctx,
		d.client,
		"GET",
		fmt.Sprintf("/ext/v0/cluster_environment_builds/?cluster_environment_id=%s&count=1&desc=true", clusterEnvID),
		nil,
		http.StatusOK,
	)
	if err != nil {
		return "", fmt.Errorf("failed to list builds for cluster environment %s: %w", clusterEnvID, err)
	}

	if len(buildsResp.Results) == 0 {
		return "", nil // No builds yet - not an error
	}

	return buildsResp.Results[0].ID, nil
}
