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
// mutation-proof regression guard for design doc B2 (design doc:
// .crystl/quest/design/cloud-selector-design.md): before forge's fix,
// anyscale_cloud_resource had no validation requiring one of cloud_id or
// cloud_name, so omitting both silently sent a create request with an empty
// cloud instead of failing with a clear diagnostic.
//
// Confirmed failing-first (2026-07-25) against pre-B2 main. The mock
// deliberately has no real add_resource handler, only a catch-all that
// t.Errorf's and 500s, so this test's specific pre-fix failure mode is that
// catch-all firing on a genuine PUT /api/v2/clouds/add_resource with an
// empty cloud, surfaced as "API Request Failed ... unexpected status 500" -
// not the "Cloud Reference Required" diagnostic this test expects. That is
// the point: without the guard, Create() proceeds all the way to a real
// backend call with an empty cloud instead of stopping at plan-adjacent
// config validation, exactly what B2 describes ("sends a create request
// with an empty cloud instead of erroring"). A real backend might behave
// differently on an empty cloud_id (accept it, 400 it, or something else) -
// this test does not depend on that, it only needs to prove the provider
// itself never stops the request before it goes out.
//
// Confirmed passing against forge's landed fix (6b270c8), which adds an
// AddConfigError guard as the very first check on cloud_id/cloud_name in
// Create - before the cloud_name-resolution branch and before any HTTP call
// - matching the canonical error_helpers.go wording ("Cloud Reference
// Required" / "Either 'cloud_id' or 'cloud_name' must be specified...").
// Mutation-proven with the normal break-and-revert cycle on the fixed code,
// byte-diff clean.
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
				ExpectError: regexp.MustCompile(`(?s)Cloud Reference Required.*Either 'cloud_id' or 'cloud_name' must be specified`),
			},
		},
	})
}
