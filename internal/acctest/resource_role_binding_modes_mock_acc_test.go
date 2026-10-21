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
func TestAccRoleBindingResource_ModesDuplicateKeepsImportHint(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	existing := m.seedBinding("user_group", rbGroup, rbRole, "cloud", rbCloud)
	check, assertCalled := rbErrorCheck(t,
		regexp.MustCompile(`Role Binding Already Exists`))
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
func TestAccRoleBindingResource_NotConvertedCreate(t *testing.T) {
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
func TestAccRoleBindingResource_NotConvertedDelete(t *testing.T) {
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
func TestAccRoleBindingResource_ArchivedRoleCreate(t *testing.T) {
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

// A revoke that would leave the organization without an Organization owner
// is refused; the diagnostic carries the API detail and the remedy, and the
// binding stays.
func TestAccRoleBindingResource_LockoutDelete(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole)},
			{
				PreConfig: func() { m.setDeleteMode("lockout") },
				Config:    m.providerBlock(),
				ExpectError: regexp.MustCompile(`(?s)Change Would Leave Organization Without an Owner.*` +
					`without\s+an\s+Organization\s+owner.*keep\s+at\s+least\s+one\s+Organization\s+owner\s+binding`),
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

// A 409 the provider does not recognize passes through with the API detail
// and is never reported as a duplicate.
func TestAccRoleBindingResource_UnrecognizedConflictPassesThrough(t *testing.T) {
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
