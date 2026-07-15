package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// AddAPIError adds a diagnostic error for a general API operation failure.
// Use this for errors from the HTTP client or other non-status-code errors.
//
// Example usage:
//
//	if err != nil {
//	    AddAPIError(&resp.Diagnostics, "create project", err)
//	    return
//	}
func AddAPIError(diags *diag.Diagnostics, operation string, err error) {
	diags.AddError(
		"API Request Failed",
		fmt.Sprintf("Failed to %s: %s", operation, err.Error()),
	)
}

// AddJSONError adds a diagnostic error for JSON marshaling/unmarshaling failures.
//
// Example usage:
//
//	if err := json.Unmarshal(body, &result); err != nil {
//	    AddJSONError(&resp.Diagnostics, "unmarshal", "project response", err)
//	    return
//	}
func AddJSONError(diags *diag.Diagnostics, operation string, dataType string, err error) {
	diags.AddError(
		"JSON Error",
		fmt.Sprintf("Failed to %s %s: %s", operation, dataType, err.Error()),
	)
}

// AddConfigError adds a diagnostic error for configuration/validation issues.
//
// Example usage:
//
//	if plan.CloudID.IsNull() && plan.CloudName.IsNull() {
//	    AddConfigError(&resp.Diagnostics, "Cloud Reference Required",
//	        "Either 'cloud_id' or 'cloud_name' must be specified.")
//	    return
//	}
func AddConfigError(diags *diag.Diagnostics, summary string, detail string) {
	diags.AddError(summary, detail)
}

// extractAPIErrorDetail pulls the backend's own error detail message out of an
// error produced by DoRequestRaw/DoRequestAndParse (formatted as "unexpected
// status %d: %s", where %s is the raw response body), so a caller can present
// Anyscale's own error text instead of that wrapper plus a raw JSON dump.
//
// The real wire shape is nested - {"error": {"detail": "...", ...}} - traced
// against api_common.py's HTTPException handler (`ErrorResponse(error=Error(detail=exc.detail))`)
// and the Error/ErrorResponse Pydantic models in api/common/models/base.py.
// Every AnyscaleHTTPException (which every raised detail in this provider's
// traced 403s/400s is) goes through this handler - it is not a bare top-level
// {"detail": "..."}, which would silently under-parse to an empty Detail and
// fall through to the raw wrapper every time (caught in review before ship).
//
// Falls back to the full wrapped error text if the body isn't this shape.
// Preferred over per-message string-matching: it surfaces every distinct
// backend error for an endpoint cleanly and uniformly, including ones not
// specifically anticipated by the caller.
func extractAPIErrorDetail(err error) string {
	msg := err.Error()

	idx := strings.Index(msg, "{")
	if idx == -1 {
		return msg
	}

	var body struct {
		Error struct {
			Detail string `json:"detail"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal([]byte(msg[idx:]), &body); jsonErr != nil || body.Error.Detail == "" {
		return msg
	}

	return body.Error.Detail
}

// WarnIfMultipleMatches logs a warning if multiple matches were found.
// This is commonly used when resolving names to IDs.
//
// Example usage:
//
//	WarnIfMultipleMatches(ctx, "cloud", cloudName, matchCount, selectedID)
func WarnIfMultipleMatches(ctx context.Context, resourceType string, name string, count int, selectedID string) {
	if count > 1 {
		tflog.Warn(ctx, fmt.Sprintf("Multiple %ss found with same name, using most recent", resourceType), map[string]any{
			fmt.Sprintf("%s_name", resourceType): name,
			"count":                              count,
			"selected":                           selectedID,
		})
	}
}
