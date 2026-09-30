package acctest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

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

	// Cache for ValidateAuth's live probe - see its doc comment for why only a
	// definitive answer (not a request error) is cached. authProbeStatus is
	// the rejecting HTTP status, or 0 when the token was accepted.
	authProbeDone   bool
	authProbeStatus int
	authProbeMutex  sync.Mutex

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

// errCloudNameNotFound and errNoCloudsInOrg mark the only resolution outcomes
// that may fall through to the next resolver step or skip a test. Any other
// failure (transport error, non-200, undecodable body) is a harness failure:
// turning it into a skip is how an outage or a revoked token reads as a green
// acctest run.
var (
	errCloudNameNotFound = errors.New("no cloud found with name")
	errNoCloudsInOrg     = errors.New("no clouds found in the account")
)

// resolveDefaultKnownGoodCloudID resolves defaultKnownGoodCloudName to a cloud
// ID via the API. Returns "" (caller falls through to auto-discovery) only when
// the name does not exist in the current org; any other error fails the test.
// The ID is deliberately not hardcoded in the repo.
func resolveDefaultKnownGoodCloudID(t *testing.T) string {
	id, err := resolveCloudNameToID(t, defaultKnownGoodCloudName)
	if errors.Is(err, errCloudNameNotFound) {
		return ""
	}
	if err != nil {
		t.Fatalf("resolving default test cloud %q: %v", defaultKnownGoodCloudName, err)
	}
	ensureCloudAwake(t, id, cloudLabelFor(defaultKnownGoodCloudName))
	return id
}

// GetTestCloudID returns a test cloud ID with the following priority:
//  1. ANYSCALE_TEST_CLOUD_ID environment variable (explicit override, returned
//     as-is: no API call checks that the cloud exists)
//  2. ANYSCALE_TEST_CLOUD_NAME environment variable (resolve name to ID)
//  3. Known-good static fixture cloud (resolved by name; falls through if absent)
//  4. Auto-discover any available cloud (prefers test-named clouds)
//
// The result is cached after the first successful resolution.
// Unlike sync.Once, this will retry on failure.
// On later calls the cached ID is checked with GET /api/v2/clouds/{id} and
// re-resolved if the cloud is gone.
func GetTestCloudID(t *testing.T) string {
	id := resolveTestCloudID(t)
	cloudIDMutex.Lock()
	name := cachedTestCloudName
	cloudIDMutex.Unlock()
	ensureCloudAwake(t, id, cloudLabelFor(name))
	return id
}

// resolveTestCloudID is the resolution behind GetTestCloudID, before the
// wake gate.
func resolveTestCloudID(t *testing.T) string {
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
		if err != nil && !errors.Is(err, errCloudNameNotFound) {
			t.Fatalf("resolving ANYSCALE_TEST_CLOUD_NAME %q: %v", envCloudName, err)
		}
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
	if errors.Is(err, errNoCloudsInOrg) {
		t.Skip("No test cloud ID available. Set ANYSCALE_TEST_CLOUD_ID or ANYSCALE_TEST_CLOUD_NAME, or ensure at least one cloud exists in the account.")
	}
	if err != nil {
		t.Fatalf("auto-discovering test cloud: %v", err)
	}

	cachedTestCloudID = cloudID
	cachedTestCloudName = cloudName
	return cachedTestCloudID
}

// GetTestCloudName returns a test cloud name with the following priority:
//  1. ANYSCALE_TEST_CLOUD_NAME environment variable (explicit override; falls
//     through if the name does not resolve)
//  2. Known-good static fixture cloud (falls through if absent)
//  3. Auto-discover any available cloud and return its name
//
// It shares GetTestCloudID's cache: a name cached alongside its ID is returned
// after checking the cloud still exists. ANYSCALE_TEST_CLOUD_ID is not
// consulted here.
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
		if err != nil && !errors.Is(err, errCloudNameNotFound) {
			t.Fatalf("resolving ANYSCALE_TEST_CLOUD_NAME %q: %v", envCloudName, err)
		}
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
	if errors.Is(err, errNoCloudsInOrg) {
		t.Skip("No test cloud name available. Set ANYSCALE_TEST_CLOUD_NAME or ensure at least one cloud exists in the account.")
	}
	if err != nil {
		t.Fatalf("auto-discovering test cloud: %v", err)
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

// resolveCloudNameToID resolves a cloud name to its ID by querying the API.
//
// Paginates across every page of GET /api/v2/clouds, not just the first -
// this used to read only page 1, so once an org's cloud list exceeded one
// page, a valid name resolved to "no cloud found" (the same bug class fixed
// in the provider's own ResolveCloudNameToID, cloud_helpers.go). This
// function resolves ANYSCALE_TEST_CLOUD_NAME and the default pinned fixture
// name (tfp-test-aws-useast1-STATIC) at runtime, so the failure mode here is
// "the entire acceptance suite can no longer find its own fixture" the day
// the test org's cloud list crosses a page boundary - worth the extra
// diligence over a typical test-helper bug. Deliberately keeps the exact
// local most-recent tiebreak and duplicate-warning log below unchanged
// (rather than delegating to the provider package's PickMostRecentMatch) -
// only the pagination gap is being closed here.
func resolveCloudNameToID(t *testing.T, cloudName string) (string, error) {
	id, matchCount, err := resolveCloudNameToIDCore(cloudName)
	if err != nil {
		return "", err
	}
	if matchCount > 1 {
		t.Logf("Warning: Multiple clouds (%d) found with name '%s', using most recent: %s", matchCount, cloudName, id)
	}
	t.Logf("Resolved cloud name '%s' to ID: %s", cloudName, id)
	return id, nil
}

// resolveCloudNameToIDCore is resolveCloudNameToID without a *testing.T, so
// callers outside a test function can reuse the same fully-paginated lookup
// instead of duplicating it. The sweep-target org guard in TestMain needs
// exactly this: TestMain has no *testing.T, and a second copy of the paging
// loop is precisely the divergence this repo has been bitten by before (a
// body-vs-query paging mismatch silently truncating a sweep's candidate list).
// Returns the resolved ID and how many clouds carried that name; the caller
// decides whether a duplicate is worth logging.
func resolveCloudNameToIDCore(cloudName string) (string, int, error) {
	client, err := GetTestClient()
	if err != nil {
		return "", 0, fmt.Errorf("failed to get test client: %w", err)
	}

	// Find matching cloud(s) across every page - if multiple exist, use the most recent
	var matchedCloudID string
	var latestCreatedAt string
	matchCount := 0

	pagingToken := ""
	for {
		path := "/api/v2/clouds"
		if pagingToken != "" {
			path = fmt.Sprintf("%s?paging_token=%s", path, url.QueryEscape(pagingToken))
		}

		resp, err := client.DoRequest(context.Background(), "GET", path, nil)
		if err != nil {
			return "", 0, fmt.Errorf("failed to list clouds: %w", err)
		}

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return "", 0, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return "", 0, fmt.Errorf("failed to read response: %w", err)
		}

		var cloudsResp struct {
			Results []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				CreatedAt string `json:"created_at"`
			} `json:"results"`
			Metadata struct {
				NextPagingToken *string `json:"next_paging_token"`
			} `json:"metadata"`
		}

		if err := json.Unmarshal(body, &cloudsResp); err != nil {
			return "", 0, fmt.Errorf("failed to parse clouds response: %w", err)
		}

		for _, cloud := range cloudsResp.Results {
			if cloud.Name == cloudName {
				matchCount++
				if matchedCloudID == "" || cloud.CreatedAt > latestCreatedAt {
					matchedCloudID = cloud.ID
					latestCreatedAt = cloud.CreatedAt
				}
			}
		}

		if cloudsResp.Metadata.NextPagingToken == nil || *cloudsResp.Metadata.NextPagingToken == "" {
			break
		}
		pagingToken = *cloudsResp.Metadata.NextPagingToken
	}

	if matchedCloudID == "" {
		return "", 0, fmt.Errorf("%w '%s'", errCloudNameNotFound, cloudName)
	}

	return matchedCloudID, matchCount, nil
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

	resp, err := client.DoRequest(context.Background(), "POST", "/api/v2/clouds", bytes.NewReader(reqBody))
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
		return "", "", fmt.Errorf("%w (set ANYSCALE_TEST_CREATE_CLOUD=1 to auto-create)", errNoCloudsInOrg)
	}

	// MAY CREATE, MAY NOT ADOPT.
	//
	// Reaching this point means two things at once: the pinned fixture cloud did
	// NOT resolve (both callers check it immediately before calling us), AND this
	// organization already contains clouds. The pinned fixture exists only in the
	// acctest org, so its absence is a reliable signal that we are somewhere else
	// - and the clouds sitting here belong to whoever owns that organization.
	//
	// Adopting one is how four tfacc- clouds were created in a user's working org
	// on 2026-08-02: the resolver logged "did not resolve in this org", fell
	// through, scored a cloud, and every later test built on it. Worse, the leak
	// self-conceals - a wrong-org run leaves tfacc- clouds behind, and tfacc-
	// scores priority 9 below, so the NEXT run adopts the previous run's leftovers
	// and passes, with a plausible-looking test cloud waiting for it.
	//
	// Creating in an EMPTY org stays allowed - that is the branch above, and it is
	// the path CI's ANYSCALE_TEST_CREATE_CLOUD=1 exists for. Only adoption of a
	// pre-existing cloud is refused.
	if os.Getenv("ANYSCALE_TEST_ALLOW_CLOUD_ADOPTION") != "1" {
		names := make([]string, 0, len(testClouds))
		for _, c := range testClouds {
			names = append(names, c.Name)
		}
		return "", "", fmt.Errorf(
			"refusing to adopt an existing cloud: the pinned fixture %q does not exist in this "+
				"organization, so these credentials are probably not pointed at the acctest org. "+
				"Found %d cloud(s) here that this suite did not create: %s.\n\n"+
				"Point ANYSCALE_CLI_TOKEN at the acctest org, or set ANYSCALE_TEST_CLOUD_ID / "+
				"ANYSCALE_TEST_CLOUD_NAME to choose a cloud explicitly. If you genuinely mean to run "+
				"against a cloud this suite did not create, set ANYSCALE_TEST_ALLOW_CLOUD_ADOPTION=1",
			defaultKnownGoodCloudName, len(testClouds), strings.Join(names, ", "),
		)
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
		t.Fatalf("GetAnyCloudID: failed to get test client: %v", err)
	}

	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/clouds", nil)
	if err != nil {
		t.Fatalf("GetAnyCloudID: failed to list clouds: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GetAnyCloudID: list clouds returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GetAnyCloudID: failed to read response: %v", err)
	}

	var cloudsResp struct {
		Results []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &cloudsResp); err != nil {
		t.Fatalf("GetAnyCloudID: failed to parse clouds response: %v", err)
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
		t.Fatalf("GetAllConfiguredClouds: failed to get test client: %v", err)
	}

	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/clouds", nil)
	if err != nil {
		t.Fatalf("GetAllConfiguredClouds: failed to list clouds: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GetAllConfiguredClouds: list clouds returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GetAllConfiguredClouds: failed to read response: %v", err)
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
		t.Fatalf("GetAllConfiguredClouds: failed to parse clouds response: %v", err)
	}

	var clouds []CloudInfo
	allDefinitive := true

	// Collect all clouds with a healthy resource. GET /api/v2/clouds never
	// embeds cloud_resources inline (confirmed against the real API: the key
	// is absent from each result, not an empty array), so checking an inline
	// field would exclude every cloud and silently skip every caller.
	// Resource health comes from the per-cloud endpoint instead.
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
	// resource.

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
