package acctest

import (
	"strings"
	"testing"
)

// The legacy RBAC surfaces are deprecated (see the RBAC guide's legacy RBAC section).
// resource.Test drops warning diagnostics, so these run real `terraform validate -json`
// against a freshly built binary (warning_diagnostics_acc_test.go) and assert the warning
// Terraform actually prints. No API calls; no credentials.
//
// Measured 2026-10-23 on Terraform 1.10.0, 1.15.7 and 1.16.6: a configured deprecated
// resource or attribute warns with the provider's message on all three. Referencing a
// deprecated read-only data-source attribute also warns on all three; 1.15 and later print
// the provider's message, 1.10 a generic "The attribute ... is deprecated" notice.

// legacyRBACDeprecationText is a phrase from every legacy RBAC deprecation message.
const legacyRBACDeprecationText = "organizations converted to role bindings"

// deprecationWarnings returns the deprecation warnings in result: the provider's message,
// or Core's generic notice for a referenced attribute (Terraform 1.10).
func deprecationWarnings(result tfValidateJSON) []tfDiagnostic {
	var out []tfDiagnostic
	for _, d := range result.Diagnostics {
		if d.Severity != "warning" {
			continue
		}
		if strings.Contains(d.Detail, legacyRBACDeprecationText) ||
			(d.Summary == "Deprecated attribute" && strings.Contains(d.Detail, "is deprecated")) {
			out = append(out, d)
		}
	}
	return out
}

func expectOneDeprecationWarning(t *testing.T, hcl string, wantInDetail ...string) {
	t.Helper()
	result := runTerraformValidate(t, buildProviderBinaryForCLICheck(t), hcl)
	if !result.Valid {
		t.Fatalf("validate failed; diagnostics: %+v", result.Diagnostics)
	}
	got := deprecationWarnings(result)
	if len(got) != 1 {
		t.Fatalf("got %d deprecation warnings, want 1; all diagnostics: %+v", len(got), result.Diagnostics)
	}
	for _, want := range wantInDetail {
		if !strings.Contains(got[0].Detail, want) {
			t.Errorf("deprecation warning detail %q does not contain %q", got[0].Detail, want)
		}
	}
}

func TestAccCloudAccessResource_DeprecationWarningFires(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	expectOneDeprecationWarning(t, `
resource "anyscale_cloud_access" "test" {
  cloud_id = "cld_deprecation_check"
  member   = { "a@example.com" = { base_role = "readonly" } }
}
`, legacyRBACDeprecationText, "anyscale_role_binding", "Azure")
}

func TestAccOrganizationUserRoleResource_DeprecationWarningFires(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	expectOneDeprecationWarning(t, `
resource "anyscale_organization_user_role" "test" {
  email     = "a@example.com"
  base_role = "collaborator"
}
`, legacyRBACDeprecationText, "anyscale_role_binding", "Azure")
}

func TestAccCloudResource_AutoAddUserDeprecationWarningFires(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	expectOneDeprecationWarning(t, `
resource "anyscale_cloud" "test" {
  name           = "deprecation-check"
  cloud_provider = "AWS"
  region         = "us-east-2"
  auto_add_user  = true
}
`, "All users group")
}

// Referencing a deprecated read-only attribute warns, even though it is never configured.
func TestAccOrganizationUserDataSource_DeprecatedAttributeReferenceWarns(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	expectOneDeprecationWarning(t, `
data "anyscale_organization_user" "test" {
  email = "a@example.com"
}

output "base_role" {
  value = data.anyscale_organization_user.test.base_role
}
`)
}

func TestAccCloudDataSource_DeprecatedAutoAddUserReferenceWarns(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	expectOneDeprecationWarning(t, `
data "anyscale_cloud" "test" {
  id = "cld_deprecation_check"
}

output "auto_add_user" {
  value = data.anyscale_cloud.test.auto_add_user
}
`)
}

// A configuration that uses no legacy RBAC surface gets no deprecation warning, including
// from a data source whose deprecated attributes are present but unreferenced.
func TestAccProjectResource_NoLegacyRBACDeprecationWarning(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	result := runTerraformValidate(t, buildProviderBinaryForCLICheck(t), `
resource "anyscale_project" "test" {
  name     = "deprecation-check"
  cloud_id = "cld_deprecation_check"
}

resource "anyscale_cloud" "test" {
  name           = "deprecation-check"
  cloud_provider = "AWS"
  region         = "us-east-2"
}

data "anyscale_organization_user" "test" {
  email = "a@example.com"
}

output "email" {
  value = data.anyscale_organization_user.test.email
}
`)
	if !result.Valid {
		t.Fatalf("validate failed; diagnostics: %+v", result.Diagnostics)
	}
	if got := deprecationWarnings(result); len(got) != 0 {
		t.Fatalf("got deprecation warnings %+v, want none", got)
	}
}
