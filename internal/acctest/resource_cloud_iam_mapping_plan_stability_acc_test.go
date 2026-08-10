package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// writeJSONResult and readJSONBody are small local helpers for this file's
// mock server - it needs to decode and re-encode arbitrary spec shapes
// (rather than a fixed struct) so a PUT's body can be stored and echoed back
// verbatim by the next GET.
func writeJSONResult(t *testing.T, w http.ResponseWriter, result map[string]any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(map[string]any{"result": result}); err != nil {
		t.Fatalf("failed to encode mock response: %v", err)
	}
}

func readJSONBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode request body: %v", err)
	}
	return body
}

// This file proves plan-stability for anyscale_cloud_iam_mapping against a
// controlled mock server - a framework-level property that a function-level
// unit test on dataplaneIAMMappingRulesToState/FromState cannot establish,
// since Core's own plan diffing is what actually compares config against
// refreshed state.
//
// newCloudIAMMappingMockServer serves GET/PUT
// /api/v2/clouds/{cloudID}/deployment/{cloudResourceID}/config from a single
// mutable spec, so a PUT during apply is reflected by the next GET during
// plan/refresh - a real (if minimal) config store, not a fixed echo.
func newCloudIAMMappingMockServer(t *testing.T, cloudID, cloudResourceID string) *httptest.Server {
	t.Helper()

	var mu sync.Mutex
	// cloud_provider/compute_stack/user_tag_annotation_prefix are the
	// required-but-mostly-irrelevant transport fields every write must
	// resend; dataplane_iam_mapping starts with no rules, matching a cloud
	// that has never had a mapping configured.
	spec := map[string]any{
		"cloud_provider":             "AWS",
		"compute_stack":              "VM",
		"user_tag_annotation_prefix": "acme-",
		"dataplane_iam_mapping":      map[string]any{},
	}

	mux := http.NewServeMux()
	path := fmt.Sprintf("/api/v2/clouds/%s/deployment/%s/config", cloudID, cloudResourceID)
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			writeJSONResult(t, w, map[string]any{"spec": spec})
		case http.MethodPut:
			// putCloudDeploymentConfig marshals CloudDeploymentConfigResult{Spec}
			// directly as the request body - {"spec": {...}}, with no "result"
			// wrapper. Only GET/PUT RESPONSES are wrapped in "result".
			body := readJSONBody(t, r)
			newSpec, ok := body["spec"].(map[string]any)
			if !ok {
				t.Fatalf("PUT body missing spec: %v", body)
			}
			spec = newSpec
			w.WriteHeader(http.StatusOK)
			writeJSONResult(t, w, map[string]any{"spec": spec})
		default:
			t.Fatalf("unexpected method %s on %s", r.Method, path)
		}
	})

	return httptest.NewServer(mux)
}

// TestAccCloudIAMMappingResource_ExplicitEmptyRulesRejectedAtPlanTime proves
// the framework-level half of a plan-stability guarantee a function-level
// unit test cannot: ValidateConfig rejects an explicit `rules = []` before
// it ever applies, so this configuration never even reaches a plan/apply
// cycle. Read canonicalizes an empty mapping to a NULL list
// (dataplaneIAMMappingRulesToState's deliberate choice - see
// TestDataplaneIAMMappingRulesToState_EmptyMapsToNull), and since rules is
// non-Computed, a null-vs-config-[] mismatch would otherwise be a diff on
// every single plan forever; rejecting the `[]` spelling up front is what
// prevents that from ever being reachable through the real Terraform CLI
// plan/apply path, not just through this provider's own Go code.
func TestAccCloudIAMMappingResource_ExplicitEmptyRulesRejectedAtPlanTime(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_iammap_empty"
	const cloudResourceID = "cldrsrc_iammap_empty"

	server := newCloudIAMMappingMockServer(t, cloudID, cloudResourceID)
	defer server.Close()

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud_iam_mapping" "test" {
  cloud_id          = %[1]q
  cloud_resource_id = %[2]q
  rules             = []
}
`, cloudID, cloudResourceID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`rules must not be an explicit empty list`),
			},
		},
	})
}

// TestAccCloudIAMMappingResource_OmittedRulesConverges is the control this
// test suite needs alongside the rejection test above: omitting `rules`
// entirely (as opposed to declaring it `[]`) is the one spelling of "no
// mapping" this provider keeps working, and it must actually reach a
// stable, empty plan - proving the rejection above is specific to the `[]`
// spelling and not to "no mapping" configurations in general.
func TestAccCloudIAMMappingResource_OmittedRulesConverges(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_iammap_omitted"
	const cloudResourceID = "cldrsrc_iammap_omitted"

	server := newCloudIAMMappingMockServer(t, cloudID, cloudResourceID)
	defer server.Close()

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud_iam_mapping" "test" {
  cloud_id          = %[1]q
  cloud_resource_id = %[2]q
}
`, cloudID, cloudResourceID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckNoResourceAttr("anyscale_cloud_iam_mapping.test", "rules"),
			},
			{
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}
