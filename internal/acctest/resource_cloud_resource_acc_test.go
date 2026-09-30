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
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

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
