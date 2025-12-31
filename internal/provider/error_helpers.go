package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// AddHTTPError adds a diagnostic error for an HTTP response with an unexpected status.
// It formats the error message to include the status code and response body.
//
// Example usage:
//
//	if httpResp.StatusCode != http.StatusOK {
//	    AddHTTPError(&resp.Diagnostics, "Get Project", httpResp.StatusCode, bodyBytes)
//	    return
//	}
func AddHTTPError(diags *diag.Diagnostics, operation string, statusCode int, body []byte) {
	diags.AddError(
		fmt.Sprintf("%s Failed", operation),
		fmt.Sprintf("API returned status %d: %s", statusCode, string(body)),
	)
}

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
		fmt.Sprintf("API Request Failed"),
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

// HandleAPIError is a comprehensive helper that checks HTTP status codes and
// adds appropriate diagnostic errors. Returns true if an error occurred.
//
// If expectedStatuses is empty, it defaults to checking for http.StatusOK.
//
// Example usage:
//
//	if HandleAPIError(ctx, &resp.Diagnostics, "create project", httpResp, bodyBytes, http.StatusCreated) {
//	    return
//	}
func HandleAPIError(
	ctx context.Context,
	diags *diag.Diagnostics,
	operation string,
	httpResp *http.Response,
	body []byte,
	expectedStatuses ...int,
) bool {
	// Default to http.StatusOK if no expected statuses provided
	if len(expectedStatuses) == 0 {
		expectedStatuses = []int{http.StatusOK}
	}

	// Check if status is expected
	statusExpected := false
	for _, expected := range expectedStatuses {
		if httpResp.StatusCode == expected {
			statusExpected = true
			break
		}
	}

	if !statusExpected {
		// Log the error for debugging
		tflog.Error(ctx, "Unexpected HTTP status", map[string]any{
			"operation":       operation,
			"status_code":     httpResp.StatusCode,
			"expected_status": expectedStatuses,
			"response":        SanitizeJSONForLog(string(body)),
		})

		AddHTTPError(diags, operation, httpResp.StatusCode, body)
		return true
	}

	return false
}

// HandleNotFoundError checks if an HTTP response is a 404 and adds an appropriate error.
// This is useful for Read operations where a 404 means the resource no longer exists.
// Returns true if it was a 404 error.
//
// Example usage:
//
//	if HandleNotFoundError(&resp.Diagnostics, "project", httpResp.StatusCode) {
//	    resp.State.RemoveResource(ctx)
//	    return
//	}
func HandleNotFoundError(diags *diag.Diagnostics, resourceType string, statusCode int) bool {
	if statusCode == http.StatusNotFound {
		diags.AddError(
			"Resource Not Found",
			fmt.Sprintf("The %s was not found (404). It may have been deleted outside of Terraform.", resourceType),
		)
		return true
	}
	return false
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
