package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// cloudResourceFileStorageMock is a stateful fake of one cloud's resource
// collection for anyscale_cloud_resource's in-place file_storage update. It
// applies the same PUT /resources semantics as newFileStorageUpdateMockServer
// (resource_cloud_acc_test.go): the body must be a bare JSON list, top-level
// keys the PUT sends replace the stored ones, keys it omits survive, except
// file_storage, which omission clears.
type cloudResourceFileStorageMock struct {
	t       *testing.T
	cloudID string

	mu       sync.Mutex
	stored   map[string]map[string]interface{} // by name
	order    []string
	putCount int
	lastPUT  string
}

func newCloudResourceFileStorageMockServer(t *testing.T, cloudID string) (*httptest.Server, *cloudResourceFileStorageMock) {
	t.Helper()
	m := &cloudResourceFileStorageMock{t: t, cloudID: cloudID, stored: map[string]map[string]interface{}{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on /api/v2/clouds/%s", r.Method, cloudID)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "name": "cres-fs-update", "provider": "GCP", "region": "us-central1", "status": "ready", "state": "ACTIVE", "compute_stack": "K8S"}}`, cloudID)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode add_resource body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		name, _ := req["name"].(string)
		m.mu.Lock()
		req["cloud_resource_id"] = "cldrsrc_mock_" + name
		req["is_default"] = false
		m.stored[name] = req
		m.order = append(m.order, name)
		body, _ := json.Marshal(map[string]interface{}{"result": req})
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			m.mu.Lock()
			results := make([]map[string]interface{}, 0, len(m.order))
			for _, n := range m.order {
				results = append(results, m.stored[n])
			}
			body, _ := json.Marshal(map[string]interface{}{
				"results":  results,
				"metadata": map[string]interface{}{"total": len(results), "next_paging_token": nil},
			})
			m.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			var sentList []map[string]interface{}
			if err := json.Unmarshal(raw, &sentList); err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = fmt.Fprint(w, `{"detail": "value is not a valid list"}`)
				return
			}
			if len(sentList) != 1 {
				t.Errorf("PUT /resources: expected exactly 1 element, got %d: %s", len(sentList), raw)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sent := sentList[0]
			m.mu.Lock()
			defer m.mu.Unlock()
			m.putCount++
			m.lastPUT = string(raw)
			var target map[string]interface{}
			for _, n := range m.order {
				if m.stored[n]["cloud_resource_id"] == sent["cloud_resource_id"] {
					target = m.stored[n]
				}
			}
			if target == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprintf(w, `{"detail": "cloud resource %v not found"}`, sent["cloud_resource_id"])
				return
			}
			for k, v := range sent {
				target[k] = v
			}
			if _, ok := sent["file_storage"]; !ok {
				target["file_storage"] = nil
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"result": {}}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s/resources", r.Method, cloudID)
		}
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/remove_resource", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("cloud_resource_name")
		m.mu.Lock()
		delete(m.stored, name)
		for i, n := range m.order {
			if n == name {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, m
}

func (m *cloudResourceFileStorageMock) puts() (int, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.putCount, m.lastPUT
}

// storedPVC returns the persistent_volume_claim the mock holds for name, or ""
// when file_storage is absent.
func (m *cloudResourceFileStorageMock) storedPVC(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	fs, _ := m.stored[name]["file_storage"].(map[string]interface{})
	pvc, _ := fs["persistent_volume_claim"].(string)
	return pvc
}

// TestAccCloudResourceResource_FileStorageInPlaceUpdate_MockServer is the
// anyscale_cloud_resource twin of anyscale_cloud's in-place file_storage tests
// (TestAccCloudResource_FileStorageAddIsUpdatable and siblings): adding
// file_storage to a live cloud_resource, then changing
// persistent_volume_claim, each plans an Update rather than a replace, sends
// one PUT /resources carrying the new value, keeps kubernetes_config in the
// PUT body so the round-trip does not wipe it server-side, and leaves a clean
// plan behind.
func TestAccCloudResourceResource_FileStorageInPlaceUpdate_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_cres_fs_update_mock"
	const resName = "k8s-gcp-us-central1-secondary"
	server, mock := newCloudResourceFileStorageMockServer(t, cloudID)

	config := func(pvc string, extra ...string) string {
		fileStorage := ""
		if pvc != "" {
			fileStorage = fmt.Sprintf(`
  file_storage {
    persistent_volume_claim = %q
  }
`, pvc)
		}
		return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud_resource" "test" {
  cloud_id       = %[1]q
  name           = %[2]q
  cloud_provider = "GCP"
  region         = "us-central1"
  compute_stack  = "K8S"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-cres-fs-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-cres-fs-update-bucket"
  }
%[3]s%[4]s}
`, cloudID, resName, fileStorage, strings.Join(extra, ""))
	}

	updateStep := func(pvc string, wantPUTs int) resource.TestStep {
		return resource.TestStep{
			Config: config(pvc),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("anyscale_cloud_resource.test", plancheck.ResourceActionUpdate),
				},
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "file_storage.persistent_volume_claim", pvc),
				func(*terraform.State) error {
					n, body := mock.puts()
					if n != wantPUTs {
						return fmt.Errorf("expected %d PUT /resources in total, got %d", wantPUTs, n)
					}
					if !regexp.MustCompile(`"persistent_volume_claim"\s*:\s*"` + regexp.QuoteMeta(pvc) + `"`).MatchString(body) {
						return fmt.Errorf("PUT body missing persistent_volume_claim=%s: %s", pvc, body)
					}
					if !regexp.MustCompile(`"kubernetes_config"\s*:\s*\{[^}]*"anyscale_operator_iam_identity"`).MatchString(body) {
						return fmt.Errorf("PUT body dropped kubernetes_config, which would wipe it server-side: %s", body)
					}
					if got := mock.storedPVC(resName); got != pvc {
						return fmt.Errorf("mock stored persistent_volume_claim = %q, want %q", got, pvc)
					}
					return nil
				},
			),
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("anyscale_cloud_resource.test", "file_storage"),
					func(*terraform.State) error {
						if n, _ := mock.puts(); n != 0 {
							return fmt.Errorf("create must not PUT /resources, got %d", n)
						}
						return nil
					},
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			updateStep("ray-shared-pvc-v1", 1),
			updateStep("ray-shared-pvc-v2", 2),
			// A timeouts-only change is the other in-place update this resource
			// allows. It must plan an Update, send nothing, and land the new value
			// in state; before the fix, Update kept the prior timeouts and Terraform
			// rejected the apply as inconsistent.
			{
				Config: config("ray-shared-pvc-v2", `
  timeouts {
    create = "45m"
  }
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud_resource.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud_resource.test", "timeouts.create", "45m"),
					func(*terraform.State) error {
						if n, _ := mock.puts(); n != 2 {
							return fmt.Errorf("a timeouts-only change must not PUT /resources; got %d in total, want 2", n)
						}
						return nil
					},
				),
			},
		},
	})
}
