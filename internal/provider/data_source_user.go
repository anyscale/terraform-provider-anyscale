package provider

import (
	"context"
	"encoding/json"
	"fmt"
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
		AddConfigError(&resp.Diagnostics,
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = client
}

// userInfoResponse is the subset of GET /api/v2/userinfo this data source
// needs. DS-USER-1/DS-USER-2: organization_permission_level and each
// organization's default_cloud_id are genuinely nullable server-side (see
// backend/server/api/product/models/users.go's UserInfo.organization_permission_level
// docstring: "absent if the user does not have a permission level assigned",
// and organizations.go's Organization.default_cloud_id) - both *string here,
// mapped via StringPointerValue, matching how anyscale_organization already
// handles the identical default_cloud_id field.
type userInfoResponse struct {
	Result struct {
		ID                          string   `json:"id"`
		Email                       string   `json:"email"`
		Name                        string   `json:"name"`
		Username                    string   `json:"username"`
		OrganizationPermissionLevel *string  `json:"organization_permission_level"`
		OrganizationIDs             []string `json:"organization_ids"`
		Organizations               []struct {
			ID               string  `json:"id"`
			Name             string  `json:"name"`
			PublicIdentifier string  `json:"public_identifier"`
			DefaultCloudID   *string `json:"default_cloud_id"`
		} `json:"organizations"`
	} `json:"result"`
}

// Read refreshes the Terraform state with the latest data.
func (d *UserDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state UserDataSourceModel

	// DS-USER-4: adopt DoRequestAndParse, matching anyscale_organization's
	// fetchCurrentOrganization, instead of hand-rolling the request/read/parse
	// sequence. Same empty-organizations guard as that data source too.
	userResp, err := DoRequestAndParse[userInfoResponse](ctx, d.client, "GET", "/api/v2/userinfo", nil, http.StatusOK)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch user info", map[string]any{"error": err.Error()})
		AddAPIError(&resp.Diagnostics, "fetch user info", err)
		return
	}

	if len(userResp.Result.Organizations) == 0 {
		AddAPIError(&resp.Diagnostics, "fetch user info",
			fmt.Errorf("userinfo returned no organization for the authenticated token"))
		return
	}

	// Populate user info fields
	state.ID = types.StringValue(userResp.Result.ID)
	state.Email = types.StringValue(userResp.Result.Email)
	state.Name = types.StringValue(userResp.Result.Name)
	state.Username = types.StringValue(userResp.Result.Username)
	state.OrganizationPermissionLevel = types.StringPointerValue(userResp.Result.OrganizationPermissionLevel)

	// Convert organization IDs to list
	orgIDsList, diags := types.ListValueFrom(ctx, types.StringType, userResp.Result.OrganizationIDs)
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
			DefaultCloudID:   types.StringPointerValue(org.DefaultCloudID),
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

	// Fetch clouds the user has access to from /api/v2/clouds, across every
	// page rather than just the first (this endpoint paginates - see the
	// dedicated anyscale_clouds data source, which already does the same).
	clouds, err := PaginatedRequest(
		ctx, d.client, "/api/v2/clouds", nil,
		func(body []byte) ([]CloudResult, *string, error) {
			var listResp CloudsListResponse
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, fmt.Errorf("failed to unmarshal clouds: %w", err)
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch clouds", map[string]any{"error": err.Error()})
		AddAPIError(&resp.Diagnostics, "fetch clouds", err)
		return
	}

	// Convert cloud IDs to list
	cloudIDs := make([]string, len(clouds))
	for i, cloud := range clouds {
		cloudIDs[i] = cloud.ID
	}
	cloudIDsList, diags := types.ListValueFrom(ctx, types.StringType, cloudIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.CloudIDs = cloudIDsList

	// DS-USER-3: GET /api/v2/user_groups genuinely paginates (traced against
	// product backend/server/api/product/routers/user_groups_router.py
	// list_user_groups: required PagingContext, ListResponse[UserGroup]) - the
	// previous inline comment claiming it was confirmed non-paginated was
	// stale/wrong, and reading only page 1 silently truncated user_group_ids
	// for any org with more than one page of groups. Now paginated like every
	// other list endpoint in the provider.
	userGroups, err := PaginatedRequest(
		ctx, d.client, "/api/v2/user_groups", nil,
		func(body []byte) ([]struct {
			ID string `json:"id"`
		}, *string, error) {
			var listResp struct {
				Results []struct {
					ID string `json:"id"`
				} `json:"results"`
				Metadata struct {
					NextPagingToken *string `json:"next_paging_token"`
				} `json:"metadata"`
			}
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, fmt.Errorf("failed to unmarshal user groups: %w", err)
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		// Kept as a warning-and-empty-list rather than AddAPIError: user_group_ids
		// remains a light-weight, best-effort field on this data source (unlike
		// the required organizations/clouds fetches above), consistent with its
		// pre-existing degrade-gracefully behavior.
		tflog.Warn(ctx, "Failed to fetch user groups, returning empty list", map[string]any{"error": err.Error()})
		state.UserGroupIDs = types.ListNull(types.StringType)
	} else {
		userGroupIDs := make([]string, len(userGroups))
		for i, group := range userGroups {
			userGroupIDs[i] = group.ID
		}
		userGroupIDsList, diags := types.ListValueFrom(ctx, types.StringType, userGroupIDs)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		state.UserGroupIDs = userGroupIDsList
	}

	tflog.Info(ctx, "Successfully retrieved user info", map[string]any{
		"user_id":    userResp.Result.ID,
		"email":      userResp.Result.Email,
		"num_clouds": len(cloudIDs),
		"num_orgs":   len(userResp.Result.OrganizationIDs),
	})

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
