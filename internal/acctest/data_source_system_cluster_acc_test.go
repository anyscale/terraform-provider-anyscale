package acctest

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccSystemClusterDataSource_ReturnsObservedState covers AC20's "observed
// state" half: once a System Cluster is running (via the resource in the
// same config), the data source pointed at the same cloud_id must reflect
// that real state - not just echo config back.
func TestAccSystemClusterDataSource_ReturnsObservedState(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, _ := newMockSystemClusterServer(t)
	const dsAddr = "data.anyscale_system_cluster.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + syscClusterBaseConfig + `
data "anyscale_system_cluster" "test" {
  cloud_id = anyscale_cloud.test.id
  depends_on = [anyscale_system_cluster.test]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(dsAddr, "state", "Running"),
					resource.TestCheckResourceAttr(dsAddr, "is_enabled", "true"),
					resource.TestCheckResourceAttr(dsAddr, "cluster_id", "cluster_syscluster_lifecycle"),
					resource.TestCheckResourceAttr(dsAddr, "workload_service_url", "https://syscluster-lifecycle.example.com"),
					resource.TestCheckResourceAttrPair(dsAddr, "cloud_id", "anyscale_cloud.test", "id"),
				),
			},
		},
	})
}

// TestAccSystemClusterDataSource_NotConfiguredReturnsCleanNull covers AC20's
// other two halves at once: a cloud with no System Cluster yet returns clean
// null computed fields (not an error), and doing so is provably
// side-effect-free - the mock's describe endpoint must never be hit at all
// (regardless of start_cluster), only the existence oracle. If the data
// source's Read ever called describeSystemWorkload before confirming
// existence, this would risk the create-on-read hazard the whole two-call
// design exists to avoid; asserting zero describe calls here is the
// mutation-relevant check, not just "no error was returned."
func TestAccSystemClusterDataSource_NotConfiguredReturnsCleanNull(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, mockServer := newMockSystemClusterServer(t)
	const dsAddr = "data.anyscale_system_cluster.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Deliberately no anyscale_system_cluster resource in this
				// config - only the cloud exists, never enabled/started.
				Config: testAccProviderBlock(server.URL) + `
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

data "anyscale_system_cluster" "test" {
  cloud_id = anyscale_cloud.test.id
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(dsAddr, "cluster_id"),
					resource.TestCheckNoResourceAttr(dsAddr, "state"),
					resource.TestCheckNoResourceAttr(dsAddr, "is_enabled"),
					resource.TestCheckNoResourceAttr(dsAddr, "workload_service_url"),
				),
			},
		},
	})

	_, describeStartCalls, _ := mockServer.snapshot()
	if len(describeStartCalls) != 0 {
		t.Fatalf("describe was called %d times for a cloud with no System Cluster configured - the data source must confirm existence first and never call describe at all in this case (create-on-read hazard)", len(describeStartCalls))
	}
}
