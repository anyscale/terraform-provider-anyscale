package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	_ resource.Resource                = &OrganizationCollaboratorResource{}
	_ resource.ResourceWithConfigure   = &OrganizationCollaboratorResource{}
	_ resource.ResourceWithImportState = &OrganizationCollaboratorResource{}
)

// NewOrganizationCollaboratorResource creates a new organization collaborator resource.
func NewOrganizationCollaboratorResource() resource.Resource {
	return &OrganizationCollaboratorResource{}
}

// OrganizationCollaboratorResource defines the resource implementation.
type OrganizationCollaboratorResource struct {
	client *Client
}

// OrganizationCollaboratorResourceModel describes the resource data model.
type OrganizationCollaboratorResourceModel struct {
	// Identity
	ID types.String `tfsdk:"id"` // identity_id

	// Manageable field
	PermissionLevel types.String `tfsdk:"permission_level"`

	// Computed fields
	Email     types.String `tfsdk:"email"`
	UserID    types.String `tfsdk:"user_id"`
	Name      types.String `tfsdk:"name"`
	CreatedAt types.String `tfsdk:"created_at"`
}

// Metadata returns the resource type name.
func (r *OrganizationCollaboratorResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_collaborator"
}

// Schema defines the schema for the resource.
func (r *OrganizationCollaboratorResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an existing Anyscale Organization Collaborator's permissions.\n\n" +
			"**Important:** This resource cannot create new users. Users must first be added to the organization through:\n" +
			"1. An accepted `anyscale_organization_invitation`, or\n" +
			"2. SCIM provisioning\n\n" +
			"Once a user exists in the organization, import them using `terraform import` to manage their permissions.\n\n" +
			"**Example Import:**\n```\nterraform import anyscale_organization_collaborator.user <identity_id>\n```",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identity ID of the collaborator. Used for import.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"permission_level": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The permission level for this collaborator. Must be either `owner` or `collaborator`.",
				Validators: []validator.String{
					stringvalidator.OneOf("owner", "collaborator"),
				},
			},

			"email": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The email address of the collaborator.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"user_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The user ID of the collaborator.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The name of the collaborator.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the collaborator was added to the organization.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *OrganizationCollaboratorResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create returns an error directing users to use the invitation resource or import.
func (r *OrganizationCollaboratorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError(
		"Direct Creation Not Supported",
		"Organization collaborators cannot be created directly through the API.\n\n"+
			"To add a new user to your organization:\n"+
			"1. Use the 'anyscale_organization_invitation' resource to send an invitation:\n"+
			"   resource \"anyscale_organization_invitation\" \"new_user\" {\n"+
			"     email            = \"user@example.com\"\n"+
			"     permission_level = \"collaborator\"\n"+
			"   }\n\n"+
			"2. Wait for the user to accept the invitation\n\n"+
			"3. Find the user's identity_id using the data source:\n"+
			"   data \"anyscale_organization_user\" \"new_user\" {\n"+
			"     email = \"user@example.com\"\n"+
			"   }\n\n"+
			"4. Import the collaborator to manage their permissions:\n"+
			"   terraform import anyscale_organization_collaborator.new_user <identity_id>\n\n"+
			"Alternatively, if the user already exists in your organization (e.g., via SCIM),\n"+
			"you can import them directly using their identity_id.",
	)
}

// Read reads the current state of an organization collaborator.
func (r *OrganizationCollaboratorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OrganizationCollaboratorResourceModel

	// Read current state
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	identityID := state.ID.ValueString()

	tflog.Info(ctx, "Reading organization collaborator", map[string]interface{}{
		"identity_id": identityID,
	})

	// Fetch collaborator from API
	collaborator, err := r.findCollaboratorByID(ctx, identityID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") {
			// Collaborator was removed outside of Terraform
			tflog.Warn(ctx, "Collaborator not found, removing from state", map[string]interface{}{
				"identity_id": identityID,
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Error reading collaborator",
			fmt.Sprintf("Could not read collaborator %s: %s", identityID, err.Error()),
		)
		return
	}

	// Update state with API data
	state.Email = types.StringValue(collaborator.Email)
	state.PermissionLevel = types.StringValue(collaborator.PermissionLevel)
	state.CreatedAt = types.StringValue(collaborator.CreatedAt)

	if collaborator.UserID != nil && *collaborator.UserID != "" {
		state.UserID = types.StringValue(*collaborator.UserID)
	} else {
		state.UserID = types.StringNull()
	}

	if collaborator.Name != nil && *collaborator.Name != "" {
		state.Name = types.StringValue(*collaborator.Name)
	} else {
		state.Name = types.StringNull()
	}

	// Save updated state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates an organization collaborator's permission level.
func (r *OrganizationCollaboratorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state OrganizationCollaboratorResourceModel

	// Read plan and state
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	identityID := state.ID.ValueString()

	tflog.Info(ctx, "Updating organization collaborator", map[string]interface{}{
		"identity_id":          identityID,
		"old_permission_level": state.PermissionLevel.ValueString(),
		"new_permission_level": plan.PermissionLevel.ValueString(),
	})

	// Create update request
	updateReq := UpdateOrganizationCollaboratorRequest{
		PermissionLevel: plan.PermissionLevel.ValueString(),
	}

	jsonData, err := json.Marshal(updateReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error marshaling request",
			fmt.Sprintf("Could not marshal collaborator update request: %s", err.Error()),
		)
		return
	}

	// Send update request
	httpResp, err := r.client.DoRequest(ctx, "PUT", fmt.Sprintf("/api/v2/organization_collaborators/%s", identityID), strings.NewReader(string(jsonData)))
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating collaborator",
			fmt.Sprintf("Could not update collaborator %s: %s", identityID, err.Error()),
		)
		return
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading response",
			fmt.Sprintf("Could not read update response: %s", err.Error()),
		)
		return
	}

	if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusNoContent {
		resp.Diagnostics.AddError(
			"Error updating collaborator",
			fmt.Sprintf("API returned status %d: %s", httpResp.StatusCode, string(body)),
		)
		return
	}

	tflog.Info(ctx, "Updated organization collaborator", map[string]interface{}{
		"identity_id":      identityID,
		"permission_level": plan.PermissionLevel.ValueString(),
	})

	// Read back to get current state
	collaborator, err := r.findCollaboratorByID(ctx, identityID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading updated collaborator",
			fmt.Sprintf("Could not read collaborator after update: %s", err.Error()),
		)
		return
	}

	// Update plan with latest data
	plan.Email = types.StringValue(collaborator.Email)
	plan.CreatedAt = types.StringValue(collaborator.CreatedAt)

	if collaborator.UserID != nil && *collaborator.UserID != "" {
		plan.UserID = types.StringValue(*collaborator.UserID)
	} else {
		plan.UserID = types.StringNull()
	}

	if collaborator.Name != nil && *collaborator.Name != "" {
		plan.Name = types.StringValue(*collaborator.Name)
	} else {
		plan.Name = types.StringNull()
	}

	// Save updated state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes a collaborator from the organization.
func (r *OrganizationCollaboratorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state OrganizationCollaboratorResourceModel

	// Read current state
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	identityID := state.ID.ValueString()

	tflog.Info(ctx, "Removing organization collaborator", map[string]interface{}{
		"identity_id":  identityID,
		"email_domain": getEmailDomain(state.Email.ValueString()),
	})

	// Delete the collaborator
	httpResp, err := r.client.DoRequest(ctx, "DELETE", fmt.Sprintf("/api/v2/organization_collaborators/%s", identityID), nil)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error removing collaborator",
			fmt.Sprintf("Could not remove collaborator %s: %s", identityID, err.Error()),
		)
		return
	}
	defer httpResp.Body.Close()

	// Handle response - treat 404 as success (already removed)
	if httpResp.StatusCode != http.StatusOK &&
		httpResp.StatusCode != http.StatusNoContent &&
		httpResp.StatusCode != http.StatusNotFound {
		body, err := io.ReadAll(httpResp.Body)
		if err != nil {
			tflog.Error(ctx, "Failed to read response", map[string]any{"error": err.Error()})
			resp.Diagnostics.AddError("Read Error", err.Error())
			return
		}

		resp.Diagnostics.AddError(
			"Error removing collaborator",
			fmt.Sprintf("API returned status %d: %s", httpResp.StatusCode, string(body)),
		)
		return
	}

	tflog.Info(ctx, "Removed organization collaborator", map[string]interface{}{
		"identity_id": identityID,
	})
}

// ImportState imports an existing collaborator by identity_id.
func (r *OrganizationCollaboratorResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by identity_id
	identityID := req.ID

	tflog.Info(ctx, "Importing organization collaborator", map[string]interface{}{
		"identity_id": identityID,
	})

	// Fetch collaborator to validate it exists
	collaborator, err := r.findCollaboratorByID(ctx, identityID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error importing collaborator",
			fmt.Sprintf("Could not find collaborator with identity_id %s: %s\n\n"+
				"Tip: Use the anyscale_organization_user data source to find the identity_id:\n"+
				"  data \"anyscale_organization_user\" \"example\" {\n"+
				"    email = \"user@example.com\"\n"+
				"  }\n\n"+
				"Then import using: terraform import anyscale_organization_collaborator.example <identity_id>",
				identityID, err.Error()),
		)
		return
	}

	// Set all attributes
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), identityID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("email"), collaborator.Email)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("permission_level"), collaborator.PermissionLevel)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("created_at"), collaborator.CreatedAt)...)

	if collaborator.UserID != nil && *collaborator.UserID != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), *collaborator.UserID)...)
	}

	if collaborator.Name != nil && *collaborator.Name != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), *collaborator.Name)...)
	}

	tflog.Info(ctx, "Imported organization collaborator", map[string]interface{}{
		"identity_id":      identityID,
		"email":            collaborator.Email,
		"permission_level": collaborator.PermissionLevel,
	})
}

// Helper functions

// findCollaboratorByID fetches a collaborator by identity_id
func (r *OrganizationCollaboratorResource) findCollaboratorByID(ctx context.Context, identityID string) (*OrganizationCollaboratorResult, error) {
	// List all collaborators with pagination
	// Note: The API doesn't have a direct GET endpoint for a single collaborator,
	// so we need to list and filter
	httpResp, err := r.client.DoRequest(ctx, "GET", "/api/v2/organization_collaborators?count=50", nil)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", httpResp.StatusCode, string(body))
	}

	var listResp OrganizationCollaboratorsListResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("error parsing response: %w", err)
	}

	// Find the collaborator with matching identity_id
	for _, collab := range listResp.Results {
		if collab.ID == identityID {
			return &collab, nil
		}
	}

	// If not found in first page, check if there are more pages
	// For now, we'll assume 50 is enough for most cases
	// TODO: Implement pagination if needed
	if listResp.Metadata.NextPagingToken != nil {
		tflog.Warn(ctx, "Collaborator list has more pages, pagination not fully implemented", map[string]interface{}{
			"total": listResp.Metadata.Total,
		})
	}

	return nil, fmt.Errorf("collaborator not found")
}

// findCollaboratorByEmail fetches a collaborator by email (convenience function)
// nolint:unused
func (r *OrganizationCollaboratorResource) findCollaboratorByEmail(ctx context.Context, email string) (*OrganizationCollaboratorResult, error) {
	encodedEmail := url.QueryEscape(email)
	httpResp, err := r.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/organization_collaborators?email=%s", encodedEmail), nil)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", httpResp.StatusCode, string(body))
	}

	var listResp OrganizationCollaboratorsListResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("error parsing response: %w", err)
	}

	if len(listResp.Results) == 0 {
		return nil, fmt.Errorf("collaborator not found")
	}

	if len(listResp.Results) > 1 {
		return nil, fmt.Errorf("multiple collaborators found with email %s", email)
	}

	return &listResp.Results[0], nil
}
