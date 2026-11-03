package acctest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccCloudResource_NonOrgExternalIDOmittedFromCreate proves a create whose
// aws_config.external_id does not start with the organization ID still
// succeeds, and that the value is left out of POST /api/v2/clouds: the API
// refuses it as the cloud's own external_id. The plan carries a warning for it,
// which the framework test harness does not surface; the unit test
// TestWarnCreateExternalIDNotRecorded asserts that.
func TestAccCloudResource_NonOrgExternalIDOmittedFromCreate(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	inner, _ := newConvOrgCloudServer(t)
	target, err := url.Parse(inner.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	var mu sync.Mutex
	var createBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v2/clouds" {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("POST body is not JSON: %v", err)
			}
			mu.Lock()
			createBodies = append(createBodies, body)
			mu.Unlock()
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	config := strings.Replace(convOrgCloudConfig(server.URL, false),
		`external_id               = "org_real-external-id-ct"`,
		`external_id               = "not-an-org-external-id"`, 1)
	if !strings.Contains(config, "not-an-org-external-id") {
		t.Fatal("fixture no longer carries the external_id line this test rewrites")
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps:                    []resource.TestStep{{Config: config}},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(createBodies) != 1 {
		t.Fatalf("POST /api/v2/clouds count = %d, want 1", len(createBodies))
	}
	if v, present := createBodies[0]["external_id"]; present {
		t.Errorf("create request carried external_id = %v, want it omitted", v)
	}
}
