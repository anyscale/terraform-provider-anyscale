package acctest

// container_image_registry.name import round-trip - see
// docs/decisions/container-image-registry-name for the full design and closed gates.
//
// name is now Optional+Computed+RequiresReplace+UseStateForUnknown, mirroring ray_version on
// this same resource. Two tests, because a Create -> Import -> re-apply -> assert-no-op sequence
// provably cannot detect an import-recovery bug (ImportState without ImportStatePersist runs in a
// throwaway working directory - see the package doc comment on testStepNewImportState in
// terraform-plugin-testing for why a later step plans against what Create left, never against
// what import recovered):
//
//   - Test A (cold import) proves what import actually recovers, via ImportStateCheck inside the
//     import step itself - the only place a throwaway-directory import's result is observable.
//   - Test B (two Config-only steps, no import) proves planning against the recovered shape is
//     stable - these DO carry state forward, reconstructing what import would leave.
//
// Both mocks return a name that is NOT derivable from image_uri (no sanitizeImageURIForName
// output would ever produce it) - the fixture trap the design doc calls out: a name the provider
// could have reconstructed from config would make these tests pass against a broken fix that
// never actually reads the backend's value.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// newRegistryColdImportMockServer serves only the two GET endpoints Read() needs
// (decorated template + build) - deliberately no Create/Delete handlers, so a stray
// request to either fails the test loudly rather than silently mocking a Create that
// this scenario (a "pre-seeded" object, per the design doc) must never issue.
func newRegistryColdImportMockServer(t *testing.T, templateID, buildID, realBackendName, imageURI string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	const revision = 1
	const createdAt = "2024-01-01T00:00:00Z"

	mux.HandleFunc("/api/v2/application_templates/"+templateID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on application_templates/%s - cold import must never Create", r.Method, templateID)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "name": %[2]q, "creator_id": "user_mock",
			"created_at": %[3]q, "anonymous": false, "is_default": false,
			"latest_build": {"id": %[4]q, "revision": %[5]d, "status": "succeeded"}
		}}`, templateID, realBackendName, createdAt, buildID, revision)
	})

	mux.HandleFunc("/api/v2/builds/"+buildID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on builds/%s", r.Method, buildID)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"docker_image_name": %[3]q,
			"revision": %[4]d, "creator_id": "user_mock", "status": "succeeded",
			"created_at": %[5]q, "last_modified_at": %[5]q, "is_byod": true,
			"digest": "sha256:coldimportmock0000000000000000000000000000000000000000000000"
		}}`, buildID, templateID, imageURI, revision, createdAt)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccContainerImageRegistryResource_NameColdImportRecoversRealBackendValue is Test A from the
// design doc: a cluster environment that exists only in the backend (no Create in this test at
// all - "seeded out of band", modeled here by the mock always having it ready) must come back with
// its real name on import, not null. Before name became Computed, ImportState was a bare
// ImportStatePassthroughID recovering nothing, so this would have failed.
func TestAccContainerImageRegistryResource_NameColdImportRecoversRealBackendValue(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_coldimport_mock"
	const buildID = "bld_coldimport_mock"
	const imageURI = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-coldimport:v1"
	// Deliberately NOT derivable from imageURI (see file header) - if the fix regressed to
	// reconstructing a name locally instead of reading the backend's, this would still be null
	// or a sanitizeImageURIForName-shaped string, never this.
	const realBackendName = "tfacc-coldimport-real-backend-only-4f8a1c"

	server := newRegistryColdImportMockServer(t, templateID, buildID, realBackendName, imageURI)

	// image_uri is Required, so a config for import must still declare it - name is
	// deliberately omitted, since the whole point is that import recovers it independent of
	// config, not that config supplies it.
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  image_uri = %[1]q
}
`, imageURI)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:  "anyscale_container_image_registry.test",
				ImportState:   true,
				ImportStateId: templateID,
				Config:        config,
				// ImportStateVerify is unavailable here - there is no prior apply in this
				// test to verify against (that is the whole point of "cold"). ImportStateCheck
				// inspecting the raw imported state is the only way to observe what a
				// throwaway-directory import actually recovered.
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					got, ok := states[0].Attributes["name"]
					if !ok || got == "" {
						return fmt.Errorf("name = %q (present=%v), want the real backend name %q - cold import recovered nothing", got, ok, realBackendName)
					}
					if got != realBackendName {
						return fmt.Errorf("name = %q, want %q (the real backend value, not something reconstructed from image_uri)", got, realBackendName)
					}
					return nil
				},
			},
		},
	})
}

// TestAccContainerImageRegistryResource_NamePlanStableOnceRecovered is Test B from the design
// doc: two Config-only steps (no import at all) that DO carry state forward, reconstructing the
// exact shape import would leave - step 1 omits name (so Create/Read populate it from the
// backend, matching what a cold import would also recover), step 2 declares it explicitly at
// that recovered value. Asserts both an empty plan AND the real value - ExpectEmptyPlan alone is
// a placebo here: Gate 2 proved it stays green even with Read's fill-on-null removed, since a
// stably-null state trivially plans empty regardless of whether the attribute ever gets filled.
func TestAccContainerImageRegistryResource_NamePlanStableOnceRecovered(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_planstable_mock"
	const buildID = "bld_planstable_mock"
	const imageURI = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-planstable:v1"
	// Same non-derivable-from-image_uri requirement as Test A. This mock always echoes this
	// fixed name regardless of what Create's request actually contains (matching
	// newRegistryF3MockServer's established shape in this package) - realistic per Gate 1
	// (the backend echoes verbatim, never renames), and means step 2's config can declare a
	// value known in advance rather than trying to predict Create's live-timestamped fallback.
	const realBackendName = "tfacc-planstable-real-backend-only-7d2b9e"

	server := newRegistryF3MockServer(t, templateID, buildID, realBackendName, imageURI, "2.44.0", "sha256:planstablemock000000000000000000000000000000000000000000000000")

	configNameOmitted := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  image_uri   = %[1]q
  ray_version = "2.44.0"
}
`, imageURI)

	configNameDeclared := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name        = %[1]q
  image_uri   = %[2]q
  ray_version = "2.44.0"
}
`, realBackendName, imageURI)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Step 1: name omitted. Create fills it from the mock's response (Unknown ->
				// the real backend value), reconstructing what a cold import would also see.
				Config: configNameOmitted,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "name", realBackendName),
				),
			},
			{
				// Step 2: config now declares name at exactly the value step 1 recovered -
				// reconstructing the shape a cold import followed by a config update to match
				// would leave. The original bug: state null, config sets a value, plans
				// null -> value on RequiresReplace = destroy-and-recreate. This step proves
				// that exact transition (recovered non-null state, config now declaring the
				// same value) no longer forces a replace.
				Config: configNameDeclared,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "name", realBackendName),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}
