package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Importing a split-pattern cloud with "<cloud_id>:empty" recovers none of the config blocks.
// In the split pattern anyscale_cloud carries no aws_config/object_storage; the cloud's resource
// is a separate anyscale_cloud_resource that owns them. The mock's resources listing therefore
// has a default resource WITH aws_config and object_storage, exactly what a bare-ID import
// recovers. ImportStateVerify against the block-less state, with no block ignored, passes only
// if ":empty" leaves the blocks null. (docs/decisions/cloud-import-empty-pattern:
// the empty form's import recovers no config blocks.)
//
// The bare-ID form is guarded by TestAccCloudResource_Lifecycle_AWS_MockServer, whose import step
// must recover both blocks to pass ImportStateVerify.
func TestAccCloudResource_ImportEmptyPatternRecoversNoBlocks_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_import_empty_pattern_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "import-empty-pattern", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"
	}`, cloudID)
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"compute_stack": "VM", "region": "us-east-2",
		"aws_config": {
			"vpc_id": "vpc-split123",
			"subnet_ids": ["subnet-split1", "subnet-split2"],
			"zones": ["us-east-2a", "us-east-2b"],
			"security_group_ids": ["sg-split1"],
			"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/split-crossaccount",
			"cluster_iam_role_id": "arn:aws:iam::123456789012:role/split-cluster-node",
			"external_id": "split-external-id"
		},
		"object_storage": {"bucket_name": "s3://split-bucket-name"}
	}]`
	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_mock_default")
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "import-empty-pattern"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("anyscale_cloud.test", "aws_config.vpc_id"),
					resource.TestCheckNoResourceAttr("anyscale_cloud.test", "object_storage.bucket_name"),
				),
			},
			{
				ResourceName:      "anyscale_cloud.test",
				ImportState:       true,
				ImportStateId:     cloudID + ":empty",
				ImportStateVerify: true,
				// The id in state is the bare cloud ID; the suffix only selects import behavior.
				ImportStateVerifyIdentifierAttribute: "id",
				// credentials and is_empty_cloud as in the bare-ID import tests. cloud_resource_id
				// is computed: create of a block-less cloud leaves it null, while import reads the
				// cloud's default resource ID. Asserted below rather than silently excluded.
				ImportStateVerifyIgnore: []string{"credentials", "is_empty_cloud", "cloud_resource_id"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d instances, want 1", len(states))
					}
					a := states[0].Attributes
					if a["id"] != cloudID {
						return fmt.Errorf("imported id = %q, want the bare cloud ID %q", a["id"], cloudID)
					}
					if got := a["cloud_resource_id"]; got != "cldrsrc_mock_default" {
						return fmt.Errorf("imported cloud_resource_id = %q, want the default resource's ID", got)
					}
					for _, k := range []string{"aws_config.vpc_id", "object_storage.bucket_name"} {
						if v, ok := a[k]; ok && v != "" {
							return fmt.Errorf("imported %s = %q, want no recovered block", k, v)
						}
					}
					return nil
				},
			},
		},
	})
}

// An import ID with any other suffix, or no cloud ID before ":empty", is rejected and names both
// accepted forms.
func TestAccCloudResource_ImportEmptyPatternBadIDRejected_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_import_bad_id_mock"
	server := newC3MockCloudServer(t, cloudID, fmt.Sprintf(`{
		"id": %[1]q, "name": "import-bad-id", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"
	}`, cloudID), `[]`, "cldrsrc_mock_default")
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "import-bad-id"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"
}
`
	forms := regexp.MustCompile(`(?s)Invalid Import ID.*<cloud_id>.*<cloud_id>:empty`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:  "anyscale_cloud.test",
				Config:        config,
				ImportState:   true,
				ImportStateId: cloudID + ":other",
				ExpectError:   forms,
			},
			{
				ResourceName:  "anyscale_cloud.test",
				Config:        config,
				ImportState:   true,
				ImportStateId: ":empty",
				ExpectError:   forms,
			},
		},
	})
}

// A cold ":empty" import of a split-pattern cloud plans no change against a block-less config:
// the import is the first step and persists its state, so the plan really runs against what
// import recovered. The design's goal is exactly this empty plan.
func TestAccCloudResource_ImportEmptyPatternPlansEmpty_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_import_empty_plan_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "import-empty-plan", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"
	}`, cloudID)
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"compute_stack": "VM", "region": "us-east-2",
		"aws_config": {
			"vpc_id": "vpc-split123",
			"subnet_ids": ["subnet-split1"],
			"zones": ["us-east-2a"],
			"security_group_ids": ["sg-split1"],
			"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/split-crossaccount",
			"cluster_iam_role_id": "arn:aws:iam::123456789012:role/split-cluster-node",
			"external_id": "split-external-id"
		},
		"object_storage": {"bucket_name": "s3://split-bucket-name"}
	}]`
	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_mock_default")
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "import-empty-plan"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_cloud.test",
				Config:             config,
				ImportState:        true,
				ImportStateId:      cloudID + ":empty",
				ImportStatePersist: true,
			},
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}
