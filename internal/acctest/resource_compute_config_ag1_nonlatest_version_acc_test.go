package acctest

// AG-1 (compute-config-import-parity quest, L0/L7): the missing "retain the
// exact intended version" proof. Existing tests prove config_id changes
// across versions and that the data source's versions list enumerates every
// version, but nothing imports an OLDER, non-latest config_id while a NEWER
// version of the same name also exists and checks that the OLDER version's
// own distinct field values come back - not the latest's. This closes that
// gap: both versions are minted entirely out of band (CreateEphemeralComputeConfig
// / UpdateEphemeralComputeConfig), so this is a pure import test, not a
// create-then-import-back-what-we-just-made test.

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccComputeConfigResource_ImportNonLatestVersionRetainsOwnValues(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	PreCheck(t)
	cloudID := GetComputeConfigCloudID(t)

	const v1InstanceType = "m5.large"
	const v2InstanceType = "m5.xlarge"

	v1, err := CreateEphemeralComputeConfig(t, cloudID, v1InstanceType)
	if err != nil {
		t.Fatalf("failed to create out-of-band v1 fixture: %v", err)
	}
	if v1.Version != 1 {
		t.Logf("note: fresh fixture's first version reported as %d, not necessarily 1 - reading it back rather than assuming, per design", v1.Version)
	}

	v2, err := UpdateEphemeralComputeConfig(t, cloudID, v1.Name, v2InstanceType)
	if err != nil {
		t.Fatalf("failed to mint out-of-band v2 fixture: %v", err)
	}
	if v2.Version <= v1.Version {
		t.Fatalf("expected v2's version (%d) to be greater than v1's (%d) - minting a newer version did not behave as expected", v2.Version, v1.Version)
	}
	if v2.ConfigID == v1.ConfigID {
		t.Fatalf("expected v2 to have a distinct config_id from v1 (cpt_ ids are immutable per version), got the same id %q for both", v1.ConfigID)
	}

	// Import the OLDER (v1) config_id, not the newer one that now exists for
	// the same name - the config below matches v1's OWN instance_type, not
	// v2's, so a no-op plan after import proves the exact intended version
	// (not "latest") was retained.
	config := fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q

  head_node = {
    instance_type = %[3]q
  }
}
`, v1.Name, cloudID, v1InstanceType)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_compute_config.test",
				ImportState:        true,
				ImportStateId:      v1.ConfigID,
				ImportStatePersist: true,
				Config:             config,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					attrs := states[0].Attributes
					if got := attrs["config_id"]; got != v1.ConfigID {
						return fmt.Errorf("config_id = %q, want the OLDER version's id %q (not v2's %q) - import must pin the exact requested version", got, v1.ConfigID, v2.ConfigID)
					}
					if got := attrs["version"]; got != fmt.Sprintf("%d", v1.Version) {
						return fmt.Errorf("version = %q, want v1's own version %d, not v2's %d", got, v1.Version, v2.Version)
					}
					if got := attrs["head_node.instance_type"]; got != v1InstanceType {
						return fmt.Errorf("head_node.instance_type = %q, want v1's OWN value %q - got a value that looks like v2 (%q) or something else, meaning import resolved the wrong version's data", got, v1InstanceType, v2InstanceType)
					}
					return nil
				},
			},
			{
				// No-op bar: a config matching the OLDER version's real
				// values must plan clean - if import had silently resolved
				// to v2 (latest) instead, this step would show a spurious
				// instance_type diff.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}
