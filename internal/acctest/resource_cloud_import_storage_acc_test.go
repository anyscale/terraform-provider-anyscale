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

// TestAccCloudResource_ImportDropsMountTargets_AWSVM is the mutation-proof
// regression test for the mount_targets tail of the storage-import fix
// (yunhao's follow-up report, ratified plan:
// .crystl/quest/mount-targets-import-plan.md, Option C). Where the test
// above proves file_storage_id/mount_path now recover correctly at import,
// this one proves the opposite is required for mount_targets specifically:
// it must NOT be recovered, even though the backend returns it.
//
// Why: mount_targets is a schema.ListNestedBlock, and blocks cannot be
// Computed in terraform-plugin-framework (only Attributes can) - confirmed
// against the vendored framework source, not just precedent. Writing a
// recovered value into state at import (what v0.15.2's flattenFileStorage
// did for every file_storage field, mount_targets included) can never
// survive the NEXT plan when config omits the block: a Block's planned
// value comes from config alone, with no Computed fallback to prior state,
// so state-populated/config-absent reads as a real removal and the block's
// own listplanmodifier.RequiresReplace() fires - destroying and recreating
// the live, in-use cloud. Since these addresses are cloud-provider-assigned
// (AWS EFS per-mount-target IPs) and not reliably expressible in HCL, a
// correct import-target config can only ever declare file_storage_id, so
// this isn't a fixable recovery bug - the field must stay unrecovered.
//
// Mock fixture uses exactly ONE mount_targets entry, address only, no zone -
// matching what a real registered AWS cloud's GET actually returns (AWS/GCP
// store exactly one address server-side and drop zone; the backend errors
// on more than one; only Azure/Generic, always K8S, return a genuine
// multi-element per-zone list). A two-element AWS fixture would be an
// impossible backend state, so this is the faithful reproduction of
// yunhao's exact bug (architect's fidelity note, cross-validated by two
// independent backend traces). The general "any non-empty list is dropped,
// regardless of count or zone" behavior is proven separately at the unit
// level (TestFlattenFileStorage_MountTargetsNeverRecoveredAtImport and
// TestRequiredImportConfigBlocks_VMPopulatesProviderBlockPlusStorage in
// internal/provider), which deliberately keep a two-element, zone-populated
// fixture since flattenFileStorage itself is provider-agnostic and that
// shape is real for Azure/Generic.
//
// Step 1 creates with file_storage_id only (mount_targets is not, and
// cannot practically be, set in config) - establishing mount_targets = null
// in state, matching what a config that never touches the field produces.
// Step 2 imports the SAME cloud from a mock whose API response DOES include
// mount_targets (simulating the backend's real EFS auto-discovery for an
// out-of-band-registered cloud) - ImportStateVerify must still match step
// 1's null exactly, with file_storage NOT in the ignore list, proving
// mount_targets did not get recovered despite being present server-side.
// Step 3 re-applies the same config and asserts the plan is a no-op - the
// literal destroy-and-recreate this test exists to prevent.
//
// Mutation-proof: reverting flattenFileStorage's mount_targets handling back
// to recovering cfg.MountTargets (the v0.15.2 behavior) makes step 2's
// ImportStateVerify fail immediately (imported mount_targets non-null vs.
// step 1's null), before the plan-check in step 3 is even reached.
func TestAccCloudResource_ImportDropsMountTargets_AWSVM(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_mount_targets_import_aws_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "mount-targets-import-aws-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": false,
		"is_private_cloud": true
	}`, cloudID)
	// mount_targets IS populated server-side (simulating real EFS
	// auto-discovery for a cloud registered out of band) even though config
	// only ever sets file_storage_id. Exactly one entry, address only, no
	// zone - what a real AWS backend response actually contains (see doc
	// comment above).
	resourcesJSON := `[{
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

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_mount_targets_mock_default")
	resourceName := "anyscale_cloud.test"
	config := testAccProviderBlock(server.URL) + `
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

  # Out-of-band-registration mirror: file_storage_id only. mount_targets
  # cannot be set here - the addresses are AWS-assigned and unknowable to
  # whoever writes this config.
  file_storage {
    file_storage_id = "fs-mt123"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Establish real applied state: mount_targets absent from
				// config means null in state, since Create/Read never
				// touch file_storage from the API (C3-v2).
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "file_storage.file_storage_id", "fs-mt123"),
					resource.TestCheckResourceAttr(resourceName, "file_storage.mount_targets.#", "0"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				// THE regression proof: even though the mock's API response
				// for this cloud DOES carry mount_targets, imported state
				// must still come back null - matching step 1 exactly, not
				// the API's populated value. Today (pre-fix) this fails:
				// flattenFileStorage recovers mount_targets from cfg.MountTargets,
				// so imported state mismatches step 1's null.
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
				},
			},
			{
				// The literal bug-report bar: for a config matching the live
				// cloud, the plan after import is a no-op - never the
				// destroy-and-recreate yunhao reported.
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
