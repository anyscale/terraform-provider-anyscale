package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testAccCheckPolicyBindingDestroyed is a custom CheckDestroy. The policy
// endpoint never 404s after delete — it just returns 200 with an empty
// bindings list — so the generic NewAPIDestroyCheck helper doesn't fit.
func testAccCheckPolicyBindingDestroyed(s *terraform.State) error {
	client, err := GetTestClient()
	if err != nil {
		return fmt.Errorf("CheckDestroy(anyscale_policy_binding): failed to get test client: %w", err)
	}

	var leaks []string
	for name, rs := range s.RootModule().Resources {
		if rs.Type != "anyscale_policy_binding" {
			continue
		}

		resourceType := rs.Primary.Attributes["resource_type"]
		resourceID := rs.Primary.Attributes["resource_id"]
		if resourceType == "" || resourceID == "" {
			continue
		}

		path := fmt.Sprintf("/api/v2/policy/%s/%s", resourceType, resourceID)
		resp, err := client.DoRequest(context.Background(), "GET", path, nil)
		if err != nil {
			log.Printf("[WARN] CheckDestroy(anyscale_policy_binding) network error for %s (%s/%s): %v", name, resourceType, resourceID, err)
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode == 404:
			continue
		case resp.StatusCode >= 500:
			log.Printf("[WARN] CheckDestroy(anyscale_policy_binding) transient %d for %s (%s/%s)", resp.StatusCode, name, resourceType, resourceID)
			continue
		case resp.StatusCode != 200:
			log.Printf("[WARN] CheckDestroy(anyscale_policy_binding) unexpected status %d for %s (%s/%s)", resp.StatusCode, name, resourceType, resourceID)
			continue
		}

		if readErr != nil {
			log.Printf("[WARN] CheckDestroy(anyscale_policy_binding) failed to read body for %s (%s/%s): %v", name, resourceType, resourceID, readErr)
			continue
		}

		var parsed struct {
			Result struct {
				Bindings []json.RawMessage `json:"bindings"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			log.Printf("[WARN] CheckDestroy(anyscale_policy_binding) failed to parse body for %s (%s/%s): %v", name, resourceType, resourceID, err)
			continue
		}

		if len(parsed.Result.Bindings) > 0 {
			leaks = append(leaks, fmt.Sprintf("%s (%s/%s) still has %d bindings after destroy", name, resourceType, resourceID, len(parsed.Result.Bindings)))
		}
	}

	if len(leaks) > 0 {
		return fmt.Errorf("CheckDestroy(anyscale_policy_binding) found leaked bindings:\n  %s", strings.Join(leaks, "\n  "))
	}
	return nil
}

func TestAccPolicyBindingResource_CloudBasic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	// Policy bindings REPLACE all existing bindings on the target resource, so
	// this runs against a freshly created, disposable cloud rather than a
	// shared fixture — otherwise the test would wipe any real bindings on
	// whatever cloud ANYSCALE_TEST_CLOUD_ID happened to resolve to.
	testCloudID, _, err := createEphemeralTestCloud(t)
	if err != nil {
		t.Skipf("Could not create ephemeral test cloud: %v", err)
	}
	testGroupID := GetTestUserGroupID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPolicyBindingDestroyed,
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

	// Policy bindings REPLACE all existing bindings on the target resource, so
	// this runs against a freshly created, disposable project rather than a
	// shared fixture — otherwise the test would wipe any real bindings on
	// whatever project ANYSCALE_TEST_PROJECT_ID happened to resolve to.
	testProjectID, _, err := createEphemeralTestProject(t)
	if err != nil {
		t.Skipf("Could not create ephemeral test project: %v", err)
	}
	testGroupID := GetTestUserGroupID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPolicyBindingDestroyed,
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

	testCloudID, _, err := createEphemeralTestCloud(t)
	if err != nil {
		t.Skipf("Could not create ephemeral test cloud: %v", err)
	}
	testGroupID := GetTestUserGroupID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPolicyBindingDestroyed,
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

	testProjectID, _, err := createEphemeralTestProject(t)
	if err != nil {
		t.Skipf("Could not create ephemeral test project: %v", err)
	}
	testGroupID := GetTestUserGroupID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPolicyBindingDestroyed,
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

	testCloudID, _, err := createEphemeralTestCloud(t)
	if err != nil {
		t.Skipf("Could not create ephemeral test cloud: %v", err)
	}
	testGroupID1 := GetTestUserGroupID(t)

	// A second, distinct group isn't auto-discoverable safely (we'd have no
	// way to tell two arbitrary groups apart), so this stays an optional
	// explicit override. Falls back to the same group for both roles, which
	// still exercises the multiple-bindings-in-one-call code path.
	testGroupID2 := os.Getenv("ANYSCALE_TEST_USER_GROUP_ID_2")
	if testGroupID2 == "" {
		testGroupID2 = testGroupID1
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPolicyBindingDestroyed,
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

	testCloudID, _, err := createEphemeralTestCloud(t)
	if err != nil {
		t.Skipf("Could not create ephemeral test cloud: %v", err)
	}
	testGroupID := GetTestUserGroupID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPolicyBindingDestroyed,
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

	testCloudID, _, err := createEphemeralTestCloud(t)
	if err != nil {
		t.Skipf("Could not create ephemeral test cloud: %v", err)
	}
	testGroupID := GetTestUserGroupID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPolicyBindingDestroyed,
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

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("Failed to read policy binding response: %w", err)
		}

		var parsed struct {
			Result struct {
				Bindings []json.RawMessage `json:"bindings"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return fmt.Errorf("Failed to parse policy binding response: %w", err)
		}

		if len(parsed.Result.Bindings) > 0 {
			return fmt.Errorf("expected empty bindings for %s/%s after delete, found %d", resourceType, resourceID, len(parsed.Result.Bindings))
		}

		return nil
	}
}
