package provider

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestPR2TimeoutDefaults_MatchLockedSpec closes the gap TestTimeoutsCreateResolvesExpectedDuration
// (resource_container_image_build_test.go) cannot: that test imports defaultBuildTimeout on BOTH
// sides of its assertion (got, diags := v.Create(ctx, defaultBuildTimeout); if got != defaultBuildTimeout),
// so it passes unconditionally regardless of what value the constant actually holds - confirmed by
// mutation-testing it directly (temporarily set defaultBuildTimeout to 45m, the test still passed).
// It proves the timeouts library's plumbing works; it cannot prove the constant holds the value
// PR2-TIMEOUTS-PLAN.md actually locked.
//
// This test hardcodes the locked-spec values as independent literals (never importing the
// resource's own constant into the "want" side), so a silent drift in any constant's value - not
// just a broken library call - fails here.
func TestPR2TimeoutDefaults_MatchLockedSpec(t *testing.T) {
	tests := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"service create/update/delete (defaultServiceRolloutTimeout)", defaultServiceRolloutTimeout, 30 * time.Minute},
		{"system_cluster create (defaultSystemClusterCreateTimeout)", defaultSystemClusterCreateTimeout, 20 * time.Minute},
		{"container_image_build create/update (defaultBuildTimeout)", defaultBuildTimeout, 30 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %v, want %v per PR2-TIMEOUTS-PLAN.md's locked spec", tt.name, tt.got, tt.want)
			}
		})
	}
}

// TestPR2TimeoutWiring_OmittedBlockResolvesToDefault proves, for every op every PR2 resource
// declares in its timeouts.Block(ctx, timeouts.Opts{...}), that an omitted timeouts{} block
// resolves through the real terraform-plugin-framework-timeouts library to that resource's actual
// default, for the exact attrTypes shape (which sub-fields exist) each resource's schema declares.
//
// Scope, stated plainly so this isn't overclaimed the way the test it partly supersedes was: this
// calls timeouts.Value.Create/Update/Delete() directly with each resource's own constant passed in
// BY THIS TEST, the same way TestTimeoutsCreateResolvesExpectedDuration (resource_container_image_build_test.go)
// already did for build's Create(). It does NOT invoke the resource's real Create/Update/Delete
// method, so it cannot catch a real call site accidentally passing the WRONG resource's constant
// (e.g. system_cluster's Create calling plan.Timeouts.Create(ctx, defaultServiceRolloutTimeout) by
// copy-paste mistake) - that would silently compile and "work", just resolve to the wrong resource's
// default. Today's 5 real call sites were confirmed correct by direct source read (resource_service.go:624,898,973;
// resource_system_cluster.go:141; resource_container_image_build.go:214,464; resource_cloud.go:1104;
// resource_cloud_resource.go:921) - a one-time manual trace, not an automated regression guard. Closing
// that residual gap for good would need either a go/ast structural check of each call site's argument
// identifier, or extracting timeout resolution into a resource-specific pure function unit tests can
// call directly - both touch resource implementation files outside test ownership, so left as a
// follow-up rather than done here. What IS automated and mutation-proof here is the value half
// (TestPR2TimeoutDefaults_MatchLockedSpec, above) and the library-resolution half (this test).
//
// anyscale_cloud and anyscale_cloud_resource have no named default constant (both call
// plan.Timeouts.Create(ctx, 30*time.Minute) with an inline literal - resource_cloud.go:1104,
// resource_cloud_resource.go:921, confirmed by direct source read, pre-existing behavior PR2 only
// adds the surrounding optional block around). Their subtests below hardcode that same literal -
// also flagged as a follow-up (extract named constants for consistency/testability, matching the
// other 3 resources), not fixed here since it touches resource implementation files outside test
// ownership.
func TestPR2TimeoutWiring_OmittedBlockResolvesToDefault(t *testing.T) {
	ctx := context.Background()
	omitted := func(attrTypes map[string]attr.Type) timeouts.Value {
		return timeouts.Value{Object: types.ObjectNull(attrTypes)}
	}

	t.Run("service", func(t *testing.T) {
		attrTypes := map[string]attr.Type{"create": types.StringType, "update": types.StringType, "delete": types.StringType}
		v := omitted(attrTypes)

		if got, diags := v.Create(ctx, defaultServiceRolloutTimeout); diags.HasError() || got != defaultServiceRolloutTimeout {
			t.Errorf("Create() = %v, diags=%v, want %v", got, diags, defaultServiceRolloutTimeout)
		}
		if got, diags := v.Update(ctx, defaultServiceRolloutTimeout); diags.HasError() || got != defaultServiceRolloutTimeout {
			t.Errorf("Update() = %v, diags=%v, want %v", got, diags, defaultServiceRolloutTimeout)
		}
		if got, diags := v.Delete(ctx, defaultServiceRolloutTimeout); diags.HasError() || got != defaultServiceRolloutTimeout {
			t.Errorf("Delete() = %v, diags=%v, want %v", got, diags, defaultServiceRolloutTimeout)
		}
	})

	t.Run("system_cluster", func(t *testing.T) {
		attrTypes := map[string]attr.Type{"create": types.StringType}
		v := omitted(attrTypes)

		if got, diags := v.Create(ctx, defaultSystemClusterCreateTimeout); diags.HasError() || got != defaultSystemClusterCreateTimeout {
			t.Errorf("Create() = %v, diags=%v, want %v", got, diags, defaultSystemClusterCreateTimeout)
		}
	})

	t.Run("container_image_build_update", func(t *testing.T) {
		// Create() is already covered by TestTimeoutsCreateResolvesExpectedDuration; Update() is not.
		attrTypes := map[string]attr.Type{"create": types.StringType, "update": types.StringType}
		v := omitted(attrTypes)

		if got, diags := v.Update(ctx, defaultBuildTimeout); diags.HasError() || got != defaultBuildTimeout {
			t.Errorf("Update() = %v, diags=%v, want %v", got, diags, defaultBuildTimeout)
		}
	})

	t.Run("cloud", func(t *testing.T) {
		attrTypes := map[string]attr.Type{"create": types.StringType}
		v := omitted(attrTypes)

		const cloudDefaultCreateTimeout = 30 * time.Minute // resource_cloud.go:1104, inline literal - see doc comment above
		if got, diags := v.Create(ctx, cloudDefaultCreateTimeout); diags.HasError() || got != cloudDefaultCreateTimeout {
			t.Errorf("Create() = %v, diags=%v, want %v", got, diags, cloudDefaultCreateTimeout)
		}
	})

	t.Run("cloud_resource", func(t *testing.T) {
		attrTypes := map[string]attr.Type{"create": types.StringType}
		v := omitted(attrTypes)

		const cloudResourceDefaultCreateTimeout = 30 * time.Minute // resource_cloud_resource.go:921, inline literal - see doc comment above
		if got, diags := v.Create(ctx, cloudResourceDefaultCreateTimeout); diags.HasError() || got != cloudResourceDefaultCreateTimeout {
			t.Errorf("Create() = %v, diags=%v, want %v", got, diags, cloudResourceDefaultCreateTimeout)
		}
	})
}
