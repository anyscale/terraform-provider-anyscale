package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource                     = &IAMUserGroupDataSource{}
	_ datasource.DataSourceWithConfigure        = &IAMUserGroupDataSource{}
	_ datasource.DataSourceWithConfigValidators = &IAMUserGroupDataSource{}
)

// NewIAMUserGroupDataSource creates a new user group data source.
func NewIAMUserGroupDataSource() datasource.DataSource {
	return &IAMUserGroupDataSource{}
}

// IAMUserGroupDataSource reads one user group, including directory-synced
// groups, which the resources refuse to manage.
type IAMUserGroupDataSource struct {
	client *Client
}

// IAMUserGroupDataSourceModel describes the data source data model.
type IAMUserGroupDataSourceModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	Source           types.String `tfsdk:"source"`
	Members          types.List   `tfsdk:"members"`
	OrganizationRole types.Object `tfsdk:"organization_role"`
}

var userGroupMemberAttrTypes = map[string]attr.Type{
	"user_id": types.StringType,
	"email":   types.StringType,
	"name":    types.StringType,
}

var userGroupOrganizationRoleAttrTypes = map[string]attr.Type{
	"base_role":        types.StringType,
	"additional_roles": types.ListType{ElemType: types.StringType},
}

func (d *IAMUserGroupDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_user_group"
}

func (d *IAMUserGroupDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: userGroupsBetaBanner +
			"Reads one user group by ID or name, including groups synced from an identity provider (SCIM), which the user group resources cannot manage.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The user group ID (`ug_...`). Specify exactly one of `id` or `name`.",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The group name, matched exactly. Specify exactly one of `id` or `name`.",
			},
			"source": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Where the group came from: `user` (created in Anyscale) or `scim` (synced from an identity provider). Null for groups created before Anyscale recorded this.",
			},
			"members": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The group's members, sorted by email. Members who have left the organization are not listed.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"user_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The member's user ID (`usr_...`), the `user_id` of `anyscale_iam_organization_user`.",
						},
						"email": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The member's email address.",
						},
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The member's display name; the email when the user has no name.",
						},
					},
				},
			},
			"organization_role": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The organization-level role granted to the group. Null when the group has none.",
				Attributes: map[string]schema.Attribute{
					"base_role": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "`owner` or `collaborator`.",
					},
					"additional_roles": schema.ListAttribute{
						Computed:            true,
						ElementType:         types.StringType,
						MarkdownDescription: "Additional organization roles, such as `image_reader`.",
					},
				},
			},
		},
	}
}

func (d *IAMUserGroupDataSource) ConfigValidators(ctx context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *IAMUserGroupDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if client, ok := clientFromProviderData(req.ProviderData, "Unexpected Data Source Configure Type", &resp.Diagnostics); ok {
		d.client = client
	}
}

func (d *IAMUserGroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if refuseRBACOnAzure(d.client, "anyscale_iam_user_group", &resp.Diagnostics) {
		return
	}
	var config IAMUserGroupDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupID := config.ID.ValueString()
	if config.ID.IsNull() {
		name := config.Name.ValueString()
		found, err := findUserGroupByName(ctx, d.client, name)
		if err != nil {
			AddAPIError(&resp.Diagnostics, "list user groups", err)
			return
		}
		if found == nil {
			AddConfigError(&resp.Diagnostics, "User Group Not Found",
				fmt.Sprintf("No user group named %q exists in this organization.", name))
			return
		}
		groupID = found.ID
	}

	// GET /{id} is the only route that returns organization_permissions.
	group, err := getUserGroup(ctx, d.client, groupID)
	if err != nil {
		if isUserGroupNotFound(err) {
			AddConfigError(&resp.Diagnostics, "User Group Not Found",
				fmt.Sprintf("No user group exists with ID %q in this organization.", groupID))
			return
		}
		AddAPIError(&resp.Diagnostics, "read user group", err)
		return
	}

	members, _, err := getUserGroupMembers(ctx, d.client, groupID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read user group members", err)
		return
	}
	memberValues := make([]attr.Value, 0, len(members))
	for _, m := range members {
		obj, diags := types.ObjectValue(userGroupMemberAttrTypes, map[string]attr.Value{
			"user_id": types.StringValue(m.UserID),
			"email":   types.StringValue(m.UserEmail),
			"name":    types.StringValue(m.UserName),
		})
		resp.Diagnostics.Append(diags...)
		memberValues = append(memberValues, obj)
	}
	memberList, diags := types.ListValue(types.ObjectType{AttrTypes: userGroupMemberAttrTypes}, memberValues)
	resp.Diagnostics.Append(diags...)

	role, diags := userGroupOrganizationRoleValue(group.OrganizationPermissions)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &IAMUserGroupDataSourceModel{
		ID:               types.StringValue(group.ID),
		Name:             types.StringValue(group.Name),
		Source:           types.StringPointerValue(group.Source),
		Members:          memberList,
		OrganizationRole: role,
	})...)
}

// userGroupOrganizationRoleValue maps organization_permissions to the
// organization_role object. A null object, or one with no base role, means
// the group holds no organization role.
func userGroupOrganizationRoleValue(perms *userGroupOrganizationPermsAPI) (types.Object, diag.Diagnostics) {
	if perms == nil || perms.BaseRole == nil {
		return types.ObjectNull(userGroupOrganizationRoleAttrTypes), nil
	}
	additional := make([]attr.Value, 0, len(perms.AdditionalRoles))
	for _, r := range perms.AdditionalRoles {
		additional = append(additional, types.StringValue(r))
	}
	list, diags := types.ListValue(types.StringType, additional)
	if diags.HasError() {
		return types.ObjectNull(userGroupOrganizationRoleAttrTypes), diags
	}
	return types.ObjectValue(userGroupOrganizationRoleAttrTypes, map[string]attr.Value{
		"base_role":        types.StringPointerValue(perms.BaseRole),
		"additional_roles": list,
	})
}
