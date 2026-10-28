// Mock-backed tests for anyscale_service's compute_config_id validator: a value without the cpt_
// prefix - typically anyscale_compute_config.<x>.id, which is the config's name - must be rejected
// before PUT /services-v2/apply is ever called.
package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// computeConfigIDErr matches the validator's own message, never the mock's 400 body, so a run
// where the bad value reaches the API cannot satisfy ExpectError.
var computeConfigIDErr = regexp.MustCompile(`(?s)Invalid Compute Config ID.*use its\s+config_id\s+output,\s+not\s+id`)

// newComputeConfigIDServer serves anyscale_compute_config CRUD and counts service applies. The
// apply handler answers 400 because no step in these tests should reach it.
func newComputeConfigIDServer(t *testing.T) (*int32, *httptest.Server) {
	t.Helper()
	var applies int32
	var mu sync.Mutex
	var record map[string]any

	mux := http.NewServeMux()
	computeTemplates := func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var req struct {
				Name   string         `json:"name"`
				Config map[string]any `json:"config"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			if req.Config == nil {
				req.Config = map[string]any{}
			}
			req.Config["cloud_id"] = "cld_ccid"
			mu.Lock()
			record = map[string]any{
				"id": "cpt_ccid", "name": req.Name, "version": 1,
				"created_at": "2026-01-01T00:00:00Z", "last_modified_at": "2026-01-01T00:00:00Z",
				"archived_at": nil, "config": req.Config,
			}
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": record})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			mu.Lock()
			rec := record
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": rec})
		}
	}
	mux.HandleFunc("/api/v2/compute_templates/", computeTemplates)
	mux.HandleFunc("/api/v2/compute_templates", computeTemplates)
	mux.HandleFunc("/api/v2/compute_templates/cpt_ccid/archive", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&applies, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error": {"detail": "mock: compute template not found"}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &applies, server
}

func noServiceApplies(applies *int32) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := atomic.LoadInt32(applies); got != 0 {
			return fmt.Errorf("PUT /services-v2/apply called %d time(s), want 0 - the invalid "+
				"compute_config_id reached the API", got)
		}
		return nil
	}
}

func computeConfigIDServiceConfig(serverURL, computeConfigBlock, computeConfigID string) string {
	return testAccProviderBlock(serverURL) + computeConfigBlock + fmt.Sprintf(`
resource "anyscale_service" "test" {
  name              = "ccid-service"
  project_id        = "prj_ccid"
  build_id          = "bld_ccid"
  compute_config_id = %s
  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
      }
    ]
  }
}
`, computeConfigID)
}

const computeConfigIDComputeConfig = `
resource "anyscale_compute_config" "test" {
  name     = "ccid-compute-config"
  cloud_id = "cld_ccid"
  head_node = {
    instance_type = "m5.large"
  }
}
`

// TestAccServiceResource_ComputeConfigIDRejectsLiteralName: a known non-cpt_ value fails at plan.
func TestAccServiceResource_ComputeConfigIDRejectsLiteralName(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	applies, server := newComputeConfigIDServer(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             noServiceApplies(applies),
		Steps: []resource.TestStep{
			{
				Config:      computeConfigIDServiceConfig(server.URL, "", `"ccid-compute-config"`),
				PlanOnly:    true,
				ExpectError: computeConfigIDErr,
			},
		},
	})
}

// TestAccServiceResource_ComputeConfigIDRejectsComputeConfigName wires the footgun itself,
// anyscale_compute_config.test.id:
//   - Step 1 creates both in one apply, so .id is unknown at plan and the validator skips there.
//     Core re-validates the service's config during apply once .id is known, so the error still
//     lands before the service apply call.
//   - Step 2 plans with the compute config already in state, so .id is known and the plan fails.
func TestAccServiceResource_ComputeConfigIDRejectsComputeConfigName(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	applies, server := newComputeConfigIDServer(t)
	config := computeConfigIDServiceConfig(server.URL, computeConfigIDComputeConfig,
		"anyscale_compute_config.test.id")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             noServiceApplies(applies),
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: computeConfigIDErr,
			},
			{
				Config:      config,
				PlanOnly:    true,
				ExpectError: computeConfigIDErr,
			},
		},
	})
}
