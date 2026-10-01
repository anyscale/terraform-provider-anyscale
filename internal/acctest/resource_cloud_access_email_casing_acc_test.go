package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// A member key written with capitals must plan clean after apply. The API
// lowercases emails; re-keying state to its spelling made every plan show the
// member removed and re-added.
//
// The cloud's implicit owner is declared at its real role so the plan can be
// empty at all (see TestAccCloudAccessResourceImportRoundTrip). Positive
// control: the lowercase key is declared the same way and is unaffected.
func TestAccCloudAccessResource_MixedCaseMemberKeyPlansClean(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server := newMockCloudAccessServer(t)
	const resourceName = "anyscale_cloud_access.test"
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud_access" "test" {
  cloud_id = %[1]q

  member = {
    %[2]q = {
      base_role = "owner"
    }
    "Alice@Example.com" = {
      base_role = "writer"
    }
    "bob@example.com" = {
      base_role = "collaborator"
    }
  }
}
`, cloudAccessMockCloudID, cloudAccessMockImplicitMember)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config:           config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "member.Alice@Example.com.base_role", "writer"),
					resource.TestCheckNoResourceAttr(resourceName, "member.alice@example.com.base_role"),
					resource.TestCheckResourceAttr(resourceName, "member.bob@example.com.base_role", "collaborator"),
				),
			},
		},
	})
}
