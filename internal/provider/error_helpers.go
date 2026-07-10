package provider

import (
	"context"
	"fmt"

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
