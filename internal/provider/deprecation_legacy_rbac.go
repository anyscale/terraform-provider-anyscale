package provider

import "strings"

// Deprecation of the legacy RBAC surfaces: collaborator and policy records, which an
// organization converted to role bindings refuses to write and no longer reads. Each one
// sets both channels: DeprecationMessage (the warning Terraform prints) and a callout in
// MarkdownDescription, because the Registry docs do not render DeprecationMessage. The
// shared explanation lives in the RBAC guide, linked from every callout.
const (
	legacyRBACGuideLink = "[Legacy RBAC deprecation](../guides/rbac.md#legacy-rbac-deprecation)"

	// legacyRBACAttributeNote starts the MarkdownDescription of a deprecated attribute. The
	// docs already tag the attribute "Deprecated", so this only points at the explanation.
	legacyRBACAttributeNote = "See " + legacyRBACGuideLink + ". "

	// legacyRBACResourceDeprecation is for the resources that write legacy access.
	legacyRBACResourceDeprecation = "Deprecated for organizations converted to role bindings; use anyscale_role_binding there. " +
		"Unconverted organizations keep using this resource. Not available on Azure, where access is managed in the Azure portal."

	// legacyAutoAddUserDeprecation is for anyscale_cloud.auto_add_user, the one legacy write
	// that is an attribute.
	legacyAutoAddUserDeprecation = "Deprecated for organizations converted to role bindings; bind a role to the All users group " +
		"on the cloud with anyscale_role_binding there. Unconverted organizations keep using it. " +
		"Not available on Azure, where access is managed in the Azure portal."

	// legacyAutoAddUserReadDeprecation is for the data sources' read-only auto_add_user.
	legacyAutoAddUserReadDeprecation = "Deprecated for organizations converted to role bindings, where it no longer applies; " +
		"a role binding for the All users group replaces it. Unconverted organizations keep using it."

	// legacyRBACReadDeprecation is for read-only attributes that report legacy access and
	// have no replacement. Reads are not blocked on Azure, so there is no Azure clause.
	legacyRBACReadDeprecation = "Deprecated for organizations converted to role bindings, where it is a snapshot from " +
		"before conversion and has no replacement yet. Unconverted organizations keep using it."
)

// legacyRBACResourceCallout is the top-of-page callout for a deprecated resource.
func legacyRBACResourceCallout() string {
	return "~> **Deprecated** " + strings.TrimPrefix(legacyRBACResourceDeprecation, "Deprecated ") + " See " + legacyRBACGuideLink + "."
}
