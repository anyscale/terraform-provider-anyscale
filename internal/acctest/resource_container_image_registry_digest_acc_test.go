// GATE-F5 (registry side): Computed build-mirror attributes must be pinned across an
// ordinary refresh (UseStateForUnknown) but must still ADVANCE, non-destructively, when the
// backend's latest build genuinely changes out from under Terraform.
//
// build_id, revision, name_version, digest, and build_status (resource_container_image_
// registry.go's containerImageRegistryAttributes()) all carry ONLY
// stringplanmodifier.UseStateForUnknown() / int64planmodifier.UseStateForUnknown() - none of
// them carry RequiresReplace. That is a deliberate choice, not an oversight: this resource's
// identity is the cluster environment (id == cluster_environment_id, see F3), and Read()
// unconditionally re-fetches "the current latest build for that cluster environment" on every
// refresh (GET application_templates/{id} for the latest_build stub, then GET builds/{id} for
// the decorated detail). Someone registering a new build against the same underlying template
// outside Terraform - e.g. via the Anyscale CLI or console - is expected, and must be absorbed
// as ordinary, non-destructive drift on these Computed fields: never a Replace/Destroy. This is
// a different, LOWER-severity class of change than F3: F3 was the resource's own TERRAFORM
// IDENTITY (id) drifting, which breaks import/state-addressing outright; this is a Computed
// attribute mirroring upstream state, which RequiresReplace's absence here explicitly declares
// safe to update in place.
//
// A mechanical note on what "surfaces as Update" actually means here, verified empirically
// against this exact resource (not assumed): for a purely Computed, non-Optional attribute
// whose only plan modifier is UseStateForUnknown(), Terraform Core's ordinary implicit-refresh
// plan (a plain `terraform plan`, no `-refresh=false`) folds the new backend value into prior
// state BEFORE the plan-vs-config diff step ever runs (Core's node_resource_plan_instance.go
// reassigns the same refreshed-state variable that the diff step then reads; see
// objchange.proposedNewAttributes, which sets newV = priorV for non-Optional attributes). Since
// none of these five attributes are ever mentioned in HCL config, that diff step has nothing to
// compare against config for them either way, so the resulting single-invocation plan action is
// NoOp, not Update - there is no code path where the pre-refresh and post-refresh values are
// held simultaneously for a config-vs-state diff to report as a change. This is not specific to
// UseStateForUnknown: the same refresh-then-diff ordering means RequiresReplace could not
// observe this transition either, for the same underlying reason (nothing survives to compare
// the old value against once refresh has already overwritten it) - so "RequiresReplace would
// show up as Replace here" is not a safe assumption either, and is deliberately not the contrast
// drawn below. What Terraform Core DOES still guarantee, and what is actually load-bearing to
// prove: the refreshed value replacing the old one is absorbed as an ordinary, error-free NoOp
// (or ordinary Update, if a real per-attribute diff exists to report on that particular plan),
// with the resource's plan action NEVER Replace/Destroy/CreateBeforeDestroy/DestroyBeforeCreate
// - which is exactly the safety property RequiresReplace's absence on these fields is meant to
// guarantee, and exactly what would break if any of the five picked up RequiresReplace later.
//
// Two tests, one proving each half:
//   - DigestStableAcrossRefresh: nothing changes backend-side between two refreshes -> the
//     second refresh's plan must be truly empty (plancheck.ExpectEmptyPlan()), matching F3's
//     and F4's existing "no refresh-induced noise" bar for this resource.
//   - LatestBuildAdvance_UpdatesNoReplace: the mock's "latest build" advances out-of-band
//     between two refreshes (new build_id/revision/name_version/digest/build_status, same
//     cluster environment). The money assertion is plancheck.ExpectResourceAction(addr,
//     plancheck.ResourceActionNoop) on that transition's own plan - per the mechanical note
//     above, NoOp (not Update) is the correct, expected action for this exact case today, and
//     is proven here specifically so a future change to how these attributes are populated
//     cannot silently regress into Replace/Destroy without this test catching it. The test then
//     separately proves state genuinely advanced to build-B's values (via Check) and that a
//     further, independent refresh against the now-current build-B stays stable
//     (ExpectEmptyPlan) - i.e. the "latest build" transition is real, absorbed correctly, and
//     never destructive, even though it is invisible as a distinct "Update" step in the single
//     plan that performs it.
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
// cluster environment out-of-band (i.e. not through this Terraform resource). This is the one
// behavior newRegistryF3MockServer/newRegistryF4MockServer do not need and do not have: their
// GET handlers are closed over fixed values for the lifetime of the httptest.Server. Mutable
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
// out-of-band between two refreshes of the same cluster environment.
func (s *digestMockRegistryServer) advanceLatestBuild(next registryBuildSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = next
}

// newDigestMockRegistryServer wires up the same endpoint shape as newRegistryF3MockServer /
// newRegistryF4MockServer (create template, create build, GET template, GET build, archive),
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
	mux.HandleFunc("/api/v2/builds/", func(w http.ResponseWriter, r *http.Request) {
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
	s.serverURL = server.URL
	return s
}

// TestAccContainerImageRegistryResource_DigestStableAcrossRefresh_MockServer proves the
// "nothing changed" half: build_id, revision, name_version, digest, and build_status must
// all be pinned by UseStateForUnknown() across a refresh where the backend's latest build has
// not moved - producing a truly EMPTY plan, not just an unchanged-but-still-planned attribute
// set. This is the same bar F3's and F4's lifecycle tests already hold this resource to;
// this test isolates it specifically for the five build-mirror attributes together.
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
// come back as plancheck.ResourceActionNoop - per the file header's mechanical note, that is the
// correct, verified action for this exact case (ordinary implicit-refresh plan, unchanged
// config, Computed-only attributes), not a weaker stand-in for Update. What actually matters,
// and what this test is built to catch a regression in, is that the action is NEVER Replace,
// Destroy, CreateBeforeDestroy, or DestroyBeforeCreate - which would only become reachable if
// one of these five attributes picked up RequiresReplace (or something else config-influencing)
// later. The test separately proves state genuinely advanced to build-B's values (Check) and
// that a further, independent refresh against the now-current build-B stays stable
// (ExpectEmptyPlan), so the transition is real and non-destructive even though it does not
// surface as its own distinct "Update" step.
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
				// (registered against the same cluster environment out-of-band, e.g. via the
				// Anyscale CLI/console rather than through this Terraform resource). This
				// PreConfig is what simulates that: it flips the mock's snapshot immediately
				// before Terraform plans this step, so the plan/apply below is exactly what a
				// user would see running `terraform plan` after such an out-of-band build.
				PreConfig: func() {
					mock.advanceLatestBuild(buildB)
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					// Money assertion: the plan action for THIS resource's own transitioning
					// plan is NoOp - the mechanically correct, verified action here (see file
					// header), not Replace/Destroy/CreateBeforeDestroy/DestroyBeforeCreate. A
					// regression that gave any of these five attributes RequiresReplace (or
					// otherwise made them config-influencing) would flip this to something in
					// that destructive set, which is exactly what this assertion guards against.
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
					resource.TestCheckResourceAttr(resourceAddress, "id", templateID), // identity (F3) unaffected - only the build-mirror attrs move
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
