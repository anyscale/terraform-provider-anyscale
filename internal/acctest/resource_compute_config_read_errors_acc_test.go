package acctest

// Read/ImportState must distinguish "genuinely not found" from "some other
// error", in both directions:
//   - A genuine 404 must remove the resource from state. DoRequestAndParse
//     lists StatusNotFound as ACCEPTED, so a 404 would otherwise decode to
//     (zero-struct, nil-err) and a deleted compute config would look healthy
//     forever.
//   - A transient error (500, network blip) must surface as an error and
//     leave a HEALTHY resource in state. Treating "no result" as a proxy for
//     "not found" would silently wipe it.
// Both rely on the typed ErrNotFound sentinel (api_helpers.go). The mock
// returns the real error-shaped 404 body
// ({"error":{"detail":"Could not find entity with id ..."}}) for the
// genuine-404 case, and a real 500 for the transient case - not an idealized
// empty response either mock could get away with echoing.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

type readErrorsMockServer struct {
	mu     sync.Mutex
	record map[string]any
	// nextGetStatus, when non-zero, overrides EVERY subsequent GET's response
	// with this status instead of the normal 200 - simulates the config
	// disappearing (404) or a transient backend failure (500) between the
	// initial apply and a later refresh. Deliberately sticky (not reset after
	// one read): a single non-PlanOnly TestStep triggers more than one GET (a
	// refresh during plan, then another during apply), and a resource that is
	// genuinely gone stays gone across all of them - a one-shot override that
	// silently reverts to 200 on the second read doesn't match real backend
	// behavior.
	nextGetStatus int
}

func newReadErrorsMockServer(t *testing.T) (*httptest.Server, *readErrorsMockServer) {
	t.Helper()
	state := &readErrorsMockServer{}
	mux := http.NewServeMux()

	// Registered under both the subtree and bare-path forms (see
	// helpers_cloud_adoption_test.go: a subtree-only mock makes ServeMux
	// 301-redirect a bare-path request, and whether that redirect is followed
	// is not portable across Go versions/http.Client configs).
	computeTemplatesHandler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/compute_templates/":
			state.mu.Lock()
			state.record = map[string]any{
				"id": "cpt_f3_mock", "name": "cc-f3-notfound", "version": int64(1),
				"created_at": "2026-01-01T00:00:00Z", "last_modified_at": "2026-01-01T00:00:00Z",
				"archived_at": nil,
				"config": map[string]any{
					"cloud_id": "cld_mock_cc",
					"head_node_type": map[string]any{
						"name": "head", "instance_type": "m5.large",
					},
				},
			}
			state.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": state.record})
		case r.Method == http.MethodGet:
			state.mu.Lock()
			override := state.nextGetStatus
			record := state.record
			state.mu.Unlock()

			if override == http.StatusNotFound {
				w.WriteHeader(http.StatusNotFound)
				// The real error-shaped body the backend actually returns -
				// well-formed JSON with an "error" key, not an empty/broken
				// body, which is exactly what let bug A/B happen in the
				// first place (json.Unmarshal succeeds on it).
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{"detail": fmt.Sprintf("Could not find entity with id %q", lastPathSegment(r.URL.Path))},
				})
				return
			}
			if override == http.StatusInternalServerError {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{"detail": "Internal Server Error (mock-simulated transient failure)"},
				})
				return
			}
			if record == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"detail": "not found"}})
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": record})
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on %s", r.Method, r.URL.Path)
		}
	}
	mux.HandleFunc("/api/v2/compute_templates/", computeTemplatesHandler)
	mux.HandleFunc("/api/v2/compute_templates", computeTemplatesHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, state
}

// TestAccComputeConfigResource_ReadRemovesOnGenuine404_MockServer is bug A's
// regression proof: a compute config that genuinely no longer exists
// (real 404) must be detected and removed from state on the next refresh -
// not silently reported as still healthy.
func TestAccComputeConfigResource_ReadRemovesOnGenuine404_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, state := newReadErrorsMockServer(t)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_compute_config" "test" {
  name     = "cc-f3-notfound"
  cloud_id = "cld_mock_cc"
  head_node = {
    instance_type = "m5.large"
  }
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				PreConfig: func() {
					state.mu.Lock()
					state.nextGetStatus = http.StatusNotFound
					state.mu.Unlock()
				},
				Config: config,
				// Read must remove it, so the refreshed plan is a create.
				// ExpectNonEmptyPlan alone is also satisfied by a Read that
				// keeps a corrupted state (planning an update or replace), so
				// the action is asserted on the post-refresh plan itself. The
				// mock keeps returning 404 after the re-create, so the
				// post-apply plan is non-empty too.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_compute_config.test", plancheck.ResourceActionCreate),
					},
				},
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccComputeConfigResource_ReadSurfacesTransientErrorWithoutRemoving_MockServer
// is bug B's regression proof - the more alarming symptom: a real 500 must
// surface as an error, NOT be treated as "not found" and silently wipe a
// healthy resource from state. Before the fix, apiResult==nil for ANY error
// (not just 404), so this exact scenario would have removed the resource
// with no error at all.
func TestAccComputeConfigResource_ReadSurfacesTransientErrorWithoutRemoving_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, state := newReadErrorsMockServer(t)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_compute_config" "test" {
  name     = "cc-f3-notfound"
  cloud_id = "cld_mock_cc"
  head_node = {
    instance_type = "m5.large"
  }
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				PreConfig: func() {
					state.mu.Lock()
					state.nextGetStatus = http.StatusInternalServerError
					state.mu.Unlock()
				},
				// PlanOnly still triggers a Read/refresh internally. Before
				// the fix, this would have silently succeeded with an empty
				// plan (the resource wrongly removed, no error) - the exact
				// bug-B symptom. The fix must surface a real error instead.
				Config:      config,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?i)(API Request Failed|Internal Server Error|500)`),
			},
		},
	})
}

// TestAccComputeConfigResource_ImportBogusIDProducesClearDiagnostic_MockServer
// proves ImportState's half of the fix: importing a cpt_ id that does not
// exist must produce a clear "not found" diagnostic, not silently proceed
// and create an empty phantom resource with no error at all.
func TestAccComputeConfigResource_ImportBogusIDProducesClearDiagnostic_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, _ := newReadErrorsMockServer(t)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_compute_config" "test" {
  name     = "cc-f3-notfound-import"
  cloud_id = "cld_mock_cc"
  head_node = {
    instance_type = "m5.large"
  }
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:  "anyscale_compute_config.test",
				ImportState:   true,
				ImportStateId: "cpt_does_not_exist_mock",
				Config:        config,
				ExpectError:   regexp.MustCompile(`(?i)(not found|no compute config)`),
			},
		},
	})
}
