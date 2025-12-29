package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Client represents the Anyscale API client
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

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
		BaseURL: baseURL,
		Token:   token,
		HTTPClient: &http.Client{
			Timeout: time.Second * 30,
		},
	}, nil
}

// NewClientWithToken creates a new Anyscale API client with explicit token
func NewClientWithToken(baseURL, token string) *Client {
	return &Client{
		BaseURL: baseURL,
		Token:   token,
		HTTPClient: &http.Client{
			Timeout: time.Second * 30,
		},
	}
}

// GetAuthToken retrieves the authentication token from either
// ANYSCALE_CLI_TOKEN environment variable or ~/.anyscale/credentials.json
func GetAuthToken() (string, error) {
	// First, try to get token from environment variable
	token := os.Getenv("ANYSCALE_CLI_TOKEN")
	if token != "" {
		return token, nil
	}

	// If not found, try to read from ~/.anyscale/credentials.json
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}

	credentialsPath := filepath.Join(homeDir, ".anyscale", "credentials.json")
	file, err := os.Open(credentialsPath)
	if err != nil {
		return "", fmt.Errorf("no ANYSCALE_CLI_TOKEN environment variable set and failed to read %s: %w", credentialsPath, err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("failed to read credentials file: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", fmt.Errorf("failed to parse credentials file: %w", err)
	}

	// Try cli_token first, then fall back to token
	token = creds.CLIToken
	if token == "" {
		token = creds.Token
	}

	if token == "" {
		return "", fmt.Errorf("token field is empty in credentials file")
	}

	return token, nil
}

// DoRequest performs an authenticated HTTP request to the Anyscale API.
// The context is used for cancellation and timeouts.
func (c *Client) DoRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := c.BaseURL + path

	// Use NewRequestWithContext to support context cancellation
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add authentication header
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}

	return resp, nil
}
