package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withFastAutoAddUserRetryBackoff overrides the package-level retry timing
// vars to millisecond-scale values for the duration of a test, restoring the
// originals afterward - these tests exercise real retry/backoff control
// flow, not just a mocked sleep, so they need real (tiny) durations rather
// than skipping the wait entirely.
func withFastAutoAddUserRetryBackoff(t *testing.T, totalCap time.Duration) {
	t.Helper()
	origInitial, origMax, origCap := autoAddUserRetryInitialBackoff, autoAddUserRetryMaxBackoff, autoAddUserRetryTotalCap
	autoAddUserRetryInitialBackoff = 1 * time.Millisecond
	autoAddUserRetryMaxBackoff = 3 * time.Millisecond
	autoAddUserRetryTotalCap = totalCap
	t.Cleanup(func() {
		autoAddUserRetryInitialBackoff, autoAddUserRetryMaxBackoff, autoAddUserRetryTotalCap = origInitial, origMax, origCap
	})
}

// transientAutoAddUserBody is the real backend response body confirmed
// against the CI failure this fix addresses (PR #74's TestAccCloudDataSource_MatchesResourceState).
const transientAutoAddUserBody = `{"error":{"detail":"Failed to update cloud cld_test. The auto add user setting of this cloud is still being applied. Please try again in 30 seconds after this finishes."}}`

func TestUpdateCloudBoolField_RetriesTransientConflictThenSucceeds(t *testing.T) {
	withFastAutoAddUserRetryBackoff(t, 1*time.Second)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(transientAutoAddUserBody))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	err := r.updateCloudBoolField(context.Background(), "cloud-1", "auto_add_user", true)
	if err != nil {
		t.Fatalf("unexpected error after retry should have recovered: %v", err)
	}
	if requestCount != 2 {
		t.Errorf("request count = %d, want exactly 2 (one 409, one retry that succeeds)", requestCount)
	}
}

func TestUpdateCloudBoolField_DoesNotRetryNonMatchingError(t *testing.T) {
	withFastAutoAddUserRetryBackoff(t, 1*time.Second)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		// A 409 that is NOT the documented auto_add_user reconciliation
		// conflict - a genuinely different failure must never be retried,
		// only ever propagated immediately.
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"detail":"Cloud is locked for an unrelated maintenance operation."}}`))
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	err := r.updateCloudBoolField(context.Background(), "cloud-1", "auto_add_user", true)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "unrelated maintenance operation") {
		t.Errorf("error = %q, want it to surface the original unrelated conflict text", err.Error())
	}
	if requestCount != 1 {
		t.Errorf("request count = %d, want exactly 1 - a non-matching error must not be retried", requestCount)
	}
}

func TestUpdateCloudBoolField_DoesNotRetryNonConflictStatus(t *testing.T) {
	withFastAutoAddUserRetryBackoff(t, 1*time.Second)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"detail":"invalid field value"}}`))
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	err := r.updateCloudBoolField(context.Background(), "cloud-1", "auto_add_user", true)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if requestCount != 1 {
		t.Errorf("request count = %d, want exactly 1 - a non-409 error must not be retried", requestCount)
	}
}

func TestUpdateCloudBoolField_ExhaustsRetryAndSurfacesOriginalError(t *testing.T) {
	withFastAutoAddUserRetryBackoff(t, 20*time.Millisecond)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(transientAutoAddUserBody))
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	err := r.updateCloudBoolField(context.Background(), "cloud-1", "auto_add_user", true)
	if err == nil {
		t.Fatal("expected the original 409 to surface once the retry cap is exhausted, got nil")
	}
	if !strings.Contains(err.Error(), "still being applied") {
		t.Errorf("error = %q, want the original transient-conflict text to surface unchanged, not a generic timeout error", err.Error())
	}
	if requestCount < 2 {
		t.Errorf("request count = %d, want at least 2 - it must actually retry at least once before giving up", requestCount)
	}
}

func TestUpdateCloudBoolField_RespectsContextCancellation(t *testing.T) {
	withFastAutoAddUserRetryBackoff(t, 1*time.Minute)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(transientAutoAddUserBody))
	}))
	defer server.Close()

	// A long total cap (1 minute) but a context that cancels almost
	// immediately: the retry must give up on ctx.Done(), not hang for the
	// full cap, and must return promptly rather than block past cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	start := time.Now()
	err := r.updateCloudBoolField(ctx, "cloud-1", "auto_add_user", true)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error once the context is cancelled, got nil")
	}
	if elapsed > 1*time.Second {
		t.Errorf("updateCloudBoolField took %s after context cancellation, want it to return promptly", elapsed)
	}
}

// realDoRequestRawError spins up a one-shot server returning the given
// status and body, then returns the actual error DoRequestRaw produces for
// it - so isTransientAutoAddUserConflict's test cases are checked against
// the real error format, not a hand-typed guess of what that format looks
// like. If DoRequestRaw's format ever changes, this drifts with it instead
// of silently going stale.
func realDoRequestRawError(t *testing.T, status int, body string) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := NewClientWithToken(server.URL, "test-token")
	_, err := DoRequestRaw(context.Background(), client, "PUT", "/x", nil, http.StatusOK)
	if err == nil {
		t.Fatalf("expected DoRequestRaw to error for status %d, got nil", status)
	}
	return err
}

func TestIsTransientAutoAddUserConflict(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "real documented conflict matches",
			status: http.StatusConflict,
			body:   transientAutoAddUserBody,
			want:   true,
		},
		{
			name:   "409 with try again wording also matches",
			status: http.StatusConflict,
			body:   `{"error":{"detail":"lock held, try again shortly"}}`,
			want:   true,
		},
		{
			name:   "different 409 does not match",
			status: http.StatusConflict,
			body:   `{"error":{"detail":"Cloud is locked for an unrelated maintenance operation."}}`,
			want:   false,
		},
		{
			name:   "non-409 status does not match even with the right words",
			status: http.StatusBadRequest,
			body:   `{"error":{"detail":"still being applied, try again"}}`,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := realDoRequestRawError(t, tt.status, tt.body)
			if got := isTransientAutoAddUserConflict(err); got != tt.want {
				t.Errorf("isTransientAutoAddUserConflict(%q) = %v, want %v", err.Error(), got, tt.want)
			}
		})
	}
}
