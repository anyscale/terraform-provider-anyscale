package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The hand-rolled pagination loops (the POST searches that cannot use the
// GET-only PaginatedRequest) must stop with an error when the API repeats a
// paging token, instead of requesting the same page forever.

// repeatedTokenRequestCap bounds every mock below. A loop that never stops
// gets a 500 carrying repeatedTokenCapMarker once it passes the cap, so a
// regression fails the test instead of hanging CI.
const (
	repeatedTokenRequestCap = 8
	repeatedTokenCapMarker  = "pagination test request cap reached"
)

// pagingScript maps the incoming paging_token ("" for the first request) to
// the next_paging_token the response carries; nil ends pagination. Each page
// holds one result whose id is "item-<incoming token>".
type pagingScript map[string]*string

func tok(s string) *string { return &s }

func newPagingScriptServer(t *testing.T, method, path string, script pagingScript) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method || r.URL.Path != path {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if requests.Add(1) > repeatedTokenRequestCap {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"detail":"` + repeatedTokenCapMarker + `"}}`))
			return
		}
		in := r.URL.Query().Get("paging_token")
		next, ok := script[in]
		if !ok {
			t.Errorf("request with unscripted paging_token %q", in)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results":  []map[string]any{{"id": "item-" + in, "name": "item-" + in, "created_at": "2026-01-01T00:00:00Z"}},
			"metadata": map[string]any{"total": 99, "next_paging_token": next},
		})
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

// The non-advancing and cycling scripts, and the requests each loop may make
// before it must notice: the repeat is visible in the response to the second
// request (A->A) or the third (A->B->A).
var repeatedTokenCases = []struct {
	name     string
	script   pagingScript
	requests int32
}{
	{"same_token_twice", pagingScript{"": tok("pg-tok-A"), "pg-tok-A": tok("pg-tok-A")}, 2},
	{"cycle_A_B_A", pagingScript{"": tok("pg-tok-A"), "pg-tok-A": tok("pg-tok-B"), "pg-tok-B": tok("pg-tok-A")}, 3},
}

// distinctTokensScript is the positive control: two pages with distinct
// tokens, then the end. Every item must come back.
var distinctTokensScript = pagingScript{"": tok("pg-tok-A"), "pg-tok-A": nil}

func assertRepeatedTokenError(t *testing.T, err error, requests *atomic.Int32, want int32) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error for a repeated paging token, got nil after %d requests", requests.Load())
	}
	if strings.Contains(err.Error(), repeatedTokenCapMarker) {
		t.Fatalf("the loop never noticed the repeated token; it ran into the mock's %d-request cap: %v", repeatedTokenRequestCap, err)
	}
	if !strings.Contains(err.Error(), "pg-tok-A") {
		t.Errorf("error should name the repeated token %q: %v", "pg-tok-A", err)
	}
	if got := requests.Load(); got != want {
		t.Errorf("made %d requests, want exactly %d (stop on the response that repeats the token)", got, want)
	}
}

func repeatedTokenCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSearchCloudCollaborators_RepeatedPagingToken(t *testing.T) {
	const cloudID = "cld_repeat"
	path := "/api/v2/clouds/" + cloudID + "/collaborators/users/search"
	for _, tc := range repeatedTokenCases {
		t.Run(tc.name, func(t *testing.T) {
			server, requests := newPagingScriptServer(t, http.MethodPost, path, tc.script)
			_, err := searchCloudCollaborators(repeatedTokenCtx(t), NewClientWithToken(server.URL, "test-token"), cloudID)
			assertRepeatedTokenError(t, err, requests, tc.requests)
		})
	}
	t.Run("distinct_tokens_positive_control", func(t *testing.T) {
		server, requests := newPagingScriptServer(t, http.MethodPost, path, distinctTokensScript)
		got, err := searchCloudCollaborators(repeatedTokenCtx(t), NewClientWithToken(server.URL, "test-token"), cloudID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 || got[0].ID != "item-" || got[1].ID != "item-pg-tok-A" || requests.Load() != 2 {
			t.Errorf("got %d results over %d requests, want both pages' items over 2 requests: %+v", len(got), requests.Load(), got)
		}
	})
}

func TestSearchComputeTemplatesPaged_RepeatedPagingToken(t *testing.T) {
	const path = "/api/v2/compute_templates/search"
	payload := func() map[string]interface{} {
		return map[string]interface{}{"name": map[string]string{"equals": "repeat"}}
	}
	for _, tc := range repeatedTokenCases {
		t.Run(tc.name, func(t *testing.T) {
			server, requests := newPagingScriptServer(t, http.MethodPost, path, tc.script)
			_, err := searchComputeTemplatesPaged(repeatedTokenCtx(t), NewClientWithToken(server.URL, "test-token"), payload())
			assertRepeatedTokenError(t, err, requests, tc.requests)
		})
	}
	t.Run("distinct_tokens_positive_control", func(t *testing.T) {
		server, requests := newPagingScriptServer(t, http.MethodPost, path, distinctTokensScript)
		got, err := searchComputeTemplatesPaged(repeatedTokenCtx(t), NewClientWithToken(server.URL, "test-token"), payload())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 || requests.Load() != 2 {
			t.Errorf("got %d results over %d requests, want 2 over 2: %+v", len(got), requests.Load(), got)
		}
	})
}
