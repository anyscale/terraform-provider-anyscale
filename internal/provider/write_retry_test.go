package provider

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// These tests pin what each caller of doWriteWithRetry keeps of its own policy. The two callers
// share one loop and differ only in policy, so a policy wired to the wrong caller would pass
// every loop-shape test and change behavior. The deciding difference is a 500: the IAM mapping
// PUT retries it (a full idempotent replace that can 500 after committing), cloud_access does not.

func cloudIAMMappingFastRetryBackoff(t *testing.T) {
	t.Helper()
	origInitial, origMax := cloudIAMMappingRetryInitialBackoff, cloudIAMMappingRetryMaxBackoff
	cloudIAMMappingRetryInitialBackoff = time.Millisecond
	cloudIAMMappingRetryMaxBackoff = 5 * time.Millisecond
	t.Cleanup(func() {
		cloudIAMMappingRetryInitialBackoff, cloudIAMMappingRetryMaxBackoff = origInitial, origMax
	})
}

// statusThenOKServer answers failStatus to the first failures requests and then 200 with body,
// counting requests and recording each request body so a test can prove the body was resent.
func statusThenOKServer(t *testing.T, failStatus, failures int, body string) (client *Client, requests *int, bodies *[]string) {
	t.Helper()
	var n int
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		seen = append(seen, buf.String())
		if n <= failures {
			w.WriteHeader(failStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}, &n, &seen
}

func TestDoCloudIAMMappingConfigRequestRetry_Retries500AndResendsBody(t *testing.T) {
	cloudIAMMappingFastRetryBackoff(t)
	client, requests, bodies := statusThenOKServer(t, http.StatusInternalServerError, 2, `{"result":{"spec":{}}}`)

	reqBody, err := MarshalRequestBody(CloudDeploymentConfigResult{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doCloudIAMMappingConfigRequestRetry(context.Background(), client, http.MethodPut, "cld_x", "cldrsrc_x", reqBody); err != nil {
		t.Fatalf("expected two 500s then success to converge, got %v", err)
	}
	if *requests != cloudIAMMappingRetryMaxAttempts {
		t.Fatalf("got %d requests, want %d (two retried 500s, then success)", *requests, cloudIAMMappingRetryMaxAttempts)
	}
	for i, b := range *bodies {
		if b == "" || b != (*bodies)[0] {
			t.Errorf("attempt %d sent body %q, want every attempt to resend %q", i+1, b, (*bodies)[0])
		}
	}
}

func TestDoCloudIAMMappingConfigRequestRetry_BoundedAttempts(t *testing.T) {
	cloudIAMMappingFastRetryBackoff(t)
	client, requests, _ := statusThenOKServer(t, http.StatusInternalServerError, 100, "")

	if _, err := doCloudIAMMappingConfigRequestRetry(context.Background(), client, http.MethodPut, "cld_x", "cldrsrc_x", nil); err == nil {
		t.Fatal("expected an error once attempts are exhausted")
	}
	if *requests != cloudIAMMappingRetryMaxAttempts {
		t.Fatalf("got %d requests, want exactly %d", *requests, cloudIAMMappingRetryMaxAttempts)
	}
}

func TestCloudAccessDoWriteRetry_DoesNotRetry500(t *testing.T) {
	cloudAccessFastRetryBackoff(t)
	client, requests, _ := statusThenOKServer(t, http.StatusInternalServerError, 100, "")

	if _, err := cloudAccessDoWriteRetry(context.Background(), client, "PUT", "/api/v2/whatever", nil, http.StatusOK); err == nil {
		t.Fatal("expected the 500 to be returned")
	}
	if *requests != 1 {
		t.Fatalf("got %d requests, want exactly 1 (a 500 is not transient for cloud_access writes)", *requests)
	}
}
