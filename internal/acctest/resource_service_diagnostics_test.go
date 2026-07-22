package acctest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// serviceState hand-builds the terraform.State CaptureServiceDiagnosticsOnFailure sees for a
// single anyscale_service resource under test, matching how TestAccServiceResource_RealInfra and
// its InPlaceRollout companion declare their resource ("anyscale_service.test").
func serviceState(id string) *terraform.State {
	return &terraform.State{
		Modules: []*terraform.ModuleState{
			{
				Path: []string{"root"},
				Resources: map[string]*terraform.ResourceState{
					"anyscale_service.test": {
						Type: "anyscale_service",
						Primary: &terraform.InstanceState{
							ID:         id,
							Attributes: map[string]string{"id": id},
						},
					},
				},
			},
		},
	}
}

// TestCaptureServiceDiagnosticsOnFailure_NoOpWhenCheckPasses proves the wrapper never touches the
// network when the wrapped check succeeds - there is nothing to diagnose, so it must not spend an
// extra API call on every green run.
func TestCaptureServiceDiagnosticsOnFailure_NoOpWhenCheckPasses(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-diagnostics-passing-check")
	t.Setenv("ANYSCALE_TEST_KEEP", "1")

	passingCheck := func(*terraform.State) error { return nil }
	wrapped := CaptureServiceDiagnosticsOnFailure(t, "anyscale_service.test", passingCheck)

	if err := wrapped(serviceState("service_abc")); err != nil {
		t.Fatalf("expected nil error from a passing check, got: %v", err)
	}
	if requestCount != 0 {
		t.Fatalf("expected 0 diagnostic requests for a passing check, got %d", requestCount)
	}
}

// TestCaptureServiceDiagnosticsOnFailure_NoOpWhenKeepUnset proves default behavior (the env var
// unset, the common case for every CI run and every local run that never sets it) is byte-for-byte
// a passthrough: the original error comes back unchanged and no diagnostic fetch ever fires. This
// is the regression guard for "default behavior unchanged" - the whole reason this feature is
// opt-in.
func TestCaptureServiceDiagnosticsOnFailure_NoOpWhenKeepUnset(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-diagnostics-keep-unset")
	t.Setenv("ANYSCALE_TEST_KEEP", "")

	wantErr := errFixed("current_state was TERMINATED, want RUNNING")
	failingCheck := func(*terraform.State) error { return wantErr }
	wrapped := CaptureServiceDiagnosticsOnFailure(t, "anyscale_service.test", failingCheck)

	gotErr := wrapped(serviceState("service_abc"))
	if gotErr != wantErr {
		t.Fatalf("expected the original error to pass through unchanged, got: %v", gotErr)
	}
	if requestCount != 0 {
		t.Fatalf("expected 0 diagnostic requests when ANYSCALE_TEST_KEEP is unset, got %d - "+
			"this must be a no-op passthrough by default", requestCount)
	}
}

// TestCaptureServiceDiagnosticsOnFailure_FetchesLiveOnFailureWhenKeepSet is the core positive
// proof: ANYSCALE_TEST_KEEP=1 plus a failing check must synchronously fetch the service by the id
// captured from Terraform state, and still return the original error unchanged (never masked).
// "Synchronously" is the load-bearing property here: the wrapper is called during Step processing,
// strictly before the TestCase's own trailing destroy (a plain defer registered in
// terraform-plugin-testing's runNewTest before any step runs, confirmed by reading the vendored
// v1.16.0 source directly) - a real goroutine/async fetch inside this wrapper would break that
// ordering guarantee invisibly, so asserting requestCount==1 immediately after the wrapped call
// returns (no sleep, no wait) is what proves there is no such escape hatch.
func TestCaptureServiceDiagnosticsOnFailure_FetchesLiveOnFailureWhenKeepSet(t *testing.T) {
	var requestCount int
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"id":            "service_abc",
				"name":          "tfacc-diag-test",
				"project_id":    "prj_1",
				"cloud_id":      "cld_1",
				"hostname":      "tfacc-diag-test.example.anyscaleuserdata.com",
				"base_url":      "https://tfacc-diag-test.example.anyscaleuserdata.com",
				"current_state": "UNHEALTHY",
				"goal_state":    "RUNNING",
			},
		})
	}))
	defer server.Close()
	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-diagnostics-keep-set")
	t.Setenv("ANYSCALE_TEST_KEEP", "1")

	wantErr := errFixed("current_state was UNHEALTHY, want RUNNING")
	failingCheck := func(*terraform.State) error { return wantErr }
	wrapped := CaptureServiceDiagnosticsOnFailure(t, "anyscale_service.test", failingCheck)

	gotErr := wrapped(serviceState("service_abc"))
	if gotErr != wantErr {
		t.Fatalf("expected the original error to pass through unchanged, got: %v", gotErr)
	}
	if requestCount != 1 {
		t.Fatalf("expected exactly 1 live diagnostic GET immediately on return (proving a synchronous, pre-destroy fetch, not a background goroutine), got %d", requestCount)
	}
	wantPath := "/api/v2/services-v2/service_abc"
	if requestedPath != wantPath {
		t.Errorf("requested path = %q, want %q - diagnostics must key on the id captured from state", requestedPath, wantPath)
	}
}

// TestCaptureServiceDiagnosticsOnFailure_NoIDInStateIsSafe covers the edge case where the check
// fails before the resource ever landed in state (e.g. a plan-time failure) - there is no id to
// fetch, and the wrapper must degrade to a log line rather than fetching garbage or panicking on a
// nil Primary.
func TestCaptureServiceDiagnosticsOnFailure_NoIDInStateIsSafe(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-diagnostics-no-id")
	t.Setenv("ANYSCALE_TEST_KEEP", "1")

	emptyState := &terraform.State{Modules: []*terraform.ModuleState{{Path: []string{"root"}, Resources: map[string]*terraform.ResourceState{}}}}
	wantErr := errFixed("resource never appeared in state")
	failingCheck := func(*terraform.State) error { return wantErr }
	wrapped := CaptureServiceDiagnosticsOnFailure(t, "anyscale_service.test", failingCheck)

	gotErr := wrapped(emptyState)
	if gotErr != wantErr {
		t.Fatalf("expected the original error to pass through unchanged, got: %v", gotErr)
	}
	if requestCount != 0 {
		t.Fatalf("expected 0 diagnostic requests when no id is available in state, got %d", requestCount)
	}
}

// TestServiceDiagnosticLines_NeverIncludesRayServeConfigOrSecrets is the whitelist-safety proof
// the architect's review required: even when the underlying API response carries a ray_serve_config
// blob containing something that looks exactly like a leaked secret (a user env_var value), the
// formatted diagnostic lines must never surface it - only the named whitelist (ids, state, weights,
// hostname/base_url, status checklist) is ever included.
func TestServiceDiagnosticLines_NeverIncludesRayServeConfigOrSecrets(t *testing.T) {
	const forbidden = "sk-should-never-appear-in-any-log-line"

	weight := int64(100)
	result := &provider.ServiceResult{
		ID:           "service_abc",
		Name:         "tfacc-diag-test",
		ProjectID:    "prj_1",
		CloudID:      "cld_1",
		Hostname:     "tfacc-diag-test.example.anyscaleuserdata.com",
		BaseURL:      "https://tfacc-diag-test.example.anyscaleuserdata.com",
		CurrentState: "UNHEALTHY",
		GoalState:    "RUNNING",
		PrimaryVersion: &provider.ServiceVersionResult{
			ID:              "svcver_1",
			Version:         "1",
			CurrentState:    "UNHEALTHY",
			Weight:          weight,
			BuildID:         "build_1",
			ComputeConfigID: "cpt_1",
			// The exact real shape: env_vars nested inside ray_serve_config.applications[].runtime_env.
			// If any code path ever logged this field directly, the forbidden value below would leak.
			RayServeConfig: json.RawMessage(`{"applications":[{"runtime_env":{"env_vars":{"SOME_TOKEN":"` + forbidden + `"}}}]}`),
		},
	}

	lines := serviceDiagnosticLines(result)

	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, forbidden) {
		t.Fatalf("serviceDiagnosticLines leaked a value from ray_serve_config/env_vars into a diagnostic log line - this must never happen:\n%s", joined)
	}
	if strings.Contains(joined, "ray_serve_config") || strings.Contains(joined, "env_vars") {
		t.Fatalf("serviceDiagnosticLines mentioned ray_serve_config or env_vars at all - the whitelist must exclude this field entirely, not just its secret values:\n%s", joined)
	}
	// Positive check too: prove the whitelist itself isn't accidentally empty (a formatter that
	// logged nothing would also "pass" the checks above without proving anything).
	if !strings.Contains(joined, "svcver_1") || !strings.Contains(joined, "UNHEALTHY") {
		t.Fatalf("expected the whitelisted primary_version fields (id, current_state) to be present, got:\n%s", joined)
	}
}

// errFixed is a trivial error type so tests can assert on identity (==) rather than string
// matching, ruling out any accidental error-wrapping/rewriting inside the wrapper under test.
type errFixed string

func (e errFixed) Error() string { return string(e) }
