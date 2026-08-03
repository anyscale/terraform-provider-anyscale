package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Tests the REFUSAL path of the sweep-target org guard - the load-bearing half.
//
// The flag-detection unit test covers whether the guard NOTICES a sweep. This
// covers what it DOES: abort the process before any sweeper runs. That has to be
// a subprocess test, because the guard lives in TestMain and its effect is
// os.Exit(1) - unobservable in-process.
//
// WHY THIS TEST EXISTS AT ALL (tfp-assayer found the gap): a TestMain that exits
// non-zero produces ZERO "--- FAIL" lines. `go vet` passes, the exit code is 1,
// and any reporter grepping for "--- FAIL" says "no test failures". So the
// guard's own firing is invisible to the usual signal, and a mutation that broke
// the refusal would have looked like a clean run. The assertion here is on the
// EXIT CODE and the stderr message, never on test-failure lines.
//
// Safety: ANYSCALE_API_URL points the subprocess at a local httptest server that
// returns an empty cloud list, so the fixture cloud cannot resolve and the guard
// must refuse. Even in the failure case where the guard wrongly allowed the
// sweep, the subprocess's sweepers would talk to that same local server - never
// real infrastructure.
const sweepGuardSubprocessEnv = "ANYSCALE_TEST_SWEEP_GUARD_SUBPROCESS"

func TestSweepOrgGuardRefusesWhenFixtureAbsent(t *testing.T) {
	if os.Getenv(sweepGuardSubprocessEnv) == "1" {
		// Re-entrant guard: this branch is never reached, because TestMain's
		// refusal exits before any test function runs. Present so that if the
		// guard ever fails to fire, this test is what runs and fails loudly
		// rather than the process silently proceeding to real sweepers.
		t.Fatal("guard did not fire: TestMain allowed a sweep against a server with no fixture cloud")
		return
	}

	var cloudsCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v2/clouds"):
			cloudsCalls++
			// Empty list: the pinned fixture cloud does not exist here.
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"results":[],"metadata":{"next_paging_token":null}}`)
		case r.URL.Path == "/api/v2/userinfo":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"result":{"email":"nobody@example.com","organizations":[{"id":"org_x","name":"Definitely Not The Acctest Org"}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cmd := exec.Command(os.Args[0], "-sweep=anyscale", "-sweep-run=")
	cmd.Env = append(os.Environ(),
		sweepGuardSubprocessEnv+"=1",
		"ANYSCALE_API_URL="+srv.URL,
		"ANYSCALE_CLI_TOKEN=fake-token-for-guard-test",
		"TF_ACC=1",
	)
	out, err := cmd.CombinedOutput()
	combined := string(out)

	// 1. It must abort. A nil error means the process exited 0, i.e. the sweep
	//    was allowed to proceed - the exact failure this guard exists to prevent.
	if err == nil {
		t.Fatalf("expected the guard to abort with a non-zero exit; got exit 0.\noutput:\n%s", combined)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected an ExitError from the subprocess, got %T: %v\noutput:\n%s", err, err, combined)
	}
	if code := exitErr.ExitCode(); code != 1 {
		t.Errorf("expected exit code 1 from the refusal, got %d\noutput:\n%s", code, combined)
	}

	// 2. The refusal must be legible. A bare non-zero exit would leave whoever
	//    ran `make sweep` with no idea why - and this guard's whole purpose is
	//    telling them which org they were actually pointed at.
	for _, want := range []string{
		"REFUSING TO SWEEP",
		defaultKnownGoodCloudName,
		"Definitely Not The Acctest Org",
	} {
		if !strings.Contains(combined, want) {
			t.Errorf("refusal message missing %q; got:\n%s", want, combined)
		}
	}

	// 3. It must abort BEFORE sweeping. Sweepers hit many endpoints; the guard
	//    should have consulted the clouds list and then stopped.
	if cloudsCalls == 0 {
		t.Error("guard never queried the clouds list - it cannot have checked the org")
	}
}
