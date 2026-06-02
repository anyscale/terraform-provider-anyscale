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
	// Both build and registry resources back the same cluster_environments API,
	// so one F covers both. Registering twice keeps `terraform-plugin-testing`
	// happy when callers ask for one resource by name.
	resource.AddTestSweepers("anyscale_container_image_build", &resource.Sweeper{
		Name: "anyscale_container_image_build",
		F:    sweepContainerImages,
	})
	resource.AddTestSweepers("anyscale_container_image_registry", &resource.Sweeper{
		Name: "anyscale_container_image_registry",
		F:    sweepContainerImages,
	})
}

var sweepContainerImagePrefixes = []string{"tfacc-", "tf-test-", "tfprovider-"}

const sweepContainerImageDefaultMinAge = 2 * time.Hour

type sweepContainerImageResult struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	CreatedAt string  `json:"created_at"`
	DeletedAt *string `json:"deleted_at"`
	Anonymous bool    `json:"anonymous"`
	IsDefault bool    `json:"is_default"`
}

type sweepContainerImageListResponse struct {
	Results  []sweepContainerImageResult `json:"results"`
	Metadata struct {
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

func sweepContainerImages(_ string) error {
	client, err := GetTestClient()
	if err != nil {
		log.Printf("[sweep:anyscale_container_image] skipping: %v", err)
		return nil
	}

	minAge := sweepContainerImageDefaultMinAge
	if raw := os.Getenv("ANYSCALE_SWEEP_MIN_AGE"); raw != "" {
		parsed, parseErr := time.ParseDuration(raw)
		if parseErr != nil {
			return fmt.Errorf("invalid ANYSCALE_SWEEP_MIN_AGE %q: %w", raw, parseErr)
		}
		minAge = parsed
	}
	cutoff := time.Now().Add(-minAge)

	ctx := context.Background()

	seen := make(map[string]struct{})
	var candidates []sweepContainerImageResult
	for _, prefix := range sweepContainerImagePrefixes {
		results, listErr := searchContainerImagesByContains(ctx, client, prefix)
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

	log.Printf("[sweep:anyscale_container_image] candidates=%d min-age=%s", len(candidates), minAge)

	var failures []string
	archivedCount := 0
	alreadyArchivedCount := 0
	for _, c := range candidates {
		if c.Anonymous || c.IsDefault {
			continue
		}
		if !hasAnyPrefix(c.Name, sweepContainerImagePrefixes) {
			continue
		}

		createdAt, perr := time.Parse(time.RFC3339, c.CreatedAt)
		if perr != nil {
			log.Printf("[sweep:anyscale_container_image] KEEP %s (%s): unparseable created_at %q: %v", c.ID, c.Name, c.CreatedAt, perr)
			continue
		}
		if createdAt.After(cutoff) {
			log.Printf("[sweep:anyscale_container_image] KEEP %s (%s): too young (created %s)", c.ID, c.Name, c.CreatedAt)
			continue
		}

		if c.DeletedAt != nil && *c.DeletedAt != "" {
			alreadyArchivedCount++
			log.Printf("[sweep:anyscale_container_image] KEEP %s (%s): already archived at %s (no permanent delete API)", c.ID, c.Name, *c.DeletedAt)
			continue
		}

		if derr := sweepArchiveContainerImage(ctx, client, c); derr != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): %v", c.ID, c.Name, derr))
			continue
		}
		archivedCount++
	}

	log.Printf("[sweep:anyscale_container_image] archived=%d already_archived=%d failed=%d", archivedCount, alreadyArchivedCount, len(failures))
	if alreadyArchivedCount > 0 {
		log.Printf("[sweep] WARNING: anyscale_container_image_build is archive-only; %d archived images cannot be permanently deleted", alreadyArchivedCount)
	}
	if len(failures) > 0 {
		return fmt.Errorf("container image sweep had %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func searchContainerImagesByContains(ctx context.Context, client *provider.Client, contains string) ([]sweepContainerImageResult, error) {
	var all []sweepContainerImageResult
	var pagingToken *string

	for {
		payload := map[string]interface{}{
			"name":              map[string]string{"contains": contains},
			"include_archived":  true,
			"include_anonymous": false,
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
			return nil, fmt.Errorf("marshal container image search payload: %w", err)
		}

		resp, err := client.DoRequest(ctx, "POST", "/ext/v0/cluster_environments/search", strings.NewReader(string(body)))
		if err != nil {
			return nil, fmt.Errorf("search container images: %w", err)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read container image search response: %w", readErr)
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("search container images: status %d: %s", resp.StatusCode, truncateBody(string(respBody), 256))
		}

		var page sweepContainerImageListResponse
		if err := json.Unmarshal(respBody, &page); err != nil {
			return nil, fmt.Errorf("parse container image search response: %w", err)
		}
		all = append(all, page.Results...)

		if page.Metadata.NextPagingToken == nil || *page.Metadata.NextPagingToken == "" {
			break
		}
		pagingToken = page.Metadata.NextPagingToken
	}

	return all, nil
}

func sweepArchiveContainerImage(ctx context.Context, client *provider.Client, c sweepContainerImageResult) error {
	// The cluster_environments endpoint has no DELETE verb; archive via
	// application_templates instead. The API may respond 200 with the env in
	// an archived state, 4xx for default envs, or 404 if it raced — all OK.
	resp, err := client.DoRequest(ctx, "POST", fmt.Sprintf("/api/v2/application_templates/%s/archive", c.ID), nil)
	if err != nil {
		log.Printf("[sweep:anyscale_container_image] DELETE FAILED %s (%s): %v", c.ID, c.Name, err)
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	switch resp.StatusCode {
	case 200, 202, 204, 404:
		log.Printf("[sweep:anyscale_container_image] DELETE OK %s (%s): status=%d", c.ID, c.Name, resp.StatusCode)
		return nil
	case 400:
		// Default cluster envs and already-archived rows can fail with 400.
		// Treat as best-effort success so concurrent sweeps don't cascade.
		log.Printf("[sweep:anyscale_container_image] DELETE SKIP %s (%s): status=400 body=%s", c.ID, c.Name, truncateBody(string(body), 256))
		return nil
	default:
		log.Printf("[sweep:anyscale_container_image] DELETE FAILED %s (%s): status=%d body=%s", c.ID, c.Name, resp.StatusCode, truncateBody(string(body), 256))
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncateBody(string(body), 256))
	}
}
