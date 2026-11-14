package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Role-binding policy: the full set of direct role bindings on one scope, as
// GET .../policy/generate returns it and PUT .../policy applies it. Both sides
// compare bindings by what they grant, never by ID, and ignore order.

type roleBindingPolicyPrincipal struct {
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
}

type roleBindingPolicyRole struct {
	RoleID     string                       `json:"role_id"`
	Principals []roleBindingPolicyPrincipal `json:"principals"`
}

type roleBindingPolicy struct {
	Roles []roleBindingPolicyRole `json:"roles"`
}

type roleBindingPolicyResponse struct {
	Result roleBindingPolicy `json:"result"`
}

// roleBindingPolicyBinding is a binding an apply added or removed. A dry run's
// added entries carry no ID.
type roleBindingPolicyBinding struct {
	ID            *string `json:"id"`
	PrincipalType string  `json:"principal_type"`
	PrincipalID   string  `json:"principal_id"`
	RoleID        string  `json:"role_id"`
	ResourceType  string  `json:"resource_type"`
	ResourceID    string  `json:"resource_id"`
	// Origin is the path that made the grant: "declarative" for a policy
	// apply, "imperative" for a direct grant (anyscale_iam_role_grant, the
	// console, a CLI grant). Absent on a dry run's added entries and on older
	// bindings.
	Origin *string `json:"origin"`
}

// roleBindingOriginDeclarative is the origin a policy apply gives the grants
// it makes.
const roleBindingOriginDeclarative = "declarative"

// grantedOutsidePolicy returns the bindings a policy apply did not make. An
// absent origin counts as outside: nothing shows a policy made it.
func grantedOutsidePolicy(bs []roleBindingPolicyBinding) []roleBindingPolicyBinding {
	var out []roleBindingPolicyBinding
	for _, b := range bs {
		if b.Origin == nil || *b.Origin != roleBindingOriginDeclarative {
			out = append(out, b)
		}
	}
	return out
}

// policyMixHint closes a warning listing grants a policy revokes but may not
// have made. Neither resource can see the other's config, so it names the
// hazard rather than detecting it.
const policyMixHint = "If any of these is an anyscale_iam_role_grant in any configuration, this scope is managed twice: " +
	"remove one of the two before applying, or each apply revokes what the other grants."

type roleBindingPolicyApplyResult struct {
	Added   []roleBindingPolicyBinding `json:"added"`
	Removed []roleBindingPolicyBinding `json:"removed"`
}

type roleBindingPolicyApplyResponse struct {
	Result roleBindingPolicyApplyResult `json:"result"`
}

// Refusal detail substrings of PUT .../policy. Full texts:
//   - 403 "This policy would revoke your own role binding 'rb_..'. Name it in the policy to keep it." (measured)
//   - 409 "This policy would revoke the frozen role binding 'rb_..'. Name it in the policy to keep it." (traced)
//   - 403 "You do not manage IAM on <type> '<id>'." for a scope that is deleted,
//     does not exist, or that the caller does not manage IAM on (measured)
const (
	policyOwnBindingSubstring    = "would revoke your own role binding"
	policyFrozenBindingSubstring = "would revoke the frozen role binding"
	policyManageIAMSubstring     = "You do not manage IAM on"
)

// policyRefusedBindingID extracts the rb_ ID a revoke refusal names.
var policyRefusedBindingID = regexp.MustCompile(`'(rb_[^']+)'`)

// policyElementIndex extracts the roles[i].principals[j] prefix of a per-element
// refusal.
var policyElementIndex = regexp.MustCompile(`roles\[(\d+)\]\.principals\[(\d+)\]`)

// roleBindingPolicyScopePath is .../role_bindings/{type}/{id}/policy.
func roleBindingPolicyScopePath(scopeType, scopeID string) string {
	return fmt.Sprintf("%s/%s/%s/policy", roleBindingsBasePath, url.PathEscape(scopeType), url.PathEscape(scopeID))
}

func generateRoleBindingPolicy(ctx context.Context, client *Client, scopeType, scopeID string) (*roleBindingPolicy, error) {
	resp, err := DoRequestAndParse[roleBindingPolicyResponse](ctx, client, "GET", roleBindingPolicyScopePath(scopeType, scopeID)+"/generate", nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

func applyRoleBindingPolicy(ctx context.Context, client *Client, scopeType, scopeID string, policy roleBindingPolicy, dryRun bool) (*roleBindingPolicyApplyResult, error) {
	if policy.Roles == nil {
		// An empty list is what revokes everything; null is rejected.
		policy.Roles = []roleBindingPolicyRole{}
	}
	body, err := MarshalRequestBody(policy)
	if err != nil {
		return nil, err
	}
	path := roleBindingPolicyScopePath(scopeType, scopeID)
	if dryRun {
		path += "?dry_run=true"
	}
	resp, err := DoRequestAndParse[roleBindingPolicyApplyResponse](ctx, client, "PUT", path, body, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

// roleBindingPolicyScopeReadPath is the scope's own GET, used to tell a deleted
// scope from lost IAM after a 403. An organization has none: it is the token's
// own and cannot be deleted from under it, so "" means a 403 on it is always
// lost IAM.
func roleBindingPolicyScopeReadPath(scopeType, scopeID string) string {
	switch scopeType {
	case roleBindingResourceProject:
		return "/api/v2/projects/" + url.PathEscape(scopeID)
	case roleBindingResourceCloud:
		return "/api/v2/clouds/" + url.PathEscape(scopeID)
	}
	return ""
}

// policyRoleModel is one entry of roles.
type policyRoleModel struct {
	UserIDs      types.Set `tfsdk:"user_ids"`
	UserGroupIDs types.Set `tfsdk:"user_group_ids"`
}

var policyRoleAttrTypes = map[string]attr.Type{
	"user_ids":       types.SetType{ElemType: types.StringType},
	"user_group_ids": types.SetType{ElemType: types.StringType},
}

var policyRoleObjectType = types.ObjectType{AttrTypes: policyRoleAttrTypes}

// policyElement names one principal under one role, in wire order, so a
// roles[i].principals[j] refusal can be reported by name.
type policyElement struct {
	RoleID        string
	PrincipalType string
	PrincipalID   string
}

// policyGrants is a policy as a sorted list of grants per role.
type policyGrants map[string]struct{ Users, Groups []string }

// wire returns the policy body in a fixed order (roles by ID; users, then
// groups, each by ID) and the element each roles[i].principals[j] names.
func (g policyGrants) wire() (roleBindingPolicy, [][]policyElement) {
	roleIDs := make([]string, 0, len(g))
	for id := range g {
		roleIDs = append(roleIDs, id)
	}
	sort.Strings(roleIDs)
	policy := roleBindingPolicy{Roles: []roleBindingPolicyRole{}}
	var index [][]policyElement
	for _, roleID := range roleIDs {
		entry := g[roleID]
		users := append([]string(nil), entry.Users...)
		groups := append([]string(nil), entry.Groups...)
		sort.Strings(users)
		sort.Strings(groups)
		role := roleBindingPolicyRole{RoleID: roleID, Principals: []roleBindingPolicyPrincipal{}}
		var elems []policyElement
		for _, u := range users {
			role.Principals = append(role.Principals, roleBindingPolicyPrincipal{PrincipalType: roleBindingPrincipalUser, PrincipalID: u})
			elems = append(elems, policyElement{roleID, roleBindingPrincipalUser, u})
		}
		for _, gid := range groups {
			role.Principals = append(role.Principals, roleBindingPolicyPrincipal{PrincipalType: roleBindingPrincipalUserGroup, PrincipalID: gid})
			elems = append(elems, policyElement{roleID, roleBindingPrincipalUserGroup, gid})
		}
		policy.Roles = append(policy.Roles, role)
		index = append(index, elems)
	}
	return policy, index
}

// add grants roleID to one principal, ignoring a grant already present.
func (g policyGrants) add(roleID, principalType, principalID string) {
	entry := g[roleID]
	list := &entry.Groups
	if principalType == roleBindingPrincipalUser {
		list = &entry.Users
	}
	for _, id := range *list {
		if id == principalID {
			g[roleID] = entry
			return
		}
	}
	*list = append(*list, principalID)
	g[roleID] = entry
}

// policyGrantsFromModel converts a known roles map. Callers check every value
// is known first.
func policyGrantsFromModel(ctx context.Context, roles types.Map) (policyGrants, diag.Diagnostics) {
	var diags diag.Diagnostics
	g := policyGrants{}
	var entries map[string]policyRoleModel
	diags.Append(roles.ElementsAs(ctx, &entries, false)...)
	if diags.HasError() {
		return nil, diags
	}
	for roleID, entry := range entries {
		var users, groups []string
		if !entry.UserIDs.IsNull() {
			diags.Append(entry.UserIDs.ElementsAs(ctx, &users, false)...)
		}
		if !entry.UserGroupIDs.IsNull() {
			diags.Append(entry.UserGroupIDs.ElementsAs(ctx, &groups, false)...)
		}
		g[roleID] = struct{ Users, Groups []string }{users, groups}
	}
	return g, diags
}

// policyGrantsFromAPI converts a generated policy. A principal type the
// schema cannot hold is an error, never a silent drop: dropping it would hide
// a grant the next apply revokes.
func policyGrantsFromAPI(policy *roleBindingPolicy) (policyGrants, error) {
	g := policyGrants{}
	for _, role := range policy.Roles {
		for _, p := range role.Principals {
			if !isManagedRoleBindingPrincipal(p.PrincipalType) {
				return nil, fmt.Errorf("role %q is held by %s %q, a principal type anyscale_iam_role_binding cannot represent (it manages user and user_group)",
					role.RoleID, p.PrincipalType, p.PrincipalID)
			}
			g.add(role.RoleID, p.PrincipalType, p.PrincipalID)
		}
	}
	return g, nil
}

// rolesMapValue builds the roles attribute. A role with no principals cannot
// be held, so none appears; an absent principal kind is null, never an empty
// set, matching the schema's SizeAtLeast(1).
func (g policyGrants) rolesMapValue() (types.Map, diag.Diagnostics) {
	var diags diag.Diagnostics
	elems := map[string]attr.Value{}
	for roleID, entry := range g {
		if len(entry.Users) == 0 && len(entry.Groups) == 0 {
			continue
		}
		users, d := stringSetOrNull(entry.Users)
		diags.Append(d...)
		groups, d := stringSetOrNull(entry.Groups)
		diags.Append(d...)
		obj, d := types.ObjectValue(policyRoleAttrTypes, map[string]attr.Value{"user_ids": users, "user_group_ids": groups})
		diags.Append(d...)
		elems[roleID] = obj
	}
	m, d := types.MapValue(policyRoleObjectType, elems)
	diags.Append(d...)
	return m, diags
}

func stringSetOrNull(values []string) (types.Set, diag.Diagnostics) {
	if len(values) == 0 {
		return types.SetNull(types.StringType), nil
	}
	return types.SetValueFrom(context.Background(), types.StringType, values)
}

// describePolicyBinding names a grant for a diagnostic.
func describePolicyBinding(b roleBindingPolicyBinding) string {
	return fmt.Sprintf("role %s to %s %s", b.RoleID, b.PrincipalType, b.PrincipalID)
}

func describePolicyBindings(bs []roleBindingPolicyBinding) string {
	lines := make([]string, 0, len(bs))
	for _, b := range bs {
		lines = append(lines, "  - "+describePolicyBinding(b))
	}
	return strings.Join(lines, "\n")
}

// policyEntryHint is the roles entry that keeps a grant, as HCL.
func policyEntryHint(roleID, principalType, principalID string) string {
	attrName := "user_group_ids"
	if principalType == roleBindingPrincipalUser {
		attrName = "user_ids"
	}
	return fmt.Sprintf("  %q = {\n    %s = [%q]\n  }", roleID, attrName, principalID)
}

// namePolicyElement appends, to a roles[i].principals[j] refusal, the role
// and principal that element is, since the body order is the provider's own.
func namePolicyElement(detail string, index [][]policyElement) string {
	m := policyElementIndex.FindStringSubmatch(detail)
	if m == nil {
		return detail
	}
	i, _ := strconv.Atoi(m[1])
	j, _ := strconv.Atoi(m[2])
	if i >= len(index) || j >= len(index[i]) {
		return detail
	}
	e := index[i][j]
	return fmt.Sprintf("%s (role %q, %s %q)", detail, e.RoleID, e.PrincipalType, e.PrincipalID)
}

// addPolicyWriteError reports a refused PUT .../policy, dry run or not. op names
// the call; outcome, when set, says what the refusal means for state.
func addPolicyWriteError(ctx context.Context, client *Client, diags *diag.Diagnostics, op string, index [][]policyElement, err error, outcome string) {
	detail := extractAPIErrorDetail(err)
	switch {
	case isStatusWithSubstring(err, http.StatusForbidden, policyOwnBindingSubstring):
		diags.AddError("Policy Would Revoke Your Own Role Binding",
			withOutcome(detail+keepBindingHint(ctx, client, detail), outcome))
	case isStatusWithSubstring(err, http.StatusConflict, policyFrozenBindingSubstring):
		diags.AddError("Policy Would Revoke a Frozen Role Binding",
			withOutcome(detail+keepBindingHint(ctx, client, detail), outcome))
	case isStatusWithSubstring(err, http.StatusForbidden, roleBindingSelfGrantSubstring):
		diags.AddError("Cannot Grant a Role to the Provider's Own Identity",
			withOutcome(namePolicyElement(detail, index)+" Grant it to a user group this identity belongs to, or apply with a different identity.", outcome))
	case isStatusWithSubstring(err, http.StatusForbidden, policyManageIAMSubstring):
		diags.AddError("Cannot Manage IAM on Scope",
			withOutcome(detail+" The scope does not exist, or this identity does not manage IAM on it.", outcome))
	case isRoleBindingAdminGroupRemovalRefusal(err):
		addRoleBindingAdminGroupPolicyRemovalError(diags, err, outcome)
	case isRoleBindingAdminGroupExtraRoleRefusal(err):
		addRoleBindingAdminGroupExtraRoleError(diags, err, "policy", outcome)
	case isOrgNotConvertedConflict(err):
		addOrgNotConvertedError(diags, err, outcome)
	case isRoleBindingsDisabled(err):
		// PUT also answers 404 for an unknown role or group, so ask whether
		// the feature is on before calling it off.
		enabled, probeErr := roleBindingsEnabled(ctx, client)
		switch {
		case probeErr != nil:
			addRoleBindingsProbeFailedError(diags, op, err, probeErr)
		case !enabled:
			addRoleBindingsDisabledError(diags, outcome)
		default:
			diags.AddError("Role or User Group Not Found", withOutcome(namePolicyElement(detail, index), outcome))
		}
	default:
		AddAPIError(diags, op, err)
	}
}

// withOutcome appends outcome, when set, to a diagnostic detail.
func withOutcome(detail, outcome string) string {
	if outcome == "" {
		return detail
	}
	return detail + " " + outcome
}

// keepBindingHint looks up the binding a revoke refusal names and returns the
// roles entry that keeps it. A failed lookup returns "": the refusal's own
// text still names the binding.
func keepBindingHint(ctx context.Context, client *Client, detail string) string {
	m := policyRefusedBindingID.FindStringSubmatch(detail)
	if m == nil {
		return ""
	}
	b, err := getRoleBindingWithRetry(ctx, client, m[1])
	if err != nil {
		return ""
	}
	return fmt.Sprintf(" Binding %s grants role %s to %s %s. To keep it, add to roles:\n\n%s",
		b.ID, b.RoleID, b.PrincipalType, b.PrincipalID, policyEntryHint(b.RoleID, b.PrincipalType, b.PrincipalID))
}
