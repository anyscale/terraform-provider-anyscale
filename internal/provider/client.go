package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Client represents the Anyscale API client
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client

	// attemptTimeout overrides defaultRequestTimeout per attempt; 0 means
	// defaultRequestTimeout. sleep overrides the backoff wait; nil means a
	// timer that returns early with ctx.Err() if ctx is done. Both exist so
	// retry tests run in milliseconds.
	attemptTimeout time.Duration
	sleep          func(ctx context.Context, d time.Duration) error

	// azureControlPlaneHost is the API host when it is an Azure control plane, else "".
	// Set by the provider's Configure; RBAC resources and data sources refuse to run
	// against it (see azure_control_plane.go).
	azureControlPlaneHost string
}

// defaultRequestTimeout bounds an API call when the caller's context carries
// no deadline of its own. Long-running calls (e.g. add_resource, which
// legitimately runs for minutes while real cloud infra is registered) set
// their own longer deadline on ctx before calling DoRequest, which then
// leaves it untouched instead of overriding it with this default.
const defaultRequestTimeout = 60 * time.Second

// Read retry budget: DoRequest makes at most maxReadAttempts attempts of a
// GET or HEAD, waiting retryBaseBackoff, then twice that (each +/-20%
// jitter), before the next one. A Retry-After header on 429/503 replaces the
// computed wait. No wait exceeds maxRetryBackoff.
const (
	maxReadAttempts  = 3
	retryBaseBackoff = 1 * time.Second
	maxRetryBackoff  = 8 * time.Second
)

// addResourceRequestTimeout is the deadline for the add_resource call
// (POST/PUT .../add_resource), which registers real cloud infrastructure
// server-side and legitimately takes longer than defaultRequestTimeout.
const addResourceRequestTimeout = 15 * time.Minute

// Credentials represents the structure of ~/.anyscale/credentials.json
type Credentials struct {
	Token    string `json:"token"`
	CLIToken string `json:"cli_token"`
}

// NewClient creates a new Anyscale API client with authentication
func NewClient(baseURL string) (*Client, error) {
	token, err := GetAuthToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get authentication token: %w", err)
	}

	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		// No blanket Timeout here: it would cap every request (including
		// long-running ones like add_resource) regardless of context, which
		// is exactly the bug this shape previously had. DoRequest applies
		// defaultRequestTimeout via context instead, which callers can override.
		HTTPClient: &http.Client{},
	}, nil
}

// NewClientWithToken creates a new Anyscale API client with explicit token
func NewClientWithToken(baseURL, token string) *Client {
	return &Client{
		// A trailing slash would turn every request into //api/v2/...
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		HTTPClient: &http.Client{},
	}
}

// Credential sources reported by resolveAuthToken, for diagnostics and logs.
const (
	tokenSourceEnv  = "ANYSCALE_CLI_TOKEN environment variable"
	tokenSourceFile = "credentials file"
)

// errCredentialsFileNotFound means there is no credentials file at all, as
// opposed to one that exists but cannot be used. Configure reports the two
// differently: "nothing supplied" versus "supplied but broken".
var errCredentialsFileNotFound = errors.New("credentials file not found")

// credentialsFilePath is where the Anyscale CLI (`anyscale login`) stores credentials.
func credentialsFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}
	return filepath.Join(homeDir, ".anyscale", "credentials.json"), nil
}

// GetAuthToken retrieves the authentication token from either
// ANYSCALE_CLI_TOKEN environment variable or ~/.anyscale/credentials.json
func GetAuthToken() (string, error) {
	token, _, err := resolveAuthToken(context.Background())
	return token, err
}

// resolveAuthToken is GetAuthToken plus the source the token came from. A
// missing credentials file wraps errCredentialsFileNotFound; any other error
// means a file was found but could not be used.
func resolveAuthToken(ctx context.Context) (token, source string, err error) {
	// First, try to get token from environment variable
	if token := os.Getenv("ANYSCALE_CLI_TOKEN"); token != "" {
		return token, tokenSourceEnv, nil
	}

	// If not found, try to read from ~/.anyscale/credentials.json
	credentialsPath, err := credentialsFilePath()
	if err != nil {
		return "", "", err
	}

	file, err := os.Open(credentialsPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("no ANYSCALE_CLI_TOKEN environment variable set and failed to read %s: %w", credentialsPath, errors.Join(errCredentialsFileNotFound, err))
		}
		return "", "", fmt.Errorf("no ANYSCALE_CLI_TOKEN environment variable set and failed to read %s: %w", credentialsPath, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			// Log the error but don't override the primary error
			tflog.Warn(ctx, "failed to close credentials file", map[string]any{"error": closeErr.Error()})
		}
	}()

	data, err := io.ReadAll(file)
	if err != nil {
		return "", "", fmt.Errorf("failed to read credentials file: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", "", fmt.Errorf("failed to parse credentials file: %w", err)
	}

	// Try cli_token first, then fall back to token
	token = creds.CLIToken
	if token == "" {
		token = creds.Token
	}

	if token == "" {
		return "", "", fmt.Errorf("token field is empty in credentials file")
	}

	return token, tokenSourceFile, nil
}

// DoRequest performs an authenticated HTTP request to the Anyscale API.
// The context is used for cancellation and timeouts: if ctx has no deadline
// of its own, each attempt gets defaultRequestTimeout; if it already has one
// (set by a caller that knows a particular call runs long), every attempt
// shares that deadline.
//
// GET and HEAD requests without a body are retried, up to maxReadAttempts in
// total, on transport timeouts and connection errors and on HTTP 429, 502,
// 503 and 504. Writes are never retried here: they are not idempotent, and
// the resources that need a write retry run their own. A non-2xx status is
// still returned as a response, not an error, once retries are exhausted; the
// attempt count rides on that response's request context (see
// requestAttempts). Retrying stops early when the caller's context is done
// or its deadline is too close for the next wait.
func (c *Client) DoRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	maxAttempts := 1
	if body == nil && (method == http.MethodGet || method == http.MethodHead) {
		maxAttempts = maxReadAttempts
	}

	for attempt := 1; ; attempt++ {
		resp, err := c.doAttempt(ctx, method, path, body, attempt)
		reason := retryReason(ctx, resp, err)
		if reason == "" || attempt == maxAttempts {
			return finalAttempt(method, path, attempt, resp, err)
		}
		wait := retryWait(resp, attempt)
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < wait {
			return finalAttempt(method, path, attempt, resp, err)
		}

		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
		}
		tflog.Warn(ctx, "Retrying Anyscale API read request", map[string]any{
			"method":  method,
			"path":    path,
			"attempt": attempt,
			"reason":  reason,
			"wait":    wait.String(),
		})
		sleep := c.sleep
		if sleep == nil {
			sleep = sleepCtx
		}
		if err := sleep(ctx, wait); err != nil {
			return nil, fmt.Errorf("%s %s: stopped retrying after %d attempts: %w", method, path, attempt, err)
		}
	}
}

// SetRetrySleep replaces the wait between read retries; nil restores the
// default timer. Tests in other packages use it to skip real backoff.
func (c *Client) SetRetrySleep(sleep func(ctx context.Context, d time.Duration) error) {
	c.sleep = sleep
}

// finalAttempt returns the last attempt's outcome. A transport error after a
// retry says how many attempts were made; a status response is returned as is.
func finalAttempt(method, path string, attempt int, resp *http.Response, err error) (*http.Response, error) {
	if err != nil && attempt > 1 {
		return nil, fmt.Errorf("%s %s failed after %d attempts (last error: %w). This is usually a transient Anyscale API or network problem; re-run the command", method, path, attempt, err)
	}
	return resp, err
}

// doAttempt sends one request. When ctx has no deadline the attempt gets its
// own timeout, released when the response body is closed.
func (c *Client) doAttempt(ctx context.Context, method, path string, body io.Reader, attempt int) (*http.Response, error) {
	url := c.BaseURL + path

	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		timeout := c.attemptTimeout
		if timeout == 0 {
			timeout = defaultRequestTimeout
		}
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	ctx = context.WithValue(ctx, attemptsKey{}, attempt)

	// Use NewRequestWithContext to support context cancellation
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add authentication header
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}

	// Tie the timeout's cancel to the body close, not this function's return:
	// callers read resp.Body after DoRequest returns, and canceling any
	// earlier would abort that read.
	if cancel != nil {
		resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	}

	return resp, nil
}

// attemptsKey carries the attempt number on each request's context, so
// DoRequestRaw can report how many attempts a final status took.
type attemptsKey struct{}

// requestAttempts returns the attempt number DoRequest recorded on resp's
// request, or 1 if there is none.
func requestAttempts(resp *http.Response) int {
	if resp == nil || resp.Request == nil {
		return 1
	}
	if n, ok := resp.Request.Context().Value(attemptsKey{}).(int); ok {
		return n
	}
	return 1
}

// retryReason returns why a read attempt is worth retrying, or "" if it is
// not. The caller's own cancellation or deadline is never retried.
func retryReason(ctx context.Context, resp *http.Response, err error) string {
	if ctx.Err() != nil {
		return ""
	}
	if err != nil {
		var netErr net.Error
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return "attempt timed out"
		case errors.As(err, &netErr) && netErr.Timeout():
			return "network timeout"
		case errors.Is(err, syscall.ECONNRESET):
			return "connection reset"
		case errors.Is(err, syscall.ECONNREFUSED):
			return "connection refused"
		case errors.Is(err, io.ErrUnexpectedEOF):
			return "unexpected EOF"
		}
		return ""
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "HTTP " + strconv.Itoa(resp.StatusCode)
	}
	return ""
}

// retryWait is the wait before the attempt after this one: a Retry-After in
// seconds on 429/503, else retryBaseBackoff doubled per attempt with +/-20%
// jitter. Either is capped at maxRetryBackoff.
func retryWait(resp *http.Response, attempt int) time.Duration {
	if resp != nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) {
		if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs >= 0 {
			return min(time.Duration(secs)*time.Second, maxRetryBackoff)
		}
	}
	backoff := retryBaseBackoff << (attempt - 1)
	jitter := 0.8 + 0.4*rand.Float64()
	return min(time.Duration(float64(backoff)*jitter), maxRetryBackoff)
}

// sleepCtx waits d, returning ctx.Err() early if ctx is done first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// cancelOnCloseBody releases a DoRequest-created context timeout when the
// response body is closed, rather than when DoRequest itself returns.
type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}
