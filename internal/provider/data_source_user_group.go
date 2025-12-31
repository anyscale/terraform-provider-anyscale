package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &UserGroupDataSource{}
	_ datasource.DataSourceWithConfigure = &UserGroupDataSource{}
)

// NewUserGroupDataSource returns a new user group data source.
func NewUserGroupDataSource() datasource.DataSource {
	return &UserGroupDataSource{}
}

// UserGroupDataSource defines the data source implementation.
type UserGroupDataSource struct {
	client *Client
}

// UserGroupDataSourceModel describes the data source data model.
type UserGroupDataSourceModel struct {
	// Input - either ID or Name must be specified
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`

	// Computed outputs
	OrgID     types.String `tfsdk:"org_id"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

// Metadata returns the data source type name.
func (d *UserGroupDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_group"
}

// Schema defines the data source schema.
func (d *UserGroupDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "**BETA FEATURE**: Use this data source to retrieve information about a specific user group in your organization. You can look up a group by its ID or name. User groups are typically synced from your identity provider (IdP) via SCIM.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The unique identifier of the user group (format: `ug_*`). Either `id` or `name` must be specified.",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The name of the user group. Either `id` or `name` must be specified. If multiple groups have the same name, the most recently created one will be returned.",
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
	}
}

// Configure adds the provider configured client to the data source.
func (d *UserGroupDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *UserGroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config UserGroupDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Validate that either ID or Name is provided
	if config.ID.IsNull() && config.Name.IsNull() {
		resp.Diagnostics.AddError(
			"Missing Required Attribute",
			"Either 'id' or 'name' must be specified to look up a user group.",
		)
		return
	}

	var groupID string
	var err error

	if !config.ID.IsNull() {
		// Look up by ID
		groupID = config.ID.ValueString()
		tflog.Info(ctx, "Looking up user group by ID", map[string]any{"id": groupID})
	} else {
		// Look up by name
		name := config.Name.ValueString()
		tflog.Info(ctx, "Looking up user group by name", map[string]any{"name": name})

		groupID, err = d.findUserGroupByName(ctx, name)
		if err != nil {
			resp.Diagnostics.AddError(
				"User Group Lookup Failed",
				fmt.Sprintf("Failed to find user group with name '%s': %s", name, err.Error()),
			)
			return
		}

		if groupID == "" {
			resp.Diagnostics.AddError(
				"User Group Not Found",
				fmt.Sprintf("No user group found with name '%s'", name),
			)
			return
		}
	}

	// Fetch user group details from API
	apiResp, err := d.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/user_groups/%s", groupID), nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch user group", map[string]any{"error": err.Error()})
		resp.Diagnostics.AddError("API Request Failed", err.Error())
		return
	}
	defer func() {
		if closeErr := apiResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	if apiResp.StatusCode == http.StatusNotFound {
		resp.Diagnostics.AddError(
			"User Group Not Found",
			fmt.Sprintf("User group with ID '%s' not found in Anyscale", groupID),
		)
		return
	}

	body, err := io.ReadAll(apiResp.Body)
	if err != nil {
		resp.Diagnostics.AddError("Response Read Error", err.Error())
		return
	}

	if apiResp.StatusCode != http.StatusOK {
		resp.Diagnostics.AddError(
			"API Error",
			fmt.Sprintf("Failed to read user group: %s - %s", apiResp.Status, string(body)),
		)
		return
	}

	var groupResp struct {
		Result struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			OrgID     string `json:"org_id"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &groupResp); err != nil {
		resp.Diagnostics.AddError("JSON Unmarshal Error", err.Error())
		return
	}

	// Populate the data source model
	config.ID = types.StringValue(groupResp.Result.ID)
	config.Name = types.StringValue(groupResp.Result.Name)
	config.OrgID = types.StringValue(groupResp.Result.OrgID)
	config.CreatedAt = types.StringValue(groupResp.Result.CreatedAt)
	config.UpdatedAt = types.StringValue(groupResp.Result.UpdatedAt)

	tflog.Info(ctx, "Successfully retrieved user group", map[string]any{
		"id":   groupID,
		"name": groupResp.Result.Name,
	})

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findUserGroupByName looks for a user group with the given name
func (d *UserGroupDataSource) findUserGroupByName(ctx context.Context, name string) (string, error) {
	resp, err := d.client.DoRequest(ctx, "GET", "/api/v2/user_groups", nil)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to list user groups: %s - %s", resp.Status, string(body))
	}

	var groupsResp struct {
		Results []struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			CreatedAt string  `json:"created_at"`
			DeletedAt *string `json:"deleted_at"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &groupsResp); err != nil {
		return "", err
	}

	// Find groups with matching name (excluding deleted groups)
	// If multiple exist, return the most recently created one
	var matchedGroupID string
	var latestCreatedAt string

	for _, group := range groupsResp.Results {
		// Skip deleted groups
		if group.DeletedAt != nil {
			continue
		}

		if group.Name == name {
			if matchedGroupID == "" || group.CreatedAt > latestCreatedAt {
				matchedGroupID = group.ID
				latestCreatedAt = group.CreatedAt
			}
		}
	}

	if matchedGroupID != "" && len(groupsResp.Results) > 1 {
		// Log warning if multiple groups with same name exist
		tflog.Warn(ctx, "Multiple user groups found with same name, returning most recent", map[string]any{
			"name":       name,
			"group_id":   matchedGroupID,
			"created_at": latestCreatedAt,
		})
	}

	return matchedGroupID, nil
}
