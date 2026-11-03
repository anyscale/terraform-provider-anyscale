package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// The API answers HTTP 403 "Permission denied", not 404, for a deleted or nonexistent cloud,
// cloud resource, project, or service. These mock tests prove, through Terraform Core, that
// anyscale_cloud, anyscale_cloud_resource, anyscale_project and anyscale_service treat that 403
// as gone:
//
//   - Read: a refresh that gets a 403 drops the resource from state, so the next plan creates it
//     instead of failing. A PlanOnly step with ExpectNonEmptyPlan shows it; with 403 as an error
//     the refresh fails the step.
//   - Delete: a destroy whose DELETE gets a 403 succeeds. The end-of-test destroy shows it; a
//     Delete error fails the test.
//
// The warning each 403 adds is asserted by the provider package's unit tests, which can read
// diagnostics directly.

// forbiddenSwitch makes a mock answer 403 for the resource's reads or deletes once set.
type forbiddenSwitch struct {
	reads   atomic.Bool
	deletes atomic.Bool
	// deleteCalls counts the DELETE (or terminate) requests answered 403, so a Delete test proves
	// the destroy reached the API rather than skipping it.
	deleteCalls atomic.Int32
}

func writeForbidden(w http.ResponseWriter) {
	w.WriteHeader(http.StatusForbidden)
	_, _ = fmt.Fprint(w, `{"error": {"detail": "Permission denied"}}`)
}

func checkForbiddenDeleteReached(sw *forbiddenSwitch) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := sw.deleteCalls.Load(); got != 1 {
			return fmt.Errorf("forbidden delete calls = %d, want 1", got)
		}
		return nil
	}
}

// coldImportStep brings an existing mock object into state without a Create, which for the cloud
// resources would need a much larger mock. ImportStatePersist keeps the imported state for the
// next step and the end-of-test destroy.
func coldImportStep(config, addr, id string) resource.TestStep {
	return resource.TestStep{
		Config:             config,
		ResourceName:       addr,
		ImportState:        true,
		ImportStateId:      id,
		ImportStatePersist: true,
	}
}

// forbiddenReadStep flips the mock to answer 403 for everything about the object, as the API
// does once it is deleted, and plans: the refresh must drop the resource, so the plan creates it.
// Deletes answer 403 too because the end-of-test destroy does not refresh first.
func forbiddenReadStep(config string, sw *forbiddenSwitch) resource.TestStep {
	return resource.TestStep{
		PreConfig: func() {
			sw.reads.Store(true)
			sw.deletes.Store(true)
		},
		Config:             config,
		PlanOnly:           true,
		ExpectNonEmptyPlan: true,
	}
}

// ─── anyscale_cloud ──────────────────────────────────────────────────────────

func newForbiddenCloudMock(t *testing.T, cloudID string, sw *forbiddenSwitch) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && sw.reads.Load():
			writeForbidden(w)
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": {
				"id": %[1]q, "name": "forbidden-cloud", "provider": "AWS", "region": "us-east-2",
				"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
			}}`, cloudID)
		case r.Method == http.MethodDelete && sw.deletes.Load():
			sw.deleteCalls.Add(1)
			writeForbidden(w)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [{
			"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_forbidden",
			"compute_stack": "K8S", "region": "us-east-2",
			"kubernetes_config": {
				"anyscale_operator_iam_identity": "arn:aws:iam::123456789012:role/forbidden-operator",
				"zones": ["us-east-2a"]
			},
			"object_storage": {"bucket_name": "s3://forbidden-bucket"}
		}], "metadata": {"total": 1, "next_paging_token": null}}`)
	})
	emptyList := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {"machine_pools": []}, "results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	}
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/machine_pools", emptyList)
	mux.HandleFunc("/api/v2/machine_pools/", emptyList)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func testAccForbiddenCloudConfig(serverURL string) string {
	return testAccProviderBlock(serverURL) + `
resource "anyscale_cloud" "test" {
  name           = "forbidden-cloud"
  cloud_provider = "AWS"
  compute_stack  = "K8S"
  region         = "us-east-2"

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::123456789012:role/forbidden-operator"
    zones                          = ["us-east-2a"]
  }

  object_storage {
    bucket_name = "forbidden-bucket"
  }
}
`
}

func TestAccCloudResource_ReadForbiddenTreatedAsGone_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_forbidden_read"
	sw := &forbiddenSwitch{}
	config := testAccForbiddenCloudConfig(newForbiddenCloudMock(t, cloudID, sw).URL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			coldImportStep(config, "anyscale_cloud.test", cloudID),
			forbiddenReadStep(config, sw),
		},
	})
}

func TestAccCloudResource_DeleteForbiddenTreatedAsGone_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_forbidden_delete"
	sw := &forbiddenSwitch{}
	sw.deletes.Store(true)
	config := testAccForbiddenCloudConfig(newForbiddenCloudMock(t, cloudID, sw).URL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             checkForbiddenDeleteReached(sw),
		Steps:                    []resource.TestStep{coldImportStep(config, "anyscale_cloud.test", cloudID)},
	})
}

// ─── anyscale_cloud_resource ─────────────────────────────────────────────────

func newForbiddenCloudResourceMock(t *testing.T, cloudID string, sw *forbiddenSwitch) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		if sw.reads.Load() {
			writeForbidden(w)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [{
			"name": "secondary", "is_default": false, "cloud_resource_id": "cldrsrc_forbidden_secondary",
			"provider": "AWS", "compute_stack": "K8S", "region": "us-east-2",
			"kubernetes_config": {
				"anyscale_operator_iam_identity": "arn:aws:iam::123456789012:role/forbidden-operator",
				"zones": ["us-east-2a"]
			},
			"object_storage": {"bucket_name": "s3://forbidden-bucket"}
		}], "metadata": {"total": 1, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/remove_resource", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && sw.deletes.Load() {
			sw.deleteCalls.Add(1)
			writeForbidden(w)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func testAccForbiddenCloudResourceConfig(serverURL, cloudID string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud_resource" "test" {
  cloud_id      = %[1]q
  name          = "secondary"
  compute_stack = "K8S"
  region        = "us-east-2"

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::123456789012:role/forbidden-operator"
    zones                          = ["us-east-2a"]
  }

  object_storage {
    bucket_name = "forbidden-bucket"
  }
}
`, cloudID)
}

func TestAccCloudResourceResource_ReadForbiddenTreatedAsGone_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_forbidden_cr_read"
	sw := &forbiddenSwitch{}
	config := testAccForbiddenCloudResourceConfig(newForbiddenCloudResourceMock(t, cloudID, sw).URL, cloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			coldImportStep(config, "anyscale_cloud_resource.test", cloudID+":secondary"),
			forbiddenReadStep(config, sw),
		},
	})
}

func TestAccCloudResourceResource_DeleteForbiddenTreatedAsGone_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_forbidden_cr_delete"
	sw := &forbiddenSwitch{}
	sw.deletes.Store(true)
	config := testAccForbiddenCloudResourceConfig(newForbiddenCloudResourceMock(t, cloudID, sw).URL, cloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             checkForbiddenDeleteReached(sw),
		Steps:                    []resource.TestStep{coldImportStep(config, "anyscale_cloud_resource.test", cloudID+":secondary")},
	})
}

// ─── anyscale_project ────────────────────────────────────────────────────────

// newForbiddenProjectMock wraps the project lifecycle mock. Its projects carry a 2026-01-01
// created_at, outside the delete-time 403 retry window, so a Delete 403 is not retried here; the
// retry itself is covered by the provider package's TestProjectResourceDelete_403Retry.
func newForbiddenProjectMock(t *testing.T, sw *forbiddenSwitch) *httptest.Server {
	t.Helper()
	s := &mockProjectServer{projects: make(map[string]map[string]any)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2/projects/") {
			if r.Method == http.MethodGet && sw.reads.Load() {
				writeForbidden(w)
				return
			}
			if r.Method == http.MethodDelete && sw.deletes.Load() {
				sw.deleteCalls.Add(1)
				writeForbidden(w)
				return
			}
		}
		s.handle(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func testAccForbiddenProjectConfig(serverURL string) string {
	return testAccProviderBlock(serverURL) + `
resource "anyscale_project" "test" {
  name        = "forbidden-project"
  cloud_id    = "cld_mock"
  description = "forbidden mock project"
}
`
}

func TestAccProjectResource_ReadForbiddenTreatedAsGone_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	sw := &forbiddenSwitch{}
	config := testAccForbiddenProjectConfig(newForbiddenProjectMock(t, sw).URL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			forbiddenReadStep(config, sw),
		},
	})
}

func TestAccProjectResource_DeleteForbiddenTreatedAsGone_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	sw := &forbiddenSwitch{}
	config := testAccForbiddenProjectConfig(newForbiddenProjectMock(t, sw).URL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             checkForbiddenDeleteReached(sw),
		Steps: []resource.TestStep{{
			Config: config,
			// Created normally, then every later DELETE answers 403.
			Check: func(*terraform.State) error { sw.deletes.Store(true); return nil },
		}},
	})
}

// ─── anyscale_service ────────────────────────────────────────────────────────

func newForbiddenServiceMock(t *testing.T, serviceID string, sw *forbiddenSwitch) *httptest.Server {
	t.Helper()
	body := serviceFindingsJSON(serviceID, "forbidden-service", "prj_forbidden", "RUNNING")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+body+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && sw.reads.Load():
			writeForbidden(w)
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"result": `+body+`}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		if sw.deletes.Load() {
			sw.deleteCalls.Add(1)
			writeForbidden(w)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestAccServiceResource_ReadForbiddenTreatedAsGone_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_forbidden_read"
	sw := &forbiddenSwitch{}
	config := testAccServiceFindingsConfig(newForbiddenServiceMock(t, serviceID, sw).URL, "forbidden-service", "prj_forbidden")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			forbiddenReadStep(config, sw),
		},
	})
}

func TestAccServiceResource_DeleteForbiddenTreatedAsGone_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_forbidden_delete"
	sw := &forbiddenSwitch{}
	sw.deletes.Store(true)
	config := testAccServiceFindingsConfig(newForbiddenServiceMock(t, serviceID, sw).URL, "forbidden-service", "prj_forbidden")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             checkForbiddenDeleteReached(sw),
		Steps:                    []resource.TestStep{{Config: config}},
	})
}
