package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Changing image_uri, ray_version or registry_login_secret registers one new build on the
// same cluster environment; it never replaces the environment (archived names stay taken,
// so a replace fails its recreate). An omitted ray_version is not sent: the backend then
// records none on the build, rather than the provider stamping a default.
//
// inplaceRegistryMock models the API as observed live: the environment create registers
// revision 1 from its config_json; every POST builds/byod registers the next revision with
// no deduplication; a build echoes ray_version, byod_ray_version and registry_login_secret
// as sent, and null for an omitted key. It records each registration's raw config_json so
// a test can assert which keys were sent, and fails the test on any environment create or
// archive it was told not to expect.
type inplaceRegistryMock struct {
	t          *testing.T
	templateID string
	name       string

	mu              sync.Mutex
	registrations   []map[string]any // config_json of revision i+1
	buildPosts      int
	templateCreates int
	archives        int
	failNextBuild   bool
}

func newInplaceRegistryMock(t *testing.T, templateID, name string) (*inplaceRegistryMock, *httptest.Server) {
	t.Helper()
	m := &inplaceRegistryMock{t: t, templateID: templateID, name: name}
	mux := http.NewServeMux()
	decode := func(r *http.Request) map[string]any {
		var body struct {
			ConfigJSON map[string]any `json:"config_json"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		return body.ConfigJSON
	}
	mux.HandleFunc("POST /api/v2/application_templates/byod", func(w http.ResponseWriter, r *http.Request) {
		cfg := decode(r)
		m.mu.Lock()
		m.templateCreates++
		m.registrations = append(m.registrations, cfg)
		m.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "name": %q, "creator_id": "user_mock", "created_at": "2024-01-01T00:00:00Z"}}`, templateID, name)
	})
	mux.HandleFunc("POST /api/v2/builds/byod", func(w http.ResponseWriter, r *http.Request) {
		cfg := decode(r)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.buildPosts++
		if m.failNextBuild {
			m.failNextBuild = false
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "Internal Server Error"}}`)
			return
		}
		m.registrations = append(m.registrations, cfg)
		m.writeBuild(w, len(m.registrations))
	})
	mux.HandleFunc("GET /api/v2/application_templates/"+templateID, func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "name": %q, "creator_id": "user_mock", "created_at": "2024-01-01T00:00:00Z",
			"latest_build": {"id": "bld_%d", "revision": %[3]d, "status": "succeeded"}}}`, templateID, name, len(m.registrations))
	})
	mux.HandleFunc("GET /api/v2/builds/", func(w http.ResponseWriter, r *http.Request) {
		rev, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v2/builds/bld_"))
		m.mu.Lock()
		defer m.mu.Unlock()
		if rev < 1 || rev > len(m.registrations) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.writeBuild(w, rev)
	})
	mux.HandleFunc("POST /api/v2/application_templates/"+templateID+"/archive", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.archives++
		m.mu.Unlock()
		_, _ = fmt.Fprint(w, `{"result": {"archived_at": "2024-01-01T00:00:01Z"}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return m, server
}

// seed registers revisions without Terraform, for cold-import tests.
func (m *inplaceRegistryMock) seed(cfgs ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registrations = append(m.registrations, cfgs...)
}

// writeBuild writes revision rev. Caller holds mu.
func (m *inplaceRegistryMock) writeBuild(w http.ResponseWriter, rev int) {
	cfg := m.registrations[rev-1]
	field := func(key string) any { return cfg[key] } // nil (JSON null) when not sent
	body, _ := json.Marshal(map[string]any{"result": map[string]any{
		"id": fmt.Sprintf("bld_%d", rev), "application_template_id": m.templateID,
		"docker_image_name": cfg["docker_image"], "ray_version": field("ray_version"),
		"byod_ray_version": field("ray_version"), "registry_login_secret": field("registry_login_secret"),
		"revision": rev, "creator_id": "user_mock", "status": "succeeded",
		"created_at": "2024-01-01T00:00:00Z", "last_modified_at": "2024-01-01T00:00:00Z",
		"is_byod": true, "digest": fmt.Sprintf("sha256:%d", rev),
	}})
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(body)
}

// expectRegistrations asserts the total number of POST builds/byod requests, that the
// environment was created wantCreates times and never archived, and that the latest
// registration's config_json has exactly the want values for the keys named in want; a nil
// value means the key must be absent.
func (m *inplaceRegistryMock) expectRegistrations(wantCreates, wantBuildPosts int, want map[string]any) resource.TestCheckFunc {
	return func(*terraform.State) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.templateCreates != wantCreates || m.archives != 0 {
			return fmt.Errorf("environment creates = %d, archives = %d; want %d and 0", m.templateCreates, m.archives, wantCreates)
		}
		if m.buildPosts != wantBuildPosts {
			return fmt.Errorf("POST builds/byod requests = %d, want %d", m.buildPosts, wantBuildPosts)
		}
		last := m.registrations[len(m.registrations)-1]
		for key, wantValue := range want {
			got, present := last[key]
			if wantValue == nil {
				if present {
					return fmt.Errorf("latest registration sent %s = %v, want the key absent", key, got)
				}
				continue
			}
			if !present || got != wantValue {
				return fmt.Errorf("latest registration %s = %v (present %t), want %v", key, got, present, wantValue)
			}
		}
		return nil
	}
}

// inplaceRegistryConfig renders the resource. An empty rayVersion or secret omits the
// argument; imageExpr is an HCL expression, so a test can pass another resource's output.
func inplaceRegistryConfig(serverURL, name, imageExpr, rayVersion, secret, extra string) string {
	optional := ""
	if rayVersion != "" {
		optional += fmt.Sprintf("  ray_version           = %q\n", rayVersion)
	}
	if secret != "" {
		optional += fmt.Sprintf("  registry_login_secret = %q\n", secret)
	}
	return testAccProviderBlock(serverURL) + extra + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name                  = %q
  image_uri             = %s
%s}
`, name, imageExpr, optional)
}

const inplaceRegistryAddr = "anyscale_container_image_registry.test"

func expectInPlaceUpdate() resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{
		PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(inplaceRegistryAddr, plancheck.ResourceActionUpdate)},
		PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
	}
}

func TestAccContainerImageRegistryResource_InPlaceRayVersionAndSecret_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const templateID, name = "apptemp_inplace_mock", "tfacc-inplace-mock"
	m, server := newInplaceRegistryMock(t, templateID, name)
	cfg := func(image, rayVersion, secret string) string {
		return inplaceRegistryConfig(server.URL, name, strconv.Quote(image), rayVersion, secret, "")
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("docker.io/example/image:v1", "2.9.0", "secret-1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "revision", "1"),
					m.expectRegistrations(1, 0, map[string]any{"ray_version": "2.9.0", "registry_login_secret": "secret-1"}),
				),
			},
			{
				Config:           cfg("docker.io/example/image:v1", "2.10.0", "secret-1"),
				ConfigPlanChecks: expectInPlaceUpdate(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "id", templateID),
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "revision", "2"),
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "ray_version", "2.10.0"),
					m.expectRegistrations(1, 1, map[string]any{"ray_version": "2.10.0", "registry_login_secret": "secret-1", "docker_image": "docker.io/example/image:v1"}),
				),
			},
			{
				Config:           cfg("docker.io/example/image:v1", "2.10.0", "secret-2"),
				ConfigPlanChecks: expectInPlaceUpdate(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "revision", "3"),
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "registry_login_secret", "secret-2"),
					m.expectRegistrations(1, 2, map[string]any{"ray_version": "2.10.0", "registry_login_secret": "secret-2"}),
				),
			},
			{
				// All three at once is still one build.
				Config:           cfg("docker.io/example/image:v2", "2.11.0", "secret-3"),
				ConfigPlanChecks: expectInPlaceUpdate(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "id", templateID),
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "revision", "4"),
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "name_version", name+":4"),
					m.expectRegistrations(1, 3, map[string]any{"ray_version": "2.11.0", "registry_login_secret": "secret-3", "docker_image": "docker.io/example/image:v2"}),
				),
			},
			{
				// Removing ray_version from config keeps the current value: no plan, no build.
				Config: cfg("docker.io/example/image:v2", "", "secret-3"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "ray_version", "2.11.0"),
					m.expectRegistrations(1, 3, nil),
				),
			},
			{
				// Removing the secret registers a build without registry credentials.
				Config:           cfg("docker.io/example/image:v2", "2.11.0", ""),
				ConfigPlanChecks: expectInPlaceUpdate(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(inplaceRegistryAddr, "registry_login_secret"),
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "revision", "5"),
					m.expectRegistrations(1, 4, map[string]any{"registry_login_secret": nil, "ray_version": "2.11.0"}),
				),
			},
		},
	})
}

// An omitted ray_version is never sent, on create or on a later build, and reads back null.
func TestAccContainerImageRegistryResource_RayVersionOmittedIsNotSent_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const templateID, name = "apptemp_ray_omitted_mock", "tfacc-ray-omitted-mock"
	m, server := newInplaceRegistryMock(t, templateID, name)
	cfg := func(image string) string {
		return inplaceRegistryConfig(server.URL, name, strconv.Quote(image), "", "", "")
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("docker.io/example/image:2.53.0"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(inplaceRegistryAddr, "ray_version"),
					m.expectRegistrations(1, 0, map[string]any{"ray_version": nil}),
				),
			},
			{
				Config:           cfg("docker.io/example/image:2.54.0"),
				ConfigPlanChecks: expectInPlaceUpdate(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(inplaceRegistryAddr, "ray_version"),
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "revision", "2"),
					m.expectRegistrations(1, 1, map[string]any{"ray_version": nil}),
				),
			},
		},
	})
}

// State written by an older provider version holds the 2.44.0 it used to send when config
// omitted ray_version (reconstructed here by setting it, then omitting it). That value must
// not be re-sent: a build triggered by another change sends no ray_version, and state takes
// the API's null. With no build, nothing changes.
func TestAccContainerImageRegistryResource_StampedRayVersionNotResent_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const templateID, name = "apptemp_stamped_mock", "tfacc-stamped-mock"
	m, server := newInplaceRegistryMock(t, templateID, name)
	cfg := func(image, rayVersion string) string {
		return inplaceRegistryConfig(server.URL, name, strconv.Quote(image), rayVersion, "", "")
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("docker.io/example/image:v1", "2.44.0"),
			},
			{
				// No trigger: empty plan, stamped value kept.
				Config: cfg("docker.io/example/image:v1", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "ray_version", "2.44.0"),
					m.expectRegistrations(1, 0, nil),
				),
			},
			{
				Config: cfg("docker.io/example/image:v2", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(inplaceRegistryAddr, plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue(inplaceRegistryAddr, tfjsonpath.New("ray_version")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(inplaceRegistryAddr, "ray_version"),
					m.expectRegistrations(1, 1, map[string]any{"ray_version": nil, "docker_image": "docker.io/example/image:v2"}),
				),
			},
		},
	})
}

// A failed build request leaves state as it was, so the next plan proposes the same update.
func TestAccContainerImageRegistryResource_InPlaceBuildFailureKeepsState_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const templateID, name = "apptemp_build_fail_mock", "tfacc-build-fail-mock"
	m, server := newInplaceRegistryMock(t, templateID, name)
	cfg := func(rayVersion string) string {
		return inplaceRegistryConfig(server.URL, name, strconv.Quote("docker.io/example/image:v1"), rayVersion, "", "")
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("2.9.0"),
			},
			{
				PreConfig: func() {
					m.mu.Lock()
					m.failNextBuild = true
					m.mu.Unlock()
				},
				Config:      cfg("9.99.0"),
				ExpectError: regexp.MustCompile(`(?s)register container image build.*500`),
			},
			{
				// State still holds revision 1 and 2.9.0, so the update is proposed again.
				Config: cfg("9.99.0"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(inplaceRegistryAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "revision", "2"),
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "ray_version", "9.99.0"),
					m.expectRegistrations(1, 2, map[string]any{"ray_version": "9.99.0"}),
				),
			},
		},
	})
}

// image_uri from another resource's computed output is unknown at plan time. That counts
// as a build: the build-derived attributes and the config-omitted ray_version plan
// Unknown, and apply produces a consistent result.
func TestAccContainerImageRegistryResource_UnknownImageURIPlansBuild_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const templateID, name = "apptemp_unknown_image_mock", "tfacc-unknown-image-mock"
	m, server := newInplaceRegistryMock(t, templateID, name)
	cfg := func(tag string) string {
		extra := fmt.Sprintf(`
resource "terraform_data" "image" {
  input = "docker.io/example/image:%s"
}
`, tag)
		return inplaceRegistryConfig(server.URL, name, "terraform_data.image.output", "", "", extra)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("v1"),
			},
			{
				Config: cfg("v2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(inplaceRegistryAddr, plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue(inplaceRegistryAddr, tfjsonpath.New("image_uri")),
						plancheck.ExpectUnknownValue(inplaceRegistryAddr, tfjsonpath.New("revision")),
						plancheck.ExpectUnknownValue(inplaceRegistryAddr, tfjsonpath.New("ray_version")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "revision", "2"),
					m.expectRegistrations(1, 1, map[string]any{"docker_image": "docker.io/example/image:v2", "ray_version": nil}),
				),
			},
		},
	})
}

// Cold import of an image whose latest build came from an in-place update (revision 2,
// ray_version changed). Import is the first step and persists, so later steps plan
// against exactly what import recovered. The secret is not recovered, so a config that
// sets one plans one in-place build; a config without one plans nothing.
func TestAccContainerImageRegistryResource_ImportAfterInPlaceUpdate_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const templateID, name = "apptemp_import_inplace_mock", "tfacc-import-inplace-mock"
	const image = "docker.io/example/image:v1"
	m, server := newInplaceRegistryMock(t, templateID, name)
	m.seed(
		map[string]any{"docker_image": image, "ray_version": "2.9.0", "registry_login_secret": "live-secret"},
		map[string]any{"docker_image": image, "ray_version": "2.10.0", "registry_login_secret": "live-secret"},
	)
	cfg := func(secret string) string {
		return inplaceRegistryConfig(server.URL, name, strconv.Quote(image), "2.10.0", secret, "")
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       inplaceRegistryAddr,
				ImportState:        true,
				ImportStateId:      templateID,
				ImportStatePersist: true,
				Config:             cfg(""),
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d instances, want 1", len(states))
					}
					attrs := states[0].Attributes
					for key, want := range map[string]string{"ray_version": "2.10.0", "revision": "2", "image_uri": image, "id": templateID} {
						if attrs[key] != want {
							return fmt.Errorf("imported %s = %q, want %q", key, attrs[key], want)
						}
					}
					if v, ok := attrs["registry_login_secret"]; ok && v != "" {
						return fmt.Errorf("imported registry_login_secret = %q, want null (not recovered)", v)
					}
					return nil
				},
			},
			{
				Config: cfg(""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: m.expectRegistrations(0, 0, nil),
			},
			{
				Config:           cfg("live-secret"),
				ConfigPlanChecks: expectInPlaceUpdate(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "revision", "3"),
					m.expectRegistrations(0, 1, map[string]any{"registry_login_secret": "live-secret", "ray_version": "2.10.0"}),
				),
			},
		},
	})
}

// registry_login_secret from another resource's computed output is unknown at plan time.
// Like an unknown image_uri, that counts as a build.
func TestAccContainerImageRegistryResource_UnknownSecretPlansBuild_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const templateID, name = "apptemp_unknown_secret_mock", "tfacc-unknown-secret-mock"
	m, server := newInplaceRegistryMock(t, templateID, name)
	cfg := func(secret string) string {
		return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "terraform_data" "secret" {
  input = %q
}

resource "anyscale_container_image_registry" "test" {
  name                  = %q
  image_uri             = "docker.io/example/image:v1"
  registry_login_secret = terraform_data.secret.output
}
`, secret, name)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("secret-1"),
			},
			{
				Config: cfg("secret-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(inplaceRegistryAddr, plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue(inplaceRegistryAddr, tfjsonpath.New("registry_login_secret")),
						plancheck.ExpectUnknownValue(inplaceRegistryAddr, tfjsonpath.New("revision")),
						plancheck.ExpectUnknownValue(inplaceRegistryAddr, tfjsonpath.New("ray_version")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(inplaceRegistryAddr, "revision", "2"),
					m.expectRegistrations(1, 1, map[string]any{"registry_login_secret": "secret-2", "ray_version": nil}),
				),
			},
		},
	})
}
