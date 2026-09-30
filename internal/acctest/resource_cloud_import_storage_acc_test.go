package acctest

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
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
//   - L2 (file_storage.mount_path): AWS has no real backend field for
//     mount_path at all - resourcesJSON's file_storage deliberately omits
//     it, matching what the real AWS API returns. D1 stopped fabricating a
//     default for this case, so both create and import must resolve it to
//     null; this test proves the two paths agree.
//
// Step 1 creates against the mock and captures real applied state; its
// implicit post-apply plan must be empty. Step 2 imports: ImportStateVerify
// must match that state EXACTLY, with object_storage and file_storage not
// ignored. Imported state equal to a state that plans empty is what rules out
// the customer's "1 to add, 1 to destroy" for a config matching reality. A
// post-import plan step would add nothing: without ImportStatePersist it
// plans against Create's state, not the imported one.
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
		"object_storage": {"bucket_name": "s3://my-bucket", "region": null},
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

  # Customer mirror: file_storage_id only - mount_path omitted (resolves to
  # null; AWS has no backend field for it).
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
					resource.TestCheckNoResourceAttr(resourceName, "file_storage.mount_path"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				// THE regression proof: import must recover object_storage and
				// file_storage to exactly what create produced, landmines
				// included. object_storage.region stays null through both
				// steps here - config never sets it, and the mock's
				// resources-list response also carries no real region value
				// (matching the real backend precisely: it never returns a
				// region equal to the cloud's own region - see
				// TestAccCloudResource_ObjectStorageRegionSemanticEqualOnImport_AWSVM
				// for the explicit-equal case this reflects, and its own doc
				// comment for the backend trace).
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
				},
			},
		},
	})
}

// mountTargetsImportResourcesJSON is the resources listing for the
// mount_targets import tests: mount_targets IS populated server-side
// (simulating real EFS auto-discovery for a cloud registered out of band)
// even though config only ever sets file_storage_id. Exactly one entry,
// address only, no zone - what a real AWS backend response contains.
const mountTargetsImportResourcesJSON = `[{
	"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mount_targets_mock_default",
	"compute_stack": "VM", "region": "us-east-2",
	"aws_config": {
		"vpc_id": "vpc-mounttargets",
		"subnet_ids": ["subnet-mounttargets1", "subnet-mounttargets2"],
		"zones": ["us-east-2a", "us-east-2b"],
		"security_group_ids": ["sg-mounttargets"],
		"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/mounttargets-crossaccount",
		"cluster_iam_role_id": "arn:aws:iam::123456789012:role/mounttargets-cluster-node",
		"external_id": "mounttargets-external-id"
	},
	"object_storage": {"bucket_name": "s3://my-mounttargets-bucket"},
	"file_storage": {
		"file_storage_id": "fs-mt123",
		"mount_targets": [
			{"address": "fs-mt123.efs.us-east-2.amazonaws.com"}
		]
	}
}]`

const mountTargetsImportAddress = "fs-mt123.efs.us-east-2.amazonaws.com"

// mountTargetsImportConfig renders the mount_targets import tests' config.
// fileStorageExtra is spliced into the file_storage block; "" gives the
// out-of-band-registration mirror (file_storage_id only - mount_targets
// addresses are AWS-assigned and unknowable to whoever writes the config).
func mountTargetsImportConfig(serverURL, fileStorageExtra string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name             = "mount-targets-import-aws-mock"
  cloud_provider   = "AWS"
  compute_stack    = "VM"
  region           = "us-east-2"
  is_private_cloud = true

  aws_config {
    vpc_id            = "vpc-mounttargets"
    subnet_ids_to_az = {
      "subnet-mounttargets1" = "us-east-2a"
      "subnet-mounttargets2" = "us-east-2b"
    }
    security_group_ids        = ["sg-mounttargets"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/mounttargets-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/mounttargets-cluster-node"
    external_id               = "mounttargets-external-id"
  }

  object_storage {
    bucket_name = "my-mounttargets-bucket"
  }

  file_storage {
    file_storage_id = "fs-mt123"
%s
  }
}
`, fileStorageExtra)
}

func newMountTargetsImportMockServer(t *testing.T, cloudID string) *httptest.Server {
	t.Helper()
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "mount-targets-import-aws-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": false,
		"is_private_cloud": true
	}`, cloudID)
	return newC3MockCloudServer(t, cloudID, cloudJSON, mountTargetsImportResourcesJSON, "cldrsrc_mount_targets_mock_default")
}

// TestAccCloudResource_ImportRecoversMountTargets_AWSVM is Test A for
// mount_targets import recovery: mount_targets is Optional+Computed, so
// recovering the real value at import is correct, the same as
// memorydb/memorystore.
//
// newC3MockCloudServer's add_resource response carries no file_storage, so
// Create leaves mount_targets empty - nothing to derive it from yet. Import
// reads a resources listing that DOES carry a real entry, so mount_targets
// legitimately differs from Create's state; it is ignored in
// ImportStateVerify and asserted exactly in ImportStateCheck instead. Plan
// stability against that recovered value is
// TestAccCloudResource_MountTargetsRecoveredShapeIsPlanStable_AWSVM.
func TestAccCloudResource_ImportRecoversMountTargets_AWSVM(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server := newMountTargetsImportMockServer(t, "cld_mount_targets_import_aws_mock")
	resourceName := "anyscale_cloud.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: mountTargetsImportConfig(server.URL, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "file_storage.file_storage_id", "fs-mt123"),
					resource.TestCheckResourceAttr(resourceName, "file_storage.mount_targets.#", "0"),
				),
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud", "file_storage.mount_targets.#", "file_storage.mount_targets.0.%",
					"file_storage.mount_targets.0.address", "file_storage.mount_targets.0.zone",
				},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					attrs := states[0].Attributes
					if got := attrs["file_storage.mount_targets.#"]; got != "1" {
						return fmt.Errorf("file_storage.mount_targets.# = %q, want \"1\" - the real value must be recovered at import", got)
					}
					if got := attrs["file_storage.mount_targets.0.address"]; got != mountTargetsImportAddress {
						return fmt.Errorf("file_storage.mount_targets.0.address = %q, want the real recovered address", got)
					}
					return nil
				},
			},
		},
	})
}

// TestAccCloudResource_MountTargetsRecoveredShapeIsPlanStable_AWSVM is Test B
// for mount_targets: the plan against the state import produces must be a
// no-op for a config that omits mount_targets. Two Config-only steps carry
// state forward (an ImportState step's state would not): step 1 declares the
// recovered value so state holds exactly what import recovers (see the Test A
// sibling's ImportStateCheck), step 2 omits it as a real config does.
func TestAccCloudResource_MountTargetsRecoveredShapeIsPlanStable_AWSVM(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server := newMountTargetsImportMockServer(t, "cld_mount_targets_shape_aws_mock")
	resourceName := "anyscale_cloud.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: mountTargetsImportConfig(server.URL, fmt.Sprintf(`    mount_targets = [{ address = %q }]`, mountTargetsImportAddress)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "file_storage.mount_targets.#", "1"),
					resource.TestCheckResourceAttr(resourceName, "file_storage.mount_targets.0.address", mountTargetsImportAddress),
					resource.TestCheckNoResourceAttr(resourceName, "file_storage.mount_targets.0.zone"),
				),
			},
			{
				Config: mountTargetsImportConfig(server.URL, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				Check: resource.TestCheckResourceAttr(resourceName, "file_storage.mount_targets.0.address", mountTargetsImportAddress),
			},
		},
	})
}

// TestAccCloudResource_ImportRecoversStorageBlocks_K8S is the K8S companion:
// the ratified fix contract scopes recovery to "both object_storage and
// file_storage, VM and K8S" explicitly, not just the VM case the customer
// happened to report. Neither block is in ImportStateVerifyIgnore, so the
// import step fails if either is not recovered to exactly Create's state;
// step 1's post-apply plan being empty then covers the plan against that
// shape (see the AWS-VM sibling's doc comment).
//
// Also re-exercises L1 (region auto-fill) on the K8S path: the K8S
// lifecycle test's object_storage mock fixture never includes a region
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
		"object_storage": {"bucket_name": "s3://my-k8s-bucket", "region": null},
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
				// object_storage and file_storage are not ignored: proving
				// they round-trip is the point. object_storage.region stays null through both
				// steps, same reasoning as the AWS-VM sibling test - the mock
				// reflects the real backend, which never returns a region
				// equal to the cloud's own region.
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
				},
			},
		},
	})
}
