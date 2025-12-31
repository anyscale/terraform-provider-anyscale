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
	_ datasource.DataSource              = &OrganizationUsersDataSource{}
	_ datasource.DataSourceWithConfigure = &OrganizationUsersDataSource{}
)

// NewOrganizationUsersDataSource returns a new organization users data source.
func NewOrganizationUsersDataSource() datasource.DataSource {
	return &OrganizationUsersDataSource{}
}

// OrganizationUsersDataSource defines the data source implementation.
type OrganizationUsersDataSource struct {
	client *Client
}

// OrganizationUsersDataSourceModel describes the data source data model.
type OrganizationUsersDataSourceModel struct {
	// Filters
	Email            types.String `tfsdk:"email"`
	Name             types.String `tfsdk:"name"`
	IsServiceAccount types.Bool   `tfsdk:"is_service_account"`

	// Output
	Users types.List `tfsdk:"users"`
}

// OrganizationUserModel represents a single user.
type OrganizationUserModel struct {
	ID              types.String `tfsdk:"id"`
	UserID          types.String `tfsdk:"user_id"`
	Name            types.String `tfsdk:"name"`
	Email           types.String `tfsdk:"email"`
	PermissionLevel types.String `tfsdk:"permission_level"`
	CreatedAt       types.String `tfsdk:"created_at"`
}

// Metadata returns the data source type name.
func (d *OrganizationUsersDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_users"
}

// Schema defines the data source schema.
func (d *OrganizationUsersDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "**BETA FEATURE**: Use this data source to retrieve a list of all users (including service accounts) in your organization. This is useful for SCIM provisioning and user management.",

		Attributes: map[string]schema.Attribute{
			"email": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter users by email (case-insensitive partial match).",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter users by name (case-insensitive partial match).",
			},
			"is_service_account": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Filter by account type. Set to `true` to return only service accounts, `false` to return only user accounts. If not specified, returns only user accounts by default.",
			},
			"users": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "List of users in the organization.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The identity ID of the user.",
						},
						"user_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The user ID of the user.",
						},
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The name of the user.",
						},
						"email": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The email address of the user.",
						},
						"permission_level": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The organization permission level (owner, collaborator, etc.).",
						},
						"created_at": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The timestamp when the user was added to the organization.",
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *OrganizationUsersDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *OrganizationUsersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config OrganizationUsersDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build query parameters
	queryParams := "?count=50"
	if !config.Email.IsNull() {
		queryParams += fmt.Sprintf("&email=%s", config.Email.ValueString())
	}
	if !config.Name.IsNull() {
		queryParams += fmt.Sprintf("&name=%s", config.Name.ValueString())
	}
	if !config.IsServiceAccount.IsNull() {
		queryParams += fmt.Sprintf("&is_service_account=%t", config.IsServiceAccount.ValueBool())
	}

	// Fetch organization users from API
	apiResp, err := d.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/organization_collaborators%s", queryParams), nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch organization users", map[string]any{"error": err.Error()})
		resp.Diagnostics.AddError("API Request Failed", fmt.Sprintf("Failed to fetch organization users: %s", err.Error()))
		return
	}
	defer func() {
		if closeErr := apiResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	body, err := io.ReadAll(apiResp.Body)
	if err != nil {
		resp.Diagnostics.AddError("Response Read Error", fmt.Sprintf("Failed to read response: %s", err.Error()))
		return
	}

	if apiResp.StatusCode != http.StatusOK {
		resp.Diagnostics.AddError(
			"API Error",
			fmt.Sprintf("Failed to list organization users: %s - %s", apiResp.Status, string(body)),
		)
		return
	}

	var usersResp struct {
		Results []struct {
			ID              string  `json:"id"`
			UserID          *string `json:"user_id"`
			Name            string  `json:"name"`
			Email           string  `json:"email"`
			PermissionLevel string  `json:"permission_level"`
			CreatedAt       string  `json:"created_at"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &usersResp); err != nil {
		resp.Diagnostics.AddError("JSON Unmarshal Error", fmt.Sprintf("Failed to unmarshal response: %s", err.Error()))
		return
	}

	// Convert to Terraform model
	users := make([]OrganizationUserModel, len(usersResp.Results))
	for i, user := range usersResp.Results {
		userID := types.StringNull()
		if user.UserID != nil {
			userID = types.StringValue(*user.UserID)
		}

		users[i] = OrganizationUserModel{
			ID:              types.StringValue(user.ID),
			UserID:          userID,
			Name:            types.StringValue(user.Name),
			Email:           types.StringValue(user.Email),
			PermissionLevel: types.StringValue(user.PermissionLevel),
			CreatedAt:       types.StringValue(user.CreatedAt),
		}
	}

	usersList, diags := types.ListValueFrom(ctx, types.ObjectType{
		AttrTypes: map[string]attr.Type{
			"id":               types.StringType,
			"user_id":          types.StringType,
			"name":             types.StringType,
			"email":            types.StringType,
			"permission_level": types.StringType,
			"created_at":       types.StringType,
		},
	}, users)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.Users = usersList

	tflog.Info(ctx, "Successfully retrieved organization users", map[string]any{
		"count": len(users),
	})

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
