package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &OrganizationDataSource{}
	_ datasource.DataSourceWithConfigure = &OrganizationDataSource{}
)

// NewOrganizationDataSource returns a new organization data source.
func NewOrganizationDataSource() datasource.DataSource {
	return &OrganizationDataSource{}
}

// OrganizationDataSource defines the data source implementation.
type OrganizationDataSource struct {
	client *Client
}

// OrganizationDataSourceModel describes the data source data model.
type OrganizationDataSourceModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	PublicIdentifier types.String `tfsdk:"public_identifier"`
	DefaultCloudID   types.String `tfsdk:"default_cloud_id"`
}

// Metadata returns the data source type name.
func (d *OrganizationDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization"
}

// Schema defines the data source schema.
func (d *OrganizationDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Use this data source to retrieve identity information about the organization the provider's token is connected to. Takes no arguments: an Anyscale API token is always scoped to exactly one organization, so there is never a set to select from.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the organization.",
			},
			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The name of the organization.",
			},
			"public_identifier": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The public, human-facing identifier of the organization.",
			},
			"default_cloud_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The ID of the organization's default cloud. Null if the organization has no default cloud configured.",
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *OrganizationDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *OrganizationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	state, err := fetchCurrentOrganization(ctx, d.client)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "fetch organization info", err)
		return
	}

	tflog.Info(ctx, "Successfully retrieved organization info", map[string]any{
		"organization_id": state.ID.ValueString(),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// organizationUserInfoResponse is the subset of GET /api/v2/userinfo this
// data source needs.
type organizationUserInfoResponse struct {
	Result struct {
		Organizations []struct {
			ID               string  `json:"id"`
			Name             string  `json:"name"`
			PublicIdentifier string  `json:"public_identifier"`
			DefaultCloudID   *string `json:"default_cloud_id"`
		} `json:"organizations"`
	} `json:"result"`
}

// fetchCurrentOrganization fetches /api/v2/userinfo and maps
// result.organizations[0] to the organization data source model.
//
// The backend userinfo handler always returns exactly one element in
// organizations (the token-scoped org) - see
// .crystl/quest/design/anyscale_organization_contract.md. An empty list is
// treated as a real anomaly rather than a panic.
func fetchCurrentOrganization(ctx context.Context, client *Client) (*OrganizationDataSourceModel, error) {
	userInfo, err := DoRequestAndParse[organizationUserInfoResponse](ctx, client, "GET", "/api/v2/userinfo", nil, http.StatusOK)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user info: %w", err)
	}

	if len(userInfo.Result.Organizations) == 0 {
		return nil, fmt.Errorf("userinfo returned no organization for the authenticated token")
	}

	org := userInfo.Result.Organizations[0]

	return &OrganizationDataSourceModel{
		ID:               types.StringValue(org.ID),
		Name:             types.StringValue(org.Name),
		PublicIdentifier: types.StringValue(org.PublicIdentifier),
		DefaultCloudID:   types.StringPointerValue(org.DefaultCloudID),
	}, nil
}
