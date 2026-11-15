package acctest

import (
	"strings"
	"testing"
)

// terraform destroy plans the configuration first, in a pass the provider
// cannot tell apart from an ordinary plan; the scheduler-config plan-time
// validate used to error there, blocking destroy. The contract these tests
// pin: the check runs only when the document it validates would change (an
// unchanged resource - which is what nearly every destroy plans first - makes
// no plan-time API call), a validate refusal on a changed config warns with a
// forecast at plan, and the apply still refuses server-side with the same
// rejection.

// Destroy after flipping the validate endpoint to refuse: the unchanged
// config means the check never runs (the skip), so the destroy proceeds
// silently.
func TestAccSchedulerConfigResource_DestroySurvivesRefusedValidate_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{})
	defer server.Close()
	run := cliDevWorkspace(t, schedulerReadImportHCL(server.URL))

	pmRunOK(t, run, "apply", "-auto-approve", "-json")
	baseline := mock.ValidateHits()
	mock.SetValidateResponse(422, `{"error":{"detail":"queue does not exist"}}`)

	if _, out, err := run("destroy", "-auto-approve", "-json"); err != nil {
		t.Fatalf("terraform destroy must not be blocked by a plan-time validate refusal; it failed with:\n%s", out)
	}
	if got := mock.ValidateHits(); got != baseline {
		t.Errorf("destroy's plan pass re-validated an unchanged config: validate hits went from %d to %d (the no-change skip should have left it)", baseline, got)
	}
	if _, stateOut, err := run("state", "list"); err != nil {
		t.Fatalf("terraform state list: %v\n%s", err, stateOut)
	} else if strings.Contains(stateOut, "anyscale_scheduler_config.test") {
		t.Errorf("resource still in state after destroy:\n%s", stateOut)
	}
}

// A validate refusal on a CHANGED config is a plan warning (the forecast), and
// the apply still refuses: the mock's apply endpoint answers the POST with
// the same server-side rejection, so nothing lands.
func TestAccSchedulerConfigResource_ApplyStillRefusesAfterPlanForecast_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{})
	defer server.Close()

	plain := schedulerReadImportHCL(server.URL)
	withQueue := testAccProviderBlock(server.URL) + `
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    { name = "default" },
  ]

  resource_queues = [
    { name = "gpu" }
  ]
}
`
	run, rewrite := cliDevWorkspaceEditable(t, plain)
	pmRunOK(t, run, "apply", "-auto-approve", "-json")

	// Refuse both the plan-time check and the apply, server-side.
	mock.SetValidateResponse(422, `{"error":{"detail":"flavor does not cover its group"}}`)
	mock.SetApplyResponse(422, `{"error":{"detail":"flavor does not cover its group"}}`)
	rewrite(withQueue)

	planLines := pmRunOK(t, run, "plan", "-json")
	found := false
	for _, l := range planLines {
		if l.Type == "diagnostic" && l.Diagnostic.Severity == "warning" &&
			l.Diagnostic.Summary == "Invalid Anyscale Scheduler Configuration" {
			found = true
			for _, want := range []string{"If applied, this change fails.", "flavor does not cover its group", "the destroy itself is not affected"} {
				if !strings.Contains(l.Diagnostic.Detail, want) {
					t.Errorf("forecast warning lacks %q: %s", want, l.Diagnostic.Detail)
				}
			}
		}
	}
	if !found {
		t.Errorf("the plan must warn with the forecast; got diagnostics: %v", cliDiagnostics(planLines, ""))
	}

	writesBefore := mock.WriteCount()
	applyLines, out, err := run("apply", "-auto-approve", "-json")
	if err == nil {
		t.Fatalf("the apply must still refuse with the server-side rejection; got success:\n%s", out)
	}
	found = false
	for _, l := range applyLines {
		if l.Type != "diagnostic" || l.Diagnostic.Severity != "error" {
			continue
		}
		if strings.Contains(l.Diagnostic.Summary+l.Diagnostic.Detail, "Unable to Apply Anyscale Scheduler Config") &&
			strings.Contains(l.Diagnostic.Summary+l.Diagnostic.Detail, "flavor does not cover its group") &&
			!strings.Contains(l.Diagnostic.Detail, "If applied") {
			found = true
		}
	}
	if !found {
		t.Errorf("the apply must error with the real, non-forecast refusal; got: %v\n%s", cliDiagnostics(applyLines, ""), out)
	}
	if got := mock.WriteCount(); got != writesBefore {
		t.Errorf("a refused apply landed a write: write count went from %d to %d", writesBefore, got)
	}
}
