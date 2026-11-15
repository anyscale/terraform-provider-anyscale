package provider

import (
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// Diagnostics for IAM writes the API refuses because the identity does not
// manage IAM on the scope the write targets, and for the group-shaped 404 a
// role-grant or policy write answers for a user group the identity cannot see
// for it. Shared by anyscale_iam_user_group create,
// anyscale_iam_role_grant create and the role-binding policy writes; every
// call site names its own operation and scope.

// iamManageAccessSummary is the error summary for such a refusal.
const iamManageAccessSummary = "Not Allowed to Manage Access"

// iamManageAccessRemedy is what to do about the refusal, appended to every
// detail below.
const iamManageAccessRemedy = "Apply with an identity that manages IAM on that scope - for example one holding the Admin role (Organization owner) there - or ask an admin."

// roleBindingManageIAMSubstring is the detail substring of the 403 that
// role-grant creates and role-binding policy writes answer when the caller
// does not manage IAM on the scope. Full detail, measured on the real API:
// "You do not manage IAM on <type> '<id>'."
const roleBindingManageIAMSubstring = "You do not manage IAM on"

// roleBindingGroupNotFoundDetail matches the 404 a role-grant or policy write
// answers for a user group ID, measured on the real API: "User group
// 'ug_...' not found.", prefixed on the policy path by a
// roles[i].principals[j] index. Its meaning is split - the group is deleted,
// or it exists but the caller cannot see it for this write - and only a
// read-back of the group tells the two apart.
var roleBindingGroupNotFoundDetail = regexp.MustCompile(`User group '(ug_[^']*)' not found\.`)

// iamScopeDesc names a scope for a diagnostic detail.
func iamScopeDesc(resourceType, resourceID string) string {
	return fmt.Sprintf("%s %q", resourceType, resourceID)
}

// iamManageAccessDetail builds the detail for a write the API refused with 403:
// the API's own refusal, the operation and the scope it refused it on, and
// the remedy. operation reads like a sentence subject ("creating a role
// grant"); scopeDesc like its object ('cloud "cld_..."').
func iamManageAccessDetail(apiDetail, operation, scopeDesc string) string {
	return fmt.Sprintf("%s %s on %s requires an identity that manages IAM there. %s",
		apiDetail, operation, scopeDesc, iamManageAccessRemedy)
}

// addIAMManageAccessError reports a write refused with 403 because the
// identity does not manage IAM on the scope it targets.
func addIAMManageAccessError(diags *diag.Diagnostics, apiDetail, operation, scopeDesc string) {
	diags.AddError(iamManageAccessSummary, iamManageAccessDetail(apiDetail, operation, scopeDesc))
}

// iamManageAccessGroupUnseenDetail builds the detail for a grant refused with
// the group-not-found 404 while the group itself still reads back fine: the
// group exists, but this identity cannot see it for this write.
func iamManageAccessGroupUnseenDetail(apiDetail, operation, scopeDesc string) string {
	return fmt.Sprintf("%s %s on %s names a user group that exists but that this identity cannot see: this identity cannot bind roles for that group. %s",
		apiDetail, operation, scopeDesc, iamManageAccessRemedy)
}

// addIAMManageAccessGroupUnseenError reports a grant write refused with the
// group-not-found 404 whose group still reads back with GET.
func addIAMManageAccessGroupUnseenError(diags *diag.Diagnostics, apiDetail, operation, scopeDesc string) {
	diags.AddError(iamManageAccessSummary, iamManageAccessGroupUnseenDetail(apiDetail, operation, scopeDesc))
}
