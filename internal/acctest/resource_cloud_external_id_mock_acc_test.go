package acctest

import (
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccCloudResource_NonOrgExternalIDRefusedAtPlan proves a create whose
// aws_config.external_id does not start with the organization ID fails at plan,
// before any create request. Create sends the value as the cloud's own
// external_id, and the API refuses that create.
func TestAccCloudResource_NonOrgExternalIDRefusedAtPlan(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newConvOrgCloudServer(t)
	config := strings.Replace(convOrgCloudConfig(server.URL, false),
		`external_id               = "org_real-external-id-ct"`,
		`external_id               = "not-an-org-external-id"`, 1)
	if !strings.Contains(config, "not-an-org-external-id") {
		t.Fatal("fixture no longer carries the external_id line this test rewrites")
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`Invalid aws_config\.external_id(?s).*must start with your\s+organization ID`),
		}},
	})

	if i := mock.firstIndex("POST /api/v2/clouds"); i != -1 {
		t.Errorf("a create request was sent (request #%d); the plan must fail first", i)
	}
}
