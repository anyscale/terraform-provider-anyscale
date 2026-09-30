package acctest

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccUserDataSource_Basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserDataSourceConfig_basic(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Check basic user fields are populated
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "email"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "name"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "username"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organization_permission_level"),
				),
			},
		},
	})
}

func TestAccUserDataSource_OrganizationData(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserDataSourceConfig_basic(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// userinfo is token-scoped: the handler always returns exactly
					// one organization, even though the field is typed as a list.
					resource.TestCheckResourceAttr("data.anyscale_user.test", "organization_ids.#", "1"),
					resource.TestCheckResourceAttr("data.anyscale_user.test", "organizations.#", "1"),
					resource.TestCheckResourceAttrPair(
						"data.anyscale_user.test", "organization_ids.0",
						"data.anyscale_user.test", "organizations.0.id",
					),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organizations.0.id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organizations.0.name"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organizations.0.public_identifier"),
					// Note: default_cloud_id is optional and may not be set in all organizations
				),
			},
		},
	})
}

// TestAccUserDataSource_CloudAccess resolves the real test cloud and asserts
// it appears in cloud_ids. Checking only that "cloud_ids.#" is set would pass
// on an empty list.
func TestAccUserDataSource_CloudAccess(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserDataSourceConfig_basic(),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckUserCloudIDsContains("data.anyscale_user.test", cloudID),
				),
			},
		},
	})
}

// testAccCheckUserCloudIDsContains asserts cloudID appears somewhere in
// usersResourceName's cloud_ids list. cloud_ids is a plain (non-Set) list, so
// element order is not guaranteed - scanning every index rather than
// asserting a fixed one.
func testAccCheckUserCloudIDsContains(resourceName, cloudID string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		count, err := strconv.Atoi(rs.Primary.Attributes["cloud_ids.#"])
		if err != nil {
			return fmt.Errorf("failed to parse cloud_ids.#: %w", err)
		}
		for i := 0; i < count; i++ {
			if rs.Primary.Attributes[fmt.Sprintf("cloud_ids.%d", i)] == cloudID {
				return nil
			}
		}
		return fmt.Errorf("expected cloud_ids to contain the resolved test cloud %q, got %d entries none matching", cloudID, count)
	}
}

func TestAccUserDataSource_WithCloudReference(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserDataSourceConfig_withCloudReference(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Check user data is populated
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "email"),
					// Check cloud data is populated
					resource.TestCheckResourceAttr("data.anyscale_cloud.test", "id", cloudID),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test", "name"),
				),
			},
		},
	})
}

func TestAccUserDataSource_OutputsWork(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserDataSourceConfig_withOutputs(),
				// Each declared output must carry the data source attribute it
				// references, and that attribute must be non-empty.
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "email"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organization_permission_level"),
					testAccCheckOutputMatchesAttr("user_id", "data.anyscale_user.test", "id"),
					testAccCheckOutputMatchesAttr("user_email", "data.anyscale_user.test", "email"),
					testAccCheckOutputMatchesAttr("user_permission_level", "data.anyscale_user.test", "organization_permission_level"),
				),
			},
		},
	})
}

// testAccCheckOutputMatchesAttr asserts the root output outputName equals
// resourceName's attr.
func testAccCheckOutputMatchesAttr(outputName, resourceName, attr string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		out, ok := s.RootModule().Outputs[outputName]
		if !ok {
			return fmt.Errorf("output %q not found in state", outputName)
		}
		want := rs.Primary.Attributes[attr]
		if got, _ := out.Value.(string); got != want {
			return fmt.Errorf("output %q = %#v, want %q (%s.%s)", outputName, out.Value, want, resourceName, attr)
		}
		return nil
	}
}

// Configuration templates

func testAccUserDataSourceConfig_basic() string {
	return `
data "anyscale_user" "test" {
}
`
}

func testAccUserDataSourceConfig_withCloudReference(cloudID string) string {
	return `
data "anyscale_user" "test" {
}

data "anyscale_cloud" "test" {
  id = "` + cloudID + `"
}
`
}

func testAccUserDataSourceConfig_withOutputs() string {
	return `
data "anyscale_user" "test" {
}

output "user_id" {
  value = data.anyscale_user.test.id
}

output "user_email" {
  value = data.anyscale_user.test.email
}

output "user_permission_level" {
  value = data.anyscale_user.test.organization_permission_level
}
`
}
