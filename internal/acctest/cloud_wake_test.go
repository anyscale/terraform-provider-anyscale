package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The exact body the backend returned in the failing scheduled run.
const hibernatedBody = `{"error":{"detail":"Cloud cld_x has been hibernated due to inactivity. The cloud is being started and should be available in 5-10 minutes."}}`

// fakeClock advances only when the gate sleeps, so no test waits for real time.
type fakeClock struct {
	mu  sync.Mutex
	cur time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

func (c *fakeClock) sleep(d time.Duration) {
	c.mu.Lock()
	c.cur = c.cur.Add(d)
	c.mu.Unlock()
}

func testWakeOpts() wakeOpts {
	o, _ := testWakeOptsWithClock()
	return o
}

// testWakeOptsWithClock also returns the clock, for tests that advance time
// between gate calls.
func testWakeOptsWithClock() (wakeOpts, *fakeClock) {
	c := &fakeClock{cur: time.Unix(0, 0)}
	o := defaultWakeOpts()
	o.sleep, o.now = c.sleep, c.now
	return o, c
}

func resetWakeGates(t *testing.T) {
	t.Helper()
	wakeGatesMutex.Lock()
	orig := wakeGates
	wakeGates = map[string]*wakeGate{}
	wakeGatesMutex.Unlock()
	t.Cleanup(func() {
		wakeGatesMutex.Lock()
		wakeGates = orig
		wakeGatesMutex.Unlock()
	})
}

// httpProbe posts to srv, the same request shape the real probe sends.
func httpProbe(srv *httptest.Server) wakeProbe {
	return func(ctx context.Context) (int, string, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", srv.URL+"/api/v2/compute_templates/", strings.NewReader("{}"))
		if err != nil {
			return 0, "", err
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), nil
	}
}

// sleepyServer answers 599+hibernatedBody for the first n requests, then 200.
func sleepyServer(t *testing.T, n int32, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= n {
			w.WriteHeader(599)
			_, _ = io.WriteString(w, hibernatedBody)
			return
		}
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"result":{"id":"cpt_1"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEnsureCloudAwake_AsleepThenAwake(t *testing.T) {
	resetWakeGates(t)
	var calls atomic.Int32
	const n = 4
	srv := sleepyServer(t, n, &calls)

	if err := ensureCloudAwakeWith(context.Background(), "cld_a", `"c"`, httpProbe(srv), testWakeOpts(), t.Logf); err != nil {
		t.Fatalf("expected the gate to succeed once the cloud wakes: %v", err)
	}
	if got := calls.Load(); got != n+1 {
		t.Fatalf("probe count = %d, want %d", got, n+1)
	}
}

func TestEnsureCloudAwake_NeverWakes(t *testing.T) {
	resetWakeGates(t)
	var calls atomic.Int32
	srv := sleepyServer(t, 1<<30, &calls)

	err := ensureCloudAwakeWith(context.Background(), "cld_b", `"static-cloud"`, httpProbe(srv), testWakeOpts(), t.Logf)
	if err == nil {
		t.Fatal("expected a bounded failure when the cloud never wakes")
	}
	for _, want := range []string{`"static-cloud"`, "still hibernated after 12m", "probes", "last detail: Cloud cld_x has been hibernated due to inactivity", "re-run once it is awake"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "cld_b") {
		t.Errorf("error must name the cloud, not its ID: %q", err)
	}
	// The failure is cached: later callers fail fast without probing again.
	before := calls.Load()
	if err2 := ensureCloudAwakeWith(context.Background(), "cld_b", `"static-cloud"`, httpProbe(srv), testWakeOpts(), t.Logf); err2 == nil {
		t.Fatal("expected the cached failure")
	}
	if calls.Load() != before {
		t.Fatal("cached failure must not probe again")
	}
}

func TestEnsureCloudAwake_OtherStatusPassesThrough(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"599 with a different body": {599, `{"error":{"detail":"something else entirely"}}`},
		"599 with a non-JSON body":  {599, `hibernated`},
		"plain 500":                 {500, `{"error":{"detail":"boom"}}`},
		"403":                       {403, `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			resetWakeGates(t)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			if err := ensureCloudAwakeWith(context.Background(), "cld_c", `"c"`, httpProbe(srv), testWakeOpts(), t.Logf); err != nil {
				t.Fatalf("gate must not fail or wait on a non-hibernation response: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("probe count = %d, want 1 (no retry)", calls.Load())
			}
		})
	}
}

func TestEnsureCloudAwake_OnceUnderConcurrency(t *testing.T) {
	resetWakeGates(t)
	var calls atomic.Int32
	const n = 3
	srv := sleepyServer(t, n, &calls)
	probe := httpProbe(srv)
	opts := testWakeOpts()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ensureCloudAwakeWith(context.Background(), "cld_d", `"c"`, probe, opts, t.Logf); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != n+1 {
		t.Fatalf("probe count = %d, want one sequence of %d", got, n+1)
	}
}

// An awake verdict is reused only for 2 minutes: the cloud can hibernate
// minutes after the gate saw it awake. The TTL is a literal here, not
// opts.recheckAfter, so lengthening the default fails this test.
func TestEnsureCloudAwake_AwakeReprobedAfterTTL(t *testing.T) {
	resetWakeGates(t)
	var calls atomic.Int32
	srv := sleepyServer(t, 0, &calls)
	opts, clock := testWakeOptsWithClock()
	gate := func() {
		t.Helper()
		if err := ensureCloudAwakeWith(context.Background(), "cld_f", `"c"`, httpProbe(srv), opts, t.Logf); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	gate()
	if got := calls.Load(); got != 1 {
		t.Fatalf("first call: probe count = %d, want 1", got)
	}
	clock.sleep(2*time.Minute - time.Second)
	gate()
	if got := calls.Load(); got != 1 {
		t.Fatalf("within the TTL: probe count = %d, want 1 (cached)", got)
	}
	clock.sleep(time.Second)
	gate()
	if got := calls.Load(); got != 2 {
		t.Fatalf("at the TTL: probe count = %d, want 2 (re-probed)", got)
	}
}

// A cloud that went back to sleep after a cached awake verdict is waited on
// again, not reported awake.
func TestEnsureCloudAwake_ReprobeWaitsWhenCloudFellAsleep(t *testing.T) {
	resetWakeGates(t)
	var calls atomic.Int32
	asleep := atomic.Bool{}
	var remaining atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if asleep.Load() && remaining.Add(-1) >= 0 {
			w.WriteHeader(599)
			_, _ = io.WriteString(w, hibernatedBody)
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	opts, clock := testWakeOptsWithClock()

	if err := ensureCloudAwakeWith(context.Background(), "cld_g", `"c"`, httpProbe(srv), opts, t.Logf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	asleep.Store(true)
	remaining.Store(2)
	clock.sleep(2 * time.Minute)
	if err := ensureCloudAwakeWith(context.Background(), "cld_g", `"c"`, httpProbe(srv), opts, t.Logf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("probe count = %d, want 4 (1 awake, then 2 hibernated + 1 awake)", got)
	}
}

// A hibernation timeout stays cached past the TTL: parallel tests must fail
// fast, not each wait out the bound again.
func TestEnsureCloudAwake_TimeoutCachedPastTTL(t *testing.T) {
	resetWakeGates(t)
	var calls atomic.Int32
	srv := sleepyServer(t, 1<<30, &calls)
	opts, clock := testWakeOptsWithClock()

	if err := ensureCloudAwakeWith(context.Background(), "cld_h", `"c"`, httpProbe(srv), opts, t.Logf); err == nil {
		t.Fatal("expected a bounded failure")
	}
	before := calls.Load()
	clock.sleep(10 * opts.recheckAfter)
	if err := ensureCloudAwakeWith(context.Background(), "cld_h", `"c"`, httpProbe(srv), opts, t.Logf); err == nil {
		t.Fatal("expected the cached failure")
	}
	if calls.Load() != before {
		t.Fatal("cached timeout must not probe again, even past the TTL")
	}
}

// Every probe is logged, including a first-probe pass, so a later failure
// shows what the gate saw.
func TestEnsureCloudAwake_LogsEveryProbe(t *testing.T) {
	resetWakeGates(t)
	var calls atomic.Int32
	srv := sleepyServer(t, 1, &calls)
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	if err := ensureCloudAwakeWith(context.Background(), "cld_i", `"c"`, httpProbe(srv), testWakeOpts(), logf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{
		"Wake probe 1 for cloud \"c\": HTTP 599 after 0s (hibernation response: true)",
		"Wake probe 2 for cloud \"c\": HTTP 200 after 5s (hibernation response: false)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("logs missing %q; got:\n%s", want, joined)
		}
	}

	resetWakeGates(t)
	logs = nil
	if err := ensureCloudAwakeWith(context.Background(), "cld_j", `"c"`, httpProbe(srv), testWakeOpts(), logf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "Wake probe 1 for cloud \"c\": HTTP 200 after 0s (hibernation response: false)"; len(logs) != 1 || logs[0] != want {
		t.Errorf("first-probe pass: logs = %q, want exactly %q", logs, want)
	}
}

func TestEnsureCloudAwake_TransportErrorRetriedNotCached(t *testing.T) {
	resetWakeGates(t)
	var calls atomic.Int32
	probe := func(context.Context) (int, string, error) {
		calls.Add(1)
		return 0, "", fmt.Errorf("connection reset")
	}
	if err := ensureCloudAwakeWith(context.Background(), "cld_e", `"c"`, probe, testWakeOpts(), t.Logf); err != nil {
		t.Fatalf("an inconclusive probe must not fail the suite: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("probe count = %d, want 2 (one retry)", calls.Load())
	}
	// Not cached: the next caller probes again.
	_ = ensureCloudAwakeWith(context.Background(), "cld_e", `"c"`, probe, testWakeOpts(), t.Logf)
	if calls.Load() != 4 {
		t.Fatalf("inconclusive result was cached; probe count = %d, want 4", calls.Load())
	}
}

func TestIsHibernationResponse(t *testing.T) {
	if !isHibernationResponse(599, hibernatedBody) {
		t.Fatal("exact logged body must match")
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(hibernatedBody), &v); err != nil {
		t.Fatalf("fixture must be valid JSON: %v", err)
	}
	if isHibernationResponse(500, hibernatedBody) {
		t.Fatal("only 599 counts")
	}
}
