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

// A retried read's UnexpectedStatusError.Error() appends "(after N attempts;
// ...)" after the JSON body, so parsing the backend detail out of err.Error()
// fails for it. These pin that the detail is read from Body instead, that
// AddAPIError keeps the retry advisory, and that a single attempt's text is
// unchanged.
func TestAPIErrorDetail_RetriedRead(t *testing.T) {
	const apiDetail = "You do not have permission to read this cloud."
	jsonBody := `{"error":{"detail":"` + apiDetail + `"}}`
	const advisory = " The request failed on all 3 attempts; this is usually a transient Anyscale API problem, so re-run the command."

	addAPIErrorDetail := func(err error) string {
		var diags diag.Diagnostics
		AddAPIError(&diags, "read cloud", err)
		return diags[0].Detail()
	}

	t.Run("JSON detail, 3 attempts", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", &UnexpectedStatusError{StatusCode: 503, Body: jsonBody, Attempts: 3})
		if got := extractAPIErrorDetail(err); got != apiDetail {
			t.Errorf("extractAPIErrorDetail = %q, want the bare API detail %q", got, apiDetail)
		}
		if got, want := addAPIErrorDetail(err), "Failed to read cloud (HTTP 503): "+apiDetail+advisory; got != want {
			t.Errorf("AddAPIError detail = %q, want %q", got, want)
		}
	})

	// A detail with no closing punctuation gets a period before the note.
	t.Run("JSON detail without a period, 3 attempts", func(t *testing.T) {
		err := &UnexpectedStatusError{StatusCode: 404, Body: `{"error":{"detail":"Cloud not found"}}`, Attempts: 3}
		if got, want := addAPIErrorDetail(err), "Failed to read cloud (HTTP 404): Cloud not found."+advisory; got != want {
			t.Errorf("AddAPIError detail = %q, want %q", got, want)
		}
	})

	t.Run("non-JSON body, 3 attempts", func(t *testing.T) {
		err := &UnexpectedStatusError{StatusCode: 502, Body: "bad gateway", Attempts: 3}
		if got, want := addAPIErrorDetail(err), "Failed to read cloud (HTTP 502): bad gateway."+advisory; got != want {
			t.Errorf("AddAPIError detail = %q, want %q", got, want)
		}
		// No detail to extract: callers get the full error text, as before.
		if got := extractAPIErrorDetail(err); got != err.Error() {
			t.Errorf("extractAPIErrorDetail = %q, want err.Error() %q", got, err.Error())
		}
	})

	t.Run("empty body, 3 attempts", func(t *testing.T) {
		err := &UnexpectedStatusError{StatusCode: 504, Body: "", Attempts: 3}
		if got, want := addAPIErrorDetail(err), "Failed to read cloud (HTTP 504): the response had no body."+advisory; got != want {
			t.Errorf("AddAPIError detail = %q, want %q", got, want)
		}
	})

	t.Run("JSON detail, 1 attempt: text unchanged", func(t *testing.T) {
		err := &UnexpectedStatusError{StatusCode: 403, Body: jsonBody, Attempts: 1}
		if got := extractAPIErrorDetail(err); got != apiDetail {
			t.Errorf("extractAPIErrorDetail = %q, want %q", got, apiDetail)
		}
		if got, want := addAPIErrorDetail(err), "Failed to read cloud (HTTP 403): "+apiDetail; got != want {
			t.Errorf("AddAPIError detail = %q, want %q", got, want)
		}
	})

	// The backend detail matched by a classifier survives a retry.
	t.Run("classifier on a retried 409", func(t *testing.T) {
		err := fmt.Errorf("grant: %w", &UnexpectedStatusError{
			StatusCode: 409,
			Body:       `{"error":{"detail":"` + orgConvertedSubstring + `. Use role bindings."}}`,
			Attempts:   3,
		})
		if !isOrgConvertedConflict(err) {
			t.Error("isOrgConvertedConflict = false for a retried 409 carrying the converted-org detail")
		}
	})

	// A non-status error still has its detail parsed from the text.
	t.Run("non-status error with JSON in its text", func(t *testing.T) {
		err := errors.New("request failed: " + jsonBody)
		if got := extractAPIErrorDetail(err); got != apiDetail {
			t.Errorf("extractAPIErrorDetail = %q, want %q", got, apiDetail)
		}
	})
}
