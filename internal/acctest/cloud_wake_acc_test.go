package acctest

import (
	"os"
	"testing"
	"time"
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
// rerun. Only a workflow_dispatch input sets ANYSCALE_TEST_FORCE_RERUN_CONTROL:
//
//   - hibernation: fails with the backend's hibernation detail on the first
//     attempt and passes on the rerun (ANYSCALE_TEST_RERUN=1).
//   - other: fails with an unrelated error, which must not be rerun.
//   - mixed: two parallel subtests fail with interleaved output, one on
//     hibernation and one for an unrelated reason. Nothing must be rerun, and
//     each failure must be attributed to its own subtest.
func TestAccHibernationRerunControlResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	mode := os.Getenv("ANYSCALE_TEST_FORCE_RERUN_CONTROL")
	switch {
	case mode == "":
		t.Skip("set ANYSCALE_TEST_FORCE_RERUN_CONTROL=hibernation|other|mixed to force a failure")
	case os.Getenv("ANYSCALE_TEST_RERUN") == "1":
		t.Log("rerun attempt: passing")
	case mode == "hibernation":
		t.Fatal(forcedHibernationFailure)
	case mode == "mixed":
		forceInterleavedFailures(t)
	default:
		t.Fatalf("forced by ANYSCALE_TEST_FORCE_RERUN_CONTROL=%s: a failure unrelated to hibernation", mode)
	}
}

const forcedHibernationFailure = "forced by ANYSCALE_TEST_FORCE_RERUN_CONTROL: Failed to create compute config (HTTP 599): " +
	"Cloud cld_control has been hibernated due to inactivity. The cloud is being started and should be available in 5-10 minutes."

// forceInterleavedFailures runs two parallel subtests that take turns logging,
// so their output interleaves in the -v log, then both fail. Each wait is
// bounded: with no free -parallel slot the other subtest may not run yet.
func forceInterleavedFailures(t *testing.T) {
	hibernationLogged, otherLogged := make(chan struct{}), make(chan struct{})
	wait := func(ch chan struct{}) {
		select {
		case <-ch:
		case <-time.After(30 * time.Second):
		}
	}
	t.Run("hibernation", func(t *testing.T) {
		t.Parallel()
		t.Log("hibernation subtest: about to fail")
		close(hibernationLogged)
		wait(otherLogged)
		t.Fatal(forcedHibernationFailure)
	})
	t.Run("other", func(t *testing.T) {
		t.Parallel()
		wait(hibernationLogged)
		t.Log("other subtest: about to fail")
		close(otherLogged)
		t.Fatal("forced by ANYSCALE_TEST_FORCE_RERUN_CONTROL=mixed: a failure unrelated to hibernation")
	})
}
