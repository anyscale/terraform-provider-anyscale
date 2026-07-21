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
			"**Note:** Invitations have an expiration time and must be accepted by the recipient. Once accepted, the user will have default collaborator permissions. Use the `anyscale_organization_collaborator` resource to manage their permissions. There is no need to remove this resource once accepted - an accepted invitation is a harmless historical record, its `status` simply reads as `accepted` from then on, and leaving it in your configuration has no side effects.\n\n" +
			"~> **Warning:** What `terraform destroy` actually does here depends on whether the invitation was accepted. Destroying a **pending** invitation genuinely revokes it - it is invalidated immediately, and the recipient can no longer accept it. Destroying an **already-accepted** invitation has no effect on the resulting member: acceptance created a separate, independent `anyscale_organization_collaborator` identity, and invalidating the (already-consumed) invitation record does not touch it. To remove an existing member's access, destroy their `anyscale_organization_collaborator` resource instead - destroying this resource never does that.\n\n" +
			"**Duplicate invitations:** Inviting an email address that is already an organization member fails with a clear error directing you to the `anyscale_organization_collaborator` resource instead. Inviting an email address that already has a *pending* invitation does not fail - it silently invalidates the previous invitation (expiring it immediately) and creates a new one with a new `id`; a different letter-casing of the same address counts as the same recipient for this purpose. If that previous invitation is tracked elsewhere (a separate resource block, a different Terraform configuration, or state left over from a prior apply), its `status` will simply read as `expired` on the next refresh rather than surfacing an error.",

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
				MarkdownDescription: "The email address to send the invitation to. Stored exactly as configured (the API lower-cases it internally for its own matching, but the casing you type is what's kept in state). Changing to a genuinely different email replaces the invitation; changing only its letter case does not, since the Anyscale API treats those as the same invitation.",
				PlanModifiers: []planmodifier.String{
					caseInsensitiveEmailPlanModifier{},
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
				MarkdownDescription: "The current status of the invitation. Can be `pending`, `accepted`, or `expired`. Computed from `accepted_at` and `expires_at` - note that sending a new invitation to the same email address invalidates this one immediately, which surfaces here as `expired` on the next refresh rather than as an error.",
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

	reqBody, err := MarshalRequestBody(createReq)
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

	// Send create request. The API only returns the invitation ID here (full details are
	// fetched separately below via getInvitationByID) - OrganizationInvitationResponse's other
	// fields are simply left at their zero value by json.Unmarshal, which is fine since only
	// Result.ID is read.
	invitationResp, err := DoRequestAndParse[OrganizationInvitationResponse](
		ctx, r.client, "POST", "/api/v2/organization_invitations", reqBody,
		http.StatusOK, http.StatusCreated,
	)
	if err != nil {
		detail := extractAPIErrorDetail(err)
		if strings.Contains(detail, "already a member of your organization") {
			resp.Diagnostics.AddError(
				"Email Already Belongs to an Organization Member",
				fmt.Sprintf("%s\n\nLook up their identity_id with the anyscale_organization_user data source and import them as an anyscale_organization_collaborator instead of inviting them again.", detail),
			)
			return
		}
		AddAPIError(&resp.Diagnostics, "create invitation", err)
		return
	}

	invitationID := invitationResp.Result.ID
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

	// Update plan with full details. Email is deliberately NOT overwritten with
	// invitation.Email here - the backend always lowercases the stored email
	// regardless of what was sent, and email is Required (not Computed), so
	// overwriting plan.Email with a differently-cased API echo made Terraform
	// Core reject the apply outright ("Provider produced inconsistent result
	// after apply") for any email containing an uppercase character - a Create-
	// time hard failure present since v0.1.0, not just a later plan diff. The
	// user's configured casing stays in state instead.
	plan.OrganizationID = types.StringValue(invitation.OrganizationID)
	plan.CreatedAt = types.StringValue(invitation.CreatedAt)
	plan.ExpiresAt = types.StringValue(invitation.ExpiresAt)

	plan.AcceptedAt = types.StringPointerValue(invitation.AcceptedAt)

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

	// Update state with API data. email is deliberately NOT refreshed from
	// invitation.Email - the backend always returns it lower-cased regardless of
	// configured casing, and overwriting state here would fight the
	// caseInsensitiveEmailPlanModifier on next plan and re-surface the same
	// inconsistent-result failure this fix addresses in Create. email never
	// changes out from under Read anyway (RequiresReplace-equivalent), so
	// there's nothing legitimate to refresh.
	state.OrganizationID = types.StringValue(invitation.OrganizationID)
	state.CreatedAt = types.StringValue(invitation.CreatedAt)
	state.ExpiresAt = types.StringValue(invitation.ExpiresAt)

	state.AcceptedAt = types.StringPointerValue(invitation.AcceptedAt)

	// Compute status
	status := computeInvitationStatus(invitation.AcceptedAt, invitation.ExpiresAt)
	state.Status = types.StringValue(status)

	switch status {
	case "expired":
		tflog.Warn(ctx, "Invitation has expired", map[string]interface{}{
			"invitation_id": invitationID,
			"expires_at":    invitation.ExpiresAt,
		})
	case "accepted":
		tflog.Info(ctx, "Invitation has been accepted", map[string]interface{}{
			"invitation_id": invitationID,
			"accepted_at":   *invitation.AcceptedAt,
		})
	}

	// Save updated state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates an organization invitation.
// Note: Invitations cannot be updated - email has RequiresReplace and every other attribute is Computed-only.
func (r *OrganizationInvitationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// This should never be called due to RequiresReplace on all mutable fields
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"Organization invitations cannot be updated. Changing the email requires replacing the invitation.",
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

	// Invalidate the invitation - 404 is OK (already gone)
	_, err := DoRequestRaw(ctx, r.client, "POST", fmt.Sprintf("/api/v2/organization_invitations/%s/invalidate", invitationID), nil,
		http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound)
	if err != nil {
		AddAPIError(&resp.Diagnostics, fmt.Sprintf("invalidate invitation %s", invitationID), err)
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
	defer func() { _ = httpResp.Body.Close() }()

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

// caseInsensitiveEmailPlanModifier suppresses a plan diff (and the replacement
// this attribute would otherwise trigger) when the only difference between the
// configured email and the stored value is letter case. The Anyscale API dedups
// invitations by lower-cased email (contract I2/I-OPEN, traced against
// organization_invitations_dao.py's create_invitation/find_invitation, both of
// which normalize through LOWER), so a case-only edit is the same invitation to
// the backend - forcing a destroy+recreate over it would be a real
// revoke-then-reinvite access event for no functional change. A genuinely
// different email still requires replace.
type caseInsensitiveEmailPlanModifier struct{}

func (m caseInsensitiveEmailPlanModifier) Description(_ context.Context) string {
	return "Requires replacement for an email change, unless the only difference is letter case."
}

func (m caseInsensitiveEmailPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m caseInsensitiveEmailPlanModifier) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// No established prior value to compare: a fresh create, or state not yet
	// populated (e.g. immediately post-import before the first Read).
	if req.StateValue.IsNull() || req.PlanValue.IsUnknown() {
		return
	}
	if req.PlanValue.ValueString() == req.StateValue.ValueString() {
		return
	}
	if strings.EqualFold(req.PlanValue.ValueString(), req.StateValue.ValueString()) {
		// Case-only change - keep the originally-stored casing rather than
		// forcing a replace.
		resp.PlanValue = req.StateValue
		return
	}
	resp.RequiresReplace = true
}
