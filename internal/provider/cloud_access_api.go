package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The API layer for anyscale_cloud_access. Reading one cloud's member list is a
// JOIN across two endpoints, not one call, and which endpoint owns which field
// is the load-bearing part:
//
//   - POST /clouds/{id}/collaborators/users/search supplies IDENTITY only -
//     email, identity_id, user_id. Its permission_level field is LOSSY and is
//     never read here: the same endpoint was already proven to misreport role
//     data on the predecessor resource, where a workload_operator read back as a
//     plain readonly. Membership yes, role never.
//   - GET /clouds/{id}/collaborators/roles supplies the ROLES - base_roles and
//     deny_roles, keyed by user_id, with no email on the response.
//
// Neither endpoint alone can produce the resource's member map, which is keyed
// by email and valued by role. The join key is user_id.
//
// One consequence worth knowing before trusting a read: organization admins are
// invisible to the search endpoint, which filters them out server-side. So the
// member list this returns is "collaborators", not "everyone with access", and
// an admin's access can neither be seen nor revoked through this resource.

// cloudCollaboratorSearchResult is one entry of POST
// /clouds/{cloud_id}/collaborators/users/search.
//
// PermissionLevel is deliberately absent from this struct rather than parsed
// and ignored: the endpoint reports it, it is wrong, and a field sitting in the
// struct is an invitation for a later change to start using it.
type cloudCollaboratorSearchResult struct {
	// ID is the identity_id - the id the collaborator DELETE takes, and not the
	// same thing as the user_id the roles endpoints take.
	ID    string `json:"id"`
	Value struct {
		ID    string `json:"id"` // user_id
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"value"`
}

type cloudCollaboratorSearchResponse struct {
	Results  []cloudCollaboratorSearchResult `json:"results"`
	Metadata struct {
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// cloudUserRolesResult is one entry of GET /clouds/{cloud_id}/collaborators/roles.
//
// BaseRoles is a LIST even though this provider only ever writes exactly one.
// A directory-sync interaction outside Terraform can produce more than one, and
// callers must handle that rather than indexing element 0.
type cloudUserRolesResult struct {
	UserID    string   `json:"user_id"`
	BaseRoles []string `json:"base_roles"`
	DenyRoles []string `json:"deny_roles"`
}

type cloudUserRolesListResponse struct {
	Results  []cloudUserRolesResult `json:"results"`
	Metadata struct {
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// cloudAccessRemoteMember is one member of a cloud as the API reports them, after the
// identity/roles join.
type cloudAccessRemoteMember struct {
	Email      string
	IdentityID string
	UserID     string
	BaseRoles  []string
	DenyRoles  []string
}

// searchCloudCollaborators pages through POST
// /clouds/{cloud_id}/collaborators/users/search.
//
// Pagination is a SPLIT TRANSPORT and getting it wrong fails silently rather
// than loudly: count and paging_token go in the URL query string while the
// filter goes in the JSON body. Sending the pagination in the body instead
// returns a valid first page and no error, so the failure mode is a truncated
// member list - which, for an authoritative resource, means quietly revoking
// everyone past the page boundary. The empty JSON body matches every
// collaborator (the query model's only field is optional).
//
// This mirrors searchComputeTemplatesPaged rather than using PaginatedRequest,
// which only issues GETs.
func searchCloudCollaborators(ctx context.Context, client *Client, cloudID string) ([]cloudCollaboratorSearchResult, error) {
	var all []cloudCollaboratorSearchResult
	pagingToken := ""

	for {
		body, err := MarshalRequestBody(map[string]interface{}{})
		if err != nil {
			return nil, err
		}

		query := url.Values{}
		// 50 is the real endpoint's enforced maximum - confirmed live: count=100
		// 422s outright ("query.count: ensure this value is less than or equal to
		// 50"), on the very first request, unconditionally. This was never caught
		// by any test because the mock echoes back whatever count is sent instead
		// of enforcing the real cap. Must match listCloudRoles's count=50 below.
		query.Set("count", "50")
		if pagingToken != "" {
			query.Set("paging_token", pagingToken)
		}
		path := fmt.Sprintf("/api/v2/clouds/%s/collaborators/users/search?%s", cloudID, query.Encode())

		respBody, err := DoRequestRaw(ctx, client, "POST", path, body, http.StatusOK)
		if err != nil {
			return nil, err
		}

		var page cloudCollaboratorSearchResponse
		if err := json.Unmarshal(respBody, &page); err != nil {
			return nil, fmt.Errorf("failed to parse cloud collaborator search response: %w", err)
		}
		all = append(all, page.Results...)

		if page.Metadata.NextPagingToken == nil || *page.Metadata.NextPagingToken == "" {
			return all, nil
		}
		pagingToken = *page.Metadata.NextPagingToken
	}
}

// listCloudRoles pages through GET /clouds/{cloud_id}/collaborators/roles.
func listCloudRoles(ctx context.Context, client *Client, cloudID string) ([]cloudUserRolesResult, error) {
	return PaginatedRequest(
		ctx, client, fmt.Sprintf("/api/v2/clouds/%s/collaborators/roles", cloudID), url.Values{"count": []string{"50"}},
		func(body []byte) ([]cloudUserRolesResult, *string, error) {
			var listResp cloudUserRolesListResponse
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, fmt.Errorf("failed to parse cloud roles response: %w", err)
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
}

// listCloudAccessMembers joins the identity search onto the roles list and
// returns one entry per member, in the API's own order.
//
// A collaborator the roles endpoint says nothing about is returned with no
// roles rather than dropped. Dropping it would make an existing member
// invisible to an authoritative resource, which is the one failure mode here
// that silently loses someone's access instead of reporting it.
//
// A roles entry with no matching collaborator is dropped, because there is no
// email to key it by and email is this resource's identifier. That is not
// merely a gap in the join: an organization admin holds cloud roles while being
// filtered out of the search endpoint, so this case is expected rather than
// anomalous, and it is the mechanism by which admins are invisible here.
func listCloudAccessMembers(ctx context.Context, client *Client, cloudID string) ([]cloudAccessRemoteMember, error) {
	collaborators, err := searchCloudCollaborators(ctx, client, cloudID)
	if err != nil {
		return nil, err
	}
	roles, err := listCloudRoles(ctx, client, cloudID)
	if err != nil {
		return nil, err
	}

	rolesByUserID := make(map[string]cloudUserRolesResult, len(roles))
	for _, entry := range roles {
		rolesByUserID[entry.UserID] = entry
	}

	members := make([]cloudAccessRemoteMember, 0, len(collaborators))
	for _, c := range collaborators {
		if strings.TrimSpace(c.Value.Email) == "" {
			// Nothing can be done with an entry that has no email: it cannot be
			// keyed, matched against config, or revoked by this resource. Skipping it
			// silently would understate the member list, so this is reported as an
			// error by the caller rather than absorbed here.
			return nil, fmt.Errorf("cloud %s has a collaborator with no email address (identity %s); this resource keys members by email and cannot represent it", cloudID, c.ID)
		}
		m := cloudAccessRemoteMember{
			Email:      c.Value.Email,
			IdentityID: c.ID,
			UserID:     c.Value.ID,
		}
		if entry, ok := rolesByUserID[c.Value.ID]; ok {
			m.BaseRoles = entry.BaseRoles
			m.DenyRoles = entry.DenyRoles
		}
		members = append(members, m)
	}
	return members, nil
}

// CreateCloudCollaboratorRequest is the body of the legacy
// POST /clouds/{cloud_id}/collaborators/users call used to establish
// membership before a role can be written.
type CreateCloudCollaboratorRequest struct {
	Email           string `json:"email"`
	PermissionLevel string `json:"permission_level"`
}

// SetCloudRolesRequest is the body of
// PUT /clouds/{cloud_id}/collaborators/users/{user_id}/roles.
//
// DenyRoles is always sent, even when empty: the wire field is required, and
// the call is an authoritative SET over the pair rather than an addition to it.
type SetCloudRolesRequest struct {
	BaseRole  string   `json:"base_role"`
	DenyRoles []string `json:"deny_roles"`
}

// cloudAutoAddUserEnabled reports whether the cloud auto-enrolls every
// organization member.
//
// Called at the entry of any operation that would REVOKE, on every apply, and
// never cached from an earlier one - anyscale_cloud in the same configuration
// can flip the flag, so a check made at create time can be invalidated by a
// sibling resource. It is deliberately not consulted from ModifyPlan either;
// plan must stay cheap and free of side effects.
func cloudAutoAddUserEnabled(ctx context.Context, client *Client, cloudID string) (bool, error) {
	cloudResp, err := DoRequestAndParse[CloudResponse](
		ctx, client, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil, http.StatusOK,
	)
	if err != nil {
		return false, err
	}
	// DoRequestAndParse returns a nil result rather than an error for a 404 when
	// that status is in its accepted list; it is not here, but the nil check is
	// kept so that a future change to the accepted list cannot turn "cloud gone"
	// into "auto_add_user is false", which would let a revoke pass proceed on a
	// guard that never actually answered.
	if cloudResp == nil {
		return false, fmt.Errorf("%w: cloud %s", ErrNotFound, cloudID)
	}
	return cloudResp.Result.AutoAddUser, nil
}

// listProjectCollaborators pages through GET
// /projects/{project_id}/collaborators/users.
//
// One call per project, because there is no endpoint that lists project
// collaborators for a whole cloud. That cost is what makes this resource's
// project-role authority SCOPED: it reads the projects the configuration names
// rather than every project under the cloud.
//
// Cheaper than it first looks - the response is a list of collaborators per
// project, so the cost is one request per project, never one per
// (project, member).
func listProjectCollaborators(ctx context.Context, client *Client, projectID string) ([]ProjectCollaboratorResult, error) {
	return PaginatedRequest(
		ctx, client, fmt.Sprintf("/api/v2/projects/%s/collaborators/users", projectID), nil,
		func(body []byte) ([]ProjectCollaboratorResult, *string, error) {
			var listResp ProjectCollaboratorListResponse
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, fmt.Errorf("failed to parse project collaborators response: %w", err)
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
}

// cloudAccessProjectRoles maps each requested project to the roles held on it,
// keyed by the collaborator's user_id.
//
// A project that cannot be read is reported in the error rather than silently
// omitted: an unreadable project's roles would otherwise look like roles that no
// longer exist, which for an authoritative resource reads as "revoke these".
func cloudAccessProjectRoles(ctx context.Context, client *Client, projectIDs []string) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string, len(projectIDs))
	for _, projectID := range projectIDs {
		collaborators, err := listProjectCollaborators(ctx, client, projectID)
		if err != nil {
			return nil, fmt.Errorf("reading collaborators of project %s: %w", projectID, err)
		}
		byUserID := make(map[string]string, len(collaborators))
		for _, c := range collaborators {
			if c.Value.ID == "" {
				continue
			}
			byUserID[c.Value.ID] = c.PermissionLevel
		}
		out[projectID] = byUserID
	}
	return out, nil
}

// resourcePolicyBinding is one entry of GET /api/v2/policy/cloud/{cloud_id}'s
// bindings list (product backend resource_policies_service.py:751-780). Every
// principal representable at this path is a user-group ID: the backend's own
// write-side validator, _validate_user_groups_exist, resolves every entry in
// principals[] against the user_groups table unconditionally, with no other
// principal shape accepted. So a non-empty bindings list IS a group-principal
// binding by construction - there is no user-principal variant it could
// instead be, and no need to inspect principal shape further (J.20,
// Amendment 2).
type resourcePolicyBinding struct {
	RoleName   string   `json:"role_name"`
	Principals []string `json:"principals"`
}

type resourcePolicyResult struct {
	Bindings []resourcePolicyBinding `json:"bindings"`
}

type resourcePolicyResponse struct {
	Result resourcePolicyResult `json:"result"`
}

// cloudAccessGroupPolicyStatus is the tri-state result of checking whether a
// cloud carries a group policy binding (J.20). Kept tri-state rather than a
// bool: Present and Absent are both CONFIRMED, and Undetermined - this
// resource could not check at all - must never be conflated with either one.
type cloudAccessGroupPolicyStatus int

const (
	cloudAccessGroupPolicyAbsent cloudAccessGroupPolicyStatus = iota
	cloudAccessGroupPolicyPresent
	cloudAccessGroupPolicyUndetermined
)

// cloudAccessGroupPolicyBinding reports whether cloudID carries a group
// policy binding through the (BETA) Policy API.
//
// Present = 200 with a non-empty bindings list.
//
// Absent = 404 ("No policy found for resource"), or 200 with an empty
// bindings list. 404 is the ORDINARY response for the overwhelming majority
// of clouds today, not evidence the API is unavailable: the backend's only
// feature flag on this surface, FLAG_ENABLE_NEW_POLICY_API_ACCESS, gates the
// WRITE path (update_policy_for_resource) only - the GET this calls has no
// flag gate at all, and a 404 means exactly "no resource_permission row
// exists for this cloud." Treating 404 as Undetermined would warn on nearly
// every apply forever, the same always-on-alarm J.2 already rejects for the
// caller-exclusion case.
//
// Undetermined = a 403 (this GET requires the same update-tier, org-admin
// permission as the write path, and the token running Terraform is usually a
// cloud collaborator or owner rather than an org admin - see J.2's identical
// observation about the member-search endpoint) or any other unexpected
// status. Every Undetermined result carries a non-nil error; every Present or
// Absent result carries a nil one, so a caller can dispatch on the status
// alone. See J.20 in the design record for the full rationale.
func cloudAccessGroupPolicyBinding(ctx context.Context, client *Client, cloudID string) (cloudAccessGroupPolicyStatus, error) {
	resp, err := DoRequestAndParse[resourcePolicyResponse](
		ctx, client, "GET", fmt.Sprintf("/api/v2/policy/cloud/%s", cloudID), nil, http.StatusOK,
	)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return cloudAccessGroupPolicyAbsent, nil
		}
		return cloudAccessGroupPolicyUndetermined, err
	}
	if resp == nil || len(resp.Result.Bindings) == 0 {
		return cloudAccessGroupPolicyAbsent, nil
	}
	return cloudAccessGroupPolicyPresent, nil
}

// cloudAccessLikelyOrgAdmin reports whether email is PROBABLY an organization
// admin, for J.22's best-effort ModifyPlan warning - see the doc comment on
// refuseDeclaringOrgAdmins for why this is a proxy rather than the backend's
// actual signal (live ORG_OWNER managed-group membership, which no provider-
// facing API exposes) and why base_role from the SINGULAR
// organization-collaborator GET is the proxy that goes stale in the same
// direction as that signal, rather than permission_level from the list
// endpoint.
//
// One call per invocation (resolveIdentityForEmail plus one singular GET),
// deliberately not a full organization listing: a cloud's declared member set
// is almost always far smaller than the whole organization, so this is
// O(declared) at the call site in refuseDeclaringOrgAdmins rather than
// O(organization) per plan.
func cloudAccessLikelyOrgAdmin(ctx context.Context, client *Client, email string) (bool, error) {
	_, userID, err := resolveIdentityForEmail(ctx, client, email)
	if err != nil {
		return false, err
	}
	singular, err := DoRequestAndParse[OrganizationCollaboratorSingularResponse](
		ctx, client, "GET", fmt.Sprintf("/api/v2/organization_collaborators/%s", userID), nil, http.StatusOK,
	)
	if err != nil {
		return false, err
	}
	return singular.Result.BaseRole == "owner", nil
}
