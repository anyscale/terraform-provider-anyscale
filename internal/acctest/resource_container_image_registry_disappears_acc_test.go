package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// newContainerImageRegistryDisappearsMockServer serves the BYOD
// anyscale_container_image_registry lifecycle (POST application_templates/byod
// -> POST builds/byod, then GET application_templates/{id} -> GET builds/{id}
// on refresh, archive on delete) for one template whose liveness is *mode (see
// containerImageGoneMode). The BYOD template create resets it to live, so the
// planned recreate can apply; creates counts those POSTs.
func newContainerImageRegistryDisappearsMockServer(t *testing.T, templateID, buildID, name, imageURI, rayVersion string, mode *atomic.Int32, creates *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	const createdAt = "2024-01-01T00:00:00Z"
	const digest = "sha256:disappearsregistryaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	templateBody := func(archivedAt string) string {
		return fmt.Sprintf(`{"result": {
			"id": %[1]q, "name": %[2]q, "creator_id": "user_mock",
			"created_at": %[3]q, "anonymous": false, "is_default": false,
			"archived_at": %[4]s,
			"latest_build": {"id": %[5]q, "revision": 1, "status": "succeeded"}
		}}`, templateID, name, createdAt, archivedAt, buildID)
	}
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
		creates.Add(1)
		mode.Store(int32(containerImageLive))
		writeJSON(w, http.StatusCreated, templateBody("null"))
	})
	mux.HandleFunc("/api/v2/builds/byod", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, build)
	})
	mux.HandleFunc("/api/v2/application_templates/"+templateID, func(w http.ResponseWriter, r *http.Request) {
		switch containerImageGoneMode(mode.Load()) {
		case containerImageNotFound:
			writeJSON(w, http.StatusNotFound, `{"error": {"detail": "Cluster environment not found."}}`)
		case containerImageArchived:
			writeJSON(w, http.StatusOK, templateBody(`"2024-01-02T00:00:00Z"`))
		default:
			writeJSON(w, http.StatusOK, templateBody("null"))
		}
	})
	// The real API answers GET builds/{id} with 201.
	mux.HandleFunc("/api/v2/builds/"+buildID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, build)
	})
	mux.HandleFunc("/api/v2/application_templates/"+templateID+"/archive", func(w http.ResponseWriter, r *http.Request) {
		mode.Store(int32(containerImageArchived))
		writeJSON(w, http.StatusOK, `{"result": {"archived_at": "2024-01-02T00:00:00Z"}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// testAccContainerImageRegistryDisappears creates the resource, flips the
// mock template to gone, and requires the next plan to be a Create: Read must
// drop the resource from state rather than error or keep the stale entry.
func testAccContainerImageRegistryDisappears(t *testing.T, slug string, gone containerImageGoneMode) {
	t.Helper()
	templateID := "apptemp_" + slug + "_mock"
	buildID := "bld_" + slug + "_mock"
	name := "tfacc-" + slug + "-mock"
	const imageURI = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-disappears:v1"
	const rayVersion = "2.44.0"
	const addr = "anyscale_container_image_registry.test"

	var mode, creates atomic.Int32
	server := newContainerImageRegistryDisappearsMockServer(t, templateID, buildID, name, imageURI, rayVersion, &mode, &creates)

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name        = %[1]q
  image_uri   = %[2]q
  ray_version = %[3]q
}
`, name, imageURI, rayVersion)

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

// TestAccContainerImageRegistryResource_Disappears_MockServer: the template
// was archived out of band (GET 200 with archived_at set); the next plan must
// recreate it.
func TestAccContainerImageRegistryResource_Disappears_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccContainerImageRegistryDisappears(t, "cir-disappears-archived", containerImageArchived)
}

// TestAccContainerImageRegistryResource_DisappearsNotFound_MockServer: the
// template no longer resolves at all (GET 404); the next plan must recreate it.
func TestAccContainerImageRegistryResource_DisappearsNotFound_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccContainerImageRegistryDisappears(t, "cir-disappears-404", containerImageNotFound)
}
