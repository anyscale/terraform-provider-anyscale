package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = &UserGroupsDataSource{}
	_ datasource.DataSourceWithConfigure = &UserGroupsDataSource{}
)

// NewUserGroupsDataSource creates a new user groups data source.
func NewUserGroupsDataSource() datasource.DataSource {
	return &UserGroupsDataSource{}
}

// UserGroupsDataSource lists the organization's user groups.
type UserGroupsDataSource struct {
	client *Client
}

// UserGroupsDataSourceModel describes the data source data model.
type UserGroupsDataSourceModel struct {
	Source types.String `tfsdk:"source"`
	Groups types.List   `tfsdk:"groups"`
}

var userGroupSummaryAttrTypes = map[string]attr.Type{
	"id":     types.StringType,
	"name":   types.StringType,
	"source": types.StringType,
}

func (d *UserGroupsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_groups"
}

func (d *UserGroupsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: userGroupsAlphaBanner +
			"Lists the user groups in the token's organization. Use `anyscale_user_group` for a group's members and organization role.",
		Attributes: map[string]schema.Attribute{
			"source": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Return only groups with this source: `user` or `scim`. Groups whose source is null match neither.",
				Validators: []validator.String{
					stringvalidator.OneOf(userGroupSourceUser, userGroupSourceSCIM),
				},
			},
			"groups": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The matching groups, most recently created first.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The user group ID (`ug_...`).",
						},
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The group name.",
						},
						"source": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "`user`, `scim`, or null for groups created before Anyscale recorded this.",
						},
					},
				},
			},
		},
	}
}

func (d *UserGroupsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	d.client = client
}

func (d *UserGroupsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config UserGroupsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groups, err := listUserGroups(ctx, d.client)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "list user groups", err)
		return
	}

	values := make([]attr.Value, 0, len(groups))
	for _, g := range groups {
		if !config.Source.IsNull() && (g.Source == nil || *g.Source != config.Source.ValueString()) {
			continue
		}
		obj, diags := types.ObjectValue(userGroupSummaryAttrTypes, map[string]attr.Value{
			"id":     types.StringValue(g.ID),
			"name":   types.StringValue(g.Name),
			"source": types.StringPointerValue(g.Source),
		})
		resp.Diagnostics.Append(diags...)
		values = append(values, obj)
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: userGroupSummaryAttrTypes}, values)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.Groups = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
