// Mock-server acceptance tests for anyscale_runtime_env_package. They run the real terraform
// binary against an httptest backend that returns the wire shape of
// POST /api/v2/clouds/{id}/generate_cloud_data_bucket_presigned_url (201, a result envelope,
// lowercase url_scheme, file_uri built from bucket_path) and records every PUT to the presigned
// URL. They are the Gate 2 evidence cited in docs/decisions/service-working-dir-upload/README.md.
package acctest

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const pkgMockCloudID = "cld_pkgmock"

// pkgMockBackend serves the presigned-URL endpoint and the presigned PUT target.
type pkgMockBackend struct {
	t      *testing.T
	server *httptest.Server

	mu          sync.Mutex
	objects     map[string][]byte // "<bucket>/<bucket_path>" -> uploaded bytes
	puts        int
	presigns    []map[string]any
	alwaysExist bool // file_exists=true for every request, as for an object uploaded earlier

	// services, when set, handles /api/v2/services-v2 and /api/v2/tags paths.
	services *pkgMockService
}

func newPkgMockBackend(t *testing.T) *pkgMockBackend {
	m := &pkgMockBackend{t: t, objects: map[string][]byte{}}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}

var pkgPresignPath = regexp.MustCompile(`^/api/v2/clouds/([^/]+)/generate_cloud_data_bucket_presigned_url$`)

func (m *pkgMockBackend) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/upload/"):
		m.servePut(w, r)
	case pkgPresignPath.MatchString(r.URL.Path):
		m.servePresign(w, r, pkgPresignPath.FindStringSubmatch(r.URL.Path)[1])
	case m.services != nil && (strings.HasPrefix(r.URL.Path, "/api/v2/services-v2") || r.URL.Path == "/api/v2/tags/resource"):
		m.services.serve(m.t, w, r)
	default:
		http.NotFound(w, r)
	}
}

func (m *pkgMockBackend) servePresign(w http.ResponseWriter, r *http.Request, cloudID string) {
	if r.Method != http.MethodPost {
		m.t.Errorf("presign: unexpected method %s", r.Method)
	}
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		m.t.Errorf("presign: bad body: %v", err)
	}
	if req["file_type"] != "runtime_env_packages" {
		m.t.Errorf("presign: file_type %v", req["file_type"])
	}
	if cloudID != pkgMockCloudID {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error": {"detail": "Cloud not found"}}`)
		return
	}

	bucket := "mock-data-bucket-primary"
	if crid, ok := req["cloud_resource_id"].(string); ok {
		bucket = "mock-data-bucket-" + crid
	}
	fileName, _ := req["file_name"].(string)
	bucketPath := "org_mock/" + cloudID + "/runtime_env_packages/" + fileName

	m.mu.Lock()
	m.presigns = append(m.presigns, req)
	obj, exists := m.objects[bucket+"/"+bucketPath]
	exists = exists || m.alwaysExist
	m.mu.Unlock()

	resp := map[string]any{
		"file_uri":    "s3://" + bucket + "/" + bucketPath,
		"url":         m.server.URL + "/upload/" + bucket + "/" + bucketPath + "?X-Amz-Signature=mocksig",
		"file_exists": exists,
		"bucket_name": bucket,
		"bucket_path": bucketPath,
		"url_scheme":  "http",
		"file_size":   len(obj),
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": resp})
}

func (m *pkgMockBackend) servePut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		m.t.Errorf("upload: unexpected method %s", r.Method)
	}
	if r.Header.Get("Authorization") != "" {
		m.t.Errorf("upload: the Anyscale token was sent to the storage URL")
	}
	if r.ContentLength <= 0 || len(r.TransferEncoding) > 0 {
		m.t.Errorf("upload: want an explicit Content-Length, got %d (transfer-encoding %v)", r.ContentLength, r.TransferEncoding)
	}
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.objects[strings.TrimPrefix(r.URL.Path, "/upload/")] = body
	m.puts++
	m.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (m *pkgMockBackend) putCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.puts
}

func (m *pkgMockBackend) deleteAllObjects() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects = map[string][]byte{}
}

func (m *pkgMockBackend) object(uri string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[strings.TrimPrefix(uri, "s3://")]
	return b, ok
}

// expectPuts is a TestCheckFunc asserting the total number of uploads so far.
func (m *pkgMockBackend) expectPuts(want int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := m.putCount(); got != want {
			return fmt.Errorf("uploads so far: %d, want %d", got, want)
		}
		return nil
	}
}

// writePkgFixture writes a small source tree, including a .gitignored secret that must never
// be uploaded.
func writePkgFixture(t *testing.T, dir, marker string) {
	t.Helper()
	files := map[string]string{
		"app.py":         "MARKER = " + fmt.Sprintf("%q", marker) + "\n",
		"lib/helpers.py": "def f():\n    return 1\n",
		".gitignore":     ".env\n",
		".env":           "SECRET=do-not-upload\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func testAccRuntimeEnvPackageConfig(serverURL, dir string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_runtime_env_package" "test" {
  source_dir = %q
  cloud_id   = %q
}
`, dir, pkgMockCloudID)
}

var pkgURIPattern = regexp.MustCompile(`^s3://mock-data-bucket-primary/org_mock/` + pkgMockCloudID + `/runtime_env_packages/pkg_[0-9a-f]{64}\.zip$`)

// checkPkgOutputs asserts uri, relative_path, id and content_hash agree with each other.
func checkPkgOutputs(name string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return fmt.Errorf("%s not in state", name)
		}
		a := rs.Primary.Attributes
		if !pkgURIPattern.MatchString(a["uri"]) {
			return fmt.Errorf("uri %q has the wrong shape", a["uri"])
		}
		if !strings.HasSuffix(a["uri"], "/pkg_"+a["content_hash"]+".zip") {
			return fmt.Errorf("uri %q is not named after content_hash %q", a["uri"], a["content_hash"])
		}
		if want := strings.TrimPrefix(a["uri"], "s3://mock-data-bucket-primary/"); a["relative_path"] != want || a["id"] != want {
			return fmt.Errorf("relative_path %q / id %q, want %q", a["relative_path"], a["id"], want)
		}
		return nil
	}
}

// TestAccRuntimeEnvPackageResource_basic: Create uploads once, the uploaded zip holds the tree
// under a single top-level directory without the .gitignored file, and a re-plan is empty.
func TestAccRuntimeEnvPackageResource_basic(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPkgMockBackend(t)
	dir := t.TempDir()
	writePkgFixture(t, dir, "v1")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccRuntimeEnvPackageConfig(m.server.URL, dir),
			Check: resource.ComposeAggregateTestCheckFunc(
				checkPkgOutputs("anyscale_runtime_env_package.test"),
				m.expectPuts(1),
				func(s *terraform.State) error {
					uri := s.RootModule().Resources["anyscale_runtime_env_package.test"].Primary.Attributes["uri"]
					body, ok := m.object(uri)
					if !ok {
						return fmt.Errorf("nothing uploaded at %s", uri)
					}
					return checkPkgZip(body, map[string]string{
						"pkg/.gitignore":     ".env\n",
						"pkg/app.py":         "MARKER = \"v1\"\n",
						"pkg/lib/helpers.py": "def f():\n    return 1\n",
					})
				},
			),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
		}},
	})
}

// checkPkgZip asserts the zip holds exactly want (entry name -> content), in sorted order.
func checkPkgZip(body []byte, want map[string]string) error {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return fmt.Errorf("uploaded bytes are not a zip: %w", err)
	}
	var names, wantNames []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		if w, ok := want[f.Name]; ok && w != string(b) {
			return fmt.Errorf("%s holds %q, want %q", f.Name, b, w)
		}
	}
	for k := range want {
		wantNames = append(wantNames, k)
	}
	sort.Strings(wantNames)
	if strings.Join(names, ",") != strings.Join(wantNames, ",") {
		return fmt.Errorf("zip entries %v, want %v", names, wantNames)
	}
	return nil
}

// TestAccRuntimeEnvPackageResource_unchangedTreeEmptyPlan (G2-c): re-creating every file with
// new mtimes and in a different order plans nothing and uploads nothing.
func TestAccRuntimeEnvPackageResource_unchangedTreeEmptyPlan(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPkgMockBackend(t)
	dir := t.TempDir()
	writePkgFixture(t, dir, "v1")
	config := testAccRuntimeEnvPackageConfig(m.server.URL, dir)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, Check: m.expectPuts(1)},
			{
				PreConfig: func() {
					if err := os.RemoveAll(dir); err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatal(err)
					}
					writePkgFixture(t, dir, "v1")
					later := time.Now().Add(48 * time.Hour)
					_ = filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
						if err != nil {
							return err
						}
						return os.Chtimes(p, later, later)
					})
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: m.expectPuts(1),
			},
		},
	})
}

// TestAccRuntimeEnvPackageResource_contentChangePlansUpdate (G2-a): editing a file while the
// configuration is unchanged plans an in-place Update with uri unknown, Core accepts the
// ModifyPlan-set content_hash, and apply uploads a new object without touching the old one.
func TestAccRuntimeEnvPackageResource_contentChangePlansUpdate(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPkgMockBackend(t)
	dir := t.TempDir()
	writePkgFixture(t, dir, "v1")
	config := testAccRuntimeEnvPackageConfig(m.server.URL, dir)
	var firstURI string

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					m.expectPuts(1),
					CaptureResourceAttr("anyscale_runtime_env_package.test", "uri", &firstURI),
				),
			},
			{
				PreConfig: func() { writePkgFixture(t, dir, "v2") },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_runtime_env_package.test", plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue("anyscale_runtime_env_package.test", tfjsonpath.New("uri")),
						plancheck.ExpectUnknownValue("anyscale_runtime_env_package.test", tfjsonpath.New("relative_path")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					m.expectPuts(2),
					checkPkgOutputs("anyscale_runtime_env_package.test"),
					func(s *terraform.State) error {
						uri := s.RootModule().Resources["anyscale_runtime_env_package.test"].Primary.Attributes["uri"]
						if uri == firstURI {
							return fmt.Errorf("uri did not change after a content change: %s", uri)
						}
						if _, ok := m.object(firstURI); !ok {
							return fmt.Errorf("the previous object %s is gone; it must be left for running versions", firstURI)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccRuntimeEnvPackageResource_existingObjectSkipsUpload: when the server reports
// file_exists=true for the content-addressed name, Create uploads nothing.
func TestAccRuntimeEnvPackageResource_existingObjectSkipsUpload(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPkgMockBackend(t)
	m.alwaysExist = true
	dir := t.TempDir()
	writePkgFixture(t, dir, "v1")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccRuntimeEnvPackageConfig(m.server.URL, dir),
			Check: resource.ComposeAggregateTestCheckFunc(
				checkPkgOutputs("anyscale_runtime_env_package.test"),
				m.expectPuts(0),
			),
		}},
	})
}

// TestAccRuntimeEnvPackageResource_objectGoneRecreates: when Read's live probe reports the object
// missing, the resource leaves state, the plan shows a Create, and apply uploads the same name.
func TestAccRuntimeEnvPackageResource_objectGoneRecreates(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPkgMockBackend(t)
	dir := t.TempDir()
	writePkgFixture(t, dir, "v1")
	config := testAccRuntimeEnvPackageConfig(m.server.URL, dir)
	var firstURI string

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					m.expectPuts(1),
					CaptureResourceAttr("anyscale_runtime_env_package.test", "uri", &firstURI),
				),
			},
			{
				PreConfig: m.deleteAllObjects,
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_runtime_env_package.test", plancheck.ResourceActionCreate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					m.expectPuts(2),
					VerifyResourceAttrUnchanged("anyscale_runtime_env_package.test", "uri", &firstURI),
				),
			},
		},
	})
}

// TestAccRuntimeEnvPackageResource_changedAfterPlanErrors (G2-d): a file edited after plan,
// before apply, fails the apply instead of uploading content the plan did not show.
func TestAccRuntimeEnvPackageResource_changedAfterPlanErrors(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPkgMockBackend(t)
	dir := t.TempDir()
	writePkgFixture(t, dir, "v1")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccRuntimeEnvPackageConfig(m.server.URL, dir),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				// Runs after the plan is saved and before it is applied.
				PreApply: []plancheck.PlanCheck{editBetweenPlanAndApply{t: t, dir: dir}},
			},
			ExpectError: regexp.MustCompile(`(?s)(Provider produced inconsistent final plan.*content_hash|source_dir Changed Since Plan)`),
		}},
	})
	if got := m.putCount(); got != 0 {
		t.Fatalf("uploads: %d, want 0", got)
	}
}

type editBetweenPlanAndApply struct {
	t   *testing.T
	dir string
}

func (e editBetweenPlanAndApply) CheckPlan(_ context.Context, _ plancheck.CheckPlanRequest, _ *plancheck.CheckPlanResponse) {
	writePkgFixture(e.t, e.dir, "edited-after-plan")
}

// pkgMockService is a minimal services-v2 backend that records every PUT /apply body.
type pkgMockService struct {
	mu         sync.Mutex
	applies    []map[string]any
	terminated bool
}

const pkgMockServiceID = "svc_pkgmock"

// serviceJSON carries an auth_token, as a real service with query auth enabled (the default)
// does: Read derives query_auth_token_enabled from its presence, so a service without one plans a
// change to it, which IN_PLACE then rejects.
func (s *pkgMockService) serviceJSON() string {
	s.mu.Lock()
	state := "RUNNING"
	if s.terminated {
		state = "TERMINATED"
	}
	s.mu.Unlock()
	return fmt.Sprintf(`{
		"id": %[1]q, "name": "pkg-svc", "project_id": "prj_pkg", "cloud_id": %[2]q,
		"hostname": "pkg.example.com", "base_url": "https://pkg.example.com",
		"current_state": %[3]q, "goal_state": "RUNNING",
		"creator_id": "usr_pkg", "created_at": "2026-01-01T00:00:00Z",
		"is_multi_version": false, "auto_rollout_enabled": true,
		"auth_token": "tok_pkg",
		"service_observability_urls": {},
		"primary_version": {
			"id": "svcver_pkg", "created_at": "2026-01-01T00:00:00Z", "version": "v1",
			"current_state": %[3]q, "weight": 100, "build_id": "bld_pkg",
			"compute_config_id": "cpt_pkg", "production_job_ids": [], "connection_ids": [],
			"ray_serve_config": {}
		}
	}`, pkgMockServiceID, pkgMockCloudID, state)
}

func (s *pkgMockService) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v2/services-v2/apply":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("apply: bad body: %v", err)
		}
		s.mu.Lock()
		s.applies = append(s.applies, body)
		s.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+s.serviceJSON()+`}`)
	case "/api/v2/services-v2":
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	case "/api/v2/services-v2/" + pkgMockServiceID:
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprint(w, `{"result": `+s.serviceJSON()+`}`)
	case "/api/v2/services-v2/" + pkgMockServiceID + "/terminate":
		s.mu.Lock()
		s.terminated = true
		s.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	case "/api/v2/tags/resource":
		_, _ = fmt.Fprint(w, `{"result": {"tags": []}}`)
	default:
		http.NotFound(w, r)
	}
}

// expectApplies asserts the number of service deploys so far and that the last one sent the
// package's current uri as the working_dir.
func (s *pkgMockService) expectApplies(want int) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.applies) != want {
			return fmt.Errorf("service deploys so far: %d, want %d", len(s.applies), want)
		}
		uri := st.RootModule().Resources["anyscale_runtime_env_package.app"].Primary.Attributes["uri"]
		rsc, _ := s.applies[len(s.applies)-1]["ray_serve_config"].(map[string]any)
		apps, _ := rsc["applications"].([]any)
		if len(apps) != 1 {
			return fmt.Errorf("last deploy sent %d applications", len(apps))
		}
		app, _ := apps[0].(map[string]any)
		env, _ := app["runtime_env"].(map[string]any)
		if got := env["working_dir"]; got != uri {
			return fmt.Errorf("last deploy sent working_dir %v, want the package uri %s", got, uri)
		}
		return nil
	}
}

// TestAccRuntimeEnvPackageResource_serviceRedeploysOnChange (G2-b): a service whose
// ray_serve_config references the package's uri plans an Update in the same plan as a content
// change, through every service plan-time path (rollout_strategy IN_PLACE makes the IN_PLACE
// check run on the partially-unknown ray_serve_config), and deploys the new uri. When the object
// is lost and re-uploaded under the same name, the service plans an Update with a value known
// only after apply, but deploys no new version.
func TestAccRuntimeEnvPackageResource_serviceRedeploysOnChange(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPkgMockBackend(t)
	svc := &pkgMockService{}
	m.services = svc
	dir := t.TempDir()
	writePkgFixture(t, dir, "v1")

	config := testAccProviderBlock(m.server.URL) + fmt.Sprintf(`
resource "anyscale_runtime_env_package" "app" {
  source_dir = %[1]q
  cloud_id   = %[2]q
}

resource "anyscale_service" "test" {
  name              = "pkg-svc"
  project_id        = "prj_pkg"
  build_id          = "bld_pkg"
  compute_config_id = "cpt_pkg"
  rollout_strategy  = "IN_PLACE"
  ray_serve_config = {
    applications = [
      {
        import_path = "app:app"
        runtime_env = {
          working_dir = anyscale_runtime_env_package.app.uri
        }
      }
    ]
  }
}
`, dir, pkgMockCloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.ComposeAggregateTestCheckFunc(m.expectPuts(1), svc.expectApplies(1)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				PreConfig: func() { writePkgFixture(t, dir, "v2") },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_runtime_env_package.app", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(m.expectPuts(2), svc.expectApplies(2)),
			},
			{
				PreConfig: m.deleteAllObjects,
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_runtime_env_package.app", plancheck.ResourceActionCreate),
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(m.expectPuts(3), svc.expectApplies(2)),
			},
		},
	})
}
