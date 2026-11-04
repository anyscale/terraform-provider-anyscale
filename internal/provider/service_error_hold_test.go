package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// heldErrorEventMessage is the shape of the real ERROR event a split-pattern AWS cloud's
// service reported when its load balancer could not assume the control-plane role (captured
// 2026-11-04; the node account ID and instance ID replaced).
const heldErrorEventMessage = "Service has experienced failure (due to user-error)\n" +
	"Failed to update Cloud resources due to missing or misconfigured permissions: Failed to invoke AWS acm::list_certificates due to insufficient permissions " +
	"(ClientError('An error occurred (AccessDenied) when calling the AssumeRole operation: User: arn:aws:sts::111111111111:assumed-role/cluster_node_role/i-0123456789abcdef0 " +
	"is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::000000000000:role/anyscale-placeholder-example'))\n" +
	"Please run `anyscale cloud verify <cloud_name>` to validate permissions. Reach out to Anyscale support for more details"

// heldObservation is one scripted GET of the service: the fake clock reads at, and the service
// reports state with its primary version in primary ("" means no primary version).
type heldObservation struct {
	at      time.Duration
	state   string
	primary string
}

// heldErrorServer serves the scripted observations in order (repeating the last) and the
// events endpoint. Each service GET moves the fake clock to that observation's offset, so the
// wait loop's now() reads the scripted time right after the GET. The live wire shape for an
// unconfirmed USER_ERROR_FAILURE has error_message null and service_status_checklist null.
func heldErrorServer(t *testing.T, serviceID string, obs []heldObservation, eventsStatus int) (*httptest.Server, func() time.Time, *int) {
	t.Helper()
	start := time.Date(2026, 11, 4, 16, 37, 47, 0, time.UTC)
	var mu sync.Mutex
	clock := start
	n, eventCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/services-v2/" + serviceID:
			o := obs[min(n, len(obs)-1)]
			n++
			clock = start.Add(o.at)
			result := ServiceResult{ID: serviceID, CurrentState: o.state, GoalState: serviceStateRunning}
			if o.primary != "" {
				result.PrimaryVersion = &ServiceVersionResult{ID: "srv_1", CurrentState: o.primary, Weight: 100}
			}
			_ = json.NewEncoder(w).Encode(ServiceResponse{Result: result})
		case "/api/v2/services-v2/" + serviceID + "/events":
			eventCalls++
			if got := r.URL.Query().Get("level"); got != "ERROR" {
				t.Errorf("events level = %q, want ERROR", got)
			}
			if eventsStatus != http.StatusOK {
				w.WriteHeader(eventsStatus)
				return
			}
			_, _ = w.Write([]byte(`{"results": [{"level": "ERROR", "event_type": "USER_ERROR_FAILURE", "message": ` +
				mustJSON(t, heldErrorEventMessage) + `}], "metadata": {"total": 1}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	return server, now, &eventCalls
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func runHeldErrorWait(t *testing.T, target string, obs []heldObservation, eventsStatus int) (*ServiceResult, int, error) {
	t.Helper()
	server, now, eventCalls := heldErrorServer(t, "svc_held", obs, eventsStatus)
	client := NewClientWithToken(server.URL, "test-token")
	// A real deadline turns a wait that never ends (the fake clock stops at the last
	// observation) into a failure instead of a hung test.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service, err := pollServiceState(ctx, client, "svc_held", target,
		time.Hour, time.Millisecond, nil, now, serviceUnconfirmedErrorHold)
	return service, *eventCalls, err
}

const (
	uef = "USER_ERROR_FAILURE"
	run = serviceStateRunning
)

// An error held for just under the bound keeps the wait going, so a service that then
// recovers succeeds.
func TestPollServiceState_UnconfirmedErrorBelowHoldKeepsWaiting(t *testing.T) {
	_, _, err := runHeldErrorWait(t, run, []heldObservation{
		{0, uef, run},
		{serviceUnconfirmedErrorHold - time.Second, uef, run},
		{serviceUnconfirmedErrorHold + 10*time.Second, run, run},
	}, http.StatusOK)
	if err != nil {
		t.Fatalf("error held for under %s ended the wait: %v", serviceUnconfirmedErrorHold, err)
	}
}

// An error held for the full bound fails the wait, quoting the newest ERROR event.
func TestPollServiceState_UnconfirmedErrorAtHoldFails(t *testing.T) {
	_, eventCalls, err := runHeldErrorWait(t, run, []heldObservation{
		{0, uef, run},
		{serviceUnconfirmedErrorHold, uef, run},
		{serviceUnconfirmedErrorHold + 10*time.Second, run, run},
	}, http.StatusOK)
	var held *serviceErrorHeldError
	if !errors.As(err, &held) {
		t.Fatalf("err = %v, want *serviceErrorHeldError", err)
	}
	if held.State != uef || held.Held != serviceUnconfirmedErrorHold {
		t.Errorf("held = %s for %s, want %s for %s", held.State, held.Held, uef, serviceUnconfirmedErrorHold)
	}
	for _, want := range []string{"USER_ERROR_FAILURE for 5m0s", "not authorized to perform: sts:AssumeRole"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
	if eventCalls != 1 {
		t.Errorf("events fetched %d times, want once (failure path only)", eventCalls)
	}
}

// Any non-error observation restarts the clock: an error that clears and recurs is measured
// from the recurrence.
func TestPollServiceState_UnconfirmedErrorClockResetsOnRecovery(t *testing.T) {
	_, eventCalls, err := runHeldErrorWait(t, run, []heldObservation{
		{0, uef, run},
		{4 * time.Minute, "STARTING", run},
		{4*time.Minute + 30*time.Second, uef, run},
		{9*time.Minute + 29*time.Second, uef, run},
		{9*time.Minute + 40*time.Second, run, run},
	}, http.StatusOK)
	if err != nil {
		t.Fatalf("clock did not reset after the error cleared: %v", err)
	}
	if eventCalls != 0 {
		t.Errorf("events fetched %d times on a successful wait, want 0", eventCalls)
	}
}

// The bound applies only while the versions run: an error with the primary version not yet
// RUNNING is left to the existing rules and the timeout.
func TestPollServiceState_UnconfirmedErrorIgnoredWhileVersionNotRunning(t *testing.T) {
	_, _, err := runHeldErrorWait(t, run, []heldObservation{
		{0, uef, "STARTING"},
		{30 * time.Minute, uef, "STARTING"},
		{31 * time.Minute, run, run},
	}, http.StatusOK)
	if err != nil {
		t.Fatalf("error with a non-RUNNING version ended the wait: %v", err)
	}
}

// A TERMINATED wait never applies the bound.
func TestPollServiceState_UnconfirmedErrorIgnoredWhenTerminating(t *testing.T) {
	_, eventCalls, err := runHeldErrorWait(t, serviceStateTerminated, []heldObservation{
		{0, uef, run},
		{30 * time.Minute, uef, run},
		{31 * time.Minute, serviceStateTerminated, run},
	}, http.StatusOK)
	if err != nil {
		t.Fatalf("TERMINATED wait failed on a held error: %v", err)
	}
	if eventCalls != 0 {
		t.Errorf("events fetched %d times, want 0", eventCalls)
	}
}

// If the events call fails, the error still reports the held state and points at the console.
func TestPollServiceState_UnconfirmedErrorEventsUnavailable(t *testing.T) {
	_, _, err := runHeldErrorWait(t, run, []heldObservation{
		{0, uef, run},
		{serviceUnconfirmedErrorHold, uef, run},
	}, http.StatusForbidden)
	var held *serviceErrorHeldError
	if !errors.As(err, &held) {
		t.Fatalf("err = %v, want *serviceErrorHeldError", err)
	}
	if !strings.Contains(err.Error(), "Events tab") {
		t.Errorf("error %q does not point at the console events", err.Error())
	}
}

func TestServiceUnconfirmedErrorHoldIsFiveMinutes(t *testing.T) {
	if serviceUnconfirmedErrorHold != 5*time.Minute {
		t.Fatalf("serviceUnconfirmedErrorHold = %s, want 5m; update the docs and this test together", serviceUnconfirmedErrorHold)
	}
}
