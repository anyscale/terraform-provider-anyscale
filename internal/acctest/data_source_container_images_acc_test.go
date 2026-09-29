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

// TestAccContainerImagesDataSource_Basic tests unfiltered listing without building images.
// Only the default (non-archived) listing is exercised here: archived images are never
// deleted, so an unfiltered include_archived = true listing grows with every run of this
// suite. include_archived is covered by a name-filtered assertion in _WithBuild and full
// pagination by _Pagination_MockServer.
func TestAccContainerImagesDataSource_Basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "anyscale_container_images" "no_filters" {
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_container_images.no_filters", "container_images.#"),
				),
			},
		},
	})
}

// TestAccContainerImagesDataSource_WithBuild tests listing and filtering with a built image.
// This consolidates FilterByNameContains, ExcludeArchived, and FieldsPopulated tests
// to reduce build time by reusing a single built image.
func TestAccContainerImagesDataSource_WithBuild(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	imageName := UniqueName(t, "ds-imgs")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContainerImagesDataSourceWithBuildConfig(imageName),
				Check: resource.ComposeAggregateTestCheckFunc(
					// DS-IMG-7: pin the exact count, not just non-zero. imageName is a
					// UniqueName-generated random string, so name_contains matching
					// exactly one image is the expected outcome if filtering genuinely
					// narrows; a no-op filter would return every image in the (possibly
					// long-lived, never-truly-deleted per the file-level comment above)
					// test org instead of exactly this one.
					resource.TestCheckResourceAttr("data.anyscale_container_images.by_name", "container_images.#", "1"),

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
			// Drop the image resource; destroy archives it. Data sources read at plan
			// time, so anything read in this step still sees the pre-destroy image --
			// the archived assertions need the following step.
			{
				Config: testAccContainerImagesDataSourceArchivedConfig(imageName + "-none"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_container_images.archived", "container_images.#", "0"),
				),
			},
			// include_archived, bounded by name: the archived image is returned only
			// when include_archived = true.
			{
				Config: testAccContainerImagesDataSourceArchivedConfig(imageName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_container_images.archived", "container_images.#", "1"),
					resource.TestCheckResourceAttr("data.anyscale_container_images.archived", "container_images.0.name", imageName),
					resource.TestCheckResourceAttr("data.anyscale_container_images.archived", "container_images.0.is_archived", "true"),
					resource.TestCheckResourceAttr("data.anyscale_container_images.not_archived", "container_images.#", "0"),
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
  timeouts {
    create = "45m"
  }
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

func testAccContainerImagesDataSourceArchivedConfig(name string) string {
	return fmt.Sprintf(`
data "anyscale_container_images" "archived" {
  name_contains    = %[1]q
  include_archived = true
}

data "anyscale_container_images" "not_archived" {
  name_contains    = %[1]q
  include_archived = false
}
`, name)
}
