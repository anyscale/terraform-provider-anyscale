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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Mock-backed import round-trip coverage for anyscale_service: Create, then an
// ImportState step with ImportStateVerify, so every attribute import recovers
// is byte-compared against what Create wrote.
//
// The mock is stateful and echoes what the provider sent, matching the real
// backend: PUT /apply's ray_serve_config/description/build_id/compute_config_id
// come back on GET (ray_serve_config under primary_version, which is where
// ImportState seeds it from), and PUT /tags/resource upserts into the set GET
// /tags/resource returns. A mock that returned a fixed `"ray_serve_config": {}`
// (as the older service mocks do) could not detect ImportState seeding the
// wrong value, so this one deliberately does not.

// serviceImportVerifyMock holds the service as last applied, plus its tags.
type serviceImportVerifyMock struct {
	mu             sync.Mutex
	applied        map[string]json.RawMessage // raw PUT /apply body fields
	tags           map[string]string
	terminated     bool
	applyCallCount int
}

func (m *serviceImportVerifyMock) serviceJSON(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := "RUNNING"
	if m.terminated {
		state = "TERMINATED"
	}
	field := func(k, fallback string) string {
		if v, ok := m.applied[k]; ok && string(v) != "null" {
			return string(v)
		}
		return fallback
	}
	return fmt.Sprintf(`{
		"id": %[1]q, "name": %[2]s, "project_id": %[3]s, "cloud_id": "cld_svc_import",
		"description": %[4]s,
		"hostname": "svc-import.example.com", "base_url": "https://svc-import.example.com",
		"current_state": %[5]q, "goal_state": "RUNNING",
		"creator_id": "usr_svc_import", "created_at": "2026-01-01T00:00:00Z",
		"is_multi_version": false, "auto_rollout_enabled": true,
		"service_observability_urls": {},
		"primary_version": {
			"id": "svcver_import", "created_at": "2026-01-01T00:00:00Z", "version": "v1",
			"current_state": %[5]q, "weight": 100, "build_id": %[6]s,
			"compute_config_id": %[7]s, "production_job_ids": [], "connection_ids": [],
			"ray_serve_config": %[8]s
		}
	}`, id, field("name", `""`), field("project_id", `"prj_svc_import_default"`),
		field("description", "null"), state,
		field("build_id", `""`), field("compute_config_id", `""`), field("ray_serve_config", "{}"))
}

func (m *serviceImportVerifyMock) tagsJSON() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	type tag struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	out := struct {
		Result struct {
			Tags []tag `json:"tags"`
		} `json:"result"`
	}{}
	out.Result.Tags = []tag{}
	for k, v := range m.tags {
		out.Result.Tags = append(out.Result.Tags, tag{Key: k, Value: v})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func newServiceImportVerifyMockServer(t *testing.T, serviceID string) (*httptest.Server, *serviceImportVerifyMock) {
	t.Helper()
	m := &serviceImportVerifyMock{tags: map[string]string{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected method %s on services-v2/apply", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var applied map[string]json.RawMessage
		if err := json.Unmarshal(body, &applied); err != nil {
			t.Errorf("apply body is not a JSON object: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.applied = applied
		m.applyCallCount++
		m.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+m.serviceJSON(serviceID)+`}`)
	})
	// Create's adoption guard lists by name first; nothing exists yet.
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		serveServiceGetOrDelete(t, w, r, m.serviceJSON(serviceID))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.terminated = true
		m.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, m.tagsJSON())
		case http.MethodPut:
			var req struct {
				Tags map[string]string `json:"tags"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("tags upsert body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			m.mu.Lock()
			for k, v := range req.Tags {
				m.tags[k] = v
			}
			m.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"result": {}}`)
		default:
			t.Errorf("unexpected method %s on tags/resource", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, m
}

// TestAccServiceResource_ImportStateVerify_MockServer proves that importing an
// anyscale_service recovers the same state Create wrote, attribute for
// attribute: ray_serve_config (seeded by ImportState from
// primary_version.ray_serve_config), name/description/build_id/
// compute_config_id/project_id (refreshed by Read), tags (a separate
// endpoint), and every Computed output.
//
// The config sets description and tags, not just the required attributes, so
// both of Read's secondary refresh paths are exercised: a Read that stopped
// refreshing either would leave it null after import and fail verification.
func TestAccServiceResource_ImportStateVerify_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_import_verify"
	const projectID = "prj_svc_import_verify"
	server, m := newServiceImportVerifyMockServer(t, serviceID)

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_service" "test" {
  name              = "svc-import-verify"
  project_id        = %[1]q
  description       = "import round-trip"
  build_id          = "bld_svc_import"
  compute_config_id = "cpt_svc_import"
  tags = {
    env = "test"
  }
%[2]s
}
`, projectID, testAccServiceRayServeConfigHCL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "id", serviceID),
					resource.TestCheckResourceAttr("anyscale_service.test", "rollout_strategy", "ROLLOUT"),
					resource.TestCheckResourceAttr("anyscale_service.test", "tags.env", "test"),
				),
			},
			{
				Config:            config,
				ResourceName:      "anyscale_service.test",
				ImportState:       true,
				ImportStateId:     serviceID,
				ImportStateVerify: true,
				// rollout_strategy is a rollout directive the API never returns (see its
				// schema description), so import has nothing to recover it from: Create's
				// state holds the schema Default "ROLLOUT", the imported state holds null.
				// ImportStateCheck below pins that null explicitly rather than excluding the
				// attribute silently. Note the consequence: a cold import's first plan shows
				// rollout_strategy null -> "ROLLOUT" as an in-place update, and Update counts
				// rollout_strategy as a deploy field, so that apply re-sends PUT /apply. A fix
				// that seeds the default at import time should flip this assertion.
				ImportStateVerifyIgnore: []string{"rollout_strategy"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported resource, got %d", len(states))
					}
					attrs := states[0].Attributes
					if v, ok := attrs["rollout_strategy"]; ok && v != "" {
						return fmt.Errorf("rollout_strategy after import = %q, want null: it is not "+
							"returned by the API, so a non-null value means ImportState/Read started "+
							"fabricating it", v)
					}
					// Spot-check the recovered values that matter most, so a failure names
					// the field rather than only a verify diff.
					for k, want := range map[string]string{
						"id":                serviceID,
						"project_id":        projectID,
						"build_id":          "bld_svc_import",
						"compute_config_id": "cpt_svc_import",
						"description":       "import round-trip",
						"tags.env":          "test",
					} {
						if got := attrs[k]; got != want {
							return fmt.Errorf("%s after import = %q, want %q", k, got, want)
						}
					}
					return nil
				},
			},
		},
	})

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.applyCallCount != 1 {
		t.Errorf("PUT /apply called %d times, want exactly 1 (Create only; import must not deploy)", m.applyCallCount)
	}
}
