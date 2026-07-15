package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// mockCloudCreateTimeServer is a stateful mock proving the Create-time mutable-field
// bug: a freshly created cloud always starts with auto_add_user/lineage_tracking_enabled/
// is_aggregated_logs_enabled false (traced against the real backend's CreateCloud Pydantic
// model, which declares the first two Field(False) and omits the third entirely - a fresh
// cloud has no log-ingestion config at all until the update endpoint below is called at
// least once). Before the fix, Create() never called that update endpoint, so config
// enable_log_ingestion = true would apply cleanly in the plan but the immediately-following
// readCloudState would read back the untouched false default, producing "Provider produced
// inconsistent result after apply" on the very first apply. This mock tracks whether the
// update endpoint was actually called and what value it was called with, not just whether
// the final state happens to look right - either signal on its own can be gamed, together
// they can't.
type mockCloudCreateTimeServer struct {
	mu                      sync.Mutex
	cloudID                 string
	isAggregatedLogsEnabled bool
	logIngestionPutCalled   bool
	logIngestionPutValue    bool
}

func newMockCloudCreateTimeServer(t *testing.T) (*httptest.Server, *mockCloudCreateTimeServer) {
	t.Helper()
	s := &mockCloudCreateTimeServer{cloudID: "cld_createtime_mock"}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		switch r.Method {
		case http.MethodPost:
			// A freshly created cloud always starts with is_aggregated_logs_enabled
			// false, regardless of what the Terraform config asks for - CreateCloud
			// has no field for it at all. Any non-default value MUST come from the
			// update endpoint below, never from this response.
			_, _ = fmt.Fprintf(w, `{"result": %s}`, s.cloudJSONLocked())
		case http.MethodGet:
			// findCloudByName's existing-cloud check: report none, so Create takes
			// the fresh-create path instead of adopting.
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds", r.Method)
		}
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, s.cloudJSONLocked())
		case http.MethodDelete:
			// resource.Test's own teardown always destroys, pass or fail.
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s", r.Method, s.cloudID)
		}
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/update_customer_aggregated_logs_config", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method != http.MethodPut {
			t.Errorf("unexpected method %s on update_customer_aggregated_logs_config", r.Method)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		enabled := r.URL.Query().Get("is_enabled") == "true"
		s.logIngestionPutCalled = true
		s.logIngestionPutValue = enabled
		s.isAggregatedLogsEnabled = enabled
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [{
				"name": "default", "is_default": true, "cloud_deployment_id": "cldrsrc_createtime_mock",
				"compute_stack": "VM", "region": "us-east-2",
				"aws_config": {
					"vpc_id": "vpc-realct123",
					"subnet_ids": ["subnet-realct1", "subnet-realct2"],
					"zones": ["us-east-2a", "us-east-2b"],
					"security_group_ids": ["sg-realct1"],
					"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/real-crossaccount",
					"cluster_iam_role_id": "arn:aws:iam::123456789012:role/real-cluster-node",
					"external_id": "real-external-id-ct"
				},
				"object_storage": {"bucket_name": "s3://real-ct-bucket"}
			}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {"cloud_deployment_id": "cldrsrc_createtime_mock"}}`)
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, s
}

// cloudJSONLocked builds the current cloud JSON body. Caller must hold s.mu.
func (s *mockCloudCreateTimeServer) cloudJSONLocked() string {
	return fmt.Sprintf(`{
		"id": %[1]q, "name": "createtime-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM",
		"auto_add_user": false, "lineage_tracking_enabled": false,
		"is_aggregated_logs_enabled": %[2]t
	}`, s.cloudID, s.isAggregatedLogsEnabled)
}

func (s *mockCloudCreateTimeServer) snapshot() (putCalled, putValue bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logIngestionPutCalled, s.logIngestionPutValue
}

// TestAccCloudResource_EnableLogIngestion_AppliesAtCreate is the fail-without-fix regression
// guard for the Create-time mutable-field bug: setting enable_log_ingestion = true in the
// very first apply must actually call the update endpoint during Create() (not just Update()),
// and the resulting state must stay consistent with what was configured. Before the fix,
// Create() never called updateMutableFields at all, so this mock (which always starts a fresh
// cloud at is_aggregated_logs_enabled=false, matching the real backend) would make Terraform
// Core reject the apply outright with "Provider produced inconsistent result after apply" -
// the exact class of bug the collaborator base_role fix was built around, on a different
// resource and field. Confirmed by reverting the fix locally and rerunning: this test goes red
// with a real inconsistent-result apply error, not just a failed assertion.
func TestAccCloudResource_EnableLogIngestion_AppliesAtCreate(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, mockServer := newMockCloudCreateTimeServer(t)
	const resourceAddr = "anyscale_cloud.test"

	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name                 = "createtime-mock"
  cloud_provider       = "AWS"
  compute_stack        = "VM"
  region               = "us-east-2"
  enable_log_ingestion = true

  aws_config {
    vpc_id             = "vpc-realct123"
    subnet_ids_to_az = {
      "subnet-realct1" = "us-east-2a"
      "subnet-realct2" = "us-east-2b"
    }
    security_group_ids        = ["sg-realct1"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/real-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/real-cluster-node"
    external_id               = "real-external-id-ct"
  }

  object_storage {
    bucket_name = "real-ct-bucket"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "enable_log_ingestion", "true"),
				),
				// The headline consistency gate: if Create() silently skipped applying
				// enable_log_ingestion, the immediately-following readCloudState would
				// have read back false, and Terraform Core itself would reject the
				// apply before this plan check ever ran - ExpectEmptyPlan here is a
				// secondary confirmation that nothing drifts on top of that.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})

	putCalled, putValue := mockServer.snapshot()
	if !putCalled {
		t.Fatal("update_customer_aggregated_logs_config was never called during Create - enable_log_ingestion = true in the initial config must apply at Create time, not only on a later Update")
	}
	if !putValue {
		t.Errorf("update_customer_aggregated_logs_config was called with is_enabled=false, want true to match the configured value")
	}
}
