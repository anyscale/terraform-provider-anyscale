package acctest

// Container Image Registry Resource Acceptance Tests
//
// KNOWN LIMITATION: The Anyscale API does not currently support permanent deletion
// of container images. When resources are destroyed, they are archived but not deleted.
// This means:
// - Tests may leave behind archived container images in the Anyscale account
// - Use include_archived=true on anyscale_container_images to view archived images
// - Archived images do not count against quotas but remain visible in the API
//
// This is a temporary gap that will be addressed when the Anyscale API adds
// support for permanent deletion of container images.

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccContainerImageRegistryResource_Basic(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	// Use a public Anyscale Ray image that's guaranteed to exist
	// Note: For Anyscale images, we cannot provide a custom name - the API only
	// allows cluster_env_name for external registry images
	// Also note: Anyscale-provided images (anyscale/ray:*) are NOT considered BYOD,
	// only external registry images are marked as is_byod=true
	imageURI := "anyscale/ray:2.53.0-slim-py312"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// No CheckDestroy: Anyscale-provided (is_default) cluster environments
		// cannot be archived or deleted by the API, so destroy is a no-op and
		// the underlying object intentionally persists.
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccContainerImageRegistryResourceAnyscaleImageConfig(imageURI),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_container_image_registry.test", "id"),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "image_uri", imageURI),
					resource.TestCheckResourceAttrSet("anyscale_container_image_registry.test", "build_id"),
					resource.TestCheckResourceAttrSet("anyscale_container_image_registry.test", "cluster_environment_id"),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "build_status", "succeeded"),
					// Note: Anyscale-provided images are NOT considered BYOD
					resource.TestCheckResourceAttrSet("anyscale_container_image_registry.test", "is_byod"),
					resource.TestCheckResourceAttrSet("anyscale_container_image_registry.test", "created_at"),
					testAccCheckContainerImageRegistryExistsInAPI("anyscale_container_image_registry.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// ImportState testing
			{
				ResourceName:      "anyscale_container_image_registry.test",
				ImportState:       true,
				ImportStateVerify: true,
				// Sensitive fields and user-provided values not returned by Read
				ImportStateVerifyIgnore: []string{"registry_login_secret", "name", "image_uri", "ray_version"},
			},
		},
	})
}

// TestAccContainerImageRegistryResource_BYOD tests registering a BYOD (Bring Your Own Docker) image.
// Uses a fake ECR-style URI to test the BYOD registration flow.
func TestAccContainerImageRegistryResource_BYOD(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	imageName := UniqueName(t, "img-registry-byod")
	// Use a fake ECR-style URI - the API accepts the format even if the image doesn't exist
	// Format: <account_id>.dkr.ecr.<region>.amazonaws.com/<repository>:<tag>
	fakeECRImageURI := "123456789012.dkr.ecr.us-west-2.amazonaws.com/my-ray-image:latest"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// API archives (not deletes) the cluster environment on destroy — verify deleted_at is set.
		CheckDestroy: NewAPIArchivedDestroyCheckByAttr("anyscale_container_image_registry", "cluster_environment_id", "/ext/v0/cluster_environments/%s", "result.deleted_at"),
		Steps: []resource.TestStep{
			{
				Config: testAccContainerImageRegistryResourceBYODConfig(imageName, fakeECRImageURI),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "name", imageName),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "image_uri", fakeECRImageURI),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "is_byod", "true"),
					resource.TestCheckResourceAttrSet("anyscale_container_image_registry.test", "build_id"),
					resource.TestCheckResourceAttrSet("anyscale_container_image_registry.test", "cluster_environment_id"),
					testAccCheckContainerImageRegistryExistsInAPI("anyscale_container_image_registry.test"),
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

// Helper functions

func testAccCheckContainerImageRegistryExistsInAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}

		buildID := rs.Primary.Attributes["build_id"]
		if buildID == "" {
			return fmt.Errorf("build ID not set")
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/builds/%s", buildID), nil)
		if err != nil {
			return fmt.Errorf("failed to get build: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != 200 && resp.StatusCode != 201 {
			return fmt.Errorf("build not found in API: status %d", resp.StatusCode)
		}

		return nil
	}
}

// Configuration templates

// testAccContainerImageRegistryResourceAnyscaleImageConfig creates a registry resource
// using an Anyscale image (without custom name - not allowed for Anyscale images)
func testAccContainerImageRegistryResourceAnyscaleImageConfig(imageURI string) string {
	return fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  image_uri = "%s"
}
`, imageURI)
}

// testAccContainerImageRegistryResourceBYODConfig creates a registry resource
// using a BYOD (Bring Your Own Docker) image with custom name
func testAccContainerImageRegistryResourceBYODConfig(name, imageURI string) string {
	return fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name      = "%s"
  image_uri = "%s"
}
`, name, imageURI)
}
