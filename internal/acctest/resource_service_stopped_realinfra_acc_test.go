package acctest

import (
	"context"
	"fmt"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccServiceResource_RealInfra_StopAndRestart closes the real-API (Gate 1) items of
// docs/decisions/service-stopped against a real backend, on the static VM cloud:
//
//  1. creates a service;
//  2. stopped = true: the service is TERMINATED with the same id and hostname, and the
//     post-apply plan is empty;
//  3. stopped = false: the service is RUNNING with the same id and hostname on a new primary
//     version, and the post-apply plan is empty.
//
// After each step the test logs the service's GET shape (whitelisted: states, version ids/names,
// ended_at; never ray_serve_config or tokens). Destroy is checked by GET returning 404.
//
// Run (one real service, two cluster starts):
//
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 \
//	  go test ./internal/acctest -run 'TestAccServiceResource_RealInfra_StopAndRestart' -v -timeout 120m
func TestAccServiceResource_RealInfra_StopAndRestart(t *testing.T) {
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
	computeConfigName := UniqueName(t, "cc-for-service-stop")
	serviceName := UniqueName(t, "service-stop")

	configFor := func(stopped bool) string {
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
  stopped           = %[6]t
  timeouts {
    create = "60m"
    update = "60m"
  }

  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
        runtime_env = {
          working_dir = "https://github.com/anyscale/first-service/archive/refs/heads/main.zip"
        }
      }
    ]
  }
}
`, computeConfigName, cloud.ID, instanceTypes.Small, serviceName, projectID, stopped)
	}

	var serviceID, hostname, createdVersionID string
	sameIdentity := func(s *terraform.State) error {
		rs := s.RootModule().Resources["anyscale_service.test"]
		if rs.Primary.ID != serviceID {
			return fmt.Errorf("id changed from %s to %s", serviceID, rs.Primary.ID)
		}
		if got := rs.Primary.Attributes["hostname"]; got != hostname {
			return fmt.Errorf("hostname changed from %s to %s", hostname, got)
		}
		return nil
	}
	logLive := func(label string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			client, err := GetTestClient()
			if err != nil {
				return err
			}
			resp, err := provider.DoRequestAndParse[provider.ServiceResponse](context.Background(), client, "GET",
				fmt.Sprintf("/api/v2/services-v2/%s", serviceID), nil, 200)
			if err != nil {
				return err
			}
			t.Logf("GATE1 %s: id=%s hostname_same=%t %s", label, resp.Result.ID,
				resp.Result.Hostname == hostname, serviceSnapshotLine(&resp.Result))
			return nil
		}
	}
	updates := []plancheck.PlanCheck{
		plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_service", "/api/v2/services-v2/%s"),
		Steps: []resource.TestStep{
			{
				Config: configFor(false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					resource.TestCheckResourceAttr("anyscale_service.test", "stopped", "false"),
					CaptureResourceAttr("anyscale_service.test", "id", &serviceID),
					CaptureResourceAttr("anyscale_service.test", "hostname", &hostname),
					CaptureResourceAttr("anyscale_service.test", "primary_version.id", &createdVersionID),
				),
			},
			{
				Config: configFor(true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             updates,
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "stopped", "true"),
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "TERMINATED"),
					resource.TestCheckResourceAttr("anyscale_service.test", "goal_state", "TERMINATED"),
					sameIdentity,
					logLive("after stop"),
				),
			},
			{
				Config: configFor(false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             updates,
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "stopped", "false"),
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					sameIdentity,
					func(s *terraform.State) error {
						rs := s.RootModule().Resources["anyscale_service.test"]
						if v := rs.Primary.Attributes["primary_version.id"]; v == createdVersionID {
							return fmt.Errorf("restart did not create a new version (still %s)", v)
						}
						return nil
					},
					logLive("after restart"),
				),
			},
		},
	})
}
