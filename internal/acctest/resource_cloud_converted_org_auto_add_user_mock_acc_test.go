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

// convOrgCloudServer fronts mockCloudCreateTimeServer and owns the
// auto_add_user PUT, answering it with a scripted 409 when one is set. It also
// owns DELETE of the cloud, counting it and failing it when deleteStatus is set.
type convOrgCloudServer struct {
	mu           sync.Mutex
	cloudID      string
	status       int
	detail       string
	autoPuts     int
	deletes      int
	deleteStatus int
	// requests is every request in arrival order, as "METHOD path", so a test
	// can assert ordering rather than only counts.
	requests []string
	base     *mockCloudCreateTimeServer
}

func newConvOrgCloudServer(t *testing.T) (*httptest.Server, *convOrgCloudServer) {
	t.Helper()
	inner, base := newMockCloudCreateTimeServer(t)
	target, err := url.Parse(inner.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	s := &convOrgCloudServer{cloudID: base.cloudID, base: base}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		if r.URL.Path == "/api/v2/clouds/"+s.cloudID+"/auto_add_user" {
			s.mu.Lock()
			s.autoPuts++
			status, detail := s.status, s.detail
			s.mu.Unlock()
			if r.Method != http.MethodPut {
				t.Errorf("unexpected method %s on auto_add_user", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			if status == 0 {
				s.base.setAutoAddUser(r.URL.Query().Get("auto_add_user") == "true")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"error":{"detail":%q}}`, detail)
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/api/v2/clouds/"+s.cloudID {
			s.mu.Lock()
			s.deletes++
			deleteStatus := s.deleteStatus
			s.mu.Unlock()
			if deleteStatus != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(deleteStatus)
				_, _ = fmt.Fprint(w, `{"error":{"detail":"Cloud cannot be deleted right now."}}`)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, s
}

func (s *convOrgCloudServer) set(status int, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.detail = status, detail
}

func (s *convOrgCloudServer) setDeleteStatus(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteStatus = status
}

func (s *convOrgCloudServer) deleteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deletes
}

// firstIndex returns the position of the first request equal to req, or -1.
func (s *convOrgCloudServer) firstIndex(req string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.requests {
		if r == req {
			return i
		}
	}
	return -1
}

func (s *convOrgCloudServer) puts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoPuts
}

func convOrgCloudConfig(url string, autoAddUser bool) string {
	return testAccProviderBlock(url) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "createtime-mock"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"
  auto_add_user  = %t

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
`, autoAddUser)
}

// convOrgCloudAutoAddUserUpdate creates the cloud with auto_add_user = false
// (no PUT), then flips it to true so Update issues exactly one auto_add_user
// PUT, which the mock refuses with detail.
func convOrgCloudAutoAddUserUpdate(t *testing.T, detail string, check func(error) error) *convOrgCloudServer {
	t.Helper()
	server, mock := newConvOrgCloudServer(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               check,
		Steps: []resource.TestStep{
			{Config: convOrgCloudConfig(server.URL, false)},
			{
				PreConfig: func() { mock.set(http.StatusConflict, detail) },
				Config:    convOrgCloudConfig(server.URL, true),
			},
		},
	})
	return mock
}

// In a converted organization the auto_add_user PUT answers 409; the update
// must report the role-bindings diagnostic and must not retry it.
func TestAccCloudResource_ConvertedOrgAutoAddUserUpdateErrors(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	check, assertCalled := convOrgConvertedCheck(t)
	mock := convOrgCloudAutoAddUserUpdate(t, convOrgConvertedDetail, check)
	assertCalled()
	if got := mock.puts(); got != 1 {
		t.Errorf("auto_add_user PUTs = %d, want 1: the converted-org 409 must not be retried", got)
	}
}

// The same refusal at create time, where auto_add_user = true is applied
// right after the cloud is created.
func TestAccCloudResource_ConvertedOrgAutoAddUserCreateErrors(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newConvOrgCloudServer(t)
	mock.set(http.StatusConflict, convOrgConvertedDetail)
	check, assertCalled := convOrgConvertedCheck(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               check,
		Steps:                    []resource.TestStep{{Config: convOrgCloudConfig(server.URL, true)}},
	})
	assertCalled()
	if got := mock.puts(); got != 1 {
		t.Errorf("auto_add_user PUTs = %d, want 1: the converted-org 409 must not be retried", got)
	}
}

// Control: an unrecognized 409 keeps its own detail, is not reported as the
// converted-org diagnostic, and (lacking the transient wording) is not retried.
func TestAccCloudResource_ConvertedOrgUnrecognizedConflictPassesThrough(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	check, assertCalled := convOrgPassThroughCheck(t)
	mock := convOrgCloudAutoAddUserUpdate(t, convOrgUnrecognizedDetail, check)
	assertCalled()
	if got := mock.puts(); got != 1 {
		t.Errorf("auto_add_user PUTs = %d, want 1", got)
	}
}

// convOrgCreateThenPlan applies auto_add_user = true against a mock that
// refuses the PUT with refusal, expecting createErr; then, with the refusal
// and any DELETE failure cleared, plans auto_add_user = false and asserts the
// plan's action. That action is what proves where the failed create left
// state: Create means nothing is in state, DestroyBeforeCreate means the
// cloud stayed in state tainted. deletesAfterCreate is the number of cloud
// DELETEs the failed create sent (resource.Test's own teardown adds more).
func convOrgCreateThenPlan(t *testing.T, refusal string, deleteStatus int, createErr *regexp.Regexp, wantAction plancheck.ResourceActionType) (deletesAfterCreate int) {
	t.Helper()
	server, mock := newConvOrgCloudServer(t)
	mock.set(http.StatusConflict, refusal)
	mock.setDeleteStatus(deleteStatus)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      convOrgCloudConfig(server.URL, true),
				ExpectError: createErr,
			},
			{
				PreConfig: func() {
					deletesAfterCreate = mock.deleteCount()
					mock.set(0, "")
					mock.setDeleteStatus(0)
				},
				Config: convOrgCloudConfig(server.URL, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_cloud.test", wantAction)},
				},
			},
		},
	})
	return deletesAfterCreate
}

// In a converted organization a refused auto_add_user at create is
// deterministic, so the provider deletes the cloud it just created and leaves
// nothing in state: the next apply is an ordinary create, not a replacement.
func TestAccCloudResource_ConvertedOrgAutoAddUserCreateDeletesCloud(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	deletes := convOrgCreateThenPlan(t, convOrgConvertedDetail, 0,
		regexp.MustCompile(`(?s)Organization\s+Uses\s+Role\s+Bindings.*The\s+cloud\s+was\s+not\s+created`),
		plancheck.ResourceActionCreate)
	if deletes != 1 {
		t.Errorf("cloud DELETEs during the failed create = %d, want 1", deletes)
	}
}

// If deleting the refused cloud fails, the cloud stays in state, tainted, and
// the error names the manual recovery.
func TestAccCloudResource_ConvertedOrgAutoAddUserCreateDeleteFailsTaints(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	deletes := convOrgCreateThenPlan(t, convOrgConvertedDetail, http.StatusBadRequest,
		regexp.MustCompile(`(?s)deleting\s+the\s+cloud\s+also\s+failed.*terraform\s+untaint`),
		plancheck.ResourceActionDestroyBeforeCreate)
	if deletes != 1 {
		t.Errorf("cloud DELETEs during the failed create = %d, want 1 (the attempt that failed)", deletes)
	}
}

// A 409 that is not the converted-organization refusal is not deterministic:
// the cloud must not be deleted, and it stays in state tainted as before.
func TestAccCloudResource_UnrecognizedAutoAddUserConflictAtCreateKeepsCloud(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	deletes := convOrgCreateThenPlan(t, convOrgUnrecognizedDetail, 0,
		regexp.MustCompile(`another\s+membership\s+change`),
		plancheck.ResourceActionDestroyBeforeCreate)
	if deletes != 0 {
		t.Errorf("cloud DELETEs during the failed create = %d, want 0: only the converted-org refusal deletes the cloud", deletes)
	}
}

// convOrgEmptyCloudConfig is a cloud with no embedded resource config, which
// Create returns from before adding a resource or waiting for readiness.
func convOrgEmptyCloudConfig(url string, autoAddUser bool) string {
	return testAccProviderBlock(url) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "createtime-mock"
  cloud_provider = "AWS"
  region         = "us-east-2"
  auto_add_user  = %t
}
`, autoAddUser)
}

// Legacy organization: auto_add_user = true is applied once, right after the
// cloud is created and before the resource add and the readiness wait, and the
// cloud is not deleted.
func TestAccCloudResource_AutoAddUserAppliedBeforeResourceAdd(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newConvOrgCloudServer(t)
	var deletesAfterCreate int
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: convOrgCloudConfig(server.URL, true),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "auto_add_user", "true"),
					func(*terraform.State) error {
						deletesAfterCreate = mock.deleteCount()
						return nil
					},
				),
			},
		},
	})
	if got := mock.puts(); got != 1 {
		t.Errorf("auto_add_user PUTs = %d, want 1", got)
	}
	if deletesAfterCreate != 0 {
		t.Errorf("cloud DELETEs during create = %d, want 0", deletesAfterCreate)
	}
	put := mock.firstIndex("PUT /api/v2/clouds/" + mock.cloudID + "/auto_add_user")
	add := mock.firstIndex("PUT /api/v2/clouds/" + mock.cloudID + "/add_resource")
	poll := mock.firstIndex("GET /api/v2/clouds/" + mock.cloudID)
	if put < 0 || add < 0 || poll < 0 || put > add || put > poll {
		t.Errorf("request order: auto_add_user PUT at %d, add_resource at %d, first cloud GET at %d; want the PUT first", put, add, poll)
	}
}

// An empty cloud returns from Create before the resource add and the wait.
// auto_add_user = true must still be applied there; before it was, the
// readback reported false and the apply failed as an inconsistent result.
func TestAccCloudResource_EmptyCloudAppliesAutoAddUser(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newConvOrgCloudServer(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: convOrgEmptyCloudConfig(server.URL, true),
				Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "auto_add_user", "true"),
			},
		},
	})
	if got := mock.puts(); got != 1 {
		t.Errorf("auto_add_user PUTs = %d, want 1", got)
	}
}

// The converted-organization refusal on an empty cloud is undone the same way.
func TestAccCloudResource_ConvertedOrgEmptyCloudAutoAddUserDeletesCloud(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newConvOrgCloudServer(t)
	mock.set(http.StatusConflict, convOrgConvertedDetail)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      convOrgEmptyCloudConfig(server.URL, true),
				ExpectError: regexp.MustCompile(`The\s+cloud\s+was\s+not\s+created`),
			},
			{
				PreConfig: func() {
					if got := mock.deleteCount(); got != 1 {
						t.Errorf("cloud DELETEs during the failed create = %d, want 1", got)
					}
					mock.set(0, "")
				},
				Config: convOrgEmptyCloudConfig(server.URL, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionCreate)},
				},
			},
		},
	})
}
