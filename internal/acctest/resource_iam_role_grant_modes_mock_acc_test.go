package acctest

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// A 409 from role_bindings has several meanings, and only a duplicate grant
// may be reported as one with an import hint. These tests pin each meaning to
// its own diagnostic; the details are traced from the backend source
// (converted_organizations.py, role_bindings_service.py), not recorded live.

var (
	rbImportHint    = regexp.MustCompile(`(?i)terraform\s+import`)
	rbAlreadyExists = regexp.MustCompile(`(?i)already\s+exists`)
)

// rbErrorCheck returns an ErrorCheck that requires the step's error to match
// want and to match none of forbid, plus a function that fails the test if
// the step never errored. ExpectError cannot assert that text is absent.
func rbErrorCheck(t *testing.T, want *regexp.Regexp, forbid ...*regexp.Regexp) (func(error) error, func()) {
	t.Helper()
	called := false
	check := func(err error) error {
		called = true
		msg := err.Error()
		if !want.MatchString(msg) {
			return fmt.Errorf("error does not match %s: %w", want, err)
		}
		for _, f := range forbid {
			if f.MatchString(msg) {
				return fmt.Errorf("error must not match %s: %w", f, err)
			}
		}
		return nil
	}
	assertCalled := func() {
		t.Helper()
		if !called {
			t.Errorf("expected the step to error matching %s, but it did not error", want)
		}
	}
	return check, assertCalled
}

// Positive control for the absence checks below: the duplicate diagnostic
// does carry the import hint and "already exists", so a test that forbids
// them is matching text the provider really emits.
func TestAccIAMRoleGrantResource_ModesDuplicateKeepsImportHint(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	existing := m.seedBinding("user_group", rbGroup, rbRole, "cloud", rbCloud)
	check, assertCalled := rbErrorCheck(t,
		regexp.MustCompile(`Role Grant Already Exists`))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck: func(err error) error {
			if err := check(err); err != nil {
				return err
			}
			if !rbImportHint.MatchString(err.Error()) || !rbAlreadyExists.MatchString(err.Error()) || !strings.Contains(err.Error(), existing) {
				return fmt.Errorf("duplicate diagnostic lost its import hint: %w", err)
			}
			return nil
		},
		Steps: []resource.TestStep{{Config: rbConfig(m, rbRole)}},
	})
	assertCalled()
}

// An organization that has not converted refuses every role binding write
// with 409. That is not a duplicate: no import hint, and nothing is created.
func TestAccIAMRoleGrantResource_NotConvertedCreate(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	m.setUnconverted(true)
	check, assertCalled := rbErrorCheck(t,
		regexp.MustCompile(`Organization Not Converted to Role Bindings`),
		rbImportHint, rbAlreadyExists)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               check,
		CheckDestroy:             rbDestroyed(m),
		Steps:                    []resource.TestStep{{Config: rbConfig(m, rbRole)}},
	})
	assertCalled()
}

// The same refusal on destroy: the destroy fails with the conversion
// diagnostic and the binding stays. Once the refusal lifts, it destroys.
func TestAccIAMRoleGrantResource_NotConvertedDelete(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole)},
			{
				PreConfig:   func() { m.setUnconverted(true) },
				Config:      m.providerBlock(),
				ExpectError: regexp.MustCompile(`Organization Not Converted to Role Bindings`),
			},
			{
				PreConfig: func() {
					if m.bindingCount() != 1 {
						t.Errorf("refused DELETE removed the binding")
					}
					m.setUnconverted(false)
				},
				Config: m.providerBlock(),
				Check:  rbExpectBindings(m, 0),
			},
		},
	})
}

// An archived role cannot be newly assigned (409, not a duplicate).
func TestAccIAMRoleGrantResource_ArchivedRoleCreate(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	const archived = "rol_00000000000000000000000099"
	m.addRole(roleBindingMockRole{ID: archived, Name: "retired_role", Archived: true})
	check, assertCalled := rbErrorCheck(t,
		regexp.MustCompile(`Role Is Archived`),
		rbImportHint, rbAlreadyExists)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               check,
		CheckDestroy:             rbDestroyed(m),
		Steps:                    []resource.TestStep{{Config: rbConfig(m, archived)}},
	})
	assertCalled()
}

// A revoke of an organization admin group binding is refused; the
// diagnostic carries the API detail, says the platform owns the group's
// bindings, and the binding stays.
func TestAccIAMRoleGrantResource_AdminGroupDelete(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole)},
			{
				PreConfig: func() { m.setDeleteMode("admin_group") },
				Config:    m.providerBlock(),
				ExpectError: regexp.MustCompile(`(?s)Organization Admin Group Is Managed by Anyscale.*` +
					`The\s+organization\s+admin\s+group's\s+role\s+bindings\s+cannot\s+be\s+removed.*` +
					`platform-owned\s+and\s+cannot\s+be\s+revoked.*stays\s+in\s+Terraform\s+state.*terraform\s+state\s+rm`),
			},
			{
				PreConfig: func() {
					if m.bindingCount() != 1 {
						t.Errorf("refused DELETE removed the binding")
					}
					m.setDeleteMode("")
				},
				Config: m.providerBlock(),
				Check:  rbExpectBindings(m, 0),
			},
		},
	})
}

// A create that names the organization admin group is refused with the
// holds-only-platform-roles 409 and the admin-group diagnostic; it is never
// reported as a duplicate.
func TestAccIAMRoleGrantResource_AdminGroupCreateRefused(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	m.setCreateConflict(roleBindingMockAdminGroupRolesDetail)
	check, assertCalled := rbErrorCheck(t,
		regexp.MustCompile(`(?s)Organization Admin Group Is Managed by Anyscale.*`+
			`holds\s+only\s+the\s+roles\s+the\s+platform\s+binds\s+it\s+to.*`+
			`Remove\s+the\s+group\s+from\s+the\s+grant`),
		rbImportHint, rbAlreadyExists)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               check,
		CheckDestroy:             rbDestroyed(m),
		Steps:                    []resource.TestStep{{Config: rbConfig(m, rbRole)}},
	})
	assertCalled()
}

// A 409 the provider does not recognize passes through with the API detail
// and is never reported as a duplicate.
func TestAccIAMRoleGrantResource_UnrecognizedConflictPassesThrough(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	m.setCreateConflict("A refusal this provider has never seen.")
	check, assertCalled := rbErrorCheck(t,
		regexp.MustCompile(`A\s+refusal\s+this\s+provider\s+has\s+never\s+seen`),
		rbImportHint, rbAlreadyExists)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               check,
		CheckDestroy:             rbDestroyed(m),
		Steps:                    []resource.TestStep{{Config: rbConfig(m, rbRole)}},
	})
	assertCalled()
}

// ---- writes the API refuses because the caller does not manage IAM ----

// A create refused with the manage-IAM 403 (measured on the real API as
// "You do not manage IAM on <type> '<id>'.") reports lost IAM: the
// diagnostic names the operation and scope and says what identity to apply
// with - never a duplicate, never an import hint.
func TestAccIAMRoleGrantResource_ManageIAMOnCreate(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	orgID := "org_00000000000000000000000001"
	m.setCreateForbidden(fmt.Sprintf("You do not manage IAM on organization '%s'.", orgID))
	check, assertCalled := rbErrorCheck(t,
		regexp.MustCompile(`(?s)Not\s+Allowed\s+to\s+Manage\s+Access.*`+
			`You\s+do\s+not\s+manage\s+IAM\s+on\s+organization\s+'`+orgID+`'.*`+
			`creating\s+a\s+role\s+grant\s+on\s+organization\s+"`+orgID+`".*`+
			`requires\s+an\s+identity\s+that\s+manages\s+IAM\s+there.*`+
			`Apply\s+with\s+an\s+identity\s+that\s+manages\s+IAM\s+on\s+that\s+scope.*`+
			`Admin\s+role\s+there.*ask\s+an\s+admin.*If\s+the\s+scope\s+was\s+deleted`),
		rbImportHint, rbAlreadyExists)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               check,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{{
			Config: m.providerBlock() + fmt.Sprintf(`
resource "anyscale_iam_role_grant" "test" {
  user_group_id   = %q
  role_id         = %q
  organization_id = %q
}
`, rbGroup, rbRole, orgID),
		}},
	})
	assertCalled()
}

// A create that names a group the write route refuses as not found, while
// the group itself still reads back with GET ("User group 'ug_...' not
// found.", measured): lost IAM on the group, not a missing group, so the
// manage-access diagnostic gets the group wording.
func TestAccIAMRoleGrantResource_InvisibleGroupOnCreate(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	invisible := "ug_00000000000000000000000099"
	m.addInvisibleGroup(invisible)
	check, assertCalled := rbErrorCheck(t,
		regexp.MustCompile(`(?s)Not\s+Allowed\s+to\s+Manage\s+Access.*`+
			`User\s+group\s+'`+invisible+`'\s+not\s+found\..*`+
			`names\s+a\s+user\s+group\s+this\s+identity\s+cannot\s+bind\s+roles\s+for.*`+
			`Apply\s+with\s+an\s+identity\s+that\s+manages\s+IAM\s+on\s+that\s+scope.*ask\s+an\s+admin`),
		rbImportHint, rbAlreadyExists)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               check,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{{
			Config: m.providerBlock() + fmt.Sprintf(`
resource "anyscale_iam_role_grant" "test" {
  user_group_id = %q
  role_id       = %q
  cloud_id      = %q
}
`, invisible, rbRole, rbCloud),
		}},
	})
	assertCalled()
}

// A create that names a group which does not read back either keeps the
// ordinary 404 reporting: the raw API error, never the manage-access
// diagnostic.
func TestAccIAMRoleGrantResource_UnknownGroupOnCreate(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	unknown := "ug_00000000000000000000000098"
	check, assertCalled := rbErrorCheck(t,
		regexp.MustCompile(`(?s)Failed\s+to\s+create\s+role\s+binding\s+\(HTTP\s+404\).*`+
			`User\s+group\s+'`+unknown+`'\s+not\s+found`),
		regexp.MustCompile(`Not\s+Allowed\s+to\s+Manage\s+Access`))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               check,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{{
			Config: m.providerBlock() + fmt.Sprintf(`
resource "anyscale_iam_role_grant" "test" {
  user_group_id = %q
  role_id       = %q
  cloud_id      = %q
}
`, unknown, rbRole, rbCloud),
		}},
	})
	assertCalled()
}
