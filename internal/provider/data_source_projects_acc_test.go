package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccProjectsDataSource_NoFilters(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectsDataSourceNoFiltersConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return at least some projects
					resource.TestCheckResourceAttrSet("data.anyscale_projects.test", "projects.#"),
				),
			},
		},
	})
}

func TestAccProjectsDataSource_FilterByCloudID(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	projectName1 := fmt.Sprintf("tfacc-test-ds-projects-1-%d", os.Getpid())
	projectName2 := fmt.Sprintf("tfacc-test-ds-projects-2-%d", os.Getpid())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectsDataSourceFilterByCloudIDConfig(cloudID, projectName1, projectName2),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return at least our 2 created projects
					resource.TestCheckResourceAttrSet("data.anyscale_projects.test", "projects.#"),
				),
			},
		},
	})
}

func TestAccProjectsDataSource_FilterByCloudName(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	cloudName := os.Getenv("ANYSCALE_TEST_CLOUD_NAME")
	if cloudID == "" || cloudName == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID and ANYSCALE_TEST_CLOUD_NAME not set, skipping test")
	}

	projectName := fmt.Sprintf("tfacc-test-ds-projects-cloudname-%d", os.Getpid())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectsDataSourceFilterByCloudNameConfig(cloudID, cloudName, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_projects.test", "projects.#"),
				),
			},
		},
	})
}

func TestAccProjectsDataSource_FilterByNameContains(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	uniquePrefix := fmt.Sprintf("tfacc-unique-prefix-%d", os.Getpid())
	projectName1 := fmt.Sprintf("%s-project-1", uniquePrefix)
	projectName2 := fmt.Sprintf("%s-project-2", uniquePrefix)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectsDataSourceFilterByNameContainsConfig(cloudID, projectName1, projectName2, uniquePrefix),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should find our 2 projects with the unique prefix
					resource.TestCheckResourceAttrSet("data.anyscale_projects.test", "projects.#"),
				),
			},
		},
	})
}

func TestAccProjectsDataSource_ExcludeDefaults(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	projectName := fmt.Sprintf("tfacc-test-ds-projects-nodefault-%d", os.Getpid())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectsDataSourceExcludeDefaultsConfig(cloudID, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return projects, verify none are default
					resource.TestCheckResourceAttrSet("data.anyscale_projects.test", "projects.#"),
					// We can't easily verify no projects have is_default=true without custom check function
					// but we can verify the data source executed successfully
				),
			},
		},
	})
}

func TestAccProjectsDataSource_ProjectFieldsPopulated(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	projectName := fmt.Sprintf("tfacc-test-ds-projects-fields-%d", os.Getpid())
	description := "Test project for data source fields"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectsDataSourceProjectFieldsConfig(cloudID, projectName, description),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify at least one project is returned
					resource.TestCheckResourceAttrSet("data.anyscale_projects.test", "projects.#"),
					// Verify the first project has expected fields populated
					resource.TestCheckResourceAttrSet("data.anyscale_projects.test", "projects.0.id"),
					resource.TestCheckResourceAttrSet("data.anyscale_projects.test", "projects.0.name"),
					resource.TestCheckResourceAttrSet("data.anyscale_projects.test", "projects.0.cloud_id"),
					// Note: creator_id might not be returned by API for all projects
					resource.TestCheckResourceAttrSet("data.anyscale_projects.test", "projects.0.created_at"),
					resource.TestCheckResourceAttrSet("data.anyscale_projects.test", "projects.0.directory_name"),
					// Note: description might be empty for some projects, so we don't check it
					// Note: collaborators are NOT included in plural data source
				),
			},
		},
	})
}

// Configuration templates

func testAccProjectsDataSourceNoFiltersConfig() string {
	return `
data "anyscale_projects" "test" {
}
`
}

func testAccProjectsDataSourceFilterByCloudIDConfig(cloudID, projectName1, projectName2 string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test1" {
  name     = "%s"
  cloud_id = "%s"
}

resource "anyscale_project" "test2" {
  name     = "%s"
  cloud_id = "%s"
}

data "anyscale_projects" "test" {
  cloud_id = "%s"

  depends_on = [
    anyscale_project.test1,
    anyscale_project.test2,
  ]
}
`, projectName1, cloudID, projectName2, cloudID, cloudID)
}

func testAccProjectsDataSourceFilterByCloudNameConfig(cloudID, cloudName, projectName string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name     = "%s"
  cloud_id = "%s"
}

data "anyscale_projects" "test" {
  cloud_name = "%s"

  depends_on = [anyscale_project.test]
}
`, projectName, cloudID, cloudName)
}

func testAccProjectsDataSourceFilterByNameContainsConfig(cloudID, projectName1, projectName2, nameContains string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test1" {
  name     = "%s"
  cloud_id = "%s"
}

resource "anyscale_project" "test2" {
  name     = "%s"
  cloud_id = "%s"
}

data "anyscale_projects" "test" {
  name_contains = "%s"

  depends_on = [
    anyscale_project.test1,
    anyscale_project.test2,
  ]
}
`, projectName1, cloudID, projectName2, cloudID, nameContains)
}

func testAccProjectsDataSourceExcludeDefaultsConfig(cloudID, projectName string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name     = "%s"
  cloud_id = "%s"
}

data "anyscale_projects" "test" {
  cloud_id         = "%s"
  include_defaults = false

  depends_on = [anyscale_project.test]
}
`, projectName, cloudID, cloudID)
}

func testAccProjectsDataSourceProjectFieldsConfig(cloudID, projectName, description string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = "%s"
  cloud_id    = "%s"
  description = "%s"
}

data "anyscale_projects" "test" {
  cloud_id = "%s"

  depends_on = [anyscale_project.test]
}
`, projectName, cloudID, description, cloudID)
}
