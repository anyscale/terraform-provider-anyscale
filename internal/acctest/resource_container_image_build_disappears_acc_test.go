package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// containerImageGoneMode is how a mocked application template has gone away
// out of band. Both container image resources' Read treat either as "remove
// from state": an archived template still answers GET with 200 and a non-empty
// archived_at (ApplicationTemplateResult.IsArchived), and a template the
// backend no longer resolves answers 404 (ErrNotFound).
type containerImageGoneMode int32

const (
	containerImageLive containerImageGoneMode = iota
	containerImageArchived
	containerImageNotFound
)

// newContainerImageBuildDisappearsMockServer serves the
// anyscale_container_image_build lifecycle (POST application_templates/ ->
// decorated GET application_templates/{id} -> GET builds/{id}, archive on
// delete) for one template whose liveness is *mode. A create resets it to
// live, so the recreate the disappears test plans can actually apply; creates
// counts those POSTs.
func newContainerImageBuildDisappearsMockServer(t *testing.T, templateID, buildID, name string, mode *atomic.Int32, creates *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	const createdAt = "2024-01-01T00:00:00Z"
	const digest = "sha256:disappearsbuildaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	writeJSON := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}

	// Registered under both the subtree and bare-path forms so a bare-path
	// request is never 301-redirected by ServeMux.
	createHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on /api/v2/application_templates/", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		creates.Add(1)
		mode.Store(int32(containerImageLive))
		// Bare create response: no latest_build, as the real API returns.
		writeJSON(w, http.StatusCreated, fmt.Sprintf(`{"result": {
			"id": %[1]q, "name": %[2]q, "creator_id": "user_mock",
			"created_at": %[3]q, "anonymous": false, "is_default": false
		}}`, templateID, name, createdAt))
	}
	mux.HandleFunc("/api/v2/application_templates/", createHandler)
	mux.HandleFunc("/api/v2/application_templates", createHandler)

	mux.HandleFunc("/api/v2/application_templates/"+templateID, func(w http.ResponseWriter, r *http.Request) {
		archivedAt := "null"
		switch containerImageGoneMode(mode.Load()) {
		case containerImageNotFound:
			writeJSON(w, http.StatusNotFound, `{"error": {"detail": "Cluster environment not found."}}`)
			return
		case containerImageArchived:
			archivedAt = `"2024-01-02T00:00:00Z"`
		}
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"result": {
			"id": %[1]q, "name": %[2]q, "creator_id": "user_mock",
			"created_at": %[3]q, "anonymous": false, "is_default": false,
			"archived_at": %[4]s,
			"latest_build": {"id": %[5]q, "revision": 1, "status": "succeeded"}
		}}`, templateID, name, createdAt, archivedAt, buildID))
	})

	// The real API answers GET builds/{id} with 201.
	mux.HandleFunc("/api/v2/builds/"+buildID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, fmt.Sprintf(`{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"docker_image_name": "anyscale/image/tfacc-disappears:1",
			"revision": 1, "creator_id": "user_mock", "status": "succeeded",
			"created_at": %[3]q, "last_modified_at": %[3]q, "is_byod": false,
			"digest": %[4]q
		}}`, buildID, templateID, createdAt, digest))
	})

	mux.HandleFunc("/api/v2/application_templates/"+templateID+"/archive", func(w http.ResponseWriter, r *http.Request) {
		mode.Store(int32(containerImageArchived))
		writeJSON(w, http.StatusOK, `{"result": {"archived_at": "2024-01-02T00:00:00Z"}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// checkMockCreateCount asserts how many creates the mock has served, proving
// a planned Create actually reached the API rather than only being planned.
func checkMockCreateCount(creates *atomic.Int32, want int32) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := creates.Load(); got != want {
			return fmt.Errorf("mock served %d create(s), want %d", got, want)
		}
		return nil
	}
}

// testAccContainerImageBuildDisappears creates the resource, flips the mock
// template to gone, and requires the next plan to be a Create: Read must drop
// the resource from state rather than error or keep the stale entry (which
// would plan a no-op against an image that no longer exists).
func testAccContainerImageBuildDisappears(t *testing.T, slug string, gone containerImageGoneMode) {
	t.Helper()
	templateID := "apptemp_" + slug + "_mock"
	buildID := "bld_" + slug + "_mock"
	name := "tfacc-" + slug + "-mock"
	const addr = "anyscale_container_image_build.test"

	var mode, creates atomic.Int32
	server := newContainerImageBuildDisappearsMockServer(t, templateID, buildID, name, &mode, &creates)

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_build" "test" {
  name          = %[1]q
  containerfile = "FROM anyscale/ray:2.53.0-slim-py312"
}
`, name)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", templateID),
					checkMockCreateCount(&creates, 1),
				),
			},
			{
				PreConfig: func() { mode.Store(int32(gone)) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", templateID),
					checkMockCreateCount(&creates, 2),
				),
			},
		},
	})
}

// TestAccContainerImageBuildResource_Disappears_MockServer: the template was
// archived out of band (GET 200 with archived_at set, the state the archive
// endpoint leaves behind); the next plan must recreate it.
func TestAccContainerImageBuildResource_Disappears_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccContainerImageBuildDisappears(t, "cib-disappears-archived", containerImageArchived)
}

// TestAccContainerImageBuildResource_DisappearsNotFound_MockServer: the
// template no longer resolves at all (GET 404); the next plan must recreate it.
func TestAccContainerImageBuildResource_DisappearsNotFound_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccContainerImageBuildDisappears(t, "cib-disappears-404", containerImageNotFound)
}
