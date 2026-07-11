package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
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

var (
	// Cache for test cloud ID to avoid repeated API calls
	cachedTestCloudID   string
	cachedTestCloudName string
	cloudIDMutex        sync.Mutex

	// Cache for any cloud ID (fallback for data source tests)
	cachedAnyCloudID string
	anyCloudIDMutex  sync.Mutex

	// Cache for GetAllConfiguredClouds - avoids repeating its list-clouds-then
	// per-cloud-resources-check API calls on every call site across a test run.
	cachedAllConfiguredClouds []CloudInfo
	allConfiguredCloudsCached bool
	allConfiguredCloudsMutex  sync.Mutex

	// Cache for ValidateAuthOrSkip's live probe - see its doc comment for why
	// only a definitive answer (not a request error) is cached.
	authProbeDone    bool
	authProbeInvalid bool
	authProbeMutex   sync.Mutex

	// Track ephemeral clouds created by tests for cleanup. Keyed by cloud ID
	// so concurrent createEphemeralTestCloud calls do not clobber each other.
	ephemeralClouds      = map[string]ephemeralCloud{}
	ephemeralCloudsMutex sync.Mutex
)

type ephemeralCloud struct {
	ID   string
	Name string
}

// defaultKnownGoodCloudName identifies a real, healthy, static fixture cloud in
// the Anyscale test org. It is the resolution fallback (after env overrides,
// before auto-discovery) so cloud-dependent acceptance tests get a healthy
// cloud with zero setup, locally and in CI. Only the NAME is stored — the repo
// is public, so the cloud ID is resolved from the name at runtime, never
// hardcoded. Resolution falls through to auto-discovery if the name does not
// resolve in the current org. Override per-run with ANYSCALE_TEST_CLOUD_ID or
// ANYSCALE_TEST_CLOUD_NAME.
const defaultKnownGoodCloudName = "tfp-test-aws-useast1-STATIC"

// resolveDefaultKnownGoodCloudID resolves defaultKnownGoodCloudName to a cloud
// ID via the API. Returns "" (caller falls through to auto-discovery) when the
// name cannot be resolved in the current org. The ID is deliberately not
// hardcoded in the repo.
func resolveDefaultKnownGoodCloudID(t *testing.T) string {
	id, err := resolveCloudNameToID(t, defaultKnownGoodCloudName)
	if err != nil {
		return ""
	}
	return id
}

// GetTestCloudID returns a test cloud ID with the following priority:
// 1. ANYSCALE_TEST_CLOUD_ID environment variable (explicit override)
// 2. ANYSCALE_TEST_CLOUD_NAME environment variable (resolve name to ID)
// 3. Known-good static fixture cloud (validated; falls through if absent)
// 4. Auto-discover any available cloud (prefers test-named clouds)
//
// The result is cached after the first successful resolution.
// Unlike sync.Once, this will retry on failure.
// Cached values are validated to ensure the cloud still exists.
func GetTestCloudID(t *testing.T) string {
	cloudIDMutex.Lock()
	defer cloudIDMutex.Unlock()

	// Return cached value if available and still valid
	if cachedTestCloudID != "" {
		if validateCloudExists(cachedTestCloudID) {
			return cachedTestCloudID
		}
		// Cached cloud no longer exists, clear cache and re-discover
		t.Logf("Cached cloud ID %s no longer exists, clearing cache and re-discovering", cachedTestCloudID)
		cachedTestCloudID = ""
		cachedTestCloudName = ""
	}

	var cloudID string
	var err error

	// Priority 1: Explicit cloud ID
	if envCloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID"); envCloudID != "" {
		t.Logf("Using test cloud ID from ANYSCALE_TEST_CLOUD_ID: %s", envCloudID)
		cachedTestCloudID = envCloudID
		return cachedTestCloudID
	}

	// Priority 2: Cloud name to resolve
	if envCloudName := os.Getenv("ANYSCALE_TEST_CLOUD_NAME"); envCloudName != "" {
		t.Logf("Resolving test cloud name from ANYSCALE_TEST_CLOUD_NAME: %s", envCloudName)
		cloudID, err = resolveCloudNameToID(t, envCloudName)
		if err != nil {
			t.Logf("Warning: Failed to resolve cloud name '%s': %v", envCloudName, err)
		} else {
			cachedTestCloudID = cloudID
			cachedTestCloudName = envCloudName
			return cachedTestCloudID
		}
	}

	// Priority 3: Known-good static fixture cloud, resolved by NAME at runtime
	// (ID not hardcoded). Gives every run a healthy cloud with zero setup;
	// falls through if the name does not resolve in the current org.
	if id := resolveDefaultKnownGoodCloudID(t); id != "" {
		t.Logf("Using default known-good test cloud: %s (%s)", defaultKnownGoodCloudName, id)
		cachedTestCloudID = id
		cachedTestCloudName = defaultKnownGoodCloudName
		return cachedTestCloudID
	}
	t.Logf("Default known-good cloud %q did not resolve in this org; falling through to auto-discovery", defaultKnownGoodCloudName)

	// Priority 4: Auto-discover
	t.Logf("Auto-discovering test cloud...")
	var cloudName string
	cloudID, cloudName, err = autoDiscoverTestCloud(t)
	if err != nil {
		t.Logf("Warning: Failed to auto-discover test cloud: %v", err)
		t.Skip("No test cloud ID available. Set ANYSCALE_TEST_CLOUD_ID or ANYSCALE_TEST_CLOUD_NAME, or ensure at least one cloud exists in the account.")
	}

	cachedTestCloudID = cloudID
	cachedTestCloudName = cloudName
	return cachedTestCloudID
}

// GetTestCloudName returns a test cloud name with the following priority:
// 1. ANYSCALE_TEST_CLOUD_NAME environment variable (explicit override, validated to exist)
// 2. Auto-discover any available cloud and return its name
//
// This function ensures GetTestCloudID has been called first to populate the cache.
// Cached values are validated to ensure the cloud still exists.
func GetTestCloudName(t *testing.T) string {
	cloudIDMutex.Lock()
	defer cloudIDMutex.Unlock()

	// If we have a cached name and ID, validate the cloud still exists
	if cachedTestCloudName != "" && cachedTestCloudID != "" {
		if validateCloudExists(cachedTestCloudID) {
			return cachedTestCloudName
		}
		// Cached cloud no longer exists, clear cache and re-discover
		t.Logf("Cached cloud %s (ID: %s) no longer exists, clearing cache and re-discovering", cachedTestCloudName, cachedTestCloudID)
		cachedTestCloudID = ""
		cachedTestCloudName = ""
	}

	// Priority 1: Explicit cloud name from environment (validate it exists)
	if envCloudName := os.Getenv("ANYSCALE_TEST_CLOUD_NAME"); envCloudName != "" {
		t.Logf("Validating test cloud name from ANYSCALE_TEST_CLOUD_NAME: %s", envCloudName)
		cloudID, err := resolveCloudNameToID(t, envCloudName)
		if err != nil {
			t.Logf("Warning: Failed to resolve cloud name '%s': %v", envCloudName, err)
			// Fall through to auto-discovery
		} else {
			cachedTestCloudID = cloudID
			cachedTestCloudName = envCloudName
			return cachedTestCloudName
		}
	}

	// Priority 2: Known-good static fixture cloud, resolved by NAME at runtime.
	if id := resolveDefaultKnownGoodCloudID(t); id != "" {
		t.Logf("Using default known-good test cloud for name: %s", defaultKnownGoodCloudName)
		cachedTestCloudID = id
		cachedTestCloudName = defaultKnownGoodCloudName
		return cachedTestCloudName
	}

	// Priority 3: Auto-discover (this will populate both ID and Name caches)
	t.Logf("Auto-discovering test cloud for name...")
	cloudID, cloudName, err := autoDiscoverTestCloud(t)
	if err != nil {
		t.Logf("Warning: Failed to auto-discover test cloud: %v", err)
		t.Skip("No test cloud name available. Set ANYSCALE_TEST_CLOUD_NAME or ensure at least one cloud exists in the account.")
	}

	cachedTestCloudID = cloudID
	cachedTestCloudName = cloudName
	return cachedTestCloudName
}

// validateCloudExists checks if a cloud with the given ID exists in the API
func validateCloudExists(cloudID string) bool {
	client, err := GetTestClient()
	if err != nil {
		return false
	}

	resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == 200
}

// resolveCloudNameToID resolves a cloud name to its ID by querying the API
func resolveCloudNameToID(t *testing.T, cloudName string) (string, error) {
	client, err := GetTestClient()
	if err != nil {
		return "", fmt.Errorf("failed to get test client: %w", err)
	}

	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/clouds", nil)
	if err != nil {
		return "", fmt.Errorf("failed to list clouds: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var cloudsResp struct {
		Results []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			CreatedAt string `json:"created_at"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &cloudsResp); err != nil {
		return "", fmt.Errorf("failed to parse clouds response: %w", err)
	}

	// Find matching cloud(s) - if multiple exist, use the most recent
	var matchedCloudID string
	var latestCreatedAt string

	for _, cloud := range cloudsResp.Results {
		if cloud.Name == cloudName {
			if matchedCloudID == "" || cloud.CreatedAt > latestCreatedAt {
				matchedCloudID = cloud.ID
				latestCreatedAt = cloud.CreatedAt
			}
		}
	}

	if matchedCloudID == "" {
		return "", fmt.Errorf("no cloud found with name '%s'", cloudName)
	}

	matchCount := 0
	for _, cloud := range cloudsResp.Results {
		if cloud.Name == cloudName {
			matchCount++
		}
	}
	if matchCount > 1 {
		t.Logf("Warning: Multiple clouds (%d) found with name '%s', using most recent: %s", matchCount, cloudName, matchedCloudID)
	}

	t.Logf("Resolved cloud name '%s' to ID: %s", cloudName, matchedCloudID)
	return matchedCloudID, nil
}

// createEphemeralTestCloud creates a minimal empty cloud for testing.
// The cloud will be cleaned up after tests unless ANYSCALE_TEST_KEEP=1 is set.
// Returns the cloud ID and name.
func createEphemeralTestCloud(t *testing.T) (cloudID string, cloudName string, err error) {
	client, err := GetTestClient()
	if err != nil {
		return "", "", fmt.Errorf("failed to get test client: %w", err)
	}

	// Generate a unique cloud name
	cloudName = fmt.Sprintf("tfacc-ephemeral-%d", time.Now().UnixNano())

	t.Logf("Creating ephemeral test cloud: %s", cloudName)

	// Create minimal empty cloud request. The API requires a credentials
	// value even for an empty cloud with no resource attached; an obviously
	// fake placeholder ARN is fine since nothing ever assumes this role. This
	// mirrors the exact placeholder format resource_cloud.go's
	// getOrGenerateCredentials generates for the same empty-cloud pattern.
	createReq := struct {
		Name        string `json:"name"`
		Provider    string `json:"provider"`
		Region      string `json:"region"`
		Credentials string `json:"credentials"`
	}{
		Name:        cloudName,
		Provider:    "AWS",
		Region:      "us-east-2",
		Credentials: fmt.Sprintf("arn:aws:iam::000000000000:role/%s", cloudName),
	}

	reqBody, err := json.Marshal(createReq)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal create request: %w", err)
	}

	resp, err := client.DoRequest(context.Background(), "POST", "/api/v2/clouds", strings.NewReader(string(reqBody)))
	if err != nil {
		return "", "", fmt.Errorf("failed to create cloud: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return "", "", fmt.Errorf("failed to create cloud (status %d): %s", resp.StatusCode, string(body))
	}

	var cloudResp struct {
		Result struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &cloudResp); err != nil {
		return "", "", fmt.Errorf("failed to parse cloud response: %w", err)
	}

	createdID := cloudResp.Result.ID
	createdName := cloudResp.Result.Name

	ephemeralCloudsMutex.Lock()
	ephemeralClouds[createdID] = ephemeralCloud{ID: createdID, Name: createdName}
	ephemeralCloudsMutex.Unlock()

	t.Logf("Created ephemeral test cloud: %s (ID: %s)", createdName, createdID)

	if os.Getenv("ANYSCALE_TEST_KEEP") == "1" {
		t.Logf("ANYSCALE_TEST_KEEP=1: Cloud will be preserved after tests")
	} else {
		t.Cleanup(func() {
			cleanupEphemeralCloud(t, createdID)
		})
	}

	return createdID, createdName, nil
}

// cleanupEphemeralCloud deletes a specific ephemeral cloud created for testing.
func cleanupEphemeralCloud(t *testing.T, cloudID string) {
	if cloudID == "" {
		return
	}

	ephemeralCloudsMutex.Lock()
	ec, tracked := ephemeralClouds[cloudID]
	ephemeralCloudsMutex.Unlock()
	if !tracked {
		return
	}

	// Invalidate caches that reference this cloud so subsequent tests don't reuse a deleted ID.
	cloudIDMutex.Lock()
	if cachedTestCloudID == cloudID {
		cachedTestCloudID = ""
		cachedTestCloudName = ""
	}
	cloudIDMutex.Unlock()

	anyCloudIDMutex.Lock()
	if cachedAnyCloudID == cloudID {
		cachedAnyCloudID = ""
	}
	anyCloudIDMutex.Unlock()

	t.Logf("Cleaning up ephemeral test cloud: %s (ID: %s)", ec.Name, ec.ID)

	client, err := GetTestClient()
	if err != nil {
		t.Logf("Warning: Failed to get client for cleanup: %v", err)
		return
	}

	resp, err := client.DoRequest(context.Background(), "DELETE", fmt.Sprintf("/api/v2/clouds/%s", ec.ID), nil)
	if err != nil {
		t.Logf("Warning: Failed to delete ephemeral cloud: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 200 || resp.StatusCode == 204 || resp.StatusCode == 404 {
		t.Logf("Successfully cleaned up ephemeral cloud: %s", ec.Name)
		ephemeralCloudsMutex.Lock()
		delete(ephemeralClouds, cloudID)
		ephemeralCloudsMutex.Unlock()
	} else {
		body, _ := io.ReadAll(resp.Body)
		t.Logf("Warning: Failed to delete ephemeral cloud (status %d): %s", resp.StatusCode, string(body))
	}
}

// autoDiscoverTestCloud attempts to find a suitable test cloud automatically.
// If no clouds exist and ANYSCALE_TEST_CREATE_CLOUD=1 is set, creates an ephemeral cloud.
// Returns both the cloud ID and name.
func autoDiscoverTestCloud(t *testing.T) (cloudID string, cloudName string, err error) {
	client, err := GetTestClient()
	if err != nil {
		return "", "", fmt.Errorf("failed to get test client: %w", err)
	}

	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/clouds", nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to list clouds: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read response: %w", err)
	}

	var cloudsResp struct {
		Results []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			CreatedAt string `json:"created_at"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &cloudsResp); err != nil {
		return "", "", fmt.Errorf("failed to parse clouds response: %w", err)
	}

	// Look for clouds with test-related names (prefer "tfprovider" prefix)
	// Fall back to any cloud if no test-specific clouds are found
	var testClouds []struct {
		ID        string
		Name      string
		CreatedAt string
		Priority  int
	}

	for _, cloud := range cloudsResp.Results {
		nameLower := strings.ToLower(cloud.Name)
		priority := 1 // Default priority for any cloud

		if strings.Contains(nameLower, "tfprovider") {
			priority = 10 // Highest priority
		} else if strings.HasPrefix(nameLower, "tf-acc-") || strings.HasPrefix(nameLower, "tfacc-") {
			priority = 9
		} else if strings.Contains(nameLower, "test") {
			priority = 5
		}

		testClouds = append(testClouds, struct {
			ID        string
			Name      string
			CreatedAt string
			Priority  int
		}{
			ID:        cloud.ID,
			Name:      cloud.Name,
			CreatedAt: cloud.CreatedAt,
			Priority:  priority,
		})
	}

	if len(testClouds) == 0 {
		// No clouds exist - try to create an ephemeral one if enabled
		if os.Getenv("ANYSCALE_TEST_CREATE_CLOUD") == "1" {
			t.Logf("No clouds found, ANYSCALE_TEST_CREATE_CLOUD=1: Creating ephemeral test cloud...")
			return createEphemeralTestCloud(t)
		}
		return "", "", fmt.Errorf("no clouds found in the account (set ANYSCALE_TEST_CREATE_CLOUD=1 to auto-create)")
	}

	// Sort by priority (highest first), then by created_at (most recent first)
	bestCloud := testClouds[0]
	for _, cloud := range testClouds {
		if cloud.Priority > bestCloud.Priority ||
			(cloud.Priority == bestCloud.Priority && cloud.CreatedAt > bestCloud.CreatedAt) {
			bestCloud = cloud
		}
	}

	if bestCloud.Priority == 1 {
		t.Logf("Auto-discovered cloud (no test-specific cloud found): %s (ID: %s)", bestCloud.Name, bestCloud.ID)
	} else {
		t.Logf("Auto-discovered test cloud: %s (ID: %s)", bestCloud.Name, bestCloud.ID)
	}
	if len(testClouds) > 1 {
		t.Logf("Note: Found %d clouds, selected '%s' based on priority and recency", len(testClouds), bestCloud.Name)
	}

	return bestCloud.ID, bestCloud.Name, nil
}

// GetAnyCloudID returns any available cloud ID from the account.
// This is useful for data source tests that just need a valid cloud to query.
// The result is cached after the first successful call.
func GetAnyCloudID(t *testing.T) string {
	anyCloudIDMutex.Lock()
	defer anyCloudIDMutex.Unlock()

	// Return cached value if available
	if cachedAnyCloudID != "" {
		return cachedAnyCloudID
	}

	client, err := GetTestClient()
	if err != nil {
		t.Logf("Warning: Failed to get test client: %v", err)
		t.Skip("No cloud available - failed to get test client.")
		return ""
	}

	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/clouds", nil)
	if err != nil {
		t.Logf("Warning: Failed to list clouds: %v", err)
		t.Skip("No cloud available - failed to list clouds.")
		return ""
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Logf("Warning: API returned status %d: %s", resp.StatusCode, string(body))
		t.Skip("No cloud available - API error.")
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Logf("Warning: Failed to read response: %v", err)
		t.Skip("No cloud available - failed to read response.")
		return ""
	}

	var cloudsResp struct {
		Results []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &cloudsResp); err != nil {
		t.Logf("Warning: Failed to parse clouds response: %v", err)
		t.Skip("No cloud available - failed to parse response.")
		return ""
	}

	if len(cloudsResp.Results) == 0 {
		t.Skip("No cloud available - no clouds found in the account.")
		return ""
	}

	// Return the first available cloud
	cachedAnyCloudID = cloudsResp.Results[0].ID
	t.Logf("Using cloud for data source test: %s (ID: %s)", cloudsResp.Results[0].Name, cachedAnyCloudID)
	return cachedAnyCloudID
}

// CloudInfo contains information about a discovered cloud
type CloudInfo struct {
	ID           string
	Name         string
	Provider     string // "AWS" or "GCP"
	ComputeStack string // "VM" or "K8S"
}

// IsK8s returns true if this cloud uses Kubernetes compute stack
func (c CloudInfo) IsK8s() bool {
	return c.ComputeStack == "K8S"
}

// IsVM returns true if this cloud uses VM compute stack
func (c CloudInfo) IsVM() bool {
	return c.ComputeStack == "VM" || c.ComputeStack == ""
}

// InstanceTypes returns appropriate instance types for the cloud provider.
// For K8S clouds, returns empty values - K8S instance types are defined by the
// operator pod shapes, not by the cloud provider's VM instance types.
// TODO: Add K8S pod shape support when operator-defined shapes are available via API.
func (c CloudInfo) InstanceTypes() InstanceTypeSet {
	// K8S clouds use operator-defined pod shapes, not cloud provider instance types
	if c.IsK8s() {
		return InstanceTypeSet{
			// K8S instance types are defined by the operator, not the cloud provider.
			// These are placeholders - tests requiring instance types should skip K8S clouds.
			Small:        "",
			Medium:       "",
			Large:        "",
			XLarge:       "",
			Zones:        nil,
			Provider:     c.Provider,
			ComputeStack: "K8S",
		}
	}

	if c.Provider == "GCP" {
		return InstanceTypeSet{
			Small:        "n2-standard-2",
			Medium:       "n2-standard-4",
			Large:        "n2-standard-8",
			XLarge:       "n2-standard-16",
			Zones:        []string{"us-central1-a", "us-central1-b"},
			Provider:     "GCP",
			ComputeStack: "VM",
		}
	}
	// Default to AWS
	return InstanceTypeSet{
		Small:        "m5.large",
		Medium:       "m5.xlarge",
		Large:        "m5.2xlarge",
		XLarge:       "m5.4xlarge",
		Zones:        []string{"us-west-2a", "us-west-2b"},
		Provider:     "AWS",
		ComputeStack: "VM",
	}
}

// InstanceTypeSet contains instance types for a specific cloud provider
type InstanceTypeSet struct {
	Small        string   // 2 vCPU equivalent
	Medium       string   // 4 vCPU equivalent
	Large        string   // 8 vCPU equivalent
	XLarge       string   // 16 vCPU equivalent
	Zones        []string // Example availability zones
	Provider     string
	ComputeStack string // "VM" or "K8S"
}

// IsValid returns true if this instance type set has valid instance types.
// K8S clouds return empty instance types since they use operator-defined pod shapes.
func (i InstanceTypeSet) IsValid() bool {
	return i.Small != "" && i.Medium != ""
}

// normalizeComputeStack returns a normalized compute stack value.
// Empty string defaults to "VM" for backwards compatibility.
func normalizeComputeStack(computeStack string) string {
	if computeStack == "" {
		return "VM"
	}
	return computeStack
}

// isKnownProvider returns true if the provider is a known cloud provider (AWS, GCP, or Generic for K8S).
func isKnownProvider(provider, computeStack string) bool {
	if provider == "AWS" || provider == "GCP" {
		return true
	}
	// Generic provider is only valid for K8S compute stack
	if provider == "Generic" && computeStack == "K8S" {
		return true
	}
	return false
}

// GetConfiguredCloud returns a cloud that has cloud resources configured.
// This is required for tests that attach machine pools or need a fully configured cloud.
// Returns CloudInfo so tests can adapt instance types based on provider.
// Falls back to any cloud with a known provider if no fully configured clouds exist.
func GetConfiguredCloud(t *testing.T) CloudInfo {
	client, err := GetTestClient()
	if err != nil {
		t.Skip("No configured cloud available - failed to get test client.")
		return CloudInfo{}
	}

	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/clouds", nil)
	if err != nil {
		t.Skip("No configured cloud available - failed to list clouds.")
		return CloudInfo{}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		t.Skip("No configured cloud available - API error.")
		return CloudInfo{}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Skip("No configured cloud available - failed to read response.")
		return CloudInfo{}
	}

	var cloudsResp struct {
		Results []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Provider       string `json:"provider"`
			ComputeStack   string `json:"compute_stack"`
			CloudResources []struct {
				ID string `json:"id"`
			} `json:"cloud_resources"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &cloudsResp); err != nil {
		t.Skip("No configured cloud available - failed to parse response.")
		return CloudInfo{}
	}

	// Priority 1: Look for VM clouds with cloud resources configured (best for compute config tests)
	for _, cloud := range cloudsResp.Results {
		if len(cloud.CloudResources) > 0 && (cloud.ComputeStack == "VM" || cloud.ComputeStack == "") {
			t.Logf("Found configured VM cloud: %s (ID: %s, provider: %s, compute_stack: %s)", cloud.Name, cloud.ID, cloud.Provider, cloud.ComputeStack)
			return CloudInfo{
				ID:           cloud.ID,
				Name:         cloud.Name,
				Provider:     cloud.Provider,
				ComputeStack: normalizeComputeStack(cloud.ComputeStack),
			}
		}
	}

	// Priority 2: Look for any cloud with cloud resources configured (including K8S)
	for _, cloud := range cloudsResp.Results {
		if len(cloud.CloudResources) > 0 {
			t.Logf("Found configured cloud: %s (ID: %s, provider: %s, compute_stack: %s)", cloud.Name, cloud.ID, cloud.Provider, cloud.ComputeStack)
			return CloudInfo{
				ID:           cloud.ID,
				Name:         cloud.Name,
				Provider:     cloud.Provider,
				ComputeStack: normalizeComputeStack(cloud.ComputeStack),
			}
		}
	}

	// Priority 3: Fall back to any VM cloud with a known provider (AWS, GCP)
	for _, cloud := range cloudsResp.Results {
		computeStack := normalizeComputeStack(cloud.ComputeStack)
		if isKnownProvider(cloud.Provider, computeStack) && computeStack == "VM" {
			t.Logf("Found VM cloud without cloud_resources (may not work for all tests): %s (ID: %s, provider: %s)", cloud.Name, cloud.ID, cloud.Provider)
			return CloudInfo{
				ID:           cloud.ID,
				Name:         cloud.Name,
				Provider:     cloud.Provider,
				ComputeStack: computeStack,
			}
		}
	}

	// Priority 4: Fall back to any cloud with a known provider (including K8S and Generic)
	for _, cloud := range cloudsResp.Results {
		computeStack := normalizeComputeStack(cloud.ComputeStack)
		if isKnownProvider(cloud.Provider, computeStack) {
			t.Logf("Found cloud without cloud_resources (may not work for all tests): %s (ID: %s, provider: %s, compute_stack: %s)", cloud.Name, cloud.ID, cloud.Provider, computeStack)
			return CloudInfo{
				ID:           cloud.ID,
				Name:         cloud.Name,
				Provider:     cloud.Provider,
				ComputeStack: computeStack,
			}
		}
	}

	// Priority 5: Fall back to any cloud, defaulting to AWS instance types
	if len(cloudsResp.Results) > 0 {
		cloud := cloudsResp.Results[0]
		provider := cloud.Provider
		if provider == "" {
			provider = "AWS" // Default to AWS instance types
		}
		t.Logf("Found cloud without known provider (using %s defaults): %s (ID: %s, compute_stack: %s)", provider, cloud.Name, cloud.ID, cloud.ComputeStack)
		return CloudInfo{
			ID:           cloud.ID,
			Name:         cloud.Name,
			Provider:     provider,
			ComputeStack: normalizeComputeStack(cloud.ComputeStack),
		}
	}

	t.Skip("No configured cloud available - no clouds found in the account.")
	return CloudInfo{}
}

// GetConfiguredCloudID returns a cloud ID that has cloud resources configured.
// This is required for tests that attach machine pools or need a fully configured cloud.
// Falls back to any cloud if no fully configured clouds exist.
func GetConfiguredCloudID(t *testing.T) string {
	cloud := GetConfiguredCloud(t)
	return cloud.ID
}

// GetAllConfiguredClouds returns all clouds that have cloud resources configured.
// This is useful for running tests across multiple cloud types (AWS VM, GCP VM, AWS K8S, etc.).
// Returns an empty slice if no clouds are available.
//
// The result is cached for the duration of the test binary run (same
// assumption GetTestCloudID/GetAnyCloudID already make: acceptance tests
// don't mutate the shared cloud fleet mid-run outside their own tracked
// ephemeral clouds), since this does a full list-clouds call plus one
// per-cloud resources check for every candidate.
func GetAllConfiguredClouds(t *testing.T) []CloudInfo {
	allConfiguredCloudsMutex.Lock()
	defer allConfiguredCloudsMutex.Unlock()

	if allConfiguredCloudsCached {
		return cachedAllConfiguredClouds
	}

	client, err := GetTestClient()
	if err != nil {
		t.Logf("Failed to get test client: %v", err)
		return nil
	}

	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/clouds", nil)
	if err != nil {
		t.Logf("Failed to list clouds: %v", err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		t.Logf("API returned status %d", resp.StatusCode)
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Logf("Failed to read response: %v", err)
		return nil
	}

	var cloudsResp struct {
		Results []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Provider     string `json:"provider"`
			ComputeStack string `json:"compute_stack"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &cloudsResp); err != nil {
		t.Logf("Failed to parse response: %v", err)
		return nil
	}

	var clouds []CloudInfo
	allDefinitive := true

	// Collect all clouds with a healthy resource. GET /api/v2/clouds never
	// embeds cloud_resources inline (confirmed against the real API: the key
	// is simply absent from each result, not an empty array) - a prior
	// version of this function checked that inline field directly, which
	// meant the len(...) > 0 condition could never be true for ANY cloud in
	// ANY org, so TestAccComputeConfigResource_Basic/_Disappears silently
	// skipped everywhere, including CI, regardless of how many healthy
	// clouds actually existed. Resource-health now comes from the real
	// per-cloud endpoint instead.
	for _, cloud := range cloudsResp.Results {
		computeStack := normalizeComputeStack(cloud.ComputeStack)
		if !isKnownProvider(cloud.Provider, computeStack) {
			continue
		}
		hasResources, definitive := cloudHasResources(client, cloud.ID)
		if !definitive {
			// Transient failure, not a confirmed absence of resources - do
			// not let this round's result get cached (see below), so a
			// later call gets a real chance to see this cloud once the
			// blip clears, instead of it being silently excluded forever.
			allDefinitive = false
			t.Logf("  skipping %s (ID: %s): could not determine resource status (transient error), not caching this result", cloud.Name, cloud.ID)
			continue
		}
		if !hasResources {
			t.Logf("  skipping %s (ID: %s): no cloud resources configured", cloud.Name, cloud.ID)
			continue
		}
		clouds = append(clouds, CloudInfo{
			ID:           cloud.ID,
			Name:         cloud.Name,
			Provider:     cloud.Provider,
			ComputeStack: computeStack,
		})
	}

	// Intentionally no fallback to clouds without cloud_resources: creating a
	// compute config against a cloud that lacks a healthy primary cloud resource
	// returns a backend 500. Returning an empty slice lets callers skip cleanly
	// rather than hard-fail on a degraded cloud.
	//
	// We also intentionally do NOT substitute the static fixture here, so
	// TestAccComputeConfigResource_Basic/_Disappears (which iterate
	// GetAllVMClouds) skip rather than run against a cloud with no healthy
	// resource. _Disappears exposes a separate unresolved issue: it archives
	// the config out-of-band and expects a non-empty plan, but the
	// compute-config Read returns an archived config as still-present, so the
	// disappearance is not detected ("expected non-empty plan, got empty").
	// That needs the provider Read to treat archived_at as gone (tracked,
	// forge lane).

	t.Logf("Found %d configured clouds for testing", len(clouds))
	for _, c := range clouds {
		t.Logf("  - %s (ID: %s, provider: %s, compute_stack: %s)", c.Name, c.ID, c.Provider, c.ComputeStack)
	}

	// Only a genuine, fully-definitive resolution is cached - the early
	// returns above (client/list/read/parse failure) and any per-cloud
	// transient resources-check failure (allDefinitive) must not be baked
	// in, so a later call gets a real chance to succeed instead of a
	// transient blip silently and permanently excluding a healthy cloud.
	if allDefinitive {
		cachedAllConfiguredClouds = clouds
		allConfiguredCloudsCached = true
	}
	return clouds
}

// cloudHasResources reports whether cloudID has at least one cloud resource
// configured, via GET /api/v2/clouds/{id}/resources, and whether that answer
// is definitive. The list-clouds endpoint (GET /api/v2/clouds) never embeds
// cloud_resources inline, so this dedicated per-cloud lookup is the only
// reliable way to detect a usable cloud.
//
// definitive is false on any request/read/parse error or non-200 - the
// caller must treat that as "unknown", not as a confirmed absence of
// resources: GetAllConfiguredClouds caches its overall result, and baking an
// inconclusive per-cloud answer into that cache would silently and
// permanently exclude a genuinely healthy cloud after one transient blip,
// for the rest of the whole test binary run (100+ call sites), rather than
// just costing that one call the way it did before caching existed.
func cloudHasResources(client *provider.Client, cloudID string) (hasResources bool, definitive bool) {
	resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), nil)
	if err != nil {
		return false, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return false, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, false
	}
	var resourcesResp struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resourcesResp); err != nil {
		return false, false
	}
	return len(resourcesResp.Results) > 0, true
}

// GetComputeConfigCloudID returns the ID of a cloud suitable for creating
// compute configs: one with at least one cloud resource (a proxy for a healthy
// primary cloud resource). POST /api/v2/compute_templates/ returns a backend
// 500 for clouds lacking a healthy primary resource, so when none are available
// the test is skipped rather than hard-failing. An explicit ANYSCALE_TEST_CLOUD_ID
// override is honored first (the operator is asserting that cloud is healthy).
func GetComputeConfigCloudID(t *testing.T) string {
	if id := os.Getenv("ANYSCALE_TEST_CLOUD_ID"); id != "" {
		return id
	}
	// Known-good static fixture (resolved by name) before auto-discovery.
	if id := resolveDefaultKnownGoodCloudID(t); id != "" {
		t.Logf("Using default known-good cloud for compute config: %s (%s)", defaultKnownGoodCloudName, id)
		return id
	}
	for _, c := range GetAllConfiguredClouds(t) {
		if c.IsVM() {
			return c.ID
		}
	}
	t.Skip("No VM cloud with a healthy primary cloud resource available; " +
		"compute config creation returns a backend 500 on degraded clouds. " +
		"Set ANYSCALE_TEST_CLOUD_ID to a healthy cloud to run this test.")
	return ""
}

// GetComputeConfigCloudName is like GetComputeConfigCloudID but returns the
// cloud name, for tests that reference a cloud by name. Honors
// ANYSCALE_TEST_CLOUD_NAME first.
func GetComputeConfigCloudName(t *testing.T) string {
	if name := os.Getenv("ANYSCALE_TEST_CLOUD_NAME"); name != "" {
		return name
	}
	// Known-good static fixture (resolved by name) before auto-discovery.
	if resolveDefaultKnownGoodCloudID(t) != "" {
		return defaultKnownGoodCloudName
	}
	for _, c := range GetAllConfiguredClouds(t) {
		if c.IsVM() {
			return c.Name
		}
	}
	t.Skip("No VM cloud with a healthy primary cloud resource available; " +
		"compute config creation returns a backend 500 on degraded clouds. " +
		"Set ANYSCALE_TEST_CLOUD_NAME to a healthy cloud to run this test.")
	return ""
}

// GetAllVMClouds returns one VM cloud per provider (AWS, GCP).
// This deduplicates clouds so tests run once per provider type, not once per cloud.
// This is useful for tests that require VM-specific instance types.
func GetAllVMClouds(t *testing.T) []CloudInfo {
	allClouds := GetAllConfiguredClouds(t)

	// Deduplicate by provider - we only need one cloud per provider type
	// since instance types are the same for all clouds of the same provider
	seen := make(map[string]bool)
	var vmClouds []CloudInfo

	for _, cloud := range allClouds {
		if cloud.IsVM() {
			key := cloud.Provider // e.g., "AWS", "GCP"
			if !seen[key] {
				seen[key] = true
				vmClouds = append(vmClouds, cloud)
				t.Logf("Selected %s VM cloud for testing: %s (ID: %s)", cloud.Provider, cloud.Name, cloud.ID)
			}
		}
	}

	if len(vmClouds) == 0 {
		t.Logf("No VM clouds available for testing")
	} else {
		t.Logf("Found %d unique VM cloud providers for testing", len(vmClouds))
	}
	return vmClouds
}

// GetAllK8sClouds returns every configured cloud whose compute stack is K8S.
// Unlike GetAllVMClouds this does not deduplicate by provider: K8S clouds are
// identified by their registered instance types (see ResolveK8sInstanceType),
// not by provider-wide SKU catalogs, so two K8S clouds on the same provider
// can still have different usable instance types.
func GetAllK8sClouds(t *testing.T) []CloudInfo {
	allClouds := GetAllConfiguredClouds(t)

	var k8sClouds []CloudInfo
	for _, cloud := range allClouds {
		if cloud.IsK8s() {
			k8sClouds = append(k8sClouds, cloud)
			t.Logf("Selected K8S cloud for testing: %s (ID: %s)", cloud.Name, cloud.ID)
		}
	}

	if len(k8sClouds) == 0 {
		t.Logf("No K8S clouds available for testing")
	}
	return k8sClouds
}

// ResolveK8sInstanceType returns the name of the smallest CPU-only (non-GPU)
// instance type registered for cloudID, via
// GET /api/v2/clouds/{cloud_id}/additional_instance_types. This mirrors the
// backend's own default-compute-config selection for K8S clouds (see
// clouds_resource.py's get_smallest_cpu_instance_type in the Platform repo) -
// K8S compute configs use instance_type values drawn from a cloud's own
// registered/discovered set, not a fixed provider-wide SKU list like
// "m5.large", which is why InstanceTypeSet.InstanceTypes() returns empty
// placeholders for K8S clouds (see that function's TODO comment). Returns ""
// if the cloud has no registered instance types (caller should skip).
func ResolveK8sInstanceType(t *testing.T, cloudID string) string {
	t.Helper()
	client, err := GetTestClient()
	if err != nil {
		t.Logf("ResolveK8sInstanceType: failed to get test client: %v", err)
		return ""
	}

	resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s/additional_instance_types", cloudID), nil)
	if err != nil {
		t.Logf("ResolveK8sInstanceType: request failed: %v", err)
		return ""
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		t.Logf("ResolveK8sInstanceType: API returned status %d", resp.StatusCode)
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Logf("ResolveK8sInstanceType: failed to read response: %v", err)
		return ""
	}

	var instanceTypesResp struct {
		Results []struct {
			Name     string `json:"name"`
			CPUCount int    `json:"cpu_count"`
			GPUCount *int   `json:"gpu_count"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &instanceTypesResp); err != nil {
		t.Logf("ResolveK8sInstanceType: failed to parse response: %v", err)
		return ""
	}

	var smallestName string
	smallestCPU := -1
	for _, it := range instanceTypesResp.Results {
		if it.GPUCount != nil && *it.GPUCount > 0 {
			continue // CPU-only, matching the backend's own default-config selection
		}
		if smallestCPU == -1 || it.CPUCount < smallestCPU {
			smallestCPU = it.CPUCount
			smallestName = it.Name
		}
	}

	if smallestName == "" {
		t.Logf("ResolveK8sInstanceType: cloud %s has no registered CPU-only instance types", cloudID)
	} else {
		t.Logf("ResolveK8sInstanceType: selected %s (cpu_count=%d) for cloud %s", smallestName, smallestCPU, cloudID)
	}
	return smallestName
}

var (
	// Cache for a read-only test project ID. Only for tests that merely read
	// a project (e.g. data source lookups) — never for tests that create,
	// update, or replace project-scoped state, since this may resolve to a
	// shared, real project. Those should call CreateEphemeralTestProject instead.
	cachedTestProjectID string
	testProjectIDMutex  sync.Mutex
)

// GetTestProjectID returns a project ID for READ-ONLY acceptance tests, with
// priority:
//  1. ANYSCALE_TEST_PROJECT_ID environment variable (explicit override)
//  2. Auto-discover: list projects, prefer the org's default project (always
//     present, stable across runs), else the first result.
//
// Do not use this for tests that mutate or replace state scoped to the
// project itself — this may resolve to a shared, real project. Call
// CreateEphemeralTestProject instead so the test never touches one it does
// not own.
func GetTestProjectID(t *testing.T) string {
	testProjectIDMutex.Lock()
	defer testProjectIDMutex.Unlock()

	if cachedTestProjectID != "" {
		return cachedTestProjectID
	}

	if envProjectID := os.Getenv("ANYSCALE_TEST_PROJECT_ID"); envProjectID != "" {
		t.Logf("Using test project ID from ANYSCALE_TEST_PROJECT_ID: %s", envProjectID)
		cachedTestProjectID = envProjectID
		return cachedTestProjectID
	}

	client, err := GetTestClient()
	if err != nil {
		t.Skip("No project available - failed to get test client.")
		return ""
	}

	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/projects", nil)
	if err != nil {
		t.Skip("No project available - failed to list projects.")
		return ""
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		t.Skip("No project available - API error listing projects.")
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Skip("No project available - failed to read response.")
		return ""
	}

	var projectsResp struct {
		Results []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			IsDefault bool   `json:"is_default"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &projectsResp); err != nil {
		t.Skip("No project available - failed to parse response.")
		return ""
	}

	if len(projectsResp.Results) == 0 {
		t.Skip("No project available - no projects found in the account.")
		return ""
	}

	// Prefer the default project: always present, stable across runs, so
	// repeated test invocations resolve to the same read target.
	for _, p := range projectsResp.Results {
		if p.IsDefault {
			t.Logf("Using default project for test: %s (ID: %s)", p.Name, p.ID)
			cachedTestProjectID = p.ID
			return cachedTestProjectID
		}
	}

	t.Logf("Using first available project for test: %s (ID: %s)", projectsResp.Results[0].Name, projectsResp.Results[0].ID)
	cachedTestProjectID = projectsResp.Results[0].ID
	return cachedTestProjectID
}

// CreateEphemeralTestProject creates a minimal disposable project under a
// resolved test cloud, named with the "tfacc-" prefix so the existing project
// sweeper cleans it up if test cleanup is interrupted. Tests that mutate or
// replace project-scoped state should use this instead of GetTestProjectID,
// so they never touch a shared/real project.
func CreateEphemeralTestProject(t *testing.T) (projectID string, projectName string, err error) {
	client, err := GetTestClient()
	if err != nil {
		return "", "", fmt.Errorf("failed to get test client: %w", err)
	}

	parentCloudID := GetTestCloudID(t)

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

	resp, err := client.DoRequest(context.Background(), "POST", "/api/v2/projects", strings.NewReader(string(reqBody)))
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
		t.Cleanup(func() {
			delResp, delErr := client.DoRequest(context.Background(), "DELETE", fmt.Sprintf("/api/v2/projects/%s", createdID), nil)
			if delErr != nil {
				t.Logf("Warning: Failed to delete ephemeral project %s: %v", createdID, delErr)
				return
			}
			defer func() { _ = delResp.Body.Close() }()
			if delResp.StatusCode != 200 && delResp.StatusCode != 202 && delResp.StatusCode != 204 && delResp.StatusCode != 404 {
				t.Logf("Warning: Failed to delete ephemeral project %s: status %d", createdID, delResp.StatusCode)
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

	// Verify the token actually works against the API. If it's expired/invalid,
	// skip rather than fail so CI doesn't go red on a stale secret.
	ValidateAuthOrSkip(t)

	// Note: We don't require ANYSCALE_TEST_CLOUD_ID here anymore
	// Tests should use GetTestCloudID() which handles auto-discovery
}

// ValidateAuthOrSkip probes the Anyscale API with the configured token and
// SKIPS the test if the API returns 401. Other errors (network, etc.) are
// logged and ignored — they will surface naturally if they affect the test.
//
// The live probe result (valid vs. 401-invalid) is cached for the run once
// definitively known, since this is called from PreCheck on every single
// acceptance test (100+ call sites) and the token's validity doesn't change
// mid-run. A request error is deliberately NOT cached - unlike an actual
// 401, that's the same "inconclusive, try again" case the uncached version
// already tolerated per-test, and caching it would let one transient network
// blip silently suppress the real 401 check for every later test in the run.
func ValidateAuthOrSkip(t *testing.T) {
	authProbeMutex.Lock()
	if authProbeDone {
		invalid := authProbeInvalid
		authProbeMutex.Unlock()
		if invalid {
			t.Skip("ANYSCALE_CLI_TOKEN is invalid or expired (401 from /api/v2/clouds); skipping acceptance test")
		}
		return
	}
	authProbeMutex.Unlock()

	client, err := GetTestClient()
	if err != nil {
		t.Skipf("No usable Anyscale credentials: %v", err)
	}
	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/clouds", nil)
	if err != nil {
		t.Logf("Auth probe request error (continuing): %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	authProbeMutex.Lock()
	authProbeDone = true
	authProbeInvalid = resp.StatusCode == 401
	authProbeMutex.Unlock()

	if resp.StatusCode == 401 {
		t.Skip("ANYSCALE_CLI_TOKEN is invalid or expired (401 from /api/v2/clouds); skipping acceptance test")
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

// NewAPIDestroyCheck returns a CheckDestroy function that verifies every
// resource of resourceType in the Terraform state is gone from the Anyscale
// API. getPathFmt is a printf format string with one %s for the resource ID
// (e.g. "/api/v2/projects/%s"). 404 = success. 200 = leak (returns error).
// Transient errors (network, 5xx) log a warning and continue so a flaky API
// doesn't mask a real leak from the rest of the state.
func NewAPIDestroyCheck(resourceType, getPathFmt string) resource.TestCheckFunc {
	return newAPIDestroyCheckImpl(resourceType, "", getPathFmt, "")
}

// NewAPIDestroyCheckByAttr is like NewAPIDestroyCheck but pulls the resource
// ID from rs.Primary.Attributes[attrName] instead of rs.Primary.ID. Used when
// the API-side identifier is exposed as a non-ID attribute (e.g. compute
// config's version-specific config_id).
func NewAPIDestroyCheckByAttr(resourceType, attrName, getPathFmt string) resource.TestCheckFunc {
	return newAPIDestroyCheckImpl(resourceType, attrName, getPathFmt, "")
}

// NewAPIArchivedDestroyCheck is the variant for resources that the API cannot
// permanently delete — container images, cluster environments. It verifies
// that the resource still exists but its archived field is truthy.
// archivedJSONPath is the dotted JSON path to read from the GET response
// (e.g. "result.deleted_at" or "is_archived"). A value of true (bool) or a
// non-empty/non-null string at that path counts as archived.
func NewAPIArchivedDestroyCheck(resourceType, getPathFmt, archivedJSONPath string) resource.TestCheckFunc {
	return newAPIDestroyCheckImpl(resourceType, "", getPathFmt, archivedJSONPath)
}

// NewAPIArchivedDestroyCheckByAttr is the attribute-keyed variant of
// NewAPIArchivedDestroyCheck.
func NewAPIArchivedDestroyCheckByAttr(resourceType, attrName, getPathFmt, archivedJSONPath string) resource.TestCheckFunc {
	return newAPIDestroyCheckImpl(resourceType, attrName, getPathFmt, archivedJSONPath)
}

// NewAPIArchivedDestroyCheckForID is the ID-pinned variant of
// NewAPIArchivedDestroyCheck: rather than discovering which resources to
// check by scanning Terraform state for resourceType, it polls the single id
// the caller supplies via a *string. Use this when the fact under test
// concerns an id a prior step has already dropped from state — e.g. a
// RequiresReplace swapped in a new id, and the point of the check is that the
// OLD id was actually archived server-side, not merely that the new resource
// looks right. Populate id with a TestCheckFunc earlier in the same Check
// chain, since its value isn't known until that step runs.
//
// Unlike the CheckDestroy-oriented family above, an unexpected status here is
// a hard error rather than a logged warning: this runs inside a Check as a
// positive assertion about one known id, not a best-effort leak scan across
// however many resources remain in state.
//
// failureHint is an optional trailing string appended to the timeout error,
// for a caller that wants the failure message to name the specific
// regression it proves rather than just the generic timeout.
func NewAPIArchivedDestroyCheckForID(resourceType string, id *string, getPathFmt, archivedJSONPath string, failureHint ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		if id == nil || *id == "" {
			return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): no id captured to check", resourceType)
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): failed to get test client: %w", resourceType, err)
		}

		path := fmt.Sprintf(getPathFmt, *id)
		deadline := time.Now().Add(destroyCheckPollTimeout)
		for {
			resp, err := client.DoRequest(context.Background(), "GET", path, nil)
			if err != nil {
				return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): failed to check archived status of %s: %w", resourceType, *id, err)
			}

			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): failed to read response for %s: %w", resourceType, *id, readErr)
			}
			if resp.StatusCode != 200 {
				return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): unexpected status %d checking %s: %s",
					resourceType, resp.StatusCode, *id, body[:min(len(body), 256)])
			}

			archived, perr := extractArchivedValue(body, archivedJSONPath)
			if perr != nil {
				return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): failed to parse %s for %s: %w", resourceType, archivedJSONPath, *id, perr)
			}
			if archived {
				return nil
			}
			if time.Now().After(deadline) {
				hint := ""
				if len(failureHint) > 0 && failureHint[0] != "" {
					hint = " - " + failureHint[0]
				}
				return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): %s was never archived (checked %s) within the poll window%s", resourceType, *id, archivedJSONPath, hint)
			}
			time.Sleep(destroyCheckPollInterval)
		}
	}
}

// Anyscale archives/deletes are asynchronous: the API accepts the request but
// the archive marker (e.g. result.deleted_at) can take a few seconds to
// persist. CheckDestroy polls for the archived variant up to this bound so it
// does not race the backend and report a false leak.
const (
	destroyCheckPollTimeout  = 30 * time.Second
	destroyCheckPollInterval = 2 * time.Second
)

// newAPIDestroyCheckImpl is the shared implementation behind the public
// CheckDestroy helpers. When archivedJSONPath is empty, a 200 response is
// treated as a leak. Otherwise, the value at archivedJSONPath must be truthy
// (bool true or non-empty/non-null string) or the resource is a leak. For the
// archived variant the check polls up to destroyCheckPollTimeout because the
// backend sets the archive marker asynchronously.
func newAPIDestroyCheckImpl(resourceType, attrName, getPathFmt, archivedJSONPath string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		if len(s.RootModule().Resources) == 0 {
			return nil
		}

		client, err := GetTestClient()
		if err != nil {
			// No client = can't verify; surface clearly rather than silently passing.
			return fmt.Errorf("CheckDestroy(%s): failed to get test client: %w", resourceType, err)
		}

		var leaks []string

		for name, rs := range s.RootModule().Resources {
			if rs.Type != resourceType {
				continue
			}

			id := rs.Primary.ID
			if attrName != "" {
				id = rs.Primary.Attributes[attrName]
			}
			if id == "" {
				continue
			}

			path := fmt.Sprintf(getPathFmt, id)

			// Poll so we don't race the backend's asynchronous archive/delete.
			// Definitive outcomes (404 gone, or a truthy archive marker) exit
			// immediately; only a still-present, not-yet-archived resource is
			// retried until destroyCheckPollTimeout elapses.
			deadline := time.Now().Add(destroyCheckPollTimeout)
			for {
				resp, err := client.DoRequest(context.Background(), "GET", path, nil)
				if err != nil {
					log.Printf("[WARN] CheckDestroy(%s) network error for %s (id=%s): %v", resourceType, name, id, err)
					break
				}

				body, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()

				if resp.StatusCode == 404 {
					break // gone — success
				}
				if resp.StatusCode >= 500 {
					log.Printf("[WARN] CheckDestroy(%s) transient %d for %s (id=%s)", resourceType, resp.StatusCode, name, id)
					break
				}
				if resp.StatusCode != 200 && resp.StatusCode != 201 {
					log.Printf("[WARN] CheckDestroy(%s) unexpected status %d for %s (id=%s)", resourceType, resp.StatusCode, name, id)
					break
				}

				// 200/201: the resource still exists.
				if archivedJSONPath == "" {
					leaks = append(leaks, fmt.Sprintf("%s (id=%s) still returns 200 from %s", name, id, path))
					break
				}
				if readErr != nil {
					log.Printf("[WARN] CheckDestroy(%s) failed to read body for %s (id=%s): %v", resourceType, name, id, readErr)
					break
				}
				archived, perr := extractArchivedValue(body, archivedJSONPath)
				if perr != nil {
					log.Printf("[WARN] CheckDestroy(%s) failed to parse %s for %s (id=%s): %v", resourceType, archivedJSONPath, name, id, perr)
					break
				}
				if archived {
					break // archived — success
				}

				// Still present and not yet archived: the async delete may still
				// be in flight. Retry until the deadline, then record a leak.
				if time.Now().After(deadline) {
					leaks = append(leaks, fmt.Sprintf("%s (id=%s) exists at %s and %s is not truthy", name, id, path, archivedJSONPath))
					break
				}
				time.Sleep(destroyCheckPollInterval)
			}
		}

		if len(leaks) > 0 {
			return fmt.Errorf("CheckDestroy(%s) found leaked resources:\n  %s", resourceType, strings.Join(leaks, "\n  "))
		}
		return nil
	}
}

// extractArchivedValue walks a dotted JSON path and returns whether the value
// at the leaf is truthy. true (bool) or a non-empty/non-null string are
// truthy; all other values (false, null, missing key, numbers, arrays) are
// not. Designed for tolerant detection of "this resource has been archived
// rather than deleted" markers (is_archived bool, deleted_at string, etc.).
func extractArchivedValue(body []byte, jsonPath string) (bool, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return false, err
	}

	cur := root
	for _, segment := range strings.Split(jsonPath, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false, nil
		}
		cur, ok = m[segment]
		if !ok {
			return false, nil
		}
	}

	switch v := cur.(type) {
	case bool:
		return v, nil
	case string:
		return v != "", nil
	case nil:
		return false, nil
	default:
		return false, nil
	}
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
