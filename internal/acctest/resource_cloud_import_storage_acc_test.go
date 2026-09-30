package acctest

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccCloudResource_ImportRecoversStorageBlocks_AWSVM is the regression
// test for the customer-reported AWS VM import destroy-and-recreate bug:
// requiredImportConfigBlocks used to recover aws_config on import but leave
// object_storage and file_storage null for a VM cloud, and both are
// ForceNew, so a configuration matching the live cloud planned a full
// replace instead of a no-op.
//
// Mirrors the customer's config: AWS, VM, private, object_storage with
// bucket_name ONLY (no region, no endpoint), file_storage with
// file_storage_id and mount_path omitted. The mock reproduces the real API
// shape for both omissions, so a fix that fabricates a value is caught here,
// not just the base "nothing is recovered" bug:
//
//   - object_storage.region: resourcesJSON sends "region": null, as the real
//     backend does when the user set only bucket_name. Create and import
//     must both leave region null. The case where the backend returns a
//     region equal to the cloud's own region is covered by
//     TestAccCloudResource_ObjectStorageRegionSemanticEqualOnImport_AWSVM.
//   - file_storage.mount_path: AWS has no backend field for mount_path -
//     resourcesJSON's file_storage omits it, matching the real AWS API. The
//     provider does not fabricate a default, so create and import must both
//     resolve it to null; this test proves the two paths agree.
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
	// Null region and absent mount_path reproduce the real API shape - see
	// the doc comment above. Do not add a region or mount_path here without
	// updating the test's intent: a mock that echoes values the config never
	// set would let a fabricating fix pass.
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
				// file_storage to exactly what create produced. object_storage.region stays null through both
				// steps - config never sets it and the mock returns null.
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
// recovery covers object_storage and file_storage on both VM and K8S, not
// just the VM case the customer reported. Neither block is in
// ImportStateVerifyIgnore, so the import step fails if either is not
// recovered to exactly Create's state; step 1's post-apply plan being empty
// then covers the plan against that shape (see the AWS-VM sibling's doc
// comment).
//
// Also exercises the null object_storage.region on the K8S path: the K8S
// lifecycle test's object_storage mock fixture has no region key at all,
// while this mock sends "region": null like the VM test, so a K8S-specific
// flatten path that mishandled an explicit null would be caught here.
func TestAccCloudResource_ImportRecoversStorageBlocks_K8S(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_storage_import_k8s_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "storage-import-k8s-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S", "is_default": false,
		"is_private_cloud": true
	}`, cloudID)
	// object_storage.region is null, same as the VM test above. No mount_path
	// here - K8S's file_storage uses persistent_volume_claim/
	// csi_ephemeral_volume_driver, not mount_path.
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
