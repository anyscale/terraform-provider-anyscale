package acctest

import (
	"strings"
	"testing"
)

// Warning-level diagnostics of anyscale_role_binding, driven through the
// terraform CLI because resource.Test does not surface warnings.

// A binding revoked out of band warns on refresh, naming the role-binding
// policy hazard, and the plan re-creates it.
func TestAccRoleBindingResource_RevokedOutOfBandWarnsCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRBMock(t)
	run := cliDevWorkspace(t, rbConfig(m, rbRole))
	pmRunOK(t, run, "apply", "-auto-approve", "-json")
	if n := m.bindingCount(); n != 1 {
		t.Fatalf("after apply the mock holds %d bindings, want 1", n)
	}
	m.revoke(rbOnlyBindingID(m))

	lines, out, err := run("plan", "-json")
	if err != nil || len(cliDiagnostics(lines, "error")) > 0 {
		t.Fatalf("terraform plan failed: %v %v\n%s", err, cliDiagnostics(lines, "error"), out)
	}
	pmExpectWarning(t, lines, "Role Binding Revoked Outside Terraform",
		"role "+rbRole+" to user_group "+rbGroup+" on cloud "+rbCloud,
		"Terraform plans to re-create it",
		"(anyscale_role_binding_policy or anyscale role-binding policy apply) manages this cloud")
	if !rbPlansCreate(out, "anyscale_role_binding.test") {
		t.Errorf("plan does not create anyscale_role_binding.test:\n%s", out)
	}
}

func rbOnlyBindingID(m *roleBindingMockServer) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.bindings {
		return id
	}
	return ""
}

// rbPlansCreate reports whether plan -json output plans a create of addr.
func rbPlansCreate(out, addr string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, `"type":"planned_change"`) && strings.Contains(line, `"addr":"`+addr+`"`) && strings.Contains(line, `"action":"create"`) {
			return true
		}
	}
	return false
}
