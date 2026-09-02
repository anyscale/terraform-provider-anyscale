package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccOrganizationDefaultCloudResource_ImportNonexistentCloud_MockServer
// closes the last named import edge case for anyscale_organization_default_cloud:
// importing a cloud_id that does not exist at all (as opposed to
// TestAccOrganizationDefaultCloudResource_ImportNotCurrentDefault_MockServer's
// case, where the cloud exists but isn't the org default). ImportState's own
// GET /clouds/{cloud_id} 404s for any ID not in knownCloudIDs (see
// newOrgDefaultCloudMockServer) - this asserts that 404 surfaces as the
// resource's own clean "Cloud ... was not found." diagnostic, not a silent
// import or a generic bubbled-up HTTP error.
func TestAccOrganizationDefaultCloudResource_ImportNonexistentCloud_MockServer(t *testing.T) {
	const orgID = "org_default_cloud_import404_mock"
	const realDefaultCloudID = "cld_default_cloud_import404_real"
	const nonexistentCloudID = "cld_does_not_exist_at_all"
	// nonexistentCloudID is deliberately absent from knownCloudIDs (nil here,
	// same as realDefaultCloudID's own default) - the mock's /api/v2/clouds/
	// handler 404s any ID that isn't the managed cloud or explicitly listed.
	server, _ := newOrgDefaultCloudMockServer(t, orgID, realDefaultCloudID, nil)

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_organization_default_cloud" "test" {
  cloud_id = %[1]q
}
`, nonexistentCloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_organization_default_cloud.test",
				ImportState:        true,
				ImportStateId:      nonexistentCloudID,
				Config:             config,
				ImportStatePersist: false,
				ExpectError:        regexp.MustCompile(`(?s)Cloud\s+"cld_does_not_exist_at_all"\s+was\s+not\s+found`),
			},
		},
	})
}
