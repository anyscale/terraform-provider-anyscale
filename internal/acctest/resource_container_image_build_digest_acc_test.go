// GATE-F5 (build-resource half): digest has NO plan modifiers on
// anyscale_container_image_build, deliberately, unlike its sibling registry
// resource's Computed+UseStateForUnknown digest. A rebuild (containerfile change on
// Update, or a fresh build on Create) genuinely produces a new digest every time, so
// pinning it to the prior state value would be wrong. This proves a containerfile-driven
// rebuild transitions digest to a new, different, known-after-apply value with no
// Terraform Core error - specifically not the "provider produced inconsistent result
// after apply" class that UseStateForUnknown would trigger here, since Core would then
// reject a value that changed despite the plan promising continuity.
package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// checkDigestChanged asserts that resourceName's current digest attribute is
// non-empty and NOT equal to staleDigest. This is deliberately independent of the
// literal-value TestCheckResourceAttr assertions in the test step: even if a mock
// or fixture bug happened to make the new digest coincidentally equal to the
// literal constant a step expects, this check would still catch a regression
// where the resource actually failed to move off the prior build's digest.
func checkDigestChanged(resourceName, staleDigest string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}

		digest := rs.Primary.Attributes["digest"]
		if digest == "" {
			return fmt.Errorf("digest attribute is empty after rebuild")
		}
		if digest == staleDigest {
			return fmt.Errorf("digest did not change after rebuild: still %q (would indicate a stale UseStateForUnknown-style pin)", staleDigest)
		}

		return nil
	}
}

// newBuildDigestMockServer serves a single application template whose builds
// endpoint reports digest1 for the first (create) build and digest2 for the second
// (update/rebuild) build. Both builds are immediately "succeeded" so waitForBuild's
// poll loop in resource_container_image_build.go terminates on the first check.
//
// Real contract this mirrors (resource_container_image_build.go):
//   - Create(): POST /api/v2/application_templates/ (bare create, no latest_build) ->
//     GET /api/v2/application_templates/{id} (decorated, carries latest_build.id) ->
//     GET /api/v2/builds/{id} (decorated Build, carries digest) via waitForBuild.
//   - Update(): when containerfile changes, POST /api/v2/builds/ creates a new build
//     for the SAME application_template_id -> GET /api/v2/builds/{id} again via
//     waitForBuild, resolving to the second build's digest.
//
// Both handlers return the real BuildResponse envelope shape ({"result": {...
// BuildResult fields}}), with digest as a plain top-level string field (never
// omitted here - this fixture doesn't need to exercise the nil-digest branch, only
// the value-changes-between-builds branch).
func newBuildDigestMockServer(t *testing.T, templateID, name, buildID1, digest1, buildID2, digest2 string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	const createdAt = "2024-01-01T00:00:00Z"
	const buildStatus = "succeeded"

	// currentBuildID tracks which build GET /api/v2/application_templates/{id} should
	// report as latest_build, and which build ID the shared /api/v2/builds/ GET
	// handler (registered per-ID below) should currently be answering for. Update()
	// flips this after the second build is created, mirroring the real backend's
	// "latest_build always reflects the newest build" behavior.
	currentBuildID := buildID1

	mux.HandleFunc("/api/v2/application_templates/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on /api/v2/application_templates/", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		// Bare create response: no latest_build, matching the real contract that
		// Create() re-fetches the decorated template rather than trusting this
		// response to carry a build reference.
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "name": %[2]q, "creator_id": "user_mock",
			"created_at": %[3]q, "anonymous": false, "is_default": false
		}}`, templateID, name, createdAt)
	})

	mux.HandleFunc("/api/v2/application_templates/"+templateID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on /api/v2/application_templates/%s", r.Method, templateID)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "name": %[2]q, "creator_id": "user_mock",
			"created_at": %[3]q, "anonymous": false, "is_default": false,
			"latest_build": {"id": %[4]q, "revision": 1, "status": %[5]q}
		}}`, templateID, name, createdAt, currentBuildID, buildStatus)
	})

	// New build creation for Update(): CreateBuildRequest{application_template_id,
	// containerfile} -> bare BuildResponse for the second build. This is what
	// containerfileChanged triggers instead of a call to application_templates/.
	mux.HandleFunc("/api/v2/builds/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on /api/v2/builds/", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		currentBuildID = buildID2
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"revision": 2, "creator_id": "user_mock", "status": %[3]q,
			"created_at": %[4]q, "last_modified_at": %[4]q, "is_byod": false,
			"digest": %[5]q
		}}`, buildID2, templateID, buildStatus, createdAt, digest2)
	})

	// getBuild / waitForBuild polling target for the first build (Create + every
	// subsequent Read while state.build_id is buildID1).
	mux.HandleFunc("/api/v2/builds/"+buildID1, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on /api/v2/builds/%s", r.Method, buildID1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated) // getBuild accepts 200 or 201 for GET
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"revision": 1, "creator_id": "user_mock", "status": %[3]q,
			"created_at": %[4]q, "last_modified_at": %[4]q, "is_byod": false,
			"digest": %[5]q
		}}`, buildID1, templateID, buildStatus, createdAt, digest1)
	})

	// getBuild / waitForBuild polling target for the second (rebuilt) build.
	mux.HandleFunc("/api/v2/builds/"+buildID2, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on /api/v2/builds/%s", r.Method, buildID2)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"revision": 2, "creator_id": "user_mock", "status": %[3]q,
			"created_at": %[4]q, "last_modified_at": %[4]q, "is_byod": false,
			"digest": %[5]q
		}}`, buildID2, templateID, buildStatus, createdAt, digest2)
	})

	mux.HandleFunc("/api/v2/application_templates/"+templateID+"/archive", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on /api/v2/application_templates/%s/archive", r.Method, templateID)
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

// TestAccContainerImageBuildResource_DigestUpdatesOnRebuild_MockServer is the money
// test for GATE-F5's build-resource half: a containerfile change on Update must
// trigger a real rebuild whose digest transitions to a genuinely different value,
// and the apply must complete with NO Terraform Core error. If digest carried
// UseStateForUnknown (the registry resource's pattern, wrongly copied here), Core
// would reject this exact scenario with "Provider produced inconsistent result
// after apply" the moment the post-apply value diverged from the plan's promised
// continuity - so a clean, error-free apply plus a real D1->D2 change is the whole
// point of this test, not two separate concerns.
func TestAccContainerImageBuildResource_DigestUpdatesOnRebuild_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_f5_build_digest_mock"
	const name = "tfacc-f5-build-digest-mock"
	const buildID1 = "bld_f5_digest_v1_mock"
	const buildID2 = "bld_f5_digest_v2_mock"
	// Deliberately distinct fake digests (not just cosmetically different strings -
	// no shared substring beyond the sha256: prefix) so an accidental "always return
	// D1" regression in the mock itself, or a UseStateForUnknown regression in the
	// resource, cannot coincidentally satisfy a weaker equality check.
	const digest1 = "sha256:f5build0d1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const digest2 = "sha256:f5build0d2bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	server := newBuildDigestMockServer(t, templateID, name, buildID1, digest1, buildID2, digest2)

	containerfileV1 := `FROM anyscale/ray:2.53.0-slim-py312
RUN pip install emoji==2.15.0`

	containerfileV2 := `FROM anyscale/ray:2.53.0-slim-py312
RUN pip install emoji==2.15.0
RUN echo "rebuild triggers a new digest"`

	configFor := func(containerfile string) string {
		return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_build" "test" {
  name          = %[1]q
  containerfile = <<-EOF
%[2]s
EOF
  build_timeout = "5m"
}
`, name, containerfile)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Step 1: initial create. Build 1's response reports digest1; this is
			// the baseline the rebuild in step 2 must move away from.
			{
				Config: configFor(containerfileV1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "id", templateID),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "build_id", buildID1),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "revision", "1"),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "digest", digest1),
				),
			},
			// Step 2: containerfile changes (V1 -> V2), triggering Update()'s
			// rebuild path (POST /api/v2/builds/ for a new build under the same
			// application_template_id). The mock's build 2 reports digest2.
			//
			// Money assertions: apply succeeds (resource.Test already fails the
			// test on any Core/provider error, including "inconsistent result
			// after apply" - so reaching this Check at all is part of the proof),
			// build_id/revision advance, and digest lands on digest2 - a value
			// that is genuinely different from digest1, not coincidentally equal.
			{
				Config: configFor(containerfileV2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "id", templateID),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "build_id", buildID2),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "revision", "2"),
					resource.TestCheckResourceAttr("anyscale_container_image_build.test", "digest", digest2),
					checkDigestChanged("anyscale_container_image_build.test", digest1),
				),
			},
		},
	})
}
