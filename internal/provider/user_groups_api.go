package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

// userGroupsAlphaBanner opens the MarkdownDescription of every user group
// resource and data source. The policy it links to is written once, in the
// RBAC guide, rather than repeated per attribute.
const userGroupsAlphaBanner = "~> **Alpha.** User groups are an Alpha feature. The Anyscale API and this provider's schema for them may change drastically before Beta, including breaking changes in a minor release. See the [RBAC guide](../guides/rbac.md#user-groups-alpha).\n\n"

const userGroupsBasePath = "/api/v2/user_groups"

// userGroupSourceSCIM marks a group synced from an identity provider
// directory. The backend owns its name and membership: PATCH and the member
// routes reject it with 409, but DELETE does not, so the provider refuses to
// manage such a group at all rather than relying on the backend to stop it.
const (
	userGroupSourceUser = "user"
	userGroupSourceSCIM = "scim"
)

// userGroupNotFoundDetail is the prefix of the backend's 404 detail for a
// missing group ("User group with id '...' not found..."). The member routes
// also answer 404 when a user_id is not an org member ("User IDs not
// found: ..."), so a bare 404 does not mean the group is gone. Only this
// detail does.
const userGroupNotFoundDetail = "User group with id"

// userGroupResult is the UserGroup model returned by the user_groups routes.
// organization_permissions is populated only by GET /{id}; the list, create
// and rename responses leave it null.
type userGroupResult struct {
	ID                      string                         `json:"id"`
	Name                    string                         `json:"name"`
	OrgID                   string                         `json:"org_id"`
	CreatedAt               string                         `json:"created_at"`
	UpdatedAt               string                         `json:"updated_at"`
	DeletedAt               *string                        `json:"deleted_at"`
	Source                  *string                        `json:"source"`
	OrganizationPermissions *userGroupOrganizationPermsAPI `json:"organization_permissions"`
}

type userGroupOrganizationPermsAPI struct {
	BaseRole        *string  `json:"base_role"`
	AdditionalRoles []string `json:"additional_roles"`
}

type userGroupResponse struct {
	Result userGroupResult `json:"result"`
}

type userGroupListResponse struct {
	Results  []userGroupResult `json:"results"`
	Metadata struct {
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

type userGroupMemberAPI struct {
	UserID    string `json:"user_id"`
	UserEmail string `json:"user_email"`
	UserName  string `json:"user_name"`
}

type userGroupWithMembersAPI struct {
	GroupID   string               `json:"group_id"`
	GroupName string               `json:"group_name"`
	Members   []userGroupMemberAPI `json:"members"`
}

type userGroupMembershipsResponse struct {
	Result struct {
		Groups []userGroupWithMembersAPI `json:"groups"`
	} `json:"result"`
}

type userGroupNameRequest struct {
	Name string `json:"name"`
}

type userGroupMembersRequest struct {
	UserIDs []string `json:"user_ids"`
}

// isUserGroupSCIM reports whether the group is directory-synced. A null
// source (groups created before the backend recorded a creator) is not.
func isUserGroupSCIM(g *userGroupResult) bool {
	return g.Source != nil && *g.Source == userGroupSourceSCIM
}

// userGroupSCIMDetail is the error detail for an attempt to manage a
// directory-synced group.
func userGroupSCIMDetail(groupID string) string {
	return fmt.Sprintf("User group %q is synced from your identity provider, which owns its name and membership. "+
		"Directory-synced groups cannot be managed by Terraform; read them with the anyscale_user_group data source instead.", groupID)
}

// isUserGroupNotFound reports whether err is a 404 whose detail says the
// group itself does not exist, as opposed to any other 404 (an unknown member
// user ID, or a route the API does not serve).
func isUserGroupNotFound(err error) bool {
	if !errors.Is(err, ErrNotFound) {
		return false
	}
	return strings.HasPrefix(extractAPIErrorDetail(err), userGroupNotFoundDetail)
}

func userGroupPath(groupID string) string {
	return userGroupsBasePath + "/" + url.PathEscape(groupID)
}

// getUserGroup fetches one group. A missing group is returned as an error
// for which isUserGroupNotFound is true.
func getUserGroup(ctx context.Context, client *Client, groupID string) (*userGroupResult, error) {
	resp, err := DoRequestAndParse[userGroupResponse](ctx, client, "GET", userGroupPath(groupID), nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

// listUserGroups returns every live group in the token's organization.
//
// The backend pages by offset over created_at desc, so a group created
// between two page requests shifts later rows forward and one can appear on
// two pages. Results are de-duplicated by ID.
func listUserGroups(ctx context.Context, client *Client) ([]userGroupResult, error) {
	groups, err := PaginatedRequest(
		ctx, client, userGroupsBasePath+"/", url.Values{"count": []string{"50"}},
		func(body []byte) ([]userGroupResult, *string, error) {
			var listResp userGroupListResponse
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, fmt.Errorf("error parsing response: %w", err)
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(groups))
	unique := make([]userGroupResult, 0, len(groups))
	for _, g := range groups {
		if _, ok := seen[g.ID]; ok {
			continue
		}
		seen[g.ID] = struct{}{}
		unique = append(unique, g)
	}
	return unique, nil
}

// findUserGroupByName returns the live group with exactly this name, or nil.
// Names are unique among an organization's live groups, so there is at most
// one.
func findUserGroupByName(ctx context.Context, client *Client, name string) (*userGroupResult, error) {
	groups, err := listUserGroups(ctx, client)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].Name == name {
			return &groups[i], nil
		}
	}
	return nil, nil
}

func createUserGroup(ctx context.Context, client *Client, name string) (*userGroupResult, error) {
	body, err := MarshalRequestBody(userGroupNameRequest{Name: name})
	if err != nil {
		return nil, err
	}
	resp, err := DoRequestAndParse[userGroupResponse](ctx, client, "POST", userGroupsBasePath+"/", body, http.StatusCreated, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

func renameUserGroup(ctx context.Context, client *Client, groupID, name string) (*userGroupResult, error) {
	body, err := MarshalRequestBody(userGroupNameRequest{Name: name})
	if err != nil {
		return nil, err
	}
	resp, err := DoRequestAndParse[userGroupResponse](ctx, client, "PATCH", userGroupPath(groupID), body, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

func deleteUserGroup(ctx context.Context, client *Client, groupID string) error {
	_, err := DoRequestRaw(ctx, client, "DELETE", userGroupPath(groupID), nil, http.StatusNoContent, http.StatusOK)
	return err
}

// addUserGroupMembers and removeUserGroupMembers are idempotent server-side:
// only the delta against current membership is written, so re-sending after
// a partial failure is safe. Both reject an empty user_ids list, so callers
// skip the request when there is nothing to change.
func addUserGroupMembers(ctx context.Context, client *Client, groupID string, userIDs []string) error {
	return writeUserGroupMembers(ctx, client, "POST", groupID, userIDs)
}

func removeUserGroupMembers(ctx context.Context, client *Client, groupID string, userIDs []string) error {
	return writeUserGroupMembers(ctx, client, "DELETE", groupID, userIDs)
}

func writeUserGroupMembers(ctx context.Context, client *Client, method, groupID string, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}
	body, err := MarshalRequestBody(userGroupMembersRequest{UserIDs: userIDs})
	if err != nil {
		return err
	}
	_, err = DoRequestRaw(ctx, client, method, userGroupPath(groupID)+"/members", body, http.StatusNoContent, http.StatusOK)
	return err
}

// getUserGroupMembers returns the group's members from the org-wide
// memberships listing, which is the only route that returns members. The
// listing joins members against active org users, so a member who has left
// the organization is not returned. found is false when the group is absent
// from the listing.
func getUserGroupMembers(ctx context.Context, client *Client, groupID string) (members []userGroupMemberAPI, found bool, err error) {
	resp, err := DoRequestAndParse[userGroupMembershipsResponse](ctx, client, "GET", userGroupsBasePath+"/memberships/list", nil, http.StatusOK)
	if err != nil {
		return nil, false, err
	}
	for _, g := range resp.Result.Groups {
		if g.GroupID == groupID {
			return g.Members, true, nil
		}
	}
	return nil, false, nil
}

// removeAllUserGroupMembers empties a group. Per the backend source, group
// delete does not revoke access members derived from the group, while member
// removal does, so the group is emptied before it is deleted.
//
// A member who leaves the organization between the listing and the removal
// makes the backend reject the whole request with 404 "User IDs not found".
// The listing excludes departed users, so one re-list and retry settles it;
// the retry is bounded to that single attempt.
func removeAllUserGroupMembers(ctx context.Context, client *Client, groupID string) error {
	for attempt := 0; ; attempt++ {
		members, _, err := getUserGroupMembers(ctx, client, groupID)
		if err != nil {
			return fmt.Errorf("list members of user group %s: %w", groupID, err)
		}
		ids := make([]string, 0, len(members))
		for _, m := range members {
			ids = append(ids, m.UserID)
		}
		sort.Strings(ids)

		err = removeUserGroupMembers(ctx, client, groupID, ids)
		if err == nil {
			return nil
		}
		if attempt == 0 && errors.Is(err, ErrNotFound) && !isUserGroupNotFound(err) {
			continue
		}
		return err
	}
}

// resolveOrgUserIDsByEmail maps each email to the org member's user_id
// (usr_...), the ID the member routes take. One org-wide listing serves every
// email, rather than one lookup per member. Emails are matched
// case-insensitively; the result is keyed by the email as given.
//
// Every email that cannot be resolved is reported together, so one apply
// names all of them.
func resolveOrgUserIDsByEmail(ctx context.Context, client *Client, emails []string) (map[string]string, error) {
	collaborators, err := listAllOrganizationCollaborators(ctx, client, nil)
	if err != nil {
		return nil, fmt.Errorf("list organization members: %w", err)
	}
	byFolded := make(map[string]OrganizationCollaboratorResult, len(collaborators))
	for _, c := range collaborators {
		byFolded[strings.ToLower(c.Email)] = c
	}

	resolved := make(map[string]string, len(emails))
	var missing, noUserID []string
	for _, email := range emails {
		c, ok := byFolded[strings.ToLower(email)]
		switch {
		case !ok:
			missing = append(missing, email)
		case c.UserID == nil || *c.UserID == "":
			noUserID = append(noUserID, email)
		default:
			resolved[email] = *c.UserID
		}
	}

	var problems []string
	if len(missing) > 0 {
		sort.Strings(missing)
		problems = append(problems, fmt.Sprintf(
			"not members of the organization: %s. Invite them first, for example with anyscale_organization_invitation, and add them once they have accepted",
			strings.Join(missing, ", ")))
	}
	if len(noUserID) > 0 {
		sort.Strings(noUserID)
		problems = append(problems, fmt.Sprintf(
			"organization members with no user ID, which cannot belong to a user group: %s", strings.Join(noUserID, ", ")))
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return resolved, nil
}

// isPythonStripSpace reports whether the backend's str.strip() removes r. Go's
// unicode.IsSpace misses U+001C..U+001F, which Python treats as whitespace.
func isPythonStripSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// trimUserGroupName mirrors the backend's name normalization (str.strip()).
func trimUserGroupName(name string) string {
	return strings.TrimFunc(name, isPythonStripSpace)
}
