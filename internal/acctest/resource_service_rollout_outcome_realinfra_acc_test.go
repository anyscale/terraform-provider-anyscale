package acctest

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccServiceResource_RealInfra_RolledBackRollout closes the real-API (Gate 1) items of
// docs/decisions/service-rollout-outcome against a real backend:
//
//  1. creates a service on the static VM cloud;
//  2. rolls out a deliberately broken import_path and expects the apply to fail with
//     "Service Rollout Rolled Back", then a PlanOnly step expects the change to plan again;
//  3. terminates the service out of band and expects an apply of the original config to
//     restart the same id on a new version.
//
// A background poller logs every distinct GET snapshot (states, version ids/names, error_message,
// failing checklist items; never ray_serve_config or tokens) and, after the rollback, the
// service's ROLLING_BACK events, so the observed sequence can be recorded in the decision doc.
// The provider logs each PUT /apply response's version fields at debug level.
//
// Run (one real service, ~10-15 min):
//
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 TF_LOG_PROVIDER=DEBUG TF_ACC_LOG_PATH=/tmp/svc-rollback.log \
//	  go test ./internal/acctest -run 'TestAccServiceResource_RealInfra_RolledBackRollout' -v -timeout 120m
func TestAccServiceResource_RealInfra_RolledBackRollout(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloud := pinnedVMCloud(t)
	instanceTypes := cloud.InstanceTypes()
	if !instanceTypes.IsValid() {
		t.Skip("No valid instance types on the resolved cloud")
	}
	// Scoped to the cloud: see TestAccServiceResource_RealInfra for why not the default project.
	projectID, _, err := CreateEphemeralTestProjectForCloud(t, cloud.ID)
	if err != nil {
		t.Fatalf("failed to create ephemeral test project scoped to cloud %s: %v", cloud.ID, err)
	}
	computeConfigName := UniqueName(t, "cc-for-service-rb")
	serviceName := UniqueName(t, "service-rollback")

	configFor := func(importPath string) string {
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
        }
      }
    ]
  }
}
`, computeConfigName, cloud.ID, instanceTypes.Small, serviceName, projectID, importPath)
	}
	good := configFor("main:app")
	broken := configFor("main:tfacc_missing_app")

	poller := &serviceSnapshotPoller{t: t}
	t.Cleanup(poller.stop)

	var serviceID, createdVersionID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_service", "/api/v2/services-v2/%s"),
		Steps: []resource.TestStep{
			{
				Config: good,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					CaptureResourceAttr("anyscale_service.test", "id", &serviceID),
					CaptureResourceAttr("anyscale_service.test", "primary_version.id", &createdVersionID),
				),
			},
			{
				PreConfig:   func() { poller.start(serviceID) },
				Config:      broken,
				ExpectError: regexp.MustCompile(`Service Rollout Rolled Back`),
			},
			{
				PreConfig:          func() { poller.logEvents(serviceID) },
				Config:             broken,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				// Terminate out of band; the original config must restart the same id.
				PreConfig: func() { terminateServiceOutOfBand(t, serviceID) },
				Config:    good,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					func(s *terraform.State) error {
						rs := s.RootModule().Resources["anyscale_service.test"]
						if rs.Primary.ID != serviceID {
							return fmt.Errorf("restart changed the id from %s to %s", serviceID, rs.Primary.ID)
						}
						if v := rs.Primary.Attributes["primary_version.id"]; v == createdVersionID {
							return fmt.Errorf("restart did not create a new version (still %s)", v)
						}
						return nil
					},
				),
			},
		},
	})
}

// terminateServiceOutOfBand terminates a service through the API, as `anyscale service
// terminate` does, logs the terminated GET shape, and waits for TERMINATED.
func terminateServiceOutOfBand(t *testing.T, serviceID string) {
	t.Helper()
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("test client: %v", err)
	}
	ctx := context.Background()
	if _, err := provider.DoRequestRaw(ctx, client, "POST",
		fmt.Sprintf("/api/v2/services-v2/%s/terminate", serviceID), nil, 202); err != nil {
		t.Fatalf("terminate %s: %v", serviceID, err)
	}
	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		resp, err := provider.DoRequestAndParse[provider.ServiceResponse](ctx, client, "GET",
			fmt.Sprintf("/api/v2/services-v2/%s", serviceID), nil, 200)
		if err == nil && resp.Result.CurrentState == "TERMINATED" {
			t.Logf("GATE1 terminated GET shape: %s", serviceSnapshotLine(&resp.Result))
			return
		}
		time.Sleep(15 * time.Second)
	}
	t.Fatalf("service %s did not reach TERMINATED", serviceID)
}

// serviceSnapshotPoller logs each distinct service snapshot while a rollout runs.
type serviceSnapshotPoller struct {
	t    *testing.T
	once sync.Once
	done chan struct{}
	wg   sync.WaitGroup
}

func (p *serviceSnapshotPoller) start(serviceID string) {
	p.once.Do(func() {
		p.done = make(chan struct{})
		client, err := GetTestClient()
		if err != nil {
			p.t.Logf("GATE1 poller disabled: %v", err)
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			last := ""
			for {
				resp, err := provider.DoRequestAndParse[provider.ServiceResponse](context.Background(), client, "GET",
					fmt.Sprintf("/api/v2/services-v2/%s", serviceID), nil, 200)
				if err == nil {
					if line := serviceSnapshotLine(&resp.Result); line != last {
						p.t.Logf("GATE1 %s %s", time.Now().UTC().Format(time.RFC3339), line)
						last = line
					}
				}
				select {
				case <-p.done:
					return
				case <-time.After(5 * time.Second):
				}
			}
		}()
	})
}

func (p *serviceSnapshotPoller) stop() {
	if p.done != nil {
		select {
		case <-p.done:
		default:
			close(p.done)
		}
	}
	p.wg.Wait()
}

// logEvents logs the service's recent state events, where the backend records the rollback
// reason. Only events that carry a current_state are logged.
func (p *serviceSnapshotPoller) logEvents(serviceID string) {
	client, err := GetTestClient()
	if err != nil {
		return
	}
	type event struct {
		CurrentState *string `json:"current_state"`
		EventType    *string `json:"event_type"`
		Message      *string `json:"message"`
	}
	resp, err := provider.DoRequestAndParse[struct {
		Results []event `json:"results"`
	}](context.Background(), client, "GET", fmt.Sprintf("/api/v2/services-v2/%s/events?count=30", serviceID), nil, 200)
	if err != nil {
		p.t.Logf("GATE1 events fetch failed: %v", err)
		return
	}
	deref := func(s *string) string {
		if s == nil {
			return "null"
		}
		return *s
	}
	for _, e := range resp.Results {
		if e.CurrentState == nil {
			continue
		}
		p.t.Logf("GATE1 event type=%s current_state=%s message=%s", deref(e.EventType), deref(e.CurrentState), deref(e.Message))
	}
}

// serviceSnapshotLine is the whitelisted one-line form of a GET response: no ray_serve_config,
// no tokens.
func serviceSnapshotLine(r *provider.ServiceResult) string {
	parts := []string{fmt.Sprintf("current_state=%s goal_state=%s", r.CurrentState, r.GoalState)}
	for i, v := range []*provider.ServiceVersionResult{r.PrimaryVersion, r.CanaryVersion} {
		label := [...]string{"primary", "canary"}[i]
		if v == nil {
			parts = append(parts, label+"=null")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s/%s/%s", label, v.ID, v.Version, v.CurrentState))
	}
	if r.ErrorMessage != nil {
		parts = append(parts, "error_message="+*r.ErrorMessage)
	}
	if r.EndedAt != nil {
		parts = append(parts, "ended_at="+*r.EndedAt)
	}
	if r.ServiceStatusChecklist != nil {
		for _, item := range r.ServiceStatusChecklist.Shared {
			if item.State != "RUNNING" {
				parts = append(parts, fmt.Sprintf("checklist[shared]=%s:%s:%q", item.Kind, item.State, item.Message))
			}
		}
		for _, g := range r.ServiceStatusChecklist.PerVersion {
			for _, item := range g.Items {
				if item.State != "RUNNING" {
					parts = append(parts, fmt.Sprintf("checklist[%s]=%s:%s:%q", g.VersionID, item.Kind, item.State, item.Message))
				}
			}
		}
	}
	return strings.Join(parts, " ")
}
