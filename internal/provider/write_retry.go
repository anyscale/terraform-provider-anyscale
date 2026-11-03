package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// writeRetryPolicy is the bounded retry schedule for one kind of API write.
// Callers build it when they make the call, so a test that overrides a
// backoff var takes effect.
type writeRetryPolicy struct {
	initialBackoff time.Duration
	maxBackoff     time.Duration
	maxAttempts    int
	backoffFactor  float64
	// retryable reports whether a failed attempt is transient. Every other
	// error is returned at once.
	retryable func(error) bool
	// warnMessage is logged before each retry.
	warnMessage string
}

// doWriteWithRetry sends one request through DoRequestRaw, retrying failures
// the policy calls transient with capped exponential backoff, at most
// policy.maxAttempts times. It returns the last response body and error.
//
// body is rewound via io.Seeker before each retry after the first, since the
// first attempt's read already consumed it; a nil body is unaffected. A
// cancelled context ends the wait between attempts with an error naming both
// the cancellation and the most recent failure.
func doWriteWithRetry(
	ctx context.Context, client *Client, policy writeRetryPolicy, method, path string, body io.Reader, expectedStatuses ...int,
) ([]byte, error) {
	seeker, _ := body.(io.Seeker)
	backoff := policy.initialBackoff

	var lastResp []byte
	var lastErr error
	for attempt := 1; attempt <= policy.maxAttempts; attempt++ {
		if attempt > 1 && seeker != nil {
			if _, seekErr := seeker.Seek(0, io.SeekStart); seekErr != nil {
				return nil, fmt.Errorf("could not rewind request body for retry attempt %d: %w", attempt, seekErr)
			}
		}

		lastResp, lastErr = DoRequestRaw(ctx, client, method, path, body, expectedStatuses...)
		if lastErr == nil || !policy.retryable(lastErr) || attempt == policy.maxAttempts {
			return lastResp, lastErr
		}

		tflog.Warn(ctx, policy.warnMessage, map[string]any{
			"method": method, "path": path, "attempt": attempt, "backoff": backoff.String(),
		})
		select {
		case <-ctx.Done():
			// Named explicitly rather than returning lastErr bare: this is the
			// caller's own timeout or a cancelled apply, not the transient
			// condition that triggered the retry.
			return lastResp, fmt.Errorf("%w (after %d attempt(s), most recent failure: %s)", ctx.Err(), attempt, lastErr)
		case <-time.After(backoff):
		}
		backoff = time.Duration(float64(backoff) * policy.backoffFactor)
		if backoff > policy.maxBackoff {
			backoff = policy.maxBackoff
		}
	}
	return lastResp, lastErr
}

// transientWriteError reports whether a failed write is worth retrying: an
// HTTP response whose status is one of statuses, or a failure before any
// response arrived.
//
// The status check is structural, not a text match: DoRequestRaw's
// "unexpected status %d: %s" format embeds the raw response body after the
// code, so grepping for "unexpected status 503" would also match a body that
// echoes that text (a gateway relaying an upstream status, for instance) and
// misclassify a real 4xx decision as transient. errors.As unwraps the typed
// value however many times the error was wrapped with %w.
//
// No UnexpectedStatusError means the request never got a response:
// client.DoRequest itself failed (DNS, connection refused, a timeout
// mid-request), which DoRequestRaw wraps as "API request failed: %w". That
// text match is safe because the prefix never embeds a response body; there
// was no response to embed.
func transientWriteError(err error, statuses ...int) bool {
	if err == nil {
		return false
	}
	var statusErr *UnexpectedStatusError
	if errors.As(err, &statusErr) {
		return slices.Contains(statuses, statusErr.StatusCode)
	}
	return strings.Contains(err.Error(), "API request failed")
}
