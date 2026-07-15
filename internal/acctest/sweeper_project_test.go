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

// knownPermanentlyStuck403ProjectIDs are project IDs confirmed (see the
// project-delete-403 investigation notes) to have never received their
// SpiceDB owner tuple at creation time: DELETE permanently 403s for any
// caller, including this sweeper, no matter how many times or how long
// after creation it retries. They are intentionally left alive in the test
// org for backend investigation (Anyscale backend eng Matt Weber, as of
// 2026-07-12) and must never be deleted.
//
// These IDs - not just the shape "every failure this run was 403" - are
// what finalizeSweepResult uses to tell "the eternal known specimens,
// which can legitimately be the ONLY sweep candidates on a quiet day" apart
// from "something genuinely broke sweeper-wide (e.g. lost delete access)."
// Prune an entry once backend confirms its tuple was backfilled (delete
// succeeds again) or the project is gone.
var knownPermanentlyStuck403ProjectIDs = map[string]bool{
	"prj_3qdcd9k622abrtbvnrqtd9ifss": true, // tfacc-project-desc-9uc3s9pf
	"prj_suumi2fbarpw2h25ac5jmyheqd": true, // tfacc-project-desc-f9cymgmb (Matt's specimen)
}

// isKnownPermanentlyStuckProject reports whether id is one of the tracked
// permanent-403 specimens above.
func isKnownPermanentlyStuckProject(id string) bool {
	return knownPermanentlyStuck403ProjectIDs[id]
}

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

	var knownStuckFailures []string
	var unknownPermissionDeniedFailures []string
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
		case isSweepPermissionDenied(derr) && isKnownPermanentlyStuckProject(p.ID):
			// A project whose delete-time permission check permanently denies it (the
			// SpiceDB tuple was never written - see the project-delete-403 investigation
			// notes) can never be swept by ANY client, including this one: the sweeper's
			// own DELETE call is denied the identical way a user's would be. Treating this
			// expected, already-tracked condition as a job failure would make the daily
			// sweep permanently red for as long as any such project exists, training
			// everyone to ignore a job that could otherwise still catch a real regression.
			knownStuckFailures = append(knownStuckFailures, fmt.Sprintf("%s (%s): %v", p.ID, p.Name, derr))
		case isSweepPermissionDenied(derr):
			unknownPermissionDeniedFailures = append(unknownPermissionDeniedFailures, fmt.Sprintf("%s (%s): %v", p.ID, p.Name, derr))
		default:
			otherFailures = append(otherFailures, fmt.Sprintf("%s (%s): %v", p.ID, p.Name, derr))
		}
	}

	log.Printf("[sweep:anyscale_project] swept=%d known_stuck=%d unknown_permission_denied=%d other_failed=%d", swept, len(knownStuckFailures), len(unknownPermissionDeniedFailures), len(otherFailures))
	return finalizeSweepResult(swept, knownStuckFailures, unknownPermissionDeniedFailures, otherFailures)
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

// finalizeSweepResult decides the sweep job's overall pass/fail outcome given
// the three failure buckets sweepProjects splits errors into, plus how many
// deletes actually succeeded.
//
// knownStuckFailures (permission-denied on an ID in
// knownPermanentlyStuck403ProjectIDs) are logged loudly - so they stay
// visible, not silently swallowed - but NEVER fail the job and never count
// toward the systemic-regression guard below, no matter how many of them
// there are or whether swept is zero: they are specific, already-identified
// objects, not a shape that might coincidentally mean something else. See
// isSweepPermissionDenied's doc comment for why a sweeper retry can never
// succeed on one anyway.
//
// otherFailures (a non-403 error) always fail the job, so a genuinely
// new/unexpected sweeper problem is never masked.
//
// unknownPermissionDeniedFailures (permission-denied on an ID NOT in the
// known-stuck allowlist) get the "systemic regression" guard: if there were
// resources to act on but EVERY single delete came back permission-denied
// on an unrecognized project and NONE succeeded, that is not "a couple of
// already-known stuck specimens" (those are excluded from this count
// entirely) - it is the shape of a real, systemic permission regression
// (e.g. the sweeper's own credentials lost delete access entirely), and
// fails the job. If at least one delete succeeded, credentials clearly
// still work, so an unrecognized 403 alongside real successes is logged as
// a warning instead - it may be a new permanent specimen worth adding to
// the allowlist, or a slow instance of the transient SpiceDB race - but
// does not fail the job on its own.
func finalizeSweepResult(swept int, knownStuckFailures, unknownPermissionDeniedFailures, otherFailures []string) error {
	if len(knownStuckFailures) > 0 {
		log.Printf("[sweep:anyscale_project] WARNING: %d project(s) could not be swept due to a known, backend-side permanent permission-check issue (not a sweeper bug - see project-delete-403 investigation notes): %s", len(knownStuckFailures), strings.Join(knownStuckFailures, "; "))
	}
	if len(otherFailures) > 0 {
		return fmt.Errorf("project sweep had %d unexpected failure(s): %s", len(otherFailures), strings.Join(otherFailures, "; "))
	}
	if len(unknownPermissionDeniedFailures) == 0 {
		return nil
	}
	if swept == 0 {
		return fmt.Errorf("project sweep had %d permission-denied failure(s) on project(s) not in the known-stuck allowlist and ZERO successful deletes - this looks like a systemic permission regression (e.g. lost delete access), not the tracked known-stuck specimens, so treating it as a failure: %s", len(unknownPermissionDeniedFailures), strings.Join(unknownPermissionDeniedFailures, "; "))
	}
	log.Printf("[sweep:anyscale_project] WARNING: %d project(s) got permission-denied and are NOT in the known-stuck allowlist (possibly a new permanent specimen worth investigating/tracking, or a slow instance of the transient SpiceDB race) - not failing the job since %d other delete(s) succeeded: %s", len(unknownPermissionDeniedFailures), swept, strings.Join(unknownPermissionDeniedFailures, "; "))
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

// TestIsKnownPermanentlyStuckProject proves the exact allowlist finalizeSweepResult's
// systemic-regression guard relies on to tell the two tracked permanent-403
// specimens apart from any other project ID.
func TestIsKnownPermanentlyStuckProject(t *testing.T) {
	for id := range knownPermanentlyStuck403ProjectIDs {
		if !isKnownPermanentlyStuckProject(id) {
			t.Errorf("expected tracked specimen %s to be classified as known-stuck", id)
		}
	}
	if isKnownPermanentlyStuckProject("prj_some_unrelated_project_id") {
		t.Error("expected an ID absent from the allowlist to NOT be classified as known-stuck")
	}
	if isKnownPermanentlyStuckProject("") {
		t.Error("expected an empty ID to NOT be classified as known-stuck")
	}
}

// TestFinalizeSweepResult is the mutation-proof guard for two distinct bugs
// this investigation found in real daily sweep runs, both in
// internal/acctest/sweeper_project_test.go:
//
//  1. Run 29179390163 (2026-07-12): two projects permanently 403 on delete
//     (their SpiceDB owner tuple was never written - see the
//     project-delete-403 investigation notes), and the sweeper's own DELETE
//     call is denied the identical way, every single day, forever, until
//     backend backfills the tuple. Before the PR #99 fix, ANY sweep failure
//     (permission-denied or not) failed the whole job.
//  2. Run 29304949946 (2026-07-14): the PR #99 fix's own "zero swept + all
//     permission-denied = systemic regression" guard did not distinguish
//     WHICH projects were permission-denied - so on a quiet day where the
//     ONLY two sweep candidates happened to be exactly those same two
//     already-tracked specimens (nothing else left to sweep that run), the
//     guard misfired and failed the job anyway, even though this is the
//     textbook expected case, not a regression.
//
// Both are exercised below: a request naming ONLY tracked specimen IDs must
// never fail the job regardless of swept count, while a request naming an
// unrecognized ID still trips the systemic-regression guard when nothing else
// succeeded - so a real credentials-loss regression is still caught.
func TestFinalizeSweepResult(t *testing.T) {
	t.Run("known-stuck failures alongside real successes do not fail the job", func(t *testing.T) {
		err := finalizeSweepResult(18, []string{"prj_3qdcd9k622abrtbvnrqtd9ifss (tfacc-project-desc-9uc3s9pf): status 403: Permission denied"}, nil, nil)
		if err != nil {
			t.Fatalf("expected nil (job should NOT fail on known-stuck failures when other deletes succeeded), got: %v", err)
		}
	})

	t.Run("unknown permission-denied failures alongside real successes do not fail the job", func(t *testing.T) {
		err := finalizeSweepResult(18, nil, []string{"prj_z (proj-z): status 403: Permission denied"}, nil)
		if err != nil {
			t.Fatalf("expected nil (job should NOT fail on a lone unrecognized permission-denied failure when other deletes succeeded), got: %v", err)
		}
	})

	t.Run("no failures at all does not fail the job", func(t *testing.T) {
		if err := finalizeSweepResult(5, nil, nil, nil); err != nil {
			t.Fatalf("expected nil, got: %v", err)
		}
	})

	t.Run("nothing to sweep (swept=0, no failures at all) does not fail the job", func(t *testing.T) {
		if err := finalizeSweepResult(0, nil, nil, nil); err != nil {
			t.Fatalf("expected nil - zero swept with zero failures just means nothing needed sweeping, got: %v", err)
		}
	})

	t.Run("all sweep candidates being known-stuck specimens and zero succeeding does NOT fail the job (run 29304949946, 2026-07-14)", func(t *testing.T) {
		err := finalizeSweepResult(
			0,
			[]string{
				"prj_3qdcd9k622abrtbvnrqtd9ifss (tfacc-project-desc-9uc3s9pf): status 403: Permission denied",
				"prj_suumi2fbarpw2h25ac5jmyheqd (tfacc-project-desc-f9cymgmb): status 403: Permission denied",
			},
			nil,
			nil,
		)
		if err != nil {
			t.Fatalf("expected nil - zero swept alongside ONLY known-stuck failures is the fully expected quiet-day shape, not a regression, got: %v", err)
		}
	})

	t.Run("any other failure still fails the job, even alongside known-stuck ones", func(t *testing.T) {
		err := finalizeSweepResult(
			18,
			[]string{"prj_a (proj-a): status 403: Permission denied"},
			nil,
			[]string{"prj_b (proj-b): status 500: internal server error"},
		)
		if err == nil {
			t.Fatal("expected an error - an unexpected failure must never be silently absorbed by the known-stuck carve-out")
		}
		if !strings.Contains(err.Error(), "1 unexpected failure") || !strings.Contains(err.Error(), "prj_b") {
			t.Fatalf("expected the error to identify exactly the unexpected (non-403) failure, got: %v", err)
		}
		if strings.Contains(err.Error(), "prj_a") {
			t.Fatalf("expected the known-stuck failure to be excluded from the job-failing error, got: %v", err)
		}
	})

	t.Run("multiple other failures are all reported", func(t *testing.T) {
		err := finalizeSweepResult(18, nil, nil, []string{"prj_b: status 500: boom", "prj_c: status 502: boom"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "2 unexpected failure") {
			t.Fatalf("expected the count to reflect both unexpected failures, got: %v", err)
		}
	})

	t.Run("all deletes permission-denied on unrecognized projects and zero succeeded fails the job (systemic regression shape, not known-stuck specimens)", func(t *testing.T) {
		err := finalizeSweepResult(0, nil, []string{"prj_x: status 403: denied", "prj_y: status 403: denied", "prj_z: status 403: denied"}, nil)
		if err == nil {
			t.Fatal("expected an error - 100% permission-denied on unrecognized projects with zero successes must fail loudly, not be absorbed as 'known-stuck specimens'")
		}
		if !strings.Contains(err.Error(), "systemic permission regression") {
			t.Fatalf("expected the error to name this as a systemic-regression shape, got: %v", err)
		}
	})

	t.Run("a mix of known-stuck and unrecognized permission-denied with zero succeeding still fails, naming only the unrecognized ones", func(t *testing.T) {
		err := finalizeSweepResult(
			0,
			[]string{"prj_3qdcd9k622abrtbvnrqtd9ifss (tfacc-project-desc-9uc3s9pf): status 403: Permission denied"},
			[]string{"prj_z (proj-z): status 403: Permission denied"},
			nil,
		)
		if err == nil {
			t.Fatal("expected an error - an unrecognized 403 alongside zero successes must still fail even when a known-stuck one is also present")
		}
		if !strings.Contains(err.Error(), "systemic permission regression") || !strings.Contains(err.Error(), "prj_z") {
			t.Fatalf("expected the error to name the systemic-regression shape and the unrecognized project, got: %v", err)
		}
		if strings.Contains(err.Error(), "prj_3qdcd9k622abrtbvnrqtd9ifss") {
			t.Fatalf("expected the known-stuck failure to be excluded from the job-failing error, got: %v", err)
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
