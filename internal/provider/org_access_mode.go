package provider

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// An organization manages access through one of two stores at a time: the
// legacy permission system (cloud and project collaborators, organization
// roles) or role bindings. Conversion moves it from the first to the second,
// one way. The API enforces the boundary with a 409 on every write to the
// store the organization is not on. Both refusals are classified by the
// stable part of their detail.

// orgConvertedSubstring is the detail substring of the 409 every legacy
// permission write answers in a converted organization. The full detail is
// "This organization uses role bindings. Manage access in Users & IAM or with
// the role bindings API." (backend/server/api/base/resources/
// converted_organizations.py:59-84 at product 3b1f86a). It covers the cloud
// and project collaborator writes, the organization role writes and
// PUT /clouds/{id}/auto_add_user.
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
	var statusErr *UnexpectedStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != statusCode {
		return false
	}
	return strings.Contains(extractAPIErrorDetail(err), substring)
}

// isOrgConvertedConflict reports a legacy permission write refused because the
// organization has converted to role bindings.
func isOrgConvertedConflict(err error) bool {
	return isStatusWithSubstring(err, http.StatusConflict, orgConvertedSubstring)
}

// isOrgNotConvertedConflict reports a role binding write refused because the
// organization still uses legacy access management.
func isOrgNotConvertedConflict(err error) bool {
	return isStatusWithSubstring(err, http.StatusConflict, orgNotConvertedSubstring)
}

// orgConvertedDetail explains a legacy write refused in a converted
// organization, after the API's own detail. It names the replacement without
// promising a one-to-one mapping: legacy roles and role binding roles are
// different catalogs.
func orgConvertedDetail(err error) string {
	return extractAPIErrorDetail(err) + "\n\nThis organization has converted to role bindings, so Anyscale refuses legacy access writes. " +
		"Manage access with anyscale_role_binding, on a user group (user_group_id) or on a user (user_id). " + orgAccessModeGuide
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
