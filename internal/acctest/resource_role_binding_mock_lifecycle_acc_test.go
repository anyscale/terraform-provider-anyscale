package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Happy-path lifecycle tests for anyscale_role_binding and anyscale_role,
// run through resource.Test against roleBindingMockServer. They need no
// credentials. The read contract (revoked vs feature off vs 403 vs 409) is
// covered in resource_role_binding_readcontract_mock_acc_test.go.

const (
	rbMockGroup   = "ug_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	rbMockCloud   = "cld_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	rbMockProject = "prj_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	rbMockViewer  = "rol_viewer00000000000000000000"
	rbMockWriter  = "rol_writer00000000000000000000"
)

func newRoleBindingLifecycleMock(t *testing.T) *roleBindingMockServer {
	m := newRoleBindingMockServer(t)
	m.addGroup(rbMockGroup)
	desc := "Reads projects on a cloud."
	m.addRole(roleBindingMockRole{ID: rbMockViewer, Name: "project_viewer", Description: &desc, BuiltIn: true})
	m.addRole(roleBindingMockRole{ID: rbMockWriter, Name: "writer", BuiltIn: true})
	return m
}

// rbMockConfig binds the group to the role found by name, on one scope given
// as an HCL attribute line.
func rbMockConfig(m *roleBindingMockServer, roleName, scope string) string {
	return m.providerBlock() + fmt.Sprintf(`
data "anyscale_role" "test" {
  name = %q
}

resource "anyscale_role_binding" "test" {
  user_group_id = %q
  role_id       = data.anyscale_role.test.id
  %s
}
`, roleName, rbMockGroup, scope)
}

// rbExpectBackend asserts the mock holds exactly the binding in state, with
// the given role and scope. It checks the backend, not state, so an apply
// that only planned a change cannot pass it.
func rbExpectBackend(m *roleBindingMockServer, roleID, resourceType, resourceID string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources["anyscale_role_binding.test"]
		if !ok {
			return fmt.Errorf("anyscale_role_binding.test not in state")
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		b, ok := m.bindings[rs.Primary.ID]
		if !ok {
			return fmt.Errorf("binding %s in state is not held by the backend", rs.Primary.ID)
		}
		if len(m.bindings) != 1 {
			return fmt.Errorf("backend holds %d bindings, want 1", len(m.bindings))
		}
		if b.PrincipalType != "user_group" || b.PrincipalID != rbMockGroup || b.RoleID != roleID || b.ResourceType != resourceType || b.ResourceID != resourceID {
			return fmt.Errorf("backend binding = %s %s %s on %s %s, want user_group %s %s on %s %s",
				b.PrincipalType, b.PrincipalID, b.RoleID, b.ResourceType, b.ResourceID,
				rbMockGroup, roleID, resourceType, resourceID)
		}
		return nil
	}
}

func TestAccRoleBindingResource_MockLifecycle(t *testing.T) {
	m := newRoleBindingLifecycleMock(t)
	cloudScope := fmt.Sprintf("cloud_id = %q", rbMockCloud)
	projectScope := fmt.Sprintf("project_id = %q", rbMockProject)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if n := m.bindingCount(); n != 0 {
				return fmt.Errorf("backend still holds %d bindings after destroy", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: rbMockConfig(m, "project_viewer", cloudScope),
				Check:  rbExpectBackend(m, rbMockViewer, "cloud", rbMockCloud),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.anyscale_role.test", tfjsonpath.New("id"), knownvalue.StringExact(rbMockViewer)),
					statecheck.ExpectKnownValue("data.anyscale_role.test", tfjsonpath.New("built_in"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("data.anyscale_role.test", tfjsonpath.New("description"), knownvalue.StringExact("Reads projects on a cloud.")),
					statecheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("id"), knownvalue.StringRegexp(regexp.MustCompile(`^rb_`))),
					statecheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("cloud_id"), knownvalue.StringExact(rbMockCloud)),
					statecheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("project_id"), knownvalue.Null()),
					statecheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("organization_id"), knownvalue.Null()),
					statecheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("origin"), knownvalue.StringExact("imperative")),
					statecheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("created_by"), knownvalue.StringExact(roleBindingMockCaller)),
					statecheck.ExpectKnownValue("anyscale_role_binding.test", tfjsonpath.New("created_at"), knownvalue.NotNull()),
				},
			},
			{
				// Re-plan of the same config is empty: Read refreshes without
				// diffs.
				Config: rbMockConfig(m, "project_viewer", cloudScope),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Import by binding ID recovers every attribute, with no ignores.
				ResourceName:      "anyscale_role_binding.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// A different role replaces the binding.
				Config: rbMockConfig(m, "writer", cloudScope),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_role_binding.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: rbExpectBackend(m, rbMockWriter, "cloud", rbMockCloud),
			},
			{
				// A different scope replaces the binding.
				Config: rbMockConfig(m, "writer", projectScope),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_role_binding.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: rbExpectBackend(m, rbMockWriter, "project", rbMockProject),
			},
		},
	})
}

func TestAccRoleBindingResource_MockScopeValidation(t *testing.T) {
	m := newRoleBindingLifecycleMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      rbMockConfig(m, "project_viewer", ""),
				ExpectError: regexp.MustCompile(`(?s)Missing Attribute Configuration.*organization_id.*cloud_id.*project_id`),
			},
			{
				Config:      rbMockConfig(m, "project_viewer", fmt.Sprintf("cloud_id = %q\n  project_id = %q", rbMockCloud, rbMockProject)),
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
		},
	})
	if writes := m.takeWrites(); len(writes) != 0 {
		t.Errorf("plan-time validation failures still wrote to the API: %v", writes)
	}
}

func TestAccRoleDataSource_MockCaseMismatch(t *testing.T) {
	m := newRoleBindingLifecycleMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: m.providerBlock() + `
data "anyscale_role" "test" {
  name = "Project_Viewer"
}
`,
				ExpectError: regexp.MustCompile(`(?s)No assignable role is named "Project_Viewer".*did you mean "project_viewer"`),
			},
		},
	})
}

// A binding on a scope the resource does not model must be refused at
// import, never recorded as an organization binding: Read would then list
// the wrong scope and drop it, or the next apply would grant it on the org.
func TestAccRoleBindingResource_MockImportUnsupportedScope(t *testing.T) {
	m := newRoleBindingLifecycleMock(t)
	id := m.seedBinding("user_group", rbMockGroup, rbMockViewer, "dataset", "dst_aaaaaaaaaaaaaaaaaaaaaaaaaa")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: m.providerBlock() + fmt.Sprintf(`
resource "anyscale_role_binding" "test" {
  user_group_id   = %q
  role_id         = %q
  organization_id = "org_aaaaaaaaaaaaaaaaaaaaaaaaaa"
}
`, rbMockGroup, rbMockViewer),
				ResourceName:  "anyscale_role_binding.test",
				ImportState:   true,
				ImportStateId: id,
				ExpectError:   regexp.MustCompile(`(?s)Role Binding Scope Not Supported.*held on a dataset`),
			},
		},
	})
}
