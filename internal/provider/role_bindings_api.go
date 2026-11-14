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

// roleBindingsBetaBanner opens the MarkdownDescription of the role binding
// resource and the role data source. The policy it links to is written once,
// in the RBAC guide.
const roleBindingsBetaBanner = "~> **Beta.** Role bindings are enabled per organization by Anyscale while customers migrate; contact [support@anyscale.com](mailto:support@anyscale.com) to enable them. Beta resources may change incompatibly; every breaking change is listed in the changelog. See the [RBAC guide](../guides/rbac.md#role-bindings-beta).\n\n"

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
	detail := "This organization does not have role bindings enabled. They are in Beta and enabled per organization; contact support@anyscale.com for access."
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

// roleBindingDuplicateSubstring is the detail substring of create's 409 when
// the principal already holds the role on the resource. The full detail is
// "The principal already holds this role on this resource."
// (backend/server/api/product/services/role_bindings_service.py:891-895 at
// product 3b1f86a). It is the only 409 that means the binding exists; create
// also answers 409 for an unconverted organization, an archived role, a
// lockout and a concurrent revoke.
const roleBindingDuplicateSubstring = "already holds this role"

// roleBindingArchivedRoleSubstring is the detail substring of create's 409 for
// an archived role. The full detail is "This role is archived and cannot be
// newly assigned." (role_bindings_service.py:1166-1170 at product 3b1f86a).
const roleBindingArchivedRoleSubstring = "is archived and cannot be newly assigned"

// The organization admin group's role bindings are platform-owned, and the
// API refuses writes that would change them, with one of two 409 details -
// matched by the exact substrings below rather than a shared prefix: other
// admin-group refusals exist that are not role-binding writes ("The
// organization admin group must keep at least one member.",
// iam_migration_service.py:3375; "...cannot be renamed.",
// converted_organizations.py:253) and must not classify as these.
//
// Removal, on role-grant Delete and on a policy apply whose roles omit the
// binding: "The organization admin group's role bindings cannot be removed."
// (backend/server/api/base/resources/converted_organizations.py:501 at
// product 5c6ce328ab8). Adding a role to the group, on a create whose
// principal names it: "The organization admin group holds only the roles the
// platform binds it to." (backend/server/api/product/services/
// role_bindings_service.py:~1784 at product 5c6ce328ab8).
const (
	roleBindingAdminGroupRemovalSubstring   = "admin group's role bindings cannot be removed"
	roleBindingAdminGroupExtraRoleSubstring = "holds only the roles the platform binds it to"
)

// isRoleBindingAdminGroupRemovalRefusal reports the 409 for a write that
// would remove one of the organization admin group's bindings.
func isRoleBindingAdminGroupRemovalRefusal(err error) bool {
	return isStatusWithSubstring(err, http.StatusConflict, roleBindingAdminGroupRemovalSubstring)
}

// isRoleBindingAdminGroupExtraRoleRefusal reports the 409 for a write that
// tries to add a role to the organization admin group.
func isRoleBindingAdminGroupExtraRoleRefusal(err error) bool {
	return isStatusWithSubstring(err, http.StatusConflict, roleBindingAdminGroupExtraRoleSubstring)
}

// addRoleBindingAdminGroupExtraRoleError reports a write the API refused
// because it names the organization admin group as the principal of a new
// binding. The platform binds that group's roles itself, so the remedy is to
// remove the group from the configuration. outcome, when set, applies to a
// policy apply.
func addRoleBindingAdminGroupExtraRoleError(diags *diag.Diagnostics, err error, configWhat string, outcome string) {
	detail := extractAPIErrorDetail(err) +
		" The platform binds the organization admin group's roles itself, so a write cannot add to them. Remove the group from the " + configWhat + "."
	if outcome != "" {
		detail += " " + outcome
	}
	diags.AddError("Organization Admin Group Is Managed by Anyscale", detail)
}

// addRoleBindingAdminGroupGrantDeleteError reports a role-grant destroy the
// API refused: the binding is one of the organization admin group's, so it
// stays as it is. The destroy itself was triggered by removing the grant
// from the configuration, so removing it again cannot end the loop - the way
// to stop managing the binding is terraform state rm.
func addRoleBindingAdminGroupGrantDeleteError(diags *diag.Diagnostics, err error) {
	detail := extractAPIErrorDetail(err) +
		" The organization admin group's role bindings are platform-owned and cannot be revoked. The binding was not revoked and stays in Terraform state; to stop managing it, run `terraform state rm` on its address."
	diags.AddError("Organization Admin Group Is Managed by Anyscale", detail)
}

// addRoleBindingAdminGroupPolicyRemovalError reports a policy apply the API
// refused: the authoritative roles list omits one of the organization admin
// group's bindings, so the apply would revoke it. The remedy is the opposite
// of omission - name the binding in roles to keep it. outcome, when set,
// applies to the policy operation.
func addRoleBindingAdminGroupPolicyRemovalError(diags *diag.Diagnostics, err error, outcome string) {
	detail := extractAPIErrorDetail(err) +
		" The apply would revoke an organization admin group binding because roles omits it; name the binding in roles to keep it."
	if outcome != "" {
		detail += " " + outcome
	}
	diags.AddError("Organization Admin Group Is Managed by Anyscale", detail)
}

// roleBindingSelfGrantSubstring is the detail substring of create's 403 when
// the caller names itself as the user principal. The full detail is "You
// cannot assign a role to yourself directly. Assign it to a user group you
// belong to." (backend/server/api/product/services/role_bindings_service.py:1129-1135
// at product 3b1f86a).
const roleBindingSelfGrantSubstring = "cannot assign a role to yourself directly"

// roleBindingSelfRevokeSubstring is the detail substring of delete's 403 when
// the caller revokes its own direct binding. The full detail is "You cannot
// revoke your own role binding." (role_bindings_service.py:1151-1157 at
// product 3b1f86a).
const roleBindingSelfRevokeSubstring = "cannot revoke your own role binding"

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

// listRoleBindingsForPrincipal returns every binding the principal holds
// directly on one resource. The empty list lets Read tell a revoked binding
// from a disabled feature.
func listRoleBindingsForPrincipal(ctx context.Context, client *Client, resourceType, resourceID, principalType, principalID string) ([]roleBindingSummary, error) {
	path := fmt.Sprintf("%s/%s/%s/principals/%s/%s", roleBindingsBasePath,
		url.PathEscape(resourceType), url.PathEscape(resourceID), url.PathEscape(principalType), url.PathEscape(principalID))
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

// findRoleBindingForPrincipal returns the principal's binding with this ID on
// the resource, or nil when the principal no longer holds it.
func findRoleBindingForPrincipal(ctx context.Context, client *Client, resourceType, resourceID, principalType, principalID, roleBindingID string) (*roleBindingSummary, error) {
	bindings, err := listRoleBindingsForPrincipal(ctx, client, resourceType, resourceID, principalType, principalID)
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

// roleBindingOriginImperative is the origin of a binding granted directly, as
// opposed to one a role-binding policy states ("declarative").
const roleBindingOriginImperative = "imperative"

// matchRoleBinding picks, from a principal's bindings on one resource, the
// binding with this ID, else the one granting roleID on that same resource.
// byID reports which matched. A policy apply that revokes and re-grants a
// binding gives the same grant a new ID, and the API keeps at most one live
// binding per principal, role and resource, so a same-grant match is the
// binding the ID used to name. The scope comparison is defensive: the
// resource-scoped listing does not return bindings held on other resources.
func matchRoleBinding(bindings []roleBindingSummary, roleBindingID, roleID, resourceType, resourceID string) (match *roleBindingSummary, byID bool) {
	for i := range bindings {
		if bindings[i].ID == roleBindingID {
			return &bindings[i], true
		}
	}
	for i := range bindings {
		b := &bindings[i]
		if b.RoleID == roleID && b.ResourceType == resourceType && b.ResourceID == resourceID {
			return b, false
		}
	}
	return nil, false
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
