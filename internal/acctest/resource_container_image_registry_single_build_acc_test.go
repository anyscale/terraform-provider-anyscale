package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// singleBuildRegistryMock models the real BYOD API: POST application_templates/byod creates the
// cluster environment AND its first build (revision 1) from config_json, and answers with the
// template only (no latest_build). GET application_templates/{id} carries latest_build; GET
// builds/{id} returns the build. POST builds/byod registers the next revision, so a Create that
// also calls it leaves a duplicate revision 2 behind.
type singleBuildRegistryMock struct {
	mu         sync.Mutex
	images     []string // images[i] is the image of revision i+1
	buildPosts int      // POST builds/byod calls
	hideLatest bool     // GET template omits latest_build while set
	serverURL  string
	firstBuild string // id of the build the template create registered
}

func (m *singleBuildRegistryMock) buildPostCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buildPosts
}

func (m *singleBuildRegistryMock) setHideLatest(hide bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hideLatest = hide
}

// checkBuildPosts asserts the number of POST builds/byod calls so far.
func (m *singleBuildRegistryMock) checkBuildPosts(want int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := m.buildPostCount(); got != want {
			return fmt.Errorf("got %d POST builds/byod requests, want %d", got, want)
		}
		return nil
	}
}

func newSingleBuildRegistryMockServer(t *testing.T, templateID, name string, hideLatest bool) *singleBuildRegistryMock {
	t.Helper()
	m := &singleBuildRegistryMock{hideLatest: hideLatest, firstBuild: "bld_1"}

	type configJSON struct {
		ConfigJSON struct {
			DockerImage string `json:"docker_image"`
		} `json:"config_json"`
	}

	writeBuild := func(w http.ResponseWriter, rev int) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {"id": "bld_%[1]d", "application_template_id": %[2]q,
			"docker_image_name": %[3]q, "ray_version": "2.44.0", "revision": %[1]d, "creator_id": "user_mock",
			"status": "succeeded", "created_at": "2024-01-01T00:00:00Z", "last_modified_at": "2024-01-01T00:00:00Z",
			"is_byod": true, "digest": "sha256:%[1]d"}}`, rev, templateID, m.images[rev-1])
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/application_templates/byod", func(w http.ResponseWriter, r *http.Request) {
		var req configJSON
		_ = json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		m.images = append(m.images, req.ConfigJSON.DockerImage)
		m.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "name": %q, "creator_id": "user_mock", "created_at": "2024-01-01T00:00:00Z"}}`, templateID, name)
	})
	mux.HandleFunc("POST /api/v2/builds/byod", func(w http.ResponseWriter, r *http.Request) {
		var req configJSON
		_ = json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.buildPosts++
		m.images = append(m.images, req.ConfigJSON.DockerImage)
		writeBuild(w, len(m.images))
	})
	mux.HandleFunc("GET /api/v2/application_templates/"+templateID, func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		latestBuild := "null"
		if !m.hideLatest && len(m.images) > 0 {
			latestBuild = fmt.Sprintf(`{"id": "bld_%[1]d", "revision": %[1]d, "status": "succeeded"}`, len(m.images))
		}
		_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "name": %q, "creator_id": "user_mock", "created_at": "2024-01-01T00:00:00Z",
			"latest_build": %s}}`, templateID, name, latestBuild)
	})
	mux.HandleFunc("GET /api/v2/builds/", func(w http.ResponseWriter, r *http.Request) {
		rev, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v2/builds/bld_"))
		m.mu.Lock()
		defer m.mu.Unlock()
		if rev < 1 || rev > len(m.images) {
			t.Errorf("GET of unknown build %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeBuild(w, rev)
	})
	mux.HandleFunc("POST /api/v2/application_templates/"+templateID+"/archive", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"result": {"archived_at": "2024-01-01T00:00:01Z"}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	m.serverURL = server.URL
	return m
}

func singleBuildRegistryConfig(serverURL, name, imageURI string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name      = %q
  image_uri = %q
}
`, name, imageURI)
}

// The template create already registers the image as build revision 1, so Create must not
// register it again: a second POST builds/byod leaves a duplicate revision 2 in the backend and
// in state.
func TestAccContainerImageRegistryResource_CreateRegistersOneBuild_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_single_build_mock"
	const name = "tfacc-single-build-mock"
	const imageURI = "docker.io/example/image:v1"
	const addr = "anyscale_container_image_registry.test"

	m := newSingleBuildRegistryMockServer(t, templateID, name, false)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: singleBuildRegistryConfig(m.serverURL, name, imageURI),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					m.checkBuildPosts(0),
					resource.TestCheckResourceAttr(addr, "id", templateID),
					resource.TestCheckResourceAttr(addr, "build_id", m.firstBuild),
					resource.TestCheckResourceAttr(addr, "revision", "1"),
					resource.TestCheckResourceAttr(addr, "name_version", name+":1"),
					resource.TestCheckResourceAttr(addr, "image_uri", imageURI),
				),
			},
		},
	})
}

// If the template's first build is not visible yet when Create looks for it, apply must still
// succeed (an error would taint a resource whose image is already registered), leaving the
// build-derived attributes null; the next refresh fills them in without a replace.
func TestAccContainerImageRegistryResource_CreateBuildNotFoundYetWarns_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_build_not_found_yet_mock"
	const name = "tfacc-build-not-found-yet-mock"
	const imageURI = "docker.io/example/image:v1"
	const addr = "anyscale_container_image_registry.test"

	// latest_build stays hidden for the whole apply step, including its post-apply refresh.
	m := newSingleBuildRegistryMockServer(t, templateID, name, true)
	config := singleBuildRegistryConfig(m.serverURL, name, imageURI)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					m.checkBuildPosts(0),
					resource.TestCheckResourceAttr(addr, "id", templateID),
					resource.TestCheckNoResourceAttr(addr, "build_id"),
					resource.TestCheckNoResourceAttr(addr, "revision"),
					resource.TestCheckNoResourceAttr(addr, "digest"),
					resource.TestCheckNoResourceAttr(addr, "name_version"),
				),
			},
			{
				PreConfig: func() { m.setHideLatest(false) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionNoop)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					m.checkBuildPosts(0),
					resource.TestCheckResourceAttr(addr, "id", templateID),
					resource.TestCheckResourceAttr(addr, "build_id", m.firstBuild),
					resource.TestCheckResourceAttr(addr, "revision", "1"),
					resource.TestCheckResourceAttr(addr, "name_version", name+":1"),
				),
			},
		},
	})
}
