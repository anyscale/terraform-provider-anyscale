package acctest

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// versionNameOnApply names each version from the apply body's `version`, or randomly as the
// backend does ("v-" plus random characters), and answers a name the service already used with
// the backend's 409. Apply number rollBack (0 for none) is rolled back.
func versionNameOnApply(rollBack int) func(int, map[string]any) outcomeApplyResult {
	used := map[string]bool{}
	var current outcomeVersion
	return func(n int, body map[string]any) outcomeApplyResult {
		name, _ := body["version"].(string)
		if name == "" {
			name = fmt.Sprintf("v-%010x", rand.Uint64()&0xffffffffff)
		}
		if used[name] {
			return outcomeApplyResult{
				Status: http.StatusConflict,
				Detail: fmt.Sprintf("Service version '%s' already exists for service 'x'. Use a different "+
					"--version-name, or omit it to auto-generate one.", name),
			}
		}
		used[name] = true
		rsc, _ := body["ray_serve_config"].(map[string]any)
		apps, _ := rsc["applications"].([]any)
		importPath, _ := apps[0].(map[string]any)["import_path"].(string)
		v := outcomeVersion{ID: fmt.Sprintf("svcver_%d", n), Name: name, ImportPath: importPath}

		prev := current
		switch n {
		case 1:
			current = v
			return outcomeCreateOf(v)
		case rollBack:
			return outcomeApplyResult{
				Response: outcomeSnapshot{CurrentState: "ROLLING_OUT", Primary: prev, Canary: &v},
				Script: []outcomeSnapshot{
					{CurrentState: "ROLLING_BACK", Primary: prev},
					{CurrentState: "RUNNING", Primary: prev},
				},
			}
		default:
			current = v
			return outcomeRollout(prev, v)
		}
	}
}

func versionNameLine(name string) string {
	return fmt.Sprintf("  version_name = %q", name)
}

// TestAccServiceResource_VersionNameSentAndRefreshed_MockServer: a configured name is sent as
// `version`, recorded from primary_version.version, and changing it alone rolls out.
func TestAccServiceResource_VersionNameSentAndRefreshed_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-version"
	m, server := newOutcomeMock(t, "svc_parity_version", name, versionNameOnApply(0))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: outcomeConfig(server.URL, name, "main:app", versionNameLine("tfacc-v1")),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(*terraform.State) error {
						if got := m.applyBody(0)["version"]; got != "tfacc-v1" {
							return fmt.Errorf("create sent version %v, want tfacc-v1", got)
						}
						return nil
					},
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.version", "tfacc-v1"),
				),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app", versionNameLine("tfacc-v2")),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 2),
					resource.TestCheckResourceAttr("anyscale_service.test", "version_name", "tfacc-v2"),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.version", "tfacc-v2"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccServiceResource_VersionNameOmittedStable_MockServer: unset, the generated name is
// recorded; a deploy plans it unknown and records the new one; a pacing-only change plans the
// current name (not unknown) and sends nothing (Gate 2: ModifyPlan may restore the state value
// the framework marked unknown).
func TestAccServiceResource_VersionNameOmittedStable_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-version-generated"
	m, server := newOutcomeMock(t, "svc_parity_version_generated", name, versionNameOnApply(0))
	var first, second string
	pacingOnly := `  max_surge_percent = 10`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: outcomeConfig(server.URL, name, "main:app", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					CaptureResourceAttr("anyscale_service.test", "version_name", &first),
					resource.TestCheckResourceAttrPair("anyscale_service.test", "version_name",
						"anyscale_service.test", "primary_version.version"),
					resource.TestMatchResourceAttr("anyscale_service.test", "version_name", regexp.MustCompile(`^v-`)),
				),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app_v2", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectUnknownValue("anyscale_service.test", tfjsonpath.New("version_name")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 2),
					CaptureResourceAttr("anyscale_service.test", "version_name", &second),
					resource.TestCheckResourceAttrPair("anyscale_service.test", "version_name",
						"anyscale_service.test", "primary_version.version"),
					func(*terraform.State) error {
						if _, sent := m.applyBody(1)["version"]; sent {
							return fmt.Errorf("deploy sent a version name although none is configured")
						}
						if second == first {
							return fmt.Errorf("version_name stayed %q across a rollout", first)
						}
						return nil
					},
				),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app_v2", pacingOnly),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue("anyscale_service.test", tfjsonpath.New("version_name"),
							knownvalue.StringFunc(func(v string) error {
								if v != second {
									return fmt.Errorf("planned %q, want the current %q", v, second)
								}
								return nil
							})),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: expectOutcomeApplyCount(m, 2),
			},
		},
	})
}

// TestAccServiceResource_VersionNameUnchangedWithDeployRejected: a configured name that does not
// change while another field rolls out is rejected at plan time, before the backend's 409.
func TestAccServiceResource_VersionNameUnchangedWithDeployRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-version-unchanged"
	m, server := newOutcomeMock(t, "svc_parity_version_unchanged", name, versionNameOnApply(0))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", versionNameLine("tfacc-v1"))},
			{
				Config:      outcomeConfig(server.URL, name, "main:app_v2", versionNameLine("tfacc-v1")),
				ExpectError: regexp.MustCompile(`(?s)Version Name Must Change.*"tfacc-v1" is already the current version`),
			},
		},
	})
	if got := m.applyCount(); got != 1 {
		t.Errorf("apply called %d times, want 1", got)
	}
}

// TestAccServiceResource_VersionNameReuseTranslated_MockServer: after a rolled-back rollout the
// change plans again, and re-applying the failed version's name gets an attribute error naming
// the rule instead of the backend's CLI-worded 409.
func TestAccServiceResource_VersionNameReuseTranslated_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-version-reuse"
	m, server := newOutcomeMock(t, "svc_parity_version_reuse", name, versionNameOnApply(2))
	updated := outcomeConfig(server.URL, name, "main:app_v2", versionNameLine("tfacc-v2"))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", versionNameLine("tfacc-v1"))},
			{
				Config:      updated,
				ExpectError: regexp.MustCompile(`Service Rollout Rolled Back`),
			},
			{
				Config:             updated,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue("anyscale_service.test", tfjsonpath.New("version_name"),
							knownvalue.StringExact("tfacc-v2")),
					},
				},
			},
			{
				Config: updated,
				ExpectError: regexp.MustCompile(`(?s)Version Name Already Used.*"tfacc-v2" was already used by this service` +
					`.*Choose a new name`),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app_v2", versionNameLine("tfacc-v3")),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 4),
					resource.TestCheckResourceAttr("anyscale_service.test", "version_name", "tfacc-v3"),
				),
			},
		},
	})
}

// TestAccServiceResource_InPlaceRejectsVersionNameChange: IN_PLACE keeps the primary's name.
func TestAccServiceResource_InPlaceRejectsVersionNameChange(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-version-inplace"
	_, server := newOutcomeMock(t, "svc_parity_version_inplace", name, versionNameOnApply(0))
	const inPlace = `  rollout_strategy = "IN_PLACE"
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", inPlace+versionNameLine("tfacc-v1"))},
			{
				Config:      outcomeConfig(server.URL, name, "main:app_v2", inPlace+versionNameLine("tfacc-v2")),
				ExpectError: regexp.MustCompile(`(?s)IN_PLACE.*version_name`),
			},
		},
	})
}

// TestAccServiceResource_VersionNameRestartNeedsNewName: restarting a terminated service creates
// a version, so an unchanged configured name is rejected at plan time.
func TestAccServiceResource_VersionNameRestartNeedsNewName(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-version-restart"
	m, server := newOutcomeMock(t, "svc_parity_version_restart", name, versionNameOnApply(0))
	cfg := outcomeConfig(server.URL, name, "main:app", versionNameLine("tfacc-v1"))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					m.setScript(outcomeSnapshot{CurrentState: "TERMINATED", GoalState: "TERMINATED",
						Primary: outcomeVersion{ID: "svcver_1", Name: "tfacc-v1", ImportPath: "main:app"}})
				},
				Config:      cfg,
				ExpectError: regexp.MustCompile(`(?s)Version Name Must Change`),
			},
		},
	})
}

// TestAccServiceResource_ImportRecoversVersionName (Test A): import records the primary version's
// name, so a config that names it plans no rollout.
func TestAccServiceResource_ImportRecoversVersionName(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-version-import"
	m, server := newOutcomeMock(t, "svc_parity_version_import", name, func(int, map[string]any) outcomeApplyResult {
		t.Errorf("unexpected apply after import")
		return outcomeCreate()
	})
	m.setScript(outcomeSnapshot{CurrentState: "RUNNING", Primary: outcomeV1})
	cfg := outcomeConfig(server.URL, name, "main:app", versionNameLine("v1"))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_service.test",
				ImportState:        true,
				ImportStateId:      "svc_parity_version_import",
				ImportStatePersist: true,
				Config:             cfg,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if got := states[0].Attributes["version_name"]; got != "v1" {
						return fmt.Errorf("imported version_name = %q, want v1", got)
					}
					return nil
				},
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}
