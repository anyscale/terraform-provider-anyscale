package provider

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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

	// DS-OU-2 (Phase B): permission_level above is deprecated backend-side in
	// favor of these two.
	BaseRole        types.String `tfsdk:"base_role"`
	AdditionalRoles types.List   `tfsdk:"additional_roles"`
}

// Metadata returns the data source type name.
func (d *OrganizationUserDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_user"
}

// Schema defines the data source schema.
func (d *OrganizationUserDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attributes := organizationUserSharedAttributes()
	attributes["id"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The identity ID of the user. Either `id`, `user_id`, or `email` must be specified.",
	}
	attributes["user_id"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The user ID of the user. Either `id`, `user_id`, or `email` must be specified.",
	}
	attributes["email"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The email address of the user. Either `id`, `user_id`, or `email` must be specified.",
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Use this data source to retrieve information about a specific user in your organization. You can look up a user by their identity ID, user ID, or email address.\n\n" +
			"The organization role model is migrating from a single `permission_level` to `base_role` plus `additional_roles` - see those attributes below.",
		Attributes: attributes,
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
	var lookupDiags diag.Diagnostics
	var err error

	if !config.ID.IsNull() {
		// Look up by identity ID
		user, lookupDiags, err = d.findUserByID(ctx, config.ID.ValueString())
	} else if !config.UserID.IsNull() {
		// Look up by user ID
		user, lookupDiags, err = d.findUserByUserID(ctx, config.UserID.ValueString())
	} else if !config.Email.IsNull() {
		// Look up by email
		user, lookupDiags, err = d.findUserByEmail(ctx, config.Email.ValueString())
	} else {
		AddConfigError(&resp.Diagnostics,
			"Missing Required Attribute",
			"One of id, user_id, or email must be specified",
		)
		return
	}

	if err != nil {
		AddAPIError(&resp.Diagnostics, "look up organization user", err)
		return
	}

	resp.Diagnostics.Append(lookupDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if user == nil {
		AddConfigError(&resp.Diagnostics,
			"User Not Found",
			"No user found matching the specified criteria in Anyscale",
		)
		return
	}

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, user)...)
}

// organizationCollaboratorToUserModel converts a shared API result into this
// data source's model. DS-OU-1: name is genuinely nullable server-side (see
// models.go's OrganizationCollaboratorResult.Name) and mapped via
// StringPointerValue, matching the adjacent UserID field's existing handling
// - a null name must never collapse to "". u is expected to already be
// role-hydrated (see hydrateCollaboratorRoles) by the caller.
func organizationCollaboratorToUserModel(ctx context.Context, u OrganizationCollaboratorResult) (*OrganizationUserDataSourceModel, diag.Diagnostics) {
	// additional_roles tri-state: nil (undetermined, from hydrateCollaboratorRoles)
	// renders null; a non-nil (possibly empty) slice renders as a real list,
	// never null, when genuinely queried-and-none.
	var additionalRoles types.List
	var diags diag.Diagnostics
	if u.AdditionalRoles == nil {
		additionalRoles = types.ListNull(types.StringType)
	} else {
		additionalRoles, diags = types.ListValueFrom(ctx, types.StringType, u.AdditionalRoles)
		if diags.HasError() {
			return nil, diags
		}
	}

	return &OrganizationUserDataSourceModel{
		ID:              types.StringValue(u.ID),
		UserID:          types.StringPointerValue(u.UserID),
		Name:            types.StringPointerValue(u.Name),
		Email:           types.StringValue(u.Email),
		PermissionLevel: types.StringValue(u.PermissionLevel),
		CreatedAt:       types.StringValue(u.CreatedAt),
		BaseRole:        types.StringValue(u.BaseRole),
		AdditionalRoles: additionalRoles,
	}, diags
}

// findUserByID looks up a user by their identity ID, paging through the full
// collaborator list rather than only the first page.
func (d *OrganizationUserDataSource) findUserByID(ctx context.Context, id string) (*OrganizationUserDataSourceModel, diag.Diagnostics, error) {
	users, err := listAllOrganizationCollaborators(ctx, d.client, nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch organization users", map[string]any{"error": err.Error()})
		return nil, nil, fmt.Errorf("failed to fetch organization users: %w", err)
	}

	for _, u := range users {
		if u.ID == id {
			model, diags := organizationCollaboratorToUserModel(ctx, hydrateCollaboratorRoles(ctx, d.client, u))
			return model, diags, nil
		}
	}

	return nil, nil, nil
}

// findUserByUserID looks up a user by their user ID, paging through the full
// collaborator list rather than only the first page.
func (d *OrganizationUserDataSource) findUserByUserID(ctx context.Context, userID string) (*OrganizationUserDataSourceModel, diag.Diagnostics, error) {
	users, err := listAllOrganizationCollaborators(ctx, d.client, nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch organization users", map[string]any{"error": err.Error()})
		return nil, nil, fmt.Errorf("failed to fetch organization users: %w", err)
	}

	for _, u := range users {
		if u.UserID != nil && *u.UserID == userID {
			model, diags := organizationCollaboratorToUserModel(ctx, hydrateCollaboratorRoles(ctx, d.client, u))
			return model, diags, nil
		}
	}

	return nil, nil, nil
}

// findUserByEmail looks up a user by their email address. The email query
// param narrows results server-side, but pagination is still applied in case
// that filter is not a strict exact match, rather than only ever inspecting
// its first page.
func (d *OrganizationUserDataSource) findUserByEmail(ctx context.Context, email string) (*OrganizationUserDataSourceModel, diag.Diagnostics, error) {
	users, err := listAllOrganizationCollaborators(ctx, d.client, url.Values{"email": []string{email}})
	if err != nil {
		tflog.Error(ctx, "Failed to fetch organization users", map[string]any{"error": err.Error()})
		return nil, nil, fmt.Errorf("failed to fetch organization users: %w", err)
	}

	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			model, diags := organizationCollaboratorToUserModel(ctx, hydrateCollaboratorRoles(ctx, d.client, u))
			return model, diags, nil
		}
	}

	return nil, nil, nil
}
