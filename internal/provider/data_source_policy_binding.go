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
	_ datasource.DataSource              = &PolicyBindingDataSource{}
	_ datasource.DataSourceWithConfigure = &PolicyBindingDataSource{}
)

// NewPolicyBindingDataSource returns a new policy binding data source.
func NewPolicyBindingDataSource() datasource.DataSource {
	return &PolicyBindingDataSource{}
}

// PolicyBindingDataSource defines the data source implementation.
type PolicyBindingDataSource struct {
	client *Client
}

// PolicyBindingDataSourceModel describes the data source data model.
type PolicyBindingDataSourceModel struct {
	ResourceType types.String `tfsdk:"resource_type"`
	ResourceID   types.String `tfsdk:"resource_id"`
	Bindings     types.List   `tfsdk:"bindings"`
}

// PolicyBindingRoleModel represents a role binding.
type PolicyBindingRoleModel struct {
	RoleName   types.String `tfsdk:"role_name"`
	Principals types.List   `tfsdk:"principals"`
}

// Metadata returns the data source type name.
func (d *PolicyBindingDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_policy_binding"
}

// Schema defines the data source schema.
func (d *PolicyBindingDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "**BETA FEATURE**: Use this data source to retrieve the policy binding for a specific resource. Policy bindings define which user groups have which roles on a resource. This is part of the SCIM provisioning feature.",

		Attributes: map[string]schema.Attribute{
			"resource_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The type of resource. Must be `cloud`, `project`, or `organization`.",
				Validators: []validator.String{
					stringvalidator.OneOf("cloud", "project", "organization"),
				},
			},
			"resource_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The ID of the resource (e.g., `cld_abc123`, `prj_xyz789`, `org_def456`).",
			},
			"bindings": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "List of role bindings for this resource.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"role_name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The name of the role. Valid values depend on resource type:\n  - **Organization**: `owner`, `collaborator`\n  - **Cloud**: `collaborator`, `readonly`\n  - **Project**: `owner`, `write`, `readonly`",
						},
						"principals": schema.ListAttribute{
							ElementType:         types.StringType,
							Computed:            true,
							MarkdownDescription: "List of user group IDs (format: `ug_*`) assigned to this role.",
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *PolicyBindingDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *PolicyBindingDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config PolicyBindingDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resourceType := config.ResourceType.ValueString()
	resourceID := config.ResourceID.ValueString()

	// Fetch policy binding from API
	apiResp, err := d.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/policy/%s/%s", resourceType, resourceID), nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch policy binding", map[string]any{"error": err.Error()})
		resp.Diagnostics.AddError("API Request Failed", fmt.Sprintf("Failed to fetch policy binding: %s", err.Error()))
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

	if apiResp.StatusCode == http.StatusNotFound {
		// No policy found for this resource - return empty bindings
		emptyBindings, diags := types.ListValueFrom(ctx, types.ObjectType{
			AttrTypes: map[string]attr.Type{
				"role_name":  types.StringType,
				"principals": types.ListType{ElemType: types.StringType},
			},
		}, []PolicyBindingRoleModel{})
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		config.Bindings = emptyBindings

		tflog.Info(ctx, "No policy binding found for resource", map[string]any{
			"resource_type": resourceType,
			"resource_id":   resourceID,
		})

		// Set state
		resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
		return
	}

	if apiResp.StatusCode != http.StatusOK {
		resp.Diagnostics.AddError(
			"API Error",
			fmt.Sprintf("Failed to read policy binding: %s - %s", apiResp.Status, string(body)),
		)
		return
	}

	var policyResp struct {
		Result struct {
			Bindings []struct {
				RoleName   string   `json:"role_name"`
				Principals []string `json:"principals"`
			} `json:"bindings"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &policyResp); err != nil {
		resp.Diagnostics.AddError("JSON Unmarshal Error", fmt.Sprintf("Failed to unmarshal response: %s", err.Error()))
		return
	}

	// Convert to Terraform model
	bindings := make([]PolicyBindingRoleModel, len(policyResp.Result.Bindings))
	for i, binding := range policyResp.Result.Bindings {
		principals := make([]types.String, len(binding.Principals))
		for j, principal := range binding.Principals {
			principals[j] = types.StringValue(principal)
		}

		principalsList, diags := types.ListValueFrom(ctx, types.StringType, principals)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		bindings[i] = PolicyBindingRoleModel{
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

	config.Bindings = bindingsList

	tflog.Info(ctx, "Successfully retrieved policy binding", map[string]any{
		"resource_type": resourceType,
		"resource_id":   resourceID,
		"num_bindings":  len(bindings),
	})

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
