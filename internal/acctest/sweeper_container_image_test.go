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
	// Both build and registry resources back the same cluster_environments API,
	// so one F covers both. Registering twice keeps `terraform-plugin-testing`
	// happy when callers ask for one resource by name.
	resource.AddTestSweepers("anyscale_container_image_build", &resource.Sweeper{
		Name: "anyscale_container_image_build",
		// A leaked anyscale_service holds a build_id open, so it must sweep first - see
		// sweeper_service_test.go. Belt-and-suspenders here specifically: this sweeper only
		// archives (no permanent delete API exists for builds), so it does not actually have a
		// hard in-use delete-ordering constraint the way project/compute_config do, but the edge
		// costs nothing and keeps the graph consistent if that ever changes.
		Dependencies: []string{"anyscale_service"},
		F:            sweepContainerImages,
	})
	resource.AddTestSweepers("anyscale_container_image_registry", &resource.Sweeper{
		Name: "anyscale_container_image_registry",
		F:    sweepContainerImages,
	})
}

func sweepContainerImages(_ string) error {
	client, err := GetTestClient()
	if err != nil {
		log.Printf("[sweep:anyscale_container_image] skipping: %v", err)
		return nil
	}

	minAge, err := resolveSweepMinAge(defaultSweepMinAge)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-minAge)

	ctx := context.Background()

	seen := make(map[string]struct{})
	var candidates []provider.ApplicationTemplateResult
	for _, prefix := range sweepableResourcePrefixes {
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
		if !hasAnyPrefix(c.Name, sweepableResourcePrefixes) {
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

		if c.IsArchived() {
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

// searchContainerImagesByContains lists application templates via api/v2 (GET,
// pagination as query params: name_contains + paging_token), not the ext/v0
// cluster_environments/search endpoint (POST, pagination inside the body) this
// used to call. include_archived=true is passed so already-archived rows are
// still visible for the alreadyArchivedCount bookkeeping below; there is no
// api/v2 include_anonymous filter (unlike ext/v0), but that is harmless here
// since sweepContainerImages already filters out c.Anonymous/c.IsDefault
// client-side -- the only effect of its absence is more rows to filter
// client-side, never fewer, so it cannot cause a silent-truncation leak.
func searchContainerImagesByContains(ctx context.Context, client *provider.Client, contains string) ([]provider.ApplicationTemplateResult, error) {
	params := url.Values{}
	params.Set("name_contains", contains)
	params.Set("include_archived", "true")

	return provider.PaginatedRequest(ctx, client, "/api/v2/application_templates/", params,
		func(body []byte) ([]provider.ApplicationTemplateResult, *string, error) {
			var page provider.ApplicationTemplatesListResponse
			if err := json.Unmarshal(body, &page); err != nil {
				return nil, nil, err
			}
			return page.Results, page.Metadata.NextPagingToken, nil
		},
	)
}

func sweepArchiveContainerImage(ctx context.Context, client *provider.Client, c provider.ApplicationTemplateResult) error {
	if isSweepDryRun() {
		log.Printf("[sweep:anyscale_container_image] DRY-RUN would ARCHIVE %s (%s)", c.ID, c.Name)
		return nil
	}

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
