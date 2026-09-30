package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccCloudResource_AzureVM_NotSupported is the AKS-era successor to the
// original task-a7b8a48d regression test (formerly TestAccCloudResource_Azure_NotSupported).
// Azure itself is now a supported provider (AKS - see the mock-server lifecycle
// tests in resource_cloud_azure_acc_test.go), so "Azure is not supported" is no
// longer the right claim; what remains true, and what this test now pins, is
// narrower: Anyscale does not support Azure VM clouds, only Azure Kubernetes
// (compute_stack = K8S). That rejection also moved from an apply-time
// buildProviderConfig error to a plan-time ValidateConfig error
// (validateAzureK8SOnly) - a real behavior improvement the team flagged during
// this effort: the old version let a real (broken) cloud shell get created via
// POST /clouds before failing inside add_resource, which is exactly why the old
// test needed CheckDestroy to clean up after itself. The new plan-time error
// means Create() is never reached at all, so nothing is ever created - keeping
// CheckDestroy here is now a belt-and-suspenders no-op (RootModule().Resources
// will be empty), not a required cleanup step.
func TestAccCloudResource_AzureVM_NotSupported(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-azurevm-notsup")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccCloudResourceAzureConfig(cloudName),
				ExpectError: regexp.MustCompile(`(?s)Azure Requires Kubernetes Compute Stack.*only support compute_stack = "K8S"`),
			},
		},
	})
}

// TestAccCloudResource_PVCCSIConflict (K10) pins the plan-time rejection of
// setting both file_storage.persistent_volume_claim and
// file_storage.csi_ephemeral_volume_driver: the backend accepts only one
// Kubernetes shared-storage mechanism, and the schema wires a
// ConflictsWith validator on each side of the pair, not just one direction.
// Plan-time only (ValidateConfig / schema attribute validators) - no API
// call is ever made, so no real infra is needed and CheckDestroy here is a
// belt-and-suspenders no-op, matching TestAccCloudResource_AzureVM_NotSupported.
func TestAccCloudResource_PVCCSIConflict(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-pvc-csi-conflict")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccCloudResourcePVCCSIConflictConfig(cloudName),
				ExpectError: regexp.MustCompile(`(?s)Attribute\s+"file_storage\.csi_ephemeral_volume_driver"\s+cannot\s+be\s+specified\s+when\s+"file_storage\.persistent_volume_claim"\s+is\s+specified`),
			},
		},
	})
}

// TestAccCloudResource_RedisMemoryDBConflict (K11) pins the plan-time
// rejection of setting kubernetes_config.redis_endpoint together with
// aws_config.memorydb_cluster_endpoint - the backend rejects more than one
// GCS fault-tolerance backing store on the same cloud. The ConflictsWith
// validator is wired only on redis_endpoint (pointing at both
// aws_config.memorydb_cluster_endpoint and gcp_config.memorystore_endpoint),
// not mirrored on the provider-specific side, so this exercises the AWS half
// of that one-directional pair. Plan-time only, no real infra needed.
func TestAccCloudResource_RedisMemoryDBConflict(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-redis-memorydb-conflict")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccCloudResourceRedisMemoryDBConflictConfig(cloudName),
				ExpectError: regexp.MustCompile(`(?s)Attribute\s+"aws_config\.memorydb_cluster_endpoint"\s+cannot\s+be\s+specified\s+when\s+"kubernetes_config\.redis_endpoint"\s+is\s+specified`),
			},
		},
	})
}

// TestAccCloudResource_InvalidComputeStack (K12) pins the plan-time
// rejection of any compute_stack value outside the OneOf("VM", "K8S")
// allow-list. Plan-time only, no real infra needed.
func TestAccCloudResource_InvalidComputeStack(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-invalid-stack")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccCloudResourceInvalidComputeStackConfig(cloudName),
				ExpectError: regexp.MustCompile(`(?s)Attribute\s+compute_stack\s+value\s+must\s+be\s+one\s+of:.*got:\s+"INVALID"`),
			},
		},
	})
}

// TestAccCloudResource_MountPathAWSRejected pins the plan-time rejection of
// file_storage.mount_path when cloud_provider is AWS: the backend's
// AWSNFSResources proto has no field for it (confirmed live, see
// MOUNT-PATH-BUG-TRACE.md), so a configured value would silently do nothing
// rather than error - this ValidateConfig check catches it instead. Keys off
// the explicit cloud_provider attribute (not aws_config presence), so an
// AWS+K8S cloud that omits aws_config entirely is still caught. Plan-time
// only, no real infra needed.
func TestAccCloudResource_MountPathAWSRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-mountpath-aws")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccCloudResourceMountPathAWSConfig(cloudName),
				ExpectError: regexp.MustCompile(`(?s)mount_path\s+Not\s+Supported\s+On\s+AWS`),
			},
		},
	})
}

// TestAccCloudResource_MountPathAWSInferredRejected is a regression guard for
// a real bug found during review: ValidateConfig's provider-inference
// fallback only covered an omitted cloud_provider alongside azure_config,
// not aws_config/gcp_config, so a config that relies purely on aws_config
// presence (no explicit cloud_provider = "AWS") resolved provider to an
// empty string and silently skipped the AWS mount_path check entirely -
// exactly the configs most likely to hit the underlying bug. Fixed by
// mirroring Create's full auto-detect order (AWS, then GCP, then Azure).
// This test pins that fix: aws_config present, cloud_provider OMITTED
// (inferred), mount_path set - must still be rejected. If the auto-detect
// order ever drifts, this is the test that goes red. Plan-time only, no
// real infra needed.
func TestAccCloudResource_MountPathAWSInferredRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-mountpath-aws-inferred")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccCloudResourceMountPathAWSInferredConfig(cloudName),
				ExpectError: regexp.MustCompile(`(?s)mount_path\s+Not\s+Supported\s+On\s+AWS`),
			},
		},
	})
}

// TestAccCloudResource_MountPathPVCConflict pins the plan-time rejection of
// setting file_storage.mount_path together with persistent_volume_claim: the
// Kubernetes-native shared-storage mechanism (K8SSharedStorageResources) has
// no path-shaped field either, confirmed by the backend mapping trace, so
// mount_path would silently do nothing there too. Plan-time only, no real
// infra needed.
func TestAccCloudResource_MountPathPVCConflict(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-mountpath-pvc-conflict")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccCloudResourceMountPathPVCConflictConfig(cloudName),
				ExpectError: regexp.MustCompile(`(?s)Attribute\s+"file_storage\.mount_path"\s+cannot\s+be\s+specified\s+when\s+"file_storage\.persistent_volume_claim"\s+is\s+specified`),
			},
		},
	})
}

// TestAccCloudResource_MountPathPVCDefaultNoMisfire is the negative
// counterpart to TestAccCloudResource_MountPathPVCConflict: a config that
// sets persistent_volume_claim and leaves mount_path OMITTED must NOT trip
// the new ConflictsWith - the validator must fire only on the user's raw
// config (which schema Validators evaluate directly, before any plan
// modifier runs), not on the resolved plan value. Needs a real Create to
// prove this (ConflictsWith is framework-internal machinery, not something
// a plain Go unit test can exercise), so this runs against a mock server
// rather than real infra - see newC3MockCloudServer/testAccProviderBlock in
// resource_cloud_c3_lifecycle_acc_test.go. mount_path itself resolves to
// null (D1 - no schema Default, and the mock backend returns no file_storage
// at all here), not the old fabricated "/mnt/shared".
func TestAccCloudResource_MountPathPVCDefaultNoMisfire(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_mountpath_pvc_nomisfire_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "mountpath-pvc-nomisfire", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"]
		},
		"object_storage": {"bucket_name": "tfacc-mountpath-nomisfire-bucket"}
	}]`

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_mock_default")
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "mountpath-pvc-nomisfire"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-mountpath-nomisfire-bucket"
  }

  file_storage {
    persistent_volume_claim = "ray-shared-pvc"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc"),
					// mount_path stays null - proves the new ConflictsWith
					// evaluated the raw (mount_path-omitted) config, not a
					// resolved plan value, exactly as the review required.
					resource.TestCheckNoResourceAttr("anyscale_cloud.test", "file_storage.mount_path"),
				),
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// TestAccCloudResource_SubnetNamesK8SRejected pins the plan-time rejection of
// gcp_config.subnet_names when compute_stack is K8S: the backend's
// conversion code applies subnet_names unconditionally after the Kubernetes
// zone list is written to the same NetworkInfo field, genuinely corrupting
// it (confirmed by tracing the real code, independently re-verified),
// not just leaving it a no-op. Plan-time only, no real infra needed.
func TestAccCloudResource_SubnetNamesK8SRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-subnetnames-k8s")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccCloudResourceSubnetNamesK8SConfig(cloudName),
				ExpectError: regexp.MustCompile(`(?s)subnet_names\s+Not\s+Supported\s+On\s+Kubernetes\s+Compute`),
			},
		},
	})
}

// TestAccCloudResource_SubnetNamesVMMultipleAllowed is the negative
// counterpart: GCP VM compute with MORE THAN ONE subnet_name must still plan
// clean - GCP VM clouds genuinely support multiple subnets, a real,
// intentional, tested backend feature, not something to reject. Runs
// against a mock server (no real infra) since proving no misfire needs a
// real Create through the framework's own validator dispatch, the same
// reasoning as TestAccCloudResource_MountPathPVCDefaultNoMisfire.
func TestAccCloudResource_SubnetNamesVMMultipleAllowed(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_subnetnames_vm_multi_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "subnetnames-vm-multi", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"
	}`, cloudID)
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"compute_stack": "VM", "region": "us-central1"
	}]`

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_mock_default")
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "subnetnames-vm-multi"
  cloud_provider = "GCP"
  compute_stack  = "VM"
  region         = "us-central1"

  gcp_config {
    project_id    = "my-gcp-project"
    vpc_name      = "anyscale-vpc"
    subnet_names  = ["anyscale-subnet-1", "anyscale-subnet-2"]
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "gcp_config.subnet_names.#", "2"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "gcp_config.subnet_names.0", "anyscale-subnet-1"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "gcp_config.subnet_names.1", "anyscale-subnet-2"),
				),
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// TestAccCloudResource_SubnetIDsK8SRejected pins the plan-time rejection of
// aws_config.subnet_ids when compute_stack is K8S, the plain-list form. Not
// symmetric with the GCP case at the backend level (this form trips a
// pre-existing length guard rather than reaching the actual clobber - see
// validateSubnetIDsSupported), but the ValidateConfig check pre-empts both
// AWS forms with the same clear plan-time error rather than letting either
// fall through to a different backend symptom. Plan-time only, no real
// infra needed.
func TestAccCloudResource_SubnetIDsK8SRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-subnetids-k8s")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccCloudResourceSubnetIDsK8SConfig(cloudName),
				ExpectError: regexp.MustCompile(`(?s)subnet_ids\s+Not\s+Supported\s+On\s+Kubernetes\s+Compute`),
			},
		},
	})
}

// TestAccCloudResource_SubnetIDsToAZK8SRejected pins the plan-time rejection
// of aws_config.subnet_ids_to_az when compute_stack is K8S, the map form -
// the one that actually reaches the Go-level clobber (unlike subnet_ids,
// see validateSubnetIDsSupported). Separate test from the plain-list form
// since they are genuinely different attributes with different backend
// failure modes if not caught here; both must be independently pinned.
// Plan-time only, no real infra needed.
func TestAccCloudResource_SubnetIDsToAZK8SRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-subnetidstoaz-k8s")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccCloudResourceSubnetIDsToAZK8SConfig(cloudName),
				ExpectError: regexp.MustCompile(`(?s)subnet_ids_to_az\s+Not\s+Supported\s+On\s+Kubernetes\s+Compute`),
			},
		},
	})
}

// testAccCloudResourceAzureConfig is schema-valid against the current
// azure_config (tenant_id only, per the AKS design) but still exercises the
// VM-stack rejection path: Azure only supports compute_stack = K8S, so this
// config is still expected to fail, just with a different error message than
// before AKS support landed. See TestAccCloudResource_AzureVM_NotSupported's
// own doc comment for the up-to-date expectation.
func testAccCloudResourceAzureConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name          = "%s"
  region        = "eastus"
  compute_stack = "VM"

  azure_config {
    tenant_id = "00000000-0000-0000-0000-000000000000"
  }
}
`, name)
}

// testAccCloudResourcePVCCSIConflictConfig is deliberately minimal (just
// name + the two conflicting file_storage fields): the ConflictsWith
// validator fires independent of compute_stack or any other field, and
// keeping this focused mirrors testAccCloudResourceAzureConfig's minimalism
// for the same class of plan-time-only test.
func testAccCloudResourcePVCCSIConflictConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name = "%s"

  file_storage {
    persistent_volume_claim     = "test-pvc"
    csi_ephemeral_volume_driver = "test.csi.driver"
  }
}
`, name)
}

// testAccCloudResourceMountPathAWSConfig is deliberately minimal (just name +
// cloud_provider + mount_path): the AWS ValidateConfig check keys off the
// explicit cloud_provider string alone, independent of aws_config presence -
// deliberately an AWS+K8S(EKS) shape with aws_config entirely absent (an
// empty kubernetes_config block is enough to satisfy
// hasEmbeddedResourceConfig so ValidateConfig does not return early on the
// "genuinely empty cloud" path), confirming the check keys off the explicit
// cloud_provider attribute rather than aws_config presence, matching the
// AWS+K8S(EKS) case confirmed by the backend mapping trace.
func testAccCloudResourceMountPathAWSConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "%s"
  cloud_provider = "AWS"
  compute_stack  = "K8S"

  kubernetes_config {}

  file_storage {
    mount_path = "/mnt/cluster_storage"
  }
}
`, name)
}

// testAccCloudResourceMountPathAWSInferredConfig deliberately OMITS
// cloud_provider - aws_config's presence alone must be enough for
// ValidateConfig's auto-detect fallback to resolve provider to AWS and fire
// the mount_path rejection, the same way Create's own auto-detect already
// does. This is the config that exposed the real provider-inference gap
// found in review.
func testAccCloudResourceMountPathAWSInferredConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name = "%s"

  aws_config {}

  file_storage {
    mount_path = "/mnt/cluster_storage"
  }
}
`, name)
}

// testAccCloudResourceMountPathPVCConflictConfig mirrors
// testAccCloudResourcePVCCSIConflictConfig's minimalism: the ConflictsWith
// between mount_path and persistent_volume_claim fires independent of
// cloud_provider or compute_stack (the Kubernetes-native storage mechanism
// has no path field regardless of provider), so no other config is needed.
func testAccCloudResourceMountPathPVCConflictConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name = "%s"

  file_storage {
    mount_path              = "/mnt/cluster_storage"
    persistent_volume_claim = "test-pvc"
  }
}
`, name)
}

// testAccCloudResourceSubnetNamesK8SConfig is deliberately minimal (just
// name + compute_stack + gcp_config.subnet_names): the K8S check keys off
// the explicit compute_stack attribute alone, independent of cloud_provider
// or any other gcp_config field, matching how hasEmbeddedResourceConfig
// already treats gcp_config presence as enough to avoid the
// genuinely-empty-cloud early return.
func testAccCloudResourceSubnetNamesK8SConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name          = "%s"
  compute_stack = "K8S"

  gcp_config {
    subnet_names = ["anyscale-subnet-1"]
  }
}
`, name)
}

// testAccCloudResourceSubnetIDsK8SConfig mirrors
// testAccCloudResourceSubnetNamesK8SConfig's minimalism, plain-list form.
func testAccCloudResourceSubnetIDsK8SConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name          = "%s"
  compute_stack = "K8S"

  aws_config {
    subnet_ids = ["subnet-0abc123def456789"]
  }
}
`, name)
}

// testAccCloudResourceSubnetIDsToAZK8SConfig mirrors
// testAccCloudResourceSubnetNamesK8SConfig's minimalism, map form - the one
// that actually reaches the Go-level clobber per validateSubnetIDsSupported.
func testAccCloudResourceSubnetIDsToAZK8SConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name          = "%s"
  compute_stack = "K8S"

  aws_config {
    subnet_ids_to_az = {
      "subnet-0abc123def456789" = "us-east-2a"
    }
  }
}
`, name)
}

func testAccCloudResourceRedisMemoryDBConflictConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name = "%s"

  kubernetes_config {
    redis_endpoint = "redis.ray-system.svc.cluster.local:6379"
  }

  aws_config {
    memorydb_cluster_endpoint = "memorydb.example.com:6379"
  }
}
`, name)
}

func testAccCloudResourceInvalidComputeStackConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name          = "%s"
  compute_stack = "INVALID"
}
`, name)
}
