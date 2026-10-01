package acctest

// Mock-server lifecycle coverage for the registry resource's id, which is the
// application template (cluster environment) id, never the build id.
// resource_container_image_registry_upgrade_test.go (package provider) proves
// the StateUpgrader re-keys id in isolation; it cannot prove the ordinary
// framework-level Read/plan/import path works once a resource lives under that
// id - a mapping function can be correct in isolation while the framework-level
// plan is still unstable. A resource created fresh under the current schema
// already carries id == template id end-to-end, so driving it through
// create -> apply -> plan(empty) -> import -> plan(empty) exercises the exact
// Read() codepath (state.ID.ValueString() -> GET
// /api/v2/application_templates/{id}) a migrated resource's next refresh takes.
//
// Same *_MockServer idiom as resource_cloud_c3_lifecycle_acc_test.go:
// httptest server + testAccProviderBlock, no real infra, no
// ANYSCALE_TEST_REAL_INFRA gate.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// newRegistryLifecycleMockServer serves a fixed BYOD registry lifecycle: the two
// Create() calls, the two Read() calls (decorated template + build), and
// Delete()'s archive call, all keyed off the same templateID/buildID no
// matter how many times each is hit (create, post-apply refresh, import
// refresh, destroy).
func newRegistryLifecycleMockServer(t *testing.T, templateID, buildID, name, imageURI, rayVersion, digest string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	const revision = 1
	const createdAt = "2024-01-01T00:00:00Z"
	const buildStatus = "succeeded"

	mux.HandleFunc("/api/v2/application_templates/byod", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on application_templates/byod", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "name": %[2]q, "creator_id": "user_mock",
			"created_at": %[3]q, "anonymous": false, "is_default": false
		}}`, templateID, name, createdAt)
	})

	mux.HandleFunc("/api/v2/builds/byod", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on builds/byod", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"docker_image_name": %[3]q, "ray_version": %[4]q,
			"revision": %[5]d, "creator_id": "user_mock", "status": %[6]q,
			"created_at": %[7]q, "last_modified_at": %[7]q, "is_byod": true,
			"digest": %[8]q
		}}`, buildID, templateID, imageURI, rayVersion, revision, buildStatus, createdAt, digest)
	})

	// GET by id: this is the headline gate. Read() calls
	// state.ID.ValueString() into this path - a test server keyed at
	// buildID instead of templateID would prove the bug (id still holding
	// the build id) rather than catching it.
	mux.HandleFunc("/api/v2/application_templates/"+templateID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on application_templates/%s", r.Method, templateID)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "name": %[2]q, "creator_id": "user_mock",
			"created_at": %[3]q, "anonymous": false, "is_default": false,
			"latest_build": {"id": %[4]q, "revision": %[5]d, "status": %[6]q}
		}}`, templateID, name, createdAt, buildID, revision, buildStatus)
	})

	mux.HandleFunc("/api/v2/builds/"+buildID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on builds/%s", r.Method, buildID)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Read()'s build fetch allows both 200 and 201 specifically because
		// the real API returns 201 here - exercised deliberately rather than
		// the more obvious 200, so this test would catch a regression that
		// narrowed the accepted status list back down to just 200.
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"docker_image_name": %[3]q, "ray_version": %[4]q,
			"revision": %[5]d, "creator_id": "user_mock", "status": %[6]q,
			"created_at": %[7]q, "last_modified_at": %[7]q, "is_byod": true,
			"digest": %[8]q
		}}`, buildID, templateID, imageURI, rayVersion, revision, buildStatus, createdAt, digest)
	})

	mux.HandleFunc("/api/v2/application_templates/"+templateID+"/archive", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on application_templates/%s/archive", r.Method, templateID)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {"archived_at": "2024-01-01T00:00:01Z"}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccContainerImageRegistryResource_Lifecycle_MockServer proves a registry
// resource's id is the application template id (never the build id) all the
// way through create -> apply -> plan(empty) -> import-by-id -> plan(empty).
// id is the resource's only identity attribute and import keys on it, so a
// clean ImportStateVerify here is the import coverage for that identity.
func TestAccContainerImageRegistryResource_Lifecycle_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_f3_lifecycle_mock"
	const buildID = "bld_f3_lifecycle_mock"
	const name = "tfacc-f3-lifecycle-mock"
	const imageURI = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-f3-lifecycle:v1"
	const rayVersion = "2.44.0"
	const digest = "sha256:f3lifecyclemock0000000000000000000000000000000000000000000000"

	server := newRegistryLifecycleMockServer(t, templateID, buildID, name, imageURI, rayVersion, digest)
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
					// The money assertion: id must be the TEMPLATE id, never
					// the build id - the schema-v0 behavior this guards
					// against a regression to.
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "id", templateID),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "build_id", buildID),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "image_uri", imageURI),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "ray_version", rayVersion),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "digest", digest),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "build_status", "succeeded"),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "name_version", fmt.Sprintf("%s:1", name)),
				),
				// Read stability: state populated at create through the
				// real Read() codepath (GET application_templates/{id}
				// keyed by the template id) must not diff on the next plan.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Import by the resource's own id, which is the template id.
			// name is not ignored: the import refresh fills it from the API
			// (Read sets name when state holds null).
			{
				ResourceName:      "anyscale_container_image_registry.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"registry_login_secret", // sensitive: import does not recover it
				},
			},
			// ImportStateVerify above only proves imported state matches
			// created state - both sides see the same API-echoed value - so
			// it cannot catch an operator-typed config value diverging from
			// state on the next plan. This step re-applies the create config
			// and asserts a true no-op.
			//
			// This plan runs against the state carried forward from the
			// apply step, not the imported state: terraform-plugin-testing
			// discards an ImportState step's result unless
			// ImportStatePersist is set, and setting it here fails with
			// "Resource already managed by Terraform". So this step guards
			// against config/state divergence introduced by create, not
			// import recovery; import recovery of name is proven by the
			// two-test shape in resource_container_image_registry_name_acc_test.go.
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_container_image_registry.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}
