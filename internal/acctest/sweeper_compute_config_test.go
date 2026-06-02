package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func init() {
	resource.AddTestSweepers("anyscale_compute_config", &resource.Sweeper{
		Name: "anyscale_compute_config",
		F:    sweepComputeConfigs,
	})
}

var sweepComputeConfigPrefixes = []string{"tfacc-", "tf-test-", "tfprovider-"}

const sweepComputeConfigDefaultMinAge = 2 * time.Hour

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

	minAge := sweepComputeConfigDefaultMinAge
	if raw := os.Getenv("ANYSCALE_SWEEP_MIN_AGE"); raw != "" {
		parsed, parseErr := time.ParseDuration(raw)
		if parseErr != nil {
			return fmt.Errorf("invalid ANYSCALE_SWEEP_MIN_AGE %q: %w", raw, parseErr)
		}
		minAge = parsed
	}
	cutoff := time.Now().Add(-minAge)

	ctx := context.Background()

	// Search per prefix server-side via name.contains, then filter for true
	// prefix match client-side. Avoids paginating every compute config in the
	// org when most are unrelated.
	seen := make(map[string]struct{})
	var candidates []sweepComputeConfigResult
	for _, prefix := range sweepComputeConfigPrefixes {
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
		if !hasAnyPrefix(c.Name, sweepComputeConfigPrefixes) {
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
	var pagingToken *string

	for {
		// The search endpoint returns every version of a compute config as a
		// distinct row, so we must paginate even within a single prefix query.
		payload := map[string]interface{}{
			"name":              map[string]string{"contains": contains},
			"include_anonymous": false,
			"archive_status":    "NOT_ARCHIVED",
			"paging":            map[string]interface{}{"count": 100},
		}
		if pagingToken != nil && *pagingToken != "" {
			payload["paging"] = map[string]interface{}{
				"count":        100,
				"paging_token": *pagingToken,
			}
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal compute config search payload: %w", err)
		}

		resp, err := client.DoRequest(ctx, "POST", "/ext/v0/cluster_computes/search", strings.NewReader(string(body)))
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
		pagingToken = page.Metadata.NextPagingToken
	}

	return all, nil
}

func sweepArchiveComputeConfig(ctx context.Context, client *provider.Client, c sweepComputeConfigResult) error {
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
