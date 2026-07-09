// GATE-F4: ray_version plan-stability + BYOD value-population.
//
// ray_version is Optional+Computed with UseStateForUnknown()+RequiresReplace(). Its fill
// logic (Create() and Read() in resource_container_image_registry.go) is guarded on
// plan.RayVersion.IsUnknown() / state.RayVersion.IsNull() respectively, and always prefers
// BuildResult.ResolvedRayVersion() (byod_ray_version if present, else the plain ray_version
// field) over the request-only defaultBYODRayVersion fallback.
//
// The real backend contract these tests are built against (verified directly against
// ~/projects/anyscale/product, read-only reference, not this repo):
//   - POST /api/v2/builds/byod (Create call 2) has response_model=Response[Build] - the
//     bare model, not DecoratedBuild. byod_ray_version only EXISTS as a field on
//     DecoratedBuild (builds.py), so the create response can never carry it, no matter what
//     the request sent. This is why Create()'s fill can only ever land on a concrete value
//     via a *subsequent* Read(), never at Create() itself, for a BYOD registry - proven by
//     Test A below.
//   - GET /api/v2/builds/{id} (Read) hits builds_resolver.resolve_build(), which sets
//     byod_ray_version = get_ray_version(base_image) only when
//     is_byod AND config_json AND base_image are all present, else leaves it None
//     (builds_resolver.py:65-71).
//   - get_ray_version(base_image) = base_image.split(":")[-1].split("-")[0]
//     (cluster_config_service.py:46-59) - a plain tag substring, with NO version parsing,
//     NO normalization, and no Ray-2.7.x special case. A non-version tag like `:latest` or
//     `:v3-prod` resolves to a stable, odd-but-real, non-null string ("latest", "v3") - this
//     is normal, expected behavior for that function, not an error condition.
package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// newRegistryF4MockServer mirrors newRegistryF3MockServer's shape but gives independent
// control over what the bare create response vs. the decorated Read response report for
// ray_version, since that split is exactly what F4's fill guards are built around. Per the
// real API contract above, createRayVersion is realistically always "" (the create response
// model has no byod_ray_version field, and the plain ray_version field is not populated for
// BYOD builds either - see the CreateBYODBuild finding cited in resource_container_image_
// registry.go's Create() comment); it stays parameterized here rather than hardcoded so a
// future regression that starts *reading* the create response's ray_version some other way
// still has a test able to catch it.
func newRegistryF4MockServer(t *testing.T, templateID, buildID, name, imageURI, createRayVersion, readByodRayVersion, readPlainRayVersion, digest string) *httptest.Server {
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

	// Bare create response: response_model=Response[Build] on the real API, never
	// DecoratedBuild, so byod_ray_version can never appear here regardless of request
	// content. createRayVersion models the plain ray_version field only.
	mux.HandleFunc("/api/v2/builds/byod", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on builds/byod", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		// Real Build model: no ray_version key at all when there's nothing to report - not
		// "ray_version": "". A non-nil pointer-to-empty-string is a different Go value than
		// nil and would (correctly) be treated by ResolvedRayVersion() as "a value is
		// present", so sending "" here instead of omitting the key would silently test the
		// wrong fixture shape.
		rayVersionField := ""
		if createRayVersion != "" {
			rayVersionField = fmt.Sprintf(`, "ray_version": %q`, createRayVersion)
		}
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"docker_image_name": %[3]q,
			"revision": %[4]d, "creator_id": "user_mock", "status": %[5]q,
			"created_at": %[6]q, "last_modified_at": %[6]q, "is_byod": true,
			"digest": %[7]q%[8]s
		}}`, buildID, templateID, imageURI, revision, buildStatus, createdAt, digest, rayVersionField)
	})

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

	// Decorated GET response: this is builds_resolver.resolve_build()'s output on the real
	// API - the only response that can ever carry byod_ray_version. readByodRayVersion is
	// omitted from the JSON entirely (not sent as "") when empty, matching the real
	// contract's None-default rather than manufacturing a shape the backend cannot send.
	//
	// readPlainRayVersion models the base Build.ray_version field, which DecoratedBuild
	// inherits unchanged: it is whatever was sent/stored at create time (in practice,
	// defaultBYODRayVersion's "2.44.0" fallback for every test here that leaves ray_version
	// unset), and it is realistic for it to be simultaneously present alongside a disagreeing
	// byod_ray_version - the two fields are populated by unrelated code paths. Without a
	// fixture that sends both at once, a regression that swapped ResolvedRayVersion()'s
	// preference order would be unobservable: if only one of the two fields is ever non-nil,
	// checking them in either order yields the same result.
	mux.HandleFunc("/api/v2/builds/"+buildID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on builds/%s", r.Method, buildID)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		byodField := ""
		if readByodRayVersion != "" {
			byodField = fmt.Sprintf(`, "byod_ray_version": %q`, readByodRayVersion)
		}
		plainField := ""
		if readPlainRayVersion != "" {
			plainField = fmt.Sprintf(`, "ray_version": %q`, readPlainRayVersion)
		}
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"docker_image_name": %[3]q,
			"revision": %[4]d, "creator_id": "user_mock", "status": %[5]q,
			"created_at": %[6]q, "last_modified_at": %[6]q, "is_byod": true,
			"digest": %[7]q%[8]s%[9]s
		}}`, buildID, templateID, imageURI, revision, buildStatus, createdAt, digest, byodField, plainField)
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

// TestAccContainerImageRegistryResource_RayVersionUnset_PopulatesOnRefresh_MockServer covers
// GATE-F4 cases 1+2 in a single lifecycle, plus the Create-time half of case 5's "no value
// yet" symmetry:
//   - Create-time: the create response never carries a resolved ray_version (real contract,
//     see file header), so Create()'s IsUnknown() fill guard must land on explicit null, not
//     an error and not a still-Unknown value (Core would reject the latter for a Computed
//     attribute).
//   - Case 1: the first refresh after create transitions state from null to a resolved
//     value, but produces an EMPTY plan - proven by asserting the plan is empty on the very
//     step whose apply is what triggers that null-to-value transition, not a later step.
//   - Case 2: step two confirms the value that lands is the real ResolvedRayVersion() result
//     (deliberately "9.9.9-mock-resolved", nowhere near the old defaultBYODRayVersion
//     "2.44.0" fallback) - so this cannot pass by coincidentally matching the fallback. The
//     fixture also sends "2.44.0" as the plain ray_version field alongside it (realistic:
//     that is what Create() actually sent when the config left ray_version unset), so this
//     also proves ResolvedRayVersion() genuinely prefers byod_ray_version over ray_version
//     rather than merely falling through to it because the other field happened to be nil.
func TestAccContainerImageRegistryResource_RayVersionUnset_PopulatesOnRefresh_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_f4_unset_mock"
	const buildID = "bld_f4_unset_mock"
	const name = "tfacc-f4-unset-mock"
	const imageURI = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-f4-unset:v1"
	const resolvedRayVersion = "9.9.9-mock-resolved"
	const staleDefaultDecoy = "2.44.0" // what Create() actually sent server-side when config left ray_version unset; must lose to byod_ray_version
	const digest = "sha256:f4unsetmock000000000000000000000000000000000000000000000000000"

	server := newRegistryF4MockServer(t, templateID, buildID, name, imageURI, "", resolvedRayVersion, staleDefaultDecoy, digest)
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name      = %[1]q
  image_uri = %[2]q
}
`, name, imageURI)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "id", templateID),
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "image_uri", imageURI),
					// Money assertion #1: Check evaluates against state as of right after
					// apply, before the step's own post-apply-refresh cycle runs - this is
					// Create()'s output. The bare create response never carries a resolved
					// value (real contract, see file header), so Create()'s IsUnknown() fill
					// guard must land on explicit null here, not "", not an error, and not a
					// still-Unknown value (Core would reject the latter for a Computed
					// attribute).
					resource.TestCheckNoResourceAttr("anyscale_container_image_registry.test", "ray_version"),
				),
				// Money assertion #2 (case 1): the post-apply-refresh cycle is what actually
				// calls Read() for the first time and transitions ray_version null ->
				// resolvedRayVersion. Asserting the resulting plan is empty proves that
				// transition is not itself treated as drift requiring a resource action.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				// Unchanged config: by now state already carries the value the prior step's
				// post-apply refresh resolved, and this step's own refresh reconfirms it
				// (case 2) plus stays stable beyond just the first refresh (case 1 again).
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "ray_version", resolvedRayVersion),
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

// TestAccContainerImageRegistryResource_RayVersionSet_PreservedDespiteBackendMismatch_MockServer
// covers GATE-F4 case 3: once the user sets ray_version explicitly, it must survive verbatim
// even when the backend's own resolved value disagrees. byod_ray_version is parsed purely
// from the user's own image tag (see file header) and is never derived from the ray_version
// the user typed - so a real backend response for an explicit ray_version can legitimately
// report a completely unrelated byod_ray_version. If the fill guard were missing its
// IsNull()/IsUnknown() check, this backend value would leak into state and either fight the
// user's config every plan or trigger a spurious RequiresReplace.
func TestAccContainerImageRegistryResource_RayVersionSet_PreservedDespiteBackendMismatch_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_f4_set_mock"
	const buildID = "bld_f4_set_mock"
	const name = "tfacc-f4-set-mock"
	const imageURI = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-f4-set:v3-prod"
	const userRayVersion = "2.9.0"
	const backendUnrelatedValue = "v3-prod" // what get_ray_version(imageURI) would really parse - unrelated to userRayVersion
	const digest = "sha256:f4setmock0000000000000000000000000000000000000000000000000000"

	// readPlainRayVersion realistically echoes back userRayVersion here (Create() sent it
	// verbatim since the user set it explicitly - see the plan.RayVersion.IsNull()/
	// IsUnknown() guard on the call-1 request in Create()), but it is inert for this test
	// either way: state.RayVersion is never null once the user sets it, so Read()'s fill
	// guard - and therefore ResolvedRayVersion() itself - never even runs in this flow.
	server := newRegistryF4MockServer(t, templateID, buildID, name, imageURI, "", backendUnrelatedValue, userRayVersion, digest)
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name        = %[1]q
  image_uri   = %[2]q
  ray_version = %[3]q
}
`, name, imageURI, userRayVersion)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "ray_version", userRayVersion),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				// A second, unchanged-config refresh: state.RayVersion is already non-null
				// (the user's value), so Read()'s fill guard must be a no-op here - re-check
				// the value is still exactly the user's, not the backend's unrelated string,
				// and the plan is still empty (no drift, no spurious replace).
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "ray_version", userRayVersion),
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

// TestAccContainerImageRegistryResource_RayVersionUnset_NonVersionTagResolvesStably_MockServer
// covers GATE-F4 case 4: an image tagged with something that is not a version number (e.g.
// `:latest`) still resolves through get_ray_version's plain substring split to a real,
// stable, non-null value ("latest") - this must be stored as-is, not treated as an error and
// not coerced to null, and must not keep re-planning once resolved.
func TestAccContainerImageRegistryResource_RayVersionUnset_NonVersionTagResolvesStably_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const templateID = "apptemp_f4_nonversion_mock"
	const buildID = "bld_f4_nonversion_mock"
	const name = "tfacc-f4-nonversion-mock"
	const imageURI = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-f4-nonversion:latest"
	const nonVersionResolved = "latest" // get_ray_version("...:latest") == "latest": split(":")[-1]="latest", split("-")[0]="latest"
	const staleDefaultDecoy = "2.44.0"  // what Create() actually sent server-side when config left ray_version unset; must lose to byod_ray_version, same as the sibling Unset test

	server := newRegistryF4MockServer(t, templateID, buildID, name, imageURI, "", nonVersionResolved, staleDefaultDecoy, "sha256:f4nonversionmock00000000000000000000000000000000000000000000")
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name      = %[1]q
  image_uri = %[2]q
}
`, name, imageURI)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					// Same Create-time-null proof as the Unset/PopulatesOnRefresh test above:
					// state right after apply, before this step's own post-apply refresh runs.
					resource.TestCheckNoResourceAttr("anyscale_container_image_registry.test", "ray_version"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					// Money assertion: an odd, non-version-looking string is accepted and
					// stored verbatim - proves there is no hidden "looks like a version"
					// validation or null-coercion on the resolved value.
					resource.TestCheckResourceAttr("anyscale_container_image_registry.test", "ray_version", nonVersionResolved),
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
