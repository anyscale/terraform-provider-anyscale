package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &PolicyBindingResource{}
	_ resource.ResourceWithConfigure   = &PolicyBindingResource{}
	_ resource.ResourceWithImportState = &PolicyBindingResource{}
)

// NewPolicyBindingResource creates a new policy binding resource.
func NewPolicyBindingResource() resource.Resource {
	return &PolicyBindingResource{}
}

// PolicyBindingResource defines the resource implementation.
type PolicyBindingResource struct {
	client *Client
}

// PolicyBindingResourceModel describes the resource data model.
type PolicyBindingResourceModel struct {
	// Identity (composite key)
	ID           types.String `tfsdk:"id"` // Generated as "resource_type/resource_id"
	ResourceType types.String `tfsdk:"resource_type"`
	ResourceID   types.String `tfsdk:"resource_id"`

	// Policy bindings (required)
	Bindings types.List `tfsdk:"bindings"` // List of RoleBindingModel

	// Computed
	SyncStatus types.String `tfsdk:"sync_status"`
}

// Note: RoleBindingModel is defined in data_source_policy_bindings.go and shared across data sources and resources

// Metadata returns the resource type name.
func (r *PolicyBindingResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_policy_binding"
}

// Schema defines the schema for the resource.
func (r *PolicyBindingResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "**BETA FEATURE**: Manages policy bindings for SCIM provisioning. " +
			"Policy bindings define which user groups have which roles on a resource (organization, cloud, or project).\n\n" +
			"**IMPORTANT**: Policy bindings REPLACE all existing group-based permissions on the resource. " +
			"Any role bindings not specified will be removed. Setting an empty `bindings` list removes all group permissions.\n\n" +
			"**Role Validation**:\n" +
			"- **Organization**: `owner`, `collaborator`\n" +
			"- **Cloud**: `collaborator`, `readonly`\n" +
			"- **Project**: `owner`, `write`, `readonly`\n\n" +
			"**Access Requirements**:\n" +
			"- Groups must have cloud access before granting project access in that cloud\n" +
			"- If a group has `readonly` on a cloud, they can only have `readonly` on projects in that cloud\n" +
			"- If a group has `collaborator` on a cloud, they can have any role on projects\n" +
			"- Organization owners cannot be added to cloud/project policies (they have implicit access)",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier for this policy binding (format: `resource_type/resource_id`).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"resource_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The type of resource. Must be `organization`, `cloud`, or `project`.",
				Validators: []validator.String{
					stringvalidator.OneOf("organization", "cloud", "project"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			"resource_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The ID of the resource (e.g., `org_abc123`, `cld_xyz789`, `prj_def456`).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			"bindings": schema.ListNestedAttribute{
				Required:            true,
				MarkdownDescription: "List of role bindings for this resource. Empty list removes all group permissions.",
				Validators: []validator.List{
					listvalidator.UniqueValues(),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"role_name": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "The name of the role. Valid values depend on resource type:\n" +
								"  - **Organization**: `owner`, `collaborator`\n" +
								"  - **Cloud**: `collaborator`, `readonly`\n" +
								"  - **Project**: `owner`, `write`, `readonly`",
							Validators: []validator.String{
								// Validation is done in plan-time validation to check against resource_type
								stringvalidator.LengthAtLeast(1),
							},
						},
						"principals": schema.ListAttribute{
							ElementType:         types.StringType,
							Required:            true,
							MarkdownDescription: "List of user group IDs (format: `ug_*`) assigned to this role.",
							Validators: []validator.List{
								listvalidator.SizeAtLeast(1),
								listvalidator.UniqueValues(),
							},
						},
					},
				},
			},

			"sync_status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The synchronization status of the policy (e.g., `success`, `pending`, `failed`).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *PolicyBindingResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = client
}

// Create creates a new policy binding.
func (r *PolicyBindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PolicyBindingResourceModel

	// Read plan data
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Validate role names for resource type
	if err := r.validateRoleNames(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Invalid Role Configuration", err.Error())
		return
	}

	// Set the composite ID
	resourceType := plan.ResourceType.ValueString()
	resourceID := plan.ResourceID.ValueString()
	plan.ID = types.StringValue(fmt.Sprintf("%s/%s", resourceType, resourceID))

	tflog.Info(ctx, "Creating policy binding", map[string]interface{}{
		"resource_type": resourceType,
		"resource_id":   resourceID,
	})

	// Create/Update policy (PUT replaces existing)
	if err := r.setPolicyBinding(ctx, &plan); err != nil {
		resp.Diagnostics.AddError(
			"Error creating policy binding",
			fmt.Sprintf("Could not create policy binding: %s", err.Error()),
		)
		return
	}

	// Read back the policy to get computed fields
	if err := r.readPolicyBinding(ctx, &plan); err != nil {
		resp.Diagnostics.AddError(
			"Error reading policy binding after creation",
			fmt.Sprintf("Could not read policy binding: %s", err.Error()),
		)
		return
	}

	tflog.Info(ctx, "Created policy binding", map[string]interface{}{
		"id":          plan.ID.ValueString(),
		"sync_status": plan.SyncStatus.ValueString(),
	})

	// Save plan to state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read reads the current state of a policy binding.
func (r *PolicyBindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PolicyBindingResourceModel

	// Read current state
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "Reading policy binding", map[string]interface{}{
		"id": state.ID.ValueString(),
	})

	// Read from API
	if err := r.readPolicyBinding(ctx, &state); err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") {
			// Policy was deleted outside of Terraform
			tflog.Warn(ctx, "Policy binding not found, removing from state", map[string]interface{}{
				"id": state.ID.ValueString(),
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Error reading policy binding",
			fmt.Sprintf("Could not read policy binding: %s", err.Error()),
		)
		return
	}

	// Save updated state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates a policy binding.
func (r *PolicyBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan PolicyBindingResourceModel

	// Read plan data
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Validate role names for resource type
	if err := r.validateRoleNames(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Invalid Role Configuration", err.Error())
		return
	}

	tflog.Info(ctx, "Updating policy binding", map[string]interface{}{
		"id": plan.ID.ValueString(),
	})

	// Update policy (PUT replaces existing)
	if err := r.setPolicyBinding(ctx, &plan); err != nil {
		resp.Diagnostics.AddError(
			"Error updating policy binding",
			fmt.Sprintf("Could not update policy binding: %s", err.Error()),
		)
		return
	}

	// Read back the policy to get computed fields
	if err := r.readPolicyBinding(ctx, &plan); err != nil {
		resp.Diagnostics.AddError(
			"Error reading policy binding after update",
			fmt.Sprintf("Could not read policy binding: %s", err.Error()),
		)
		return
	}

	tflog.Info(ctx, "Updated policy binding", map[string]interface{}{
		"id":          plan.ID.ValueString(),
		"sync_status": plan.SyncStatus.ValueString(),
	})

	// Save plan to state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes a policy binding (sets empty bindings).
func (r *PolicyBindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PolicyBindingResourceModel

	// Read current state
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resourceType := state.ResourceType.ValueString()
	resourceID := state.ResourceID.ValueString()

	tflog.Info(ctx, "Deleting policy binding (setting empty bindings)", map[string]interface{}{
		"resource_type": resourceType,
		"resource_id":   resourceID,
	})

	// Delete by setting empty bindings
	emptyBindings := map[string]interface{}{
		"bindings": []interface{}{},
	}

	jsonData, err := json.Marshal(emptyBindings)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error marshaling request",
			fmt.Sprintf("Could not marshal empty bindings: %s", err.Error()),
		)
		return
	}

	// Send PUT request with empty bindings
	httpResp, err := r.client.DoRequest(ctx, "PUT", fmt.Sprintf("/api/v2/policy/%s/%s", resourceType, resourceID), strings.NewReader(string(jsonData)))
	if err != nil {
		resp.Diagnostics.AddError(
			"Error deleting policy binding",
			fmt.Sprintf("Could not delete policy binding: %s", err.Error()),
		)
		return
	}
	defer func() { _ = httpResp.Body.Close() }()

	// Handle response - 404 is OK (already gone)
	if httpResp.StatusCode != http.StatusOK &&
		httpResp.StatusCode != http.StatusAccepted &&
		httpResp.StatusCode != http.StatusNoContent &&
		httpResp.StatusCode != http.StatusNotFound {
		body, err := io.ReadAll(httpResp.Body)
		if err != nil {
			resp.Diagnostics.AddError("Read Error", err.Error())
			return
		}
		resp.Diagnostics.AddError(
			"Error deleting policy binding",
			fmt.Sprintf("API returned status %d: %s", httpResp.StatusCode, string(body)),
		)
		return
	}

	tflog.Info(ctx, "Deleted policy binding", map[string]interface{}{
		"resource_type": resourceType,
		"resource_id":   resourceID,
	})
}

// ImportState imports an existing policy binding by composite ID.
func (r *PolicyBindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import format: "resource_type/resource_id"
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID format: resource_type/resource_id, got: %s", req.ID),
		)
		return
	}

	resourceType := parts[0]
	resourceID := parts[1]

	// Validate resource type
	validTypes := map[string]bool{"organization": true, "cloud": true, "project": true}
	if !validTypes[resourceType] {
		resp.Diagnostics.AddError(
			"Invalid Resource Type",
			fmt.Sprintf("Resource type must be 'organization', 'cloud', or 'project', got: %s", resourceType),
		)
		return
	}

	tflog.Info(ctx, "Importing policy binding", map[string]interface{}{
		"resource_type": resourceType,
		"resource_id":   resourceID,
	})

	// Set the attributes
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_type"), resourceType)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_id"), resourceID)...)
}

// Helper functions

// validateRoleNames validates that role names are valid for the resource type
func (r *PolicyBindingResource) validateRoleNames(ctx context.Context, model *PolicyBindingResourceModel) error {
	resourceType := model.ResourceType.ValueString()

	// Define valid roles per resource type
	validRoles := map[string]map[string]bool{
		"organization": {"owner": true, "collaborator": true},
		"cloud":        {"collaborator": true, "readonly": true},
		"project":      {"owner": true, "write": true, "readonly": true},
	}

	// Get bindings from model
	var bindings []RoleBindingModel
	model.Bindings.ElementsAs(ctx, &bindings, false)

	// Validate each role
	for _, binding := range bindings {
		roleName := binding.RoleName.ValueString()
		if !validRoles[resourceType][roleName] {
			validRolesList := []string{}
			for role := range validRoles[resourceType] {
				validRolesList = append(validRolesList, role)
			}
			return fmt.Errorf(
				"invalid role '%s' for resource type '%s'. Valid roles: %s",
				roleName,
				resourceType,
				strings.Join(validRolesList, ", "),
			)
		}
	}

	return nil
}

// setPolicyBinding sends a PUT request to set the policy binding
func (r *PolicyBindingResource) setPolicyBinding(ctx context.Context, model *PolicyBindingResourceModel) error {
	resourceType := model.ResourceType.ValueString()
	resourceID := model.ResourceID.ValueString()

	// Convert bindings to API format
	var bindings []RoleBindingModel
	model.Bindings.ElementsAs(ctx, &bindings, false)

	apiBindings := make([]map[string]interface{}, len(bindings))
	for i, binding := range bindings {
		var principals []string
		binding.Principals.ElementsAs(ctx, &principals, false)

		apiBindings[i] = map[string]interface{}{
			"role_name":  binding.RoleName.ValueString(),
			"principals": principals,
		}
	}

	requestBody := map[string]interface{}{
		"bindings": apiBindings,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("error marshaling request: %w", err)
	}

	// Send PUT request
	httpResp, err := r.client.DoRequest(ctx, "PUT", fmt.Sprintf("/api/v2/policy/%s/%s", resourceType, resourceID), strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("error sending request: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("error reading response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("API returned status %d: %s", httpResp.StatusCode, string(body))
	}

	return nil
}

// readPolicyBinding reads a policy binding from the API
func (r *PolicyBindingResource) readPolicyBinding(ctx context.Context, model *PolicyBindingResourceModel) error {
	resourceType := model.ResourceType.ValueString()
	resourceID := model.ResourceID.ValueString()

	// Fetch from API
	httpResp, err := r.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/policy/%s/%s", resourceType, resourceID), nil)
	if err != nil {
		return fmt.Errorf("error fetching policy: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("policy not found")
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("error reading response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return fmt.Errorf("API returned status %d: %s", httpResp.StatusCode, string(body))
	}

	// Parse response
	var policyResp struct {
		Result struct {
			Bindings []struct {
				RoleName   string   `json:"role_name"`
				Principals []string `json:"principals"`
			} `json:"bindings"`
			SyncStatus *string `json:"sync_status,omitempty"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &policyResp); err != nil {
		return fmt.Errorf("error parsing response: %w", err)
	}

	// Convert to Terraform model
	bindings := make([]RoleBindingModel, len(policyResp.Result.Bindings))
	for i, binding := range policyResp.Result.Bindings {
		principals := make([]types.String, len(binding.Principals))
		for j, principal := range binding.Principals {
			principals[j] = types.StringValue(principal)
		}

		principalsList, diags := types.ListValueFrom(ctx, types.StringType, principals)
		if diags.HasError() {
			return fmt.Errorf("error converting principals: %v", diags)
		}

		bindings[i] = RoleBindingModel{
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
	if diags.HasError() {
		return fmt.Errorf("error converting bindings: %v", diags)
	}

	model.Bindings = bindingsList

	// Set sync status if available
	if policyResp.Result.SyncStatus != nil {
		model.SyncStatus = types.StringValue(*policyResp.Result.SyncStatus)
	} else {
		model.SyncStatus = types.StringValue("success")
	}

	return nil
}
