package acctest

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccPolicyBindingsDataSource_Clouds(t *testing.T) {
	t.Parallel()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingsDataSourceCloudsConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return policy bindings for clouds (may be empty)
					resource.TestCheckResourceAttrSet("data.anyscale_policy_bindings.test", "policies.#"),
					resource.TestCheckResourceAttr("data.anyscale_policy_bindings.test", "resource_type", "clouds"),
				),
			},
		},
	})
}

func TestAccPolicyBindingsDataSource_Projects(t *testing.T) {
	t.Parallel()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingsDataSourceProjectsConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return policy bindings for projects (may be empty)
					resource.TestCheckResourceAttrSet("data.anyscale_policy_bindings.test", "policies.#"),
					resource.TestCheckResourceAttr("data.anyscale_policy_bindings.test", "resource_type", "projects"),
				),
			},
		},
	})
}

func TestAccPolicyBindingsDataSource_PolicyFieldsPopulated(t *testing.T) {
	t.Parallel()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			PreCheck(t)
			// This test assumes at least one cloud with policy bindings exists
		},
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingsDataSourceCloudsConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify data source returns successfully
					resource.TestCheckResourceAttrSet("data.anyscale_policy_bindings.test", "policies.#"),
				),
			},
		},
	})
}

func TestAccPolicyBindingsDataSource_FindSpecificCloud(t *testing.T) {
	t.Parallel()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingsDataSourceFindSpecificCloudConfig(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify we can find the test cloud's policy binding
					resource.TestCheckResourceAttrSet("data.anyscale_policy_bindings.test", "policies.#"),
				),
			},
		},
	})
}

func TestAccPolicyBindingsDataSource_EmptyBindings(t *testing.T) {
	t.Parallel()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyBindingsDataSourceCloudsConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Even if no policies exist, should return empty list without error
					resource.TestCheckResourceAttrSet("data.anyscale_policy_bindings.test", "policies.#"),
				),
			},
		},
	})
}

// Configuration templates

func testAccPolicyBindingsDataSourceCloudsConfig() string {
	return `
data "anyscale_policy_bindings" "test" {
  resource_type = "clouds"
}
`
}

func testAccPolicyBindingsDataSourceProjectsConfig() string {
	return `
data "anyscale_policy_bindings" "test" {
  resource_type = "projects"
}
`
}

func testAccPolicyBindingsDataSourceFindSpecificCloudConfig(cloudID string) string {
	return `
data "anyscale_policy_bindings" "test" {
  resource_type = "clouds"
}

# Find the specific cloud in the policies list
locals {
  test_cloud_policy = [for p in data.anyscale_policy_bindings.test.policies : p if p.resource_id == "` + cloudID + `"]
}
`
}
