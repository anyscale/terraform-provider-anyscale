package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// orgDefaultCloudPointerMock models the org's default_cloud_id column the way
// the backend does: one pointer, and GET /api/v2/clouds/{id} computes each
// cloud's is_default as (id == default_cloud_id). The single-cloud mock in
// resource_organization_default_cloud_acc_test.go tracks is_default for one
// managed cloud only, so it cannot represent a move between two clouds: after
// re-pointing, the old cloud must read false and the new one true.
//
// The pointer starts empty, so the only way any cloud reads is_default:true is
// a real POST update_default_cloud.
type orgDefaultCloudPointerMock struct {
	mu             sync.Mutex
	defaultCloudID string
	setCalls       []string // cloud_id of every update_default_cloud call, in order
}

func (m *orgDefaultCloudPointerMock) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.setCalls...)
}

func newOrgDefaultCloudPointerMockServer(t *testing.T, orgID string, cloudIDs ...string) (*httptest.Server, *orgDefaultCloudPointerMock) {
	t.Helper()
	known := map[string]bool{}
	for _, id := range cloudIDs {
		known[id] = true
	}
	m := &orgDefaultCloudPointerMock{}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/userinfo", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		def := m.defaultCloudID
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {"organizations": [{"id": %[1]q, "name": "mock-org", "public_identifier": "mock-org", "default_cloud_id": %[2]q}]}}`, orgID, def)
	})

	// Subtree and bare-path forms both registered, as in the sibling mock, so
	// ServeMux never 301-redirects; the bare form falls into "not found".
	cloudHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/v2/clouds/")
		if id == r.URL.Path || !known[id] {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "cloud not found"}}`)
			return
		}
		m.mu.Lock()
		isDefault := id == m.defaultCloudID
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "name": "mock-cloud", "provider": "AWS", "region": "us-east-2",
			"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": %[2]t
		}}`, id, isDefault)
	}
	mux.HandleFunc("/api/v2/clouds/", cloudHandler)
	mux.HandleFunc("/api/v2/clouds", cloudHandler)

	mux.HandleFunc("/api/v2/organizations/update_default_cloud", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on update_default_cloud", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("cloud_id")
		m.mu.Lock()
		m.setCalls = append(m.setCalls, id)
		m.defaultCloudID = id
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, m
}

// TestAccOrganizationDefaultCloudResource_UpdateCloudID_MockServer proves a
// cloud_id change re-points the org default in place: the plan is an Update,
// not a Replace (cloud_id carries no RequiresReplace), Update actually calls
// update_default_cloud with the new id, and the post-apply refresh is clean -
// which it can only be if the new cloud now reads is_default:true, since Read
// drops the resource from state otherwise.
func TestAccOrganizationDefaultCloudResource_UpdateCloudID_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const orgID = "org_default_cloud_update_mock"
	const firstCloudID = "cld_default_cloud_update_first"
	const secondCloudID = "cld_default_cloud_update_second"
	server, m := newOrgDefaultCloudPointerMockServer(t, orgID, firstCloudID, secondCloudID)

	config := func(cloudID string) string {
		return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_organization_default_cloud" "test" {
  cloud_id = %[1]q
}
`, cloudID)
	}

	const addr = "anyscale_organization_default_cloud.test"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(firstCloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cloud_id", firstCloudID),
					resource.TestCheckResourceAttr(addr, "id", orgID),
				),
			},
			{
				Config: config(secondCloudID),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cloud_id", secondCloudID),
					resource.TestCheckResourceAttr(addr, "id", orgID),
					func(_ *terraform.State) error {
						m.mu.Lock()
						defer m.mu.Unlock()
						if m.defaultCloudID != secondCloudID {
							return fmt.Errorf("org default_cloud_id = %q after update, want %q", m.defaultCloudID, secondCloudID)
						}
						return nil
					},
				),
			},
		},
	})

	// Create sets the first cloud, Update sets the second; destroy is a no-op.
	got := m.calls()
	want := []string{firstCloudID, secondCloudID}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("update_default_cloud calls = %v, want %v", got, want)
	}
}
