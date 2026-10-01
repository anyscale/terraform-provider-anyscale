package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Mock-server coverage for how the anyscale_compute_config data source scopes
// its searches: `versions` must be scoped to the resolved config's cloud
// (names are unique per cloud, not per organization), and a by-name lookup
// must prefer a non-archived config over an archived (destroyed) one.
//
// The mock search handler filters the way the backend does
// (compute_templates_dao.py _apply_filters_to_list_compute_template_query):
// by cloud_id only when set, by archive_status with NOT_ARCHIVED as the
// default (compute_templates_dao_models.py ComputeTemplateQuery), by exact
// name, and by version (-2 = no filter; absent/-1 = latest per name+cloud;
// >0 = exact).
// A mock that ignored those fields could not represent either bug.

type mockComputeTemplate struct {
	ID        string
	Name      string
	Version   int
	CloudID   string
	CreatedAt string
	Archived  bool
}

func newComputeConfigSearchMockServer(t *testing.T, templates []mockComputeTemplate) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	templateJSON := func(ct mockComputeTemplate) string {
		archivedAt := "null"
		if ct.Archived {
			archivedAt = `"2026-02-01T00:00:00Z"`
		}
		return fmt.Sprintf(`{
			"id": %q, "name": %q, "version": %d, "anonymous": false,
			"created_at": %q, "last_modified_at": %q, "archived_at": %s,
			"config": {"cloud_id": %q, "head_node_type": {"name": "head", "instance_type": "m5.2xlarge"}}
		}`, ct.ID, ct.Name, ct.Version, ct.CreatedAt, ct.CreatedAt, archivedAt, ct.CloudID)
	}

	mux.HandleFunc("/api/v2/compute_templates/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on search", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var q struct {
			Name          *struct{ Equals string } `json:"name"`
			CloudID       string                   `json:"cloud_id"`
			ArchiveStatus string                   `json:"archive_status"`
			Version       *int                     `json:"version"`
		}
		if err := json.Unmarshal(body, &q); err != nil {
			t.Errorf("failed to parse search body: %v", err)
		}
		if q.ArchiveStatus == "" {
			q.ArchiveStatus = "NOT_ARCHIVED"
		}

		var matched []mockComputeTemplate
		for _, ct := range templates {
			if q.CloudID != "" && ct.CloudID != q.CloudID {
				continue
			}
			if q.ArchiveStatus == "ARCHIVED" && !ct.Archived {
				continue
			}
			if q.ArchiveStatus == "NOT_ARCHIVED" && ct.Archived {
				continue
			}
			if q.Name != nil && ct.Name != q.Name.Equals {
				continue
			}
			if q.Version != nil && *q.Version > 0 && ct.Version != *q.Version {
				continue
			}
			matched = append(matched, ct)
		}

		// Absent or -1 version: latest version per (name, cloud_id) lineage
		// only (compute_templates_dao.py _list_latest_version_compute_templates).
		if q.Version == nil || *q.Version == -1 {
			latest := map[string]mockComputeTemplate{}
			for _, ct := range matched {
				key := ct.Name + "/" + ct.CloudID
				if cur, ok := latest[key]; !ok || ct.Version > cur.Version {
					latest[key] = ct
				}
			}
			matched = matched[:0]
			for _, ct := range latest {
				matched = append(matched, ct)
			}
		}

		parts := make([]string, 0, len(matched))
		for _, ct := range matched {
			parts = append(parts, templateJSON(ct))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results": [%s], "metadata": {"next_paging_token": null}}`, strings.Join(parts, ","))
	})

	for _, ct := range templates {
		ct := ct
		mux.HandleFunc("/api/v2/compute_templates/"+ct.ID, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, templateJSON(ct))
		})
	}

	mux.HandleFunc("/api/v2/clouds/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v2/clouds/")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "name": %q}}`, id, id+"-name")
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccComputeConfigDataSource_VersionsScopedToCloud_MockServer: the same
// name exists in two clouds with different version histories. Looking up
// cloud A's config must report only cloud A's versions. The single-cloud
// name is the positive control on the same path: its versions still list in
// full.
func TestAccComputeConfigDataSource_VersionsScopedToCloud_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudA = "cld_scope_a"
	const cloudB = "cld_scope_b"
	const sharedName = "shared-name"
	const soloName = "solo-name"

	server := newComputeConfigSearchMockServer(t, []mockComputeTemplate{
		{ID: "cpt_a_v1", Name: sharedName, Version: 1, CloudID: cloudA, CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "cpt_a_v2", Name: sharedName, Version: 2, CloudID: cloudA, CreatedAt: "2026-01-02T00:00:00Z"},
		{ID: "cpt_b_v1", Name: sharedName, Version: 1, CloudID: cloudB, CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "cpt_b_v2", Name: sharedName, Version: 2, CloudID: cloudB, CreatedAt: "2026-01-02T00:00:00Z"},
		{ID: "cpt_b_v3", Name: sharedName, Version: 3, CloudID: cloudB, CreatedAt: "2026-01-03T00:00:00Z"},
		{ID: "cpt_b_v4", Name: sharedName, Version: 4, CloudID: cloudB, CreatedAt: "2026-01-04T00:00:00Z"},
		{ID: "cpt_solo_v1", Name: soloName, Version: 1, CloudID: cloudA, CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "cpt_solo_v2", Name: soloName, Version: 2, CloudID: cloudA, CreatedAt: "2026-01-02T00:00:00Z"},
		{ID: "cpt_solo_v3", Name: soloName, Version: 3, CloudID: cloudA, CreatedAt: "2026-01-03T00:00:00Z"},
	})

	config := testAccProviderBlock(server.URL) + `
data "anyscale_compute_config" "shared" {
  id = "cpt_a_v2"
}

data "anyscale_compute_config" "solo" {
  id = "cpt_solo_v3"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_compute_config.shared", "cloud_id", cloudA),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.shared", "version", "2"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.shared", "versions.#", "2"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.shared", "versions.0", "1"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.shared", "versions.1", "2"),

					resource.TestCheckResourceAttr("data.anyscale_compute_config.solo", "versions.#", "3"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.solo", "versions.0", "1"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.solo", "versions.1", "2"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.solo", "versions.2", "3"),
				),
			},
		},
	})
}

// TestAccComputeConfigDataSource_ByNamePrefersNonArchived_MockServer: a name
// whose most recently created match is archived (destroyed) must resolve to
// the live, non-archived config instead. Positive controls on the same path:
// a name with only a live config resolves to it, and a name with only an
// archived config still resolves to that config (with a warning, asserted in
// the provider package's unit test, since resource.Test cannot observe
// warnings).
func TestAccComputeConfigDataSource_ByNamePrefersNonArchived_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudA = "cld_arch_a"

	server := newComputeConfigSearchMockServer(t, []mockComputeTemplate{
		{ID: "cpt_mixed_live", Name: "mixed", Version: 1, CloudID: cloudA, CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "cpt_mixed_archived", Name: "mixed", Version: 1, CloudID: "cld_arch_b", CreatedAt: "2026-03-01T00:00:00Z", Archived: true},
		{ID: "cpt_live_only", Name: "live-only", Version: 1, CloudID: cloudA, CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "cpt_archived_only", Name: "archived-only", Version: 1, CloudID: cloudA, CreatedAt: "2026-01-01T00:00:00Z", Archived: true},
	})

	config := testAccProviderBlock(server.URL) + `
data "anyscale_compute_config" "mixed" {
  name = "mixed"
}

data "anyscale_compute_config" "live_only" {
  name     = "live-only"
  cloud_id = "cld_arch_a"
}

data "anyscale_compute_config" "archived_only" {
  name     = "archived-only"
  cloud_id = "cld_arch_a"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_compute_config.mixed", "id", "cpt_mixed_live"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.live_only", "id", "cpt_live_only"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.archived_only", "id", "cpt_archived_only"),
				),
			},
		},
	})
}
