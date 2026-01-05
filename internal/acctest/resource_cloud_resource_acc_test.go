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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccCloudResourceResource_AWS_VM tests AWS VM cloud resource creation
func TestAccCloudResourceResource_AWS_VM(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := "tfacc-test-cloud-res-aws"
	resourceName := "default"
	// Generate random suffix for IAM roles to allow parallel test runs
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)

	resource.Test(t, resource.TestCase{
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
			},
			// ImportState testing with composite ID (cloud_id:resource_name)
			{
				ResourceName:      "anyscale_cloud_resource.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: testAccCloudResourceImportStateIdFunc("anyscale_cloud_resource.test"),
				// API doesn't return full config details, so ignore these fields
				ImportStateVerifyIgnore: []string{
					"aws_config",
					"gcp_config",
					"azure_config",
					"kubernetes_config",
					"object_storage",
					"file_storage",
					"cloud_provider",
				},
			},
		},
	})
}

// TestAccCloudResourceResource_GCP_VM tests GCP VM cloud resource creation
func TestAccCloudResourceResource_GCP_VM(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := "tfacc-test-cloud-res-gcp"
	resourceName := "default"
	// Generate random suffix for service accounts to allow parallel test runs
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)

	resource.Test(t, resource.TestCase{
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
			},
			{
				ResourceName:      "anyscale_cloud_resource.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: testAccCloudResourceImportStateIdFunc("anyscale_cloud_resource.test"),
				ImportStateVerifyIgnore: []string{
					"gcp_config",
					"object_storage",
					"cloud_provider",
				},
			},
		},
	})
}

// TestAccCloudResourceResource_AWS_K8S tests AWS K8S cloud resource creation
func TestAccCloudResourceResource_AWS_K8S(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := "tfacc-test-cloud-res-k8s"
	resourceName := "default"
	// Generate random suffix for IAM roles to allow parallel test runs
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudResourceDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceResourceK8SConfig(cloudName, resourceName, randSuffix),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_cloud_resource.test", "cloud_id"),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "name", resourceName),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "compute_stack", "K8S"),
					// API validation
					testAccCheckCloudResourceExistsInAPI("anyscale_cloud_resource.test", resourceName),
					testAccCheckCloudResourceAttributes("anyscale_cloud_resource.test", resourceName, "K8S"),
				),
			},
		},
	})
}

// TestAccCloudResourceResource_WithFileStorage tests cloud resource with file storage
func TestAccCloudResourceResource_WithFileStorage(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	// Generate random suffix for cloud resources to allow parallel test runs and avoid conflicts
	randSuffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)
	cloudName := fmt.Sprintf("tfacc-test-cloud-res-fs-%s", randSuffix)
	resourceName := fmt.Sprintf("with-file-storage-%s", randSuffix)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudResourceDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceResourceWithFileStorageConfig(cloudName, resourceName, randSuffix),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_cloud_resource.test", "cloud_id"),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "name", resourceName),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "file_storage.file_storage_id", "fs-test123"),
					// API validation
					testAccCheckCloudResourceExistsInAPI("anyscale_cloud_resource.test", resourceName),
				),
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

		// Verify resource ID fields are set
		if foundDeployment.CloudResourceID == "" {
			return fmt.Errorf("cloud_resource_id is empty in API response")
		}

		if foundDeployment.CloudDeploymentID == "" {
			return fmt.Errorf("cloud_deployment_id is empty in API response")
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
// are properly destroyed. This checks both anyscale_cloud and anyscale_cloud_resource.
func testAccCheckCloudResourceDestroy(s *terraform.State) error {
	client, err := GetTestClient()
	if err != nil {
		return fmt.Errorf("failed to get test client: %w", err)
	}

	// Check cloud resources
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "anyscale_cloud_resource" && rs.Type != "anyscale_cloud" {
			continue
		}

		if rs.Type == "anyscale_cloud" {
			cloudID := rs.Primary.ID
			if cloudID == "" {
				continue
			}

			// Check if the cloud still exists
			resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
			if err != nil {
				log.Printf("[WARN] Failed to check cloud %s during destroy verification: %v", cloudID, err)
				continue
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode == http.StatusOK {
				return fmt.Errorf("cloud %s still exists after destroy", cloudID)
			}
		}

		if rs.Type == "anyscale_cloud_resource" {
			cloudID := rs.Primary.Attributes["cloud_id"]
			resourceName := rs.Primary.Attributes["name"]
			if cloudID == "" || resourceName == "" {
				continue
			}

			// Check if the cloud resource still exists by fetching cloud deployments
			resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s/deployments", cloudID), nil)
			if err != nil {
				// Cloud might already be deleted, which is fine
				log.Printf("[DEBUG] Cloud %s may already be deleted: %v", cloudID, err)
				continue
			}
			defer func() { _ = resp.Body.Close() }()

			// If cloud is gone (404), the resource is definitely destroyed
			if resp.StatusCode == http.StatusNotFound {
				continue
			}

			// If we can still fetch deployments, check if our resource is in the list
			if resp.StatusCode == http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				// If the resource name appears in the response, it wasn't destroyed
				if len(body) > 0 {
					var deploymentsResp struct {
						Results []struct {
							Name string `json:"name"`
						} `json:"results"`
					}
					if err := json.Unmarshal(body, &deploymentsResp); err == nil {
						for _, d := range deploymentsResp.Results {
							if d.Name == resourceName {
								return fmt.Errorf("cloud resource %s:%s still exists after destroy", cloudID, resourceName)
							}
						}
					}
				}
			}
		}
	}

	return nil
}

// Configuration templates

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

  aws_config {
    vpc_id             = "vpc-test123"
    subnet_ids         = ["subnet-test1", "subnet-test2"]
    security_group_ids = ["sg-test1"]

    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/tfacc-cloudres-aws-crossaccount-%s"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/tfacc-cloudres-aws-cluster-node-%s"
    external_id               = "anyscale-external-id-test"

    subnet_ids_to_az = {
      "subnet-test1" = "us-east-2a"
      "subnet-test2" = "us-east-2b"
    }
  }

  object_storage {
    bucket_name = "tfacc-cres-aws-bucket-%s"
  }
}
`, cloudName, resourceName, randSuffix, randSuffix, randSuffix)
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

  gcp_config {
    project_id                        = "my-gcp-project"
    vpc_name                          = "anyscale-vpc"
    subnet_names                      = ["anyscale-subnet-1", "anyscale-subnet-2"]
    firewall_policy_names             = ["anyscale-fw-ssh"]
    provider_name                     = "projects/123456789012/locations/global/workloadIdentityPools/tf-cres-gcp-pool-%s/providers/tf-cres-gcp-prov-%s"
    controlplane_service_account_email = "tf-cres-gcp-cp-%s@my-gcp-project.iam.gserviceaccount.com"
    dataplane_service_account_email    = "tf-cres-gcp-dp-%s@my-gcp-project.iam.gserviceaccount.com"
  }

  object_storage {
    bucket_name = "tfacc-cres-gcp-bucket-%s"
  }
}
`, cloudName, resourceName, randSuffix, randSuffix, randSuffix, randSuffix, randSuffix)
}

func testAccCloudResourceResourceK8SConfig(cloudName, resourceName, randSuffix string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test_cloud" {
  name           = "%s"
  cloud_provider = "AWS"
  compute_stack  = "K8S"
  region         = "us-east-2"
}

resource "anyscale_cloud_resource" "test" {
  cloud_id       = anyscale_cloud.test_cloud.id
  name           = "%s"
  cloud_provider = "AWS"
  region         = "us-east-2"
  compute_stack  = "K8S"

  kubernetes_config {
    namespace                       = "anyscale"
    anyscale_operator_iam_identity  = "arn:aws:iam::123456789012:role/tfacc-cloudres-k8s-operator-%s"
    zones                           = ["us-east-2a", "us-east-2b"]
  }

  object_storage {
    bucket_name = "tfacc-cres-k8s-bucket-%s"
  }
}
`, cloudName, resourceName, randSuffix, randSuffix)
}

func testAccCloudResourceResourceWithFileStorageConfig(cloudName, resourceName, randSuffix string) string {
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

  aws_config {
    vpc_id             = "vpc-test123"
    subnet_ids         = ["subnet-test1", "subnet-test2"]
    security_group_ids = ["sg-test1"]

    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/tfacc-cloudres-fs-crossaccount-%s"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/tfacc-cloudres-fs-cluster-node-%s"
    external_id               = "anyscale-external-id-test"

    subnet_ids_to_az = {
      "subnet-test1" = "us-east-2a"
      "subnet-test2" = "us-east-2b"
    }
  }

  object_storage {
    bucket_name = "tfacc-cres-fs-bucket-%s"
  }

  file_storage {
    file_storage_id = "fs-test123"
    mount_targets {
      address = "fs-test123.efs.us-east-2.amazonaws.com"
      zone    = "us-east-2a"
    }
  }
}
`, cloudName, resourceName, randSuffix, randSuffix, randSuffix)
}
