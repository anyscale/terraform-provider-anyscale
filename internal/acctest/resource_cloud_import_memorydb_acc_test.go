package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// newMemoryDBImportMockCloudServer is a dedicated mock for the
// memorydb_cluster_arn/memorydb_cluster_endpoint replace-on-import gap
// (Import Round-Trip Gaps, HIGH). Unlike newC3MockCloudServer (whose
// add_resource handler returns only cloud_deployment_id/cloud_resource_id),
// this mock returns a REALISTIC full aws_config - including the two
// backend-derived fields - from BOTH add_resource and the
// /clouds/{id}/resources listing, matching how the real backend actually
// behaves (it has no notion of "which endpoint the provider reads its
// state from", so both legitimately echo the same underlying resource). A
// planned Path A fix may source its Create/Update merge from either
// endpoint, so this mock stays valid regardless of which one lands.
func newMemoryDBImportMockCloudServer(t *testing.T, cloudID, cloudJSON, resourcesJSON, awsConfigJSON, cloudResourceID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.Method {
		case http.MethodPost:
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		case http.MethodGet:
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds", r.Method)
		}
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s", r.Method, cloudID)
		}
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results": %s, "metadata": {"total": 1, "next_paging_token": null}}`, resourcesJSON)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Deliberately realistic, not the minimal shape newC3MockCloudServer
		// returns: a real add_resource response carries the full resource,
		// aws_config included - a Path A merge sourced directly from this
		// response (rather than the resources-list) must see the same data.
		_, _ = fmt.Fprintf(w, `{"result": {"cloud_deployment_id": "cldrsrc_memorydb_mock_default", "cloud_resource_id": %q, "aws_config": %s}}`, cloudResourceID, awsConfigJSON)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccCloudResource_MemoryDBFieldsRecoverOnImport_AWSVM is the fail-first
// regression test for the memorydb_cluster_arn/memorydb_cluster_endpoint
// replace-on-import bug (Import Round-Trip Gaps, HIGH item). Both fields
// are backend-derived from memorydb_cluster_name when left unset - like
// mount_targets, but as plain schema.StringAttribute (not Computed today),
// so flattenAWSConfig recovers them at import into a slot Create/Read
// never populate.
//
// Today (pre-Path-A): Create leaves both fields null (nothing reads
// AWSConfig back out of add_resource's response or the resources-list -
// confirmed by tracing resource_cloud.go directly), while import recovers
// the real values - so ImportStateVerify (step 2 below) catches the
// mismatch before the replace-loop (step 3) is even reached. Once Path A
// lands (Optional+Computed+UseStateForUnknown, plus the Create/Update
// merge), step 1 produces the real values too, and all 3 steps go green
// unchanged.
func TestAccCloudResource_MemoryDBFieldsRecoverOnImport_AWSVM(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_memorydb_import_aws_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "memorydb-import-aws-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": false,
		"is_private_cloud": true
	}`, cloudID)

	// The realistic backend-derived shape: config sets only
	// memorydb_cluster_name, the backend fills arn+endpoint. Deliberately
	// NOT idealized/echoed - a mock that just replays the config would
	// prove nothing here.
	awsConfigJSON := `{
		"vpc_id": "vpc-memorydbtest",
		"subnet_ids": ["subnet-memorydbtest1", "subnet-memorydbtest2"],
		"zones": ["us-east-2a", "us-east-2b"],
		"security_group_ids": ["sg-memorydbtest"],
		"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/memorydbtest-crossaccount",
		"cluster_iam_role_id": "arn:aws:iam::123456789012:role/memorydbtest-cluster-node",
		"external_id": "memorydbtest-external-id",
		"memorydb_cluster_name": "memorydbtest-cluster",
		"memorydb_cluster_arn": "arn:aws:memorydb:us-east-2:123456789012:cluster/memorydbtest-cluster",
		"memorydb_cluster_endpoint": "memorydbtest-cluster.abc123.clustercfg.memorydb.us-east-2.amazonaws.com:6379"
	}`
	resourcesJSON := fmt.Sprintf(`[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_memorydb_mock_default",
		"compute_stack": "VM", "region": "us-east-2",
		"aws_config": %s
	}]`, awsConfigJSON)

	server := newMemoryDBImportMockCloudServer(t, cloudID, cloudJSON, resourcesJSON, awsConfigJSON, "cldrsrc_memorydb_mock_default")
	resourceName := "anyscale_cloud.test"
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name             = "memorydb-import-aws-mock"
  cloud_provider   = "AWS"
  compute_stack    = "VM"
  region           = "us-east-2"
  is_private_cloud = true

  aws_config {
    vpc_id            = "vpc-memorydbtest"
    subnet_ids_to_az = {
      "subnet-memorydbtest1" = "us-east-2a"
      "subnet-memorydbtest2" = "us-east-2b"
    }
    security_group_ids        = ["sg-memorydbtest"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/memorydbtest-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/memorydbtest-cluster-node"
    external_id               = "memorydbtest-external-id"

    # The exact bug-report shape: name set, arn/endpoint left for the
    # backend to derive.
    memorydb_cluster_name = "memorydbtest-cluster"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Establish real applied state. Pre-fix: memorydb_cluster_arn/
				// endpoint are null here (Create never reads them back),
				// regardless of what the mock's add_resource/resources
				// responses carry.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "aws_config.memorydb_cluster_name", "memorydbtest-cluster"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				// THE regression proof: recovered arn/endpoint must match
				// step 1's state exactly. Pre-fix, they do not (step 1 is
				// null, import recovers the mock's real values) - this step
				// is expected to fail today via ImportStateVerify, not via a
				// plan-time replace.
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
				},
			},
			{
				// The literal bug bar: a config matching the live cloud
				// must plan as a no-op, never a replace.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}
