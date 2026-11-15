package provider

import (
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// An organization manages access through one of two stores at a time: the
// legacy permission system (cloud and project collaborators, organization
// roles) or role bindings. Conversion moves it from the first to the second,
// one way. The API enforces the boundary with a 409 on every write to the
// store the organization is not on, and while a conversion is running it
// answers the legacy writes with a third refusal: retry, do not migrate -
// and once converted it also answers the legacy collaborator reads with the
// same 409 (converted_organizations.py:119-142). All three refusals are
// classified by the stable part of their detail.

// orgConvertedSubstring is the detail substring of every 409 the legacy
// permission system answers in a converted organization: the writes, and the
// project and cloud collaborator reads. The full detail is
// "This organization uses role bindings. Manage access in Users & IAM or with
// the role bindings API. / See who has access in Users & IAM or with the role
// bindings API." (converted_organizations.py:59-84 and :119-142). It covers
// the cloud and project collaborator writes and reads, the organization role
// writes and PUT /clouds/{id}/auto_add_user.
const orgConvertedSubstring = "This organization uses role bindings"

// orgNotConvertedSubstring is the detail substring of the 409 every role
// binding write answers in an organization that has not converted. The full
// detail is "Switch this organization to role bindings to change role
// bindings." (converted_organizations.py:87-108 at product 3b1f86a).
const orgNotConvertedSubstring = "Switch this organization to role bindings"

// orgAccessModeGuide names where the two modes are explained. Diagnostics are
// read in a terminal, so it is spelled out rather than linked.
const orgAccessModeGuide = "See \"Organization access mode\" in the provider's RBAC guide (guides/rbac.md#organization-access-mode)."

// isStatusWithSubstring reports whether err is an HTTP response with this
// status whose API detail contains substring.
func isStatusWithSubstring(err error, statusCode int, substring string) bool {
	return isHTTPStatus(err, statusCode) && strings.Contains(extractAPIErrorDetail(err), substring)
}

// isOrgConvertedConflict reports a legacy permission call refused because the
// organization has converted to role bindings: a legacy write, or one of the
// legacy collaborator reads.
func isOrgConvertedConflict(err error) bool {
	return isStatusWithSubstring(err, http.StatusConflict, orgConvertedSubstring)
}

// isOrgNotConvertedConflict reports a role binding write refused because the
// organization still uses legacy access management.
func isOrgNotConvertedConflict(err error) bool {
	return isStatusWithSubstring(err, http.StatusConflict, orgNotConvertedSubstring)
}

// orgConvertingSubstring is the detail substring of the 409 every legacy
// permission write answers while a conversion to role bindings is running -
// or stalled; conversions of large organizations run in the background, and a
// failed one refuses until retried. The full detail is "This organization is
// converting to role bindings. Try again when the conversion finishes, or
// retry it in the migration wizard if it stopped." (converted_organizations.
// py:86-118). Legacy reads still answer normally while the conversion runs,
// because until it commits the legacy rows are still the truth.
const orgConvertingSubstring = "This organization is converting to role bindings"

// isOrgConvertingConflict reports a legacy permission write refused because a
// conversion to role bindings is in progress.
func isOrgConvertingConflict(err error) bool {
	return isStatusWithSubstring(err, http.StatusConflict, orgConvertingSubstring)
}

// orgConvertedDetail explains a legacy write refused in a converted
// organization, after the API's own detail. It names the replacement without
// promising a one-to-one mapping: legacy roles and role binding roles are
// different catalogs.
func orgConvertedDetail(err error) string {
	return extractAPIErrorDetail(err) + "\n\nThis organization has converted to role bindings, so Anyscale refuses legacy access writes. " +
		"Manage access with anyscale_iam_role_grant, on a user group (user_group_id) or on a user (user_id). " + orgAccessModeGuide
}

// orgConvertedSummary is the summary of every diagnostic for a legacy write
// refused in a converted organization, error or warning.
const orgConvertedSummary = "Organization Uses Role Bindings"

// addOrgConvertedError reports a legacy write refused in a converted
// organization. outcome, when set, says what that means for the operation.
func addOrgConvertedError(diags *diag.Diagnostics, err error, outcome string) {
	detail := orgConvertedDetail(err)
	if outcome != "" {
		detail += "\n\n" + outcome
	}
	diags.AddError(orgConvertedSummary, detail)
}

// orgConvertedReadDetail explains a legacy read refused in a converted
// organization, after the API's own detail. It is read-worded and says what
// to do: the readers this resource depends on no longer describe anyone's
// access, so the resource cannot be refreshed, written or destroyed - only
// removed from state and replaced.
func orgConvertedReadDetail(err error) string {
	return extractAPIErrorDetail(err) + "\n\nThis organization has converted to role bindings, so Anyscale refuses the legacy " +
		"collaborator reads this resource depends on. Remove the anyscale_cloud_access resource from the configuration, " +
		"run `terraform state rm` on it, and manage access with anyscale_iam_role_grant. " + orgAccessModeGuide
}

// addOrgConvertedReadError reports a legacy read refused in a converted
// organization. outcome, when set, says what that means for the operation.
func addOrgConvertedReadError(diags *diag.Diagnostics, err error, outcome string) {
	detail := orgConvertedReadDetail(err)
	if outcome != "" {
		detail += "\n\n" + outcome
	}
	diags.AddError(orgConvertedSummary, detail)
}

// orgConvertingSummary is the summary of every diagnostic for a legacy write
// refused while a conversion to role bindings is running.
const orgConvertingSummary = "Organization Is Converting to Role Bindings"

// orgConvertingDetail explains a legacy write refused while the conversion is
// running: access is not migrated to role bindings yet, and the legacy surface
// is the right one to keep using - briefly. Nothing needs replacing yet, only
// waiting (or restarting a stalled conversion).
func orgConvertingDetail(err error) string {
	return extractAPIErrorDetail(err) + "\n\nA conversion of this organization to role bindings is running, so Anyscale " +
		"refuses legacy access writes until it finishes. Retry once the conversion completes; if it stopped, retry it " +
		"in the migration wizard. Until then, do not change the legacy access this configuration declares - nothing " +
		"needs migrating by hand. " + orgAccessModeGuide
}

// addOrgConvertingError reports a legacy write refused while the conversion is
// running. outcome, when set, says what that means for the operation.
func addOrgConvertingError(diags *diag.Diagnostics, err error, outcome string) {
	detail := orgConvertingDetail(err)
	if outcome != "" {
		detail += "\n\n" + outcome
	}
	diags.AddError(orgConvertingSummary, detail)
}

// addOrgNotConvertedError reports a role binding write refused because the
// organization still uses legacy access management. outcome, when set, says
// what that means for the operation.
func addOrgNotConvertedError(diags *diag.Diagnostics, err error, outcome string) {
	detail := extractAPIErrorDetail(err) + "\n\nThis organization still uses legacy access management, so Anyscale refuses role binding writes " +
		"until an organization admin converts it to role bindings in Users & IAM. Until then, manage access with " +
		"anyscale_cloud_access and anyscale_organization_user_role. " + orgAccessModeGuide
	if outcome != "" {
		detail += "\n\n" + outcome
	}
	diags.AddError("Organization Not Converted to Role Bindings", detail)
}
