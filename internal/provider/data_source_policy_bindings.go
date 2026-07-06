package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &PolicyBindingsDataSource{}
	_ datasource.DataSourceWithConfigure = &PolicyBindingsDataSource{}
)

// NewPolicyBindingsDataSource returns a new policy bindings data source.
func NewPolicyBindingsDataSource() datasource.DataSource {
	return &PolicyBindingsDataSource{}
}

// PolicyBindingsDataSource defines the data source implementation.
type PolicyBindingsDataSource struct {
	client *Client
}

// PolicyBindingsDataSourceModel describes the data source data model.
type PolicyBindingsDataSourceModel struct {
	ResourceType types.String `tfsdk:"resource_type"`
	Policies     types.List   `tfsdk:"policies"`
}

// PolicyBindingModel represents a single policy binding.
type PolicyBindingModel struct {
	ResourceID   types.String `tfsdk:"resource_id"`
	ResourceType types.String `tfsdk:"resource_type"`
	Bindings     types.List   `tfsdk:"bindings"`
	SyncStatus   types.String `tfsdk:"sync_status"`
}

// RoleBindingModel represents a role binding within a policy.
type RoleBindingModel struct {
	RoleName   types.String `tfsdk:"role_name"`
	Principals types.List   `tfsdk:"principals"`
}

// Metadata returns the data source type name.
func (d *PolicyBindingsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_policy_bindings"
}

// Schema defines the data source schema.
func (d *PolicyBindingsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "**BETA FEATURE**: Use this data source to retrieve a list of all policy bindings for a specific resource type in your organization. Policy bindings define which user groups have which roles on resources. This is part of the SCIM provisioning feature.",

		Attributes: map[string]schema.Attribute{
			"resource_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The type of resources to list policies for. Must be `clouds` or `projects`.",
				Validators: []validator.String{
					stringvalidator.OneOf("clouds", "projects"),
				},
			},
			"policies": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "List of policy bindings for the specified resource type.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"resource_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The ID of the resource (cloud or project).",
						},
						"resource_type": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The type of the resource (cloud or project).",
						},
						"sync_status": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The synchronization status of the policy (e.g., success, pending, failed).",
						},
						"bindings": schema.ListNestedAttribute{
							Computed:            true,
							MarkdownDescription: "List of role bindings for this resource.",
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"role_name": schema.StringAttribute{
										Computed:            true,
										MarkdownDescription: "The name of the role (e.g., owner, write, readonly).",
									},
									"principals": schema.ListAttribute{
										ElementType:         types.StringType,
										Computed:            true,
										MarkdownDescription: "List of user group IDs assigned to this role.",
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *PolicyBindingsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *PolicyBindingsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config PolicyBindingsDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resourceType := config.ResourceType.ValueString()

	// Fetch policy bindings from API. Confirmed non-paginated (BETA endpoint;
	// PolicyBindingsListResponse has no next_paging_token) - not an a41c8e2d gap.
	apiResp, err := d.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/policy/%s", resourceType), nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch policy bindings", map[string]any{"error": err.Error()})
		resp.Diagnostics.AddError("API Request Failed", fmt.Sprintf("Failed to fetch policy bindings: %s", err.Error()))
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
			fmt.Sprintf("Failed to list policy bindings: %s - %s", apiResp.Status, string(body)),
		)
		return
	}

	var policiesResp struct {
		Results []struct {
			ResourceID   string `json:"resource_id"`
			ResourceType string `json:"resource_type"`
			SyncStatus   string `json:"sync_status"`
			Bindings     []struct {
				RoleName   string   `json:"role_name"`
				Principals []string `json:"principals"`
			} `json:"bindings"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &policiesResp); err != nil {
		resp.Diagnostics.AddError("JSON Unmarshal Error", fmt.Sprintf("Failed to unmarshal response: %s", err.Error()))
		return
	}

	// Convert to Terraform model
	policies := make([]PolicyBindingModel, len(policiesResp.Results))
	for i, policy := range policiesResp.Results {
		// Convert bindings
		bindings := make([]RoleBindingModel, len(policy.Bindings))
		for j, binding := range policy.Bindings {
			principals := make([]types.String, len(binding.Principals))
			for k, principal := range binding.Principals {
				principals[k] = types.StringValue(principal)
			}

			principalsList, diags := types.ListValueFrom(ctx, types.StringType, principals)
			resp.Diagnostics.Append(diags...)
			if resp.Diagnostics.HasError() {
				return
			}

			bindings[j] = RoleBindingModel{
				RoleName:   types.StringValue(binding.RoleName),
				Principals: principalsList,
			}
		}

		bindingsList, diags := types.ListValueFrom(ctx, types.ObjectType{
			AttrTypes: map[string]attr.Type{
				"role_name":  types.StringType,
				"principals": types.ListType{ElemType: types.StringType},
			},
		}, bindings)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		policies[i] = PolicyBindingModel{
			ResourceID:   types.StringValue(policy.ResourceID),
			ResourceType: types.StringValue(policy.ResourceType),
			SyncStatus:   types.StringValue(policy.SyncStatus),
			Bindings:     bindingsList,
		}
	}

	policiesList, diags := types.ListValueFrom(ctx, types.ObjectType{
		AttrTypes: map[string]attr.Type{
			"resource_id":   types.StringType,
			"resource_type": types.StringType,
			"sync_status":   types.StringType,
			"bindings": types.ListType{
				ElemType: types.ObjectType{
					AttrTypes: map[string]attr.Type{
						"role_name":  types.StringType,
						"principals": types.ListType{ElemType: types.StringType},
					},
				},
			},
		},
	}, policies)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.Policies = policiesList

	tflog.Info(ctx, "Successfully retrieved policy bindings", map[string]any{
		"resource_type": resourceType,
		"count":         len(policies),
	})

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
