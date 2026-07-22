package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// mockSystemClusterServer is a stateful, states-advancing mock proving the real
// enable-then-start-then-poll Create flow (AC1-AC5), the two-call
// oracle+status Read (AC6), and the state-only Delete (AC10) through the real
// provider machinery - not just a client-level unit test. The describe
// sequence deliberately mirrors the real backend's observed live behavior
// (assayer's AC26 smoke test): the first describe(start_cluster=true) call
// returns Terminated immediately (StartingUp is genuinely async), then two
// more polls advance StartingUp -> Running, proving the resource actually
// loops rather than trusting a single call.
type mockSystemClusterServer struct {
	mu sync.Mutex

	cloudID string

	enableCalls        []bool
	describeStartCalls []bool // one entry per describe call, value = the start_cluster query param
	terminateCalled    bool

	describePollCount int32 // advances the state machine on each describe(start_cluster=false) call
}

func newMockSystemClusterServer(t *testing.T) (*httptest.Server, *mockSystemClusterServer) {
	t.Helper()
	s := &mockSystemClusterServer{cloudID: "cld_syscluster_lifecycle"}
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

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [{
				"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_syscluster_lifecycle",
				"compute_stack": "VM", "region": "us-east-2",
				"aws_config": {
					"vpc_id": "vpc-syscluster-lc", "subnet_ids": ["subnet-lc-1", "subnet-lc-2"],
					"zones": ["us-east-2a", "us-east-2b"], "security_group_ids": ["sg-lc-1"],
					"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/syscluster-lc-crossaccount",
					"cluster_iam_role_id": "arn:aws:iam::123456789012:role/syscluster-lc-cluster-node",
					"external_id": "syscluster-lc-external-id"
				},
				"object_storage": {"bucket_name": "s3://syscluster-lc-bucket"}
			}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {"cloud_resource_id": "cldrsrc_syscluster_lifecycle"}}`)
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	mux.HandleFunc("/api/v2/clouds/"+s.cloudID+"/update_system_cluster_config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected method %s on update_system_cluster_config", r.Method)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		enabled := r.URL.Query().Get("is_enabled") == "true"
		s.mu.Lock()
		s.enableCalls = append(s.enableCalls, enabled)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/v2/system_workload/"+s.cloudID+"/describe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on describe", r.Method)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		startCluster := r.URL.Query().Get("start_cluster") == "true"

		s.mu.Lock()
		s.describeStartCalls = append(s.describeStartCalls, startCluster)
		s.mu.Unlock()

		var status string
		if startCluster {
			// Real observed behavior (AC26 live smoke test): the create-time
			// start call returns Terminated immediately; StartingUp is async.
			status = "Terminated"
		} else {
			n := atomic.AddInt32(&s.describePollCount, 1)
			switch {
			case n == 1:
				status = "StartingUp"
			default:
				status = "Running"
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {
			"cluster_id": "cluster_syscluster_lifecycle",
			"workload_service_url": "https://syscluster-lifecycle.example.com",
			"workload_service_url_auth": null,
			"status": %q,
			"is_enabled": true
		}}`, status)
	})

	mux.HandleFunc("/api/v2/system_workload/"+s.cloudID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.terminateCalled = true
		s.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})

	mux.HandleFunc("/api/v2/decorated_sessions/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		s.mu.Lock()
		exists := len(s.describeStartCalls) > 0
		s.mu.Unlock()
		if !exists {
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{
			"results": [{
				"id": "cluster_syscluster_lifecycle",
				"cloud_id": %q,
				"is_system_cluster": true
			}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`, s.cloudID)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, s
}

func (s *mockSystemClusterServer) cloudJSON() string {
	return fmt.Sprintf(`{
		"id": %[1]q, "name": "syscluster-lifecycle-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM",
		"auto_add_user": false, "lineage_tracking_enabled": false, "is_aggregated_logs_enabled": false
	}`, s.cloudID)
}

func (s *mockSystemClusterServer) snapshot() (enableCalls, describeStartCalls []bool, terminateCalled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.enableCalls...), append([]bool(nil), s.describeStartCalls...), s.terminateCalled
}

const syscClusterBaseConfig = `
resource "anyscale_cloud" "test" {
  name           = "syscluster-lifecycle-mock"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"

  aws_config {
    vpc_id           = "vpc-syscluster-lc"
    subnet_ids_to_az = {
      "subnet-lc-1" = "us-east-2a"
      "subnet-lc-2" = "us-east-2b"
    }
    security_group_ids        = ["sg-lc-1"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/syscluster-lc-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/syscluster-lc-cluster-node"
    external_id               = "syscluster-lc-external-id"
  }

  object_storage {
    bucket_name = "syscluster-lc-bucket"
  }
}

resource "anyscale_system_cluster" "test" {
  cloud_id = anyscale_cloud.test.id
}
`

// TestAccSystemClusterResource_CreateEnablesStartsAndPollsToRunning covers
// AC1-AC5: Create must enable-then-start (never skip the enable call - AC4's
// mutation-proof requirement, see comment below), persist through the
// Terminated->StartingUp->Running progression, and a repeat apply must be a
// pure no-op (AC5) with no additional enable/start calls.
func TestAccSystemClusterResource_CreateEnablesStartsAndPollsToRunning(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, mockServer := newMockSystemClusterServer(t)
	const resourceAddr = "anyscale_system_cluster.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + syscClusterBaseConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "state", "Running"),
					resource.TestCheckResourceAttr(resourceAddr, "is_enabled", "true"),
					resource.TestCheckResourceAttr(resourceAddr, "cluster_id", "cluster_syscluster_lifecycle"),
					resource.TestCheckResourceAttr(resourceAddr, "workload_service_url", "https://syscluster-lifecycle.example.com"),
					resource.TestCheckResourceAttrPair(resourceAddr, "id", "anyscale_cloud.test", "id"),
				),
			},
			{
				// AC5: repeat apply is a pure no-op.
				Config: testAccProviderBlock(server.URL) + syscClusterBaseConfig,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceAddr, plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})

	enableCalls, describeStartCalls, terminateCalled := mockServer.snapshot()

	// AC4, MUTATION-PROOF: removing Create's enableSystemCluster call would
	// leave enableCalls empty here while describeStartCalls still populated -
	// this assertion fails loudly in that case, not just "cluster never
	// reaches Running" (which a broken enable-then-start ordering could still
	// technically stumble into if the mock were more permissive).
	if len(enableCalls) == 0 {
		t.Fatal("update_system_cluster_config was never called - Create must enable the cloud before starting it")
	}
	if !enableCalls[0] {
		t.Errorf("first enable call had is_enabled=%t, want true", enableCalls[0])
	}

	// The real start request (start_cluster=true) must happen, and only once
	// across the whole test (Create + the no-op re-apply) - a second apply
	// that changes nothing must never re-issue a start.
	startTrueCount := 0
	for _, sc := range describeStartCalls {
		if sc {
			startTrueCount++
		}
	}
	if startTrueCount != 1 {
		t.Errorf("describe was called with start_cluster=true %d times, want exactly 1 (Create only, never re-issued on the no-op re-apply)", startTrueCount)
	}

	// AC17-adjacent (resource-level correctness): every OTHER describe call
	// (the polls) must have passed start_cluster=false.
	for i, sc := range describeStartCalls {
		if i == 0 {
			continue // the one legitimate start_cluster=true call, asserted above
		}
		if sc {
			t.Errorf("describe call #%d had start_cluster=true, want false - only the initial Create request may request a start", i)
		}
	}

	if terminateCalled {
		t.Error("terminate was called during Create/re-apply - it must never be called outside an explicit terminate action, which this resource does not expose")
	}
}

// TestAccSystemClusterResource_DeleteIsStateOnly is the AC10 mutation-proof
// test: Delete must remove the resource from Terraform state without ever
// calling terminate. Asserting terminateCalled==false after a real
// resource.Test destroy cycle proves this through the actual Delete() code
// path, not just by reading the source.
func TestAccSystemClusterResource_DeleteIsStateOnly(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, mockServer := newMockSystemClusterServer(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + syscClusterBaseConfig,
				Check:  resource.TestCheckResourceAttr("anyscale_system_cluster.test", "state", "Running"),
			},
		},
		// The framework's own post-test destroy (an unconditional defer, see
		// tf-resource-test-unconditional-destroy-no-skip-hook) exercises
		// Delete for us - no extra step needed to trigger it.
	})

	_, _, terminateCalled := mockServer.snapshot()
	if terminateCalled {
		t.Error("Delete called terminate - it must be state-only and never touch the real System Cluster (AC10)")
	}
}

// TestAccSystemClusterResource_ImportByCloudID covers AC11: a create-then-
// import (not cold-import-only, so ImportStateVerify is meaningful - see
// tf-resource-test-unconditional-destroy-no-skip-hook's sibling guidance on
// import test shape) using cloud_id as the sole import identifier, followed
// by a no-op plan.
func TestAccSystemClusterResource_ImportByCloudID(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, _ := newMockSystemClusterServer(t)
	const resourceAddr = "anyscale_system_cluster.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + syscClusterBaseConfig,
				Check:  resource.TestCheckResourceAttr(resourceAddr, "state", "Running"),
			},
			{
				Config:            testAccProviderBlock(server.URL) + syscClusterBaseConfig,
				ResourceName:      resourceAddr,
				ImportState:       true,
				ImportStateVerify: true,
				// start_timeout is Optional+Computed+Default and purely local
				// (never sent to or read from the API - see its schema doc) -
				// ImportState has no server-side value to recover it from, so
				// it is re-seeded from the schema default on import rather
				// than compared byte-for-byte against the pre-import state.
				ImportStateVerifyIgnore: []string{"start_timeout"},
			},
			{
				Config: testAccProviderBlock(server.URL) + syscClusterBaseConfig,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceAddr, plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}
