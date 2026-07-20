package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// serviceRolloutJSON is like serviceFindingsJSON but also varies the primary_version's id/version
// - used to prove a real new-version rollout actually replaces primary_version, not just that
// current_state cycles back to RUNNING.
func serviceRolloutJSON(id, name, projectID, currentState, versionID, version string) string {
	return fmt.Sprintf(`{
		"id": %[1]q, "name": %[2]q, "project_id": %[3]q, "cloud_id": "cld_findings",
		"hostname": "findings.example.com", "base_url": "https://findings.example.com",
		"current_state": %[4]q, "goal_state": "RUNNING",
		"creator_id": "usr_findings", "created_at": "2026-01-01T00:00:00Z",
		"is_multi_version": false, "auto_rollout_enabled": true,
		"service_observability_urls": {},
		"primary_version": {
			"id": %[5]q, "created_at": "2026-01-01T00:00:00Z", "version": %[6]q,
			"current_state": %[4]q, "weight": 100, "build_id": "bld_findings",
			"compute_config_id": "cpt_findings", "production_job_ids": [], "connection_ids": [],
			"ray_serve_config": {}
		}
	}`, id, name, projectID, currentState, versionID, version)
}

// TestAccServiceResource_UpdateRedeploysAndConverges is the mock companion to the real-infra
// rollout test - cheaper and faster, proving the FRAMEWORK-level mechanics of a real redeploy
// (a second PUT /apply actually fires, and the wait loop drives STARTING/ROLLING_OUT through to
// RUNNING for an UPDATE, not just a Create) without needing real compute. Contract gap: H2
// (UpdateSkipsApplyWhenOnlyTimeoutChanges) only proves the OPPOSITE case - when a change must NOT
// trigger a redeploy. Nothing in the existing suite proved the redeploy path itself converges.
func TestAccServiceResource_UpdateRedeploysAndConverges(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_rollout_converge"
	// transitionalWindow bounds how long each apply's GET responses report a transitional state
	// before settling to RUNNING - keyed off elapsed real time since the LAST apply, not a poll
	// count. A poll-count approach is fragile here: terraform-plugin-testing issues its own
	// refresh/plan-verification GETs around each step (e.g. ConfigPlanChecks.PostApplyPostRefresh
	// triggers an extra refresh), so the exact number of GETs per step isn't fully predictable.
	// Time-based is robust to that: any GET within the window sees the transitional state
	// (however many extra framework-internal calls land in it), and the production wait loop's
	// own real ~10s poll interval guarantees at least one real sleep happens before the window
	// closes, so the wait loop is still proven to actually wait and re-poll, not just check once.
	const transitionalWindow = 3 * time.Second

	var mu sync.Mutex
	applyCallCount := 0
	lastApplyAt := time.Time{}
	terminated := false

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		applyCallCount++
		n := applyCallCount
		lastApplyAt = time.Now()
		mu.Unlock()

		versionID, version := "svcver_v1", "v1"
		state := "STARTING"
		if n >= 2 {
			versionID, version = "svcver_v2", "v2"
			state = "ROLLING_OUT"
		}
		// The apply response itself reports the TRANSITIONAL state (202 + in-flight service) -
		// matches contract §5b: apply returns 202 with the service already mid-rollout, not
		// RUNNING immediately.
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceRolloutJSON(serviceID, "rollout-converge", "prj_rollout", state, versionID, version)+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n := applyCallCount
		withinWindow := n > 0 && time.Since(lastApplyAt) < transitionalWindow
		isTerminated := terminated
		mu.Unlock()

		versionID, version := "svcver_v1", "v1"
		if n >= 2 {
			versionID, version = "svcver_v2", "v2"
		}

		// Once terminate has fired (the automatic end-of-test destroy), report TERMINATED so
		// that wait loop resolves - otherwise it polls a state (RUNNING) that can never satisfy
		// its target (TERMINATED) and hangs for the real rollout_timeout default (30m). Checked
		// BEFORE the transitional-window logic since destroy always happens after both applies.
		state := "RUNNING"
		switch {
		case isTerminated:
			state = "TERMINATED"
		case withinWindow:
			if n == 1 {
				state = "STARTING"
			} else {
				state = "ROLLING_OUT"
			}
		}

		serveServiceGetOrDelete(t, w, r, serviceRolloutJSON(serviceID, "rollout-converge", "prj_rollout", state, versionID, version))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		terminated = true
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	baseConfig := testAccProviderBlock(server.URL) + `
resource "anyscale_service" "test" {
  name              = "rollout-converge"
  project_id        = "prj_rollout"
  build_id          = "bld_findings"
  compute_config_id = "cpt_findings"
  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
      }
    ]
  }
}
`
	// updatedConfig changes import_path - a genuine version-defining field change (contract §6),
	// so this must trigger a new PUT /apply and a real rollout, not the H2 no-op path.
	updatedConfig := testAccProviderBlock(server.URL) + `
resource "anyscale_service" "test" {
  name              = "rollout-converge"
  project_id        = "prj_rollout"
  build_id          = "bld_findings"
  compute_config_id = "cpt_findings"
  ray_serve_config = {
    applications = [
      {
        import_path = "main:app_v2"
      }
    ]
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: baseConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.version", "v1"),
				),
			},
			{
				Config: updatedConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					// The version actually changed - proves this was a real rollout to a NEW
					// version, not just current_state cycling back to the same value.
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.version", "v2"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})

	mu.Lock()
	got := applyCallCount
	mu.Unlock()
	if got != 2 {
		t.Errorf("apply was called %d time(s), want exactly 2 (one per step) - a version-defining "+
			"field change must trigger a real second apply", got)
	}
}

// TestAccServiceResource_InPlaceUpdateConverges is the IN_PLACE-strategy counterpart to
// TestAccServiceResource_UpdateRedeploysAndConverges - the user asked for BOTH upgrade strategies
// covered, not just the ROLLOUT default. Structurally identical (a version-defining
// ray_serve_config change must trigger a real second apply and converge to RUNNING with a new
// primary_version), but transitions through UPDATING (the IN_PLACE-specific continue-state, per
// contract §5b) rather than ROLLING_OUT, and sets rollout_strategy = "IN_PLACE" explicitly on the
// update step - only ray_serve_config differs between the two configs, honoring the ModifyPlan
// invariant that IN_PLACE permits changing only that field.
func TestAccServiceResource_InPlaceUpdateConverges(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_inplace_converge"
	const transitionalWindow = 3 * time.Second

	var mu sync.Mutex
	applyCallCount := 0
	lastApplyAt := time.Time{}
	terminated := false

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		applyCallCount++
		n := applyCallCount
		lastApplyAt = time.Now()
		mu.Unlock()

		versionID, version := "svcver_v1", "v1"
		state := "STARTING"
		if n >= 2 {
			versionID, version = "svcver_v2", "v2"
			state = "UPDATING"
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceRolloutJSON(serviceID, "inplace-converge", "prj_inplace", state, versionID, version)+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n := applyCallCount
		withinWindow := n > 0 && time.Since(lastApplyAt) < transitionalWindow
		isTerminated := terminated
		mu.Unlock()

		versionID, version := "svcver_v1", "v1"
		if n >= 2 {
			versionID, version = "svcver_v2", "v2"
		}

		state := "RUNNING"
		switch {
		case isTerminated:
			state = "TERMINATED"
		case withinWindow:
			if n == 1 {
				state = "STARTING"
			} else {
				state = "UPDATING"
			}
		}

		serveServiceGetOrDelete(t, w, r, serviceRolloutJSON(serviceID, "inplace-converge", "prj_inplace", state, versionID, version))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		terminated = true
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	baseConfig := testAccProviderBlock(server.URL) + `
resource "anyscale_service" "test" {
  name              = "inplace-converge"
  project_id        = "prj_inplace"
  build_id          = "bld_findings"
  compute_config_id = "cpt_findings"
  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
      }
    ]
  }
}
`
	// updatedConfig sets rollout_strategy = IN_PLACE and changes ONLY ray_serve_config - the one
	// field IN_PLACE permits changing (contract §4/ModifyPlan); build_id/compute_config_id/
	// connection_ids stay untouched on purpose.
	updatedConfig := testAccProviderBlock(server.URL) + `
resource "anyscale_service" "test" {
  name              = "inplace-converge"
  project_id        = "prj_inplace"
  build_id          = "bld_findings"
  compute_config_id = "cpt_findings"
  rollout_strategy  = "IN_PLACE"
  ray_serve_config = {
    applications = [
      {
        import_path = "main:app_v2"
      }
    ]
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: baseConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.version", "v1"),
				),
			},
			{
				Config: updatedConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.version", "v2"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})

	mu.Lock()
	got := applyCallCount
	mu.Unlock()
	if got != 2 {
		t.Errorf("apply was called %d time(s), want exactly 2 (one per step) - an IN_PLACE "+
			"ray_serve_config change must still trigger a real second apply", got)
	}
}
