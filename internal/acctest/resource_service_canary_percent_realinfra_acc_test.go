package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// End-to-end tests of anyscale_service canary_percent (docs/decisions/service-canary-percent)
// against the real backend. Each test creates one compute config, one ephemeral project and one
// service on the pinned fixture cloud (pinnedWorkloadCloud: any platform), and destroys them all.
//
//   - TestAccServiceResource_RealInfra_CanaryPercentHoldShiftComplete (decisions 4, 5, 12): a deploy
//     held at 0, shifted to 25 (same canary, no new version), then completed by removing
//     canary_percent. Every step plans empty afterwards.
//   - TestAccServiceResource_RealInfra_CanaryPercentFailedHoldRollsBack (decision 14): a deploy
//     that cannot come up, held at 0, is rolled back by the provider and fails with "Service
//     Rollout Rolled Back"; state keeps the prior deploy fields and the next plan proposes the
//     change again.
//   - TestAccServiceResource_RealInfra_CanaryPercentCutover (decision 3): a deploy at 100 either
//     completes, or (where Anyscale does not complete manual rollouts) fails with "Canary Rollout
//     Not Completed" keeping 100 in state; removing canary_percent then completes it. Both
//     outcomes pass; the test logs which one happened.
//
// Run one (each ~20-40 min):
//
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 go test ./internal/acctest/ \
//	  -run 'TestAccServiceResource_RealInfra_CanaryPercentHoldShiftComplete' -v -timeout 90m
//
// Run all three (sequential, allow ~2h):
//
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 go test ./internal/acctest/ \
//	  -run 'TestAccServiceResource_RealInfra_CanaryPercent(HoldShiftComplete|FailedHoldRollsBack|Cutover)$' \
//	  -v -timeout 180m
//
// For request-level evidence (for example that the shift to 25 sends one PUT /apply and starts no
// new version), add TF_LOG=DEBUG TF_ACC_LOG_PATH=/tmp/svc-canary-e2e.log: the provider runs
// in-process, so plugin-testing writes its logs to TF_ACC_LOG_PATH rather than TF_LOG_PATH.

// canaryRealInfraFixture holds the names and ids one canary_percent real-infra test deploys with.
type canaryRealInfraFixture struct {
	computeConfigName string
	serviceName       string
	cloudID           string
	instanceType      string
	projectID         string
}

func newCanaryRealInfraFixture(t *testing.T, slug string) canaryRealInfraFixture {
	t.Helper()
	cloud, headInstanceType := pinnedWorkloadCloud(t)
	// Scoped to the cloud: see TestAccServiceResource_RealInfra for why not the default project.
	projectID, _, err := CreateEphemeralTestProjectForCloud(t, cloud.ID)
	if err != nil {
		t.Fatalf("failed to create ephemeral test project scoped to cloud %s: %v", cloud.ID, err)
	}
	return canaryRealInfraFixture{
		computeConfigName: UniqueName(t, "cc-"+slug),
		serviceName:       UniqueName(t, slug),
		cloudID:           cloud.ID,
		instanceType:      headInstanceType,
		projectID:         projectID,
	}
}

// config renders the service with the given import_path and env var marker (a marker change
// changes the config hash, so it deploys a new version). canaryPercent is the literal value, or
// "" to omit canary_percent.
func (f canaryRealInfraFixture) config(importPath, marker, canaryPercent string) string {
	canaryLine := ""
	if canaryPercent != "" {
		canaryLine = "  canary_percent    = " + canaryPercent
	}
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q
  head_node = {
    instance_type = %[3]q
  }
}

resource "anyscale_service" "test" {
  name              = %[4]q
  project_id        = %[5]q
  build_id          = "anyscaleray2561-slim-py312-cu129"
  compute_config_id = anyscale_compute_config.test.config_id
%[8]s
  timeouts {
    create = "60m"
    update = "60m"
  }

  ray_serve_config = {
    applications = [
      {
        import_path = %[6]q
        runtime_env = {
          working_dir = "https://github.com/anyscale/first-service/archive/refs/heads/main.zip"
          env_vars = {
            TFACC_CANARY_MARKER = %[7]q
          }
        }
      }
    ]
  }
}
`, f.computeConfigName, f.cloudID, f.instanceType, f.serviceName, f.projectID, importPath, marker, canaryLine)
}

const canaryServiceAddr = "anyscale_service.test"

// TestAccServiceResource_RealInfra_CanaryPercentHoldShiftComplete: hold at 0 (E1), shift to 25
// (E2), complete by removing canary_percent (E3).
func TestAccServiceResource_RealInfra_CanaryPercentHoldShiftComplete(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	f := newCanaryRealInfraFixture(t, "svc-canary-flow")
	var serviceID, baselinePrimaryID, canaryID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_service", "/api/v2/services-v2/%s"),
		Steps: []resource.TestStep{
			{
				// Baseline: canary_percent null, so create rolls out normally.
				Config: f.config("main:app", "v1", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(canaryServiceAddr, "current_state", "RUNNING"),
					resource.TestCheckNoResourceAttr(canaryServiceAddr, "canary_percent"),
					resource.TestCheckNoResourceAttr(canaryServiceAddr, "canary_version.id"),
					CaptureResourceAttr(canaryServiceAddr, "id", &serviceID),
					CaptureResourceAttr(canaryServiceAddr, "primary_version.id", &baselinePrimaryID),
				),
			},
			{
				// E1 (decision 4): a deploy change held at 0. The old version stays primary, the
				// new one is the canary serving nothing.
				Config: f.config("main:app", "v2", "0"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(canaryServiceAddr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(canaryServiceAddr, "canary_percent", "0"),
					resource.TestCheckResourceAttrPtr(canaryServiceAddr, "id", &serviceID),
					resource.TestCheckResourceAttrPtr(canaryServiceAddr, "primary_version.id", &baselinePrimaryID),
					resource.TestCheckResourceAttr(canaryServiceAddr, "canary_version.current_state", "RUNNING"),
					resource.TestCheckResourceAttr(canaryServiceAddr, "canary_version.current_weight", "0"),
					resource.TestCheckResourceAttr(canaryServiceAddr, "canary_version.target_weight", "0"),
					resource.TestCheckResourceAttr(canaryServiceAddr, "canary_version.weight", "0"),
					CaptureResourceAttr(canaryServiceAddr, "canary_version.id", &canaryID),
					func(*terraform.State) error {
						if canaryID == "" || canaryID == baselinePrimaryID {
							return fmt.Errorf("canary_version.id = %q, want a new version beside primary %s", canaryID, baselinePrimaryID)
						}
						t.Logf("E1: primary %s, canary %s held at 0", baselinePrimaryID, canaryID)
						return nil
					},
				),
			},
			{
				// E2 (decision 5): only canary_percent changes. The same canary moves to 25; no new
				// version starts.
				PreConfig: func() {
					t.Log("E2: confirm in the debug log that this step sends exactly one PUT /api/v2/services-v2/apply " +
						"(\"Shifting the held canary's traffic share\") and no \"Applying service update\"")
				},
				Config: f.config("main:app", "v2", "25"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(canaryServiceAddr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(canaryServiceAddr, "canary_percent", "25"),
					resource.TestCheckResourceAttrPtr(canaryServiceAddr, "primary_version.id", &baselinePrimaryID),
					resource.TestCheckResourceAttrPtr(canaryServiceAddr, "canary_version.id", &canaryID),
					resource.TestCheckResourceAttr(canaryServiceAddr, "canary_version.current_state", "RUNNING"),
					resource.TestCheckResourceAttr(canaryServiceAddr, "canary_version.current_weight", "25"),
					resource.TestCheckResourceAttr(canaryServiceAddr, "canary_version.target_weight", "25"),
					resource.TestCheckResourceAttr(canaryServiceAddr, "canary_version.weight", "25"),
				),
			},
			{
				// E3 (decision 2): canary_percent removed. The rollout completes and the held canary is
				// promoted under the same id.
				Config: f.config("main:app", "v2", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(canaryServiceAddr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(canaryServiceAddr, "current_state", "RUNNING"),
					resource.TestCheckNoResourceAttr(canaryServiceAddr, "canary_percent"),
					resource.TestCheckNoResourceAttr(canaryServiceAddr, "canary_version.id"),
					resource.TestCheckResourceAttrPtr(canaryServiceAddr, "primary_version.id", &canaryID),
				),
			},
		},
	})
}

// TestAccServiceResource_RealInfra_CanaryPercentFailedHoldRollsBack (E4, decision 14): Anyscale
// never rolls back a held canary, so the apply holding a canary that fails rolls it back itself.
// The rolled-back step follows TestAccServiceResource_RealInfra_RolledBackRollout: ExpectError,
// then a PlanOnly step with the same broken config that must plan an update again.
func TestAccServiceResource_RealInfra_CanaryPercentFailedHoldRollsBack(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	f := newCanaryRealInfraFixture(t, "svc-canary-fail")
	// The working_dir has no tfacc_missing_app, so the new version never becomes healthy.
	broken := f.config("main:tfacc_missing_app", "v2", "0")
	var serviceID, baselinePrimaryID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_service", "/api/v2/services-v2/%s"),
		Steps: []resource.TestStep{
			{
				Config: f.config("main:app", "v1", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(canaryServiceAddr, "current_state", "RUNNING"),
					CaptureResourceAttr(canaryServiceAddr, "id", &serviceID),
					CaptureResourceAttr(canaryServiceAddr, "primary_version.id", &baselinePrimaryID),
				),
			},
			{
				Config:      broken,
				ExpectError: regexp.MustCompile(`Service Rollout Rolled Back`),
			},
			{
				// The old primary still serves everything, with no canary left behind.
				PreConfig: func() {
					svc := getServiceForCanaryTest(t, serviceID)
					if svc.CanaryVersion != nil {
						t.Fatalf("after the rollback, canary %s (%s) is still present", svc.CanaryVersion.ID, svc.CanaryVersion.CurrentState)
					}
					if svc.PrimaryVersion == nil || svc.PrimaryVersion.ID != baselinePrimaryID {
						t.Fatalf("after the rollback, primary is %v, want %s", svc.PrimaryVersion, baselinePrimaryID)
					}
					t.Logf("E4: rolled back; primary %s, current_state %s", baselinePrimaryID, svc.CurrentState)
				},
				// Same broken config, plan only: re-applying would re-run the failing deploy.
				Config:             broken,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(canaryServiceAddr, plancheck.ResourceActionUpdate),
						// State (the plan's prior, after refresh) kept the deploy fields that are live.
						canaryPlanChangeCheck{addr: canaryServiceAddr, check: func(before, _ map[string]any) error {
							if got := planNestedString(before, "primary_version", "id"); got != baselinePrimaryID {
								return fmt.Errorf("state primary_version.id = %q, want %s", got, baselinePrimaryID)
							}
							if before["canary_version"] != nil {
								return fmt.Errorf("state canary_version = %v, want null", before["canary_version"])
							}
							if before["canary_percent"] != nil {
								return fmt.Errorf("state canary_percent = %v, want the prior null", before["canary_percent"])
							}
							rsc, err := json.Marshal(before["ray_serve_config"])
							if err != nil {
								return err
							}
							if s := string(rsc); !strings.Contains(s, `"main:app"`) || !strings.Contains(s, `"v1"`) {
								return fmt.Errorf("state ray_serve_config = %s, want the prior main:app / v1 config", s)
							}
							return nil
						}},
					},
				},
			},
		},
	})
}

// TestAccServiceResource_RealInfra_CanaryPercentCutover (E5, decision 3): canary_percent = 100 is
// a full cutover. Whether Anyscale then completes the rollout depends on a per-cloud backend flag,
// so the cutover step has two valid outcomes, and the test passes for either and fails for any
// other:
//
//   - completed: the apply succeeds with canary_version null, a new primary and 100 in state; the
//     post-apply plan is empty. Removing canary_percent is then state-only (decision 6) and must
//     not start another version.
//   - held: the apply fails with "Canary Rollout Not Completed" after the grace period, keeping
//     100 in state, with the canary serving 100% beside the old primary. Removing canary_percent
//     must then complete it, promoting that canary.
//
// An optional error cannot be expressed with ExpectError, which fails when no error occurs. So
// the cutover step sets no ExpectError and the TestCase's ErrorCheck accepts exactly the "held"
// diagnostic, only while the cutover step runs (cutoverStep). Any other error, including a
// non-empty post-apply plan or a failed Check, still fails the step. The step's Check runs only
// when the apply succeeded, so it records the "completed" outcome; ErrorCheck records "held".
func TestAccServiceResource_RealInfra_CanaryPercentCutover(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	f := newCanaryRealInfraFixture(t, "svc-canary-cut")
	var serviceID, baselinePrimaryID, wantPrimaryID string
	var cutoverStep bool
	outcome := ""
	notCompleted := regexp.MustCompile(`Canary Rollout Not Completed`)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_service", "/api/v2/services-v2/%s"),
		ErrorCheck: func(err error) error {
			if cutoverStep && notCompleted.MatchString(err.Error()) {
				outcome = "held"
				t.Logf("E5 outcome: held at 100 (Anyscale did not complete the manual rollout): %v", err)
				return nil
			}
			return err
		},
		Steps: []resource.TestStep{
			{
				Config: f.config("main:app", "v1", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(canaryServiceAddr, "current_state", "RUNNING"),
					CaptureResourceAttr(canaryServiceAddr, "id", &serviceID),
					CaptureResourceAttr(canaryServiceAddr, "primary_version.id", &baselinePrimaryID),
				),
			},
			{
				PreConfig: func() { cutoverStep = true },
				Config:    f.config("main:app", "v2", "100"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(canaryServiceAddr, "current_state", "RUNNING"),
					resource.TestCheckResourceAttr(canaryServiceAddr, "canary_percent", "100"),
					resource.TestCheckNoResourceAttr(canaryServiceAddr, "canary_version.id"),
					CaptureResourceAttr(canaryServiceAddr, "primary_version.id", &wantPrimaryID),
					func(*terraform.State) error {
						if wantPrimaryID == "" || wantPrimaryID == baselinePrimaryID {
							return fmt.Errorf("primary_version.id = %q after the cutover, want a new version (baseline %s)",
								wantPrimaryID, baselinePrimaryID)
						}
						outcome = "completed"
						t.Logf("E5 outcome: completed at 100; primary %s -> %s", baselinePrimaryID, wantPrimaryID)
						return nil
					},
				),
			},
			{
				PreConfig: func() {
					cutoverStep = false
					switch outcome {
					case "completed":
						// wantPrimaryID was captured by the cutover step's Check.
					case "held":
						svc := getServiceForCanaryTest(t, serviceID)
						c := svc.CanaryVersion
						if c == nil || c.CurrentWeight == nil || *c.CurrentWeight != 100 {
							t.Fatalf("E5 held: want a canary serving 100%%, got %s", canarySnapshotLine(svc))
						}
						if svc.PrimaryVersion == nil || svc.PrimaryVersion.ID != baselinePrimaryID {
							t.Fatalf("E5 held: want primary %s beside the canary, got %s", baselinePrimaryID, canarySnapshotLine(svc))
						}
						wantPrimaryID = c.ID
						t.Logf("E5 held: canary %s at 100%% beside primary %s; removing canary_percent", c.ID, baselinePrimaryID)
					default:
						t.Fatal("E5: the cutover step recorded neither outcome")
					}
				},
				Config: f.config("main:app", "v2", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(canaryServiceAddr, plancheck.ResourceActionUpdate),
						// Both outcomes keep the planned 100 in state; this step plans it to null.
						canaryPlanChangeCheck{addr: canaryServiceAddr, check: func(before, after map[string]any) error {
							if got := fmt.Sprint(before["canary_percent"]); got != "100" {
								return fmt.Errorf("state canary_percent = %s, want 100", got)
							}
							if after["canary_percent"] != nil {
								return fmt.Errorf("planned canary_percent = %v, want null", after["canary_percent"])
							}
							return nil
						}},
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(canaryServiceAddr, "current_state", "RUNNING"),
					resource.TestCheckNoResourceAttr(canaryServiceAddr, "canary_percent"),
					resource.TestCheckNoResourceAttr(canaryServiceAddr, "canary_version.id"),
					// completed: unchanged, so no new version started; held: the canary was promoted.
					resource.TestCheckResourceAttrPtr(canaryServiceAddr, "primary_version.id", &wantPrimaryID),
				),
			},
		},
	})
	t.Logf("E5 recorded outcome: %s", outcome)
}

// getServiceForCanaryTest GETs a service, failing the test on error.
func getServiceForCanaryTest(t *testing.T, serviceID string) *provider.ServiceResult {
	t.Helper()
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("test client: %v", err)
	}
	resp, err := provider.DoRequestAndParse[provider.ServiceResponse](context.Background(), client, "GET",
		fmt.Sprintf("/api/v2/services-v2/%s", serviceID), nil, 200)
	if err != nil {
		t.Fatalf("get service %s: %v", serviceID, err)
	}
	return &resp.Result
}

// canaryPlanChangeCheck runs check on the prior (refreshed state) and planned values of one
// resource's planned change.
type canaryPlanChangeCheck struct {
	addr  string
	check func(before, after map[string]any) error
}

func (c canaryPlanChangeCheck) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	for _, rc := range req.Plan.ResourceChanges {
		if rc.Address != c.addr || rc.Change == nil {
			continue
		}
		before, ok := rc.Change.Before.(map[string]any)
		if !ok {
			resp.Error = fmt.Errorf("%s: prior state is %T, want an object", c.addr, rc.Change.Before)
			return
		}
		after, _ := rc.Change.After.(map[string]any)
		resp.Error = c.check(before, after)
		return
	}
	resp.Error = fmt.Errorf("no planned change for %s", c.addr)
}

// planNestedString returns obj[outer][inner] as a string, or "" when absent.
func planNestedString(obj map[string]any, outer, inner string) string {
	m, ok := obj[outer].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m[inner].(string)
	return s
}
