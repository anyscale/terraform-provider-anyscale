package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// retryTestServer answers each request with the next entry in steps (the last entry
// repeats) and counts attempts. A step with stall set blocks until the request context
// is done (or a safety cap passes), standing in for an API call that never answers.
type retryStep struct {
	status     int
	retryAfter string
	stall      bool
}

type retryTestServer struct {
	*httptest.Server
	attempts atomic.Int32
	mu       sync.Mutex
	starts   []time.Time
}

func newRetryTestServer(t *testing.T, steps ...retryStep) *retryTestServer {
	t.Helper()
	s := &retryTestServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(s.attempts.Add(1))
		s.mu.Lock()
		s.starts = append(s.starts, time.Now())
		s.mu.Unlock()
		step := steps[min(n, len(steps))-1]
		if step.stall {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(3 * time.Second):
				// Safety cap: without a per-attempt timeout the client would wait the
				// full 60s default. Answering after 3s keeps a failing run short.
			}
		}
		if step.retryAfter != "" {
			w.Header().Set("Retry-After", step.retryAfter)
		}
		w.WriteHeader(step.status)
		_, _ = w.Write([]byte(`{"result": {}}`))
	}))
	t.Cleanup(s.Close)
	return s
}

// newRetryTestClient returns a client whose backoff sleeps are recorded instead of
// slept, and whose per-attempt timeout is short, so retry tests run in milliseconds.
func newRetryTestClient(baseURL string, sleeps *[]time.Duration) *Client {
	return &Client{
		BaseURL:        baseURL,
		Token:          "test-token",
		HTTPClient:     &http.Client{},
		attemptTimeout: 200 * time.Millisecond,
		sleep: func(ctx context.Context, d time.Duration) error {
			*sleeps = append(*sleeps, d)
			return ctx.Err()
		},
	}
}

type emptyResult struct{}

func TestDoRequest_RetriesTransientStatusOnGET(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout, http.StatusTooManyRequests} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv := newRetryTestServer(t, retryStep{status: status}, retryStep{status: http.StatusOK})
			var sleeps []time.Duration
			c := newRetryTestClient(srv.URL, &sleeps)

			if _, err := DoRequestAndParse[emptyResult](context.Background(), c, http.MethodGet, "/api/v2/clouds/", nil); err != nil {
				t.Fatalf("GET after one %d: got error %v, want success", status, err)
			}
			if got := srv.attempts.Load(); got != 2 {
				t.Fatalf("attempts = %d, want 2", got)
			}
			if len(sleeps) != 1 || sleeps[0] < 800*time.Millisecond || sleeps[0] > 1200*time.Millisecond {
				t.Errorf("backoff before attempt 2 = %v, want one sleep of 1s +/-20%%", sleeps)
			}
		})
	}
}

func TestDoRequest_HonorsRetryAfterWithCap(t *testing.T) {
	for _, tc := range []struct {
		status     int
		retryAfter string
		min, max   time.Duration
	}{
		{http.StatusTooManyRequests, "3", 3 * time.Second, 8 * time.Second},
		{http.StatusServiceUnavailable, "3", 3 * time.Second, 8 * time.Second},
		{http.StatusTooManyRequests, "120", 0, 8 * time.Second},
	} {
		t.Run(strconv.Itoa(tc.status)+"/"+tc.retryAfter, func(t *testing.T) {
			srv := newRetryTestServer(t, retryStep{status: tc.status, retryAfter: tc.retryAfter}, retryStep{status: http.StatusOK})
			var sleeps []time.Duration
			c := newRetryTestClient(srv.URL, &sleeps)

			if _, err := DoRequestAndParse[emptyResult](context.Background(), c, http.MethodGet, "/api/v2/clouds/", nil); err != nil {
				t.Fatalf("got error %v, want success", err)
			}
			if len(sleeps) != 1 || sleeps[0] < tc.min || sleeps[0] > tc.max {
				t.Errorf("Retry-After %s: sleep = %v, want one sleep in [%v, %v]", tc.retryAfter, sleeps, tc.min, tc.max)
			}
		})
	}
}

// The failure this retry exists for: one GET that never answers used to consume the
// whole request budget and fail. Each attempt must get its own timeout.
func TestDoRequest_RetriesPerAttemptTimeoutOnGET(t *testing.T) {
	srv := newRetryTestServer(t, retryStep{status: http.StatusOK, stall: true}, retryStep{status: http.StatusOK})
	var sleeps []time.Duration
	c := newRetryTestClient(srv.URL, &sleeps)

	start := time.Now()
	if _, err := DoRequestAndParse[emptyResult](context.Background(), c, http.MethodGet, "/api/v2/clouds/", nil); err != nil {
		t.Fatalf("GET after a stalled attempt: got error %v, want success", err)
	}
	if got := srv.attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2 (stalled attempt, then success)", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %v; the stalled attempt was not cut off by the per-attempt timeout", elapsed)
	}
}

func TestDoRequest_DoesNotRetryNonTransientStatus(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusBadRequest, http.StatusInternalServerError, 599} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv := newRetryTestServer(t, retryStep{status: status}, retryStep{status: http.StatusOK})
			var sleeps []time.Duration
			c := newRetryTestClient(srv.URL, &sleeps)

			_, err := DoRequestAndParse[emptyResult](context.Background(), c, http.MethodGet, "/api/v2/clouds/x", nil)
			if err == nil {
				t.Fatalf("got success, want the %d surfaced", status)
			}
			if got := srv.attempts.Load(); got != 1 {
				t.Fatalf("attempts = %d, want 1", got)
			}
			if status == http.StatusNotFound && !errors.Is(err, ErrNotFound) {
				t.Errorf("404: errors.Is(err, ErrNotFound) = false; err = %v", err)
			}
			var statusErr *UnexpectedStatusError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != status {
				t.Errorf("errors.As(*UnexpectedStatusError) did not find status %d; err = %v", status, err)
			}
		})
	}
}

func TestDoRequest_DoesNotRetryWrites(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			srv := newRetryTestServer(t, retryStep{status: http.StatusServiceUnavailable}, retryStep{status: http.StatusOK})
			var sleeps []time.Duration
			c := newRetryTestClient(srv.URL, &sleeps)

			if _, err := DoRequestAndParse[emptyResult](context.Background(), c, method, "/api/v2/projects/", strings.NewReader(`{}`)); err == nil {
				t.Fatalf("%s: got success, want the 503 surfaced", method)
			}
			if got := srv.attempts.Load(); got != 1 {
				t.Fatalf("%s: attempts = %d, want 1 (writes are never retried)", method, got)
			}
		})
	}
}

func TestDoRequest_GivesUpAfterThreeAttempts(t *testing.T) {
	srv := newRetryTestServer(t, retryStep{status: http.StatusServiceUnavailable})
	var sleeps []time.Duration
	c := newRetryTestClient(srv.URL, &sleeps)

	_, err := DoRequestAndParse[emptyResult](context.Background(), c, http.MethodGet, "/api/v2/clouds/", nil)
	if err == nil {
		t.Fatal("got success against a persistent 503")
	}
	if got := srv.attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("error does not say how many attempts were made: %v", err)
	}
	var statusErr *UnexpectedStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("errors.As(*UnexpectedStatusError) did not find the 503; err = %v", err)
	}
	if len(sleeps) != 2 ||
		sleeps[0] < 800*time.Millisecond || sleeps[0] > 1200*time.Millisecond ||
		sleeps[1] < 1600*time.Millisecond || sleeps[1] > 2400*time.Millisecond {
		t.Errorf("backoffs = %v, want [1s, 2s] each +/-20%%", sleeps)
	}
}

func TestDoRequest_StopsWhenCallerCancelsDuringBackoff(t *testing.T) {
	srv := newRetryTestServer(t, retryStep{status: http.StatusServiceUnavailable}, retryStep{status: http.StatusOK})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Client{
		BaseURL:        srv.URL,
		Token:          "test-token",
		HTTPClient:     &http.Client{},
		attemptTimeout: 200 * time.Millisecond,
		sleep: func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		},
	}

	_, err := DoRequestAndParse[emptyResult](ctx, c, http.MethodGet, "/api/v2/clouds/", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := srv.attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (no attempt after the caller cancelled)", got)
	}
}

// The real backoff timer (no injected sleep) must end early when the caller cancels.
func TestDoRequest_RealBackoffStopsWhenCallerCancels(t *testing.T) {
	srv := newRetryTestServer(t, retryStep{status: http.StatusServiceUnavailable})
	c := &Client{
		BaseURL:        srv.URL,
		Token:          "test-token",
		HTTPClient:     &http.Client{},
		attemptTimeout: 200 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	_, err := DoRequestAndParse[emptyResult](ctx, c, http.MethodGet, "/api/v2/clouds/", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 600*time.Millisecond {
		t.Errorf("returned after %v; the ~1s backoff did not stop when the caller cancelled at 100ms", elapsed)
	}
	if got := srv.attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

// A caller deadline closer than the next backoff ends retrying at once, and the caller
// gets the API's own failure rather than a context error from waiting out the deadline.
func TestDoRequest_NoAttemptStartsAfterCallerDeadline(t *testing.T) {
	srv := newRetryTestServer(t, retryStep{status: http.StatusServiceUnavailable})
	// A real sleep here: the deadline must interact with actual time.
	c := &Client{
		BaseURL:        srv.URL,
		Token:          "test-token",
		HTTPClient:     &http.Client{},
		attemptTimeout: 200 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()

	start := time.Now()
	_, err := DoRequestAndParse[emptyResult](ctx, c, http.MethodGet, "/api/v2/clouds/", nil)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("returned after %v; with 300ms left and a ~1s backoff next, retrying should stop at once", elapsed)
	}
	var statusErr *UnexpectedStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("err = %v, want the API's 503, not a context error", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for i, s := range srv.starts {
		if s.After(deadline) {
			t.Errorf("attempt %d started %v after the caller's deadline", i+1, s.Sub(deadline))
		}
	}
}
