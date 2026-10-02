package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = &RoleDataSource{}
	_ datasource.DataSourceWithConfigure = &RoleDataSource{}
)

// NewRoleDataSource creates a new role data source.
func NewRoleDataSource() datasource.DataSource {
	return &RoleDataSource{}
}

// RoleDataSource resolves an assignable role by name: a built-in role or one
// of the organization's own.
type RoleDataSource struct {
	client *Client
}

// RoleDataSourceModel describes the data source data model.
type RoleDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	BuiltIn     types.Bool   `tfsdk:"built_in"`
}

func (d *RoleDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

func (d *RoleDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: roleBindingsAlphaBanner +
			"Looks up a role that can be granted with `anyscale_role_binding`: a built-in role or one of the organization's own. " +
			"Archived roles are not found, since they cannot be newly granted. " +
			"The API does not check that a role suits the resource it is granted on, so a role with only organization permissions granted on a cloud is accepted and has no effect.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The role name, matched exactly, including case. Names can change; a binding refers to the role by `id`, so a rename does not affect existing bindings.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The role ID (`rol_...`), as `anyscale_role_binding.role_id` takes it.",
			},
			"description": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The role's description. Null when it has none.",
			},
			"built_in": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether Anyscale provides the role to every organization, rather than this organization defining it.",
			},
		},
	}
}

func (d *RoleDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		AddConfigError(&resp.Diagnostics, "Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	d.client = client
}

func (d *RoleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config RoleDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := config.Name.ValueString()
	roles, err := listRolesByName(ctx, d.client, name)
	if err != nil {
		// The roles listing has no 404 of its own, so a 404 is the feature
		// being off.
		if isRoleBindingsDisabled(err) {
			addRoleBindingsDisabledError(&resp.Diagnostics, "")
			return
		}
		AddAPIError(&resp.Diagnostics, "list roles", err)
		return
	}

	var exact []roleResult
	var folded []string
	for _, role := range roles {
		switch {
		case role.Name == name:
			exact = append(exact, role)
		case strings.EqualFold(role.Name, name):
			folded = append(folded, role.Name)
		}
	}
	switch len(exact) {
	case 0:
		detail := fmt.Sprintf("No assignable role is named %q. Archived roles are not listed.", name)
		if len(folded) > 0 {
			detail += fmt.Sprintf(" Names are matched with case: did you mean %q?", folded[0])
		}
		AddConfigError(&resp.Diagnostics, "Role Not Found", detail)
		return
	case 1:
	default:
		AddConfigError(&resp.Diagnostics, "Multiple Roles Found",
			fmt.Sprintf("%d roles are named %q, so the name does not identify one. Please report this issue to the provider developers.", len(exact), name))
		return
	}

	role := exact[0]
	config.ID = types.StringValue(role.ID)
	config.Name = types.StringValue(role.Name)
	config.Description = types.StringPointerValue(role.Description)
	config.BuiltIn = types.BoolValue(role.BuiltIn)
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
