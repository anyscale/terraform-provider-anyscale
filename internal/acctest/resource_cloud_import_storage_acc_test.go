package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccCloudResource_ImportRecoversStorageBlocks_AWSVM is the fail-first
// regression test for the customer-reported AWS VM import destroy-and-recreate
// bug: requiredImportConfigBlocks recovers aws_config on import but leaves
// object_storage and file_storage null for a VM cloud, and both are
// ForceNew, so a configuration that matches the live cloud plans as a full
// replace instead of a no-op.
//
// Mirrors the customer's exact config per the ratified fix contract: AWS,
// VM, private, object_storage with bucket_name ONLY (no region, no
// endpoint), file_storage with file_storage_id and mount_path left to the
// schema default. The mock also bakes in the two landmines the contract
// calls out, so a naive recover-whatever-the-API-returns fix is caught
// here, not just the base "nothing is recovered" bug:
//
//   - L1 (object_storage.region auto-fill): the backend defaults the bucket
//     region to the cloud-resource's own region and returns it even though
//     the user set only bucket_name. resourcesJSON's object_storage.region
//     is deliberately equal to the cloud-resource region ("us-east-2") -
//     copying it verbatim would write a non-null region into state against
//     a config that never set one, forcing the exact same destroy-and-
//     recreate this test exists to catch, just relocated to a new attribute.
//   - L2 (file_storage.mount_path default collapse): AWS has no real
//     backend field for mount_path. resourcesJSON's file_storage
//     deliberately omits mount_path (empty, as the real API returns for
//     AWS) - copying that empty value verbatim would collapse the Computed
//     schema default ("/mnt/shared") to "", which is itself a spurious diff
//     against a freshly-created cloud that never touched the field.
//
// Step 1 creates against the mock and captures real applied state. Step 2
// imports: ImportStateVerify must match that state EXACTLY - unlike the
// pre-existing C3 lifecycle tests, object_storage and file_storage are
// deliberately NOT in ImportStateVerifyIgnore, since proving they now
// round-trip is the entire point of this test. Step 3 re-applies the same
// config and asserts the plan is a no-op: the literal customer complaint
// ("Plan: 1 to import, 1 to add, 0 to change, 1 to destroy") must never
// happen for a configuration that matches reality.
func TestAccCloudResource_ImportRecoversStorageBlocks_AWSVM(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_storage_import_aws_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "storage-import-aws-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": false,
		"is_private_cloud": true
	}`, cloudID)
	// L1 + L2 hazards baked in deliberately - see doc comment above. Do not
	// "clean up" region or add a mount_path here without updating the test's
	// intent: a mock that idealizes the response instead of reproducing these
	// two real API quirks would let a naive fix pass when it shouldn't.
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_storage_mock_default",
		"compute_stack": "VM", "region": "us-east-2",
		"aws_config": {
			"vpc_id": "vpc-storagetest",
			"subnet_ids": ["subnet-storagetest1", "subnet-storagetest2"],
			"zones": ["us-east-2a", "us-east-2b"],
			"security_group_ids": ["sg-storagetest"],
			"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/storagetest-crossaccount",
			"cluster_iam_role_id": "arn:aws:iam::123456789012:role/storagetest-cluster-node",
			"external_id": "storagetest-external-id"
		},
		"object_storage": {"bucket_name": "s3://my-bucket", "region": "us-east-2"},
		"file_storage": {"file_storage_id": "fs-storagetest123"}
	}]`

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_storage_mock_default")
	resourceName := "anyscale_cloud.test"
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name             = "storage-import-aws-mock"
  cloud_provider   = "AWS"
  compute_stack    = "VM"
  region           = "us-east-2"
  is_private_cloud = true

  aws_config {
    vpc_id            = "vpc-storagetest"
    subnet_ids_to_az = {
      "subnet-storagetest1" = "us-east-2a"
      "subnet-storagetest2" = "us-east-2b"
    }
    security_group_ids        = ["sg-storagetest"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/storagetest-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/storagetest-cluster-node"
    external_id               = "storagetest-external-id"
  }

  # Customer mirror: bucket_name ONLY - no region, no endpoint.
  object_storage {
    bucket_name = "my-bucket"
  }

  # Customer mirror: file_storage_id only - mount_path left to the schema default.
  file_storage {
    file_storage_id = "fs-storagetest123"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Establish real applied state to import-compare against.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "object_storage.bucket_name", "my-bucket"),
					resource.TestCheckNoResourceAttr(resourceName, "object_storage.region"),
					resource.TestCheckResourceAttr(resourceName, "file_storage.file_storage_id", "fs-storagetest123"),
					resource.TestCheckResourceAttr(resourceName, "file_storage.mount_path", "/mnt/shared"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				// THE regression proof: import must recover object_storage and
				// file_storage to exactly what create produced, landmines
				// included. Today this fails: ImportState leaves both blocks
				// null for a VM cloud, so they mismatch the prior (populated)
				// state instead of matching it.
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
				},
			},
			{
				// The literal customer bar: for a matching config, the plan
				// after import is a no-op - never a replace.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}

// TestAccCloudResource_ImportRecoversStorageBlocks_K8S is the K8S companion:
// the ratified fix contract scopes recovery to "both object_storage and
// file_storage, VM and K8S" explicitly, not just the VM case the customer
// happened to report. object_storage is already recovered for K8S today (it
// is one of the two compute-stack-required blocks); file_storage is not,
// exactly like VM, and the pre-existing TestAccCloudResource_Lifecycle_K8S_MockServer
// proves it: it puts "file_storage" in ImportStateVerifyIgnore with the
// comment "optional even for K8S; not recovered at import by design
// (C3-v2)" - the same silencing pattern the AWS/VM test used for
// object_storage before this bug was reported. This test removes that
// silencing for file_storage.
//
// Also re-exercises L1 (region auto-fill) on the K8S path: the pre-existing
// K8S lifecycle test's object_storage mock fixture never includes a region
// field at all, so it could not have caught L1 even though K8S recovery
// shares the exact same flattenObjectStorage call as VM. This test's mock
// does include it, matching the cloud-resource region, so a fix that
// handles L1 for VM but not K8S (e.g. by branching on compute_stack instead
// of reusing one shared helper) gets caught here instead of shipping silently.
func TestAccCloudResource_ImportRecoversStorageBlocks_K8S(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_storage_import_k8s_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "storage-import-k8s-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S", "is_default": false,
		"is_private_cloud": true
	}`, cloudID)
	// L1 hazard: object_storage.region equal to the cloud-resource region,
	// same as the VM test above. No L2 (mount_path) here - K8S's file_storage
	// uses persistent_volume_claim/csi_ephemeral_volume_driver, not mount_path.
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_storage_k8s_mock_default",
		"compute_stack": "K8S", "region": "us-east-2",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "arn:aws:iam::123456789012:role/storagetest-k8s-operator",
			"zones": ["us-east-2a", "us-east-2b"]
		},
		"object_storage": {"bucket_name": "s3://my-k8s-bucket", "region": "us-east-2"},
		"file_storage": {"persistent_volume_claim": "storagetest-pvc"}
	}]`

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_storage_k8s_mock_default")
	resourceName := "anyscale_cloud.test"
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name             = "storage-import-k8s-mock"
  cloud_provider   = "AWS"
  compute_stack    = "K8S"
  region           = "us-east-2"
  is_private_cloud = true

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::123456789012:role/storagetest-k8s-operator"
    zones                          = ["us-east-2a", "us-east-2b"]
  }

  object_storage {
    bucket_name = "my-k8s-bucket"
  }

  file_storage {
    persistent_volume_claim = "storagetest-pvc"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "object_storage.bucket_name", "my-k8s-bucket"),
					resource.TestCheckNoResourceAttr(resourceName, "object_storage.region"),
					resource.TestCheckResourceAttr(resourceName, "file_storage.persistent_volume_claim", "storagetest-pvc"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				// file_storage is deliberately NOT ignored here, unlike the
				// pre-existing K8S lifecycle test - proving it now round-trips
				// is the point. object_storage was already fine for K8S before
				// this fix, so this step also guards against a regression there.
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
				},
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}
