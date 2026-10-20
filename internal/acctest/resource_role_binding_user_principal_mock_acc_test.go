package acctest

import (
	"fmt"
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

// anyscale_role_binding with a user principal (user_id), run against
// roleBindingMockServer. Exactly one of user_group_id and user_id is set;
// the other is null in state, including after import.

const (
	rbUserA = "usr_00000000000000000000000001"
	rbUserB = "usr_00000000000000000000000002"
)

func rbUserConfig(m *roleBindingMockServer, userID string) string {
	return m.providerBlock() + fmt.Sprintf(`
resource "anyscale_role_binding" "test" {
  user_id  = %q
  role_id  = %q
  cloud_id = %q
}
`, userID, rbRole, rbCloud)
}

// rbExpectPrincipal asserts the backend holds exactly one binding, granted to
// the given principal.
func rbExpectPrincipal(m *roleBindingMockServer, principalType, principalID string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.bindings) != 1 {
			return fmt.Errorf("backend holds %d bindings, want 1", len(m.bindings))
		}
		for _, b := range m.bindings {
			if b.PrincipalType != principalType || b.PrincipalID != principalID {
				return fmt.Errorf("backend binding is %s %s, want %s %s", b.PrincipalType, b.PrincipalID, principalType, principalID)
			}
		}
		return nil
	}
}

func rbUserStateChecks(userID string) []statecheck.StateCheck {
	return []statecheck.StateCheck{
		statecheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("user_id"), knownvalue.StringExact(userID)),
		statecheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("user_group_id"), knownvalue.Null()),
	}
}

// Create, refresh, import and destroy a user binding. The create sends
// principal_type "user", the re-plan is empty (Read finds it through the
// user principal listing), and import recovers user_id with user_group_id
// null.
func TestAccRoleBindingResource_UserPrincipalLifecycle(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{
				Config:            rbUserConfig(m, rbUserA),
				Check:             rbExpectPrincipal(m, "user", rbUserA),
				ConfigStateChecks: rbUserStateChecks(rbUserA),
			},
			{
				Config: rbUserConfig(m, rbUserA),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName:      "anyscale_role_binding.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// A user binding revoked out of band is removed from state on refresh and
// recreated: Read follows user_id to the user principal listing.
func TestAccRoleBindingResource_UserPrincipalRevokedOutOfBandIsRecreated(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	var first string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbUserConfig(m, rbUserA), Check: rbCaptureID(&first)},
			{
				PreConfig: func() { m.revoke(first) },
				Config:    rbUserConfig(m, rbUserA),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_role_binding.test", plancheck.ResourceActionCreate),
					},
				},
				Check: rbExpectPrincipal(m, "user", rbUserA),
			},
		},
	})
}

// Importing a binding created outside Terraform recovers the user principal.
// Asserted inside the import step, which is where the imported values live.
func TestAccRoleBindingResource_UserPrincipalColdImport(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	id := m.seedBinding("user", rbUserA, rbRole, "cloud", rbCloud)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        rbUserConfig(m, rbUserA),
				ResourceName:  "anyscale_role_binding.test",
				ImportState:   true,
				ImportStateId: id,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d instances, want 1", len(states))
					}
					a := states[0].Attributes
					if a["user_id"] != rbUserA {
						return fmt.Errorf("user_id = %q, want %q", a["user_id"], rbUserA)
					}
					if v, ok := a["user_group_id"]; ok && v != "" {
						return fmt.Errorf("user_group_id = %q, want null", v)
					}
					if a["cloud_id"] != rbCloud {
						return fmt.Errorf("cloud_id = %q, want %q", a["cloud_id"], rbCloud)
					}
					return nil
				},
			},
		},
	})
}

// A group binding imported after the change still maps to user_group_id with
// user_id null.
func TestAccRoleBindingResource_GroupPrincipalColdImportLeavesUserNull(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	id := m.seedBinding("user_group", rbGroup, rbRole, "cloud", rbCloud)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        rbConfig(m, rbRole),
				ResourceName:  "anyscale_role_binding.test",
				ImportState:   true,
				ImportStateId: id,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					a := states[0].Attributes
					if a["user_group_id"] != rbGroup {
						return fmt.Errorf("user_group_id = %q, want %q", a["user_group_id"], rbGroup)
					}
					if v, ok := a["user_id"]; ok && v != "" {
						return fmt.Errorf("user_id = %q, want null", v)
					}
					return nil
				},
			},
		},
	})
}

// Exactly one principal: both, or neither, is rejected before any request.
func TestAccRoleBindingResource_PrincipalExactlyOne(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	both := m.providerBlock() + fmt.Sprintf(`
resource "anyscale_role_binding" "test" {
  user_group_id = %q
  user_id       = %q
  role_id       = %q
  cloud_id      = %q
}
`, rbGroup, rbUserA, rbRole, rbCloud)
	neither := m.providerBlock() + fmt.Sprintf(`
resource "anyscale_role_binding" "test" {
  role_id  = %q
  cloud_id = %q
}
`, rbRole, rbCloud)
	// The framework's ExactlyOneOf config validator reports the two cases
	// under different summaries (configvalidator/exactly_one_of.go).
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: both, ExpectError: regexp.MustCompile(`Invalid Attribute Combination`)},
			{Config: neither, ExpectError: regexp.MustCompile(`Missing Attribute Configuration`)},
		},
	})
	if w := m.takeWrites(); len(w) != 0 {
		t.Errorf("invalid principal config reached the API: %v", w)
	}
}

// Changing the user, or switching between a group and a user, replaces the
// binding: there is no in-place update of a grant.
func TestAccRoleBindingResource_PrincipalChangeReplaces(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	replace := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{
			plancheck.ExpectResourceAction("anyscale_role_binding.test", plancheck.ResourceActionDestroyBeforeCreate),
		},
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole), Check: rbExpectPrincipal(m, "user_group", rbGroup)},
			{
				Config:            rbUserConfig(m, rbUserA),
				ConfigPlanChecks:  replace,
				Check:             rbExpectPrincipal(m, "user", rbUserA),
				ConfigStateChecks: rbUserStateChecks(rbUserA),
			},
			{
				Config:            rbUserConfig(m, rbUserB),
				ConfigPlanChecks:  replace,
				Check:             rbExpectPrincipal(m, "user", rbUserB),
				ConfigStateChecks: rbUserStateChecks(rbUserB),
			},
			{
				Config:           rbConfig(m, rbRole),
				ConfigPlanChecks: replace,
				Check:            rbExpectPrincipal(m, "user_group", rbGroup),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("user_id"), knownvalue.Null()),
				},
			},
		},
	})
}

// Changing the group replaces the binding, as it did before user principals.
func TestAccRoleBindingResource_GroupChangeReplaces(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	const otherGroup = "ug_00000000000000000000000002"
	m.addGroup(otherGroup)
	other := strings.Replace(rbConfig(m, rbRole), rbGroup, otherGroup, 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbConfig(m, rbRole), Check: rbExpectPrincipal(m, "user_group", rbGroup)},
			{
				Config: other,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_role_binding.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: rbExpectPrincipal(m, "user_group", otherGroup),
			},
		},
	})
}

// The API refuses a grant to the caller's own user (403). The diagnostic
// names the provider's identity and the way forward, and nothing is created.
func TestAccRoleBindingResource_SelfGrantRefused(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{
				Config:      rbUserConfig(m, roleBindingMockCaller),
				ExpectError: regexp.MustCompile(`(?s)Cannot Grant a Role to the Provider's Own Identity.*different\s+identity`),
			},
		},
	})
}

// The API refuses to revoke the caller's own binding (403). Destroy fails
// with a diagnostic that offers terraform state rm, and the binding stays.
func TestAccRoleBindingResource_SelfRevokeRefused(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	m.setAllowSelf(true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             rbDestroyed(m),
		Steps: []resource.TestStep{
			{Config: rbUserConfig(m, roleBindingMockCaller)},
			{
				PreConfig:   func() { m.setAllowSelf(false) },
				Config:      m.providerBlock(),
				ExpectError: regexp.MustCompile(`(?s)Cannot Revoke the Provider's Own Role Binding.*terraform\s+state\s+rm`),
			},
			{
				PreConfig: func() {
					if m.bindingCount() != 1 {
						t.Errorf("refused DELETE removed the binding")
					}
					m.setAllowSelf(true)
				},
				Config: m.providerBlock(),
				Check:  rbExpectBindings(m, 0),
			},
		},
	})
}

// Upgrade from v0.30.0, the last release before user principals: a group
// binding created by that release plans no changes under this build. Step 1
// is the positive control that the external provider really is v0.30.0 (it
// rejects user_id); without it, a dev_overrides CLI config that substitutes
// the local build would make this test pass without testing an upgrade.
// Needs the public Registry to download v0.30.0.
func TestAccRoleBindingResource_UpgradeFromV030GroupBindingPlansNoop(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	v030 := map[string]resource.ExternalProvider{
		"anyscale": {Source: "anyscale/anyscale", VersionConstraint: "0.30.0"},
	}
	resource.Test(t, resource.TestCase{
		CheckDestroy: rbDestroyed(m),
		Steps: []resource.TestStep{
			{
				ExternalProviders: v030,
				Config:            rbUserConfig(m, rbUserA),
				ExpectError:       regexp.MustCompile(`(?s)Unsupported argument.*user_id`),
			},
			{
				ExternalProviders: v030,
				Config:            rbConfig(m, rbRole),
				Check:             rbExpectPrincipal(m, "user_group", rbGroup),
			},
			{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Config:                   rbConfig(m, rbRole),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("user_group_id"), knownvalue.StringExact(rbGroup)),
					statecheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("user_id"), knownvalue.Null()),
				},
			},
		},
	})
}
