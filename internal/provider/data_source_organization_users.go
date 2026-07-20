package provider

import (
	"context"
	"fmt"
	"net/url"

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

	// DS-OU-2 (Phase B): permission_level above is deprecated backend-side in
	// favor of these two.
	BaseRole        types.String `tfsdk:"base_role"`
	AdditionalRoles types.List   `tfsdk:"additional_roles"`
}

// Metadata returns the data source type name.
func (d *OrganizationUsersDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_users"
}

// Schema defines the data source schema.
func (d *OrganizationUsersDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	itemAttributes := organizationUserSharedAttributes()
	itemAttributes["id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The identity ID of the user.",
	}
	itemAttributes["user_id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The user ID of the user.",
	}
	itemAttributes["email"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The email address of the user.",
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Use this data source to retrieve a list of all users (including service accounts) in your organization. Useful for auditing organization membership, resolving `id` values before importing `anyscale_organization_collaborator` resources, or filtering users by email or account type.\n\n" +
			"The organization role model is migrating from a single `permission_level` to `base_role` plus `additional_roles` - see those attributes below.",

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
					Attributes: itemAttributes,
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
		AddConfigError(&resp.Diagnostics,
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

	// Build extra query parameters (page size is added by listAllOrganizationCollaborators)
	extraParams := url.Values{}
	if !config.Email.IsNull() {
		extraParams.Set("email", config.Email.ValueString())
	}
	if !config.Name.IsNull() {
		extraParams.Set("name", config.Name.ValueString())
	}
	if !config.IsServiceAccount.IsNull() {
		extraParams.Set("is_service_account", fmt.Sprintf("%t", config.IsServiceAccount.ValueBool()))
	}

	// Fetch organization users from API, across every page rather than just the first.
	collaborators, err := listAllOrganizationCollaborators(ctx, d.client, extraParams)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch organization users", map[string]any{"error": err.Error()})
		AddAPIError(&resp.Diagnostics, "fetch organization users", err)
		return
	}

	// Convert to Terraform model. DS-OU-1: name is genuinely nullable server-side,
	// mapped via StringPointerValue matching the adjacent UserID field - a null
	// name must never collapse to "".
	//
	// additional_roles is backfilled per result via a supplementary singular GET
	// (hydrateCollaboratorRoles) - the list endpoint this data source's primary
	// fetch uses hardcodes it to empty unconditionally (architect ruling 1), and
	// switching the primary fetch to POST /search to get it in bulk was traced
	// and rejected: search has no is_service_account filter and only a combined
	// name_or_email field, so it cannot replace list-and-filter without losing
	// this data source's existing filters. This is therefore N+1 (one extra
	// request per result, bounded by page size) - an accepted, deliberate
	// trade-off for an auditing data source, not an oversight.
	users := make([]OrganizationUserModel, len(collaborators))
	for i, user := range collaborators {
		user = hydrateCollaboratorRoles(ctx, d.client, user)

		additionalRoles, diags := additionalRolesToList(ctx, user.AdditionalRoles)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		users[i] = OrganizationUserModel{
			ID:              types.StringValue(user.ID),
			UserID:          types.StringPointerValue(user.UserID),
			Name:            types.StringPointerValue(user.Name),
			Email:           types.StringValue(user.Email),
			PermissionLevel: types.StringValue(user.PermissionLevel),
			CreatedAt:       types.StringValue(user.CreatedAt),
			BaseRole:        types.StringValue(user.BaseRole),
			AdditionalRoles: additionalRoles,
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
			"base_role":        types.StringType,
			"additional_roles": types.ListType{ElemType: types.StringType},
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
