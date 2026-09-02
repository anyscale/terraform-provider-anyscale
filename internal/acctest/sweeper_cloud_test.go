package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func init() {
	resource.AddTestSweepers("anyscale_cloud", &resource.Sweeper{
		Name:         "anyscale_cloud",
		Dependencies: []string{"anyscale_project", "anyscale_compute_config"},
		F:            sweepClouds,
	})
}

// sweepableCloudResult mirrors only the fields the sweeper needs from
// GET /api/v2/clouds.
type sweepableCloudResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type sweepableCloudsListResponse struct {
	Results  []sweepableCloudResult `json:"results"`
	Metadata struct {
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// listAllCloudsForSweep pages through every cloud in the org. GET /api/v2/clouds
// paginates the same way every other collection endpoint this provider calls
// does (paging_token in, next_paging_token out) - a single unpaginated GET
// here silently truncated the sweep to page 1, so anything matching a
// sweepable prefix beyond it was invisible to `make sweep` even though it was
// never protected from anything else finding and deleting it.
func listAllCloudsForSweep(ctx context.Context, client *provider.Client) ([]sweepableCloudResult, error) {
	return provider.PaginatedRequest(ctx, client, "/api/v2/clouds", nil,
		func(body []byte) ([]sweepableCloudResult, *string, error) {
			var page sweepableCloudsListResponse
			if err := json.Unmarshal(body, &page); err != nil {
				return nil, nil, fmt.Errorf("parse clouds response: %w", err)
			}
			return page.Results, page.Metadata.NextPagingToken, nil
		},
	)
}

// sweepClouds deletes test clouds whose names start with one of the sweepable
// prefixes (sweepableResourcePrefixes: "tfacc-", "tf-test-", "tfprovider-" -
// this also covers ephemeral clouds, named "tfacc-ephemeral-<nanos>") and
// whose age exceeds the minimum threshold (default 2h, override via
// ANYSCALE_SWEEP_MIN_AGE using time.ParseDuration syntax). The age threshold
// avoids racing live tests; the prefix filter ensures we never touch
// production clouds.
func sweepClouds(region string) error {
	client, err := GetTestClient()
	if err != nil {
		log.Printf("[sweepClouds] SKIP: no credentials available: %v", err)
		return nil
	}

	minAge, err := resolveSweepMinAge(defaultSweepMinAge)
	if err != nil {
		return err
	}

	ctx := context.Background()
	clouds, err := listAllCloudsForSweep(ctx, client)
	if err != nil {
		return fmt.Errorf("list clouds: %w", err)
	}

	now := time.Now().UTC()
	var failures []string

	for _, cloud := range clouds {
		// Never sweep the designated static fixture cloud (single source of
		// truth: defaultKnownGoodCloudName). Its current name is outside the
		// sweepable prefixes, but this explicit guard protects it even if it is
		// ever renamed under one, so CI never deletes its own known-good cloud.
		if cloud.Name == defaultKnownGoodCloudName {
			log.Printf("[sweepClouds] KEEP %s (%s): protected static test fixture", cloud.Name, cloud.ID)
			continue
		}
		if !hasAnyPrefix(cloud.Name, sweepableResourcePrefixes) {
			continue
		}

		createdAt, parseErr := time.Parse(time.RFC3339, cloud.CreatedAt)
		if parseErr != nil {
			log.Printf("[sweepClouds] KEEP %s (%s): unparseable created_at %q: %v", cloud.Name, cloud.ID, cloud.CreatedAt, parseErr)
			continue
		}

		age := now.Sub(createdAt.UTC())
		if age < minAge {
			log.Printf("[sweepClouds] KEEP %s (%s): age %s younger than threshold %s", cloud.Name, cloud.ID, age.Round(time.Second), minAge)
			continue
		}

		if isSweepDryRun() {
			log.Printf("[sweepClouds] DRY-RUN would DELETE %s (%s), age %s", cloud.Name, cloud.ID, age.Round(time.Second))
			continue
		}

		delResp, err := client.DoRequest(ctx, "DELETE", fmt.Sprintf("/api/v2/clouds/%s", cloud.ID), nil)
		if err != nil {
			log.Printf("[sweepClouds] DELETE FAILED %s (%s): request error: %v", cloud.Name, cloud.ID, err)
			failures = append(failures, fmt.Sprintf("%s (%s): %v", cloud.Name, cloud.ID, err))
			continue
		}

		status := delResp.StatusCode
		switch status {
		case http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound:
			log.Printf("[sweepClouds] DELETE OK %s (%s): status %d", cloud.Name, cloud.ID, status)
		default:
			delBody, _ := io.ReadAll(delResp.Body)
			log.Printf("[sweepClouds] DELETE FAILED %s (%s): status %d body %s", cloud.Name, cloud.ID, status, truncateBody(string(delBody), 512))
			failures = append(failures, fmt.Sprintf("%s (%s): status %d", cloud.Name, cloud.ID, status))
		}
		_ = delResp.Body.Close()
	}

	if len(failures) > 0 {
		return fmt.Errorf("sweepClouds: %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}
