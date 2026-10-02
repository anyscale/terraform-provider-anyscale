package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// roleBindingsAlphaBanner opens the MarkdownDescription of the role binding
// resource and the role data source. The policy it links to is written once,
// in the RBAC guide.
const roleBindingsAlphaBanner = "~> **Alpha.** Role bindings are an Alpha feature behind a feature flag; contact [support@anyscale.com](mailto:support@anyscale.com) for access. The Anyscale API and this provider's schema for them may change drastically before Beta, including breaking changes in a minor release. See the [RBAC guide](../guides/rbac.md#role-bindings-alpha).\n\n"

const (
	roleBindingsBasePath = "/api/v2/role_bindings"
	rolesBasePath        = "/api/v2/roles"
)

// Principal and resource types as the role bindings API spells them.
const (
	roleBindingPrincipalUser      = "user"
	roleBindingPrincipalUserGroup = "user_group"

	roleBindingResourceOrganization = "organization"
	roleBindingResourceCloud        = "cloud"
	roleBindingResourceProject      = "project"
)

// roleBindingResult is the RoleBinding model returned by create and by
// GET /role_bindings/{id}.
type roleBindingResult struct {
	ID            string  `json:"id"`
	PrincipalType string  `json:"principal_type"`
	PrincipalID   string  `json:"principal_id"`
	RoleID        string  `json:"role_id"`
	ResourceType  string  `json:"resource_type"`
	ResourceID    string  `json:"resource_id"`
	CreatedBy     *string `json:"created_by"`
	CreatedAt     string  `json:"created_at"`
	Origin        *string `json:"origin"`
}

type roleBindingResponse struct {
	Result roleBindingResult `json:"result"`
}

// roleBindingSummary is a binding as listed under the principal that holds
// it; the principal is the one the listing was asked for.
type roleBindingSummary struct {
	ID           string  `json:"id"`
	RoleID       string  `json:"role_id"`
	ResourceType string  `json:"resource_type"`
	ResourceID   string  `json:"resource_id"`
	Origin       *string `json:"origin"`
}

type pagedListMetadata struct {
	NextPagingToken *string `json:"next_paging_token"`
}

type roleBindingSummaryListResponse struct {
	Results  []roleBindingSummary `json:"results"`
	Metadata pagedListMetadata    `json:"metadata"`
}

type createRoleBindingRequest struct {
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	RoleID        string `json:"role_id"`
	ResourceType  string `json:"resource_type"`
	ResourceID    string `json:"resource_id"`
}

// roleResult is the RoleMetadata model returned by the roles listing.
type roleResult struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	BuiltIn     bool    `json:"built_in"`
	ArchivedAt  *string `json:"archived_at"`
}

type roleListResponse struct {
	Results  []roleResult      `json:"results"`
	Metadata pagedListMetadata `json:"metadata"`
}

// isRoleBindingsDisabled reports a 404, which every roles and role_bindings
// route returns when the feature is off. Create, Read and Delete can also get
// a real 404, so they call roleBindingsEnabled first; never read a bare 404 as
// gone.
func isRoleBindingsDisabled(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// addRoleBindingsDisabledError reports a 404 from the role bindings or roles
// routes. Callers return without touching state; outcome, when set, says what
// that means for the operation and is appended to the detail.
func addRoleBindingsDisabledError(diags *diag.Diagnostics, outcome string) {
	detail := "This organization does not have role bindings enabled. They are in Alpha behind a feature flag; contact support@anyscale.com for access."
	if outcome != "" {
		detail += " " + outcome
	}
	diags.AddError("Role bindings are not enabled", detail)
}

// Outcomes for addRoleBindingsDisabledError.
const (
	roleBindingsDisabledReadOutcome   = "The binding was kept in Terraform state unchanged."
	roleBindingsDisabledDeleteOutcome = "The binding was not revoked and stays in Terraform state; to stop managing it, run `terraform state rm` on its address."
)

// isForbidden reports whether err is an HTTP 403.
func isForbidden(err error) bool {
	var statusErr *UnexpectedStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusForbidden
}

func roleBindingPath(roleBindingID string) string {
	return roleBindingsBasePath + "/" + url.PathEscape(roleBindingID)
}

func createRoleBinding(ctx context.Context, client *Client, req createRoleBindingRequest) (*roleBindingResult, error) {
	body, err := MarshalRequestBody(req)
	if err != nil {
		return nil, err
	}
	resp, err := DoRequestAndParse[roleBindingResponse](ctx, client, "POST", roleBindingsBasePath+"/", body, http.StatusCreated, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

// getRoleBinding fetches one binding by ID. A revoked ID answers 403, not 404,
// so only import uses it.
func getRoleBinding(ctx context.Context, client *Client, roleBindingID string) (*roleBindingResult, error) {
	resp, err := DoRequestAndParse[roleBindingResponse](ctx, client, "GET", roleBindingPath(roleBindingID), nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

func deleteRoleBinding(ctx context.Context, client *Client, roleBindingID string) error {
	_, err := DoRequestRaw(ctx, client, "DELETE", roleBindingPath(roleBindingID), nil, http.StatusNoContent, http.StatusOK)
	return err
}

// listRoleBindingsForGroup returns every binding the group holds directly on
// one resource. The empty list lets Read tell a revoked binding from a
// disabled feature.
func listRoleBindingsForGroup(ctx context.Context, client *Client, resourceType, resourceID, groupID string) ([]roleBindingSummary, error) {
	path := fmt.Sprintf("%s/%s/%s/principals/%s/%s", roleBindingsBasePath,
		url.PathEscape(resourceType), url.PathEscape(resourceID), roleBindingPrincipalUserGroup, url.PathEscape(groupID))
	return PaginatedRequest(
		ctx, client, path, url.Values{"count": []string{"50"}},
		func(body []byte) ([]roleBindingSummary, *string, error) {
			var listResp roleBindingSummaryListResponse
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, fmt.Errorf("error parsing response: %w", err)
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
}

// findRoleBindingForGroup returns the group's binding with this ID on the
// resource, or nil when the group no longer holds it.
func findRoleBindingForGroup(ctx context.Context, client *Client, resourceType, resourceID, groupID, roleBindingID string) (*roleBindingSummary, error) {
	bindings, err := listRoleBindingsForGroup(ctx, client, resourceType, resourceID, groupID)
	if err != nil {
		return nil, err
	}
	for i := range bindings {
		if bindings[i].ID == roleBindingID {
			return &bindings[i], nil
		}
	}
	return nil, nil
}

// listRolesByName returns the assignable roles whose name contains name: the
// organization's own and the built-ins. The listing's name filter is a
// substring match, so callers pick the exact match. Archived roles are not
// listed, since they cannot be newly assigned.
func listRolesByName(ctx context.Context, client *Client, name string) ([]roleResult, error) {
	params := url.Values{
		"name":             []string{name},
		"include_built_in": []string{"true"},
		"count":            []string{"50"},
	}
	return PaginatedRequest(
		ctx, client, rolesBasePath+"/", params,
		func(body []byte) ([]roleResult, *string, error) {
			var listResp roleListResponse
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, fmt.Errorf("error parsing response: %w", err)
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
}

// roleBindingsEnabled reports whether the organization has the role bindings
// feature, by listing one role: the roles listing is behind the same flag and
// has no 404 of its own. It is only called after a 404 from a route that can
// also answer 404 for a real reason (create rejects an unknown group or role
// with 404), so a status alone settles which 404 that was.
func roleBindingsEnabled(ctx context.Context, client *Client) (bool, error) {
	_, err := DoRequestRaw(ctx, client, "GET", rolesBasePath+"/?count=1", nil, http.StatusOK)
	if err == nil {
		return true, nil
	}
	if isRoleBindingsDisabled(err) {
		return false, nil
	}
	return false, err
}

// addRoleBindingsProbeFailedError reports a 404 that roleBindingsEnabled could
// not classify.
func addRoleBindingsProbeFailedError(diags *diag.Diagnostics, operation string, notFoundErr, probeErr error) {
	diags.AddError("Could Not Classify Role Binding 404",
		fmt.Sprintf("The Anyscale API returned 404 Not Found to %s (%s), and checking whether role bindings are enabled also failed: %s. "+
			"Terraform state was not changed.", operation, extractAPIErrorDetail(notFoundErr), probeErr.Error()))
}
