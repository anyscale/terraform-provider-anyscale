package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                     = &IAMRoleBindingResource{}
	_ resource.ResourceWithConfigure        = &IAMRoleBindingResource{}
	_ resource.ResourceWithModifyPlan       = &IAMRoleBindingResource{}
	_ resource.ResourceWithImportState      = &IAMRoleBindingResource{}
	_ resource.ResourceWithConfigValidators = &IAMRoleBindingResource{}
)

const iamRoleBindingTypeName = "anyscale_iam_role_binding"

// roleBindingPolicyMaxFrozenRetries bounds how many frozen bindings destroy
// adds to the policy it keeps before giving up.
const roleBindingPolicyMaxFrozenRetries = 10

// NewIAMRoleBindingResource creates a new role binding policy resource.
func NewIAMRoleBindingResource() resource.Resource {
	return &IAMRoleBindingResource{}
}

// IAMRoleBindingResource manages every direct role binding on one scope,
// authoritatively, through the policy generate and apply API.
type IAMRoleBindingResource struct {
	client *Client
}

// RoleBindingPolicyModel is the resource's and the data source's model.
type RoleBindingPolicyModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	CloudID        types.String `tfsdk:"cloud_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	Roles          types.Map    `tfsdk:"roles"`
}

// scope returns the scope type and its ID value, which may be unknown at plan
// time. The config validator guarantees exactly one is set.
func (m *RoleBindingPolicyModel) scope() (string, types.String) {
	switch {
	case !m.CloudID.IsNull():
		return roleBindingResourceCloud, m.CloudID
	case !m.ProjectID.IsNull():
		return roleBindingResourceProject, m.ProjectID
	default:
		return roleBindingResourceOrganization, m.OrganizationID
	}
}

func roleBindingPolicyID(scopeType, scopeID string) string {
	return scopeType + "/" + scopeID
}

// scopeKind is the display noun of a scope type.
func scopeKind(scopeType string) string {
	switch scopeType {
	case roleBindingResourceProject:
		return "Project"
	case roleBindingResourceCloud:
		return "Cloud"
	}
	return "Organization"
}

// policyRoleEntryValidator requires each roles entry to name a principal.
// The API accepts an entry with none and drops it, so the next read would
// lack it and every plan would show it again.
type policyRoleEntryValidator struct{}

func (policyRoleEntryValidator) Description(context.Context) string {
	return "at least one of user_ids or user_group_ids must be set"
}

func (v policyRoleEntryValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (policyRoleEntryValidator) ValidateObject(ctx context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, name := range []string{"user_ids", "user_group_ids"} {
		if v, ok := req.ConfigValue.Attributes()[name]; ok && !v.IsNull() {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Role Entry Names No Principal",
		"Set at least one of user_ids or user_group_ids. The API ignores a role with no principals, so the entry would plan a change on every run; remove it to grant the role to nobody.")
}

func (r *IAMRoleBindingResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_role_binding"
}

func (r *IAMRoleBindingResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	scopeDescription := "Specify exactly one of `organization_id`, `cloud_id` or `project_id`. Changing it replaces the policy."
	idSet := func(description string) schema.SetAttribute {
		return schema.SetAttribute{
			ElementType:         types.StringType,
			Optional:            true,
			MarkdownDescription: description,
			Validators: []validator.Set{
				setvalidator.SizeAtLeast(1),
				setvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
			},
		}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: roleBindingsBetaBanner +
			"Manages every direct role binding on one organization, cloud or project. Authoritative: an apply revokes any direct binding on the scope that `roles` does not name, " +
			"including ones Anyscale grants itself, such as a project or cloud creator's owner binding. " +
			"Never combine it with `anyscale_iam_role_grant` or `anyscale role-binding policy apply` on the same scope. " +
			"Destroy, import and the first-apply behavior are in the [RBAC guide](../guides/rbac.md#managing-every-grant-on-a-scope-anyscale_iam_role_binding). " +
			"Requires an organization [converted to role bindings](../guides/rbac.md#organization-access-mode).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`<scope_type>/<scope_id>`, for example `project/prj_...`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"organization_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Manage the bindings on the organization: the token's own, as returned by the `anyscale_organization` data source. " + scopeDescription,
				PlanModifiers:       replace,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"cloud_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Manage the bindings on this cloud. " + scopeDescription,
				PlanModifiers:       replace,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Manage the bindings on this project. " + scopeDescription,
				PlanModifiers:       replace,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"roles": schema.MapNestedAttribute{
				Required: true,
				MarkdownDescription: "Every direct binding on the scope, keyed by role ID (`rol_...`; look one up with the `anyscale_iam_role` data source). " +
					"`{}` revokes every binding the API allows. The API refuses to revoke the provider identity's own binding, so name it to keep it.",
				Validators: []validator.Map{mapvalidator.KeysAre(stringvalidator.LengthAtLeast(1))},
				NestedObject: schema.NestedAttributeObject{
					Validators: []validator.Object{policyRoleEntryValidator{}},
					Attributes: map[string]schema.Attribute{
						"user_ids": idSet("Users (`usr_...`) that hold the role. Set at least one of `user_ids` or `user_group_ids`. " +
							"The provider identity can keep a role it holds but cannot grant itself one."),
						"user_group_ids": idSet("User groups (`ug_...`) that hold the role. Set at least one of `user_ids` or `user_group_ids`."),
					},
				},
			},
		},
	}
}

func (r *IAMRoleBindingResource) ConfigValidators(ctx context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(
			path.MatchRoot("organization_id"),
			path.MatchRoot("cloud_id"),
			path.MatchRoot("project_id"),
		),
	}
}

func (r *IAMRoleBindingResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if client, ok := clientFromProviderData(req.ProviderData, "Unexpected Resource Configure Type", &resp.Diagnostics); ok {
		r.client = client
	}
}

// policyRolesKnown reports whether every role key and principal ID is known,
// checking each value: a plan with any Computed attribute is never wholly
// known, so the raw plan's IsFullyKnown cannot answer this.
func policyRolesKnown(roles types.Map) bool {
	if roles.IsUnknown() {
		return false
	}
	for _, v := range roles.Elements() {
		obj, ok := v.(types.Object)
		if !ok || obj.IsUnknown() {
			return false
		}
		for _, a := range obj.Attributes() {
			set, ok := a.(types.Set)
			if !ok || set.IsUnknown() {
				return false
			}
			for _, e := range set.Elements() {
				if e.IsUnknown() {
					return false
				}
			}
		}
	}
	return true
}

func (r *IAMRoleBindingResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if refuseRBACPlanOnAzure(r.client, iamRoleBindingTypeName, req, resp) || r.client == nil {
		return
	}
	if req.Plan.Raw.IsNull() {
		r.planDestroy(ctx, req, resp)
		return
	}
	if !req.State.Raw.IsNull() && req.Plan.Raw.Equal(req.State.Raw) {
		return
	}

	var plan RoleBindingPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	scopeType, scopeID := plan.scope()
	creating := req.State.Raw.IsNull()
	if !creating {
		var state RoleBindingPolicyModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		stateType, stateID := state.scope()
		creating = stateType != scopeType || !stateID.Equal(scopeID)
	}

	if scopeID.IsUnknown() {
		if creating {
			resp.Diagnostics.AddWarning("Role Binding Not Checked at Plan",
				fmt.Sprintf("The %s ID is not known until apply, so this plan cannot check the policy or list the bindings the first apply revokes. "+
					"Anyscale grants the identity that creates a project or cloud its owner role on it; name that binding in roles, or the first apply fails with HTTP 403.",
					scopeType))
		}
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), roleBindingPolicyID(scopeType, scopeID.ValueString()))...)
	if !policyRolesKnown(plan.Roles) {
		return
	}

	grants, diags := policyGrantsFromModel(ctx, plan.Roles)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	policy, index := grants.wire()
	result, err := applyRoleBindingPolicy(ctx, r.client, scopeType, scopeID.ValueString(), policy, true)
	if err != nil {
		addPolicyWriteError(ctx, r.client, &resp.Diagnostics, "check role binding policy", index, err, "")
		return
	}
	if !creating {
		// The plan diff already shows the revokes the config asked for, which
		// are the policy's own grants. A grant made outside it is likely
		// someone else's, such as an anyscale_iam_role_grant fighting this
		// policy: refresh pulls it into state, so it plans as a revoke.
		if outside := grantedOutsidePolicy(result.Removed); len(outside) > 0 {
			resp.Diagnostics.AddWarning("Policy Revokes Role Bindings Granted Outside It",
				fmt.Sprintf("This apply revokes these direct bindings on %s %s, which were made outside this policy:\n%s\n\n%s",
					scopeType, scopeID.ValueString(), describePolicyBindings(outside), policyMixHint))
		}
		return
	}
	if len(result.Removed) > 0 {
		resp.Diagnostics.AddWarning("Policy Revokes Existing Role Bindings",
			fmt.Sprintf("Creating this policy takes over every direct binding on %s %s, so the first apply revokes these, which the roles map does not name:\n%s\n\n%s",
				scopeType, scopeID.ValueString(), describePolicyBindings(result.Removed), policyMixHint))
	}
}

// planDestroy dry-runs the policy destroy applies, so a refusal fails the plan
// and the plan lists what destroy revokes. A scope that cannot be read is left
// to Delete, which tells a deleted scope from lost access.
func (r *IAMRoleBindingResource) planDestroy(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	var state RoleBindingPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	scopeType, scopeIDValue := state.scope()
	scopeID := scopeIDValue.ValueString()
	current, err := generateRoleBindingPolicy(ctx, r.client, scopeType, scopeID)
	if err != nil {
		tflog.Debug(ctx, "Could not read the policy to check destroy; Delete handles it", map[string]any{"error": err.Error()})
		return
	}
	// A frozen refusal here means the binding is kept, as in Delete, not that
	// the destroy is refused.
	kept, frozen, result, ok := r.applyDestroyPolicy(ctx, &resp.Diagnostics, scopeType, scopeID, current, true, "")
	if !ok || (len(result.Removed) == 0 && len(frozen) == 0) {
		return
	}
	revoked := "  (none)"
	if len(result.Removed) > 0 {
		revoked = describePolicyBindings(result.Removed)
	}
	detail := fmt.Sprintf("Destroying this policy revokes these direct bindings on %s %s:\n%s", scopeType, scopeID, revoked)
	if len(kept) > 0 {
		detail += "\n\nIt keeps these, which the API does not let Terraform revoke (the provider identity's own and any frozen binding):\n" + describePolicyBindings(kept)
	}
	if outside := grantedOutsidePolicy(result.Removed); len(outside) > 0 {
		detail += "\n\nThese revoked grants were made outside this policy:\n" + describePolicyBindings(outside) +
			"\nIf any of these is an anyscale_iam_role_grant in any configuration, this scope is managed twice: destroying this anyscale_iam_role_binding revokes that resource's grant until its next apply re-creates it."
	}
	resp.Diagnostics.AddWarning("Destroy Revokes Role Bindings", detail)
}

// applyDestroyPolicy applies (or dry-runs) the policy destroy leaves behind:
// the caller's own direct bindings, which the API refuses to revoke, plus each
// frozen binding a refusal names, retried up to
// roleBindingPolicyMaxFrozenRetries times. It returns every binding kept, the
// frozen ones among them, and the apply result, or false after adding the
// diagnostic.
func (r *IAMRoleBindingResource) applyDestroyPolicy(ctx context.Context, diags *diag.Diagnostics, scopeType, scopeID string, current *roleBindingPolicy, dryRun bool, outcome string) (kept, frozenKept []roleBindingPolicyBinding, _ *roleBindingPolicyApplyResult, _ bool) {
	caller, err := fetchCloudAccessCallerIdentity(ctx, r.client)
	if err != nil {
		diags.AddError("Could Not Identify the Provider Identity",
			fmt.Sprintf("Destroy keeps this identity's own role bindings, which the API refuses to revoke, so it must know who it is; GET /api/v2/userinfo failed: %s. %s",
				extractAPIErrorDetail(err), outcome))
		return nil, nil, nil, false
	}

	desired := policyGrants{}
	keep := func(roleID, principalType, principalID string) {
		desired.add(roleID, principalType, principalID)
		kept = append(kept, roleBindingPolicyBinding{PrincipalType: principalType, PrincipalID: principalID, RoleID: roleID, ResourceType: scopeType, ResourceID: scopeID})
	}
	for _, role := range current.Roles {
		for _, p := range role.Principals {
			if p.PrincipalType == roleBindingPrincipalUser && p.PrincipalID == caller.UserID {
				keep(role.RoleID, p.PrincipalType, p.PrincipalID)
			}
		}
	}

	op := "apply role binding policy for destroy"
	for attempt := 0; ; attempt++ {
		policy, index := desired.wire()
		result, err := applyRoleBindingPolicy(ctx, r.client, scopeType, scopeID, policy, dryRun)
		if err == nil {
			return kept, frozenKept, result, true
		}
		if !isStatusWithSubstring(err, http.StatusConflict, policyFrozenBindingSubstring) {
			addPolicyWriteError(ctx, r.client, diags, op, index, err, outcome)
			return nil, nil, nil, false
		}
		if attempt == roleBindingPolicyMaxFrozenRetries {
			diags.AddError("Too Many Frozen Role Bindings",
				withOutcome(fmt.Sprintf("Destroy kept %d frozen bindings and the API refused again: %s Bindings kept so far:\n%s",
					roleBindingPolicyMaxFrozenRetries, extractAPIErrorDetail(err), describePolicyBindings(kept)), outcome))
			return nil, nil, nil, false
		}
		m := policyRefusedBindingID.FindStringSubmatch(extractAPIErrorDetail(err))
		if m == nil {
			addPolicyWriteError(ctx, r.client, diags, op, index, err, outcome)
			return nil, nil, nil, false
		}
		frozen, getErr := getRoleBindingWithRetry(ctx, r.client, m[1])
		if getErr != nil {
			diags.AddError("Could Not Read Frozen Role Binding",
				withOutcome(fmt.Sprintf("%s Reading %s, to keep it, failed: %s.", extractAPIErrorDetail(err), m[1], extractAPIErrorDetail(getErr)), outcome))
			return nil, nil, nil, false
		}
		if !isManagedRoleBindingPrincipal(frozen.PrincipalType) {
			diags.AddError("Frozen Role Binding Principal Not Supported",
				withOutcome(fmt.Sprintf("%s It is held by a %s, which anyscale_iam_role_binding cannot name.", extractAPIErrorDetail(err), frozen.PrincipalType), outcome))
			return nil, nil, nil, false
		}
		tflog.Info(ctx, "Keeping a frozen role binding on destroy", map[string]any{"role_binding_id": frozen.ID})
		keep(frozen.RoleID, frozen.PrincipalType, frozen.PrincipalID)
		frozenKept = append(frozenKept, kept[len(kept)-1])
	}
}

// writePolicy applies the planned policy and sets plan's roles from the
// generated policy that results. applied reports whether the PUT succeeded;
// ok, whether the read-back did too.
func (r *IAMRoleBindingResource) writePolicy(ctx context.Context, plan *RoleBindingPolicyModel, diags *diag.Diagnostics, op string) (applied, ok bool) {
	scopeType, scopeIDValue := plan.scope()
	scopeID := scopeIDValue.ValueString()
	grants, d := policyGrantsFromModel(ctx, plan.Roles)
	diags.Append(d...)
	if diags.HasError() {
		return false, false
	}
	policy, index := grants.wire()
	result, err := applyRoleBindingPolicy(ctx, r.client, scopeType, scopeID, policy, false)
	if err != nil {
		addPolicyWriteError(ctx, r.client, diags, op, index, err, "")
		return false, false
	}
	plan.ID = types.StringValue(roleBindingPolicyID(scopeType, scopeID))
	tflog.Info(ctx, "Role binding policy applied", map[string]any{"scope": roleBindingPolicyID(scopeType, scopeID), "added": len(result.Added), "removed": len(result.Removed)})

	current, err := generateRoleBindingPolicy(ctx, r.client, scopeType, scopeID)
	if err != nil {
		AddAPIError(diags, "read role binding policy after apply", err)
		return true, false
	}
	roles, readOK := policyRolesValue(current, diags)
	if !readOK {
		return true, false
	}
	plan.Roles = roles
	return true, true
}

// policyRolesValue converts a generated policy into the roles attribute.
func policyRolesValue(current *roleBindingPolicy, diags *diag.Diagnostics) (types.Map, bool) {
	grants, err := policyGrantsFromAPI(current)
	if err != nil {
		diags.AddError("Unsupported Role Binding Principal", err.Error())
		return types.Map{}, false
	}
	roles, d := grants.rolesMapValue()
	diags.Append(d...)
	return roles, !d.HasError()
}

func (r *IAMRoleBindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RoleBindingPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.writeAndSetState(ctx, &plan, &resp.Diagnostics, &resp.State)
}

// writeAndSetState applies the plan and saves state. A refused PUT changed
// nothing, so no state is saved. A PUT that succeeded is saved even when the
// read-back fails, from the plan, so Terraform keeps (on create, as tainted)
// the policy it just enforced instead of forgetting it.
func (r *IAMRoleBindingResource) writeAndSetState(ctx context.Context, plan *RoleBindingPolicyModel, diags *diag.Diagnostics, state *tfsdk.State) {
	if applied, _ := r.writePolicy(ctx, plan, diags, "apply role binding policy"); applied {
		diags.Append(state.Set(ctx, plan)...)
	}
}

func (r *IAMRoleBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan RoleBindingPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.writeAndSetState(ctx, &plan, &resp.Diagnostics, &resp.State)
}

// Outcomes for diagnostics that leave state unchanged.
const (
	policyKeptReadOutcome    = "The policy was kept in Terraform state unchanged."
	policyKeptDestroyOutcome = "Nothing was revoked, and the policy stays in Terraform state; to stop managing it without revoking anything, run `terraform state rm` on its address."
)

// policyNotFoundOutcome settles a 404 from generate: the feature is off, or
// something else, reported as the API error.
func policyNotFound(ctx context.Context, client *Client, diags *diag.Diagnostics, op string, err error, disabledOutcome string) {
	enabled, probeErr := roleBindingsEnabled(ctx, client)
	switch {
	case probeErr != nil:
		addRoleBindingsProbeFailedError(diags, op, err, probeErr)
	case !enabled:
		addRoleBindingsDisabledError(diags, disabledOutcome)
	default:
		AddAPIError(diags, op, err)
	}
}

// scopeGoneAfterForbidden reads the scope after generate answered 403. It
// reports gone=true with the scope's own error when that read is also 403 or
// 404; gone=false with a nil error when the scope still reads, which means the
// caller lost IAM on it. An organization has no read of its own and is never
// gone.
func (r *IAMRoleBindingResource) scopeGoneAfterForbidden(ctx context.Context, scopeType, scopeID string) (gone bool, scopeErr error) {
	readPath := roleBindingPolicyScopeReadPath(scopeType, scopeID)
	if readPath == "" {
		return false, nil
	}
	_, err := DoRequestRaw(ctx, r.client, "GET", readPath, nil, http.StatusOK)
	if err == nil {
		return false, nil
	}
	if isGoneOrForbidden(err) {
		return true, err
	}
	return false, err
}

func addPolicyNotReadableError(diags *diag.Diagnostics, scopeType, scopeID string, err error, outcome string) {
	diags.AddError("Role Binding Not Readable",
		withOutcome(fmt.Sprintf("The Anyscale API refused to read the role bindings on %s %q (HTTP 403): %s The %s still exists, so this identity no longer manages IAM on it.",
			scopeType, scopeID, extractAPIErrorDetail(err), scopeType), outcome))
}

func (r *IAMRoleBindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RoleBindingPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	scopeType, scopeIDValue := state.scope()
	scopeID := scopeIDValue.ValueString()

	current, err := generateRoleBindingPolicy(ctx, r.client, scopeType, scopeID)
	if err != nil {
		switch {
		case isHTTPStatus(err, http.StatusForbidden):
			gone, scopeErr := r.scopeGoneAfterForbidden(ctx, scopeType, scopeID)
			switch {
			case gone:
				addForbiddenTreatedAsDeletedWarning(&resp.Diagnostics, scopeKind(scopeType), scopeID, scopeErr)
				tflog.Warn(ctx, "Role binding policy scope is gone, removing from state", map[string]any{"scope": roleBindingPolicyID(scopeType, scopeID)})
				resp.State.RemoveResource(ctx)
			case scopeErr != nil:
				AddAPIError(&resp.Diagnostics, "read "+scopeType+" after a 403 on its role binding policy", scopeErr)
			default:
				addPolicyNotReadableError(&resp.Diagnostics, scopeType, scopeID, err, policyKeptReadOutcome)
			}
		case isRoleBindingsDisabled(err):
			policyNotFound(ctx, r.client, &resp.Diagnostics, "read role binding policy", err, policyKeptReadOutcome)
		default:
			AddAPIError(&resp.Diagnostics, "read role binding policy", err)
		}
		return
	}

	roles, ok := policyRolesValue(current, &resp.Diagnostics)
	if !ok {
		return
	}
	state.ID = types.StringValue(roleBindingPolicyID(scopeType, scopeID))
	state.Roles = roles
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *IAMRoleBindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RoleBindingPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	scopeType, scopeIDValue := state.scope()
	scopeID := scopeIDValue.ValueString()

	current, err := generateRoleBindingPolicy(ctx, r.client, scopeType, scopeID)
	if err != nil {
		switch {
		case isHTTPStatus(err, http.StatusForbidden):
			gone, scopeErr := r.scopeGoneAfterForbidden(ctx, scopeType, scopeID)
			switch {
			case gone:
				addForbiddenTreatedAsDeletedOnDestroyWarning(&resp.Diagnostics, scopeKind(scopeType), scopeID, scopeErr)
			case scopeErr != nil:
				AddAPIError(&resp.Diagnostics, "read "+scopeType+" after a 403 on its role binding policy", scopeErr)
			default:
				addPolicyNotReadableError(&resp.Diagnostics, scopeType, scopeID, err, policyKeptDestroyOutcome)
			}
		case isRoleBindingsDisabled(err):
			policyNotFound(ctx, r.client, &resp.Diagnostics, "read role binding policy for destroy", err, policyKeptDestroyOutcome)
		default:
			AddAPIError(&resp.Diagnostics, "read role binding policy for destroy", err)
		}
		return
	}

	kept, _, _, ok := r.applyDestroyPolicy(ctx, &resp.Diagnostics, scopeType, scopeID, current, false, policyKeptDestroyOutcome)
	if !ok {
		return
	}
	if len(kept) > 0 {
		resp.Diagnostics.AddWarning("Role Bindings Kept on Destroy",
			fmt.Sprintf("The API does not let Terraform revoke these, so they remain on %s %s:\n%s", scopeType, scopeID, describePolicyBindings(kept)))
	}
}

// roleBindingPolicyImportFormat names the accepted import ID, for diagnostics.
const roleBindingPolicyImportFormat = "Import a role binding policy by `<scope_type>/<scope_id>`, where scope_type is organization, cloud or project, for example `project/prj_abc123`."

func (r *IAMRoleBindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if refuseRBACOnAzure(r.client, iamRoleBindingTypeName, &resp.Diagnostics) {
		return
	}
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || parts[1] == "" || !isManagedRoleBindingScope(parts[0]) {
		AddConfigError(&resp.Diagnostics, "Invalid Role Binding Import ID",
			fmt.Sprintf("%q is not a valid import ID. %s", req.ID, roleBindingPolicyImportFormat))
		return
	}
	state := RoleBindingPolicyModel{
		ID:             types.StringValue(req.ID),
		OrganizationID: types.StringNull(),
		CloudID:        types.StringNull(),
		ProjectID:      types.StringNull(),
		Roles:          types.MapNull(policyRoleObjectType),
	}
	scopeID := types.StringValue(parts[1])
	switch parts[0] {
	case roleBindingResourceOrganization:
		state.OrganizationID = scopeID
	case roleBindingResourceCloud:
		state.CloudID = scopeID
	case roleBindingResourceProject:
		state.ProjectID = scopeID
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
