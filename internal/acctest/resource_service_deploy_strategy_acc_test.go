// Mock-backed regression suite for rollout_strategy / max_surge_percent being deploy DIRECTIVES
// (how a deploy happens), not deploy FIELDS (what is deployed). Neither is readable back from the
// API, so both are null after ImportState; when Update counted them as deploy fields, the first
// apply of an unchanged config after `terraform import` redeployed the running service.
//
// Every test counts PUT /services-v2/apply calls. The post-import tests assert zero applies, NOT
// an empty plan: the plan still shows an in-place update of the two local-only attributes
// (null -> configured/default value), which is the accepted residual - apply persists it to state
// without any remote call.
package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// deployStrategyMock is a minimal services-v2 backend that records a COPY of every PUT /apply
// request body (never a reference into a buffer or map that is reused later).
type deployStrategyMock struct {
	mu          sync.Mutex
	applyBodies [][]byte
	terminated  int32
}

func (m *deployStrategyMock) applyCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.applyBodies)
}

// applyBody decodes the i-th captured apply body.
func (m *deployStrategyMock) applyBody(t *testing.T, i int) map[string]any {
	t.Helper()
	m.mu.Lock()
	raw := append([]byte(nil), m.applyBodies[i]...)
	m.mu.Unlock()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode apply body %d: %v (raw: %s)", i, err, raw)
	}
	return body
}

// deployStrategyServiceJSON is a RUNNING (or TERMINATED) service whose primary_version carries a
// real ray_serve_config, so a cold import seeds a value matching deployStrategyConfig's.
func deployStrategyServiceJSON(id, name, projectID, currentState string) string {
	return fmt.Sprintf(`{
		"id": %[1]q, "name": %[2]q, "project_id": %[3]q, "cloud_id": "cld_findings",
		"hostname": "findings.example.com", "base_url": "https://findings.example.com",
		"current_state": %[4]q, "goal_state": "RUNNING",
		"creator_id": "usr_findings", "created_at": "2026-01-01T00:00:00Z",
		"is_multi_version": false, "auto_rollout_enabled": true,
		"service_observability_urls": {},
		"primary_version": {
			"id": "svcver_findings", "created_at": "2026-01-01T00:00:00Z", "version": "v1",
			"current_state": %[4]q, "weight": 100, "build_id": "bld_findings",
			"compute_config_id": "cpt_findings", "production_job_ids": [], "connection_ids": [],
			"ray_serve_config": {"applications": [{"import_path": "main:app"}]}
		}
	}`, id, name, projectID, currentState)
}

func newDeployStrategyServer(t *testing.T, serviceID, name, projectID string) (*deployStrategyMock, *httptest.Server) {
	t.Helper()
	m := &deployStrategyMock{}
	state := func() string {
		if atomic.LoadInt32(&m.terminated) == 1 {
			return "TERMINATED"
		}
		return "RUNNING"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read apply body: %v", err)
		}
		m.mu.Lock()
		m.applyBodies = append(m.applyBodies, append([]byte(nil), raw...))
		m.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+deployStrategyServiceJSON(serviceID, name, projectID, "RUNNING")+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		serveServiceGetOrDelete(t, w, r, deployStrategyServiceJSON(serviceID, name, projectID, state()))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&m.terminated, 1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return m, server
}

// deployStrategyConfig renders the service with the given import_path and extra attribute lines
// (rollout_strategy / max_surge_percent, or "").
func deployStrategyConfig(serverURL, name, projectID, importPath, extra string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_service" "test" {
  name              = %[1]q
  project_id        = %[2]q
  build_id          = "bld_findings"
  compute_config_id = "cpt_findings"
%[4]s
  ray_serve_config = {
    applications = [
      {
        import_path = %[3]q
      }
    ]
  }
}
`, name, projectID, importPath, extra)
}

func expectApplyCount(m *deployStrategyMock, want int, why string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := m.applyCount(); got != want {
			return fmt.Errorf("PUT /services-v2/apply called %d time(s), want %d - %s", got, want, why)
		}
		return nil
	}
}

// testAccServiceColdImportNoRedeploy imports a pre-existing service as the FIRST step (so the
// imported state is what the next step plans against), then applies the unchanged config.
func testAccServiceColdImportNoRedeploy(t *testing.T, serviceID, name, extra string, checks ...resource.TestCheckFunc) {
	t.Helper()
	const projectID = "prj_deploy_strategy"
	m, server := newDeployStrategyServer(t, serviceID, name, projectID)
	config := deployStrategyConfig(server.URL, name, projectID, "main:app", extra)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_service.test",
				ImportState:        true,
				ImportStateId:      serviceID,
				ImportStatePersist: true,
				Config:             config,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					// The precondition that made import redeploy: neither directive is readable
					// back from the API, so both are null in the imported state.
					for _, attr := range []string{"rollout_strategy", "max_surge_percent"} {
						if v, ok := states[0].Attributes[attr]; ok && v != "" {
							return fmt.Errorf("%s after import = %q, want null (not readable from the API)", attr, v)
						}
					}
					return nil
				},
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					// Accepted residual: the null -> value directive diff is still planned.
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(append([]resource.TestCheckFunc{
					expectApplyCount(m, 0, "applying an unchanged config after import must not redeploy the running service"),
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
				}, checks...)...),
			},
		},
	})

	if got := m.applyCount(); got != 0 {
		t.Errorf("PUT /services-v2/apply called %d time(s) across import + unchanged apply, want 0", got)
	}
}

// TestAccServiceResource_ImportUnchangedDefaultStrategyNoRedeploy: config omits rollout_strategy,
// so its "ROLLOUT" default is the only planned change after import.
func TestAccServiceResource_ImportUnchangedDefaultStrategyNoRedeploy(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccServiceColdImportNoRedeploy(t, "svc_import_default_strategy", "import-default-strategy", "",
		resource.TestCheckResourceAttr("anyscale_service.test", "rollout_strategy", "ROLLOUT"),
		resource.TestCheckNoResourceAttr("anyscale_service.test", "max_surge_percent"),
	)
}

// TestAccServiceResource_ImportUnchangedMaxSurgeNoRedeploy: config sets max_surge_percent, which
// is null after import regardless of what the service was deployed with.
func TestAccServiceResource_ImportUnchangedMaxSurgeNoRedeploy(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccServiceColdImportNoRedeploy(t, "svc_import_max_surge", "import-max-surge", "  max_surge_percent = 25",
		resource.TestCheckResourceAttr("anyscale_service.test", "rollout_strategy", "ROLLOUT"),
		resource.TestCheckResourceAttr("anyscale_service.test", "max_surge_percent", "25"),
	)
}

// testAccServiceDirectiveChange creates the service with baseExtra, then applies updatedExtra
// (and updatedImportPath). wantApplies is the total apply count after step 2 (1 = Create only).
// checkBody, if non-nil, inspects the second apply's body.
func testAccServiceDirectiveChange(t *testing.T, serviceID, name, baseExtra, updatedExtra, updatedImportPath string,
	wantApplies int, why string, checkBody func(body map[string]any) error, checks ...resource.TestCheckFunc) {
	t.Helper()
	const projectID = "prj_deploy_strategy"
	m, server := newDeployStrategyServer(t, serviceID, name, projectID)

	step2Checks := []resource.TestCheckFunc{expectApplyCount(m, wantApplies, why)}
	if checkBody != nil {
		step2Checks = append(step2Checks, func(*terraform.State) error {
			if m.applyCount() < 2 {
				return fmt.Errorf("no second apply captured")
			}
			return checkBody(m.applyBody(t, 1))
		})
	}
	step2Checks = append(step2Checks, checks...)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: deployStrategyConfig(server.URL, name, projectID, "main:app", baseExtra),
				Check:  expectApplyCount(m, 1, "Create must apply exactly once"),
			},
			{
				Config: deployStrategyConfig(server.URL, name, projectID, updatedImportPath, updatedExtra),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(step2Checks...),
			},
		},
	})
}

func expectBodyField(field string, want any) func(map[string]any) error {
	return func(body map[string]any) error {
		if got := body[field]; fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("apply body %s = %v, want %v - the deploy must send the PLANNED value", field, got, want)
		}
		return nil
	}
}

// TestAccServiceResource_RolloutStrategyOnlyChangeNoRedeploy: ROLLOUT -> IN_PLACE alone is a
// state-only update.
func TestAccServiceResource_RolloutStrategyOnlyChangeNoRedeploy(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccServiceDirectiveChange(t, "svc_strategy_only", "strategy-only",
		"", `  rollout_strategy = "IN_PLACE"`, "main:app",
		1, "changing only rollout_strategy must not redeploy", nil,
		resource.TestCheckResourceAttr("anyscale_service.test", "rollout_strategy", "IN_PLACE"),
		resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
	)
}

// TestAccServiceResource_RolloutStrategyWithDeployChangeSendsPlanned: IN_PLACE together with a
// ray_serve_config change (the one change IN_PLACE permits) deploys once, with IN_PLACE.
func TestAccServiceResource_RolloutStrategyWithDeployChangeSendsPlanned(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccServiceDirectiveChange(t, "svc_strategy_deploy", "strategy-deploy",
		"", `  rollout_strategy = "IN_PLACE"`, "main:app_v2",
		2, "a ray_serve_config change must deploy exactly once",
		expectBodyField("rollout_strategy", "IN_PLACE"),
		resource.TestCheckResourceAttr("anyscale_service.test", "rollout_strategy", "IN_PLACE"),
	)
}

// TestAccServiceResource_MaxSurgeOnlyChangeNoRedeploy: 25 -> 50 alone is a state-only update.
func TestAccServiceResource_MaxSurgeOnlyChangeNoRedeploy(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccServiceDirectiveChange(t, "svc_surge_only", "surge-only",
		"  max_surge_percent = 25", "  max_surge_percent = 50", "main:app",
		1, "changing only max_surge_percent must not redeploy", nil,
		resource.TestCheckResourceAttr("anyscale_service.test", "max_surge_percent", "50"),
		resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
	)
}

// TestAccServiceResource_MaxSurgeWithDeployChangeSendsPlanned: 25 -> 50 together with a
// ray_serve_config change deploys once, with 50.
func TestAccServiceResource_MaxSurgeWithDeployChangeSendsPlanned(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccServiceDirectiveChange(t, "svc_surge_deploy", "surge-deploy",
		"  max_surge_percent = 25", "  max_surge_percent = 50", "main:app_v2",
		2, "a ray_serve_config change must deploy exactly once",
		expectBodyField("max_surge_percent", 50),
		resource.TestCheckResourceAttr("anyscale_service.test", "max_surge_percent", "50"),
	)
}
