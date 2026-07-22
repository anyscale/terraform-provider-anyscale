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

// This file verifies WARNING-level diagnostics actually reach the user -
// schema.DeprecationMessage (C5) and resp.Diagnostics.AddWarning (C9) both
// looked correct at the source level but that doesn't guarantee Terraform
// ever shows them. resource.Test's ProtoV6ProviderFactories path (used
// everywhere else in this package) runs terraform via a reattach mechanism
// that does not surface warning-level diagnostics at all, even on a passing
// step (confirmed empirically 2026-07-07) - so it cannot verify either
// criterion regardless of the underlying implementation. This drives a real
// `terraform validate`/`plan -json` against a freshly-built provider binary
// instead, which is the only way to observe actually-served warning text.
//
// Also serves as the regression test for a real methodology bug found while
// building this: the ambient `~/.terraformrc` dev_overrides entry resolves
// to a binary at the main repo checkout root, not any worktree, and can
// silently be stale (see [[dev-overrides-shared-stale-binary]]). Every
// helper here builds its own binary into a throwaway directory and points
// dev_overrides at THAT via a scratch TF_CLI_CONFIG_FILE, deliberately never
// touching ~/.terraformrc, so these tests can't fall into that trap.

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

// buildProviderBinaryForCLICheck compiles the provider under test into a
// scratch directory and returns that directory's path, for use as a
// dev_overrides target. Skips the test (not fails) if `terraform` or `go
// build` are unavailable, since this is an environment precondition, not a
// provider defect.
func buildProviderBinaryForCLICheck(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform CLI not found in PATH, skipping warning-diagnostics acceptance check")
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "terraform-provider-anyscale")

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine caller file to locate module root")
	}
	// this file lives at internal/acctest/warning_diagnostics_acc_test.go
	moduleRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = moduleRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build provider for warning-diagnostics check: %v\n%s", err, out)
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

// TestAccCloudResource_KubernetesConfigRemovedFieldsRejected replaces
// TestAccCloudResource_KubernetesConfigDeprecatedFields_WarningActuallyFires:
// task #8 removed the 5 fields entirely rather than just deprecating them
// (see TestFlattenKubernetesConfig_APIBackedFieldsPopulate for why), which
// changes the diagnostic shape completely - a removed schema attribute is a
// config-vs-schema mismatch Terraform Core itself rejects at validate time
// ("Unsupported argument"), not a provider-served warning. Leaving the old
// warning-count assertion running would either fail confusingly (0
// warnings, since the fields no longer exist to warn about) or, worse,
// quietly stop proving anything if some
// unrelated diagnostic happened to match its loose Contains check. This
// asserts the actual post-removal contract: validate fails, and every one
// of the 5 removed names is named in its own "Unsupported argument" error.
func TestAccCloudResource_KubernetesConfigRemovedFieldsRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	binDir := buildProviderBinaryForCLICheck(t)

	resourceHCL := `
resource "anyscale_cloud" "test" {
  name           = "k8s-field-removal-check"
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

	if result.Valid {
		t.Fatal("expected validate to fail - all 5 removed kubernetes_config fields are still set in config, " +
			"each must be rejected as an unsupported argument")
	}

	removedFields := []string{"namespace", "ingress_host", "cluster_name", "context", "kubeconfig_path"}
	unsupportedArgumentCount := 0
	for _, d := range result.Diagnostics {
		if d.Severity == "error" && d.Summary == "Unsupported argument" {
			unsupportedArgumentCount++
		}
	}
	if unsupportedArgumentCount != len(removedFields) {
		t.Errorf("expected exactly %d \"Unsupported argument\" errors (one per removed field: %v), got %d. Full diagnostics: %+v",
			len(removedFields), removedFields, unsupportedArgumentCount, result.Diagnostics)
	}

	// Cross-check that the errors actually name the removed fields, not some
	// other unrelated schema mismatch that happens to produce the same
	// summary text.
	for _, field := range removedFields {
		found := false
		for _, d := range result.Diagnostics {
			if d.Severity == "error" && d.Summary == "Unsupported argument" && strings.Contains(d.Detail, field) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no \"Unsupported argument\" error named %q. Full diagnostics: %+v", field, result.Diagnostics)
		}
	}
}

// tfApplyJSONLine mirrors the one field we need from each line of
// `terraform apply -json`'s streamed output (Terraform's "JSON UI" line
// protocol - one JSON object per line, several "type"s; we only care about
// "diagnostic" lines).
type tfApplyJSONLine struct {
	Type       string `json:"type"`
	Diagnostic struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
		Detail   string `json:"detail"`
	} `json:"diagnostic"`
}

// runTerraformApplyJSON is runTerraformValidate's apply-time analogue: C9's
// warning is emitted from inside Create() (getOrGenerateCredentials), which
// only ever runs during apply, never during plan/validate. apiURL points the
// provider at a mock backend (see newC3MockCloudServer) so this needs no
// real credentials or infra. Returns every diagnostic line from the
// streamed JSON output.
func runTerraformApplyJSON(t *testing.T, binDir, apiURL, resourceHCL string) []tfApplyJSONLine {
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
  token   = "warning-check-fake-token"
}

%[2]s
`, apiURL, resourceHCL)
	if err := os.WriteFile(filepath.Join(workDir, "main.tf"), []byte(mainTF), 0o600); err != nil {
		t.Fatalf("failed to write scratch main.tf: %v", err)
	}

	cmd := exec.Command("terraform", "apply", "-auto-approve", "-json")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "TF_CLI_CONFIG_FILE="+cliConfigPath)

	out, _ := cmd.CombinedOutput() // apply may exit non-zero; we only care about the diagnostic lines either way

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
		t.Fatalf("no parseable JSON lines from `terraform apply -json`; raw output:\n%s", out)
	}
	return lines
}

// TestAccCloudResource_CredentialPlaceholder_WarningActuallyFires is the corrected C9
// acceptance criterion: an all-in-one cloud whose aws_config is present but
// has no way to derive a credential must produce a real, served warning
// during apply - not just a struct-level "wasPlaceholder" assertion.
func TestAccCloudResource_CredentialPlaceholder_WarningActuallyFires(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	binDir := buildProviderBinaryForCLICheck(t)

	const cloudID = "cld_c9_warning_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "c9-warning-check", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"
	}`, cloudID)
	resourcesJSON := `[]`
	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_mock_default")

	// aws_config is present (so isEmptyCloud is false) but deliberately
	// omits controlplane_iam_role_arn/dataplane_iam_role_arn - the fields
	// getOrGenerateCredentials derives a credential from - simulating a
	// user who configured the block but forgot the actual role.
	resourceHCL := `
resource "anyscale_cloud" "test" {
  name           = "c9-warning-check"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"

  aws_config {
    vpc_id             = "vpc-test123"
    subnet_ids_to_az   = { "subnet-test1" = "us-east-2a" }
    security_group_ids = ["sg-test1"]
  }
}
`
	lines := runTerraformApplyJSON(t, binDir, server.URL, resourceHCL)

	found := false
	for _, l := range lines {
		if l.Type == "diagnostic" && l.Diagnostic.Severity == "warning" &&
			strings.Contains(l.Diagnostic.Summary, "Placeholder Credentials Generated") {
			found = true
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
		t.Errorf("expected a 'Placeholder Credentials Generated' warning, got none. All diagnostics seen: %v", summaries)
	}
}
