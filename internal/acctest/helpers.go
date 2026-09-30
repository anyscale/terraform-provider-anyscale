package acctest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	tfacctest "github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// ProtoV6ProviderFactories are used to instantiate a provider during
// acceptance testing. The factory function will be invoked for every Terraform
// CLI command executed to create a provider server to which the CLI can
// reattach.
var ProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"anyscale": providerserver.NewProtocol6WithError(provider.NewFramework("test")()),
}

// CreateEphemeralTestProjectForCloud creates a minimal disposable project under parentCloudID,
// named with the "tfacc-" prefix so the existing project sweeper cleans it up if test cleanup is
// interrupted. Always pass the specific cloud.ID your test already resolved (e.g. via
// GetTestCloudID/GetAllVMClouds) rather than independently re-resolving one - the backend enforces
// that a project's parent_cloud_id must match whatever cloud a cluster is provisioned on
// (confirmed via a real 403, 2026-07-20 - see check_cloud_id_of_project_and_cluster_match in the
// backend reference), and a mismatch surfaces as an opaque UNHEALTHY, not a clear plan-time error.
// Passing a cloud ID resolved separately from the one your compute_config/service actually targets
// reproduces that exact failure.
func CreateEphemeralTestProjectForCloud(t *testing.T, parentCloudID string) (projectID string, projectName string, err error) {
	client, err := GetTestClient()
	if err != nil {
		return "", "", fmt.Errorf("failed to get test client: %w", err)
	}

	projectName = UniqueName(t, "project")
	t.Logf("Creating ephemeral test project: %s (parent cloud: %s)", projectName, parentCloudID)

	createReq := struct {
		Name          string `json:"name"`
		ParentCloudID string `json:"parent_cloud_id"`
		Description   string `json:"description"`
	}{
		Name:          projectName,
		ParentCloudID: parentCloudID,
		Description:   "Ephemeral project created by terraform-provider-anyscale acceptance tests",
	}

	reqBody, err := json.Marshal(createReq)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal create request: %w", err)
	}

	resp, err := client.DoRequest(context.Background(), "POST", "/api/v2/projects", bytes.NewReader(reqBody))
	if err != nil {
		return "", "", fmt.Errorf("failed to create project: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return "", "", fmt.Errorf("failed to create project (status %d): %s", resp.StatusCode, string(body))
	}

	var projectResp struct {
		Result struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &projectResp); err != nil {
		return "", "", fmt.Errorf("failed to parse project response: %w", err)
	}

	createdID := projectResp.Result.ID
	t.Logf("Created ephemeral test project: %s (ID: %s)", projectName, createdID)

	if os.Getenv("ANYSCALE_TEST_KEEP") == "1" {
		t.Logf("ANYSCALE_TEST_KEEP=1: Project will be preserved after tests")
	} else {
		createdAt := time.Now().Format(time.RFC3339)
		t.Cleanup(func() {
			// Same retry as the resource's own Delete: a delete issued shortly
			// after create can 403 until the owner grant propagates. A final
			// failure only warns - the sweeper is the backstop.
			if delErr := provider.DeleteProjectWithRetry(context.Background(), client, createdID, createdAt); delErr != nil {
				t.Logf("Warning: Failed to delete ephemeral project %s: %v", createdID, delErr)
			}
		})
	}

	return createdID, projectName, nil
}

// GetTestClient returns an authenticated client for testing
func GetTestClient() (*provider.Client, error) {
	// Get API URL from environment or use default
	apiURL := os.Getenv("ANYSCALE_API_URL")
	if apiURL == "" {
		apiURL = "https://console.anyscale.com"
	}

	// Get token from environment or credentials file
	token := os.Getenv("ANYSCALE_CLI_TOKEN")
	if token == "" {
		var err error
		token, err = provider.GetAuthToken()
		if err != nil {
			return nil, fmt.Errorf("failed to get auth token: %w", err)
		}
	}

	if token == "" {
		return nil, fmt.Errorf("no authentication token available")
	}

	return provider.NewClientWithToken(apiURL, token), nil
}

// PreCheck validates that required environment variables are set
// This is a common precheck function that can be used across all acceptance tests
func PreCheck(t *testing.T) {
	// Check for authentication
	token := os.Getenv("ANYSCALE_CLI_TOKEN")
	if token == "" {
		// Try credentials file
		if _, err := provider.GetAuthToken(); err != nil {
			t.Fatalf("ANYSCALE_CLI_TOKEN must be set or ~/.anyscale/credentials.json must exist for acceptance tests")
		}
	}

	ValidateAuth(t)

	// Note: We don't require ANYSCALE_TEST_CLOUD_ID here anymore
	// Tests should use GetTestCloudID() which handles auto-discovery
}

// authT is the subset of *testing.T that validateAuth uses, so its
// fail/skip decision can be unit tested without failing the calling test.
type authT interface {
	Helper()
	Fatalf(format string, args ...any)
	Skipf(format string, args ...any)
	Logf(format string, args ...any)
}

// ValidateAuth probes the Anyscale API with the configured token and FAILS the
// test if the API rejects it (401 or 403). A rejected token must turn CI red:
// skipping instead makes every acceptance test skip, and an all-skip run
// reports green with nothing tested. The test is skipped only when no
// credential source exists at all. A request error is logged and tolerated;
// it will surface in the test itself if it persists.
//
// The live probe result (accepted vs. rejected) is cached for the run once
// definitively known, since this is called from PreCheck on every single
// acceptance test (100+ call sites) and the token's validity doesn't change
// mid-run. A request error is deliberately NOT cached: caching it would let
// one transient network blip suppress the real auth check for every later
// test in the run.
func ValidateAuth(t *testing.T) {
	validateAuth(t)
}

func validateAuth(t authT) {
	t.Helper()
	authProbeMutex.Lock()
	if authProbeDone {
		status := authProbeStatus
		authProbeMutex.Unlock()
		if status != 0 {
			t.Fatalf("Anyscale API rejected the configured token (HTTP %d from /api/v2/clouds); refresh ANYSCALE_CLI_TOKEN or the credentials file", status)
		}
		return
	}
	authProbeMutex.Unlock()

	client, err := GetTestClient()
	if err != nil {
		t.Skipf("No usable Anyscale credentials: %v", err)
		return
	}
	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/clouds", nil)
	if err != nil {
		t.Logf("Auth probe request error (continuing): %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	rejected := resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden
	authProbeMutex.Lock()
	authProbeDone = true
	authProbeStatus = 0
	if rejected {
		authProbeStatus = resp.StatusCode
	}
	authProbeMutex.Unlock()

	if rejected {
		t.Fatalf("Anyscale API rejected the configured token (HTTP %d from /api/v2/clouds); refresh ANYSCALE_CLI_TOKEN or the credentials file", resp.StatusCode)
	}
}

// SkipIfNotAcceptanceTest skips the test if TF_ACC is not set
// This replaces the common pattern at the start of every acceptance test
func SkipIfNotAcceptanceTest(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
	}
}

// SkipIfNoRealInfra skips tests that create real clouds / cloud-resources from
// PLACEHOLDER config (fake IAM ARNs, vpc-test123, AWS example account
// 123456789012). The backend cannot provision against fake credentials — it
// fails with STS AssumeRole 403 / add_resource 500 / client timeout — so these
// tests cannot pass in the placeholder acctest lane regardless of org health.
// They are skipped (loud + tracked) unless ANYSCALE_TEST_REAL_INFRA=1. Real
// end-to-end coverage of cloud/resource creation comes from the make
// test-primary / buildkite e2e lane against real infra.
//
// NOTE: this only unblocks CI; it does not fix the underlying items. The K8S
// compute_stack "was K8S, but now VM" behavior (F2) remains a tracked bug to
// investigate on a real K8S cloud.
func SkipIfNoRealInfra(t *testing.T) {
	t.Helper()
	if os.Getenv("ANYSCALE_TEST_REAL_INFRA") == "1" {
		return
	}
	t.Skip("SKIP(no-real-infra): requires real cloud infra; not runnable in the " +
		"placeholder acctest lane (fake creds -> STS 403 / add_resource 500 / timeout). " +
		"Real coverage via make test-primary / buildkite e2e; set ANYSCALE_TEST_REAL_INFRA=1 to run.")
}

// CaptureResourceAttr captures a resource attribute value for later comparison.
// Useful for verifying that updates happen in-place (ID doesn't change) vs replacement.
// Example usage:
//
//	var originalID string
//	resource.TestStep{
//	    Config: config,
//	    Check: CaptureResourceAttr("my_resource.test", "id", &originalID),
//	}
func CaptureResourceAttr(resourceName, attrName string, value *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		*value = rs.Primary.Attributes[attrName]
		return nil
	}
}

// VerifyResourceAttrUnchanged verifies that a resource attribute hasn't changed from a captured value.
// Useful for verifying that updates happen in-place (ID doesn't change) vs replacement.
// Example usage:
//
//	var originalID string
//	// Step 1: capture ID
//	// Step 2: update config and verify ID unchanged
//	resource.TestStep{
//	    Config: updatedConfig,
//	    Check: VerifyResourceAttrUnchanged("my_resource.test", "id", &originalID),
//	}
func VerifyResourceAttrUnchanged(resourceName, attrName string, originalValue *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		currentValue := rs.Primary.Attributes[attrName]
		if currentValue != *originalValue {
			return fmt.Errorf("%s.%s changed from %q to %q (expected update-in-place, not replacement)",
				resourceName, attrName, *originalValue, currentValue)
		}
		return nil
	}
}

// truncateBody caps s at max characters, appending a marker if it was cut, so
// a raw HTTP error body embedded in a log line or wrapped error doesn't dump
// megabytes of unrelated payload. Shared by every sweeper and CheckDestroy
// helper that surfaces a raw response body.
func truncateBody(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

// testAccProviderBlock returns a provider "anyscale" block pointed at an
// httptest mock server instead of the real Anyscale API, for acceptance
// tests that prove framework-level behavior (plan-emptiness, import,
// lifecycle) against a controlled response shape rather than real infra.
func testAccProviderBlock(serverURL string) string {
	return fmt.Sprintf(`
provider "anyscale" {
  api_url = %[1]q
  token   = "mock-token"
}
`, serverURL)
}

// UniqueName returns a deterministic-prefixed but per-invocation-unique
// test resource name in the form "tfacc-<slug>-<8charrand>". Use this in
// every new acceptance test rather than literal names; literal names
// collide between concurrent CI runs and require manual cleanup.
//
// slug should be a short, lowercase-with-dashes identifier of the test
// purpose, e.g. "cloud-aws-basic" or "project-collab".
func UniqueName(t *testing.T, slug string) string {
	t.Helper()
	suffix := tfacctest.RandStringFromCharSet(8, tfacctest.CharSetAlphaNum)
	return fmt.Sprintf("tfacc-%s-%s", slug, strings.ToLower(suffix))
}
