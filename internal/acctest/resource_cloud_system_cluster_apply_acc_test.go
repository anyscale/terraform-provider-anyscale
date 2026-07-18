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

// mockCloudSystemClusterServer is a stateful mock proving the write-only
// enable_system_cluster contract in updateMutableFields: unlike the other three
// cloud booleans, this field is Optional-only (never Computed, no safe read
// exists), so the null-vs-false distinction is load-bearing - null must mean
// "user never touched this" and skip the endpoint entirely, while an explicit
// true or false must always reach update_system_cluster_config and never the
// unrelated terminate route. This mock records every system-cluster PUT call in
// order (so a toggle sequence can be checked call-by-call, not just by final
// value) and separately tracks whether terminate was ever hit.
type mockCloudSystemClusterServer struct {
	mu                 sync.Mutex
	cloudID            string
	systemClusterCalls []bool
	terminateCalled    bool
}

func newMockCloudSystemClusterServer(t *testing.T) (*httptest.Server, *mockCloudSystemClusterServer) {
	t.Helper()
	s := &mockCloudSystemClusterServer{cloudID: "cld_syscluster_mock"}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.Method {
		case http.MethodPost:
			_, _ = fmt.Fprintf(w, `{"result": %s}`, s.cloudJSON())
		case http.MethodGet:
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds", r.Method)
		}
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, s.cloudJSON())
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s", r.Method, s.cloudID)
		}
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/update_system_cluster_config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected method %s on update_system_cluster_config", r.Method)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		enabled := r.URL.Query().Get("is_enabled") == "true"
		s.mu.Lock()
		s.systemClusterCalls = append(s.systemClusterCalls, enabled)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.terminateCalled = true
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [{
				"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_syscluster_mock",
				"compute_stack": "VM", "region": "us-east-2",
				"aws_config": {
					"vpc_id": "vpc-syscluster123",
					"subnet_ids": ["subnet-syscluster1", "subnet-syscluster2"],
					"zones": ["us-east-2a", "us-east-2b"],
					"security_group_ids": ["sg-syscluster1"],
					"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/syscluster-crossaccount",
					"cluster_iam_role_id": "arn:aws:iam::123456789012:role/syscluster-cluster-node",
					"external_id": "syscluster-external-id"
				},
				"object_storage": {"bucket_name": "s3://syscluster-bucket"}
			}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {"cloud_resource_id": "cldrsrc_syscluster_mock"}}`)
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, s
}

func (s *mockCloudSystemClusterServer) cloudJSON() string {
	return fmt.Sprintf(`{
		"id": %[1]q, "name": "syscluster-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM",
		"auto_add_user": false, "lineage_tracking_enabled": false, "is_aggregated_logs_enabled": false
	}`, s.cloudID)
}

func (s *mockCloudSystemClusterServer) snapshot() (calls []bool, terminateCalled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.systemClusterCalls...), s.terminateCalled
}

const syscBaseConfig = `
resource "anyscale_cloud" "test" {
  name           = "syscluster-mock"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"
%s
  aws_config {
    vpc_id           = "vpc-syscluster123"
    subnet_ids_to_az = {
      "subnet-syscluster1" = "us-east-2a"
      "subnet-syscluster2" = "us-east-2b"
    }
    security_group_ids        = ["sg-syscluster1"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/syscluster-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/syscluster-cluster-node"
    external_id               = "syscluster-external-id"
  }

  object_storage {
    bucket_name = "syscluster-bucket"
  }
}
`

// TestAccCloudResource_EnableSystemCluster_ToggleTrueThenFalse_AppliesAtCreateAndUpdate
// covers assertions 1 and 2 of the write-only contract: enable_system_cluster =
// true in the initial config must fire update_system_cluster_config?is_enabled=true
// during Create (same Create-time-apply class the sibling log-ingestion test
// guards), and flipping it to false on a later Update must fire ...is_enabled=false
// - never the unrelated terminate route, which is a heavier, async, real-cluster
// operation this config toggle must never trigger.
func TestAccCloudResource_EnableSystemCluster_ToggleTrueThenFalse_AppliesAtCreateAndUpdate(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, mockServer := newMockCloudSystemClusterServer(t)
	const resourceAddr = "anyscale_cloud.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + fmt.Sprintf(syscBaseConfig, "  enable_system_cluster = true\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "enable_system_cluster", "true"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				Config: testAccProviderBlock(server.URL) + fmt.Sprintf(syscBaseConfig, "  enable_system_cluster = false\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "enable_system_cluster", "false"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})

	calls, terminateCalled := mockServer.snapshot()
	if len(calls) != 2 {
		t.Fatalf("update_system_cluster_config called %d times, want exactly 2 (once at Create, once at Update)", len(calls))
	}
	if !calls[0] {
		t.Errorf("first call (Create) had is_enabled=%t, want true", calls[0])
	}
	if calls[1] {
		t.Errorf("second call (Update) had is_enabled=%t, want false", calls[1])
	}
	if terminateCalled {
		t.Error("terminate route was called when toggling enable_system_cluster to false - disabling must only ever call update_system_cluster_config, never terminate")
	}
}

// TestAccCloudResource_EnableSystemCluster_Unconfigured_NeverCallsEndpoint is a
// basic sanity check for a cloud whose config never sets enable_system_cluster at
// all: state must stay null and the endpoint must never be called. Note this
// case alone is NOT a distinguishing test for the null-guard in
// updateMutableFields - at Create, the zero-value baseline itself is also null
// (types.BoolNull()), so plan.Equal(state) already evaluates true and skips the
// call even without the explicit IsNull() check. See the Removed-after-being-set
// test below for the scenario that actually exercises that guard.
func TestAccCloudResource_EnableSystemCluster_Unconfigured_NeverCallsEndpoint(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, mockServer := newMockCloudSystemClusterServer(t)
	const resourceAddr = "anyscale_cloud.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + fmt.Sprintf(syscBaseConfig, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceAddr, "enable_system_cluster"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})

	calls, terminateCalled := mockServer.snapshot()
	if len(calls) != 0 {
		t.Fatalf("update_system_cluster_config called %d times with an unconfigured field, want 0 - null must never be coerced into an explicit call", len(calls))
	}
	if terminateCalled {
		t.Error("terminate route was called for an unconfigured enable_system_cluster - it should never be called at all here")
	}
}

// TestAccCloudResource_EnableSystemCluster_RemovedAfterBeingSet_NeverCallsEndpoint
// is the actual distinguishing test for the null-guard in updateMutableFields
// (assertion 3, the one architect flagged as most worth locking): after
// enable_system_cluster has been explicitly set, removing it from config entirely
// must leave the endpoint uncalled and the value simply null, NOT fire an
// implicit disable. Unlike the fresh-Create case above, the prior state here is
// non-null (true from step 1), so plan.Equal(state) alone would NOT skip the
// call - only the explicit !plan.EnableSystemCluster.IsNull() clause does. If
// that clause were removed, plan.EnableSystemCluster.ValueBool() on a null Bool
// returns false with no error, and the endpoint would be wrongly called with
// is_enabled=false, silently disabling something the user simply stopped
// mentioning rather than asked to turn off.
func TestAccCloudResource_EnableSystemCluster_RemovedAfterBeingSet_NeverCallsEndpoint(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, mockServer := newMockCloudSystemClusterServer(t)
	const resourceAddr = "anyscale_cloud.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + fmt.Sprintf(syscBaseConfig, "  enable_system_cluster = true\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "enable_system_cluster", "true"),
				),
			},
			{
				Config: testAccProviderBlock(server.URL) + fmt.Sprintf(syscBaseConfig, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceAddr, "enable_system_cluster"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})

	calls, terminateCalled := mockServer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("update_system_cluster_config called %d times across create-true-then-remove-from-config, want exactly 1 (only the initial true at Create) - removing the field from config must never itself fire a call", len(calls))
	}
	if !calls[0] {
		t.Errorf("the one recorded call had is_enabled=%t, want true (from the initial Create) - a later call with false would mean removal was wrongly treated as an explicit disable", calls[0])
	}
	if terminateCalled {
		t.Error("terminate route was called after removing enable_system_cluster from config - it should never be called at all here")
	}
}
