package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccCloudResource_AWS_Basic tests basic AWS cloud creation with all-in-one pattern
func TestAccCloudResource_AWS_Basic(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloudName := UniqueName(t, "cloud-aws-basic")
	// Generate random suffix for IAM roles to allow parallel test runs
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccCloudResourceAWSBasicConfig(cloudName, randSuffix),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "name", cloudName),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "cloud_provider", "AWS"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "compute_stack", "VM"),
					resource.TestCheckResourceAttrSet("anyscale_cloud.test", "id"),
					resource.TestCheckResourceAttrSet("anyscale_cloud.test", "region"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "is_empty_cloud", "false"),
					// API validation: verify cloud exists and has correct attributes
					testAccCheckCloudExistsInAPI("anyscale_cloud.test"),
					testAccCheckCloudAttributes("anyscale_cloud.test", cloudName, "AWS", "us-east-2"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// ImportState testing
			{
				ResourceName:      "anyscale_cloud.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials",       // sensitive: API never returns auth tokens after create
					"aws_config",        // write-only block: API does not echo back provider-specific config on cloud GET
					"gcp_config",        // write-only block: API does not echo back provider-specific config on cloud GET
					"azure_config",      // write-only block: API does not echo back provider-specific config on cloud GET
					"kubernetes_config", // write-only block: API does not echo back provider-specific config on cloud GET
					"object_storage",    // write-only block: storage lives on the cloud deployment, not on the cloud GET
					"file_storage",      // write-only block: storage lives on the cloud deployment, not on the cloud GET
					"is_empty_cloud",    // create-time-only flag derived from plan; not surfaced by the API
				},
			},
		},
	})
}

// TestAccCloudResource_AWS_EmptyCloud tests AWS empty cloud pattern
func TestAccCloudResource_AWS_EmptyCloud(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-aws-empty")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceAWSEmptyConfig(cloudName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "name", cloudName),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "cloud_provider", "AWS"),
					resource.TestCheckResourceAttrSet("anyscale_cloud.test", "id"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "is_empty_cloud", "true"),
					// API validation
					testAccCheckCloudExistsInAPI("anyscale_cloud.test"),
					testAccCheckCloudAttributes("anyscale_cloud.test", cloudName, "AWS", "us-east-2"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

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

// TestAccCloudResource_GCP_Basic tests basic GCP cloud creation
func TestAccCloudResource_GCP_Basic(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloudName := UniqueName(t, "cloud-gcp-basic")
	// Generate random suffix for service accounts to allow parallel test runs
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceGCPBasicConfig(cloudName, randSuffix),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "name", cloudName),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "cloud_provider", "GCP"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "compute_stack", "VM"),
					resource.TestCheckResourceAttrSet("anyscale_cloud.test", "id"),
					resource.TestCheckResourceAttrSet("anyscale_cloud.test", "region"),
					// API validation
					testAccCheckCloudExistsInAPI("anyscale_cloud.test"),
					testAccCheckCloudAttributes("anyscale_cloud.test", cloudName, "GCP", "us-central1"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				ResourceName:      "anyscale_cloud.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials",    // sensitive: API never returns auth tokens after create
					"gcp_config",     // write-only block: API does not echo back provider-specific config on cloud GET
					"object_storage", // write-only block: storage lives on the cloud deployment, not on the cloud GET
					"is_empty_cloud", // create-time-only flag derived from plan; not surfaced by the API
				},
			},
		},
	})
}

// TestAccCloudResource_AWS_K8S tests AWS K8S cloud creation
func TestAccCloudResource_AWS_K8S(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloudName := UniqueName(t, "cloud-aws-k8s")
	// Generate random suffix for IAM roles to allow parallel test runs
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)

	const redisEndpoint = "redis.ray-system.svc.cluster.local:6379"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceAWSK8SConfig(cloudName, randSuffix, "anyscale", redisEndpoint),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "name", cloudName),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "cloud_provider", "AWS"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "compute_stack", "K8S"),
					resource.TestCheckResourceAttrSet("anyscale_cloud.test", "id"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "kubernetes_config.redis_endpoint", redisEndpoint),
					// API validation
					testAccCheckCloudExistsInAPI("anyscale_cloud.test"),
					testAccCheckCloudAttributes("anyscale_cloud.test", cloudName, "AWS", "us-east-2"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// ImportState testing against REAL infra (not just the mock server):
			// proves the real add_resource/resources-listing API round-trips
			// kubernetes_config - including redis_endpoint - through the C3-v2
			// import-recovery path (requiredImportConfigBlocks), not just that a
			// mocked response shaped the way we assume it would. Placed before the
			// namespace-edit step below (still "anyscale", the same default
			// flattenKubernetesConfig always recovers) so there is no known hazard
			// to ignore; kubernetes_config is deliberately NOT in
			// ImportStateVerifyIgnore for that reason.
			{
				ResourceName:      "anyscale_cloud.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
					"file_storage", // optional even for K8S; not recovered at import by design (C3-v2)
				},
			},
			// regression test for task 02118d55: this kubernetes_config block is a
			// duplicate of the one fixed under 861aaf10 on anyscale_cloud_resource and
			// had the same missing RequiresReplace, so an edit here plans a clean
			// replace now instead of a diff Update() (partial no-op) used to swallow.
			{
				Config: testAccCloudResourceAWSK8SConfig(cloudName, randSuffix, "custom-ns", redisEndpoint),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "kubernetes_config.namespace", "custom-ns"),
					testAccCheckCloudExistsInAPI("anyscale_cloud.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionReplace),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// TestAccCloudResource_GCP_K8S tests GCP K8S (GKE) cloud creation
func TestAccCloudResource_GCP_K8S(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloudName := UniqueName(t, "cloud-gcp-k8s")
	// Generate random suffix for service accounts to allow parallel test runs
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)

	const redisEndpoint = "redis.ray-system.svc.cluster.local:6379"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceGCPK8SConfig(cloudName, randSuffix, redisEndpoint),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "name", cloudName),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "cloud_provider", "GCP"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "compute_stack", "K8S"),
					resource.TestCheckResourceAttrSet("anyscale_cloud.test", "id"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "kubernetes_config.redis_endpoint", redisEndpoint),
					// API validation
					testAccCheckCloudExistsInAPI("anyscale_cloud.test"),
					testAccCheckCloudAttributes("anyscale_cloud.test", cloudName, "GCP", "us-central1"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// ImportState testing against REAL infra - see the identical step's
			// comment in TestAccCloudResource_AWS_K8S above for why
			// kubernetes_config is deliberately not in the ignore list here.
			{
				ResourceName:      "anyscale_cloud.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
					"file_storage", // optional even for K8S; not recovered at import by design (C3-v2)
				},
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

// newFileStorageUpdateMockServer builds a stateful mock exercising D2's
// in-place update path: GET /resources returns the currently-stored
// deployment; PUT /resources requires a bare JSON list (422 on anything
// else, matching the real API's "value is not a valid list" rejection of a
// single object), then shallow-merges the sent object's top-level keys into
// the stored one - a key the PUT omits survives untouched, a key it sends
// replaces the old value wholesale, including any inner field that key's own
// old value carried but the new one didn't. That is exactly the backend's
// documented asymmetry for sibling blocks (docs/decisions/
// cloud-file-storage-lifecycle/README.md's Gate 1 results: "top-level block
// omitted -> preserved" vs. "field omitted inside a block that IS sent ->
// wiped"), reproduced without hand-simulating the backend's real merge
// logic.
//
// file_storage itself is the one documented exception to "omitted ->
// preserved": G1.2 confirmed omitting it from the PUT clears it server-side
// (the design's clear-by-omission path), so this mock special-cases that one
// key to clear-on-omit rather than preserve-on-omit.
//
// putHook, when non-nil, runs before the merge on every PUT and may
// substitute a failure response (running-clusters / anyscale-managed /
// infrastructure-manager-changed all arrive as an opaque 400, told apart
// only by body substring) by returning handled=true; handled=false falls
// through to the normal 200+merge path. lastPUTBody returns the raw body of
// the most recent PUT, for tests that need to assert on exactly what the
// provider sent rather than only on what the mock now stores.
func newFileStorageUpdateMockServer(
	t *testing.T,
	cloudID string,
	cloudJSON string,
	initialResourceJSON string,
	addResourceFileStorageJSON string,
	putHook func(sent map[string]interface{}) (handled bool, statusCode int, body string),
) (server *httptest.Server, lastPUTBody func() string) {
	t.Helper()
	mux := http.NewServeMux()

	var mu sync.Mutex
	var stored map[string]interface{}
	if err := json.Unmarshal([]byte(initialResourceJSON), &stored); err != nil {
		t.Fatalf("newFileStorageUpdateMockServer: invalid initialResourceJSON: %v", err)
	}
	var lastBody atomic.Value
	lastBody.Store("")

	marshalResults := func() string {
		mu.Lock()
		defer mu.Unlock()
		b, err := json.Marshal([]map[string]interface{}{stored})
		if err != nil {
			t.Fatalf("newFileStorageUpdateMockServer: marshal stored resource: %v", err)
		}
		return string(b)
	}

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.Method {
		case http.MethodPost:
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		case http.MethodGet:
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds", r.Method)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s", r.Method, cloudID)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"results": %s, "metadata": {"total": 1, "next_paging_token": null}}`, marshalResults())
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			lastBody.Store(string(raw))

			var sentList []map[string]interface{}
			if err := json.Unmarshal(raw, &sentList); err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = fmt.Fprint(w, `{"detail": "value is not a valid list"}`)
				return
			}
			if len(sentList) != 1 {
				t.Fatalf("PUT /resources: expected exactly 1 element, got %d: %s", len(sentList), raw)
			}
			sent := sentList[0]

			if putHook != nil {
				if handled, code, body := putHook(sent); handled {
					w.WriteHeader(code)
					_, _ = fmt.Fprint(w, body)
					return
				}
			}

			mu.Lock()
			for k, v := range sent {
				stored[k] = v
			}
			if _, sentFileStorage := sent["file_storage"]; !sentFileStorage {
				stored["file_storage"] = nil
			}
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"result": {}}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s/resources", r.Method, cloudID)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {"cloud_deployment_id": "cldrsrc_mock_default", "cloud_resource_id": "cldrsrc_mock_default", "file_storage": %s}}`, addResourceFileStorageJSON)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() string {
		v, _ := lastBody.Load().(string)
		return v
	}
}

// TestAccCloudResource_FileStorageAddIsUpdatable covers acceptance
// criterion 1 of D2 (docs/decisions/cloud-file-storage-lifecycle/README.md):
// on an already-live anyscale_cloud with no file_storage block, adding one
// that sets only persistent_volume_claim now plans and applies an in-place
// Update, not a replace. This is the direct inversion of the pre-D2 pinning
// test of the same shape (every file_storage attribute carried an
// unconditional stringplanmodifier.RequiresReplace(), and mount_path's
// Computed+Default separately forced replace on its own absent->present
// change) - neither marker is a static-schema property (`terraform providers
// schema -json` has no requires-replace field; it's computed per-plan), so
// this can only be proven with a real plan, which is what this test runs.
func TestAccCloudResource_FileStorageAddIsUpdatable(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_add_updatable_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-add-updatable", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"],
			"redis_endpoint": "redis.ray-system.svc.cluster.local:6379"
		},
		"object_storage": {"bucket_name": "tfacc-filestorage-updatable-bucket"}
	}`

	server, lastPUTBody := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null", nil)
	baseConfig := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "filestorage-add-updatable"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-filestorage-updatable-bucket"
  }
}
`
	withFileStorage := baseConfig[:len(baseConfig)-2] + `
  file_storage {
    persistent_volume_claim = "ray-shared-pvc"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Baseline: cloud already live, no file_storage declared.
				Config: baseConfig,
				Check:  resource.TestCheckNoResourceAttr("anyscale_cloud.test", "file_storage"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				// Declaring file_storage for the first time, setting only
				// persistent_volume_claim, plans and applies an in-place
				// Update - not the pre-D2 full replace.
				Config: withFileStorage,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc"),
					func(_ *terraform.State) error {
						// Independent of Terraform state: the mock's stored
						// deployment is what the PUT actually persisted, so
						// this proves the new file_storage value reached the
						// wire, not merely that the plan looked right.
						sent := lastPUTBody()
						if sent == "" {
							return fmt.Errorf("expected a PUT to /resources, got none")
						}
						if !regexp.MustCompile(`"persistent_volume_claim"\s*:\s*"ray-shared-pvc"`).MatchString(sent) {
							return fmt.Errorf("PUT body missing persistent_volume_claim=ray-shared-pvc: %s", sent)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccCloudResource_FileStorageUpdateDoesNotWipeSiblings covers acceptance
// criterion 2 of D2: updating file_storage on a live K8S cloud must not
// collaterally wipe kubernetes_config.redis_endpoint or object_storage -
// D2's round-trip PUT is a full-spec replace, so any sibling field the
// provider fails to echo back is cleared by the API, not merely left stale in
// state. The check reads the mock's stored deployment directly over raw HTTP,
// bypassing Terraform state entirely: config blocks are deliberately not
// Read-refreshed (see CLAUDE.md's Import Round-trip safety section), so a
// state-only assertion could pass even if the PUT body itself had dropped a
// field the mock happened not to overwrite.
func TestAccCloudResource_FileStorageUpdateDoesNotWipeSiblings(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_nowipe_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-nowipe", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"],
			"redis_endpoint": "redis.ray-system.svc.cluster.local:6379"
		},
		"object_storage": {"bucket_name": "tfacc-filestorage-nowipe-bucket"},
		"file_storage": {"persistent_volume_claim": "ray-shared-pvc-v1"}
	}`

	server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null", nil)
	configWithPVC := func(pvc string) string {
		return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "filestorage-nowipe"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-filestorage-nowipe-bucket"
  }

  file_storage {
    persistent_volume_claim = %[1]q
  }
}
`, pvc)
	}

	checkSiblingsIntact := func(_ *terraform.State) error {
		resp, err := http.Get(server.URL + "/api/v2/clouds/" + cloudID + "/resources")
		if err != nil {
			return fmt.Errorf("GET /resources: %w", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading /resources response: %w", err)
		}
		body := string(raw)
		for _, want := range []string{
			`"redis_endpoint"`,
			"redis.ray-system.svc.cluster.local:6379",
			`"bucket_name"`,
			"tfacc-filestorage-nowipe-bucket",
			"ray-shared-pvc-v2",
		} {
			if !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(body) {
				return fmt.Errorf("expected %q to survive the file_storage update, stored deployment: %s", want, body)
			}
		}
		return nil
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configWithPVC("ray-shared-pvc-v1"),
				Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v1"),
			},
			{
				// Changing only persistent_volume_claim must not clear
				// redis_endpoint or bucket_name from the live deployment.
				Config: configWithPVC("ray-shared-pvc-v2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v2"),
					checkSiblingsIntact,
				),
			},
		},
	})
}

// TestAccCloudResource_FileStorageClearingWorks covers acceptance criterion 5
// of D2: removing a previously-declared file_storage block from config plans
// and applies an in-place Update that clears it - D2's round-trip PUT omits
// the key entirely when planFileStorage is nil, and per the design doc's own
// Gate-1 evidence (G1.2), omission clears file_storage on the live deployment
// rather than leaving the last value in place (the opposite of how the
// sibling blocks behave on omission). Sibling fields are asserted intact for
// the same reason as criterion 2: clearing file_storage must not collaterally
// wipe kubernetes_config or object_storage.
func TestAccCloudResource_FileStorageClearingWorks(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_clear_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-clear", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"],
			"redis_endpoint": "redis.ray-system.svc.cluster.local:6379"
		},
		"object_storage": {"bucket_name": "tfacc-filestorage-clear-bucket"},
		"file_storage": {"persistent_volume_claim": "ray-shared-pvc"}
	}`

	server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null", nil)
	baseConfig := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "filestorage-clear"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-filestorage-clear-bucket"
  }
`
	withFileStorage := baseConfig + `
  file_storage {
    persistent_volume_claim = "ray-shared-pvc"
  }
}
`
	withoutFileStorage := baseConfig + `
}
`

	checkCleared := func(_ *terraform.State) error {
		resp, err := http.Get(server.URL + "/api/v2/clouds/" + cloudID + "/resources")
		if err != nil {
			return fmt.Errorf("GET /resources: %w", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading /resources response: %w", err)
		}
		body := string(raw)
		if strings.Contains(body, "persistent_volume_claim") {
			return fmt.Errorf("expected file_storage to be cleared, stored deployment still has it: %s", body)
		}
		for _, want := range []string{
			"redis.ray-system.svc.cluster.local:6379",
			"tfacc-filestorage-clear-bucket",
		} {
			if !strings.Contains(body, want) {
				return fmt.Errorf("expected %q to survive clearing file_storage, stored deployment: %s", want, body)
			}
		}
		return nil
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: withFileStorage,
				Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc"),
			},
			{
				// Removing the block entirely plans and applies an Update
				// that clears file_storage, not a replace and not a no-op.
				Config: withoutFileStorage,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("anyscale_cloud.test", "file_storage"),
					checkCleared,
				),
			},
		},
	})
}

// TestAccCloudResource_FileStorageImportRecoversValue covers acceptance
// criterion 6a of D2: importing a cloud that already has file_storage set
// recovers it, via requiredImportConfigBlocks' flattenFileStorage call. This
// is a COLD import (no preceding Create in this test) - per CLAUDE.md's own
// documented ImportStatePersist gotcha, an ImportState step's recovered state
// is discarded at the end of the step verifying it, so the only place that
// can see what import actually recovered is ImportStateCheck run inside that
// same step, asserted directly on the imported InstanceState's Attributes.
// ImportStateVerify is deliberately not used here: there is no prior Create
// state in this test to verify against.
func TestAccCloudResource_FileStorageImportRecoversValue(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_import_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-import", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"]
		},
		"file_storage": {"persistent_volume_claim": "ray-shared-pvc-imported"}
	}`

	server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null", nil)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "filestorage-import"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        config,
				ResourceName:  "anyscale_cloud.test",
				ImportState:   true,
				ImportStateId: cloudID,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					attrs := states[0].Attributes
					if got := attrs["file_storage.persistent_volume_claim"]; got != "ray-shared-pvc-imported" {
						return fmt.Errorf("criterion 6a FAILED: import did not recover file_storage.persistent_volume_claim, got %q: %+v",
							got, attrs)
					}
					return nil
				},
			},
		},
	})
}

// TestAccCloudResource_FileStorageImportedShapeIsPlanStable covers acceptance
// criterion 6b of D2: a config that reconstructs the shape import would
// produce (file_storage declared, matching the live deployment) plans EMPTY -
// import recovering file_storage must not itself introduce a phantom diff.
// This is the "Test B" shape CLAUDE.md prescribes for import-recovery
// criteria: two sequential Config-only steps, no ImportState involved, so
// state actually carries forward between them (unlike the throwaway
// ImportState step in criterion 6a above, whose recovered state cannot reach
// a later step in the same test).
func TestAccCloudResource_FileStorageImportedShapeIsPlanStable(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_importstable_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-importstable", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"]
		},
		"object_storage": {"bucket_name": "tfacc-filestorage-importstable-bucket"},
		"file_storage": {"persistent_volume_claim": "ray-shared-pvc-stable"}
	}`

	server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null", nil)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "filestorage-importstable"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-filestorage-importstable-bucket"
  }

  file_storage {
    persistent_volume_claim = "ray-shared-pvc-stable"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-stable"),
			},
			{
				// criterion 6b: re-planning the identical config - the shape
				// import would produce - must show no changes.
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// TestAccCloudResource_FileStorageUpdateRunningClustersError covers
// acceptance criterion 7 of D2: when the backend refuses a file_storage
// update because clusters are running, the practitioner sees a designed
// diagnostic naming that cause, not an opaque 400 body. The "active
// clusters" substring match in addFileStorageUpdateError is sourced from
// backend source rather than a captured response - G1.3 was never run (see
// the Gate 1 results table in
// docs/decisions/cloud-file-storage-lifecycle/README.md) - so this test
// proves only the provider's own branching on that substring, not that the
// real backend sends it; the code comment documenting that gap is a
// deliberate, accepted degradation and must not be read as something this
// test could fix.
//
// putHook injects the two 400 bodies the mock would otherwise never
// produce. Both are asserted in the same test since they are two branches of
// the same match, and proving one without the other cannot show the
// substring match is selective rather than matching everything.
func TestAccCloudResource_FileStorageUpdateRunningClustersError(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_clusters_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-clusters", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"]
		},
		"object_storage": {"bucket_name": "tfacc-filestorage-clusters-bucket"},
		"file_storage": {"persistent_volume_claim": "ray-shared-pvc-v1"}
	}`

	configWithPVC := func(serverURL, pvc string) string {
		return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "filestorage-clusters"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-filestorage-clusters-bucket"
  }

  file_storage {
    persistent_volume_claim = %[1]q
  }
}
`, pvc)
	}

	t.Run("active_clusters_substring_fires_designed_diagnostic", func(t *testing.T) {
		server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null",
			func(sent map[string]interface{}) (bool, int, string) {
				return true, http.StatusBadRequest, `{"detail": "Cannot update cloud resource: active clusters are still running"}`
			})

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: ProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					// Create goes through add_resource, never PUT /resources,
					// so the putHook does not fire here - this step must
					// succeed to give the second step something to Update.
					Config: configWithPVC(server.URL, "ray-shared-pvc-v1"),
					Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v1"),
				},
				{
					// Changing the value forces an Update, which does go
					// through the PUT the putHook intercepts.
					Config:      configWithPVC(server.URL, "ray-shared-pvc-v2"),
					ExpectError: regexp.MustCompile(`(?s)Cannot Update file_storage While Clusters Are Running.*active\s+clusters`),
				},
			},
		})
	})

	t.Run("unrelated_400_falls_through_to_generic_error", func(t *testing.T) {
		server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null",
			func(sent map[string]interface{}) (bool, int, string) {
				return true, http.StatusBadRequest, `{"detail": "some unrelated validation failure"}`
			})

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: ProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: configWithPVC(server.URL, "ray-shared-pvc-v1"),
					Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v1"),
				},
				{
					Config:      configWithPVC(server.URL, "ray-shared-pvc-v2"),
					ExpectError: regexp.MustCompile(`(?s)API Request Failed.*some\s+unrelated\s+validation\s+failure`),
				},
			},
		})
	})
}

// TestAccCloudResource_FileStorageManagedCloudRefusal covers acceptance
// criterion 8 of D2: a file_storage change on a cloud created with `anyscale
// cloud setup` (rather than registered) is refused, both at plan time
// (refuseFileStorageChangeOnManagedCloud/isAnyscaleManaged, so the
// practitioner sees it before an apply starts writing) and, as a backstop, at
// apply time if the plan-time check didn't catch it
// (addFileStorageUpdateError's "anyscale-managed" substring branch, mirroring
// criterion 7's shape). isAnyscaleManaged treats any of three provenance
// fields as sufficient: AWS's cloudformation_id, or either of GCP's
// deployment_manager_id/infrastructure_manager_id - all three are exercised,
// plus a negative control (none set) proving the guard is selective rather
// than firing unconditionally.
func TestAccCloudResource_FileStorageManagedCloudRefusal(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_managed_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-managed", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)

	configWithPVC := func(serverURL, pvc string) string {
		return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "filestorage-managed"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-filestorage-managed-bucket"
  }

  file_storage {
    persistent_volume_claim = %[1]q
  }
}
`, pvc)
	}

	resourcesJSONWithManagedField := func(managedFieldJSON string) string {
		return fmt.Sprintf(`{
			"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
			"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
			"kubernetes_config": {
				"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
				"zones": ["us-central1-a", "us-central1-b"]
			},
			"object_storage": {"bucket_name": "tfacc-filestorage-managed-bucket"},
			"file_storage": {"persistent_volume_claim": "ray-shared-pvc-v1"}
			%s
		}`, managedFieldJSON)
	}

	const managedDiagnosticPattern = `(?s)Cannot Update file_storage on an Anyscale-Managed Cloud`

	planTimeCases := []struct {
		name             string
		managedFieldJSON string
		wantRefused      bool
	}{
		{
			name:             "aws_cloudformation_id",
			managedFieldJSON: `, "aws_config": {"cloudformation_id": "cfn-mock-managed-id"}`,
			wantRefused:      true,
		},
		{
			name:             "gcp_deployment_manager_id",
			managedFieldJSON: `, "gcp_config": {"deployment_manager_id": "dm-mock-managed-id"}`,
			wantRefused:      true,
		},
		{
			name:             "gcp_infrastructure_manager_id",
			managedFieldJSON: `, "gcp_config": {"infrastructure_manager_id": "im-mock-managed-id"}`,
			wantRefused:      true,
		},
		{
			name:             "not_managed_no_refusal",
			managedFieldJSON: ``,
			wantRefused:      false,
		},
	}

	for _, tc := range planTimeCases {
		t.Run("plan_time_"+tc.name, func(t *testing.T) {
			server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON,
				resourcesJSONWithManagedField(tc.managedFieldJSON), "null", nil)

			steps := []resource.TestStep{
				{
					// Create is unguarded (ModifyPlan skips a null-state plan), so this
					// must succeed regardless of tc.wantRefused to give the second step
					// something to Update.
					Config: configWithPVC(server.URL, "ray-shared-pvc-v1"),
					Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v1"),
				},
			}
			if tc.wantRefused {
				steps = append(steps, resource.TestStep{
					Config:      configWithPVC(server.URL, "ray-shared-pvc-v2"),
					ExpectError: regexp.MustCompile(managedDiagnosticPattern),
				})
			} else {
				steps = append(steps, resource.TestStep{
					Config: configWithPVC(server.URL, "ray-shared-pvc-v2"),
					Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v2"),
				})
			}

			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps:                    steps,
			})
		})
	}

	t.Run("apply_time_anyscale_managed_substring_backstop", func(t *testing.T) {
		// live GET carries no managed field, so the plan-time guard does not fire -
		// this isolates the apply-time addFileStorageUpdateError translation as its
		// own backstop, mirroring criterion 7's shape.
		server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON,
			resourcesJSONWithManagedField(""), "null",
			func(sent map[string]interface{}) (bool, int, string) {
				return true, http.StatusBadRequest, `{"detail": "Cannot update: cloud resource is anyscale-managed"}`
			})

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: ProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: configWithPVC(server.URL, "ray-shared-pvc-v1"),
					Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v1"),
				},
				{
					Config:      configWithPVC(server.URL, "ray-shared-pvc-v2"),
					ExpectError: regexp.MustCompile(managedDiagnosticPattern),
				},
			},
		})
	})
}

// newMountPathMockCloudServer builds a mock like newC3MockCloudServer, but
// with an add_resource response that DOES carry file_storage - the shared
// helper's stub never does, so it can't exercise the Create-time
// mergeFileStorageDerivedFields resolution the two tests below need.
func newMountPathMockCloudServer(t *testing.T, cloudID, cloudJSON, resourcesJSON, addResourceFileStorageJSON string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.Method {
		case http.MethodPost:
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		case http.MethodGet:
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds", r.Method)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s", r.Method, cloudID)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results": %s, "metadata": {"total": 1, "next_paging_token": null}}`, resourcesJSON)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {"cloud_deployment_id": "cldrsrc_mock_default", "cloud_resource_id": "cldrsrc_mock_default", "file_storage": %s}}`, addResourceFileStorageJSON)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccCloudResource_MountPathOmittedDoesNotForceReplace pins D1's Gate 2:
// a real resource.Test proving the Framework/Core plan-modifier contract,
// since a plain mapping-function unit test can't exercise it. Before D1,
// mount_path carried only RequiresReplace(): an Optional+Computed attribute
// the config omits plans to Unknown on every subsequent apply, and
// RequiresReplace treats "planned Unknown, prior state known" as a
// difference - forcing a destroy-and-recreate of an already-live cloud on
// every single plan, with no real config change.
// stringplanmodifier.UseStateForUnknown(), placed before RequiresReplace()
// in PlanModifiers, resolves the Unknown to the prior state value first, so
// RequiresReplace sees no diff - mirroring the pre-existing mount_targets
// list attribute's identical ordering.
func TestAccCloudResource_MountPathOmittedDoesNotForceReplace(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_mountpath_omit_noreplace_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "mountpath-omit-noreplace", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"
	}`, cloudID)
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"compute_stack": "VM", "region": "us-central1",
		"gcp_config": {
			"project_id": "my-gcp-project",
			"provider_name": "projects/my-gcp-project/serviceAccounts/anyscale@my-gcp-project.iam.gserviceaccount.com",
			"vpc_name": "vpc-mountpath",
			"subnet_names": ["subnet-mountpath"],
			"anyscale_service_account_email": "anyscale@my-gcp-project.iam.gserviceaccount.com",
			"cluster_service_account_email": "cluster@my-gcp-project.iam.gserviceaccount.com"
		},
		"object_storage": {"bucket_name": "tfacc-mountpath-omit-bucket"},
		"file_storage": {"file_storage_id": "filestore-omit-test", "mount_path": "/mnt/filestore-real"}
	}]`
	addResourceFileStorageJSON := `{"file_storage_id": "filestore-omit-test", "mount_path": "/mnt/filestore-real"}`

	server := newMountPathMockCloudServer(t, cloudID, cloudJSON, resourcesJSON, addResourceFileStorageJSON)
	resourceName := "anyscale_cloud.test"
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "mountpath-omit-noreplace"
  cloud_provider = "GCP"
  compute_stack  = "VM"
  region         = "us-central1"

  gcp_config {
    project_id                     = "my-gcp-project"
    provider_name                  = "projects/my-gcp-project/serviceAccounts/anyscale@my-gcp-project.iam.gserviceaccount.com"
    vpc_name                       = "vpc-mountpath"
    subnet_names                   = ["subnet-mountpath"]
    controlplane_service_account_email = "anyscale@my-gcp-project.iam.gserviceaccount.com"
    dataplane_service_account_email    = "cluster@my-gcp-project.iam.gserviceaccount.com"
  }

  object_storage {
    bucket_name = "tfacc-mountpath-omit-bucket"
  }

  # mount_path deliberately omitted - the backend resolves and returns a
  # real value; this config never sets it.
  file_storage {
    file_storage_id = "filestore-omit-test"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Create resolves mount_path from the real add_resource
				// response (D1's mergeFileStorageDerivedFields path), not a
				// fabricated default.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "file_storage.file_storage_id", "filestore-omit-test"),
					resource.TestCheckResourceAttr(resourceName, "file_storage.mount_path", "/mnt/filestore-real"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				// G2.1: re-applying the SAME config (mount_path still
				// omitted) must plan a no-op, not a forced replace.
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
// clean - this is the multi-subnet case that
// subnet-names-gcp-supports-multiple-no-cardinality-validator confirmed is a
// real, intentional, tested backend feature, not something to reject. Runs
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

// Helper function to check if cloud exists in API and fetch its details
func testAccCheckCloudExistsInAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("no Cloud ID is set")
		}

		// Get the API client
		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		// Make API call to verify cloud exists
		cloudID := rs.Primary.ID
		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
		if err != nil {
			return fmt.Errorf("API request failed: %w", err)
		}
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Printf("[WARN] Failed to close response body: %v", closeErr)
			}
		}()

		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("cloud %s not found in API", cloudID)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("API returned error status %d: %s", resp.StatusCode, string(body))
		}

		var cloudResp provider.CloudResponse
		if err := json.Unmarshal(body, &cloudResp); err != nil {
			return fmt.Errorf("failed to parse API response: %w", err)
		}

		if cloudResp.Result.ID != cloudID {
			return fmt.Errorf("cloud ID mismatch: expected %s, got %s", cloudID, cloudResp.Result.ID)
		}

		return nil
	}
}

// Helper function to validate cloud attributes in the API
func testAccCheckCloudAttributes(resourceName, expectedName, expectedProvider, expectedRegion string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}

		cloudID := rs.Primary.ID

		// Get the API client
		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		// Fetch cloud from API
		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
		if err != nil {
			return fmt.Errorf("API request failed: %w", err)
		}
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Printf("[WARN] Failed to close response body: %v", closeErr)
			}
		}()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("API returned error status %d: %s", resp.StatusCode, string(body))
		}

		var cloudResp provider.CloudResponse
		if err := json.Unmarshal(body, &cloudResp); err != nil {
			return fmt.Errorf("failed to parse API response: %w", err)
		}

		// Validate attributes
		if cloudResp.Result.Name != expectedName {
			return fmt.Errorf("name mismatch: expected %s, got %s", expectedName, cloudResp.Result.Name)
		}

		if cloudResp.Result.Provider != expectedProvider {
			return fmt.Errorf("provider mismatch: expected %s, got %s", expectedProvider, cloudResp.Result.Provider)
		}

		if cloudResp.Result.Region != expectedRegion {
			return fmt.Errorf("region mismatch: expected %s, got %s", expectedRegion, cloudResp.Result.Region)
		}

		return nil
	}
}

// testAccCheckCloudDestroy verifies that clouds created by tests are properly
// destroyed. Shares the same poll-for-async-delete behavior every other
// resource's CheckDestroy gets from NewAPIDestroyCheck, instead of the
// one-shot GET this used to hand-roll.
var testAccCheckCloudDestroy = NewAPIDestroyCheck("anyscale_cloud", "/api/v2/clouds/%s")

// Configuration templates

func testAccCloudResourceAWSBasicConfig(name, randSuffix string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "%s"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"

%s
}
`, name, awsConfigBlock("tfacc-aws-basic", randSuffix))
}

func testAccCloudResourceAWSEmptyConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "%s"
  cloud_provider = "AWS"
  region         = "us-east-2"
}
`, name)
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

func testAccCloudResourceGCPBasicConfig(name, randSuffix string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "%s"
  cloud_provider = "GCP"
  compute_stack  = "VM"
  region         = "us-central1"

%s
}
`, name, gcpConfigBlock("tfacc-gcp-basic", randSuffix))
}

func testAccCloudResourceAWSK8SConfig(name, randSuffix, namespace, redisEndpoint string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "%s"
  cloud_provider = "AWS"
  compute_stack  = "K8S"
  region         = "us-east-2"

%s

  object_storage {
    bucket_name = "tfacc-aws-k8s-bucket-%s"
  }
}
`, name, k8sConfigBlock(namespace, fmt.Sprintf("arn:aws:iam::123456789012:role/tfacc-aws-k8s-operator-%s", randSuffix), []string{"us-east-2a", "us-east-2b"}, redisEndpoint), randSuffix)
}

func testAccCloudResourceGCPK8SConfig(name, randSuffix, redisEndpoint string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "%s"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

%s

  object_storage {
    // Deliberately BARE (no gs:// prefix) - this is the realistic example
    // form (examples/gcp-gke-basic wires the same bare module output) and is
    // what surfaced BUG A live via ANYSCALE_TEST_REAL_INFRA=1 (2026-07-16):
    // apply stores this bare value, but import flattens the API's canonical
    // gs://-prefixed form, and stripBucketPrefix only un-prefixes AWS - so
    // the two diverged. The fix is a
    // semantic-equality type/plan-modifier on bucket_name, NOT
    // canonicalizing the test to gs://: this bare form must keep working
    // once that fix lands, since real existing GCP clouds may have been
    // created with a bare name too, and bucket_name is RequiresReplace -
    // silently forcing a canonical form would spuriously replace them. Keep
    // this test bare so it's a genuine regression guard for that fix, not a
    // way to dodge the bug.
    bucket_name = "tfacc-gcp-k8s-bucket-%s"
  }
}
`, name, k8sConfigBlock("anyscale", fmt.Sprintf("tfacc-gcp-k8s-operator-%s@my-gcp-project.iam.gserviceaccount.com", randSuffix), []string{"us-central1-a", "us-central1-b"}, redisEndpoint), randSuffix)
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

// TestAccCloudResource_Disappears verifies that an out-of-band cloud deletion
// is detected by the next plan as drift rather than silently succeeding.
func TestAccCloudResource_Disappears(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-disappears")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceAWSEmptyConfig(cloudName),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckCloudExistsInAPI("anyscale_cloud.test"),
					testAccDeleteCloudViaAPI("anyscale_cloud.test"),
				),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// testAccDeleteCloudViaAPI deletes the cloud directly via the Anyscale API so
// the next plan must observe drift. 200/202/204/404 all count as success.
func testAccDeleteCloudViaAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}

		cloudID := rs.Primary.ID
		if cloudID == "" {
			return fmt.Errorf("no Cloud ID is set for %s", resourceName)
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		resp, err := client.DoRequest(context.Background(), "DELETE", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
		if err != nil {
			return fmt.Errorf("failed to delete cloud %s via API: %w", cloudID, err)
		}
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Printf("[WARN] Failed to close response body: %v", closeErr)
			}
		}()

		switch resp.StatusCode {
		case http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound:
			return nil
		default:
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("unexpected status %d deleting cloud %s: %s", resp.StatusCode, cloudID, truncateBody(string(body), 256))
		}
	}
}
