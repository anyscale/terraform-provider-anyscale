package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource                     = &IAMRoleBindingDataSource{}
	_ datasource.DataSourceWithConfigure        = &IAMRoleBindingDataSource{}
	_ datasource.DataSourceWithConfigValidators = &IAMRoleBindingDataSource{}
)

// NewIAMRoleBindingDataSource creates a new role binding policy data source.
func NewIAMRoleBindingDataSource() datasource.DataSource {
	return &IAMRoleBindingDataSource{}
}

// IAMRoleBindingDataSource reads every direct role binding on one scope.
type IAMRoleBindingDataSource struct {
	client *Client
}

func (d *IAMRoleBindingDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_role_binding"
}

func (d *IAMRoleBindingDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	scopeDescription := "Specify exactly one of `organization_id`, `cloud_id` or `project_id`."
	idSet := func(description string) schema.SetAttribute {
		return schema.SetAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: description}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: roleBindingsBetaBanner +
			"Reads every direct role binding on one organization, cloud or project, in the shape `anyscale_iam_role_binding` takes. " +
			"Bindings held on a parent scope are not included. Reading requires managing IAM on the scope.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`<scope_type>/<scope_id>`, for example `project/prj_...`.",
			},
			"organization_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Read the bindings on the organization: the token's own, as returned by the `anyscale_organization` data source. " + scopeDescription,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"cloud_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Read the bindings on this cloud. " + scopeDescription,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Read the bindings on this project. " + scopeDescription,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"roles": schema.MapNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Every direct binding on the scope, keyed by role ID (`rol_...`).",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"user_ids":       idSet("Users (`usr_...`) that hold the role. Null when none do."),
						"user_group_ids": idSet("User groups (`ug_...`) that hold the role. Null when none do."),
					},
				},
			},
		},
	}
}

func (d *IAMRoleBindingDataSource) ConfigValidators(ctx context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("organization_id"),
			path.MatchRoot("cloud_id"),
			path.MatchRoot("project_id"),
		),
	}
}

func (d *IAMRoleBindingDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if client, ok := clientFromProviderData(req.ProviderData, "Unexpected Data Source Configure Type", &resp.Diagnostics); ok {
		d.client = client
	}
}

func (d *IAMRoleBindingDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if refuseRBACOnAzure(d.client, iamRoleBindingTypeName, &resp.Diagnostics) {
		return
	}
	var config RoleBindingPolicyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	scopeType, scopeIDValue := config.scope()
	scopeID := scopeIDValue.ValueString()

	current, err := generateRoleBindingPolicy(ctx, d.client, scopeType, scopeID)
	if err != nil {
		switch {
		case isHTTPStatus(err, http.StatusForbidden):
			// A data source has no state to drop, so a deleted scope and lost
			// access are both errors.
			resp.Diagnostics.AddError("Role Binding Not Readable",
				"The Anyscale API refused to read the role bindings on "+scopeType+" "+scopeID+" (HTTP 403): "+extractAPIErrorDetail(err)+
					" The "+scopeType+" does not exist, or this identity does not manage IAM on it.")
		case isRoleBindingsDisabled(err):
			policyNotFound(ctx, d.client, &resp.Diagnostics, "read role binding policy", err, "")
		default:
			AddAPIError(&resp.Diagnostics, "read role binding policy", err)
		}
		return
	}

	roles, ok := policyRolesValue(current, &resp.Diagnostics)
	if !ok {
		return
	}
	config.ID = types.StringValue(roleBindingPolicyID(scopeType, scopeID))
	config.Roles = roles
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
