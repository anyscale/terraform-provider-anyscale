package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Client represents the Anyscale API client
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// defaultRequestTimeout bounds an API call when the caller's context carries
// no deadline of its own. Long-running calls (e.g. add_resource, which
// legitimately runs for minutes while real cloud infra is registered) set
// their own longer deadline on ctx before calling DoRequest, which then
// leaves it untouched instead of overriding it with this default.
const defaultRequestTimeout = 60 * time.Second

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
// of its own, defaultRequestTimeout is applied; if it already has one (set by
// a caller that knows a particular call runs long), that deadline is left as-is.
func (c *Client) DoRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := c.BaseURL + path

	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
	}

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
