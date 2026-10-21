package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// convOrgCloudServer fronts mockCloudCreateTimeServer and owns the
// auto_add_user PUT, answering it with a scripted 409 when one is set.
type convOrgCloudServer struct {
	mu       sync.Mutex
	cloudID  string
	status   int
	detail   string
	autoPuts int
}

func newConvOrgCloudServer(t *testing.T) (*httptest.Server, *convOrgCloudServer) {
	t.Helper()
	inner, base := newMockCloudCreateTimeServer(t)
	target, err := url.Parse(inner.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	s := &convOrgCloudServer{cloudID: base.cloudID}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"error":{"detail":%q}}`, detail)
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
    external_id               = "real-external-id-ct"
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
