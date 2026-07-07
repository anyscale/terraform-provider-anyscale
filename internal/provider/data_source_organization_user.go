package provider

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource                     = &OrganizationUserDataSource{}
	_ datasource.DataSourceWithConfigure        = &OrganizationUserDataSource{}
	_ datasource.DataSourceWithConfigValidators = &OrganizationUserDataSource{}
)

// NewOrganizationUserDataSource returns a new organization user data source.
func NewOrganizationUserDataSource() datasource.DataSource {
	return &OrganizationUserDataSource{}
}

// OrganizationUserDataSource defines the data source implementation.
type OrganizationUserDataSource struct {
	client *Client
}

// OrganizationUserDataSourceModel describes the data source data model.
type OrganizationUserDataSourceModel struct {
	ID              types.String `tfsdk:"id"`
	UserID          types.String `tfsdk:"user_id"`
	Email           types.String `tfsdk:"email"`
	Name            types.String `tfsdk:"name"`
	PermissionLevel types.String `tfsdk:"permission_level"`
	CreatedAt       types.String `tfsdk:"created_at"`
}

// Metadata returns the data source type name.
func (d *OrganizationUserDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_user"
}

// Schema defines the data source schema.
func (d *OrganizationUserDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "**BETA FEATURE**: Use this data source to retrieve information about a specific user in your organization. You can look up a user by their identity ID, user ID, or email address.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The identity ID of the user. Either `id`, `user_id`, or `email` must be specified.",
			},
			"user_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The user ID of the user. Either `id`, `user_id`, or `email` must be specified.",
			},
			"email": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The email address of the user. Either `id`, `user_id`, or `email` must be specified.",
			},
			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The name of the user.",
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
	}
}

// ConfigValidators returns validators for the data source configuration.
func (d *OrganizationUserDataSource) ConfigValidators(ctx context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("id"),
			path.MatchRoot("user_id"),
			path.MatchRoot("email"),
		),
	}
}

// Configure adds the provider configured client to the data source.
func (d *OrganizationUserDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *OrganizationUserDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config OrganizationUserDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Determine which lookup method to use
	var user *OrganizationUserDataSourceModel
	var err error

	if !config.ID.IsNull() {
		// Look up by identity ID
		user, err = d.findUserByID(ctx, config.ID.ValueString())
	} else if !config.UserID.IsNull() {
		// Look up by user ID
		user, err = d.findUserByUserID(ctx, config.UserID.ValueString())
	} else if !config.Email.IsNull() {
		// Look up by email
		user, err = d.findUserByEmail(ctx, config.Email.ValueString())
	} else {
		resp.Diagnostics.AddError(
			"Missing Required Attribute",
			"One of id, user_id, or email must be specified",
		)
		return
	}

	if err != nil {
		resp.Diagnostics.AddError("User Lookup Failed", err.Error())
		return
	}

	if user == nil {
		resp.Diagnostics.AddError(
			"User Not Found",
			"No user found matching the specified criteria in Anyscale",
		)
		return
	}

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, user)...)
}

// organizationCollaboratorToUserModel converts a shared API result into this
// data source's model.
func organizationCollaboratorToUserModel(u OrganizationCollaboratorResult) *OrganizationUserDataSourceModel {
	userID := types.StringNull()
	if u.UserID != nil {
		userID = types.StringValue(*u.UserID)
	}

	name := ""
	if u.Name != nil {
		name = *u.Name
	}

	return &OrganizationUserDataSourceModel{
		ID:              types.StringValue(u.ID),
		UserID:          userID,
		Name:            types.StringValue(name),
		Email:           types.StringValue(u.Email),
		PermissionLevel: types.StringValue(u.PermissionLevel),
		CreatedAt:       types.StringValue(u.CreatedAt),
	}
}

// findUserByID looks up a user by their identity ID, paging through the full
// collaborator list rather than only the first page.
func (d *OrganizationUserDataSource) findUserByID(ctx context.Context, id string) (*OrganizationUserDataSourceModel, error) {
	users, err := listAllOrganizationCollaborators(ctx, d.client, nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch organization users", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("failed to fetch organization users: %w", err)
	}

	for _, u := range users {
		if u.ID == id {
			return organizationCollaboratorToUserModel(u), nil
		}
	}

	return nil, nil
}

// findUserByUserID looks up a user by their user ID, paging through the full
// collaborator list rather than only the first page.
func (d *OrganizationUserDataSource) findUserByUserID(ctx context.Context, userID string) (*OrganizationUserDataSourceModel, error) {
	users, err := listAllOrganizationCollaborators(ctx, d.client, nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch organization users", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("failed to fetch organization users: %w", err)
	}

	for _, u := range users {
		if u.UserID != nil && *u.UserID == userID {
			return organizationCollaboratorToUserModel(u), nil
		}
	}

	return nil, nil
}

// findUserByEmail looks up a user by their email address. The email query
// param narrows results server-side, but pagination is still applied in case
// that filter is not a strict exact match, rather than only ever inspecting
// its first page.
func (d *OrganizationUserDataSource) findUserByEmail(ctx context.Context, email string) (*OrganizationUserDataSourceModel, error) {
	users, err := listAllOrganizationCollaborators(ctx, d.client, url.Values{"email": []string{email}})
	if err != nil {
		tflog.Error(ctx, "Failed to fetch organization users", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("failed to fetch organization users: %w", err)
	}

	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			return organizationCollaboratorToUserModel(u), nil
		}
	}

	return nil, nil
}
