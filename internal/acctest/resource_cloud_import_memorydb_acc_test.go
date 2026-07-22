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
// replace-on-import bug (Import Round-Trip Gaps, HIGH item, architect review
// 2026-07-21). Both fields are backend-derived from memorydb_cluster_name
// (product backend _populate_missing_derived_values) whenever the user
// leaves them unset, exactly like mount_targets - but unlike mount_targets
// they are plain schema.StringAttribute (Optional, RequiresReplace, NOT
// Computed today), not a Block, so today's flattenAWSConfig recovers them
// VERBATIM at import into a slot Create/Read never populate.
//
// Mirrors the customer-shape config: memorydb_cluster_name only, arn/
// endpoint both omitted - the exact input this fix targets.
//
// Step 1 creates and captures real applied state. Since aws_config is not
// Computed, Create/Read never touch it from the API (C3-v2) - a config that
// omits arn/endpoint MUST leave them null in state today, regardless of
// what the backend derived and would return, because nothing in the
// current Create path ever reads AWSConfig back out of add_resource's
// response (confirmed by tracing resource_cloud.go directly - only
// CloudResourceID is extracted from it) or out of readCloudState's
// listCloudResources call (backfillComputedCloudFields only ever touches
// is_empty_cloud/cloud_resource_id, by the same C3-v2 discipline that keeps
// config blocks out of Read).
//
// Step 2 imports the SAME cloud from a mock whose API responses (both
// add_resource and the resources listing) DO carry real derived arn/
// endpoint values, simulating a live registered cloud. ImportStateVerify
// compares against step 1's state with memorydb_cluster_arn/endpoint
// deliberately NOT in ImportStateVerifyIgnore - proving whether recovery
// is consistent with create.
//
// Step 3 re-applies the same name-only config and asserts the plan is a
// no-op - the literal replace-on-import bar this fix exists to clear.
//
// Today (pre-Path-A) this test does not reach step 3 clean: step 1 leaves
// both fields null, step 2 recovers real non-null values, so
// ImportStateVerify itself reports the mismatch - proof the create-path
// and import-path disagree on the exact same live cloud before either
// hits the replace-loop. Once Path A's Optional+Computed+UseStateForUnknown
// fix (plus the Create/Update merge from add_resource/resources-list) is
// in place, step 1 must ALSO produce the real values (not null), making
// steps 1-3 agree end-to-end.
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
