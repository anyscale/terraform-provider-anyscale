package acctest

// Container Image Build Resource Acceptance Tests
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
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccContainerImageBuildResource_Basic tests building from an inline containerfile.
// This consolidates basic creation, timeout configuration, and import testing.
func TestAccContainerImageBuildResource_Basic(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	imageName := fmt.Sprintf("tfacc-test-build-basic-%d", os.Getpid())

	// Simple containerfile that just adds a pip package to the base Ray image
	containerfile := `FROM anyscale/ray:2.53.0-slim-py312
RUN pip install emoji==2.15.0`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccContainerImageBuildResourceBasicConfig(imageName, containerfile),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_container_image_build.test", "id"),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "name", imageName),
					resource.TestCheckResourceAttrSet("anyscale_container_image_build.test", "build_id"),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "build_status", "succeeded"),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "build_timeout", "45m"),
					resource.TestCheckResourceAttrSet("anyscale_container_image_build.test", "created_at"),
					resource.TestCheckResourceAttrSet("anyscale_container_image_build.test", "revision"),
					resource.TestCheckResourceAttrSet("anyscale_container_image_build.test", "name_version"),
					testAccCheckContainerImageBuildExistsInAPI("anyscale_container_image_build.test"),
				),
			},
			// ImportState testing
			{
				ResourceName:      "anyscale_container_image_build.test",
				ImportState:       true,
				ImportStateVerify: true,
				// User-provided values not stored in state after import
				ImportStateVerifyIgnore: []string{"containerfile", "containerfile_path", "build_timeout"},
			},
		},
	})
}

// TestAccContainerImageBuildResource_Update tests that updating the containerfile creates a new build revision.
func TestAccContainerImageBuildResource_Update(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	imageName := fmt.Sprintf("tfacc-test-build-update-%d", os.Getpid())

	// Initial containerfile
	containerfileV1 := `FROM anyscale/ray:2.53.0-slim-py312
RUN pip install emoji==2.15.0`

	// Updated containerfile with additional command
	containerfileV2 := `FROM anyscale/ray:2.53.0-slim-py312
RUN pip install emoji==2.15.0
# Create a directory
RUN sudo mkdir -p /anyscale/init`

	var clusterEnvID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Step 1: Create initial image
			{
				Config: testAccContainerImageBuildResourceBasicConfig(imageName, containerfileV1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "name", imageName),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "build_status", "succeeded"),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "revision", "1"),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "name_version", fmt.Sprintf("%s:1", imageName)),
					// Capture the cluster environment ID to verify it doesn't change
					CaptureResourceAttr("anyscale_container_image_build.test", "id", &clusterEnvID),
				),
			},
			// Step 2: Update containerfile - should create a new build (revision 2)
			{
				Config: testAccContainerImageBuildResourceBasicConfig(imageName, containerfileV2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "name", imageName),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "build_status", "succeeded"),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "revision", "2"),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "name_version", fmt.Sprintf("%s:2", imageName)),
					// Verify the cluster environment ID is the same (not a replacement)
					VerifyResourceAttrUnchanged("anyscale_container_image_build.test", "id", &clusterEnvID),
				),
			},
		},
	})
}

// TestAccContainerImageBuildResource_WithProjectID tests building with a project association.
// Commented out to reduce test runtime - can be enabled when testing project_id functionality.
/*
func TestAccContainerImageBuildResource_WithProjectID(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	imageName := fmt.Sprintf("tfacc-test-build-project-%d", os.Getpid())
	projectName := fmt.Sprintf("tfacc-test-project-build-%d", os.Getpid())

	containerfile := `FROM anyscale/ray:2.53.0-slim-py312
RUN pip install emoji==2.15.0`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContainerImageBuildResourceWithProjectConfig(cloudID, projectName, imageName, containerfile),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "name", imageName),
					resource.TestCheckResourceAttrSet("anyscale_container_image_build.test", "project_id"),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "build_status", "succeeded"),
					testAccCheckContainerImageBuildExistsInAPI("anyscale_container_image_build.test"),
				),
			},
		},
	})
}
*/

// Helper functions

func testAccCheckContainerImageBuildExistsInAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}

		clusterEnvID := rs.Primary.Attributes["id"]
		if clusterEnvID == "" {
			return fmt.Errorf("cluster environment ID not set")
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/ext/v0/cluster_environments/%s", clusterEnvID), nil)
		if err != nil {
			return fmt.Errorf("failed to get cluster environment: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != 200 {
			return fmt.Errorf("cluster environment not found in API: status %d", resp.StatusCode)
		}

		return nil
	}
}

// Configuration templates

func testAccContainerImageBuildResourceBasicConfig(name, containerfile string) string {
	return fmt.Sprintf(`
resource "anyscale_container_image_build" "test" {
  name          = "%s"
  containerfile = <<-EOF
%s
EOF
  build_timeout = "45m"
}
`, name, containerfile)
}

// Kept for potential future use with project_id testing
/*
func testAccContainerImageBuildResourceWithProjectConfig(cloudID, projectName, imageName, containerfile string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project for container image build"
}

resource "anyscale_container_image_build" "test" {
  name          = "%s"
  project_id    = anyscale_project.test.id
  containerfile = <<-EOF
%s
EOF
  build_timeout = "45m"
}
`, projectName, cloudID, imageName, containerfile)
}
*/
