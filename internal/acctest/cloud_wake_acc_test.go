package acctest

import (
	"os"
	"testing"
)

// Harness entry points for scheduled-acctest.yml's hibernation rerun (see
// .github/scripts/acctest-with-hibernation-rerun.sh). Both skip unless their
// own environment variable is set, so no other run executes them.

// TestAccWakeGate runs the wake gate on every cloud a compute-config test can
// be handed, waiting up to the gate's bound for a hibernated cloud to wake.
// The rerun script runs it alone before re-running tests that failed on
// hibernation. Its name matches neither CI shard pattern.
func TestAccWakeGate(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	if os.Getenv("ANYSCALE_TEST_WAKE_GATE") != "1" {
		t.Skip("set ANYSCALE_TEST_WAKE_GATE=1 to run the wake gate on its own")
	}
	GetComputeConfigCloudID(t)
	GetAllVMClouds(t)
}

// TestAccHibernationRerunControlResource is the positive control for the
// rerun: with ANYSCALE_TEST_FORCE_RERUN_CONTROL=hibernation it fails with the
// backend's hibernation detail on the first attempt and passes on the rerun
// (ANYSCALE_TEST_RERUN=1); with =other it fails with an unrelated error, which
// must not be rerun. Only a workflow_dispatch input sets it.
func TestAccHibernationRerunControlResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	mode := os.Getenv("ANYSCALE_TEST_FORCE_RERUN_CONTROL")
	switch {
	case mode == "":
		t.Skip("set ANYSCALE_TEST_FORCE_RERUN_CONTROL=hibernation|other to force a failure")
	case os.Getenv("ANYSCALE_TEST_RERUN") == "1":
		t.Log("rerun attempt: passing")
	case mode == "hibernation":
		t.Fatal("forced by ANYSCALE_TEST_FORCE_RERUN_CONTROL: Failed to create compute config (HTTP 599): " +
			"Cloud cld_control has been hibernated due to inactivity. The cloud is being started and should be available in 5-10 minutes.")
	default:
		t.Fatalf("forced by ANYSCALE_TEST_FORCE_RERUN_CONTROL=%s: a failure unrelated to hibernation", mode)
	}
}
