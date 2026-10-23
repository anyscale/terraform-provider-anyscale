package acctest

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Changing ray_version on a named image against the real API: the new build lands on the
// same cluster environment at revision 2, no environment is recreated (a replace would
// 409 on the archived name), and a refresh plans clean. The mock-server tests prove what
// the provider sends; this proves the backend accepts it.
func TestAccContainerImageRegistryResource_RayVersionUpdate_RealAPI(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	const addr = "anyscale_container_image_registry.test"
	name := UniqueName(t, "img-registry-ray-update")
	// Fake ECR-style URI: the API accepts the format even if the image doesn't exist.
	const image = "123456789012.dkr.ecr.us-west-2.amazonaws.com/my-ray-image:v1"
	config := func(rayVersion string) string {
		return fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name        = %q
  image_uri   = %q
  ray_version = %q
}
`, name, image, rayVersion)
	}

	var firstID, firstBuildID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIArchivedDestroyCheck("anyscale_container_image_registry", "/api/v2/application_templates/%s", "result.archived_at"),
		Steps: []resource.TestStep{
			{
				Config: config("2.44.0"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "revision", "1"),
					resource.TestCheckResourceAttr(addr, "ray_version", "2.44.0"),
					func(s *terraform.State) error {
						rs := s.RootModule().Resources[addr]
						firstID, firstBuildID = rs.Primary.ID, rs.Primary.Attributes["build_id"]
						return nil
					},
				),
			},
			{
				Config: config("2.43.0"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", name),
					resource.TestCheckResourceAttr(addr, "ray_version", "2.43.0"),
					resource.TestCheckResourceAttr(addr, "revision", "2"),
					func(s *terraform.State) error {
						rs := s.RootModule().Resources[addr]
						if rs.Primary.ID != firstID {
							return fmt.Errorf("id changed from %q to %q: the environment was replaced", firstID, rs.Primary.ID)
						}
						if rs.Primary.Attributes["build_id"] == firstBuildID {
							return fmt.Errorf("build_id is still %q: no new build was registered", firstBuildID)
						}
						rev, _ := strconv.Atoi(rs.Primary.Attributes["revision"])
						return testAccCheckRegistryLatestBuildInAPI(firstID, rs.Primary.Attributes["build_id"], rev)
					},
				),
			},
		},
	})
}
