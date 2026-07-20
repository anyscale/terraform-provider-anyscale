package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccServiceResource_RealInfra is the load-bearing real-infra gate for contract AC-R5/AC-R6 -
// no mock can prove either of these, since both depend on the real backend's actual behavior:
//
//   - AC-R5: after a real apply, a SECOND plan (refresh + diff against unchanged config) must be
//     completely EMPTY - not just ray_serve_config, but every attribute (connection_ids, tags,
//     every computed field). A server-side normalization of the applied ray_serve_config, or any
//     other drift source, would show up here and nowhere else.
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
				),
				// AC-R5, the load-bearing gate: an EMPTY plan across every attribute after a real
				// apply, not just a spot-check on ray_serve_config.
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
