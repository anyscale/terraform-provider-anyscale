package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func init() {
	resource.AddTestSweepers("anyscale_service", &resource.Sweeper{
		Name: "anyscale_service",
		F:    sweepServices,
	})
}

// sweepServiceMaxTerminateWait/sweepServiceTerminatePollTick bound the sweeper's own
// terminate-then-wait loop: DELETE /{id} 400s unless current_state is already TERMINATED (traced
// via contract §H1/the resource's own Delete), so a bare terminate-then-immediate-delete would
// fail here too. Self-contained rather than reusing service_helpers.go's waitForServiceState:
// that helper is unexported (package provider, this file is package acctest), and a sweeper has
// no need for the millisecond-injectable timing that helper's unit tests rely on - a real,
// several-minute bound is the right shape for a background cleanup job.
const (
	sweepServiceMaxTerminateWait  = 5 * time.Minute
	sweepServiceTerminatePollTick = 10 * time.Second
)

type sweepServiceResult struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	CreatedAt    string `json:"created_at"`
	CurrentState string `json:"current_state"`
}

type sweepServiceListResponse struct {
	Results  []sweepServiceResult `json:"results"`
	Metadata struct {
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// sweepServices deletes test services whose names start with a sweepable prefix and whose age
// exceeds the minimum threshold - mirrors sweepProjects/sweepClouds' shape (list all, filter
// client-side by prefix+age, since this endpoint has no name_contains-style server-side filter
// this sweeper needs to rely on).
func sweepServices(_ string) error {
	client, err := GetTestClient()
	if err != nil {
		log.Printf("[sweep:anyscale_service] skipping: %v", err)
		return nil
	}

	minAge, err := resolveSweepMinAge(defaultSweepMinAge)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-minAge)

	ctx := context.Background()
	services, err := listAllServicesForSweep(ctx, client)
	if err != nil {
		return err
	}

	log.Printf("[sweep:anyscale_service] listed %d service(s); min-age=%s", len(services), minAge)

	var failures []string
	swept := 0
	for _, s := range services {
		if !hasAnyPrefix(s.Name, sweepableResourcePrefixes) {
			continue
		}

		createdAt, perr := time.Parse(time.RFC3339, s.CreatedAt)
		if perr != nil {
			log.Printf("[sweep:anyscale_service] skip %s (%s): unparseable created_at %q: %v", s.ID, s.Name, s.CreatedAt, perr)
			continue
		}
		if createdAt.After(cutoff) {
			log.Printf("[sweep:anyscale_service] skip %s (%s): too young (created %s)", s.ID, s.Name, s.CreatedAt)
			continue
		}

		if derr := sweepDeleteService(ctx, client, s); derr != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): %v", s.ID, s.Name, derr))
			continue
		}
		swept++
	}

	log.Printf("[sweep:anyscale_service] swept=%d failed=%d", swept, len(failures))
	if len(failures) > 0 {
		return fmt.Errorf("service sweep had %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func listAllServicesForSweep(ctx context.Context, client *provider.Client) ([]sweepServiceResult, error) {
	return provider.PaginatedRequest(ctx, client, "/api/v2/services-v2", url.Values{},
		func(body []byte) ([]sweepServiceResult, *string, error) {
			var page sweepServiceListResponse
			if err := json.Unmarshal(body, &page); err != nil {
				return nil, nil, fmt.Errorf("parse services response: %w", err)
			}
			return page.Results, page.Metadata.NextPagingToken, nil
		},
	)
}

// TestListAllServicesForSweep_MultiPage proves listAllServicesForSweep actually follows
// next_paging_token across pages instead of silently truncating to page one - mirrors
// TestListAllProjectsForSweep_MultiPage's guard for the identical class of bug.
func TestListAllServicesForSweep_MultiPage(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			if got := r.URL.Query().Get("paging_token"); got != "" {
				t.Errorf("first request should not carry a paging_token, got %q", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"results":[{"id":"svc1","name":"tfacc-one","current_state":"RUNNING","created_at":"2020-01-01T00:00:00Z"}],"metadata":{"next_paging_token":"page2"}}`)
			return
		}
		if got := r.URL.Query().Get("paging_token"); got != "page2" {
			t.Errorf("second request should carry paging_token=page2 as a query param, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results":[{"id":"svc2","name":"tfacc-two","current_state":"RUNNING","created_at":"2020-01-01T00:00:00Z"}],"metadata":{"next_paging_token":null}}`)
	}))
	defer server.Close()

	client := provider.NewClientWithToken(server.URL, "test-token")
	services, err := listAllServicesForSweep(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("expected 2 requests (one per page), got %d", requestCount)
	}
	if len(services) != 2 {
		t.Fatalf("expected 2 services across both pages, got %d (silent truncation would show up as a short result here)", len(services))
	}
	if services[0].ID != "svc1" || services[1].ID != "svc2" {
		t.Fatalf("expected [svc1, svc2] in page order, got %+v", services)
	}
}

// sweepDeleteService terminates a leaked test service, waits for it to reach TERMINATED (DELETE
// 400s otherwise), then deletes the record. Mirrors the resource's own Delete lifecycle.
func sweepDeleteService(ctx context.Context, client *provider.Client, s sweepServiceResult) error {
	if isSweepDryRun() {
		log.Printf("[sweep:anyscale_service] DRY-RUN would TERMINATE+DELETE %s (%s)", s.ID, s.Name)
		return nil
	}

	if s.CurrentState != "TERMINATED" {
		resp, err := client.DoRequest(ctx, "POST", fmt.Sprintf("/api/v2/services-v2/%s/terminate", s.ID), nil)
		if err != nil {
			return fmt.Errorf("terminate: %w", err)
		}
		_ = resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusAccepted:
			if err := waitForServiceTerminatedForSweep(ctx, client, s.ID); err != nil {
				return err
			}
		case http.StatusNotFound:
			// Already gone (terminated+deleted out-of-band) - matches the resource's own H1
			// handling; nothing left to wait for or delete.
			log.Printf("[sweep:anyscale_service] %s (%s) already gone at terminate", s.ID, s.Name)
			return nil
		default:
			return fmt.Errorf("terminate: unexpected status %d", resp.StatusCode)
		}
	}

	resp, err := client.DoRequest(ctx, "DELETE", fmt.Sprintf("/api/v2/services-v2/%s", s.ID), nil)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound:
		log.Printf("[sweep:anyscale_service] DELETE OK %s (%s): status=%d", s.ID, s.Name, resp.StatusCode)
		return nil
	default:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete: unexpected status %d: %s", resp.StatusCode, truncateBody(string(body), 256))
	}
}

// waitForServiceTerminatedForSweep polls a service until its current_state reaches TERMINATED, a
// 404 (already gone), or sweepServiceMaxTerminateWait elapses.
func waitForServiceTerminatedForSweep(ctx context.Context, client *provider.Client, serviceID string) error {
	deadline := time.Now().Add(sweepServiceMaxTerminateWait)
	for {
		resp, err := client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/services-v2/%s", serviceID), nil)
		if err != nil {
			return fmt.Errorf("poll for termination: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return nil
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("poll for termination: unexpected status %d", resp.StatusCode)
		}

		var page struct {
			Result struct {
				CurrentState string `json:"current_state"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("parse termination poll response: %w", err)
		}
		if page.Result.CurrentState == "TERMINATED" {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for service %s to terminate (currently %s)",
				sweepServiceMaxTerminateWait, serviceID, page.Result.CurrentState)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sweepServiceTerminatePollTick):
		}
	}
}
