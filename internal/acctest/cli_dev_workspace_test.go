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

// cliWorkspacePreamble opens every scratch main.tf: the anyscale provider under
// the source address the scratch CLI config's dev_overrides entry names.
const cliWorkspacePreamble = `
terraform {
  required_providers {
    anyscale = {
      source = "terraform-providers/anyscale"
    }
  }
}
`

// writeCLIWorkspace writes a fresh working directory whose scratch CLI config
// points dev_overrides at binDir, with a main.tf of cliWorkspacePreamble
// followed by body. It never touches ~/.terraformrc, whose dev_overrides entry
// can point at a stale binary from another checkout. It returns the directory
// and the CLI config path to pass as TF_CLI_CONFIG_FILE.
func writeCLIWorkspace(t *testing.T, binDir, body string) (workDir, cliConfigPath string) {
	t.Helper()
	workDir = t.TempDir()
	cliConfigPath = filepath.Join(workDir, "dev.tfrc")
	cliConfig := fmt.Sprintf(`
provider_installation {
  dev_overrides {
    "terraform-providers/anyscale" = %q
  }
  direct {}
}
`, binDir)
	if err := os.WriteFile(cliConfigPath, []byte(cliConfig), 0o600); err != nil {
		t.Fatalf("failed to write scratch CLI config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "main.tf"), []byte(cliWorkspacePreamble+body), 0o600); err != nil {
		t.Fatalf("failed to write scratch main.tf: %v", err)
	}
	return workDir, cliConfigPath
}

// runTerraformCLI runs the terraform CLI in a workspace from writeCLIWorkspace
// and returns its combined output.
func runTerraformCLI(workDir, cliConfigPath string, args ...string) ([]byte, error) {
	cmd := exec.Command("terraform", args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "TF_CLI_CONFIG_FILE="+cliConfigPath)
	return cmd.CombinedOutput()
}

// parseTFJSONLines decodes each line of `terraform ... -json` output, skipping
// lines that are not JSON (the dev_overrides banner and similar).
func parseTFJSONLines(out []byte) []tfApplyJSONLine {
	var lines []tfApplyJSONLine
	for _, raw := range strings.Split(string(out), "\n") {
		var l tfApplyJSONLine
		if json.Unmarshal([]byte(strings.TrimSpace(raw)), &l) == nil {
			lines = append(lines, l)
		}
	}
	return lines
}

// cliDevWorkspace writes hcl into a fresh working directory whose CLI config
// points the anyscale provider at a freshly built binary, and returns a
// function that runs the terraform CLI there. For assertions resource.Test
// cannot make, such as warnings, which its reattach path does not surface
// (see warning_diagnostics_acc_test.go). hcl supplies the provider block.
func cliDevWorkspace(t *testing.T, hcl string) func(args ...string) ([]tfApplyJSONLine, string, error) {
	t.Helper()
	workDir, cliConfigPath := writeCLIWorkspace(t, buildProviderBinaryForCLICheck(t), hcl)
	return func(args ...string) ([]tfApplyJSONLine, string, error) {
		out, err := runTerraformCLI(workDir, cliConfigPath, args...)
		return parseTFJSONLines(out), string(out), err
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

// cliDevWorkspaceEditable is cliDevWorkspace plus a function that replaces
// main.tf with new hcl, for a plan against changed config in the same state.
func cliDevWorkspaceEditable(t *testing.T, hcl string) (run func(args ...string) ([]tfApplyJSONLine, string, error), rewrite func(hcl string)) {
	t.Helper()
	workDir, cliConfigPath := writeCLIWorkspace(t, buildProviderBinaryForCLICheck(t), hcl)
	run = func(args ...string) ([]tfApplyJSONLine, string, error) {
		out, err := runTerraformCLI(workDir, cliConfigPath, args...)
		return parseTFJSONLines(out), string(out), err
	}
	rewrite = func(hcl string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(workDir, "main.tf"), []byte(cliWorkspacePreamble+hcl), 0o600); err != nil {
			t.Fatalf("failed to rewrite scratch main.tf: %v", err)
		}
	}
	return run, rewrite
}
