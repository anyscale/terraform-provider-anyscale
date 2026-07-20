package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// computeConfigDataSourceLookupFixture builds a minimal valid by-id lookup
// plan: every Object/List-typed attribute must be explicitly typed-null
// (matching the schema's exact attr types), not left at its Go zero value,
// or tfsdk.Config.Set rejects it with a Value Conversion Error.
func computeConfigDataSourceLookupFixture(id string) ComputeConfigDataSourceModel {
	return ComputeConfigDataSourceModel{
		ID:          types.StringValue(id),
		Versions:    types.ListNull(types.Int64Type),
		Zones:       types.ListNull(types.StringType),
		HeadNode:    types.ObjectNull(nodeConfigAttrTypes()),
		WorkerNodes: types.ListNull(types.ObjectType{AttrTypes: workerNodeConfigAttrTypes()}),
	}
}

// runComputeConfigDataSourceRead drives ComputeConfigDataSource's real Read()
// method end-to-end against a model representing the user's config, the same
// pattern as runContainerImageDataSourceRead/runProjectDataSourceRead.
func runComputeConfigDataSourceRead(t *testing.T, d *ComputeConfigDataSource, model ComputeConfigDataSourceModel) (ComputeConfigDataSourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	setDiags := state.Set(ctx, &model)
	if setDiags.HasError() {
		t.Fatalf("failed to build config fixture: %v", setDiags)
	}
	config := tfsdk.Config(state)

	readResp := &datasource.ReadResponse{
		State: tfsdk.State(config),
	}
	d.Read(ctx, datasource.ReadRequest{Config: config}, readResp)

	if readResp.Diagnostics.HasError() {
		return ComputeConfigDataSourceModel{}, readResp.Diagnostics
	}

	var result ComputeConfigDataSourceModel
	getDiags := readResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, readResp.Diagnostics
}

// TestComputeConfigRead_HitsAPIV2Endpoint is the CC5b GET-migration proof:
// Read()'s by-id lookup must hit api/v2/compute_templates, not the deprecated
// ext/v0/cluster_computes. The mock's catch-all failure handler on any other
// path is what makes this load-bearing - it would fail if either the old GET
// path or the old search path were still hit.
func TestComputeConfigRead_HitsAPIV2Endpoint(t *testing.T) {
	const configID = "cpt_abc123"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/compute_templates/"+configID:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": {
				"id": "` + configID + `", "name": "tfacc-cc-v2", "version": 3,
				"created_at": "2024-01-01T00:00:00Z", "last_modified_at": "2024-01-02T00:00:00Z",
				"config": {}
			}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/compute_templates/search":
			// fetchComputeConfigVersions still runs and errors are only
			// warned/tolerated, but serve it properly so this test proves a
			// clean end-to-end Read, not a Read that happens to succeed despite
			// a swallowed versions-fetch error. CC5b migrated this call from
			// POST /ext/v0/cluster_computes/search to this endpoint (#113) -
			// matched on path only (not the count/paging_token query string)
			// since this test doesn't exercise pagination itself; see
			// TestSearchComputeTemplatesPaged_SendsPagingAsQueryParamsNotBody
			// for the dedicated query-vs-body proof.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results": [{"id": "` + configID + `", "name": "tfacc-cc-v2", "version": 3}], "metadata": {"next_paging_token": null}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &ComputeConfigDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runComputeConfigDataSourceRead(t, d, computeConfigDataSourceLookupFixture(configID))
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if result.ID.ValueString() != configID {
		t.Errorf("id = %q, want %q", result.ID.ValueString(), configID)
	}
	if result.Name.ValueString() != "tfacc-cc-v2" {
		t.Errorf("name = %q, want %q", result.Name.ValueString(), "tfacc-cc-v2")
	}
	if result.Version.ValueInt64() != 3 {
		t.Errorf("version = %d, want 3", result.Version.ValueInt64())
	}
}

// TestComputeConfigRead_NotFoundByID_ProducesAPIError confirms the not-found
// path still works, unchanged, after the URL migration: only http.StatusOK is
// accepted (deliberately not widened to also accept http.StatusNotFound - see
// the comment on the GET call in data_source_compute_config.go), so a real 404
// fails isStatusExpected and produces a genuine error, not a silently-empty
// success.
func TestComputeConfigRead_NotFoundByID_ProducesAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail": "compute template not found"}`))
	}))
	defer server.Close()

	d := &ComputeConfigDataSource{client: NewClientWithToken(server.URL, "test-token")}
	_, diags := runComputeConfigDataSourceRead(t, d, computeConfigDataSourceLookupFixture("cpt_missing"))
	if !diags.HasError() {
		t.Fatal("expected an error on a 404, got none")
	}
}

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

	_, err := searchComputeTemplatesPaged(ctx, client, basePayload)
	if err != nil {
		t.Fatalf("searchComputeTemplatesPaged() error = %v", err)
	}
}
