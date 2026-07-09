package acctest

// GATE-F5.4: mock-server proof that the anyscale_container_image data source's
// `digest` attribute (added alongside the resource-side digest work, see
// resource_container_image_registry_digest_acc_test.go and
// resource_container_image_build_digest_acc_test.go for the resource-side
// gates) actually gets populated from the live build, through BOTH of the data
// source's lookup paths (by id, by name) -- not just one. data_source_container_image.go's
// Read() resolves the two paths through genuinely different API calls
// (getApplicationTemplateByID: a single GET; getApplicationTemplateByName: a
// paginated list-and-filter), each returning its own ApplicationTemplateResult,
// so a bug isolated to one path (e.g. the list response's LatestBuild wiring)
// would not be caught by only exercising the other.
//
// Deliberately independent of the V1(c) cluster_environment_id removal: this
// data source has only ever exposed `id`/`name` as its lookup keys (confirmed
// by inspection -- there is no cluster_environment_id attribute or reference
// anywhere in data_source_container_image.go or data_source_container_images.go),
// so this test needs no changes once that removal lands on the registry
// resource and does not need to wait for it.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// newContainerImageDigestDataSourceMockServer serves a single fixed
// application template + build pair through all three endpoints the data
// source's two lookup paths can hit: GET by id (decorated), GET the list
// (decorated, for name_contains filtering), and GET the build by id.
func newContainerImageDigestDataSourceMockServer(t *testing.T, templateID, buildID, name, imageURI, rayVersion, digest string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	const revision = 3
	const createdAt = "2024-01-01T00:00:00Z"
	const buildStatus = "succeeded"

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

	// By-name lookup goes through the LIST endpoint (name_contains filter),
	// not the single-item GET above -- a real, separate call, not a re-use of
	// the by-id path. The list result must carry its own latest_build; that's
	// what lets getApplicationTemplateByName return a template already fit to
	// resolve the build from, with no second template fetch.
	mux.HandleFunc("/api/v2/application_templates/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on application_templates/", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results": [{
			"id": %[1]q, "name": %[2]q, "creator_id": "user_mock",
			"created_at": %[3]q, "anonymous": false, "is_default": false,
			"latest_build": {"id": %[4]q, "revision": %[5]d, "status": %[6]q}
		}], "metadata": {"total": 1, "next_paging_token": null}}`, templateID, name, createdAt, buildID, revision, buildStatus)
	})

	mux.HandleFunc("/api/v2/builds/"+buildID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on builds/%s", r.Method, buildID)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Read()'s build fetch allows both 200 and 201 specifically because the
		// real API returns 201 here (see resource_container_image_registry.go's
		// Read() and resource_container_image_registry_lifecycle_acc_test.go) --
		// exercised deliberately rather than the more obvious 200.
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"docker_image_name": %[3]q, "ray_version": %[4]q,
			"revision": %[5]d, "creator_id": "user_mock", "status": %[6]q,
			"created_at": %[7]q, "last_modified_at": %[7]q, "is_byod": true,
			"digest": %[8]q
		}}`, buildID, templateID, imageURI, rayVersion, revision, buildStatus, createdAt, digest)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccContainerImageDataSource_DigestPopulatesFromLatestBuild_MockServer is
// the GATE-F5.4 proof: both the by-id and by-name lookup paths surface the
// SAME latest build's digest, proving Read() resolves digest correctly
// through either route rather than one being a coincidental pass-through of
// the other's result.
func TestAccContainerImageDataSource_DigestPopulatesFromLatestBuild_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_f5_4_ds_digest_mock"
	const buildID = "bld_f5_4_ds_digest_mock"
	const name = "tfacc-f5-4-ds-digest-mock"
	const imageURI = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-f5-4-ds-digest:v1"
	const rayVersion = "2.44.0"
	const digest = "sha256:f54dsdigestmock00000000000000000000000000000000000000000000000"

	server := newContainerImageDigestDataSourceMockServer(t, templateID, buildID, name, imageURI, rayVersion, digest)
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
data "anyscale_container_image" "by_id" {
  id = %[1]q
}

data "anyscale_container_image" "by_name" {
  name = %[2]q
}
`, templateID, name)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					// The money assertions: digest resolves to the build's real
					// value through BOTH lookup paths, not just one.
					resource.TestCheckResourceAttr("data.anyscale_container_image.by_id", "digest", digest),
					resource.TestCheckResourceAttr("data.anyscale_container_image.by_name", "digest", digest),
					resource.TestCheckResourceAttr("data.anyscale_container_image.by_id", "build_id", buildID),
					resource.TestCheckResourceAttr("data.anyscale_container_image.by_name", "build_id", buildID),
					resource.TestCheckResourceAttr("data.anyscale_container_image.by_id", "revision", "3"),
					resource.TestCheckResourceAttr("data.anyscale_container_image.by_id", "name_version", fmt.Sprintf("%s:3", name)),
				),
			},
		},
	})
}
