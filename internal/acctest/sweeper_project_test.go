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
	resource.AddTestSweepers("anyscale_project", &resource.Sweeper{
		Name: "anyscale_project",
		F:    sweepProjects,
	})
}

const sweepProjectDefaultMinAge = 2 * time.Hour

type sweepProjectResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
	CreatedAt string `json:"created_at"`
}

type sweepProjectListResponse struct {
	Results  []sweepProjectResult `json:"results"`
	Metadata struct {
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

func sweepProjects(_ string) error {
	client, err := GetTestClient()
	if err != nil {
		// No credentials available — make `-sweep` a no-op rather than failing
		// CI runs that don't have secrets wired up.
		log.Printf("[sweep:anyscale_project] skipping: %v", err)
		return nil
	}

	minAge, err := resolveSweepMinAge(sweepProjectDefaultMinAge)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-minAge)

	ctx := context.Background()
	projects, err := listAllProjectsForSweep(ctx, client)
	if err != nil {
		return err
	}

	log.Printf("[sweep:anyscale_project] listed %d project(s); min-age=%s", len(projects), minAge)

	var failures []string
	swept := 0
	for _, p := range projects {
		if p.IsDefault || p.Name == "default" {
			continue
		}
		if !hasAnyPrefix(p.Name, sweepableResourcePrefixes) {
			continue
		}

		createdAt, perr := time.Parse(time.RFC3339, p.CreatedAt)
		if perr != nil {
			log.Printf("[sweep:anyscale_project] skip %s (%s): unparseable created_at %q: %v", p.ID, p.Name, p.CreatedAt, perr)
			continue
		}
		if createdAt.After(cutoff) {
			log.Printf("[sweep:anyscale_project] skip %s (%s): too young (created %s)", p.ID, p.Name, p.CreatedAt)
			continue
		}

		if derr := sweepDeleteProject(ctx, client, p); derr != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): %v", p.ID, p.Name, derr))
			continue
		}
		swept++
	}

	log.Printf("[sweep:anyscale_project] swept=%d failed=%d", swept, len(failures))
	if len(failures) > 0 {
		return fmt.Errorf("project sweep had %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func listAllProjectsForSweep(ctx context.Context, client *provider.Client) ([]sweepProjectResult, error) {
	var all []sweepProjectResult
	pagingToken := ""

	for {
		path := "/api/v2/projects"
		if pagingToken != "" {
			params := url.Values{}
			params.Set("paging_token", pagingToken)
			path = fmt.Sprintf("%s?%s", path, params.Encode())
		}

		resp, err := client.DoRequest(ctx, "GET", path, nil)
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read projects response: %w", readErr)
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("list projects: status %d: %s", resp.StatusCode, truncateBody(string(body), 256))
		}

		var page sweepProjectListResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("parse projects response: %w", err)
		}
		all = append(all, page.Results...)

		if page.Metadata.NextPagingToken == nil || *page.Metadata.NextPagingToken == "" {
			break
		}
		pagingToken = *page.Metadata.NextPagingToken
	}

	return all, nil
}

func sweepDeleteProject(ctx context.Context, client *provider.Client, p sweepProjectResult) error {
	if isSweepDryRun() {
		log.Printf("[sweep:anyscale_project] DRY-RUN would DELETE %s (%s)", p.ID, p.Name)
		return nil
	}

	resp, err := client.DoRequest(ctx, "DELETE", fmt.Sprintf("/api/v2/projects/%s", p.ID), nil)
	if err != nil {
		log.Printf("[sweep:anyscale_project] DELETE FAILED %s (%s): %v", p.ID, p.Name, err)
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	switch resp.StatusCode {
	case 200, 202, 204, 404:
		log.Printf("[sweep:anyscale_project] DELETE OK %s (%s): status=%d", p.ID, p.Name, resp.StatusCode)
		return nil
	default:
		log.Printf("[sweep:anyscale_project] DELETE FAILED %s (%s): status=%d body=%s", p.ID, p.Name, resp.StatusCode, truncateBody(string(body), 256))
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncateBody(string(body), 256))
	}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
