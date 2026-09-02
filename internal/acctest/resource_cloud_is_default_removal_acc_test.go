package acctest

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccCloudResource_IsDefaultRemovalNoSpuriousDiff is the import round-trip
// proof for is_default's removal from the anyscale_cloud resource (v3->v4
// state upgrader - see TestCloudResourceStateUpgradeV3toV4_DropsIsDefault for
// the upgrader-level unit test). Removal is the actual fix for the reported
// bug (is_default showing `(known after apply)` on the anyscale_cloud
// resource): the org default is now correctly and safely observable via the
// anyscale_cloud/anyscale_clouds data sources' own is_default instead (same
// auth-independent GET /clouds path, no plan-consistency hazard since data
// sources carry no prior-state contract).
//
// Guards the same three shapes the original idle-plan investigation cared
// about - a second independent plan right after Create; the same, byte
// identical config across separate TestSteps rather than relying on a single
// step's own PostApplyPostRefresh check; and the same again right after
// Import - proving the resource is clean end to end now that the attribute
// causing the noise is simply gone, not patched.
func TestAccCloudResource_IsDefaultRemovalNoSpuriousDiff(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, _ := newMockCloudCreateTimeServer(t)
	const resourceAddr = "anyscale_cloud.test"

	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name                       = "createtime-mock"
  cloud_provider             = "AWS"
  compute_stack              = "VM"
  region                     = "us-east-2"

  aws_config {
    vpc_id             = "vpc-realct123"
    subnet_ids_to_az = {
      "subnet-realct1" = "us-east-2a"
      "subnet-realct2" = "us-east-2b"
    }
    security_group_ids        = ["sg-realct1"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/real-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/real-cluster-node"
    external_id               = "real-external-id-ct"
  }

  object_storage {
    bucket_name = "real-ct-bucket"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					// is_default no longer exists as a resource attribute -
					// asserting its ABSENCE is the actual fix, not a
					// side effect. TestCheckNoResourceAttr fails loudly if
					// the attribute reappears in state for any reason.
					resource.TestCheckNoResourceAttr(resourceAddr, "is_default"),
					resource.TestCheckResourceAttrSet(resourceAddr, "id"),
				),
			},
			// Byte-identical config in a genuinely separate TestStep (not just
			// the same step's own PostApplyPostRefresh check), simulating a
			// second, independent `terraform plan` invocation - e.g. a later CI
			// run on the same PR - with nothing changed anywhere.
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Import, mirroring the reported workflow (terraform import
			// against an already-existing cloud, not a Terraform-driven
			// Create). ImportStateVerify has nothing to compare is_default
			// against since it no longer exists on either side.
			{
				ResourceName:      resourceAddr,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "aws_config", "gcp_config", "azure_config",
					"kubernetes_config", "object_storage", "file_storage", "is_empty_cloud",
				},
			},
			// The same config again, right after import - the exact moment
			// the original bug report was made against: the first
			// `terraform plan` a user runs right after `terraform import` to
			// confirm it matches config. No is_default attribute means
			// nothing left to show `(known after apply)` noise on.
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}
