package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoRequestAndParse(t *testing.T) {
	ctx := context.Background()

	t.Run("successful request with default status", func(t *testing.T) {
		// Create a test server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": {"id": "test-123", "name": "test"}}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		type TestResponse struct {
			Result struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"result"`
		}

		resp, err := DoRequestAndParse[TestResponse](ctx, client, "GET", "/test", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if resp.Result.ID != "test-123" {
			t.Errorf("expected ID 'test-123', got '%s'", resp.Result.ID)
		}
		if resp.Result.Name != "test" {
			t.Errorf("expected Name 'test', got '%s'", resp.Result.Name)
		}
	})

	t.Run("successful request with custom status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"result": {"id": "created-123"}}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		type TestResponse struct {
			Result struct {
				ID string `json:"id"`
			} `json:"result"`
		}

		resp, err := DoRequestAndParse[TestResponse](ctx, client, "POST", "/test", nil, http.StatusCreated)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if resp.Result.ID != "created-123" {
			t.Errorf("expected ID 'created-123', got '%s'", resp.Result.ID)
		}
	})

	t.Run("unexpected status code", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error": "bad request"}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		type TestResponse struct {
			Result struct {
				ID string `json:"id"`
			} `json:"result"`
		}

		_, err := DoRequestAndParse[TestResponse](ctx, client, "GET", "/test", nil)
		if err == nil {
			t.Fatal("expected error for bad status code, got nil")
		}

		if !strings.Contains(err.Error(), "unexpected status 400") {
			t.Errorf("expected error message to contain 'unexpected status 400', got: %v", err)
		}
	})

	t.Run("invalid JSON response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`invalid json`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		type TestResponse struct {
			Result struct {
				ID string `json:"id"`
			} `json:"result"`
		}

		_, err := DoRequestAndParse[TestResponse](ctx, client, "GET", "/test", nil)
		if err == nil {
			t.Fatal("expected error for invalid JSON, got nil")
		}

		if !strings.Contains(err.Error(), "failed to parse JSON response") {
			t.Errorf("expected error message to contain 'failed to parse JSON response', got: %v", err)
		}
	})
}

func TestDoRequestRaw(t *testing.T) {
	ctx := context.Background()

	t.Run("successful request", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`test body`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		body, err := DoRequestRaw(ctx, client, "GET", "/test", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if string(body) != "test body" {
			t.Errorf("expected body 'test body', got '%s'", string(body))
		}
	})

	t.Run("multiple expected statuses", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`created`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		body, err := DoRequestRaw(ctx, client, "POST", "/test", nil, http.StatusOK, http.StatusCreated)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if string(body) != "created" {
			t.Errorf("expected body 'created', got '%s'", string(body))
		}
	})

	t.Run("unexpected status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`error`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		_, err := DoRequestRaw(ctx, client, "GET", "/test", nil)
		if err == nil {
			t.Fatal("expected error for 500 status, got nil")
		}
	})

	t.Run("404 not listed as expected satisfies errors.Is(ErrNotFound) and preserves the legacy verbatim phrase", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error": {"detail": "not found"}}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		_, err := DoRequestRaw(ctx, client, "GET", "/test", nil)
		if err == nil {
			t.Fatal("expected error for 404 status, got nil")
		}

		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected errors.Is(err, ErrNotFound) to be true, got err: %v", err)
		}

		if !strings.Contains(err.Error(), "unexpected status 404") {
			t.Errorf("expected error text to contain the verbatim legacy phrase \"unexpected status 404\", not just the digits. No production code matches this text any more (all not-found sites use errors.Is), but this assertion and the ExpectError regexp in acctest/ephemeral_service_credentials_acc_test.go both anchor on the literal phrase - rewording it in api_helpers.go breaks both. Got: %v", err)
		}
	})

	t.Run("404 listed as an expected status is a nil-error success, not ErrNotFound", func(t *testing.T) {
		// Guards an invariant two delete paths rely on: resource_service.go's final
		// Delete call and container_image_helpers.go's archive call both deliberately list
		// http.StatusNotFound as expected (an already-gone resource is a successful,
		// idempotent delete). Their old strings.Contains(err.Error(), "404") guards were
		// removed as dead code on exactly this basis - if this ever regressed (404 stopped
		// being treated as accepted here), those call sites would start receiving a real
		// ErrNotFound-wrapped error with nothing left to catch it, surfacing as a hard
		// destroy/archive failure instead of a silent success.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error": {"detail": "not found"}}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		_, err := DoRequestRaw(ctx, client, "DELETE", "/test", nil, http.StatusNoContent, http.StatusNotFound)
		if err != nil {
			t.Fatalf("expected nil error when 404 is a listed expected status, got: %v", err)
		}
	})
}

func TestMarshalRequestBody(t *testing.T) {
	t.Run("successful marshal", func(t *testing.T) {
		type TestRequest struct {
			Name  string `json:"name"`
			Value int    `json:"value"`
		}

		req := TestRequest{Name: "test", Value: 123}
		reader, err := MarshalRequestBody(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		expected := `{"name":"test","value":123}`
		if string(body) != expected {
			t.Errorf("expected body '%s', got '%s'", expected, string(body))
		}
	})

	t.Run("unmarshalable data", func(t *testing.T) {
		// channels cannot be marshaled to JSON
		invalidData := make(chan int)

		_, err := MarshalRequestBody(invalidData)
		if err == nil {
			t.Fatal("expected error for unmarshalable data, got nil")
		}
	})
}

func TestPaginatedRequest(t *testing.T) {
	ctx := context.Background()

	t.Run("single page", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"results": [{"id": "1"}, {"id": "2"}],
				"metadata": {"total": 2, "next_paging_token": null}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		type TestItem struct {
			ID string `json:"id"`
		}

		type TestResponse struct {
			Results  []TestItem `json:"results"`
			Metadata struct {
				Total           int     `json:"total"`
				NextPagingToken *string `json:"next_paging_token"`
			} `json:"metadata"`
		}

		items, err := PaginatedRequest(ctx, client, "/test", nil,
			func(body []byte) ([]TestItem, *string, error) {
				var resp TestResponse
				if err := unmarshalJSON(body, &resp); err != nil {
					return nil, nil, err
				}
				return resp.Results, resp.Metadata.NextPagingToken, nil
			},
		)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(items) != 2 {
			t.Errorf("expected 2 items, got %d", len(items))
		}
	})

	t.Run("multiple pages", func(t *testing.T) {
		requestCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.WriteHeader(http.StatusOK)

			if requestCount == 1 {
				nextToken := "page2"
				_, _ = fmt.Fprintf(w, `{
					"results": [{"id": "1"}, {"id": "2"}],
					"metadata": {"total": 4, "next_paging_token": "%s"}
				}`, nextToken)
			} else {
				_, _ = w.Write([]byte(`{
					"results": [{"id": "3"}, {"id": "4"}],
					"metadata": {"total": 4, "next_paging_token": null}
				}`))
			}
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		type TestItem struct {
			ID string `json:"id"`
		}

		type TestResponse struct {
			Results  []TestItem `json:"results"`
			Metadata struct {
				Total           int     `json:"total"`
				NextPagingToken *string `json:"next_paging_token"`
			} `json:"metadata"`
		}

		items, err := PaginatedRequest(ctx, client, "/test", nil,
			func(body []byte) ([]TestItem, *string, error) {
				var resp TestResponse
				if err := unmarshalJSON(body, &resp); err != nil {
					return nil, nil, err
				}
				return resp.Results, resp.Metadata.NextPagingToken, nil
			},
		)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(items) != 4 {
			t.Errorf("expected 4 items, got %d", len(items))
		}

		if requestCount != 2 {
			t.Errorf("expected 2 requests, got %d", requestCount)
		}
	})

	t.Run("parse error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`invalid json`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		type TestItem struct {
			ID string `json:"id"`
		}

		_, err := PaginatedRequest(ctx, client, "/test", nil,
			func(body []byte) ([]TestItem, *string, error) {
				return nil, nil, fmt.Errorf("parse error")
			},
		)

		if err == nil {
			t.Fatal("expected error for parse failure, got nil")
		}

		if !strings.Contains(err.Error(), "failed to parse paginated response") {
			t.Errorf("expected error message to contain 'failed to parse paginated response', got: %v", err)
		}
	})
}

func TestCloseBody(t *testing.T) {
	ctx := context.Background()

	t.Run("successful close", func(t *testing.T) {
		body := io.NopCloser(strings.NewReader("test"))
		// This should not panic or error
		CloseBody(ctx, body)
	})

	t.Run("nil body", func(t *testing.T) {
		// This should not panic
		CloseBody(ctx, nil)
	})
}

// Helper function for unmarshaling JSON in tests
func unmarshalJSON(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
