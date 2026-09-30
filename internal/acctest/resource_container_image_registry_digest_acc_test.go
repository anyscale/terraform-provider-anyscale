// Registry-side build-mirror attributes: pinned across an ordinary refresh, yet still
// advancing non-destructively when the backend's latest build changes out from under Terraform.
//
// build_id, revision, name_version, digest, and build_status (containerImageRegistryAttributes()
// in resource_container_image_registry.go) carry only UseStateForUnknown - no RequiresReplace.
// The resource's identity is the application template (id); Read() re-fetches that template's
// current latest build on every refresh (GET application_templates/{id} for the latest_build
// stub, then GET builds/{id} for the detail). A build registered against the same template
// outside Terraform (CLI, console) must therefore be absorbed as ordinary drift on these
// Computed fields, never a Replace/Destroy.
//
// What the transition's plan action actually is: for a Computed, non-Optional attribute,
// Terraform Core's implicit refresh folds the new backend value into prior state BEFORE the
// plan-vs-config diff runs (objchange.proposedNewAttributes sets newV = priorV for non-Optional
// attributes), and none of these five attributes appear in config. So the plan that performs
// the transition is NoOp, not Update - no code path holds the pre- and post-refresh values at
// once. The same ordering means a RequiresReplace on one of these attributes would not observe
// the transition either, so this test does not claim to catch that; it pins the verified NoOp
// action, proves state really advanced, and proves the new values are then stable.
//
// Two tests, one per half:
//   - DigestStableAcrossRefresh: nothing changes backend-side between two refreshes -> the
//     second refresh's plan must be truly empty (plancheck.ExpectEmptyPlan()).
//   - LatestBuildAdvance_UpdatesNoReplace: the mock's "latest build" advances out-of-band
//     between two refreshes (new build_id/revision/name_version/digest/build_status, same
//     template). The transitioning plan must be plancheck.ResourceActionNoop, state must show
//     build-B's values (Check), and a further refresh against build-B must be empty.
package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// registryBuildSnapshot is everything Read() re-derives from "the current latest build" on
// every refresh: the MiniBuildResult stub embedded on the decorated application_templates GET
// (id/revision/status) plus the full BuildResult detail from the decorated builds GET
// (id/revision/status/created_at/is_byod/digest). Both handlers below serve from the SAME
// snapshot so the two endpoints never disagree with each other, mirroring the real backend
// (both are derived from the one latest build row).
type registryBuildSnapshot struct {
	buildID     string
	revision    int
	buildStatus string
	createdAt   string
	digest      string
}

// digestMockRegistryServer serves a BYOD registry lifecycle whose "latest build" can be
// swapped out mid-test via advanceLatestBuild, simulating a build registered against the same
// application template out-of-band (i.e. not through this Terraform resource). This is the one
// behavior newRegistryLifecycleMockServer/newRegistryRayVersionMockServer do not have: their GET handlers are
// closed over fixed values for the lifetime of the httptest.Server. Mutable
// state guarded by a mutex mirrors the established pattern in
// resource_compute_config_lifecycle_acc_test.go's mockComputeConfigServer, sized down to the
// single field this test needs to flip.
type digestMockRegistryServer struct {
	mu        sync.Mutex
	current   registryBuildSnapshot
	serverURL string
}

func (s *digestMockRegistryServer) snapshot() registryBuildSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// advanceLatestBuild swaps in a new "latest build" snapshot. Call this between TestSteps (not
// concurrently with an in-flight request) to simulate the backend's latest build changing
// out-of-band between two refreshes of the same application template.
func (s *digestMockRegistryServer) advanceLatestBuild(next registryBuildSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = next
}

// newDigestMockRegistryServer wires up the same endpoint shape as newRegistryLifecycleMockServer /
// newRegistryRayVersionMockServer (create template, create build, GET template, GET build, archive),
// but the two GET handlers read from the server's mutable snapshot instead of closing over
// fixed values, so a test can call advanceLatestBuild between steps to change what the NEXT
// refresh sees without needing a new httptest.Server or a new resource.
func newDigestMockRegistryServer(t *testing.T, templateID, name, imageURI, rayVersion string, initial registryBuildSnapshot) *digestMockRegistryServer {
	t.Helper()
	mux := http.NewServeMux()

	const createdAt = "2024-01-01T00:00:00Z"

	s := &digestMockRegistryServer{current: initial}

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

	// Bare create response (Call 2 of Create()): response_model=Response[Build] on the real
	// API, so this always reports whatever the FIRST snapshot was - Create() only ever runs
	// once, before any advanceLatestBuild call, so there is no ambiguity about which snapshot
	// this should serve. Field shape matches BuildResult (models.go): id,
	// application_template_id, docker_image_name, revision, status, created_at,
	// last_modified_at, is_byod, digest.
	mux.HandleFunc("/api/v2/builds/byod", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on builds/byod", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		snap := s.snapshot()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"docker_image_name": %[3]q, "ray_version": %[4]q,
			"revision": %[5]d, "creator_id": "user_mock", "status": %[6]q,
			"created_at": %[7]q, "last_modified_at": %[7]q, "is_byod": true,
			"digest": %[8]q
		}}`, snap.buildID, templateID, imageURI, rayVersion, snap.revision, snap.buildStatus, snap.createdAt, snap.digest)
	})

	// GET application_templates/{id}: decorated response carrying the latest_build stub
	// (MiniBuildResult: id/revision/status only - matches models.go exactly, no digest or
	// created_at at this layer). Read() calls this first on every refresh; serving from the
	// live snapshot is what lets advanceLatestBuild change what the NEXT refresh observes.
	mux.HandleFunc("/api/v2/application_templates/"+templateID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on application_templates/%s", r.Method, templateID)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		snap := s.snapshot()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "name": %[2]q, "creator_id": "user_mock",
			"created_at": %[3]q, "anonymous": false, "is_default": false,
			"latest_build": {"id": %[4]q, "revision": %[5]d, "status": %[6]q}
		}}`, templateID, name, createdAt, snap.buildID, snap.revision, snap.buildStatus)
	})

	// GET builds/{id}: this handler is registered once, keyed at the FIRST snapshot's
	// buildID. That is deliberate, not an oversight - the whole point of this test's
	// volatility half is that build-B's id differs from build-A's, and Go's ServeMux can only
	// route a fixed path to a fixed handler. Re-registering a second literal path for
	// build-B's id would work for THIS test's exact two IDs, but would silently stop matching
	// the moment either ID changed, which defeats the "genuinely distinct values" requirement
	// this test is built around. Instead, this single handler ignores which literal id
	// segment it was actually called with and always answers with whatever the CURRENT
	// snapshot is - correctly matching the real backend's contract of "GET the latest build
	// for this application template", since template.LatestBuild.ID (fed into this URL by
	// Read()) and the snapshot below always advance together via advanceLatestBuild.
	// Registered under both the subtree and bare-path forms (see
	// helpers_cloud_adoption_test.go: a subtree-only mock makes ServeMux
	// 301-redirect a bare-path request, and whether that redirect is followed
	// is not portable across Go versions/http.Client configs). Safe here because
	// this handler (see comment above) never inspects the literal id segment.
	buildsHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		snap := s.snapshot()
		w.Header().Set("Content-Type", "application/json")
		// Read()'s build fetch allows both 200 and 201 specifically because the real API
		// returns 201 here (see resource_container_image_registry_lifecycle_acc_test.go).
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"docker_image_name": %[3]q, "ray_version": %[4]q,
			"revision": %[5]d, "creator_id": "user_mock", "status": %[6]q,
			"created_at": %[7]q, "last_modified_at": %[7]q, "is_byod": true,
			"digest": %[8]q
		}}`, snap.buildID, templateID, imageURI, rayVersion, snap.revision, snap.buildStatus, snap.createdAt, snap.digest)
	}
	mux.HandleFunc("/api/v2/builds/", buildsHandler)
	mux.HandleFunc("/api/v2/builds", buildsHandler)

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
	s.serverURL = server.URL
	return s
}

// TestAccContainerImageRegistryResource_DigestStableAcrossRefresh_MockServer proves the
// "nothing changed" half: build_id, revision, name_version, digest, and build_status must
// all be pinned by UseStateForUnknown() across a refresh where the backend's latest build has
// not moved - producing a truly EMPTY plan, not just an unchanged-but-still-planned attribute
// set. The lifecycle and ray_version tests hold this resource to the same bar; this test
// isolates it for the five build-mirror attributes together.
func TestAccContainerImageRegistryResource_DigestStableAcrossRefresh_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_f5_stable_mock"
	const buildIDA = "bld_f5_stable_a_mock"
	const name = "tfacc-f5-stable-mock"
	const imageURI = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-f5-stable:v1"
	const rayVersion = "2.44.0"

	buildA := registryBuildSnapshot{
		buildID:     buildIDA,
		revision:    1,
		buildStatus: "succeeded",
		createdAt:   "2024-01-01T00:00:00Z",
		digest:      "sha256:f5stablebuildaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	mock := newDigestMockRegistryServer(t, templateID, name, imageURI, rayVersion, buildA)

	resourceAddress := "anyscale_container_image_registry.test"
	config := testAccProviderBlock(mock.serverURL) + fmt.Sprintf(`
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
					resource.TestCheckResourceAttr(resourceAddress, "id", templateID),
					resource.TestCheckResourceAttr(resourceAddress, "build_id", buildA.buildID),
					resource.TestCheckResourceAttr(resourceAddress, "revision", "1"),
					resource.TestCheckResourceAttr(resourceAddress, "digest", buildA.digest),
					resource.TestCheckResourceAttr(resourceAddress, "build_status", buildA.buildStatus),
					resource.TestCheckResourceAttr(resourceAddress, "name_version", fmt.Sprintf("%s:1", name)),
				),
				// Post-apply refresh already exercises Read() once against an unchanged
				// backend - must not be treated as drift.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				// Unchanged config, unchanged mock backend data (advanceLatestBuild is never
				// called in this test): a second, independent refresh must reconfirm the same
				// values and again produce a truly empty plan.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddress, "build_id", buildA.buildID),
					resource.TestCheckResourceAttr(resourceAddress, "revision", "1"),
					resource.TestCheckResourceAttr(resourceAddress, "digest", buildA.digest),
					resource.TestCheckResourceAttr(resourceAddress, "build_status", buildA.buildStatus),
					resource.TestCheckResourceAttr(resourceAddress, "name_version", fmt.Sprintf("%s:1", name)),
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

// TestAccContainerImageRegistryResource_LatestBuildAdvance_UpdatesNoReplace_MockServer proves
// the "backend moved" half: when the cluster environment's latest build advances out-of-band
// (build-A -> build-B, with every one of build_id/revision/name_version/digest/build_status
// genuinely different, not coincidentally similar), the transitioning refresh's own plan must
// come back as plancheck.ResourceActionNoop - per the file header, that is the verified action
// for an implicit-refresh plan over Computed-only attributes with unchanged config. The test
// separately proves state genuinely advanced to build-B's values (Check) and that a further,
// independent refresh against the now-current build-B stays stable (ExpectEmptyPlan), so the
// transition is real and non-destructive even though it does not surface as its own "Update".
func TestAccContainerImageRegistryResource_LatestBuildAdvance_UpdatesNoReplace_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_f5_advance_mock"
	const buildIDA = "bld_f5_advance_a_mock"
	const buildIDB = "bld_f5_advance_b_mock"
	const name = "tfacc-f5-advance-mock"
	const imageURI = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-f5-advance:v1"
	const rayVersion = "2.44.0"

	// build-A and build-B differ in every one of the five fields Read() re-derives, so no
	// assertion below can pass by accident (e.g. matching the OTHER build's value).
	buildA := registryBuildSnapshot{
		buildID:     buildIDA,
		revision:    1,
		buildStatus: "succeeded",
		createdAt:   "2024-01-01T00:00:00Z",
		digest:      "sha256:f5advancebuildaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	buildB := registryBuildSnapshot{
		buildID:     buildIDB,
		revision:    7,
		buildStatus: "pending", // deliberately not "succeeded" - proves build_status itself is re-read, not just echoed
		createdAt:   "2024-06-15T12:34:56Z",
		digest:      "sha256:f5advancebuildbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}

	mock := newDigestMockRegistryServer(t, templateID, name, imageURI, rayVersion, buildA)

	resourceAddress := "anyscale_container_image_registry.test"
	config := testAccProviderBlock(mock.serverURL) + fmt.Sprintf(`
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
				// Create against build-A. Same shape as the stability test's first step.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddress, "id", templateID),
					resource.TestCheckResourceAttr(resourceAddress, "build_id", buildA.buildID),
					resource.TestCheckResourceAttr(resourceAddress, "revision", "1"),
					resource.TestCheckResourceAttr(resourceAddress, "digest", buildA.digest),
					resource.TestCheckResourceAttr(resourceAddress, "build_status", buildA.buildStatus),
					resource.TestCheckResourceAttr(resourceAddress, "name_version", fmt.Sprintf("%s:1", name)),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				// Unchanged config, but the backend's latest build has advanced to build-B
				// (registered against the same application template out-of-band, e.g. via the
				// Anyscale CLI/console rather than through this Terraform resource). This
				// PreConfig is what simulates that: it flips the mock's snapshot immediately
				// before Terraform plans this step, so the plan/apply below is exactly what a
				// user would see running `terraform plan` after such an out-of-band build.
				PreConfig: func() {
					mock.advanceLatestBuild(buildB)
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					// Money assertion: the transitioning plan is NoOp, the verified action for
					// this case (see file header). Refresh has already folded build-B into prior
					// state, so a RequiresReplace on one of the five attributes would not change
					// this action either; what this pins is that the transition plans cleanly,
					// with no Update/Replace and no error, before Check confirms the new values.
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceAddress, plancheck.ResourceActionNoop),
					},
					// A further, automatic post-apply refresh (still against build-B, since
					// nothing calls advanceLatestBuild again) must now be stable, mirroring the
					// stability test's bar but for the NEW values.
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					// Post-apply, state must show build-B's values, not build-A's stale ones.
					resource.TestCheckResourceAttr(resourceAddress, "id", templateID), // identity unaffected - only the build-mirror attrs move
					resource.TestCheckResourceAttr(resourceAddress, "build_id", buildB.buildID),
					resource.TestCheckResourceAttr(resourceAddress, "revision", "7"),
					resource.TestCheckResourceAttr(resourceAddress, "digest", buildB.digest),
					resource.TestCheckResourceAttr(resourceAddress, "build_status", buildB.buildStatus),
					resource.TestCheckResourceAttr(resourceAddress, "name_version", fmt.Sprintf("%s:7", name)),
				),
			},
		},
	})
}
