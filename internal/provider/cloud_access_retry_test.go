package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// These tests cover cloudAccessDoWriteRetry, J.13's bounded retry policy for
// this resource's write calls. Every case here asserts the REQUEST COUNT, not
// merely the outcome (AC-25) - a 403 attempted twice would pass an
// outcome-only test and fail this one.

// cloudAccessFastRetryBackoff overrides the package-level backoff vars to
// milliseconds for the duration of a test, so a test exercising real retries
// does not wait out real seconds. Restored via t.Cleanup.
func cloudAccessFastRetryBackoff(t *testing.T) {
	t.Helper()
	origInitial, origMax := cloudAccessRetryInitialBackoff, cloudAccessRetryMaxBackoff
	cloudAccessRetryInitialBackoff = time.Millisecond
	cloudAccessRetryMaxBackoff = 5 * time.Millisecond
	t.Cleanup(func() {
		cloudAccessRetryInitialBackoff, cloudAccessRetryMaxBackoff = origInitial, origMax
	})
}

func TestCloudAccessDoWriteRetry_TransientFailureThenSuccessConverges(t *testing.T) {
	cloudAccessFastRetryBackoff(t)

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	client := &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}

	_, err := cloudAccessDoWriteRetry(context.Background(), client, "PUT", "/api/v2/whatever", nil, http.StatusNoContent)
	if err != nil {
		t.Fatalf("a 429 followed by success must converge: %v", err)
	}
	if requests != 2 {
		t.Errorf("got %d requests, want exactly 2 (one 429, one success) - AC-25 requires asserting the count, not just the outcome", requests)
	}
}

func TestCloudAccessDoWriteRetry_TerminalErrorAttemptedExactlyOnce(t *testing.T) {
	cloudAccessFastRetryBackoff(t)

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	client := &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}

	_, err := cloudAccessDoWriteRetry(context.Background(), client, "PUT", "/api/v2/whatever", nil, http.StatusNoContent)
	if err == nil {
		t.Fatal("expected an error: 403 is a terminal decision, not a transient condition")
	}
	if requests != 1 {
		t.Errorf("got %d requests, want exactly 1 - a 403 must never be retried, it is a decision not a transient failure", requests)
	}
}

func TestCloudAccessDoWriteRetry_ExhaustsAtMaxAttempts(t *testing.T) {
	cloudAccessFastRetryBackoff(t)

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	client := &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}

	_, err := cloudAccessDoWriteRetry(context.Background(), client, "PUT", "/api/v2/whatever", nil, http.StatusNoContent)
	if err == nil {
		t.Fatal("expected an error: every attempt hit 503")
	}
	if requests != cloudAccessRetryMaxAttempts {
		t.Errorf("got %d requests, want exactly %d (the bound must be enforced, not merely typical)", requests, cloudAccessRetryMaxAttempts)
	}
}

// TestCloudAccessDoWriteRetry_RewindsBodyOnRetry proves the seek-before-retry
// fix works: a PUT/POST body is an io.Reader consumed by the first attempt,
// and a retry that sends an empty body would silently write nothing instead
// of the intended role/deny_roles.
func TestCloudAccessDoWriteRetry_RewindsBodyOnRetry(t *testing.T) {
	cloudAccessFastRetryBackoff(t)

	var bodies []string
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		bodies = append(bodies, string(buf[:n]))
		if requests == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	client := &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}

	body, err := MarshalRequestBody(map[string]string{"base_role": "writer"})
	if err != nil {
		t.Fatalf("failed to build request body: %v", err)
	}
	_, err = cloudAccessDoWriteRetry(context.Background(), client, "PUT", "/api/v2/whatever", body, http.StatusNoContent)
	if err != nil {
		t.Fatalf("a 502 followed by success must converge: %v", err)
	}
	if len(bodies) != 2 || bodies[0] == "" || bodies[1] == "" {
		t.Fatalf("expected a non-empty body on BOTH attempts, got: %+v - the retry sent an empty body, which would silently write nothing", bodies)
	}
	if bodies[0] != bodies[1] {
		t.Errorf("the retried body %q differs from the first attempt's %q", bodies[1], bodies[0])
	}
}

// TestCloudAccessDoWriteRetry_BodyEchoingAnotherStatusIsNotMisclassified
// covers the hardening item found in review: cloudAccessRetryableError checks
// the REAL numeric status via UnexpectedStatusError, not a text match against
// the whole error string - because DoRequestRaw's error format embeds the
// response BODY after the code, a 403 whose body happens to contain the text
// "unexpected status 503" (a gateway or proxy relaying upstream status,
// plausibly) must NOT be retried. Retrying it would convert a terminal
// decision into a slow one, the exact thing J.13 rules out.
func TestCloudAccessDoWriteRetry_BodyEchoingAnotherStatusIsNotMisclassified(t *testing.T) {
	cloudAccessFastRetryBackoff(t)

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusForbidden)
		// The response body itself contains text shaped like a DIFFERENT status -
		// exactly the case a text-matching classifier would misread.
		_, _ = w.Write([]byte(`{"error": "upstream gateway reported: unexpected status 503: service unavailable"}`))
	}))
	t.Cleanup(server.Close)
	client := &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}

	_, err := cloudAccessDoWriteRetry(context.Background(), client, "PUT", "/api/v2/whatever", nil, http.StatusNoContent)
	if err == nil {
		t.Fatal("expected an error: this is a 403, a terminal decision")
	}
	if requests != 1 {
		t.Errorf("got %d requests, want exactly 1 - a 403 whose BODY happens to mention 'unexpected status 503' must not be retried on the strength of its body text", requests)
	}
}
