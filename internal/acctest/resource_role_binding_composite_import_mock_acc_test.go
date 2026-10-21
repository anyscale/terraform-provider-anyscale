package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Import of anyscale_role_binding by its natural key,
// <scope_type>/<scope_id>/<principal_type>/<principal_id>/<role_id>, in
// addition to the rb_ id. The key resolves to the existing binding's rb_ id;
// import never creates one. rb_ import stays covered by
// TestAccRoleBindingResource_ImportMapsScope and _MockLifecycle.

var rbCompositeFormatErr = regexp.MustCompile(`(?s)rb_.*<scope_type>/<scope_id>/<principal_type>/<principal_id>/<role_id>`)

func rbScopedConfig(m *roleBindingMockServer, principalAttr, principalID, scopeAttr, scopeID string) string {
	return m.providerBlock() + fmt.Sprintf(`
resource "anyscale_role_binding" "test" {
  %s = %q
  role_id = %q
  %s = %q
}
`, principalAttr, principalID, rbRole, scopeAttr, scopeID)
}

// Test A: the composite key recovers the existing binding, for each principal
// type and scope type. Asserted inside the import step, where the imported
// values live; the other principal attribute is null.
func TestAccRoleBindingResource_CompositeImportRecoversBinding(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	cases := []struct {
		name, principalType, principalAttr, principalID, otherAttr, scopeType, scopeAttr, scopeID string
	}{
		{"group_cloud", "user_group", "user_group_id", rbGroup, "user_id", "cloud", "cloud_id", rbCloud},
		{"user_project", "user", "user_id", rbUserA, "user_group_id", "project", "project_id", "prj_00000000000000000000000001"},
		{"group_organization", "user_group", "user_group_id", rbGroup, "user_id", "organization", "organization_id", "org_00000000000000000000000001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newRBMock(t)
			want := m.seedBinding(tc.principalType, tc.principalID, rbRole, tc.scopeType, tc.scopeID)
			key := fmt.Sprintf("%s/%s/%s/%s/%s", tc.scopeType, tc.scopeID, tc.principalType, tc.principalID, rbRole)
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:        rbScopedConfig(m, tc.principalAttr, tc.principalID, tc.scopeAttr, tc.scopeID),
						ResourceName:  "anyscale_role_binding.test",
						ImportState:   true,
						ImportStateId: key,
						ImportStateCheck: func(states []*terraform.InstanceState) error {
							if len(states) != 1 {
								return fmt.Errorf("imported %d instances, want 1", len(states))
							}
							a := states[0].Attributes
							for k, v := range map[string]string{
								"id": want, tc.principalAttr: tc.principalID, "role_id": rbRole, tc.scopeAttr: tc.scopeID,
							} {
								if a[k] != v {
									return fmt.Errorf("%s = %q, want %q", k, a[k], v)
								}
							}
							if v, ok := a[tc.otherAttr]; ok && v != "" {
								return fmt.Errorf("%s = %q, want null", tc.otherAttr, v)
							}
							return nil
						},
					},
				},
			})
			if w := m.takeWrites(); len(w) != 0 {
				t.Errorf("import wrote to the API: %v", w)
			}
		})
	}
}

// The import-block form with a composite id, as a practitioner would write
// it: the plan imports the existing binding with no changes.
func TestAccRoleBindingResource_CompositeImportBlock(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	want := m.seedBinding("user_group", rbGroup, rbRole, "cloud", rbCloud)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:    "anyscale_role_binding.test",
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
				ImportStateId:   "anyscale_role_binding.test",
				Config: rbConfig(m, rbRole) + fmt.Sprintf(`
import {
  to = anyscale_role_binding.test
  id = "cloud/%s/user_group/%s/%s"
}
`, rbCloud, rbGroup, rbRole),
				ImportStateConfigExact: true,
				ImportPlanChecks: resource.ImportPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_role_binding.test", plancheck.ResourceActionNoop),
						plancheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("id"), knownvalue.StringExact(want)),
						plancheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("user_id"), knownvalue.Null()),
					},
				},
			},
		},
	})
}

// The principal holds a different role on the scope: no binding matches, so
// import fails and nothing is created.
func TestAccRoleBindingResource_CompositeImportNoMatch(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	m.seedBinding("user_group", rbGroup, "rol_00000000000000000000000077", "cloud", rbCloud)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        rbConfig(m, rbRole),
				ResourceName:  "anyscale_role_binding.test",
				ImportState:   true,
				ImportStateId: fmt.Sprintf("cloud/%s/user_group/%s/%s", rbCloud, rbGroup, rbRole),
				ExpectError:   regexp.MustCompile(`(?i)no\s+role\s+binding\s+grants`),
			},
		},
	})
	if w := m.takeWrites(); len(w) != 0 {
		t.Errorf("a failed import wrote to the API: %v", w)
	}
	if n := m.bindingCount(); n != 1 {
		t.Errorf("backend holds %d bindings after a failed import, want 1", n)
	}
}

// The match is found past the first page of the principal listing.
func TestAccRoleBindingResource_CompositeImportWalksEveryPage(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	for i := 0; i < 60; i++ {
		m.seedBinding("user_group", rbGroup, fmt.Sprintf("rol_%026d", 100+i), "cloud", rbCloud)
	}
	// Seeded last, so it sorts after the 60 decoys and lands on page 2.
	want := m.seedBinding("user_group", rbGroup, rbRole, "cloud", rbCloud)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        rbConfig(m, rbRole),
				ResourceName:  "anyscale_role_binding.test",
				ImportState:   true,
				ImportStateId: fmt.Sprintf("cloud/%s/user_group/%s/%s", rbCloud, rbGroup, rbRole),
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if got := states[0].Attributes["id"]; got != want {
						return fmt.Errorf("id = %q, want %q", got, want)
					}
					return nil
				},
			},
		},
	})
}

// With the feature off, a composite import gets the flag-off diagnostic, as
// Read does, not a no-match error.
func TestAccRoleBindingResource_CompositeImportFlagOff(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	m.setFlagOff(true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        rbConfig(m, rbRole),
				ResourceName:  "anyscale_role_binding.test",
				ImportState:   true,
				ImportStateId: fmt.Sprintf("cloud/%s/user_group/%s/%s", rbCloud, rbGroup, rbRole),
				ExpectError:   rbFlagOffErr,
			},
		},
	})
}

// A malformed id is a config error that names both accepted formats, and
// reaches no write route.
func TestAccRoleBindingResource_CompositeImportMalformed(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	for name, id := range map[string]string{
		"four_parts":      fmt.Sprintf("cloud/%s/user_group/%s", rbCloud, rbGroup),
		"six_parts":       fmt.Sprintf("cloud/%s/user_group/%s/%s/extra", rbCloud, rbGroup, rbRole),
		"empty_part":      fmt.Sprintf("cloud//user_group/%s/%s", rbGroup, rbRole),
		"bad_scope_type":  fmt.Sprintf("workspace/%s/user_group/%s/%s", rbCloud, rbGroup, rbRole),
		"bad_principal":   fmt.Sprintf("cloud/%s/service_account/%s/%s", rbCloud, rbGroup, rbRole),
		"not_an_id":       "garbage",
		"trailing_slash":  fmt.Sprintf("cloud/%s/user_group/%s/%s/", rbCloud, rbGroup, rbRole),
		"plural_scope":    fmt.Sprintf("clouds/%s/user_group/%s/%s", rbCloud, rbGroup, rbRole),
		"principal_users": fmt.Sprintf("cloud/%s/users/%s/%s", rbCloud, rbUserA, rbRole),
	} {
		t.Run(name, func(t *testing.T) {
			m := newRBMock(t)
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:        rbConfig(m, rbRole),
						ResourceName:  "anyscale_role_binding.test",
						ImportState:   true,
						ImportStateId: id,
						ExpectError:   rbCompositeFormatErr,
					},
				},
			})
			if w := m.takeWrites(); len(w) != 0 {
				t.Errorf("malformed import wrote to the API: %v", w)
			}
		})
	}
}
