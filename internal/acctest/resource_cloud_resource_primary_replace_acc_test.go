package acctest

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// Anyscale cannot replace a cloud's primary resource: the backend refuses to
// remove it, and its in-place update rejects changes to the IAM role,
// external ID, region, provider, compute stack and networking mode. Destroying
// a primary anyscale_cloud_resource also tears down the cloud's service load
// balancers and marks the cloud DELETING. So a config change that would
// replace a primary resource must fail at plan time instead of starting to
// delete the cloud. These tests run real Terraform plans against a mock API,
// because whether the error can fire depends on when the framework reports a
// replacement, which a unit test of the provider code cannot show.

// primaryReplaceConfig renders an empty cloud (region set, so changing it
// replaces the cloud) with a primary resource "a" and a second resource "b".
func primaryReplaceConfig(serverURL, cloudRegion, vpcA, vpcB string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name   = "multi-resource-mock"
  region = %[1]q
}

resource "anyscale_cloud_resource" "a" {
  cloud_id      = anyscale_cloud.test.id
  name          = "vm-aws-us-east-2"
  region        = "us-east-2"
  compute_stack = "VM"

  aws_config {
    vpc_id = %[2]q
    subnet_ids_to_az = {
      "subnet-a1" = "us-east-2a"
    }
    security_group_ids        = ["sg-a"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/a-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/a-cluster-node"
  }

  object_storage {
    bucket_name = "bucket-a"
  }
}

resource "anyscale_cloud_resource" "b" {
  cloud_id      = anyscale_cloud.test.id
  name          = "vm-aws-us-east-2-secondary"
  region        = "us-east-2"
  compute_stack = "VM"
  depends_on    = [anyscale_cloud_resource.a]

  aws_config {
    vpc_id = %[3]q
    subnet_ids_to_az = {
      "subnet-b1" = "us-east-2a"
    }
    security_group_ids        = ["sg-b"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/b-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/b-cluster-node"
  }

  object_storage {
    bucket_name = "bucket-b"
  }
}
`, cloudRegion, vpcA, vpcB)
}

func TestAccCloudResourcePrimaryReplace_PlanError(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_primary_replace"
	server, mock := newMultiResourceMockServer(t, cloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: primaryReplaceConfig(server.URL, "us-east-2", "vpc-a", "vpc-b"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud_resource.a", "is_default", "true"),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.b", "is_default", "false"),
				),
			},
			{
				// An in-place change on the primary resource still applies: the
				// guard must not refuse an update just because an unset
				// Optional+Computed attribute (cloud_provider) is unknown while
				// RequiresReplace runs.
				Config: strings.Replace(primaryReplaceConfig(server.URL, "us-east-2", "vpc-a", "vpc-b"),
					`name          = "vm-aws-us-east-2"`,
					"name          = \"vm-aws-us-east-2\"\n  timeouts {\n    delete = \"45m\"\n  }", 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud_resource.a", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				// Back to the base config, also in place.
				Config: primaryReplaceConfig(server.URL, "us-east-2", "vpc-a", "vpc-b"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud_resource.a", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				// A replace-forcing change on the primary resource fails the
				// plan and names the attribute.
				Config:      primaryReplaceConfig(server.URL, "us-east-2", "vpc-a2", "vpc-b"),
				ExpectError: regexp.MustCompile(`(?s)Cannot Replace a Primary Cloud Resource.*aws_config\.vpc_id cannot be changed on a cloud's primary resource`),
			},
			{
				// The same change on a non-primary resource plans an ordinary
				// replace.
				Config: primaryReplaceConfig(server.URL, "us-east-2", "vpc-a", "vpc-b2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud_resource.b", plancheck.ResourceActionReplace),
						plancheck.ExpectResourceAction("anyscale_cloud_resource.a", plancheck.ResourceActionNoop),
					},
				},
			},
			{
				// Replacing the cloud itself leaves cloud_id unknown, so the
				// primary resource is replaced with it. That is a legitimate
				// new cloud, not a primary-resource change: no error.
				Config:             primaryReplaceConfig(server.URL, "us-west-2", "vpc-a", "vpc-b2"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})

	if got := mock.resourceCount(); got != 0 {
		t.Errorf("want every resource removed after destroy, mock still has %d", got)
	}
}
