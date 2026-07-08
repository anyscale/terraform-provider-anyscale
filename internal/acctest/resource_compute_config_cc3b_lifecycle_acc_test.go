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

// This file proves CC3b against a mock backend, the same httptest-server
// pattern resource_cloud_c3_lifecycle_acc_test.go established: no real
// infra, no ANYSCALE_TEST_REAL_INFRA gate, runs in ordinary CI.
//
// CC3b's final shape is an error guard in Update, NOT a RequiresReplace plan
// modifier: a first pass tried RequiresReplace on cloud_id (with
// UseStateForUnknown) and cloud_name, but that cannot correctly handle
// "switching representation" -- expressing the SAME cloud via cloud_id
// instead of cloud_name, or vice versa -- without resolving cloud_name at
// plan time, which would mean a network call from a plan modifier. The
// architect ruling: leave cloud_id/cloud_name exactly as they were (no plan
// modifiers at all), and instead have Update compare the plan's resolved
// cloud_id (buildComputeConfigRequest already resolves cloud_name to
// cloud_id before this check runs) against state's cloud_id, erroring only
// when they genuinely differ. This closes the orphan for both the cloud_id
// and cloud_name paths with zero plan-time network calls, at the cost of the
// change only being caught at apply time instead of plan time -- an
// intentional, documented asymmetry with CC3a's name handling.
//
// TestAccComputeConfigLifecycle_CloudNameOnly_MockServer proves the
// non-regression and switching-representation halves: a cloud_name-only
// config plans empty forever (no modifier to misfire in the first place now),
// and switching from cloud_name to an explicit, matching cloud_id is a plain
// update, not a replace, settling to an empty plan.
//
// TestAccComputeConfigCloudImmutable_ErrorGuard_MockServer proves the actual
// protection: changing to a genuinely different cloud is refused with a
// clear error before any request that would create the orphan is ever sent.
//
// CC2 (incidentally, but load-bearing in the first test): that config also
// deliberately omits maximum_uptime_minutes, which is exactly the shape that
// exposed the Provider produced inconsistent result after apply bug architect
// and assayer both caught in review (populateComputedFieldsFromResponse not
// having existed yet). A regression here would fail this test too.
func newCC3bMockComputeConfigServer(t *testing.T, cloudID, cloudName, configID, configName string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results": [{"id": %[1]q, "name": %[2]q, "created_at": "2026-01-01T00:00:00Z"}], "metadata": {"total": 1, "next_paging_token": null}}`, cloudID, cloudName)
	})

	computeTemplateJSON := func() string {
		return fmt.Sprintf(`{
			"id": %[1]q, "name": %[2]q, "version": 1,
			"created_at": "2026-01-01T00:00:00Z", "last_modified_at": "2026-01-01T00:00:00Z",
			"archived_at": "",
			"config": {
				"cloud_id": %[3]q,
				"idle_termination_minutes": 120,
				"head_node_type": {"name": "head", "instance_type": "m5.2xlarge"}
			}
		}`, configID, configName, cloudID)
	}

	mux.HandleFunc("/api/v2/compute_templates/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, computeTemplateJSON())
		default:
			t.Errorf("unexpected method %s on /api/v2/compute_templates/", r.Method)
		}
	})

	mux.HandleFunc("/api/v2/compute_templates/"+configID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": %s}`, computeTemplateJSON())
	})

	// Delete() archives rather than deletes (compute configs are versioned,
	// never hard-deleted) -- without this, ServeMux's more specific exact
	// match above still wins for plain GET/POST on the bare id, but the
	// archive sub-path needs its own handler or it would fall through to the
	// generic "/api/v2/compute_templates/" prefix handler above, which only
	// understands create.
	mux.HandleFunc("/api/v2/compute_templates/"+configID+"/archive", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {"archived_at": "2026-01-01T00:00:00Z"}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccComputeConfigLifecycle_CloudNameOnly_MockServer is the CC3b proof:
// a cloud_name-only config (cloud_id always Computed) must plan empty on the
// very next plan, not just on the apply that created it.
func TestAccComputeConfigLifecycle_CloudNameOnly_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_cc3b_mock"
	const cloudName = "cc3b-mock-cloud"
	const configID = "cpt_cc3b_mock"
	const configName = "cc3b-mock-config"

	server := newCC3bMockComputeConfigServer(t, cloudID, cloudName, configID, configName)
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name       = %[1]q
  cloud_name = %[2]q

  head_node = {
    instance_type = "m5.2xlarge"
  }
}
`, configName, cloudName)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "cloud_name", cloudName),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "cloud_id", cloudID),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "idle_termination_minutes", "120"),
					resource.TestCheckNoResourceAttr("anyscale_compute_config.test", "maximum_uptime_minutes"),
				),
				// Headline CC3b gate: cloud_id resolved purely from cloud_name
				// must not force a replace (or any diff) on the very next
				// plan, even though cloud_id itself is never set in config.
				ExpectNonEmptyPlan: false,
			},
			{
				// Explicit, independent second plan against the unchanged
				// config: belt-and-suspenders on top of the automatic
				// post-apply check above, since this is exactly the
				// "every single plan" failure mode under test, not just the
				// first one.
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				// CC3b "switching representation" gate: dropping cloud_name in
				// favor of an explicit cloud_id that matches what state already
				// resolved must be a plain update, not a replace, even though
				// cloud_name's own value is disappearing from config entirely.
				// ExpectResourceAction (not just ExpectNonEmptyPlan) is the
				// point here: a replace and an update can both show a non-empty
				// plan, only the action type distinguishes them.
				Config: testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q

  head_node = {
    instance_type = "m5.2xlarge"
  }
}
`, configName, cloudID),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_compute_config.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "cloud_id", cloudID),
					resource.TestCheckNoResourceAttr("anyscale_compute_config.test", "cloud_name"),
				),
			},
		},
	})
}

// newCC3bTwoCloudMockServer serves TWO distinct clouds by name, plus a single
// compute config that starts on cloudAID, for the immutable-cloud proof.
func newCC3bTwoCloudMockServer(t *testing.T, cloudAID, cloudAName, cloudBID, cloudBName, configID, configName string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results": [
			{"id": %[1]q, "name": %[2]q, "created_at": "2026-01-01T00:00:00Z"},
			{"id": %[3]q, "name": %[4]q, "created_at": "2026-01-01T00:00:00Z"}
		], "metadata": {"total": 2, "next_paging_token": null}}`, cloudAID, cloudAName, cloudBID, cloudBName)
	})

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

// TestAccComputeConfigCloudImmutable_ErrorGuard_MockServer is CC3b's core
// proof: attempting to move an existing compute config to a genuinely
// different cloud (here, via cloud_name -- the harder path, since it needs
// resolution; a direct cloud_id change hits the identical comparison in
// Update) must be refused with a clear, named error, never silently orphan
// the config on the old cloud, and never even reach the API call that would
// create the orphan (the mock only implements POST/archive for the ORIGINAL
// config; if Update ever sent a cross-cloud create it would be evidence of
// exactly the bug this guard exists to prevent, though the test asserts on
// the error rather than depending on that as its primary signal).
func TestAccComputeConfigCloudImmutable_ErrorGuard_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudAID = "cld_cc3b_a"
	const cloudAName = "cc3b-cloud-a"
	const cloudBID = "cld_cc3b_b"
	const cloudBName = "cc3b-cloud-b"
	const configID = "cpt_cc3b_immutable"
	const configName = "cc3b-immutable-config"

	server := newCC3bTwoCloudMockServer(t, cloudAID, cloudAName, cloudBID, cloudBName, configID, configName)
	providerBlock := testAccProviderBlock(server.URL)

	configOnCloudA := providerBlock + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name       = %[1]q
  cloud_name = %[2]q

  head_node = {
    instance_type = "m5.2xlarge"
  }
}
`, configName, cloudAName)

	configOnCloudB := providerBlock + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name       = %[1]q
  cloud_name = %[2]q

  head_node = {
    instance_type = "m5.2xlarge"
  }
}
`, configName, cloudBName)

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
