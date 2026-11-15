package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccCloudResourceResource_BothNullCloudSelectorRejected is the
// mutation-proof regression guard proving that declaring neither cloud_id
// nor cloud_name fails with a clear diagnostic instead of silently sending
// a create request with an empty cloud. Required now enforces strictly
// more than the old runtime check did: Core rejects the plan up front,
// before the provider is ever invoked.
func TestAccCloudResourceResource_BothNullCloudSelectorRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s - the both-null guard must reject this config before any backend call", r.Method, r.URL.String())
		w.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud_resource" "test" {
  name          = %[1]q
  compute_stack = "VM"

  aws_config {
    vpc_id = "vpc-both-null-guard"
    subnet_ids_to_az = {
      "subnet-both-null-guard" = "us-east-2a"
    }
    security_group_ids        = ["sg-both-null-guard"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/both-null-guard-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/both-null-guard-cluster-node"
    external_id               = "both-null-guard-external-id"
  }

  object_storage {
    bucket_name = "bucket-both-null-guard"
  }
}
`, "both-null-guard-test")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)Missing required argument.*"cloud_id" is required`),
			},
		},
	})
}
