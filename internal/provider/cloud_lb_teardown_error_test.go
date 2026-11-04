package provider

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// The teardown-timeout recovery names what to delete by hand, so it must name the
// resources of the cloud's own provider: AWS steps on a GCP cloud send the user looking
// for resources that do not exist.
func TestAddCloudLBTeardownError_RecoveryNamesProviderResources(t *testing.T) {
	cases := []struct {
		provider string
		want     []string
		notWant  []string
	}{
		{"AWS", []string{"control-plane IAM role.", "target groups and ACM certificate in AWS"}, []string{"GCP", "forwarding rule"}},
		{"GCP", []string{"control-plane service account.", "forwarding rule, target HTTPS proxy, URL map, backend service, health check, and Certificate Manager certificate and map in your GCP project"}, []string{"ACM", "in AWS"}},
		{"", []string{"IAM role or service account", "load balancer resources in your cloud account"}, []string{"ACM", "forwarding rule"}},
	}
	for _, tc := range cases {
		t.Run("provider="+tc.provider, func(t *testing.T) {
			var diags diag.Diagnostics
			addCloudLBTeardownError(&diags, "Cloud cld_x was not deleted", "this cloud", "cld_x", tc.provider,
				30*time.Minute, true, fmt.Errorf("%w", errCloudLBTeardownTimeout))
			if !diags.HasError() {
				t.Fatal("want an error")
			}
			d := diags.Errors()[0].Detail()
			for _, w := range append(tc.want, "terraform state rm on this cloud", "anyscale cloud delete --cloud-id cld_x", "contact Anyscale support with the cloud ID", "marked DELETING") {
				if !strings.Contains(d, w) {
					t.Errorf("detail %q does not contain %q", d, w)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(d, nw) {
					t.Errorf("detail %q must not contain %q", d, nw)
				}
			}
		})
	}
}
