package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// anyscale_cloud_iam_mapping (data source) - read-only, keyed the same way
// as the resource. The C12 "provider produced inconsistent result after
// apply" constraint that stops anyscale_cloud_resource's config blocks from
// refreshing does not apply here: every attribute is Computed and a data
// source contributes nothing to a resource plan, so a real GET on every
// read is unconditionally safe. See
// docs/decisions/cloud-iam-mapping/README.md's Data source section.
var (
	_ datasource.DataSource              = &CloudIAMMappingDataSource{}
	_ datasource.DataSourceWithConfigure = &CloudIAMMappingDataSource{}
)

// NewCloudIAMMappingDataSource returns a new cloud IAM mapping data source.
func NewCloudIAMMappingDataSource() datasource.DataSource {
	return &CloudIAMMappingDataSource{}
}

// CloudIAMMappingDataSource defines the data source implementation.
type CloudIAMMappingDataSource struct {
	client *Client
}

// CloudIAMMappingDataSourceModel describes the data source data model.
type CloudIAMMappingDataSourceModel struct {
	ID              types.String `tfsdk:"id"`
	CloudID         types.String `tfsdk:"cloud_id"`
	CloudResourceID types.String `tfsdk:"cloud_resource_id"`
	Rules           types.List   `tfsdk:"rules"`
	FallbackRule    types.String `tfsdk:"fallback_rule"`
	Mode            types.String `tfsdk:"mode"`
}

func (d *CloudIAMMappingDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_iam_mapping"
}

func (d *CloudIAMMappingDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the current dataplane IAM mapping of one Anyscale cloud deployment - the " +
			"ordered rules that route a launching workload to a cloud IAM identity, plus the fallback behavior " +
			"when no rule matches. See the [`anyscale_cloud_iam_mapping` resource](../resources/cloud_iam_mapping.md) " +
			"to manage this value instead of only reading it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`<cloud_id>/<cloud_resource_id>`.",
			},
			"cloud_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The cloud to read the mapping from. To reference a cloud by name instead of by id, look it up with the [`anyscale_cloud` data source](./cloud.md).",
			},
			"cloud_resource_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "The cloud deployment to read the mapping from. When omitted, resolves to " +
					"the cloud's primary deployment (`is_default`) and is written back to the data source's result.",
			},
			"rules": schema.ListNestedAttribute{
				Computed: true,
				MarkdownDescription: "The mapping's rules, in the order they are evaluated - the first matching " +
					"rule wins. Null when the cloud has no mapping configured.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"selector": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The Kubernetes label-selector expression matched against the launching workload.",
						},
						"value": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The cloud IAM identity this rule assigns when its selector matches.",
						},
					},
				},
			},
			"fallback_rule": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "What happens when no rule in `rules` matches: `CLOUD_DEFAULT` or `FAIL`. " +
					"Null when `rules` is empty.",
			},
			"mode": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The mapping's server-derived mode, currently always `CUSTOMER_MANAGED` once any rule exists. Null when `rules` is empty.",
			},
		},
	}
}

func (d *CloudIAMMappingDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *CloudIAMMappingDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config CloudIAMMappingDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := config.CloudID.ValueString()
	cloudResourceID := config.CloudResourceID.ValueString()
	if config.CloudResourceID.IsNull() || config.CloudResourceID.IsUnknown() {
		resolved, err := resolvePrimaryCloudResourceID(ctx, d.client, cloudID)
		if err != nil {
			resp.Diagnostics.AddError("Unable to resolve primary cloud resource", err.Error())
			return
		}
		cloudResourceID = resolved
	}

	current, err := getCloudDeploymentConfig(ctx, d.client, cloudID, cloudResourceID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read cloud IAM mapping", err)
		return
	}

	rulesList, diags := dataplaneIAMMappingRulesToState(current.DataplaneIAMMapping.Rules)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.ID = types.StringValue(cloudID + "/" + cloudResourceID)
	config.CloudResourceID = types.StringValue(cloudResourceID)
	config.Rules = rulesList
	config.FallbackRule = stringOrNull(current.DataplaneIAMMapping.FallbackRule)
	config.Mode = stringOrNull(current.DataplaneIAMMapping.Mode)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
