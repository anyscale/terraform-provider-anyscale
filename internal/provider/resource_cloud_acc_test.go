package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccCloudResource_AWS_Basic tests basic AWS cloud creation with all-in-one pattern
func TestAccCloudResource_AWS_Basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudName := "tfacc-test-aws-basic"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccCloudResourceAWSBasicConfig(cloudName),
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
			},
			// ImportState testing
			{
				ResourceName:      "anyscale_cloud.test",
				ImportState:       true,
				ImportStateVerify: true,
				// API doesn't return full config details, so ignore these fields
				ImportStateVerifyIgnore: []string{
					"credentials",
					"aws_config",
					"gcp_config",
					"azure_config",
					"kubernetes_config",
					"object_storage",
					"file_storage",
					"compute_stack",
					"is_empty_cloud",
					"auto_add_user",
					"enable_lineage_tracking",
					"enable_log_ingestion",
					"is_private_cloud",
				},
			},
		},
	})
}

// TestAccCloudResource_AWS_EmptyCloud tests AWS empty cloud pattern
func TestAccCloudResource_AWS_EmptyCloud(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudName := "tfacc-test-aws-empty"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
			},
		},
	})
}

// TestAccCloudResource_GCP_Basic tests basic GCP cloud creation
func TestAccCloudResource_GCP_Basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudName := "tfacc-test-gcp-basic"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceGCPBasicConfig(cloudName),
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
			},
			{
				ResourceName:      "anyscale_cloud.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials",
					"gcp_config",
					"object_storage",
					"compute_stack",
					"is_empty_cloud",
					"auto_add_user",
					"enable_lineage_tracking",
					"enable_log_ingestion",
					"is_private_cloud",
				},
			},
		},
	})
}

// TestAccCloudResource_AWS_K8S tests AWS K8S cloud creation
func TestAccCloudResource_AWS_K8S(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudName := "tfacc-test-aws-k8s"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceAWSK8SConfig(cloudName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "name", cloudName),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "cloud_provider", "AWS"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "compute_stack", "K8S"),
					resource.TestCheckResourceAttrSet("anyscale_cloud.test", "id"),
					// API validation
					testAccCheckCloudExistsInAPI("anyscale_cloud.test"),
					testAccCheckCloudAttributes("anyscale_cloud.test", cloudName, "AWS", "us-east-2"),
				),
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
		client, err := getTestClient()
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

		var cloudResp CloudResponse
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
		client, err := getTestClient()
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

		var cloudResp CloudResponse
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

// Helper function to get an authenticated test client
func getTestClient() (*Client, error) {
	// Get API URL from environment or use default
	apiURL := os.Getenv("ANYSCALE_API_URL")
	if apiURL == "" {
		apiURL = "https://console.anyscale.com"
	}

	// Get token from environment or credentials file
	token := os.Getenv("ANYSCALE_CLI_TOKEN")
	if token == "" {
		var err error
		token, err = GetAuthToken()
		if err != nil {
			return nil, fmt.Errorf("failed to get auth token: %w", err)
		}
	}

	if token == "" {
		return nil, fmt.Errorf("no authentication token available")
	}

	return NewClientWithToken(apiURL, token), nil
}

// Configuration templates

func testAccCloudResourceAWSBasicConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "%s"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"

  aws_config {
    vpc_id             = "vpc-test123"
    subnet_ids         = ["subnet-test1", "subnet-test2"]
    security_group_ids = ["sg-test1"]

    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/anyscale-crossaccount-role"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/anyscale-cluster-node-role"
    external_id               = "anyscale-external-id-test"

    subnet_ids_to_az = {
      "subnet-test1" = "us-east-2a"
      "subnet-test2" = "us-east-2b"
    }
  }
}
`, name)
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

func testAccCloudResourceGCPBasicConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "%s"
  cloud_provider = "GCP"
  compute_stack  = "VM"
  region         = "us-central1"

  gcp_config {
    project_id                        = "my-gcp-project"
    vpc_name                          = "anyscale-vpc"
    subnet_names                      = ["anyscale-subnet-1", "anyscale-subnet-2"]
    firewall_policy_names             = ["anyscale-fw-ssh"]
    provider_name                     = "projects/123456789012/locations/global/workloadIdentityPools/anyscale-pool/providers/anyscale-provider"
    controlplane_service_account_email = "anyscale-cp@my-gcp-project.iam.gserviceaccount.com"
    dataplane_service_account_email    = "anyscale-dp@my-gcp-project.iam.gserviceaccount.com"
  }
}
`, name)
}

func testAccCloudResourceAWSK8SConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "%s"
  cloud_provider = "AWS"
  compute_stack  = "K8S"
  region         = "us-east-2"

  kubernetes_config {
    namespace                       = "anyscale"
    anyscale_operator_iam_identity  = "arn:aws:iam::123456789012:role/anyscale-operator-role"
    zones                           = ["us-east-2a", "us-east-2b"]
  }

  object_storage {
    bucket_name = "anyscale-test-bucket"
  }
}
`, name)
}
