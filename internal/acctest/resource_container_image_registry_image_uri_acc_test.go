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

// Changing image_uri must add a build to the existing cluster environment. Replacing it
// archives the environment, and the recreate then fails because archived names are taken.
// The mock records every image registration (the environment create registers revision 1,
// as the real API does; each POST builds/byod registers the next revision) so the test can
// assert the latest one targets the same environment and carries registry_login_secret, that
// create registers no extra build, and that no environment is created or archived along the way.
func TestAccContainerImageRegistryResource_ImageURIUpdateInPlace_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_image_uri_update_mock"
	const name = "tfacc-image-uri-update-mock"
	const secret = "tfacc-image-uri-update-secret"
	const addr = "anyscale_container_image_registry.test"

	type buildRequest struct {
		ApplicationTemplateID string `json:"application_template_id"`
		ConfigJSON            struct {
			DockerImage         string  `json:"docker_image"`
			RegistryLoginSecret *string `json:"registry_login_secret"`
		} `json:"config_json"`
	}

	var mu sync.Mutex
	var images []string       // images[i] is the image of revision i+1
	var builds []buildRequest // every registration: the environment create, then each POST builds/byod
	var buildPosts int        // POST builds/byod calls
	var templateCreates, archives int
	writeBuild := func(w http.ResponseWriter, rev int) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {"id": "bld_%[1]d", "application_template_id": %[2]q,
			"docker_image_name": %[3]q, "ray_version": "2.44.0", "revision": %[1]d, "creator_id": "user_mock",
			"status": "succeeded", "created_at": "2024-01-01T00:00:00Z", "last_modified_at": "2024-01-01T00:00:00Z",
			"is_byod": true, "digest": "sha256:%[1]d"}}`, rev, templateID, images[rev-1])
	}

	mux := http.NewServeMux()
	// Like the real API, creating the environment also registers its first build from
	// config_json; the response is the template only.
	mux.HandleFunc("POST /api/v2/application_templates/byod", func(w http.ResponseWriter, r *http.Request) {
		var req buildRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		req.ApplicationTemplateID = templateID
		mu.Lock()
		templateCreates++
		builds = append(builds, req)
		images = append(images, req.ConfigJSON.DockerImage)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "name": %q, "creator_id": "user_mock", "created_at": "2024-01-01T00:00:00Z"}}`, templateID, name)
	})
	mux.HandleFunc("POST /api/v2/builds/byod", func(w http.ResponseWriter, r *http.Request) {
		var req buildRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		defer mu.Unlock()
		buildPosts++
		builds = append(builds, req)
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
		mu.Lock()
		archives++
		mu.Unlock()
		_, _ = fmt.Fprint(w, `{"result": {"archived_at": "2024-01-01T00:00:01Z"}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	config := func(imageURI string) string {
		return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name                  = %q
  image_uri             = %q
  registry_login_secret = %q
}
`, name, imageURI, secret)
	}

	// checkLastBuild asserts the most recent registration put image on the original environment
	// with the login secret, that there were wantBuildPosts POST builds/byod calls in total, and
	// that the environment was never recreated or archived.
	checkLastBuild := func(image string, wantBuildPosts int) resource.TestCheckFunc {
		return func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if buildPosts != wantBuildPosts {
				return fmt.Errorf("got %d POST builds/byod requests, want %d", buildPosts, wantBuildPosts)
			}
			last := builds[len(builds)-1]
			if last.ApplicationTemplateID != templateID {
				return fmt.Errorf("build registered on application_template_id %q, want %q", last.ApplicationTemplateID, templateID)
			}
			if last.ConfigJSON.DockerImage != image {
				return fmt.Errorf("build registered docker_image %q, want %q", last.ConfigJSON.DockerImage, image)
			}
			if last.ConfigJSON.RegistryLoginSecret == nil || *last.ConfigJSON.RegistryLoginSecret != secret {
				return fmt.Errorf("build request did not carry registry_login_secret")
			}
			if templateCreates != 1 || archives != 0 {
				return fmt.Errorf("got %d environment creates and %d archives, want 1 and 0", templateCreates, archives)
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("docker.io/example/image:v1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "revision", "1"),
					checkLastBuild("docker.io/example/image:v1", 0),
				),
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
					checkLastBuild("docker.io/example/image:v2", 1),
				),
			},
			{
				// A build registered outside Terraform with a different image moves image_uri
				// on refresh. The plan re-registers the configured image in place; it does not
				// replace the environment.
				PreConfig: func() {
					mu.Lock()
					defer mu.Unlock()
					images = append(images, "docker.io/example/image:out-of-band")
				},
				Config: config("docker.io/example/image:v2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", templateID),
					resource.TestCheckResourceAttr(addr, "revision", "4"),
					resource.TestCheckResourceAttr(addr, "image_uri", "docker.io/example/image:v2"),
					checkLastBuild("docker.io/example/image:v2", 2),
				),
			},
		},
	})
}
