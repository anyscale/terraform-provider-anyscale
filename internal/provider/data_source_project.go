package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &ProjectDataSource{}
	_ datasource.DataSourceWithConfigure = &ProjectDataSource{}
)

// NewProjectDataSource creates a new project data source.
func NewProjectDataSource() datasource.DataSource {
	return &ProjectDataSource{}
}

// ProjectDataSource defines the data source implementation.
type ProjectDataSource struct {
	client *Client
}

// ProjectDataSourceModel describes the data source data model.
type ProjectDataSourceModel struct {
	// Input attributes (at least one required)
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`

	// Cloud filter attributes (both optional, cloud_name resolves to cloud_id)
	CloudID   types.String `tfsdk:"cloud_id"`
	CloudName types.String `tfsdk:"cloud_name"`

	// Computed outputs
	Description     types.String `tfsdk:"description"`
	CreatorID       types.String `tfsdk:"creator_id"`
	CreatedAt       types.String `tfsdk:"created_at"`
	LastUsedCloudID types.String `tfsdk:"last_used_cloud_id"`
	IsDefault       types.Bool   `tfsdk:"is_default"`
	DirectoryName   types.String `tfsdk:"directory_name"`

	// Collaborators (nested list of objects)
	Collaborators []ProjectDataSourceCollaboratorModel `tfsdk:"collaborators"`
}

// ProjectDataSourceCollaboratorModel represents a collaborator in the data source.
type ProjectDataSourceCollaboratorModel struct {
	Email           types.String `tfsdk:"email"`
	PermissionLevel types.String `tfsdk:"permission_level"`
	IdentityID      types.String `tfsdk:"identity_id"`
	UserID          types.String `tfsdk:"user_id"`
}

// Metadata returns the data source type name.
func (d *ProjectDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

// Schema defines the schema for the data source.
func (d *ProjectDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attributes := projectSharedAttributes()
	attributes["id"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The unique identifier of the project. Either `id` or `name` must be specified.",
	}
	attributes["name"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The name of the project. Either `id` or `name` must be specified. If multiple projects have the same name, the most recently created one will be returned.",
	}
	attributes["cloud_id"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The cloud ID this project belongs to. Can be used as a filter when looking up by name.",
	}
	attributes["cloud_name"] = schema.StringAttribute{
		Optional:            true,
		MarkdownDescription: "The cloud name this project belongs to. Can be used as a filter when looking up by name. Will be resolved to cloud_id.",
	}
	attributes["collaborators"] = schema.ListNestedAttribute{
		Computed:            true,
		MarkdownDescription: "List of collaborators with access to this project.",
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"email": schema.StringAttribute{
					Computed:            true,
					MarkdownDescription: "Email address of the collaborator.",
				},
				"permission_level": schema.StringAttribute{
					Computed:            true,
					MarkdownDescription: "Permission level: `owner`, `write`, or `readonly`.",
				},
				"identity_id": schema.StringAttribute{
					Computed:            true,
					MarkdownDescription: "The identity ID of the collaborator.",
				},
				"user_id": schema.StringAttribute{
					Computed:            true,
					MarkdownDescription: "The user ID of the collaborator.",
				},
			},
		},
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches details about an Anyscale Project by ID or name.",
		Attributes:          attributes,
	}
}

// Configure adds the provider configured client to the data source.
func (d *ProjectDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *ProjectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config ProjectDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Validate inputs
	if config.ID.IsNull() && config.Name.IsNull() {
		AddConfigError(&resp.Diagnostics, "Missing Required Attribute",
			"Either 'id' or 'name' must be specified to look up a project.")
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

	// Determine lookup strategy
	var projectID string
	var err error

	if !config.ID.IsNull() {
		// Direct lookup by ID
		projectID = config.ID.ValueString()
		tflog.Debug(ctx, "Looking up project by ID", map[string]any{"project_id": projectID})
	} else {
		// Lookup by name
		projectName := config.Name.ValueString()
		tflog.Debug(ctx, "Looking up project by name", map[string]any{
			"project_name": projectName,
			"cloud_id":     cloudID,
		})

		projectID, err = d.findProjectByName(ctx, projectName, cloudID)
		if err != nil {
			AddAPIError(&resp.Diagnostics, "find project by name", err)
			return
		}
	}

	// Fetch project details
	project, err := d.getProject(ctx, projectID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read project", err)
		return
	}

	// Fetch collaborators
	collaborators, err := d.getCollaborators(ctx, projectID)
	if err != nil {
		tflog.Warn(ctx, "Failed to get collaborators", map[string]any{
			"project_id": projectID,
			"error":      err.Error(),
		})
		// Continue without collaborators rather than failing
		collaborators = []ProjectDataSourceCollaboratorModel{}
	}

	// Populate config
	config.ID = types.StringValue(project.ID)
	config.Name = types.StringValue(project.Name)
	config.CloudID = types.StringValue(project.ParentCloudID)

	if project.Description != nil {
		config.Description = types.StringValue(*project.Description)
	} else {
		config.Description = types.StringNull()
	}

	if project.CreatorID != nil {
		config.CreatorID = types.StringValue(*project.CreatorID)
	} else {
		config.CreatorID = types.StringNull()
	}

	config.CreatedAt = types.StringValue(project.CreatedAt)

	if project.LastUsedCloudID != nil {
		config.LastUsedCloudID = types.StringValue(*project.LastUsedCloudID)
	} else {
		config.LastUsedCloudID = types.StringNull()
	}

	config.IsDefault = types.BoolValue(project.IsDefault)
	config.DirectoryName = types.StringValue(project.DirectoryName)
	config.Collaborators = collaborators

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// Helper functions

// findProjectByName searches for a project by name with optional cloud filter.
func (d *ProjectDataSource) findProjectByName(ctx context.Context, name string, cloudID string) (string, error) {
	// Build query parameters
	params := url.Values{}
	if cloudID != "" {
		params.Add("parent_cloud_id", cloudID)
	}

	// Use PaginatedRequest to fetch all projects
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
		return "", fmt.Errorf("failed to list projects: %w", err)
	}

	// Find exact name match
	var matchedProjectID string
	var latestCreatedAt string
	matchCount := 0

	for _, project := range results {
		if project.Name == name {
			matchCount++
			if matchedProjectID == "" || project.CreatedAt > latestCreatedAt {
				matchedProjectID = project.ID
				latestCreatedAt = project.CreatedAt
			}
		}
	}

	if matchedProjectID == "" {
		return "", fmt.Errorf("no project found with name '%s'", name)
	}

	WarnIfMultipleMatches(ctx, "project", name, matchCount, matchedProjectID)

	return matchedProjectID, nil
}

// getProject fetches a single project by ID.
func (d *ProjectDataSource) getProject(ctx context.Context, projectID string) (*ProjectResult, error) {
	projectResp, err := DoRequestAndParse[ProjectResponse](
		ctx, d.client, "GET", fmt.Sprintf("/api/v2/projects/%s", projectID), nil, http.StatusOK,
	)
	if err != nil {
		// Check for 404
		if err.Error() == "unexpected status 404: {\"detail\":\"Project not found\"}" {
			return nil, fmt.Errorf("project not found")
		}
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	return &projectResp.Result, nil
}

// getCollaborators fetches the list of collaborators for a project.
//
// Pages through every page rather than just the first, so a project with
// more collaborators than fit on one page doesn't silently drop the rest.
func (d *ProjectDataSource) getCollaborators(ctx context.Context, projectID string) ([]ProjectDataSourceCollaboratorModel, error) {
	results, err := PaginatedRequest(
		ctx, d.client, fmt.Sprintf("/api/v2/projects/%s/collaborators/users", projectID), nil,
		func(body []byte) ([]ProjectCollaboratorResult, *string, error) {
			var listResp ProjectCollaboratorListResponse
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, fmt.Errorf("failed to unmarshal collaborators response: %w", err)
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get collaborators: %w", err)
	}

	// Map to model
	collaborators := make([]ProjectDataSourceCollaboratorModel, 0, len(results))
	for _, collab := range results {
		collaborators = append(collaborators, ProjectDataSourceCollaboratorModel{
			Email:           types.StringValue(collab.Value.Email),
			PermissionLevel: types.StringValue(collab.PermissionLevel),
			IdentityID:      types.StringValue(collab.ID),
			UserID:          types.StringValue(collab.Value.ID),
		})
	}

	return collaborators, nil
}
