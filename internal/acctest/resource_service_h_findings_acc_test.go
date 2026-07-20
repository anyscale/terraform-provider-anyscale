// This file is the mock-server regression suite for CONTRACT_anyscale_service_resource.md
// section H (tfp-architect's semantic-pass findings against tfp-forge's resource_service.go).
// Each test is a deliberate mutation-proof pairing with a real, currently-shipping bug or gap:
// written to FAIL against the code as it stands when this file was authored, expected to PASS
// once forge's corresponding fix lands - see each test's doc comment for the specific finding.
package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// serviceFindingsJSON builds a realistic, minimally-complete service JSON body (the `result` of
// GET/apply) for the section-H regression tests below - current_state drives most of the
// interesting behavior these tests exercise, so it is the one field callers vary.
func serviceFindingsJSON(id, name, projectID, currentState string) string {
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
			"ray_serve_config": {}
		}
	}`, id, name, projectID, currentState)
}

func testAccServiceFindingsConfig(serverURL, name, projectID string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_service" "test" {
  name              = %[1]q
  project_id        = %[2]q
  build_id          = "bld_findings"
  compute_config_id = "cpt_findings"
  ray_serve_config = {
    applications = []
  }
}
`, name, projectID)
}

// emptyTagsBody / oneTagBody are the two GET /api/v2/tags/resource response shapes the H4 test
// toggles between.
const emptyTagsBody = `{"result": {"tags": []}}`

func oneTagBody(key, value string) string {
	return fmt.Sprintf(`{"result": {"tags": [{"key": %q, "value": %q}]}}`, key, value)
}

// serviceFindingsCurrentState reports "TERMINATED" once terminated is set, else "RUNNING" - used
// by every test below's GET handler so the resource.Test-automatic end-of-test destroy's
// wait-for-TERMINATED loop resolves on its first or second poll instead of hanging for the real
// rollout_timeout default (30m) against a mock that never reflects termination.
func serviceFindingsCurrentState(terminated *int32) string {
	if atomic.LoadInt32(terminated) == 1 {
		return "TERMINATED"
	}
	return "RUNNING"
}

// serveServiceGetOrDelete handles the two methods every test's services-v2/{id} mux entry needs
// to answer beyond just GET: DELETE is the automatic end-of-test destroy's final call, once the
// termination wait resolves, and must succeed (204) - not fall through to whatever a bare
// "always 200" handler would do, which would fail the destroy on a real status-mismatch error.
func serveServiceGetOrDelete(t *testing.T, w http.ResponseWriter, r *http.Request, resultBody string) {
	t.Helper()
	switch r.Method {
	case http.MethodGet:
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": `+resultBody+`}`)
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		t.Errorf("unexpected method %s on services-v2 id path", r.Method)
	}
}

// TestAccServiceResource_PlainCreateSucceeds is the permanent regression guard for contract
// section P0: a bare, minimal apply of anyscale_service - no H-finding-specific mocking, just
// the required attributes - must succeed with zero diagnostics. This is deliberately the
// simplest possible test in this file. Before the P0 fix, EVERY create failed here with a
// framework-level "Value Conversion Error: Received unknown value, however the target type
// cannot handle unknown values" while decoding the plan's Computed-only nested-object fields
// (service_observability_urls / primary_version / canary_version / service_status_checklist) -
// this test is what would have caught that on day one, so it stays in the committed suite
// rather than living only as a scratch repro.
func TestAccServiceResource_PlainCreateSucceeds(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_p0_plain_create"
	var terminated int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceFindingsJSON(serviceID, "p0-plain-create", "prj_p0", "RUNNING")+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		serveServiceGetOrDelete(t, w, r, serviceFindingsJSON(serviceID, "p0-plain-create", "prj_p0", serviceFindingsCurrentState(&terminated)))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		// Every test's TestCase issues a REAL destroy automatically at the end, which waits for
		// current_state==TERMINATED before the DELETE call - so GET must reflect termination
		// once this fires, or that wait polls a state that can never arrive and hangs for the
		// real rollout_timeout default (30m), blowing well past this test's own timeout.
		atomic.StoreInt32(&terminated, 1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	config := testAccServiceFindingsConfig(server.URL, "p0-plain-create", "prj_p0")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "id", serviceID),
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					resource.TestCheckResourceAttr("anyscale_service.test", "hostname", "findings.example.com"),
				),
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// TestAccServiceResource_DeleteAlreadyGone is the H1 regression: terminate returning 404 (the
// service was already terminated+deleted out-of-band) must make Delete succeed cleanly, not
// fall through to the termination wait. Today, DoRequestRaw's terminate call lists
// http.StatusNotFound as an ACCEPTED status, so a real 404 produces err=nil - the
// strings.Contains(err.Error(), "404") guard right after it is dead code, unreachable, and
// control falls into waitForServiceState, which GETs the now-absent service, 404s for real
// this time (GET only accepts 200), and fails the destroy. Proven here by tracking whether the
// service-by-id endpoint is ever hit AFTER terminate fires - the fixed behavior must return
// immediately without ever needing another GET; the buggy behavior falls through into exactly
// one such GET, which then errors and fails the whole resource.Test (a leaked destroy diagnostic
// fails the test's own automatic cleanup destroy).
func TestAccServiceResource_DeleteAlreadyGone(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_h1_already_gone"
	var terminateCalled int32
	var getsAfterTerminate int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceFindingsJSON(serviceID, "h1-already-gone", "prj_h1", "RUNNING")+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if atomic.LoadInt32(&terminateCalled) == 1 {
				// Once terminate has fired, the mock treats the service as genuinely gone from
				// GET's point of view too - consistent with the already-gone scenario, and
				// critically, this makes a regression (falling through to the wait) fail FAST
				// (getServiceByID errors on the first poll, since GET only accepts 200) rather
				// than hang for the real rollout_timeout default (30m) waiting on a state that
				// can never arrive, which would otherwise blow well past this test's own timeout
				// instead of failing informatively.
				atomic.AddInt32(&getsAfterTerminate, 1)
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, `{"error": {"detail": "Service not found"}}`)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"result": `+serviceFindingsJSON(serviceID, "h1-already-gone", "prj_h1", "RUNNING")+`}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/services-v2/%s", r.Method, serviceID)
		}
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&terminateCalled, 1)
		// The already-gone scenario: someone else terminated and deleted this service
		// out-of-band, so the backend has no record left to terminate.
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error": {"detail": "Service not found"}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	config := testAccServiceFindingsConfig(server.URL, "h1-already-gone", "prj_h1")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
			},
			// No explicit destroy step: resource.Test issues the real destroy automatically at
			// the end of the TestCase, and fails the overall test if that destroy's Delete()
			// returns a diagnostic error - exactly the signal this finding needs.
		},
	})

	if got := atomic.LoadInt32(&getsAfterTerminate); got != 0 {
		t.Errorf("GET /services-v2/%s was called %d time(s) AFTER terminate - the fixed Delete "+
			"must return immediately on a terminate-404 without ever polling again; falling "+
			"through to the wait (today's bug) is exactly what this proves did NOT happen if this fails",
			serviceID, got)
	}
}

// TestAccServiceResource_UpdateSkipsApplyWhenOnlyTimeoutChanges is the H2 regression:
// rollout_timeout is a purely client-local wait knob (never sent to or read from the API - see
// its schema MarkdownDescription), so changing ONLY it must not trigger a new PUT /apply or
// rollout wait against an already-healthy, unrelated-in-every-other-way service. Today, Update()
// unconditionally applies+waits on every call regardless of which field changed, so this asserts
// the apply endpoint is hit exactly once (from Create) even after a second apply with a
// different rollout_timeout.
func TestAccServiceResource_UpdateSkipsApplyWhenOnlyTimeoutChanges(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_h2_timeout_only"
	var applyCallCount int32
	var terminated int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&applyCallCount, 1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceFindingsJSON(serviceID, "h2-timeout-only", "prj_h2", "RUNNING")+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		serveServiceGetOrDelete(t, w, r, serviceFindingsJSON(serviceID, "h2-timeout-only", "prj_h2", serviceFindingsCurrentState(&terminated)))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&terminated, 1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	baseConfig := testAccServiceFindingsConfig(server.URL, "h2-timeout-only", "prj_h2")
	timeoutOnlyConfig := testAccProviderBlock(server.URL) + `
resource "anyscale_service" "test" {
  name              = "h2-timeout-only"
  project_id        = "prj_h2"
  build_id          = "bld_findings"
  compute_config_id = "cpt_findings"
  ray_serve_config = {
    applications = []
  }
  rollout_timeout = "45m"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: baseConfig,
				Check:  resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
			},
			{
				Config: timeoutOnlyConfig,
				// The step succeeding at all (no "provider produced inconsistent result after
				// apply" / unknown-value error) is itself part of what this proves - that is
				// exactly the shape contract §H5 broke: a no-deploy Update branch that persists
				// state without populating the computed outputs leaves them Unknown post-apply.
				// The explicit checks below additionally pin down that current_state/hostname
				// specifically carry real, known values (not just "the step didn't error").
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					resource.TestCheckResourceAttr("anyscale_service.test", "hostname", "findings.example.com"),
					func(s *terraform.State) error {
						if got := atomic.LoadInt32(&applyCallCount); got != 1 {
							return fmt.Errorf("apply was called %d time(s) after a rollout_timeout-only "+
								"change (want exactly 1, from Create only) - Update must not re-apply/"+
								"re-wait a running service just because a client-local wait knob changed", got)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccServiceResource_ImportPopulatesProjectID is the H3 regression: Read never refreshes
// project_id (only name/description/build_id/compute_config_id), and ImportState seeds only id +
// ray_serve_config - so after import, the automatic post-import Read leaves project_id null.
// Because project_id is Optional+Computed+RequiresReplace, a user who then writes the real
// project_id into config to match what they imported would see null -> value, which
// RequiresReplace reads as "destroy and recreate" - potentially a production service. Proven by
// importing a service created with a known project_id and asserting the imported state's
// project_id is populated with that real value, not empty/null.
func TestAccServiceResource_ImportPopulatesProjectID(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_h3_import"
	const wantProjectID = "prj_h3_real_project"
	var terminated int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceFindingsJSON(serviceID, "h3-import", wantProjectID, "RUNNING")+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		serveServiceGetOrDelete(t, w, r, serviceFindingsJSON(serviceID, "h3-import", wantProjectID, serviceFindingsCurrentState(&terminated)))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&terminated, 1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	config := testAccServiceFindingsConfig(server.URL, "h3-import", wantProjectID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("anyscale_service.test", "project_id", wantProjectID),
			},
			{
				Config:        config,
				ResourceName:  "anyscale_service.test",
				ImportState:   true,
				ImportStateId: serviceID,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported resource, got %d", len(states))
					}
					if got := states[0].Attributes["project_id"]; got != wantProjectID {
						return fmt.Errorf("project_id after import = %q, want %q - Read must refresh "+
							"project_id (today it does not), or a post-import config write of the real "+
							"project_id would spuriously RequiresReplace this service", got, wantProjectID)
					}
					return nil
				},
			},
		},
	})
}

// TestAccServiceResource_TagsFullRemovalDetected is the H4 regression: refreshServiceTagsIntoModel
// leaves the tags model untouched whenever the fetch comes back empty, to preserve the
// null-vs-empty distinction (never-configured vs explicitly-{}) - but that also means a full,
// out-of-band removal of every tag is never detected on Read, since "was {a:1}, fetch now empty"
// looks identical to "always been empty" from that function's point of view. Proven with a
// PlanOnly refresh step: after tags go from {a:"1"} to an empty fetch, a fixed Read must write the
// model to a real (non-null) empty map, which config (still asking for tags={a:"1"}) now diffs
// against - a non-empty plan. Buggy code leaves the stale {a:"1"} in state, which matches config,
// so no diff is detected and the plan comes back empty.
func TestAccServiceResource_TagsFullRemovalDetected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_h4_tags_removed"
	var tagsBody atomic.Value
	tagsBody.Store(oneTagBody("a", "1"))
	var terminated int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceFindingsJSON(serviceID, "h4-tags-removed", "prj_h4", "RUNNING")+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		serveServiceGetOrDelete(t, w, r, serviceFindingsJSON(serviceID, "h4-tags-removed", "prj_h4", serviceFindingsCurrentState(&terminated)))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&terminated, 1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, tagsBody.Load().(string))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	config := testAccProviderBlock(server.URL) + `
resource "anyscale_service" "test" {
  name              = "h4-tags-removed"
  project_id        = "prj_h4"
  build_id          = "bld_findings"
  compute_config_id = "cpt_findings"
  ray_serve_config = {
    applications = []
  }
  tags = {
    a = "1"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("anyscale_service.test", "tags.a", "1"),
			},
			{
				PreConfig: func() {
					// Simulate an out-of-band removal of every tag between the create above and
					// this refresh - the mock now reports zero tags for the same service.
					tagsBody.Store(emptyTagsBody)
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				Check:              resource.TestCheckResourceAttr("anyscale_service.test", "tags.%", "0"),
			},
		},
	})
}

// TestAccServiceResource_CreateWaitTimeoutPreservesID is the contract §G2 regression: orphan
// prevention. PUT /apply creates the service (its id is known) BEFORE the rollout wait begins -
// if Create only wrote state on a successful wait, a service that gets created remotely but then
// times out waiting for RUNNING would be orphaned: it exists in Anyscale, but Terraform has no
// record of it to destroy or reconcile on the next apply. The fix is to persist id (and the rest
// of the computed fields) via resp.State.Set BEFORE the wait, so a subsequent wait failure still
// leaves a recoverable record. Proven here with a service that never leaves STARTING and a short
// rollout_timeout: the apply is expected to error (the wait times out), but the resource's id
// must still be checkable afterward - proving it landed in state despite the error, not just that
// the error occurred.
func TestAccServiceResource_CreateWaitTimeoutPreservesID(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_g2_orphan_prevention"
	var terminated int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceFindingsJSON(serviceID, "g2-orphan-prevention", "prj_g2", "STARTING")+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		// Never reaches RUNNING on its own - forces Create's wait to time out. Once terminate
		// fires (the automatic end-of-test destroy, cleaning up the orphan-preventing record this
		// test proves got persisted), it does transition to TERMINATED so that destroy itself
		// completes quickly rather than timing out a second time.
		state := "STARTING"
		if atomic.LoadInt32(&terminated) == 1 {
			state = "TERMINATED"
		}
		serveServiceGetOrDelete(t, w, r, serviceFindingsJSON(serviceID, "g2-orphan-prevention", "prj_g2", state))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&terminated, 1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	config := testAccProviderBlock(server.URL) + `
resource "anyscale_service" "test" {
  name              = "g2-orphan-prevention"
  project_id        = "prj_g2"
  build_id          = "bld_findings"
  compute_config_id = "cpt_findings"
  ray_serve_config = {
    applications = []
  }
  rollout_timeout = "2s"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)wait for service rollout.*timed out`),
				Check:       resource.TestCheckResourceAttr("anyscale_service.test", "id", serviceID),
			},
		},
	})
}
