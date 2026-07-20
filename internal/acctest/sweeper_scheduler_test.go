package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func init() {
	resource.AddTestSweepers("anyscale_global_resource_scheduler", &resource.Sweeper{
		Name: "anyscale_global_resource_scheduler",
		F:    sweepSchedulers,
	})
}

// sweepSchedulerResult mirrors only the fields the sweeper needs. The list API
// response model doesn't expose a created_at; we read it opportunistically in
// case the upstream schema is extended, but we don't depend on it.
type sweepSchedulerResult struct {
	MachinePoolID   string `json:"machine_pool_id"`
	MachinePoolName string `json:"machine_pool_name"`
	CreatedAt       string `json:"created_at,omitempty"`
}

type sweepSchedulerListResponse struct {
	Result struct {
		MachinePools []sweepSchedulerResult `json:"machine_pools"`
	} `json:"result"`
}

func sweepSchedulers(_ string) error {
	client, err := GetTestClient()
	if err != nil {
		log.Printf("[sweep:anyscale_global_resource_scheduler] skipping: %v", err)
		return nil
	}

	minAge, err := resolveSweepMinAge(defaultSweepMinAge)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-minAge)

	ctx := context.Background()
	schedulers, err := listAllSchedulersForSweep(ctx, client)
	if err != nil {
		return err
	}

	log.Printf("[sweep:anyscale_global_resource_scheduler] listed %d scheduler(s); min-age=%s", len(schedulers), minAge)

	var failures []string
	swept := 0
	for _, s := range schedulers {
		if !hasAnyPrefix(s.MachinePoolName, sweepableResourcePrefixes) {
			continue
		}

		// The list endpoint may or may not return created_at depending on API
		// version. Skip the age check when missing — the strict prefix match
		// is the safety invariant. Logged so this gap stays visible.
		if s.CreatedAt != "" {
			createdAt, perr := time.Parse(time.RFC3339, s.CreatedAt)
			if perr != nil {
				log.Printf("[sweep:anyscale_global_resource_scheduler] KEEP %s (%s): unparseable created_at %q: %v", s.MachinePoolID, s.MachinePoolName, s.CreatedAt, perr)
				continue
			}
			if createdAt.After(cutoff) {
				log.Printf("[sweep:anyscale_global_resource_scheduler] KEEP %s (%s): too young (created %s)", s.MachinePoolID, s.MachinePoolName, s.CreatedAt)
				continue
			}
		} else {
			log.Printf("[sweep:anyscale_global_resource_scheduler] WARN no created_at for %s (%s); proceeding on prefix match only", s.MachinePoolID, s.MachinePoolName)
		}

		if derr := sweepDeleteScheduler(ctx, client, s); derr != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): %v", s.MachinePoolID, s.MachinePoolName, derr))
			continue
		}
		swept++
	}

	log.Printf("[sweep:anyscale_global_resource_scheduler] swept=%d failed=%d", swept, len(failures))
	if len(failures) > 0 {
		return fmt.Errorf("scheduler sweep had %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func listAllSchedulersForSweep(ctx context.Context, client *provider.Client) ([]sweepSchedulerResult, error) {
	// The endpoint does not paginate in the current API; if pagination is
	// added later we'll need to thread paging_token here too.
	resp, err := client.DoRequest(ctx, "GET", "/api/v2/machine_pools/", nil)
	if err != nil {
		return nil, fmt.Errorf("list schedulers: %w", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read schedulers response: %w", readErr)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("list schedulers: status %d: %s", resp.StatusCode, truncateBody(string(body), 256))
	}

	var page sweepSchedulerListResponse
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("parse schedulers response: %w", err)
	}
	return page.Result.MachinePools, nil
}

func sweepDeleteScheduler(ctx context.Context, client *provider.Client, s sweepSchedulerResult) error {
	if isSweepDryRun() {
		log.Printf("[sweep:anyscale_global_resource_scheduler] DRY-RUN would DELETE %s (%s)", s.MachinePoolID, s.MachinePoolName)
		return nil
	}

	// Delete via POST with a body, matching the resource implementation.
	payload := map[string]string{"machine_pool_name": s.MachinePoolName}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal scheduler delete request: %w", err)
	}

	resp, err := client.DoRequest(ctx, "POST", "/api/v2/machine_pools/delete", strings.NewReader(string(body)))
	if err != nil {
		log.Printf("[sweep:anyscale_global_resource_scheduler] DELETE FAILED %s (%s): %v", s.MachinePoolID, s.MachinePoolName, err)
		return err
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	switch resp.StatusCode {
	case 200, 202, 204, 404:
		log.Printf("[sweep:anyscale_global_resource_scheduler] DELETE OK %s (%s): status=%d", s.MachinePoolID, s.MachinePoolName, resp.StatusCode)
		return nil
	default:
		log.Printf("[sweep:anyscale_global_resource_scheduler] DELETE FAILED %s (%s): status=%d body=%s", s.MachinePoolID, s.MachinePoolName, resp.StatusCode, truncateBody(string(respBody), 256))
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncateBody(string(respBody), 256))
	}
}
