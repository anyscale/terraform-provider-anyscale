package acctest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The sweeper half of the GATE-11 Part B orphan-prevention proof.
// sweepContainerImageResult (see sweeper_container_image_test.go) carries no
// build-related field at all -- ID, Name, CreatedAt, DeletedAt, Anonymous,
// IsDefault -- so a template-without-a-build candidate is not a special case
// the sweeper has to detect, it's the only shape the sweeper ever sees.
// What had zero test coverage before this file is sweepContainerImages
// itself: only its search helper (searchContainerImagesByContains, in
// sweeper_container_image_pagination_test.go) was tested in isolation. These
// tests drive the real top-level orchestration -- search, cross-prefix dedup,
// age filter, prefix filter, already-archived filter, archive call -- against
// a mock server, using t.Setenv to redirect the package-internal
// GetTestClient() call the same way helpers_checkdestroy_test.go does.

// buildlessSweepCandidate builds the search-result shape for a template that
// was created but never got a build -- the exact orphan a failed registry
// Create() call 2 leaves behind (see
// resource_container_image_registry_orphan_prevention_test.go).
func buildlessSweepCandidate(id, name string, createdAt time.Time, deletedAt *string) sweepContainerImageResult {
	return sweepContainerImageResult{
		ID:        id,
		Name:      name,
		CreatedAt: createdAt.UTC().Format(time.RFC3339),
		DeletedAt: deletedAt,
		Anonymous: false,
		IsDefault: false,
	}
}

// newBuildlessSweepServer wires a mock server that answers all 3 configured
// sweepContainerImagePrefixes searches (tfacc-, tf-test-, tfprovider-) --
// returning candidate on the first search only, so cross-prefix dedup logic
// isn't required to make the candidate appear exactly once -- and records
// archive calls. Fails the test on any other request.
func newBuildlessSweepServer(t *testing.T, candidate sweepContainerImageResult) (*httptest.Server, *int, *[]string) {
	t.Helper()
	searchCalls := 0
	var archivedPaths []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/ext/v0/cluster_environments/search":
			searchCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if searchCalls == 1 {
				_ = json.NewEncoder(w).Encode(sweepContainerImageListResponse{
					Results: []sweepContainerImageResult{candidate},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(sweepContainerImageListResponse{})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/application_templates/"+candidate.ID+"/archive":
			archivedPaths = append(archivedPaths, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-sweep-buildless")
	t.Setenv("ANYSCALE_SWEEP_DRY_RUN", "")
	t.Setenv("ANYSCALE_SWEEP_MIN_AGE", "")

	return server, &searchCalls, &archivedPaths
}

// TestSweepContainerImages_BuildlessOrphan_ArchivesCleanly is the main GATE
// proof: a build-less template old enough to sweep, correctly prefixed, not
// anonymous/default, and not yet archived must be archived exactly once, with
// no error -- using nothing but the candidate's own id.
func TestSweepContainerImages_BuildlessOrphan_ArchivesCleanly(t *testing.T) {
	const templateID = "apptemp_sweep_buildless_orphan"
	candidate := buildlessSweepCandidate(templateID, "tfacc-buildless-orphan-sweep", time.Now().Add(-24*time.Hour), nil)

	server, searchCalls, archivedPaths := newBuildlessSweepServer(t, candidate)
	defer server.Close()

	if err := sweepContainerImages(""); err != nil {
		t.Fatalf("sweepContainerImages returned an error for a build-less orphan: %v", err)
	}

	if *searchCalls != len(sweepContainerImagePrefixes) {
		t.Fatalf("expected %d search calls (one per configured prefix), got %d -- did GetTestClient resolve the mock server?", len(sweepContainerImagePrefixes), *searchCalls)
	}
	if len(*archivedPaths) != 1 {
		t.Fatalf("expected exactly 1 archive call, got %d -- build-less orphan was not swept", len(*archivedPaths))
	}
	wantPath := "/api/v2/application_templates/" + templateID + "/archive"
	if (*archivedPaths)[0] != wantPath {
		t.Errorf("archived path = %q, want %q", (*archivedPaths)[0], wantPath)
	}
}

// TestSweepContainerImages_BuildlessOrphan_TooYoungIsKept proves the age
// guard evaluates a build-less candidate the same as any other: a template
// created inside the min-age window must be kept, not archived, and the sweep
// must still complete without error.
func TestSweepContainerImages_BuildlessOrphan_TooYoungIsKept(t *testing.T) {
	const templateID = "apptemp_sweep_buildless_toofresh"
	candidate := buildlessSweepCandidate(templateID, "tfacc-buildless-too-young", time.Now().Add(-1*time.Minute), nil)

	server, _, archivedPaths := newBuildlessSweepServer(t, candidate)
	defer server.Close()

	if err := sweepContainerImages(""); err != nil {
		t.Fatalf("sweepContainerImages returned an error: %v", err)
	}
	if len(*archivedPaths) != 0 {
		t.Fatalf("expected 0 archive calls for a too-young build-less candidate, got %d", len(*archivedPaths))
	}
}

// TestSweepContainerImages_BuildlessOrphan_AlreadyArchivedIsSkipped proves the
// already-archived guard (DeletedAt set) also works for a build-less
// candidate: no redundant archive call, no error.
func TestSweepContainerImages_BuildlessOrphan_AlreadyArchivedIsSkipped(t *testing.T) {
	const templateID = "apptemp_sweep_buildless_alreadygone"
	deletedAt := "2024-01-01T00:00:01Z"
	candidate := buildlessSweepCandidate(templateID, "tfacc-buildless-already-archived", time.Now().Add(-24*time.Hour), &deletedAt)

	server, _, archivedPaths := newBuildlessSweepServer(t, candidate)
	defer server.Close()

	if err := sweepContainerImages(""); err != nil {
		t.Fatalf("sweepContainerImages returned an error: %v", err)
	}
	if len(*archivedPaths) != 0 {
		t.Fatalf("expected 0 archive calls for an already-archived build-less candidate, got %d", len(*archivedPaths))
	}
}
