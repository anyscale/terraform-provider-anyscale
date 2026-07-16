package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
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

// testAccCheckCloudDestroy verifies that clouds created by tests are properly destroyed.
// This function is called automatically by the test framework after all test steps complete.
func testAccCheckCloudDestroy(s *terraform.State) error {
	client, err := GetTestClient()
	if err != nil {
		return fmt.Errorf("failed to get test client: %w", err)
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "anyscale_cloud" {
			continue
		}

		cloudID := rs.Primary.ID
		if cloudID == "" {
			continue
		}

		if err := verifyCloudDestroyed(client, cloudID); err != nil {
			return err
		}
	}

	return nil
}

// verifyCloudDestroyed returns nil if the cloud is gone (404) and an error
// for any state that prevents proving destruction (200, 5xx, transport error, etc.).
func verifyCloudDestroyed(client *provider.Client, cloudID string) error {
	resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
	if err != nil {
		return fmt.Errorf("verify destroy of cloud %s: %w", cloudID, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("[WARN] Failed to close response body: %v", closeErr)
		}
	}()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil
	case http.StatusOK:
		return fmt.Errorf("cloud %s still exists after destroy", cloudID)
	default:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cannot verify destroy of cloud %s: API returned status %d: %s", cloudID, resp.StatusCode, truncateBody(string(body), 256))
	}
}

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
    // the two diverged. Per architect's disposition, the fix is a
    // semantic-equality type/plan-modifier on bucket_name (Forge), NOT
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
