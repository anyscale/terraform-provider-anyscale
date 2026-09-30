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
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// newFileStorageUpdateMockServer builds a stateful mock exercising D2's
// in-place update path: GET /resources returns the currently-stored
// deployment; PUT /resources requires a bare JSON list (422 on anything
// else, matching the real API's "value is not a valid list" rejection of a
// single object), then shallow-merges the sent object's top-level keys into
// the stored one - a key the PUT omits survives untouched, a key it sends
// replaces the old value wholesale, including any inner field that key's own
// old value carried but the new one didn't. That is exactly the backend's
// documented asymmetry for sibling blocks (docs/decisions/
// cloud-file-storage-lifecycle/README.md's Gate 1 results: "top-level block
// omitted -> preserved" vs. "field omitted inside a block that IS sent ->
// wiped"), reproduced without hand-simulating the backend's real merge
// logic.
//
// file_storage itself is the one documented exception to "omitted ->
// preserved": G1.2 confirmed omitting it from the PUT clears it server-side
// (the design's clear-by-omission path), so this mock special-cases that one
// key to clear-on-omit rather than preserve-on-omit.
//
// putHook, when non-nil, runs before the merge on every PUT and may
// substitute a failure response (running-clusters / anyscale-managed /
// infrastructure-manager-changed all arrive as an opaque 400, told apart
// only by body substring) by returning handled=true; handled=false falls
// through to the normal 200+merge path. lastPUTBody returns the raw body of
// the most recent PUT, for tests that need to assert on exactly what the
// provider sent rather than only on what the mock now stores.
func newFileStorageUpdateMockServer(
	t *testing.T,
	cloudID string,
	cloudJSON string,
	initialResourceJSON string,
	addResourceFileStorageJSON string,
	putHook func(sent map[string]interface{}) (handled bool, statusCode int, body string),
) (server *httptest.Server, lastPUTBody func() string) {
	t.Helper()
	mux := http.NewServeMux()

	var mu sync.Mutex
	var stored map[string]interface{}
	if err := json.Unmarshal([]byte(initialResourceJSON), &stored); err != nil {
		t.Fatalf("newFileStorageUpdateMockServer: invalid initialResourceJSON: %v", err)
	}
	var lastBody atomic.Value
	lastBody.Store("")

	marshalResults := func() string {
		mu.Lock()
		defer mu.Unlock()
		b, err := json.Marshal([]map[string]interface{}{stored})
		if err != nil {
			t.Fatalf("newFileStorageUpdateMockServer: marshal stored resource: %v", err)
		}
		return string(b)
	}

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.Method {
		case http.MethodPost:
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		case http.MethodGet:
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds", r.Method)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s", r.Method, cloudID)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"results": %s, "metadata": {"total": 1, "next_paging_token": null}}`, marshalResults())
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			lastBody.Store(string(raw))

			var sentList []map[string]interface{}
			if err := json.Unmarshal(raw, &sentList); err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = fmt.Fprint(w, `{"detail": "value is not a valid list"}`)
				return
			}
			if len(sentList) != 1 {
				t.Fatalf("PUT /resources: expected exactly 1 element, got %d: %s", len(sentList), raw)
			}
			sent := sentList[0]

			if putHook != nil {
				if handled, code, body := putHook(sent); handled {
					w.WriteHeader(code)
					_, _ = fmt.Fprint(w, body)
					return
				}
			}

			mu.Lock()
			for k, v := range sent {
				stored[k] = v
			}
			if _, sentFileStorage := sent["file_storage"]; !sentFileStorage {
				stored["file_storage"] = nil
			}
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"result": {}}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s/resources", r.Method, cloudID)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {"cloud_deployment_id": "cldrsrc_mock_default", "cloud_resource_id": "cldrsrc_mock_default", "file_storage": %s}}`, addResourceFileStorageJSON)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() string {
		v, _ := lastBody.Load().(string)
		return v
	}
}

// TestAccCloudResource_FileStorageAddIsUpdatable proves that, for D2
// (docs/decisions/cloud-file-storage-lifecycle/README.md), on an
// already-live anyscale_cloud with no file_storage block, adding one
// that sets only persistent_volume_claim now plans and applies an in-place
// Update, not a replace. This is the direct inversion of the pre-D2 pinning
// test of the same shape (every file_storage attribute carried an
// unconditional stringplanmodifier.RequiresReplace(), and mount_path's
// Computed+Default separately forced replace on its own absent->present
// change) - neither marker is a static-schema property (`terraform providers
// schema -json` has no requires-replace field; it's computed per-plan), so
// this can only be proven with a real plan, which is what this test runs.
func TestAccCloudResource_FileStorageAddIsUpdatable(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_add_updatable_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-add-updatable", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"],
			"redis_endpoint": "redis.ray-system.svc.cluster.local:6379"
		},
		"object_storage": {"bucket_name": "tfacc-filestorage-updatable-bucket"}
	}`

	server, lastPUTBody := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null", nil)
	baseConfig := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "filestorage-add-updatable"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-filestorage-updatable-bucket"
  }
}
`
	withFileStorage := baseConfig[:len(baseConfig)-2] + `
  file_storage {
    persistent_volume_claim = "ray-shared-pvc"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Baseline: cloud already live, no file_storage declared.
				Config: baseConfig,
				Check:  resource.TestCheckNoResourceAttr("anyscale_cloud.test", "file_storage"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				// Declaring file_storage for the first time, setting only
				// persistent_volume_claim, plans and applies an in-place
				// Update - not the pre-D2 full replace.
				Config: withFileStorage,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc"),
					func(_ *terraform.State) error {
						// Independent of Terraform state: the mock's stored
						// deployment is what the PUT actually persisted, so
						// this proves the new file_storage value reached the
						// wire, not merely that the plan looked right.
						sent := lastPUTBody()
						if sent == "" {
							return fmt.Errorf("expected a PUT to /resources, got none")
						}
						if !regexp.MustCompile(`"persistent_volume_claim"\s*:\s*"ray-shared-pvc"`).MatchString(sent) {
							return fmt.Errorf("PUT body missing persistent_volume_claim=ray-shared-pvc: %s", sent)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccCloudResource_FileStorageUpdateDoesNotWipeSiblings proves that, for
// D2, updating file_storage on a live K8S cloud must not collaterally wipe
// kubernetes_config.redis_endpoint or object_storage -
// D2's round-trip PUT is a full-spec replace, so any sibling field the
// provider fails to echo back is cleared by the API, not merely left stale in
// state. The check reads the mock's stored deployment directly over raw HTTP,
// bypassing Terraform state entirely: config blocks are deliberately not
// Read-refreshed (see CLAUDE.md's Import Round-trip safety section), so a
// state-only assertion could pass even if the PUT body itself had dropped a
// field the mock happened not to overwrite.
func TestAccCloudResource_FileStorageUpdateDoesNotWipeSiblings(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_nowipe_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-nowipe", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"],
			"redis_endpoint": "redis.ray-system.svc.cluster.local:6379"
		},
		"object_storage": {"bucket_name": "tfacc-filestorage-nowipe-bucket"},
		"file_storage": {"persistent_volume_claim": "ray-shared-pvc-v1"}
	}`

	server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null", nil)
	configWithPVC := func(pvc string) string {
		return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "filestorage-nowipe"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-filestorage-nowipe-bucket"
  }

  file_storage {
    persistent_volume_claim = %[1]q
  }
}
`, pvc)
	}

	checkSiblingsIntact := func(_ *terraform.State) error {
		resp, err := http.Get(server.URL + "/api/v2/clouds/" + cloudID + "/resources")
		if err != nil {
			return fmt.Errorf("GET /resources: %w", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading /resources response: %w", err)
		}
		body := string(raw)
		for _, want := range []string{
			`"redis_endpoint"`,
			"redis.ray-system.svc.cluster.local:6379",
			`"bucket_name"`,
			"tfacc-filestorage-nowipe-bucket",
			"ray-shared-pvc-v2",
		} {
			if !strings.Contains(body, want) {
				return fmt.Errorf("expected %q to survive the file_storage update, stored deployment: %s", want, body)
			}
		}
		return nil
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configWithPVC("ray-shared-pvc-v1"),
				Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v1"),
			},
			{
				// Changing only persistent_volume_claim must not clear
				// redis_endpoint or bucket_name from the live deployment.
				Config: configWithPVC("ray-shared-pvc-v2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v2"),
					checkSiblingsIntact,
				),
			},
		},
	})
}

// TestAccCloudResource_FileStorageClearingWorks proves that, for D2, removing
// a previously-declared file_storage block from config plans and applies an
// in-place Update that clears it - D2's round-trip PUT omits the key entirely
// when planFileStorage is nil, and per the design doc's own Gate-1 evidence
// (G1.2), omission clears file_storage on the live deployment rather than
// leaving the last value in place (the opposite of how the sibling blocks
// behave on omission). Sibling fields are asserted intact for the same
// reason as TestAccCloudResource_FileStorageUpdateDoesNotWipeSiblings above:
// clearing file_storage must not collaterally wipe kubernetes_config or
// object_storage.
func TestAccCloudResource_FileStorageClearingWorks(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_clear_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-clear", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"],
			"redis_endpoint": "redis.ray-system.svc.cluster.local:6379"
		},
		"object_storage": {"bucket_name": "tfacc-filestorage-clear-bucket"},
		"file_storage": {"persistent_volume_claim": "ray-shared-pvc"}
	}`

	server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null", nil)
	baseConfig := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "filestorage-clear"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-filestorage-clear-bucket"
  }
`
	withFileStorage := baseConfig + `
  file_storage {
    persistent_volume_claim = "ray-shared-pvc"
  }
}
`
	withoutFileStorage := baseConfig + `
}
`

	checkCleared := func(_ *terraform.State) error {
		resp, err := http.Get(server.URL + "/api/v2/clouds/" + cloudID + "/resources")
		if err != nil {
			return fmt.Errorf("GET /resources: %w", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading /resources response: %w", err)
		}
		body := string(raw)
		if strings.Contains(body, "persistent_volume_claim") {
			return fmt.Errorf("expected file_storage to be cleared, stored deployment still has it: %s", body)
		}
		for _, want := range []string{
			"redis.ray-system.svc.cluster.local:6379",
			"tfacc-filestorage-clear-bucket",
		} {
			if !strings.Contains(body, want) {
				return fmt.Errorf("expected %q to survive clearing file_storage, stored deployment: %s", want, body)
			}
		}
		return nil
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: withFileStorage,
				Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc"),
			},
			{
				// Removing the block entirely plans and applies an Update
				// that clears file_storage, not a replace and not a no-op.
				Config: withoutFileStorage,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("anyscale_cloud.test", "file_storage"),
					checkCleared,
				),
			},
		},
	})
}

// TestAccCloudResource_FileStorageImportRecoversValue proves that, for D2,
// importing a cloud that already has file_storage set recovers it, via
// requiredImportConfigBlocks' flattenFileStorage call. This
// is a COLD import (no preceding Create in this test) - per CLAUDE.md's own
// documented ImportStatePersist gotcha, an ImportState step's recovered state
// is discarded at the end of the step verifying it, so the only place that
// can see what import actually recovered is ImportStateCheck run inside that
// same step, asserted directly on the imported InstanceState's Attributes.
// ImportStateVerify is deliberately not used here: there is no prior Create
// state in this test to verify against.
func TestAccCloudResource_FileStorageImportRecoversValue(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_import_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-import", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"]
		},
		"file_storage": {"persistent_volume_claim": "ray-shared-pvc-imported"}
	}`

	server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null", nil)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "filestorage-import"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        config,
				ResourceName:  "anyscale_cloud.test",
				ImportState:   true,
				ImportStateId: cloudID,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					attrs := states[0].Attributes
					if got := attrs["file_storage.persistent_volume_claim"]; got != "ray-shared-pvc-imported" {
						return fmt.Errorf("criterion 6a FAILED: import did not recover file_storage.persistent_volume_claim, got %q: %+v",
							got, attrs)
					}
					return nil
				},
			},
		},
	})
}

// TestAccCloudResource_FileStorageUpdateRunningClustersError proves that,
// for D2, when the backend refuses a file_storage update because clusters
// are running, the practitioner sees a designed
// diagnostic naming that cause, not an opaque 400 body. The "active
// clusters" substring match in addFileStorageUpdateError is sourced from
// backend source rather than a captured response - G1.3 was never run (see
// the Gate 1 results table in
// docs/decisions/cloud-file-storage-lifecycle/README.md) - so this test
// proves only the provider's own branching on that substring, not that the
// real backend sends it; the code comment documenting that gap is a
// deliberate, accepted degradation and must not be read as something this
// test could fix.
//
// putHook injects the two 400 bodies the mock would otherwise never
// produce. Both are asserted in the same test since they are two branches of
// the same match, and proving one without the other cannot show the
// substring match is selective rather than matching everything.
func TestAccCloudResource_FileStorageUpdateRunningClustersError(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_clusters_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-clusters", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"]
		},
		"object_storage": {"bucket_name": "tfacc-filestorage-clusters-bucket"},
		"file_storage": {"persistent_volume_claim": "ray-shared-pvc-v1"}
	}`

	configWithPVC := func(serverURL, pvc string) string {
		return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "filestorage-clusters"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-filestorage-clusters-bucket"
  }

  file_storage {
    persistent_volume_claim = %[1]q
  }
}
`, pvc)
	}

	t.Run("active_clusters_substring_fires_designed_diagnostic", func(t *testing.T) {
		server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null",
			func(sent map[string]interface{}) (bool, int, string) {
				return true, http.StatusBadRequest, `{"detail": "Cannot update cloud resource: active clusters are still running"}`
			})

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: ProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					// Create goes through add_resource, never PUT /resources,
					// so the putHook does not fire here - this step must
					// succeed to give the second step something to Update.
					Config: configWithPVC(server.URL, "ray-shared-pvc-v1"),
					Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v1"),
				},
				{
					// Changing the value forces an Update, which does go
					// through the PUT the putHook intercepts.
					Config:      configWithPVC(server.URL, "ray-shared-pvc-v2"),
					ExpectError: regexp.MustCompile(`(?s)Cannot Update file_storage While Clusters Are Running.*active\s+clusters`),
				},
			},
		})
	})

	t.Run("unrelated_400_falls_through_to_generic_error", func(t *testing.T) {
		server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON, resourcesJSON, "null",
			func(sent map[string]interface{}) (bool, int, string) {
				return true, http.StatusBadRequest, `{"detail": "some unrelated validation failure"}`
			})

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: ProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: configWithPVC(server.URL, "ray-shared-pvc-v1"),
					Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v1"),
				},
				{
					Config:      configWithPVC(server.URL, "ray-shared-pvc-v2"),
					ExpectError: regexp.MustCompile(`(?s)API Request Failed.*some\s+unrelated\s+validation\s+failure`),
				},
			},
		})
	})
}

// TestAccCloudResource_FileStorageManagedCloudRefusal proves that, for D2, a
// file_storage change on a cloud created with `anyscale cloud setup` (rather
// than registered) is refused, both at plan time
// (refuseFileStorageChangeOnManagedCloud/isAnyscaleManaged, so the
// practitioner sees it before an apply starts writing) and, as a backstop, at
// apply time if the plan-time check didn't catch it
// (addFileStorageUpdateError's "anyscale-managed" substring branch, mirroring
// TestAccCloudResource_FileStorageUpdateRunningClustersError's shape).
// isAnyscaleManaged treats any of three provenance
// fields as sufficient: AWS's cloudformation_id, or either of GCP's
// deployment_manager_id/infrastructure_manager_id - all three are exercised,
// plus a negative control (none set) proving the guard is selective rather
// than firing unconditionally.
func TestAccCloudResource_FileStorageManagedCloudRefusal(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_filestorage_managed_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "filestorage-managed", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)

	configWithPVC := func(serverURL, pvc string) string {
		return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "filestorage-managed"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = "tfacc-filestorage-managed-bucket"
  }

  file_storage {
    persistent_volume_claim = %[1]q
  }
}
`, pvc)
	}

	resourcesJSONWithManagedField := func(managedFieldJSON string) string {
		return fmt.Sprintf(`{
			"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
			"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
			"kubernetes_config": {
				"anyscale_operator_iam_identity": "tfacc-gke-operator@my-gcp-project.iam.gserviceaccount.com",
				"zones": ["us-central1-a", "us-central1-b"]
			},
			"object_storage": {"bucket_name": "tfacc-filestorage-managed-bucket"},
			"file_storage": {"persistent_volume_claim": "ray-shared-pvc-v1"}
			%s
		}`, managedFieldJSON)
	}

	const managedDiagnosticPattern = `(?s)Cannot Update file_storage on an Anyscale-Managed Cloud`

	planTimeCases := []struct {
		name             string
		managedFieldJSON string
		wantRefused      bool
	}{
		{
			name:             "aws_cloudformation_id",
			managedFieldJSON: `, "aws_config": {"cloudformation_id": "cfn-mock-managed-id"}`,
			wantRefused:      true,
		},
		{
			name:             "gcp_deployment_manager_id",
			managedFieldJSON: `, "gcp_config": {"deployment_manager_id": "dm-mock-managed-id"}`,
			wantRefused:      true,
		},
		{
			name:             "gcp_infrastructure_manager_id",
			managedFieldJSON: `, "gcp_config": {"infrastructure_manager_id": "im-mock-managed-id"}`,
			wantRefused:      true,
		},
		{
			name:             "not_managed_no_refusal",
			managedFieldJSON: ``,
			wantRefused:      false,
		},
	}

	for _, tc := range planTimeCases {
		t.Run("plan_time_"+tc.name, func(t *testing.T) {
			server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON,
				resourcesJSONWithManagedField(tc.managedFieldJSON), "null", nil)

			steps := []resource.TestStep{
				{
					// Create is unguarded (ModifyPlan skips a null-state plan), so this
					// must succeed regardless of tc.wantRefused to give the second step
					// something to Update.
					Config: configWithPVC(server.URL, "ray-shared-pvc-v1"),
					Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v1"),
				},
			}
			if tc.wantRefused {
				steps = append(steps, resource.TestStep{
					Config:      configWithPVC(server.URL, "ray-shared-pvc-v2"),
					ExpectError: regexp.MustCompile(managedDiagnosticPattern),
				})
			} else {
				steps = append(steps, resource.TestStep{
					Config: configWithPVC(server.URL, "ray-shared-pvc-v2"),
					Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v2"),
				})
			}

			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps:                    steps,
			})
		})
	}

	t.Run("apply_time_anyscale_managed_substring_backstop", func(t *testing.T) {
		// live GET carries no managed field, so the plan-time guard does not fire -
		// this isolates the apply-time addFileStorageUpdateError translation as its
		// own backstop, mirroring TestAccCloudResource_FileStorageUpdateRunningClustersError's shape.
		server, _ := newFileStorageUpdateMockServer(t, cloudID, cloudJSON,
			resourcesJSONWithManagedField(""), "null",
			func(sent map[string]interface{}) (bool, int, string) {
				return true, http.StatusBadRequest, `{"detail": "Cannot update: cloud resource is anyscale-managed"}`
			})

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: ProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: configWithPVC(server.URL, "ray-shared-pvc-v1"),
					Check:  resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "ray-shared-pvc-v1"),
				},
				{
					Config:      configWithPVC(server.URL, "ray-shared-pvc-v2"),
					ExpectError: regexp.MustCompile(managedDiagnosticPattern),
				},
			},
		})
	})
}

// newMountPathMockCloudServer builds a mock like newC3MockCloudServer, but
// with an add_resource response that DOES carry file_storage - the shared
// helper's stub never does, so it can't exercise the Create-time
// mergeFileStorageDerivedFields resolution the two tests below need.
func newMountPathMockCloudServer(t *testing.T, cloudID, cloudJSON, resourcesJSON, addResourceFileStorageJSON string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.Method {
		case http.MethodPost:
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		case http.MethodGet:
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds", r.Method)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s", r.Method, cloudID)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results": %s, "metadata": {"total": 1, "next_paging_token": null}}`, resourcesJSON)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {"cloud_deployment_id": "cldrsrc_mock_default", "cloud_resource_id": "cldrsrc_mock_default", "file_storage": %s}}`, addResourceFileStorageJSON)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccCloudResource_MountPathOmittedDoesNotForceReplace pins D1's Gate 2:
// a real resource.Test proving the Framework/Core plan-modifier contract,
// since a plain mapping-function unit test can't exercise it. Before D1,
// mount_path carried only RequiresReplace(): an Optional+Computed attribute
// the config omits plans to Unknown on every subsequent apply, and
// RequiresReplace treats "planned Unknown, prior state known" as a
// difference - forcing a destroy-and-recreate of an already-live cloud on
// every single plan, with no real config change.
// stringplanmodifier.UseStateForUnknown(), placed before RequiresReplace()
// in PlanModifiers, resolves the Unknown to the prior state value first, so
// RequiresReplace sees no diff - mirroring the pre-existing mount_targets
// list attribute's identical ordering.
func TestAccCloudResource_MountPathOmittedDoesNotForceReplace(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_mountpath_omit_noreplace_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "mountpath-omit-noreplace", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"
	}`, cloudID)
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"compute_stack": "VM", "region": "us-central1",
		"gcp_config": {
			"project_id": "my-gcp-project",
			"provider_name": "projects/my-gcp-project/serviceAccounts/anyscale@my-gcp-project.iam.gserviceaccount.com",
			"vpc_name": "vpc-mountpath",
			"subnet_names": ["subnet-mountpath"],
			"anyscale_service_account_email": "anyscale@my-gcp-project.iam.gserviceaccount.com",
			"cluster_service_account_email": "cluster@my-gcp-project.iam.gserviceaccount.com"
		},
		"object_storage": {"bucket_name": "tfacc-mountpath-omit-bucket"},
		"file_storage": {"file_storage_id": "filestore-omit-test", "mount_path": "/mnt/filestore-real"}
	}]`
	addResourceFileStorageJSON := `{"file_storage_id": "filestore-omit-test", "mount_path": "/mnt/filestore-real"}`

	server := newMountPathMockCloudServer(t, cloudID, cloudJSON, resourcesJSON, addResourceFileStorageJSON)
	resourceName := "anyscale_cloud.test"
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "mountpath-omit-noreplace"
  cloud_provider = "GCP"
  compute_stack  = "VM"
  region         = "us-central1"

  gcp_config {
    project_id                     = "my-gcp-project"
    provider_name                  = "projects/my-gcp-project/serviceAccounts/anyscale@my-gcp-project.iam.gserviceaccount.com"
    vpc_name                       = "vpc-mountpath"
    subnet_names                   = ["subnet-mountpath"]
    controlplane_service_account_email = "anyscale@my-gcp-project.iam.gserviceaccount.com"
    dataplane_service_account_email    = "cluster@my-gcp-project.iam.gserviceaccount.com"
  }

  object_storage {
    bucket_name = "tfacc-mountpath-omit-bucket"
  }

  # mount_path deliberately omitted - the backend resolves and returns a
  # real value; this config never sets it.
  file_storage {
    file_storage_id = "filestore-omit-test"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Create resolves mount_path from the real add_resource
				// response (D1's mergeFileStorageDerivedFields path), not a
				// fabricated default.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "file_storage.file_storage_id", "filestore-omit-test"),
					resource.TestCheckResourceAttr(resourceName, "file_storage.mount_path", "/mnt/filestore-real"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				// G2.1: re-applying the SAME config (mount_path still
				// omitted) must plan a no-op, not a forced replace.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}
