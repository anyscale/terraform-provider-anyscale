package acctest

// F2 regression (compute-config-import-parity quest): required_resources.memory
// is documented as accepting a unit-string like "4Gi", but the real backend
// only accepts a plain integer byte count and 422s on a unit-suffixed string
// (assayer live confirm). The fix parses the unit string client-side before
// sending, matching what the Python SDK already does. This proves it with a
// mock server that mirrors the real backend's strictness (rejects anything
// that isn't a bare integer for memory), so a regression back to sending the
// raw string fails the same way a live create would.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func newF2MemoryParseMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var lastRecord map[string]any

	mux.HandleFunc("/api/v2/compute_templates/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/compute_templates/":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read create body: %v", err)
				return
			}
			var req struct {
				Name   string         `json:"name"`
				Config map[string]any `json:"config"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("unmarshal create body: %v", err)
				return
			}

			headNode, _ := req.Config["head_node_type"].(map[string]any)
			reqRes, _ := headNode["required_resources"].(map[string]any)
			memVal, present := reqRes["memory"]

			// Mirrors the real backend's strictness (assayer G1g/live: "value
			// is not a valid integer" on a unit-suffixed string like "4Gi").
			// A JSON number decodes to float64 here; a JSON string does not.
			if present {
				if _, isNumber := memVal.(float64); !isNumber {
					w.WriteHeader(http.StatusUnprocessableEntity)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{
							"detail": []map[string]any{{
								"loc": []string{"body", "config", "head_node_type", "required_resources", "memory"},
								"msg": "value is not a valid integer (mock mirrors the real backend's strictness)",
							}},
						},
					})
					return
				}
			}

			record := map[string]any{
				"id":               "cpt_f2_mock",
				"name":             req.Name,
				"version":          int64(1),
				"created_at":       "2026-01-01T00:00:00Z",
				"last_modified_at": "2026-01-01T00:00:00Z",
				"archived_at":      nil,
				"config":           req.Config,
			}
			lastRecord = record
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": record})
		case r.Method == http.MethodGet:
			if lastRecord == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": lastRecord})
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on %s", r.Method, r.URL.Path)
		}
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestAccComputeConfigResource_MemoryUnitStringParsesToBytes_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server := newF2MemoryParseMockServer(t)
	name := "cc-f2-memory-parse"

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = "cld_mock_cc"

  head_node = {
    instance_type = "m5.large"
    required_resources = {
      memory = "4Gi"
    }
  }
}
`, name)

	const expectedBytes = "4294967296" // 4 * 1024^3, the documented "4Gi" contract

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					// The schema attribute stays a string round-trippable
					// against the user's own "4Gi" - the parsed byte count is
					// what actually goes over the wire (asserted by the mock
					// server's own strictness above), not necessarily what
					// state displays. This asserts state shows the value the
					// mock echoed back (parsed bytes as an integer string),
					// proving the wire request really was numeric bytes.
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "head_node.required_resources.memory", expectedBytes),
				),
			},
		},
	})
}
