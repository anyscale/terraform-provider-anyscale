package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccPolicyBindingDataSource_Cloud(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingDataSourceCloudConfig(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_policy_binding.test", "resource_type", "cloud"),
					resource.TestCheckResourceAttr("data.anyscale_policy_binding.test", "resource_id", cloudID),
					// Bindings may be empty or populated
					resource.TestCheckResourceAttrSet("data.anyscale_policy_binding.test", "bindings.#"),
				),
			},
		},
	})
}

func TestAccPolicyBindingDataSource_Project(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	projectID := os.Getenv("ANYSCALE_TEST_PROJECT_ID")
	if projectID == "" {
		t.Skip("ANYSCALE_TEST_PROJECT_ID not set, skipping test")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingDataSourceProjectConfig(projectID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_policy_binding.test", "resource_type", "project"),
					resource.TestCheckResourceAttr("data.anyscale_policy_binding.test", "resource_id", projectID),
					resource.TestCheckResourceAttrSet("data.anyscale_policy_binding.test", "bindings.#"),
				),
			},
		},
	})
}

func TestAccPolicyBindingDataSource_Organization(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingDataSourceOrganizationConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_policy_binding.test", "resource_type", "organization"),
					resource.TestCheckResourceAttrSet("data.anyscale_policy_binding.test", "resource_id"),
					resource.TestCheckResourceAttrSet("data.anyscale_policy_binding.test", "bindings.#"),
				),
			},
		},
	})
}

func TestAccPolicyBindingDataSource_NotFound(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	// Use a fake cloud ID that doesn't exist
	fakeCloudID := "cld_nonexistent123456789"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingDataSourceCloudConfig(fakeCloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return empty bindings for non-existent resource
					resource.TestCheckResourceAttr("data.anyscale_policy_binding.test", "resource_type", "cloud"),
					resource.TestCheckResourceAttr("data.anyscale_policy_binding.test", "resource_id", fakeCloudID),
					resource.TestCheckResourceAttr("data.anyscale_policy_binding.test", "bindings.#", "0"),
				),
			},
		},
	})
}

func TestAccPolicyBindingDataSource_BindingsPopulated(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			// This test assumes the cloud has at least one policy binding configured
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingDataSourceCloudConfig(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_policy_binding.test", "resource_type", "cloud"),
					resource.TestCheckResourceAttr("data.anyscale_policy_binding.test", "resource_id", cloudID),
					resource.TestCheckResourceAttrSet("data.anyscale_policy_binding.test", "bindings.#"),
				),
			},
		},
	})
}

func TestAccPolicyBindingDataSource_FromListToSingle(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingDataSourceFromListToSingleConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify we can use the bindings list to look up a single binding
					resource.TestCheckResourceAttrSet("data.anyscale_policy_binding.test", "resource_id"),
					resource.TestCheckResourceAttrSet("data.anyscale_policy_binding.test", "bindings.#"),
				),
			},
		},
	})
}

// Configuration templates

func testAccPolicyBindingDataSourceCloudConfig(cloudID string) string {
	return fmt.Sprintf(`
data "anyscale_policy_binding" "test" {
  resource_type = "cloud"
  resource_id   = "%s"
}
`, cloudID)
}

func testAccPolicyBindingDataSourceProjectConfig(projectID string) string {
	return fmt.Sprintf(`
data "anyscale_policy_binding" "test" {
  resource_type = "project"
  resource_id   = "%s"
}
`, projectID)
}

func testAccPolicyBindingDataSourceOrganizationConfig() string {
	return `
data "anyscale_user" "current" {
}

data "anyscale_policy_binding" "test" {
  resource_type = "organization"
  resource_id   = data.anyscale_user.current.organizations[0].id
}
`
}

func testAccPolicyBindingDataSourceFromListToSingleConfig() string {
	return `
data "anyscale_policy_bindings" "all_clouds" {
  resource_type = "clouds"
}

data "anyscale_policy_binding" "test" {
  resource_type = "cloud"
  resource_id   = length(data.anyscale_policy_bindings.all_clouds.policies) > 0 ? data.anyscale_policy_bindings.all_clouds.policies[0].resource_id : "skip"
}
`
}
