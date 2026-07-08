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
					// resource_compute_config_acc_test.go. enable_cross_zone_scaling,
					// advanced_instance_config, and flags used to be listed here too
					// (pre-CC11/CC12/CC14); this config sets none of the three, so
					// they now correctly resolve/stay at their pre-import values with
					// nothing to ignore - see TestAccComputeConfigImportRecoversWriteOnlyFields
					// for the actual CC12 recovery-with-real-values proof.
					"head_node", "worker_nodes",
					"min_resources", "max_resources", "zones",
				},
			},
		},
	})
}

// newCC12MockComputeConfigServer serves a single, fixed compute config that
// already has real top-level flags and advanced_instance_config set - the
// realistic "pre-existing config imported into a config that omits them"
// scenario CC12 exists for. The mock never validates these values (unlike
// the real Anyscale API, which rejects arbitrary flag keys and validates
// advanced_instance_config against a provider-specific instance-launch
// shape - see the design discussion), which is exactly why this has to be a
// mock-server test rather than a real-API one: a synthetic marker payload
// that proves the mechanism would 400 against the real backend.
func newCC12MockComputeConfigServer(t *testing.T, configID, configName, cloudID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	computeTemplateJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": %[2]q, "version": 1,
		"created_at": "2026-01-01T00:00:00Z", "last_modified_at": "2026-01-01T00:00:00Z",
		"archived_at": "",
		"config": {
			"cloud_id": %[3]q,
			"head_node_type": {"name": "head", "instance_type": "m5.2xlarge"},
			"flags": {"cc12-marker-flag": true, "cc12-marker-count": 3},
			"advanced_configurations_json": {"disk_size": 100, "enable_monitoring": true}
		}
	}`, configID, configName, cloudID)

	mux.HandleFunc("/api/v2/compute_templates/"+configID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": `+computeTemplateJSON+`}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccComputeConfigImportRecoversWriteOnlyFields is CC12's verify-gate,
// assigned jointly to assayer and forge when CC12 was designed but not
// actually written until caught reviewing the release PR text that claimed
// it existed. Read intentionally never reads flags/advanced_instance_config
// back from the API on ordinary refresh (to avoid perpetual drift against
// what the user configured) - ImportState is the one place recovering them
// is unambiguous, since there is no prior state yet to confuse "recovered at
// import" with "genuinely never configured". Architect's three-point gate,
// all exercised here:
//  1. A config that already matches the recovered values reaches an empty
//     plan (the recovered values are not phantom/unknown).
//  2. A config that omits them shows a TRUTHFUL, non-empty diff - not a
//     silent one - proving the recovered values are real tracked state, not
//     re-masked back to invisible.
//  3. The recovered values survive the immediate post-import refresh Read
//     (Read must leave them alone, not wipe them back out).
func TestAccComputeConfigImportRecoversWriteOnlyFields(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const configID = "cpt_cc12_mock"
	const configName = "cc12-import-mock"
	const cloudID = "cld_cc12_mock"

	server := newCC12MockComputeConfigServer(t, configID, configName, cloudID)
	providerBlock := testAccProviderBlock(server.URL)

	// What a user would naturally write without knowing the backend already
	// has flags/advanced_instance_config set - the realistic import case.
	configOmittingWriteOnlyFields := providerBlock + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q

  head_node = {
    instance_type = "m5.2xlarge"
  }
}
`, configName, cloudID)

	// The same config, now stating the values ImportState should have
	// recovered. jsonencode() sorts object keys alphabetically, the same way
	// Go's json.Marshal does on the provider side (both apiNodeTypeToTerraform
	// and the top-level recovery path re-marshal a decoded map), so a
	// matching plan requires getting this byte-shape right, not just the
	// logical content.
	configMatchingRecoveredValues := providerBlock + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q

  head_node = {
    instance_type = "m5.2xlarge"
  }

  flags = {
    cc12-marker-flag  = true
    cc12-marker-count = 3
  }

  advanced_instance_config = {
    disk_size         = 100
    enable_monitoring = true
  }
}
`, configName, cloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Fresh import: Config omits flags/advanced_instance_config,
				// matching what Read alone would have left in state (never
				// populated). ImportStateVerify is deliberately NOT used here
				// - it would fail on a config/state mismatch by design, since
				// proving the mismatch IS gate 2 below. ImportStatePersist is
				// required because this test has no prior apply step to
				// establish state from (the whole point is exercising a
				// fresh import as the very first action) - without it, the
				// framework discards the imported state after this step's
				// check and the next step would plan a fresh create instead
				// of diffing against what was actually imported.
				ResourceName:       "anyscale_compute_config.test",
				ImportState:        true,
				ImportStateId:      configID,
				ImportStatePersist: true,
				Config:             configOmittingWriteOnlyFields,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "config_id", configID),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "flags.cc12-marker-flag", "true"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "flags.cc12-marker-count", "3"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "advanced_instance_config.disk_size", "100"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "advanced_instance_config.enable_monitoring", "true"),
				),
			},
			{
				// Gate 1 + gate 3 together: a config that now states the
				// recovered values reaches an empty plan - proving they are
				// real, stable, known values (gate 1) that survived the
				// post-import refresh Read untouched (gate 3), not
				// re-masked or left Unknown.
				Config:             configMatchingRecoveredValues,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				// Gate 2: going back to the omitting config must show a
				// truthful, non-empty removal diff - proving state actually
				// tracks these as real values now, not a silent pass-through
				// that would let an unrelated apply wipe them with no
				// warning (the exact CC12 was designed to close).
				Config:             configOmittingWriteOnlyFields,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// newCC12PerNodeMockComputeConfigServer serves a compute config with TWO
// worker node groups, each carrying a real, realistic per-node
// advanced_instance_config - an IAM instance profile assignment, the exact
// shape already shipping in examples/aws-vm-basic/compute_config.tf
// (worker_nodes[].advanced_instance_config = jsonencode({IamInstanceProfile
// = {Arn = ...}})), per scribe's find and the user's explicit ask to cover
// workers specifically with more than one entry.
func newCC12PerNodeMockComputeConfigServer(t *testing.T, configID, configName, cloudID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	computeTemplateJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": %[2]q, "version": 1,
		"created_at": "2026-01-01T00:00:00Z", "last_modified_at": "2026-01-01T00:00:00Z",
		"archived_at": "",
		"config": {
			"cloud_id": %[3]q,
			"head_node_type": {"name": "head", "instance_type": "m5.2xlarge"},
			"worker_node_types": [
				{
					"name": "general-compute", "instance_type": "m5.4xlarge",
					"min_workers": 2, "max_workers": 10,
					"use_spot": false, "fallback_to_ondemand": false,
					"advanced_configurations_json": {"IamInstanceProfile": {"Arn": "arn:aws:iam::123456789012:instance-profile/general-compute-role"}}
				},
				{
					"name": "gpu-workers", "instance_type": "g5.2xlarge",
					"min_workers": 0, "max_workers": 5,
					"use_spot": true, "fallback_to_ondemand": true,
					"advanced_configurations_json": {"IamInstanceProfile": {"Arn": "arn:aws:iam::123456789012:instance-profile/gpu-workers-role"}}
				}
			]
		}
	}`, configID, configName, cloudID)

	mux.HandleFunc("/api/v2/compute_templates/"+configID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": `+computeTemplateJSON+`}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccComputeConfigImportRecoversPerNodeAdvancedInstanceConfig is CC12's
// per-node companion to TestAccComputeConfigImportRecoversWriteOnlyFields,
// requested explicitly by the user after review caught that the top-level
// test never actually exercised the nested case despite a comment implying
// it did. Unlike the top-level Dynamic fields (CC15's structural List-vs-
// Tuple concern), per-node advanced_instance_config/flags are plain JSON
// STRINGS (schema.StringAttribute) - apiNodeTypeToTerraform/
// apiWorkerNodeTypeToTerraform build them via a straight json.Marshal of the
// decoded API response. The open question here is a byte-compare one: does
// Go's compact, sorted-key json.Marshal output match what Terraform's own
// jsonencode() produces for the same logical content. Architect's ruling: if
// this does not reach an empty plan, that is a real per-node JSON
// canonicalization fix for forge (in the same spirit as CC15), not a
// shrug-and-document fallback - the user confirmed this is a common real
// customer pattern, not an edge case.
func TestAccComputeConfigImportRecoversPerNodeAdvancedInstanceConfig(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const configID = "cpt_cc12_pernode_mock"
	const configName = "cc12-pernode-mock"
	const cloudID = "cld_cc12_pernode_mock"

	server := newCC12PerNodeMockComputeConfigServer(t, configID, configName, cloudID)
	providerBlock := testAccProviderBlock(server.URL)

	configOmittingPerNodeFields := providerBlock + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q

  head_node = {
    instance_type = "m5.2xlarge"
  }

  worker_nodes = [
    {
      name          = "general-compute"
      instance_type = "m5.4xlarge"
      min_nodes     = 2
      max_nodes     = 10
      market_type   = "ON_DEMAND"
    },
    {
      name          = "gpu-workers"
      instance_type = "g5.2xlarge"
      min_nodes     = 0
      max_nodes     = 5
      market_type   = "PREFER_SPOT"
    }
  ]
}
`, configName, cloudID)

	// jsonencode() sorts object keys alphabetically the same way Go's
	// json.Marshal does on the provider side - the real question this test
	// answers is whether the two independently-produced compact JSON strings
	// are byte-identical, not just semantically equivalent.
	configMatchingRecoveredValues := providerBlock + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q

  head_node = {
    instance_type = "m5.2xlarge"
  }

  worker_nodes = [
    {
      name          = "general-compute"
      instance_type = "m5.4xlarge"
      min_nodes     = 2
      max_nodes     = 10
      market_type   = "ON_DEMAND"
      advanced_instance_config = jsonencode({
        IamInstanceProfile = {
          Arn = "arn:aws:iam::123456789012:instance-profile/general-compute-role"
        }
      })
    },
    {
      name          = "gpu-workers"
      instance_type = "g5.2xlarge"
      min_nodes     = 0
      max_nodes     = 5
      market_type   = "PREFER_SPOT"
      advanced_instance_config = jsonencode({
        IamInstanceProfile = {
          Arn = "arn:aws:iam::123456789012:instance-profile/gpu-workers-role"
        }
      })
    }
  ]
}
`, configName, cloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_compute_config.test",
				ImportState:        true,
				ImportStateId:      configID,
				ImportStatePersist: true,
				Config:             configOmittingPerNodeFields,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "config_id", configID),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.#", "2"),
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "worker_nodes.0.advanced_instance_config"),
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "worker_nodes.1.advanced_instance_config"),
				),
			},
			{
				// The actual gate: does the recovered per-node JSON string
				// byte-match what jsonencode() produces for the same content.
				Config:             configMatchingRecoveredValues,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				// Gate 2's per-node analogue: omitting must show a truthful
				// diff, not a silent one.
				Config:             configOmittingPerNodeFields,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}
