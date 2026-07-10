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

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

var sweepableCloudPrefixes = []string{
	"tfacc-",
	"tf-test-",
	"tfprovider-",
	"tfacc-ephemeral-",
}

const defaultSweepMinAge = 2 * time.Hour

func init() {
	resource.AddTestSweepers("anyscale_cloud", &resource.Sweeper{
		Name:         "anyscale_cloud",
		Dependencies: []string{"anyscale_project", "anyscale_compute_config"},
		F:            sweepClouds,
	})
}

// sweepClouds deletes test clouds whose names start with one of the sweepable
// prefixes ("tfacc-", "tf-test-", "tfprovider-", "tfacc-ephemeral-") and whose
// age exceeds the minimum threshold (default 2h, override via
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
	resp, err := client.DoRequest(ctx, "GET", "/api/v2/clouds", nil)
	if err != nil {
		return fmt.Errorf("list clouds: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read clouds response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("list clouds returned status %d: %s", resp.StatusCode, truncateBody(string(body), 512))
	}

	var listResp struct {
		Results []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			CreatedAt string `json:"created_at"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		return fmt.Errorf("parse clouds response: %w", err)
	}

	now := time.Now().UTC()
	var failures []string

	for _, cloud := range listResp.Results {
		// Never sweep the designated static fixture cloud (single source of
		// truth: defaultKnownGoodCloudName). Its current name is outside the
		// sweepable prefixes, but this explicit guard protects it even if it is
		// ever renamed under one, so CI never deletes its own known-good cloud.
		if cloud.Name == defaultKnownGoodCloudName {
			log.Printf("[sweepClouds] KEEP %s (%s): protected static test fixture", cloud.Name, cloud.ID)
			continue
		}
		if !hasAnyPrefix(cloud.Name, sweepableCloudPrefixes) {
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

func truncateBody(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
