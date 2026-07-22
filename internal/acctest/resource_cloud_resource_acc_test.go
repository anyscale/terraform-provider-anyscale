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

// TestAccCloudResourceResource_AWS_VM tests AWS VM cloud resource creation
func TestAccCloudResourceResource_AWS_VM(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloudName := UniqueName(t, "cloud-res-aws")
	resourceName := "default"
	// Generate random suffix for IAM roles to allow parallel test runs
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudResourceDestroy,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccCloudResourceResourceAWSConfig(cloudName, resourceName, randSuffix),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_cloud_resource.test", "cloud_id"),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "name", resourceName),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "compute_stack", "VM"),
					// API validation: verify resource exists in cloud deployments
					testAccCheckCloudResourceExists("anyscale_cloud_resource.test"),
					testAccCheckCloudResourceExistsInAPI("anyscale_cloud_resource.test", resourceName),
					testAccCheckCloudResourceAttributes("anyscale_cloud_resource.test", resourceName, "VM"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// ImportState testing with composite ID (cloud_id:resource_name)
			{
				ResourceName:      "anyscale_cloud_resource.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: testAccCloudResourceImportStateIdFunc("anyscale_cloud_resource.test"),
				ImportStateVerifyIgnore: []string{
					"aws_config",        // write-only block: nested attrs (subnet_ids, IAM ARNs) are not echoed back by /clouds/{id}/resources
					"gcp_config",        // write-only block: nested attrs are not echoed back by /clouds/{id}/resources
					"azure_config",      // write-only block: nested attrs are not echoed back by /clouds/{id}/resources
					"kubernetes_config", // write-only block: nested attrs are not echoed back by /clouds/{id}/resources
					"object_storage",    // write-only block: bucket name is normalized server-side and not echoed back
					"file_storage",      // write-only block: mount target details are not echoed back by /clouds/{id}/resources
				},
			},
		},
	})
}

// TestAccCloudResourceResource_GCP_VM tests GCP VM cloud resource creation
func TestAccCloudResourceResource_GCP_VM(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloudName := UniqueName(t, "cloud-res-gcp")
	resourceName := "default"
	// Generate random suffix for service accounts to allow parallel test runs
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudResourceDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceResourceGCPConfig(cloudName, resourceName, randSuffix),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_cloud_resource.test", "cloud_id"),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "name", resourceName),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "compute_stack", "VM"),
					// API validation
					testAccCheckCloudResourceExistsInAPI("anyscale_cloud_resource.test", resourceName),
					testAccCheckCloudResourceAttributes("anyscale_cloud_resource.test", resourceName, "VM"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				ResourceName:      "anyscale_cloud_resource.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: testAccCloudResourceImportStateIdFunc("anyscale_cloud_resource.test"),
				ImportStateVerifyIgnore: []string{
					"gcp_config",     // write-only block: nested attrs (project_id, subnet_names) are not echoed back by /clouds/{id}/resources
					"object_storage", // write-only block: bucket name is normalized server-side and not echoed back
				},
			},
		},
	})
}

// TestAccCloudResourceResource_AWS_K8S tests AWS K8S cloud resource creation
func TestAccCloudResourceResource_AWS_K8S(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloudName := UniqueName(t, "cloud-res-k8s")
	resourceName := "default"
	// Generate random suffix for IAM roles to allow parallel test runs
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudResourceDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceResourceK8SConfig(cloudName, resourceName, randSuffix, "anyscale"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_cloud_resource.test", "cloud_id"),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "name", resourceName),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "compute_stack", "K8S"),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "kubernetes_config.namespace", "anyscale"),
					// API validation
					testAccCheckCloudResourceExistsInAPI("anyscale_cloud_resource.test", resourceName),
					testAccCheckCloudResourceAttributes("anyscale_cloud_resource.test", resourceName, "K8S"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// regression test for task 861aaf10: kubernetes_config.namespace had no
			// RequiresReplace, so Update() (a no-op) silently swallowed this edit and
			// every subsequent plan showed the same diff forever. Editing it must now
			// plan a clean replace, and the plan after apply must be empty again.
			{
				Config: testAccCloudResourceResourceK8SConfig(cloudName, resourceName, randSuffix, "custom-ns"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "kubernetes_config.namespace", "custom-ns"),
					testAccCheckCloudResourceExistsInAPI("anyscale_cloud_resource.test", resourceName),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud_resource.test", plancheck.ResourceActionReplace),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// TestAccCloudResourceResource_WithFileStorage tests cloud resource with file storage
func TestAccCloudResourceResource_WithFileStorage(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloudName := UniqueName(t, "cloud-res-fs")
	// Random suffix for embedded IAM ARNs / bucket names in the config template.
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)
	resourceName := fmt.Sprintf("with-file-storage-%s", randSuffix)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudResourceDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceResourceWithFileStorageConfig(cloudName, resourceName, randSuffix, "/mnt/shared", "us-east-2a"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_cloud_resource.test", "cloud_id"),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "name", resourceName),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "file_storage.file_storage_id", "fs-test123"),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "file_storage.mount_path", "/mnt/shared"),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "file_storage.mount_targets.0.zone", "us-east-2a"),
					// API validation
					testAccCheckCloudResourceExistsInAPI("anyscale_cloud_resource.test", resourceName),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// regression test for task 861aaf10: file_storage.mount_path had no
			// RequiresReplace, so Update() (a no-op) silently swallowed this edit.
			// Editing it must now plan a clean replace.
			{
				Config: testAccCloudResourceResourceWithFileStorageConfig(cloudName, resourceName, randSuffix, "/mnt/custom", "us-east-2a"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "file_storage.mount_path", "/mnt/custom"),
					testAccCheckCloudResourceExistsInAPI("anyscale_cloud_resource.test", resourceName),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud_resource.test", plancheck.ResourceActionReplace),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// regression test for task 861aaf10: mount_targets had no list-level
			// RequiresReplace, so editing a target's zone hit the same swallowed-diff
			// bug via a different modifier type (listplanmodifier, not stringplanmodifier).
			{
				Config: testAccCloudResourceResourceWithFileStorageConfig(cloudName, resourceName, randSuffix, "/mnt/custom", "us-east-2b"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "file_storage.mount_targets.0.zone", "us-east-2b"),
					testAccCheckCloudResourceExistsInAPI("anyscale_cloud_resource.test", resourceName),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud_resource.test", plancheck.ResourceActionReplace),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// TestAccCloudResourceResource_GCP_K8S tests GCP K8S (GKE) cloud resource creation
func TestAccCloudResourceResource_GCP_K8S(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloudName := UniqueName(t, "cloud-res-gcp-k8s")
	resourceName := "default"
	// Generate random suffix for service accounts to allow parallel test runs
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudResourceDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceResourceGCPK8SConfig(cloudName, resourceName, randSuffix),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_cloud_resource.test", "cloud_id"),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "name", resourceName),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "compute_stack", "K8S"),
					// API validation
					testAccCheckCloudResourceExistsInAPI("anyscale_cloud_resource.test", resourceName),
					testAccCheckCloudResourceAttributes("anyscale_cloud_resource.test", resourceName, "K8S"),
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

// TestAccCloudResourceResource_AzureVM_NotSupported is the AKS-era successor to
// the original task-02118d55 regression test (formerly
// TestAccCloudResourceResource_Azure_NotSupported) - see the doc comment on its
// anyscale_cloud sibling, TestAccCloudResource_AzureVM_NotSupported, for the
// full context on why "Azure not supported" narrowed to "Azure VM not
// supported" and moved to a plan-time error. The one thing worth calling out
// here specifically: this config attaches the rejected Azure/VM
// anyscale_cloud_resource to an otherwise-valid AWS anyscale_cloud parent, and
// Terraform's config validation runs across the whole configuration before
// any apply begins - so the plan-time ValidateConfig failure on the child
// blocks the parent from being created too, same as before. No real infra
// touched either way.
func TestAccCloudResourceResource_AzureVM_NotSupported(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-res-azurevm-notsup")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudResourceDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccCloudResourceResourceAzureConfig(cloudName),
				ExpectError: regexp.MustCompile(`(?s)Azure Requires Kubernetes Compute Stack.*only support compute_stack = "K8S"`),
			},
		},
	})
}

// TestAccCloudResourceResource_Generic_NotSupported mirrors the Azure test above for
// the GENERIC provider value: confirmed with product that provider-agnostic BYO-kubeconfig
// K8s is not a v0.1.0 launch feature, so it must error clearly rather than silently no-op.
func TestAccCloudResourceResource_Generic_NotSupported(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "cloud-res-generic-notsup")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudResourceDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccCloudResourceResourceGenericConfig(cloudName),
				ExpectError: regexp.MustCompile("generic clouds are not yet supported"),
			},
		},
	})
}

// Helper function to check if cloud resource exists in state
func testAccCheckCloudResourceExists(n string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[n]
		if !ok {
			return fmt.Errorf("not found: %s", n)
		}

		if rs.Primary.Attributes["cloud_id"] == "" {
			return fmt.Errorf("no Cloud ID is set")
		}

		if rs.Primary.Attributes["name"] == "" {
			return fmt.Errorf("no Resource Name is set")
		}

		return nil
	}
}

// Helper function to check if cloud resource exists in API
func testAccCheckCloudResourceExistsInAPI(resourceName, expectedResourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}

		cloudID := rs.Primary.Attributes["cloud_id"]
		if cloudID == "" {
			return fmt.Errorf("no cloud_id set")
		}

		// Get the API client
		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		// Make API call to list cloud resources/deployments
		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), nil)
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

		var deploymentsResp provider.CloudDeploymentsResponse
		if err := json.Unmarshal(body, &deploymentsResp); err != nil {
			return fmt.Errorf("failed to parse API response: %w", err)
		}

		// Look for the resource by name
		found := false
		for _, deployment := range deploymentsResp.Results {
			if deployment.Name == expectedResourceName {
				found = true
				break
			}
		}

		if !found {
			return fmt.Errorf("cloud resource %s not found in API for cloud %s", expectedResourceName, cloudID)
		}

		return nil
	}
}

// Helper function to validate cloud resource attributes in the API
func testAccCheckCloudResourceAttributes(resourceName, expectedName, expectedComputeStack string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}

		cloudID := rs.Primary.Attributes["cloud_id"]
		if cloudID == "" {
			return fmt.Errorf("no cloud_id set")
		}

		// Get the API client
		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		// Fetch cloud resources from API
		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), nil)
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

		var deploymentsResp provider.CloudDeploymentsResponse
		if err := json.Unmarshal(body, &deploymentsResp); err != nil {
			return fmt.Errorf("failed to parse API response: %w", err)
		}

		// Find the specific resource
		var foundDeployment *provider.CloudDeploymentResult
		for _, deployment := range deploymentsResp.Results {
			if deployment.Name == expectedName {
				foundDeployment = &deployment
				break
			}
		}

		if foundDeployment == nil {
			return fmt.Errorf("cloud resource %s not found in API", expectedName)
		}

		// Validate attributes
		if foundDeployment.Name != expectedName {
			return fmt.Errorf("name mismatch: expected %s, got %s", expectedName, foundDeployment.Name)
		}

		if foundDeployment.ComputeStack != expectedComputeStack {
			return fmt.Errorf("compute_stack mismatch: expected %s, got %s", expectedComputeStack, foundDeployment.ComputeStack)
		}

		// Verify resource ID field is set
		if foundDeployment.CloudResourceID == "" {
			return fmt.Errorf("cloud_resource_id is empty in API response")
		}

		return nil
	}
}

// Helper to generate import ID in cloud_id:resource_name format
func testAccCloudResourceImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("not found: %s", resourceName)
		}

		cloudID := rs.Primary.Attributes["cloud_id"]
		resName := rs.Primary.Attributes["name"]

		if cloudID == "" || resName == "" {
			return "", fmt.Errorf("cloud_id or name is empty")
		}

		return fmt.Sprintf("%s:%s", cloudID, resName), nil
	}
}

// testAccCheckCloudResourceDestroy verifies that clouds and cloud resources created by tests
// are properly destroyed. This checks both anyscale_cloud (delegating to the shared
// testAccCheckCloudDestroy, so it gets the same poll-for-async-delete behavior every other
// resource's CheckDestroy gets) and anyscale_cloud_resource.
func testAccCheckCloudResourceDestroy(s *terraform.State) error {
	if err := testAccCheckCloudDestroy(s); err != nil {
		return err
	}

	client, err := GetTestClient()
	if err != nil {
		return fmt.Errorf("failed to get test client: %w", err)
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "anyscale_cloud_resource" {
			continue
		}
		cloudID := rs.Primary.Attributes["cloud_id"]
		resourceName := rs.Primary.Attributes["name"]
		if cloudID == "" || resourceName == "" {
			continue
		}
		if err := verifyCloudResourceDestroyed(client, cloudID, resourceName); err != nil {
			return err
		}
	}

	return nil
}

// verifyCloudResourceDestroyed checks that a cloud_resource (deployment) is gone.
// The cloud being 404 implies the deployment is gone with it; otherwise the
// deployments list must not contain the resource name.
func verifyCloudResourceDestroyed(client *provider.Client, cloudID, resourceName string) error {
	resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s/deployments", cloudID), nil)
	if err != nil {
		return fmt.Errorf("verify destroy of cloud resource %s:%s: %w", cloudID, resourceName, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("[WARN] Failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("verify destroy of cloud resource %s:%s: read body: %w", cloudID, resourceName, readErr)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cannot verify destroy of cloud resource %s:%s: API returned status %d: %s", cloudID, resourceName, resp.StatusCode, truncateBody(string(body), 256))
	}

	var deploymentsResp struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &deploymentsResp); err != nil {
		return fmt.Errorf("verify destroy of cloud resource %s:%s: parse response: %w", cloudID, resourceName, err)
	}

	for _, d := range deploymentsResp.Results {
		if d.Name == resourceName {
			return fmt.Errorf("cloud resource %s:%s still exists after destroy", cloudID, resourceName)
		}
	}

	return nil
}

// Configuration templates

func testAccCloudResourceResourceAzureConfig(cloudName string) string {
	return fmt.Sprintf(`
# Parent cloud is a normal empty AWS cloud - the point of this test is that
# adding an AZURE-provider resource to it errors, not that the parent is Azure.
resource "anyscale_cloud" "test_cloud" {
  name           = "%s"
  cloud_provider = "AWS"
  region         = "us-east-2"
}

resource "anyscale_cloud_resource" "test" {
  cloud_id       = anyscale_cloud.test_cloud.id
  name           = "azure-attempt"
  cloud_provider = "AZURE"
  region         = "eastus"
  compute_stack  = "VM"
}
`, cloudName)
}

func testAccCloudResourceResourceGenericConfig(cloudName string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test_cloud" {
  name           = "%s"
  cloud_provider = "AWS"
  region         = "us-east-2"
}

resource "anyscale_cloud_resource" "test" {
  cloud_id       = anyscale_cloud.test_cloud.id
  name           = "generic-attempt"
  cloud_provider = "GENERIC"
  compute_stack  = "K8S"

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::123456789012:role/fake"
  }
}
`, cloudName)
}

func testAccCloudResourceResourceAWSConfig(cloudName, resourceName, randSuffix string) string {
	return fmt.Sprintf(`
# First create an empty cloud
resource "anyscale_cloud" "test_cloud" {
  name           = "%s"
  cloud_provider = "AWS"
  region         = "us-east-2"
}

# Then attach a cloud resource to it
resource "anyscale_cloud_resource" "test" {
  cloud_id       = anyscale_cloud.test_cloud.id
  name           = "%s"
  cloud_provider = "AWS"
  region         = "us-east-2"
  compute_stack  = "VM"

%s

  object_storage {
    bucket_name = "tfacc-cres-aws-bucket-%s"
  }
}
`, cloudName, resourceName, awsConfigBlock("tfacc-cloudres-aws", randSuffix), randSuffix)
}

func testAccCloudResourceResourceGCPConfig(cloudName, resourceName, randSuffix string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test_cloud" {
  name           = "%s"
  cloud_provider = "GCP"
  region         = "us-central1"
}

resource "anyscale_cloud_resource" "test" {
  cloud_id       = anyscale_cloud.test_cloud.id
  name           = "%s"
  cloud_provider = "GCP"
  region         = "us-central1"
  compute_stack  = "VM"

%s

  object_storage {
    // Deliberately BARE - see the detailed comment on
    // testAccCloudResourceGCPK8SConfig in resource_cloud_acc_test.go (BUG A);
    // this must keep working once Forge's semantic-equality fix lands, not
    // be dodged by switching to gs://.
    bucket_name = "tfacc-cres-gcp-bucket-%s"
  }
}
`, cloudName, resourceName, gcpConfigBlock("tf-cres-gcp", randSuffix), randSuffix)
}

func testAccCloudResourceResourceK8SConfig(cloudName, resourceName, randSuffix, namespace string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test_cloud" {
  name           = "%s"
  cloud_provider = "AWS"
  region         = "us-east-2"
  # compute_stack deliberately omitted (stays Computed): the parent starts
  # empty and derives its compute_stack from the attached cloud_resource -
  # setting K8S explicitly here can never be honored by the empty-cloud
  # create path (C14, see quest chat).
}

resource "anyscale_cloud_resource" "test" {
  cloud_id       = anyscale_cloud.test_cloud.id
  name           = "%s"
  cloud_provider = "AWS"
  region         = "us-east-2"
  compute_stack  = "K8S"

%s

  object_storage {
    bucket_name = "tfacc-cres-k8s-bucket-%s"
  }
}
`, cloudName, resourceName, k8sConfigBlock(namespace, fmt.Sprintf("arn:aws:iam::123456789012:role/tfacc-cloudres-k8s-operator-%s", randSuffix), []string{"us-east-2a", "us-east-2b"}, ""), randSuffix)
}

func testAccCloudResourceResourceGCPK8SConfig(cloudName, resourceName, randSuffix string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test_cloud" {
  name           = "%s"
  cloud_provider = "GCP"
  region         = "us-central1"
  # compute_stack deliberately omitted (stays Computed): the parent starts
  # empty and derives its compute_stack from the attached cloud_resource -
  # setting K8S explicitly here can never be honored by the empty-cloud
  # create path (C14, see quest chat).
}

resource "anyscale_cloud_resource" "test" {
  cloud_id       = anyscale_cloud.test_cloud.id
  name           = "%s"
  cloud_provider = "GCP"
  region         = "us-central1"
  compute_stack  = "K8S"

%s

  object_storage {
    // Deliberately BARE - see the detailed comment on
    // testAccCloudResourceGCPK8SConfig in resource_cloud_acc_test.go (BUG A);
    // this must keep working once Forge's semantic-equality fix lands, not
    // be dodged by switching to gs://.
    bucket_name = "tfacc-cres-gcp-k8s-bucket-%s"
  }
}
`, cloudName, resourceName, k8sConfigBlock("anyscale", fmt.Sprintf("tfacc-cloudres-gcp-k8s-operator-%s@my-gcp-project.iam.gserviceaccount.com", randSuffix), []string{"us-central1-a", "us-central1-b"}, ""), randSuffix)
}

func testAccCloudResourceResourceWithFileStorageConfig(cloudName, resourceName, randSuffix, mountPath, mountTargetZone string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test_cloud" {
  name           = "%s"
  cloud_provider = "AWS"
  region         = "us-east-2"
}

resource "anyscale_cloud_resource" "test" {
  cloud_id       = anyscale_cloud.test_cloud.id
  name           = "%s"
  cloud_provider = "AWS"
  region         = "us-east-2"
  compute_stack  = "VM"

%s

  object_storage {
    bucket_name = "tfacc-cres-fs-bucket-%s"
  }

  file_storage {
    file_storage_id = "fs-test123"
    mount_path      = "%s"
    mount_targets = [{
      address = "fs-test123.efs.us-east-2.amazonaws.com"
      zone    = "%s"
    }]
  }
}
`, cloudName, resourceName, awsConfigBlock("tfacc-cloudres-fs", randSuffix), randSuffix, mountPath, mountTargetZone)
}
