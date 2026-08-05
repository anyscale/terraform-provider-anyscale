package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// The project-scope half of the reconcile: it makes each member's roles on the
// projects the configuration NAMES match what the configuration says.
//
// WHAT THIS IS AUTHORITATIVE OVER, exactly: the (member, project) pairs named in
// the configuration, plus the pairs previously named and now dropped - the latter
// being what makes a removal expressible at all. It is NOT authoritative over
// every project under the cloud. Enumerating those would mean adopting this
// resource silently took ownership of projects the practitioner never mentioned
// and stripped roles on them, which is a much larger claim than the one the
// resource makes.
//
// THE CASCADE IS WHY THIS IS NOT SYMMETRIC WITH THE CLOUD PASS. Removing a cloud
// collaborator recursively revokes their permissions on that cloud's projects, so
// a member being dropped at cloud scope needs no per-project revokes at all -
// attempting them would produce 404s that this resource would then have to
// misreport as failures.
//
// Live-confirmed rather than taken from the design record: an identity added as
// both a cloud collaborator and a project collaborator, revoked at CLOUD scope
// only, was gone from the project's collaborator list on the next read with no
// project-level call made.
//
// The project vocabulary is also genuinely different from the cloud one: a
// project takes owner, write or readonly, and 422s on the cloud's "writer".
// ValidateConfig rejects that spelling at plan time.

// projectCollaboratorAlreadyHasPermissionsSubstring is the detail substring when
// the batch create is given someone who already holds a role on the project. It
// is a real, expected outcome rather than a failure: the create is how a role is
// established and it cannot know in advance whether one exists.
const projectCollaboratorAlreadyHasPermissionsSubstring = "already has permissions for this project"

// projectCollaboratorSelfModifySubstring is the detail substring when the target
// resolves to the calling identity. Reachable here despite the caller being
// excluded from the member map, because a member's email can resolve to the
// caller through a different identity than the one userinfo reports.
const projectCollaboratorSelfModifySubstring = "alter your own role"

// projectCollaboratorEntry is one entry of the batch-create body. Restored here
// rather than shared: the anyscale_project resource no longer manages
// collaborators, so its request models went with the block, and this resource is
// now the only writer.
type projectCollaboratorEntry struct {
	Value struct {
		Email string `json:"email"`
	} `json:"value"`
	PermissionLevel string `json:"permission_level"`
}

// projectCollaboratorUpdateRequest is the body of the authoritative
// per-collaborator PUT.
type projectCollaboratorUpdateRequest struct {
	PermissionLevel string `json:"permission_level"`
}

// grantProjectRole sets one member's role on one project.
//
// The batch_create endpoint takes an email, so it needs no identity lookup; the
// update takes an identity id, so the already-has-permissions path resolves one
// from the project's own collaborator list rather than reusing the cloud-scope
// identity. Same person, but the project listing is the authoritative source for
// the id that project's endpoints accept.
func grantProjectRole(ctx context.Context, client *Client, projectID, email, level string) error {
	entry := projectCollaboratorEntry{PermissionLevel: level}
	entry.Value.Email = email
	entries := []projectCollaboratorEntry{entry}
	jsonBytes, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("failed to serialize project collaborator request: %w", err)
	}

	_, err = cloudAccessDoWriteRetry(
		ctx, client, "POST", fmt.Sprintf("/api/v2/projects/%s/collaborators/users/batch_create", projectID),
		strings.NewReader(string(jsonBytes)),
		http.StatusOK, http.StatusNoContent, http.StatusCreated,
	)
	if err == nil {
		return nil
	}

	detail := extractAPIErrorDetail(err)
	if !strings.Contains(detail, projectCollaboratorAlreadyHasPermissionsSubstring) {
		return err
	}

	identityID, found, resolveErr := findProjectCollaboratorIdentity(ctx, client, projectID, email)
	if resolveErr != nil {
		return fmt.Errorf("%s already has a role on project %s, but their identity could not be resolved to change it: %w", email, projectID, resolveErr)
	}
	if !found {
		// The API says they have permissions and the listing says they do not.
		// Reported rather than retried: a create/list disagreement is not something
		// another attempt resolves, and guessing an identity id here would write a
		// role onto whoever that id belongs to.
		return fmt.Errorf("%s already has a role on project %s according to the API, but does not appear in that project's collaborator list", email, projectID)
	}

	return setProjectRole(ctx, client, projectID, identityID, level)
}

// setProjectRole issues the authoritative PUT for one collaborator's role.
func setProjectRole(ctx context.Context, client *Client, projectID, identityID, level string) error {
	reqBody, err := MarshalRequestBody(projectCollaboratorUpdateRequest{PermissionLevel: level})
	if err != nil {
		return fmt.Errorf("failed to serialize project role request: %w", err)
	}
	_, err = cloudAccessDoWriteRetry(
		ctx, client, "PUT", fmt.Sprintf("/api/v2/projects/%s/collaborators/%s", projectID, identityID),
		reqBody, http.StatusOK, http.StatusNoContent,
	)
	return err
}

// revokeProjectRole removes a collaborator from a project. A 404 counts as
// success: they are not there, which is the requested end state.
func revokeProjectRole(ctx context.Context, client *Client, projectID, identityID string) error {
	_, err := cloudAccessDoWriteRetry(
		ctx, client, "DELETE", fmt.Sprintf("/api/v2/projects/%s/collaborators/%s", projectID, identityID), nil,
		http.StatusOK, http.StatusNoContent, http.StatusNotFound,
	)
	return err
}

// findProjectCollaboratorIdentity resolves an email to the identity id that
// project's endpoints accept. Matched case-insensitively, since Anyscale treats
// email identity as case-insensitive and an exact match would read one person as
// two.
func findProjectCollaboratorIdentity(ctx context.Context, client *Client, projectID, email string) (string, bool, error) {
	collaborators, err := listProjectCollaborators(ctx, client, projectID)
	if err != nil {
		return "", false, err
	}
	for _, c := range collaborators {
		if strings.EqualFold(strings.TrimSpace(c.Value.Email), strings.TrimSpace(email)) {
			return c.ID, true, nil
		}
	}
	return "", false, nil
}

// cloudAccessProjectPlan is the project work one reconcile pass needs to do.
type cloudAccessProjectPlan struct {
	// Grants are (project, email, level) triples to establish or change.
	Grants []cloudAccessProjectGrant
	// Revokes are (project, email) pairs to remove.
	Revokes []cloudAccessProjectGrant
}

type cloudAccessProjectGrant struct {
	ProjectID string
	Email     string
	Level     string
}

// planProjectRoles works out the project grants and revokes for one reconcile.
//
// current is what the projects in scope report, keyed by project then user_id -
// the same shape Read uses. cascaded is the set of folded emails being revoked at
// CLOUD scope, whose project permissions the backend removes recursively; those
// members get no per-project revokes, because attempting them would produce 404s
// this resource would then have to report as failures.
//
// A revoke is emitted only for a pair that was PREVIOUSLY declared and is now
// dropped, never for a role merely observed on an in-scope project. That is the
// authority boundary: dropping an entry from the configuration revokes it, but a
// role somebody else granted on a project you happen to name is not this
// resource's to remove.
func planProjectRoles(
	desired map[string]cloudAccessDesiredMember,
	priorProjects map[string]map[string]string,
	current map[string]map[string]string,
	identityByFoldedEmail map[string]cloudAccessRemoteMember,
	cascaded map[string]bool,
) cloudAccessProjectPlan {
	var plan cloudAccessProjectPlan

	// Sorted so an identical re-apply issues identical requests in an identical
	// order: which grants land before a failure then depends on the configuration
	// rather than on Go's randomized map iteration.
	for _, folded := range sortedFoldedEmails(desired) {
		m := desired[folded]
		for _, projectID := range sortedProjectIDs(m.Projects) {
			level := m.Projects[projectID]
			held, read := current[projectID]
			if read {
				if remote, ok := identityByFoldedEmail[folded]; ok {
					if existing, has := held[remote.UserID]; has && existing == level {
						// Already exactly right. Skipped rather than rewritten, so a no-op
						// apply issues no writes at all.
						continue
					}
				}
			}
			plan.Grants = append(plan.Grants, cloudAccessProjectGrant{ProjectID: projectID, Email: m.Email, Level: level})
		}
	}

	for _, folded := range sortedFoldedKeys(priorProjects) {
		if cascaded[folded] {
			continue
		}
		wanted := map[string]string{}
		if m, declared := desired[folded]; declared {
			wanted = m.Projects
		}
		for _, projectID := range sortedProjectIDs(priorProjects[folded]) {
			if _, still := wanted[projectID]; still {
				continue
			}
			plan.Revokes = append(plan.Revokes, cloudAccessProjectGrant{ProjectID: projectID, Email: folded})
		}
	}

	return plan
}

// cloudAccessUnplannedProjectShortfall is the safe fallback when the project
// pass could not even be PLANNED - a read failure before planProjectRoles
// could run, most likely the reconcile's own context deadline (J.13) firing
// while that read was in flight or retrying. It cannot know which project
// roles are already correct without the read that failed, so it deliberately
// OVER-reports rather than under-reports: every project role the
// configuration declares becomes an ungranted entry, and every previously
// declared project role not covered by a cloud-scope cascade becomes an
// unmanaged entry, both carrying reason. Over-reporting a shortfall that may
// have partially succeeded is the safe direction - the next apply re-attempts
// everything reported here, which is harmless for anything that actually
// already succeeded, and the alternative (erroring) is what taints the
// resource.
func cloudAccessUnplannedProjectShortfall(
	desired map[string]cloudAccessDesiredMember,
	priorProjects map[string]map[string]string,
	cascaded map[string]bool,
	reason string,
) (unmanaged []CloudAccessUnmanagedGrantModel, ungranted []CloudAccessUnmanagedGrantModel) {
	for _, folded := range sortedFoldedEmails(desired) {
		m := desired[folded]
		for _, projectID := range sortedProjectIDs(m.Projects) {
			ungranted = append(ungranted, CloudAccessUnmanagedGrantModel{
				Email:     types.StringValue(m.Email),
				ProjectID: types.StringValue(projectID),
				Reason:    types.StringValue(reason),
			})
		}
	}

	for _, folded := range sortedFoldedKeys(priorProjects) {
		if cascaded[folded] {
			continue
		}
		wanted := map[string]string{}
		if m, declared := desired[folded]; declared {
			wanted = m.Projects
		}
		for _, projectID := range sortedProjectIDs(priorProjects[folded]) {
			if _, still := wanted[projectID]; still {
				// Already reported above as an ungranted entry - it is a grant to
				// (re)confirm, not a drop, so it must not also appear as unmanaged.
				continue
			}
			unmanaged = append(unmanaged, CloudAccessUnmanagedGrantModel{
				Email:     types.StringValue(folded),
				ProjectID: types.StringValue(projectID),
				Reason:    types.StringValue(reason),
			})
		}
	}

	return unmanaged, ungranted
}

func sortedFoldedEmails(desired map[string]cloudAccessDesiredMember) []string {
	out := make([]string, 0, len(desired))
	for k := range desired {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedFoldedKeys(m map[string]map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedProjectIDs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cloudAccessProjectWrite403Reason names both known causes of a 403 from a
// project-scope write route (the batch_create grant, the per-collaborator
// PUT, or the DELETE) without picking between them. Message only - the
// caller already converges and records this as ungranted/unmanaged (see
// applyProjectRoles below), so this must never become a hard error or a
// retry: a 403 is a decision, not a transient condition, and J.13 forbids
// retrying one.
//
// Fires on ANY 403 from these routes, not on a detail substring: the real
// body is a bare "Permission denied" with nothing to distinguish on. The
// ordinary cause is named first on purpose - a bare "Permission denied" is
// exactly what a token that genuinely lacks the permission produces, so
// leading with the rarer explanation would mislead the common case. That
// rarer cause - this project's owner grant missing from the authorization
// service, a known backend condition that can also leave the project
// undeletable - is a question for whoever administers the organization's
// feature flags, not the practitioner's token, and this error alone cannot
// tell the two apart.
//
// Project-scope only, deliberately not folded into cloudAccessRevokeFailureReason
// (the cloud-scope equivalent): cloud-scope already has its own distinguishable
// 409 causes (auto_add_user, Policy API) and no matching 403 condition:
// merging the two would fire this message on an unrelated cloud-scope failure.
func cloudAccessProjectWrite403Reason(err error) string {
	detail := extractAPIErrorDetail(err)
	var statusErr *UnexpectedStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusForbidden {
		return detail
	}
	return fmt.Sprintf("%s (two things are known to cause this: most commonly, the token being used does not "+
		"have permission to manage collaborators on this project - check that first. Less commonly, this "+
		"project's owner grant is missing from the authorization service, a known backend condition that can "+
		"also leave the project undeletable - a question for whoever administers this organization's feature "+
		"flags, not something this configuration did. This error alone cannot distinguish the two)", detail)
}

// applyProjectRoles executes a project plan.
//
// Grants are ALWAYS attempted, every one, regardless of an earlier failure in
// this same loop - additive and safe under the same rule reconcileCloudAccess
// follows for cloud-scope grants: never fail to attempt what configuration
// asked for. A failed grant records into ungranted rather than aborting.
//
// Revokes are skipped ENTIRELY - none attempted - when priorGrantsFailed is
// true (a cloud-scope grant already failed elsewhere in this reconcile) or
// when a grant failed in the loop above. Each skipped revoke is still
// recorded into unmanaged, with a reason distinguishing "skipped because a
// grant failed" from an attempted-and-failed revoke, so the apply's warning
// does not silently omit that these members still have access. When neither
// condition holds, revokes run and record failures exactly as before.
func applyProjectRoles(
	ctx context.Context,
	client *Client,
	plan cloudAccessProjectPlan,
	identityByFoldedEmail map[string]cloudAccessRemoteMember,
	priorGrantsFailed bool,
) (unmanaged []CloudAccessUnmanagedGrantModel, ungranted []CloudAccessUnmanagedGrantModel, diags diag.Diagnostics) {
	for _, g := range plan.Grants {
		if err := grantProjectRole(ctx, client, g.ProjectID, g.Email, g.Level); err != nil {
			detail := extractAPIErrorDetail(err)
			switch {
			case strings.Contains(detail, projectCollaboratorSelfModifySubstring):
				detail = fmt.Sprintf("%s\n\nThis email resolves to the identity Terraform is authenticated as. Anyscale "+
					"does not allow changing your own role on a project - have another project owner apply this "+
					"configuration instead.", detail)
			default:
				detail = cloudAccessProjectWrite403Reason(err)
			}
			ungranted = append(ungranted, CloudAccessUnmanagedGrantModel{
				Email:     types.StringValue(g.Email),
				ProjectID: types.StringValue(g.ProjectID),
				Reason:    types.StringValue(detail),
			})
			continue
		}
		tflog.Info(ctx, "Granted project role", map[string]interface{}{"project_id": g.ProjectID, "level": g.Level})
	}

	if priorGrantsFailed || len(ungranted) > 0 {
		for _, g := range plan.Revokes {
			unmanaged = append(unmanaged, CloudAccessUnmanagedGrantModel{
				Email:     types.StringValue(g.Email),
				ProjectID: types.StringValue(g.ProjectID),
				Reason: types.StringValue("skipped: a grant this apply attempted failed, so no revokes were attempted " +
					"this apply - see ungranted_members for what failed"),
			})
		}
		return unmanaged, ungranted, diags
	}

	for _, g := range plan.Revokes {
		remote, known := identityByFoldedEmail[g.Email]
		if !known {
			// No identity to revoke against. Recorded rather than skipped: the
			// configuration says this role should be gone, and silence here is exactly
			// the silent-partial-revoke this attribute exists to prevent.
			unmanaged = append(unmanaged, CloudAccessUnmanagedGrantModel{
				Email:     types.StringValue(g.Email),
				ProjectID: types.StringValue(g.ProjectID),
				Reason: types.StringValue("this member is no longer a collaborator on the cloud, so their identity could " +
					"not be resolved to remove the project role; if the cloud-level removal did not cascade, remove it manually"),
			})
			continue
		}

		identityID, found, err := findProjectCollaboratorIdentity(ctx, client, g.ProjectID, remote.Email)
		if err != nil {
			unmanaged = append(unmanaged, CloudAccessUnmanagedGrantModel{
				Email:     types.StringValue(remote.Email),
				ProjectID: types.StringValue(g.ProjectID),
				Reason:    types.StringValue(extractAPIErrorDetail(err)),
			})
			continue
		}
		if !found {
			// Already gone - very often because the cloud-level removal cascaded, or
			// somebody removed it out of band. The requested end state, so not a failure.
			continue
		}

		if err := revokeProjectRole(ctx, client, g.ProjectID, identityID); err != nil {
			unmanaged = append(unmanaged, CloudAccessUnmanagedGrantModel{
				Email:     types.StringValue(remote.Email),
				ProjectID: types.StringValue(g.ProjectID),
				Reason:    types.StringValue(cloudAccessProjectWrite403Reason(err)),
			})
			continue
		}
		tflog.Info(ctx, "Revoked project role", map[string]interface{}{"project_id": g.ProjectID})
	}

	return unmanaged, ungranted, diags
}
