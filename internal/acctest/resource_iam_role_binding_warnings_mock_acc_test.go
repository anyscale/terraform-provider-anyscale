package acctest

import (
	"fmt"
	"strings"
	"testing"
)

// Warning-level diagnostics of anyscale_iam_role_binding, driven through the
// terraform CLI (a real plan and apply against a freshly built provider)
// because resource.Test does not surface warnings.

// pmExpectWarning fails unless lines hold a warning with this summary whose
// detail contains every one of want.
func pmExpectWarning(t *testing.T, lines []tfApplyJSONLine, summary string, want ...string) {
	t.Helper()
	for _, l := range lines {
		if l.Type != "diagnostic" || l.Diagnostic.Severity != "warning" || l.Diagnostic.Summary != summary {
			continue
		}
		missing := []string{}
		for _, w := range want {
			if !strings.Contains(l.Diagnostic.Detail, w) {
				missing = append(missing, w)
			}
		}
		if len(missing) == 0 {
			return
		}
		t.Errorf("warning %q lacks %q; detail: %s", summary, missing, l.Diagnostic.Detail)
		return
	}
	t.Errorf("no warning %q; diagnostics: %v", summary, cliDiagnostics(lines, ""))
}

// pmProviderWarnings is every warning but the CLI's own dev_overrides notice.
func pmProviderWarnings(lines []tfApplyJSONLine) []string {
	var out []string
	for _, w := range cliDiagnostics(lines, "warning") {
		if !strings.HasPrefix(w, "warning: Provider development overrides") {
			out = append(out, w)
		}
	}
	return out
}

func pmRunOK(t *testing.T, run func(args ...string) ([]tfApplyJSONLine, string, error), args ...string) []tfApplyJSONLine {
	t.Helper()
	lines, out, err := run(args...)
	if err != nil || len(cliDiagnostics(lines, "error")) > 0 {
		t.Fatalf("terraform %v failed: %v %v\n%s", args, err, cliDiagnostics(lines, "error"), out)
	}
	return lines
}

// Criterion 3: the create plan names each grant the first apply revokes.
func TestAccIAMRoleBindingResource_CreateTakeoverWarningCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	m.seed("user", pmBob, pmReader, "project", pmProject)
	m.seed("user_group", pmGroupB, pmWriter, "project", pmProject)
	run := cliDevWorkspace(t, pmConfig(m, "project_id", pmProject, pmBase()...))

	lines := pmRunOK(t, run, "plan", "-json")
	pmExpectWarning(t, lines, "Policy Revokes Existing Role Bindings",
		"which the roles map does not name:",
		"role "+pmReader+" to user "+pmBob,
		"role "+pmWriter+" to user_group "+pmGroupB,
		"is an anyscale_iam_role_grant in any configuration, this scope is managed twice")
	if real, dry := pmApplyBodies(t, m.takeRequests()); len(real) != 0 || len(dry) == 0 {
		t.Errorf("plan made %d applies and %d dry runs, want 0 and at least 1", len(real), len(dry))
	}
}

const pmOutsideSummary = "Policy Revokes Role Bindings Granted Outside It"

// pmOutsideWarning returns the detail of the update plan's outside-grant
// warning, failing the test if lines hold any other provider warning.
func pmOutsideWarning(t *testing.T, lines []tfApplyJSONLine) (detail string, found bool) {
	t.Helper()
	for _, l := range lines {
		if l.Type == "diagnostic" && l.Diagnostic.Severity == "warning" && l.Diagnostic.Summary == pmOutsideSummary {
			detail, found = l.Diagnostic.Detail, true
		}
	}
	for _, w := range pmProviderWarnings(lines) {
		if !strings.HasPrefix(w, "warning: "+pmOutsideSummary+":") {
			t.Errorf("update plan warned: %s", w)
		}
	}
	return detail, found
}

// pmUpdatePlan applies pmBase, runs seed, and returns the next plan's lines:
// an update, since refresh pulls the seeded grants into state.
func pmUpdatePlan(t *testing.T, m *policyMockServer, config string, seed func()) []tfApplyJSONLine {
	t.Helper()
	run := cliDevWorkspace(t, config)
	pmRunOK(t, run, "apply", "-auto-approve", "-json")
	seed()
	m.takeRequests()
	lines := pmRunOK(t, run, "plan", "-json")
	if real, dry := pmApplyBodies(t, m.takeRequests()); len(real) != 0 || len(dry) == 0 {
		t.Errorf("update plan made %d applies and %d dry runs, want 0 and at least 1", len(real), len(dry))
	}
	return lines
}

// B: an update that revokes a direct grant warns, listing exactly it.
func TestAccIAMRoleBindingResource_UpdateRevokesOutsideGrantWarningCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	lines := pmUpdatePlan(t, m, pmConfig(m, "project_id", pmProject, pmBase()...), func() {
		m.seed("user", pmBob, pmReader, "project", pmProject)
	})
	detail, found := pmOutsideWarning(t, lines)
	if !found {
		t.Fatalf("no %q warning; diagnostics: %v", pmOutsideSummary, cliDiagnostics(lines, ""))
	}
	for _, w := range []string{"which were made outside this policy:", "role " + pmReader + " to user " + pmBob, "managed twice"} {
		if !strings.Contains(detail, w) {
			t.Errorf("warning lacks %q; detail: %s", w, detail)
		}
	}
	if n := strings.Count(detail, "  - "); n != 1 {
		t.Errorf("warning lists %d bindings, want 1; detail: %s", n, detail)
	}
}

// C: an update that drops a principal the policy granted revokes only a
// declarative grant, which the plan diff shows; it does not warn.
func TestAccIAMRoleBindingResource_UpdateRevokesOwnGrantNoWarningCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	run, rewrite := cliDevWorkspaceEditable(t, pmConfig(m, "project_id", pmProject,
		append(pmBase(), pmRole(pmWriter, []string{pmBob}, nil))...))
	pmRunOK(t, run, "apply", "-auto-approve", "-json")

	rewrite(pmConfig(m, "project_id", pmProject, pmBase()...))
	m.takeRequests()
	lines := pmRunOK(t, run, "plan", "-json")
	if real, dry := pmApplyBodies(t, m.takeRequests()); len(real) != 0 || len(dry) == 0 {
		t.Errorf("update plan made %d applies and %d dry runs, want 0 and at least 1", len(real), len(dry))
	}
	if detail, found := pmOutsideWarning(t, lines); found {
		t.Errorf("revoking the policy's own grant warned: %s", detail)
	}
}

// D: with both kinds revoked, the warning lists only the direct grant.
func TestAccIAMRoleBindingResource_UpdateMixedRevokesWarningCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	run, rewrite := cliDevWorkspaceEditable(t, pmConfig(m, "project_id", pmProject,
		append(pmBase(), pmRole(pmWriter, []string{pmBob}, nil))...))
	pmRunOK(t, run, "apply", "-auto-approve", "-json")
	m.seed("user", pmAlice, pmReader, "project", pmProject)

	rewrite(pmConfig(m, "project_id", pmProject, pmBase()...))
	lines := pmRunOK(t, run, "plan", "-json")
	detail, found := pmOutsideWarning(t, lines)
	if !found {
		t.Fatalf("no %q warning; diagnostics: %v", pmOutsideSummary, cliDiagnostics(lines, ""))
	}
	if !strings.Contains(detail, "role "+pmReader+" to user "+pmAlice) {
		t.Errorf("warning does not list the direct grant; detail: %s", detail)
	}
	if strings.Contains(detail, pmBob) {
		t.Errorf("warning lists the policy's own grant to %s; detail: %s", pmBob, detail)
	}
}

// E: a revoked grant with no origin counts as granted outside the policy.
func TestAccIAMRoleBindingResource_UpdateRevokesOriginlessGrantWarningCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	lines := pmUpdatePlan(t, m, pmConfig(m, "project_id", pmProject, pmBase()...), func() {
		m.seedOrigin("user", pmBob, pmWriter, "project", pmProject, "")
	})
	detail, found := pmOutsideWarning(t, lines)
	if !found {
		t.Fatalf("no %q warning; diagnostics: %v", pmOutsideSummary, cliDiagnostics(lines, ""))
	}
	if !strings.Contains(detail, "role "+pmWriter+" to user "+pmBob) {
		t.Errorf("warning does not list the originless grant; detail: %s", detail)
	}
}

// Criterion 5: with the scope unknown at plan, the plan warns once and makes
// no dry run.
func TestAccIAMRoleBindingResource_UnknownScopeWarningCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	run := cliDevWorkspace(t, m.providerBlock()+fmt.Sprintf(`
resource "terraform_data" "scope" {
  input = %q
}

resource "anyscale_iam_role_binding" "test" {
  project_id = terraform_data.scope.output
  roles = {
%s  }
}
`, pmProject, strings.Join(pmBase(), "")))

	lines := pmRunOK(t, run, "plan", "-json")
	pmExpectWarning(t, lines, "Role Binding Not Checked at Plan", "owner role", "HTTP 403")
	if w := pmProviderWarnings(lines); len(w) != 1 {
		t.Errorf("want exactly one warning, got %d: %v", len(w), w)
	}
	if puts := m.requestsMatching("PUT "); len(puts) != 0 {
		t.Errorf("plan with an unknown scope made policy calls: %v", puts)
	}
}

// Criterion 9: destroy's plan lists what it revokes, and destroy warns naming
// every binding it kept: the caller's own and each frozen one.
func TestAccIAMRoleBindingResource_DestroyWarningsCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	m.seedFrozen("user", pmBob, pmWriter, "project", pmProject)
	run := cliDevWorkspace(t, pmConfig(m, "project_id", pmProject,
		append(pmBase(), pmRole(pmWriter, []string{pmBob}, nil))...))
	pmRunOK(t, run, "apply", "-auto-approve", "-json")

	lines := pmRunOK(t, run, "plan", "-destroy", "-json")
	pmExpectDestroyPlanWarning(t, lines,
		[]string{"role " + pmReader + " to user_group " + pmGroupA},
		[]string{"role " + pmOwner + " to user " + policyMockCaller, "role " + pmWriter + " to user " + pmBob})

	// Every revoked grant is the policy's own, so there is no mix hint.
	pmExpectNoMixHint(t, lines)

	lines = pmRunOK(t, run, "destroy", "-auto-approve", "-json")
	pmExpectWarning(t, lines, "Role Bindings Kept on Destroy",
		"role "+pmOwner+" to user "+policyMockCaller, "role "+pmWriter+" to user "+pmBob)
	if got, want := m.facts("project", pmProject), []string{pmFact(pmOwner, "user", policyMockCaller), pmFact(pmWriter, "user", pmBob)}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("after destroy the project holds %v, want %v", got, want)
	}
}

// Criterion 11: a policy whose project is gone is removed from state with the
// treated-as-deleted warning.
func TestAccIAMRoleBindingResource_ScopeGoneWarningCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	run := cliDevWorkspace(t, pmConfig(m, "project_id", pmProject, pmBase()...))
	pmRunOK(t, run, "apply", "-auto-approve", "-json")

	m.deleteScope("project", pmProject)
	lines := pmRunOK(t, run, "apply", "-refresh-only", "-auto-approve", "-json")
	pmExpectWarning(t, lines, "Project Treated As Deleted", pmProject)
	_, out, err := run("state", "list")
	if err != nil {
		t.Fatalf("terraform state list: %v\n%s", err, out)
	}
	if strings.Contains(out, pmAddr) {
		t.Errorf("%s still in state after its project was deleted:\n%s", pmAddr, out)
	}
}

// pmExpectDestroyPlanWarning checks the destroy plan's warning lists each of
// revoked before its keeps-these section and each of kept inside it.
func pmExpectDestroyPlanWarning(t *testing.T, lines []tfApplyJSONLine, revoked, kept []string) {
	t.Helper()
	const marker = "It keeps these"
	for _, l := range lines {
		if l.Type != "diagnostic" || l.Diagnostic.Severity != "warning" || l.Diagnostic.Summary != "Destroy Revokes Role Bindings" {
			continue
		}
		head, tail, found := strings.Cut(l.Diagnostic.Detail, marker)
		if !found {
			t.Errorf("destroy-plan warning has no %q section; detail: %s", marker, l.Diagnostic.Detail)
			return
		}
		for _, r := range revoked {
			if !strings.Contains(head, r) {
				t.Errorf("destroy-plan warning does not list %q as revoked; detail: %s", r, l.Diagnostic.Detail)
			}
		}
		for _, k := range kept {
			if !strings.Contains(tail, k) {
				t.Errorf("destroy-plan warning does not list %q as kept; detail: %s", k, l.Diagnostic.Detail)
			}
		}
		return
	}
	t.Errorf("no destroy-plan warning; diagnostics: %v", cliDiagnostics(lines, ""))
}

// A frozen 409 in the destroy plan's dry run keeps the binding: the plan
// succeeds and warns naming it, even when nothing else is revoked.
func TestAccIAMRoleBindingResource_DestroyPlanKeepsFrozenCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	m.seedFrozen("user", pmBob, pmWriter, "project", pmProject)
	run := cliDevWorkspace(t, pmConfig(m, "project_id", pmProject,
		pmRole(pmOwner, []string{policyMockCaller}, nil), pmRole(pmWriter, []string{pmBob}, nil)))
	pmRunOK(t, run, "apply", "-auto-approve", "-json")

	m.takeRequests()
	lines := pmRunOK(t, run, "plan", "-destroy", "-json")
	pmExpectDestroyPlanWarning(t, lines, nil, []string{"role " + pmWriter + " to user " + pmBob})
	if real, dry := pmApplyBodies(t, m.takeRequests()); len(real) != 0 || len(dry) < 2 {
		t.Errorf("destroy plan made %d applies and %d dry runs, want 0 and a retry after the frozen refusal", len(real), len(dry))
	}
}

const pmDestroyMixMarker = "These revoked grants were made outside this policy:"

// pmExpectNoMixHint fails if the destroy plan's warning carries the mix hint.
func pmExpectNoMixHint(t *testing.T, lines []tfApplyJSONLine) {
	t.Helper()
	for _, l := range lines {
		if l.Type == "diagnostic" && l.Diagnostic.Summary == "Destroy Revokes Role Bindings" && strings.Contains(l.Diagnostic.Detail, pmDestroyMixMarker) {
			t.Errorf("destroy of policy-made grants only carries the mix hint; detail: %s", l.Diagnostic.Detail)
		}
	}
}

// A destroy that revokes a grant made outside the policy names it and warns
// that the scope may be managed twice; the policy's own grants stay out of
// that list.
func TestAccIAMRoleBindingResource_DestroyMixHintCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	run := cliDevWorkspace(t, pmConfig(m, "project_id", pmProject, pmBase()...))
	pmRunOK(t, run, "apply", "-auto-approve", "-json")
	// Granted directly after the apply, as anyscale_iam_role_grant would.
	m.seed("user", pmBob, pmWriter, "project", pmProject)

	lines := pmRunOK(t, run, "plan", "-destroy", "-json")
	for _, l := range lines {
		if l.Type != "diagnostic" || l.Diagnostic.Summary != "Destroy Revokes Role Bindings" {
			continue
		}
		_, mix, found := strings.Cut(l.Diagnostic.Detail, pmDestroyMixMarker)
		if !found {
			t.Fatalf("destroy-plan warning has no mix hint; detail: %s", l.Diagnostic.Detail)
		}
		if !strings.Contains(mix, "role "+pmWriter+" to user "+pmBob) || !strings.Contains(mix, "managed twice") {
			t.Errorf("mix hint does not name the outside grant; detail: %s", l.Diagnostic.Detail)
		}
		if strings.Contains(mix, pmGroupA) {
			t.Errorf("mix hint lists the policy's own grant to %s; detail: %s", pmGroupA, l.Diagnostic.Detail)
		}
		return
	}
	t.Errorf("no destroy-plan warning; diagnostics: %v", cliDiagnostics(lines, ""))
}
