package acctest

import (
	"context"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccProjectResource_Basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	projectName := UniqueName(t, "project-basic")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccProjectResourceBasicConfig(cloudID, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_project.test", "id"),
					resource.TestCheckResourceAttr("anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("anyscale_project.test", "cloud_id", cloudID),
					resource.TestCheckResourceAttrSet("anyscale_project.test", "created_at"),
					resource.TestCheckResourceAttrSet("anyscale_project.test", "creator_id"),
					resource.TestCheckResourceAttrSet("anyscale_project.test", "directory_name"),
					resource.TestCheckResourceAttr("anyscale_project.test", "is_default", "false"),
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// ImportState testing
			{
				ResourceName:      "anyscale_project.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"cloud_name", // input-only alias for cloud_id; project API stores only parent_cloud_id
				},
			},
		},
	})
}

func TestAccProjectResource_WithDescription(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	projectName := UniqueName(t, "project-desc")
	description := "Test project with description"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccProjectResourceWithDescriptionConfig(cloudID, projectName, description),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("anyscale_project.test", "description", description),
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
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

func TestAccProjectResource_WithCloudName(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudName := GetTestCloudName(t)

	projectName := UniqueName(t, "project-cloudname")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccProjectResourceWithCloudNameConfig(cloudName, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("anyscale_project.test", "cloud_name", cloudName),
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
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

func TestAccProjectResource_WithCollaborators(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	// Note: This test requires valid test user emails
	// Skip if not provided
	testEmail1 := os.Getenv("ANYSCALE_TEST_USER_EMAIL_1")
	testEmail2 := os.Getenv("ANYSCALE_TEST_USER_EMAIL_2")
	if testEmail1 == "" || testEmail2 == "" {
		t.Skip("ANYSCALE_TEST_USER_EMAIL_1 and ANYSCALE_TEST_USER_EMAIL_2 not set, skipping collaborator test")
	}

	projectName := UniqueName(t, "project-collab")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			// Create with collaborators
			{
				Config: testAccProjectResourceWithCollaboratorsConfig(cloudID, projectName, testEmail1, testEmail2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "2"),
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Update collaborators (remove one, add different permission)
			{
				Config: testAccProjectResourceWithUpdatedCollaboratorsConfig(cloudID, projectName, testEmail1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "1"),
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

func testAccCheckProjectExistsInAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}

		projectID := rs.Primary.Attributes["id"]
		if projectID == "" {
			return fmt.Errorf("project ID not set")
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/projects/%s", projectID), nil)
		if err != nil {
			return fmt.Errorf("failed to get project: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != 200 {
			return fmt.Errorf("project not found in API: status %d", resp.StatusCode)
		}

		return nil
	}
}

// Configuration templates

func testAccProjectResourceBasicConfig(cloudID, projectName string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project created by acceptance tests"
}
`, projectName, cloudID)
}

func testAccProjectResourceWithDescriptionConfig(cloudID, projectName, description string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = "%s"
  cloud_id    = "%s"
  description = "%s"
}
`, projectName, cloudID, description)
}

func testAccProjectResourceWithCloudNameConfig(cloudName, projectName string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = "%s"
  cloud_name  = "%s"
  description = "Test project using cloud_name"
}
`, projectName, cloudName)
}

func testAccProjectResourceWithCollaboratorsConfig(cloudID, projectName, email1, email2 string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project with collaborators"

  collaborator {
    email            = "%s"
    permission_level = "owner"
  }

  collaborator {
    email            = "%s"
    permission_level = "writer"
  }
}
`, projectName, cloudID, email1, email2)
}

func testAccProjectResourceWithUpdatedCollaboratorsConfig(cloudID, projectName, email1 string) string {
	return fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project with collaborators"

  collaborator {
    email            = "%s"
    permission_level = "writer"
  }
}
`, projectName, cloudID, email1)
}

func TestAccProjectResource_WithUserDataSource(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	projectName := UniqueName(t, "project-datasource")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			// Create project with current user as collaborator using data source
			{
				Config: testAccProjectResourceWithUserDataSourceConfig(cloudID, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "name", projectName),
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "1"),
					resource.TestCheckResourceAttrPair(
						"anyscale_project.test", "collaborator.0.email",
						"data.anyscale_user.current", "email",
					),
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.0.permission_level", "owner"),
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
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

func testAccProjectResourceWithUserDataSourceConfig(cloudID, projectName string) string {
	return fmt.Sprintf(`
data "anyscale_user" "current" {}

resource "anyscale_project" "test" {
  name        = "%s"
  cloud_id    = "%s"
  description = "Test project using user data source"

  collaborator {
    email            = data.anyscale_user.current.email
    permission_level = "owner"
  }
}
`, projectName, cloudID)
}

// TestAccProjectResource_Disappears verifies that an out-of-band project
// deletion is detected by the next plan as drift.
func TestAccProjectResource_Disappears(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	projectName := UniqueName(t, "project-disappears")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_project", "/api/v2/projects/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccProjectResourceBasicConfig(cloudID, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckProjectExistsInAPI("anyscale_project.test"),
					testAccDeleteProjectViaAPI("anyscale_project.test"),
				),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// testAccDeleteProjectViaAPI deletes the project directly via the Anyscale API
// so the next plan must observe drift. 200/202/204/404 all count as success.
func testAccDeleteProjectViaAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}

		projectID := rs.Primary.ID
		if projectID == "" {
			return fmt.Errorf("no Project ID is set for %s", resourceName)
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		resp, err := client.DoRequest(context.Background(), "DELETE", fmt.Sprintf("/api/v2/projects/%s", projectID), nil)
		if err != nil {
			return fmt.Errorf("failed to delete project %s via API: %w", projectID, err)
		}
		defer func() { _ = resp.Body.Close() }()

		switch resp.StatusCode {
		case 200, 202, 204, 404:
			return nil
		default:
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("unexpected status %d deleting project %s: %s", resp.StatusCode, projectID, truncateBody(string(body), 256))
		}
	}
}
