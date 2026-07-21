package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// isPrivateCloudMockServer serves a single anyscale_cloud whose
// is_private_cloud/is_private_service_cloud values are whatever POST
// /api/v2/clouds actually received on create, defaulting to false when the
// key is absent from the request body - the load-bearing difference from
// newC3MockCloudServer's static echo (that mock never inspects the create
// body at all). This must genuinely round-trip the request to distinguish
// "the fix sends the field" from "the fix doesn't", mirroring the real
// backend exactly: forge's trace confirmed the Cloud.is_private_cloud
// column is create-only (clouds_dao.py update path has no param for it) and
// GET always reads back whatever was persisted at create (spec.json:
// is_private_cloud_fix).
type isPrivateCloudMockServer struct {
	mu               sync.Mutex
	name             string
	isPrivate        bool
	isPrivateService bool
}

func newIsPrivateCloudMockServer(t *testing.T, cloudID string) *httptest.Server {
	t.Helper()
	m := &isPrivateCloudMockServer{}
	mux := http.NewServeMux()

	render := func() string {
		m.mu.Lock()
		defer m.mu.Unlock()
		return fmt.Sprintf(`{
			"id": %[1]q, "name": %[2]q, "provider": "AWS", "region": "us-east-2",
			"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": false,
			"is_private_cloud": %[3]t, "is_private_service_cloud": %[4]t
		}`, cloudID, m.name, m.isPrivate, m.isPrivateService)
	}

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)

			m.mu.Lock()
			// Echo the requested name verbatim - a real create echoes what
			// was sent, and a hardcoded/static name here would falsely trip
			// the framework's OWN inconsistent-result check on the unrelated
			// "name" attribute, masking the is_private_cloud signal this
			// mock exists to isolate.
			if v, ok := body["name"].(string); ok {
				m.name = v
			}
			// Mimic today's real backend column exactly: a request that
			// omits the key persists false (the column's zero value), not
			// "unchanged" or "unknown" - this is the pre-fix
			// CreateCloudRequest path. Once the fix lands, is_private_cloud
			// is Optional+Computed+Default(false), so ValueBool() is never
			// unknown at create and the key is always present on the wire.
			if v, ok := body["is_private_cloud"].(bool); ok {
				m.isPrivate = v
			} else {
				m.isPrivate = false
			}
			if v, ok := body["is_private_service_cloud"].(bool); ok {
				m.isPrivateService = v
			} else {
				m.isPrivateService = false
			}
			m.mu.Unlock()

			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, render())
		case http.MethodGet:
			// anyscale_cloud's own by-name adopt check: report none so
			// Create always takes the fresh-create path.
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds", r.Method)
		}
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, render())
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s", r.Method, cloudID)
		}
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {"cloud_deployment_id": "cldrsrc_ipc_mock", "cloud_resource_id": "cldrsrc_ipc_mock"}}`)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccCloudResource_IsPrivateCloudRoundTrip_MockServer is the mutation-proof
// regression test for the yunhao-reported bug: is_private_cloud=true used to
// create the cloud then fail with Terraform's "Provider produced inconsistent
// result after apply" (CreateCloudRequest never sent the field, so the
// create-only backend column defaulted false and the post-create Read read
// that false straight back). Run against unfixed code this fails with
// exactly that error; against the fix (models.go CreateCloudRequest +
// resource_cloud.go createReq wiring) it passes clean. The false case is a
// same-shape regression guard in the other direction - it must stay clean on
// both sides of the fix, proving the fix didn't flip the default.
func TestAccCloudResource_IsPrivateCloudRoundTrip_MockServer(t *testing.T) {
	cases := []struct {
		name      string
		isPrivate bool
	}{
		{name: "true", isPrivate: true},
		{name: "false", isPrivate: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cloudID := fmt.Sprintf("cld_ipc_mock_%s", tc.name)
			server := newIsPrivateCloudMockServer(t, cloudID)

			config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name             = "ipc-mock-%s"
  cloud_provider   = "AWS"
  region           = "us-east-2"
  is_private_cloud = %t
}
`, tc.name, tc.isPrivate)

			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config: config,
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttr("anyscale_cloud.test", "is_private_cloud", fmt.Sprintf("%t", tc.isPrivate)),
						),
						// The headline gate: if the create response doesn't
						// echo what was planned, resource.Test's own apply
						// step fails with the framework's inconsistent-result
						// error before this field is even reached - so a
						// clean, empty post-apply plan is only reachable at
						// all once the round-trip is honest.
						ExpectNonEmptyPlan: false,
					},
				},
			})
		})
	}
}

// TestAccCloudResourceResource_IsPrivateRoundTrip_MockServer proves
// anyscale_cloud_resource's OWN is_private is unaffected by the
// anyscale_cloud bug above: unlike anyscale_cloud (a separate Cloud-level
// column the old Create never wrote), cloud_resource sends NetworkingMode
// directly on its own add_resource call and reads the same field back from
// the resources listing - a closed loop against one field, not two. Reuses
// newMultiResourceMockServer (resource_cloud_resource_multi_lifecycle_acc_test.go),
// which already threads NetworkingMode through add_resource -> resources
// faithfully, rather than a fresh mock that could accidentally paper over
// the exact seam under test.
func TestAccCloudResourceResource_IsPrivateRoundTrip_MockServer(t *testing.T) {
	const cloudID = "cld_ipc_cr_mock"
	server, _ := newMultiResourceMockServer(t, cloudID)

	config := testAccProviderBlock(server.URL) + testAccMultiResourceCloudBlock() + `
resource "anyscale_cloud_resource" "test" {
  cloud_id      = anyscale_cloud.test.id
  name          = "vm-aws-us-east-2"
  region        = "us-east-2"
  compute_stack = "VM"
  is_private    = true

` + awsConfigBlockLifecycle("ipc") + `

  object_storage {
    bucket_name = "bucket-ipc"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "is_private", "true"),
				),
				ExpectNonEmptyPlan: false,
			},
		},
	})
}
