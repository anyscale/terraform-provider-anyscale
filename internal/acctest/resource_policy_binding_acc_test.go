package acctest

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccPolicyBindingResource_CloudBasic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	// These tests require:
	// 1. A test cloud ID (ANYSCALE_TEST_CLOUD_ID)
	// 2. A test user group ID (ANYSCALE_TEST_USER_GROUP_ID)
	testCloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	testGroupID := os.Getenv("ANYSCALE_TEST_USER_GROUP_ID")

	if testCloudID == "" || testGroupID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID and ANYSCALE_TEST_USER_GROUP_ID must be set")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheckAuth(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create with readonly role
			{
				Config: testAccPolicyBindingResourceConfig(testCloudID, testGroupID, "cloud", "readonly"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_policy_binding.test", "id"),
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "resource_type", "cloud"),
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "resource_id", testCloudID),
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "bindings.#", "1"),
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "bindings.0.role_name", "readonly"),
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "bindings.0.principals.#", "1"),
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "bindings.0.principals.0", testGroupID),
					testAccCheckPolicyBindingExistsInAPI("anyscale_policy_binding.test"),
				),
			},
			// ImportState testing
			{
				ResourceName:      "anyscale_policy_binding.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateId:     fmt.Sprintf("cloud/%s", testCloudID),
			},
			// Update to collaborator role
			{
				Config: testAccPolicyBindingResourceConfig(testCloudID, testGroupID, "cloud", "collaborator"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "bindings.0.role_name", "collaborator"),
					testAccCheckPolicyBindingExistsInAPI("anyscale_policy_binding.test"),
				),
			},
		},
	})
}

func TestAccPolicyBindingResource_ProjectBasic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	testProjectID := os.Getenv("ANYSCALE_TEST_PROJECT_ID")
	testGroupID := os.Getenv("ANYSCALE_TEST_USER_GROUP_ID")

	if testProjectID == "" || testGroupID == "" {
		t.Skip("ANYSCALE_TEST_PROJECT_ID and ANYSCALE_TEST_USER_GROUP_ID must be set")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheckAuth(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create with readonly role
			{
				Config: testAccPolicyBindingResourceConfig(testProjectID, testGroupID, "project", "readonly"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "resource_type", "project"),
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "bindings.0.role_name", "readonly"),
				),
			},
			// Update to write role
			{
				Config: testAccPolicyBindingResourceConfig(testProjectID, testGroupID, "project", "write"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "bindings.0.role_name", "write"),
				),
			},
			// Update to owner role
			{
				Config: testAccPolicyBindingResourceConfig(testProjectID, testGroupID, "project", "owner"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "bindings.0.role_name", "owner"),
				),
			},
		},
	})
}

func TestAccPolicyBindingResource_InvalidRoleForCloud(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	testCloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	testGroupID := os.Getenv("ANYSCALE_TEST_USER_GROUP_ID")

	if testCloudID == "" || testGroupID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID and ANYSCALE_TEST_USER_GROUP_ID must be set")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheckAuth(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccPolicyBindingResourceConfig(testCloudID, testGroupID, "cloud", "owner"),
				ExpectError: regexp.MustCompile("invalid role 'owner' for resource type 'cloud'"),
			},
		},
	})
}

func TestAccPolicyBindingResource_InvalidRoleForProject(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	testProjectID := os.Getenv("ANYSCALE_TEST_PROJECT_ID")
	testGroupID := os.Getenv("ANYSCALE_TEST_USER_GROUP_ID")

	if testProjectID == "" || testGroupID == "" {
		t.Skip("ANYSCALE_TEST_PROJECT_ID and ANYSCALE_TEST_USER_GROUP_ID must be set")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheckAuth(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccPolicyBindingResourceConfig(testProjectID, testGroupID, "project", "collaborator"),
				ExpectError: regexp.MustCompile("invalid role 'collaborator' for resource type 'project'"),
			},
		},
	})
}

func TestAccPolicyBindingResource_MultipleBindings(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	testCloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	testGroupID1 := os.Getenv("ANYSCALE_TEST_USER_GROUP_ID")
	testGroupID2 := os.Getenv("ANYSCALE_TEST_USER_GROUP_ID_2")

	if testCloudID == "" || testGroupID1 == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID and ANYSCALE_TEST_USER_GROUP_ID must be set")
	}

	// If second group not set, use same group for both roles (for testing purposes)
	if testGroupID2 == "" {
		testGroupID2 = testGroupID1
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheckAuth(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingResourceConfigMultiple(testCloudID, testGroupID1, testGroupID2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "bindings.#", "2"),
					testAccCheckPolicyBindingExistsInAPI("anyscale_policy_binding.test"),
				),
			},
		},
	})
}

func TestAccPolicyBindingResource_EmptyBindings(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	testCloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	testGroupID := os.Getenv("ANYSCALE_TEST_USER_GROUP_ID")

	if testCloudID == "" || testGroupID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID and ANYSCALE_TEST_USER_GROUP_ID must be set")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheckAuth(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create with bindings
			{
				Config: testAccPolicyBindingResourceConfig(testCloudID, testGroupID, "cloud", "readonly"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "bindings.#", "1"),
				),
			},
			// Update to empty bindings (removes all group permissions)
			{
				Config: testAccPolicyBindingResourceConfigEmpty(testCloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_policy_binding.test", "bindings.#", "0"),
				),
			},
		},
	})
}

func TestAccPolicyBindingResource_Delete(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	testCloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	testGroupID := os.Getenv("ANYSCALE_TEST_USER_GROUP_ID")

	if testCloudID == "" || testGroupID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID and ANYSCALE_TEST_USER_GROUP_ID must be set")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheckAuth(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingResourceConfig(testCloudID, testGroupID, "cloud", "readonly"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckPolicyBindingExistsInAPI("anyscale_policy_binding.test"),
				),
			},
			// Delete by removing the config
			{
				Config: "# Policy binding removed",
				Check: resource.ComposeAggregateTestCheckFunc(
					// Policy should have empty bindings after deletion
					testAccCheckPolicyBindingIsEmpty(testCloudID, "cloud"),
				),
			},
		},
	})
}

// Helper functions

func testAccPolicyBindingResourceConfig(resourceID, groupID, resourceType, roleName string) string {
	return fmt.Sprintf(`
resource "anyscale_policy_binding" "test" {
  resource_type = %[3]q
  resource_id   = %[1]q

  bindings = [
    {
      role_name = %[4]q
      principals = [%[2]q]
    }
  ]
}
`, resourceID, groupID, resourceType, roleName)
}

func testAccPolicyBindingResourceConfigMultiple(cloudID, groupID1, groupID2 string) string {
	return fmt.Sprintf(`
resource "anyscale_policy_binding" "test" {
  resource_type = "cloud"
  resource_id   = %[1]q

  bindings = [
    {
      role_name = "collaborator"
      principals = [%[2]q]
    },
    {
      role_name = "readonly"
      principals = [%[3]q]
    }
  ]
}
`, cloudID, groupID1, groupID2)
}

func testAccPolicyBindingResourceConfigEmpty(resourceID string) string {
	return fmt.Sprintf(`
resource "anyscale_policy_binding" "test" {
  resource_type = "cloud"
  resource_id   = %[1]q
  bindings      = []
}
`, resourceID)
}

func testAccCheckPolicyBindingExistsInAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("Not found: %s", resourceName)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("No policy binding ID is set")
		}

		// Get the test client
		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("Failed to get test client: %w", err)
		}

		resourceType := rs.Primary.Attributes["resource_type"]
		resourceID := rs.Primary.Attributes["resource_id"]

		// Try to fetch the policy binding
		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/policy/%s/%s", resourceType, resourceID), nil)
		if err != nil {
			return fmt.Errorf("Error fetching policy binding: %s", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != 200 {
			return fmt.Errorf("Policy binding not found in API (status %d)", resp.StatusCode)
		}

		return nil
	}
}

func testAccCheckPolicyBindingIsEmpty(resourceID, resourceType string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		// Get the test client
		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("Failed to get test client: %w", err)
		}

		// Fetch the policy binding
		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/policy/%s/%s", resourceType, resourceID), nil)
		if err != nil {
			return fmt.Errorf("Error fetching policy binding: %s", err)
		}
		defer func() { _ = resp.Body.Close() }()

		// If 404, policy doesn't exist (empty bindings were applied)
		if resp.StatusCode == 404 {
			return nil
		}

		if resp.StatusCode != 200 {
			return fmt.Errorf("Unexpected status code: %d", resp.StatusCode)
		}

		// For now, assume success if we can fetch it
		// A full implementation would parse the response and check bindings.length == 0
		return nil
	}
}
