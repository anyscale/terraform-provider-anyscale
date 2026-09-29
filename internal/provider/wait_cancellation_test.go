package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Each wait below sleeps between polls. A cancelled context (Ctrl-C, or an
// operation timeout) must interrupt that sleep rather than let it run out.
// Every test cancels shortly after the wait starts and requires it to return
// well before one sleep interval would have elapsed.

const (
	cancelAfter   = 50 * time.Millisecond
	promptReturn  = 500 * time.Millisecond
	neverResponds = time.Hour
	// Well above promptReturn, but short enough that a regression fails fast
	// instead of hanging the suite for a full interval.
	digestTestInterval = 2 * time.Second
)

func cancelSoon(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	time.AfterFunc(cancelAfter, cancel)
	return ctx
}

func requirePrompt(t *testing.T, start time.Time) {
	t.Helper()
	if elapsed := time.Since(start); elapsed > promptReturn {
		t.Fatalf("wait returned after %v, want under %v once the context is cancelled", elapsed, promptReturn)
	}
}

// newInProgressBuildServer answers every GET builds/{id} with a build that
// never finishes and never has a digest.
func newInProgressBuildServer(t *testing.T, buildID string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "status": "in_progress"}}`, buildID)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestWaitForBuild_CancelInterruptsPollSleep(t *testing.T) {
	const buildID = "bld_cancel_wait"
	server := newInProgressBuildServer(t, buildID)
	r := &ContainerImageBuildResource{client: NewClientWithToken(server.URL, "test-token")}

	ctx := cancelSoon(t)
	start := time.Now()
	_, err := r.waitForBuild(ctx, buildID, neverResponds)
	requirePrompt(t, start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForBuild error = %v, want context.Canceled", err)
	}
}

func TestWaitForBuildDigest_CancelInterruptsPollSleep(t *testing.T) {
	const buildID = "bld_cancel_digest"
	server := newInProgressBuildServer(t, buildID)
	client := NewClientWithToken(server.URL, "test-token")

	ctx := cancelSoon(t)
	start := time.Now()
	_, settled := waitForBuildDigestWithTiming(ctx, client, &BuildResult{ID: buildID}, neverResponds, digestTestInterval)
	requirePrompt(t, start)
	if settled {
		t.Fatal("waitForBuildDigestWithTiming reported settled, want false on cancellation")
	}
}

func TestRetryOn403_CancelInterruptsBackoffSleep(t *testing.T) {
	forbidden := &UnexpectedStatusError{StatusCode: http.StatusForbidden, Body: "not yet authorized"}

	ctx := cancelSoon(t)
	start := time.Now()
	_, err := retryOn403(ctx, true, nil, func() ([]byte, error) { return nil, forbidden }, nil)
	requirePrompt(t, start)
	if !errors.Is(err, forbidden) {
		t.Fatalf("retryOn403 error = %v, want the last 403", err)
	}
}
