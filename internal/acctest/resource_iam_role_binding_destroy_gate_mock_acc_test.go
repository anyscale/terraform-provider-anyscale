package acctest

import (
	"strings"
	"testing"
)

// terraform destroy first plans the configuration in normal mode, and the
// provider cannot tell that pass apart from an ordinary plan - so a plan-time
// API check that errors there blocks the destroy of a perfectly good
// configuration. The contract these tests pin:
//
//   - a check runs only when something it checks would change, so an
//     unchanged resource makes no plan-time API call and nearly every destroy
//     skips the check entirely;
//   - a refusal the API derives from live data warns at plan -
//     "If applied, this change fails" - instead of erroring, and the destroy
//     proceeds;
//   - the apply still refuses: the real PUT answers with the same refusal the
//     dry-run forecast.
//
// Driven through the terraform CLI because the whole bug is about real
// destroy's plan-then-destroy sequence.

const pmSecondCaller = "usr_pmsecondcaller000000000"

// pmNoPolicyPuts asserts the mock saw no policy PUT at all (neither dry-run
// nor real), which is the R1-skip evidence.
func pmNoPolicyPuts(t *testing.T, m *policyMockServer) {
	t.Helper()
	var puts []string
	for _, r := range m.takeRequests() {
		if strings.Contains(r, "PUT") && strings.Contains(r, "/policy") {
			puts = append(puts, r)
		}
	}
	if len(puts) != 0 {
		t.Errorf("plan made %d policy PUT(s), want 0 (an unchanged resource makes no plan-time API call): %v", len(puts), puts)
	}
}

// pmDestroyWarnsAndSucceeds applies config as the author, flips the mock into
// a state whose plan-time check refuses, and destroys: the plan in that state
// forecasts the refusal as a warning, and the destroy must exit zero and
// remove the resource from state.
func pmDestroyWarnsAndSucceeds(t *testing.T, refusal func(m *policyMockServer)) {
	t.Helper()
	m := newPolicyMock(t)
	run := cliDevWorkspace(t, pmConfig(m, "project_id", pmProject, pmBase()...))
	pmRunOK(t, run, "apply", "-auto-approve", "-json")

	refusal(m)
	m.takeRequests()

	// The forecast warning is what an ordinary plan shows in this state. The
	// destroy below runs the same plan pass first, and empirically the CLI
	// does not re-print that pass's warnings in its own output: the request
	// trace shows the dry-run refusing and the warning being built, so the
	// observable contract here is the plan warning plus the destroy's exit.
	planLines := pmRunOK(t, run, "plan", "-json")
	pmExpectWarning(t, planLines, "Policy Would Revoke Your Own Role Binding",
		"If applied, this change fails:", "the destroy itself is not affected")
	m.takeRequests()

	lines, out, err := run("destroy", "-auto-approve", "-json")
	if err != nil {
		t.Fatalf("terraform destroy must succeed when only the plan-time check refuses - the destroy itself is unaffected; it failed: %v\n%s",
			cliDiagnostics(lines, "error"), out)
	}
	if _, stateOut, listErr := run("state", "list"); listErr != nil {
		t.Fatalf("terraform state list: %v\n%s", listErr, stateOut)
	} else if strings.Contains(stateOut, "anyscale_iam_role_binding.test") {
		t.Errorf("resource still in state after destroy:\n%s", stateOut)
	}
}

// Test 1: the plan-time check refuses - here the policy would revoke the
// caller's own role binding - and the destroy must still succeed, warning
// with the forecast instead of erroring.
func TestAccIAMRoleBindingResource_DestroySurvivesRefusedPlanCheckCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	pmDestroyWarnsAndSucceeds(t, func(m *policyMockServer) {
		// Seeded after the apply: refresh pulls it into state, the plan sees
		// a roles diff, the dry-run runs and refuses.
		m.seed("user", policyMockCaller, pmWriter, "project", pmProject)
	})
}

// Test 4: the author applies, and a second caller destroys. The second
// caller's own binding is what the policy would revoke, so their plan-time
// check refuses - the exact case that blocked destroy before, now a warning.
func TestAccIAMRoleBindingResource_DestroyBySecondIdentityCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	pmDestroyWarnsAndSucceeds(t, func(m *policyMockServer) {
		m.addScope("user", pmSecondCaller)
		m.seed("user", pmSecondCaller, pmWriter, "project", pmProject)
		m.setCallerID(pmSecondCaller)
	})
}

// Test 2: the apply must still refuse with the real error. Same mock state as
// the destroy test, changed configuration: the plan warns, the apply errors,
// and no binding changes on the backend.
func TestAccIAMRoleBindingResource_ApplyStillRefusesAfterPlanWarningCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	changed := pmConfig(m, "project_id", pmProject,
		append(pmBase(), pmRole(pmWriter, nil, []string{pmGroupB}))...)
	run := cliDevWorkspace(t, changed)

	pmRunOK(t, run, "apply", "-auto-approve", "-json")

	// The out-of-band binding the policy would revoke: the caller's own.
	m.seed("user", policyMockCaller, pmWriter, "project", pmProject)
	m.takeRequests()

	planLines := pmRunOK(t, run, "plan", "-json")
	pmExpectWarning(t, planLines, "Policy Would Revoke Your Own Role Binding", "If applied, this change fails:")

	lines, out, err := run("apply", "-auto-approve", "-json")
	if err == nil {
		t.Fatal("the apply must still refuse with the real refusal; it succeeded")
	}
	found := false
	for _, l := range lines {
		if l.Type == "diagnostic" && l.Diagnostic.Severity == "error" &&
			strings.Contains(l.Diagnostic.Summary, "Policy Would Revoke Your Own Role Binding") &&
			!strings.Contains(l.Diagnostic.Detail, "If applied, this change fails:") {
			found = true
		}
	}
	if !found {
		t.Errorf("the apply must error with the real refusal (no forecast wording); got: %v\n%s", cliDiagnostics(lines, ""), out)
	}
	// Exactly one real PUT failed, and nothing changed on the backend: the
	// caller's out-of-band writer binding is still held.
	if real, dry := pmApplyBodies(t, m.takeRequests()); len(real) != 1 {
		t.Errorf("want exactly 1 real policy PUT (the refused one), got %d real and %d dry", len(real), len(dry))
	}
	for _, fact := range m.facts("project", pmProject) {
		if fact == pmFact(pmWriter, "user", policyMockCaller) {
			return
		}
	}
	t.Errorf("the refused apply changed the backend; the caller's own %s binding is gone", pmWriter)
}

// Test 3: an unchanged resource makes no plan-time API call - the R1 skip. A
// re-plan against an unchanged state-and-config reaches the provider, but the
// dry-run does not fire.
func TestAccIAMRoleBindingResource_UnchangedPlanSkipsPolicyCheckCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	run := cliDevWorkspace(t, pmConfig(m, "project_id", pmProject, pmBase()...))
	pmRunOK(t, run, "apply", "-auto-approve", "-json")
	m.takeRequests()

	planLines := pmRunOK(t, run, "plan", "-json")
	if warns := pmProviderWarnings(planLines); len(warns) != 0 {
		t.Errorf("an unchanged re-plan must not warn; got %v", warns)
	}
	pmNoPolicyPuts(t, m)
}
