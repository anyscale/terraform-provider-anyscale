package acctest

import (
	"os"
	"strings"
	"testing"
)

// TestSchedulerSweeperGatedBehindGRSEnabled guards the disarm fix for the
// anyscale_global_resource_scheduler sweeper. GRS is unregistered in
// provider.go (its resource and data sources are commented out), so nothing
// in the default build can create a machine pool through this provider and
// the sweeper has no legitimate target - its real
// POST /api/v2/machine_pools/delete must not compile into the default build.
//
// This can only be checked by reading the source directly: a test gated by
// the same build tag would simply not run when the tag is absent, so it could
// never observe the tag having been removed. Reads sweeper_scheduler_test.go
// itself and asserts its first line is the build constraint that also gates
// resource_global_resource_scheduler_acc_test.go and
// data_source_global_resource_scheduler_acc_test.go, so all three re-arm
// together the moment GRS is re-enabled and stay disarmed together until then.
//
// Not a TestAcc* name on purpose: pure source-inspection, no API calls.
func TestSchedulerSweeperGatedBehindGRSEnabled(t *testing.T) {
	const path = "sweeper_scheduler_test.go"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	firstLine, _, _ := strings.Cut(string(src), "\n")
	firstLine = strings.TrimSpace(firstLine)
	if firstLine != "//go:build grs_enabled" {
		t.Fatalf("%s must open with `//go:build grs_enabled` (got %q) - without it, "+
			"this file (and its real POST /api/v2/machine_pools/delete) compiles into "+
			"the default build, and the daily sweep.yml job deletes real machine pools "+
			"for a resource type provider.go does not register", path, firstLine)
	}
}
