package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &UserDataSource{}
	_ datasource.DataSourceWithConfigure = &UserDataSource{}
)

// NewUserDataSource returns a new user data source.
func NewUserDataSource() datasource.DataSource {
	return &UserDataSource{}
}

// UserDataSource defines the data source implementation.
type UserDataSource struct {
	client *Client
}

// UserDataSourceModel describes the data source data model.
type UserDataSourceModel struct {
	// User information
	ID                          types.String `tfsdk:"id"`
	Email                       types.String `tfsdk:"email"`
	Name                        types.String `tfsdk:"name"`
	Username                    types.String `tfsdk:"username"`
	OrganizationPermissionLevel types.String `tfsdk:"organization_permission_level"`
	OrganizationIDs             types.List   `tfsdk:"organization_ids"`
	Organizations               types.List   `tfsdk:"organizations"`

	// Cloud access
	CloudIDs types.List `tfsdk:"cloud_ids"`

	// User groups (placeholder for future implementation)
	UserGroupIDs types.List `tfsdk:"user_group_ids"`
}

// OrganizationModel describes an organization in the user data.
type OrganizationModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	PublicIdentifier types.String `tfsdk:"public_identifier"`
	DefaultCloudID   types.String `tfsdk:"default_cloud_id"`
}

// Metadata returns the data source type name.
func (d *UserDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

// Schema defines the data source schema.
func (d *UserDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Use this data source to retrieve information about the current authenticated user, including their organization membership, permission level, and accessible clouds.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the user.",
			},
			"email": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The email address of the user.",
			},
			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The name of the user.",
			},
			"username": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The username of the user.",
			},
			"organization_permission_level": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The permission level of the user within their organization (e.g., owner, admin, member).",
			},
			"organization_ids": schema.ListAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "List of organization IDs the user belongs to.",
			},
			"organizations": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "List of organizations the user belongs to with detailed information.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The unique identifier of the organization.",
						},
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The name of the organization.",
						},
						"public_identifier": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The public identifier of the organization.",
						},
						"default_cloud_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The default cloud ID for the organization.",
						},
					},
				},
			},
			"cloud_ids": schema.ListAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "List of cloud IDs the user has access to.",
			},
			"user_group_ids": schema.ListAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "List of user group IDs the user belongs to. Note: This feature is not fully implemented in the API yet and may return an empty list.",
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *UserDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *UserDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state UserDataSourceModel

	// Fetch user info from /api/v2/userinfo
	userInfoResp, err := d.client.DoRequest(ctx, "GET", "/api/v2/userinfo", nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch user info", map[string]any{"error": err.Error()})
		resp.Diagnostics.AddError("API Request Failed", fmt.Sprintf("Failed to fetch user info: %s", err.Error()))
		return
	}
	defer func() {
		if closeErr := userInfoResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	body, err := io.ReadAll(userInfoResp.Body)
	if err != nil {
		resp.Diagnostics.AddError("Response Read Error", fmt.Sprintf("Failed to read user info response: %s", err.Error()))
		return
	}

	if userInfoResp.StatusCode != http.StatusOK {
		resp.Diagnostics.AddError(
			"API Error",
			fmt.Sprintf("Failed to read user info: %s - %s", userInfoResp.Status, string(body)),
		)
		return
	}

	var userResp struct {
		Result struct {
			ID                          string   `json:"id"`
			Email                       string   `json:"email"`
			Name                        string   `json:"name"`
			Username                    string   `json:"username"`
			OrganizationPermissionLevel string   `json:"organization_permission_level"`
			OrganizationIDs             []string `json:"organization_ids"`
			Organizations               []struct {
				ID               string `json:"id"`
				Name             string `json:"name"`
				PublicIdentifier string `json:"public_identifier"`
				DefaultCloudID   string `json:"default_cloud_id"`
			} `json:"organizations"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &userResp); err != nil {
		resp.Diagnostics.AddError("JSON Unmarshal Error", fmt.Sprintf("Failed to unmarshal user info: %s", err.Error()))
		return
	}

	// Populate user info fields
	state.ID = types.StringValue(userResp.Result.ID)
	state.Email = types.StringValue(userResp.Result.Email)
	state.Name = types.StringValue(userResp.Result.Name)
	state.Username = types.StringValue(userResp.Result.Username)
	state.OrganizationPermissionLevel = types.StringValue(userResp.Result.OrganizationPermissionLevel)

	// Convert organization IDs to list
	orgIDs := make([]types.String, len(userResp.Result.OrganizationIDs))
	for i, orgID := range userResp.Result.OrganizationIDs {
		orgIDs[i] = types.StringValue(orgID)
	}
	orgIDsList, diags := types.ListValueFrom(ctx, types.StringType, orgIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.OrganizationIDs = orgIDsList

	// Convert organizations to list of objects
	orgObjects := make([]OrganizationModel, len(userResp.Result.Organizations))
	for i, org := range userResp.Result.Organizations {
		orgObjects[i] = OrganizationModel{
			ID:               types.StringValue(org.ID),
			Name:             types.StringValue(org.Name),
			PublicIdentifier: types.StringValue(org.PublicIdentifier),
			DefaultCloudID:   types.StringValue(org.DefaultCloudID),
		}
	}
	orgsList, diags := types.ListValueFrom(ctx, types.ObjectType{
		AttrTypes: map[string]attr.Type{
			"id":                types.StringType,
			"name":              types.StringType,
			"public_identifier": types.StringType,
			"default_cloud_id":  types.StringType,
		},
	}, orgObjects)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Organizations = orgsList

	// Fetch clouds the user has access to from /api/v2/clouds
	cloudsResp, err := d.client.DoRequest(ctx, "GET", "/api/v2/clouds", nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch clouds", map[string]any{"error": err.Error()})
		resp.Diagnostics.AddError("API Request Failed", fmt.Sprintf("Failed to fetch clouds: %s", err.Error()))
		return
	}
	defer func() {
		if closeErr := cloudsResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	cloudsBody, err := io.ReadAll(cloudsResp.Body)
	if err != nil {
		resp.Diagnostics.AddError("Response Read Error", fmt.Sprintf("Failed to read clouds response: %s", err.Error()))
		return
	}

	if cloudsResp.StatusCode != http.StatusOK {
		resp.Diagnostics.AddError(
			"API Error",
			fmt.Sprintf("Failed to read clouds: %s - %s", cloudsResp.Status, string(cloudsBody)),
		)
		return
	}

	var cloudsAPIResp struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}

	if err := json.Unmarshal(cloudsBody, &cloudsAPIResp); err != nil {
		resp.Diagnostics.AddError("JSON Unmarshal Error", fmt.Sprintf("Failed to unmarshal clouds: %s", err.Error()))
		return
	}

	// Convert cloud IDs to list
	cloudIDs := make([]types.String, len(cloudsAPIResp.Results))
	for i, cloud := range cloudsAPIResp.Results {
		cloudIDs[i] = types.StringValue(cloud.ID)
	}
	cloudIDsList, diags := types.ListValueFrom(ctx, types.StringType, cloudIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.CloudIDs = cloudIDsList

	// Fetch user groups from /api/v2/user_groups (placeholder for future implementation)
	userGroupsResp, err := d.client.DoRequest(ctx, "GET", "/api/v2/user_groups", nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch user groups", map[string]any{"error": err.Error()})
		resp.Diagnostics.AddError("API Request Failed", fmt.Sprintf("Failed to fetch user groups: %s", err.Error()))
		return
	}
	defer func() {
		if closeErr := userGroupsResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	userGroupsBody, err := io.ReadAll(userGroupsResp.Body)
	if err != nil {
		resp.Diagnostics.AddError("Response Read Error", fmt.Sprintf("Failed to read user groups response: %s", err.Error()))
		return
	}

	if userGroupsResp.StatusCode != http.StatusOK {
		// Log warning but don't fail since this feature is not fully implemented
		tflog.Warn(ctx, "Failed to fetch user groups, returning empty list", map[string]any{
			"status": userGroupsResp.Status,
			"body":   string(userGroupsBody),
		})
		state.UserGroupIDs, _ = types.ListValueFrom(ctx, types.StringType, []types.String{})
	} else {
		var userGroupsAPIResp struct {
			Results []struct {
				ID string `json:"id"`
			} `json:"results"`
		}

		if err := json.Unmarshal(userGroupsBody, &userGroupsAPIResp); err != nil {
			tflog.Warn(ctx, "Failed to unmarshal user groups, returning empty list", map[string]any{"error": err.Error()})
			state.UserGroupIDs, _ = types.ListValueFrom(ctx, types.StringType, []types.String{})
		} else {
			// Convert user group IDs to list
			userGroupIDs := make([]types.String, len(userGroupsAPIResp.Results))
			for i, group := range userGroupsAPIResp.Results {
				userGroupIDs[i] = types.StringValue(group.ID)
			}
			userGroupIDsList, diags := types.ListValueFrom(ctx, types.StringType, userGroupIDs)
			resp.Diagnostics.Append(diags...)
			if resp.Diagnostics.HasError() {
				return
			}
			state.UserGroupIDs = userGroupIDsList
		}
	}

	tflog.Info(ctx, "Successfully retrieved user info", map[string]any{
		"user_id":    userResp.Result.ID,
		"email":      userResp.Result.Email,
		"num_clouds": len(cloudIDs),
		"num_orgs":   len(orgIDs),
	})

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
