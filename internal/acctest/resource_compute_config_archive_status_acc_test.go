package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// newArchiveOverrideComputeConfigServer fronts the stateful compute-config
// mock and, while override holds a response, answers every archive call with
// it instead.
func newArchiveOverrideComputeConfigServer(t *testing.T, override *atomic.Pointer[archiveResponse]) *httptest.Server {
	t.Helper()
	inner := newMockComputeConfigServer(t)
	target, err := url.Parse(inner.URL)
	if err != nil {
		t.Fatalf("parse inner URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if resp := override.Load(); resp != nil && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/archive") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.status)
			_, _ = fmt.Fprint(w, resp.body)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func testAccComputeConfigArchiveStatus(t *testing.T, name string, onDestroy archiveResponse, expectErr *regexp.Regexp) {
	t.Helper()
	var override atomic.Pointer[archiveResponse]
	server := newArchiveOverrideComputeConfigServer(t, &override)
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name      = %q
  cloud_id  = "cld_mock_cc"
  head_node = { instance_type = "m5.large" }
}
`, name)

	steps := []resource.TestStep{
		{Config: config},
		{
			PreConfig:   func() { override.Store(&onDestroy) },
			Config:      config,
			Destroy:     true,
			ExpectError: expectErr,
		},
	}
	if expectErr != nil {
		// Let the framework's own teardown destroy succeed.
		steps = append(steps, resource.TestStep{
			PreConfig: func() { override.Store(nil) },
			Config:    config,
		})
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps:                    steps,
	})
}

// An Azure control plane rejects every archive with this 400. Destroy must
// still succeed, leaving the config in place with a warning, as it does for
// container images.
func TestAccComputeConfigResource_ArchiveAzureControlPlane400_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccComputeConfigArchiveStatus(t, "cc-archive-azure", archiveResponse{
		status: http.StatusBadRequest,
		body:   `{"error": {"detail": "Archiving compute configs is not supported on Azure Control Plane."}}`,
	}, nil)
}

// Positive control on the same path: any other 400 still fails destroy.
func TestAccComputeConfigResource_ArchiveOther400FailsDestroy_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccComputeConfigArchiveStatus(t, "cc-archive-other", archiveResponse{
		status: http.StatusBadRequest,
		body:   `{"error": {"detail": "Something else went wrong."}}`,
	}, regexp.MustCompile(`delete compute config`))
}
