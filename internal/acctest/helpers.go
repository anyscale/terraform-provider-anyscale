package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
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

	// Track ephemeral cloud created by tests for cleanup
	ephemeralCloudID   string
	ephemeralCloudName string
)

// GetTestCloudID returns a test cloud ID with the following priority:
// 1. ANYSCALE_TEST_CLOUD_ID environment variable (explicit override)
// 2. ANYSCALE_TEST_CLOUD_NAME environment variable (resolve name to ID)
// 3. Auto-discover any available cloud (prefers test-named clouds)
//
// The result is cached after the first successful resolution.
// Unlike sync.Once, this will retry on failure.
func GetTestCloudID(t *testing.T) string {
	cloudIDMutex.Lock()
	defer cloudIDMutex.Unlock()

	// Return cached value if available
	if cachedTestCloudID != "" {
		return cachedTestCloudID
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

	// Priority 3: Auto-discover
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
func GetTestCloudName(t *testing.T) string {
	cloudIDMutex.Lock()
	defer cloudIDMutex.Unlock()

	// If we have a cached name, return it
	if cachedTestCloudName != "" {
		return cachedTestCloudName
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

	// Priority 2: Auto-discover (this will populate both ID and Name caches)
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
	cloudName = fmt.Sprintf("tfacc-ephemeral-%d", os.Getpid())

	t.Logf("Creating ephemeral test cloud: %s", cloudName)

	// Create minimal empty cloud request
	createReq := struct {
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Region   string `json:"region"`
	}{
		Name:     cloudName,
		Provider: "AWS",
		Region:   "us-east-2",
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

	// Track the ephemeral cloud for cleanup
	ephemeralCloudID = cloudResp.Result.ID
	ephemeralCloudName = cloudResp.Result.Name

	t.Logf("Created ephemeral test cloud: %s (ID: %s)", ephemeralCloudName, ephemeralCloudID)

	if os.Getenv("ANYSCALE_TEST_KEEP") == "1" {
		t.Logf("ANYSCALE_TEST_KEEP=1: Cloud will be preserved after tests")
	} else {
		// Register cleanup
		t.Cleanup(func() {
			cleanupEphemeralCloud(t)
		})
	}

	return ephemeralCloudID, ephemeralCloudName, nil
}

// cleanupEphemeralCloud deletes the ephemeral cloud created for testing
func cleanupEphemeralCloud(t *testing.T) {
	if ephemeralCloudID == "" {
		return
	}

	t.Logf("Cleaning up ephemeral test cloud: %s (ID: %s)", ephemeralCloudName, ephemeralCloudID)

	client, err := GetTestClient()
	if err != nil {
		t.Logf("Warning: Failed to get client for cleanup: %v", err)
		return
	}

	resp, err := client.DoRequest(context.Background(), "DELETE", fmt.Sprintf("/api/v2/clouds/%s", ephemeralCloudID), nil)
	if err != nil {
		t.Logf("Warning: Failed to delete ephemeral cloud: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 200 || resp.StatusCode == 204 || resp.StatusCode == 404 {
		t.Logf("Successfully cleaned up ephemeral cloud: %s", ephemeralCloudName)
		ephemeralCloudID = ""
		ephemeralCloudName = ""
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

// IsK8s returns true if this instance type set is for a K8S cloud
func (i InstanceTypeSet) IsK8s() bool {
	return i.ComputeStack == "K8S"
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
func GetAllConfiguredClouds(t *testing.T) []CloudInfo {
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
		t.Logf("Failed to parse response: %v", err)
		return nil
	}

	var clouds []CloudInfo

	// Collect all clouds with cloud resources configured
	for _, cloud := range cloudsResp.Results {
		computeStack := normalizeComputeStack(cloud.ComputeStack)
		if len(cloud.CloudResources) > 0 && isKnownProvider(cloud.Provider, computeStack) {
			clouds = append(clouds, CloudInfo{
				ID:           cloud.ID,
				Name:         cloud.Name,
				Provider:     cloud.Provider,
				ComputeStack: computeStack,
			})
		}
	}

	// If no configured clouds found, try clouds without cloud_resources
	if len(clouds) == 0 {
		for _, cloud := range cloudsResp.Results {
			computeStack := normalizeComputeStack(cloud.ComputeStack)
			if isKnownProvider(cloud.Provider, computeStack) {
				clouds = append(clouds, CloudInfo{
					ID:           cloud.ID,
					Name:         cloud.Name,
					Provider:     cloud.Provider,
					ComputeStack: computeStack,
				})
			}
		}
	}

	t.Logf("Found %d configured clouds for testing", len(clouds))
	for _, c := range clouds {
		t.Logf("  - %s (ID: %s, provider: %s, compute_stack: %s)", c.Name, c.ID, c.Provider, c.ComputeStack)
	}

	return clouds
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

	// Note: We don't require ANYSCALE_TEST_CLOUD_ID here anymore
	// Tests should use GetTestCloudID() which handles auto-discovery
}

// SkipIfNotAcceptanceTest skips the test if TF_ACC is not set
// This replaces the common pattern at the start of every acceptance test
func SkipIfNotAcceptanceTest(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
	}
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
