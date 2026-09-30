package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// This file covers AKS (Azure Kubernetes) the same way
// resource_cloud_c3_lifecycle_acc_test.go covers AWS/GCP K8S: a mock-server
// resource.Test lifecycle (create -> apply -> plan(empty) -> import ->
// plan(empty)), no real Azure infra required or available. See
// K8S-CLOUD-CONTRACT.md's "AKS DECISION: GO" section for the full design;
// the two things that would silently corrupt an Azure cloud if this test
// didn't catch them are covered explicitly below: the abfss:// bucket must
// never gain or lose its scheme, and azure_config must not be treated as a
// C3-v2 "required" block (it isn't - only kubernetes_config/object_storage
// are required for K8S, regardless of provider).

// TestAccCloudResource_Lifecycle_AzureK8S_MockServer proves the AKS create ->
// apply -> plan-empty -> import -> plan-empty lifecycle against a mock
// backend, using the all-in-one anyscale_cloud pattern (mirrors
// TestAccCloudResource_Lifecycle_K8S_MockServer's AWS case and
// TestAccCloudResource_Lifecycle_GCP_K8S_MockServer's GCP case).
func TestAccCloudResource_Lifecycle_AzureK8S_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_c3_azure_k8s_mock"
	const bucket = "abfss://ray-data@anyscaletest.dfs.core.windows.net"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "c3-azure-k8s-mock", "provider": "AZURE", "region": "eastus",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	// The mock deliberately returns the bucket in the exact abfss:// shape a
	// real Azure Storage account uses - the mutation-proof below breaks
	// stripBucketPrefix to strip it, which only this import-time flatten path
	// can ever catch (a plain create+check assertion reads the plan's own
	// echoed value, not a flattened one - see the C12/C3-v2 comments in
	// cloud_config_flatten.go for why config blocks are never re-derived
	// outside of ImportState).
	resourcesJSON := fmt.Sprintf(`[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"compute_stack": "K8S", "region": "eastus",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "11111111-2222-3333-4444-555555555555",
			"zones": ["1", "2"],
			"redis_endpoint": "redis.ray-system.svc.cluster.local:6379"
		},
		"object_storage": {"bucket_name": %[1]q}
	}]`, bucket)

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_mock_default")
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "c3-azure-k8s-mock"
  cloud_provider = "AZURE"
  compute_stack  = "K8S"
  region         = "eastus"

  kubernetes_config {
    anyscale_operator_iam_identity = "11111111-2222-3333-4444-555555555555"
    zones                          = ["1", "2"]
    redis_endpoint                 = "redis.ray-system.svc.cluster.local:6379"
  }

  object_storage {
    bucket_name = %[1]q
  }

  azure_config {
    tenant_id = "66666666-7777-8888-9999-aaaaaaaaaaaa"
  }
}
`, bucket)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "cloud_provider", "AZURE"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "compute_stack", "K8S"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "object_storage.bucket_name", bucket),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "azure_config.tenant_id", "66666666-7777-8888-9999-aaaaaaaaaaaa"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "kubernetes_config.redis_endpoint", "redis.ray-system.svc.cluster.local:6379"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "cloud_resource_id", "cldrsrc_mock_default"),
				),
				// Headline gate, same as the AWS/GCP K8S lifecycle tests: a
				// config populated at create against a realistically-shaped
				// API response must not diff on the very next plan.
				ExpectNonEmptyPlan: false,
			},
			// ImportState is the ONLY path that exercises flattenObjectStorage/
			// stripBucketPrefix for a real API response (see the comment on
			// resourcesJSON above) - kubernetes_config and object_storage are
			// both asserted here (not ignored), so this is real round-trip
			// proof for the abfss:// passthrough and for redis_endpoint, not
			// just a plan-echo. azure_config is ignored deliberately: it is
			// optional, and C3-v2's requiredImportConfigBlocks only recovers
			// kubernetes_config+object_storage for K8S regardless of provider
			// (same as aws_config/gcp_config never being recovered for a K8S
			// cloud) - confirmed against the real requiredImportConfigBlocks
			// source, not assumed.
			{
				ResourceName:      "anyscale_cloud.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
					"azure_config", // optional, never recovered at import by design (C3-v2) - same treatment as aws_config/gcp_config for a K8S cloud
				},
			},
			// ImportStateVerify above only proves imported state matches
			// created state - both sides see the same API-echoed value - so
			// it cannot catch a defect where an operator-typed config value
			// diverges from state on the very next plan after import. This
			// step re-applies the SAME config used at create and asserts
			// that plan is a true no-op.
			//
			// Caveat confirmed empirically (not just inferred from docs):
			// this step's plan is computed against the CARRIED-FORWARD
			// state from the preceding real apply, not the freshly
			// imported state - terraform-plugin-testing discards an
			// ImportState step's result unless ImportStatePersist is also
			// set (see its doc comment), and setting ImportStatePersist:
			// true here to force it reproducibly fails with "Error:
			// Resource already managed by Terraform" (terraform import
			// refuses an address already present in the same working
			// directory's state, which it is here after the preceding
			// apply step) - a Terraform CLI-level constraint, not a
			// provider defect, and not fixable from within this test
			// shape. So this step, as added, does not independently prove
			// azure_config (ignored above because it's never recovered at
			// import) survives a real import unscathed - it does still
			// guard against any OTHER config/state divergence introduced
			// between create and this point. See
			// resource_cloud_import_object_storage_region_acc_test.go for
			// the two-test shape that actually proves import recovery
			// (ImportStateCheck on the import step itself, plus a separate
			// Config-only two-step test for plan stability).
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}
