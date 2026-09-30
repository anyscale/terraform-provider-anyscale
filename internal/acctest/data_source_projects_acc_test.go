package acctest

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testAccCheckAllProjectsHaveCloudID asserts every project in the plural data
// source's result set belongs to the given cloud. Combined with
// testAccCheckProjectsContainsName below, this proves the cloud_id/cloud_name
// filter actually NARROWS the set (no unrelated cloud's project sneaking in)
// rather than the old assertion, which only checked projects.# was non-empty
// and would have passed even if the filter were silently ignored.
func testAccCheckAllProjectsHaveCloudID(resourceName, cloudID string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		count, err := strconv.Atoi(rs.Primary.Attributes["projects.#"])
		if err != nil {
			return fmt.Errorf("failed to parse projects.#: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("expected at least one project, got 0")
		}
		for i := 0; i < count; i++ {
			got := rs.Primary.Attributes[fmt.Sprintf("projects.%d.cloud_id", i)]
			if got != cloudID {
				return fmt.Errorf("projects.%d.cloud_id = %q, want %q (filter did not narrow to the requested cloud)", i, got, cloudID)
			}
		}
		return nil
	}
}

// testAccCheckProjectsContainsName asserts the plural data source's result
// set includes a project with the given name -- the complementary check to
// testAccCheckAllProjectsHaveCloudID/testAccCheckAllProjectNamesContain
// (which only prove no false positives): this proves the filter doesn't drop
// a genuine match either.
func testAccCheckProjectsContainsName(resourceName, name string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		count, err := strconv.Atoi(rs.Primary.Attributes["projects.#"])
		if err != nil {
			return fmt.Errorf("failed to parse projects.#: %w", err)
		}
		for i := 0; i < count; i++ {
			if rs.Primary.Attributes[fmt.Sprintf("projects.%d.name", i)] == name {
				return nil
			}
		}
		return fmt.Errorf("expected a project named %q in the %d-project result set, not found", name, count)
	}
}

// testAccCheckAllProjectNamesContain asserts every project's name in the
// result set contains the given substring, proving name_contains actually
// filters server-side rather than being silently ignored.
func testAccCheckAllProjectNamesContain(resourceName, substr string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		count, err := strconv.Atoi(rs.Primary.Attributes["projects.#"])
		if err != nil {
			return fmt.Errorf("failed to parse projects.#: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("expected at least one project, got 0")
		}
		for i := 0; i < count; i++ {
			name := rs.Primary.Attributes[fmt.Sprintf("projects.%d.name", i)]
			if !strings.Contains(name, substr) {
				return fmt.Errorf("projects.%d.name = %q does not contain %q (filter did not narrow by name_contains)", i, name, substr)
			}
		}
		return nil
	}
}

// testAccCheckNoProjectIsDefault asserts no project in the result set has
// is_default = true, proving include_defaults = false actually excludes them
// rather than being silently ignored.
func testAccCheckNoProjectIsDefault(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		count, err := strconv.Atoi(rs.Primary.Attributes["projects.#"])
		if err != nil {
			return fmt.Errorf("failed to parse projects.#: %w", err)
		}
		for i := 0; i < count; i++ {
			if rs.Primary.Attributes[fmt.Sprintf("projects.%d.is_default", i)] == "true" {
				return fmt.Errorf("projects.%d is_default = true, want no default projects in an include_defaults=false result", i)
			}
		}
		return nil
	}
}

// testAccCheckCreatedProjectFieldsPopulated finds the project created as
// resName in the plural data source's result set (by name) and asserts its
// fields match the created resource: same id, the given cloud_id and
// description, and non-empty created_at/directory_name. Locating the entry by
// name rather than reading projects.0 is what makes this about the created
// project and not whichever project the API happened to list first.
func testAccCheckCreatedProjectFieldsPopulated(dsName, resName, cloudID, description string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		res, ok := s.RootModule().Resources[resName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resName)
		}
		ds, ok := s.RootModule().Resources[dsName]
		if !ok {
			return fmt.Errorf("data source not found: %s", dsName)
		}
		name := res.Primary.Attributes["name"]
		count, err := strconv.Atoi(ds.Primary.Attributes["projects.#"])
		if err != nil {
			return fmt.Errorf("failed to parse projects.#: %w", err)
		}
		for i := 0; i < count; i++ {
			attr := func(k string) string { return ds.Primary.Attributes[fmt.Sprintf("projects.%d.%s", i, k)] }
			if attr("name") != name {
				continue
			}
			if got := attr("id"); got != res.Primary.ID {
				return fmt.Errorf("projects.%d.id = %q, want %q (the created project's id)", i, got, res.Primary.ID)
			}
			if got := attr("cloud_id"); got != cloudID {
				return fmt.Errorf("projects.%d.cloud_id = %q, want %q", i, got, cloudID)
			}
			if got := attr("description"); got != description {
				return fmt.Errorf("projects.%d.description = %q, want %q", i, got, description)
			}
			for _, k := range []string{"created_at", "directory_name"} {
				if attr(k) == "" {
					return fmt.Errorf("projects.%d.%s is empty for the created project", i, k)
				}
			}
			return nil
		}
		return fmt.Errorf("created project %q not found in the %d-project result set", name, count)
	}
}

func TestAccProjectsDataSource_NoFilters(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectsDataSourceNoFiltersConfig(cloudID),
				// Every cloud has a default project and include_defaults
				// defaults to true, so the result is non-empty, and every entry
				// must belong to the requested cloud.
				Check: testAccCheckAllProjectsHaveCloudID("data.anyscale_projects.test", cloudID),
			},
		},
	})
}

func TestAccProjectsDataSource_FilterByCloudID(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	projectName1 := UniqueName(t, "ds-projects-1")
	projectName2 := UniqueName(t, "ds-projects-2")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectsDataSourceFilterByCloudIDConfig(cloudID, projectName1, projectName2),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Every returned project must belong to this cloud (no
					// false positives) and both created projects must appear
					// (no false negatives) - proving cloud_id actually
					// narrows the set, not just that the call didn't error.
					testAccCheckAllProjectsHaveCloudID("data.anyscale_projects.test", cloudID),
					testAccCheckProjectsContainsName("data.anyscale_projects.test", projectName1),
					testAccCheckProjectsContainsName("data.anyscale_projects.test", projectName2),
				),
			},
		},
	})
}

func TestAccProjectsDataSource_FilterByCloudName(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	cloudName := GetTestCloudName(t)

	projectName := UniqueName(t, "ds-projects-cloudname")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectsDataSourceFilterByCloudNameConfig(cloudID, cloudName, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					// cloudName and cloudID resolve to the same cloud
					// (GetTestCloudID/GetTestCloudName share one cache), so
					// the same narrowing proof applies as FilterByCloudID.
					testAccCheckAllProjectsHaveCloudID("data.anyscale_projects.test", cloudID),
					testAccCheckProjectsContainsName("data.anyscale_projects.test", projectName),
				),
			},
		},
	})
}

func TestAccProjectsDataSource_FilterByNameContains(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	uniquePrefix := UniqueName(t, "ds-projects-prefix")
	projectName1 := fmt.Sprintf("%s-project-1", uniquePrefix)
	projectName2 := fmt.Sprintf("%s-project-2", uniquePrefix)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectsDataSourceFilterByNameContainsConfig(cloudID, projectName1, projectName2, uniquePrefix),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Every returned project's name must contain the unique
					// prefix (no false positives) and both created projects
					// must appear (no false negatives) - proving
					// name_contains actually filters server-side.
					testAccCheckAllProjectNamesContain("data.anyscale_projects.test", uniquePrefix),
					testAccCheckProjectsContainsName("data.anyscale_projects.test", projectName1),
					testAccCheckProjectsContainsName("data.anyscale_projects.test", projectName2),
				),
			},
		},
	})
}

func TestAccProjectsDataSource_ExcludeDefaults(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	projectName := UniqueName(t, "ds-projects-nodefault")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectsDataSourceExcludeDefaultsConfig(cloudID, projectName),
				Check: resource.ComposeAggregateTestCheckFunc(
					// No project in the result set may be the default
					// project - proving include_defaults=false actually
					// excludes it rather than being silently ignored.
					testAccCheckNoProjectIsDefault("data.anyscale_projects.test"),
					testAccCheckProjectsContainsName("data.anyscale_projects.test", projectName),
				),
			},
		},
	})
}

func TestAccProjectsDataSource_ProjectFieldsPopulated(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	projectName := UniqueName(t, "ds-projects-fields")
	description := "Test project for data source fields"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectsDataSourceProjectFieldsConfig(cloudID, projectName, description),
				Check: testAccCheckCreatedProjectFieldsPopulated(
					"data.anyscale_projects.test", "anyscale_project.test", cloudID, description,
				),
			},
		},
	})
}

// Configuration templates

func testAccProjectsDataSourceNoFiltersConfig(cloudID string) string {
	return fmt.Sprintf(`
data "anyscale_projects" "test" {
  cloud_id = "%s"
}
`, cloudID)
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
