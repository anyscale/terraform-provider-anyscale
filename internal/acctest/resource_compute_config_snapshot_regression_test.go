package acctest

// This file guards the byte-identical contract from the Option C design
// (approved-design.md): a plain single-resource anyscale_compute_config
// config (no additional_resources block) must send the EXACT SAME
// create/update request shape after the multi-resource generalization as it
// did before. Per the design doc, that claim is an acceptance GATE proven by
// a real request snapshot, not a source-read assumption - this is that gate.
//
// The captured shape asserted below was taken from the actual request this
// mock server received BEFORE any Option C / F1-F7 changes landed. If a
// future change to expand/nodeConfigToAPI/resource_compute_config.go's
// Create legitimately needs to alter this shape, this test must fail loudly
// first - update it deliberately, with the reason written down, not by
// silently loosening the assertion.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccComputeConfigResource_SingleResourceRequestSnapshot_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, state := newMockComputeConfigServerWithState(t)
	const name = "cc-snapshot-mock"

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = "cld_mock_cc"

  head_node = {
    instance_type = "m5.large"
  }

  worker_nodes = [
    {
      name          = "worker-a"
      instance_type = "m5.xlarge"
      min_nodes     = 0
      max_nodes     = 1
      market_type   = "ON_DEMAND"
    }
  ]
}
`, name)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "name", name),
				),
			},
		},
	})

	if len(state.createRequests) == 0 {
		t.Fatal("mock server received no create requests - test did not exercise Create at all")
	}
	got := state.createRequests[0]

	gotJSON, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal captured request: %v", err)
	}

	// The golden shape: a single-resource config sends exactly ONE
	// deployment_configs entry (mirroring the top-level fields), no
	// additional_resources concept anywhere on the wire, and no top-level
	// cloud_resource/deployment selector fields beyond cloud_id. This is the
	// literal shape captured from the real Create call before Option C.
	want := map[string]any{
		"cloud_id": "cld_mock_cc",
		"head_node_type": map[string]any{
			"name":          "head",
			"instance_type": "m5.large",
		},
		"worker_node_types": []any{
			map[string]any{
				"name":                 "worker-a",
				"instance_type":        "m5.xlarge",
				"min_workers":          float64(0),
				"max_workers":          float64(1),
				"use_spot":             false,
				"fallback_to_ondemand": false,
			},
		},
		"flags": map[string]any{
			"allow-cross-zone-autoscaling": false,
		},
		"deployment_configs": []any{
			map[string]any{
				"head_node_type": map[string]any{
					"name":          "head",
					"instance_type": "m5.large",
				},
				"worker_node_types": []any{
					map[string]any{
						"name":                 "worker-a",
						"instance_type":        "m5.xlarge",
						"min_workers":          float64(0),
						"max_workers":          float64(1),
						"use_spot":             false,
						"fallback_to_ondemand": false,
					},
				},
				"flags": map[string]any{
					"allow-cross-zone-autoscaling": false,
				},
			},
		},
	}
	wantJSON, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}

	if string(gotJSON) != string(wantJSON) {
		t.Errorf("single-resource Create request shape changed from the captured baseline.\n"+
			"If this change is deliberate (e.g. a real Option C/F1-F7 fix), update this golden "+
			"snapshot explicitly with the reason in the commit message - do not just relax the "+
			"assertion.\n--- got ---\n%s\n--- want ---\n%s", gotJSON, wantJSON)
	}
}
