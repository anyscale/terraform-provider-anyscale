package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveCloudNameToID(t *testing.T) {
	ctx := context.Background()

	t.Run("single matching cloud", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v2/clouds" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}

			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"results": [
					{
						"id": "cloud-123",
						"name": "test-cloud",
						"provider": "aws",
						"created_at": "2024-01-01T00:00:00Z"
					},
					{
						"id": "cloud-456",
						"name": "other-cloud",
						"provider": "gcp",
						"created_at": "2024-01-02T00:00:00Z"
					}
				],
				"metadata": {
					"total": 2,
					"next_paging_token": null
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		cloudID, err := ResolveCloudNameToID(ctx, client, "test-cloud")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if cloudID != "cloud-123" {
			t.Errorf("expected cloud ID 'cloud-123', got '%s'", cloudID)
		}
	})

	t.Run("multiple clouds with same name - returns most recent", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"results": [
					{
						"id": "cloud-old",
						"name": "duplicate-cloud",
						"provider": "aws",
						"created_at": "2024-01-01T00:00:00Z"
					},
					{
						"id": "cloud-new",
						"name": "duplicate-cloud",
						"provider": "aws",
						"created_at": "2024-01-15T00:00:00Z"
					},
					{
						"id": "cloud-middle",
						"name": "duplicate-cloud",
						"provider": "aws",
						"created_at": "2024-01-10T00:00:00Z"
					}
				],
				"metadata": {
					"total": 3,
					"next_paging_token": null
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		cloudID, err := ResolveCloudNameToID(ctx, client, "duplicate-cloud")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if cloudID != "cloud-new" {
			t.Errorf("expected most recent cloud ID 'cloud-new', got '%s'", cloudID)
		}
	})

	t.Run("cloud not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"results": [
					{
						"id": "cloud-123",
						"name": "existing-cloud",
						"provider": "aws",
						"created_at": "2024-01-01T00:00:00Z"
					}
				],
				"metadata": {
					"total": 1,
					"next_paging_token": null
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		_, err := ResolveCloudNameToID(ctx, client, "nonexistent-cloud")
		if err == nil {
			t.Fatal("expected error for nonexistent cloud, got nil")
		}

		if !strings.Contains(err.Error(), "no cloud found with name 'nonexistent-cloud'") {
			t.Errorf("expected error about cloud not found, got: %v", err)
		}
	})

	t.Run("API error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "internal server error"}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		_, err := ResolveCloudNameToID(ctx, client, "test-cloud")
		if err == nil {
			t.Fatal("expected error for API failure, got nil")
		}

		if !strings.Contains(err.Error(), "failed to list clouds") {
			t.Errorf("expected error about listing clouds, got: %v", err)
		}
	})

	t.Run("invalid JSON response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`invalid json`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		_, err := ResolveCloudNameToID(ctx, client, "test-cloud")
		if err == nil {
			t.Fatal("expected error for invalid JSON, got nil")
		}

		if !strings.Contains(err.Error(), "failed to list clouds") {
			t.Errorf("expected error about listing clouds, got: %v", err)
		}
	})

	t.Run("empty results", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"results": [],
				"metadata": {
					"total": 0,
					"next_paging_token": null
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		_, err := ResolveCloudNameToID(ctx, client, "any-cloud")
		if err == nil {
			t.Fatal("expected error for no results, got nil")
		}

		if !strings.Contains(err.Error(), "no cloud found") {
			t.Errorf("expected error about cloud not found, got: %v", err)
		}
	})

	t.Run("case sensitive matching", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"results": [
					{
						"id": "cloud-123",
						"name": "Test-Cloud",
						"provider": "aws",
						"created_at": "2024-01-01T00:00:00Z"
					}
				],
				"metadata": {
					"total": 1,
					"next_paging_token": null
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		// Should not match with different case
		_, err := ResolveCloudNameToID(ctx, client, "test-cloud")
		if err == nil {
			t.Fatal("expected error for case mismatch, got nil")
		}

		// Should match with exact case
		cloudID, err := ResolveCloudNameToID(ctx, client, "Test-Cloud")
		if err != nil {
			t.Fatalf("unexpected error for exact case match: %v", err)
		}

		if cloudID != "cloud-123" {
			t.Errorf("expected cloud ID 'cloud-123', got '%s'", cloudID)
		}
	})
}
