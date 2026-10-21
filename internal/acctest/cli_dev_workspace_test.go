package acctest

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cliDevWorkspace writes hcl into a fresh working directory whose CLI config
// points the anyscale provider at a freshly built binary, and returns a
// function that runs the terraform CLI there. For assertions resource.Test
// cannot make, such as warnings, which its reattach path does not surface
// (see warning_diagnostics_acc_test.go). hcl supplies the provider block.
func cliDevWorkspace(t *testing.T, hcl string) func(args ...string) ([]tfApplyJSONLine, string, error) {
	t.Helper()
	binDir := buildProviderBinaryForCLICheck(t)
	workDir := t.TempDir()
	cliConfigPath := filepath.Join(workDir, "dev.tfrc")
	cliConfig := fmt.Sprintf(`
provider_installation {
  dev_overrides {
    "terraform-providers/anyscale" = %q
  }
  direct {}
}
`, binDir)
	if err := os.WriteFile(cliConfigPath, []byte(cliConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	mainTF := `
terraform {
  required_providers {
    anyscale = {
      source = "terraform-providers/anyscale"
    }
  }
}
` + hcl
	if err := os.WriteFile(filepath.Join(workDir, "main.tf"), []byte(mainTF), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(args ...string) ([]tfApplyJSONLine, string, error) {
		cmd := exec.Command("terraform", args...)
		cmd.Dir = workDir
		cmd.Env = append(os.Environ(), "TF_CLI_CONFIG_FILE="+cliConfigPath)
		out, err := cmd.CombinedOutput()
		var lines []tfApplyJSONLine
		for _, raw := range strings.Split(string(out), "\n") {
			var l tfApplyJSONLine
			if json.Unmarshal([]byte(strings.TrimSpace(raw)), &l) == nil {
				lines = append(lines, l)
			}
		}
		return lines, string(out), err
	}
}

// cliDiagnostics returns "severity: summary" for each diagnostic of the given
// severity ("" for all).
func cliDiagnostics(lines []tfApplyJSONLine, severity string) []string {
	var out []string
	for _, l := range lines {
		if l.Type == "diagnostic" && (severity == "" || l.Diagnostic.Severity == severity) {
			out = append(out, l.Diagnostic.Severity+": "+l.Diagnostic.Summary+": "+l.Diagnostic.Detail)
		}
	}
	return out
}
