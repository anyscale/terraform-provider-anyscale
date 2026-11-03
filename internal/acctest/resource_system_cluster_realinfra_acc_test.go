package acctest

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

// systemClusterTestCloudEnv names the dedicated cloud the system cluster real-infra test runs
// on. Create enables the cloud's system cluster for good (Delete only removes it from state), so
// the test never runs on a resolved or shared cloud.
const systemClusterTestCloudEnv = "ANYSCALE_TEST_SYSTEM_CLUSTER_CLOUD_ID"

// TestAccSystemClusterResource_RealInfra runs anyscale_system_cluster against a real cloud:
// create starts the cluster and waits for Running; the next plan is empty; import by cloud_id
// recovers the same state; the anyscale_system_cluster data source reads it back; destroy
// leaves the cluster enabled, which is the resource's documented contract.
//
// On a Kubernetes cloud the backend serves system clusters only behind a feature flag and
// answers 501 when it is off. There the test accepts that diagnostic from create as the
// outcome, and logs it.
//
// Requires ANYSCALE_TEST_SYSTEM_CLUSTER_CLOUD_ID: a dedicated cloud with a cloud resource,
// never the static fixture. A cleanup terminates the cluster so it does not keep running; the
// cloud is left enabled with a terminated cluster.
//
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 ANYSCALE_TEST_SYSTEM_CLUSTER_CLOUD_ID=<cloud id> \
//	  go test ./internal/acctest/ -run '^TestAccSystemClusterResource_RealInfra$' -v -timeout 60m
func TestAccSystemClusterResource_RealInfra(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)
	cloud := requireSystemClusterTestCloud(t)
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("failed to get test client: %v", err)
	}
	t.Cleanup(func() { terminateSystemClusterForCleanup(t, client, cloud.ID) })

	const addr = "anyscale_system_cluster.test"
	config := fmt.Sprintf(`
resource "anyscale_system_cluster" "test" {
  cloud_id = %q

  timeouts {
    create = "30m"
  }
}
`, cloud.ID)
	withDataSource := config + `
data "anyscale_system_cluster" "test" {
  cloud_id = anyscale_system_cluster.test.cloud_id
}
`
	notImplemented := regexp.MustCompile(`501`)
	var createRejected bool

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// Only a Kubernetes cloud may reject create, and only with the 501. Every later step is
		// skipped then: there is nothing to plan, import or read.
		ErrorCheck: func(err error) error {
			if err != nil && cloud.IsK8s() && notImplemented.MatchString(err.Error()) {
				createRejected = true
				t.Logf("%s cloud %s: system clusters are not enabled for Kubernetes (501): %v", cloud.Platform(), cloud.Name, err)
				return nil
			}
			return err
		},
		CheckDestroy: func(*terraform.State) error {
			if createRejected {
				return nil
			}
			return checkSystemClusterStillEnabled(client, cloud.ID)
		},
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cloud_id", cloud.ID),
					resource.TestCheckResourceAttr(addr, "state", "Running"),
					resource.TestCheckResourceAttr(addr, "is_enabled", "true"),
					resource.TestCheckResourceAttrSet(addr, "cluster_id"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				SkipFunc:                func() (bool, error) { return createRejected, nil },
				ResourceName:            addr,
				ImportState:             true,
				ImportStateId:           cloud.ID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				SkipFunc: func() (bool, error) { return createRejected, nil },
				Config:   withDataSource,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.anyscale_system_cluster.test", "cluster_id", addr, "cluster_id"),
					resource.TestCheckResourceAttr("data.anyscale_system_cluster.test", "state", "Running"),
					resource.TestCheckResourceAttr("data.anyscale_system_cluster.test", "is_enabled", "true"),
				),
			},
		},
	})
}

// requireSystemClusterTestCloud returns the dedicated cloud named by systemClusterTestCloudEnv,
// or skips when it is unset. It refuses the static fixture by name and by ID.
func requireSystemClusterTestCloud(t *testing.T) CloudInfo {
	t.Helper()
	id := os.Getenv(systemClusterTestCloudEnv)
	if id == "" {
		t.Skip(systemClusterTestCloudEnv + " not set: create enables the cloud's system cluster for good, so this " +
			"test needs an explicitly chosen dedicated cloud and never falls back to a resolved or shared one")
	}
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("failed to get test client: %v", err)
	}
	cloud, err := configuredCloudByID(client, id)
	if err != nil {
		t.Fatalf("%s=%s: %v", systemClusterTestCloudEnv, id, err)
	}
	if cloud.Name == defaultKnownGoodCloudName {
		t.Fatalf("%s points at the shared fixture %q; use a dedicated cloud", systemClusterTestCloudEnv, cloud.Name)
	}
	if staticID, err := resolveCloudNameToID(t, defaultKnownGoodCloudName); err == nil && staticID == id {
		t.Fatalf("%s is the shared fixture's ID; use a dedicated cloud", systemClusterTestCloudEnv)
	}
	return cloud
}

// checkSystemClusterStillEnabled asserts that destroy left the cloud's system cluster enabled.
// The describe call is safe here: the cluster already exists, so start_cluster=false cannot
// create one.
func checkSystemClusterStillEnabled(client *provider.Client, cloudID string) error {
	resp, err := provider.DoRequestAndParse[struct {
		Result struct {
			IsEnabled bool `json:"is_enabled"`
		} `json:"result"`
	}](context.Background(), client, "POST",
		fmt.Sprintf("/api/v2/system_workload/%s/describe?workload_name=RAY_OBS_EVENTS_API_SERVICE&start_cluster=false", cloudID),
		nil, http.StatusOK)
	if err != nil {
		return fmt.Errorf("describe system cluster after destroy: %w", err)
	}
	if !resp.Result.IsEnabled {
		return fmt.Errorf("system cluster on %s is disabled after destroy; destroy must only remove it from state", cloudID)
	}
	return nil
}

// terminateSystemClusterForCleanup stops the cluster the test started, as `anyscale cloud
// terminate-system-cluster` does. 404 (none was created) and 409 (already terminated) are fine.
func terminateSystemClusterForCleanup(t *testing.T, client *provider.Client, cloudID string) {
	resp, err := client.DoRequest(context.Background(), "POST", fmt.Sprintf("/api/v2/system_workload/%s/terminate", cloudID), nil)
	if err != nil {
		t.Errorf("cleanup: terminate system cluster on %s: %v", cloudID, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusOK, http.StatusNotFound, http.StatusConflict:
		t.Logf("cleanup: terminate system cluster on %s: HTTP %d", cloudID, resp.StatusCode)
	default:
		t.Errorf("cleanup: terminate system cluster on %s: HTTP %d; stop it by hand", cloudID, resp.StatusCode)
	}
}
