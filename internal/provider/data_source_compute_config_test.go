package provider

import (
	"context"
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
