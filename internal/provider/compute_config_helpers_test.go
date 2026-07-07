package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func mustFloat64Map(t *testing.T, ctx context.Context, m map[string]float64) types.Map {
	t.Helper()
	v, diags := types.MapValueFrom(ctx, types.Float64Type, m)
	if diags.HasError() {
		t.Fatalf("failed to build test map: %v", diags)
	}
	return v
}

// TestRestoreMapKeyCasing pins task 451e2845's other half: Anyscale's API
// normalizes resource-map keys (e.g. "CPU" -> "cpu") regardless of how the
// user configured them, so reading the API's casing straight into state
// diverges from plan on every single refresh. restoreMapKeyCasing repairs
// this by restoring each API key's casing from the case-insensitively
// matching key in prior state, traced to resource_compute_config.go:1091
// where the request side forces the lowercase canonical key before the API
// call - the request side is correct to do that; the bug was state not
// reflecting it back.
func TestRestoreMapKeyCasing(t *testing.T) {
	ctx := context.Background()

	t.Run("restores case-insensitively matching prior key", func(t *testing.T) {
		apiMap := mustFloat64Map(t, ctx, map[string]float64{"cpu": 4, "gpu": 1})
		priorMap := mustFloat64Map(t, ctx, map[string]float64{"CPU": 4, "GPU": 1})

		got := restoreMapKeyCasing(ctx, apiMap, priorMap)

		want := mustFloat64Map(t, ctx, map[string]float64{"CPU": 4, "GPU": 1})
		if !got.Equal(want) {
			t.Errorf("restoreMapKeyCasing() = %v, want %v - task 451e2845", got, want)
		}
	})

	t.Run("leaves a key with no prior match in the API's casing", func(t *testing.T) {
		apiMap := mustFloat64Map(t, ctx, map[string]float64{"cpu": 4, "custom_resource": 2})
		priorMap := mustFloat64Map(t, ctx, map[string]float64{"CPU": 4})

		got := restoreMapKeyCasing(ctx, apiMap, priorMap)

		want := mustFloat64Map(t, ctx, map[string]float64{"CPU": 4, "custom_resource": 2})
		if !got.Equal(want) {
			t.Errorf("restoreMapKeyCasing() = %v, want %v - a genuinely new key keeps the API's casing", got, want)
		}
	})

	t.Run("passes through unchanged when there is no prior state", func(t *testing.T) {
		apiMap := mustFloat64Map(t, ctx, map[string]float64{"cpu": 4})
		priorMap := types.MapNull(types.Float64Type)

		got := restoreMapKeyCasing(ctx, apiMap, priorMap)

		if !got.Equal(apiMap) {
			t.Errorf("restoreMapKeyCasing() = %v, want unchanged %v - no prior means nothing to restore", got, apiMap)
		}
	})
}
