package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// Mock-server lifecycle coverage for anyscale_organization_default_cloud (the
// manage half of the 2026-07-23 is_default quest's split - see
// resource_organization_default_cloud.go's own doc comment for the design).
// No real infra needed: stands up httptest servers for /api/v2/userinfo,
// /api/v2/clouds/{id}, and /api/v2/organizations/update_default_cloud, and
// points the provider at them via api_url, following this package's
// established mock-server pattern (see resource_cloud_c3_lifecycle_acc_test.go).

// orgDefaultCloudMockServer serves a single org (fixed id) and a single
// managed cloud whose is_default flag reflects whatever the last successful
// update_default_cloud call set it to - mutable so tests can flip it
// out-of-band to simulate drift, and to prove the resource's own Create call
// is what flips it (not an unconditional echo).
//
// isDefault starts false, deliberately NOT pre-matching the cloud_id the
// test config asserts - assayer's catch: seeding it true from the start
// would make the Lifecycle test pass even against a completely broken
// Create() that never calls update_default_cloud at all, since GET
// /clouds/{cloud_id} would still read back is_default:true regardless of
// whether the write ever happened (the same mock-omission failure shape as
// the mount_targets/aggregated_logs precedents this session already hit).
// Starting false means a real write is the ONLY way the managed cloud ever
// reads is_default:true.
type orgDefaultCloudMockServer struct {
	mu          sync.Mutex
	setCalls    int
	lastCloudID string
	isDefault   bool // the MANAGED cloud's current is_default value
}

// snapshot returns a consistent, race-free read of the call-tracking
// fields, for assertions made after resource.Test has returned.
func (m *orgDefaultCloudMockServer) snapshot() (setCalls int, lastCloudID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.setCalls, m.lastCloudID
}

func newOrgDefaultCloudMockServer(t *testing.T, orgID, cloudID string, knownCloudIDs map[string]bool) (*httptest.Server, *orgDefaultCloudMockServer) {
	t.Helper()
	m := &orgDefaultCloudMockServer{isDefault: false}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {"organizations": [{"id": %[1]q, "name": "mock-org", "public_identifier": "mock-org", "default_cloud_id": %[2]q}]}}`, orgID, cloudID)
	})

	mux.HandleFunc("/api/v2/clouds/", func(w http.ResponseWriter, r *http.Request) {
		requestedID := r.URL.Path[len("/api/v2/clouds/"):]
		if requestedID != cloudID && !knownCloudIDs[requestedID] {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "cloud not found"}}`)
			return
		}
		m.mu.Lock()
		isDefault := requestedID == cloudID && m.isDefault
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "name": "mock-cloud", "provider": "AWS", "region": "us-east-2",
			"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": %[2]t
		}}`, requestedID, isDefault)
	})

	mux.HandleFunc("/api/v2/organizations/update_default_cloud", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on update_default_cloud", r.Method)
			return
		}
		newCloudID := r.URL.Query().Get("cloud_id")
		m.mu.Lock()
		m.setCalls++
		m.lastCloudID = newCloudID
		m.isDefault = newCloudID == cloudID
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, m
}

// TestAccOrganizationDefaultCloudResource_Lifecycle_MockServer proves the
// basic create -> plan-empty -> import round trip against a mock backend,
// and - per assayer's review catch - that Create() actually calls the write
// API rather than merely producing state values a broken Create() could
// have matched by coincidence (the mock starts isDefault:false precisely so
// there is no coincidental match available).
func TestAccOrganizationDefaultCloudResource_Lifecycle_MockServer(t *testing.T) {
	const orgID = "org_default_cloud_mock"
	const cloudID = "cld_default_cloud_mock"
	server, mockServer := newOrgDefaultCloudMockServer(t, orgID, cloudID, nil)

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
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_organization_default_cloud.test", "cloud_id", cloudID),
					resource.TestCheckResourceAttr("anyscale_organization_default_cloud.test", "id", orgID),
				),
				// Headline consistency gate: if Create() silently skipped
				// calling update_default_cloud, the mock's is_default would
				// still read back false (it starts false, with no other
				// path to true), and this immediately-following
				// refresh+plan would show a non-empty plan (Read finds
				// is_default:false -> RemoveResource -> next plan re-adds),
				// not a clean no-op.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				// Identical config, nothing changed - the mock's is_default
				// is genuinely true now (Create's own POST set it), so this
				// must be a clean no-op.
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				// Import is keyed by cloud_id, NOT the resource's own "id"
				// (organization id) - ImportStateId pins that explicitly,
				// since terraform-plugin-testing otherwise defaults to the
				// resource's "id" attribute, which would be the wrong value
				// here and (before this fix) silently succeeded anyway
				// because the old ImportState ignored req.ID entirely.
				ResourceName:      "anyscale_organization_default_cloud.test",
				ImportState:       true,
				ImportStateId:     cloudID,
				ImportStateVerify: true,
			},
		},
	})

	setCalls, lastCloudID := mockServer.snapshot()
	if setCalls == 0 {
		t.Fatal("update_default_cloud was never called during Create - the resource must actually call the write API, not just produce matching state values")
	}
	if lastCloudID != cloudID {
		t.Errorf("update_default_cloud was last called with cloud_id=%q, want %q", lastCloudID, cloudID)
	}
}

// TestAccOrganizationDefaultCloudResource_BogusCloudID_MockServer is the
// Gate 2 test architect named as a required build/test case: the backend's
// update_default_cloud performs zero validation on cloud_id (confirmed
// against the real service), so the ONLY thing standing between a user and
// silently repointing their org default at a typo'd or wrong-org id is this
// resource's own client-side check. Prove it fires as a clean, specific
// diagnostic - not a silent success and not a generic bubbled-up Go error.
func TestAccOrganizationDefaultCloudResource_BogusCloudID_MockServer(t *testing.T) {
	const orgID = "org_default_cloud_bogus_mock"
	const cloudID = "cld_default_cloud_bogus_mock"
	const bogusCloudID = "cld_does_not_exist"
	server, m := newOrgDefaultCloudMockServer(t, orgID, cloudID, nil)

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_organization_default_cloud" "test" {
  cloud_id = %[1]q
}
`, bogusCloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)cloud\s+"cld_does_not_exist"\s+not\s+found`),
			},
		},
	})

	if m.setCalls != 0 {
		t.Errorf("update_default_cloud was called %d times for a bogus cloud_id that should have failed client-side validation before ever reaching the API", m.setCalls)
	}
}

// TestAccOrganizationDefaultCloudResource_ImportNotCurrentDefault_MockServer
// is the Gate 2 "import non-default -> clean error" case the authoritative
// spec names explicitly: importing a cloud that EXISTS but is not the
// current organization default must fail with a clear diagnostic, not
// silently import garbage state.
func TestAccOrganizationDefaultCloudResource_ImportNotCurrentDefault_MockServer(t *testing.T) {
	const orgID = "org_default_cloud_notdefault_mock"
	const realDefaultCloudID = "cld_default_cloud_notdefault_real"
	const nonDefaultCloudID = "cld_default_cloud_notdefault_other"
	server, _ := newOrgDefaultCloudMockServer(t, orgID, realDefaultCloudID, map[string]bool{nonDefaultCloudID: true})

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_organization_default_cloud" "test" {
  cloud_id = %[1]q
}
`, nonDefaultCloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_organization_default_cloud.test",
				ImportState:        true,
				ImportStateId:      nonDefaultCloudID,
				Config:             config,
				ImportStatePersist: false,
				ExpectError:        regexp.MustCompile(`(?s)Cloud\s+"cld_default_cloud_notdefault_other"\s+is\s+not\s+the\s+current\s+organization\s+default`),
			},
		},
	})
}

// TestAccOrganizationDefaultCloudResource_Drift_MockServer proves the
// drift-detection design: if the org default moves to a different cloud out
// of band, the next plan detects it (the managed cloud's own is_default
// reads false) and plans a replace that re-asserts cloud_id - not a silent
// no-op, and not an error.
func TestAccOrganizationDefaultCloudResource_Drift_MockServer(t *testing.T) {
	const orgID = "org_default_cloud_drift_mock"
	const cloudID = "cld_default_cloud_drift_mock"
	server, m := newOrgDefaultCloudMockServer(t, orgID, cloudID, nil)

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
				PreConfig: func() {
					// Simulate the org default moving to a different cloud
					// out of band (e.g. via the console) - the managed
					// cloud is no longer the default.
					m.mu.Lock()
					m.isDefault = false
					m.mu.Unlock()
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}
