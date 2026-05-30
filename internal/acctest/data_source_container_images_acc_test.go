package acctest

// Container Images Data Source Acceptance Tests
//
// KNOWN LIMITATION: The Anyscale API does not currently support permanent deletion
// of container images. When resources are destroyed, they are archived but not deleted.
// This means:
// - Tests may leave behind archived container images in the Anyscale account
// - Use include_archived=true to view archived images
// - Archived images do not count against quotas but remain visible in the API
//
// This is a temporary gap that will be addressed when the Anyscale API adds
// support for permanent deletion of container images.

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccContainerImagesDataSource_Basic tests basic listing functionality without building images.
// Tests NoFilters and IncludeArchived which don't require building new images.
func TestAccContainerImagesDataSource_Basic(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Test listing with no filters
			{
				Config: `
data "anyscale_container_images" "no_filters" {
}

data "anyscale_container_images" "include_archived" {
  include_archived = true
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					// No filters - should return at least some container images (or empty list)
					resource.TestCheckResourceAttrSet("data.anyscale_container_images.no_filters", "container_images.#"),
					// Include archived - should also work
					resource.TestCheckResourceAttrSet("data.anyscale_container_images.include_archived", "container_images.#"),
				),
			},
		},
	})
}

// TestAccContainerImagesDataSource_WithBuild tests listing and filtering with a built image.
// This consolidates FilterByNameContains, ExcludeArchived, and FieldsPopulated tests
// to reduce build time by reusing a single built image.
func TestAccContainerImagesDataSource_WithBuild(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	imageName := UniqueName(t, "ds-imgs")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContainerImagesDataSourceWithBuildConfig(imageName),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify the data source returns results (filtered by name)
					resource.TestCheckResourceAttrSet("data.anyscale_container_images.by_name", "container_images.#"),

					// Verify the first image has expected fields populated
					resource.TestCheckResourceAttrSet("data.anyscale_container_images.by_name", "container_images.0.id"),
					resource.TestCheckResourceAttr("data.anyscale_container_images.by_name", "container_images.0.name", imageName),
					resource.TestCheckResourceAttrSet("data.anyscale_container_images.by_name", "container_images.0.created_at"),
					resource.TestCheckResourceAttr("data.anyscale_container_images.by_name", "container_images.0.is_archived", "false"),

					// Build-related fields should be populated for images with builds
					resource.TestCheckResourceAttrSet("data.anyscale_container_images.by_name", "container_images.0.latest_build_id"),
					resource.TestCheckResourceAttr("data.anyscale_container_images.by_name", "container_images.0.latest_build_status", "succeeded"),
					resource.TestCheckResourceAttrSet("data.anyscale_container_images.by_name", "container_images.0.revision"),

					// For a new image, revision is typically 1, so name_version should be "imageName:1"
					resource.TestCheckResourceAttr("data.anyscale_container_images.by_name", "container_images.0.name_version", fmt.Sprintf("%s:1", imageName)),

					// Verify exclude_archived also works (separate data source in same config)
					resource.TestCheckResourceAttrSet("data.anyscale_container_images.exclude_archived", "container_images.#"),
				),
			},
		},
	})
}

// Configuration template for tests that need a built image

func testAccContainerImagesDataSourceWithBuildConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_container_image_build" "test" {
  name          = "%s"
  containerfile = <<-EOF
FROM anyscale/ray:2.53.0-slim-py312
RUN pip install emoji==2.15.0
EOF
  build_timeout = "45m"
}

# Filter by name
data "anyscale_container_images" "by_name" {
  name_contains = "%s"

  depends_on = [anyscale_container_image_build.test]
}

# Test exclude archived (default behavior)
data "anyscale_container_images" "exclude_archived" {
  include_archived = false

  depends_on = [anyscale_container_image_build.test]
}
`, name, name)
}
