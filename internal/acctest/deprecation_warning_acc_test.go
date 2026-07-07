package acctest

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This file is a regression test for change C5: setting DeprecationMessage
// on a terraform-plugin-framework schema attribute does NOT, by itself,
// guarantee a runtime deprecation warning is ever shown to the user - that
// depends on validation actually running and reaching the check, which this
// codebase's first C5 implementation did not (block-nested attributes,
// confirmed empirically 2026-07-07: schema.DeprecationMessage was set
// correctly, but neither `terraform plan` nor `terraform validate` surfaced
// any warning).
//
// resource.Test's ProtoV6ProviderFactories path (used everywhere else in
// this package) runs terraform via a reattach mechanism that does not
// surface warning-level diagnostics at all, even on a passing step - so it
// cannot verify this criterion regardless of the underlying fix. This drives
// a real `terraform validate -json` against a freshly-built provider binary
// instead, which is the only way to observe the actual served warning text.

// tfDiagnostic mirrors the subset of `terraform validate -json`'s
// diagnostics we care about.
type tfDiagnostic struct {
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail"`
}

type tfValidateJSON struct {
	Valid        bool           `json:"valid"`
	WarningCount int            `json:"warning_count"`
	Diagnostics  []tfDiagnostic `json:"diagnostics"`
}

// buildProviderForDeprecationCheck compiles the provider under test into a
// scratch directory and returns that directory's path, for use as a
// dev_overrides target. Skips the test (not fails) if `terraform` or `go
// build` are unavailable, since this is an environment precondition, not a
// provider defect.
func buildProviderForDeprecationCheck(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform CLI not found in PATH, skipping deprecation-warning acceptance check")
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "terraform-provider-anyscale")

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine caller file to locate module root")
	}
	// this file lives at internal/acctest/deprecation_warning_acc_test.go
	moduleRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = moduleRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build provider for deprecation check: %v\n%s", err, out)
	}

	return binDir
}

// runTerraformValidate writes a scratch CLI config pointing dev_overrides at
// binDir, a main.tf with the given resource body, and runs
// `terraform validate -json` against it - self-contained, no dependency on
// the ambient ~/.terraformrc.
func runTerraformValidate(t *testing.T, binDir, resourceHCL string) tfValidateJSON {
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
  token = "deprecation-check-fake-token"
}

%s
`, resourceHCL)
	if err := os.WriteFile(filepath.Join(workDir, "main.tf"), []byte(mainTF), 0o600); err != nil {
		t.Fatalf("failed to write scratch main.tf: %v", err)
	}

	cmd := exec.Command("terraform", "validate", "-json")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "TF_CLI_CONFIG_FILE="+cliConfigPath)

	out, _ := cmd.CombinedOutput() // validate exits non-zero on warnings-as-well in some versions; parse regardless
	var result tfValidateJSON
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("failed to parse `terraform validate -json` output: %v\nraw output:\n%s", err, out)
	}
	return result
}

// TestAccKubernetesConfigDeprecatedFields_WarningActuallyFires is the
// corrected C5 acceptance criterion: setting each of the 5 inert
// kubernetes_config fields must produce a real, served deprecation warning,
// not just a schema struct field asserted by a unit test.
func TestAccKubernetesConfigDeprecatedFields_WarningActuallyFires(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	binDir := buildProviderForDeprecationCheck(t)

	resourceHCL := `
resource "anyscale_cloud" "test" {
  name           = "c5-deprecation-check"
  cloud_provider = "AWS"
  compute_stack  = "K8S"

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::123456789012:role/fake"
    namespace                      = "custom-ns"
    ingress_host                   = "anyscale.example.com"
    cluster_name                   = "my-cluster"
    context                        = "my-context"
    kubeconfig_path                = "/tmp/kubeconfig"
  }

  object_storage {
    bucket_name = "fake-bucket"
  }
}
`
	result := runTerraformValidate(t, binDir, resourceHCL)

	deprecatedFields := []string{"namespace", "ingress_host", "cluster_name", "context", "kubeconfig_path"}
	// All 5 fields share the identical DeprecationMessage text
	// (kubernetesConfigInertFieldDeprecationMessage), so a single warning
	// can't be attributed to one specific field by content alone. What IS
	// verifiable, and is the actual point of this test, is the count: all 5
	// fields are set above, so a correct fix produces exactly 5 such
	// warnings. A fix that only wires up some of the 5 (or the previous,
	// silently-broken state producing 0) shows up as a wrong count here.
	sharedMessageCount := 0
	for _, d := range result.Diagnostics {
		if d.Severity == "warning" && strings.Contains(d.Detail, "not sent to the Anyscale API") {
			sharedMessageCount++
		}
	}
	if sharedMessageCount != len(deprecatedFields) {
		t.Errorf("expected exactly %d deprecation warnings (one per inert field: %v), got %d. Full diagnostics: %+v",
			len(deprecatedFields), deprecatedFields, sharedMessageCount, result.Diagnostics)
	}
}
