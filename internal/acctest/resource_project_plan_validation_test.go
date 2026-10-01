package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// These run against newMockProjectServer, which rewrites names and empty
// descriptions the way the API does. Each rejected value would otherwise
// create the project and then fail the apply as an inconsistent result; each
// accepted control goes through the same mock and must apply cleanly.

func projectValidationConfig(serverURL, name, descriptionLine string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_project" "test" {
  name     = %q
  cloud_id = "cld_mock_validation"
  %s
}
`, name, descriptionLine)
}

func TestProjectResource_NameTheAPIWouldRewriteIsRejectedAtPlan(t *testing.T) {
	for _, name := range []string{"my project", "a--b", "a.b", "café", " lead"} {
		t.Run(name, func(t *testing.T) {
			server := newMockProjectServer(t)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      projectValidationConfig(server.URL, name, ""),
					ExpectError: regexp.MustCompile(`must contain only ASCII letters, digits,\s+underscores and\s+single hyphens`),
				}},
			})
		})
	}
}

func TestProjectResource_NameTheAPIKeepsIsAccepted(t *testing.T) {
	for _, name := range []string{"my-project", "My_Project_2", "-a-"} {
		t.Run(name, func(t *testing.T) {
			server := newMockProjectServer(t)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: projectValidationConfig(server.URL, name, ""),
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_project.test", plancheck.ResourceActionCreate)},
						PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
					},
					Check: resource.TestCheckResourceAttr("anyscale_project.test", "name", name),
				}},
			})
		})
	}
}

func TestProjectResource_EmptyDescriptionIsRejectedAtPlan(t *testing.T) {
	server := newMockProjectServer(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      projectValidationConfig(server.URL, "empty-description", `description = ""`),
			ExpectError: regexp.MustCompile(`string length must be at least 1`),
		}},
	})
}

func TestProjectResource_DescriptionSetOrOmittedApplies(t *testing.T) {
	for label, line := range map[string]string{"set": `description = "kept as written"`, "omitted": ""} {
		t.Run(label, func(t *testing.T) {
			server := newMockProjectServer(t)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: projectValidationConfig(server.URL, "description-"+label, line),
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_project.test", plancheck.ResourceActionCreate)},
						PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
					},
				}},
			})
		})
	}
}
