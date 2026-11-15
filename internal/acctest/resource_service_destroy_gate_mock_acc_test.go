package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// terraform destroy plans the configuration first, in a pass the provider
// cannot tell apart from an ordinary plan; the service cloud-match guard used
// to error there, blocking destroy. These tests pin: a mismatch forecast at
// plan (a plan in the mismatching state must warn, not error), the destroy
// proceeds and cleans up, and the apply path refuses a mismatched pair before
// any write (checked in TestAccServiceResource_CloudMatchRejectsMismatch).

// Apply a matching pair, flip the project onto a different cloud out of band,
// then: the plan warns with the forecast, and terraform destroy still exits
// zero and removes the resource.
func TestAccServiceResource_DestroySurvivesCloudMismatch_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const serviceID = "svc_destroygate"
	var mismatch atomic.Bool
	var terminated atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceRolloutJSON(serviceID, "destroy-gate",
			"prj_gate", "STARTING", "svcver_v1", "v1")+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/compute_templates/cpt_gate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, serviceCloudMatchComputeConfigJSON("cpt_gate", "cld_alpha"))
	})
	mux.HandleFunc("/api/v2/projects/prj_gate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		parent := "cld_alpha"
		if mismatch.Load() {
			parent = "cld_beta"
		}
		_, _ = fmt.Fprint(w, serviceCloudMatchProjectJSON("prj_gate", &parent))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		state := "RUNNING"
		if terminated.Load() {
			state = "TERMINATED"
		}
		serveServiceGetOrDelete(t, w, r, serviceRolloutJSON(serviceID, "destroy-gate",
			"prj_gate", state, "svcver_v1", "v1"))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		terminated.Store(true)
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
  name              = "destroy-gate"
  project_id        = "prj_gate"
  build_id          = "bld_findings"
  compute_config_id = "cpt_gate"
  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
      }
    ]
  }
}
`
	run := cliDevWorkspace(t, config)
	pmRunOK(t, run, "apply", "-auto-approve", "-json")

	// Same ids, whose clouds now disagree out of band: unchanged ids would
	// skip the check entirely, so pin the skip too, then flip the project's
	// cloud away to manufacture the mismatch the plan pass will see.
	if planLines := pmRunOK(t, run, "plan", "-json"); len(pmProviderWarnings(planLines)) != 0 {
		t.Errorf("an unchanged re-plan must not warn; got %v", pmProviderWarnings(planLines))
	}
	mismatch.Store(true)

	planLines := pmRunOK(t, run, "plan", "-json")
	found := false
	for _, l := range planLines {
		if l.Type == "diagnostic" && l.Diagnostic.Severity == "warning" &&
			strings.Contains(l.Diagnostic.Summary, "Project/Compute Config Cloud Mismatch") {
			found = true
			for _, want := range []string{"If applied, this change fails.", "cld_alpha", "cld_beta", "the destroy itself is not affected"} {
				if !strings.Contains(l.Diagnostic.Detail, want) {
					t.Errorf("forecast detail lacks %q: %s", want, l.Diagnostic.Detail)
				}
			}
		}
	}
	if !found {
		t.Errorf("the plan must warn with the mismatch forecast; got diagnostics: %v", cliDiagnostics(planLines, ""))
	}

	if _, out, err := run("destroy", "-auto-approve", "-json"); err != nil {
		t.Fatalf("terraform destroy must survive a plan-time cloud-match forecast; it failed:\n%s", out)
	}
	if _, stateOut, err := run("state", "list"); err != nil {
		t.Fatalf("terraform state list: %v\n%s", err, stateOut)
	} else if strings.Contains(stateOut, "anyscale_service.test") {
		t.Errorf("resource still in state after destroy:\n%s", stateOut)
	}
	if !terminated.Load() {
		t.Error("destroy never terminated the service")
	}
}
