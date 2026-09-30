package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"

	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// This file proves compute-config cloud immutability against a mock backend
// (httptest server): no real infra, no ANYSCALE_TEST_REAL_INFRA gate, runs in
// ordinary CI.
//
// Immutability is an error guard in Update, NOT a RequiresReplace plan
// modifier: RequiresReplace on cloud_id (with UseStateForUnknown) cannot
// correctly detect a genuine cloud change at plan time without a network call.
// cloud_id has no plan modifiers; Update compares the plan's cloud_id against
// state's and errors only when they genuinely differ. This catches the orphan
// at apply time instead of plan time -- an intentional, documented tradeoff.
//
// TestAccComputeConfigResource_CloudImmutable_ErrorGuard_MockServer proves the actual
// protection: changing to a genuinely different cloud is refused with a
// clear error before any request that would create the orphan is ever sent.
func newTwoCloudComputeConfigMockServer(t *testing.T, cloudAID, cloudBID, configID, configName string) *httptest.Server {
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

	// Registered under both the subtree and bare-path forms (see
	// helpers_cloud_adoption_test.go for why a subtree-only mock is a portability
	// hazard: ServeMux 301-redirects a bare-path request, and whether that
	// redirect is followed is not consistent across Go versions/http.Client
	// configs).
	computeTemplatesCreateHandler := func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, computeTemplateJSON)
		default:
			t.Errorf("unexpected method %s on /api/v2/compute_templates/", r.Method)
		}
	}
	mux.HandleFunc("/api/v2/compute_templates/", computeTemplatesCreateHandler)
	mux.HandleFunc("/api/v2/compute_templates", computeTemplatesCreateHandler)

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

// TestAccComputeConfigResource_CloudImmutable_ErrorGuard_MockServer is the core
// immutability proof: attempting to move an existing compute config to a genuinely
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

	server := newTwoCloudComputeConfigMockServer(t, cloudAID, cloudBID, configID, configName)
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
				// The headline check: a real cloud change is refused, not
				// replaced and not silently applied. Match on the diagnostic
				// summary only -- Terraform word-wraps the detail text, so
				// asserting on a longer literal phrase from the detail is
				// fragile against wrap points that have nothing to do with
				// this test's actual claim.
				Config:      configOnCloudB,
				ExpectError: regexp.MustCompile(`Compute Config Cloud Is Immutable`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_compute_config.test", plancheck.ResourceActionUpdate),
					},
				},
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
