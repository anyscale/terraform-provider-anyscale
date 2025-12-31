# Helper Functions Documentation

This document describes the common helper functions available in the Terraform provider and provides usage examples showing the code simplification benefits.

## Overview

The provider includes four helper modules that eliminate code duplication and standardize common patterns:

1. **api_helpers.go** - API request/response handling and pagination
2. **error_helpers.go** - Diagnostic error handling
3. **cloud_helpers.go** - Cloud-specific utilities (name resolution)
4. **framework_helpers.go** - Type conversion utilities (existing)

## API Helpers (`api_helpers.go`)

### DoRequestAndParse[T]

Performs an HTTP request, reads the response, checks status, and unmarshals JSON in one call.

**Before:**
```go
httpResp, err := r.client.DoRequest(ctx, "GET", "/api/v2/projects/"+id, nil)
if err != nil {
    resp.Diagnostics.AddError("API Request Error", err.Error())
    return
}
defer func() {
    if closeErr := httpResp.Body.Close(); closeErr != nil {
        tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
    }
}()

bodyBytes, err := io.ReadAll(httpResp.Body)
if err != nil {
    resp.Diagnostics.AddError("Response Read Error", err.Error())
    return
}

if httpResp.StatusCode != http.StatusOK {
    resp.Diagnostics.AddError("API Error", fmt.Sprintf("Status: %d - %s", httpResp.StatusCode, string(bodyBytes)))
    return
}

var project ProjectResponse
if err := json.Unmarshal(bodyBytes, &project); err != nil {
    resp.Diagnostics.AddError("JSON Unmarshal Error", err.Error())
    return
}
```

**After:**
```go
project, err := DoRequestAndParse[ProjectResponse](
    ctx, r.client, "GET", "/api/v2/projects/"+id, nil, http.StatusOK,
)
if err != nil {
    AddAPIError(&resp.Diagnostics, "read project", err)
    return
}
```

**Lines reduced:** 28 → 7 (75% reduction)

### DoRequestRaw

Same as DoRequestAndParse but returns raw bytes instead of unmarshaling.

**Usage:**
```go
bodyBytes, err := DoRequestRaw(
    ctx, client, "DELETE", "/api/v2/projects/"+id, nil, http.StatusNoContent,
)
```

### PaginatedRequest[T]

Handles automatic pagination for list endpoints.

**Before:**
```go
allProjects := []ProjectResult{}
nextToken := ""

for {
    queryParams := url.Values{}
    for k, v := range params {
        queryParams[k] = v
    }
    if nextToken != "" {
        queryParams.Add("paging_token", nextToken)
    }

    path := "/api/v2/projects"
    if len(queryParams) > 0 {
        path = fmt.Sprintf("%s?%s", path, queryParams.Encode())
    }

    httpResp, err := d.client.DoRequest(ctx, "GET", path, nil)
    if err != nil {
        return err
    }
    defer httpResp.Body.Close()

    bodyBytes, err := io.ReadAll(httpResp.Body)
    if err != nil {
        return err
    }

    var resp ProjectsListResponse
    if err := json.Unmarshal(bodyBytes, &resp); err != nil {
        return err
    }

    allProjects = append(allProjects, resp.Results...)

    if resp.Metadata.NextPagingToken == nil || *resp.Metadata.NextPagingToken == "" {
        break
    }
    nextToken = *resp.Metadata.NextPagingToken
}
```

**After:**
```go
allProjects, err := PaginatedRequest(ctx, d.client, "/api/v2/projects", params,
    func(body []byte) ([]ProjectResult, *string, error) {
        var resp ProjectsListResponse
        if err := json.Unmarshal(body, &resp); err != nil {
            return nil, nil, err
        }
        return resp.Results, resp.Metadata.NextPagingToken, nil
    },
)
```

**Lines reduced:** ~40 → ~9 (77% reduction)

### MarshalRequestBody

Convenience helper for preparing request bodies.

**Before:**
```go
reqBody, err := json.Marshal(createReq)
if err != nil {
    resp.Diagnostics.AddError("Request Serialization Error", err.Error())
    return
}
resp, err := DoRequestAndParse[ProjectResponse](
    ctx, client, "POST", "/api/v2/projects", strings.NewReader(string(reqBody)), http.StatusCreated,
)
```

**After:**
```go
reqBody, err := MarshalRequestBody(createReq)
if err != nil {
    AddJSONError(&resp.Diagnostics, "marshal", "project request", err)
    return
}
resp, err := DoRequestAndParse[ProjectResponse](
    ctx, client, "POST", "/api/v2/projects", reqBody, http.StatusCreated,
)
```

### CloseBody

Safely closes response body with error logging.

**Usage:**
```go
defer CloseBody(ctx, httpResp.Body)
```

## Error Helpers (`error_helpers.go`)

### AddHTTPError

Adds standardized diagnostic for HTTP status errors.

**Before:**
```go
resp.Diagnostics.AddError(
    "Project Creation Failed",
    fmt.Sprintf("API returned status %d: %s", httpResp.StatusCode, string(bodyBytes)),
)
```

**After:**
```go
AddHTTPError(&resp.Diagnostics, "Create Project", httpResp.StatusCode, bodyBytes)
```

### AddAPIError

Adds diagnostic for general API operation errors.

**Before:**
```go
resp.Diagnostics.AddError(
    "API Request Error",
    fmt.Sprintf("Failed to create project: %s", err.Error()),
)
```

**After:**
```go
AddAPIError(&resp.Diagnostics, "create project", err)
```

### AddJSONError

Adds diagnostic for JSON marshaling/unmarshaling errors.

**Usage:**
```go
if err := json.Unmarshal(body, &result); err != nil {
    AddJSONError(&resp.Diagnostics, "unmarshal", "project response", err)
    return
}
```

### AddConfigError

Adds diagnostic for configuration/validation issues.

**Usage:**
```go
if plan.CloudID.IsNull() && plan.CloudName.IsNull() {
    AddConfigError(&resp.Diagnostics, "Cloud Reference Required",
        "Either 'cloud_id' or 'cloud_name' must be specified.")
    return
}
```

### HandleAPIError

Comprehensive helper that checks status and adds diagnostics. Returns true if error occurred.

**Before:**
```go
if httpResp.StatusCode != http.StatusCreated && httpResp.StatusCode != http.StatusOK {
    bodyBytes, _ := io.ReadAll(httpResp.Body)
    resp.Diagnostics.AddError("Project Creation Failed",
        fmt.Sprintf("API returned status %d: %s", httpResp.StatusCode, string(bodyBytes)))
    return
}
```

**After:**
```go
if HandleAPIError(ctx, &resp.Diagnostics, "create project", httpResp, bodyBytes, http.StatusCreated, http.StatusOK) {
    return
}
```

### HandleNotFoundError

Checks for 404 and adds appropriate error. Useful in Read operations.

**Usage:**
```go
if HandleNotFoundError(&resp.Diagnostics, "project", httpResp.StatusCode) {
    resp.State.RemoveResource(ctx)
    return
}
```

### WarnIfMultipleMatches

Logs warning when multiple resources match a name query.

**Usage:**
```go
WarnIfMultipleMatches(ctx, "cloud", cloudName, matchCount, selectedID)
```

## Cloud Helpers (`cloud_helpers.go`)

### ResolveCloudNameToID

Converts cloud name to cloud ID. Previously duplicated in 7 files.

**Before (in each resource/data source file):**
```go
func (r *ProjectResource) resolveCloudNameToID(ctx context.Context, cloudName string) (string, error) {
    httpResp, err := r.client.DoRequest(ctx, "GET", "/api/v2/clouds", nil)
    if err != nil {
        return "", fmt.Errorf("failed to list clouds: %w", err)
    }
    defer httpResp.Body.Close()

    if httpResp.StatusCode != http.StatusOK {
        bodyBytes, _ := io.ReadAll(httpResp.Body)
        return "", fmt.Errorf("API returned status %d: %s", httpResp.StatusCode, string(bodyBytes))
    }

    bodyBytes, err := io.ReadAll(httpResp.Body)
    if err != nil {
        return "", fmt.Errorf("failed to read response: %w", err)
    }

    var cloudsResp CloudsListResponse
    if err := json.Unmarshal(bodyBytes, &cloudsResp); err != nil {
        return "", fmt.Errorf("failed to parse clouds response: %w", err)
    }

    var matchedCloudID string
    var latestCreatedAt string
    for _, cloud := range cloudsResp.Results {
        if cloud.Name == cloudName {
            if matchedCloudID == "" || cloud.CreatedAt > latestCreatedAt {
                matchedCloudID = cloud.ID
                latestCreatedAt = cloud.CreatedAt
            }
        }
    }

    if matchedCloudID == "" {
        return "", fmt.Errorf("no cloud found with name '%s'", cloudName)
    }

    // [... additional logic for multiple matches warning ...]

    return matchedCloudID, nil
}
```

**After (single shared implementation):**
```go
cloudID, err := ResolveCloudNameToID(ctx, r.client, cloudName)
if err != nil {
    resp.Diagnostics.AddError(
        "Cloud Name Resolution Failed",
        fmt.Sprintf("Failed to resolve cloud name '%s': %s", cloudName, err.Error()),
    )
    return
}
```

**Lines eliminated:** ~65 lines × 7 files = ~455 lines removed

## Complete Example: Resource Create Method

### Before (typical pattern)
```go
func (r *ProjectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
    var plan ProjectResourceModel
    resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
    if resp.Diagnostics.HasError() {
        return
    }

    // Resolve cloud name if needed
    cloudID := plan.CloudID.ValueString()
    if plan.CloudID.IsNull() && !plan.CloudName.IsNull() {
        resolvedID, err := r.resolveCloudNameToID(ctx, plan.CloudName.ValueString())
        if err != nil {
            resp.Diagnostics.AddError("Cloud Resolution Failed", err.Error())
            return
        }
        cloudID = resolvedID
    }

    // Build and marshal request
    createReq := CreateProjectRequest{
        Name:          plan.Name.ValueString(),
        ParentCloudID: cloudID,
    }

    reqBody, err := json.Marshal(createReq)
    if err != nil {
        resp.Diagnostics.AddError("Serialization Error", err.Error())
        return
    }

    // Make API request
    httpResp, err := r.client.DoRequest(ctx, "POST", "/api/v2/projects", strings.NewReader(string(reqBody)))
    if err != nil {
        resp.Diagnostics.AddError("API Request Error", err.Error())
        return
    }
    defer func() {
        if closeErr := httpResp.Body.Close(); closeErr != nil {
            tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
        }
    }()

    // Read response
    bodyBytes, err := io.ReadAll(httpResp.Body)
    if err != nil {
        resp.Diagnostics.AddError("Response Read Error", err.Error())
        return
    }

    // Check status
    if httpResp.StatusCode != http.StatusCreated {
        resp.Diagnostics.AddError("Creation Failed",
            fmt.Sprintf("Status %d: %s", httpResp.StatusCode, string(bodyBytes)))
        return
    }

    // Parse response
    var projectResp ProjectResponse
    if err := json.Unmarshal(bodyBytes, &projectResp); err != nil {
        resp.Diagnostics.AddError("Parse Error", err.Error())
        return
    }

    plan.ID = types.StringValue(projectResp.Result.ID)
    resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
```

### After (with helpers)
```go
func (r *ProjectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
    var plan ProjectResourceModel
    resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
    if resp.Diagnostics.HasError() {
        return
    }

    // Resolve cloud name if needed
    cloudID := plan.CloudID.ValueString()
    if plan.CloudID.IsNull() && !plan.CloudName.IsNull() {
        resolvedID, err := ResolveCloudNameToID(ctx, r.client, plan.CloudName.ValueString())
        if err != nil {
            AddAPIError(&resp.Diagnostics, "resolve cloud name", err)
            return
        }
        cloudID = resolvedID
    }

    // Build request
    createReq := CreateProjectRequest{
        Name:          plan.Name.ValueString(),
        ParentCloudID: cloudID,
    }

    reqBody, err := MarshalRequestBody(createReq)
    if err != nil {
        AddJSONError(&resp.Diagnostics, "marshal", "project request", err)
        return
    }

    // Create project
    projectResp, err := DoRequestAndParse[ProjectResponse](
        ctx, r.client, "POST", "/api/v2/projects", reqBody, http.StatusCreated,
    )
    if err != nil {
        AddAPIError(&resp.Diagnostics, "create project", err)
        return
    }

    plan.ID = types.StringValue(projectResp.Result.ID)
    resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
```

**Lines reduced:** ~65 → ~35 (46% reduction)

## Impact Summary

### Code Reduction
- **API request handling**: 28 lines → 7 lines (75% reduction)
- **Pagination handling**: 40 lines → 9 lines (77% reduction)
- **Cloud name resolution**: Eliminated ~455 lines of duplication across 7 files
- **Typical Create method**: 65 lines → 35 lines (46% reduction)

### Benefits
1. **Reduced duplication**: Eliminates 500-800 lines of repeated code
2. **Standardized error handling**: Consistent error messages across the provider
3. **Easier maintenance**: Bug fixes in one place benefit all resources
4. **Simpler resource development**: Less boilerplate for new resources
5. **Better testability**: Helpers have comprehensive unit tests
6. **Improved readability**: Intent is clearer with high-level helper functions

### Test Coverage
All helper functions include comprehensive unit tests:
- `api_helpers_test.go` - 5 test functions, 13 test cases
- `error_helpers_test.go` - 7 test functions, 14 test cases
- `cloud_helpers_test.go` - 1 test function, 7 test cases

All tests pass: ✅

## Migration Guide

When updating existing resources to use these helpers:

1. **Identify the pattern** - Look for repeated request/response/error handling code
2. **Choose the appropriate helper** - Match the pattern to the helper function
3. **Replace the code** - Substitute with the helper call
4. **Simplify error handling** - Use AddAPIError/AddHTTPError for diagnostics
5. **Test thoroughly** - Run unit and acceptance tests

The helpers are backward compatible and don't require changes to existing working code, but using them will make future development and maintenance easier.
