package acctest

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestMain(m *testing.M) {
	resource.TestMain(m)
}

// isSweepDryRun reports whether ANYSCALE_SWEEP_DRY_RUN is set. Every sweeper's
// delete/archive helper MUST check this immediately before its mutating
// DoRequest call and log-and-return instead of sending it. CLAUDE.md and
// `make sweep-dry-run` have documented this env var as a safe preview mode
// since it was introduced, but nothing ever actually read it — dry-run was a
// full-strength sweep under a misleading name. Discovered 2026-07-02 when a
// "dry run" archived 96 real container images.
func isSweepDryRun() bool {
	return os.Getenv("ANYSCALE_SWEEP_DRY_RUN") != ""
}

// sweepableResourcePrefixes are the only name prefixes any sweeper will ever
// delete - a safety invariant shared across every resource type (cloud,
// compute_config, container_image, project, global_resource_scheduler, ...).
// Ephemeral clouds are named "tfacc-ephemeral-<nanos>" (see
// createEphemeralTestCloud), which already starts with "tfacc-", so no
// resource type needs a separate prefix list of its own.
var sweepableResourcePrefixes = []string{"tfacc-", "tf-test-", "tfprovider-"}

// hasAnyPrefix reports whether s starts with any of prefixes. Shared by every
// sweeper's name-matching guard.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// defaultSweepMinAge is the default minimum resource age every sweeper
// requires before it will delete a match, fed through resolveSweepMinAge
// below (overridable via ANYSCALE_SWEEP_MIN_AGE). Shared by every sweeper -
// there is no actual per-resource-type variation today.
const defaultSweepMinAge = 2 * time.Hour

// resolveSweepMinAge returns defaultMinAge, or the ANYSCALE_SWEEP_MIN_AGE
// override if set (time.ParseDuration syntax). Every sweeper uses this same
// age guard to avoid racing live tests.
func resolveSweepMinAge(defaultMinAge time.Duration) (time.Duration, error) {
	raw := os.Getenv("ANYSCALE_SWEEP_MIN_AGE")
	if raw == "" {
		return defaultMinAge, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid ANYSCALE_SWEEP_MIN_AGE %q: %w", raw, err)
	}
	return parsed, nil
}
