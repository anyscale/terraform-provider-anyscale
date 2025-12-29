package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccProjectDataSource_ByID(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	projectName := fmt.Sprintf("tfacc-test-ds-project-id-%d", os.Getpid())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
					resource.TestCheckResourceAttrSet("data.anyscale_project.test", "organization_id"),
					resource.TestCheckResourceAttrSet("data.anyscale_project.test", "cluster_config_id"),
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
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	projectName := fmt.Sprintf("tfacc-test-ds-project-name-%d", os.Getpid())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	projectName := fmt.Sprintf("tfacc-test-ds-project-filter-%d", os.Getpid())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	cloudName := os.Getenv("ANYSCALE_TEST_CLOUD_NAME")
	if cloudID == "" || cloudName == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID and ANYSCALE_TEST_CLOUD_NAME not set, skipping test")
	}

	projectName := fmt.Sprintf("tfacc-test-ds-project-cloudname-%d", os.Getpid())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	testEmail := os.Getenv("ANYSCALE_TEST_USER_EMAIL_1")
	if testEmail == "" {
		t.Skip("ANYSCALE_TEST_USER_EMAIL_1 not set, skipping collaborator test")
	}

	projectName := fmt.Sprintf("tfacc-test-ds-project-collab-%d", os.Getpid())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
  name     = "%s"
  cloud_id = "%s"
}

data "anyscale_project" "test" {
  id = anyscale_project.source.id
}
`, projectName, cloudID)
}

func testAccProjectDataSourceByNameConfig(cloudID, projectName string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "source" {
  name     = "%s"
  cloud_id = "%s"
}

data "anyscale_project" "test" {
  name = anyscale_project.source.name
}
`, projectName, cloudID)
}

func testAccProjectDataSourceByNameWithCloudFilterConfig(cloudID, projectName string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "source" {
  name     = "%s"
  cloud_id = "%s"
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
  name     = "%s"
  cloud_id = "%s"
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
  name     = "%s"
  cloud_id = "%s"

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
