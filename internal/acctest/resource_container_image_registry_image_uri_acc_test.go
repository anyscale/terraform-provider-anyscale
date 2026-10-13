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
)

// Changing image_uri must add a build to the existing cluster environment. Replacing it
// archives the environment, and the recreate then fails because archived names are taken.
func TestAccContainerImageRegistryResource_ImageURIUpdateInPlace_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_image_uri_update_mock"
	const name = "tfacc-image-uri-update-mock"
	const addr = "anyscale_container_image_registry.test"

	var mu sync.Mutex
	var images []string // images[i] is the image of revision i+1
	writeBuild := func(w http.ResponseWriter, rev int) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {"id": "bld_%[1]d", "application_template_id": %[2]q,
			"docker_image_name": %[3]q, "ray_version": "2.44.0", "revision": %[1]d, "creator_id": "user_mock",
			"status": "succeeded", "created_at": "2024-01-01T00:00:00Z", "last_modified_at": "2024-01-01T00:00:00Z",
			"is_byod": true, "digest": "sha256:%[1]d"}}`, rev, templateID, images[rev-1])
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/application_templates/byod", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "name": %q, "creator_id": "user_mock", "created_at": "2024-01-01T00:00:00Z"}}`, templateID, name)
	})
	mux.HandleFunc("POST /api/v2/builds/byod", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ConfigJSON struct {
				DockerImage string `json:"docker_image"`
			} `json:"config_json"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		defer mu.Unlock()
		images = append(images, req.ConfigJSON.DockerImage)
		writeBuild(w, len(images))
	})
	mux.HandleFunc("GET /api/v2/application_templates/"+templateID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "name": %q, "creator_id": "user_mock", "created_at": "2024-01-01T00:00:00Z",
			"latest_build": {"id": "bld_%d", "revision": %[3]d, "status": "succeeded"}}}`, templateID, name, len(images))
	})
	mux.HandleFunc("GET /api/v2/builds/", func(w http.ResponseWriter, r *http.Request) {
		rev, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v2/builds/bld_"))
		mu.Lock()
		defer mu.Unlock()
		writeBuild(w, rev)
	})
	mux.HandleFunc("POST /api/v2/application_templates/"+templateID+"/archive", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"result": {"archived_at": "2024-01-01T00:00:01Z"}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	config := func(imageURI string) string {
		return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name      = %q
  image_uri = %q
}
`, name, imageURI)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("docker.io/example/image:v1"),
				Check:  resource.TestCheckResourceAttr(addr, "revision", "1"),
			},
			{
				Config: config("docker.io/example/image:v2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", templateID),
					resource.TestCheckResourceAttr(addr, "revision", "2"),
					resource.TestCheckResourceAttr(addr, "name_version", name+":2"),
				),
			},
		},
	})
}
