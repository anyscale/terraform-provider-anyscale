package acctest

// F3 regression: the 404 handling bug had two distinct, opposite-direction
// symptoms -
//   - bug A: DoRequestAndParse lists StatusNotFound as ACCEPTED, so a genuine
//     404 decodes to (zero-struct, nil-err) and the "not found -> remove"
//     branch never fires - a deleted compute config looks perfectly healthy
//     forever.
//   - bug B (the more alarming one, needs no real deletion): apiResult is
//     ALWAYS nil whenever DoRequestAndParse returns any error, so a genuinely
//     transient error (500, network blip) hits the exact same "not found"
//     branch a 404 would - silently wiping a HEALTHY resource from state.
// The fix is a typed ErrNotFound sentinel (api_helpers.go) so Read/ImportState
// can tell "genuinely not found" apart from "some other error" instead of
// using apiResult==nil as a proxy for both. This file proves both directions
// with a mock server that returns the real error-shaped 404 body
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
)

type f3NotFoundMockServer struct {
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

func newF3NotFoundMockServer(t *testing.T) (*httptest.Server, *f3NotFoundMockServer) {
	t.Helper()
	state := &f3NotFoundMockServer{}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/compute_templates/", func(w http.ResponseWriter, r *http.Request) {
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
	})

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

	server, state := newF3NotFoundMockServer(t)
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
				Config:             config,
				ExpectNonEmptyPlan: true, // Read must remove it -> plan shows a create
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

	server, state := newF3NotFoundMockServer(t)
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

	server, _ := newF3NotFoundMockServer(t)
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
