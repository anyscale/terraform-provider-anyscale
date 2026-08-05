package provider

import (
	"context"
	"encoding/json"
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
		query.Set("count", "100")
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
