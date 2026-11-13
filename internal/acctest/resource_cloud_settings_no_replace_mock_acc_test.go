package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// The create-time settings anti-taint contract (2026-11-09): a settings
// write that fails after the cloud exists must never leave a tainted cloud
// for the next apply to replace. On the fresh-create path a refusal rolls the
// whole create back (DELETE + remove from state) while the cloud is still
// empty - before add_resource and the readiness wait; if the rollback DELETE
// fails, the create downgrades to a warning and succeeds; on the adopt-by-name
// path nothing is ever deleted, and a settings failure is only ever a
// warning. Mock 403 body is the real production one, verbatim.

// cloudSettingsGateServer fronts mockCloudCreateTimeServer and owns the
// three settings PUTs, the cloud DELETE, and the adopt-by-name list response,
// with a scripted status for each and an ordered request log.
type cloudSettingsGateServer struct {
	mu      sync.Mutex
	base    *mockCloudCreateTimeServer
	cloudID string

	// Scripted statuses; 0 means "succeed and record the value".
	lineageStatus int
	autoStatus    int
	logsStatus    int

	// logsFailFrom fails the aggregated-logs PUT from the Nth call on
	// (1-based, 0 = never), for the readback-re-attempt tests.
	logsCalls    int
	logsFailFrom int

	// flipLogsAfterAddResource resets the aggregated-logs value the moment
	// add_resource arrives, simulating a backend that dropped the setting
	// during the resource add.
	flipLogsAfterAddResource bool

	// adoptList makes GET /api/v2/clouds report the mock cloud, so Create
	// takes the adopt-by-name path instead of a fresh create.
	adoptList bool

	deleteStatus int
	deletes      int

	requests []string
}

func newCloudSettingsGateServer(t *testing.T) (*httptest.Server, *cloudSettingsGateServer) {
	t.Helper()
	inner, base := newMockCloudCreateTimeServer(t)
	target, err := url.Parse(inner.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	s := &cloudSettingsGateServer{base: base, cloudID: base.cloudID}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/v2/clouds/"+s.cloudID+"/lineage_tracking_enabled":
			if s.lineageStatus != 0 {
				w.WriteHeader(s.lineageStatus)
				_, _ = fmt.Fprintf(w, `{"error":{"detail":%q}}`, lineageDetailOf(s.lineageStatus))
				s.mu.Unlock()
				return
			}
			s.base.setLineageTracking(r.URL.Query().Get("lineage_tracking_enabled") == "true")
		case r.Method == http.MethodPut && r.URL.Path == "/api/v2/clouds/"+s.cloudID+"/auto_add_user":
			if s.autoStatus != 0 {
				w.WriteHeader(s.autoStatus)
				_, _ = fmt.Fprintf(w, `{"error":{"detail":%q}}`, lineageDetailOf(s.autoStatus))
				s.mu.Unlock()
				return
			}
			s.base.setAutoAddUser(r.URL.Query().Get("auto_add_user") == "true")
		case r.Method == http.MethodPut && r.URL.Path == "/api/v2/clouds/"+s.cloudID+"/update_customer_aggregated_logs_config":
			s.logsCalls++
			if s.logsStatus != 0 || (s.logsFailFrom > 0 && s.logsCalls >= s.logsFailFrom) {
				status := s.logsStatus
				if status == 0 {
					status = http.StatusForbidden
				}
				w.WriteHeader(status)
				_, _ = fmt.Fprintf(w, `{"error":{"detail":%q}}`, lineageDetailOf(status))
				s.mu.Unlock()
				return
			}
			s.base.setAggregatedLogsEnabled(r.URL.Query().Get("is_enabled") == "true")
		case r.Method == http.MethodPut && r.URL.Path == "/api/v2/clouds/"+s.cloudID+"/add_resource":
			if s.flipLogsAfterAddResource {
				s.base.setAggregatedLogsEnabled(false)
			}
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v2/clouds/"+s.cloudID:
			s.deletes++
			if s.deleteStatus != 0 {
				w.WriteHeader(s.deleteStatus)
				_, _ = w.Write([]byte(`{"error":{"detail":"Cloud cannot be deleted right now."}}`))
				s.mu.Unlock()
				return
			}
			w.WriteHeader(http.StatusNoContent)
			s.mu.Unlock()
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/clouds" && s.adoptList:
			// findCloudByName finds the same cloud: Create adopts it.
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"results": [{"id": %q, "name": "createtime-mock"}], "metadata": {"total": 1, "next_paging_token": null}}`, s.cloudID)
			s.mu.Unlock()
			return
		default:
			// fall through to the proxy after releasing the lock
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/api/v2/clouds/"+s.cloudID {
			// handled above in all paths that reach here
			w.WriteHeader(http.StatusNoContent)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, s
}

func lineageDetailOf(status int) string {
	if status == http.StatusForbidden {
		return "Lineage tracking is not enabled for your organization."
	}
	return "The setting could not be changed."
}

// An aggregated-logs failure has the same rollback shape as the
// lineage refusal above - any
// settings write, not just lineage, rolls the create back.
func TestAccCloudResource_AggregatedLogsRefusalAtCreateRollsBack_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newCloudSettingsGateServer(t)
	mock.failLogsFrom(1)

	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name                     = "createtime-mock"
  cloud_provider           = "AWS"
  compute_stack            = "VM"
  region                   = "us-east-2"
  aggregated_logs_enabled  = true

  aws_config {
    vpc_id             = "vpc-realct123"
    subnet_ids_to_az = {
      "subnet-realct1" = "us-east-2a"
      "subnet-realct2" = "us-east-2b"
    }
    security_group_ids        = ["sg-realct1"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/real-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/real-cluster-node"
    external_id               = "org_real-external-id-ct"
  }

  object_storage {
    bucket_name = "real-ct-bucket"
  }
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)Cloud\s+Not\s+Created.*aggregated_logs_enabled.*Nothing\s+was\s+left\s+behind`),
			},
			{
				PreConfig: func() {
					deletes, _ := mock.snapshot()
					if deletes != 1 {
						t.Errorf("cloud DELETEs during the failed create = %d, want 1", deletes)
					}
					if i := mock.indexOfRequest("PUT /api/v2/clouds/cld_createtime_mock/add_resource"); i != -1 {
						t.Errorf("add_resource was called during the failed create (request %d): the rollback must happen before the resource add", i)
					}
					mock.failLogsFrom(0)
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionCreate),
					},
				},
			},
		},
	})
}

// An adopt-by-name cloud is never deleted on a settings failure - this
// provider did not create it. The create (adoption) succeeds with a warning,
// state records the configured value, and the next plan is an update.
func TestAccCloudResource_AdoptedCloudSettingFailureNeverDeletes_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newCloudSettingsGateServer(t)
	mock.setAdoptList(true)
	mock.setLineageStatus(http.StatusForbidden)

	config := allInOneCloudConfig(server.URL, true)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// No ExpectError: adoption succeeds with a warning, even
				// though the settings PUT was refused. The plan is
				// deliberately NOT empty afterwards - state over-reports
				// the setting until the next refresh (exit (b), checked in
				// the next step).
				Config:             config,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "id", "cld_createtime_mock"),
					// Exit (b): state records the configured value.
					resource.TestCheckResourceAttr("anyscale_cloud.test", "lineage_tracking_enabled", "true"),
				),
			},
			{
				PreConfig: func() {
					deletes, _ := mock.snapshot()
					if deletes != 0 {
						t.Errorf("cloud DELETEs during the adoption = %d, want 0: an adopted cloud is never deleted", deletes)
					}
					if got := mock.indexOfRequest("PUT /api/v2/clouds/cld_createtime_mock/add_resource"); got != -1 {
						t.Errorf("adoption must not re-add resources (request %d)", got)
					}
					mock.setLineageStatus(0)
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
}

// Adopting a cloud whose auto_add_user differs from the configuration
// PUTs the configured value and applies cleanly. A read-back that
// overwrote the plan value would fail the apply as an inconsistent result
// and taint the adopted cloud - this test fails against that behavior.
func TestAccCloudResource_AdoptedCloudAppliesConfiguredAutoAddUser_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newCloudSettingsGateServer(t)
	mock.setAdoptList(true)

	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "createtime-mock"
  cloud_provider = "AWS"
  region         = "us-east-2"
  auto_add_user  = true
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Counted inside the step: after resource.Test returns, its
				// own teardown destroy would add a legitimate DELETE.
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "id", "cld_createtime_mock"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "auto_add_user", "true"),
					func(*terraform.State) error {
						if got := mock.requestCount("PUT /api/v2/clouds/cld_createtime_mock/auto_add_user"); got != 1 {
							return fmt.Errorf("auto_add_user PUTs during the adoption = %d, want 1", got)
						}
						if deletes, _ := mock.snapshot(); deletes != 0 {
							return fmt.Errorf("cloud DELETEs during the adoption = %d, want 0", deletes)
						}
						return nil
					},
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// An aggregated-logs value the backend drops during the resource add is
// re-attempted exactly once at read-back. A re-attempt that succeeds
// converges; a re-attempt that fails downgrades to a warning and the create
// still succeeds with the configured value recorded.
func TestAccCloudResource_ReadbackReappliesDroppedSetting_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newCloudSettingsGateServer(t)
	mock.flipLogsAfterAddResource = true

	config := allInOneConfigWithLogs(server.URL)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "aggregated_logs_enabled", "true"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
	if _, logsCalls := mock.snapshot(); logsCalls != 2 {
		t.Errorf("aggregated-logs PUTs = %d, want 2 (the settings step plus exactly one read-back re-attempt)", logsCalls)
	}
}

// The same dropped setting whose re-attempt fails downgrades to a
// warning: apply succeeds, state records the configured value (and
// over-reports it until the next refresh), next plan is an update.
func TestAccCloudResource_ReadbackReattemptFailureWarnsInstead_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newCloudSettingsGateServer(t)
	mock.flipLogsAfterAddResource = true
	mock.failLogsFrom(2) // settings step succeeds; the read-back re-attempt fails

	config := allInOneConfigWithLogs(server.URL)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The create succeeds with a warning; the plan is then
				// deliberately non-empty (state over-reports until the next
				// refresh - exit (b), checked in the next step).
				Config:             config,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "aggregated_logs_enabled", "true"),
				),
			},
			{
				PreConfig: func() {
					_, logsCalls := mock.snapshot()
					if logsCalls != 2 {
						t.Errorf("aggregated-logs PUTs after the first apply = %d, want 2", logsCalls)
					}
					mock.failLogsFrom(0)
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
}

func allInOneConfigWithLogs(serverURL string) string {
	return testAccProviderBlock(serverURL) + `
resource "anyscale_cloud" "test" {
  name                      = "createtime-mock"
  cloud_provider            = "AWS"
  compute_stack             = "VM"
  region                    = "us-east-2"
  aggregated_logs_enabled   = true

  aws_config {
    vpc_id             = "vpc-realct123"
    subnet_ids_to_az = {
      "subnet-realct1" = "us-east-2a"
      "subnet-realct2" = "us-east-2b"
    }
    security_group_ids        = ["sg-realct1"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/real-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/real-cluster-node"
    external_id               = "org_real-external-id-ct"
  }

  object_storage {
    bucket_name = "real-ct-bucket"
  }
}
`
}

func (s *cloudSettingsGateServer) setLineageStatus(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lineageStatus = status
}

func (s *cloudSettingsGateServer) setDeleteStatus(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteStatus = status
}

func (s *cloudSettingsGateServer) failLogsFrom(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logsFailFrom = n
	s.logsCalls = 0
}

func (s *cloudSettingsGateServer) setAdoptList(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adoptList = v
}

func (s *cloudSettingsGateServer) snapshot() (deletes, logsCalls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deletes, s.logsCalls
}

// indexOfRequest returns the position of the first request equal to req, or
// -1; -2 when req occurs more than once is not needed here.
func (s *cloudSettingsGateServer) indexOfRequest(req string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.requests {
		if r == req {
			return i
		}
	}
	return -1
}

func (s *cloudSettingsGateServer) requestCount(req string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r == req {
			n++
		}
	}
	return n
}

// allInOneCloudConfig is a full embedded-config cloud: create, settings,
// add_resource, readiness, readback - the whole all-in-one path.
func allInOneCloudConfig(serverURL string, lineage bool) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name                       = "createtime-mock"
  cloud_provider             = "AWS"
  compute_stack              = "VM"
  region                     = "us-east-2"
  lineage_tracking_enabled   = %t

  aws_config {
    vpc_id             = "vpc-realct123"
    subnet_ids_to_az = {
      "subnet-realct1" = "us-east-2a"
      "subnet-realct2" = "us-east-2b"
    }
    security_group_ids        = ["sg-realct1"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/real-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/real-cluster-node"
    external_id               = "org_real-external-id-ct"
  }

  object_storage {
    bucket_name = "real-ct-bucket"
  }
}
`, lineage)
}

// A lineage refusal on the all-in-one path rolls the whole create back
// while the cloud is still empty: no add_resource, no readiness wait, one
// DELETE, nothing in state (the next apply is an ordinary create), and the
// error says so. If the settings step instead ran after add_resource and
// the readiness wait, it would error and leave the cloud tainted for the
// next apply to replace - this test fails against that behavior.
func TestAccCloudResource_LineageRefusalAtCreateRollsBack_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newCloudSettingsGateServer(t)
	mock.setLineageStatus(http.StatusForbidden)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      allInOneCloudConfig(server.URL, true),
				ExpectError: regexp.MustCompile(`(?s)Cloud\s+Not\s+Created.*lineage_tracking_enabled.*Nothing\s+was\s+left\s+behind`),
			},
			{
				PreConfig: func() {
					deletes, _ := mock.snapshot()
					if deletes != 1 {
						t.Errorf("cloud DELETEs during the failed create = %d, want 1", deletes)
					}
					if i := mock.indexOfRequest("PUT /api/v2/clouds/cld_createtime_mock/add_resource"); i != -1 {
						t.Errorf("add_resource was called during the failed create (request %d): the rollback must happen before the resource add", i)
					}
					mock.setLineageStatus(0)
				},
				Config: allInOneCloudConfig(server.URL, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionCreate),
					},
				},
				Check: resource.TestCheckResourceAttr("anyscale_cloud.test", "lineage_tracking_enabled", "true"),
			},
		},
	})
}

// The same refusal on an empty cloud has the same shape - the settings
// all run before the empty-cloud early return.
func TestAccCloudResource_EmptyCloudLineageRefusalRollsBack_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newCloudSettingsGateServer(t)
	mock.setLineageStatus(http.StatusForbidden)
	emptyConfig := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name                     = "createtime-mock"
  cloud_provider           = "AWS"
  region                   = "us-east-2"
  lineage_tracking_enabled = true
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      emptyConfig,
				ExpectError: regexp.MustCompile(`(?s)Cloud\s+Not\s+Created.*lineage_tracking_enabled.*Nothing\s+was\s+left\s+behind`),
			},
			{
				PreConfig: func() {
					deletes, _ := mock.snapshot()
					if deletes != 1 {
						t.Errorf("cloud DELETEs during the failed create = %d, want 1", deletes)
					}
					mock.setLineageStatus(0)
				},
				Config: emptyConfig,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionCreate),
					},
				},
			},
		},
	})
}

// If the rollback DELETE itself fails, the create downgrades to a
// warning and succeeds - the cloud exists, is not tainted, and the recorded
// configured value surfaces the conflict as an update on the next plan (never
// a replace). Erroring the create instead would taint the cloud - this
// test fails against that behavior.
func TestAccCloudResource_SettingRefusalDeleteFailsWarnsInstead_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newCloudSettingsGateServer(t)
	mock.setLineageStatus(http.StatusForbidden)
	mock.setDeleteStatus(http.StatusBadRequest)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// No ExpectError: the create SUCCEEDS with a warning. The
				// plan is deliberately NOT empty afterwards: state records
				// the configured value while the cloud reports the refused
				// one, so the next refresh reads the real value and the
				// next plan is an update of the setting (exit (b), checked
				// in the step below).
				Config:             allInOneCloudConfig(server.URL, true),
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "lineage_tracking_enabled", "true"),
				),
			},
			{
				PreConfig: func() {
					deletes, _ := mock.snapshot()
					if deletes != 1 {
						t.Errorf("cloud DELETE attempts during the create = %d, want 1 (the failed rollback)", deletes)
					}
					mock.setLineageStatus(0)
					mock.setDeleteStatus(0)
				},
				Config: allInOneCloudConfig(server.URL, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
}
