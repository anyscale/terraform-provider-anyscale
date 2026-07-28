package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.Resource                = &CloudUserRoleResource{}
	_ resource.ResourceWithConfigure   = &CloudUserRoleResource{}
	_ resource.ResourceWithImportState = &CloudUserRoleResource{}
)

// NewCloudUserRoleResource creates a new cloud user role resource.
func NewCloudUserRoleResource() resource.Resource {
	return &CloudUserRoleResource{}
}

// CloudUserRoleResource manages a user's Phase 2B RBAC role on a cloud (design
// doc section 4.2). Keyed on email, not user_id: this resource genuinely
// needs three different identifiers across one lifecycle (email for the
// legacy bootstrap POST, user_id for the roles PUT/GET, identity_id for the
// legacy DELETE) and no single one covers all three, but email is the only
// one that reliably resolves the other two in one call (an org-wide search by
// email returns both identity_id and user_id; the reverse direction is weaker
// since user_id is optional in that same response shape). user_id and
// identity_id are therefore Computed outputs only, never practitioner inputs.
type CloudUserRoleResource struct {
	client *Client
}

// CloudUserRoleResourceModel describes the resource data model.
type CloudUserRoleResourceModel struct {
	ID         types.String `tfsdk:"id"` // computed composite: cloud_id/email
	CloudID    types.String `tfsdk:"cloud_id"`
	Email      types.String `tfsdk:"email"`
	UserID     types.String `tfsdk:"user_id"`
	IdentityID types.String `tfsdk:"identity_id"`
	BaseRole   types.String `tfsdk:"base_role"`
	DenyRoles  types.List   `tfsdk:"deny_roles"`
}

// Metadata returns the resource type name.
func (r *CloudUserRoleResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_user_role"
}

// Schema defines the schema for the resource.
func (r *CloudUserRoleResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a user's Phase 2B RBAC role on an Anyscale cloud (`base_role` plus optional `deny_roles`) - a newer, richer role model than the legacy `owner`/`write`/`readonly` permission level, and currently the only way to grant `project_viewer`, `compute_config_viewer`, or `workload_operator`. These roles are not visible or settable in the Anyscale console today; this resource is the only interface to them.\n\n" +
			"~> **Warning:** This feature is gated behind two independent, separately-controlled backend flags (one for reading roles, one for writing them) - if either is off for your organization, every operation on this resource fails, most visibly as an HTTP 501. There is no way for the provider to detect this ahead of time short of trying.\n\n" +
			"**Create is not a single API call.** The Anyscale API has no endpoint that both grants a Phase 2B role AND establishes the underlying cloud membership a later `destroy` needs in order to revoke it - so this resource's `Create` ALWAYS makes the legacy collaborator call first, unconditionally, and only then sets the real role. This is deliberate, not an inefficiency: a version of this resource that skipped that first call when the user already appeared to be a cloud collaborator would occasionally walk someone into a role grant with no way to ever revoke it (there is no way to tell those two situations apart ahead of time - see the `email` and destroy documentation below). If the second call fails after the first succeeded, this resource rolls back what it just created rather than silently leaving unintended access in place.\n\n" +
			"**A role assignment whose target already held cloud permissions from outside Terraform before this resource's `Create` ever ran can never be destroyed.** `Create` always attempts the bootstrap call described above first, but if the target already had a permissions row on this cloud (granted directly through the API, or by a script), that call fails with an expected, non-fatal 409 and `Create` proceeds without ever establishing the membership record `destroy` needs - so this condition can arise through `Create` itself, not only through `import`, whenever it is layered on top of a pre-existing out-of-band grant. Terraform cannot detect this ahead of time (there is no read that distinguishes it from a healthy assignment), so neither `Create` nor `import` refuses it; the only place it can ever surface is a failed `destroy`, which returns a diagnostic explaining that `terraform state rm` is the only clean exit.\n\n" +
			"**Directory-synced organizations and self-role-changes are not supported** - the underlying API rejects both, and this resource surfaces the API's own error rather than retrying.\n\n" +
			"**Example Import:**\n```\nterraform import anyscale_cloud_user_role.analyst <cloud_id>/<email>\n```",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Composite identifier in `cloud_id/email` format. Used for import.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"cloud_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The ID of the cloud this role grant applies to.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			"email": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The email address of the org member this role is granted to. Required (rather than `user_id`) because this resource's own Create needs an email for the legacy bootstrap call it always makes first, and email is the one identifier that reliably resolves to both `user_id` and `identity_id` in a single lookup - the reverse direction is unreliable, since `user_id` is an optional field on the identity-search response this provider would otherwise need. See `user_id`/`identity_id` below for the resolved values.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			"user_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The user ID resolved from `email` - the same `user_id` exposed by the `anyscale_organization_user`/`anyscale_organization_users` data sources. Read-only: this resource resolves it automatically, you never set it directly.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"identity_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The identity ID resolved from `email`, used internally by this resource's `destroy` (the legacy revoke path is keyed by identity, not user, id - see the resource-level documentation on identifier handling). Read-only: you never need to supply or look this up yourself.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"base_role": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The user's base RBAC role on this cloud. One of `owner`, `writer`, `collaborator`, `project_viewer`, `compute_config_viewer`, `workload_operator`. " +
					"This is a distinct vocabulary from the legacy `owner`/`write`/`readonly` permission level exposed elsewhere in this provider (for example `writer` here is not the same role as legacy `write`, and `collaborator` here is not the same role as legacy `write` either, despite the public docs calling that legacy role \"Cloud collaborator\") - never assume the two line up, and this resource never exposes the legacy vocabulary alongside this one. " +
					"Setting this attribute is fully authoritative for this (cloud, user) pair: it replaces whatever role and deny roles the user previously held on this cloud through this API, it never adds to them.",
				Validators: []validator.String{
					stringvalidator.OneOf("owner", "writer", "collaborator", "project_viewer", "compute_config_viewer", "workload_operator"),
				},
			},

			"deny_roles": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				MarkdownDescription: "Additional restriction roles layered on top of `base_role`, if any. Currently only `cloud_read_only` exists. " +
					"Deliberately plain Optional, not Optional+Computed: omitting this attribute means none, not \"whatever the server already has\" - every apply sends the full deny-role set (empty if omitted), so removing an entry from your configuration removes it for real on the next apply. " +
					"In the rare case the backend reports more than one base role for this user on this cloud (only possible via a directory-sync interaction outside this resource's own writes, which always set exactly one), this attribute - and `base_role` - are left untouched on refresh rather than guessed at; a warning diagnostic names what was actually found.",
				Validators: []validator.List{
					listvalidator.ValueStringsAre(stringvalidator.OneOf("cloud_read_only")),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *CloudUserRoleResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// cloudUserRoleBridgePermissionLevel is the legacy permission_level used to
// establish cloud membership in Create's step 1 (H21), when the roles PUT
// itself is the only thing that actually matters. Deliberately the most
// restrictive real legacy value: if step 2 fails and this is what gets left
// behind, the blast radius of an unintended grant is minimized.
const cloudUserRoleBridgePermissionLevel = "readonly"

// cloudUserRoleAlreadyHasPermissionsDetailSubstring is the exact, load-bearing
// substring of the legacy bootstrap POST's 409 detail when the target user
// already has a permissions row for the cloud (traced against
// cloud_collaborators_service.py's _resolve_cloud_collaborator: "User with
// email {email} already has permissions for this cloud."). H22 requires this
// specific 409 be treated as an expected, non-fatal outcome of an
// unconditional step 1 - not a generic failure.
const cloudUserRoleAlreadyHasPermissionsDetailSubstring = "already has permissions for this cloud"

// cloudUserRoleSelfModifyDetailSubstring is H23's exact 403 detail substring
// (traced against cloud_collaborators_service.py's
// _validate_user_for_cloud_role_change: "You cannot modify your own role."),
// returned by the roles PUT when its target user_id is the calling token's
// own identity.
const cloudUserRoleSelfModifyDetailSubstring = "cannot modify your own role"

// Create implements H21/H22's forced two-call sequence: the legacy
// collaborator POST ALWAYS runs first, unconditionally - never skipped based
// on a pre-check, per H22. Assayer found live that a user granted a role
// through the roles PUT alone ends up with a permissions row but no
// membership edge, which makes the bootstrap 409 and the eventual Delete 404 -
// an unrepairable state (G8: confirmed live that no read distinguishes this
// from a healthy grant, so the only safe policy is never producing it in the
// first place). A conditional bootstrap (skip if a pre-check shows they're
// already a collaborator) is exactly the shape of bug that produces it, so
// this always attempts step 1 and treats ONLY its specific
// "already has permissions for this cloud" 409 as an expected, non-fatal
// outcome meaning membership already existed before this Create touched
// anything. Order between the two calls remains load-bearing regardless -
// the legacy POST has SET semantics and would clobber the role if run
// second. If the roles PUT then fails after THIS Create established
// membership itself, it attempts a rollback and reports plainly whether that
// rollback succeeded, rather than ever silently leaving an unintended grant
// in place; if the user already held membership before this Create ran, a
// roles PUT failure never touches that pre-existing membership at all.
func (r *CloudUserRoleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan CloudUserRoleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := plan.CloudID.ValueString()
	email := plan.Email.ValueString()

	tflog.Info(ctx, "Creating cloud user role", map[string]interface{}{"cloud_id": cloudID, "email": email})

	identityID, userID, err := resolveIdentityForEmail(ctx, r.client, email)
	if err != nil {
		resp.Diagnostics.AddError(
			"Could Not Resolve Organization Member",
			fmt.Sprintf("Could not resolve an organization member with email %s: %s", email, err.Error()),
		)
		return
	}

	weCreatedMembership := false
	createReq := CreateCloudCollaboratorRequest{Email: email, PermissionLevel: cloudUserRoleBridgePermissionLevel}
	reqBody, err := MarshalRequestBody(createReq)
	if err != nil {
		resp.Diagnostics.AddError("Error Marshaling Request", err.Error())
		return
	}
	if _, err := DoRequestRaw(
		ctx, r.client, "POST", fmt.Sprintf("/api/v2/clouds/%s/collaborators/users", cloudID), reqBody,
		http.StatusOK, http.StatusNoContent,
	); err != nil {
		detail := extractAPIErrorDetail(err)
		if !strings.Contains(detail, cloudUserRoleAlreadyHasPermissionsDetailSubstring) {
			resp.Diagnostics.AddError("Could Not Establish Cloud Membership", detail)
			return
		}
		// Expected per H22: the bootstrap always runs, and this specific 409
		// just means the user already had cloud membership before this
		// Create touched anything - proceed to the role grant without having
		// created (and therefore without ever needing to roll back) anything
		// ourselves.
		tflog.Info(ctx, "User already had cloud membership; bootstrap 409 treated as expected", map[string]interface{}{"cloud_id": cloudID, "email": email})
	} else {
		weCreatedMembership = true
	}

	denyRoles, diags := stringListToSlice(ctx, plan.DenyRoles)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := setCloudRole(ctx, r.client, cloudID, userID, plan.BaseRole.ValueString(), denyRoles); err != nil {
		detail := extractAPIErrorDetail(err)
		if strings.Contains(detail, cloudUserRoleSelfModifyDetailSubstring) {
			detail = fmt.Sprintf("%s\n\nThis role's email resolves to the identity Terraform is currently authenticated as. The Anyscale API does not allow granting (or revoking) a cloud role for your own identity - have another cloud owner apply this configuration instead.", detail)
		}

		if !weCreatedMembership {
			resp.Diagnostics.AddError(
				"Could Not Set Cloud Role",
				fmt.Sprintf("%s\n\nThis user was already a cloud collaborator before this apply, and that pre-existing collaborator relationship was NOT modified or removed.", detail),
			)
			return
		}

		// H21: we established membership ourselves this Create; the role PUT
		// that was supposed to immediately follow it failed. Roll back rather
		// than leave the bridge permission_level in place unintentionally.
		// identityID was already resolved above (from email, before either
		// call), so no extra lookup is needed to attempt this.
		if _, delErr := DoRequestRaw(
			ctx, r.client, "DELETE", fmt.Sprintf("/api/v2/clouds/%s/collaborators/%s", cloudID, identityID), nil,
			http.StatusOK, http.StatusNoContent, http.StatusNotFound,
		); delErr != nil {
			resp.Diagnostics.AddError(
				"Could Not Set Cloud Role, And Rollback Also Failed",
				fmt.Sprintf("Setting the role failed: %s\n\nThis resource then tried to roll back the cloud membership it had just created moments ago, and that ALSO failed: %s\n\nAs a result, identity %s (email %s) is now left holding an unintended %s permission_level on cloud %s that Terraform did not manage to remove. Delete it manually - for example DELETE /api/v2/clouds/%s/collaborators/%s - or via the equivalent CLI/console action, then retry.",
					detail, extractAPIErrorDetail(delErr), identityID, email, cloudUserRoleBridgePermissionLevel, cloudID, cloudID, identityID),
			)
			return
		}

		resp.Diagnostics.AddError(
			"Could Not Set Cloud Role",
			fmt.Sprintf("%s\n\nThe cloud membership this resource had just created moments ago was rolled back successfully - no unintended access was left behind.", detail),
		)
		return
	}

	tflog.Info(ctx, "Set cloud role", map[string]interface{}{"cloud_id": cloudID, "email": email, "base_role": plan.BaseRole.ValueString()})

	if diags := readCloudUserRoleIntoModel(ctx, r.client, cloudID, userID, &plan); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	plan.UserID = types.StringValue(userID)
	plan.IdentityID = types.StringValue(identityID)
	plan.ID = types.StringValue(cloudUserRoleID(cloudID, email))

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes base_role/deny_roles from the roles GET only - never the
// legacy search, which H19 proved is lossy (a workload_operator role reads
// back as plain readonly through it). user_id/identity_id are UseStateForUnknown
// (see the schema) and not re-resolved here: email is RequiresReplace, so the
// identity behind it cannot change without a replace regardless.
func (r *CloudUserRoleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state CloudUserRoleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := state.CloudID.ValueString()
	userID := state.UserID.ValueString()

	found, diags := readCloudUserRoleIntoModelIfPresent(ctx, r.client, cloudID, userID, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		tflog.Warn(ctx, "Cloud user role not found, removing from state", map[string]interface{}{"cloud_id": cloudID, "user_id": userID})
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update re-sends the roles PUT only - cloud_id/email (and the user_id/identity_id
// resolved from email) are RequiresReplace or UseStateForUnknown, so this
// never touches cloud membership at all.
func (r *CloudUserRoleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan CloudUserRoleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state CloudUserRoleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := plan.CloudID.ValueString()
	userID := state.UserID.ValueString()

	denyRoles, diags := stringListToSlice(ctx, plan.DenyRoles)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := setCloudRole(ctx, r.client, cloudID, userID, plan.BaseRole.ValueString(), denyRoles); err != nil {
		detail := extractAPIErrorDetail(err)
		if strings.Contains(detail, cloudUserRoleSelfModifyDetailSubstring) {
			detail = fmt.Sprintf("%s\n\nThis role's email resolves to the identity Terraform is currently authenticated as. The Anyscale API does not allow granting (or revoking) a cloud role for your own identity - have another cloud owner apply this configuration instead.", detail)
		}
		resp.Diagnostics.AddError("Could Not Update Cloud Role", detail)
		return
	}

	if diags := readCloudUserRoleIntoModel(ctx, r.client, cloudID, userID, &plan); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	plan.UserID = state.UserID
	plan.IdentityID = state.IdentityID
	plan.ID = state.ID

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete calls the legacy DELETE using the identity_id resolved and stored at
// Create/Import time - the only revoke path (H21), since the roles API itself
// has no delete. Surfaces the auto_add_user 409 (H17) and self-removal 403 as
// clear, named diagnostics rather than retrying either.
//
// G8 (live-confirmed): the cloud collaborator search reflects the same
// permissions row the legacy DELETE checks for existence, not the separate
// membership edge DELETE actually requires to succeed - a role granted
// outside Terraform through the roles endpoint alone shows up in that search
// as an ordinary-looking record, identically to a properly bootstrapped one.
// There is no read-only way to tell these apart ahead of time. Create always
// attempts the bootstrap first, but a target that already had a permissions
// row from an out-of-band grant makes that bootstrap 409 in the same
// "already has permissions" shape as a genuine pre-existing collaborator, so
// Create proceeds either way and can inherit this condition too, not only
// import. When the legacy DELETE 404s, this does not pass the raw API error
// through - see the diagnostic below for why and what to do about it.
func (r *CloudUserRoleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state CloudUserRoleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := state.CloudID.ValueString()
	email := state.Email.ValueString()
	identityID := state.IdentityID.ValueString()

	tflog.Info(ctx, "Removing cloud user role", map[string]interface{}{"cloud_id": cloudID, "email": email})

	_, err := DoRequestRaw(ctx, r.client, "DELETE", fmt.Sprintf("/api/v2/clouds/%s/collaborators/%s", cloudID, identityID), nil,
		http.StatusOK, http.StatusNoContent)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			resp.Diagnostics.AddError(
				"Cannot Destroy: No API Sequence Can Remove This Role Assignment",
				fmt.Sprintf(
					"This role assignment (email %s on cloud %s) is missing the underlying cloud membership record its only revoke path requires. This resource's own Create always attempts to establish that record first - but if %s already had a permissions row on this cloud from a role granted outside Terraform (directly against the API, or by a script) before this resource's Create ever ran, Create proceeds over that pre-existing row rather than failing, and this resource inherits the condition rather than causing it. There is no API sequence that can repair this after the fact: nothing can create the missing record retroactively, and the only revoke endpoint 404s without it, exactly as it just did. Run 'terraform state rm' on this resource instead of retrying destroy - destroy will never succeed for it.",
					email, cloudID, email,
				),
			)
			return
		}
		resp.Diagnostics.AddError("Could Not Remove Cloud Role", cloudUserRoleDeleteErrorDiagnosticDetail(err))
		return
	}

	tflog.Info(ctx, "Removed cloud user role", map[string]interface{}{"cloud_id": cloudID, "email": email})
}

// ImportState imports by "cloud_id/email" - email is Required, so import must
// populate it or the very next plan would see a Required attribute missing
// from state, breaking the no-op-import contract. Proceeds normally, with no
// attempt to detect whether the role has an underlying cloud membership
// record (H22's bootstrap-vs-roles-only distinction) - G8 proved live that
// the cloud collaborator search reflects the permissions row, not the
// membership edge, so a role granted outside Terraform without ever going
// through the bootstrap call is indistinguishable from a healthy one through
// every available read. This is not import-only: Create inherits the same
// condition whenever its own bootstrap attempt 409s against a pre-existing
// out-of-band permissions row (see Create and Delete's own doc comments).
// Pretending to detect and refuse that state would be its own kind of lie;
// the only place it can actually surface either way is a failed Delete,
// which carries a diagnostic explaining exactly that (see Delete).
func (r *CloudUserRoleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	cloudID, email, err := parseCloudUserRoleID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error Importing Cloud User Role",
			fmt.Sprintf("%s\n\nExpected format: terraform import anyscale_cloud_user_role.<name> <cloud_id>/<email>", err.Error()),
		)
		return
	}

	identityID, userID, err := resolveIdentityForEmail(ctx, r.client, email)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error Importing Cloud User Role",
			fmt.Sprintf("Could not resolve an organization member with email %s: %s", email, err.Error()),
		)
		return
	}

	var model CloudUserRoleResourceModel
	found, diags := readCloudUserRoleIntoModelIfPresent(ctx, r.client, cloudID, userID, &model)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError(
			"Error Importing Cloud User Role",
			fmt.Sprintf("No Phase 2B role found for email %s on cloud %s. If this cloud/org has the cloud-roles feature disabled, the roles GET returns nothing to import rather than an error.", email, cloudID),
		)
		return
	}

	model.ID = types.StringValue(cloudUserRoleID(cloudID, email))
	model.CloudID = types.StringValue(cloudID)
	model.Email = types.StringValue(email)
	model.UserID = types.StringValue(userID)
	model.IdentityID = types.StringValue(identityID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

// Helper functions

func cloudUserRoleID(cloudID, email string) string {
	return cloudID + "/" + email
}

// parseCloudUserRoleID parses a composite ID in format "cloud_id/email", the
// same shape parseCloudResourceID already established for this provider,
// just with "/" per this resource's own import documentation instead of ":".
func parseCloudUserRoleID(id string) (cloudID, email string, err error) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid cloud user role ID format: expected 'cloud_id/email', got %q", id)
	}
	return parts[0], parts[1], nil
}

// stringListToSlice converts a types.List of strings to a []string, treating
// a null or unknown list as empty - the deny_roles Optional-not-Computed
// contract means "omitted" must translate to "send an empty deny_roles set",
// never "omit the field," since SetCloudRoles.deny_roles is a required field
// on the wire.
func stringListToSlice(ctx context.Context, l types.List) ([]string, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return []string{}, nil
	}
	var out []string
	diags := l.ElementsAs(ctx, &out, false)
	if out == nil {
		out = []string{}
	}
	return out, diags
}

// API request/response types for the cloud collaborators and cloud roles
// endpoints (backend models clouds.py: CreateCloudCollaborator, SetCloudRoles,
// CloudUserRoles).

// CreateCloudCollaboratorRequest is the body for the legacy
// POST /clouds/{cloud_id}/collaborators/users call (H21 step 1).
type CreateCloudCollaboratorRequest struct {
	Email           string `json:"email"`
	PermissionLevel string `json:"permission_level"`
}

// SetCloudRolesRequest is the body for PUT
// /clouds/{cloud_id}/collaborators/users/{user_id}/roles (H21 step 2).
// DenyRoles is always sent, even when empty - the wire field is required,
// and an authoritative SET is exactly what H4 says this call does.
type SetCloudRolesRequest struct {
	BaseRole  string   `json:"base_role"`
	DenyRoles []string `json:"deny_roles"`
}

// CloudUserRolesResult is one entry of GET /clouds/{cloud_id}/collaborators/roles.
type CloudUserRolesResult struct {
	UserID    string   `json:"user_id"`
	BaseRoles []string `json:"base_roles"`
	DenyRoles []string `json:"deny_roles"`
}

// CloudUserRolesListResponse is the roles GET's list envelope.
type CloudUserRolesListResponse struct {
	Results  []CloudUserRolesResult `json:"results"`
	Metadata struct {
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// resolveIdentityForEmail resolves an org member's identity_id and user_id
// from their email via the org-wide collaborator list (the same
// listAllOrganizationCollaborators already shared with
// anyscale_organization_user/users), mirroring the email-lookup branch that
// data source already established: a server-side email query param as a
// narrowing hint, plus a case-insensitive exact match client-side, since the
// filter is not guaranteed to be a strict match. This resolution direction
// (email -> both ids) is reliable; the reverse (user_id -> email) is not,
// since user_id is an optional field on the org-wide response - the reason
// this resource is keyed on email in the first place.
func resolveIdentityForEmail(ctx context.Context, client *Client, email string) (identityID, userID string, err error) {
	collaborators, err := listAllOrganizationCollaborators(ctx, client, url.Values{"email": []string{email}})
	if err != nil {
		return "", "", err
	}
	for _, c := range collaborators {
		if !strings.EqualFold(c.Email, email) {
			continue
		}
		if c.UserID == nil || *c.UserID == "" {
			return "", "", fmt.Errorf("organization member %s has no user_id (some identity types do not have one) and cannot be granted a cloud role through this resource", email)
		}
		return c.ID, *c.UserID, nil
	}
	return "", "", fmt.Errorf("no organization member found with email %s", email)
}

// setCloudRole calls the roles PUT (H21 step 2) - a full authoritative SET
// per (user, cloud), never additive (H4).
func setCloudRole(ctx context.Context, client *Client, cloudID, userID, baseRole string, denyRoles []string) error {
	reqBody, err := MarshalRequestBody(SetCloudRolesRequest{BaseRole: baseRole, DenyRoles: denyRoles})
	if err != nil {
		return fmt.Errorf("failed to marshal set cloud role request: %w", err)
	}
	_, err = DoRequestRaw(
		ctx, client, "PUT", fmt.Sprintf("/api/v2/clouds/%s/collaborators/users/%s/roles", cloudID, userID), reqBody,
		http.StatusOK, http.StatusNoContent,
	)
	return err
}

// listCloudUserRoles pages through GET /clouds/{cloud_id}/collaborators/roles,
// the only role read path this resource ever uses - H19 proved the legacy
// search silently misreports role data (a workload_operator reads back as
// plain readonly through it), so that endpoint is never used for role state.
func listCloudUserRoles(ctx context.Context, client *Client, cloudID string) ([]CloudUserRolesResult, error) {
	return PaginatedRequest(
		ctx, client, fmt.Sprintf("/api/v2/clouds/%s/collaborators/roles", cloudID), url.Values{"count": []string{"50"}},
		func(body []byte) ([]CloudUserRolesResult, *string, error) {
			var listResp CloudUserRolesListResponse
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, fmt.Errorf("failed to parse cloud roles response: %w", err)
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
}

// readCloudUserRoleIntoModelIfPresent looks up userID's roles on cloudID and,
// if found, writes base_role/deny_roles into model. Returns found=false if
// the cloud reports no role at all for this user (the caller decides what
// that means - removed from state on a plain Read, an import error on
// ImportState). id/cloud_id/email/user_id/identity_id are never touched here -
// callers set those themselves.
//
// H4's "tolerate a set, never guess" rule: if the API reports zero or more
// than one base role for this user (only reachable via a directory-sync
// interaction outside this resource's own writes, which always set exactly
// one), this leaves model.BaseRole/DenyRoles COMPLETELY UNTOUCHED rather than
// picking element 0 or crashing, and adds a warning diagnostic naming what
// was actually found - so a config/state drift shows up as a loud warning,
// not a silently wrong value or a panic.
func readCloudUserRoleIntoModelIfPresent(ctx context.Context, client *Client, cloudID, userID string, model *CloudUserRoleResourceModel) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics

	allRoles, err := listCloudUserRoles(ctx, client, cloudID)
	if err != nil {
		diags.AddError("Could Not List Cloud Roles", extractAPIErrorDetail(err))
		return false, diags
	}

	var match *CloudUserRolesResult
	for _, entry := range allRoles {
		if entry.UserID == userID {
			found := entry
			match = &found
			break
		}
	}
	if match == nil {
		return false, diags
	}

	if len(match.BaseRoles) != 1 {
		diags.AddWarning(
			"Cloud User Role Has An Unexpected Number Of Base Roles",
			fmt.Sprintf("Expected exactly one base role for user_id %s on cloud %s, found %d: %v. This can only happen through a directory-sync interaction outside this resource's own writes, which always set exactly one. Leaving base_role and deny_roles untouched in state rather than guessing; reconcile manually (for example via GET /api/v2/clouds/%s/collaborators/roles) before trusting the next plan.",
				userID, cloudID, len(match.BaseRoles), match.BaseRoles, cloudID),
		)
		return true, diags
	}

	model.BaseRole = types.StringValue(match.BaseRoles[0])

	denyRolesList, denyDiags := denyRolesToList(ctx, match.DenyRoles, model.DenyRoles)
	diags.Append(denyDiags...)
	model.DenyRoles = denyRolesList

	return true, diags
}

// readCloudUserRoleIntoModel is readCloudUserRoleIntoModelIfPresent for
// Create/Update, where the role genuinely not being found afterward is
// itself a real error - this resource just wrote it.
func readCloudUserRoleIntoModel(ctx context.Context, client *Client, cloudID, userID string, model *CloudUserRoleResourceModel) diag.Diagnostics {
	found, diags := readCloudUserRoleIntoModelIfPresent(ctx, client, cloudID, userID, model)
	if diags.HasError() {
		return diags
	}
	if !found {
		diags.AddError(
			"Cloud Role Set But Not Found On Read-Back",
			fmt.Sprintf("The role write for user_id %s on cloud %s succeeded, but it does not appear in a subsequent read of the cloud's roles. This may indicate the read and write flags for this feature are not both enabled for this organization.", userID, cloudID),
		)
	}
	return diags
}

// denyRolesToList converts the API's real deny-roles slice into a types.List,
// preserving prior state's null-vs-empty-list shape when the API reports zero
// deny roles - deny_roles is Optional-not-Computed, and the wire response
// cannot distinguish "config omitted deny_roles" from "config explicitly set
// deny_roles = []" (this resource always sends deny_roles: [] for both), so
// reproducing one shape unconditionally would create a permanent diff against
// whichever one the user actually wrote. A genuinely non-empty API result
// always wins outright, null or not - that is real, current server state.
func denyRolesToList(ctx context.Context, apiDenyRoles []string, priorState types.List) (types.List, diag.Diagnostics) {
	if len(apiDenyRoles) == 0 {
		if priorState.IsNull() {
			return types.ListNull(types.StringType), nil
		}
		return types.ListValueFrom(ctx, types.StringType, []string{})
	}
	return types.ListValueFrom(ctx, types.StringType, apiDenyRoles)
}

// cloudUserRoleDeleteErrorDiagnosticDetail builds a clean diagnostic body for
// a failed cloud role Delete that is NOT the H22/G8 unrepairable-state 404
// (handled separately, before this is ever called): H17's auto_add_user 409,
// cloud self-removal 403, and the same directory-sync/Policy-API 403
// anyscale_organization_user already detects (remove_cloud_collaborator
// shares that same validation path). Per H24, the missing-edge 404 and the
// auto_add_user 409 are mutually exclusive on a single request - the 404
// check runs first in the same backend function, so a role in the
// unrepairable state always 404s and never reaches this function at all.
func cloudUserRoleDeleteErrorDiagnosticDetail(err error) string {
	detail := extractAPIErrorDetail(err)

	switch {
	case strings.Contains(detail, "auto add users enabled"):
		return fmt.Sprintf("%s\n\nThis cloud has auto_add_user enabled, which this provider also exposes on anyscale_cloud. While that setting is on, the backend refuses to leave any user without cloud access, so it blocks removing this role's underlying cloud membership entirely - not just this resource's role. Disable auto_add_user on the cloud first if you need to fully revoke this user's cloud access, or accept that this role cannot be destroyed while it remains enabled.", detail)
	case strings.Contains(detail, "cannot remove yourself"):
		return fmt.Sprintf("%s\n\nYou cannot destroy an anyscale_cloud_user_role for your own identity. Have another cloud owner destroy it instead.", detail)
	case strings.Contains(detail, "Policy API"):
		return fmt.Sprintf("%s\n\nThis organization's cloud permissions are managed outside Terraform. Use 'anyscale policy set' to manage this user's cloud access instead of this resource. See https://docs.anyscale.com/reference/cli/policy#policy-cli for details.", detail)
	default:
		return detail
	}
}
