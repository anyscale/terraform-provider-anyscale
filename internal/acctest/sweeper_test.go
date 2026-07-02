package acctest

import (
	"os"
	"testing"

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
