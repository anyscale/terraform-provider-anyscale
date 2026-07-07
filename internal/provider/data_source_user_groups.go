package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &UserGroupsDataSource{}
	_ datasource.DataSourceWithConfigure = &UserGroupsDataSource{}
)

// NewUserGroupsDataSource returns a new user groups data source.
func NewUserGroupsDataSource() datasource.DataSource {
	return &UserGroupsDataSource{}
}

// UserGroupsDataSource defines the data source implementation.
type UserGroupsDataSource struct {
	client *Client
}

// UserGroupsDataSourceModel describes the data source data model.
type UserGroupsDataSourceModel struct {
	Groups types.List `tfsdk:"groups"`
}

// UserGroupModel represents a single user group.
type UserGroupModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	OrgID     types.String `tfsdk:"org_id"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

// Metadata returns the data source type name.
func (d *UserGroupsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_groups"
}

// Schema defines the data source schema.
func (d *UserGroupsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "**BETA FEATURE**: Use this data source to retrieve a list of all user groups in your organization. User groups are typically synced from your identity provider (IdP) via SCIM and are used for managing group-based permissions on resources.",

		Attributes: map[string]schema.Attribute{
			"groups": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "List of user groups in the organization.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The unique identifier of the user group (format: `ug_*`).",
						},
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The name of the user group.",
						},
						"org_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The organization ID this group belongs to.",
						},
						"created_at": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The timestamp when the group was created.",
						},
						"updated_at": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The timestamp when the group was last updated.",
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *UserGroupsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *UserGroupsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state UserGroupsDataSourceModel

	// Fetch user groups from API. Confirmed non-paginated (no next_paging_token
	// in the response) - not an a41c8e2d gap.
	apiResp, err := d.client.DoRequest(ctx, "GET", "/api/v2/user_groups", nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch user groups", map[string]any{"error": err.Error()})
		resp.Diagnostics.AddError("API Request Failed", fmt.Sprintf("Failed to fetch user groups: %s", err.Error()))
		return
	}
	defer func() {
		if closeErr := apiResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	body, err := io.ReadAll(apiResp.Body)
	if err != nil {
		resp.Diagnostics.AddError("Response Read Error", fmt.Sprintf("Failed to read response: %s", err.Error()))
		return
	}

	if apiResp.StatusCode != http.StatusOK {
		resp.Diagnostics.AddError(
			"API Error",
			fmt.Sprintf("Failed to list user groups: %s - %s", apiResp.Status, string(body)),
		)
		return
	}

	var groupsResp struct {
		Results []struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			OrgID     string  `json:"org_id"`
			CreatedAt string  `json:"created_at"`
			UpdatedAt string  `json:"updated_at"`
			DeletedAt *string `json:"deleted_at"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &groupsResp); err != nil {
		resp.Diagnostics.AddError("JSON Unmarshal Error", fmt.Sprintf("Failed to unmarshal response: %s", err.Error()))
		return
	}

	// Filter out deleted groups and convert to Terraform model
	groups := make([]UserGroupModel, 0, len(groupsResp.Results))
	for _, group := range groupsResp.Results {
		// Skip deleted groups
		if group.DeletedAt != nil {
			continue
		}

		groups = append(groups, UserGroupModel{
			ID:        types.StringValue(group.ID),
			Name:      types.StringValue(group.Name),
			OrgID:     types.StringValue(group.OrgID),
			CreatedAt: types.StringValue(group.CreatedAt),
			UpdatedAt: types.StringValue(group.UpdatedAt),
		})
	}

	groupsList, diags := types.ListValueFrom(ctx, types.ObjectType{
		AttrTypes: map[string]attr.Type{
			"id":         types.StringType,
			"name":       types.StringType,
			"org_id":     types.StringType,
			"created_at": types.StringType,
			"updated_at": types.StringType,
		},
	}, groups)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Groups = groupsList

	tflog.Info(ctx, "Successfully retrieved user groups", map[string]any{
		"count": len(groups),
	})

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
