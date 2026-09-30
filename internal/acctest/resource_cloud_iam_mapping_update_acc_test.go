package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccCloudIAMMappingResource_UpdateInPlace_MockServer proves a rules
// change is an in-place update, not a replace, and that the update actually
// reaches the backend. The plan check pins the action before apply; the
// post-apply check reads the mock's stored spec directly over HTTP rather
// than from Terraform state, because state after apply is just the plan
// written back and would look correct even if Update never sent the PUT.
func TestAccCloudIAMMappingResource_UpdateInPlace_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_iammap_update"
	const cloudResourceID = "cldrsrc_iammap_update"
	const addr = "anyscale_cloud_iam_mapping.test"

	server := newCloudIAMMappingMockServer(t, cloudID, cloudResourceID)
	defer server.Close()

	config := func(value string) string {
		return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud_iam_mapping" "test" {
  cloud_id          = %[1]q
  cloud_resource_id = %[2]q

  rules = [
    { selector = "workload-type=job", value = %[3]q }
  ]
  fallback_rule = "CLOUD_DEFAULT"
}
`, cloudID, cloudResourceID, value)
	}

	configURL := fmt.Sprintf("%s/api/v2/clouds/%s/deployment/%s/config", server.URL, cloudID, cloudResourceID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("tfacc-iammap-update-role-a"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "rules.0.value", "tfacc-iammap-update-role-a"),
					checkMockIAMMappingFirstRuleValue(configURL, "tfacc-iammap-update-role-a"),
				),
			},
			{
				Config: config("tfacc-iammap-update-role-b"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "rules.#", "1"),
					resource.TestCheckResourceAttr(addr, "rules.0.value", "tfacc-iammap-update-role-b"),
					resource.TestCheckResourceAttr(addr, "fallback_rule", "CLOUD_DEFAULT"),
					checkMockIAMMappingFirstRuleValue(configURL, "tfacc-iammap-update-role-b"),
				),
			},
		},
	})
}

// checkMockIAMMappingFirstRuleValue GETs the mock's stored spec and asserts
// its single rule carries want - the mock only changes that spec on a PUT.
func checkMockIAMMappingFirstRuleValue(configURL, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		resp, err := http.Get(configURL) //nolint:gosec,noctx // test-local httptest URL
		if err != nil {
			return fmt.Errorf("reading mock config: %w", err)
		}
		defer resp.Body.Close()

		var body struct {
			Result struct {
				Spec struct {
					DataplaneIAMMapping struct {
						Rules []struct {
							Selector string `json:"selector"`
							Value    string `json:"value"`
						} `json:"rules"`
						FallbackRule string `json:"fallback_rule"`
					} `json:"dataplane_iam_mapping"`
				} `json:"spec"`
			} `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return fmt.Errorf("decoding mock config: %w", err)
		}

		mapping := body.Result.Spec.DataplaneIAMMapping
		if len(mapping.Rules) != 1 || mapping.Rules[0].Value != want {
			return fmt.Errorf("mock backend holds rules %+v, want exactly one rule with value %q - the write did not reach the backend", mapping.Rules, want)
		}
		if mapping.FallbackRule != "CLOUD_DEFAULT" {
			return fmt.Errorf("mock backend holds fallback_rule %q, want CLOUD_DEFAULT", mapping.FallbackRule)
		}
		return nil
	}
}
