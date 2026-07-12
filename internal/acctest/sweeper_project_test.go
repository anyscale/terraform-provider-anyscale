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

	var permissionDeniedFailures []string
	var otherFailures []string
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

		derr := sweepDeleteProject(ctx, client, p)
		switch {
		case derr == nil:
			swept++
		case isSweepPermissionDenied(derr):
			// A project whose delete-time permission check permanently denies it (the
			// SpiceDB tuple was never written - see the project-delete-403 investigation
			// notes) can never be swept by ANY client, including this one: the sweeper's
			// own DELETE call is denied the identical way a user's would be. Treating this
			// expected, already-tracked condition as a job failure would make the daily
			// sweep permanently red for as long as any such project exists, training
			// everyone to ignore a job that could otherwise still catch a real regression.
			permissionDeniedFailures = append(permissionDeniedFailures, fmt.Sprintf("%s (%s): %v", p.ID, p.Name, derr))
		default:
			otherFailures = append(otherFailures, fmt.Sprintf("%s (%s): %v", p.ID, p.Name, derr))
		}
	}

	log.Printf("[sweep:anyscale_project] swept=%d permission_denied=%d other_failed=%d", swept, len(permissionDeniedFailures), len(otherFailures))
	return finalizeSweepResult(swept, permissionDeniedFailures, otherFailures)
}

// isSweepPermissionDenied reports whether a sweepDeleteProject error is the known,
// backend-side permanent permission-check denial (see the project-delete-403
// investigation notes), as opposed to an unexpected failure (network error, auth
// failure, an unexpected status code). sweepDeleteProject formats HTTP-status
// failures as "status %d: ...", matching the same convention resource_project.go's
// deleteProjectWithRetry uses to detect a 403.
func isSweepPermissionDenied(err error) bool {
	return err != nil && strings.Contains(err.Error(), "status 403")
}

// finalizeSweepResult decides the sweep job's overall pass/fail outcome given the
// two failure buckets isSweepPermissionDenied splits errors into, plus how many
// deletes actually succeeded. Permission-denied failures are logged loudly (so
// they stay visible, not silently swallowed) but do NOT fail the job on their
// own - see isSweepPermissionDenied's doc comment for why a sweeper retry can
// never succeed on one anyway. Any OTHER failure still fails the job, so a
// genuinely new/unexpected sweeper problem is never masked by this carve-out.
//
// One guard on top of that: if there were resources to act on but EVERY single
// delete came back permission-denied and NONE succeeded, that is not "a couple
// of already-known stuck specimens" - it is the shape of a real, systemic
// permission regression (e.g. the sweeper's own credentials lost delete access
// entirely), and gets treated as a job failure too, not a quiet warning.
func finalizeSweepResult(swept int, permissionDeniedFailures, otherFailures []string) error {
	if len(permissionDeniedFailures) > 0 {
		log.Printf("[sweep:anyscale_project] WARNING: %d project(s) could not be swept due to a known, backend-side permanent permission-check issue (not a sweeper bug - see project-delete-403 investigation notes): %s", len(permissionDeniedFailures), strings.Join(permissionDeniedFailures, "; "))
	}
	if len(otherFailures) > 0 {
		return fmt.Errorf("project sweep had %d unexpected failure(s): %s", len(otherFailures), strings.Join(otherFailures, "; "))
	}
	if swept == 0 && len(permissionDeniedFailures) > 0 {
		return fmt.Errorf("project sweep had %d permission-denied failure(s) and ZERO successful deletes - this looks like a systemic permission regression (e.g. lost delete access), not a couple of known-stuck specimens, so treating it as a failure: %s", len(permissionDeniedFailures), strings.Join(permissionDeniedFailures, "; "))
	}
	return nil
}

func listAllProjectsForSweep(ctx context.Context, client *provider.Client) ([]sweepProjectResult, error) {
	return provider.PaginatedRequest(ctx, client, "/api/v2/projects", url.Values{},
		func(body []byte) ([]sweepProjectResult, *string, error) {
			var page sweepProjectListResponse
			if err := json.Unmarshal(body, &page); err != nil {
				return nil, nil, fmt.Errorf("parse projects response: %w", err)
			}
			return page.Results, page.Metadata.NextPagingToken, nil
		},
	)
}

// TestListAllProjectsForSweep_MultiPage proves listAllProjectsForSweep
// actually follows next_paging_token across pages instead of silently
// truncating to page one - a truncation bug here would not error, it would
// just make the sweeper blind to leaked projects on later pages.
func TestListAllProjectsForSweep_MultiPage(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			if got := r.URL.Query().Get("paging_token"); got != "" {
				t.Errorf("first request should not carry a paging_token, got %q", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"results":[{"id":"p1","name":"tfacc-one","is_default":false,"created_at":"2020-01-01T00:00:00Z"}],"metadata":{"next_paging_token":"page2"}}`)
			return
		}
		if got := r.URL.Query().Get("paging_token"); got != "page2" {
			t.Errorf("second request should carry paging_token=page2 as a query param, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results":[{"id":"p2","name":"tfacc-two","is_default":false,"created_at":"2020-01-01T00:00:00Z"}],"metadata":{"next_paging_token":null}}`)
	}))
	defer server.Close()

	client := provider.NewClientWithToken(server.URL, "test-token")
	projects, err := listAllProjectsForSweep(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("expected 2 requests (one per page), got %d", requestCount)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects across both pages, got %d (silent truncation would show up as a short result here)", len(projects))
	}
	if projects[0].ID != "p1" || projects[1].ID != "p2" {
		t.Fatalf("expected [p1, p2] in page order, got %+v", projects)
	}
}

// TestIsSweepPermissionDenied proves the exact classification the sweep's
// pass/fail decision hinges on: a status-403 error (the known, permanent,
// backend-side permission-check denial) is recognized, everything else is not.
func TestIsSweepPermissionDenied(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"status 403", fmt.Errorf("status 403: {\"error\":{\"detail\":\"Permission denied\"}}"), true},
		{"status 404", fmt.Errorf("status 404: not found"), false},
		{"status 500", fmt.Errorf("status 500: internal server error"), false},
		{"network error", fmt.Errorf("dial tcp: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSweepPermissionDenied(tc.err); got != tc.want {
				t.Errorf("isSweepPermissionDenied(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestFinalizeSweepResult is the mutation-proof guard for the actual bug this
// investigation found in the real daily sweep run (29179390163, 2026-07-12):
// two projects permanently 403 on delete (their SpiceDB owner tuple was never
// written - see the project-delete-403 investigation notes), and the sweeper's
// own DELETE call is denied the identical way, every single day, forever, until
// backend backfills the tuple. Before this fix, ANY sweep failure (permission-
// denied or not) failed the whole job - meaning the daily sweep would show
// FAILED in CI every day for as long as those two projects exist, training
// everyone to ignore a job that could otherwise still catch a real regression.
func TestFinalizeSweepResult(t *testing.T) {
	t.Run("permission-denied-only failures alongside real successes do not fail the job", func(t *testing.T) {
		err := finalizeSweepResult(18, []string{"prj_a (proj-a): status 403: Permission denied"}, nil)
		if err != nil {
			t.Fatalf("expected nil (job should NOT fail on known permission-denied failures when other deletes succeeded), got: %v", err)
		}
	})

	t.Run("no failures at all does not fail the job", func(t *testing.T) {
		if err := finalizeSweepResult(5, nil, nil); err != nil {
			t.Fatalf("expected nil, got: %v", err)
		}
	})

	t.Run("nothing to sweep (swept=0, no failures at all) does not fail the job", func(t *testing.T) {
		if err := finalizeSweepResult(0, nil, nil); err != nil {
			t.Fatalf("expected nil - zero swept with zero failures just means nothing needed sweeping, got: %v", err)
		}
	})

	t.Run("any other failure still fails the job, even alongside permission-denied ones", func(t *testing.T) {
		err := finalizeSweepResult(
			18,
			[]string{"prj_a (proj-a): status 403: Permission denied"},
			[]string{"prj_b (proj-b): status 500: internal server error"},
		)
		if err == nil {
			t.Fatal("expected an error - an unexpected failure must never be silently absorbed by the permission-denied carve-out")
		}
		if !strings.Contains(err.Error(), "1 unexpected failure") || !strings.Contains(err.Error(), "prj_b") {
			t.Fatalf("expected the error to identify exactly the unexpected (non-403) failure, got: %v", err)
		}
		if strings.Contains(err.Error(), "prj_a") {
			t.Fatalf("expected the permission-denied failure to be excluded from the job-failing error, got: %v", err)
		}
	})

	t.Run("multiple other failures are all reported", func(t *testing.T) {
		err := finalizeSweepResult(18, nil, []string{"prj_b: status 500: boom", "prj_c: status 502: boom"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "2 unexpected failure") {
			t.Fatalf("expected the count to reflect both unexpected failures, got: %v", err)
		}
	})

	t.Run("all deletes permission-denied and zero succeeded fails the job (systemic regression shape, not a couple stuck specimens)", func(t *testing.T) {
		err := finalizeSweepResult(0, []string{"prj_a: status 403: denied", "prj_b: status 403: denied", "prj_c: status 403: denied"}, nil)
		if err == nil {
			t.Fatal("expected an error - 100% permission-denied with zero successes must fail loudly, not be absorbed as 'a couple of known-stuck specimens'")
		}
		if !strings.Contains(err.Error(), "systemic permission regression") {
			t.Fatalf("expected the error to name this as a systemic-regression shape, got: %v", err)
		}
	})
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
