package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func init() {
	resource.AddTestSweepers("anyscale_role_binding", &resource.Sweeper{
		Name: "anyscale_role_binding",
		F:    sweepRoleBindings,
	})
}

type sweepRoleBindingSummary struct {
	ID           string `json:"id"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
}

// sweepRoleBindings revokes every binding held by a sweepable user group, so
// the user group sweeper (which depends on this one) deletes groups that hold
// no roles. Deleting a group also drops its bindings, but revoking them first
// goes through the same path a practitioner's destroy does. It sweeps nothing
// when the organization does not have role bindings enabled.
func sweepRoleBindings(_ string) error {
	client, err := GetTestClient()
	if err != nil {
		log.Printf("[sweep:anyscale_role_binding] skipping: %v", err)
		return nil
	}
	minAge, err := resolveSweepMinAge(defaultSweepMinAge)
	if err != nil {
		return err
	}
	return sweepRoleBindingsWithClient(context.Background(), client, time.Now().Add(-minAge))
}

func sweepRoleBindingsWithClient(ctx context.Context, client *provider.Client, cutoff time.Time) error {
	status, body, err := sweepUserGroupRequest(ctx, client, "GET", "/api/v2/roles/?count=1", nil)
	if err != nil {
		return fmt.Errorf("check role bindings enabled: %w", err)
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		log.Printf("[sweep:anyscale_role_binding] skipping: role bindings are not enabled for this organization")
		return nil
	default:
		return fmt.Errorf("check role bindings enabled: status %d: %s", status, truncateBody(body, 256))
	}

	groups, err := listAllUserGroupsForSweep(ctx, client)
	if err != nil {
		return err
	}
	candidates := userGroupSweepCandidates(groups, cutoff)

	var failures []string
	swept := 0
	for _, g := range candidates {
		bindings, err := listRoleBindingsForSweep(ctx, client, g.ID)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): %v", g.ID, g.Name, err))
			continue
		}
		for _, b := range bindings {
			if err := sweepDeleteRoleBinding(ctx, client, b); err != nil {
				failures = append(failures, fmt.Sprintf("%s on %s (%s): %v", b.ID, g.ID, g.Name, err))
				continue
			}
			swept++
		}
	}
	log.Printf("[sweep:anyscale_role_binding] %d candidate group(s), swept=%d failed=%d", len(candidates), swept, len(failures))
	if len(failures) > 0 {
		return fmt.Errorf("role binding sweep had %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

// listRoleBindingsForSweep returns every binding the group holds directly, on
// any resource.
func listRoleBindingsForSweep(ctx context.Context, client *provider.Client, groupID string) ([]sweepRoleBindingSummary, error) {
	return provider.PaginatedRequest(ctx, client, "/api/v2/role_bindings/principals/user_group/"+url.PathEscape(groupID), url.Values{"count": []string{"50"}},
		func(body []byte) ([]sweepRoleBindingSummary, *string, error) {
			var page struct {
				Results  []sweepRoleBindingSummary `json:"results"`
				Metadata struct {
					NextPagingToken *string `json:"next_paging_token"`
				} `json:"metadata"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				return nil, nil, fmt.Errorf("parse role bindings response: %w", err)
			}
			return page.Results, page.Metadata.NextPagingToken, nil
		},
	)
}

// sweepDeleteRoleBinding revokes one binding. A 403 is not read as "already
// gone": the backend answers 403 both for a revoked binding and for a caller
// who may not revoke it, and only the next sweep's listing tells them apart.
func sweepDeleteRoleBinding(ctx context.Context, client *provider.Client, b sweepRoleBindingSummary) error {
	if isSweepDryRun() {
		log.Printf("[sweep:anyscale_role_binding] DRY-RUN would DELETE %s (%s %s)", b.ID, b.ResourceType, b.ResourceID)
		return nil
	}
	status, body, err := sweepUserGroupRequest(ctx, client, "DELETE", "/api/v2/role_bindings/"+url.PathEscape(b.ID), nil)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return fmt.Errorf("status %d: %s", status, truncateBody(body, 256))
	}
	log.Printf("[sweep:anyscale_role_binding] revoked %s (%s %s)", b.ID, b.ResourceType, b.ResourceID)
	return nil
}
