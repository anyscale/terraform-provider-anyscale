package acctest

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Read, Delete and Create error-path tests for anyscale_role_binding, run
// through resource.Test against roleBindingMockServer. They pin how the
// resource tells "the binding is gone" (remove from state) apart from "the
// feature is off" or "the caller lost access" (error, keep state), which every
// role_bindings route makes hard by answering 404 for the first and 403 for
// revoked binding IDs. The happy-path lifecycle is in
// resource_role_binding_mock_lifecycle_acc_test.go.

const (
	rbGroup = "ug_00000000000000000000000001"
	rbCloud = "cld_00000000000000000000000001"
	rbRole  = "rol_00000000000000000000000001"
)

var (
	rbFlagOffErr = regexp.MustCompile(`(?i)role bindings (are )?not enabled`)
	rbStateRmErr = regexp.MustCompile(`terraform state rm`)
)

func newRBMock(t *testing.T) *roleBindingMockServer {
	m := newRoleBindingMockServer(t)
	m.addGroup(rbGroup)
	m.addRole(roleBindingMockRole{ID: rbRole, Name: "cloud_viewer", BuiltIn: true})
	return m
}

func rbConfig(m *roleBindingMockServer, roleID string) string {
	return m.providerBlock() + fmt.Sprintf(`
resource "anyscale_role_binding" "test" {
  user_group_id = %q
  role_id       = %q
  cloud_id      = %q
}
`, rbGroup, roleID, rbCloud)
}

// rbCaptureID records the binding ID in state.
func rbCaptureID(id *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources["anyscale_role_binding.test"]
		if !ok {
			return fmt.Errorf("anyscale_role_binding.test not in state")
		}
		*id = rs.Primary.ID
		return nil
	}
}

func rbExpectBindings(m *roleBindingMockServer, n int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := m.bindingCount(); got != n {
			return fmt.Errorf("backend holds %d bindings, want %d", got, n)
		}
		return nil
	}
}

// rbExpectNoDelete asserts no DELETE was sent since the last takeWrites.
func rbExpectNoDelete(m *roleBindingMockServer) resource.TestCheckFunc {
	return func(*terraform.State) error {
		for _, w := range m.takeWrites() {
			if strings.HasPrefix(w, http.MethodDelete) {
				return fmt.Errorf("expected no DELETE, got %q", w)
			}
		}
		return nil
	}
}

func rbDestroyed(m *roleBindingMockServer) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if n := m.bindingCount(); n != 0 {
			return fmt.Errorf("%d bindings remain after destroy", n)
		}
		return nil
	}
}

// Flag off: every route 404s, including the probe. Read must error and keep
// state; step 3's empty plan proves the binding was not removed.
func TestAccRoleBindingResource_FlagOffKeepsState(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole), Check: rbExpectBindings(m, 1)},
			{
				PreConfig:   func() { m.setFlagOff(true) },
				Config:      rbConfig(m, rbRole),
				PlanOnly:    true,
				ExpectError: rbFlagOffErr,
			},
			{
				PreConfig: func() { m.setFlagOff(false); m.takeWrites() },
				Config:    rbConfig(m, rbRole),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeTestCheckFunc(rbExpectBindings(m, 1), rbExpectNoDelete(m)),
			},
		},
	})
}

// Revoked out of band: the listing omits the ID, so refresh removes it and
// the plan recreates it.
func TestAccRoleBindingResource_RevokedOutOfBandIsRecreated(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	var first, second string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole), Check: rbCaptureID(&first)},
			{
				PreConfig: func() { m.revoke(first) },
				Config:    rbConfig(m, rbRole),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_role_binding.test", plancheck.ResourceActionCreate),
					},
				},
				Check: resource.ComposeTestCheckFunc(
					rbCaptureID(&second),
					rbExpectBindings(m, 1),
					func(*terraform.State) error {
						if second == first || !m.bindingExists(second) {
							return fmt.Errorf("want a new live binding, got %q (was %q)", second, first)
						}
						return nil
					},
				),
			},
		},
	})
}

// Group deleted out of band, which cascades its bindings. Source does not
// settle whether the listing then answers with an empty page or a 404, so both
// are pinned: refresh removes the binding and sends no DELETE. In the 404 case
// the roles probe's 200 is what rules out the flag being off.
func TestAccRoleBindingResource_DeletedGroupIsRemoved(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	for _, tc := range []struct {
		name  string
		as404 bool
	}{{"empty page", false}, {"404 with roles probe 200", true}} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRBMock(t)
			m.setGroupGone404(tc.as404)
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				CheckDestroy:             rbDestroyed(m),
				Steps: []resource.TestStep{
					{Config: rbConfig(m, rbRole)},
					{
						PreConfig: func() { m.deleteGroup(rbGroup); m.takeWrites() },
						Config:    m.providerBlock(),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
						},
						Check: rbExpectNoDelete(m),
					},
				},
			})
		})
	}
}

// The listing 404s and the probe fails some other way: neither "gone" nor
// "flag off" is established, so Read must fail closed and keep state.
func TestAccRoleBindingResource_ProbeFailureFailsClosed(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	m.setGroupGone404(true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole)},
			{
				PreConfig:   func() { m.deleteGroup(rbGroup); m.setRolesStatus(http.StatusInternalServerError) },
				Config:      rbConfig(m, rbRole),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`500`),
			},
			{
				// State was kept: with the probe healthy, refresh now removes it.
				PreConfig: func() { m.setRolesStatus(0); m.takeWrites() },
				Config:    m.providerBlock(),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: rbExpectNoDelete(m),
			},
		},
	})
}

// A deleted cloud fails the listing's IAM check with 403 before any 404 the
// service could give. Read must error with the state-rm hint and keep state.
func TestAccRoleBindingResource_DeletedScopeKeepsStateWithHint(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole)},
			{
				PreConfig:   func() { m.deleteScope("cloud", rbCloud) },
				Config:      rbConfig(m, rbRole),
				PlanOnly:    true,
				ExpectError: rbStateRmErr,
			},
			{
				PreConfig: func() { m.restoreScope("cloud", rbCloud) },
				Config:    rbConfig(m, rbRole),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// Read walks every page. The managed binding is last of 61 on the scope, so a
// Read that checks only the first page (50) removes it and plans a create.
func TestAccRoleBindingResource_ReadWalksEveryPage(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	for i := range 60 {
		role := fmt.Sprintf("rol_%026d", 1000+i)
		m.addRole(roleBindingMockRole{ID: role, Name: fmt.Sprintf("filler_%d", i)})
		m.seedBinding("user_group", rbGroup, role, "cloud", rbCloud)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole), Check: rbExpectBindings(m, 61)},
			{
				Config: rbConfig(m, rbRole),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// A duplicate grant is 409. The error must carry an import hint naming the
// existing binding, found with one listing call, and nothing is adopted.
func TestAccRoleBindingResource_Duplicate409NamesExistingID(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	existing := m.seedBinding("user_group", rbGroup, rbRole, "cloud", rbCloud)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      rbConfig(m, rbRole),
				ExpectError: regexp.MustCompile(`(?s)(?i)already exists.*import.*` + existing),
			},
		},
	})
}

// If the listing that would name the existing binding fails, the 409 still
// errors with the generic import hint.
func TestAccRoleBindingResource_Duplicate409GenericHintWhenListingFails(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	m.seedBinding("user_group", rbGroup, rbRole, "cloud", rbCloud)
	m.setListingStatus(http.StatusInternalServerError)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      rbConfig(m, rbRole),
				ExpectError: regexp.MustCompile(`(?s)(?i)already exists.*import`),
			},
		},
	})
}

// Flag off at create: the flag-off diagnostic, not a generic 404.
func TestAccRoleBindingResource_FlagOffOnCreate(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	m.setFlagOff(true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole), ExpectError: rbFlagOffErr},
		},
	})
}

// DELETE answers 403 for a binding revoked between refresh and delete. The
// re-list finds it gone, so the destroy succeeds.
func TestAccRoleBindingResource_DeleteRevokedConcurrentlySucceeds(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole)},
			{
				PreConfig: func() { m.setDeleteMode("revoked") },
				Config:    m.providerBlock(),
				Check:     rbExpectBindings(m, 0),
			},
		},
	})
}

// DELETE answers 403 while the binding is still listed: a real permission
// loss. The destroy must fail, and the binding is still there.
func TestAccRoleBindingResource_DeleteDeniedErrors(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole)},
			{
				PreConfig:   func() { m.setDeleteMode("denied") },
				Config:      m.providerBlock(),
				ExpectError: regexp.MustCompile(`You may not revoke role binding`),
			},
			{
				PreConfig: func() {
					if m.bindingCount() != 1 {
						t.Errorf("denied DELETE removed the binding")
					}
					m.setDeleteMode("")
				},
				Config: m.providerBlock(),
				Check:  rbExpectBindings(m, 0),
			},
		},
	})
}

// Import of a binding held by a user, not a group, is refused.
func TestAccRoleBindingResource_ImportUserPrincipalRefused(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	userBinding := m.seedBinding("user", "usr_00000000000000000000000001", rbRole, "cloud", rbCloud)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        rbConfig(m, rbRole),
				ResourceName:  "anyscale_role_binding.test",
				ImportState:   true,
				ImportStateId: userBinding,
				ExpectError:   regexp.MustCompile(`(?i)user`),
			},
		},
	})
}

// Import maps resource_type to the one scope attribute that matches it.
func TestAccRoleBindingResource_ImportMapsScope(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	for _, scope := range []struct{ rt, attr, id string }{
		{"organization", "organization_id", "org_00000000000000000000000001"},
		{"cloud", "cloud_id", rbCloud},
		{"project", "project_id", "prj_00000000000000000000000001"},
	} {
		t.Run(scope.rt, func(t *testing.T) {
			m := newRBMock(t)
			id := m.seedBinding("user_group", rbGroup, rbRole, scope.rt, scope.id)
			cfg := m.providerBlock() + fmt.Sprintf(`
resource "anyscale_role_binding" "test" {
  user_group_id = %q
  role_id       = %q
  %s = %q
}
`, rbGroup, rbRole, scope.attr, scope.id)
			others := []string{"organization_id", "cloud_id", "project_id"}
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:        cfg,
						ResourceName:  "anyscale_role_binding.test",
						ImportState:   true,
						ImportStateId: id,
						ImportStateCheck: func(states []*terraform.InstanceState) error {
							if len(states) != 1 {
								return fmt.Errorf("want 1 imported instance, got %d", len(states))
							}
							a := states[0].Attributes
							if a[scope.attr] != scope.id {
								return fmt.Errorf("%s = %q, want %q", scope.attr, a[scope.attr], scope.id)
							}
							for _, o := range others {
								if o != scope.attr && a[o] != "" {
									return fmt.Errorf("%s = %q, want null", o, a[o])
								}
							}
							if a["user_group_id"] != rbGroup || a["role_id"] != rbRole {
								return fmt.Errorf("user_group_id/role_id = %q/%q", a["user_group_id"], a["role_id"])
							}
							return nil
						},
					},
				},
			})
		})
	}
}

// Exactly one scope attribute is required, checked at plan time with no API call.
func TestAccRoleBindingResource_ScopeExactlyOne(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	for name, scopes := range map[string]string{
		"none": "",
		"two":  fmt.Sprintf("cloud_id = %q\n  project_id = %q", rbCloud, "prj_00000000000000000000000001"),
	} {
		t.Run(name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config: m.providerBlock() + fmt.Sprintf(`
resource "anyscale_role_binding" "test" {
  user_group_id = %q
  role_id       = %q
  %s
}
`, rbGroup, rbRole, scopes),
						PlanOnly:    true,
						ExpectError: regexp.MustCompile(`(?i)(exactly one|invalid attribute combination)`),
					},
				},
			})
		})
	}
	if w := m.takeWrites(); len(w) != 0 {
		t.Errorf("plan-time validation sent writes: %q", w)
	}
}

// ---- anyscale_role data source ----

func roleDSConfig(m *roleBindingMockServer, arg, value string) string {
	return m.providerBlock() + fmt.Sprintf(`
data "anyscale_role" "test" {
  %s = %q
}
`, arg, value)
}

func newRoleMock(t *testing.T) *roleBindingMockServer {
	m := newRoleBindingMockServer(t)
	desc := "Read clouds."
	m.addRole(roleBindingMockRole{ID: "rol_00000000000000000000000010", Name: "viewer", Description: &desc, BuiltIn: true, Allowed: []string{"cloud.get", "cloud.list"}, Denied: []string{}})
	m.addRole(roleBindingMockRole{ID: "rol_00000000000000000000000011", Name: "viewer_plus"})
	m.addRole(roleBindingMockRole{ID: "rol_00000000000000000000000012", Name: "old_role", Archived: true})
	return m
}

// By name: the exact match among the listing's "contains" results.
func TestAccRoleDataSource_ByNameExact(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRoleMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: roleDSConfig(m, "name", "viewer"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.anyscale_role.test", tfjsonpath.New("id"), knownvalue.StringExact("rol_00000000000000000000000010")),
					statecheck.ExpectKnownValue("data.anyscale_role.test", tfjsonpath.New("built_in"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("data.anyscale_role.test", tfjsonpath.New("description"), knownvalue.StringExact("Read clouds.")),
				},
			},
		},
	})
}

// Description is null, not "", when the role has none.
func TestAccRoleDataSource_NullDescription(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRoleMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: roleDSConfig(m, "name", "viewer_plus"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.anyscale_role.test", tfjsonpath.New("description"), knownvalue.Null()),
				},
			},
		},
	})
}

// Match is case-sensitive; a case-only mismatch errors and names the role.
func TestAccRoleDataSource_CaseMismatchHint(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRoleMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: roleDSConfig(m, "name", "Viewer"), ExpectError: regexp.MustCompile(`(?i)did you mean.*"?viewer"?`)},
		},
	})
}

// An archived role cannot be assigned, so lookup by name excludes it.
func TestAccRoleDataSource_ArchivedExcludedByName(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRoleMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: roleDSConfig(m, "name", "old_role"), ExpectError: regexp.MustCompile(`(?i)not found|no role`)},
		},
	})
}

// The name filter is "contains", so the exact match can sit past page one.
func TestAccRoleDataSource_ByNameWalksEveryPage(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRoleBindingMockServer(t)
	for i := range 60 {
		m.addRole(roleBindingMockRole{ID: fmt.Sprintf("rol_%026d", i), Name: fmt.Sprintf("operator_%02d", i)})
	}
	m.addRole(roleBindingMockRole{ID: "rol_zzzzzzzzzzzzzzzzzzzzzzzzzz", Name: "operator"})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: roleDSConfig(m, "name", "operator"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.anyscale_role.test", tfjsonpath.New("id"), knownvalue.StringExact("rol_zzzzzzzzzzzzzzzzzzzzzzzzzz")),
				},
			},
		},
	})
}

func TestAccRoleDataSource_FlagOff(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRoleMock(t)
	m.setFlagOff(true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: roleDSConfig(m, "name", "viewer"), ExpectError: rbFlagOffErr},
		},
	})
}
