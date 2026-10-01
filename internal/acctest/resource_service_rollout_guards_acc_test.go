// Mock-backed regression suite for two anyscale_service plan-time rules around rollouts:
//
//   - description is not a version-defining field the IN_PLACE backend path honors (it silently
//     drops it), so a description change under rollout_strategy = "IN_PLACE" must be rejected at
//     plan time instead of reporting success and reverting on the next refresh.
//   - connection_ids is never refreshed from the API, so it is null in state after an import (or
//     whenever it was omitted at create). Declaring the connections the service already has must
//     compare against primary_version.connection_ids rather than null, or the first apply rolls
//     out a new version (or is rejected under IN_PLACE) for no real change.
//
// Every test counts PUT /services-v2/apply calls.
package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const rolloutGuardsProject = "prj_rollout_guards"

type rolloutGuardsMock struct {
	applies     int32
	terminated  int32
	description atomic.Value // string; the stored description, as the real backend persists it
}

// newRolloutGuardsServer serves a RUNNING service whose primary_version reports liveConnIDs.
func newRolloutGuardsServer(t *testing.T, serviceID, name string, liveConnIDs []string) (*rolloutGuardsMock, *httptest.Server) {
	t.Helper()
	m := &rolloutGuardsMock{}
	conns, err := json.Marshal(liveConnIDs)
	if err != nil {
		t.Fatalf("marshal connection ids: %v", err)
	}
	body := func(state string) string {
		desc, _ := m.description.Load().(string)
		descJSON := "null"
		if desc != "" {
			descJSON = fmt.Sprintf("%q", desc)
		}
		return fmt.Sprintf(`{
		"id": %[1]q, "name": %[2]q, "project_id": %[3]q, "cloud_id": "cld_findings", "description": %[6]s,
		"hostname": "findings.example.com", "base_url": "https://findings.example.com",
		"current_state": %[4]q, "goal_state": "RUNNING",
		"creator_id": "usr_findings", "created_at": "2026-01-01T00:00:00Z",
		"is_multi_version": false, "auto_rollout_enabled": true,
		"service_observability_urls": {},
		"primary_version": {
			"id": "svcver_findings", "created_at": "2026-01-01T00:00:00Z", "version": "v1",
			"current_state": %[4]q, "weight": 100, "build_id": "bld_findings",
			"compute_config_id": "cpt_findings", "production_job_ids": [], "connection_ids": %[5]s,
			"ray_serve_config": {"applications": [{"import_path": "main:app"}]}
		}
	}`, serviceID, name, rolloutGuardsProject, state, conns, descJSON)
	}
	state := func() string {
		if atomic.LoadInt32(&m.terminated) == 1 {
			return "TERMINATED"
		}
		return "RUNNING"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.applies, 1)
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Description *string `json:"description"`
			Strategy    string  `json:"rollout_strategy"`
		}
		_ = json.Unmarshal(raw, &req)
		// Like the real backend, the IN_PLACE upgrade path does not persist a changed description;
		// Create always takes the standard path.
		if req.Description != nil && (req.Strategy != "IN_PLACE" || atomic.LoadInt32(&m.applies) == 1) {
			m.description.Store(*req.Description)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+body("RUNNING")+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		serveServiceGetOrDelete(t, w, r, body(state()))
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

func (m *rolloutGuardsMock) expectApplies(want int, why string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := int(atomic.LoadInt32(&m.applies)); got != want {
			return fmt.Errorf("PUT /services-v2/apply called %d time(s), want %d - %s", got, want, why)
		}
		return nil
	}
}

// rolloutGuardsConfig renders the service; extra holds optional attribute lines.
func rolloutGuardsConfig(serverURL, name, importPath, extra string) string {
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
`, name, rolloutGuardsProject, importPath, extra)
}

func updateAction() resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{
			plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
		},
	}
}

func strategyLine(strategy string) string {
	return fmt.Sprintf("  rollout_strategy = %q\n", strategy)
}

// TestAccServiceResource_InPlaceRejectsDescriptionChange: under IN_PLACE the backend drops a
// changed description, so the plan must fail. Controls on the same path: an unchanged description
// with a ray_serve_config change (the one change IN_PLACE permits) still applies.
func TestAccServiceResource_InPlaceRejectsDescriptionChange(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const id, name = "svc_inplace_desc", "inplace-desc"
	m, server := newRolloutGuardsServer(t, id, name, nil)
	inPlace := strategyLine("IN_PLACE")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: rolloutGuardsConfig(server.URL, name, "main:app", inPlace+`  description = "first"`),
				Check:  m.expectApplies(1, "Create applies once"),
			},
			{
				Config:      rolloutGuardsConfig(server.URL, name, "main:app", inPlace+`  description = "second"`),
				ExpectError: regexp.MustCompile(`(?s)IN_PLACE.*description`),
			},
			{
				// Positive control: same strategy, same description, permitted ray_serve_config change.
				Config:           rolloutGuardsConfig(server.URL, name, "main:app_v2", inPlace+`  description = "first"`),
				ConfigPlanChecks: updateAction(),
				Check:            m.expectApplies(2, "a ray_serve_config-only change under IN_PLACE still deploys"),
			},
		},
	})
}

// TestAccServiceResource_RolloutAcceptsDescriptionChange: the same description change under
// ROLLOUT is accepted and deploys.
func TestAccServiceResource_RolloutAcceptsDescriptionChange(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const id, name = "svc_rollout_desc", "rollout-desc"
	m, server := newRolloutGuardsServer(t, id, name, nil)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: rolloutGuardsConfig(server.URL, name, "main:app", `  description = "first"`),
				Check:  m.expectApplies(1, "Create applies once"),
			},
			{
				Config:           rolloutGuardsConfig(server.URL, name, "main:app", `  description = "second"`),
				ConfigPlanChecks: updateAction(),
				Check:            m.expectApplies(2, "a description change under ROLLOUT deploys once"),
			},
		},
	})
}

func connLine(ids ...string) string {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	return fmt.Sprintf("  connection_ids = [%s]\n", strings.Join(quoted, ", "))
}

// TestAccServiceResource_ConnectionIDsDeclaredAfterOmittedNoRedeploy (Test B): state carries a
// null connection_ids (the shape import produces), then the config declares the connections the
// service already has. That must not deploy. Controls: declaring a different set deploys once.
func TestAccServiceResource_ConnectionIDsDeclaredAfterOmittedNoRedeploy(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const id, name = "svc_conn_declared", "conn-declared"
	m, server := newRolloutGuardsServer(t, id, name, []string{"con_a", "con_b"})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: rolloutGuardsConfig(server.URL, name, "main:app", ""),
				Check:  m.expectApplies(1, "Create applies once"),
			},
			{
				Config:           rolloutGuardsConfig(server.URL, name, "main:app", connLine("con_a", "con_b")),
				ConfigPlanChecks: updateAction(),
				Check:            m.expectApplies(1, "declaring the already-attached connections must not roll out a new version"),
			},
			{
				// Positive control: a genuinely different set still deploys.
				Config:           rolloutGuardsConfig(server.URL, name, "main:app", connLine("con_a", "con_c")),
				ConfigPlanChecks: updateAction(),
				Check:            m.expectApplies(2, "changed connection_ids must still roll out"),
			},
		},
	})
}

// TestAccServiceResource_ConnectionIDsDeclaredAfterOmittedInPlace (Test B, IN_PLACE): the same
// declaration under IN_PLACE must plan clean rather than fail with "connection_ids changed".
// Control: a different set is still rejected.
func TestAccServiceResource_ConnectionIDsDeclaredAfterOmittedInPlace(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const id, name = "svc_conn_inplace", "conn-inplace"
	m, server := newRolloutGuardsServer(t, id, name, []string{"con_a", "con_b"})
	inPlace := strategyLine("IN_PLACE")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: rolloutGuardsConfig(server.URL, name, "main:app", inPlace),
				Check:  m.expectApplies(1, "Create applies once"),
			},
			{
				Config:           rolloutGuardsConfig(server.URL, name, "main:app", inPlace+connLine("con_a", "con_b")),
				ConfigPlanChecks: updateAction(),
				Check:            m.expectApplies(1, "declaring the already-attached connections must not deploy"),
			},
			{
				// Positive control. The prior step set state to the declared list, so this is a
				// real change and IN_PLACE must reject it.
				Config:      rolloutGuardsConfig(server.URL, name, "main:app", inPlace+connLine("con_a", "con_c")),
				ExpectError: regexp.MustCompile(`(?s)IN_PLACE.*connection_ids`),
			},
		},
	})
}

// TestAccServiceResource_ImportConnectionIDsNoRedeploy (Test A + cold import): a cold import
// leaves connection_ids null but recovers the live set in primary_version.connection_ids; the
// next apply of a config declaring that same set must not deploy.
func TestAccServiceResource_ImportConnectionIDsNoRedeploy(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const id, name = "svc_conn_import", "conn-import"
	m, server := newRolloutGuardsServer(t, id, name, []string{"con_a", "con_b"})
	config := rolloutGuardsConfig(server.URL, name, "main:app", connLine("con_a", "con_b"))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_service.test",
				ImportState:        true,
				ImportStateId:      id,
				ImportStatePersist: true,
				Config:             config,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance, got %d", len(states))
					}
					a := states[0].Attributes
					if v, ok := a["connection_ids.#"]; ok && v != "" && v != "0" {
						return fmt.Errorf("connection_ids after import = %q entries, want null", v)
					}
					if got := a["primary_version.connection_ids.#"]; got != "2" {
						return fmt.Errorf("primary_version.connection_ids.# = %q, want 2", got)
					}
					return nil
				},
			},
			{
				Config:           config,
				ConfigPlanChecks: updateAction(),
				Check:            m.expectApplies(0, "applying declared connections that match the live set must not deploy"),
			},
		},
	})
}
