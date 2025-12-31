package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// testAccProtoV6ProviderFactories are used to instantiate a provider during
// acceptance testing. The factory function will be invoked for every Terraform
// CLI command executed to create a provider server to which the CLI can
// reattach.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"anyscale": providerserver.NewProtocol6WithError(NewFramework("test")()),
}

var (
	// Cache for test cloud ID to avoid repeated API calls
	cachedTestCloudID     string
	cachedTestCloudIDOnce sync.Once
)

// getTestCloudID returns a test cloud ID with the following priority:
// 1. ANYSCALE_TEST_CLOUD_ID environment variable (explicit override)
// 2. ANYSCALE_TEST_CLOUD_NAME environment variable (resolve name to ID)
// 3. Auto-discover a cloud with "tfprovider" or "test" in the name
//
// The result is cached after the first successful resolution.
func getTestCloudID(t *testing.T) string {
	var cloudID string
	var err error

	cachedTestCloudIDOnce.Do(func() {
		// Priority 1: Explicit cloud ID
		if envCloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID"); envCloudID != "" {
			t.Logf("Using test cloud ID from ANYSCALE_TEST_CLOUD_ID: %s", envCloudID)
			cachedTestCloudID = envCloudID
			return
		}

		// Priority 2: Cloud name to resolve
		if envCloudName := os.Getenv("ANYSCALE_TEST_CLOUD_NAME"); envCloudName != "" {
			t.Logf("Resolving test cloud name from ANYSCALE_TEST_CLOUD_NAME: %s", envCloudName)
			cloudID, err = resolveCloudNameToID(t, envCloudName)
			if err != nil {
				t.Logf("Warning: Failed to resolve cloud name '%s': %v", envCloudName, err)
			} else {
				cachedTestCloudID = cloudID
				return
			}
		}

		// Priority 3: Auto-discover
		t.Logf("Auto-discovering test cloud (looking for clouds with 'tfprovider' or 'test' in name)")
		cloudID, err = autoDiscoverTestCloud(t)
		if err != nil {
			t.Logf("Warning: Failed to auto-discover test cloud: %v", err)
		} else {
			cachedTestCloudID = cloudID
		}
	})

	if cachedTestCloudID == "" {
		t.Skip("No test cloud ID available. Set ANYSCALE_TEST_CLOUD_ID or ANYSCALE_TEST_CLOUD_NAME, or ensure a cloud with 'tfprovider' or 'test' in the name exists.")
	}

	return cachedTestCloudID
}

// resolveCloudNameToID resolves a cloud name to its ID by querying the API
func resolveCloudNameToID(t *testing.T, cloudName string) (string, error) {
	client, err := getTestClient()
	if err != nil {
		return "", fmt.Errorf("failed to get test client: %w", err)
	}

	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/clouds", nil)
	if err != nil {
		return "", fmt.Errorf("failed to list clouds: %w", err)
	}
	defer resp.Body.Close()

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

// autoDiscoverTestCloud attempts to find a suitable test cloud automatically
func autoDiscoverTestCloud(t *testing.T) (string, error) {
	client, err := getTestClient()
	if err != nil {
		return "", fmt.Errorf("failed to get test client: %w", err)
	}

	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/clouds", nil)
	if err != nil {
		return "", fmt.Errorf("failed to list clouds: %w", err)
	}
	defer resp.Body.Close()

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

	// Look for clouds with test-related names (prefer "tfprovider" prefix)
	var testClouds []struct {
		ID        string
		Name      string
		CreatedAt string
		Priority  int
	}

	for _, cloud := range cloudsResp.Results {
		nameLower := strings.ToLower(cloud.Name)
		priority := 0

		if strings.Contains(nameLower, "tfprovider") {
			priority = 10 // Highest priority
		} else if strings.HasPrefix(nameLower, "tf-acc-") || strings.HasPrefix(nameLower, "tfacc-") {
			priority = 9
		} else if strings.Contains(nameLower, "test") {
			priority = 5
		}

		if priority > 0 {
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
	}

	if len(testClouds) == 0 {
		return "", fmt.Errorf("no test clouds found (looking for names containing 'tfprovider', 'tf-acc-', or 'test')")
	}

	// Sort by priority (highest first), then by created_at (most recent first)
	bestCloud := testClouds[0]
	for _, cloud := range testClouds {
		if cloud.Priority > bestCloud.Priority ||
			(cloud.Priority == bestCloud.Priority && cloud.CreatedAt > bestCloud.CreatedAt) {
			bestCloud = cloud
		}
	}

	t.Logf("Auto-discovered test cloud: %s (ID: %s)", bestCloud.Name, bestCloud.ID)
	if len(testClouds) > 1 {
		t.Logf("Note: Found %d potential test clouds, selected '%s' based on priority and recency", len(testClouds), bestCloud.Name)
	}

	return bestCloud.ID, nil
}

// getTestClient returns an authenticated client for testing
// This is moved here from resource_cloud_acc_test.go to be shared across all test files
func getTestClient() (*Client, error) {
	// Get API URL from environment or use default
	apiURL := os.Getenv("ANYSCALE_API_URL")
	if apiURL == "" {
		apiURL = "https://console.anyscale.com"
	}

	// Get token from environment or credentials file
	token := os.Getenv("ANYSCALE_CLI_TOKEN")
	if token == "" {
		var err error
		token, err = GetAuthToken()
		if err != nil {
			return nil, fmt.Errorf("failed to get auth token: %w", err)
		}
	}

	if token == "" {
		return nil, fmt.Errorf("no authentication token available")
	}

	return NewClientWithToken(apiURL, token), nil
}

// testAccPreCheck validates that required environment variables are set
// This is a common precheck function that can be used across all acceptance tests
func testAccPreCheck(t *testing.T) {
	// Check for authentication
	token := os.Getenv("ANYSCALE_CLI_TOKEN")
	if token == "" {
		// Try credentials file
		if _, err := GetAuthToken(); err != nil {
			t.Fatalf("ANYSCALE_CLI_TOKEN must be set or ~/.anyscale/credentials.json must exist for acceptance tests")
		}
	}

	// Note: We don't require ANYSCALE_TEST_CLOUD_ID here anymore
	// Tests should use getTestCloudID() which handles auto-discovery
}

// skipIfNotAcceptanceTest skips the test if TF_ACC is not set
// This replaces the common pattern at the start of every acceptance test
func skipIfNotAcceptanceTest(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
	}
}

// testResourceName generates a unique resource name for testing
// Uses the process ID to ensure uniqueness across parallel test runs
// Example: testResourceName("project") returns "tfacc-project-12345"
func testResourceName(prefix string) string {
	return fmt.Sprintf("tfacc-%s-%d", prefix, os.Getpid())
}

// getTestCloudName returns the cloud name from ANYSCALE_TEST_CLOUD_NAME environment variable
// Returns empty string if not set (caller should handle appropriately)
// This is useful for tests that specifically need to test cloud name resolution
func getTestCloudName(t *testing.T) string {
	cloudName := os.Getenv("ANYSCALE_TEST_CLOUD_NAME")
	if cloudName != "" {
		t.Logf("Using test cloud name from ANYSCALE_TEST_CLOUD_NAME: %s", cloudName)
	}
	return cloudName
}
