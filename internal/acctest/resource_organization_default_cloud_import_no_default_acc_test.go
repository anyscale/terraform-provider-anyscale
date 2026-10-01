package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// When the organization has no explicit default cloud, the console and
// `anyscale cloud list` still show one cloud as default, a per-user fallback.
// A single GET /clouds/{id} reports is_default false for every cloud, and
// userinfo reports default_cloud_id null; both shapes were confirmed against
// the test organization. Importing that cloud must explain the fallback
// rather than tell the user to import a default that does not exist.
func newOrgNoDefaultCloudMockServer(t *testing.T, orgID, cloudID string, userinfoStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(userinfoStatus)
		if userinfoStatus != http.StatusOK {
			_, _ = fmt.Fprint(w, `{"error": {"detail": "mock upstream failure"}}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"result": {"organizations": [{"id": %q, "name": "mock-org", "public_identifier": "mock-org", "default_cloud_id": null}]}}`, orgID)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %q, "name": "mock-cloud", "provider": "AWS", "region": "us-east-2",
			"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": false
		}}`, cloudID)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestAccOrganizationDefaultCloudResource_ImportWithNoOrgDefault_MockServer(t *testing.T) {
	const orgID = "org_default_cloud_nodefault_mock"
	const cloudID = "cld_default_cloud_nodefault_mock"

	cases := []struct {
		name           string
		userinfoStatus int
		wantError      *regexp.Regexp
	}{
		{
			name:           "explains the per-user fallback",
			userinfoStatus: http.StatusOK,
			wantError:      regexp.MustCompile(`(?s)Organization\s+org_default_cloud_nodefault_mock\s+has\s+no\s+explicit\s+default\s+cloud.*per-user\s+fallback.*terraform\s+apply`),
		},
		{
			// If the organization cannot be read, the import still fails
			// with the plain message rather than an unrelated API error.
			name:           "falls back when userinfo fails",
			userinfoStatus: http.StatusInternalServerError,
			wantError:      regexp.MustCompile(`(?s)Cloud\s+"cld_default_cloud_nodefault_mock"\s+is\s+not\s+the\s+current\s+organization\s+default`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newOrgNoDefaultCloudMockServer(t, orgID, cloudID, tc.userinfoStatus)
			config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_organization_default_cloud" "test" {
  cloud_id = %q
}
`, cloudID)
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					ResourceName:  "anyscale_organization_default_cloud.test",
					ImportState:   true,
					ImportStateId: cloudID,
					Config:        config,
					ExpectError:   tc.wantError,
				}},
			})
		})
	}
}
