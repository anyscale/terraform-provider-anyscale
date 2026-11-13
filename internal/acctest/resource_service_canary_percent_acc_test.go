package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

// Mock acceptance tests for anyscale_service canary_percent (docs/decisions/service-canary-percent).

func intPtr(v int) *int { return &v }

// outcomeVersions models how Anyscale matches an apply to a version (services_dao.py, the
// apply path's _version_matches). A version is identified by md5_static_hash over its
// base_ha_jobs_config (models_v2.py to_json_str). Of the fields that hash covers, the mock hashes
// the ones the provider's apply body sends: build_id, compute_config_id, the service config,
// ray_serve_config without target_capacity, and connection_ids when sent. The hash also covers
// entrypoint, runtime_env_json, cloud_id, ray_gcs_external_storage_config and max_retries; the
// apply body sends none of them, so they are constant per service and leaving them out is
// equivalent. The version name, canary_percent, rollout_strategy and max_surge_percent are not
// part of it.
//
// With a rollout in flight, an apply matching the rollout's goal version updates that rollout
// and any other apply gets the backend's 409. With no rollout, re-sending the primary's
// own config starts a new version; the mock counts those so a test can bound them.
//
// Two corners are stricter than the backend. The backend decides whether connection_ids count
// from the new apply alone, so a canary held with ids matches a later apply without them; the
// mock 409s it. The provider cannot send that sequence: a connection_ids change is a deploy-field
// change, which rolls the canary back first. And a canary held out of band rejects
// every apply here, where the backend would accept one with the same config.
type outcomeVersions struct {
	primaryHash string
	// canaryHash is the in-flight rollout's goal version, "" for none. A canary that appears
	// through setScript rather than an apply (held out of band) gets a hash no apply matches,
	// since Terraform cannot know that version's config.
	canaryHash, canaryID, canaryName string
	// heldTarget is the last manual share an accepted apply set, nil when none is held.
	heldTarget *int
	// primaryResends counts applies that re-sent the primary's config with no rollout in flight.
	primaryResends, wantPrimaryResends int
}

const outOfBandVersionHash = "out-of-band"

// newCanaryMock is newOutcomeMock with version matching on. wantPrimaryResends is how many
// applies may re-send the primary's own config (each starts a new version); the test fails at
// cleanup if the count differs.
func newCanaryMock(t *testing.T, serviceID, name string, wantPrimaryResends int, onApply func(n int, body map[string]any) outcomeApplyResult) (*outcomeMock, *httptest.Server) {
	t.Helper()
	m, server := newOutcomeMock(t, serviceID, name, onApply)
	m.versions = &outcomeVersions{wantPrimaryResends: wantPrimaryResends}
	t.Cleanup(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if got := m.versions.primaryResends; got != wantPrimaryResends {
			t.Errorf("%d applies re-sent the primary's config with no rollout in flight (each starts a new version), want %d", got, wantPrimaryResends)
		}
	})
	return m, server
}

// backendVersionHash stands in for md5_static_hash. It is computed from the request body
// independently of the provider's own apply fingerprint, so it can disagree with it.
func backendVersionHash(body map[string]any) string {
	rsc := map[string]any{}
	if in, ok := body["ray_serve_config"].(map[string]any); ok {
		for k, v := range in {
			if k != "target_capacity" {
				rsc[k] = v
			}
		}
	}
	fields := []any{body["build_id"], body["compute_config_id"], body["config"], rsc}
	if conns, ok := body["connection_ids"].([]any); ok {
		ids := make([]string, 0, len(conns))
		for _, c := range conns {
			ids = append(ids, fmt.Sprint(c))
		}
		sort.Strings(ids)
		fields = append(fields, ids)
	}
	raw, _ := json.Marshal(fields) // map keys marshal sorted, like sort_keys=True
	return string(raw)
}

// matchVersion returns the backend's rejection of an apply body, or 0 when it is accepted.
func (m *outcomeMock) matchVersion(body map[string]any) (int, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.versions
	if v == nil {
		return 0, ""
	}
	h := backendVersionHash(body)
	switch {
	case v.canaryHash != "" && h != v.canaryHash:
		id := v.canaryID
		if len(id) > 8 {
			id = id[len(id)-8:]
		}
		return http.StatusConflict, fmt.Sprintf("There is already a rollout to Service Version %s (%s) that is "+
			"currently in progress. If you want to start a new rollout, please rollback previous rollout "+
			"first using `anyscale service rollback` command.", v.canaryName, id)
	case v.canaryHash == "" && v.primaryHash != "" && h == v.primaryHash:
		v.primaryResends++
	}
	return 0, ""
}

// recordAcceptedVersion records what an accepted apply started, and fails the test when the
// scripted response contradicts the backend: a manual share is reported exactly when the apply
// sent canary_percent (auto_rollout_enabled), and a changed share has not settled in the 202.
func (m *outcomeMock) recordAcceptedVersion(body map[string]any, resp outcomeSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.versions
	if v == nil {
		return
	}
	h := backendVersionHash(body)
	if resp.Canary == nil {
		v.primaryHash, v.canaryHash, v.heldTarget = h, "", nil
		return
	}
	_, manual := body["canary_percent"]
	if resp.Manual != manual {
		m.t.Errorf("mock script: apply with canary_percent sent = %t answered with a manual hold = %t", manual, resp.Manual)
	}
	c := resp.Canary
	if manual && c.TargetWeight != nil && c.CurrentWeight != nil && *c.CurrentWeight == *c.TargetWeight &&
		(v.heldTarget == nil || *v.heldTarget != *c.TargetWeight) {
		m.t.Errorf("mock script: a new share of %d is already settled in the apply response; current_weight lags target_weight", *c.TargetWeight)
	}
	v.canaryHash, v.canaryID, v.canaryName = h, c.ID, c.Name
	v.heldTarget = nil
	if manual {
		v.heldTarget = c.TargetWeight
	}
}

// observe updates the model from a snapshot GET returns: a canary that is gone was promoted
// (it is now the primary) or rolled back, and a canary the mock did not start was held out of
// band. Called with m.mu held.
func (v *outcomeVersions) observe(s outcomeSnapshot) {
	if v == nil {
		return
	}
	switch {
	case s.Canary == nil && v.canaryHash != "":
		if s.Primary.ID == v.canaryID {
			v.primaryHash = v.canaryHash
		}
		v.canaryHash, v.heldTarget = "", nil
	case s.Canary != nil && v.canaryHash == "":
		v.canaryHash, v.canaryID, v.canaryName = outOfBandVersionHash, s.Canary.ID, s.Canary.Name
	}
}

// heldCanary is v2 held as a manual canary at percent, with its share settled.
func heldCanary(percent int) outcomeSnapshot {
	c := outcomeV2
	c.TargetWeight, c.CurrentWeight = intPtr(percent), intPtr(percent)
	return outcomeSnapshot{CurrentState: "ROLLING_OUT", Primary: outcomeV1, Canary: &c, Manual: true, CanaryState: "RUNNING"}
}

// A canary held from outside Terraform (anyscale service deploy --canary-percent) is not this
// resource's rollout: Read must not adopt its share, so a config without canary_percent plans
// empty rather than proposing to finish someone else's rollout.
func TestAccServiceResource_CanaryPercentOutOfBandCanaryPlansEmpty(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-oob"
	m, server := newCanaryMock(t, "svc_canary_oob", name, 0, func(n int, _ map[string]any) outcomeApplyResult {
		if n > 1 {
			t.Errorf("unexpected apply %d", n)
		}
		return outcomeCreate()
	})
	base := outcomeConfig(server.URL, name, "main:app", "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				PreConfig: func() { m.setScript(heldCanary(10)) },
				Config:    base,
				PlanOnly:  true,
			},
		},
	})
}

// Importing a service with a manually held canary recovers canary_percent from it and adopts
// the hold, so a config that declares the same share plans empty.
func TestAccServiceResource_CanaryPercentImportRecoversHeldCanary(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-import"
	m, server := newCanaryMock(t, "svc_canary_import", name, 0, func(int, map[string]any) outcomeApplyResult {
		t.Errorf("unexpected apply after import")
		return outcomeCreate()
	})
	m.setScript(heldCanary(25))
	held := outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 25")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_service.test",
				ImportState:        true,
				ImportStateId:      "svc_canary_import",
				ImportStatePersist: true,
				Config:             held,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					return expectImportedAttr(states, "canary_percent", "25")
				},
			},
			{
				// rollout_strategy's default is the accepted post-import residual (see
				// TestAccServiceResource_ImportSeedsTrackingResource); applying records it locally
				// without a PUT /apply, and nothing else differs.
				Config: held,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr("anyscale_service.test", "canary_percent", "25"),
			},
		},
	})
}

// canary_percent splits traffic between two versions, which an IN_PLACE upgrade never has.
func TestAccServiceResource_CanaryPercentInPlaceRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-inplace"
	_, server := newCanaryMock(t, "svc_canary_inplace", name, 0, func(int, map[string]any) outcomeApplyResult {
		t.Errorf("unexpected apply")
		return outcomeCreate()
	})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: outcomeConfig(server.URL, name, "main:app",
				"  canary_percent    = 10\n  rollout_strategy  = \"IN_PLACE\""),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`canary_percent Requires ROLLOUT`),
		}},
	})
}

// While a canary is held, renaming it alone is rejected at plan: the next apply only re-sends
// the held version, and Anyscale keeps that version's name.
func TestAccServiceResource_CanaryPercentRenameWhileHeldRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-rename"
	m, server := newCanaryMock(t, "svc_canary_rename", name, 0, func(int, map[string]any) outcomeApplyResult {
		t.Errorf("unexpected apply")
		return outcomeCreate()
	})
	m.setScript(heldCanary(25))
	held := outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 25")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_service.test",
				ImportState:        true,
				ImportStateId:      "svc_canary_rename",
				ImportStatePersist: true,
				Config:             held,
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app_v2",
					"  canary_percent    = 25\n  version_name      = \"renamed\""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Cannot Rename a Held Canary`),
			},
		},
	})
}

// expectImportedAttr checks one attribute of the single imported instance.
func expectImportedAttr(states []*terraform.InstanceState, key, want string) error {
	if len(states) != 1 {
		return fmt.Errorf("imported %d instances, want 1", len(states))
	}
	if got := states[0].Attributes[key]; got != want {
		return fmt.Errorf("imported %s = %q, want %q", key, got, want)
	}
	return nil
}

// State written by a provider without version tracking has no record of a held canary, so a
// canary held from outside Terraform must not be adopted on its first read either: the plan
// stays empty instead of proposing to finish someone else's rollout.
func TestAccServiceResource_CanaryPercentLegacyStateOutOfBandCanaryPlansEmpty(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-legacy-oob"
	m, server := newCanaryMock(t, "svc_canary_legacy_oob", name, 0, func(n int, _ map[string]any) outcomeApplyResult {
		if n > 1 {
			t.Errorf("unexpected apply %d", n)
		}
		return outcomeCreate()
	})
	base := outcomeConfig(server.URL, name, "main:app", "")
	legacy := map[string]func() (tfprotov6.ProviderServer, error){
		"anyscale": func() (tfprotov6.ProviderServer, error) {
			s, err := ProtoV6ProviderFactories["anyscale"]()
			return privateStrippingServer{s}, err
		},
	}

	resource.Test(t, resource.TestCase{
		Steps: []resource.TestStep{
			{ProtoV6ProviderFactories: legacy, Config: base},
			{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				PreConfig:                func() { m.setScript(heldCanary(10)) },
				Config:                   base,
				PlanOnly:                 true,
			},
		},
	})
}

// heldAt is v2 held at percent with the given settled share (nil: not reported yet).
func heldAt(percent int, current *int) outcomeSnapshot {
	c := outcomeV2
	c.TargetWeight, c.CurrentWeight = intPtr(percent), current
	return outcomeSnapshot{CurrentState: "ROLLING_OUT", Primary: outcomeV1, Canary: &c, Manual: true, CanaryState: "RUNNING"}
}

func (m *outcomeMock) eventLog() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.events...)
}

// completingFrom is the 202 answering an apply that drops canary_percent from a hold at current:
// the rollout turns automatic and targets 100%.
func completingFrom(current int) outcomeSnapshot {
	s := heldAt(100, intPtr(current))
	s.Manual = false
	return s
}

// canaryApplyBodyCheck checks one recorded PUT /apply body's canary_percent, import path and
// whether it named a version.
func canaryApplyBodyCheck(m *outcomeMock, i int, wantPercent any, wantImportPath string, wantVersion bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if m.applyCount() <= i {
			return fmt.Errorf("apply %d was not sent (%d applies)", i+1, m.applyCount())
		}
		body := m.applyBody(i)
		got, has := body["canary_percent"]
		switch {
		case wantPercent == nil && has:
			return fmt.Errorf("apply %d sent canary_percent %v, want it omitted", i+1, got)
		case wantPercent != nil && got != wantPercent:
			return fmt.Errorf("apply %d canary_percent = %v, want %v", i+1, got, wantPercent)
		}
		apps, _ := body["ray_serve_config"].(map[string]any)["applications"].([]any)
		if len(apps) != 1 || apps[0].(map[string]any)["import_path"] != wantImportPath {
			return fmt.Errorf("apply %d ray_serve_config = %v, want import_path %q", i+1, body["ray_serve_config"], wantImportPath)
		}
		if _, named := body["version"]; named != wantVersion {
			return fmt.Errorf("apply %d sent version = %t, want %t", i+1, named, wantVersion)
		}
		return nil
	}
}

// The canary workflow: deploy held at 0, shift to 25, then remove canary_percent to finish. The
// shift re-sends the held version's config without a name (Anyscale matches it by config, so the
// canary moves), and every step plans empty afterwards. The first hold reports a null
// current_weight before settling: null is not settled, even at 0.
func TestAccServiceResource_CanaryPercentHoldShiftComplete(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-flow"
	settledV2 := outcomeSnapshot{CurrentState: "RUNNING", Primary: outcomeV2}
	m, server := newCanaryMock(t, "svc_canary_flow", name, 0, func(n int, _ map[string]any) outcomeApplyResult {
		switch n {
		case 1:
			return outcomeCreate()
		case 2:
			return outcomeApplyResult{Response: heldAt(0, nil), Script: []outcomeSnapshot{heldAt(0, nil), heldAt(0, intPtr(0))}}
		case 3:
			return outcomeApplyResult{Response: heldAt(25, intPtr(0)), Script: []outcomeSnapshot{heldAt(25, intPtr(25))}}
		default:
			return outcomeApplyResult{Response: completingFrom(25), Script: []outcomeSnapshot{settledV2}}
		}
	})
	empty := resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{
				Config:           outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 0"),
				ConfigPlanChecks: empty,
				Check: resource.ComposeAggregateTestCheckFunc(
					canaryApplyBodyCheck(m, 1, float64(0), "main:app_v2", false),
					resource.TestCheckResourceAttr("anyscale_service.test", "canary_version.current_weight", "0"),
				),
			},
			{
				Config:           outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 25"),
				ConfigPlanChecks: empty,
				Check: resource.ComposeAggregateTestCheckFunc(
					canaryApplyBodyCheck(m, 2, float64(25), "main:app_v2", false),
					resource.TestCheckResourceAttr("anyscale_service.test", "canary_percent", "25"),
				),
			},
			{
				Config:           outcomeConfig(server.URL, name, "main:app_v2", ""),
				ConfigPlanChecks: empty,
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 4),
					canaryApplyBodyCheck(m, 3, nil, "main:app_v2", false),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.id", "svcver_v2"),
				),
			},
		},
	})
}

// With no canary present, a canary_percent change has nothing to split: it is recorded in state
// with no API write and takes effect on the next deploy.
func TestAccServiceResource_CanaryPercentNoCanaryIsStateOnly(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-state-only"
	m, server := newCanaryMock(t, "svc_canary_state_only", name, 0, func(n int, _ map[string]any) outcomeApplyResult {
		if n > 1 {
			t.Errorf("unexpected apply %d", n)
		}
		return outcomeCreate()
	})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{
				Config: outcomeConfig(server.URL, name, "main:app", "  canary_percent    = 25"),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 1),
					resource.TestCheckResourceAttr("anyscale_service.test", "canary_percent", "25"),
				),
			},
		},
	})
	if got := m.eventLog(); len(got) != 1 {
		t.Errorf("events = %v, want only the create apply", got)
	}
}

// A deploy while a canary is held rolls the canary back first, then deploys. Deploying back to
// the config that was live before the hold stops after the rollback: re-sending it would start a
// new version.
func TestAccServiceResource_CanaryPercentDeployWhileHeldRollsBack(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-redeploy"
	m, server := newCanaryMock(t, "svc_canary_redeploy", name, 0, func(n int, _ map[string]any) outcomeApplyResult {
		switch n {
		case 1:
			return outcomeCreate()
		case 2, 3:
			return outcomeApplyResult{Response: heldAt(10, nil), Script: []outcomeSnapshot{heldAt(10, intPtr(10))}}
		default:
			t.Errorf("unexpected apply %d", n)
			return outcomeCreate()
		}
	})
	m.rollbackScript = []outcomeSnapshot{{CurrentState: "RUNNING", Primary: outcomeV1}}
	held := outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 10")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{Config: held},
			{
				// New config while held: rollback, then a new hold. The mock answers with v2 again,
				// which is enough to see the order.
				Config: outcomeConfig(server.URL, name, "main:app_v3", "  canary_percent    = 10"),
				Check: func(*terraform.State) error {
					want := []string{"apply", "apply", "rollback", "apply"}
					if got := m.eventLog(); fmt.Sprint(got) != fmt.Sprint(want) {
						return fmt.Errorf("events = %v, want %v", got, want)
					}
					return canaryApplyBodyCheck(m, 2, float64(10), "main:app_v3", false)(nil)
				},
			},
			{
				// Back to the pre-canary config: the rollback restores it, and nothing is re-sent.
				Config: outcomeConfig(server.URL, name, "main:app", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 3),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.id", "svcver_v1"),
					resource.TestCheckNoResourceAttr("anyscale_service.test", "canary_version.id"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// A held canary that fails is never rolled back by Anyscale. The apply holding it rolls it back
// itself, fails with Service Rollout Rolled Back, and the next plan proposes the change again.
func TestAccServiceResource_CanaryPercentFailedHoldRollsBack(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-failed"
	failed := heldAt(10, intPtr(0))
	failed.CanaryState, failed.CurrentState, failed.CanaryRow = "UNHEALTHY", "UNHEALTHY", "UNHEALTHY"
	m, server := newCanaryMock(t, "svc_canary_failed", name, 0, func(n int, _ map[string]any) outcomeApplyResult {
		if n == 1 {
			return outcomeCreate()
		}
		return outcomeApplyResult{Response: heldAt(10, nil), Script: []outcomeSnapshot{failed}}
	})
	m.rollbackScript = []outcomeSnapshot{{CurrentState: "RUNNING", Primary: outcomeV1}}
	held := outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 10")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{Config: held, ExpectError: regexp.MustCompile(`Service Rollout Rolled Back`)},
			{
				PreConfig: func() {
					if got := m.eventLog(); fmt.Sprint(got) != fmt.Sprint([]string{"apply", "apply", "rollback"}) {
						t.Errorf("events = %v, want the failed hold rolled back", got)
					}
				},
				Config:             held,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// Deploying back to the pre-canary config with a new version_name is not a revert: the
// rollback restores the old version under its old name, so the new name needs the PUT.
func TestAccServiceResource_CanaryPercentRevertWithNewNameDeploys(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-revert-name"
	renamed := outcomeVersion{ID: "svcver_v4", Name: "v1-again", ImportPath: "main:app"}
	m, server := newCanaryMock(t, "svc_canary_revert_name", name, 1, func(n int, _ map[string]any) outcomeApplyResult {
		switch n {
		case 1:
			return outcomeCreate()
		case 2:
			return outcomeApplyResult{Response: heldAt(10, nil), Script: []outcomeSnapshot{heldAt(10, intPtr(10))}}
		default:
			return outcomeRollout(outcomeV1, renamed)
		}
	})
	m.rollbackScript = []outcomeSnapshot{{CurrentState: "RUNNING", Primary: outcomeV1}}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{Config: outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 10")},
			{
				Config: outcomeConfig(server.URL, name, "main:app", "  version_name      = \"v1-again\""),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 3),
					resource.TestCheckResourceAttr("anyscale_service.test", "version_name", "v1-again"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// canary_percent = 100 cuts all traffic over at once; where Anyscale completes the rollout the
// apply waits for it and ends on the new version.
func TestAccServiceResource_CanaryPercentCutoverCompletes(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-cutover"
	m, server := newCanaryMock(t, "svc_canary_cutover", name, 0, func(n int, _ map[string]any) outcomeApplyResult {
		switch n {
		case 1:
			return outcomeCreate()
		case 2:
			return outcomeApplyResult{Response: heldAt(10, nil), Script: []outcomeSnapshot{heldAt(10, intPtr(10))}}
		default:
			return outcomeApplyResult{Response: heldAt(100, intPtr(10)), Script: []outcomeSnapshot{
				heldAt(100, intPtr(100)), {CurrentState: "RUNNING", Primary: outcomeV2},
			}}
		}
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{Config: outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 10")},
			{
				Config: outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 100"),
				Check: resource.ComposeAggregateTestCheckFunc(
					canaryApplyBodyCheck(m, 2, float64(100), "main:app_v2", false),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.id", "svcver_v2"),
				),
			},
		},
	})
}

// A single error reading of the canary version, with its checklist row not in error, is not a
// failure: rolling back is destructive. A canary that recovers is held as planned.
func TestAccServiceResource_CanaryPercentUnconfirmedFailureNotRolledBack(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-blip"
	blip := heldAt(10, intPtr(0))
	blip.CanaryState, blip.CanaryRow = "UNHEALTHY", "STARTING"
	m, server := newCanaryMock(t, "svc_canary_blip", name, 0, func(n int, _ map[string]any) outcomeApplyResult {
		if n == 1 {
			return outcomeCreate()
		}
		return outcomeApplyResult{Response: heldAt(10, nil), Script: []outcomeSnapshot{blip, heldAt(10, intPtr(10))}}
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{
				Config: outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 10"),
				Check:  resource.TestCheckResourceAttr("anyscale_service.test", "canary_version.current_weight", "10"),
			},
		},
	})
	for _, e := range m.eventLog() {
		if e == "rollback" {
			t.Errorf("events = %v, want no rollback for an unconfirmed failure", m.eventLog())
		}
	}
}

// A 100 cutover that Anyscale does not complete fails the apply after the grace period, and the
// canary stays this resource's hold: removing canary_percent then re-sends the held config
// without it, which finishes the rollout.
func TestAccServiceResource_CanaryPercentCutoverNotCompletedThenNullFinishes(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	t.Cleanup(provider.SetServiceCutoverGraceForTest(time.Millisecond))

	const name = "canary-cutover-stuck"
	m, server := newCanaryMock(t, "svc_canary_cutover_stuck", name, 0, func(n int, _ map[string]any) outcomeApplyResult {
		switch n {
		case 1:
			return outcomeCreate()
		case 2:
			return outcomeApplyResult{Response: heldAt(100, intPtr(0)), Script: []outcomeSnapshot{heldAt(100, intPtr(100))}}
		default:
			return outcomeApplyResult{Response: completingFrom(100), Script: []outcomeSnapshot{{CurrentState: "RUNNING", Primary: outcomeV2}}}
		}
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{
				Config:      outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 100"),
				ExpectError: regexp.MustCompile(`Canary Rollout Not Completed`),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app_v2", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 3),
					canaryApplyBodyCheck(m, 2, nil, "main:app_v2", false),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.id", "svcver_v2"),
				),
			},
		},
	})
}

// A canary failing at 100 is a manual rollout too, so Anyscale never rolls it back; the apply
// does, rather than leave all traffic on a failed version.
func TestAccServiceResource_CanaryPercentCutoverFailureRollsBack(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-cutover-failed"
	failed := heldAt(100, intPtr(100))
	failed.CanaryState, failed.CurrentState, failed.CanaryRow = "UNHEALTHY", "UNHEALTHY", "UNHEALTHY"
	m, server := newCanaryMock(t, "svc_canary_cutover_failed", name, 0, func(n int, _ map[string]any) outcomeApplyResult {
		if n == 1 {
			return outcomeCreate()
		}
		return outcomeApplyResult{Response: heldAt(100, intPtr(0)), Script: []outcomeSnapshot{failed}}
	})
	m.rollbackScript = []outcomeSnapshot{{CurrentState: "RUNNING", Primary: outcomeV1}}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{
				Config:      outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 100"),
				ExpectError: regexp.MustCompile(`Service Rollout Rolled Back`),
			},
		},
	})
	if got := m.eventLog(); fmt.Sprint(got) != fmt.Sprint([]string{"apply", "apply", "rollback"}) {
		t.Errorf("events = %v, want the failed cutover rolled back", got)
	}
}

// A service-level error can end the cutover wait one poll before the canary's own checklist row
// confirms the failure. The apply keeps polling briefly and still rolls the canary back.
func TestAccServiceResource_CanaryPercentCutoverFailureConfirmedLate(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-cutover-late"
	early := heldAt(100, intPtr(100))
	early.CanaryState, early.CurrentState = "UNHEALTHY", "UNHEALTHY"
	confirmed := early
	confirmed.CanaryRow = "UNHEALTHY"
	m, server := newCanaryMock(t, "svc_canary_cutover_late", name, 0, func(n int, _ map[string]any) outcomeApplyResult {
		if n == 1 {
			return outcomeCreate()
		}
		return outcomeApplyResult{Response: heldAt(100, intPtr(0)), Script: []outcomeSnapshot{early, confirmed}}
	})
	m.rollbackScript = []outcomeSnapshot{{CurrentState: "RUNNING", Primary: outcomeV1}}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{
				Config:      outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 100"),
				ExpectError: regexp.MustCompile(`Service Rollout Rolled Back`),
			},
		},
	})
	if got := m.eventLog(); fmt.Sprint(got) != fmt.Sprint([]string{"apply", "apply", "rollback"}) {
		t.Errorf("events = %v, want the late-confirmed cutover failure rolled back", got)
	}
}
