package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"

	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// This file proves CC3b against a mock backend, the same httptest-server
// pattern resource_cloud_c3_lifecycle_acc_test.go established: no real
// infra, no ANYSCALE_TEST_REAL_INFRA gate, runs in ordinary CI.
//
// CC3b's final shape is an error guard in Update, NOT a RequiresReplace plan
// modifier: a first pass tried RequiresReplace on cloud_id (with
// UseStateForUnknown), but that cannot correctly detect a genuine cloud
// change at plan time without a network call. The architect ruling: leave
// cloud_id with no plan modifiers, and instead have Update compare the
// plan's cloud_id against state's cloud_id, erroring only when they
// genuinely differ. This catches the orphan at apply time instead of plan
// time -- an intentional, documented tradeoff.
//
// R1 (2026-07-27): cloud_name was removed from this resource entirely
// (cloud_id is now Required, cloud_name-based name resolution moved to the
// anyscale_cloud data source). This file previously also proved a
// cloud_name-based "switching selectors" guarantee
// (Lifecycle_CloudNameOnly_MockServer) -- deleted along with cloud_name
// itself, since there is no longer a second selector to switch away from.
// The immutability guarantee below is unrelated to that and still real.
//
// TestAccComputeConfigResource_CloudImmutable_ErrorGuard_MockServer proves the actual
// protection: changing to a genuinely different cloud is refused with a
// clear error before any request that would create the orphan is ever sent.
func newCC3bTwoCloudMockServer(t *testing.T, cloudAID, cloudBID, configID, configName string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// The mock always reports the config as still living on cloud A: the
	// point of this test is that Update must refuse to send the request that
	// would move it to cloud B in the first place, so the server never needs
	// to honor a cross-cloud update.
	computeTemplateJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": %[2]q, "version": 1,
		"created_at": "2026-01-01T00:00:00Z", "last_modified_at": "2026-01-01T00:00:00Z",
		"archived_at": "",
		"config": {
			"cloud_id": %[3]q,
			"idle_termination_minutes": 120,
			"head_node_type": {"name": "head", "instance_type": "m5.2xlarge"}
		}
	}`, configID, configName, cloudAID)

	mux.HandleFunc("/api/v2/compute_templates/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, computeTemplateJSON)
		default:
			t.Errorf("unexpected method %s on /api/v2/compute_templates/", r.Method)
		}
	})

	mux.HandleFunc("/api/v2/compute_templates/"+configID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": %s}`, computeTemplateJSON)
	})

	mux.HandleFunc("/api/v2/compute_templates/"+configID+"/archive", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {"archived_at": "2026-01-01T00:00:00Z"}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccComputeConfigResource_CloudImmutable_ErrorGuard_MockServer is CC3b's core
// proof: attempting to move an existing compute config to a genuinely
// different cloud must be refused with a clear, named error, never silently
// orphan the config on the old cloud, and never even reach the API call
// that would create the orphan (the mock only implements POST/archive for
// the ORIGINAL config; if Update ever sent a cross-cloud create it would be
// evidence of exactly the bug this guard exists to prevent, though the test
// asserts on the error rather than depending on that as its primary signal).
func TestAccComputeConfigResource_CloudImmutable_ErrorGuard_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudAID = "cld_cc3b_a"
	const cloudBID = "cld_cc3b_b"
	const configID = "cpt_cc3b_immutable"
	const configName = "cc3b-immutable-config"

	server := newCC3bTwoCloudMockServer(t, cloudAID, cloudBID, configID, configName)
	providerBlock := testAccProviderBlock(server.URL)

	configOnCloudA := providerBlock + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q

  head_node = {
    instance_type = "m5.2xlarge"
  }
}
`, configName, cloudAID)

	configOnCloudB := providerBlock + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q

  head_node = {
    instance_type = "m5.2xlarge"
  }
}
`, configName, cloudBID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configOnCloudA,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "cloud_id", cloudAID),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				// The headline CC3b gate: a real cloud change is refused, not
				// replaced and not silently applied. Match on the diagnostic
				// summary only -- Terraform word-wraps the detail text, so
				// asserting on a longer literal phrase from the detail is
				// fragile against wrap points that have nothing to do with
				// this test's actual claim.
				Config:      configOnCloudB,
				ExpectError: regexp.MustCompile(`Compute Config Cloud Is Immutable`),
			},
			{
				// Confirms the refused apply left the resource exactly as it
				// was: back on the original config, plan is clean, nothing
				// was silently orphaned or half-applied by the attempt above.
				Config:             configOnCloudA,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}
