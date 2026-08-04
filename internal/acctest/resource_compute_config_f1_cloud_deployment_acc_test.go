package acctest

// F1 regression: node-level cloud_deployment used to be written to the
// WRONG wire location - a top-level sibling key on the node's config map -
// which the real backend rejects outright with a 422 "extra fields not
// permitted". The fix nests it inside that node's flags dict instead, matching
// the SDK/backend/our-own-flatten's actual expectation. This proves the fix
// with a mock server that MIRRORS the real backend's strictness: it 422s if
// cloud_deployment ever arrives at the wrong (top-level) location, so this
// test fails the same way a live create would if the bug ever came back -
// not just "does read work," but "did we send it to the right place at all."

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func newF1CloudDeploymentMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var lastRecord map[string]any

	// Registered under both the subtree and bare-path forms (see
	// helpers_cloud_adoption_test.go: a subtree-only mock makes ServeMux
	// 301-redirect a bare-path request, and whether that redirect is followed
	// is not portable across Go versions/http.Client configs).
	computeTemplatesHandler := func(w http.ResponseWriter, r *http.Request) {
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
			if headNode == nil {
				t.Errorf("create request missing head_node_type: %s", body)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			// The real backend's strictness this mock mirrors: cloud_deployment
			// as a TOP-LEVEL key on the node is REJECTED with a 422 "extra
			// fields not permitted". A regression here must fail exactly the
			// way a real create would.
			if _, wrongLocation := headNode["cloud_deployment"]; wrongLocation {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"detail": []map[string]any{{
							"loc": []string{"body", "config", "head_node_type", "cloud_deployment"},
							"msg": "extra fields not permitted (mock mirrors the real backend's strictness)",
						}},
					},
				})
				return
			}

			flags, _ := headNode["flags"].(map[string]any)
			cloudDep, correctLocation := flags["cloud_deployment"]
			if !correctLocation {
				t.Errorf("expected cloud_deployment inside head_node_type.flags, found neither location: %s", body)
			}

			record := map[string]any{
				"id":               "cpt_f1_mock",
				"name":             req.Name,
				"version":          int64(1),
				"created_at":       "2026-01-01T00:00:00Z",
				"last_modified_at": "2026-01-01T00:00:00Z",
				"archived_at":      nil,
				"config":           req.Config,
			}
			_ = cloudDep
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
	}
	mux.HandleFunc("/api/v2/compute_templates/", computeTemplatesHandler)
	mux.HandleFunc("/api/v2/compute_templates", computeTemplatesHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestAccComputeConfigResource_CloudDeploymentNestsInFlags_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server := newF1CloudDeploymentMockServer(t)
	name := "cc-f1-cloud-deployment"

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = "cld_mock_cc"

  head_node = {
    instance_type = "m5.large"
    cloud_deployment = {
      provider = "aws"
      region   = "us-east-1"
    }
  }
}
`, name)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "head_node.cloud_deployment.provider", "aws"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "head_node.cloud_deployment.region", "us-east-1"),
				),
			},
			{
				// No-op bar: the value that was just set must survive a
				// refresh unchanged, not drift back to null.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}
