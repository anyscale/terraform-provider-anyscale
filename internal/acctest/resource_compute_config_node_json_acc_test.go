package acctest

// Per-node flags and advanced_instance_config are JSON strings. The API returns
// them re-marshalled (compact, sorted keys), so they must compare by JSON
// meaning rather than byte for byte, and invalid JSON must be rejected at plan
// time instead of being dropped from the request.

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

func nodeJSONConfig(serverURL, name, nodeAttrs string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name      = %q
  cloud_id  = "cld_mock_cc"
  head_node = {
    instance_type = "m5.large"
%[2]s
  }
  worker_nodes = [
    {
      instance_type = "m5.xlarge"
%[2]s
    },
  ]
}
`, name, nodeAttrs)
}

func TestAccComputeConfigResource_NodeJSONComparedSemantically_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, _ := newMockComputeConfigServerWithState(t)
	// Valid JSON that is not in the compact, key-sorted form the API echoes back.
	attrs := `    flags                    = "{\"b\": 2,  \"a\": 1}"
    advanced_instance_config = <<-EOT
      {
        "IamInstanceProfile": {"Arn": "arn:aws:iam::123456789012:instance-profile/x"}
      }
    EOT`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: nodeJSONConfig(server.URL, "cc-node-json", attrs),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
		}},
	})
}

func TestAccComputeConfigResource_NodeInvalidJSONRejectedAtPlan_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newMockComputeConfigServerWithState(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      nodeJSONConfig(server.URL, "cc-node-badjson", `    advanced_instance_config = "not json"`),
				ExpectError: regexp.MustCompile(`Invalid JSON String Value`),
			},
			{
				Config:             nodeJSONConfig(server.URL, "cc-node-badjson", ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				Check:              expectCreateRequests(mock, 0),
			},
		},
	})
}

// Positive control on the same path: jsonencode output already matches the
// API's form and applies cleanly on every provider version.
func TestAccComputeConfigResource_NodeJSONEncodedApplies_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, _ := newMockComputeConfigServerWithState(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: nodeJSONConfig(server.URL, "cc-node-jsonencode", `    flags = jsonencode({ a = 1, b = 2 })`),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
		}},
	})
}
