package acctest

// Removing an additional_resources entry from the middle of the list shifts
// every later entry to a new index. A shifted entry must not inherit the
// removed entry's node resources or worker names.

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func additionalShiftConfig(serverURL, entries string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name           = "cc-additional-shift"
  cloud_id       = "cld_mock_cc"
  cloud_resource = "resource-a"
  head_node      = { instance_type = "m5.large" }

  additional_resources = [
%s
  ]
}
`, entries)
}

func TestAccComputeConfigResource_RemovedAdditionalEntryDoesNotLendResources_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mu, storedDCs := newAdditionalResourcesMockServer(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: additionalShiftConfig(server.URL, `
    {
      cloud_resource = "resource-b"
      head_node      = { instance_type = "m5.large", resources = { CPU = 2 } }
      worker_nodes   = [{ name = "wb", instance_type = "m5.xlarge", resources = { CPU = 3 } }]
    },
    {
      cloud_resource = "resource-c"
      head_node      = { instance_type = "m5.large" }
      worker_nodes   = [{ instance_type = "m5.xlarge" }]
    },`)},
			{
				// resource-b is removed; resource-c moves to index 0.
				Config: additionalShiftConfig(server.URL, `
    {
      cloud_resource = "resource-c"
      head_node      = { instance_type = "m5.large" }
      worker_nodes   = [{ instance_type = "m5.xlarge" }]
    },`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("anyscale_compute_config.test", "additional_resources.0.head_node.resources.%"),
					resource.TestCheckNoResourceAttr("anyscale_compute_config.test", "additional_resources.0.worker_nodes.0.resources.%"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "additional_resources.0.worker_nodes.0.name", "m5.xlarge"),
					func(*terraform.State) error {
						mu.Lock()
						defer mu.Unlock()
						for _, dc := range *storedDCs {
							entry, _ := dc.(map[string]any)
							if entry["cloud_deployment"] != "resource-c" {
								continue
							}
							head, _ := entry["head_node_type"].(map[string]any)
							if v, ok := head["resources"]; ok {
								return fmt.Errorf("resource-c head_node sent resources %v it never configured", v)
							}
							workers, _ := entry["worker_node_types"].([]any)
							for _, w := range workers {
								wm, _ := w.(map[string]any)
								if v, ok := wm["resources"]; ok {
									return fmt.Errorf("resource-c worker sent resources %v it never configured", v)
								}
								if n, ok := wm["name"]; ok && n != "m5.xlarge" {
									return fmt.Errorf("resource-c worker sent name %v", n)
								}
							}
						}
						return nil
					},
				),
			},
		},
	})
}
