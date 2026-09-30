package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

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

// TestAccCloudResource_GCP_K8S tests GCP K8S (GKE) cloud creation.
// Real-infra gated; run by hand:
//
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 go test ./internal/acctest -run '^TestAccCloudResource_GCP_K8S$' -v -count=1
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
					// Create keeps the configured bare name; the backend returns GCS buckets
					// gs://-prefixed and import recovers that form. Asserted below instead.
					"object_storage.bucket_name",
				},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					want := "gs://tfacc-gcp-k8s-bucket-" + randSuffix
					if got := states[0].Attributes["object_storage.bucket_name"]; got != want {
						return fmt.Errorf("imported object_storage.bucket_name = %q, want %q", got, want)
					}
					return nil
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

// testAccCheckCloudDestroy verifies that clouds created by tests are properly
// destroyed. Shares the same poll-for-async-delete behavior every other
// resource's CheckDestroy gets from NewAPIDestroyCheck, instead of the
// one-shot GET this used to hand-roll.
var testAccCheckCloudDestroy = NewAPIDestroyCheck("anyscale_cloud", "/api/v2/clouds/%s")

// Configuration templates

func testAccCloudResourceAWSEmptyConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "%s"
  cloud_provider = "AWS"
  region         = "us-east-2"
}
`, name)
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
`, name, k8sConfigBlock(fmt.Sprintf("tfacc-gcp-k8s-operator-%s@my-gcp-project.iam.gserviceaccount.com", randSuffix), []string{"us-central1-a", "us-central1-b"}, redisEndpoint), randSuffix)
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
