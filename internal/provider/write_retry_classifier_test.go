package provider

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// TestWriteRetryClassifiers pins exactly which failures each write-retry
// classifier treats as transient. The two lists differ on purpose: the IAM
// mapping write also retries 500, cloud_access does not.
func TestWriteRetryClassifiers(t *testing.T) {
	statuses := []int{
		http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusConflict, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusNotImplemented, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout,
	}
	classifiers := []struct {
		name      string
		retryable func(error) bool
		transient map[int]bool
	}{
		{"cloud_access", cloudAccessRetryableError, map[int]bool{429: true, 502: true, 503: true, 504: true}},
		{"cloud_iam_mapping", cloudIAMMappingRetryableError, map[int]bool{429: true, 500: true, 502: true, 503: true, 504: true}},
	}
	for _, c := range classifiers {
		t.Run(c.name, func(t *testing.T) {
			for _, code := range statuses {
				err := fmt.Errorf("write: %w", &UnexpectedStatusError{StatusCode: code, Body: "{}"})
				if got := c.retryable(err); got != c.transient[code] {
					t.Errorf("status %d: retryable = %v, want %v", code, got, c.transient[code])
				}
			}
			// A terminal status whose body echoes a transient one stays terminal.
			echo := &UnexpectedStatusError{StatusCode: http.StatusBadRequest, Body: "upstream returned unexpected status 503"}
			if c.retryable(echo) {
				t.Error("400 with a body echoing 503: retryable = true, want false")
			}
			if !c.retryable(fmt.Errorf("API request failed: %w", errors.New("connection refused"))) {
				t.Error("transport failure: retryable = false, want true")
			}
			if c.retryable(errors.New("some other failure")) {
				t.Error("unrelated error: retryable = true, want false")
			}
			if c.retryable(nil) {
				t.Error("nil: retryable = true, want false")
			}
		})
	}
}
