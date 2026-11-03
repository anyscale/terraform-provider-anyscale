package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccComputeConfigResource_RealInfra_Platform runs the compute config lifecycle on whichever
// platform's fixture cloud is pinned (ANYSCALE_TEST_CLOUD_NAME): create with a head node, one
// worker group and a platform-shaped advanced_instance_config; re-plan empty; an in-place update
// that makes a new version; import with ImportStateVerify, so advanced_instance_config must
// round-trip from the API unchanged; destroy, which archives.
//
// The advanced_instance_config payloads differ per platform because the backend passes them to
// the platform: an EC2 launch request, a GCE instance, or a Kubernetes pod. Only the AWS-VM
// payload has been confirmed against the real API; the others are the documented shapes and are
// unconfirmed until this test runs on those fixtures.
//
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 ANYSCALE_TEST_CLOUD_NAME=<fixture> \
//	  go test ./internal/acctest/ -run '^TestAccComputeConfigResource_RealInfra_Platform$' -v
//
// It creates only compute config versions (no clusters) and archives them at the end.
func TestAccComputeConfigResource_RealInfra_Platform(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloud, headType := pinnedWorkloadCloud(t)
	workerType := headType
	if !cloud.IsK8s() {
		workerType = cloud.InstanceTypes().Medium
	}
	advanced, ok := platformAdvancedInstanceConfig[cloud.Platform()]
	if !ok {
		t.Skipf("SKIP(platform): no advanced_instance_config payload for %s", cloud.Platform())
	}
	name := UniqueName(t, "cc-platform")
	config := func(maxWorkers int) string {
		return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q

  head_node = {
    instance_type = %[3]q
  }

  worker_nodes = [
    {
      name          = "worker-group-1"
      instance_type = %[4]q
      min_nodes     = 0
      max_nodes     = %[5]d
    }
  ]

  advanced_instance_config = %[6]s
}
`, name, cloud.ID, headType, workerType, maxWorkers, advanced)
	}
	const addr = "anyscale_compute_config.test"
	var firstVersionID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIArchivedDestroyCheckByAttr("anyscale_compute_config", "config_id", "/api/v2/compute_templates/%s", "result.archived_at"),
		Steps: []resource.TestStep{
			{
				Config: config(4),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cloud_id", cloud.ID),
					resource.TestCheckResourceAttr(addr, "head_node.instance_type", headType),
					resource.TestCheckResourceAttr(addr, "worker_nodes.0.instance_type", workerType),
					resource.TestCheckResourceAttr(addr, "worker_nodes.0.max_nodes", "4"),
					testAccCheckComputeConfigExistsInAPI(addr),
					CaptureResourceAttr(addr, "config_id", &firstVersionID),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: config(2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "worker_nodes.0.max_nodes", "2"),
					testAccCheckComputeConfigExistsInAPI(addr),
					func(s *terraform.State) error {
						if got := s.RootModule().Resources[addr].Primary.Attributes["config_id"]; got == firstVersionID {
							return fmt.Errorf("config_id unchanged after update (%s), want a new version", got)
						}
						return nil
					},
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: testAccComputeConfigImportStateIdFunc(addr),
				// The same exclusions as TestAccComputeConfigResource_Basic, for the same reasons.
				// advanced_instance_config is deliberately not excluded: it must come back from the
				// API exactly as configured on every platform.
				ImportStateVerifyIgnore: []string{"head_node", "worker_nodes", "min_resources", "max_resources", "zones"},
				// The exclusions above cover API-filled nested attributes; the fields this config
				// sets on the nodes must still be recovered.
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d instances, want 1", len(states))
					}
					want := map[string]string{
						"head_node.instance_type":      headType,
						"worker_nodes.#":               "1",
						"worker_nodes.0.name":          "worker-group-1",
						"worker_nodes.0.instance_type": workerType,
						"worker_nodes.0.max_nodes":     "2",
					}
					for k, v := range want {
						if got := states[0].Attributes[k]; got != v {
							return fmt.Errorf("imported %s = %q, want %q", k, got, v)
						}
					}
					return nil
				},
			},
		},
	})
}

// platformAdvancedInstanceConfig is an advanced_instance_config value, as HCL, that each
// platform's backend accepts. AWS-VM: EC2 TagSpecifications, confirmed live (see
// TestAccComputeConfigResource_ImportRecoversWriteOnlyFields_RealAPI). GCP-VM: GCE instance
// labels. Kubernetes: pod metadata labels.
var platformAdvancedInstanceConfig = map[testPlatform]string{
	platformAWSVM: `{
    TagSpecifications = [
      {
        ResourceType = "instance"
        Tags         = [{ Key = "tfacc-platform", Value = "aws-vm" }]
      }
    ]
  }`,
	platformGCPVM: `{
    labels = { tfacc-platform = "gcp-vm" }
  }`,
	platformEKS: k8sAdvancedInstanceConfig("eks"),
	platformGKE: k8sAdvancedInstanceConfig("gke"),
	platformAKS: k8sAdvancedInstanceConfig("aks"),
}

func k8sAdvancedInstanceConfig(platform string) string {
	return fmt.Sprintf(`{
    metadata = { labels = { tfacc-platform = %q } }
  }`, platform)
}
