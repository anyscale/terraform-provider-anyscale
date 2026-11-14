package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                     = &IAMRoleGrantResource{}
	_ resource.ResourceWithConfigure        = &IAMRoleGrantResource{}
	_ resource.ResourceWithModifyPlan       = &IAMRoleGrantResource{}
	_ resource.ResourceWithImportState      = &IAMRoleGrantResource{}
	_ resource.ResourceWithConfigValidators = &IAMRoleGrantResource{}
)

// NewIAMRoleGrantResource creates a new role binding resource.
func NewIAMRoleGrantResource() resource.Resource {
	return &IAMRoleGrantResource{}
}

// IAMRoleGrantResource grants one role to one user group or user on one
// organization, cloud or project. The API never edits a binding, so every argument forces
// replacement.
type IAMRoleGrantResource struct {
	client *Client
}

// IAMRoleGrantResourceModel describes the resource data model.
type IAMRoleGrantResourceModel struct {
	ID             types.String `tfsdk:"id"`
	UserGroupID    types.String `tfsdk:"user_group_id"`
	UserID         types.String `tfsdk:"user_id"`
	RoleID         types.String `tfsdk:"role_id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	CloudID        types.String `tfsdk:"cloud_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	Origin         types.String `tfsdk:"origin"`
	CreatedBy      types.String `tfsdk:"created_by"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// scope returns the resource type and ID the binding is held on: whichever of
// the three scope attributes is set. The config validator guarantees exactly
// one is.
func (m *IAMRoleGrantResourceModel) scope() (resourceType, resourceID string) {
	switch {
	case !m.CloudID.IsNull():
		return roleBindingResourceCloud, m.CloudID.ValueString()
	case !m.ProjectID.IsNull():
		return roleBindingResourceProject, m.ProjectID.ValueString()
	default:
		return roleBindingResourceOrganization, m.OrganizationID.ValueString()
	}
}

// principal returns the principal type and ID that holds the binding:
// whichever of user_group_id and user_id is set. The config validator
// guarantees exactly one is.
func (m *IAMRoleGrantResourceModel) principal() (principalType, principalID string) {
	if !m.UserID.IsNull() {
		return roleBindingPrincipalUser, m.UserID.ValueString()
	}
	return roleBindingPrincipalUserGroup, m.UserGroupID.ValueString()
}

// isManagedRoleBindingPrincipal reports whether the principal type is one
// this resource models, for the same reason as isManagedRoleBindingScope.
func isManagedRoleBindingPrincipal(principalType string) bool {
	switch principalType {
	case roleBindingPrincipalUser, roleBindingPrincipalUserGroup:
		return true
	}
	return false
}

// isManagedRoleBindingScope reports whether the resource type is one this
// resource models. The API may add scopes; a binding on one must be refused,
// never mapped onto a modeled scope it is not held on.
func isManagedRoleBindingScope(resourceType string) bool {
	switch resourceType {
	case roleBindingResourceOrganization, roleBindingResourceCloud, roleBindingResourceProject:
		return true
	}
	return false
}

// setFromAPI sets every attribute from a full binding, as create and import
// return it. Callers check isManagedRoleBindingPrincipal and
// isManagedRoleBindingScope first.
func (m *IAMRoleGrantResourceModel) setFromAPI(b *roleBindingResult) {
	m.ID = types.StringValue(b.ID)
	m.UserGroupID = types.StringNull()
	m.UserID = types.StringNull()
	switch b.PrincipalType {
	case roleBindingPrincipalUserGroup:
		m.UserGroupID = types.StringValue(b.PrincipalID)
	case roleBindingPrincipalUser:
		m.UserID = types.StringValue(b.PrincipalID)
	}
	m.RoleID = types.StringValue(b.RoleID)
	m.OrganizationID = types.StringNull()
	m.CloudID = types.StringNull()
	m.ProjectID = types.StringNull()
	switch b.ResourceType {
	case roleBindingResourceOrganization:
		m.OrganizationID = types.StringValue(b.ResourceID)
	case roleBindingResourceCloud:
		m.CloudID = types.StringValue(b.ResourceID)
	case roleBindingResourceProject:
		m.ProjectID = types.StringValue(b.ResourceID)
	}
	m.Origin = types.StringPointerValue(b.Origin)
	m.CreatedBy = types.StringPointerValue(b.CreatedBy)
	m.CreatedAt = types.StringValue(b.CreatedAt)
}

func (r *IAMRoleGrantResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_role_grant"
}

func (r *IAMRoleGrantResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	scopeDescription := "Specify exactly one of `organization_id`, `cloud_id` or `project_id`. Changing it replaces the binding."

	resp.Schema = schema.Schema{
		MarkdownDescription: roleBindingsBetaBanner +
			"Grants one role to one user group or user on an organization, cloud or project. " +
			"Access granted this way is not visible to `anyscale_cloud_access`; see the [RBAC guide](../guides/rbac.md). " +
			"A binding cannot be edited, so changing any argument replaces it. " +
			"Don't manage one scope with both this resource and `anyscale_iam_role_binding` or `anyscale role-binding policy apply`: the binding resource and a policy apply revoke this resource's grants, and the next apply re-creates them. " +
			"Requires an organization [converted to role bindings](../guides/rbac.md#organization-access-mode).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The role binding ID (`rb_...`).",
				PlanModifiers:       keep,
			},
			"user_group_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The user group that holds the role (`ug_...`). Groups synced from an identity provider are accepted. Specify exactly one of `user_group_id` or `user_id`. Changing it replaces the binding.",
				PlanModifiers:       replace,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"user_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The user that holds the role (`usr_...`); look up an organization member by email with the `anyscale_iam_organization_user` data source. Service accounts are users too (`usr_...`). Specify exactly one of `user_group_id` or `user_id`. Changing it replaces the binding.",
				PlanModifiers:       replace,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"role_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The role to grant (`rol_...`); look one up by name with the `anyscale_iam_role` data source. " +
					"Changing it replaces the binding, leaving the principal without the role in between unless the resource sets `lifecycle { create_before_destroy = true }`.",
				PlanModifiers: replace,
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"organization_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Grant the role on the organization: the token's own, as returned by the `anyscale_organization` data source. " + scopeDescription,
				PlanModifiers:       replace,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"cloud_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Grant the role on this cloud. " + scopeDescription,
				PlanModifiers:       replace,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Grant the role on this project. " + scopeDescription,
				PlanModifiers:       replace,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"origin": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "How the grant was made, as the API records it, for example `imperative`. Null when the API does not know.",
				PlanModifiers:       keep,
			},
			"created_by": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The ID of the user who made the grant. Null when the API does not know.",
				PlanModifiers:       keep,
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the grant was made.",
				PlanModifiers:       keep,
			},
		},
	}
}

func (r *IAMRoleGrantResource) ConfigValidators(ctx context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(
			path.MatchRoot("user_group_id"),
			path.MatchRoot("user_id"),
		),
		resourcevalidator.ExactlyOneOf(
			path.MatchRoot("organization_id"),
			path.MatchRoot("cloud_id"),
			path.MatchRoot("project_id"),
		),
	}
}

func (r *IAMRoleGrantResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if client, ok := clientFromProviderData(req.ProviderData, "Unexpected Resource Configure Type", &resp.Diagnostics); ok {
		r.client = client
	}
}

func (r *IAMRoleGrantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan IAMRoleGrantResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resourceType, resourceID := plan.scope()
	principalType, principalID := plan.principal()
	binding, err := createRoleBinding(ctx, r.client, createRoleBindingRequest{
		PrincipalType: principalType,
		PrincipalID:   principalID,
		RoleID:        plan.RoleID.ValueString(),
		ResourceType:  resourceType,
		ResourceID:    resourceID,
	})
	if err != nil {
		switch {
		case isStatusWithSubstring(err, http.StatusConflict, roleBindingDuplicateSubstring):
			r.addDuplicateError(ctx, &resp.Diagnostics, &plan, err)
		case isOrgNotConvertedConflict(err):
			addOrgNotConvertedError(&resp.Diagnostics, err, "")
		case isStatusWithSubstring(err, http.StatusConflict, roleBindingArchivedRoleSubstring):
			resp.Diagnostics.AddError("Role Is Archived",
				fmt.Sprintf("%s An archived role cannot be newly assigned; pick another role.", extractAPIErrorDetail(err)))
		case isRoleBindingAdminGroupRefusal(err):
			addRoleBindingAdminGroupError(&resp.Diagnostics, err, "")
		case isStatusWithSubstring(err, http.StatusForbidden, roleBindingSelfGrantSubstring):
			resp.Diagnostics.AddError("Cannot Grant a Role to the Provider's Own Identity",
				fmt.Sprintf("%s Bind a user group that this identity belongs to, or apply with a different identity.", extractAPIErrorDetail(err)))
		case isRoleBindingsDisabled(err):
			// Create also answers 404 for an unknown principal or role, so ask
			// whether the feature is on before calling it off.
			r.addCreateNotFoundError(ctx, &resp.Diagnostics, err)
		default:
			AddAPIError(&resp.Diagnostics, "create role binding", err)
		}
		return
	}

	tflog.Info(ctx, "Role binding created", map[string]any{"role_binding_id": binding.ID})
	plan.setFromAPI(binding)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// addCreateNotFoundError reports a 404 from create: the feature is off, or
// the principal or role does not exist, in which case the API's own detail says
// which.
func (r *IAMRoleGrantResource) addCreateNotFoundError(ctx context.Context, diags *diag.Diagnostics, createErr error) {
	if r.roleBindingsOnAfterNotFound(ctx, diags, "create role binding", createErr, "") {
		AddAPIError(diags, "create role binding", createErr)
	}
}

// roleBindingsOnAfterNotFound settles what a role-bindings 404 means by probing
// whether the feature is enabled. It reports false after adding the diagnostic
// when the probe fails (op names the failed call) or the feature is off
// (disabledOutcome says what happens to the binding). It reports true when the
// feature is on, leaving the caller to say what the 404 means for its operation.
func (r *IAMRoleGrantResource) roleBindingsOnAfterNotFound(ctx context.Context, diags *diag.Diagnostics, op string, notFoundErr error, disabledOutcome string) bool {
	enabled, probeErr := roleBindingsEnabled(ctx, r.client)
	switch {
	case probeErr != nil:
		addRoleBindingsProbeFailedError(diags, op, notFoundErr, probeErr)
		return false
	case !enabled:
		addRoleBindingsDisabledError(diags, disabledOutcome)
		return false
	}
	return true
}

// addDuplicateError reports a 409 from create: the principal already holds this
// role on this resource. The existing binding is looked up so the import
// command can be copied as-is; if that lookup fails the command keeps a
// placeholder. The existing binding is never adopted.
func (r *IAMRoleGrantResource) addDuplicateError(ctx context.Context, diags *diag.Diagnostics, plan *IAMRoleGrantResourceModel, createErr error) {
	importID := "<role_binding_id>"
	resourceType, resourceID := plan.scope()
	principalType, principalID := plan.principal()
	bindings, err := listRoleBindingsForPrincipal(ctx, r.client, resourceType, resourceID, principalType, principalID)
	if err != nil {
		tflog.Warn(ctx, "Could not look up the existing role binding for the import hint", map[string]any{"error": err.Error()})
	}
	for _, b := range bindings {
		if b.RoleID == plan.RoleID.ValueString() {
			importID = b.ID
			break
		}
	}
	diags.AddError("Role Grant Already Exists",
		fmt.Sprintf("%s To manage the existing binding with Terraform, import it:\n\n  terraform import <resource address> %s",
			extractAPIErrorDetail(createErr), importID))
}

func (r *IAMRoleGrantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state IAMRoleGrantResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	bindingID := state.ID.ValueString()
	resourceType, resourceID := state.scope()
	principalType, principalID := state.principal()
	bindings, err := listRoleBindingsForPrincipal(ctx, r.client, resourceType, resourceID, principalType, principalID)
	if err != nil {
		if isRoleBindingsDisabled(err) {
			if r.roleBindingsOnAfterNotFound(ctx, &resp.Diagnostics, "read role binding", err, roleBindingsDisabledReadOutcome) {
				// The feature is on, so the principal no longer exists.
				tflog.Warn(ctx, "Role binding's principal not found, removing from state",
					map[string]any{"role_binding_id": bindingID, "detail": extractAPIErrorDetail(err)})
				resp.State.RemoveResource(ctx)
			}
			return
		}
		if isHTTPStatus(err, http.StatusForbidden) {
			resp.Diagnostics.AddError("Role Grant Not Readable",
				fmt.Sprintf("The Anyscale API refused to list bindings on %s %q (HTTP 403): %s "+
					"Reading them requires managing IAM on the %s. If the %s was deleted outside Terraform, remove this binding from state with `terraform state rm`.",
					resourceType, resourceID, extractAPIErrorDetail(err), resourceType, resourceType))
			return
		}
		AddAPIError(&resp.Diagnostics, "read role binding", err)
		return
	}
	binding, byID := matchRoleBinding(bindings, bindingID, state.RoleID.ValueString(), resourceType, resourceID)
	if binding == nil {
		// Revoked outside Terraform, or the principal no longer exists.
		tflog.Warn(ctx, "Role binding not found, removing from state", map[string]any{"role_binding_id": bindingID})
		resp.Diagnostics.AddWarning("Role Grant Revoked Outside Terraform",
			fmt.Sprintf("Role binding %q (role %s to %s %s on %s %s) no longer exists: it was revoked outside Terraform, or the user or user group holding it was deleted. "+
				"Terraform plans to re-create it. If a role-binding policy (anyscale_iam_role_binding or anyscale role-binding policy apply) manages this %s, "+
				"manage its grants one way only, or each apply revokes what the other grants.",
				bindingID, state.RoleID.ValueString(), principalType, principalID, resourceType, resourceID, resourceType))
		resp.State.RemoveResource(ctx)
		return
	}
	if !byID {
		r.adoptRegrantedBinding(ctx, resp, &state, binding.ID)
		return
	}

	// The arguments are immutable, so only the computed fields the listing
	// carries are refreshed.
	state.Origin = types.StringPointerValue(binding.Origin)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// adoptRegrantedBinding moves state to newID: the same grant, revoked and
// re-granted outside Terraform under a new ID (as a role-binding policy apply
// does). Without this, refresh would drop the binding and the next apply would
// fail creating a duplicate. The listing lacks created_by and created_at, so
// the binding is read in full. The listing decides that the grant exists; a
// re-granted binding is new, so its by-ID read can 403 until its permission
// propagates and is retried. If it still fails, state is left unchanged.
func (r *IAMRoleGrantResource) adoptRegrantedBinding(ctx context.Context, resp *resource.ReadResponse, state *IAMRoleGrantResourceModel, newID string) {
	oldID := state.ID.ValueString()
	binding, err := getRoleBindingWithRetry(ctx, r.client, newID)
	if err != nil {
		resp.Diagnostics.AddError("Could Not Read Re-granted Role Grant",
			fmt.Sprintf("Role binding %q was revoked and re-granted outside Terraform as %q, but reading %q failed: %s "+
				"Terraform state was not changed; run the plan again.", oldID, newID, newID, extractAPIErrorDetail(err)))
		return
	}

	tflog.Warn(ctx, "Role binding was re-granted outside Terraform under a new ID; tracking the new ID",
		map[string]any{"role_binding_id": oldID, "new_role_binding_id": newID})
	if binding.Origin != nil && *binding.Origin != roleBindingOriginImperative {
		resourceType, _ := state.scope()
		resp.Diagnostics.AddWarning("Role Grant Now Stated by a Role-Binding Policy",
			fmt.Sprintf("Role binding %q was revoked and re-granted outside Terraform as %q (origin %q), and Terraform now tracks the new ID. "+
				"A role-binding policy on this %s states this grant, so managing it with both anyscale_iam_role_grant and `anyscale role-binding policy apply` "+
				"makes each revoke what the other grants. Manage the grants on this %s one way.",
				oldID, newID, *binding.Origin, resourceType, resourceType))
	}
	state.setFromAPI(binding)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *IAMRoleGrantResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every argument forces replacement, so Terraform never plans an update.
	resp.Diagnostics.AddError("Role Grant Cannot Be Updated",
		"Every argument of anyscale_iam_role_grant forces replacement, so an in-place update was not expected. Please report this issue to the provider developers.")
}

func (r *IAMRoleGrantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state IAMRoleGrantResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	bindingID := state.ID.ValueString()
	resourceType, resourceID := state.scope()
	principalType, principalID := state.principal()
	// A binding granted in the last few minutes can 403 here until its
	// permission propagates. The same 403 means "already revoked", so retry
	// only while the binding is recent and the listing still shows it.
	recent := isRecentlyCreated(state.CreatedAt.ValueString(), roleBindingByIDRecentWindow)
	_, err := retryRoleBindingByID(ctx, bindingID, func() (struct{}, error) {
		return struct{}{}, deleteRoleBinding(ctx, r.client, bindingID)
	}, func() bool {
		if !recent {
			return false
		}
		listed, listErr := findRoleBindingForPrincipal(ctx, r.client, resourceType, resourceID, principalType, principalID, bindingID)
		return listErr == nil && listed != nil
	})
	if err == nil {
		tflog.Info(ctx, "Role binding deleted", map[string]any{"role_binding_id": bindingID})
		return
	}
	if isRoleBindingsDisabled(err) {
		r.handleDeleteNotFound(ctx, &resp.Diagnostics, bindingID, err)
		return
	}
	if isOrgNotConvertedConflict(err) {
		addOrgNotConvertedError(&resp.Diagnostics, err, roleBindingsDisabledDeleteOutcome)
		return
	}
	if isStatusWithSubstring(err, http.StatusForbidden, roleBindingSelfRevokeSubstring) {
		resp.Diagnostics.AddError("Cannot Revoke the Provider's Own Role Grant",
			fmt.Sprintf("%s Revoke it in the Anyscale console or with a different identity, or run `terraform state rm` on this resource's address to stop managing it.",
				extractAPIErrorDetail(err)))
		return
	}
	if isRoleBindingAdminGroupRefusal(err) {
		addRoleBindingAdminGroupError(&resp.Diagnostics, err, roleBindingAdminGroupDeleteOutcome)
		return
	}
	if !isHTTPStatus(err, http.StatusForbidden) {
		AddAPIError(&resp.Diagnostics, "delete role binding", err)
		return
	}

	// A revoked binding answers 403; the listing tells that from a real
	// refusal.
	binding, listErr := findRoleBindingForPrincipal(ctx, r.client, resourceType, resourceID, principalType, principalID, bindingID)
	switch {
	case listErr != nil && isRoleBindingsDisabled(listErr):
		r.handleDeleteNotFound(ctx, &resp.Diagnostics, bindingID, listErr)
	case listErr != nil:
		tflog.Warn(ctx, "Could not check whether the role binding is already revoked", map[string]any{"error": listErr.Error()})
		AddAPIError(&resp.Diagnostics, "delete role binding", err)
	case binding == nil:
		tflog.Info(ctx, "Role binding already revoked", map[string]any{"role_binding_id": bindingID})
	default:
		AddAPIError(&resp.Diagnostics, "delete role binding", err)
	}
}

// handleDeleteNotFound settles a 404 met while deleting. With the feature on,
// the binding or its principal is gone, so there is nothing left to revoke; with
// it off, the binding stays in state.
func (r *IAMRoleGrantResource) handleDeleteNotFound(ctx context.Context, diags *diag.Diagnostics, bindingID string, notFoundErr error) {
	if r.roleBindingsOnAfterNotFound(ctx, diags, "delete role binding", notFoundErr, roleBindingsDisabledDeleteOutcome) {
		tflog.Info(ctx, "Role binding already gone", map[string]any{"role_binding_id": bindingID, "detail": extractAPIErrorDetail(notFoundErr)})
	}
}

// roleBindingNaturalKey is a binding named by what it grants rather than by
// its ID: <scope_type>/<scope_id>/<principal_type>/<principal_id>/<role_id>.
type roleBindingNaturalKey struct {
	ResourceType  string
	ResourceID    string
	PrincipalType string
	PrincipalID   string
	RoleID        string
}

// roleBindingImportFormats names both accepted import IDs, for diagnostics.
const roleBindingImportFormats = "Import a role binding by its ID (`rb_...`) or by " +
	"`<scope_type>/<scope_id>/<principal_type>/<principal_id>/<role_id>`, where scope_type is organization, cloud or project " +
	"and principal_type is user or user_group."

// parseRoleBindingNaturalKey parses a composite import ID. Anyscale IDs
// contain no "/", so the split is unambiguous.
func parseRoleBindingNaturalKey(id string) (roleBindingNaturalKey, error) {
	parts := strings.Split(id, "/")
	if len(parts) != 5 {
		return roleBindingNaturalKey{}, fmt.Errorf("%q has %d slash-separated parts, want 5", id, len(parts))
	}
	for i, part := range parts {
		if part == "" {
			return roleBindingNaturalKey{}, fmt.Errorf("%q has an empty part at position %d", id, i+1)
		}
	}
	key := roleBindingNaturalKey{ResourceType: parts[0], ResourceID: parts[1], PrincipalType: parts[2], PrincipalID: parts[3], RoleID: parts[4]}
	if !isManagedRoleBindingScope(key.ResourceType) {
		return roleBindingNaturalKey{}, fmt.Errorf("scope type %q is not organization, cloud or project", key.ResourceType)
	}
	if !isManagedRoleBindingPrincipal(key.PrincipalType) {
		return roleBindingNaturalKey{}, fmt.Errorf("principal type %q is not user or user_group", key.PrincipalType)
	}
	return key, nil
}

// resolveRoleBindingNaturalKey finds the ID of the binding a composite import
// ID names, through the principal listing Read uses. The API keeps at most one
// live binding per principal, role and resource. It returns "" with
// diagnostics when there is no such binding or the listing fails.
func (r *IAMRoleGrantResource) resolveRoleBindingNaturalKey(ctx context.Context, key roleBindingNaturalKey, diags *diag.Diagnostics) string {
	bindings, err := listRoleBindingsForPrincipal(ctx, r.client, key.ResourceType, key.ResourceID, key.PrincipalType, key.PrincipalID)
	if err != nil {
		switch {
		case isRoleBindingsDisabled(err):
			if r.roleBindingsOnAfterNotFound(ctx, diags, "list role bindings for import", err, "") {
				AddConfigError(diags, "Role Grant Principal Not Found",
					fmt.Sprintf("The %s %q could not be found: %s", key.PrincipalType, key.PrincipalID, extractAPIErrorDetail(err)))
			}
		case isHTTPStatus(err, http.StatusForbidden):
			AddConfigError(diags, "Role Grants Not Readable",
				fmt.Sprintf("The Anyscale API refused to list bindings on %s %q (HTTP 403): %s Reading them requires managing IAM on the %s.",
					key.ResourceType, key.ResourceID, extractAPIErrorDetail(err), key.ResourceType))
		default:
			AddAPIError(diags, "list role bindings for import", err)
		}
		return ""
	}
	for _, b := range bindings {
		if b.RoleID == key.RoleID {
			return b.ID
		}
	}
	AddConfigError(diags, "Role Grant Not Found",
		fmt.Sprintf("No role binding grants role %q to %s %q on %s %q. Import never creates a binding; to grant it, declare the resource and apply.",
			key.RoleID, key.PrincipalType, key.PrincipalID, key.ResourceType, key.ResourceID))
	return ""
}

// ModifyPlan refuses to create or change this resource on an Azure control plane.
func (r *IAMRoleGrantResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	refuseRBACPlanOnAzure(r.client, "anyscale_iam_role_grant", req, resp)
}

func (r *IAMRoleGrantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if refuseRBACOnAzure(r.client, "anyscale_iam_role_grant", &resp.Diagnostics) {
		return
	}
	bindingID := req.ID
	if !strings.HasPrefix(bindingID, "rb_") {
		key, err := parseRoleBindingNaturalKey(bindingID)
		if err != nil {
			AddConfigError(&resp.Diagnostics, "Invalid Role Grant Import ID", fmt.Sprintf("%s. %s", err.Error(), roleBindingImportFormats))
			return
		}
		bindingID = r.resolveRoleBindingNaturalKey(ctx, key, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	// A binding granted moments ago can 403 here until its permission
	// propagates, and nothing on an import ID says how old it is.
	binding, err := getRoleBindingWithRetry(ctx, r.client, bindingID)
	if err != nil {
		switch {
		case isRoleBindingsDisabled(err):
			if r.roleBindingsOnAfterNotFound(ctx, &resp.Diagnostics, "read role binding for import", err, "") {
				AddAPIError(&resp.Diagnostics, "read role binding for import", err)
			}
		case isHTTPStatus(err, http.StatusForbidden):
			// A binding that does not exist answers 403 here as well.
			AddConfigError(&resp.Diagnostics, "Role Grant Not Found",
				fmt.Sprintf("No role binding %q could be read: it does not exist, was revoked, or you do not manage IAM on the resource it is held on. %s",
					bindingID, extractAPIErrorDetail(err)))
		default:
			AddAPIError(&resp.Diagnostics, "read role binding for import", err)
		}
		return
	}
	if !isManagedRoleBindingPrincipal(binding.PrincipalType) {
		AddConfigError(&resp.Diagnostics, "Role Grant Principal Not Supported",
			fmt.Sprintf("Role binding %q is held by a %s, which anyscale_iam_role_grant does not manage. It manages bindings held by a user group or a user.",
				bindingID, binding.PrincipalType))
		return
	}
	if !isManagedRoleBindingScope(binding.ResourceType) {
		AddConfigError(&resp.Diagnostics, "Role Grant Scope Not Supported",
			fmt.Sprintf("Role binding %q is held on a %s, which anyscale_iam_role_grant does not manage. It manages bindings on an organization, cloud or project.",
				bindingID, binding.ResourceType))
		return
	}

	var state IAMRoleGrantResourceModel
	state.setFromAPI(binding)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
