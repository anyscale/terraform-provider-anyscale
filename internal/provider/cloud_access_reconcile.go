package provider

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// The cloud-scope reconcile for anyscale_cloud_access: it makes a cloud's
// collaborator list match the configuration, adding and updating members and
// revoking anyone undeclared.
//
// PROJECT ROLES ARE NOT WRITTEN HERE. The reconcile is deliberately split at
// cloud scope, because the project half depends on two decisions that the cloud
// half does not - whether dropping a project entry revokes that role, and
// whether project roles are refreshed at all. Building the cloud half first
// closes cloud-scope access management without pre-empting either.
//
// THREE PROPERTIES ARE LOAD-BEARING, in the sense that changing any of them
// changes who ends up with access:
//
//  1. Grants run BEFORE revokes. A configuration that replaces one owner with
//     another must never pass through a moment where the cloud has no owner, and
//     an apply that dies partway through should leave too much access rather
//     than too little - too much is visible in the next plan, while too little
//     is an outage for a real person.
//  2. A failed revoke does not fail the apply. It is recorded in
//     unmanaged_grants and the reconcile continues, because one unrevokable
//     member would otherwise block the entire set forever. The attribute is the
//     alarm; see the schema's note on alerting on its length.
//  3. The calling identity is never revoked. The token running Terraform is
//     usually a collaborator on the cloud it manages and frequently its owner,
//     so without this the first apply can lock the operator out of the thing
//     they are managing.

// cloudAccessBridgePermissionLevel is the legacy permission_level used to
// establish membership before a role can be written.
//
// Deliberately the most restrictive real legacy value: if the role write that
// follows it fails and this is what is left behind, the blast radius of an
// unintended grant is as small as the legacy vocabulary allows.
const cloudAccessBridgePermissionLevel = "readonly"

// cloudAccessAlreadyHasPermissionsSubstring is the detail substring of the
// bootstrap POST's 409 when the target already has a permissions row for the
// cloud. Treated as an expected, non-fatal outcome rather than a failure - the
// bootstrap runs unconditionally and this is what "they were already a member"
// looks like on the wire.
//
// Because the bootstrap 409s in exactly that case, it cannot downgrade an
// existing member to the bridge permission level: the write does not happen at
// all. That is what makes running it unconditionally safe as well as necessary.
const cloudAccessAlreadyHasPermissionsSubstring = "already has permissions for this cloud"

// cloudAccessAutoAddUserSubstring is the detail substring the backend returns
// when a cloud's auto_add_user setting blocks removing a collaborator. The full
// detail is "Users cannot be removed from clouds which have auto add users
// enabled." and the status is 409.
//
// LIVE-CONFIRMED against a real cloud, byte-for-byte identical to the backend
// source trace it was first taken from. The substring is
// the stable part of that sentence; matching the whole thing would break on a
// reworded message, and matching less would risk a false positive.
const cloudAccessAutoAddUserSubstring = "auto add users enabled"

// cloudAccessPolicyAPISubstring identifies the OTHER structural revoke blocker:
// an organization with directory sync enabled AND at least one Policy API role
// binding refuses to remove any non-service-account member. Unlike
// auto_add_user this is organization-wide rather than per-cloud, there is no
// endpoint to preflight it against, and it returns the SAME 409 status - so the
// two are only distinguishable by their detail text.
//
// anyscale_organization_invitation documents the identical condition on the
// invite path; SCIM alone does not trigger it.
const cloudAccessPolicyAPISubstring = "Policy API"

// cloudAccessDesiredMember is one member as the configuration declares them.
type cloudAccessDesiredMember struct {
	// Email as written in the configuration. Used for diagnostics and for the
	// bootstrap call, which takes an email rather than an id.
	Email     string
	BaseRole  string
	DenyRoles []string
	// Projects maps project ID to the role declared on it. Empty when the
	// configuration declares none, which under this resource's scope means "no
	// project roles are managed for this member", not "this member has none".
	Projects map[string]string
}

// cloudAccessDesiredMembers reads the plan's member map into a map keyed by
// FOLDED email, so it can be compared against the API's own spelling.
//
// deny_roles is sent to the API as [] when the configuration omits it. The roles
// write is a SET, so an omitted attribute means "this member has no
// restrictions", which is the contract the schema documents at cloud scope. This
// is deliberately not the organization-scope contract, where omitting the
// attribute leaves the existing value alone.
func cloudAccessDesiredMembers(ctx context.Context, member types.Map) (map[string]cloudAccessDesiredMember, diag.Diagnostics) {
	var diags diag.Diagnostics
	out := make(map[string]cloudAccessDesiredMember)
	if member.IsNull() || member.IsUnknown() {
		return out, diags
	}

	entries := make(map[string]CloudAccessMemberModel, len(member.Elements()))
	diags.Append(member.ElementsAs(ctx, &entries, false)...)
	if diags.HasError() {
		return out, diags
	}

	for email, m := range entries {
		denyRoles := []string{}
		if !m.DenyRoles.IsNull() && !m.DenyRoles.IsUnknown() {
			var declared []string
			diags.Append(m.DenyRoles.ElementsAs(ctx, &declared, false)...)
			if declared != nil {
				denyRoles = declared
			}
		}
		out[cloudAccessFoldEmail(email)] = cloudAccessDesiredMember{
			Email:     email,
			BaseRole:  m.BaseRole.ValueString(),
			DenyRoles: denyRoles,
		}
	}
	return out, diags
}

// cloudAccessSortedMemberKeys returns desired's keys in a stable order.
//
// Go map iteration order is random, and grants are applied in this order, so
// without it two identical applies would write the same members in different
// orders. That matters for reading a failure: when a grant fails partway
// through, a deterministic order makes which members were already written a
// property of the configuration rather than of chance.
func cloudAccessSortedMemberKeys(desired map[string]cloudAccessDesiredMember) []string {
	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// cloudAccessFoldEmail is the single definition of "the same person" for this
// resource. Anyscale treats email identity as case-insensitive, so every
// comparison between a configured email and an API-reported one goes through
// here rather than comparing raw strings.
func cloudAccessFoldEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// cloudAccessCallerIdentity is who the provider's token authenticates as.
type cloudAccessCallerIdentity struct {
	UserID string
	Email  string
}

// matches reports whether a member is the caller. Both the user id and the
// folded email are checked because either alone can be absent: the search
// endpoint's user_id is not guaranteed populated for every identity type, and a
// directory-managed address can be reported in a different case than the token
// owner's own record.
func (c cloudAccessCallerIdentity) matches(m cloudAccessRemoteMember) bool {
	if c.UserID != "" && m.UserID == c.UserID {
		return true
	}
	return c.Email != "" && cloudAccessFoldEmail(c.Email) == cloudAccessFoldEmail(m.Email)
}

// fetchCloudAccessCallerIdentity resolves the calling token's own identity.
//
// A failure here is fatal to the reconcile rather than tolerated, and that is
// the whole point: if the caller cannot be identified, the revoke pass cannot
// know to skip them, and the failure mode of guessing is locking the operator
// out of the cloud they are managing. A guard that cannot answer must not fail
// open.
func fetchCloudAccessCallerIdentity(ctx context.Context, client *Client) (cloudAccessCallerIdentity, error) {
	userResp, err := DoRequestAndParse[userInfoResponse](ctx, client, "GET", "/api/v2/userinfo", nil, http.StatusOK)
	if err != nil {
		return cloudAccessCallerIdentity{}, err
	}
	if userResp == nil {
		return cloudAccessCallerIdentity{}, fmt.Errorf("userinfo returned no result for the authenticated token")
	}
	return cloudAccessCallerIdentity{UserID: userResp.Result.ID, Email: userResp.Result.Email}, nil
}

// cloudAccessReconcileResult is what a reconcile pass produced.
type cloudAccessReconcileResult struct {
	// Unmanaged is one entry per grant that could not be revoked, in the order
	// the revokes were attempted.
	Unmanaged []CloudAccessUnmanagedGrantModel
}

// unmanagedGrantsList converts the result into the Computed attribute's value.
// Always a list, never null and never unknown - a Computed list-of-objects left
// unknown has crashed this provider before.
func (res cloudAccessReconcileResult) unmanagedGrantsList() (types.List, diag.Diagnostics) {
	elements := make([]attr.Value, 0, len(res.Unmanaged))
	var diags diag.Diagnostics
	for _, g := range res.Unmanaged {
		obj, objDiags := types.ObjectValue(
			map[string]attr.Type{
				"email":      types.StringType,
				"project_id": types.StringType,
				"reason":     types.StringType,
			},
			map[string]attr.Value{
				"email":      g.Email,
				"project_id": g.ProjectID,
				"reason":     g.Reason,
			},
		)
		diags.Append(objDiags...)
		if diags.HasError() {
			return types.ListNull(cloudAccessUnmanagedGrantType()), diags
		}
		elements = append(elements, obj)
	}
	list, listDiags := types.ListValue(cloudAccessUnmanagedGrantType(), elements)
	diags.Append(listDiags...)
	return list, diags
}

// cloudAccessReconcileOptions makes the two decisions that differ between an
// apply and a destroy explicit at each call site, rather than implicit in which
// function did the calling.
type cloudAccessReconcileOptions struct {
	// RevokeUndeclared removes members the configuration does not declare.
	RevokeUndeclared bool

	// RefuseWhenAutoAddUserEnabled aborts before any write when the cloud
	// auto-enrols every organization member, because Terraform cannot be
	// authoritative over such a cloud's member list at all.
	//
	// TRUE for an apply and FALSE for a destroy, and the asymmetry is deliberate.
	// Refusing a destroy would strand the resource in state with no exit but
	// 'terraform state rm', which is a worse position than a destroy that tries,
	// fails on each member and says exactly what to do about it. An apply has
	// somewhere to go back to; a destroy does not.
	RefuseWhenAutoAddUserEnabled bool

	// PriorProjects is what state previously declared, keyed by folded email then
	// project ID. It is the ONLY source of project revokes: a pair that was
	// declared and is now dropped gets removed, while a role merely observed on an
	// in-scope project is left alone. Nil on a create, which has no prior
	// declarations and therefore no project revokes.
	PriorProjects map[string]map[string]string
}

// reconcileCloudAccess makes cloudID's collaborator list match desired.
func reconcileCloudAccess(
	ctx context.Context,
	client *Client,
	cloudID string,
	desired map[string]cloudAccessDesiredMember,
	opts cloudAccessReconcileOptions,
) (cloudAccessReconcileResult, diag.Diagnostics) {
	var diags diag.Diagnostics
	var result cloudAccessReconcileResult

	caller, err := fetchCloudAccessCallerIdentity(ctx, client)
	if err != nil {
		diags.AddError(
			"Could Not Identify The Calling Identity",
			fmt.Sprintf("This resource must know which identity Terraform is authenticated as before it changes a "+
				"cloud's member list, so that it never revokes your own access to the cloud you are managing. Reading "+
				"/api/v2/userinfo failed: %s\n\nNo changes were made.", extractAPIErrorDetail(err)),
		)
		return result, diags
	}

	remote, err := listCloudAccessMembers(ctx, client, cloudID)
	if err != nil {
		diags.AddError(
			"Could Not Read The Cloud's Current Members",
			fmt.Sprintf("The current member list of cloud %s could not be read, so this apply cannot tell who needs to "+
				"be added or removed: %s\n\nNo changes were made.", cloudID, extractAPIErrorDetail(err)),
		)
		return result, diags
	}

	// Work out the revoke set BEFORE touching anything, so the auto_add_user
	// preflight below can be skipped entirely when there is nothing to revoke.
	var toRevoke []cloudAccessRemoteMember
	if opts.RevokeUndeclared {
		for _, m := range remote {
			if _, declared := desired[cloudAccessFoldEmail(m.Email)]; declared {
				continue
			}
			if caller.matches(m) {
				tflog.Info(ctx, "Skipping revoke of the calling identity", map[string]interface{}{"cloud_id": cloudID})
				continue
			}
			toRevoke = append(toRevoke, m)
		}
	}

	// The auto_add_user preflight runs on EVERY apply that would revoke, and is
	// never cached from an earlier one: anyscale_cloud in the same configuration
	// can flip the flag, so a check made at create time can be invalidated by a
	// sibling resource before the next apply. It is also not in ModifyPlan, which
	// must stay cheap and side-effect-free.
	if len(toRevoke) > 0 && opts.RefuseWhenAutoAddUserEnabled {
		enabled, err := cloudAutoAddUserEnabled(ctx, client, cloudID)
		if err != nil {
			diags.AddError(
				"Could Not Check This Cloud's auto_add_user Setting",
				fmt.Sprintf("While auto_add_user is enabled, Anyscale refuses to remove a cloud's collaborators, so this "+
					"resource checks the setting before revoking anyone. That check failed for cloud %s: %s\n\nNo changes "+
					"were made. This is deliberately not treated as 'probably fine' - proceeding would attempt %d revoke(s) "+
					"that may all fail partway through.", cloudID, extractAPIErrorDetail(err), len(toRevoke)),
			)
			return result, diags
		}
		if enabled {
			diags.AddError(
				"Cloud Has auto_add_user Enabled",
				fmt.Sprintf("Cloud %s has auto_add_user enabled, and this apply would revoke %d member(s) that the "+
					"configuration does not declare.\n\nWhile that setting is on, Anyscale automatically grants every "+
					"organization member access to this cloud and refuses to remove a collaborator, so Terraform cannot be "+
					"authoritative over the member list at all - this is not a degraded mode, it is impossible. Set "+
					"auto_add_user = false on the cloud (anyscale_cloud exposes it) before managing its members here.\n\n"+
					"No changes were made.", cloudID, len(toRevoke)),
			)
			return result, diags
		}
	}

	// GRANTS FIRST. See property 1 on this file: an apply that dies partway
	// through should leave too much access rather than too little.
	for _, folded := range cloudAccessSortedMemberKeys(desired) {
		m := desired[folded]
		if grantDiags := grantCloudAccessMember(ctx, client, cloudID, m); grantDiags.HasError() {
			diags.Append(grantDiags...)
			// A failed grant IS fatal, unlike a failed revoke: the configuration
			// asked for this access to exist and it does not, and continuing would
			// then revoke other members on the strength of a member list this apply
			// has already failed to establish.
			return result, diags
		}
	}

	// PROJECT PASS, between the cloud grants and the cloud revokes. Before,
	// because the backend refuses a project grant to someone with no role on the
	// parent cloud. Before the cloud revokes, because those cascade: removing a
	// cloud collaborator recursively revokes their project permissions, so a member
	// being dropped needs no per-project revokes and attempting them would produce
	// 404s this resource would have to report as failures.
	cascaded := make(map[string]bool, len(toRevoke))
	for _, m := range toRevoke {
		cascaded[cloudAccessFoldEmail(m.Email)] = true
	}

	projectUnmanaged, projectDiags := reconcileProjectRoles(ctx, client, cloudID, desired, opts.PriorProjects, cascaded)
	result.Unmanaged = append(result.Unmanaged, projectUnmanaged...)
	diags.Append(projectDiags...)
	if projectDiags.HasError() {
		// Same rule as a failed cloud grant: the configuration asked for access that
		// does not exist, so the revoke pass must not run on a member list this apply
		// has already failed to establish.
		return result, diags
	}

	for _, m := range toRevoke {
		if err := revokeCloudAccessMember(ctx, client, cloudID, m.IdentityID); err != nil {
			// Recorded, not fatal. One unrevokable member must not block the whole
			// set forever - see property 2 on this file.
			result.Unmanaged = append(result.Unmanaged, CloudAccessUnmanagedGrantModel{
				Email: types.StringValue(m.Email),
				// Null rather than empty: this is the cloud-level grant, not a grant on
				// some project, and the attribute's documented meaning for null is
				// exactly that.
				ProjectID: types.StringNull(),
				Reason:    types.StringValue(cloudAccessRevokeFailureReason(err)),
			})
			continue
		}
		tflog.Info(ctx, "Revoked undeclared cloud member", map[string]interface{}{"cloud_id": cloudID})
	}

	if len(result.Unmanaged) > 0 {
		diags.AddWarning(
			"Some Members Could Not Be Revoked",
			fmt.Sprintf("%d member(s) of cloud %s could not be removed and still have access this configuration says they "+
				"should not have. They are recorded in unmanaged_grants with the reason for each.\n\nThe apply converged "+
				"rather than failing, because one unrevokable member would otherwise block every other change to this "+
				"cloud's members indefinitely. Alert on unmanaged_grants having a non-zero length.", len(result.Unmanaged), cloudID),
		)
	}

	return result, diags
}

// grantCloudAccessMember establishes membership and then writes the role.
//
// The bootstrap POST runs UNCONDITIONALLY and first, and both halves of that
// matter. Unconditionally, because a conditional skip is what produces an
// unrepairable resource - a role row with no membership edge, where the
// bootstrap then 409s forever and the revoke 404s forever, and no read can
// detect the state ahead of time. First, because the legacy call is itself a SET
// over permission_level and would clobber the role if it ran second.
func grantCloudAccessMember(ctx context.Context, client *Client, cloudID string, m cloudAccessDesiredMember) diag.Diagnostics {
	var diags diag.Diagnostics

	identityID, userID, err := resolveIdentityForEmail(ctx, client, m.Email)
	if err != nil {
		diags.AddError(
			"Could Not Resolve Organization Member",
			fmt.Sprintf("No organization member could be resolved for %q: %s\n\nA cloud role can only be granted to "+
				"someone who is already a member of the organization. Add them first - anyscale_organization_user manages "+
				"membership, and anyscale_organization_invitation invites someone who has no account yet.", m.Email, err.Error()),
		)
		return diags
	}

	weCreatedMembership := false
	reqBody, err := MarshalRequestBody(CreateCloudCollaboratorRequest{
		Email:           m.Email,
		PermissionLevel: cloudAccessBridgePermissionLevel,
	})
	if err != nil {
		diags.AddError("Error Marshaling Request", err.Error())
		return diags
	}
	if _, err := DoRequestRaw(
		ctx, client, "POST", fmt.Sprintf("/api/v2/clouds/%s/collaborators/users", cloudID), reqBody,
		http.StatusOK, http.StatusNoContent,
	); err != nil {
		detail := extractAPIErrorDetail(err)
		if !strings.Contains(detail, cloudAccessAlreadyHasPermissionsSubstring) {
			diags.AddError(
				"Could Not Establish Cloud Membership",
				fmt.Sprintf("Adding %s to cloud %s failed: %s", m.Email, cloudID, detail),
			)
			return diags
		}
		// They were already a member. Nothing was written, so nothing needs rolling
		// back if the role write below fails.
		tflog.Info(ctx, "Member already had cloud membership", map[string]interface{}{"cloud_id": cloudID})
	} else {
		weCreatedMembership = true
	}

	roleErr := setCloudAccessRole(ctx, client, cloudID, userID, m.BaseRole, m.DenyRoles)
	if roleErr == nil {
		return diags
	}

	{
		detail := extractAPIErrorDetail(roleErr)

		if !weCreatedMembership {
			diags.AddError(
				"Could Not Set Cloud Role",
				fmt.Sprintf("Setting %s's role on cloud %s failed: %s\n\nThey were already a collaborator on this cloud "+
					"before this apply, and that pre-existing access was NOT modified or removed.", m.Email, cloudID, detail),
			)
			return diags
		}

		// Membership was created moments ago by this very call and the role write
		// that was supposed to follow it failed. Roll back, rather than leave the
		// bridge permission level in place as access nobody asked for.
		if _, delErr := DoRequestRaw(
			ctx, client, "DELETE", fmt.Sprintf("/api/v2/clouds/%s/collaborators/%s", cloudID, identityID), nil,
			http.StatusOK, http.StatusNoContent, http.StatusNotFound,
		); delErr != nil {
			diags.AddError(
				"Could Not Set Cloud Role, And Rollback Also Failed",
				fmt.Sprintf("Setting %s's role on cloud %s failed: %s\n\nThis resource then tried to remove the cloud "+
					"membership it had just created, and that also failed: %s\n\nAs a result %s now holds an unintended %q "+
					"permission level on cloud %s that Terraform did not manage to remove. Remove it manually (for example "+
					"DELETE /api/v2/clouds/%s/collaborators/%s, or the equivalent console action) and retry.",
					m.Email, cloudID, detail, extractAPIErrorDetail(delErr), m.Email,
					cloudAccessBridgePermissionLevel, cloudID, cloudID, identityID),
			)
			return diags
		}

		diags.AddError(
			"Could Not Set Cloud Role",
			fmt.Sprintf("Setting %s's role on cloud %s failed: %s\n\nThe cloud membership this resource had just created "+
				"was rolled back successfully - no unintended access was left behind.", m.Email, cloudID, detail),
		)
		return diags
	}
}

// setCloudAccessRole writes a member's role. The call is an authoritative SET
// over the (base_role, deny_roles) pair, never additive, so deny_roles is always
// sent even when empty.
func setCloudAccessRole(ctx context.Context, client *Client, cloudID, userID, baseRole string, denyRoles []string) error {
	if denyRoles == nil {
		denyRoles = []string{}
	}
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

// revokeCloudAccessMember removes a collaborator from the cloud. It takes the
// IDENTITY id, not the user id - the two are different ids on the same person
// and this endpoint takes the identity one.
//
// A 404 counts as success: the member is not there, which is the requested end
// state, and treating it as a failure would put a permanent entry in
// unmanaged_grants for access that does not exist.
func revokeCloudAccessMember(ctx context.Context, client *Client, cloudID, identityID string) error {
	_, err := DoRequestRaw(
		ctx, client, "DELETE", fmt.Sprintf("/api/v2/clouds/%s/collaborators/%s", cloudID, identityID), nil,
		http.StatusOK, http.StatusNoContent, http.StatusNotFound,
	)
	return err
}

// cloudAccessRevokeFailureReason turns a failed revoke into the text that lands
// in unmanaged_grants. The three recognized cases are all situations a reader
// cannot act on from the raw API detail alone.
// cloudAccessRevokeFailureReason turns a failed revoke into the text that lands
// in unmanaged_grants.
//
// The unrecognized case names BOTH structural blockers rather than returning the
// raw detail alone, because they return the same 409 status and differ only in
// wording - so a reworded message on either side would otherwise leave an
// operator with a bare conflict and no idea which of the two they hit. Naming
// both is honest about the ambiguity; guessing one would not be.
func cloudAccessRevokeFailureReason(err error) string {
	detail := extractAPIErrorDetail(err)
	switch {
	case strings.Contains(detail, cloudAccessAutoAddUserSubstring):
		return fmt.Sprintf("%s (this cloud's auto_add_user setting blocks removing collaborators; it must be turned off "+
			"before Terraform can be authoritative over this cloud's members)", detail)
	case strings.Contains(detail, "cannot remove yourself"):
		return fmt.Sprintf("%s (Anyscale does not allow removing your own cloud access; another cloud owner must apply "+
			"this change)", detail)
	case strings.Contains(detail, cloudAccessPolicyAPISubstring):
		return fmt.Sprintf("%s (this organization's cloud permissions are managed outside Terraform via 'anyscale policy "+
			"set'; see https://docs.anyscale.com/reference/cli/policy#policy-cli)", detail)
	default:
		return fmt.Sprintf("%s (two settings are known to block collaborator removal outright and both report a conflict: "+
			"the cloud's auto_add_user setting, and an organization that has directory sync enabled together with at least "+
			"one Policy API role binding. Check both before treating this as a transient failure)", detail)
	}
}

// reconcileProjectRoles is the project-scope pass, run after the cloud grants so
// that every grantee already holds a role on the parent cloud.
//
// The member list is re-read here rather than reused from before the grants,
// because a member added moments ago is not in that earlier snapshot and their
// identity is needed to remove a project role. One extra list request buys
// correctness for exactly the case a stale snapshot gets wrong.
func reconcileProjectRoles(
	ctx context.Context,
	client *Client,
	cloudID string,
	desired map[string]cloudAccessDesiredMember,
	priorProjects map[string]map[string]string,
	cascaded map[string]bool,
) ([]CloudAccessUnmanagedGrantModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	inScope := cloudAccessInScopeProjectIDs(desired, priorProjects)
	if len(inScope) == 0 {
		return nil, diags
	}

	members, err := listCloudAccessMembers(ctx, client, cloudID)
	if err != nil {
		diags.AddError(
			"Could Not Re-Read Cloud Members Before Applying Project Roles",
			fmt.Sprintf("The cloud-level changes were applied, but the member list could not be re-read, and project "+
				"roles need it to resolve who to grant and revoke: %s\n\nProject roles were not changed.",
				extractAPIErrorDetail(err)),
		)
		return nil, diags
	}
	identityByFoldedEmail := make(map[string]cloudAccessRemoteMember, len(members))
	for _, m := range members {
		identityByFoldedEmail[cloudAccessFoldEmail(m.Email)] = m
	}

	current, err := cloudAccessProjectRoles(ctx, client, inScope)
	if err != nil {
		// Read failure here is fatal rather than "assume nothing is granted": that
		// assumption would re-issue every grant, which is harmless, AND treat every
		// existing role as absent, which is not.
		diags.AddError(
			"Could Not Read Project Roles",
			fmt.Sprintf("The projects this configuration manages could not be read, so it cannot tell which roles need "+
				"changing: %s\n\nProject roles were not changed.", extractAPIErrorDetail(err)),
		)
		return nil, diags
	}

	plan := planProjectRoles(desired, priorProjects, current, identityByFoldedEmail, cascaded)
	return applyProjectRoles(ctx, client, plan, identityByFoldedEmail)
}

// cloudAccessInScopeProjectIDs is the union of the projects the configuration
// names and those state previously named, sorted.
//
// Prior-state projects are in scope even when no longer declared, which is the
// whole mechanism by which a dropped project role becomes observable and therefore
// removable. Config alone would make a drop unobservable.
func cloudAccessInScopeProjectIDs(
	desired map[string]cloudAccessDesiredMember,
	priorProjects map[string]map[string]string,
) []string {
	seen := make(map[string]bool)
	for _, m := range desired {
		for projectID := range m.Projects {
			seen[projectID] = true
		}
	}
	for _, projects := range priorProjects {
		for projectID := range projects {
			seen[projectID] = true
		}
	}
	out := make([]string, 0, len(seen))
	for projectID := range seen {
		out = append(out, projectID)
	}
	sort.Strings(out)
	return out
}

// cloudAccessPriorProjectDeclarations reads what state previously declared, keyed
// by folded email then project ID.
func cloudAccessPriorProjectDeclarations(ctx context.Context, member types.Map) (map[string]map[string]string, diag.Diagnostics) {
	var diags diag.Diagnostics
	if member.IsNull() || member.IsUnknown() {
		return nil, diags
	}
	entries := make(map[string]CloudAccessMemberModel, len(member.Elements()))
	diags.Append(member.ElementsAs(ctx, &entries, false)...)
	if diags.HasError() {
		return nil, diags
	}
	out := make(map[string]map[string]string)
	for email, m := range entries {
		if m.Projects.IsNull() || m.Projects.IsUnknown() || len(m.Projects.Elements()) == 0 {
			continue
		}
		var projects map[string]string
		diags.Append(m.Projects.ElementsAs(ctx, &projects, false)...)
		if diags.HasError() {
			return nil, diags
		}
		out[cloudAccessFoldEmail(email)] = projects
	}
	return out, diags
}
