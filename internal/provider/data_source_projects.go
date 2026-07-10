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
	_ datasource.DataSource              = &ProjectsDataSource{}
	_ datasource.DataSourceWithConfigure = &ProjectsDataSource{}
)

// NewProjectsDataSource creates a new projects data source.
func NewProjectsDataSource() datasource.DataSource {
	return &ProjectsDataSource{}
}

// ProjectsDataSource defines the data source implementation.
type ProjectsDataSource struct {
	client *Client
}

// ProjectsDataSourceModel describes the data source data model.
type ProjectsDataSourceModel struct {
	// Filter inputs (all optional)
	NameContains    types.String `tfsdk:"name_contains"`
	CreatorID       types.String `tfsdk:"creator_id"`
	CloudID         types.String `tfsdk:"cloud_id"`
	CloudName       types.String `tfsdk:"cloud_name"`
	IncludeDefaults types.Bool   `tfsdk:"include_defaults"`

	// Computed output
	Projects []ProjectSummaryModel `tfsdk:"projects"`
}

// ProjectSummaryModel represents a project in the list (without collaborators for performance).
type ProjectSummaryModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Description     types.String `tfsdk:"description"`
	CloudID         types.String `tfsdk:"cloud_id"`
	CreatorID       types.String `tfsdk:"creator_id"`
	CreatedAt       types.String `tfsdk:"created_at"`
	LastUsedCloudID types.String `tfsdk:"last_used_cloud_id"`
	IsDefault       types.Bool   `tfsdk:"is_default"`
	DirectoryName   types.String `tfsdk:"directory_name"`
	OrganizationID  types.String `tfsdk:"organization_id"`
}

// Metadata returns the data source type name.
func (d *ProjectsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_projects"
}

// Schema defines the schema for the data source.
func (d *ProjectsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists and filters Anyscale Projects. This data source returns a list of projects without collaborator details for performance.",

		Attributes: map[string]schema.Attribute{
			"name_contains": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter projects by partial name match.",
			},
			"creator_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter projects by creator ID.",
			},
			"cloud_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter projects by cloud ID.",
			},
			"cloud_name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter projects by cloud name. Will be resolved to cloud_id.",
			},
			"include_defaults": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether to include default projects in results. Defaults to true.",
			},
			"projects": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "List of projects matching the filters. Does not include collaborator details for performance.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The unique identifier of the project.",
						},
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The name of the project.",
						},
						"description": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Description of the project.",
						},
						"cloud_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The cloud ID this project belongs to.",
						},
						"creator_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The ID of the user who created the project.",
						},
						"created_at": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Timestamp when the project was created.",
						},
						"last_used_cloud_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The ID of the cloud last used by this project.",
						},
						"is_default": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "Whether this is the default project for the organization.",
						},
						"directory_name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The directory name used for this project's storage.",
						},
						"organization_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The ID of the organization this project belongs to.",
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *ProjectsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *ProjectsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config ProjectsDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Resolve cloud_name to cloud_id if provided
	cloudID := config.CloudID.ValueString()
	if !config.CloudName.IsNull() {
		cloudName := config.CloudName.ValueString()
		tflog.Info(ctx, "Resolving cloud_name to cloud_id", map[string]any{"cloud_name": cloudName})

		resolvedID, err := ResolveCloudNameToID(ctx, d.client, cloudName)
		if err != nil {
			AddAPIError(&resp.Diagnostics, "resolve cloud name", err)
			return
		}
		cloudID = resolvedID
	}

	// Build query parameters
	params := url.Values{}

	if !config.NameContains.IsNull() {
		params.Add("name_contains", config.NameContains.ValueString())
	}

	if !config.CreatorID.IsNull() {
		params.Add("creator_id", config.CreatorID.ValueString())
	}

	if cloudID != "" {
		params.Add("parent_cloud_id", cloudID)
	}

	// Set include_defaults (defaults to true if not specified)
	includeDefaults := true
	if !config.IncludeDefaults.IsNull() {
		includeDefaults = config.IncludeDefaults.ValueBool()
	}
	if includeDefaults {
		params.Add("include_defaults", "true")
	} else {
		params.Add("include_defaults", "false")
	}

	tflog.Debug(ctx, "Fetching projects with filters", map[string]any{
		"filters": params.Encode(),
	})

	// Fetch projects
	projects, err := d.fetchProjects(ctx, params)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "list projects", err)
		return
	}

	tflog.Info(ctx, "Projects fetched successfully", map[string]any{"count": len(projects)})

	// Populate config
	config.Projects = projects

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// Helper functions

// fetchProjects fetches projects with the given query parameters, handling pagination automatically.
func (d *ProjectsDataSource) fetchProjects(ctx context.Context, params url.Values) ([]ProjectSummaryModel, error) {
	// Use PaginatedRequest helper to handle pagination
	results, err := PaginatedRequest(ctx, d.client, "/api/v2/projects", params,
		func(body []byte) ([]ProjectResult, *string, error) {
			var projectsResp ProjectsListResponse
			if err := json.Unmarshal(body, &projectsResp); err != nil {
				return nil, nil, err
			}
			return projectsResp.Results, projectsResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return nil, err
	}

	// Convert to model
	allProjects := make([]ProjectSummaryModel, 0, len(results))
	for _, project := range results {
		projectModel := ProjectSummaryModel{
			ID:             types.StringValue(project.ID),
			Name:           types.StringValue(project.Name),
			CloudID:        types.StringValue(project.ParentCloudID),
			CreatedAt:      types.StringValue(project.CreatedAt),
			IsDefault:      types.BoolValue(project.IsDefault),
			DirectoryName:  types.StringValue(project.DirectoryName),
			OrganizationID: types.StringValue(project.OrganizationID),
		}

		if project.CreatorID != nil {
			projectModel.CreatorID = types.StringValue(*project.CreatorID)
		} else {
			projectModel.CreatorID = types.StringNull()
		}

		if project.Description != nil {
			projectModel.Description = types.StringValue(*project.Description)
		} else {
			projectModel.Description = types.StringNull()
		}

		if project.LastUsedCloudID != nil {
			projectModel.LastUsedCloudID = types.StringValue(*project.LastUsedCloudID)
		} else {
			projectModel.LastUsedCloudID = types.StringNull()
		}

		allProjects = append(allProjects, projectModel)
	}

	return allProjects, nil
}
