package acctest

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// CaptureServiceDiagnosticsOnFailure wraps a TestCheckFunc for an anyscale_service resource so a
// check failure triggers one last live GET on the service, logged for post-mortem inspection,
// while the service still exists.
//
// This is NOT the same mechanism as the cloud/project ANYSCALE_TEST_KEEP helpers above. Those
// fixtures are created by a plain API call outside Terraform entirely, so their own t.Cleanup can
// simply skip a delete call under our control. A service under a real-infra resource.Test lifecycle
// is different: it is a genuine Terraform-managed resource, and the testing framework
// (helper/resource/testing_new.go, confirmed against the vendored terraform-plugin-testing v1.16.0
// source) registers its own post-test destroy unconditionally via a plain defer set up before any
// step runs - there is no environment variable or TestCase field that can skip it. So
// ANYSCALE_TEST_KEEP cannot preserve the service itself the way it preserves an ephemeral cloud or
// project. What it can do is capture full diagnostic state while the service still exists, which is
// the actual gap this closes (a prior failure needed a one-off manual API call to inspect).
//
// The wrapped check's error is always returned unchanged - this never masks or alters a real
// failure. When ANYSCALE_TEST_KEEP is unset, this is a no-op passthrough identical to calling check
// directly.
func CaptureServiceDiagnosticsOnFailure(t *testing.T, resourceName string, check resource.TestCheckFunc) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		err := check(s)
		if err == nil || os.Getenv("ANYSCALE_TEST_KEEP") != "1" {
			return err
		}

		rs, ok := s.RootModule().Resources[resourceName]
		if !ok || rs.Primary == nil || rs.Primary.ID == "" {
			t.Logf("ANYSCALE_TEST_KEEP=1: check failed but no %s id is available in state to fetch diagnostics for", resourceName)
			return err
		}

		logServiceDiagnostics(t, rs.Primary.ID)
		return err
	}
}

// logServiceDiagnostics fetches a service by id and logs a whitelisted set of diagnostic fields
// for post-mortem inspection. Best-effort only: a fetch or parse failure just logs and returns,
// since this runs inside an already-failing test and must never itself panic or mask the real
// failure. Line construction is delegated to serviceDiagnosticLines, kept as a pure function
// (no testing.T, no I/O) specifically so the whitelist-safety property is directly unit-testable:
// see TestServiceDiagnosticLines_NeverIncludesRayServeConfigOrSecrets.
func logServiceDiagnostics(t *testing.T, serviceID string) {
	client, err := GetTestClient()
	if err != nil {
		t.Logf("ANYSCALE_TEST_KEEP=1: could not get test client to fetch service %s diagnostics: %v", serviceID, err)
		return
	}

	apiResp, err := provider.DoRequestAndParse[provider.ServiceResponse](
		context.Background(), client, "GET", fmt.Sprintf("/api/v2/services-v2/%s", serviceID), nil, 200,
	)
	if err != nil {
		t.Logf("ANYSCALE_TEST_KEEP=1: failed to fetch service %s diagnostics (service may already be gone): %v", serviceID, err)
		return
	}

	for _, line := range serviceDiagnosticLines(&apiResp.Result) {
		t.Logf("ANYSCALE_TEST_KEEP=1:   %s", line)
	}
	t.Logf("ANYSCALE_TEST_KEEP=1: the service above will still be destroyed automatically when this test ends - ANYSCALE_TEST_KEEP cannot prevent that for a Terraform-managed resource, this is diagnostic capture only")
}

// serviceDiagnosticLines formats a whitelisted set of diagnostic fields from a service API
// response: id/name/project/cloud, hostname/base_url, current_state/goal_state, error_message,
// the per-version identifiers/states/weights, and the status checklist. Deliberately excludes
// ray_serve_config (an open, per-version blob that can carry user-supplied env_vars), build_id,
// and compute_config_id - never format anything beyond this named set, even under
// ANYSCALE_TEST_KEEP=1. A pure function on purpose: no testing.T, no I/O, so the whitelist itself
// is directly unit-testable rather than only observable as printed test output.
func serviceDiagnosticLines(r *provider.ServiceResult) []string {
	lines := []string{
		fmt.Sprintf("service diagnostics for post-mortem - id=%s name=%s project_id=%s cloud_id=%s", r.ID, r.Name, r.ProjectID, r.CloudID),
		fmt.Sprintf("hostname=%s base_url=%s", r.Hostname, r.BaseURL),
		fmt.Sprintf("current_state=%s goal_state=%s", r.CurrentState, r.GoalState),
	}
	if r.ErrorMessage != nil {
		lines = append(lines, fmt.Sprintf("error_message=%s", *r.ErrorMessage))
	}
	lines = append(lines, serviceVersionDiagnosticLines("primary_version", r.PrimaryVersion)...)
	lines = append(lines, serviceVersionDiagnosticLines("canary_version", r.CanaryVersion)...)
	if r.ServiceStatusChecklist != nil {
		for _, item := range r.ServiceStatusChecklist.Shared {
			lines = append(lines, fmt.Sprintf("status_checklist(shared): kind=%s label=%s state=%s message=%s", item.Kind, item.Label, item.State, item.Message))
		}
		for _, group := range r.ServiceStatusChecklist.PerVersion {
			for _, item := range group.Items {
				lines = append(lines, fmt.Sprintf("status_checklist(version_id=%s): kind=%s label=%s state=%s message=%s", group.VersionID, item.Kind, item.Label, item.State, item.Message))
			}
		}
	}
	return lines
}

// serviceVersionDiagnosticLines formats the whitelisted subset of a service version's fields -
// never ray_serve_config, build_id, or compute_config_id, since those are unbounded/config-shaped
// rather than pure diagnostic state.
func serviceVersionDiagnosticLines(label string, v *provider.ServiceVersionResult) []string {
	if v == nil {
		return []string{fmt.Sprintf("%s: absent", label)}
	}
	return []string{fmt.Sprintf("%s: id=%s version=%s current_state=%s weight=%d current_weight=%s target_weight=%s",
		label, v.ID, v.Version, v.CurrentState, v.Weight, int64PtrString(v.CurrentWeight), int64PtrString(v.TargetWeight))}
}

func int64PtrString(p *int64) string {
	if p == nil {
		return "null"
	}
	return fmt.Sprintf("%d", *p)
}
