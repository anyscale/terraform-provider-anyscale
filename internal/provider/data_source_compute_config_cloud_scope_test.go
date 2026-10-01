package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// newArchiveFilteringSearchServer serves one compute config per name and
// filters search results by archive_status the way the backend does
// (NOT_ARCHIVED when absent), so a lookup that sends ALL first resolves an
// archived config without the warning.
func newArchiveFilteringSearchServer(t *testing.T, id, name string, archived bool) *httptest.Server {
	t.Helper()
	template := fmt.Sprintf(`{"id": %q, "name": %q, "version": 1, "created_at": "2026-01-01T00:00:00Z", "config": {}}`, id, name)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/compute_templates/search":
			body, _ := io.ReadAll(r.Body)
			var q struct {
				ArchiveStatus string `json:"archive_status"`
			}
			_ = json.Unmarshal(body, &q)
			if q.ArchiveStatus == "" {
				q.ArchiveStatus = "NOT_ARCHIVED"
			}
			include := q.ArchiveStatus == "ALL" ||
				(q.ArchiveStatus == "ARCHIVED" && archived) ||
				(q.ArchiveStatus == "NOT_ARCHIVED" && !archived)
			w.WriteHeader(http.StatusOK)
			if include {
				_, _ = fmt.Fprintf(w, `{"results": [%s], "metadata": {"next_paging_token": null}}`, template)
				return
			}
			_, _ = w.Write([]byte(`{"results": [], "metadata": {"next_paging_token": null}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/compute_templates/"+id:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, template)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func archivedWarning(diags diag.Diagnostics) diag.Diagnostic {
	for _, d := range diags.Warnings() {
		if d.Summary() == "Compute Config Is Archived" {
			return d
		}
	}
	return nil
}

func byNameFixture(name string) ComputeConfigDataSourceModel {
	m := computeConfigDataSourceLookupFixture("")
	m.ID = types.StringNull()
	m.Name = types.StringValue(name)
	return m
}

// TestComputeConfigRead_ByNameArchivedOnly_Warns: when the only match for a
// name is archived, the lookup still resolves it but must warn, naming the
// config, rather than silently returning a destroyed config.
func TestComputeConfigRead_ByNameArchivedOnly_Warns(t *testing.T) {
	server := newArchiveFilteringSearchServer(t, "cpt_archived", "gone-config", true)
	defer server.Close()

	d := &ComputeConfigDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runComputeConfigDataSourceRead(t, d, byNameFixture("gone-config"))
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if result.ID.ValueString() != "cpt_archived" {
		t.Errorf("id = %q, want %q", result.ID.ValueString(), "cpt_archived")
	}
	w := archivedWarning(diags)
	if w == nil {
		t.Fatalf("expected a %q warning, got diagnostics: %v", "Compute Config Is Archived", diags)
	}
	if !strings.Contains(w.Detail(), "gone-config") || !strings.Contains(w.Detail(), "cpt_archived") {
		t.Errorf("warning detail should name the config and its id, got: %s", w.Detail())
	}
}

// TestComputeConfigRead_ByNameLive_NoWarning is the positive control on the
// same path: a non-archived match resolves with no archived warning.
func TestComputeConfigRead_ByNameLive_NoWarning(t *testing.T) {
	server := newArchiveFilteringSearchServer(t, "cpt_live", "live-config", false)
	defer server.Close()

	d := &ComputeConfigDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runComputeConfigDataSourceRead(t, d, byNameFixture("live-config"))
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if result.ID.ValueString() != "cpt_live" {
		t.Errorf("id = %q, want %q", result.ID.ValueString(), "cpt_live")
	}
	if w := archivedWarning(diags); w != nil {
		t.Errorf("unexpected archived warning for a live config: %s", w.Detail())
	}
}
