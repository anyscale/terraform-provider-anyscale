package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const (
	imageTestRegistryURI = "registry.example.com/team/app:1"
	imageTestBaseURI     = "anyscale/ray:2.56.1-slim-py312-cu129"
)

// imageConfig renders the service with an image source (image_uri or build_id lines) in place of
// outcomeConfig's fixed build_id.
func imageConfig(serverURL, name, importPath, imageLines string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_service" "test" {
  name              = %[1]q
  project_id        = "prj_outcome"
  compute_config_id = "cpt_outcome"
%[3]s

  ray_serve_config = {
    applications = [
      {
        import_path = %[2]q
      }
    ]
  }
}
`, name, importPath, imageLines)
}

// imageOnApply creates and rolls out versions that run the build each apply body names.
func imageOnApply() func(int, map[string]any) outcomeApplyResult {
	var current outcomeVersion
	return func(n int, body map[string]any) outcomeApplyResult {
		build, _ := body["build_id"].(string)
		v := outcomeVersion{ID: fmt.Sprintf("svcver_v%d", n), Name: fmt.Sprintf("v%d", n), ImportPath: "main:app", BuildID: build}
		prev := current
		current = v
		if n == 1 {
			return outcomeCreateOf(v)
		}
		return outcomeRollout(prev, v)
	}
}

func expectGetOrCreateCount(m *outcomeMock, want int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := m.getOrCreateCount(); got != want {
			return fmt.Errorf("get_or_create_build_from_image_uri called %d time(s), want %d", got, want)
		}
		return nil
	}
}

func expectApplyBuildID(m *outcomeMock, i int, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := m.applyBody(i)["build_id"]; got != want {
			return fmt.Errorf("apply %d sent build_id %v, want %s", i+1, got, want)
		}
		return nil
	}
}

// TestAccServiceResource_ImageURIResolvesBuild_MockServer: image_uri (with its registry secret) is
// resolved once at create, the resolved build is deployed and recorded, and the next plan is
// empty because the deployed build still is the image.
func TestAccServiceResource_ImageURIResolvesBuild_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-image"
	m, server := newOutcomeMock(t, "svc_parity_image", name, imageOnApply())
	cfg := imageConfig(server.URL, name, "main:app", fmt.Sprintf(`  image_uri             = %q
  registry_login_secret = "team-registry-secret"`, imageTestRegistryURI))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					expectGetOrCreateCount(m, 1),
					expectApplyBuildID(m, 0, "bld_resolved_1"),
					resource.TestCheckResourceAttr("anyscale_service.test", "build_id", "bld_resolved_1"),
					func(*terraform.State) error {
						body := m.getOrCreateLog[0]
						if body["image_uri"] != imageTestRegistryURI || body["registry_login_secret"] != "team-registry-secret" {
							return fmt.Errorf("get_or_create body %v", body)
						}
						if _, ok := body["cloud_id"]; ok {
							return fmt.Errorf("get_or_create sent cloud_id on a non-Azure control plane: %v", body)
						}
						return nil
					},
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccServiceResource_ImageURIUnchangedNoResolve_MockServer: a rollout of another field keeps
// the known build, with no new resolve.
func TestAccServiceResource_ImageURIUnchangedNoResolve_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-image-unchanged"
	m, server := newOutcomeMock(t, "svc_parity_image_unchanged", name, imageOnApply())
	image := fmt.Sprintf(`  image_uri = %q`, imageTestRegistryURI)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: imageConfig(server.URL, name, "main:app", image)},
			{
				Config: imageConfig(server.URL, name, "main:app_v2", image),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 2),
					expectGetOrCreateCount(m, 1),
					expectApplyBuildID(m, 1, "bld_resolved_1"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccServiceResource_ImageURIDriftDetected_MockServer: an out-of-band deploy that moves the
// service to another image plans an update; apply re-resolves image_uri and rolls it back out.
func TestAccServiceResource_ImageURIDriftDetected_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-image-drift"
	m, server := newOutcomeMock(t, "svc_parity_image_drift", name, imageOnApply())
	m.addBuild("bld_cli", outcomeBuild{DockerImage: "registry.example.com/team/app:2", TemplateName: "anon", Revision: 1})
	// Same ray_serve_config as configured, so the build is the only difference.
	cliVersion := outcomeVersion{ID: "svcver_cli", Name: "cli", ImportPath: "main:app", BuildID: "bld_cli"}
	cfg := imageConfig(server.URL, name, "main:app", fmt.Sprintf(`  image_uri = %q`, imageTestRegistryURI))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() { m.setScript(outcomeSnapshot{CurrentState: "RUNNING", Primary: cliVersion}) },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue("anyscale_service.test", tfjsonpath.New("build_id")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 2),
					expectGetOrCreateCount(m, 2),
					expectApplyBuildID(m, 1, "bld_resolved_1"),
					resource.TestCheckResourceAttr("anyscale_service.test", "build_id", "bld_resolved_1"),
				),
			},
		},
	})
}

// TestAccServiceResource_BuildIDToImageURINoRollout_MockServer (Test B): switching from build_id
// to the image_uri that build already is plans an update that rolls nothing out and resolves
// nothing, and ExactlyOneOf accepts it although state holds a build_id (Gate 2).
func TestAccServiceResource_BuildIDToImageURINoRollout_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-image-switch"
	m, server := newOutcomeMock(t, "svc_parity_image_switch", name, imageOnApply())
	m.addBuild("anyscaleray2561-slim-py312-cu129", outcomeBuild{DockerImage: imageTestBaseURI, TemplateName: imageTestBaseURI, Revision: 1})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: imageConfig(server.URL, name, "main:app", `  build_id = "anyscaleray2561-slim-py312-cu129"`)},
			{
				Config: imageConfig(server.URL, name, "main:app", fmt.Sprintf(`  image_uri = %q`, imageTestBaseURI)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue("anyscale_service.test", tfjsonpath.New("build_id"),
							knownvalue.StringExact("anyscaleray2561-slim-py312-cu129")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 1),
					expectGetOrCreateCount(m, 0),
				),
			},
		},
	})
}

// TestAccServiceResource_ImportImageURILeavesNull (Test A): import recovers build_id and leaves
// image_uri null; a config naming that build's image then updates state only.
func TestAccServiceResource_ImportImageURILeavesNull(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-image-import"
	m, server := newOutcomeMock(t, "svc_parity_image_import", name, func(int, map[string]any) outcomeApplyResult {
		t.Errorf("unexpected apply after import")
		return outcomeCreate()
	})
	m.addBuild("bld_live", outcomeBuild{DockerImage: imageTestRegistryURI, TemplateName: "anon", Revision: 1})
	m.setScript(outcomeSnapshot{CurrentState: "RUNNING",
		Primary: outcomeVersion{ID: "svcver_live", Name: "live", ImportPath: "main:app", BuildID: "bld_live"}})
	cfg := imageConfig(server.URL, name, "main:app", fmt.Sprintf(`  image_uri = %q`, imageTestRegistryURI))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_service.test",
				ImportState:        true,
				ImportStateId:      "svc_parity_image_import",
				ImportStatePersist: true,
				Config:             cfg,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					a := states[0].Attributes
					if a["build_id"] != "bld_live" {
						return fmt.Errorf("imported build_id = %q, want bld_live", a["build_id"])
					}
					if v, ok := a["image_uri"]; ok && v != "" {
						return fmt.Errorf("import seeded image_uri = %q, want null", v)
					}
					return nil
				},
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: expectGetOrCreateCount(m, 0),
			},
		},
	})
}

// TestAccServiceResource_InPlaceRejectsImageURIChange: a new image needs a new cluster, which
// IN_PLACE cannot do; the error names image_uri.
func TestAccServiceResource_InPlaceRejectsImageURIChange(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-image-inplace"
	_, server := newOutcomeMock(t, "svc_parity_image_inplace", name, imageOnApply())
	const inPlace = `  rollout_strategy = "IN_PLACE"
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: imageConfig(server.URL, name, "main:app", inPlace+fmt.Sprintf(`  image_uri = %q`, imageTestRegistryURI))},
			{
				Config:      imageConfig(server.URL, name, "main:app", inPlace+`  image_uri = "registry.example.com/team/app:2"`),
				ExpectError: regexp.MustCompile(`(?s)IN_PLACE.*image_uri \(resolves to a new build\)`),
			},
		},
	})
}

// TestAccServiceResource_ImageURIAndBuildIDConflict: exactly one of image_uri and build_id.
func TestAccServiceResource_ImageURIAndBuildIDConflict(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: imageConfig("http://127.0.0.1:1", "parity-image-conflict", "main:app",
					fmt.Sprintf("  image_uri = %q\n  build_id = \"bld_x\"", imageTestRegistryURI)),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Combination.*build_id.*image_uri`),
			},
			{
				Config:      imageConfig("http://127.0.0.1:1", "parity-image-conflict", "main:app", ""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Missing Attribute Configuration.*Exactly one of these attributes must be configured: \[build_id,image_uri\]`),
			},
		},
	})
}
