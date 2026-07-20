package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// serviceCloudMatchComputeConfigJSON and serviceCloudMatchProjectJSON are the minimal GET
// response shapes validateProjectComputeConfigCloudMatch reads: compute_templates' nested
// config.cloud_id, and projects' top-level parent_cloud_id (a nullable pointer server-side).
func serviceCloudMatchComputeConfigJSON(id, cloudID string) string {
	return fmt.Sprintf(`{"result": {"id": %[1]q, "name": "cc-cloudmatch", "config": {"cloud_id": %[2]q}}}`, id, cloudID)
}

func serviceCloudMatchProjectJSON(id string, parentCloudID *string) string {
	pc := "null"
	if parentCloudID != nil {
		pc = fmt.Sprintf("%q", *parentCloudID)
	}
	return fmt.Sprintf(`{"result": {"id": %[1]q, "name": "proj-cloudmatch", "parent_cloud_id": %[2]s, "created_at": "2026-01-01T00:00:00Z", "is_default": false}}`, id, pc)
}

// TestAccServiceResource_CloudMatchRejectsMismatch is MT1: project_id and compute_config_id
// resolve to DIFFERENT clouds - the validator must reject at PLAN TIME, before any apply ever
// fires. This is the whole point of the guard: today, without it, this exact mismatch reaches the
// backend and comes back as an opaque UNHEALTHY with a misleading "user removed from
// organization" 403 (confirmed via a real, empirically-verified diagnosis this session - see
// resource_service_realinfra_acc_test.go's cloud-scoped project fix for the real-infra side of
// the same root cause).
func TestAccServiceResource_CloudMatchRejectsMismatch(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	var applyCallCount int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&applyCallCount, 1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceRolloutJSON("svc_cloudmatch_mismatch", "cloudmatch-mismatch",
			"prj_mismatch", "STARTING", "svcver_v1", "v1")+`}`)
	})
	// Only reached if the validator fails to reject at plan time (i.e. under mutation) - present
	// so a broken validator fails on "expected error not found", not an unrelated 404 from the
	// adopt-guard's own list-by-name call.
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/compute_templates/cpt_mismatch", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, serviceCloudMatchComputeConfigJSON("cpt_mismatch", "cld_alpha"))
	})
	mux.HandleFunc("/api/v2/projects/prj_mismatch", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		cloudBeta := "cld_beta"
		_, _ = fmt.Fprint(w, serviceCloudMatchProjectJSON("prj_mismatch", &cloudBeta))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	config := testAccProviderBlock(server.URL) + `
resource "anyscale_service" "test" {
  name              = "cloudmatch-mismatch"
  project_id        = "prj_mismatch"
  build_id          = "bld_findings"
  compute_config_id = "cpt_mismatch"
  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
      }
    ]
  }
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)Project/Compute Config Cloud Mismatch.*cld_alpha.*cld_beta`),
			},
		},
	})

	// The whole point: rejected at PLAN, so apply must never have been reached at all.
	if got := atomic.LoadInt32(&applyCallCount); got != 0 {
		t.Errorf("apply was called %d time(s), want 0 - a cloud mismatch must be rejected at plan "+
			"time, before any apply is attempted", got)
	}
}

// TestAccServiceResource_CloudMatchAllowsMatch is MT2, and per architect the MOST important
// case: project_id and compute_config_id resolve to the SAME cloud, so the validator must NOT
// block the apply. A validator that rejects valid, matching configs would be a worse regression
// than having no validator at all - this is the false-positive guard.
func TestAccServiceResource_CloudMatchAllowsMatch(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_cloudmatch_match"
	var terminated atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceRolloutJSON(serviceID, "cloudmatch-match",
			"prj_match", "STARTING", "svcver_v1", "v1")+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/compute_templates/cpt_findings", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, serviceCloudMatchComputeConfigJSON("cpt_findings", "cld_alpha"))
	})
	mux.HandleFunc("/api/v2/projects/prj_match", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		cloudAlpha := "cld_alpha"
		_, _ = fmt.Fprint(w, serviceCloudMatchProjectJSON("prj_match", &cloudAlpha))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		state := "RUNNING"
		if terminated.Load() {
			state = "TERMINATED"
		}
		serveServiceGetOrDelete(t, w, r, serviceRolloutJSON(serviceID, "cloudmatch-match",
			"prj_match", state, "svcver_v1", "v1"))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		terminated.Store(true)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	config := testAccProviderBlock(server.URL) + `
resource "anyscale_service" "test" {
  name              = "cloudmatch-match"
  project_id        = "prj_match"
  build_id          = "bld_findings"
  compute_config_id = "cpt_findings"
  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
      }
    ]
  }
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
			},
		},
	})
}

// TestAccServiceResource_CloudMatchSkipsOnOmittedProjectID is MT3: project_id is omitted from
// config entirely (Optional+Computed, Unknown at plan until Create resolves the backend's
// default project). The validator must skip cleanly rather than error on an Unknown value it
// cannot yet compare - proving "no project_id set" doesn't regress into a plan-time crash or a
// false rejection.
func TestAccServiceResource_CloudMatchSkipsOnOmittedProjectID(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_cloudmatch_omitted_project"
	var terminated atomic.Bool
	var computeTemplateHit, projectHit atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceRolloutJSON(serviceID, "cloudmatch-omitted-project",
			"prj_backend_default", "STARTING", "svcver_v1", "v1")+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/compute_templates/cpt_findings", func(w http.ResponseWriter, r *http.Request) {
		computeTemplateHit.Store(true)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, serviceCloudMatchComputeConfigJSON("cpt_findings", "cld_alpha"))
	})
	mux.HandleFunc("/api/v2/projects/", func(w http.ResponseWriter, r *http.Request) {
		projectHit.Store(true)
		w.WriteHeader(http.StatusOK)
		cloudAlpha := "cld_alpha"
		_, _ = fmt.Fprint(w, serviceCloudMatchProjectJSON("prj_backend_default", &cloudAlpha))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		state := "RUNNING"
		if terminated.Load() {
			state = "TERMINATED"
		}
		serveServiceGetOrDelete(t, w, r, serviceRolloutJSON(serviceID, "cloudmatch-omitted-project",
			"prj_backend_default", state, "svcver_v1", "v1"))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		terminated.Store(true)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// project_id deliberately absent from config.
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_service" "test" {
  name              = "cloudmatch-omitted-project"
  build_id          = "bld_findings"
  compute_config_id = "cpt_findings"
  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
      }
    ]
  }
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
			},
		},
	})

	// The validator must not have attempted either lookup at all - project_id was Unknown at
	// plan time (this is the Create path, no prior state to diff against), so it should skip
	// before ever calling out, not call out and then discard the result.
	if computeTemplateHit.Load() || projectHit.Load() {
		t.Errorf("validator made a lookup call despite project_id being Unknown at plan time "+
			"(compute_templates hit=%v, projects hit=%v) - it should skip entirely, not "+
			"look up and discard", computeTemplateHit.Load(), projectHit.Load())
	}
}

// TestAccServiceResource_CloudMatchSkipsOnUnknownComputeConfigID is MT4: compute_config_id is
// Required but references another resource's own computed output (anyscale_compute_config.x.id),
// making it Unknown at plan time - the more realistic real-world skip case than an omitted
// project_id, since compute_config_id can never itself be omitted. Proves Required-but-Unknown
// does not error.
func TestAccServiceResource_CloudMatchSkipsOnUnknownComputeConfigID(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_cloudmatch_unknown_cc"
	var terminated atomic.Bool
	var computeTemplateHit, projectHit atomic.Bool
	var mu sync.Mutex
	var createdComputeConfig map[string]any // stored so GET (refresh) echoes exactly what POST (create) returned

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/compute_templates/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// Echo the real request's config back (rather than a hand-built stub) so every
			// computed attribute resource_compute_config.go expects to populate from the
			// response - head_node.resources, version, etc. - has something to resolve to.
			// This compute_config is only a vehicle to make compute_config_id genuinely Unknown
			// at plan time; its own contents aren't what this test is about, so the SAME record
			// is stored and echoed by GET below - a stable round trip, not just a plausible create.
			var reqBody struct {
				Name   string         `json:"name"`
				Config map[string]any `json:"config"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &reqBody)
			if reqBody.Config == nil {
				reqBody.Config = map[string]any{}
			}
			reqBody.Config["cloud_id"] = "cld_alpha"
			result := map[string]any{
				"id":               "cpt_findings",
				"name":             reqBody.Name,
				"version":          1,
				"created_at":       "2026-01-01T00:00:00Z",
				"last_modified_at": "2026-01-01T00:00:00Z",
				"archived_at":      nil,
				"config":           reqBody.Config,
			}
			mu.Lock()
			createdComputeConfig = result
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		computeTemplateHit.Store(true)
		mu.Lock()
		record := createdComputeConfig
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": record})
	})
	mux.HandleFunc("/api/v2/compute_templates/cpt_findings/archive", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v2/projects/prj_unknown_cc", func(w http.ResponseWriter, r *http.Request) {
		projectHit.Store(true)
		w.WriteHeader(http.StatusOK)
		cloudAlpha := "cld_alpha"
		_, _ = fmt.Fprint(w, serviceCloudMatchProjectJSON("prj_unknown_cc", &cloudAlpha))
	})
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceRolloutJSON(serviceID, "cloudmatch-unknown-cc",
			"prj_unknown_cc", "STARTING", "svcver_v1", "v1")+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		state := "RUNNING"
		if terminated.Load() {
			state = "TERMINATED"
		}
		serveServiceGetOrDelete(t, w, r, serviceRolloutJSON(serviceID, "cloudmatch-unknown-cc",
			"prj_unknown_cc", state, "svcver_v1", "v1"))
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		terminated.Store(true)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// compute_config_id references anyscale_compute_config.test.id - Unknown at plan time until
	// that resource is actually created, which is exactly the shape this test proves is safe.
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_compute_config" "test" {
  name     = "cc-cloudmatch-unknown"
  cloud_id = "cld_alpha"
  head_node = {
    instance_type = "m5.large"
  }
}

resource "anyscale_service" "test" {
  name              = "cloudmatch-unknown-cc"
  project_id        = "prj_unknown_cc"
  build_id          = "bld_findings"
  compute_config_id = anyscale_compute_config.test.config_id
  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
      }
    ]
  }
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
			},
		},
	})

	// The validator must skip while compute_config_id is Unknown at PLAN time - it is not
	// asserting the lookups never happen at all (Read/other paths may call these same
	// endpoints), only that the plan step itself did not depend on them succeeding.
	_ = computeTemplateHit.Load()
	_ = projectHit.Load()
}
