package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
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
}

// Metadata returns the data source type name.
func (d *OrganizationUserDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_user"
}

// Schema defines the data source schema.
func (d *OrganizationUserDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "**BETA FEATURE**: Use this data source to retrieve information about a specific user in your organization. You can look up a user by their identity ID, user ID, or email address.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The identity ID of the user. Either `id`, `user_id`, or `email` must be specified.",
			},
			"user_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The user ID of the user. Either `id`, `user_id`, or `email` must be specified.",
			},
			"email": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The email address of the user. Either `id`, `user_id`, or `email` must be specified.",
			},
			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The name of the user.",
			},
			"permission_level": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The organization permission level (owner, collaborator, etc.).",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The timestamp when the user was added to the organization.",
			},
		},
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
	var err error

	if !config.ID.IsNull() {
		// Look up by identity ID
		user, err = d.findUserByID(ctx, config.ID.ValueString())
	} else if !config.UserID.IsNull() {
		// Look up by user ID
		user, err = d.findUserByUserID(ctx, config.UserID.ValueString())
	} else if !config.Email.IsNull() {
		// Look up by email
		user, err = d.findUserByEmail(ctx, config.Email.ValueString())
	} else {
		resp.Diagnostics.AddError(
			"Missing Required Attribute",
			"One of id, user_id, or email must be specified",
		)
		return
	}

	if err != nil {
		resp.Diagnostics.AddError("User Lookup Failed", err.Error())
		return
	}

	if user == nil {
		resp.Diagnostics.AddError(
			"User Not Found",
			"No user found matching the specified criteria in Anyscale",
		)
		return
	}

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, user)...)
}

// findUserByID looks up a user by their identity ID.
func (d *OrganizationUserDataSource) findUserByID(ctx context.Context, id string) (*OrganizationUserDataSourceModel, error) {
	// List all users and find the one with matching ID
	apiResp, err := d.client.DoRequest(ctx, "GET", "/api/v2/organization_collaborators?count=50", nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch organization users", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("failed to fetch organization users: %s", err.Error())
	}
	defer func() {
		if closeErr := apiResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	body, err := io.ReadAll(apiResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %s", err.Error())
	}

	if apiResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list organization users: %s - %s", apiResp.Status, string(body))
	}

	var usersResp struct {
		Results []struct {
			ID              string  `json:"id"`
			UserID          *string `json:"user_id"`
			Name            string  `json:"name"`
			Email           string  `json:"email"`
			PermissionLevel string  `json:"permission_level"`
			CreatedAt       string  `json:"created_at"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &usersResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %s", err.Error())
	}

	// Find user with matching ID
	for _, u := range usersResp.Results {
		if u.ID == id {
			userID := types.StringNull()
			if u.UserID != nil {
				userID = types.StringValue(*u.UserID)
			}

			return &OrganizationUserDataSourceModel{
				ID:              types.StringValue(u.ID),
				UserID:          userID,
				Name:            types.StringValue(u.Name),
				Email:           types.StringValue(u.Email),
				PermissionLevel: types.StringValue(u.PermissionLevel),
				CreatedAt:       types.StringValue(u.CreatedAt),
			}, nil
		}
	}

	return nil, nil
}

// findUserByUserID looks up a user by their user ID.
func (d *OrganizationUserDataSource) findUserByUserID(ctx context.Context, userID string) (*OrganizationUserDataSourceModel, error) {
	// List all users and find the one with matching user_id
	apiResp, err := d.client.DoRequest(ctx, "GET", "/api/v2/organization_collaborators?count=50", nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch organization users", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("failed to fetch organization users: %s", err.Error())
	}
	defer func() {
		if closeErr := apiResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	body, err := io.ReadAll(apiResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %s", err.Error())
	}

	if apiResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list organization users: %s - %s", apiResp.Status, string(body))
	}

	var usersResp struct {
		Results []struct {
			ID              string  `json:"id"`
			UserID          *string `json:"user_id"`
			Name            string  `json:"name"`
			Email           string  `json:"email"`
			PermissionLevel string  `json:"permission_level"`
			CreatedAt       string  `json:"created_at"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &usersResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %s", err.Error())
	}

	// Find user with matching user_id
	for _, u := range usersResp.Results {
		if u.UserID != nil && *u.UserID == userID {
			return &OrganizationUserDataSourceModel{
				ID:              types.StringValue(u.ID),
				UserID:          types.StringValue(*u.UserID),
				Name:            types.StringValue(u.Name),
				Email:           types.StringValue(u.Email),
				PermissionLevel: types.StringValue(u.PermissionLevel),
				CreatedAt:       types.StringValue(u.CreatedAt),
			}, nil
		}
	}

	return nil, nil
}

// findUserByEmail looks up a user by their email address.
func (d *OrganizationUserDataSource) findUserByEmail(ctx context.Context, email string) (*OrganizationUserDataSourceModel, error) {
	// Use API filter for email (must URL encode the email parameter)
	encodedEmail := url.QueryEscape(email)
	queryParams := fmt.Sprintf("?count=50&email=%s", encodedEmail)
	apiResp, err := d.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/organization_collaborators%s", queryParams), nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch organization users", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("failed to fetch organization users: %s", err.Error())
	}
	defer func() {
		if closeErr := apiResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	body, err := io.ReadAll(apiResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %s", err.Error())
	}

	if apiResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list organization users: %s - %s", apiResp.Status, string(body))
	}

	var usersResp struct {
		Results []struct {
			ID              string  `json:"id"`
			UserID          *string `json:"user_id"`
			Name            string  `json:"name"`
			Email           string  `json:"email"`
			PermissionLevel string  `json:"permission_level"`
			CreatedAt       string  `json:"created_at"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &usersResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %s", err.Error())
	}

	// Find exact match (case-insensitive)
	for _, u := range usersResp.Results {
		if strings.EqualFold(u.Email, email) {
			userID := types.StringNull()
			if u.UserID != nil {
				userID = types.StringValue(*u.UserID)
			}

			return &OrganizationUserDataSourceModel{
				ID:              types.StringValue(u.ID),
				UserID:          userID,
				Name:            types.StringValue(u.Name),
				Email:           types.StringValue(u.Email),
				PermissionLevel: types.StringValue(u.PermissionLevel),
				CreatedAt:       types.StringValue(u.CreatedAt),
			}, nil
		}
	}

	return nil, nil
}
