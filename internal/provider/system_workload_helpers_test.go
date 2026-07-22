package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// systemClusterMockServer is a stateful describe_system_workload mock: it serves states[n] on
// the nth request (clamped to the last entry once exhausted, matching serviceStatePollTestServer's
// shape in service_helpers_test.go) and records the start_cluster query value seen on every
// request, so a test can assert AC17 (every poll must pass start_cluster=false) directly against
// the wire, not just against what the Go code intends to send.
type systemClusterMockServer struct {
	mu                 sync.Mutex
	states             []string
	requestCount       int32
	startClusterValues []string
}

func newSystemClusterStatePollTestServer(t *testing.T, states []string) (*httptest.Server, *systemClusterMockServer) {
	t.Helper()
	s := &systemClusterMockServer{states: states}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		idx := int(s.requestCount)
		if idx >= len(s.states) {
			idx = len(s.states) - 1
		}
		s.requestCount++
		s.startClusterValues = append(s.startClusterValues, r.URL.Query().Get("start_cluster"))
		status := s.states[idx]
		s.mu.Unlock()

		result := DescribeSystemWorkloadResult{Status: &status}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(DescribeSystemWorkloadResponse{Result: result})
	}))
	t.Cleanup(server.Close)
	return server, s
}

func (s *systemClusterMockServer) snapshot() (requestCount int32, startClusterValues []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requestCount, append([]string(nil), s.startClusterValues...)
}

func TestEvaluateSystemClusterState(t *testing.T) {
	running := "Running"
	startingUp := "StartingUp"
	startupErrored := "StartupErrored"
	updatingErrored := "UpdatingErrored"
	terminatingErrored := "TerminatingErrored"
	terminating := "Terminating"
	unknown := "Unknown"
	terminated := "Terminated"

	tests := []struct {
		name       string
		status     *string
		target     string
		wantDone   bool
		wantErr    bool
		wantErrSub string
	}{
		{name: "matches target", status: &running, target: systemClusterStateRunning, wantDone: true, wantErr: false},
		{name: "nil status does not match target", status: nil, target: systemClusterStateRunning, wantDone: false, wantErr: false},
		{name: "still starting up", status: &startingUp, target: systemClusterStateRunning, wantDone: false, wantErr: false},
		{name: "StartupErrored is terminal failure", status: &startupErrored, target: systemClusterStateRunning, wantDone: true, wantErr: true, wantErrSub: "StartupErrored"},
		{name: "UpdatingErrored is terminal failure", status: &updatingErrored, target: systemClusterStateRunning, wantDone: true, wantErr: true, wantErrSub: "UpdatingErrored"},
		{name: "TerminatingErrored is terminal failure", status: &terminatingErrored, target: systemClusterStateRunning, wantDone: true, wantErr: true, wantErrSub: "TerminatingErrored"},
		{name: "Terminating is not terminal - continues (F6)", status: &terminating, target: systemClusterStateRunning, wantDone: false, wantErr: false},
		{name: "Unknown is not terminal - continues (F6)", status: &unknown, target: systemClusterStateRunning, wantDone: false, wantErr: false},
		{name: "Terminated is not terminal - continues (confirmed live: the normal first-poll-after-Create response)", status: &terminated, target: systemClusterStateRunning, wantDone: false, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done, err := evaluateSystemClusterState(&DescribeSystemWorkloadResult{Status: tt.status}, tt.target, "cld_test")
			if done != tt.wantDone {
				t.Errorf("done = %v, want %v", done, tt.wantDone)
			}
			if tt.wantErr && err == nil {
				t.Fatal("err = nil, want an error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if tt.wantErrSub != "" && !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), tt.wantErrSub)
			}
		})
	}
}

// TestEvaluateSystemClusterState_ErrorNamesTheCloud proves the terminal-failure error identifies
// which cloud it's about - describe's response has no separate message field to enrich the
// error with (see systemClusterErrorStates' doc comment), so the cloud id is the one piece of
// context this provider can add.
func TestEvaluateSystemClusterState_ErrorNamesTheCloud(t *testing.T) {
	errored := "StartupErrored"
	_, err := evaluateSystemClusterState(&DescribeSystemWorkloadResult{Status: &errored}, systemClusterStateRunning, "cld_specific")
	if err == nil {
		t.Fatal("err = nil, want an error")
	}
	if !strings.Contains(err.Error(), "cld_specific") {
		t.Errorf("err = %q, want it to name the cloud id cld_specific", err.Error())
	}
}

// TestWaitForSystemClusterStateWithTiming_TerminatedThenRunningIsQuiet proves the fix for
// architect's "quiet one expected log warning" review note: describe(start_cluster=true)
// against a cloud with no prior cluster is confirmed live (AC26) to create one and return
// status=Terminated immediately, before the async StartingUp transition is even visible - so
// this is the expected first-poll response on every Create, not an edge case. This test proves
// it settles at Running without erroring, same as any other transitional sequence.
func TestWaitForSystemClusterStateWithTiming_TerminatedThenRunningIsQuiet(t *testing.T) {
	server, mock := newSystemClusterStatePollTestServer(t, []string{"Terminated", "StartingUp", "Running"})
	client := NewClientWithToken(server.URL, "test-token")

	result, err := waitForSystemClusterStateWithTiming(context.Background(), client, "cld_fresh", systemClusterStateRunning, 200*time.Millisecond, 5*time.Millisecond)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Status == nil || *result.Status != "Running" {
		t.Fatalf("result = %+v, want Status Running", result)
	}
	if count, _ := mock.snapshot(); count < 3 {
		t.Errorf("requestCount = %d, want at least 3 (Terminated + StartingUp polls before Running)", count)
	}
}

// TestWaitForSystemClusterStateWithTiming_SettlesAtRunning is AC12: a cluster that reports
// transitional states for a couple of polls before reaching Running must be waited out, not
// just checked once - the >=3-request assertion is what actually proves the loop polls
// repeatedly rather than trusting a single describe call.
func TestWaitForSystemClusterStateWithTiming_SettlesAtRunning(t *testing.T) {
	server, mock := newSystemClusterStatePollTestServer(t, []string{"StartingUp", "StartingUp", "Running"})
	client := NewClientWithToken(server.URL, "test-token")

	result, err := waitForSystemClusterStateWithTiming(context.Background(), client, "cld_abc", systemClusterStateRunning, 200*time.Millisecond, 5*time.Millisecond)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Status == nil || *result.Status != "Running" {
		t.Fatalf("result = %+v, want Status Running", result)
	}
	if count, _ := mock.snapshot(); count < 3 {
		t.Errorf("requestCount = %d, want at least 3 (2 StartingUp polls before Running)", count)
	}
}

// TestWaitForSystemClusterStateWithTiming_SurfacesTerminalErrors is AC13, covering each of the
// three terminal-error buckets through the real polling loop (StartingUp first), not just
// evaluateSystemClusterState in isolation. Note: unlike Service's equivalent, describe's
// response carries no separate error-message field (traced directly against
// DescribeSystemWorkloadResponse in the real backend - cluster_id/workload_names/
// workload_service_url/workload_service_url_auth/status/is_enabled, nothing else), so there is
// no richer message to assert beyond the state name itself - a documented gap, not an omission
// in this test.
func TestWaitForSystemClusterStateWithTiming_SurfacesTerminalErrors(t *testing.T) {
	for _, state := range []string{"StartupErrored", "UpdatingErrored", "TerminatingErrored"} {
		t.Run(state, func(t *testing.T) {
			server, mock := newSystemClusterStatePollTestServer(t, []string{"StartingUp", state})
			client := NewClientWithToken(server.URL, "test-token")

			result, err := waitForSystemClusterStateWithTiming(context.Background(), client, "cld_x", systemClusterStateRunning, 200*time.Millisecond, 5*time.Millisecond)

			if err == nil {
				t.Fatalf("err = nil, want an error for %s", state)
			}
			if !strings.Contains(err.Error(), state) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), state)
			}
			if result == nil || result.Status == nil || *result.Status != state {
				t.Fatalf("result = %+v, want the last-observed %s result returned alongside the error", result, state)
			}
			if count, _ := mock.snapshot(); count < 2 {
				t.Errorf("requestCount = %d, want at least 2 (1 StartingUp poll before the failure surfaces)", count)
			}
		})
	}
}

// TestWaitForSystemClusterStateWithTiming_ContinuesOnUnknownAndTerminating is AC14: Terminating
// and Unknown are deliberately excluded from systemClusterContinueStates (per architect's
// ruling, since the backend's own no-op-retry-start bucket excludes them too), so this proves
// the F6 fallback still treats them as "keep polling", not a hard error, by settling at Running
// right after seeing both.
func TestWaitForSystemClusterStateWithTiming_ContinuesOnUnknownAndTerminating(t *testing.T) {
	server, mock := newSystemClusterStatePollTestServer(t, []string{"Terminating", "Unknown", "Running"})
	client := NewClientWithToken(server.URL, "test-token")

	result, err := waitForSystemClusterStateWithTiming(context.Background(), client, "cld_edge", systemClusterStateRunning, 200*time.Millisecond, 5*time.Millisecond)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Status == nil || *result.Status != "Running" {
		t.Fatalf("result = %+v, want Status Running", result)
	}
	if count, _ := mock.snapshot(); count < 3 {
		t.Errorf("requestCount = %d, want at least 3 (Terminating + Unknown polls before Running)", count)
	}
}

// TestWaitForSystemClusterStateWithTiming_TimesOutOnUnrecognizedState is the other half of F6
// (mirrors TestWaitForServiceStateWithTiming_TimesOutOnUnrecognizedState): continuing to poll
// through an edge/unrecognized state must not mean polling forever - the timeout backstop must
// still fire for a state that never resolves.
func TestWaitForSystemClusterStateWithTiming_TimesOutOnUnrecognizedState(t *testing.T) {
	server, mock := newSystemClusterStatePollTestServer(t, []string{"Terminating"})
	client := NewClientWithToken(server.URL, "test-token")

	result, err := waitForSystemClusterStateWithTiming(context.Background(), client, "cld_stuck", systemClusterStateRunning, 17*time.Millisecond, 5*time.Millisecond)

	if err == nil {
		t.Fatal("err = nil, want a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %q, want it to mention timing out", err.Error())
	}
	if result == nil || result.Status == nil || *result.Status != "Terminating" {
		t.Fatalf("result = %+v, want the last-observed Terminating result returned alongside the timeout error", result)
	}
	if count, _ := mock.snapshot(); count == 0 {
		t.Error("requestCount = 0, want at least one poll before timing out")
	}
}

// TestWaitForSystemClusterStateWithTiming_TimesOut is AC15: a recognized transitional state that
// never settles must give up within roughly the caller's timeout, not hang forever. The
// wall-clock sanity bound catches a regression that ignores the timeout entirely.
func TestWaitForSystemClusterStateWithTiming_TimesOut(t *testing.T) {
	server, mock := newSystemClusterStatePollTestServer(t, []string{"StartingUp"})
	client := NewClientWithToken(server.URL, "test-token")

	start := time.Now()
	result, err := waitForSystemClusterStateWithTiming(context.Background(), client, "cld_stuck2", systemClusterStateRunning, 17*time.Millisecond, 5*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("err = nil, want a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %q, want it to mention timing out", err.Error())
	}
	if result == nil || result.Status == nil || *result.Status != "StartingUp" {
		t.Fatalf("result = %+v, want the last-observed StartingUp result returned alongside the timeout error", result)
	}
	if count, _ := mock.snapshot(); count == 0 {
		t.Error("requestCount = 0, want at least one poll before timing out")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %s, want well under 500ms (timeout=17ms, interval=5ms) - looks like it did not honor the short timeout", elapsed)
	}
}

// TestWaitForSystemClusterStateWithTiming_ContextCancelled is AC16: an already-cancelled context
// (e.g. Terraform interrupting the apply) must stop the wait promptly instead of spending the
// full timeout polling.
func TestWaitForSystemClusterStateWithTiming_ContextCancelled(t *testing.T) {
	server, _ := newSystemClusterStatePollTestServer(t, []string{"StartingUp"})
	client := NewClientWithToken(server.URL, "test-token")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := waitForSystemClusterStateWithTiming(ctx, client, "cld_cancel", systemClusterStateRunning, 5*time.Second, 2*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("err = nil, want context.Canceled")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %s, want well under 500ms - cancellation should stop the wait immediately, not wait out the 2s interval", elapsed)
	}
}

// TestWaitForSystemClusterStateWithTiming_AlwaysPassesStartClusterFalse is AC17, the
// MUTATION-PROOF assertion: every single poll this loop issues must send start_cluster=false on
// the wire, never true or omitted (the router defaults a missing start_cluster to true - a loop
// that ever sends true or omits it would silently re-request a start on that tick). This has
// been verified mutation-proof by hand: temporarily changing the describeSystemWorkload call
// inside waitForSystemClusterStateWithTiming from `false` to `true` makes this test fail exactly
// as expected (asserting "false", got "true"), then the change was reverted byte-for-byte.
func TestWaitForSystemClusterStateWithTiming_AlwaysPassesStartClusterFalse(t *testing.T) {
	server, mock := newSystemClusterStatePollTestServer(t, []string{"StartingUp", "StartingUp", "Running"})
	client := NewClientWithToken(server.URL, "test-token")

	_, err := waitForSystemClusterStateWithTiming(context.Background(), client, "cld_safety", systemClusterStateRunning, 200*time.Millisecond, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	count, values := mock.snapshot()
	if count < 3 {
		t.Fatalf("requestCount = %d, want at least 3 to make this assertion meaningful", count)
	}
	for i, v := range values {
		if v != "false" {
			t.Errorf("poll #%d sent start_cluster=%q, want \"false\" on every poll (a bare omission or \"true\" would re-request a start)", i+1, v)
		}
	}
}

func TestDescribeSystemWorkload_HitsExpectedEndpointAndQueryParams(t *testing.T) {
	var gotPath, gotMethod string
	var gotQuery url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotQuery = r.URL.Query()
		if r.Body != nil {
			var buf [1]byte
			if n, _ := r.Body.Read(buf[:]); n != 0 {
				t.Errorf("expected an empty request body (query-param-only call), read %d byte(s)", n)
			}
		}
		status := "Running"
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(DescribeSystemWorkloadResponse{Result: DescribeSystemWorkloadResult{Status: &status, IsEnabled: true}})
	}))
	t.Cleanup(server.Close)

	client := NewClientWithToken(server.URL, "test-token")
	result, err := describeSystemWorkload(context.Background(), client, "cld_123", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Status == nil || *result.Status != "Running" || !result.IsEnabled {
		t.Fatalf("result = %+v, unexpected", result)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/api/v2/system_workload/cld_123/describe" {
		t.Errorf("path = %s, want /api/v2/system_workload/cld_123/describe", gotPath)
	}
	if got := gotQuery.Get("workload_name"); got != systemWorkloadNameRayObsEventsAPIService {
		t.Errorf("workload_name = %q, want %q", got, systemWorkloadNameRayObsEventsAPIService)
	}
	if got := gotQuery.Get("start_cluster"); got != "true" {
		t.Errorf("start_cluster = %q, want \"true\"", got)
	}
	if gotQuery.Has("cloud_resource_id") {
		t.Errorf("cloud_resource_id should never be sent (every real caller omits it), got %q", gotQuery.Get("cloud_resource_id"))
	}
}

func TestEnableSystemCluster_HitsExpectedEndpoint(t *testing.T) {
	var gotPath, gotMethod string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	client := NewClientWithToken(server.URL, "test-token")
	if err := enableSystemCluster(context.Background(), client, "cld_456", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	wantPath := "/api/v2/clouds/cld_456/update_system_cluster_config?is_enabled=true"
	if gotPath != wantPath {
		t.Errorf("path = %s, want %s", gotPath, wantPath)
	}
}

func TestTerminateSystemCluster_HitsExpectedEndpoint(t *testing.T) {
	var gotPath, gotMethod string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{}})
	}))
	t.Cleanup(server.Close)

	client := NewClientWithToken(server.URL, "test-token")
	if err := terminateSystemCluster(context.Background(), client, "cld_789"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/api/v2/system_workload/cld_789/terminate" {
		t.Errorf("path = %s, want /api/v2/system_workload/cld_789/terminate", gotPath)
	}
}

// decoratedSessionsTestServer serves a fixed page of DecoratedSessionResult rows for every
// request to /api/v2/decorated_sessions/, ignoring the (dead, per source) cloud_id param -
// deliberately, since that is exactly the real backend's own behavior findSystemWorkloadCluster
// must work correctly against.
func decoratedSessionsTestServer(t *testing.T, rows []DecoratedSessionResult) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(DecoratedSessionsListResponse{Results: rows})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestFindSystemWorkloadCluster_FindsMatchingCloudAmongMultiple(t *testing.T) {
	// Reproduces the exact hazard forge's correction flagged: decorated_sessions' cloud_id query
	// param is dead server-side, so a multi-cloud org's response can contain sessions for OTHER
	// clouds too. This proves the client-side filter picks the right one, not just the first row.
	rows := []DecoratedSessionResult{
		{ID: "ses_other_cloud", CloudID: strPtr("cld_other"), IsSystemCluster: true},
		{ID: "ses_target_cloud", CloudID: strPtr("cld_target"), IsSystemCluster: true},
		{ID: "ses_unrelated_user_cluster", CloudID: strPtr("cld_target"), IsSystemCluster: false},
	}
	server := decoratedSessionsTestServer(t, rows)
	client := NewClientWithToken(server.URL, "test-token")

	got, err := findSystemWorkloadCluster(context.Background(), client, "cld_target")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.ClusterID != "ses_target_cloud" {
		t.Fatalf("got = %+v, want ClusterID ses_target_cloud", got)
	}
}

func TestFindSystemWorkloadCluster_MatchesViaExpandedCloudObject(t *testing.T) {
	// The wire field that actually carries the cloud identifier is a live_verify_item (source
	// shows both a plain cloud_id scalar and an expanded cloud object as possible carriers) - this
	// proves the fallback to the expanded object works when only that one is populated.
	rows := []DecoratedSessionResult{
		{ID: "ses_expanded", IsSystemCluster: true, Cloud: &struct {
			ID string `json:"id"`
		}{ID: "cld_target"}},
	}
	server := decoratedSessionsTestServer(t, rows)
	client := NewClientWithToken(server.URL, "test-token")

	got, err := findSystemWorkloadCluster(context.Background(), client, "cld_target")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.ClusterID != "ses_expanded" {
		t.Fatalf("got = %+v, want ClusterID ses_expanded", got)
	}
}

func TestFindSystemWorkloadCluster_IgnoresNonSystemClusterSessions(t *testing.T) {
	rows := []DecoratedSessionResult{
		{ID: "ses_user_cluster", CloudID: strPtr("cld_target"), IsSystemCluster: false},
	}
	server := decoratedSessionsTestServer(t, rows)
	client := NewClientWithToken(server.URL, "test-token")

	got, err := findSystemWorkloadCluster(context.Background(), client, "cld_target")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("got = %+v, want nil (a matching cloud_id with is_system_cluster=false must not match)", got)
	}
}

func TestFindSystemWorkloadCluster_NotFound(t *testing.T) {
	server := decoratedSessionsTestServer(t, nil)
	client := NewClientWithToken(server.URL, "test-token")

	got, err := findSystemWorkloadCluster(context.Background(), client, "cld_missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("got = %+v, want nil for a cloud with no system cluster session", got)
	}
}

func TestFindSystemWorkloadCluster_Paginates(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
		if r.URL.Query().Get("paging_token") == "" {
			_ = json.NewEncoder(w).Encode(DecoratedSessionsListResponse{
				Results: []DecoratedSessionResult{
					{ID: "ses_page1", CloudID: strPtr("cld_other"), IsSystemCluster: true},
				},
				Metadata: struct {
					Total           int     `json:"total"`
					NextPagingToken *string `json:"next_paging_token"`
				}{Total: 2, NextPagingToken: strPtr("page2")},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(DecoratedSessionsListResponse{
			Results: []DecoratedSessionResult{
				{ID: "ses_page2_target", CloudID: strPtr("cld_target"), IsSystemCluster: true},
			},
		})
	}))
	t.Cleanup(server.Close)

	client := NewClientWithToken(server.URL, "test-token")
	got, err := findSystemWorkloadCluster(context.Background(), client, "cld_target")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.ClusterID != "ses_page2_target" {
		t.Fatalf("got = %+v, want the match from the second page (proves pagination is actually followed)", got)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2 (one per page)", requests)
	}
}
