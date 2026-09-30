package acctest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

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

// GetComputeConfigCloudID returns the ID of a cloud suitable for creating
// compute configs: one with at least one cloud resource (a proxy for a healthy
// primary cloud resource). POST /api/v2/compute_templates/ returns a backend
// 500 for clouds lacking a healthy primary resource, so when none are available
// the test is skipped rather than hard-failing. An explicit ANYSCALE_TEST_CLOUD_ID
// override is honored first (the operator is asserting that cloud is healthy).
func GetComputeConfigCloudID(t *testing.T) string {
	if id := os.Getenv("ANYSCALE_TEST_CLOUD_ID"); id != "" {
		ensureCloudAwake(t, id, cloudLabelFor(os.Getenv("ANYSCALE_TEST_CLOUD_NAME")))
		return id
	}
	// Known-good static fixture (resolved by name) before auto-discovery.
	if id := resolveDefaultKnownGoodCloudID(t); id != "" {
		t.Logf("Using default known-good cloud for compute config: %s (%s)", defaultKnownGoodCloudName, id)
		return id
	}
	for _, c := range GetAllConfiguredClouds(t) {
		if c.IsVM() {
			ensureCloudAwake(t, c.ID, cloudLabelFor(c.Name))
			return c.ID
		}
	}
	t.Skip("No VM cloud with a healthy primary cloud resource available; " +
		"compute config creation returns a backend 500 on degraded clouds. " +
		"Set ANYSCALE_TEST_CLOUD_ID to a healthy cloud to run this test.")
	return ""
}

// EphemeralComputeConfig identifies a compute config created directly against
// the API by CreateEphemeralComputeConfig, bypassing the Terraform resource
// entirely - "out-of-band" from Terraform's point of view.
type EphemeralComputeConfig struct {
	ConfigID string // version-specific cpt_ id
	Name     string
	Version  int64
}

// CreateEphemeralComputeConfig creates a compute config directly via the API
// (POST /api/v2/compute_templates/), never touching the anyscale_compute_config
// Terraform resource. This is what a "genuinely pre-existing" or "created by
// someone else" fixture looks like for import tests (AG-2), and it is also
// how a second, older version can be minted without the same test's own
// Terraform lifecycle managing it (AG-1).
//
// The API assigns the version number server-side and rejects a caller-supplied
// version tag on create (compute_config_sdk.py's create_compute_config) - so
// this reads the actual version back from the create response rather than
// assuming 1, matching the same discipline CreateEphemeralTestCloud/
// CreateEphemeralTestProjectForCloud already apply to their own identifiers.
func CreateEphemeralComputeConfig(t *testing.T, cloudID string, instanceType string) (EphemeralComputeConfig, error) {
	t.Helper()
	client, err := GetTestClient()
	if err != nil {
		return EphemeralComputeConfig{}, fmt.Errorf("failed to get test client: %w", err)
	}
	if instanceType == "" {
		instanceType = "m5.large"
	}

	name := UniqueName(t, "computeconfig")
	t.Logf("Creating ephemeral out-of-band compute config: %s (cloud: %s)", name, cloudID)

	fixture, err := postComputeConfigVersion(client, cloudID, name, instanceType, "create", "create compute config")
	if err != nil {
		return EphemeralComputeConfig{}, err
	}
	t.Logf("Created ephemeral compute config: %s (config_id: %s, version: %d)", fixture.Name, fixture.ConfigID, fixture.Version)

	if os.Getenv("ANYSCALE_TEST_KEEP") == "1" {
		t.Logf("ANYSCALE_TEST_KEEP=1: compute config %s will be preserved after tests", fixture.ConfigID)
	} else {
		t.Cleanup(func() {
			// Archive is family-wide (the whole name/cloud lineage, every
			// version), matching the resource's own Delete - no per-version
			// cleanup call exists or is needed.
			delResp, delErr := client.DoRequest(context.Background(), "POST",
				fmt.Sprintf("/api/v2/compute_templates/%s/archive", fixture.ConfigID), nil)
			if delErr != nil {
				t.Logf("Warning: failed to archive ephemeral compute config %s: %v", fixture.ConfigID, delErr)
				return
			}
			defer func() { _ = delResp.Body.Close() }()
			if delResp.StatusCode != 200 && delResp.StatusCode != 202 && delResp.StatusCode != 204 && delResp.StatusCode != 404 {
				t.Logf("Warning: failed to archive ephemeral compute config %s: status %d", fixture.ConfigID, delResp.StatusCode)
			}
		})
	}

	return fixture, nil
}

// UpdateEphemeralComputeConfig mints a NEW version of an out-of-band compute
// config created by CreateEphemeralComputeConfig, directly via the API - used
// to set up a "newer version exists" scenario (AG-1) without any Terraform
// resource involved. Returns the new version's own EphemeralComputeConfig;
// the ORIGINAL config_id/version from the prior create remain valid and
// importable (config_id is immutable per version).
func UpdateEphemeralComputeConfig(t *testing.T, cloudID string, name string, instanceType string) (EphemeralComputeConfig, error) {
	t.Helper()
	client, err := GetTestClient()
	if err != nil {
		return EphemeralComputeConfig{}, fmt.Errorf("failed to get test client: %w", err)
	}

	fixture, err := postComputeConfigVersion(client, cloudID, name, instanceType, "update", "mint new compute config version")
	if err != nil {
		return EphemeralComputeConfig{}, err
	}
	t.Logf("Minted new out-of-band compute config version: %s (config_id: %s, version: %d)", fixture.Name, fixture.ConfigID, fixture.Version)
	// No separate cleanup registration: archive is family-wide, already
	// covered by the ORIGINAL CreateEphemeralComputeConfig's t.Cleanup for
	// the same name/cloud lineage.
	return fixture, nil
}

// postComputeConfigVersion POSTs a minimal compute config (head node only) to
// /api/v2/compute_templates/ with new_version=true and returns the version the
// API assigned. verb and action only shape error messages.
func postComputeConfigVersion(client *provider.Client, cloudID, name, instanceType, verb, action string) (EphemeralComputeConfig, error) {
	bodyBytes, err := json.Marshal(computeConfigVersionBody(cloudID, name, instanceType))
	if err != nil {
		return EphemeralComputeConfig{}, fmt.Errorf("failed to marshal %s request: %w", verb, err)
	}

	resp, err := client.DoRequest(context.Background(), "POST", "/api/v2/compute_templates/", bytes.NewReader(bodyBytes))
	if err != nil {
		return EphemeralComputeConfig{}, fmt.Errorf("failed to %s: %w", action, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return EphemeralComputeConfig{}, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return EphemeralComputeConfig{}, fmt.Errorf("failed to %s (status %d): %s", action, resp.StatusCode, string(body))
	}

	var createResp struct {
		Result struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Version int64  `json:"version"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &createResp); err != nil {
		return EphemeralComputeConfig{}, fmt.Errorf("failed to parse %s response: %w", verb, err)
	}
	return EphemeralComputeConfig{
		ConfigID: createResp.Result.ID,
		Name:     createResp.Result.Name,
		Version:  createResp.Result.Version,
	}, nil
}

// computeConfigVersionBody is the request body for a minimal head-node-only
// compute config version.
func computeConfigVersionBody(cloudID, name, instanceType string) map[string]any {
	return map[string]any{
		"name":        name,
		"anonymous":   false,
		"new_version": true,
		"config": map[string]any{
			"cloud_id": cloudID,
			"head_node_type": map[string]any{
				"name":          "head",
				"instance_type": instanceType,
			},
		},
	}
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
// only if the cloud has no registered instance types (caller should skip); an
// API failure fails the test.
func ResolveK8sInstanceType(t *testing.T, cloudID string) string {
	t.Helper()
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("ResolveK8sInstanceType: failed to get test client: %v", err)
	}

	resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s/additional_instance_types", cloudID), nil)
	if err != nil {
		t.Fatalf("ResolveK8sInstanceType: request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		t.Fatalf("ResolveK8sInstanceType: API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ResolveK8sInstanceType: failed to read response: %v", err)
	}

	var instanceTypesResp struct {
		Results []struct {
			Name     string `json:"name"`
			CPUCount int    `json:"cpu_count"`
			GPUCount *int   `json:"gpu_count"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &instanceTypesResp); err != nil {
		t.Fatalf("ResolveK8sInstanceType: failed to parse response: %v", err)
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
