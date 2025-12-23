package provider

import (
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// TestProviderSchema verifies the provider schema structure
func TestProviderSchema(t *testing.T) {
	p := New()

	// Test provider schema fields
	if _, ok := p.Schema["api_url"]; !ok {
		t.Error("expected provider schema to have 'api_url' field")
	}
	if _, ok := p.Schema["token"]; !ok {
		t.Error("expected provider schema to have 'token' field")
	}

	// Verify api_url is optional with default
	apiURLSchema := p.Schema["api_url"]
	if apiURLSchema.Required {
		t.Error("expected 'api_url' to be optional")
	}
	if apiURLSchema.DefaultFunc == nil {
		t.Error("expected 'api_url' to have a default function")
	}

	// Verify token is optional and sensitive
	tokenSchema := p.Schema["token"]
	if tokenSchema.Required {
		t.Error("expected 'token' to be optional")
	}
	if !tokenSchema.Sensitive {
		t.Error("expected 'token' to be marked as sensitive")
	}
}

// TestProviderResources verifies resources are registered
func TestProviderResources(t *testing.T) {
	p := New()

	expectedResources := []string{
		"anyscale_cloud",
	}

	for _, resourceName := range expectedResources {
		if _, ok := p.ResourcesMap[resourceName]; !ok {
			t.Errorf("expected provider to have resource %q", resourceName)
		}
	}
}

// TestProviderValidation verifies the provider schema validates correctly
func TestProviderValidation(t *testing.T) {
	p := New()

	// InternalValidate checks schema consistency
	if err := p.InternalValidate(); err != nil {
		t.Errorf("provider schema validation failed: %v", err)
	}
}

// TestProviderSchemaTypes verifies schema types
func TestProviderSchemaTypes(t *testing.T) {
	p := New()

	tests := []struct {
		field        string
		expectedType schema.ValueType
	}{
		{"api_url", schema.TypeString},
		{"token", schema.TypeString},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			s, ok := p.Schema[tt.field]
			if !ok {
				t.Fatalf("expected schema to have field %q", tt.field)
			}
			if s.Type != tt.expectedType {
				t.Errorf("field %q type = %v, want %v", tt.field, s.Type, tt.expectedType)
			}
		})
	}
}

// TestNewClientWithToken verifies client creation with explicit token
func TestNewClientWithToken(t *testing.T) {
	client := NewClientWithToken("https://api.example.com", "test-token")

	if client.BaseURL != "https://api.example.com" {
		t.Errorf("BaseURL = %q, want %q", client.BaseURL, "https://api.example.com")
	}
	if client.Token != "test-token" {
		t.Errorf("Token = %q, want %q", client.Token, "test-token")
	}
	if client.HTTPClient == nil {
		t.Error("HTTPClient is nil")
	}
}

// TestCredentialsJSON tests credentials JSON unmarshaling
func TestCredentialsJSON(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected Credentials
	}{
		{
			name: "cli_token field",
			json: `{"cli_token": "my-cli-token"}`,
			expected: Credentials{
				CLIToken: "my-cli-token",
			},
		},
		{
			name: "token field",
			json: `{"token": "my-token"}`,
			expected: Credentials{
				Token: "my-token",
			},
		},
		{
			name: "both fields",
			json: `{"token": "my-token", "cli_token": "my-cli-token"}`,
			expected: Credentials{
				Token:    "my-token",
				CLIToken: "my-cli-token",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var creds Credentials
			if err := json.Unmarshal([]byte(tt.json), &creds); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if creds.Token != tt.expected.Token {
				t.Errorf("Token = %q, want %q", creds.Token, tt.expected.Token)
			}
			if creds.CLIToken != tt.expected.CLIToken {
				t.Errorf("CLIToken = %q, want %q", creds.CLIToken, tt.expected.CLIToken)
			}
		})
	}
}
