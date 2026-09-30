package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// serviceDSMockService is one service the data source mock serves. The JSON it
// renders follows provider.ServiceResult, the list and get endpoints'
// response item.
type serviceDSMockService struct {
	ID, Name, ProjectID, CloudID string
}

func (s serviceDSMockService) render() map[string]any {
	return map[string]any{
		"id":                   s.ID,
		"name":                 s.Name,
		"description":          nil,
		"project_id":           s.ProjectID,
		"cloud_id":             s.CloudID,
		"creator_id":           "usr_svcds",
		"created_at":           "2026-01-01T00:00:00Z",
		"ended_at":             nil,
		"hostname":             s.Name + ".svcds.example.com",
		"base_url":             "https://" + s.Name + ".svcds.example.com",
		"current_state":        "RUNNING",
		"goal_state":           "RUNNING",
		"auto_rollout_enabled": true,
		"is_multi_version":     false,
		"error_message":        nil,
		"service_observability_urls": map[string]any{
			"service_dashboard_url":                    "https://console.example.com/dash/" + s.ID,
			"service_dashboard_embedding_url":          nil,
			"serve_deployment_dashboard_url":           nil,
			"serve_deployment_dashboard_embedding_url": nil,
		},
		"primary_version": map[string]any{
			"id": s.ID + "_v1", "created_at": "2026-01-01T00:00:00Z", "version": "v1",
			"current_state": "RUNNING", "weight": 100, "current_weight": 100, "target_weight": 100,
			"build_id": "anyscaleray2440-py311", "compute_config_id": "cpt_svcds",
			"production_job_ids": []string{"prodjob_" + s.ID}, "connection_ids": []string{},
			"ray_serve_config": map[string]any{"applications": []any{map[string]any{"import_path": "app:main"}}},
		},
		"canary_version":           nil,
		"service_status_checklist": nil,
	}
}

// serviceDSMock serves GET /api/v2/services-v2 and GET /api/v2/services-v2/{id}.
// The list endpoint applies the backend's filters server-side (services_dao.py:
// name is a case-insensitive substring match, project_id/cloud_id/creator_id
// exact), so a filter the provider fails to forward shows up as extra results.
type serviceDSMock struct {
	services []serviceDSMockService

	mu          sync.Mutex
	listQueries []url.Values
}

func newServiceDSMockServer(t *testing.T, services []serviceDSMockService) (*httptest.Server, *serviceDSMock) {
	t.Helper()
	m := &serviceDSMock{services: services}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on /api/v2/services-v2", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		m.mu.Lock()
		m.listQueries = append(m.listQueries, q)
		m.mu.Unlock()

		results := []map[string]any{}
		for _, s := range m.services {
			if n := q.Get("name"); n != "" && !strings.Contains(strings.ToLower(s.Name), strings.ToLower(n)) {
				continue
			}
			if p := q.Get("project_id"); p != "" && s.ProjectID != p {
				continue
			}
			if c := q.Get("cloud_id"); c != "" && s.CloudID != c {
				continue
			}
			results = append(results, s.render())
		}
		body, _ := json.Marshal(map[string]any{
			"results":  results,
			"metadata": map[string]any{"total": len(results), "next_paging_token": nil},
		})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})

	mux.HandleFunc("/api/v2/services-v2/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v2/services-v2/")
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on /api/v2/services-v2/%s", r.Method, id)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		for _, s := range m.services {
			if s.ID == id {
				body, _ := json.Marshal(map[string]any{"result": s.render()})
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error": {"detail": "not found"}}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, m
}

// sawListQuery reports whether any list call carried key=value.
func (m *serviceDSMock) sawListQuery(key, value string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, q := range m.listQueries {
		if q.Get(key) == value {
			return true
		}
	}
	return false
}

// serviceDSFixture: two same-named services in different projects, plus a
// third whose name contains the first's, so a by-name lookup must match
// exactly and narrow by project, and a project filter must drop two results.
var serviceDSFixture = []serviceDSMockService{
	{ID: "service_svcds_alpha_a", Name: "alpha", ProjectID: "prj_svcds_a", CloudID: "cld_svcds"},
	{ID: "service_svcds_alpha_b", Name: "alpha", ProjectID: "prj_svcds_b", CloudID: "cld_svcds"},
	{ID: "service_svcds_alphabet", Name: "alphabet", ProjectID: "prj_svcds_b", CloudID: "cld_svcds"},
}

func TestAccServiceDataSource_ByID_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, _ := newServiceDSMockServer(t, serviceDSFixture)
	const ds = "data.anyscale_service.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + `
data "anyscale_service" "test" {
  id = "service_svcds_alpha_a"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(ds, "id", "service_svcds_alpha_a"),
					resource.TestCheckResourceAttr(ds, "name", "alpha"),
					resource.TestCheckResourceAttr(ds, "project_id", "prj_svcds_a"),
					resource.TestCheckResourceAttr(ds, "cloud_id", "cld_svcds"),
					resource.TestCheckResourceAttr(ds, "creator_id", "usr_svcds"),
					resource.TestCheckResourceAttr(ds, "created_at", "2026-01-01T00:00:00Z"),
					resource.TestCheckResourceAttr(ds, "hostname", "alpha.svcds.example.com"),
					resource.TestCheckResourceAttr(ds, "base_url", "https://alpha.svcds.example.com"),
					resource.TestCheckResourceAttr(ds, "current_state", "RUNNING"),
					resource.TestCheckResourceAttr(ds, "goal_state", "RUNNING"),
					resource.TestCheckResourceAttr(ds, "primary_version.id", "service_svcds_alpha_a_v1"),
					resource.TestCheckResourceAttr(ds, "primary_version.version", "v1"),
					resource.TestCheckResourceAttr(ds, "primary_version.current_state", "RUNNING"),
					resource.TestCheckResourceAttr(ds, "primary_version.build_id", "anyscaleray2440-py311"),
					resource.TestCheckResourceAttr(ds, "primary_version.compute_config_id", "cpt_svcds"),
					resource.TestCheckResourceAttr(ds, "primary_version.ray_serve_config", `{"applications":[{"import_path":"app:main"}]}`),
					resource.TestCheckResourceAttr(ds, "service_observability_urls.service_dashboard_url", "https://console.example.com/dash/service_svcds_alpha_a"),
					// Nullable fields the API sent as null stay null, not "".
					resource.TestCheckNoResourceAttr(ds, "description"),
					resource.TestCheckNoResourceAttr(ds, "error_message"),
					resource.TestCheckNoResourceAttr(ds, "service_observability_urls.service_dashboard_embedding_url"),
				),
			},
		},
	})
}

func TestAccServiceDataSource_ByName_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newServiceDSMockServer(t, serviceDSFixture)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// "alpha" exists in two projects and is a substring of
				// "alphabet": project_id must narrow it, and the exact-match
				// step must drop "alphabet".
				Config: testAccProviderBlock(server.URL) + `
data "anyscale_service" "by_name" {
  name       = "alpha"
  project_id = "prj_svcds_b"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_service.by_name", "id", "service_svcds_alpha_b"),
					resource.TestCheckResourceAttr("data.anyscale_service.by_name", "name", "alpha"),
					resource.TestCheckResourceAttr("data.anyscale_service.by_name", "project_id", "prj_svcds_b"),
					func(*terraform.State) error {
						if !mock.sawListQuery("name", "alpha") || !mock.sawListQuery("project_id", "prj_svcds_b") {
							return fmt.Errorf("by-name lookup did not forward name and project_id to the list endpoint")
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccServicesDataSource_Basic_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, _ := newServiceDSMockServer(t, serviceDSFixture)
	const ds = "data.anyscale_services.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + `
data "anyscale_services" "test" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(ds, "services.#", "3"),
					resource.TestCheckResourceAttr(ds, "services.0.id", "service_svcds_alpha_a"),
					resource.TestCheckResourceAttr(ds, "services.0.name", "alpha"),
					resource.TestCheckResourceAttr(ds, "services.0.project_id", "prj_svcds_a"),
					resource.TestCheckResourceAttr(ds, "services.0.current_state", "RUNNING"),
					resource.TestCheckResourceAttr(ds, "services.0.primary_version.build_id", "anyscaleray2440-py311"),
					resource.TestCheckNoResourceAttr(ds, "services.0.description"),
					resource.TestCheckResourceAttr(ds, "services.2.id", "service_svcds_alphabet"),
				),
			},
		},
	})
}

func TestAccServicesDataSource_FilterByProjectID_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newServiceDSMockServer(t, serviceDSFixture)
	const ds = "data.anyscale_services.by_project"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + `
data "anyscale_services" "by_project" {
  project_id = "prj_svcds_a"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(ds, "services.#", "1"),
					testAccCheckAllServicesHaveProjectID(ds, "prj_svcds_a"),
					func(*terraform.State) error {
						if !mock.sawListQuery("project_id", "prj_svcds_a") {
							return fmt.Errorf("project_id filter was not forwarded to the list endpoint")
						}
						return nil
					},
				),
			},
		},
	})
}
