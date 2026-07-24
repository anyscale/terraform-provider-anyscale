package acctest

// F5/F6 regression (compute-config-import-parity quest): worker-group name
// uniqueness and order stability. Two worker groups sharing an instance_type
// with BOTH names left unset used to both derive the identical default name
// (the instance_type itself) - tolerated by the compute_templates storage
// layer (assayer live-confirmed no collision there), but a real risk once
// Ray's name-keyed autoscaler dict is built from it (forge's source trace).
// F5 makes the derivation unique (deterministic "-2" suffix, client-side,
// before the request is ever sent) and warns. F6 makes a worker_nodes list
// whose backend-returned order differs from the user's own configured order
// resolve back to the CONFIGURED order (by name match, with a uniqueness
// guard), so pure reordering never causes a spurious diff.
//
// These reuse newMockComputeConfigServerWithState (resource_compute_config_
// lifecycle_acc_test.go), NOT a bespoke mock: that shared mock's
// applyServerNormalization already replicates the real backend defaulting an
// EMPTY worker name to its instance_type (confirmed necessary while building
// this file - an ad-hoc mock that just echoes the request verbatim does not
// reproduce this and made an early draft of this test fail for the wrong
// reason, an empty string rather than the real pre-fix collision).

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// F5: two worker groups, same instance_type, both names unset - must NOT
// collide (deterministic unique-derive), and must warn at plan time.
func TestAccComputeConfigResource_DuplicateDerivedWorkerNamesDisambiguated_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	binDir := buildProviderBinaryForCLICheck(t)

	resourceHCL := `
resource "anyscale_compute_config" "test" {
  name     = "f5-duplicate-derived-name-check"
  cloud_id = "cld_validate_only_fake"

  head_node = {
    instance_type = "m5.large"
  }

  worker_nodes = [
    {
      instance_type = "m5.xlarge"
      min_nodes     = 0
      max_nodes     = 1
    },
    {
      instance_type = "m5.xlarge"
      min_nodes     = 0
      max_nodes     = 1
    }
  ]
}
`
	result := runTerraformValidate(t, binDir, resourceHCL)

	found := false
	for _, d := range result.Diagnostics {
		if d.Severity != "warning" {
			continue
		}
		if containsAll(d.Detail, "instance_type", "m5.xlarge") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a warning about the duplicate-instance_type unnamed worker groups, got: %+v", result.Diagnostics)
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func TestAccComputeConfigResource_DuplicateDerivedWorkerNamesGetUniqueSuffix_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server := newMockComputeConfigServer(t)
	name := "cc-f5-suffix"

	config := testAccProviderBlock(server.URL) + `
resource "anyscale_compute_config" "test" {
  name     = "` + name + `"
  cloud_id = "cld_mock_cc"

  head_node = {
    instance_type = "m5.large"
  }

  worker_nodes = [
    {
      instance_type = "m5.xlarge"
      min_nodes     = 0
      max_nodes     = 1
    },
    {
      instance_type = "m5.xlarge"
      min_nodes     = 0
      max_nodes     = 1
    }
  ]
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.name", "m5.xlarge"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.1.name", "m5.xlarge-2"),
				),
			},
		},
	})
}

// F5, additional_resources call site: the disambiguation-index-building loop
// in additionalResourceToDeploymentConfig (compute_config_helpers.go) is a
// SEPARATE copy of the primary path's loop above - only the leaf
// workerNodeConfigToAPI/disambiguateDefaultedWorkerNames functions are
// shared, not the surrounding "is this index eligible" logic itself (forge's
// finding, confirmed against the real code: my own earlier sweep missed this
// second call site by re-deriving via a fresh grep of resource_compute_
// config.go rather than re-checking the additional_resources file I'd
// already read - see the ad-hoc-mock/grep-sweep memory notes). This test is
// the permanent regression proof for that second call site specifically -
// without it, only the primary path's fix is covered by committed CI-gated
// acctests; the additional_resources path's fix would have no acceptance
// coverage beyond forge's own scratch (deleted) verification.
func TestAccComputeConfigResource_AdditionalResourcesDuplicateDerivedWorkerNamesGetUniqueSuffix_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server := newMockComputeConfigServer(t)
	name := "cc-f5-additional-resources-suffix"

	config := testAccProviderBlock(server.URL) + `
resource "anyscale_compute_config" "test" {
  name           = "` + name + `"
  cloud_id       = "cld_mock_cc"
  cloud_resource = "resource-primary"

  head_node = {
    instance_type = "m5.large"
  }

  additional_resources = [
    {
      cloud_resource = "resource-secondary"
      head_node = {
        instance_type = "m5.large"
      }
      worker_nodes = [
        {
          instance_type = "m5.xlarge"
          min_nodes     = 0
          max_nodes     = 1
        },
        {
          instance_type = "m5.xlarge"
          min_nodes     = 0
          max_nodes     = 1
        }
      ]
    }
  ]
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "additional_resources.0.worker_nodes.0.name", "m5.xlarge"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "additional_resources.0.worker_nodes.1.name", "m5.xlarge-2"),
				),
			},
		},
	})
}

// F6: worker_nodes reordered by the backend must resolve back to the
// user's configured order, so a pure backend-side reorder never diffs.
func TestAccComputeConfigResource_WorkerOrderMatchesConfiguredOrder_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, state := newMockComputeConfigServerWithState(t)
	name := "cc-f6-reorder"

	config := testAccProviderBlock(server.URL) + `
resource "anyscale_compute_config" "test" {
  name     = "` + name + `"
  cloud_id = "cld_mock_cc"

  head_node = {
    instance_type = "m5.large"
  }

  worker_nodes = [
    {
      name          = "worker-a"
      instance_type = "m5.large"
      min_nodes     = 0
      max_nodes     = 1
    },
    {
      name          = "worker-b"
      instance_type = "m5.xlarge"
      min_nodes     = 0
      max_nodes     = 1
    }
  ]
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.name", "worker-a"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.1.name", "worker-b"),
				),
			},
			{
				PreConfig: func() {
					// Simulate the backend returning the SAME two workers in
					// the OPPOSITE order on the next Read - a real, if
					// unlikely, scenario per forge's confirmation that order
					// is not backend-meaningful (free to differ across
					// requests). Reverses the stored record's own
					// deployment_configs[0].worker_node_types in place.
					state.mu.Lock()
					for _, record := range state.records {
						cfg, _ := record["config"].(map[string]any)
						dcs, _ := cfg["deployment_configs"].([]any)
						if len(dcs) == 0 {
							continue
						}
						dc0, _ := dcs[0].(map[string]any)
						workers, _ := dc0["worker_node_types"].([]any)
						reversed := make([]any, len(workers))
						for i, w := range workers {
							reversed[len(workers)-1-i] = w
						}
						dc0["worker_node_types"] = reversed
						// Keep the legacy top-level mirror consistent too.
						cfg["worker_node_types"] = reversed
					}
					state.mu.Unlock()
				},
				// A pure backend-side reorder, with no config change, must
				// plan clean - flatten's reorder-to-match-prior-by-name
				// should put worker-a back at index 0 regardless of what
				// order the mock now returns them in.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}
