package acctest

// Container Image Data Source Acceptance Tests
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
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccContainerImageDataSource tests all data source functionality with a single built image.
// This consolidates ByID, ByName, and WithBuildResource tests to reduce build time.
func TestAccContainerImageDataSource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	imageName := fmt.Sprintf("tfacc-test-ds-%d", time.Now().UnixNano())
	containerfile := `FROM anyscale/ray:2.53.0-slim-py312
RUN pip install emoji==2.15.0`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Step 1: Test lookup by ID
			{
				Config: testAccContainerImageDataSourceByIDConfig(imageName, containerfile),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Check data source attributes match the resource
					resource.TestCheckResourceAttrPair(
						"data.anyscale_container_image.by_id", "id",
						"anyscale_container_image_build.test", "id",
					),
					resource.TestCheckResourceAttr("data.anyscale_container_image.by_id", "name", imageName),
					resource.TestCheckResourceAttrSet("data.anyscale_container_image.by_id", "build_id"),
					resource.TestCheckResourceAttr("data.anyscale_container_image.by_id", "build_status", "succeeded"),
					resource.TestCheckResourceAttr("data.anyscale_container_image.by_id", "is_byod", "false"),
					resource.TestCheckResourceAttrSet("data.anyscale_container_image.by_id", "created_at"),
					// Also test lookup by name in the same config
					resource.TestCheckResourceAttr("data.anyscale_container_image.by_name", "name", imageName),
					resource.TestCheckResourceAttrSet("data.anyscale_container_image.by_name", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_container_image.by_name", "build_id"),
					resource.TestCheckResourceAttr("data.anyscale_container_image.by_name", "build_status", "succeeded"),
					resource.TestCheckResourceAttr("data.anyscale_container_image.by_name", "is_byod", "false"),
					// Verify both lookups return the same resource
					resource.TestCheckResourceAttrPair(
						"data.anyscale_container_image.by_id", "id",
						"data.anyscale_container_image.by_name", "id",
					),
					resource.TestCheckResourceAttrPair(
						"data.anyscale_container_image.by_id", "build_id",
						"data.anyscale_container_image.by_name", "build_id",
					),
				),
			},
		},
	})
}

// Configuration template - tests both ID and name lookup with a single image

func testAccContainerImageDataSourceByIDConfig(name, containerfile string) string {
	return fmt.Sprintf(`
resource "anyscale_container_image_build" "test" {
  name          = "%s"
  containerfile = <<-EOF
%s
EOF
  build_timeout = "45m"
}

# Lookup by ID
data "anyscale_container_image" "by_id" {
  id = anyscale_container_image_build.test.id
}

# Lookup by name
data "anyscale_container_image" "by_name" {
  name = anyscale_container_image_build.test.name

  depends_on = [anyscale_container_image_build.test]
}
`, name, containerfile)
}
