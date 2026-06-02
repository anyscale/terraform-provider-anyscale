package acctest

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccProjectDataSource_ByID(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	projectName := UniqueName(t, "ds-project-id")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create a project, then look it up by ID
			{
				Config: testAccProjectDataSourceByIDConfig(cloudID, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttrSet("data.anyscale_project.test", "id"),
					resource.TestCheckResourceAttr("data.anyscale_project.test", "cloud_id", cloudID),
					resource.TestCheckResourceAttrSet("data.anyscale_project.test", "created_at"),
					resource.TestCheckResourceAttrSet("data.anyscale_project.test", "creator_id"),
					resource.TestCheckResourceAttrSet("data.anyscale_project.test", "directory_name"),
					resource.TestCheckResourceAttr("data.anyscale_project.test", "is_default", "false"),
					// Collaborators should be present (empty list is fine)
					resource.TestCheckResourceAttrSet("data.anyscale_project.test", "collaborators.#"),
				),
			},
		},
	})
}

func TestAccProjectDataSource_ByName(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	projectName := UniqueName(t, "ds-project-name")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create a project, then look it up by name
			{
				Config: testAccProjectDataSourceByNameConfig(cloudID, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttrSet("data.anyscale_project.test", "id"),
					resource.TestCheckResourceAttr("data.anyscale_project.test", "cloud_id", cloudID),
				),
			},
		},
	})
}

func TestAccProjectDataSource_ByNameWithCloudFilter(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	projectName := UniqueName(t, "ds-project-filter")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectDataSourceByNameWithCloudFilterConfig(cloudID, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("data.anyscale_project.test", "cloud_id", cloudID),
				),
			},
		},
	})
}

func TestAccProjectDataSource_ByNameWithCloudName(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	cloudName := GetTestCloudName(t)

	projectName := UniqueName(t, "ds-project-cloudname")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectDataSourceByNameWithCloudNameConfig(cloudID, cloudName, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("data.anyscale_project.test", "cloud_id", cloudID),
				),
			},
		},
	})
}

func TestAccProjectDataSource_WithCollaborators(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	testEmail := os.Getenv("ANYSCALE_TEST_USER_EMAIL_1")
	if testEmail == "" {
		t.Skip("ANYSCALE_TEST_USER_EMAIL_1 not set, skipping collaborator test")
	}

	projectName := UniqueName(t, "ds-project-collab")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectDataSourceWithCollaboratorsConfig(cloudID, projectName, testEmail),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("data.anyscale_project.test", "collaborators.#", "1"),
					resource.TestCheckResourceAttr("data.anyscale_project.test", "collaborators.0.email", testEmail),
					resource.TestCheckResourceAttr("data.anyscale_project.test", "collaborators.0.permission_level", "owner"),
					resource.TestCheckResourceAttrSet("data.anyscale_project.test", "collaborators.0.identity_id"),
					resource.TestCheckResourceAttrSet("data.anyscale_project.test", "collaborators.0.user_id"),
				),
			},
		},
	})
}

// Configuration templates

func testAccProjectDataSourceByIDConfig(cloudID, projectName string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "source" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project for data source lookup by ID"
}

data "anyscale_project" "test" {
  id = anyscale_project.source.id
}
`, projectName, cloudID)
}

func testAccProjectDataSourceByNameConfig(cloudID, projectName string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "source" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project for data source"
}

data "anyscale_project" "test" {
  name = anyscale_project.source.name
}
`, projectName, cloudID)
}

func testAccProjectDataSourceByNameWithCloudFilterConfig(cloudID, projectName string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "source" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project for data source"
}

data "anyscale_project" "test" {
  name     = anyscale_project.source.name
  cloud_id = "%s"
}
`, projectName, cloudID, cloudID)
}

func testAccProjectDataSourceByNameWithCloudNameConfig(cloudID, cloudName, projectName string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "source" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project for data source"
}

data "anyscale_project" "test" {
  name       = anyscale_project.source.name
  cloud_name = "%s"
}
`, projectName, cloudID, cloudName)
}

func testAccProjectDataSourceWithCollaboratorsConfig(cloudID, projectName, email string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "source" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project for data source"

  collaborator {
    email            = "%s"
    permission_level = "owner"
  }
}

data "anyscale_project" "test" {
  id = anyscale_project.source.id
}
`, projectName, cloudID, email)
}
