package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// newCloudIAMMappingDataSourceMockServer serves the two endpoints
// data.anyscale_cloud_iam_mapping reads:
//
//   - GET /api/v2/clouds/{cloudID}/resources - used only when
//     cloud_resource_id is omitted, to resolve the primary (is_default)
//     deployment. Two deployments are listed, only one of them default, so
//     resolution has to pick rather than take the only element.
//   - GET /api/v2/clouds/{cloudID}/deployment/{primaryID}/config - the
//     {"result": {"spec": {...}}} envelope, with dataplane_iam_mapping set to
//     mapping. The real API returns {} for a deployment that has never had a
//     mapping, and mode CUSTOMER_MANAGED once any rule exists (the same
//     shapes resource_cloud_iam_mapping_plan_stability_acc_test.go and the
//     real-infra import test assert against).
func newCloudIAMMappingDataSourceMockServer(t *testing.T, cloudID, primaryID, secondaryID string, mapping map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	writeJSON := func(w http.ResponseWriter, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("failed to encode mock response: %v", err)
		}
	}

	mux.HandleFunc(fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on cloud resources list", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, map[string]any{
			"results": []map[string]any{
				{"cloud_resource_id": secondaryID, "name": "secondary", "provider": "AWS", "compute_stack": "VM", "is_default": false},
				{"cloud_resource_id": primaryID, "name": "primary", "provider": "AWS", "compute_stack": "VM", "is_default": true},
			},
			"metadata": map[string]any{"total": 2, "next_paging_token": nil},
		})
	})

	mux.HandleFunc(fmt.Sprintf("/api/v2/clouds/%s/deployment/%s/config", cloudID, primaryID), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on deployment config (data source must only read)", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, map[string]any{"result": map[string]any{"spec": map[string]any{
			"cloud_deployment_id":        primaryID,
			"cloud_provider":             "AWS",
			"compute_stack":              "VM",
			"user_tag_annotation_prefix": "acme-",
			"dataplane_iam_mapping":      mapping,
		}}})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccCloudIAMMappingDataSource_Basic_MockServer proves Read maps a
// populated mapping into every attribute: id is <cloud_id>/<cloud_resource_id>,
// the explicitly configured cloud_resource_id is kept, rules keep the API's
// order (first-match-wins, so order is the contract), and fallback_rule/mode
// are copied through. It is also the positive control for the null
// assertions in the Empty test below: the same paths are asserted non-null
// here.
func TestAccCloudIAMMappingDataSource_Basic_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_iammapds_basic"
	const primaryID = "cldrsrc_iammapds_basic_primary"
	const secondaryID = "cldrsrc_iammapds_basic_secondary"

	server := newCloudIAMMappingDataSourceMockServer(t, cloudID, primaryID, secondaryID, map[string]any{
		"mode": "CUSTOMER_MANAGED",
		"rules": []map[string]any{
			{"selector": "workload-type=job", "value": "arn:aws:iam::123456789012:role/tfacc-jobs"},
			{"selector": "project=tfacc-iammapds", "value": "arn:aws:iam::123456789012:role/tfacc-project"},
		},
		"fallback_rule": "FAIL",
	})

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
data "anyscale_cloud_iam_mapping" "test" {
  cloud_id          = %[1]q
  cloud_resource_id = %[2]q
}
`, cloudID, primaryID)

	const addr = "data.anyscale_cloud_iam_mapping.test"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("id"), knownvalue.StringExact(cloudID+"/"+primaryID)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("cloud_id"), knownvalue.StringExact(cloudID)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("cloud_resource_id"), knownvalue.StringExact(primaryID)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("rules"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.ObjectExact(map[string]knownvalue.Check{
							"selector": knownvalue.StringExact("workload-type=job"),
							"value":    knownvalue.StringExact("arn:aws:iam::123456789012:role/tfacc-jobs"),
						}),
						knownvalue.ObjectExact(map[string]knownvalue.Check{
							"selector": knownvalue.StringExact("project=tfacc-iammapds"),
							"value":    knownvalue.StringExact("arn:aws:iam::123456789012:role/tfacc-project"),
						}),
					})),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("fallback_rule"), knownvalue.StringExact("FAIL")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("mode"), knownvalue.StringExact("CUSTOMER_MANAGED")),
				},
			},
		},
	})
}

// TestAccCloudIAMMappingDataSource_EmptyMappingPrimaryResolved_MockServer
// covers the other two Read branches: cloud_resource_id omitted resolves to
// the cloud's is_default deployment (not merely the first one listed) and is
// written back, and the {} wire shape of a never-configured mapping yields
// null rules/fallback_rule/mode - never an empty list or "".
func TestAccCloudIAMMappingDataSource_EmptyMappingPrimaryResolved_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_iammapds_empty"
	const primaryID = "cldrsrc_iammapds_empty_primary"
	const secondaryID = "cldrsrc_iammapds_empty_secondary"

	server := newCloudIAMMappingDataSourceMockServer(t, cloudID, primaryID, secondaryID, map[string]any{})

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
data "anyscale_cloud_iam_mapping" "test" {
  cloud_id = %[1]q
}
`, cloudID)

	const addr = "data.anyscale_cloud_iam_mapping.test"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("id"), knownvalue.StringExact(cloudID+"/"+primaryID)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("cloud_resource_id"), knownvalue.StringExact(primaryID)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("rules"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("fallback_rule"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("mode"), knownvalue.Null()),
				},
			},
		},
	})
}
