package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// This file closes the same framework-level gap for Compute Config that
// resource_cloud_c3_lifecycle_acc_test.go closes for Cloud (see that file's
// header comment): plan-emptiness and inconsistent-result-after-apply are
// terraform FRAMEWORK properties, not just properties of a mapping function.
// resource_compute_config_test.go's unit tests (nodeConfigToAPI etc.) are
// correct in isolation but cannot catch a regression in how the framework's
// plan modifiers, masking, and key-casing restoration interact through a real
// Create -> plan(empty) -> Update(new version) -> Import cycle. This runs
// that cycle through resource.Test backed by an httptest mock server (the
// provider's api_url points at it via testAccProviderBlock, shared with the
// Cloud lifecycle tests in this package) - no real cloud, no TF_ACC real-infra
// gate beyond the standard SkipIfNotAcceptanceTest, so it runs in ordinary CI.
//
// The mock deliberately reproduces two REAL, previously-hit hazards instead of
// echoing the request back idealized (see TestAccComputeConfigResource_
// InconsistentResultRegressions and restoreMapKeyCasing's doc comment for the
// real-API behavior this mirrors):
//   - resource-map keys come back lowercased ("CPU" configured -> "cpu"
//     returned), which restoreMapKeyCasing must repair or every refresh diffs.
//   - a worker group's name, left unset in config, comes back assigned by the
//     API (defaulted to instance_type), which must resolve the Unknown plan
//     value cleanly instead of tripping "provider produced inconsistent
//     result after apply".
// An echo-mock that just replayed the config would not exercise either path.

// mockComputeConfigServer serves a stateful, versioned compute-config
// backend: each POST bumps that name's version and mints a new config_id,
// mirroring the real API's immutable-per-version lifecycle (Update always
// creates a new version rather than patching in place).
type mockComputeConfigServer struct {
	mu       sync.Mutex
	versions map[string]int64          // name -> latest version minted
	records  map[string]map[string]any // config_id -> stored "result" body
}

func newMockComputeConfigServer(t *testing.T) *httptest.Server {
	t.Helper()
	state := &mockComputeConfigServer{
		versions: make(map[string]int64),
		records:  make(map[string]map[string]any),
	}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/compute_templates/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/compute_templates/":
			state.handleCreate(t, w, r)
		case r.Method == http.MethodGet:
			state.handleGet(t, w, r)
		case r.Method == http.MethodPost:
			// /api/v2/compute_templates/{id}/archive
			state.handleArchive(w, r)
		default:
			t.Errorf("unexpected method %s on %s", r.Method, r.URL.Path)
		}
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func (s *mockComputeConfigServer) handleCreate(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
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

	s.mu.Lock()
	s.versions[req.Name]++
	version := s.versions[req.Name]
	s.mu.Unlock()

	configID := fmt.Sprintf("cpt_mock_%s_v%d", req.Name, version)

	// Apply the two realistic hazards to whatever the request sent, rather
	// than echoing it back idealized.
	respConfig := applyServerNormalization(req.Config)

	record := map[string]any{
		"id":               configID,
		"name":             req.Name,
		"version":          version,
		"created_at":       "2026-01-01T00:00:00Z",
		"last_modified_at": "2026-01-01T00:00:00Z",
		"archived_at":      nil,
		"config":           respConfig,
	}

	s.mu.Lock()
	s.records[configID] = record
	s.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": record})
}

func (s *mockComputeConfigServer) handleGet(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	configID := lastPathSegment(r.URL.Path)
	s.mu.Lock()
	record, ok := s.records[configID]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": record})
}

func (s *mockComputeConfigServer) handleArchive(w http.ResponseWriter, r *http.Request) {
	// path is /api/v2/compute_templates/{id}/archive
	path := r.URL.Path
	const suffix = "/archive"
	configID := lastPathSegment(path[:len(path)-len(suffix)])
	s.mu.Lock()
	if record, ok := s.records[configID]; ok {
		record["archived_at"] = "2026-01-01T01:00:00Z"
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func lastPathSegment(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// applyServerNormalization mimics two documented real-API behaviors so the
// mock has teeth (see file header): lowercasing well-known resource-map keys,
// and defaulting an unset worker name to its instance_type.
func applyServerNormalization(config map[string]any) map[string]any {
	out := make(map[string]any, len(config))
	for k, v := range config {
		out[k] = v
	}

	deploymentConfigs, _ := out["deployment_configs"].([]any)
	for _, dcRaw := range deploymentConfigs {
		dc, ok := dcRaw.(map[string]any)
		if !ok {
			continue
		}
		if headNode, ok := dc["head_node_type"].(map[string]any); ok {
			lowercaseResourceKeys(headNode)
		}
		if workers, ok := dc["worker_node_types"].([]any); ok {
			for _, wRaw := range workers {
				worker, ok := wRaw.(map[string]any)
				if !ok {
					continue
				}
				lowercaseResourceKeys(worker)
				if name, _ := worker["name"].(string); name == "" {
					if it, _ := worker["instance_type"].(string); it != "" {
						worker["name"] = it
					}
				}
			}
		}
	}
	return out
}

func lowercaseResourceKeys(node map[string]any) {
	resources, ok := node["resources"].(map[string]any)
	if !ok {
		return
	}
	lowered := make(map[string]any, len(resources))
	for k, v := range resources {
		lc := k
		switch k {
		case "CPU", "GPU", "Memory", "Object_Store_Memory":
			lc = toLowerASCII(k)
		}
		lowered[lc] = v
	}
	node["resources"] = lowered
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// TestAccComputeConfigLifecycle_MockServer proves the framework-level create
// -> apply -> plan(empty) -> update(new version) -> import lifecycle against
// a mock backend carrying the two hazards described in this file's header.
func TestAccComputeConfigLifecycle_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server := newMockComputeConfigServer(t)
	const name = "cc-lifecycle-mock"

	configV1 := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = "cld_mock_cc"

  head_node = {
    instance_type = "m5.large"
  }

  worker_nodes = [
    {
      # name intentionally omitted: the mock (like the real API) assigns one
      # from instance_type.
      instance_type = "m5.xlarge"
      min_nodes     = 0
      max_nodes     = 1
      market_type   = "ON_DEMAND"
      resources = {
        CPU = 2
      }
    }
  ]
}
`, name)

	configV2 := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = "cld_mock_cc"

  head_node = {
    instance_type = "m5.2xlarge"
  }

  worker_nodes = [
    {
      instance_type = "m5.xlarge"
      min_nodes     = 0
      max_nodes     = 1
      market_type   = "ON_DEMAND"
      resources = {
        CPU = 2
      }
    }
  ]
}
`, name)

	var firstConfigID string

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configV1,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "name", name),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "version", "1"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "head_node.instance_type", "m5.large"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.instance_type", "m5.xlarge"),
					// Server-assigned name (defaulted from instance_type) must
					// resolve the Unknown plan value cleanly.
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.name", "m5.xlarge"),
					// restoreMapKeyCasing must repair the server's lowercased
					// key back to the user's configured casing.
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.resources.CPU", "2"),
					testAccCaptureComputeConfigID("anyscale_compute_config.test", &firstConfigID),
				),
				// Headline gate: a config populated at create from a
				// realistically-normalized (not echoed) API response must not
				// diff on the very next plan.
				ExpectNonEmptyPlan: false,
			},
			{
				// Changing head_node forces a new version - name stays the
				// stable Terraform id, config_id changes underneath it.
				Config: configV2,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "name", name),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "version", "2"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "head_node.instance_type", "m5.2xlarge"),
					testAccCheckComputeConfigIDChanged("anyscale_compute_config.test", &firstConfigID),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				ResourceName:      "anyscale_compute_config.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: testAccComputeConfigImportStateIdFunc("anyscale_compute_config.test"),
				ImportStateVerifyIgnore: []string{
					// Import has no prior state to mask Computed sub-attributes
					// against, same as the real-API equivalent in
					// resource_compute_config_acc_test.go.
					"head_node", "worker_nodes",
					"enable_cross_zone_scaling", "min_resources", "max_resources",
					"advanced_instance_config", "flags", "zones",
				},
			},
		},
	})
}
