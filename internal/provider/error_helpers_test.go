package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

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

func TestAddAPIError_StatusAndBackendDetail(t *testing.T) {
	detailOf := func(err error) (string, string) {
		var diags diag.Diagnostics
		AddAPIError(&diags, "read cloud", err)
		return diags[0].Summary(), diags[0].Detail()
	}

	t.Run("status error with backend detail", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", &UnexpectedStatusError{
			StatusCode: 403,
			Body:       `{"error":{"detail":"You do not have permission to read this cloud."}}`,
		})
		summary, detail := detailOf(err)
		if summary != "API Request Failed" {
			t.Errorf("summary must stay stable, got %q", summary)
		}
		if want := "Failed to read cloud (HTTP 403): You do not have permission to read this cloud."; detail != want {
			t.Errorf("detail = %q, want %q", detail, want)
		}
	})

	t.Run("status error with non-JSON body", func(t *testing.T) {
		_, detail := detailOf(&UnexpectedStatusError{StatusCode: 502, Body: "bad gateway"})
		if want := "Failed to read cloud (HTTP 502): bad gateway"; detail != want {
			t.Errorf("detail = %q, want %q", detail, want)
		}
	})

	// Positive control: an error with no HTTP status keeps the old shape.
	t.Run("transport error", func(t *testing.T) {
		_, detail := detailOf(errors.New("connection timeout"))
		if want := "Failed to read cloud: connection timeout"; detail != want {
			t.Errorf("detail = %q, want %q", detail, want)
		}
	})
}
