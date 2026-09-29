package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// newOrgDefaultCloudErrorStatusMockServer serves the endpoints
// anyscale_organization_default_cloud calls. GET /api/v2/clouds/{id} answers
// with *failStatus (and an error body) whenever it is non-zero, and otherwise
// reports cloudID as the organization default.
//
// The error body is deliberately valid JSON with no is_default field: that is
// what a real 5xx/401/403 looks like, and it is exactly the shape that decodes
// to is_default=false if the provider ignores the status code.
func newOrgDefaultCloudErrorStatusMockServer(t *testing.T, orgID, cloudID string, failStatus *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {"organizations": [{"id": %[1]q, "name": "mock-org", "public_identifier": "mock-org", "default_cloud_id": %[2]q}]}}`, orgID, cloudID)
	})

	cloudHandler := func(w http.ResponseWriter, r *http.Request) {
		if status := int(failStatus.Load()); status != 0 {
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "mock upstream failure"}}`)
			return
		}
		if strings.TrimPrefix(r.URL.Path, "/api/v2/clouds/") != cloudID {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "cloud not found"}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "name": "mock-cloud", "provider": "AWS", "region": "us-east-2",
			"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": true
		}}`, cloudID)
	}
	mux.HandleFunc("/api/v2/clouds/", cloudHandler)
	mux.HandleFunc("/api/v2/clouds", cloudHandler)

	mux.HandleFunc("/api/v2/organizations/update_default_cloud", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccOrganizationDefaultCloudResource_ReadErrorStatusKeepsState_MockServer
// asserts that a non-404 error on Read's GET /clouds/{cloud_id} surfaces as an
// error and leaves the resource in state. Before the fix, a 500's error body
// decoded to is_default=false, so Read silently removed the resource and the
// next plan proposed re-creating it.
func TestAccOrganizationDefaultCloudResource_ReadErrorStatusKeepsState_MockServer(t *testing.T) {
	const orgID = "org_default_cloud_readerr_mock"
	const cloudID = "cld_default_cloud_readerr_mock"
	var failStatus atomic.Int32
	server := newOrgDefaultCloudErrorStatusMockServer(t, orgID, cloudID, &failStatus)

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_organization_default_cloud" "test" {
  cloud_id = %[1]q
}
`, cloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				// Refresh hits the 500: must error, not drop the resource.
				// PlanOnly so the failure can only come from Read - an apply
				// would also reach Create's own GET, masking a dropped state.
				PreConfig:   func() { failStatus.Store(http.StatusInternalServerError) },
				Config:      config,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Failed to read cloud:.*unexpected status 500`),
			},
			{
				// API recovered: the resource must still be in state, so the
				// plan is empty rather than a re-create.
				PreConfig: func() { failStatus.Store(0) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccOrganizationDefaultCloudResource_ImportErrorStatus_MockServer
// asserts that a non-404 error on import's GET /clouds/{cloud_id} reports an
// API error, not "Not The Organization Default".
func TestAccOrganizationDefaultCloudResource_ImportErrorStatus_MockServer(t *testing.T) {
	const orgID = "org_default_cloud_importerr_mock"
	const cloudID = "cld_default_cloud_importerr_mock"
	var failStatus atomic.Int32
	failStatus.Store(http.StatusForbidden)
	server := newOrgDefaultCloudErrorStatusMockServer(t, orgID, cloudID, &failStatus)

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_organization_default_cloud" "test" {
  cloud_id = %[1]q
}
`, cloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:  "anyscale_organization_default_cloud.test",
				ImportState:   true,
				ImportStateId: cloudID,
				Config:        config,
				ExpectError:   regexp.MustCompile(`(?s)API Request Failed.*unexpected status 403`),
			},
		},
	})
}
