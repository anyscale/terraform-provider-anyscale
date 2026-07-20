package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestEvaluateServiceState is the exhaustive, HTTP-free proof of the wait loop's state
// classification (contract §5b): every traced ServiceEventCurrentState bucket, against both
// possible targets (RUNNING for Create/Update, TERMINATED for Delete), plus the "anything else"
// fail-fast default. evaluateServiceState is a pure function, so this needs no mock server at all.
func TestEvaluateServiceState(t *testing.T) {
	errMsg := "backend blew up"
	emptyErrMsg := ""

	cases := []struct {
		name         string
		currentState string
		errorMessage *string
		target       string
		wantDone     bool
		wantErr      bool
		wantErrText  string // substring, only checked when wantErr
	}{
		// Success: current_state matches target, for both real targets this provider uses.
		{"reaches RUNNING target", "RUNNING", nil, serviceStateRunning, true, false, ""},
		{"reaches TERMINATED target", "TERMINATED", nil, serviceStateTerminated, true, false, ""},

		// Error buckets (is_error): terminal failure, done=true, err surfaces error_message.
		{"UNHEALTHY with error_message", "UNHEALTHY", &errMsg, serviceStateRunning, true, true, errMsg},
		{"SYSTEM_FAILURE with error_message", "SYSTEM_FAILURE", &errMsg, serviceStateRunning, true, true, errMsg},
		{"USER_ERROR_FAILURE with error_message", "USER_ERROR_FAILURE", &errMsg, serviceStateRunning, true, true, errMsg},
		// error_message nil or empty must still produce an error (done=true), just without
		// appending a message the backend never sent - a nil-pointer dereference here would be
		// the failure mode this specifically guards against.
		{"UNHEALTHY with nil error_message", "UNHEALTHY", nil, serviceStateRunning, true, true, "UNHEALTHY"},
		{"SYSTEM_FAILURE with empty-string error_message", "SYSTEM_FAILURE", &emptyErrMsg, serviceStateRunning, true, true, "SYSTEM_FAILURE"},
		// Error buckets are terminal regardless of which target the caller is waiting for -
		// Delete's TERMINATED-wait must also stop and surface a SYSTEM_FAILURE, not keep polling
		// past it hoping for TERMINATED.
		{"SYSTEM_FAILURE while waiting for TERMINATED (delete path)", "SYSTEM_FAILURE", &errMsg, serviceStateTerminated, true, true, errMsg},

		// Continue buckets (is_updating + TERMINATING): keep polling, no error, regardless of target.
		{"STARTING continues (waiting for RUNNING)", "STARTING", nil, serviceStateRunning, false, false, ""},
		{"UPDATING continues", "UPDATING", nil, serviceStateRunning, false, false, ""},
		{"ROLLING_OUT continues", "ROLLING_OUT", nil, serviceStateRunning, false, false, ""},
		{"ROLLING_BACK continues", "ROLLING_BACK", nil, serviceStateRunning, false, false, ""},
		{"TERMINATING continues (waiting for TERMINATED)", "TERMINATING", nil, serviceStateTerminated, false, false, ""},
		// STARTING is also a legitimate intermediate state while waiting for TERMINATED to
		// arrive later (e.g. a service was mid-rollout when terminate was requested).
		{"STARTING continues (waiting for TERMINATED)", "STARTING", nil, serviceStateTerminated, false, false, ""},

		// Contract §F6 (ratified, forge not yet landed as of this writing - see the
		// pending-resync note on TestEvaluateServiceState): an unrecognized current_state is
		// NOT a hard error. Architect's ruling deliberately chose CONTINUE over fail-fast here,
		// since the loop is already timeout-bounded (so continuing is not an infinite poll) and
		// the asymmetry favors it - hard-erroring on a new, benign, not-yet-modeled transitional
		// state would break a healthy service's every apply until the provider is patched, which
		// is not user-recoverable, whereas continuing just waits it out (or eventually times out
		// naming the last-seen state if it truly never resolves). Production code is expected to
		// also tflog.Warn on this branch, which a return-value table test can't observe here -
		// that's covered separately if/when forge exposes a way to assert it.
		{"unrecognized state continues (does not hard-error), contract §F6", "SOME_FUTURE_STATE_NOT_YET_MODELED", nil, serviceStateRunning, false, false, ""},
		{"empty current_state continues (does not hard-error), contract §F6", "", nil, serviceStateRunning, false, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &ServiceResult{ID: "svc_test", CurrentState: tc.currentState, ErrorMessage: tc.errorMessage}

			done, err := evaluateServiceState(service, tc.target)

			if done != tc.wantDone {
				t.Errorf("done = %v, want %v", done, tc.wantDone)
			}
			if tc.wantErr && err == nil {
				t.Fatalf("err = nil, want an error containing %q", tc.wantErrText)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), tc.wantErrText) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), tc.wantErrText)
			}
		})
	}
}

// serviceStatePollTestServer serves GET /api/v2/services-v2/{id} with a scripted sequence of
// current_state values: states[0] on the first request, states[1] on the second, and so on;
// once the sequence is exhausted, it keeps repeating the last entry. Models a real rollout's
// state transitions (e.g. STARTING, STARTING, RUNNING) one poll at a time, the same shape
// digestPollTestServer (container_image_helpers_test.go) uses for the build-digest settle race.
func serviceStatePollTestServer(t *testing.T, serviceID string, states []string, errorMessage *string) (server *httptest.Server, requestCount *int32) {
	t.Helper()
	var count int32
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&count, 1))
		idx := n - 1
		if idx >= len(states) {
			idx = len(states) - 1
		}
		result := ServiceResult{ID: serviceID, CurrentState: states[idx]}
		if serviceErrorStates[states[idx]] {
			result.ErrorMessage = errorMessage
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ServiceResponse{Result: result})
	}))
	t.Cleanup(server.Close)
	return server, &count
}

// TestWaitForServiceStateWithTiming_SettlesAtRunning proves the Create/Update path: a service
// that reports STARTING for a couple of polls before reaching RUNNING must be waited out, not
// just checked once - the >=3-request assertion is what actually proves the loop polls
// repeatedly rather than trusting a single GET.
func TestWaitForServiceStateWithTiming_SettlesAtRunning(t *testing.T) {
	server, requestCount := serviceStatePollTestServer(t, "svc_running", []string{"STARTING", "STARTING", "RUNNING"}, nil)
	client := NewClientWithToken(server.URL, "test-token")

	service, err := waitForServiceStateWithTiming(context.Background(), client, "svc_running", serviceStateRunning, 200*time.Millisecond, 5*time.Millisecond)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if service == nil || service.CurrentState != "RUNNING" {
		t.Fatalf("service = %+v, want CurrentState RUNNING", service)
	}
	if got := atomic.LoadInt32(requestCount); got < 3 {
		t.Errorf("requestCount = %d, want at least 3 (2 STARTING polls before RUNNING)", got)
	}
}

// TestWaitForServiceStateWithTiming_SettlesAtTerminated proves the Delete path's own target
// bucket (TERMINATED), distinct from Create/Update's RUNNING - the one place the three call
// sites actually differ.
func TestWaitForServiceStateWithTiming_SettlesAtTerminated(t *testing.T) {
	server, requestCount := serviceStatePollTestServer(t, "svc_terminating", []string{"TERMINATING", "TERMINATED"}, nil)
	client := NewClientWithToken(server.URL, "test-token")

	service, err := waitForServiceStateWithTiming(context.Background(), client, "svc_terminating", serviceStateTerminated, 200*time.Millisecond, 5*time.Millisecond)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if service == nil || service.CurrentState != "TERMINATED" {
		t.Fatalf("service = %+v, want CurrentState TERMINATED", service)
	}
	if got := atomic.LoadInt32(requestCount); got < 2 {
		t.Errorf("requestCount = %d, want at least 2 (1 TERMINATING poll before TERMINATED)", got)
	}
}

// TestWaitForServiceStateWithTiming_SurfacesSystemFailure is the AC-R4 headline error-path
// proof: a rollout that fails must surface as a returned error carrying error_message, not a
// silent success or a hang - proven through the real polling loop (STARTING first), not just
// evaluateServiceState in isolation.
func TestWaitForServiceStateWithTiming_SurfacesSystemFailure(t *testing.T) {
	wantMsg := "container crashed on startup"
	server, requestCount := serviceStatePollTestServer(t, "svc_failed", []string{"STARTING", "SYSTEM_FAILURE"}, &wantMsg)
	client := NewClientWithToken(server.URL, "test-token")

	service, err := waitForServiceStateWithTiming(context.Background(), client, "svc_failed", serviceStateRunning, 200*time.Millisecond, 5*time.Millisecond)

	if err == nil {
		t.Fatal("err = nil, want an error (service entered SYSTEM_FAILURE)")
	}
	if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("err = %q, want it to contain the surfaced error_message %q", err.Error(), wantMsg)
	}
	// The last-observed service must still be returned alongside the error (not nil) - the
	// resource's Create/Update needs it to report current_state/error_message in diagnostics.
	if service == nil || service.CurrentState != "SYSTEM_FAILURE" {
		t.Fatalf("service = %+v, want the last-observed SYSTEM_FAILURE service returned alongside the error", service)
	}
	if got := atomic.LoadInt32(requestCount); got < 2 {
		t.Errorf("requestCount = %d, want at least 2 (1 STARTING poll before the failure surfaces)", got)
	}
}

// TestWaitForServiceStateWithTiming_SurfacesUnhealthyAndUserErrorFailure covers the other two
// is_error buckets end-to-end through the real loop (evaluateServiceState's own table test
// already proves the classification in isolation; this proves the loop actually stops and
// propagates for these two specific buckets too, not just SYSTEM_FAILURE).
func TestWaitForServiceStateWithTiming_SurfacesUnhealthyAndUserErrorFailure(t *testing.T) {
	for _, state := range []string{"UNHEALTHY", "USER_ERROR_FAILURE"} {
		t.Run(state, func(t *testing.T) {
			msg := "failure: " + state
			server, _ := serviceStatePollTestServer(t, "svc_"+strings.ToLower(state), []string{state}, &msg)
			client := NewClientWithToken(server.URL, "test-token")

			service, err := waitForServiceStateWithTiming(context.Background(), client, "svc_x", serviceStateRunning, 200*time.Millisecond, 5*time.Millisecond)

			if err == nil {
				t.Fatalf("err = nil, want an error for %s", state)
			}
			if !strings.Contains(err.Error(), msg) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), msg)
			}
			if service == nil || service.CurrentState != state {
				t.Fatalf("service = %+v, want CurrentState %s", service, state)
			}
		})
	}
}

// TestWaitForServiceStateWithTiming_TimesOutOnUnrecognizedState proves the specific safety
// argument behind contract §F6 (pending forge resync, see TestEvaluateServiceState): treating
// an unrecognized current_state as CONTINUE instead of a hard error is only safe because the
// timeout backstop still catches a state that genuinely never resolves. A service stuck
// forever on some not-yet-modeled state must still fail the apply eventually (naming that
// state), not hang past the caller's timeout.
func TestWaitForServiceStateWithTiming_TimesOutOnUnrecognizedState(t *testing.T) {
	server, requestCount := serviceStatePollTestServer(t, "svc_unrecognized_stuck", []string{"SOME_FUTURE_STATE_NOT_YET_MODELED"}, nil)
	client := NewClientWithToken(server.URL, "test-token")

	service, err := waitForServiceStateWithTiming(context.Background(), client, "svc_unrecognized_stuck", serviceStateRunning, 17*time.Millisecond, 5*time.Millisecond)

	if err == nil {
		t.Fatal("err = nil, want a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %q, want it to mention timing out (an unrecognized state must not hard-error immediately per §F6, but must still time out rather than hang forever)", err.Error())
	}
	if service == nil || service.CurrentState != "SOME_FUTURE_STATE_NOT_YET_MODELED" {
		t.Fatalf("service = %+v, want the last-observed unrecognized-state service returned alongside the timeout error", service)
	}
	if got := atomic.LoadInt32(requestCount); got < 2 {
		t.Errorf("requestCount = %d, want at least 2 (proves it actually polled/continued rather than stopping on the first unrecognized response)", got)
	}
}

// TestWaitForServiceStateWithTiming_TimesOut proves a rollout that never settles gives up
// gracefully within the caller's timeout rather than hanging - critical since a stuck
// ROLLING_OUT must not leave a Terraform apply blocked forever.
func TestWaitForServiceStateWithTiming_TimesOut(t *testing.T) {
	server, requestCount := serviceStatePollTestServer(t, "svc_stuck", []string{"ROLLING_OUT"}, nil)
	client := NewClientWithToken(server.URL, "test-token")

	start := time.Now()
	service, err := waitForServiceStateWithTiming(context.Background(), client, "svc_stuck", serviceStateRunning, 17*time.Millisecond, 5*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("err = nil, want a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %q, want it to mention timing out", err.Error())
	}
	if service == nil || service.CurrentState != "ROLLING_OUT" {
		t.Fatalf("service = %+v, want the last-observed ROLLING_OUT service returned alongside the timeout error", service)
	}
	if got := atomic.LoadInt32(requestCount); got == 0 {
		t.Error("requestCount = 0, want at least one poll before timing out")
	}
	// Sanity bound so a regression that ignores the timeout entirely (e.g. falls through to
	// waiting the interval's max or some unrelated constant) fails loudly instead of just
	// running slow - this must give up within roughly one interval of the deadline, not seconds.
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %s, want well under 500ms (timeout=17ms, interval=5ms) - looks like it did not actually honor the short timeout", elapsed)
	}
}

// TestWaitForServiceStateWithTiming_ContextCancelled proves an already-cancelled context (e.g.
// Terraform interrupting the apply) stops the wait instead of spending the full timeout
// polling. Unlike waitForBuildDigestWithTiming (which checks ctx.Done() before its first
// request and so makes exactly zero calls), this loop's first getServiceByID call is
// unconditional - unfired at ctx.Done() until the eventual server round-trip - so what this
// actually proves is that Go's context-aware HTTP client fails the request promptly and the
// loop propagates that error rather than looping past it, NOT that zero network calls occur.
// Documented deliberately so a future reader does not assume both wait helpers share the exact
// same "zero calls on cancellation" guarantee - they do not, by construction.
func TestWaitForServiceStateWithTiming_ContextCancelled(t *testing.T) {
	server, _ := serviceStatePollTestServer(t, "svc_cancelled", []string{"STARTING"}, nil)
	client := NewClientWithToken(server.URL, "test-token")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	service, err := waitForServiceStateWithTiming(ctx, client, "svc_cancelled", serviceStateRunning, time.Second, time.Millisecond)

	if err == nil {
		t.Fatal("err = nil, want an error (context already cancelled)")
	}
	// getServiceByID's own request fails against the cancelled context, so this returns
	// (nil, err) - NOT (service, err) - unlike the timeout/error-bucket paths above, which
	// always have a real last-observed service to return alongside the error. A caller that
	// unconditionally dereferences the returned service on ANY error would panic specifically
	// on this path.
	if service != nil {
		t.Errorf("service = %+v, want nil on a request-level failure (context cancelled before any successful GET)", service)
	}
}

// TestGetServiceByID_HitsServicesV2Endpoint pins the same real mount-path bug class the data
// source already hit once (service-api-mount-path-services-v2-not-services) at this new call
// site: getServiceByID is shared by the wait loop AND the resource's own Read, so a regression
// here would silently 404 both.
func TestGetServiceByID_HitsServicesV2Endpoint(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ServiceResponse{Result: ServiceResult{ID: "svc_path_test", CurrentState: "RUNNING"}})
	}))
	defer server.Close()
	client := NewClientWithToken(server.URL, "test-token")

	service, err := getServiceByID(context.Background(), client, "svc_path_test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if service.ID != "svc_path_test" {
		t.Errorf("ID = %q, want svc_path_test", service.ID)
	}
	wantPath := "/api/v2/services-v2/svc_path_test"
	if gotPath != wantPath {
		t.Errorf("request path = %q, want %q (the hyphenated -v2 mount path, not /api/v2/services)", gotPath, wantPath)
	}
}

// TestWaitForServiceState_PinsRealInterval proves the thin production wrapper actually pins
// defaultServiceRolloutPollInterval rather than e.g. accidentally passing 0 (which would busy-
// loop) or forgetting to wire it at all. Does not wait a real 10s: instead confirms a service
// already in RUNNING settles on the very first poll (no interval sleep needed either way), then
// separately asserts the pinned constant's value directly so a change to the real production
// timing is a deliberate, visible diff rather than silent.
func TestWaitForServiceState_PinsRealInterval(t *testing.T) {
	if defaultServiceRolloutPollInterval != 10*time.Second {
		t.Fatalf("defaultServiceRolloutPollInterval = %s, want 10s (contract §5b production timing) - if this changed deliberately, update this assertion too", defaultServiceRolloutPollInterval)
	}

	server, requestCount := serviceStatePollTestServer(t, "svc_already_running", []string{"RUNNING"}, nil)
	client := NewClientWithToken(server.URL, "test-token")

	service, err := waitForServiceState(context.Background(), client, "svc_already_running", serviceStateRunning, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if service.CurrentState != "RUNNING" {
		t.Errorf("CurrentState = %q, want RUNNING", service.CurrentState)
	}
	if got := atomic.LoadInt32(requestCount); got != 1 {
		t.Errorf("requestCount = %d, want exactly 1 (already RUNNING on the first poll, no interval sleep needed to observe this)", got)
	}
}
