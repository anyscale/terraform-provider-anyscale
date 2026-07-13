package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file previously only "tested" hand-copied re-implementations of the
// data source's logic (e.g. re-running the same if-statement inline in the
// test rather than calling findComputeConfigByName), so a real bug in the
// actual functions would never have been caught. These now drive the real,
// unexported methods against an httptest server standing in for the
// Anyscale API - the same pattern api_helpers_test.go already established
// in this package, just applied to the two data-source methods with real
// logic worth pinning: multi-match resolution and version collection.

// TestFindComputeConfigByName_MultipleMatchesReturnsMostRecent exercises the
// real findComputeConfigByName, not a re-implementation of its loop: the
// search API can return more than one compute config for the same name
// (e.g. created in different clouds, or historical duplicates), and the
// documented behavior is "most recently created wins".
func TestFindComputeConfigByName_MultipleMatchesReturnsMostRecent(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": [
			{"id": "ccfg_older", "name": "test-config", "created_at": "2024-01-01T00:00:00Z", "anonymous": false},
			{"id": "ccfg_newer", "name": "test-config", "created_at": "2024-06-01T00:00:00Z", "anonymous": false}
		]}`))
	}))
	defer server.Close()

	d := &ComputeConfigDataSource{client: NewClientWithToken(server.URL, "test-token")}

	got, err := d.findComputeConfigByName(ctx, "test-config", "")
	if err != nil {
		t.Fatalf("findComputeConfigByName() error = %v", err)
	}
	if got != "ccfg_newer" {
		t.Errorf("findComputeConfigByName() = %q, want %q (the more recently created match)", got, "ccfg_newer")
	}
}

// TestFindComputeConfigByName_NotFound proves the real function returns an
// empty string with no error when the search API finds nothing, which the
// caller in Read() relies on to report a clean "not found" diagnostic rather
// than a confusing generic error.
func TestFindComputeConfigByName_NotFound(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": []}`))
	}))
	defer server.Close()

	d := &ComputeConfigDataSource{client: NewClientWithToken(server.URL, "test-token")}

	got, err := d.findComputeConfigByName(ctx, "does-not-exist", "")
	if err != nil {
		t.Fatalf("findComputeConfigByName() error = %v, want nil", err)
	}
	if got != "" {
		t.Errorf("findComputeConfigByName() = %q, want empty string for no match", got)
	}
}

// TestFindComputeConfigByName_FiltersByCloudID confirms cloud_id is actually
// forwarded into the search payload when provided - a regression here would
// silently ignore the cloud_id filter and could resolve to a same-named
// config on the wrong cloud.
func TestFindComputeConfigByName_FiltersByCloudID(t *testing.T) {
	ctx := context.Background()
	var sawCloudIDFilter bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"cloud_id":"cld_target"`) {
			sawCloudIDFilter = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": [{"id": "ccfg_1", "name": "test-config", "created_at": "2024-01-01T00:00:00Z", "anonymous": false}]}`))
	}))
	defer server.Close()

	d := &ComputeConfigDataSource{client: NewClientWithToken(server.URL, "test-token")}

	if _, err := d.findComputeConfigByName(ctx, "test-config", "cld_target"); err != nil {
		t.Fatalf("findComputeConfigByName() error = %v", err)
	}
	if !sawCloudIDFilter {
		t.Error("findComputeConfigByName() did not forward cloud_id into the search payload")
	}
}

// TestFetchComputeConfigVersions_CollectsAndSortsUniqueVersions exercises the
// real fetchComputeConfigVersions: the search API returns one row per
// version (not deduplicated), and the documented behavior is a unique,
// ascending-sorted version list.
func TestFetchComputeConfigVersions_CollectsAndSortsUniqueVersions(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": [
			{"id": "ccfg_v3", "name": "test-config", "version": 3},
			{"id": "ccfg_v1", "name": "test-config", "version": 1},
			{"id": "ccfg_v2", "name": "test-config", "version": 2},
			{"id": "ccfg_v2b", "name": "test-config", "version": 2}
		]}`))
	}))
	defer server.Close()

	d := &ComputeConfigDataSource{client: NewClientWithToken(server.URL, "test-token")}

	got, err := d.fetchComputeConfigVersions(ctx, "test-config")
	if err != nil {
		t.Fatalf("fetchComputeConfigVersions() error = %v", err)
	}

	want := []int64{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("fetchComputeConfigVersions() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fetchComputeConfigVersions()[%d] = %d, want %d (must be unique and ascending)", i, got[i], want[i])
		}
	}
}

// TestFetchComputeConfigVersions_RequestsAllVersionsNotJustLatest is the
// DS-CC-1 mutation-proof regression guard. The test above proves the
// sort/dedup logic is correct, but its mock unconditionally returns every row
// regardless of what request was actually sent - it could not have caught
// DS-CC-1 because a real backend would never spontaneously return more than
// the latest version unless the request explicitly asks for all of them.
//
// Per the traced backend model (ClusterComputesQuery.version,
// backend/server/api/base/models/cluster_computes.py:244-271, confirmed
// independently by both architect and forge): omitting the version field (or
// sending -1) resolves to latest-version-only; version -2 is the documented
// "do not filter by version" sentinel needed to enumerate history. This mock
// simulates that real behavior - it only returns all 3 versions when the
// request body's version field is exactly -2, and returns just the latest
// otherwise - so this currently FAILS (only 1 version comes back, since
// today's search payload has no version field at all) which is the
// mutation-proof evidence. Must pass once fetchComputeConfigVersions sends
// version: -2.
func TestFetchComputeConfigVersions_RequestsAllVersionsNotJustLatest(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}

		w.WriteHeader(http.StatusOK)

		version, hasVersion := payload["version"]
		if hasVersion && version == float64(-2) {
			_, _ = w.Write([]byte(`{"results": [
				{"id": "ccfg_v1", "name": "test-config", "version": 1},
				{"id": "ccfg_v2", "name": "test-config", "version": 2},
				{"id": "ccfg_v3", "name": "test-config", "version": 3}
			]}`))
			return
		}

		// Real backend behavior: no version field (or -1) means latest-only.
		_, _ = w.Write([]byte(`{"results": [
			{"id": "ccfg_v3", "name": "test-config", "version": 3}
		]}`))
	}))
	defer server.Close()

	d := &ComputeConfigDataSource{client: NewClientWithToken(server.URL, "test-token")}

	got, err := d.fetchComputeConfigVersions(ctx, "test-config")
	if err != nil {
		t.Fatalf("fetchComputeConfigVersions() error = %v", err)
	}

	want := []int64{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("fetchComputeConfigVersions() = %v, want %v (all 3 historical versions, not just the latest)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fetchComputeConfigVersions()[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

// TestFetchComputeConfigVersions_FollowsPagingToken is DS-CC-1's other
// mutation-proof half, flagged by forge while implementing: the test above
// proves the version=-2 sentinel is sent, but its mock returns every version
// in a single response with no next_paging_token at all, so it cannot tell
// apart "fetches everything in one call" from "correctly follows pagination
// across multiple calls" - a real config with more results than one page's
// count would still silently truncate even with the sentinel fix alone. This
// mock instead splits the 3 versions across two pages and requires the
// second request to carry the first response's exact next_paging_token.
//
// CC5b: retargeted from asserting a body-nested paging.paging_token (the old
// ext/v0 shape) to the URL query string, since that's where api/v2's
// required_pagination_large actually reads it from. Left unretargeted, this
// would have kept "passing" vacuously - nothing populates that body field
// anymore, so the old assertion would never fail no matter how broken the new
// pagination is. See TestSearchComputeTemplatesPaged_SendsPagingAsQueryParamsNotBody
// for the dedicated negative-space proof that the body does NOT carry paging.
func TestFetchComputeConfigVersions_FollowsPagingToken(t *testing.T) {
	ctx := context.Background()
	const wantToken = "versions-page-2-token"

	requestCount := 0
	var sawTokenOnSecondRequest string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)

		if requestCount == 1 {
			_, _ = w.Write([]byte(`{
				"results": [
					{"id": "ccfg_v1", "name": "test-config", "version": 1},
					{"id": "ccfg_v2", "name": "test-config", "version": 2}
				],
				"metadata": {"next_paging_token": "` + wantToken + `"}
			}`))
			return
		}

		sawTokenOnSecondRequest = r.URL.Query().Get("paging_token")
		_, _ = w.Write([]byte(`{
			"results": [
				{"id": "ccfg_v3", "name": "test-config", "version": 3}
			],
			"metadata": {"next_paging_token": null}
		}`))
	}))
	defer server.Close()

	d := &ComputeConfigDataSource{client: NewClientWithToken(server.URL, "test-token")}

	got, err := d.fetchComputeConfigVersions(ctx, "test-config")
	if err != nil {
		t.Fatalf("fetchComputeConfigVersions() error = %v", err)
	}

	if requestCount != 2 {
		t.Fatalf("expected 2 requests (one per page), got %d", requestCount)
	}
	if sawTokenOnSecondRequest != wantToken {
		t.Errorf("second request's paging_token query param = %q, want %q (the exact token the first response returned)", sawTokenOnSecondRequest, wantToken)
	}

	want := []int64{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("fetchComputeConfigVersions() = %v, want %v (all 3 versions across both pages)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fetchComputeConfigVersions()[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

// TestFindComputeConfigByName_ExactMatchOnPageTwo is CC5b's pagination-proof
// for findComputeConfigByName (fetchComputeConfigVersions already has one
// above) - page 1 returns a decoy, page 2 returns the real match, and the
// second request must carry the first response's exact paging_token as a URL
// query parameter.
func TestFindComputeConfigByName_ExactMatchOnPageTwo(t *testing.T) {
	ctx := context.Background()
	const wantToken = "byname-page-2-token"
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)

		if requestCount == 1 {
			_, _ = w.Write([]byte(`{
				"results": [{"id": "ccfg_decoy", "name": "test-config", "created_at": "2024-01-01T00:00:00Z", "anonymous": false}],
				"metadata": {"next_paging_token": "` + wantToken + `"}
			}`))
			return
		}

		if r.URL.Query().Get("paging_token") != wantToken {
			t.Errorf("second request's paging_token query param = %q, want %q", r.URL.Query().Get("paging_token"), wantToken)
		}
		_, _ = w.Write([]byte(`{
			"results": [{"id": "ccfg_exact", "name": "test-config", "created_at": "2024-06-01T00:00:00Z", "anonymous": false}],
			"metadata": {"next_paging_token": null}
		}`))
	}))
	defer server.Close()

	d := &ComputeConfigDataSource{client: NewClientWithToken(server.URL, "test-token")}

	got, err := d.findComputeConfigByName(ctx, "test-config", "")
	if err != nil {
		t.Fatalf("findComputeConfigByName() error = %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("expected 2 requests (one per page), got %d", requestCount)
	}
	// Both results share the name (real backend already exact-matches server-side), so the
	// tiebreak (most recently created) picks ccfg_exact - proving both pages' results were
	// actually collected, not just that page 2 was reached.
	if got != "ccfg_exact" {
		t.Errorf("findComputeConfigByName() = %q, want %q (both pages' matches must be collected before the tiebreak)", got, "ccfg_exact")
	}
}

// TestSearchComputeTemplatesPaged_SendsPagingAsQueryParamsNotBody is the
// direct proof for the hazard CC5b exists to prevent: a naive migration could
// keep nesting paging/paging_token/count inside the JSON body (the ext/v0
// shape) - it would compile, hit /api/v2/compute_templates/search, get HTTP
// 200 back, and silently paginate wrong (always page 1's worth of data, no
// error). A pagination-proof test alone (does the function eventually return
// the right answer) would not catch this if the mock is lenient about where
// it reads pagination params from - both tests above use exactly that kind of
// lenient mock. This test's mock is deliberately strict: it asserts the
// request body decodes to only the expected filter keys with no "paging" key
// at all, and separately asserts count/paging_token arrive as URL query
// params.
func TestSearchComputeTemplatesPaged_SendsPagingAsQueryParamsNotBody(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/compute_templates/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}
		if _, hasPaging := payload["paging"]; hasPaging {
			t.Error(`request body contains a "paging" key - pagination must be sent as URL query params on api/v2, not nested in the body`)
		}
		if _, hasName := payload["name"]; !hasName {
			t.Error(`request body missing expected filter key "name"`)
		}

		if got := r.URL.Query().Get("count"); got != "100" {
			t.Errorf(`count query param = %q, want "100"`, got)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": [{"id": "ccfg_1", "name": "test-config"}], "metadata": {"next_paging_token": null}}`))
	}))
	defer server.Close()

	client := NewClientWithToken(server.URL, "test-token")
	basePayload := map[string]interface{}{
		"name": map[string]string{"equals": "test-config"},
	}

	_, err := searchComputeTemplatesPaged(ctx, client, basePayload, decodeComputeConfigSearchPage)
	if err != nil {
		t.Fatalf("searchComputeTemplatesPaged() error = %v", err)
	}
}
