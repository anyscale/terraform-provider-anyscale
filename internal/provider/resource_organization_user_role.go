package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Wire layer for the organization role write paths.
//
// There are TWO independent org role write paths, and which one a call must use
// is decided by the wire format, not by convenience:
//
//	PUT /api/v2/organization_collaborators/{identity_id}        body: {"permission_level": "<one value>"}
//	PUT /api/v2/organization_collaborators/{user_id}/roles      body: {"base_role": "...", "additional_roles": [...]}
//
// Note the ID namespaces differ between them - the legacy path is keyed by
// identity_id, the roles path by user_id. They are different identifiers for the
// same person and are not interchangeable; passing one where the other is
// expected produces a 404 that looks like "no such member" rather than a type
// error. resolveIdentityForEmail (resource_cloud_user_role.go) returns both, in
// that order, for exactly this reason.
//
// Why the split is mandatory rather than an optimization: permission_level is a
// SINGLE flattened enum
// (owner|collaborator|image_reader|image_reader_no_base_images|unspecified),
// so the legacy path structurally cannot express a base role and an additional
// role at the same time - it has one slot. Only the roles path can carry the
// pair. Verified 2026-07-28 against the live OpenAPI spec's request schemas
// (UpdateOrganizationCollaborator requires ['permission_level'];
// SetOrganizationRoles requires ['base_role', 'additional_roles']).
//
// NAMING: the Terraform attribute is deny_roles; the WIRE field is
// additional_roles. That mismatch is deliberate and is not a typo.
//
// The API's own name and description ("Additional RBAC organization roles") are
// misleading: Anyscale's published permission docs state that container image
// roles ARE deny roles, and the permissions table shows both legal values
// strictly SUBTRACTING from the default tier - image_reader cannot create custom
// images or register external ones, and image_reader_no_base_images
// additionally cannot deploy Anyscale base images. So the field restricts, and
// deny_roles matches both the real behavior and the cloud resource's field of
// the same name.
//
// Do not "fix" the schema name back to match the wire. The mapping is the point:
// the wire keeps the API's spelling, the schema tells the practitioner the
// truth. The OpenAPI spec is authoritative for paths, shapes and legal values -
// not for what a field means.
//
// One real asymmetry with cloud deny_roles, worth knowing and NOT flattening:
// org image deny roles override implicit permissions and therefore apply to
// organization owners too, whereas cloud deny roles do not restrict organization
// or project owners. Same mechanism, different blast radius.

// orgUserRoleBackendDefaultBaseRole is the base role a freshly-added org member
// actually has, confirmed by a real read of a newly-created member.
//
// Delete reverts to THIS value, and the distinction matters: there is a second,
// different "default" in play - the access-management script this resource
// replaces treats a role-unstated config entry as "writer". That one governs how
// CONFIG is interpreted; this one governs what the BACKEND starts a member at.
// Reverting a destroyed role to the script's default instead of the backend's
// would leave a destroyed role MORE privileged than a brand-new member, which is
// the wrong direction for a destroy. ("writer" is not even a legal org base role
// - OrganizationBaseRole is owner|collaborator - so that mistake would also fail
// on the wire, but do not rely on that as the guard.)
const orgUserRoleBackendDefaultBaseRole = "collaborator"

// SetOrganizationRolesRequest is the body for
// PUT /api/v2/organization_collaborators/{user_id}/roles.
//
// Both fields are required by the API and both are always sent, even when
// AdditionalRoles is empty. This call is a full SET over the PAIR: it writes
// base_role and additional_roles together and will overwrite a base role that a
// legacy permission_level call just wrote. Never sequence the two write paths
// within one apply.
type SetOrganizationRolesRequest struct {
	BaseRole        string   `json:"base_role"`
	AdditionalRoles []string `json:"additional_roles"`
}

// setOrganizationRoles calls the roles PUT, the only path that can write a base
// role and additional roles together.
//
// Keyed by user_id, not identity_id (see the ID-namespace note above).
//
// additionalRoles must be non-nil; pass an empty slice to clear. A nil slice
// marshals to JSON null, which the API rejects for a required array field -
// callers converting from a Terraform list should use stringListToSlice, which
// already maps null/unknown to an empty slice rather than nil.
//
// Accepts 200 and 204: the live API returns 204 on a successful write.
// StatusNotFound is deliberately NOT accepted, so a missing member surfaces as
// ErrNotFound to the caller rather than as an indistinguishable success - see
// the accepted-status contract in api_helpers.go, where listing 404 as expected
// makes it unrecoverable from a real 200.
func setOrganizationRoles(ctx context.Context, client *Client, userID, baseRole string, additionalRoles []string) error {
	if additionalRoles == nil {
		additionalRoles = []string{}
	}
	reqBody, err := MarshalRequestBody(SetOrganizationRolesRequest{
		BaseRole:        baseRole,
		AdditionalRoles: additionalRoles,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal set organization roles request: %w", err)
	}
	_, err = DoRequestRaw(
		ctx, client, "PUT", fmt.Sprintf("/api/v2/organization_collaborators/%s/roles", userID), reqBody,
		http.StatusOK, http.StatusNoContent,
	)
	return err
}

// setOrganizationPermissionLevel calls the legacy, ungated write path. It can
// carry exactly one value, so it is only usable when this resource is not
// managing deny roles.
func setOrganizationPermissionLevel(ctx context.Context, client *Client, identityID, permissionLevel string) error {
	reqBody, err := MarshalRequestBody(UpdateOrganizationCollaboratorRequest{PermissionLevel: permissionLevel})
	if err != nil {
		return fmt.Errorf("failed to marshal update organization collaborator request: %w", err)
	}
	_, err = DoRequestRaw(
		ctx, client, "PUT", fmt.Sprintf("/api/v2/organization_collaborators/%s", identityID), reqBody,
		http.StatusOK, http.StatusNoContent,
	)
	return err
}

// orgRolesFeatureDisabled reports whether an error is the org roles endpoint's
// feature gate rather than a real failure. The roles path is gated behind an
// organization feature flag and is unconditionally unavailable on Azure; both
// surface as 501. Matched on the status text DoRequestRaw produces, since the
// helper does not return the status code itself.
func orgRolesFeatureDisabled(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unexpected status 501")
}

// orgRolesDisabledDetail explains a 501 in terms of what the practitioner
// actually wrote. A raw 501 tells them nothing actionable, and the trigger is
// never obvious from the plan: it is the presence of deny_roles, not
// base_role, that forces this resource onto the gated endpoint.
const orgRolesDisabledDetail = "The organization roles API is not enabled for this organization, so deny_roles cannot be managed here. " +
	"Setting deny_roles (even to an empty list) routes this resource to the gated " +
	"PUT /api/v2/organization_collaborators/{user_id}/roles endpoint; base_role on its own uses an " +
	"ungated endpoint and works everywhere. Remove deny_roles from your configuration to manage " +
	"base_role alone, or contact Anyscale support to enable the organization roles API. This endpoint is " +
	"unavailable on Azure regardless of the feature flag."

var (
	_ resource.Resource                = &OrganizationUserRoleResource{}
	_ resource.ResourceWithConfigure   = &OrganizationUserRoleResource{}
	_ resource.ResourceWithImportState = &OrganizationUserRoleResource{}
)

// NewOrganizationUserRoleResource returns a new anyscale_organization_user_role resource.
func NewOrganizationUserRoleResource() resource.Resource {
	return &OrganizationUserRoleResource{}
}

// OrganizationUserRoleResource manages one organization member's org-scope role.
//
// Deliberately NOT authoritative over organization membership: it manages the
// role of a member who already exists, and destroying it never evicts anyone.
// Membership itself stays with anyscale_organization_user.
type OrganizationUserRoleResource struct {
	client *Client
}

// OrganizationUserRoleResourceModel maps the resource schema.
type OrganizationUserRoleResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Email      types.String `tfsdk:"email"`
	UserID     types.String `tfsdk:"user_id"`
	IdentityID types.String `tfsdk:"identity_id"`
	BaseRole   types.String `tfsdk:"base_role"`
	// DenyRoles is the schema name; it maps to the wire's additional_roles.
	// See the NAMING note at the top of this file - the API's spelling is
	// misleading and the schema deliberately does not copy it.
	DenyRoles types.List `tfsdk:"deny_roles"`
}

func (r *OrganizationUserRoleResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_user_role"
}

func (r *OrganizationUserRoleResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages one organization member's organization-scope role.\n\n" +
			"This resource manages the role of a user who is **already** a member of the organization; it does " +
			"not add or remove members. Membership is managed by `anyscale_organization_user`. Destroying this " +
			"resource reverts the member's role to the organization default (`" + orgUserRoleBackendDefaultBaseRole +
			"`, with no deny roles) and leaves them a member.\n\n" +
			"**Organization roles are not cloud roles.** The vocabularies differ and the same words mean different " +
			"things at different scopes - `collaborator` is an organization base role *and* a cloud base role with " +
			"different meanings. See the RBAC guide before mixing them.\n\n" +
			"**`deny_roles` requires a feature that is not enabled in every organization**, and is " +
			"unavailable on Azure. Managing `base_role` alone uses an endpoint that works everywhere. See the " +
			"`deny_roles` description.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The email of the organization member whose role this resource manages. Same value as `email`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"email": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Email of the organization member whose role this manages. The member must already " +
					"exist in the organization. Matched case-insensitively against the API, but changing this value " +
					"replaces the resource - it identifies a different person.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The user ID of the organization member, resolved from `email`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"identity_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The identity ID of the organization member, resolved from `email`. This is a " +
					"different identifier from `user_id`; the two organization role endpoints are keyed by different ones.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"base_role": schema.StringAttribute{
				Required: true,
				// Required with no Default, deliberately. A default here would be applied
				// during adoption of an existing member, so a real organization owner whose
				// config omitted the field would be silently demoted on the first apply. A
				// default is only safe where guessing wrong is harmless, and a privilege
				// change is not. Stating the role is this resource's entire job.
				//
				// No OneOf validator: the roles API is being actively extended, and a
				// hard-coded enum would reject a new backend role until a provider release.
				// Known values are documented here instead, per the permissive-validation
				// decision.
				MarkdownDescription: "The member's organization base role. Known values are `owner` and `collaborator`; " +
					"the API may accept newer values, so this is not validated against a fixed list. Required - " +
					"there is deliberately no default, because guessing it during adoption of an existing member " +
					"could silently change their privileges.",
			},
			"deny_roles": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				// Optional+Computed, and the two states mean different things:
				//   omitted  -> leave whatever the organization already has, and write via
				//               the ungated legacy endpoint (works in every organization).
				//   declared -> authoritative over the whole set, including an explicit []
				//               which genuinely clears, via the gated roles endpoint.
				//
				// This is deliberately ASYMMETRIC with anyscale_cloud_user_role.deny_roles,
				// which is plain Optional ("omitted means none"). Porting that contract here
				// would make every apply need the gated endpoint, so the resource would fail
				// in any organization without the feature - consistency would cost the
				// resource. The cloud roles endpoint is ungated; this one is not.
				//
				// No UseStateForUnknown: on a writable attribute it pins the prior value and
				// hides real drift instead of surfacing it.
				MarkdownDescription: "Container image deny roles for the member - restrictions layered on top of " +
					"`base_role`, never extra capability. Known values are `image_reader` (cannot create custom " +
					"images or register external images) and `image_reader_no_base_images` (the same, and " +
					"additionally cannot deploy Anyscale base images). Not validated against a fixed list, as the " +
					"API is being extended.\n\n" +
					"Note these **also restrict organization owners**, unlike cloud deny roles, which do not " +
					"restrict organization or project owners.\n\n" +
					"Omit this attribute to leave the organization's existing deny roles untouched. Set it - " +
					"including to an empty list `[]`, which removes all deny roles - to manage the set " +
					"authoritatively.\n\n" +
					"**Setting this attribute at all requires the organization roles API**, which is not enabled in " +
					"every organization and is unavailable on Azure. Managing only `base_role` uses a different " +
					"endpoint that works everywhere.\n\n" +
					"The underlying API field is named `additional_roles`; that name is misleading and this " +
					"attribute deliberately does not copy it.",
			},
		},
	}
}

func (r *OrganizationUserRoleResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// additionalRolesDeclared reports whether the practitioner actually wrote
// deny_roles, which is what selects the write path.
//
// The Unknown check is load-bearing and is the trap in an Optional+Computed
// attribute: when omitted on Create, the framework presents it as UNKNOWN, not
// null. A check written as IsNull() alone reads Unknown as "declared" and would
// route every Create onto the gated endpoint - exactly the failure the
// Optional+Computed design exists to avoid.
func denyRolesDeclared(l types.List) bool {
	return !l.IsNull() && !l.IsUnknown()
}

// writeOrganizationRole applies base role and (when declared) additional roles,
// choosing the write path from what the configuration declared.
//
// Never writes through both paths in one call: the roles endpoint is a SET over
// the pair and would clobber a base role the legacy call had just written.
func (r *OrganizationUserRoleResource) writeOrganizationRole(
	ctx context.Context, model *OrganizationUserRoleResourceModel, identityID, userID string,
) diag.Diagnostics {
	var diags diag.Diagnostics

	if !denyRolesDeclared(model.DenyRoles) {
		if err := setOrganizationPermissionLevel(ctx, r.client, identityID, model.BaseRole.ValueString()); err != nil {
			diags.AddError(
				"Could Not Set Organization Base Role",
				collaboratorErrorDiagnosticDetail(err),
			)
		}
		return diags
	}

	additionalRoles, listDiags := stringListToSlice(ctx, model.DenyRoles)
	diags.Append(listDiags...)
	if diags.HasError() {
		return diags
	}

	if err := setOrganizationRoles(ctx, r.client, userID, model.BaseRole.ValueString(), additionalRoles); err != nil {
		if orgRolesFeatureDisabled(err) {
			diags.AddAttributeError(
				path.Root("deny_roles"),
				"Organization Roles API Not Enabled",
				orgRolesDisabledDetail,
			)
			return diags
		}
		diags.AddError(
			"Could Not Set Organization Roles",
			collaboratorErrorDiagnosticDetail(err),
		)
	}
	return diags
}

// findOrgCollaboratorByEmail resolves an org member's full record from their
// email, with additional_roles hydrated from the singular per-user endpoint.
//
// The two-step is required, not incidental: the list endpoint hardcodes
// additional_roles to an empty slice regardless of the member's real roles, so
// reading roles off the list yields a FALSE empty rather than a missing one.
// hydrateCollaboratorRoles performs the second read and preserves the tri-state
// (nil = undetermined, [] = genuinely none).
//
// Note this overlaps resolveIdentityForEmail (resource_cloud_user_role.go),
// which runs the same query but returns only the two IDs. Worth collapsing onto
// one helper that returns the record, with the ID-only variant derived from it -
// left alone here to avoid changing a shipped resource's behavior in the same
// change that introduces this one.
func findOrgCollaboratorByEmail(ctx context.Context, client *Client, email string) (*OrganizationCollaboratorResult, error) {
	collaborators, err := listAllOrganizationCollaborators(ctx, client, url.Values{"email": []string{email}})
	if err != nil {
		return nil, err
	}
	for _, c := range collaborators {
		// EqualFold, not ==: the server-side email filter is a narrowing hint and is
		// not guaranteed to be an exact match, and email identity is case-insensitive.
		if !strings.EqualFold(c.Email, email) {
			continue
		}
		hydrated := hydrateCollaboratorRoles(ctx, client, c)
		return &hydrated, nil
	}
	return nil, fmt.Errorf("%w: no organization member found with email %s", ErrNotFound, email)
}

// applyCollaboratorToModel copies a read API record onto the model.
func applyCollaboratorToModel(ctx context.Context, model *OrganizationUserRoleResourceModel, c *OrganizationCollaboratorResult) diag.Diagnostics {
	var diags diag.Diagnostics

	model.ID = types.StringValue(c.Email)
	model.Email = types.StringValue(c.Email)
	model.IdentityID = types.StringValue(c.ID)
	if c.UserID != nil && *c.UserID != "" {
		model.UserID = types.StringValue(*c.UserID)
	} else {
		model.UserID = types.StringNull()
	}
	model.BaseRole = types.StringValue(c.BaseRole)

	denyRoles, listDiags := additionalRolesToList(ctx, c.AdditionalRoles)
	diags.Append(listDiags...)
	model.DenyRoles = denyRoles

	return diags
}

func (r *OrganizationUserRoleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan OrganizationUserRoleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	email := plan.Email.ValueString()
	identityID, userID, err := resolveIdentityForEmail(ctx, r.client, email)
	if err != nil {
		resp.Diagnostics.AddError(
			"Could Not Find Organization Member",
			fmt.Sprintf("This resource manages the role of an existing organization member and does not add members. "+
				"Could not resolve %q: %s", email, err.Error()),
		)
		return
	}

	// Adoption: a member always already has some role, so this is a write over an
	// existing value rather than a creation. Applying the declared role against a
	// backend that already holds it is the migration path off the access-management
	// script, and must be an authoritative write rather than an assumption that the
	// pre-existing value is correct.
	resp.Diagnostics.Append(r.writeOrganizationRole(ctx, &plan, identityID, userID)...)
	if resp.Diagnostics.HasError() {
		return
	}

	collaborator, err := findOrgCollaboratorByEmail(ctx, r.client, email)
	if err != nil {
		resp.Diagnostics.AddError(
			"Organization Role Set But Could Not Be Read Back",
			fmt.Sprintf("The role write succeeded but reading it back failed: %s", err.Error()),
		)
		return
	}
	resp.Diagnostics.Append(applyCollaboratorToModel(ctx, &plan, collaborator)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Trace(ctx, "created organization user role", map[string]any{"email": email})
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *OrganizationUserRoleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OrganizationUserRoleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	collaborator, err := findOrgCollaboratorByEmail(ctx, r.client, state.Email.ValueString())
	if err != nil {
		// The member is gone from the organization. The role has no independent
		// existence, so the resource is gone too - membership removal is out of this
		// resource's control by design.
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(applyCollaboratorToModel(ctx, &state, collaborator)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationUserRoleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan OrganizationUserRoleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state OrganizationUserRoleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	identityID := state.IdentityID.ValueString()
	userID := state.UserID.ValueString()
	if identityID == "" || userID == "" {
		var err error
		identityID, userID, err = resolveIdentityForEmail(ctx, r.client, plan.Email.ValueString())
		if err != nil {
			resp.Diagnostics.AddError(
				"Could Not Find Organization Member",
				fmt.Sprintf("Could not resolve %q: %s", plan.Email.ValueString(), err.Error()),
			)
			return
		}
	}

	resp.Diagnostics.Append(r.writeOrganizationRole(ctx, &plan, identityID, userID)...)
	if resp.Diagnostics.HasError() {
		return
	}

	collaborator, err := findOrgCollaboratorByEmail(ctx, r.client, plan.Email.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Organization Role Set But Could Not Be Read Back",
			fmt.Sprintf("The role write succeeded but reading it back failed: %s", err.Error()),
		)
		return
	}
	resp.Diagnostics.Append(applyCollaboratorToModel(ctx, &plan, collaborator)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete reverts the member's role to the organization default and leaves them a
// member.
//
// There is no DELETE endpoint for organization roles - the only delete under
// /api/v2/organization_collaborators removes the person from the organization
// entirely. Calling that here would make this resource authoritative over
// membership, which it deliberately is not: destroying a role must never evict a
// human. So destroy is a WRITE back to the backend default.
//
// The path is chosen the same way every other write is. If this resource was
// never managing deny_roles, the ungated legacy endpoint can revert the
// base role, and reverting through the gated endpoint instead would make destroy
// fail in organizations where create and update both worked.
func (r *OrganizationUserRoleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state OrganizationUserRoleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	identityID := state.IdentityID.ValueString()
	userID := state.UserID.ValueString()
	if identityID == "" || userID == "" {
		var err error
		identityID, userID, err = resolveIdentityForEmail(ctx, r.client, state.Email.ValueString())
		if err != nil {
			// The member is already gone; there is no role left to revert.
			tflog.Warn(ctx, "Organization member not found on delete; nothing to revert", map[string]any{
				"email": state.Email.ValueString(),
			})
			return
		}
	}

	if !denyRolesDeclared(state.DenyRoles) {
		if err := setOrganizationPermissionLevel(ctx, r.client, identityID, orgUserRoleBackendDefaultBaseRole); err != nil {
			resp.Diagnostics.AddError(
				"Could Not Revert Organization Base Role",
				collaboratorErrorDiagnosticDetail(err),
			)
		}
		return
	}

	if err := setOrganizationRoles(ctx, r.client, userID, orgUserRoleBackendDefaultBaseRole, []string{}); err != nil {
		if orgRolesFeatureDisabled(err) {
			resp.Diagnostics.AddError("Organization Roles API Not Enabled", orgRolesDisabledDetail)
			return
		}
		resp.Diagnostics.AddError(
			"Could Not Revert Organization Roles",
			collaboratorErrorDiagnosticDetail(err),
		)
	}
}

// ImportState imports by email, matching the resource's key.
func (r *OrganizationUserRoleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	email := strings.TrimSpace(req.ID)
	if email == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			"Expected the email of an existing organization member, for example: terraform import anyscale_organization_user_role.example user@example.com",
		)
		return
	}

	collaborator, err := findOrgCollaboratorByEmail(ctx, r.client, email)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error Importing Organization User Role",
			fmt.Sprintf("Could not find an organization member with email %q: %s\n\n"+
				"This resource manages the role of an existing member. Use the anyscale_organization_users data "+
				"source to list members and their emails.", email, err.Error()),
		)
		return
	}

	var model OrganizationUserRoleResourceModel
	resp.Diagnostics.Append(applyCollaboratorToModel(ctx, &model, collaborator)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}
