// Mock-backed suite for how anyscale_service reports a rollout's real outcome: a rollout the
// backend rolls back, a failed PUT /apply, an apply interrupted mid-rollout, an out-of-band
// deploy, legacy/imported state, and a terminated service still in config.
//
// The mock follows the traced wire shapes (see docs/decisions/service-rollout-outcome):
//   - an update apply on a RUNNING service returns 202 with the new version as canary_version;
//   - create and restart return the new version as primary_version;
//   - an auto-rolled-back rollout goes ROLLING_OUT -> ROLLING_BACK -> UNHEALTHY -> RUNNING; the
//     canary is already gone and the OLD version primary from ROLLING_BACK on (observed on a
//     real backend);
//   - a terminated service reports goal_state TERMINATED with its most recent version as primary.
package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// outcomeVersion is one service version as the mock reports it.
type outcomeVersion struct {
	ID, Name, ImportPath string
	ConnectionIDs        []string
	// BuildID is the version's build; "" means bld_outcome.
	BuildID string
	// RoutePrefix, when set, is added to the stored application the way the backend may enrich
	// a stored ray_serve_config, so the live copy no longer equals the authored one.
	RoutePrefix string
}

// outcomeSnapshot is what GET /services-v2/{id} returns at one point in time.
type outcomeSnapshot struct {
	CurrentState string
	GoalState    string // "" means RUNNING
	PrimaryState string // the primary version's current_state; "" means CurrentState
	Primary      outcomeVersion
	Canary       *outcomeVersion
}

// outcomeApplyResult is the mock's reaction to one PUT /apply call: either an error status, or a
// 202 body plus the script of GET snapshots that follows it (each GET advances one entry; the
// last entry repeats).
type outcomeApplyResult struct {
	Status   int
	Detail   string
	Response outcomeSnapshot
	Script   []outcomeSnapshot
}

// outcomeMock is a scriptable services-v2 backend. onApply decides what each apply does; the
// test can also replace the current GET script between steps via setScript.
type outcomeMock struct {
	t         *testing.T
	serviceID string
	name      string
	projectID string

	mu          sync.Mutex
	onApply     func(n int, body map[string]any) outcomeApplyResult
	applyBodies []map[string]any
	script      []outcomeSnapshot
	scriptPos   int
	terminated  bool

	// Bearer auth, as the backend tracks it on the service: a ROLLOUT apply issues a new token
	// when config.access.use_bearer_token turns it on (or is absent) and clears it when it turns
	// it off (services_dao.update_bearer_token); IN_PLACE never touches it. noTokens simulates the
	// feature flag that forces tokens off for a cloud or project.
	token    *string
	tokenSeq int
	noTokens bool

	// Builds known to GET /builds/{id} and get_or_create_build_from_image_uri, by ID.
	builds         map[string]outcomeBuild
	getOrCreateLog []map[string]any
	// archivedURIs are registry URIs whose anonymous environment was archived: get_or_create
	// answers them with the backend's 409.
	archivedURIs map[string]bool
}

// outcomeBuild is one build as GET /builds/{id} reports it.
type outcomeBuild struct {
	DockerImage, Secret, TemplateName string
	Revision                          int
}

func newOutcomeMock(t *testing.T, serviceID, name string, onApply func(n int, body map[string]any) outcomeApplyResult) (*outcomeMock, *httptest.Server) {
	t.Helper()
	m := &outcomeMock{t: t, serviceID: serviceID, name: name, projectID: "prj_outcome", onApply: onApply}
	// A service that exists before any apply (the import tests) has bearer auth on, the default.
	initialToken := "mock-service-token-0"
	m.token = &initialToken

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode apply body: %v", err)
		}
		m.mu.Lock()
		m.applyBodies = append(m.applyBodies, body)
		n := len(m.applyBodies)
		m.mu.Unlock()
		m.applyAuth(body)

		res := m.onApply(n, body)
		if res.Status != 0 && res.Status != http.StatusAccepted {
			w.WriteHeader(res.Status)
			_, _ = fmt.Fprintf(w, `{"error": {"detail": %q}}`, res.Detail)
			return
		}
		m.setScript(res.Script...)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+m.render(res.Response)+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		serveServiceGetOrDelete(t, w, r, m.render(m.next()))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.terminated = true
		m.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/workos/deployment_infra", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {"deployment_infra": "aws"}}`)
	})
	mux.HandleFunc("/api/v2/builds/get_or_create_build_from_image_uri", m.serveGetOrCreateBuild)
	mux.HandleFunc("/api/v2/builds/", m.serveGetBuild)
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return m, server
}

// applyAuth updates the service's bearer token the way an apply with body does.
func (m *outcomeMock) applyAuth(body map[string]any) {
	if body["rollout_strategy"] == "IN_PLACE" {
		return
	}
	enabled := true
	if cfg, ok := body["config"].(map[string]any); ok {
		if access, ok := cfg["access"].(map[string]any); ok && access["use_bearer_token"] == false {
			enabled = false
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case !enabled || m.noTokens:
		m.token = nil
	case m.token == nil:
		m.tokenSeq++
		tok := fmt.Sprintf("mock-service-token-%d", m.tokenSeq)
		m.token = &tok
	}
}

// addBuild registers a build the mock reports.
func (m *outcomeMock) addBuild(id string, b outcomeBuild) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.builds == nil {
		m.builds = map[string]outcomeBuild{}
	}
	m.builds[id] = b
}

// serveGetOrCreateBuild resolves image_uri the way the backend's BYOD branch does: the newest
// build with the same image and registry secret, else a new build whose ID is not derivable from
// the URI.
func (m *outcomeMock) serveGetOrCreateBuild(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	uri, _ := body["image_uri"].(string)
	secret, _ := body["registry_login_secret"].(string)

	m.mu.Lock()
	m.getOrCreateLog = append(m.getOrCreateLog, body)
	if m.archivedURIs[uri] {
		m.mu.Unlock()
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"error": {"detail": "This name is already taken"}}`)
		return
	}
	if m.builds == nil {
		m.builds = map[string]outcomeBuild{}
	}
	id := ""
	for bid, b := range m.builds {
		if b.DockerImage == uri && b.Secret == secret {
			id = bid
		}
	}
	if id == "" {
		id = fmt.Sprintf("bld_resolved_%d", len(m.getOrCreateLog))
		m.builds[id] = outcomeBuild{DockerImage: uri, Secret: secret, TemplateName: "anon", Revision: 1}
	}
	m.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `{"result": `+m.buildJSON(id)+`}`)
}

func (m *outcomeMock) serveGetBuild(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v2/builds/")
	m.mu.Lock()
	_, ok := m.builds[id]
	m.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error": {"detail": "build not found"}}`)
		return
	}
	// The real GET answers 201.
	w.WriteHeader(http.StatusCreated)
	_, _ = fmt.Fprint(w, `{"result": `+m.buildJSON(id)+`}`)
}

func (m *outcomeMock) buildJSON(id string) string {
	m.mu.Lock()
	b := m.builds[id]
	m.mu.Unlock()
	secret := "null"
	if b.Secret != "" {
		secret = fmt.Sprintf("%q", b.Secret)
	}
	return fmt.Sprintf(`{"id": %q, "status": "succeeded", "error_message": null, "docker_image_name": %q,
		"registry_login_secret": %s, "revision": %d, "application_template": {"name": %q}}`,
		id, b.DockerImage, secret, b.Revision, b.TemplateName)
}

func (m *outcomeMock) getOrCreateCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.getOrCreateLog)
}

// setToken replaces the service's bearer token out of band (nil disables auth).
func (m *outcomeMock) setToken(tok *string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = tok
}

// setScript replaces the GET script; the first GET afterwards returns snapshots[0].
func (m *outcomeMock) setScript(snapshots ...outcomeSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.script = snapshots
	m.scriptPos = 0
}

func (m *outcomeMock) next() outcomeSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.script) == 0 {
		m.t.Errorf("GET before any script was set")
		return outcomeSnapshot{CurrentState: "RUNNING"}
	}
	s := m.script[m.scriptPos]
	if m.scriptPos < len(m.script)-1 {
		m.scriptPos++
	}
	if m.terminated {
		// The end-of-test destroy: report the termination it is waiting for.
		s.CurrentState, s.GoalState, s.Canary = "TERMINATED", "TERMINATED", nil
	}
	return s
}

func (m *outcomeMock) applyCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.applyBodies)
}

func (m *outcomeMock) applyBody(i int) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyBodies[i]
}

func outcomeVersionJSON(v outcomeVersion, state string) string {
	conns, _ := json.Marshal(v.ConnectionIDs)
	if v.ConnectionIDs == nil {
		conns = []byte("[]")
	}
	buildID := v.BuildID
	if buildID == "" {
		buildID = "bld_outcome"
	}
	routePrefix := ""
	if v.RoutePrefix != "" {
		routePrefix = fmt.Sprintf(`, "route_prefix": %q`, v.RoutePrefix)
	}
	return fmt.Sprintf(`{
		"id": %q, "created_at": "2026-01-01T00:00:00Z", "version": %q,
		"current_state": %q, "weight": 100, "build_id": %q,
		"compute_config_id": "cpt_outcome", "production_job_ids": [], "connection_ids": %s,
		"ray_serve_config": {"applications": [{"import_path": %q%s}]}
	}`, v.ID, v.Name, state, buildID, conns, v.ImportPath, routePrefix)
}

func (m *outcomeMock) render(s outcomeSnapshot) string {
	goal := s.GoalState
	if goal == "" {
		goal = "RUNNING"
	}
	primaryState := s.PrimaryState
	if primaryState == "" {
		primaryState = s.CurrentState
	}
	canary := "null"
	if s.Canary != nil {
		canary = outcomeVersionJSON(*s.Canary, s.CurrentState)
	}
	endedAt := "null"
	if s.CurrentState == "TERMINATED" {
		endedAt = `"2026-01-02T00:00:00Z"`
	}
	m.mu.Lock()
	token := "null"
	if m.token != nil {
		token = fmt.Sprintf("%q", *m.token)
	}
	m.mu.Unlock()
	return fmt.Sprintf(`{
		"id": %q, "name": %q, "project_id": %q, "cloud_id": "cld_outcome",
		"hostname": "outcome.example.com", "base_url": "https://outcome.example.com",
		"auth_token": %s, "secondary_auth_token": null,
		"current_state": %q, "goal_state": %q, "ended_at": %s,
		"creator_id": "usr_outcome", "created_at": "2026-01-01T00:00:00Z",
		"is_multi_version": false, "auto_rollout_enabled": true,
		"service_observability_urls": {},
		"primary_version": %s, "canary_version": %s, "service_status_checklist": null
	}`, m.serviceID, m.name, m.projectID, token, s.CurrentState, goal, endedAt,
		outcomeVersionJSON(s.Primary, primaryState), canary)
}

// outcomeConfig renders the service with the given import_path and extra attribute lines.
func outcomeConfig(serverURL, name, importPath, extra string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_service" "test" {
  name              = %[1]q
  project_id        = "prj_outcome"
  build_id          = "bld_outcome"
  compute_config_id = "cpt_outcome"
%[3]s
  ray_serve_config = {
    applications = [
      {
        import_path = %[2]q
      }
    ]
  }
}
`, name, importPath, extra)
}

var (
	outcomeV1 = outcomeVersion{ID: "svcver_v1", Name: "v1", ImportPath: "main:app"}
	outcomeV2 = outcomeVersion{ID: "svcver_v2", Name: "v2", ImportPath: "main:app_v2"}
	outcomeV3 = outcomeVersion{ID: "svcver_v3", Name: "v3", ImportPath: "main:app_v2"}
)

// outcomeCreate is the create apply every test starts with: v1 as primary, then RUNNING.
func outcomeCreate() outcomeApplyResult {
	return outcomeCreateOf(outcomeV1)
}

func outcomeCreateOf(v outcomeVersion) outcomeApplyResult {
	return outcomeApplyResult{
		Response: outcomeSnapshot{CurrentState: "STARTING", Primary: v},
		Script:   []outcomeSnapshot{{CurrentState: "RUNNING", Primary: v}},
	}
}

// enriched is v as the backend may store it: with a field the config never set. A test that
// must plan empty against such a version proves state was not refreshed from it.
func enriched(v outcomeVersion) outcomeVersion {
	v.RoutePrefix = "/"
	return v
}

// outcomeRollout is a successful update apply: canary `to` rolls out, then becomes primary.
func outcomeRollout(from, to outcomeVersion) outcomeApplyResult {
	return outcomeApplyResult{
		Response: outcomeSnapshot{CurrentState: "ROLLING_OUT", Primary: from, Canary: &to},
		Script: []outcomeSnapshot{
			{CurrentState: "ROLLING_OUT", Primary: from, Canary: &to},
			{CurrentState: "RUNNING", Primary: to},
		},
	}
}

func expectOutcomeApplyCount(m *outcomeMock, want int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := m.applyCount(); got != want {
			return fmt.Errorf("PUT /services-v2/apply called %d time(s), want %d", got, want)
		}
		return nil
	}
}

// TestAccServiceResource_RolledBackRolloutFails (B1): a rollout that the backend auto-rolls back
// must fail the apply, naming both versions and where to find the reason, and leave the change
// pending so the next plan shows it again and a re-apply retries it.
//
// The script is the sequence observed on a real backend for a broken import_path: the rollback
// completes (canary gone, old version primary) while the service still reports ROLLING_BACK,
// then it passes through UNHEALTHY before RUNNING, and no GET field carries the failure reason.
func TestAccServiceResource_RolledBackRolloutFails(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "outcome-rolled-back"
	m, server := newOutcomeMock(t, "svc_outcome_rolled_back", name, func(n int, _ map[string]any) outcomeApplyResult {
		switch n {
		case 1:
			return outcomeCreate()
		case 2:
			return outcomeApplyResult{
				Response: outcomeSnapshot{CurrentState: "ROLLING_OUT", Primary: outcomeV1, Canary: &outcomeV2},
				Script: []outcomeSnapshot{
					{CurrentState: "ROLLING_OUT", Primary: outcomeV1, Canary: &outcomeV2},
					{CurrentState: "ROLLING_BACK", Primary: outcomeV1},
					{CurrentState: "UNHEALTHY", Primary: outcomeV1},
					{CurrentState: "RUNNING", Primary: outcomeV1},
				},
			}
		default:
			return outcomeRollout(outcomeV1, outcomeV3)
		}
	})
	base := outcomeConfig(server.URL, name, "main:app", "")
	updated := outcomeConfig(server.URL, name, "main:app_v2", "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config: updated,
				ExpectError: regexp.MustCompile(`(?s)Rollout Rolled Back.*"v2" \(svcver_v2\).*` +
					`"v1" \(svcver_v1\).*service events`),
			},
			{
				// The failed change is still pending.
				Config:             updated,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				// Re-applying retries: a third apply rolls out v3 and converges.
				Config: updated,
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 3),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.id", "svcver_v3"),
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccServiceResource_ApplyFailureKeepsPriorConfig (B2): when PUT /apply itself fails (here
// the traced 409 for a rollout already in progress), state must keep the prior deploy fields so
// the next plan shows the change again.
func TestAccServiceResource_ApplyFailureKeepsPriorConfig(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "outcome-apply-fails"
	m, server := newOutcomeMock(t, "svc_outcome_apply_fails", name, func(n int, _ map[string]any) outcomeApplyResult {
		if n == 1 {
			return outcomeCreate()
		}
		return outcomeApplyResult{
			Status: http.StatusConflict,
			Detail: "There is already a rollout to v9 that is currently in progress.",
		}
	})
	base := outcomeConfig(server.URL, name, "main:app", "")
	updated := outcomeConfig(server.URL, name, "main:app_v2", "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config:      updated,
				ExpectError: regexp.MustCompile(`(?s)apply service update.*already a rollout`),
			},
			{
				Config:             updated,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})

	if got := m.applyCount(); got != 2 {
		t.Errorf("apply called %d times, want 2", got)
	}
}

// testAccServiceInterruptedRollout runs B3: an update whose wait times out while the canary is
// still rolling out. While the canary is present the plan must be empty; once the rollout
// settles to `settled`, the plan must be empty if the new version won and non-empty if it was
// rolled back.
func testAccServiceInterruptedRollout(t *testing.T, name string, settled outcomeSnapshot, wantDiff bool) {
	t.Helper()
	m, server := newOutcomeMock(t, "svc_"+name, name, func(n int, _ map[string]any) outcomeApplyResult {
		if n == 1 {
			return outcomeCreate()
		}
		// Never settles on its own: the update wait times out mid-rollout.
		inFlight := outcomeSnapshot{CurrentState: "ROLLING_OUT", Primary: outcomeV1, Canary: &outcomeV2}
		return outcomeApplyResult{Response: inFlight, Script: []outcomeSnapshot{inFlight}}
	})
	const timeouts = `  timeouts {
    update = "1s"
  }`
	base := outcomeConfig(server.URL, name, "main:app", timeouts)
	updated := outcomeConfig(server.URL, name, "main:app_v2", timeouts)

	settledCheck := plancheck.ExpectEmptyPlan()
	if wantDiff {
		settledCheck = plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config:      updated,
				ExpectError: regexp.MustCompile(`(?s)wait for service rollout.*timed out`),
			},
			{
				// Canary still present: no drift.
				Config:   updated,
				PlanOnly: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				PreConfig:          func() { m.setScript(settled) },
				Config:             updated,
				PlanOnly:           true,
				ExpectNonEmptyPlan: wantDiff,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{settledCheck},
				},
			},
		},
	})
}

// TestAccServiceResource_InterruptedRolloutSucceededResource (B3, success): the rollout the
// provider stopped waiting for completes server-side; the next plan is empty. The live v2 is
// enriched, so a refresh from it (instead of recognizing the pending version) would show a diff.
func TestAccServiceResource_InterruptedRolloutSucceededResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccServiceInterruptedRollout(t, "outcome-interrupted-ok",
		outcomeSnapshot{CurrentState: "RUNNING", Primary: enriched(outcomeV2)}, false)
}

// TestAccServiceResource_InterruptedRolloutRolledBackResource (B3, rollback): the rollout the
// provider stopped waiting for is rolled back server-side; the next plan shows the change again.
func TestAccServiceResource_InterruptedRolloutRolledBackResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccServiceInterruptedRollout(t, "outcome-interrupted-rb",
		outcomeSnapshot{CurrentState: "RUNNING", Primary: outcomeV1}, true)
}

// TestAccServiceResource_OutOfBandDeployShowsDiff (B4): a version Terraform did not deploy
// becoming primary (CLI deploy or rollback) shows a ray_serve_config diff refreshed from the live
// primary; the steady state before it plans empty (B5).
func TestAccServiceResource_OutOfBandDeployShowsDiff(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "outcome-out-of-band"
	outOfBand := outcomeVersion{ID: "svcver_cli", Name: "cli-deploy", ImportPath: "main:from_cli"}
	m, server := newOutcomeMock(t, "svc_outcome_oob", name, func(n int, _ map[string]any) outcomeApplyResult {
		if n == 1 {
			return outcomeCreate()
		}
		return outcomeRollout(outOfBand, outcomeV3)
	})
	base := outcomeConfig(server.URL, name, "main:app", "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: base,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				PreConfig:          func() { m.setScript(outcomeSnapshot{CurrentState: "RUNNING", Primary: outOfBand}) },
				Config:             base,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				// The refresh records the live config; applying redeploys the configured one.
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check: resource.TestCheckResourceAttr("anyscale_service.test",
					"ray_serve_config.applications.0.import_path", "main:from_cli"),
			},
			{
				Config: base,
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 2),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.id", "svcver_v3"),
					resource.TestCheckResourceAttr("anyscale_service.test",
						"ray_serve_config.applications.0.import_path", "main:app"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// privateStrippingServer simulates a provider version that predates version tracking: it
// drops the private state every apply and read would otherwise persist.
type privateStrippingServer struct {
	tfprotov6.ProviderServer
}

func (s privateStrippingServer) ApplyResourceChange(ctx context.Context, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	resp, err := s.ProviderServer.ApplyResourceChange(ctx, req)
	if resp != nil {
		resp.Private = nil
	}
	return resp, err
}

func (s privateStrippingServer) ReadResource(ctx context.Context, req *tfprotov6.ReadResourceRequest) (*tfprotov6.ReadResourceResponse, error) {
	resp, err := s.ProviderServer.ReadResource(ctx, req)
	if resp != nil {
		resp.Private = nil
	}
	return resp, err
}

// TestAccServiceResource_LegacyStateSeedsTrackingResource (B5): state written by a provider
// without version tracking plans empty on first read with this one, and the seeded tracking
// then detects a later out-of-band deploy.
// The live v1 is enriched, so treating missing tracking as drift would show a diff.
func TestAccServiceResource_LegacyStateSeedsTrackingResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "outcome-legacy"
	outOfBand := outcomeVersion{ID: "svcver_cli", Name: "cli-deploy", ImportPath: "main:from_cli"}
	m, server := newOutcomeMock(t, "svc_outcome_legacy", name, func(int, map[string]any) outcomeApplyResult {
		return outcomeCreateOf(enriched(outcomeV1))
	})
	base := outcomeConfig(server.URL, name, "main:app", "")
	legacy := map[string]func() (tfprotov6.ProviderServer, error){
		"anyscale": func() (tfprotov6.ProviderServer, error) {
			s, err := ProtoV6ProviderFactories["anyscale"]()
			return privateStrippingServer{s}, err
		},
	}

	resource.Test(t, resource.TestCase{
		Steps: []resource.TestStep{
			{ProtoV6ProviderFactories: legacy, Config: base},
			{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Config:                   base,
				PlanOnly:                 true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// An empty-plan apply persists the refreshed state, seeding the tracking.
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Config:                   base,
				Check:                    expectOutcomeApplyCount(m, 1),
			},
			{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				PreConfig:                func() { m.setScript(outcomeSnapshot{CurrentState: "RUNNING", Primary: outOfBand}) },
				Config:                   base,
				PlanOnly:                 true,
				ExpectNonEmptyPlan:       true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				// Back on Terraform's version so the end-of-test destroy sees a steady service.
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				PreConfig:                func() { m.setScript(outcomeSnapshot{CurrentState: "RUNNING", Primary: enriched(outcomeV1)}) },
				Config:                   base,
				PlanOnly:                 true,
			},
		},
	})
}

// TestAccServiceResource_ImportSeedsTrackingResource (B5): a cold import plans empty against a
// matching config, and the seeded tracking detects a later out-of-band deploy.
func TestAccServiceResource_ImportSeedsTrackingResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "outcome-import"
	outOfBand := outcomeVersion{ID: "svcver_cli", Name: "cli-deploy", ImportPath: "main:from_cli"}
	m, server := newOutcomeMock(t, "svc_outcome_import", name, func(int, map[string]any) outcomeApplyResult {
		t.Errorf("unexpected apply after import")
		return outcomeCreate()
	})
	m.setScript(outcomeSnapshot{CurrentState: "RUNNING", Primary: outcomeV1})
	base := outcomeConfig(server.URL, name, "main:app", "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_service.test",
				ImportState:        true,
				ImportStateId:      "svc_outcome_import",
				ImportStatePersist: true,
				Config:             base,
			},
			{
				// rollout_strategy's default is the accepted post-import residual
				// (see resource_service_deploy_strategy_acc_test.go); apply records it locally.
				Config: base,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				PreConfig:          func() { m.setScript(outcomeSnapshot{CurrentState: "RUNNING", Primary: outOfBand}) },
				Config:             base,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				PreConfig: func() { m.setScript(outcomeSnapshot{CurrentState: "RUNNING", Primary: outcomeV1}) },
				Config:    base,
				PlanOnly:  true,
			},
		},
	})
}

// TestAccServiceResource_TerminatedServiceRestartsResource (B6): a service found terminated
// while still in config plans an in-place update; apply restarts the same id through PUT /apply
// (standard rollout, live connections carried explicitly) and waits for RUNNING on a new version.
func TestAccServiceResource_TerminatedServiceRestartsResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "outcome-restart"
	v1 := outcomeVersion{ID: "svcver_v1", Name: "v1", ImportPath: "main:app", ConnectionIDs: []string{"conn_a"}}
	v2 := outcomeVersion{ID: "svcver_v2", Name: "v2", ImportPath: "main:app", ConnectionIDs: []string{"conn_a"}}
	m, server := newOutcomeMock(t, "svc_outcome_restart", name, func(n int, _ map[string]any) outcomeApplyResult {
		if n == 1 {
			return outcomeApplyResult{
				Response: outcomeSnapshot{CurrentState: "STARTING", Primary: v1},
				Script:   []outcomeSnapshot{{CurrentState: "RUNNING", Primary: v1}},
			}
		}
		return outcomeApplyResult{
			Response: outcomeSnapshot{CurrentState: "STARTING", Primary: v2},
			Script: []outcomeSnapshot{
				{CurrentState: "STARTING", Primary: v2},
				// Observed on a real backend: UNHEALTHY while the new version starts, and for a
				// while after it is RUNNING (the health probe's jail threshold).
				{CurrentState: "UNHEALTHY", PrimaryState: "STARTING", Primary: v2},
				{CurrentState: "UNHEALTHY", PrimaryState: "RUNNING", Primary: v2},
				{CurrentState: "RUNNING", Primary: v2},
			},
		}
	})
	// IN_PLACE is configured on purpose: the restart must still send a standard rollout, which is
	// the only strategy the backend accepts for a terminated service.
	base := outcomeConfig(server.URL, name, "main:app", `  rollout_strategy = "IN_PLACE"`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				PreConfig: func() {
					m.setScript(outcomeSnapshot{CurrentState: "TERMINATED", GoalState: "TERMINATED", Primary: v1})
				},
				Config: base,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 2),
					resource.TestCheckResourceAttr("anyscale_service.test", "id", "svc_outcome_restart"),
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					resource.TestCheckResourceAttr("anyscale_service.test", "goal_state", "RUNNING"),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.id", "svcver_v2"),
					resource.TestCheckNoResourceAttr("anyscale_service.test", "connection_ids"),
					func(*terraform.State) error {
						body := m.applyBody(1)
						if got := body["rollout_strategy"]; got != "ROLLOUT" {
							return fmt.Errorf("restart sent rollout_strategy %v, want ROLLOUT", got)
						}
						conns, _ := json.Marshal(body["connection_ids"])
						if string(conns) != `["conn_a"]` {
							return fmt.Errorf("restart sent connection_ids %s, want the live [\"conn_a\"]", conns)
						}
						return nil
					},
				),
			},
		},
	})
}
