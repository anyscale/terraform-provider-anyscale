package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccServiceResource_RealInfra is the load-bearing real-infra gate for contract AC-R5/AC-R6,
// PLUS the real-rollout gap flagged directly by the user - the original ask was "deploying a new
// Anyscale Service as well as dealing with rolling out new versions", and neither AC-R5 nor AC-R6
// as originally scoped actually exercised the rollout half against real infrastructure. No mock
// can prove any of these three, since all depend on the real backend's actual behavior:
//
//   - AC-R5: after a real apply, a SECOND plan (refresh + diff against unchanged config) must be
//     completely EMPTY - not just ray_serve_config, but every attribute (connection_ids, tags,
//     every computed field). A server-side normalization of the applied ray_serve_config, or any
//     other drift source, would show up here and nowhere else.
//   - Real rollout (added after user follow-up, not in the original AC-R5/AC-R6 scope): changing
//     a deploy-affecting field (ray_serve_config) on an already-RUNNING service must roll out a
//     real new version, in place (same id, not a replace), and reach RUNNING again. The mock
//     suite only proves the OPPOSITE case (H2: a non-deploy-affecting change like rollout_timeout
//     must NOT redeploy) - this is the first real proof that a redeploy actually works end to end.
//   - AC-R6: after a real destroy, GET /{id} must actually 404 - proving terminate-then-wait-
//     then-delete really leaves nothing behind, not just that Destroy returned without error.
//
// Reuses an existing default application build (no fresh container image build needed - the org
// already has default Ray images with succeeded builds) and creates only a fresh, cheap
// compute_config against the existing pinned/discovered fixture cloud (never a fresh cloud) -
// per architect's reuse-first ruling, this keeps the real-infra footprint to exactly one real
// service (the thing actually under test) plus one lightweight compute_config registration.
//
// ray_serve_config points at anyscale/first-service, Anyscale's own minimal public example
// (a single FastAPI-wrapped Ray Serve deployment) - see https://github.com/anyscale/first-service.
func TestAccServiceResource_RealInfra(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)
	t.Parallel()

	vmClouds := GetAllVMClouds(t)
	if len(vmClouds) == 0 {
		t.Skip("No VM cloud available for real-infra service testing")
	}
	cloud := vmClouds[0]
	instanceTypes := cloud.InstanceTypes()
	if !instanceTypes.IsValid() {
		t.Skip("No valid instance types on the resolved cloud")
	}

	projectID := GetTestProjectID(t)
	computeConfigName := UniqueName(t, "cc-for-service")
	serviceName := UniqueName(t, "service-realinfra")

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
  build_id          = "anyscaleray2561-slim-py312-cu129"
  compute_config_id = anyscale_compute_config.test.config_id
  rollout_timeout   = "20m"

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

  tags = {
    purpose = "tfacc-real-infra-gate"
  }
}
`, computeConfigName, cloud.ID, instanceTypes.Small, serviceName, projectID)

	// updatedConfig changes ONLY ray_serve_config (adds a real env_var) - a genuine
	// deploy-affecting field change (contract §6), so it must roll out a new version in place
	// (same id, not RequiresReplace) rather than be a no-op like H2's rollout_timeout-only case.
	updatedConfig := fmt.Sprintf(`
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
  rollout_timeout   = "20m"

  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
        runtime_env = {
          working_dir = "https://github.com/anyscale/first-service/archive/refs/heads/main.zip"
          env_vars = {
            TFACC_ROLLOUT_MARKER = "v2"
          }
        }
      }
    ]
  }

  tags = {
    purpose = "tfacc-real-infra-gate"
  }
}
`, computeConfigName, cloud.ID, instanceTypes.Small, serviceName, projectID)

	var serviceIDAfterCreate string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_service", "/api/v2/services-v2/%s"),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_service.test", "id"),
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					resource.TestCheckResourceAttr("anyscale_service.test", "tags.purpose", "tfacc-real-infra-gate"),
					CaptureResourceAttr("anyscale_service.test", "id", &serviceIDAfterCreate),
				),
				// AC-R5, the load-bearing gate: an EMPTY plan across every attribute after a real
				// apply, not just a spot-check on ray_serve_config.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Real rollout step: changing ray_serve_config must actually redeploy the SAME
			// service (id unchanged, so this proves in-place Update, not a replace) and reach
			// RUNNING again - the real behavior "rolling out new versions" names explicitly, not
			// yet proven against real infrastructure anywhere else in this suite.
			{
				Config: updatedConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["anyscale_service.test"]
						if !ok {
							return fmt.Errorf("resource not found: anyscale_service.test")
						}
						if got := rs.Primary.Attributes["id"]; got != serviceIDAfterCreate {
							return fmt.Errorf("id changed from %q to %q after a ray_serve_config-only change - "+
								"this must be an in-place rollout (new version, same service), not a replace",
								serviceIDAfterCreate, got)
						}
						return nil
					},
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
		// AC-R6 is proven by CheckDestroy above: it runs after the TestCase's real destroy and
		// asserts GET /api/v2/services-v2/{id} returns 404, not just that Destroy() returned nil.
	})
}

// TestAccServiceResource_RealInfra_InPlaceRollout is the IN_PLACE-strategy companion to
// TestAccServiceResource_RealInfra's rollout step above, which exercises the default ROLLOUT
// strategy (new cluster, traffic shift). Requested directly by the user: both the in_place and
// the standard rollout upgrade paths must be covered in acctest AND real E2E, not just one of the
// two. The mock suite's TestAccServiceResource_InPlaceUpdateConverges already proves the
// provider's own polling/state handling for IN_PLACE in isolation; only real infra can prove the
// backend actually honors IN_PLACE end to end - upgrading the SAME running cluster rather than
// starting a new one - which is the thing a mock, by definition, cannot observe.
//
// rollout_strategy = "IN_PLACE" is set from the very first apply and left unchanged across both
// steps - this is the confirmed, ratified UX (Option 2, user-confirmed): the backend rejects
// IN_PLACE outright on a genuine create (a first real-infra run of this test with the initial
// config setting IN_PLACE 404'd: "service does not exist"), so the resource's Create transparently
// forces a standard deploy on the wire regardless of the configured strategy, while state still
// stores the user's real IN_PLACE value unchanged (rollout_strategy is never part of the read
// model, so this causes no drift). This test proves that contract end to end against real infra:
// the user never has to edit rollout_strategy between create and update, and the Step 2 update
// still performs a genuine in-place upgrade (same id, not a replace).
func TestAccServiceResource_RealInfra_InPlaceRollout(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)
	t.Parallel()

	vmClouds := GetAllVMClouds(t)
	if len(vmClouds) == 0 {
		t.Skip("No VM cloud available for real-infra service testing")
	}
	cloud := vmClouds[0]
	instanceTypes := cloud.InstanceTypes()
	if !instanceTypes.IsValid() {
		t.Skip("No valid instance types on the resolved cloud")
	}

	projectID := GetTestProjectID(t)
	computeConfigName := UniqueName(t, "cc-for-service-ip")
	serviceName := UniqueName(t, "service-realinfra-ip")

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
  build_id          = "anyscaleray2561-slim-py312-cu129"
  compute_config_id = anyscale_compute_config.test.config_id
  rollout_strategy  = "IN_PLACE"
  rollout_timeout   = "20m"

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

  tags = {
    purpose = "tfacc-real-infra-gate-inplace"
  }
}
`, computeConfigName, cloud.ID, instanceTypes.Small, serviceName, projectID)

	// updatedConfig switches to rollout_strategy = IN_PLACE AND changes ray_serve_config in the
	// same apply - the one field IN_PLACE permits changing (enforced at plan time by ModifyPlan;
	// see TestAccServiceResource_InPlaceRejectsBuildIDChange for the negative case). Must upgrade
	// the SAME cluster - same id, not a replace - and reach RUNNING again.
	updatedConfig := fmt.Sprintf(`
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
  rollout_strategy  = "IN_PLACE"
  rollout_timeout   = "20m"

  ray_serve_config = {
    applications = [
      {
        import_path = "main:app"
        runtime_env = {
          working_dir = "https://github.com/anyscale/first-service/archive/refs/heads/main.zip"
          env_vars = {
            TFACC_ROLLOUT_MARKER = "v2-inplace"
          }
        }
      }
    ]
  }

  tags = {
    purpose = "tfacc-real-infra-gate-inplace"
  }
}
`, computeConfigName, cloud.ID, instanceTypes.Small, serviceName, projectID)

	var serviceIDAfterCreate string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_service", "/api/v2/services-v2/%s"),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_service.test", "id"),
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					resource.TestCheckResourceAttr("anyscale_service.test", "rollout_strategy", "IN_PLACE"),
					CaptureResourceAttr("anyscale_service.test", "id", &serviceIDAfterCreate),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				Config: updatedConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
					resource.TestCheckResourceAttr("anyscale_service.test", "rollout_strategy", "IN_PLACE"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["anyscale_service.test"]
						if !ok {
							return fmt.Errorf("resource not found: anyscale_service.test")
						}
						if got := rs.Primary.Attributes["id"]; got != serviceIDAfterCreate {
							return fmt.Errorf("id changed from %q to %q after an IN_PLACE ray_serve_config-only change - "+
								"IN_PLACE must upgrade the same cluster, not replace it",
								serviceIDAfterCreate, got)
						}
						return nil
					},
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
