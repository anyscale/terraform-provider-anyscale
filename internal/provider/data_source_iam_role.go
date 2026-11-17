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
	_ datasource.DataSource              = &IAMRoleDataSource{}
	_ datasource.DataSourceWithConfigure = &IAMRoleDataSource{}
)

// NewIAMRoleDataSource creates a new role data source.
func NewIAMRoleDataSource() datasource.DataSource {
	return &IAMRoleDataSource{}
}

// IAMRoleDataSource resolves an assignable role by name: a built-in role or one
// of the organization's own.
type IAMRoleDataSource struct {
	client *Client
}

// IAMRoleDataSourceModel describes the data source data model.
type IAMRoleDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	BuiltIn     types.Bool   `tfsdk:"built_in"`
}

func (d *IAMRoleDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_role"
}

func (d *IAMRoleDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: roleBindingsBetaBanner +
			"Looks up a role that can be granted with `anyscale_iam_role_grant`: a built-in role or one of the organization's own. " +
			"Archived roles are not found, since they cannot be newly granted. " +
			"The API does not check that a role suits the resource it is granted on, so a role with only organization permissions granted on a cloud is accepted and has no effect.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The role name, matched exactly, including case, for example `Project reader`. Names can change; a binding refers to the role by `id`, so a rename does not affect existing bindings. The built-in role being renamed from `Organization owner` to `Admin` resolves by either name in every organization; use `Admin`.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The role ID (`rol_...`), as `anyscale_iam_role_grant.role_id` takes it.",
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

func (d *IAMRoleDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if client, ok := clientFromProviderData(req.ProviderData, "Unexpected Data Source Configure Type", &resp.Diagnostics); ok {
		d.client = client
	}
}

func (d *IAMRoleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if refuseRBACOnAzure(d.client, "anyscale_iam_role", &resp.Diagnostics) {
		return
	}
	var config IAMRoleDataSourceModel
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
	if len(exact) == 0 {
		if alias, ok := builtInRoleRenames[name]; ok {
			role, found, err := findBuiltInRoleByName(ctx, d.client, alias)
			if err != nil {
				AddAPIError(&resp.Diagnostics, "list roles", err)
				return
			}
			if found {
				if name == builtInRoleOldName {
					resp.Diagnostics.AddWarning("Role Renamed",
						fmt.Sprintf("The built-in role %q is now named %q in this organization. Set name = %q.", name, alias, alias))
				}
				exact = append(exact, role)
			}
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

	// name stays as configured: on the rename fallback it differs from the
	// role's stored name, and it is an argument, not a read-back.
	role := exact[0]
	config.ID = types.StringValue(role.ID)
	config.Description = types.StringPointerValue(role.Description)
	config.BuiltIn = types.BoolValue(role.BuiltIn)
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// The built-in organization owner role (rol_zdti...) was renamed from
// "Organization owner" to "Admin". Role rows are renamed per environment by a
// backend job, not a migration, so for a while some organizations return one
// name and some the other. Either name resolves the role in every
// organization. Only built-in roles take the fallback, so a custom role that
// shares a name still wins on an exact match.
const (
	builtInRoleOldName = "Organization owner"
	builtInRoleNewName = "Admin"
)

var builtInRoleRenames = map[string]string{
	builtInRoleOldName: builtInRoleNewName,
	builtInRoleNewName: builtInRoleOldName,
}

// findBuiltInRoleByName returns the one built-in role named exactly name.
func findBuiltInRoleByName(ctx context.Context, client *Client, name string) (roleResult, bool, error) {
	roles, err := listRolesByName(ctx, client, name)
	if err != nil {
		return roleResult{}, false, err
	}
	var match []roleResult
	for _, role := range roles {
		if role.BuiltIn && role.Name == name {
			match = append(match, role)
		}
	}
	if len(match) != 1 {
		return roleResult{}, false, nil
	}
	return match[0], true, nil
}
