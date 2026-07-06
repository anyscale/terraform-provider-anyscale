package provider

import (
	"testing"
	"time"
)

// minAcceptableHTTPTimeout is the floor below which the API client's HTTP
// timeout is considered a "blanket cap" bug rather than a deliberate bound.
// Real backend operations like PUT /add_resource (registering real cloud
// infrastructure) can legitimately run well past 30s — see task 1ea12959,
// found via a real end-to-end apply against live AWS: the previous hardcoded
// 30s http.Client.Timeout killed every request in flight past that mark,
// including the flagship anyscale_cloud all-in-one create path used by
// examples/aws-vm-basic (the provider's own quickstart example).
//
// A value of 0 (fully unbounded at the http.Client level, relying entirely on
// context deadlines threaded through DoRequest via NewRequestWithContext) is
// also acceptable and is in fact the fix's intended shape.
const minAcceptableHTTPTimeout = 5 * time.Minute

func assertNoBlanketTimeoutCap(t *testing.T, label string, timeout time.Duration) {
	t.Helper()
	if timeout != 0 && timeout < minAcceptableHTTPTimeout {
		t.Errorf("%s: http.Client.Timeout is %s, a blanket cap on every API call — "+
			"real operations like add_resource legitimately exceed 30s on live infrastructure "+
			"and will fail with 'context deadline exceeded' (task 1ea12959). Use 0 (unbounded, "+
			"deferring to per-call context deadlines) or a generous bound of at least %s.",
			label, timeout, minAcceptableHTTPTimeout)
	}
}

// TestNewClientWithToken_NoBlanketTimeoutCap pins task 1ea12959's fix for the
// explicit-token client constructor (used directly by acctest's GetTestClient
// and available to any caller supplying its own token).
func TestNewClientWithToken_NoBlanketTimeoutCap(t *testing.T) {
	client := NewClientWithToken("https://example.com", "test-token")
	if client.HTTPClient == nil {
		t.Fatal("HTTPClient must not be nil")
	}
	assertNoBlanketTimeoutCap(t, "NewClientWithToken", client.HTTPClient.Timeout)
}

// TestNewClient_NoBlanketTimeoutCap pins task 1ea12959's fix for the
// environment/credentials-resolving client constructor. Uses an env var
// token so this stays infra-free and independent of any real credentials
// file on the machine running the test.
func TestNewClient_NoBlanketTimeoutCap(t *testing.T) {
	t.Setenv("ANYSCALE_CLI_TOKEN", "test-token")

	client, err := NewClient("https://example.com")
	if err != nil {
		t.Fatalf("NewClient returned an error: %v", err)
	}
	if client.HTTPClient == nil {
		t.Fatal("HTTPClient must not be nil")
	}
	assertNoBlanketTimeoutCap(t, "NewClient", client.HTTPClient.Timeout)
}
