package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccCloudResourceResource_BothNullCloudSelectorRejected was originally
// the mutation-proof regression guard for B2: before B2's
// fix (6b270c8), anyscale_cloud_resource had no validation requiring
// cloud_id or cloud_name, so omitting both silently sent a create request
// with an empty cloud instead of failing with a clear diagnostic. B2 added
// a runtime AddConfigError guard (Create()'s first check) to close that.
//
// R1 (the cloud_name removal) subsequently made cloud_id Required and
// deleted cloud_name from this resource entirely - confirmed by grep, B2's
// own AddConfigError guard code is gone, not just unreachable. Schema-level
// Required now enforces strictly more than B2's runtime check did: Core
// itself rejects a config omitting cloud_id at plan time, before the
// provider is ever invoked, which structurally cannot regress back to
// "silently sends an empty cloud" the way an ad-hoc runtime check could
// have. Retargeted this test onto that guarantee rather than deleting it -
// confirmed failing against a mutated schema (Required flipped back to
// Optional) and passing against the real one, byte-diff clean revert.
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
