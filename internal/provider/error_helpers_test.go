package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

func TestAddHTTPError(t *testing.T) {
	var diags diag.Diagnostics

	AddHTTPError(&diags, "Create Project", 400, []byte(`{"error": "bad request"}`))

	if !diags.HasError() {
		t.Fatal("expected diagnostics to have error")
	}

	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}

	errDiag := diags[0]
	if !strings.Contains(errDiag.Summary(), "Create Project Failed") {
		t.Errorf("expected summary to contain 'Create Project Failed', got: %s", errDiag.Summary())
	}

	if !strings.Contains(errDiag.Detail(), "status 400") {
		t.Errorf("expected detail to contain 'status 400', got: %s", errDiag.Detail())
	}

	if !strings.Contains(errDiag.Detail(), "bad request") {
		t.Errorf("expected detail to contain 'bad request', got: %s", errDiag.Detail())
	}
}

func TestAddAPIError(t *testing.T) {
	var diags diag.Diagnostics

	testErr := errors.New("connection timeout")
	AddAPIError(&diags, "create project", testErr)

	if !diags.HasError() {
		t.Fatal("expected diagnostics to have error")
	}

	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}

	errDiag := diags[0]
	if !strings.Contains(errDiag.Summary(), "API Request Failed") {
		t.Errorf("expected summary to contain 'API Request Failed', got: %s", errDiag.Summary())
	}

	if !strings.Contains(errDiag.Detail(), "create project") {
		t.Errorf("expected detail to contain 'create project', got: %s", errDiag.Detail())
	}

	if !strings.Contains(errDiag.Detail(), "connection timeout") {
		t.Errorf("expected detail to contain 'connection timeout', got: %s", errDiag.Detail())
	}
}

func TestAddJSONError(t *testing.T) {
	var diags diag.Diagnostics

	testErr := errors.New("invalid character")
	AddJSONError(&diags, "unmarshal", "project response", testErr)

	if !diags.HasError() {
		t.Fatal("expected diagnostics to have error")
	}

	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}

	errDiag := diags[0]
	if errDiag.Summary() != "JSON Error" {
		t.Errorf("expected summary 'JSON Error', got: %s", errDiag.Summary())
	}

	if !strings.Contains(errDiag.Detail(), "unmarshal project response") {
		t.Errorf("expected detail to contain 'unmarshal project response', got: %s", errDiag.Detail())
	}
}

func TestAddConfigError(t *testing.T) {
	var diags diag.Diagnostics

	AddConfigError(&diags, "Invalid Configuration", "Both fields cannot be set at the same time")

	if !diags.HasError() {
		t.Fatal("expected diagnostics to have error")
	}

	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}

	errDiag := diags[0]
	if errDiag.Summary() != "Invalid Configuration" {
		t.Errorf("expected summary 'Invalid Configuration', got: %s", errDiag.Summary())
	}

	if errDiag.Detail() != "Both fields cannot be set at the same time" {
		t.Errorf("expected detail 'Both fields cannot be set at the same time', got: %s", errDiag.Detail())
	}
}

func TestHandleAPIError(t *testing.T) {
	ctx := context.Background()

	t.Run("expected status - no error", func(t *testing.T) {
		var diags diag.Diagnostics

		resp := &http.Response{StatusCode: http.StatusOK}
		hasError := HandleAPIError(ctx, &diags, "get project", resp, []byte(`{"result": {}}`))

		if hasError {
			t.Error("expected no error for status 200")
		}

		if diags.HasError() {
			t.Error("expected no diagnostics errors")
		}
	})

	t.Run("expected status with custom codes", func(t *testing.T) {
		var diags diag.Diagnostics

		resp := &http.Response{StatusCode: http.StatusCreated}
		hasError := HandleAPIError(ctx, &diags, "create project", resp, []byte(`{}`), http.StatusCreated, http.StatusOK)

		if hasError {
			t.Error("expected no error for status 201")
		}

		if diags.HasError() {
			t.Error("expected no diagnostics errors")
		}
	})

	t.Run("unexpected status - has error", func(t *testing.T) {
		var diags diag.Diagnostics

		resp := &http.Response{StatusCode: http.StatusBadRequest}
		hasError := HandleAPIError(ctx, &diags, "create project", resp, []byte(`{"error": "invalid"}`))

		if !hasError {
			t.Error("expected error for status 400")
		}

		if !diags.HasError() {
			t.Fatal("expected diagnostics to have error")
		}

		errDiag := diags[0]
		if !strings.Contains(errDiag.Summary(), "create project Failed") {
			t.Errorf("expected summary to contain 'create project Failed', got: %s", errDiag.Summary())
		}
	})

	t.Run("default expected status is 200", func(t *testing.T) {
		var diags diag.Diagnostics

		resp := &http.Response{StatusCode: http.StatusCreated}
		hasError := HandleAPIError(ctx, &diags, "test", resp, []byte(`{}`))

		if !hasError {
			t.Error("expected error when no expected statuses provided and got 201")
		}

		if !diags.HasError() {
			t.Fatal("expected diagnostics to have error")
		}
	})
}

func TestHandleNotFoundError(t *testing.T) {
	t.Run("404 status - returns true", func(t *testing.T) {
		var diags diag.Diagnostics

		isNotFound := HandleNotFoundError(&diags, "project", http.StatusNotFound)

		if !isNotFound {
			t.Error("expected true for 404 status")
		}

		if !diags.HasError() {
			t.Fatal("expected diagnostics to have error")
		}

		errDiag := diags[0]
		if errDiag.Summary() != "Resource Not Found" {
			t.Errorf("expected summary 'Resource Not Found', got: %s", errDiag.Summary())
		}

		if !strings.Contains(errDiag.Detail(), "project") {
			t.Errorf("expected detail to contain 'project', got: %s", errDiag.Detail())
		}
	})

	t.Run("non-404 status - returns false", func(t *testing.T) {
		var diags diag.Diagnostics

		isNotFound := HandleNotFoundError(&diags, "project", http.StatusOK)

		if isNotFound {
			t.Error("expected false for 200 status")
		}

		if diags.HasError() {
			t.Error("expected no diagnostics errors for non-404 status")
		}
	})
}

func TestWarnIfMultipleMatches(t *testing.T) {
	ctx := context.Background()

	// This is mainly to ensure the function doesn't panic
	// and properly logs when there are multiple matches

	t.Run("single match - no warning", func(t *testing.T) {
		// Should not panic
		WarnIfMultipleMatches(ctx, "cloud", "test-cloud", 1, "cloud-123")
	})

	t.Run("multiple matches - logs warning", func(t *testing.T) {
		// Should not panic
		WarnIfMultipleMatches(ctx, "cloud", "test-cloud", 3, "cloud-456")
	})
}

func TestHandleAPIErrorIntegration(t *testing.T) {
	ctx := context.Background()

	// Test with real HTTP response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "validation failed"}`))
	}))
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("failed to make test request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var diags diag.Diagnostics
	bodyBytes := []byte(`{"error": "validation failed"}`)

	hasError := HandleAPIError(ctx, &diags, "test operation", resp, bodyBytes)

	if !hasError {
		t.Error("expected error for 400 status")
	}

	if !diags.HasError() {
		t.Fatal("expected diagnostics to have error")
	}
}
