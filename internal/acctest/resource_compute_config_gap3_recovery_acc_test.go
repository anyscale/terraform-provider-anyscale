package acctest

// GAP-3 regression (compute-config-import-parity quest, extends AG-2): import
// must recover resources/required_resources/labels/required_labels/node
// cloud_deployment from the real API instead of leaving them null
// (nullAmbiguousImportFields et al, now deleted by forge - c3e8a96). Safety
// for this was live-confirmed across several rounds this quest (assayer's
// G1b/G1b-2/G1c/G1g-corrected/G1h checks): each of these 5 fields comes back
// null when never set (nothing to lose by recovering) and round-trips
// byte-for-byte when genuinely set (nothing invented by recovering). This
// extends AG-2's shape (a genuinely pre-existing, out-of-band fixture, not
// self-seeded by a preceding Create in this same test) rather than narrowing
// the existing ImportStateVerifyIgnore lists in resource_compute_config_
// acc_test.go / _lifecycle_acc_test.go - per forge, those ignores cover a
// SEPARATE, still-standing reason (API-normalized instance_type-derived
// defaults with no prior state to mask against) that GAP-3 does not touch,
// so a dedicated explicit-values case is the correct shape, not a shared one.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

func TestAccComputeConfigResource_ImportRecoversMaskedFieldsWhenSet(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	PreCheck(t)
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("GetTestClient: %v", err)
	}
	cloudID := GetComputeConfigCloudID(t)
	ctx := context.Background()

	name := UniqueName(t, "gap3recover")

	// Built directly (not via CreateEphemeralComputeConfig) since this
	// scenario needs all 5 GAP-3 fields explicitly set at once, including a
	// GPU-satisfying required_labels/required_resources pairing so A1's
	// GPU/TPU cross-validator (a real plan-time HARD error) doesn't trip on
	// the config used for the later import-match step.
	createBody := map[string]any{
		"name":        name,
		"anonymous":   false,
		"new_version": true,
		"config": map[string]any{
			"cloud_id": cloudID,
			"head_node_type": map[string]any{
				"name":          "head",
				"instance_type": "m5.large",
				"resources": map[string]any{
					"cpu": 4,
				},
				"required_resources": map[string]any{
					"gpu": 1,
				},
				"labels": map[string]any{
					"team": "gap3-recovery-check",
				},
				"required_labels": map[string]any{
					"ray.io/accelerator-type": "T4",
				},
				"flags": map[string]any{
					"cloud_deployment": map[string]any{
						"provider": "aws",
						"region":   "us-east-2",
					},
				},
			},
		},
	}
	bodyBytes, err := json.Marshal(createBody)
	if err != nil {
		t.Fatalf("marshal create body: %v", err)
	}
	createRaw, cerr := provider.DoRequestRaw(ctx, client, "POST", "/api/v2/compute_templates/",
		bytes.NewReader(bodyBytes), 200, 201)
	if cerr != nil {
		t.Fatalf("create out-of-band fixture: %v (body=%s)", cerr, string(createRaw))
	}
	var createDecoded map[string]any
	if err := json.Unmarshal(createRaw, &createDecoded); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	result, _ := createDecoded["result"].(map[string]any)
	configID, _ := result["id"].(string)
	if configID == "" {
		t.Fatalf("no id in create response: %#v", createDecoded)
	}
	t.Cleanup(func() {
		archiveRaw, aerr := provider.DoRequestRaw(context.Background(), client, "POST",
			fmt.Sprintf("/api/v2/compute_templates/%s/archive", configID), nil, 200, 202, 204)
		t.Logf("cleanup: archive id=%s -> err=%v body=%s", configID, aerr, string(archiveRaw))
	})

	config := fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q

  head_node = {
    instance_type = "m5.large"
    resources = {
      cpu = 4
    }
    required_resources = {
      gpu = 1
    }
    labels = {
      team = "gap3-recovery-check"
    }
    required_labels = {
      "ray.io/accelerator-type" = "T4"
    }
    cloud_deployment = {
      provider = "aws"
      region   = "us-east-2"
    }
  }
}
`, name, cloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Cold import - this test's own Terraform lifecycle never
				// created configID, the raw API call above did, entirely
				// out of band (same AG-2 shape).
				ResourceName:       "anyscale_compute_config.test",
				ImportState:        true,
				ImportStateId:      configID,
				ImportStatePersist: true,
				Config:             config,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					attrs := states[0].Attributes
					checks := map[string]string{
						"head_node.resources.cpu":                           "4",
						"head_node.required_resources.gpu":                  "1",
						"head_node.labels.team":                             "gap3-recovery-check",
						"head_node.required_labels.ray.io/accelerator-type": "T4",
						"head_node.cloud_deployment.provider":               "aws",
						"head_node.cloud_deployment.region":                 "us-east-2",
					}
					for attr, want := range checks {
						if got := attrs[attr]; got != want {
							return fmt.Errorf("%s = %q, want %q recovered from the real API on import (GAP-3) - got left null/blank means the old nulling behavior is still present", attr, got, want)
						}
					}
					return nil
				},
			},
			{
				// The self-heal bar: a config setting the exact real values
				// must plan clean after import - proving complete state,
				// not just individually-correct attributes.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}
