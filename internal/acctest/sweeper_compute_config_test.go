package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func init() {
	resource.AddTestSweepers("anyscale_compute_config", &resource.Sweeper{
		Name: "anyscale_compute_config",
		// A leaked anyscale_service holds a compute_config_id open, so it must sweep before the
		// compute config it references - see sweeper_service_test.go.
		Dependencies: []string{"anyscale_service"},
		F:            sweepComputeConfigs,
	})
}

type sweepComputeConfigResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Anonymous bool   `json:"anonymous"`
}

type sweepComputeConfigListResponse struct {
	Results  []sweepComputeConfigResult `json:"results"`
	Metadata struct {
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

func sweepComputeConfigs(_ string) error {
	client, err := GetTestClient()
	if err != nil {
		log.Printf("[sweep:anyscale_compute_config] skipping: %v", err)
		return nil
	}

	minAge, err := resolveSweepMinAge(defaultSweepMinAge)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-minAge)

	ctx := context.Background()

	// Search per prefix server-side via name.contains, then filter for true
	// prefix match client-side. Avoids paginating every compute config in the
	// org when most are unrelated.
	seen := make(map[string]struct{})
	var candidates []sweepComputeConfigResult
	for _, prefix := range sweepableResourcePrefixes {
		results, listErr := searchComputeConfigsByContains(ctx, client, prefix)
		if listErr != nil {
			return listErr
		}
		for _, r := range results {
			if _, dup := seen[r.ID]; dup {
				continue
			}
			seen[r.ID] = struct{}{}
			candidates = append(candidates, r)
		}
	}

	log.Printf("[sweep:anyscale_compute_config] candidates=%d min-age=%s", len(candidates), minAge)

	var failures []string
	swept := 0
	for _, c := range candidates {
		if c.Anonymous {
			continue
		}
		if !hasAnyPrefix(c.Name, sweepableResourcePrefixes) {
			continue
		}

		createdAt, perr := time.Parse(time.RFC3339, c.CreatedAt)
		if perr != nil {
			log.Printf("[sweep:anyscale_compute_config] KEEP %s (%s): unparseable created_at %q: %v", c.ID, c.Name, c.CreatedAt, perr)
			continue
		}
		if createdAt.After(cutoff) {
			log.Printf("[sweep:anyscale_compute_config] KEEP %s (%s): too young (created %s)", c.ID, c.Name, c.CreatedAt)
			continue
		}

		if derr := sweepArchiveComputeConfig(ctx, client, c); derr != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): %v", c.ID, c.Name, derr))
			continue
		}
		swept++
	}

	log.Printf("[sweep:anyscale_compute_config] swept=%d failed=%d", swept, len(failures))
	if len(failures) > 0 {
		return fmt.Errorf("compute config sweep had %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func searchComputeConfigsByContains(ctx context.Context, client *provider.Client, contains string) ([]sweepComputeConfigResult, error) {
	var all []sweepComputeConfigResult
	var pagingToken string

	for {
		// CC5b tail: migrated from /ext/v0/cluster_computes/search to
		// /api/v2/compute_templates/search, mirroring the identical pattern
		// already proven by the data source's searchComputeTemplatesPaged
		// (data_source_compute_config.go). Two landmines here, both traced
		// against the read-only product reference, not assumed:
		//
		// 1. Pagination moves from the request BODY to the URL QUERY STRING.
		// api/v2's search endpoint reads count/paging_token via
		// Depends(required_pagination_large), which are plain FastAPI
		// function params sourced from the query string, not the
		// ComputeTemplateQuery body model. The old ext/v0 shape nested them
		// under a "paging" body key; api/v2 simply never reads that key, so
		// leaving it in the body would compile, get a 200 back, and silently
		// paginate wrong (always page 1) -- a silent-truncation failure mode
		// that could miss leaked test clouds/configs.
		//
		// 2. ComputeTemplateQuery.version defaults to latest-version-only
		// (its own docstring: "Setting version to None is equivalent to
		// setting version to -1"), the opposite of the old ext/v0 default
		// this sweeper relied on returning every version of a name as a
		// distinct row. Architect's sharpening: the real risk isn't just
		// which row gates the sweep timer -- a leaked config whose NEWEST
		// version was created recently would be judged too-young and KEPT
		// even when older versions are well past the cutoff, so a config
		// with recent churn could evade the sweeper entirely. version: -2 is
		// the documented "do not filter by version" sentinel (same one
		// fetchComputeConfigVersions already uses) and restores the current,
		// safer, enumerate-every-version behavior.
		//
		// archive_status: "ALL" is also now sent explicitly. api/v2 defaults
		// this to NOT_ARCHIVED (ext/v0 has no equivalent and never filtered),
		// so omitting it would silently narrow results to unarchived rows
		// only. Already-archived rows passing through here are harmless --
		// sweepArchiveComputeConfig treats re-archiving as success
		// (200/202/204/404) -- so ALL preserves today's exact behavior
		// rather than narrowing it.
		payload := map[string]interface{}{
			"name":              map[string]string{"contains": contains},
			"include_anonymous": false,
			"archive_status":    "ALL",
			"version":           -2,
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal compute config search payload: %w", err)
		}

		query := url.Values{}
		query.Set("count", "100")
		if pagingToken != "" {
			query.Set("paging_token", pagingToken)
		}
		path := fmt.Sprintf("/api/v2/compute_templates/search?%s", query.Encode())

		resp, err := client.DoRequest(ctx, "POST", path, strings.NewReader(string(body)))
		if err != nil {
			return nil, fmt.Errorf("search compute configs: %w", err)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read compute config search response: %w", readErr)
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("search compute configs: status %d: %s", resp.StatusCode, truncateBody(string(respBody), 256))
		}

		var page sweepComputeConfigListResponse
		if err := json.Unmarshal(respBody, &page); err != nil {
			return nil, fmt.Errorf("parse compute config search response: %w", err)
		}
		all = append(all, page.Results...)

		if page.Metadata.NextPagingToken == nil || *page.Metadata.NextPagingToken == "" {
			break
		}
		pagingToken = *page.Metadata.NextPagingToken
	}

	return all, nil
}

func sweepArchiveComputeConfig(ctx context.Context, client *provider.Client, c sweepComputeConfigResult) error {
	if isSweepDryRun() {
		log.Printf("[sweep:anyscale_compute_config] DRY-RUN would ARCHIVE %s (%s)", c.ID, c.Name)
		return nil
	}

	// Delete maps to the resource's archive op so each version-specific ID
	// must be archived; the sweep search returns one row per version.
	resp, err := client.DoRequest(ctx, "POST", fmt.Sprintf("/api/v2/compute_templates/%s/archive", c.ID), nil)
	if err != nil {
		log.Printf("[sweep:anyscale_compute_config] DELETE FAILED %s (%s): %v", c.ID, c.Name, err)
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	switch resp.StatusCode {
	case 200, 202, 204, 404:
		log.Printf("[sweep:anyscale_compute_config] DELETE OK %s (%s): status=%d", c.ID, c.Name, resp.StatusCode)
		return nil
	default:
		log.Printf("[sweep:anyscale_compute_config] DELETE FAILED %s (%s): status=%d body=%s", c.ID, c.Name, resp.StatusCode, truncateBody(string(body), 256))
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncateBody(string(body), 256))
	}
}
