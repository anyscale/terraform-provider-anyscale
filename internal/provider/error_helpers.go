package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// AddAPIError adds a diagnostic error for a general API operation failure.
// Use this for errors from the HTTP client or from a non-accepted status code.
//
// When err carries an HTTP status (UnexpectedStatusError), the detail reads
// "Failed to <operation> (HTTP <status>): <backend detail>": the status tells
// a 403 from a 404 at a glance, and the backend's own message replaces the raw
// JSON body. Any other error keeps the "Failed to <operation>: <error>" shape.
//
// Example usage:
//
//	if err != nil {
//	    AddAPIError(&resp.Diagnostics, "create project", err)
//	    return
//	}
func AddAPIError(diags *diag.Diagnostics, operation string, err error) {
	var statusErr *UnexpectedStatusError
	if errors.As(err, &statusErr) {
		detail := extractAPIErrorDetail(statusErr)
		if detail == statusErr.Error() {
			// Not the backend's {"error": {"detail": ...}} shape: show the body
			// itself rather than repeating the status inside the message.
			detail = strings.TrimSpace(statusErr.Body)
			if detail == "" {
				detail = "the response had no body"
			}
		}
		diags.AddError(
			"API Request Failed",
			fmt.Sprintf("Failed to %s (HTTP %d): %s", operation, statusErr.StatusCode, detail),
		)
		return
	}
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
//	if plan.SomeRequiredField.IsNull() {
//	    AddConfigError(&resp.Diagnostics, "Required Field Missing",
//	        "SomeRequiredField must be specified.")
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
// fall through to the raw wrapper every time.
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

// isGoneOrForbidden reports whether err means a resource's own lookup or delete found it gone: a
// 404 (ErrNotFound) or a 403. The API answers 403 "Permission denied", not 404, for a deleted or
// nonexistent cloud, cloud resource, project, or service when the caller cannot see it, so a 403
// on those endpoints is the usual shape of an out-of-band deletion.
//
// Use it only in a managed resource's Read or Delete for those resource types, and pair every
// 403 it accepts with addForbiddenTreatedAsDeletedWarning: a 403 is also what a token that lost
// access receives, and the warning is the only signal the practitioner gets in that case. Data
// sources and other resources keep a 403 as an error.
func isGoneOrForbidden(err error) bool {
	return errors.Is(err, ErrNotFound) || isHTTPStatus(err, http.StatusForbidden)
}

// addForbiddenTreatedAsDeletedWarning adds the warning for a 403 that isGoneOrForbidden accepted
// as gone. It does nothing for any other error, so a caller can pass every gone error through it
// and a 404 stays silent as before. kind is the resource's display noun ("Cloud", "Project").
func addForbiddenTreatedAsDeletedWarning(diags *diag.Diagnostics, kind, id string, err error) {
	if !isHTTPStatus(err, http.StatusForbidden) {
		return
	}
	diags.AddWarning(
		kind+" Treated As Deleted",
		fmt.Sprintf("%s %s returned HTTP 403; it is treated as deleted and removed from state. "+
			"If the token lost access instead, the next apply would create a duplicate; restore access, or run terraform state rm.",
			kind, id),
	)
}

// addForbiddenTreatedAsDeletedOnDestroyWarning is addForbiddenTreatedAsDeletedWarning for a
// destroy. There the risk of a 403 that meant lost access is the opposite one: the object may
// still exist, no longer managed by Terraform.
func addForbiddenTreatedAsDeletedOnDestroyWarning(diags *diag.Diagnostics, kind, id string, err error) {
	if !isHTTPStatus(err, http.StatusForbidden) {
		return
	}
	diags.AddWarning(
		kind+" Treated As Deleted",
		fmt.Sprintf("%s %s returned HTTP 403 during destroy; it is treated as already deleted and removed from state. "+
			"If the token lost access instead, the %s may still exist outside Terraform; check it in the Anyscale console.",
			kind, id, strings.ToLower(kind)),
	)
}

// confirmGoneAfterForbiddenDelete decides a destroy whose delete call answered 403. That status
// is ambiguous: the API answers 403 for an object that is already gone, but also for one the
// credentials can see and are not allowed to delete. Accepting every 403 as gone would end the
// destroy successfully while the object keeps running, unmanaged. So the object is read back:
//
//   - still readable: the delete was refused, so the destroy fails and state is kept;
//   - 404: it is gone, and the destroy finishes silently;
//   - 403: it is not visible either, so it is treated as gone with the destroy warning;
//   - any other failure: whether it is gone is unknown, so the destroy fails and can be retried.
//
// readBack returns nil when the object can still be read, and otherwise the error that stopped
// it. kind is the resource's display noun ("Cloud", "Cloud Resource").
func confirmGoneAfterForbiddenDelete(ctx context.Context, diags *diag.Diagnostics, kind, id string, deleteErr error, readBack func() error) {
	noun := strings.ToLower(kind)
	err := readBack()
	switch {
	case err == nil:
		tflog.Warn(ctx, "Delete returned 403 but the object can still be read", map[string]any{"kind": kind, "id": id})
		diags.AddError(
			kind+" Was Not Deleted",
			fmt.Sprintf("Deleting %s %s was refused with HTTP 403 (%s), and the %s can still be read, so it still exists: "+
				"the credentials in use can see it but are not allowed to delete it. Destroy it with an identity that may delete it, "+
				"or run terraform state rm to stop managing it without deleting it.",
				noun, id, extractAPIErrorDetail(deleteErr), noun),
		)
	case isGoneOrForbidden(err):
		tflog.Info(ctx, "Delete returned 403 and the object can no longer be read; treating it as deleted", map[string]any{"kind": kind, "id": id})
		addForbiddenTreatedAsDeletedOnDestroyWarning(diags, kind, id, err)
	default:
		diags.AddError(
			"Could Not Confirm "+kind+" Deletion",
			fmt.Sprintf("Deleting %s %s was refused with HTTP 403 (%s), and reading it back to tell whether it is already gone failed: %s. "+
				"Retry the destroy.",
				noun, id, extractAPIErrorDetail(deleteErr), extractAPIErrorDetail(err)),
		)
	}
}

// readBackByGET is the readBack for confirmGoneAfterForbiddenDelete when one GET of path tells
// whether the object still exists.
func readBackByGET(ctx context.Context, client *Client, path string) func() error {
	return func() error {
		_, err := DoRequestRaw(ctx, client, "GET", path, nil, http.StatusOK)
		return err
	}
}
