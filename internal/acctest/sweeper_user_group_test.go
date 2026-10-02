package acctest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func init() {
	resource.AddTestSweepers("anyscale_user_group", &resource.Sweeper{
		Name:         "anyscale_user_group",
		F:            sweepUserGroups,
		Dependencies: []string{"anyscale_role_binding"},
	})
}

// userGroupSourceSCIM marks a directory-synced group. The sweeper never touches
// one, whatever its name: it is owned by the identity provider, not by a test.
const userGroupSourceSCIM = "scim"

type sweepUserGroupResult struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Source    *string `json:"source"`
	CreatedAt string  `json:"created_at"`
}

type sweepUserGroupListResponse struct {
	Results  []sweepUserGroupResult `json:"results"`
	Metadata struct {
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

type sweepUserGroupMembershipsResponse struct {
	Result struct {
		Groups []struct {
			GroupID string `json:"group_id"`
			Members []struct {
				UserID string `json:"user_id"`
			} `json:"members"`
		} `json:"groups"`
	} `json:"result"`
}

// userGroupSweepCandidates filters a group listing down to the groups the
// sweeper may delete: a sweepable name prefix, not directory-synced, and older
// than cutoff. The listing pages by offset over created_at desc, so a group
// created mid-sweep can shift an earlier group onto the next page; duplicates
// are dropped here rather than deleted twice.
func userGroupSweepCandidates(groups []sweepUserGroupResult, cutoff time.Time) []sweepUserGroupResult {
	seen := map[string]bool{}
	var out []sweepUserGroupResult
	for _, g := range groups {
		if seen[g.ID] {
			continue
		}
		seen[g.ID] = true
		if g.Source != nil && *g.Source == userGroupSourceSCIM {
			continue
		}
		if !hasAnyPrefix(g.Name, sweepableResourcePrefixes) {
			continue
		}
		createdAt, err := time.Parse(time.RFC3339, g.CreatedAt)
		if err != nil {
			log.Printf("[sweep:anyscale_user_group] skip %s (%s): unparseable created_at %q: %v", g.ID, g.Name, g.CreatedAt, err)
			continue
		}
		if createdAt.After(cutoff) {
			log.Printf("[sweep:anyscale_user_group] skip %s (%s): too young (created %s)", g.ID, g.Name, g.CreatedAt)
			continue
		}
		out = append(out, g)
	}
	return out
}

func sweepUserGroups(_ string) error {
	client, err := GetTestClient()
	if err != nil {
		log.Printf("[sweep:anyscale_user_group] skipping: %v", err)
		return nil
	}

	minAge, err := resolveSweepMinAge(defaultSweepMinAge)
	if err != nil {
		return err
	}
	return sweepUserGroupsWithClient(context.Background(), client, time.Now().Add(-minAge))
}

func sweepUserGroupsWithClient(ctx context.Context, client *provider.Client, cutoff time.Time) error {
	groups, err := listAllUserGroupsForSweep(ctx, client)
	if err != nil {
		return err
	}
	candidates := userGroupSweepCandidates(groups, cutoff)
	log.Printf("[sweep:anyscale_user_group] listed %d group(s), %d candidate(s)", len(groups), len(candidates))
	if len(candidates) == 0 {
		return nil
	}

	members, err := listUserGroupMembersForSweep(ctx, client)
	if err != nil {
		return err
	}

	var failures []string
	swept := 0
	for _, g := range candidates {
		if err := sweepDeleteUserGroup(ctx, client, g, members[g.ID]); err != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): %v", g.ID, g.Name, err))
			continue
		}
		swept++
	}
	log.Printf("[sweep:anyscale_user_group] swept=%d failed=%d", swept, len(failures))
	if len(failures) > 0 {
		return fmt.Errorf("user group sweep had %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func listAllUserGroupsForSweep(ctx context.Context, client *provider.Client) ([]sweepUserGroupResult, error) {
	return provider.PaginatedRequest(ctx, client, "/api/v2/user_groups/", url.Values{"count": []string{"50"}},
		func(body []byte) ([]sweepUserGroupResult, *string, error) {
			var page sweepUserGroupListResponse
			if err := json.Unmarshal(body, &page); err != nil {
				return nil, nil, fmt.Errorf("parse user groups response: %w", err)
			}
			return page.Results, page.Metadata.NextPagingToken, nil
		},
	)
}

// listUserGroupMembersForSweep returns member user IDs keyed by group ID. The
// endpoint is unpaginated and returns every group in the org.
func listUserGroupMembersForSweep(ctx context.Context, client *provider.Client) (map[string][]string, error) {
	body, err := provider.DoRequestRaw(ctx, client, "GET", "/api/v2/user_groups/memberships/list", nil, http.StatusOK)
	if err != nil {
		return nil, fmt.Errorf("list user group memberships: %w", err)
	}
	var resp sweepUserGroupMembershipsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse user group memberships response: %w", err)
	}
	out := map[string][]string{}
	for _, g := range resp.Result.Groups {
		for _, m := range g.Members {
			out[g.GroupID] = append(out[g.GroupID], m.UserID)
		}
	}
	return out, nil
}

// sweepDeleteUserGroup removes a group's members, then deletes the group. The
// order matters: per the backend source, group delete does not revoke the
// access its members held through the group, while member removal does. If removal fails
// the group is left in place, so the next sweep can retry rather than leaking
// that access permanently.
func sweepDeleteUserGroup(ctx context.Context, client *provider.Client, g sweepUserGroupResult, memberIDs []string) error {
	if isSweepDryRun() {
		log.Printf("[sweep:anyscale_user_group] DRY-RUN would remove %d member(s) from and DELETE %s (%s)", len(memberIDs), g.ID, g.Name)
		return nil
	}

	if len(memberIDs) > 0 {
		payload, err := json.Marshal(map[string][]string{"user_ids": memberIDs})
		if err != nil {
			return err
		}
		status, body, err := sweepUserGroupRequest(ctx, client, "DELETE", fmt.Sprintf("/api/v2/user_groups/%s/members", g.ID), payload)
		if err != nil {
			return fmt.Errorf("remove members: %w", err)
		}
		switch status {
		case http.StatusNoContent, http.StatusOK:
		case http.StatusNotFound:
			// "User group ... not found" means it is already gone; a "User IDs not found"
			// 404 means a member left the org and must not be read as success.
			if strings.Contains(body, "User IDs not found") {
				return fmt.Errorf("remove members: status %d: %s", status, truncateBody(body, 256))
			}
			log.Printf("[sweep:anyscale_user_group] %s (%s) already gone", g.ID, g.Name)
			return nil
		default:
			return fmt.Errorf("remove members: status %d: %s", status, truncateBody(body, 256))
		}
	}

	status, body, err := sweepUserGroupRequest(ctx, client, "DELETE", fmt.Sprintf("/api/v2/user_groups/%s", g.ID), nil)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		log.Printf("[sweep:anyscale_user_group] DELETE OK %s (%s): status=%d members_removed=%d", g.ID, g.Name, status, len(memberIDs))
		return nil
	default:
		return fmt.Errorf("status %d: %s", status, truncateBody(body, 256))
	}
}

func sweepUserGroupRequest(ctx context.Context, client *provider.Client, method, path string, payload []byte) (int, string, error) {
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	resp, err := client.DoRequest(ctx, method, path, reqBody)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), nil
}
