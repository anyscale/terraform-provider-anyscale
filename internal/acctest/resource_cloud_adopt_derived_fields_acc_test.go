package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// Adopting an existing cloud by name must resolve the Computed slots the
// backend derives (memorydb_cluster_arn/endpoint from memorydb_cluster_name,
// mount_targets and mount_path from file_storage_id) when the config omits
// them. Left unknown, the apply failed with "Provider returned invalid result
// object after apply". The mock's resource listing carries the derived values
// the way the backend returns them after deriving and persisting them.

const adoptDerivedCloudID = "cld_adopt_derived_mock"

func adoptDerivedServer(t *testing.T, defaultResourceJSON string) *httptest.Server {
	t.Helper()
	cloudJSON := fmt.Sprintf(`{"id": %q, "name": "adopt-derived", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"}`, adoptDerivedCloudID)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			t.Errorf("unexpected POST /api/v2/clouds: the cloud must be adopted, not created")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, `{"results": [%s], "metadata": {"total": 1, "next_paging_token": null}}`, cloudJSON)
	})
	mux.HandleFunc("/api/v2/clouds/"+adoptDerivedCloudID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
	})
	mux.HandleFunc("/api/v2/clouds/"+adoptDerivedCloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"results": [%s], "metadata": {"total": 1, "next_paging_token": null}}`, defaultResourceJSON)
	})
	mux.HandleFunc("/api/v2/machine_pools/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"result": {"machine_pools": []}}`)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func adoptDerivedConfig(url, awsExtra, fileStorage string) string {
	return testAccProviderBlock(url) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "adopt-derived"
  cloud_provider = "AWS"
  region         = "us-east-2"
  compute_stack  = "VM"

  aws_config {
    vpc_id             = "vpc-adopt"
    subnet_ids         = ["subnet-a"]
    security_group_ids = ["sg-adopt"]
    %s
  }

  object_storage {
    bucket_name = "s3://adopt-bucket"
  }
  %s
}
`, awsExtra, fileStorage)
}

func TestAccCloudResource_AdoptByNameResolvesDerivedFields(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	const arn = "arn:aws:memorydb:us-east-2:123456789012:cluster/adopt-memdb"
	const endpoint = "adopt-memdb.abc.clustercfg.memorydb.us-east-2.amazonaws.com:6379"
	server := adoptDerivedServer(t, fmt.Sprintf(`{"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_adopt_derived",
		"provider": "AWS", "compute_stack": "VM", "region": "us-east-2",
		"aws_config": {"vpc_id": "vpc-adopt", "subnet_ids": ["subnet-a"], "zones": ["us-east-2a"], "security_group_ids": ["sg-adopt"],
			"memorydb_cluster_name": "adopt-memdb", "memorydb_cluster_arn": %q, "memorydb_cluster_endpoint": %q},
		"object_storage": {"bucket_name": "s3://adopt-bucket"},
		"file_storage": {"file_storage_id": "fs-adopt", "mount_path": "/mnt/shared",
			"mount_targets": [{"address": "10.0.0.5", "zone": "us-east-2a"}]}}`, arn, endpoint))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: adoptDerivedConfig(server.URL, `memorydb_cluster_name = "adopt-memdb"`, `
  file_storage {
    file_storage_id = "fs-adopt"
  }`),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionCreate)},
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("anyscale_cloud.test", "id", adoptDerivedCloudID),
				resource.TestCheckResourceAttr("anyscale_cloud.test", "aws_config.memorydb_cluster_arn", arn),
				resource.TestCheckResourceAttr("anyscale_cloud.test", "aws_config.memorydb_cluster_endpoint", endpoint),
				resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.mount_targets.#", "1"),
				resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.mount_targets.0.address", "10.0.0.5"),
				resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.mount_path", "/mnt/shared"),
			),
		}},
	})
}

// When the adopted cloud's resource carries no derived values, the slots
// resolve to null rather than staying unknown.
func TestAccCloudResource_AdoptByNameWithoutDerivedFieldsResolvesNull(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server := adoptDerivedServer(t, `{"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_adopt_derived",
		"provider": "AWS", "compute_stack": "VM", "region": "us-east-2",
		"aws_config": {"vpc_id": "vpc-adopt", "subnet_ids": ["subnet-a"], "zones": ["us-east-2a"], "security_group_ids": ["sg-adopt"]},
		"object_storage": {"bucket_name": "s3://adopt-bucket"}}`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: adoptDerivedConfig(server.URL, "", ""),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("anyscale_cloud.test", "id", adoptDerivedCloudID),
				resource.TestCheckNoResourceAttr("anyscale_cloud.test", "aws_config.memorydb_cluster_arn"),
				resource.TestCheckNoResourceAttr("anyscale_cloud.test", "aws_config.memorydb_cluster_endpoint"),
			),
		}},
	})
}
