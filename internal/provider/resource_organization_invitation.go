package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &OrganizationInvitationResource{}
	_ resource.ResourceWithConfigure   = &OrganizationInvitationResource{}
	_ resource.ResourceWithImportState = &OrganizationInvitationResource{}
)

// NewOrganizationInvitationResource creates a new organization invitation resource.
func NewOrganizationInvitationResource() resource.Resource {
	return &OrganizationInvitationResource{}
}

// OrganizationInvitationResource defines the resource implementation.
type OrganizationInvitationResource struct {
	client *Client
}

// OrganizationInvitationResourceModel describes the resource data model.
type OrganizationInvitationResourceModel struct {
	// Identity
	ID types.String `tfsdk:"id"` // invitation_id

	// Required fields
	Email types.String `tfsdk:"email"`

	// Computed fields
	OrganizationID types.String `tfsdk:"organization_id"`
	CreatedAt      types.String `tfsdk:"created_at"`
	ExpiresAt      types.String `tfsdk:"expires_at"`
	AcceptedAt     types.String `tfsdk:"accepted_at"`
	Status         types.String `tfsdk:"status"` // "pending", "accepted", or "expired"
}

// Metadata returns the resource type name.
func (r *OrganizationInvitationResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_invitation"
}

// Schema defines the schema for the resource.
func (r *OrganizationInvitationResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Anyscale Organization Invitation. This resource sends an email invitation to join an organization.\n\n" +
			"**Note:** Invitations have an expiration time and must be accepted by the recipient. Once accepted, the user will have default collaborator permissions. Use the `anyscale_organization_collaborator` resource to manage their permissions.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the invitation (invitation_id).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"email": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The email address to send the invitation to.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			"organization_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The organization ID this invitation belongs to.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the invitation was created.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"expires_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the invitation expires.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"accepted_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the invitation was accepted. Null if not yet accepted.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The current status of the invitation. Can be `pending`, `accepted`, or `expired`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *OrganizationInvitationResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create creates a new organization invitation.
func (r *OrganizationInvitationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan OrganizationInvitationResourceModel

	// Read plan data
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Create request body
	createReq := CreateOrganizationInvitationRequest{
		Email: plan.Email.ValueString(),
	}

	jsonData, err := json.Marshal(createReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error marshaling request",
			fmt.Sprintf("Could not marshal invitation request: %s", err.Error()),
		)
		return
	}

	tflog.Info(ctx, "Creating organization invitation", map[string]interface{}{
		"email_domain": getEmailDomain(plan.Email.ValueString()),
	})

	// Send create request
	httpResp, err := r.client.DoRequest(ctx, "POST", "/api/v2/organization_invitations", strings.NewReader(string(jsonData)))
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating invitation",
			fmt.Sprintf("Could not send invitation: %s", err.Error()),
		)
		return
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading response",
			fmt.Sprintf("Could not read invitation response: %s", err.Error()),
		)
		return
	}

	if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusCreated {
		resp.Diagnostics.AddError(
			"Error creating invitation",
			fmt.Sprintf("API returned status %d: %s", httpResp.StatusCode, string(body)),
		)
		return
	}

	// Parse response (only contains invitation ID)
	var invitationBaseResp struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &invitationBaseResp); err != nil {
		resp.Diagnostics.AddError(
			"Error parsing response",
			fmt.Sprintf("Could not parse invitation response: %s", err.Error()),
		)
		return
	}

	invitationID := invitationBaseResp.Result.ID
	plan.ID = types.StringValue(invitationID)

	tflog.Info(ctx, "Created organization invitation", map[string]interface{}{
		"invitation_id": invitationID,
	})

	// Fetch full invitation details (API only returns ID on create)
	invitation, err := r.getInvitationByID(ctx, invitationID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading invitation after creation",
			fmt.Sprintf("Could not read invitation %s: %s", invitationID, err.Error()),
		)
		return
	}

	// Update plan with full details
	plan.Email = types.StringValue(invitation.Email)
	plan.OrganizationID = types.StringValue(invitation.OrganizationID)
	plan.CreatedAt = types.StringValue(invitation.CreatedAt)
	plan.ExpiresAt = types.StringValue(invitation.ExpiresAt)

	if invitation.AcceptedAt != nil {
		plan.AcceptedAt = types.StringValue(*invitation.AcceptedAt)
	} else {
		plan.AcceptedAt = types.StringNull()
	}

	// Compute status
	status := computeInvitationStatus(invitation.AcceptedAt, invitation.ExpiresAt)
	plan.Status = types.StringValue(status)

	tflog.Info(ctx, "Fetched full invitation details", map[string]interface{}{
		"invitation_id": invitationID,
		"status":        status,
	})

	// Save plan to state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read reads the current state of an organization invitation.
func (r *OrganizationInvitationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OrganizationInvitationResourceModel

	// Read current state
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	invitationID := state.ID.ValueString()

	tflog.Info(ctx, "Reading organization invitation", map[string]interface{}{
		"invitation_id": invitationID,
	})

	// Fetch invitation from API
	invitation, err := r.getInvitationByID(ctx, invitationID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") {
			// Invitation was deleted or invalidated outside of Terraform
			tflog.Warn(ctx, "Invitation not found, removing from state", map[string]interface{}{
				"invitation_id": invitationID,
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Error reading invitation",
			fmt.Sprintf("Could not read invitation %s: %s", invitationID, err.Error()),
		)
		return
	}

	// Update state with API data
	state.Email = types.StringValue(invitation.Email)
	state.OrganizationID = types.StringValue(invitation.OrganizationID)
	state.CreatedAt = types.StringValue(invitation.CreatedAt)
	state.ExpiresAt = types.StringValue(invitation.ExpiresAt)

	if invitation.AcceptedAt != nil {
		state.AcceptedAt = types.StringValue(*invitation.AcceptedAt)
	} else {
		state.AcceptedAt = types.StringNull()
	}

	// Compute status
	status := computeInvitationStatus(invitation.AcceptedAt, invitation.ExpiresAt)
	state.Status = types.StringValue(status)

	if status == "expired" {
		tflog.Warn(ctx, "Invitation has expired", map[string]interface{}{
			"invitation_id": invitationID,
			"expires_at":    invitation.ExpiresAt,
		})
	} else if status == "accepted" {
		tflog.Info(ctx, "Invitation has been accepted", map[string]interface{}{
			"invitation_id": invitationID,
			"accepted_at":   *invitation.AcceptedAt,
		})
	}

	// Save updated state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates an organization invitation.
// Note: Invitations cannot be updated - email and permission_level have RequiresReplace.
func (r *OrganizationInvitationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// This should never be called due to RequiresReplace on all mutable fields
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"Organization invitations cannot be updated. Changes to email or permission_level require replacing the invitation.",
	)
}

// Delete deletes an organization invitation (invalidates it).
func (r *OrganizationInvitationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state OrganizationInvitationResourceModel

	// Read current state
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	invitationID := state.ID.ValueString()

	tflog.Info(ctx, "Invalidating organization invitation", map[string]interface{}{
		"invitation_id": invitationID,
	})

	// Invalidate the invitation
	httpResp, err := r.client.DoRequest(ctx, "POST", fmt.Sprintf("/api/v2/organization_invitations/%s/invalidate", invitationID), nil)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error invalidating invitation",
			fmt.Sprintf("Could not invalidate invitation %s: %s", invitationID, err.Error()),
		)
		return
	}
	defer httpResp.Body.Close()

	// Handle response
	if httpResp.StatusCode != http.StatusOK &&
	   httpResp.StatusCode != http.StatusAccepted &&
	   httpResp.StatusCode != http.StatusNoContent &&
	   httpResp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(httpResp.Body)
		resp.Diagnostics.AddError(
			"Error invalidating invitation",
			fmt.Sprintf("API returned status %d: %s", httpResp.StatusCode, string(body)),
		)
		return
	}

	tflog.Info(ctx, "Invalidated organization invitation", map[string]interface{}{
		"invitation_id": invitationID,
	})
}

// ImportState imports an existing invitation by its ID.
func (r *OrganizationInvitationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by invitation_id
	invitationID := req.ID

	tflog.Info(ctx, "Importing organization invitation", map[string]interface{}{
		"invitation_id": invitationID,
	})

	// Fetch invitation to validate it exists
	invitation, err := r.getInvitationByID(ctx, invitationID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error importing invitation",
			fmt.Sprintf("Could not find invitation %s: %s", invitationID, err.Error()),
		)
		return
	}

	// Set the ID attribute
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), invitationID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("email"), invitation.Email)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), invitation.OrganizationID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("created_at"), invitation.CreatedAt)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("expires_at"), invitation.ExpiresAt)...)

	if invitation.AcceptedAt != nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("accepted_at"), *invitation.AcceptedAt)...)
	}

	status := computeInvitationStatus(invitation.AcceptedAt, invitation.ExpiresAt)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("status"), status)...)
}

// Helper functions

// getInvitationByID fetches a specific invitation by ID
func (r *OrganizationInvitationResource) getInvitationByID(ctx context.Context, invitationID string) (*OrganizationInvitationResult, error) {
	httpResp, err := r.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/organization_invitations/%s", invitationID), nil)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("invitation not found")
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", httpResp.StatusCode, string(body))
	}

	var invitationResp OrganizationInvitationResponse
	if err := json.Unmarshal(body, &invitationResp); err != nil {
		return nil, fmt.Errorf("error parsing response: %w", err)
	}

	return &invitationResp.Result, nil
}

// computeInvitationStatus determines the status based on accepted_at and expires_at timestamps
func computeInvitationStatus(acceptedAt *string, expiresAt string) string {
	if acceptedAt != nil && *acceptedAt != "" {
		return "accepted"
	}

	// Parse expiration time
	expires, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		// If we can't parse the time, assume pending
		return "pending"
	}

	if time.Now().After(expires) {
		return "expired"
	}

	return "pending"
}

// getEmailDomain extracts the domain from an email address for logging (privacy)
func getEmailDomain(email string) string {
	parts := strings.Split(email, "@")
	if len(parts) == 2 {
		return "@" + parts[1]
	}
	return "[unknown]"
}
