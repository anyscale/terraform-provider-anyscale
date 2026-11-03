package acctest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccServiceResource_RealInfra_CanaryPercentProbe is a Gate 1 probe for the canary_percent
// design (docs/decisions/service-canary-percent). The provider never sends canary_percent, so the
// probe drives the held rollout with raw PUT /api/v2/services-v2/apply calls against a service
// that Terraform created, and logs every response and GET snapshot (ids, version names, weights,
// states; never ray_serve_config or tokens):
//
//	A. a new version (v2) with canary_percent = 0; wait for the canary to settle
//	B. the identical body with canary_percent = 50 (does the canary move, or a new version start?)
//	C. the identical body with canary_percent = 50 and an explicit, different version name
//	D. the identical body with canary_percent omitted (does the held rollout complete to 100?)
//
// Between A and B, and after D, a step logs the action the current provider plans (while a canary
// is held, and after an out-of-band completion) and then applies it. Assertions are deliberately loose: the probe
// exists to record behavior, and the design doc cites its log.
//
// Run (one real service, ~20-40 min):
//
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 TF_LOG_PROVIDER=DEBUG TF_ACC_LOG_PATH=/tmp/svc-canary.log \
//	  go test ./internal/acctest -run 'TestAccServiceResource_RealInfra_CanaryPercentProbe' -v -timeout 120m
func TestAccServiceResource_RealInfra_CanaryPercentProbe(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloud := pinnedVMCloud(t)
	instanceTypes := cloud.InstanceTypes()
	if !instanceTypes.IsValid() {
		t.Skip("No valid instance types on the resolved cloud")
	}
	projectID, _, err := CreateEphemeralTestProjectForCloud(t, cloud.ID)
	if err != nil {
		t.Fatalf("failed to create ephemeral test project scoped to cloud %s: %v", cloud.ID, err)
	}
	computeConfigName := UniqueName(t, "cc-for-service-canary")
	serviceName := UniqueName(t, "service-canary")
	const buildID = "anyscaleray2561-slim-py312-cu129"
	const workingDir = "https://github.com/anyscale/first-service/archive/refs/heads/main.zip"

	config := fmt.Sprintf(`
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
  build_id          = %[6]q
  compute_config_id = anyscale_compute_config.test.config_id
  timeouts {
    create = "60m"
    update = "60m"
  }

  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
        runtime_env = {
          working_dir = %[7]q
        }
      }
    ]
  }
}
`, computeConfigName, cloud.ID, instanceTypes.Small, serviceName, projectID, buildID, workingDir)

	var serviceID, computeConfigID string
	probe := &canaryProbe{t: t}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_service", "/api/v2/services-v2/%s"),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					CaptureResourceAttr("anyscale_service.test", "id", &serviceID),
					CaptureResourceAttr("anyscale_service.test", "compute_config_id", &computeConfigID),
				),
			},
			{
				PreConfig: func() {
					probe.init(serviceID, map[string]any{
						"name":              serviceName,
						"project_id":        projectID,
						"build_id":          buildID,
						"compute_config_id": computeConfigID,
						"ray_serve_config": map[string]any{
							"applications": []any{map[string]any{
								"import_path": "main:app",
								"runtime_env": map[string]any{
									"working_dir": workingDir,
									"env_vars":    map[string]string{"TFACC_CANARY_PROBE": "v2"},
								},
							}},
						},
					})
					probe.snapshot("before A")
					probe.apply("A canary_percent=0", map[string]any{"canary_percent": 0})
					probe.waitFor("A settled", 40*time.Minute, func(r *provider.ServiceResult) bool {
						return canaryHealthy(r)
					})
					probe.logEvents("after A")
				},
				// What the current provider plans while a canary is held at 0%.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{logPlanActions{t: t, label: "plan while canary held at 0"}},
				},
			},
			{
				PreConfig: func() {
					probe.apply("B canary_percent=50", map[string]any{"canary_percent": 50})
					probe.waitFor("B settled", 20*time.Minute, func(r *provider.ServiceResult) bool {
						return r.CanaryVersion != nil && r.CanaryVersion.CurrentWeight != nil &&
							*r.CanaryVersion.CurrentWeight == 50
					})
					probe.apply("C canary_percent=50 version=tfacc-probe-renamed",
						map[string]any{"canary_percent": 50, "version": "tfacc-probe-renamed"})
					probe.snapshot("after C")
					probe.apply("D canary_percent omitted", nil)
					probe.waitFor("D settled", 30*time.Minute, func(r *provider.ServiceResult) bool {
						return r.CanaryVersion == nil && r.CurrentState == "RUNNING"
					})
					probe.logEvents("after D")
				},
				// What the current provider plans after an out-of-band completion to v2.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{logPlanActions{t: t, label: "plan after out-of-band completion"}},
				},
			},
		},
	})
}

// TestAccServiceResource_RealInfra_CanaryPercentProbeCompletion is the second Gate 1 probe for
// the canary_percent design. It records two things the first probe does not:
//
//	E. a canary held at 0, then canary_percent = 100: does the rollout complete on its own?
//	   (Manual completion at 100 is gated on a backend feature flag.) If it has not completed
//	   within 15 minutes, canary_percent is omitted to finish it.
//	G. before E, a rollback (POST .../rollback) of a healthy canary held at 0, then a fresh hold
//	   at 0: the "abandon the canary" path.
//	H. after E, the same body re-sent with no rollout in flight: no-op, or a new version?
//	F. a broken version (missing import_path) held at canary_percent = 0: what happens to a
//	   canary that fails while held, and what does the service report? A canary still present
//	   afterward is rolled back.
//
// Run (one real service, ~30-45 min):
//
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 TF_LOG_PROVIDER=DEBUG TF_ACC_LOG_PATH=/tmp/svc-canary2.log \
//	  go test ./internal/acctest -run 'TestAccServiceResource_RealInfra_CanaryPercentProbeCompletion' -v -timeout 150m
func TestAccServiceResource_RealInfra_CanaryPercentProbeCompletion(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloud := pinnedVMCloud(t)
	instanceTypes := cloud.InstanceTypes()
	if !instanceTypes.IsValid() {
		t.Skip("No valid instance types on the resolved cloud")
	}
	projectID, _, err := CreateEphemeralTestProjectForCloud(t, cloud.ID)
	if err != nil {
		t.Fatalf("failed to create ephemeral test project scoped to cloud %s: %v", cloud.ID, err)
	}
	computeConfigName := UniqueName(t, "cc-for-service-canary2")
	serviceName := UniqueName(t, "service-canary2")
	const buildID = "anyscaleray2561-slim-py312-cu129"
	const workingDir = "https://github.com/anyscale/first-service/archive/refs/heads/main.zip"

	config := fmt.Sprintf(`
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
  build_id          = %[6]q
  compute_config_id = anyscale_compute_config.test.config_id
  timeouts {
    create = "60m"
    update = "60m"
  }

  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
        runtime_env = {
          working_dir = %[7]q
        }
      }
    ]
  }
}
`, computeConfigName, cloud.ID, instanceTypes.Small, serviceName, projectID, buildID, workingDir)

	versionBody := func(importPath, marker string) map[string]any {
		return map[string]any{
			"name":       serviceName,
			"project_id": projectID,
			"build_id":   buildID,
			"ray_serve_config": map[string]any{
				"applications": []any{map[string]any{
					"import_path": importPath,
					"runtime_env": map[string]any{
						"working_dir": workingDir,
						"env_vars":    map[string]string{"TFACC_CANARY_PROBE": marker},
					},
				}},
			},
		}
	}

	var serviceID, computeConfigID string
	probe := &canaryProbe{t: t}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_service", "/api/v2/services-v2/%s"),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					CaptureResourceAttr("anyscale_service.test", "id", &serviceID),
					CaptureResourceAttr("anyscale_service.test", "compute_config_id", &computeConfigID),
				),
			},
			{
				PreConfig: func() {
					base := versionBody("main:app", "v2")
					base["compute_config_id"] = computeConfigID
					probe.init(serviceID, base)
					probe.apply("E0 canary_percent=0", map[string]any{"canary_percent": 0})
					probe.waitFor("E0 settled", 40*time.Minute, func(r *provider.ServiceResult) bool {
						return canaryHealthy(r)
					})
					probe.rollback("G rollback of a healthy held canary")
					probe.waitFor("G settled", 20*time.Minute, func(r *provider.ServiceResult) bool {
						return r.CanaryVersion == nil && r.CurrentState == "RUNNING"
					})
					probe.logEvents("after G")
					probe.apply("E0b canary_percent=0 after rollback", map[string]any{"canary_percent": 0})
					probe.waitFor("E0b settled", 40*time.Minute, canaryHealthy)
					probe.apply("E canary_percent=100", map[string]any{"canary_percent": 100})
					probe.waitFor("E settled", 15*time.Minute, func(r *provider.ServiceResult) bool {
						return r.CanaryVersion == nil && r.CurrentState == "RUNNING"
					})
					if r, err := probe.get(); err == nil && r.CanaryVersion != nil {
						probe.apply("E finish: canary_percent omitted", nil)
						probe.waitFor("E finish settled", 20*time.Minute, func(r *provider.ServiceResult) bool {
							return r.CanaryVersion == nil && r.CurrentState == "RUNNING"
						})
					}
					probe.logEvents("after E")

					// H: re-send the now-primary version's body with no rollout in flight.
					probe.apply("H re-send primary body, no rollout in flight", nil)
					probe.snapshot("after H")
					probe.waitFor("H settled", 20*time.Minute, func(r *provider.ServiceResult) bool {
						return r.CanaryVersion == nil && r.CurrentState == "RUNNING"
					})

					broken := versionBody("main:tfacc_missing_app", "v3")
					broken["compute_config_id"] = computeConfigID
					probe.base = broken
					probe.apply("F broken canary_percent=0", map[string]any{"canary_percent": 0})
					probe.waitFor("F settled", 30*time.Minute, func(r *provider.ServiceResult) bool {
						return r.CanaryVersion == nil || canaryHealthy(r)
					})
					probe.snapshot("F final")
					probe.logEvents("after F")
					if r, err := probe.get(); err == nil && r.CanaryVersion != nil {
						probe.rollback("F rollback of the held broken canary")
						probe.waitFor("F rollback settled", 20*time.Minute, func(r *provider.ServiceResult) bool {
							return r.CanaryVersion == nil
						})
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{logPlanActions{t: t, label: "plan after E and F"}},
				},
			},
		},
	})
}

// canaryProbe issues raw apply calls and logs whitelisted snapshots.
type canaryProbe struct {
	t         *testing.T
	client    *provider.Client
	serviceID string
	base      map[string]any
	last      string
}

func (p *canaryProbe) init(serviceID string, base map[string]any) {
	client, err := GetTestClient()
	if err != nil {
		p.t.Fatalf("test client: %v", err)
	}
	p.client, p.serviceID, p.base = client, serviceID, base
}

// apply PUTs the base body plus extra fields. A non-202 is logged, not fatal: a 409 or 400 is
// itself a result the probe records.
func (p *canaryProbe) apply(label string, extra map[string]any) {
	body := map[string]any{}
	for k, v := range p.base {
		body[k] = v
	}
	for k, v := range extra {
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		p.t.Fatalf("marshal: %v", err)
	}
	respBody, err := provider.DoRequestRaw(context.Background(), p.client, "PUT",
		"/api/v2/services-v2/apply", bytes.NewReader(raw), 202)
	if err != nil {
		var se *provider.UnexpectedStatusError
		if errors.As(err, &se) {
			p.t.Logf("GATE1 %s: PUT status=%d body=%s", label, se.StatusCode, se.Body)
		} else {
			p.t.Logf("GATE1 %s: PUT error: %v", label, err)
		}
		return
	}
	var parsed provider.ServiceResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		p.t.Logf("GATE1 %s: PUT 202, unparsable response: %v", label, err)
		return
	}
	p.t.Logf("GATE1 %s: PUT 202 response %s", label, canarySnapshotLine(&parsed.Result))
}

// rollback POSTs the service's rollback endpoint, as `anyscale service rollback` does, and logs
// the response.
func (p *canaryProbe) rollback(label string) {
	respBody, err := provider.DoRequestRaw(context.Background(), p.client, "POST",
		fmt.Sprintf("/api/v2/services-v2/%s/rollback", p.serviceID), nil, 202)
	if err != nil {
		var se *provider.UnexpectedStatusError
		if errors.As(err, &se) {
			p.t.Logf("GATE1 %s: POST status=%d body=%s", label, se.StatusCode, se.Body)
		} else {
			p.t.Logf("GATE1 %s: POST error: %v", label, err)
		}
		return
	}
	var parsed provider.ServiceResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		p.t.Logf("GATE1 %s: POST 202, unparsable response: %v", label, err)
		return
	}
	p.t.Logf("GATE1 %s: POST 202 response %s", label, canarySnapshotLine(&parsed.Result))
}

func (p *canaryProbe) get() (*provider.ServiceResult, error) {
	resp, err := provider.DoRequestAndParse[provider.ServiceResponse](context.Background(), p.client, "GET",
		fmt.Sprintf("/api/v2/services-v2/%s", p.serviceID), nil, 200)
	if err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

func (p *canaryProbe) snapshot(label string) {
	r, err := p.get()
	if err != nil {
		p.t.Logf("GATE1 %s: GET error: %v", label, err)
		return
	}
	p.t.Logf("GATE1 %s: GET %s", label, canarySnapshotLine(r))
}

// waitFor polls every 10s, logging each distinct snapshot, until done reports true. Timing out is
// logged, not fatal, so later probe stages still run and the service is still destroyed.
func (p *canaryProbe) waitFor(label string, timeout time.Duration, done func(*provider.ServiceResult) bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r, err := p.get()
		if err == nil {
			line := canarySnapshotLine(r)
			if line != p.last {
				p.t.Logf("GATE1 %s %s %s", label, time.Now().UTC().Format(time.RFC3339), line)
				p.last = line
			}
			if done(r) {
				return
			}
		}
		time.Sleep(10 * time.Second)
	}
	p.t.Logf("GATE1 %s: timed out after %s", label, timeout)
}

func (p *canaryProbe) logEvents(label string) {
	type event struct {
		CurrentState *string `json:"current_state"`
		EventType    *string `json:"event_type"`
		Message      *string `json:"message"`
	}
	resp, err := provider.DoRequestAndParse[struct {
		Results []event `json:"results"`
	}](context.Background(), p.client, "GET", fmt.Sprintf("/api/v2/services-v2/%s/events?count=20", p.serviceID), nil, 200)
	if err != nil {
		p.t.Logf("GATE1 %s: events fetch failed: %v", label, err)
		return
	}
	deref := func(s *string) string {
		if s == nil {
			return "null"
		}
		return *s
	}
	for _, e := range resp.Results {
		p.t.Logf("GATE1 %s event type=%s current_state=%s message=%s", label, deref(e.EventType), deref(e.CurrentState), deref(e.Message))
	}
}

// canarySnapshotLine extends serviceSnapshotLine with each version's weights.
func canarySnapshotLine(r *provider.ServiceResult) string {
	line := serviceSnapshotLine(r)
	for i, v := range []*provider.ServiceVersionResult{r.PrimaryVersion, r.CanaryVersion} {
		if v == nil {
			continue
		}
		label := [...]string{"primary", "canary"}[i]
		line += fmt.Sprintf(" %s_weight=%d/%s/%s", label, v.Weight, int64PtrString(v.CurrentWeight), int64PtrString(v.TargetWeight))
	}
	return line
}

// canaryHealthy reports a canary that is RUNNING with every one of its checklist rows RUNNING. The
// version's own state turns RUNNING before its APPLICATION and TARGET_NODE_GROUP rows do.
func canaryHealthy(r *provider.ServiceResult) bool {
	if r.CanaryVersion == nil || r.CanaryVersion.CurrentState != "RUNNING" {
		return false
	}
	if r.ServiceStatusChecklist == nil {
		return false
	}
	for _, g := range r.ServiceStatusChecklist.PerVersion {
		if g.VersionID != r.CanaryVersion.ID {
			continue
		}
		for _, item := range g.Items {
			if item.State != "RUNNING" {
				return false
			}
		}
		return true
	}
	return false
}

// logPlanActions logs each resource's planned action and never fails, so a probe step records
// what the provider plans whether or not the plan is empty.
type logPlanActions struct {
	t     *testing.T
	label string
}

func (l logPlanActions) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, _ *plancheck.CheckPlanResponse) {
	for _, rc := range req.Plan.ResourceChanges {
		l.t.Logf("GATE1 %s: %s actions=%v", l.label, rc.Address, rc.Change.Actions)
	}
}
