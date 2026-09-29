package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// archiveResponse is what the mock's archive endpoint answers with. A zero
// status means a plain 200 success.
type archiveResponse struct {
	status int
	body   string
}

// newArchiveStatusMockRegistryServer serves a minimal BYOD
// anyscale_container_image_registry lifecycle whose archive endpoint (hit on
// destroy) answers with whatever *archive currently holds.
func newArchiveStatusMockRegistryServer(t *testing.T, templateID, buildID, name, imageURI, rayVersion string, archive *atomic.Pointer[archiveResponse]) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	const createdAt = "2024-01-01T00:00:00Z"
	const digest = "sha256:archivestatusaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	template := fmt.Sprintf(`{"result": {
		"id": %[1]q, "name": %[2]q, "creator_id": "user_mock",
		"created_at": %[3]q, "anonymous": false, "is_default": false,
		"latest_build": {"id": %[4]q, "revision": 1, "status": "succeeded"}
	}}`, templateID, name, createdAt, buildID)
	build := fmt.Sprintf(`{"result": {
		"id": %[1]q, "application_template_id": %[2]q,
		"docker_image_name": %[3]q, "ray_version": %[4]q,
		"revision": 1, "creator_id": "user_mock", "status": "succeeded",
		"created_at": %[5]q, "last_modified_at": %[5]q, "is_byod": true,
		"digest": %[6]q
	}}`, buildID, templateID, imageURI, rayVersion, createdAt, digest)

	writeJSON := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}

	mux.HandleFunc("/api/v2/application_templates/byod", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, template)
	})
	mux.HandleFunc("/api/v2/builds/byod", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, build)
	})
	mux.HandleFunc("/api/v2/application_templates/"+templateID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, template)
	})
	// The real API answers GET builds/{id} with 201.
	mux.HandleFunc("/api/v2/builds/"+buildID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, build)
	})
	mux.HandleFunc("/api/v2/application_templates/"+templateID+"/archive", func(w http.ResponseWriter, r *http.Request) {
		if a := archive.Load(); a != nil && a.status != 0 {
			writeJSON(w, a.status, a.body)
			return
		}
		writeJSON(w, http.StatusOK, `{"result": {"archived_at": "2024-01-01T00:00:01Z"}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// testAccArchiveStatusRegistry runs create then an explicit destroy step whose
// archive call answers with archiveOnDestroy. If expectErr is non-nil the
// destroy step must fail with it; the archive endpoint is then reset to 200 so
// the framework's own teardown destroy can succeed.
func testAccArchiveStatusRegistry(t *testing.T, slug string, archiveOnDestroy archiveResponse, expectErr *regexp.Regexp) {
	t.Helper()
	templateID := "apptemp_" + slug + "_mock"
	buildID := "bld_" + slug + "_mock"
	name := "tfacc-" + slug + "-mock"
	const imageURI = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-archive-status:v1"
	const rayVersion = "2.44.0"

	var archive atomic.Pointer[archiveResponse]
	server := newArchiveStatusMockRegistryServer(t, templateID, buildID, name, imageURI, rayVersion, &archive)

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name        = %[1]q
  image_uri   = %[2]q
  ray_version = %[3]q
}
`, name, imageURI, rayVersion)

	steps := []resource.TestStep{
		{Config: config},
		{
			PreConfig:   func() { archive.Store(&archiveOnDestroy) },
			Config:      config,
			Destroy:     true,
			ExpectError: expectErr,
		},
	}
	if expectErr != nil {
		steps = append(steps, resource.TestStep{
			PreConfig: func() { archive.Store(nil) },
			Config:    config,
		})
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps:                    steps,
	})
}

// TestAccContainerImageRegistryResource_ArchiveDefaultEnvironment400_MockServer:
// the backend's "Cannot archive a default cluster environment." 400 is the
// already-gone state for Anyscale-provided images, so destroy succeeds.
func TestAccContainerImageRegistryResource_ArchiveDefaultEnvironment400_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccArchiveStatusRegistry(t, "archive-default400", archiveResponse{
		status: http.StatusBadRequest,
		body:   `{"error": {"detail": "Cannot archive a default cluster environment."}}`,
	}, nil)
}

// TestAccContainerImageRegistryResource_ArchiveAzureControlPlane400_MockServer:
// Azure control planes reject every archive with a 400. Destroy must still
// succeed (the image is left in place with a warning), or it could never
// complete there.
func TestAccContainerImageRegistryResource_ArchiveAzureControlPlane400_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccArchiveStatusRegistry(t, "archive-azure400", archiveResponse{
		status: http.StatusBadRequest,
		body:   `{"error": {"detail": "Archiving container images is not supported on Azure Control Plane."}}`,
	}, nil)
}

// TestAccContainerImageRegistryResource_ArchiveOther400FailsDestroy_MockServer:
// any other 400 is a real archive failure and must fail destroy. Before the
// fix, 400 was an accepted status, so this was logged as "archived
// successfully" and the resource silently left state with the image live.
func TestAccContainerImageRegistryResource_ArchiveOther400FailsDestroy_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccArchiveStatusRegistry(t, "archive-other400", archiveResponse{
		status: http.StatusBadRequest,
		body:   `{"error": {"detail": "Invalid cluster environment id."}}`,
	}, regexp.MustCompile(`(?s)Failed to archive cluster environment:.*unexpected status 400`))
}
