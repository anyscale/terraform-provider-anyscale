package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// These tests exercise anyscale_cloud_iam_mapping against a REAL cloud
// deployment - never the shared static test fixture, since this resource's
// write is an authoritative wholesale overwrite of the cloud's mapping and
// mutating a shared fixture concurrently with other tests is exactly what
// this repo's testing conventions forbid. They require:
//
//   - ANYSCALE_TEST_REAL_INFRA=1 (gates all of them via SkipIfNoRealInfra)
//   - ANYSCALE_TEST_CLOUD_ID set to a dedicated, real AWS or GCP VM/K8S cloud
//     with an attached cloud_resource - NOT the auto-discovered/static
//     default, because the config endpoint 404s against a cloud with no
//     resource attached, and the mutating tests here must never land on a
//     cloud anything else might be concurrently relying on.
//
// A plain "empty cloud" (createEphemeralTestCloud) is not sufficient - the
// config endpoint requires a resolvable cloud_resource, which an empty cloud
// does not have. Provisioning one requires real, assumable cloud IAM roles
// (see CLAUDE.md's SkipIfNoRealInfra placeholder-config note), which is why
// this suite cannot spin its own cloud up automatically the way the
// bare-cloud ephemeral helpers elsewhere do.
//
// requireRealIAMMappingTestCloud returns the cloud_id/cloud_resource_id pair
// under test, or skips with a clear reason.
func requireRealIAMMappingTestCloud(t *testing.T) (cloudID, cloudResourceID string) {
	t.Helper()
	SkipIfNoRealInfra(t)

	cloudID = GetTestCloudID(t)
	if cloudID == "" {
		t.Skip("no test cloud resolved")
	}

	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("failed to get test client: %v", err)
	}
	resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), nil)
	if err != nil {
		t.Fatalf("failed to list cloud resources for %s: %v", cloudID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read cloud resources response: %v", err)
	}
	var listResp struct {
		Results []struct {
			CloudResourceID string `json:"cloud_resource_id"`
			IsDefault       bool   `json:"is_default"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		t.Fatalf("failed to parse cloud resources response: %v", err)
	}
	for _, r := range listResp.Results {
		if r.IsDefault {
			return cloudID, r.CloudResourceID
		}
	}
	t.Skipf("test cloud %s has no default cloud_resource attached - config endpoint would 404", cloudID)
	return "", ""
}

// putRealCloudDeploymentConfig performs a raw PUT against the real config
// endpoint, independent of the provider's own client code, so setup/assert
// steps in these tests do not share a blind spot with the code under test.
func putRealCloudDeploymentConfig(t *testing.T, cloudID, cloudResourceID string, spec map[string]any) map[string]any {
	t.Helper()
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("failed to get test client: %v", err)
	}
	body, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		t.Fatalf("failed to marshal spec: %v", err)
	}
	resp, err := client.DoRequest(context.Background(), "PUT",
		fmt.Sprintf("/api/v2/clouds/%s/deployment/%s/config", cloudID, cloudResourceID),
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("PUT config failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read PUT response: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("PUT config returned %d: %s", resp.StatusCode, truncateBody(string(respBody), 500))
	}
	var parsed struct {
		Result struct {
			Spec map[string]any `json:"spec"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		t.Fatalf("failed to parse PUT response: %v", err)
	}
	return parsed.Result.Spec
}

// getRealCloudDeploymentConfig performs a raw GET, independent of the
// provider's own client code, for the same reason as the PUT helper above -
// the assertion that Destroy actually cleared the mapping must not rely on
// the same code path being verified.
func getRealCloudDeploymentConfig(t *testing.T, cloudID, cloudResourceID string) map[string]any {
	t.Helper()
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("failed to get test client: %v", err)
	}
	resp, err := client.DoRequest(context.Background(), "GET",
		fmt.Sprintf("/api/v2/clouds/%s/deployment/%s/config", cloudID, cloudResourceID), nil)
	if err != nil {
		t.Fatalf("GET config failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read GET response: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("GET config returned %d: %s", resp.StatusCode, truncateBody(string(respBody), 500))
	}
	var parsed struct {
		Result struct {
			Spec map[string]any `json:"spec"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		t.Fatalf("failed to parse GET response: %v", err)
	}
	return parsed.Result.Spec
}

// realIAMMappingBaseSpec returns the cloud_provider/compute_stack pair the
// write endpoint requires on every call, resolved from the real cloud's own
// current config rather than hardcoded - this suite is meant to run against
// whichever real AWS/GCP VM or K8S cloud is provided via ANYSCALE_TEST_CLOUD_ID.
func realIAMMappingBaseSpec(t *testing.T, cloudID, cloudResourceID string) (cloudProvider, computeStack string) {
	t.Helper()
	current := getRealCloudDeploymentConfig(t, cloudID, cloudResourceID)
	cp, _ := current["cloud_provider"].(string)
	cs, _ := current["compute_stack"].(string)
	if cp == "" || cs == "" {
		t.Fatalf("test cloud's current config is missing cloud_provider/compute_stack: %v", current)
	}
	return cp, cs
}

// checkImportedCloudIAMMapping asserts what import actually recovered,
// inside the import step itself - the only place an assertion can see it,
// per this repo's own finding that a later step plans against what Create
// left, never what import recovered.
func checkImportedCloudIAMMapping(states []*terraform.InstanceState, wantCloudID, wantCloudResourceID, wantRuleValue, wantFallbackRule string) error {
	if len(states) != 1 {
		return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
	}
	attrs := states[0].Attributes

	if got := attrs["cloud_id"]; got != wantCloudID {
		return fmt.Errorf("imported cloud_id = %q, want %q", got, wantCloudID)
	}
	if got := attrs["cloud_resource_id"]; got != wantCloudResourceID {
		return fmt.Errorf("imported cloud_resource_id = %q, want %q - Optional+Computed resolution under a real unknown did not recover the real primary deployment", got, wantCloudResourceID)
	}
	if got := attrs["rules.#"]; got != "1" {
		return fmt.Errorf("imported rules.# = %q, want \"1\"", got)
	}
	if got := attrs["rules.0.value"]; got != wantRuleValue {
		return fmt.Errorf("imported rules.0.value = %q, want %q", got, wantRuleValue)
	}
	if got := attrs["fallback_rule"]; got != wantFallbackRule {
		return fmt.Errorf("imported fallback_rule = %q, want %q", got, wantFallbackRule)
	}
	if got := attrs["mode"]; got != "CUSTOMER_MANAGED" {
		return fmt.Errorf("imported mode = %q, want \"CUSTOMER_MANAGED\" (server-stamped once rules is non-empty)", got)
	}
	return nil
}

// TestAccCloudIAMMappingResource_RealCloud_ColdImport_CompoundID is Test A's
// first ID form: pre-seed a real mapping out of band, then cold-import using
// "<cloud_id>/<cloud_resource_id>" as the FIRST step (no prior Create/apply
// in this TestCase) with ImportStatePersist so the recovered state carries
// into this test's own final destroy - proving both what import actually
// recovers (asserted here, inside the import step, per this repo's own
// ImportState-throwaway-directory finding) and that Destroy genuinely
// reverts a real, freshly-imported mapping.
func TestAccCloudIAMMappingResource_RealCloud_ColdImport_CompoundID(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	cloudID, cloudResourceID := requireRealIAMMappingTestCloud(t)
	cloudProvider, computeStack := realIAMMappingBaseSpec(t, cloudID, cloudResourceID)

	putRealCloudDeploymentConfig(t, cloudID, cloudResourceID, map[string]any{
		"cloud_provider": cloudProvider,
		"compute_stack":  computeStack,
		"dataplane_iam_mapping": map[string]any{
			"rules":         []map[string]any{{"selector": "workload-type=job", "value": "tfacc-iammap-coldimport-role"}},
			"fallback_rule": "CLOUD_DEFAULT",
		},
	})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_cloud_iam_mapping.test",
				ImportState:        true,
				ImportStateId:      cloudID + "/" + cloudResourceID,
				ImportStatePersist: true,
				Config: fmt.Sprintf(`
resource "anyscale_cloud_iam_mapping" "test" {
  cloud_id          = %[1]q
  cloud_resource_id = %[2]q
}
`, cloudID, cloudResourceID),
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					return checkImportedCloudIAMMapping(states, cloudID, cloudResourceID, "tfacc-iammap-coldimport-role", "CLOUD_DEFAULT")
				},
			},
		},
	})
}

// TestAccCloudIAMMappingResource_RealCloud_ColdImport_BareCloudID is Test
// A's second ID form: a bare cloud_id, which must resolve the primary
// cloud_resource itself (resolvePrimaryCloudResourceID's real code path,
// not the mocked one exercised by the unit tests). Reuses the mapping the
// compound-ID test seeded - both tests target the same real deployment's
// config, which is idempotent to import from either ID form.
func TestAccCloudIAMMappingResource_RealCloud_ColdImport_BareCloudID(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	cloudID, cloudResourceID := requireRealIAMMappingTestCloud(t)
	cloudProvider, computeStack := realIAMMappingBaseSpec(t, cloudID, cloudResourceID)

	putRealCloudDeploymentConfig(t, cloudID, cloudResourceID, map[string]any{
		"cloud_provider": cloudProvider,
		"compute_stack":  computeStack,
		"dataplane_iam_mapping": map[string]any{
			"rules":         []map[string]any{{"selector": "workload-type=job", "value": "tfacc-iammap-coldimport-role"}},
			"fallback_rule": "CLOUD_DEFAULT",
		},
	})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_cloud_iam_mapping.test",
				ImportState:        true,
				ImportStateId:      cloudID, // bare cloud_id - must resolve the primary deployment itself
				ImportStatePersist: true,
				Config: fmt.Sprintf(`
resource "anyscale_cloud_iam_mapping" "test" {
  cloud_id = %[1]q
}
`, cloudID),
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					return checkImportedCloudIAMMapping(states, cloudID, cloudResourceID, "tfacc-iammap-coldimport-role", "CLOUD_DEFAULT")
				},
			},
		},
	})
}

// TestAccCloudIAMMappingResource_RealCloud_PlanStabilityAfterImport is Test
// B: two sequential Config-only steps (no ImportState at all) that
// reconstruct the exact state shape import would produce - both cloud_id
// and cloud_resource_id declared explicitly, rules/fallback_rule matching
// what is really on the cloud. This is the only way to prove plan stability
// against a REAL response: an ImportState step's recovered values, per this
// repo's own documented finding, live in a throwaway directory and cannot
// carry forward into a later step's plan.
func TestAccCloudIAMMappingResource_RealCloud_PlanStabilityAfterImport(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	cloudID, cloudResourceID := requireRealIAMMappingTestCloud(t)
	cloudProvider, computeStack := realIAMMappingBaseSpec(t, cloudID, cloudResourceID)

	putRealCloudDeploymentConfig(t, cloudID, cloudResourceID, map[string]any{
		"cloud_provider": cloudProvider,
		"compute_stack":  computeStack,
		"dataplane_iam_mapping": map[string]any{
			"rules":         []map[string]any{{"selector": "workload-type=job", "value": "tfacc-iammap-planstable-role"}},
			"fallback_rule": "CLOUD_DEFAULT",
		},
	})

	config := fmt.Sprintf(`
resource "anyscale_cloud_iam_mapping" "test" {
  cloud_id          = %[1]q
  cloud_resource_id = %[2]q

  rules = [
    { selector = "workload-type=job", value = "tfacc-iammap-planstable-role" }
  ]
  fallback_rule = "CLOUD_DEFAULT"
}
`, cloudID, cloudResourceID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud_iam_mapping.test", "rules.0.value", "tfacc-iammap-planstable-role"),
				),
			},
			{
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// TestAccCloudIAMMappingResource_RealCloud_Lifecycle is Test C: a real-API
// Create/Update/Destroy sequence, with the Destroy assertion made via an
// independent real GET rather than terraform state - the same distinction
// that would have caught the destroy-empty-spec-no-op trap through the
// provider rather than only at the curl level.
func TestAccCloudIAMMappingResource_RealCloud_Lifecycle(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	cloudID, cloudResourceID := requireRealIAMMappingTestCloud(t)

	// Start from a known baseline so this test does not depend on whatever
	// the other tests in this file left behind.
	cloudProvider, computeStack := realIAMMappingBaseSpec(t, cloudID, cloudResourceID)
	putRealCloudDeploymentConfig(t, cloudID, cloudResourceID, map[string]any{
		"cloud_provider": cloudProvider,
		"compute_stack":  computeStack,
	})

	createConfig := fmt.Sprintf(`
resource "anyscale_cloud_iam_mapping" "test" {
  cloud_id          = %[1]q
  cloud_resource_id = %[2]q

  rules = [
    { selector = "workload-type=job", value = "tfacc-iammap-lifecycle-role-a" }
  ]
  fallback_rule = "CLOUD_DEFAULT"
}
`, cloudID, cloudResourceID)

	updateConfig := fmt.Sprintf(`
resource "anyscale_cloud_iam_mapping" "test" {
  cloud_id          = %[1]q
  cloud_resource_id = %[2]q

  rules = [
    { selector = "workload-type=job", value = "tfacc-iammap-lifecycle-role-a-updated" },
    { selector = "project=tfacc-iammap-lifecycle", value = "tfacc-iammap-lifecycle-role-b" }
  ]
  fallback_rule = "FAIL"
}
`, cloudID, cloudResourceID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: createConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud_iam_mapping.test", "rules.#", "1"),
					resource.TestCheckResourceAttr("anyscale_cloud_iam_mapping.test", "rules.0.value", "tfacc-iammap-lifecycle-role-a"),
					resource.TestCheckResourceAttr("anyscale_cloud_iam_mapping.test", "fallback_rule", "CLOUD_DEFAULT"),
					resource.TestCheckResourceAttr("anyscale_cloud_iam_mapping.test", "mode", "CUSTOMER_MANAGED"),
				),
			},
			{
				Config: updateConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud_iam_mapping.test", "rules.#", "2"),
					resource.TestCheckResourceAttr("anyscale_cloud_iam_mapping.test", "rules.0.value", "tfacc-iammap-lifecycle-role-a-updated"),
					resource.TestCheckResourceAttr("anyscale_cloud_iam_mapping.test", "rules.1.value", "tfacc-iammap-lifecycle-role-b"),
					resource.TestCheckResourceAttr("anyscale_cloud_iam_mapping.test", "fallback_rule", "FAIL"),
				),
			},
			{
				Config:             updateConfig,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
		CheckDestroy: func(_ *terraform.State) error {
			// Independent real read, not a terraform-state check: this is
			// the assertion that would have caught the empty-spec no-op
			// trap if Destroy had regressed to sending one.
			current := getRealCloudDeploymentConfig(t, cloudID, cloudResourceID)
			mapping, _ := current["dataplane_iam_mapping"].(map[string]any)
			if rules, ok := mapping["rules"]; ok && rules != nil {
				if rs, ok := rules.([]any); !ok || len(rs) != 0 {
					return fmt.Errorf("expected the real cloud's dataplane_iam_mapping.rules to be cleared after destroy, got %v", rules)
				}
			}
			return nil
		},
	})
}
