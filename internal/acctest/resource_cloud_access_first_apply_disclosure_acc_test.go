package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This file proves anyscale_cloud_access's plan-time first-apply-revoke
// disclosure warning the same way warning_diagnostics_acc_test.go proves
// C5/C9: resource.Test's reattach path does not surface warning-level
// diagnostics at all (confirmed empirically there), so this drives a real
// `terraform plan -json` against a freshly-built provider binary instead -
// the only way to observe what a practitioner actually sees before their
// first apply against a cloud anyscale_cloud_access has never read before.
//
// Every helper builds its own binary into a throwaway directory, same
// discipline as warning_diagnostics_acc_test.go, for the same reason: the
// ambient ~/.terraformrc dev_overrides entry can silently point at a stale
// binary from the main repo checkout.

// runTerraformPlanJSON is runTerraformApplyJSON's plan-only analogue: the
// disclosure warning fires from ModifyPlan, which only ever runs during
// plan (and the plan phase of apply) - never validate. apiURL points the
// provider at a mock backend so this needs no real credentials or infra.
func runTerraformPlanJSON(t *testing.T, binDir, apiURL, resourceHCL string) []tfApplyJSONLine {
	t.Helper()

	workDir := t.TempDir()

	tfConfig := fmt.Sprintf(`
provider_installation {
  dev_overrides {
    "terraform-providers/anyscale" = %[1]q
  }
  direct {}
}
`, binDir)
	cliConfigPath := filepath.Join(workDir, "dev.tfrc")
	if err := os.WriteFile(cliConfigPath, []byte(tfConfig), 0o600); err != nil {
		t.Fatalf("failed to write scratch CLI config: %v", err)
	}

	mainTF := fmt.Sprintf(`
terraform {
  required_providers {
    anyscale = {
      source = "terraform-providers/anyscale"
    }
  }
}

provider "anyscale" {
  api_url = %[1]q
  token   = "disclosure-check-fake-token"
}

%[2]s
`, apiURL, resourceHCL)
	if err := os.WriteFile(filepath.Join(workDir, "main.tf"), []byte(mainTF), 0o600); err != nil {
		t.Fatalf("failed to write scratch main.tf: %v", err)
	}

	cmd := exec.Command("terraform", "plan", "-json")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "TF_CLI_CONFIG_FILE="+cliConfigPath)

	out, _ := cmd.CombinedOutput() // plan may exit non-zero on a warning in some versions; parse regardless

	var lines []tfApplyJSONLine
	for _, rawLine := range strings.Split(string(out), "\n") {
		rawLine = strings.TrimSpace(rawLine)
		if rawLine == "" {
			continue
		}
		var line tfApplyJSONLine
		if err := json.Unmarshal([]byte(rawLine), &line); err != nil {
			continue // dev_overrides banner and similar lines aren't JSON; skip rather than fail
		}
		lines = append(lines, line)
	}
	if lines == nil {
		t.Fatalf("no parseable JSON lines from `terraform plan -json`; raw output:\n%s", out)
	}
	return lines
}

const cloudAccessDisclosureCloudID = "cld_disclosure_test"

// newCloudAccessDisclosureMockServer backs the three endpoints
// discloseFirstApplyRevoke needs: the collaborator search (identity), the
// roles list (base_role/deny_roles), and userinfo (caller exclusion).
//
// failSearch/failRoles independently make either half of the join 500, for
// the negative test - discloseFirstApplyRevoke must treat either failure the
// same way: read not determined, nothing named.
func newCloudAccessDisclosureMockServer(t *testing.T, memberEmail, memberUserID, memberIdentityID string, failSearch, failRoles bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds/"+cloudAccessDisclosureCloudID+"/collaborators/users/search", func(w http.ResponseWriter, r *http.Request) {
		if failSearch {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":{"detail":"search exploded"}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results":[{"id":%[1]q,"value":{"id":%[2]q,"name":"mock member","email":%[3]q}}],`+
			`"metadata":{"next_paging_token":null}}`, memberIdentityID, memberUserID, memberEmail)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudAccessDisclosureCloudID+"/collaborators/roles", func(w http.ResponseWriter, r *http.Request) {
		if failRoles {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":{"detail":"roles exploded"}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results":[{"user_id":%[1]q,"base_roles":["writer"],"deny_roles":[]}],`+
			`"metadata":{"next_paging_token":null}}`, memberUserID)
	})

	mux.HandleFunc("/api/v2/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Deliberately a different identity than memberEmail/memberUserID: the
		// calling identity must never be reported as revoked, and this proves
		// the mock member survives THAT exclusion while still being named.
		_, _ = fmt.Fprint(w, `{"result":{"id":"usr_caller","email":"caller@example.com","name":"caller",`+
			`"username":"caller","organization_permission_level":null,"organization_ids":[],"organizations":[]}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func cloudAccessDisclosureResourceHCL() string {
	return fmt.Sprintf(`
resource "anyscale_cloud_access" "test" {
  cloud_id = %[1]q
  member   = {}
}
`, cloudAccessDisclosureCloudID)
}

// TestAccCloudAccessResource_FirstApplyDisclosureNamesRevokedMembers is the
// positive case: a member exists on the cloud, is absent from config
// (member = {}), and the plan must NAME them - a count is not disclosure.
func TestAccCloudAccessResource_FirstApplyDisclosureNamesRevokedMembers(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	binDir := buildProviderBinaryForCLICheck(t)
	server := newCloudAccessDisclosureMockServer(t, "existing-member@example.com", "usr_existing", "idn_existing", false, false)

	lines := runTerraformPlanJSON(t, binDir, server.URL, cloudAccessDisclosureResourceHCL())

	found := false
	for _, l := range lines {
		if l.Type == "diagnostic" && l.Diagnostic.Severity == "warning" &&
			l.Diagnostic.Summary == "This Apply Will Revoke Existing Cloud Members" {
			found = true
			if !strings.Contains(l.Diagnostic.Detail, "existing-member@example.com") {
				t.Errorf("warning did not NAME the member, a count is not disclosure: %s", l.Diagnostic.Detail)
			}
			if strings.Contains(l.Diagnostic.Detail, "caller@example.com") {
				t.Errorf("warning must not name the calling identity, which this resource never manages: %s", l.Diagnostic.Detail)
			}
			break
		}
	}
	if !found {
		var summaries []string
		for _, l := range lines {
			if l.Type == "diagnostic" {
				summaries = append(summaries, fmt.Sprintf("%s: %s", l.Diagnostic.Severity, l.Diagnostic.Summary))
			}
		}
		t.Fatalf("expected a 'This Apply Will Revoke Existing Cloud Members' warning naming existing-member@example.com, got none. All diagnostics: %v", summaries)
	}

	// Condition 2: this is a WARNING, not an error - the plan must still
	// propose to create the resource, not fail outright.
	planSucceeded := false
	for _, l := range lines {
		if l.Type == "planned_change" {
			planSucceeded = true
		}
	}
	if !planSucceeded {
		t.Error("expected the plan to still propose creating the resource despite the warning (condition 2: warning, not error)")
	}
}

// TestAccCloudAccessResource_FirstApplyDisclosureFailsLoudlyOnReadError is the
// negative case: when the member-list read fails, the plan must say
// disclosure did not run - not silently proceed as if the cloud had no other
// members, which would look identical to a real "nobody else is here" result
// and restore the exact undisclosed-revoke hazard the warning exists to
// remove.
func TestAccCloudAccessResource_FirstApplyDisclosureFailsLoudlyOnReadError(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	binDir := buildProviderBinaryForCLICheck(t)
	server := newCloudAccessDisclosureMockServer(t, "existing-member@example.com", "usr_existing", "idn_existing", true, false)

	lines := runTerraformPlanJSON(t, binDir, server.URL, cloudAccessDisclosureResourceHCL())

	found := false
	for _, l := range lines {
		if l.Type == "diagnostic" && l.Diagnostic.Severity == "warning" &&
			l.Diagnostic.Summary == "Cloud Access Disclosure Did Not Run" {
			found = true
			break
		}
		if l.Type == "diagnostic" && l.Diagnostic.Summary == "This Apply Will Revoke Existing Cloud Members" {
			t.Fatalf("a failed member-list read must never be silently treated as an empty cloud: got the success-path warning instead of the failure one")
		}
	}
	if !found {
		var summaries []string
		for _, l := range lines {
			if l.Type == "diagnostic" {
				summaries = append(summaries, fmt.Sprintf("%s: %s", l.Diagnostic.Severity, l.Diagnostic.Summary))
			}
		}
		t.Fatalf("expected a 'Cloud Access Disclosure Did Not Run' warning when the read fails, got none. All diagnostics: %v", summaries)
	}

	// Still condition 2, even on the failure path: the apply must be able to
	// proceed. A read failure blocking the plan outright would turn a
	// transient backend error into an unplannable configuration.
	planSucceeded := false
	for _, l := range lines {
		if l.Type == "planned_change" {
			planSucceeded = true
		}
	}
	if !planSucceeded {
		t.Error("expected the plan to still propose creating the resource on a failed read (condition 2: warning, not error)")
	}
}
