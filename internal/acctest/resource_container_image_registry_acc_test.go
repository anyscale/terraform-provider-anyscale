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
	t.Parallel()
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
				ImportStateVerifyIgnore: []string{
					"registry_login_secret", // sensitive: API never returns auth secrets after create
					"name",                  // Optional-only schema field; auto-generated when omitted and not rehydrated to avoid drift on null configs
					"ray_version",           // Optional-only schema field; rehydrated only when the user set it, so import on null configs is ignored
					// Real backend timing gap, not a provider bug - same root cause as
					// TestAccContainerImageBuildResource_Basic's digest exclusion: a
					// build's digest becomes available in a second internal completion
					// state that isn't guaranteed to have settled by the time "succeeded"
					// first appears, with no bounded latency between the two. See that
					// test's comment and the digest timing gap report for detail and the
					// real-fix recommendation (forge/production code).
					"digest",
				},
			},
		},
	})
}

// TestAccContainerImageRegistryResource_BYOD tests registering a BYOD (Bring Your Own Docker) image.
// Uses a fake ECR-style URI to test the BYOD registration flow.
func TestAccContainerImageRegistryResource_BYOD(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	imageName := UniqueName(t, "img-registry-byod")
	// Use a fake ECR-style URI - the API accepts the format even if the image doesn't exist
	// Format: <account_id>.dkr.ecr.<region>.amazonaws.com/<repository>:<tag>
	fakeECRImageURI := "123456789012.dkr.ecr.us-west-2.amazonaws.com/my-ray-image:latest"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// API archives (not deletes) the cluster environment on destroy — verify
		// archived_at is set via /api/v2/application_templates, not deleted_at
		// via /ext/v0/cluster_environments (see the longer note in
		// resource_container_image_build_acc_test.go — the /ext/v0 read model
		// never serializes archived_at, so its deleted_at can never reflect an
		// archive regardless of how long you poll).
		//
		// Plain (non-ByAttr) variant, keyed on rs.Primary.ID directly — not
		// ByAttr("cluster_environment_id"). V1(c) removed that attribute
		// outright, so a lookup by that name would hit a missing map key
		// (Go returns "" for that, not an error), tripping
		// newAPIDestroyCheckImpl's id == "" guard and silently skipping the
		// API call entirely — CheckDestroy would report success having
		// never checked whether the resource was actually archived. Same
		// false-green shape TestRegistryCheckDestroy_KeyingOnBuildIDWouldSilentlySkipBuildlessOrphan
		// (helpers_checkdestroy_test.go) already proves for build_id.
		//
		// Using the plain variant rather than ByAttr("id") mirrors
		// container_image_build's own CheckDestroy just above this resource
		// in the same family (same endpoint pattern, same archivedJSONPath)
		// and reads rs.Primary.ID directly instead of going through
		// rs.Primary.Attributes["id"] — one fewer assumption about how the
		// legacy flatmap shim represents state.
		CheckDestroy: NewAPIArchivedDestroyCheck("anyscale_container_image_registry", "/api/v2/application_templates/%s", "result.archived_at"),
		Steps: []resource.TestStep{
			{
				Config: testAccContainerImageRegistryResourceBYODConfig(imageName, fakeECRImageURI),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_container_image_registry.test", "id"),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "name", imageName),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "image_uri", fakeECRImageURI),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "is_byod", "true"),
					resource.TestCheckResourceAttrSet("anyscale_container_image_registry.test", "build_id"),
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
