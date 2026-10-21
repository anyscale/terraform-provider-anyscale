package acctest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

// The acctest helpers' hand-rolled pagination loops must stop with an error
// when the API repeats a paging token, instead of requesting the same page
// until the go test timeout. These run without credentials.

// acctestRepeatedTokenCap bounds every mock below. A loop that never stops
// gets a 500 carrying acctestRepeatedTokenCapMarker once it passes the cap,
// so a regression fails the test instead of hanging CI.
const (
	acctestRepeatedTokenCap       = 8
	acctestRepeatedTokenCapMarker = "pagination test request cap reached"
)

// acctestPagingScript maps the incoming paging_token ("" for the first
// request) to the next_paging_token of the response; nil ends pagination.
// Each page holds one result named "item-<incoming token>".
type acctestPagingScript map[string]*string

func acctestTok(s string) *string { return &s }

func newAcctestPagingScriptServer(t *testing.T, method, path string, script acctestPagingScript) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method || r.URL.Path != path {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if requests.Add(1) > acctestRepeatedTokenCap {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"detail":"` + acctestRepeatedTokenCapMarker + `"}}`))
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

var acctestRepeatedTokenCases = []struct {
	name     string
	script   acctestPagingScript
	requests int32
}{
	{"same_token_twice", acctestPagingScript{"": acctestTok("pg-tok-A"), "pg-tok-A": acctestTok("pg-tok-A")}, 2},
	{"cycle_A_B_A", acctestPagingScript{"": acctestTok("pg-tok-A"), "pg-tok-A": acctestTok("pg-tok-B"), "pg-tok-B": acctestTok("pg-tok-A")}, 3},
}

var acctestDistinctTokensScript = acctestPagingScript{"": acctestTok("pg-tok-A"), "pg-tok-A": nil}

func assertAcctestRepeatedTokenError(t *testing.T, err error, requests *atomic.Int32, want int32) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error for a repeated paging token, got nil after %d requests", requests.Load())
	}
	if strings.Contains(err.Error(), acctestRepeatedTokenCapMarker) {
		t.Fatalf("the loop never noticed the repeated token; it ran into the mock's %d-request cap: %v", acctestRepeatedTokenCap, err)
	}
	if !strings.Contains(err.Error(), "pg-tok-A") {
		t.Errorf("error should name the repeated token %q: %v", "pg-tok-A", err)
	}
	if got := requests.Load(); got != want {
		t.Errorf("made %d requests, want exactly %d (stop on the response that repeats the token)", got, want)
	}
}

func TestSearchComputeConfigsByContains_RepeatedPagingToken(t *testing.T) {
	const path = "/api/v2/compute_templates/search"
	ctx := func(t *testing.T) context.Context {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		t.Cleanup(cancel)
		return c
	}
	for _, tc := range acctestRepeatedTokenCases {
		t.Run(tc.name, func(t *testing.T) {
			server, requests := newAcctestPagingScriptServer(t, http.MethodPost, path, tc.script)
			_, err := searchComputeConfigsByContains(ctx(t), provider.NewClientWithToken(server.URL, "test-token"), "tfacc-repeat")
			assertAcctestRepeatedTokenError(t, err, requests, tc.requests)
		})
	}
	t.Run("distinct_tokens_positive_control", func(t *testing.T) {
		server, requests := newAcctestPagingScriptServer(t, http.MethodPost, path, acctestDistinctTokensScript)
		got, err := searchComputeConfigsByContains(ctx(t), provider.NewClientWithToken(server.URL, "test-token"), "item-")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 || requests.Load() != 2 {
			t.Errorf("got %d results over %d requests, want 2 over 2: %+v", len(got), requests.Load(), got)
		}
	})
}

// resolveCloudNameToIDCore builds its client from the environment and uses
// context.Background, so the request cap above is the only thing standing
// between a regression and the CI timeout.
func TestResolveCloudNameToIDCore_RepeatedPagingToken(t *testing.T) {
	for _, tc := range acctestRepeatedTokenCases {
		t.Run(tc.name, func(t *testing.T) {
			server, requests := newAcctestPagingScriptServer(t, http.MethodGet, "/api/v2/clouds", tc.script)
			t.Setenv("ANYSCALE_API_URL", server.URL)
			t.Setenv("ANYSCALE_CLI_TOKEN", "test-token")
			_, _, err := resolveCloudNameToIDCore("no-such-cloud")
			assertAcctestRepeatedTokenError(t, err, requests, tc.requests)
		})
	}
	t.Run("distinct_tokens_positive_control", func(t *testing.T) {
		server, requests := newAcctestPagingScriptServer(t, http.MethodGet, "/api/v2/clouds", acctestDistinctTokensScript)
		t.Setenv("ANYSCALE_API_URL", server.URL)
		t.Setenv("ANYSCALE_CLI_TOKEN", "test-token")
		// The only cloud named "item-pg-tok-A" is on the second page.
		id, n, err := resolveCloudNameToIDCore("item-pg-tok-A")
		if err != nil || id != "item-pg-tok-A" || n != 1 || requests.Load() != 2 {
			t.Errorf("got id=%q matches=%d err=%v over %d requests, want the page-2 cloud over 2 requests", id, n, err, requests.Load())
		}
	})
}
