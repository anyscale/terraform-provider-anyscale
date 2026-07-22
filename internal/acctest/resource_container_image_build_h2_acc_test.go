// DRAFT by tfp-assayer, 2026-07-22 - PR2 (timeouts{} migration). Closes a
// real, pre-existing coverage gap I found during the PR2 survey: unlike
// service's well-tested H2 mechanism (TestAccServiceResource_
// UpdateSkipsApplyWhenOnlyTimeoutChanges), NOTHING today proves
// container_image_build's Update short-circuit (containerfileChanged check,
// resource_container_image_build.go ~426-429) actually skips a real second
// build when only the timeout changes - this is new coverage, not a repoint.
//
// Mock endpoint shapes (POST /api/v2/application_templates/, POST
// /api/v2/builds/, GET /api/v2/builds/{id}, POST .../archive) are copied
// directly from this file's own newBuildDigestMockServer (same package,
// resource_container_image_build_digest_acc_test.go) - verified against
// real, already-working mock code, not invented. NOT yet run against
// forge's real implementation (their worktree is mid-edit on a different
// resource right now) - flagging for review before treating as verified.

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

// newBuildTimeoutOnlyMockServer is a minimal single-build variant of
// newBuildDigestMockServer, with a counter on the POST /api/v2/builds/
// endpoint (the real "create a new build" call) - the one thing this test
// exists to prove is NOT hit a second time.
func newBuildTimeoutOnlyMockServer(t *testing.T, buildCallCount *int32, templateID, name, buildID, digest string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	const createdAt = "2024-01-01T00:00:00Z"
	const buildStatus = "succeeded"

	mux.HandleFunc("/api/v2/application_templates/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on /api/v2/application_templates/", r.Method)
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
		}}`, templateID, name, createdAt, buildID, buildStatus)
	})

	// THE call under test: a real new-build POST. Counted so the test can
	// assert it fires exactly once (from Create only).
	mux.HandleFunc("/api/v2/builds/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on /api/v2/builds/", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		atomic.AddInt32(buildCallCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"revision": 1, "creator_id": "user_mock", "status": %[3]q,
			"created_at": %[4]q, "last_modified_at": %[4]q, "is_byod": false,
			"digest": %[5]q
		}}`, buildID, templateID, buildStatus, createdAt, digest)
	})

	mux.HandleFunc("/api/v2/builds/"+buildID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on /api/v2/builds/%s", r.Method, buildID)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"revision": 1, "creator_id": "user_mock", "status": %[3]q,
			"created_at": %[4]q, "last_modified_at": %[4]q, "is_byod": false,
			"digest": %[5]q
		}}`, buildID, templateID, buildStatus, createdAt, digest)
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

// TestAccContainerImageBuildResource_UpdateSkipsBuildWhenOnlyTimeoutChanges
// closes the coverage gap: containerfileChanged (resource_container_image_build.go
// ~426-429) must skip the real build-creation call when only timeouts changes,
// mirroring service's H2 proof for the exact same PR2 migration concern.
func TestAccContainerImageBuildResource_UpdateSkipsBuildWhenOnlyTimeoutChanges(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "cet_h2_timeout_only"
	const buildID = "bld_h2_timeout_only"
	var buildCallCount int32

	server := newBuildTimeoutOnlyMockServer(t, &buildCallCount, templateID, "h2-build-timeout-only", buildID, "sha256:h2timeoutonly")

	containerfile := `FROM anyscale/ray:2.53.0-slim-py312
RUN pip install emoji==2.15.0`

	baseConfig := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_build" "test" {
  name          = "h2-build-timeout-only"
  containerfile = %[1]q
}
`, containerfile)

	timeoutOnlyConfig := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_build" "test" {
  name          = "h2-build-timeout-only"
  containerfile = %[1]q
  timeouts {
    update = "45m"
  }
}
`, containerfile)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: baseConfig,
				Check:  resource.TestCheckResourceAttr("anyscale_container_image_build.test", "build_status", "succeeded"),
			},
			{
				Config: timeoutOnlyConfig,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_container_image_build.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})

	// Want exactly 0, not 1: Create() never calls POST /api/v2/builds/ directly - creating
	// the application template implicitly triggers its first build server-side, and Create()
	// only resolves that build's ID via a GET (see getLatestBuildID). POST /api/v2/builds/ is
	// only ever hit by Update's containerfile-changed branch, which this test's containerfile
	// never changes - so the real assertion is "this endpoint is never hit at all", proving
	// the timeouts-only Update doesn't trigger a new build any more than Create did.
	if got := atomic.LoadInt32(&buildCallCount); got != 0 {
		t.Errorf("POST /api/v2/builds/ was called %d time(s) after a timeouts-only change (want exactly 0 - "+
			"Create never calls this endpoint directly, and Update must not trigger a real new build just "+
			"because a client-local wait knob changed)", got)
	}
}
