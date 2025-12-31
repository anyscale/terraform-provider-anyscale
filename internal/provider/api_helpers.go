package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// DoRequestAndParse performs an HTTP request, reads and closes the response body,
// checks the status code, and unmarshals the JSON response into the provided type.
// This combines the most common request pattern used throughout the provider.
//
// Example usage:
//
//	project, err := DoRequestAndParse[ProjectResponse](
//	    ctx, r.client, "GET", "/api/v2/projects/"+id, nil, http.StatusOK,
//	)
func DoRequestAndParse[T any](
	ctx context.Context,
	client *Client,
	method, path string,
	body io.Reader,
	expectedStatuses ...int,
) (*T, error) {
	bodyBytes, err := DoRequestRaw(ctx, client, method, path, body, expectedStatuses...)
	if err != nil {
		return nil, err
	}

	var result T
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return &result, nil
}

// DoRequestRaw performs an HTTP request, reads and closes the response body,
// and checks the status code. Returns the raw response body bytes.
//
// If expectedStatuses is empty, it defaults to checking for http.StatusOK.
//
// Example usage:
//
//	bodyBytes, err := DoRequestRaw(
//	    ctx, r.client, "DELETE", "/api/v2/projects/"+id, nil, http.StatusNoContent,
//	)
func DoRequestRaw(
	ctx context.Context,
	client *Client,
	method, path string,
	body io.Reader,
	expectedStatuses ...int,
) ([]byte, error) {
	// Default to http.StatusOK if no expected statuses provided
	if len(expectedStatuses) == 0 {
		expectedStatuses = []int{http.StatusOK}
	}

	// Make the request
	httpResp, err := client.DoRequest(ctx, method, path, body)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer CloseBody(ctx, httpResp.Body)

	// Read the response body
	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Check status code
	if !isStatusExpected(httpResp.StatusCode, expectedStatuses) {
		return nil, fmt.Errorf("unexpected status %d: %s", httpResp.StatusCode, string(bodyBytes))
	}

	return bodyBytes, nil
}

// CloseBody safely closes a response body with error logging.
// Use this in defer statements to ensure proper cleanup.
//
// Example usage:
//
//	defer CloseBody(ctx, httpResp.Body)
func CloseBody(ctx context.Context, body io.ReadCloser) {
	if body == nil {
		return
	}
	if closeErr := body.Close(); closeErr != nil {
		tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
	}
}

// isStatusExpected checks if the status code matches any of the expected statuses
func isStatusExpected(statusCode int, expectedStatuses []int) bool {
	for _, expected := range expectedStatuses {
		if statusCode == expected {
			return true
		}
	}
	return false
}

// PaginatedRequest handles automatic pagination for list endpoints that use
// the Anyscale API pagination pattern (next_paging_token).
//
// The parseFunc should extract items and the next paging token from the response body.
// Returns all items collected across all pages.
//
// Example usage:
//
//	items, err := PaginatedRequest(ctx, client, "/api/v2/projects", queryParams,
//	    func(body []byte) ([]ProjectResult, *string, error) {
//	        var resp ProjectsListResponse
//	        if err := json.Unmarshal(body, &resp); err != nil {
//	            return nil, nil, err
//	        }
//	        return resp.Results, resp.Metadata.NextPagingToken, nil
//	    },
//	)
func PaginatedRequest[T any](
	ctx context.Context,
	client *Client,
	basePath string,
	queryParams url.Values,
	parseFunc func(body []byte) (items []T, nextToken *string, err error),
) ([]T, error) {
	allItems := []T{}
	nextToken := ""

	for {
		// Build query parameters for this page
		pageParams := url.Values{}
		for k, v := range queryParams {
			pageParams[k] = v
		}
		if nextToken != "" {
			pageParams.Add("paging_token", nextToken)
		}

		// Build the full path with query parameters
		path := basePath
		if len(pageParams) > 0 {
			path = fmt.Sprintf("%s?%s", basePath, pageParams.Encode())
		}

		// Make the request
		bodyBytes, err := DoRequestRaw(ctx, client, "GET", path, nil, http.StatusOK)
		if err != nil {
			return nil, fmt.Errorf("pagination request failed: %w", err)
		}

		// Parse the response
		items, nextTokenPtr, err := parseFunc(bodyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse paginated response: %w", err)
		}

		// Accumulate items
		allItems = append(allItems, items...)

		// Check if there are more pages
		if nextTokenPtr == nil || *nextTokenPtr == "" {
			break
		}
		nextToken = *nextTokenPtr

		tflog.Debug(ctx, "Fetching next page", map[string]any{
			"items_so_far": len(allItems),
			"next_token":   nextToken,
		})
	}

	tflog.Debug(ctx, "Pagination complete", map[string]any{
		"total_items": len(allItems),
	})

	return allItems, nil
}

// MarshalRequestBody marshals a request struct to JSON and returns it as an io.Reader.
// This is a convenience helper for preparing request bodies.
//
// Example usage:
//
//	reqBody, err := MarshalRequestBody(createReq)
//	if err != nil {
//	    return err
//	}
//	resp, err := DoRequestAndParse[ProjectResponse](ctx, client, "POST", "/api/v2/projects", reqBody, http.StatusCreated)
func MarshalRequestBody(v interface{}) (io.Reader, error) {
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}
	return bytes.NewReader(jsonBytes), nil
}
