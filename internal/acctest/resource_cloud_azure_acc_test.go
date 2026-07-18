package acctest

import (
	"fmt"
	"os"
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

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON)
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
		},
	})
}

// requiredAzureEnv resolves the three env vars a real AKS creation needs and
// are not safe to fabricate the way the mock test above does (a real tenant
// ID, a real federated operator identity, and a real, reachable Storage
// account container) - unlike ANYSCALE_TEST_CLOUD_NAME's single override,
// there is no way to auto-discover or default any of these, so all three (or
// none) is the only sensible contract.
func requiredAzureEnv(t *testing.T) (tenantID, operatorIdentity, bucket string, ok bool) {
	t.Helper()
	tenantID = os.Getenv("ANYSCALE_TEST_AZURE_TENANT_ID")
	operatorIdentity = os.Getenv("ANYSCALE_TEST_AZURE_OPERATOR_IDENTITY")
	bucket = os.Getenv("ANYSCALE_TEST_AZURE_BUCKET")
	if tenantID == "" || operatorIdentity == "" || bucket == "" {
		return "", "", "", false
	}
	return tenantID, operatorIdentity, bucket, true
}

// TestAccCloudResource_AzureK8S_RealInfra is the real-Azure counterpart to
// TestAccCloudResource_Lifecycle_AzureK8S_MockServer above, following the
// same opt-in pattern as ANYSCALE_TEST_CLOUD_NAME (CLAUDE.md "Test cloud
// selection"): it is SKIPPED cleanly whenever real Azure credentials are not
// configured (true today - no Azure subscription exists in this repo's test
// org, per K8S-CLOUD-CONTRACT.md's AKS section), and runs a real
// create -> apply -> plan-empty -> import -> plan-empty lifecycle against the
// real Anyscale API the moment someone sets all three env vars below. This is
// what makes the AKS implementation's real-infra coverage genuinely "runs
// automatically if/when Azure creds exist" rather than a permanent TODO that
// bit-rots unnoticed.
//
// Required env vars (all three, or the test skips):
//   - ANYSCALE_TEST_AZURE_TENANT_ID: a real Azure AD tenant ID.
//   - ANYSCALE_TEST_AZURE_OPERATOR_IDENTITY: the managed identity's principal
//     ID, already federated for workload identity against a real AKS cluster
//     with the Anyscale operator installed (see docs.anyscale.com/clouds/azure/create-aks).
//   - ANYSCALE_TEST_AZURE_BUCKET: a full abfss://container@account.dfs.core.windows.net
//     URI for a real, reachable Azure Storage account container.
func TestAccCloudResource_AzureK8S_RealInfra(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	tenantID, operatorIdentity, bucket, ok := requiredAzureEnv(t)
	if !ok {
		t.Skip("SKIP(no-real-azure): requires a real Azure AD tenant + a federated operator " +
			"identity + a reachable Storage account container; not available in this org today. " +
			"Set ANYSCALE_TEST_AZURE_TENANT_ID, ANYSCALE_TEST_AZURE_OPERATOR_IDENTITY, and " +
			"ANYSCALE_TEST_AZURE_BUCKET to run this for real once Azure infra exists.")
	}

	cloudName := UniqueName(t, "cloud-azure-k8s")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = %[1]q
  cloud_provider = "AZURE"
  compute_stack  = "K8S"
  region         = "eastus"

  kubernetes_config {
    anyscale_operator_iam_identity = %[2]q
  }

  object_storage {
    bucket_name = %[3]q
  }

  azure_config {
    tenant_id = %[4]q
  }
}
`, cloudName, operatorIdentity, bucket, tenantID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "name", cloudName),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "cloud_provider", "AZURE"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "compute_stack", "K8S"),
					resource.TestCheckResourceAttrSet("anyscale_cloud.test", "id"),
					testAccCheckCloudExistsInAPI("anyscale_cloud.test"),
					testAccCheckCloudAttributes("anyscale_cloud.test", cloudName, "AZURE", "eastus"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				ResourceName:      "anyscale_cloud.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
					"azure_config", // optional, never recovered at import by design (C3-v2)
				},
			},
		},
	})
}
