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
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// derivingBackend is a stateful fake of one GCP Kubernetes cloud that follows
// the real backend where it matters for file_storage:
//
//   - add_resource and PUT /resources derive file_storage.mount_targets and
//     file_storage.mount_path from file_storage_id, but only when the request
//     carries no mount_targets (the backend skips derivation once any are
//     given, so a stale address sent with a new ID is stored as-is);
//   - PUT /resources answers 200 with a JSON null body (the real route returns
//     None), so the written values can only be read back with a GET;
//   - PUT /resources clears file_storage when the request omits it.
//
// The fixtures the neighbouring file_storage tests use answer the PUT with an
// empty result object and never derive, so none of them can show a derived
// value going stale.
type derivingBackend struct {
	t       *testing.T
	cloudID string

	mu        sync.Mutex
	stored    map[string]map[string]interface{} // by resource name
	order     []string
	puts      []string
	posts     atomic.Int32
	resources atomic.Int32 // GET /resources calls
	// failResourcesList makes GET /resources answer 500 while set.
	failResourcesList atomic.Bool
	// hostedResources makes GET /resources answer the 400 the backend gives for Anyscale-hosted
	// clouds while set.
	hostedResources atomic.Bool
	// noDeriveOnAdd turns derivation off for add_resource only, so a resource is created with the
	// file_storage_id but no mount_targets, as when the address was not yet discoverable.
	noDeriveOnAdd atomic.Bool
}

func (b *derivingBackend) derive(fs map[string]interface{}) {
	if fs == nil {
		return
	}
	id, _ := fs["file_storage_id"].(string)
	targets, _ := fs["mount_targets"].([]interface{})
	if id == "" || len(targets) > 0 {
		return
	}
	fs["mount_targets"] = []interface{}{map[string]interface{}{"address": "ip-of-" + id}}
	if p, _ := fs["mount_path"].(string); p == "" {
		fs["mount_path"] = "/share-of-" + id
	}
}

func newDerivingBackend(t *testing.T, cloudID, cloudName string) (*httptest.Server, *derivingBackend) {
	t.Helper()
	b := &derivingBackend{t: t, cloudID: cloudID, stored: map[string]map[string]interface{}{}}
	mux := http.NewServeMux()

	cloudJSON := fmt.Sprintf(`{"id": %q, "name": %q, "provider": "GCP", "region": "us-central1", "status": "ready", "state": "ACTIVE", "compute_stack": "K8S"}`, cloudID, cloudName)

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.Method {
		case http.MethodPost:
			b.posts.Add(1)
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
	mux.HandleFunc("/api/v2/machine_pools/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {"machine_pools": []}}`)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode add_resource body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		name, _ := req["name"].(string)
		fs, _ := req["file_storage"].(map[string]interface{})
		if !b.noDeriveOnAdd.Load() {
			b.derive(fs)
		}
		b.mu.Lock()
		req["cloud_resource_id"] = "cldrsrc_mock_" + name
		req["is_default"] = len(b.order) == 0
		b.stored[name] = req
		b.order = append(b.order, name)
		body, _ := json.Marshal(map[string]interface{}{"result": req})
		b.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			b.resources.Add(1)
			if b.hostedResources.Load() {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, `{"error": {"detail": "Cloud resources for Anyscale-hosted clouds can not be fetched."}}`)
				return
			}
			if b.failResourcesList.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = fmt.Fprint(w, `{"error": {"detail": "resources unavailable"}}`)
				return
			}
			b.mu.Lock()
			results := make([]map[string]interface{}, 0, len(b.order))
			for _, n := range b.order {
				results = append(results, b.stored[n])
			}
			body, _ := json.Marshal(map[string]interface{}{
				"results":  results,
				"metadata": map[string]interface{}{"total": len(results), "next_paging_token": nil},
			})
			b.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			var sentList []map[string]interface{}
			if err := json.Unmarshal(raw, &sentList); err != nil || len(sentList) != 1 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = fmt.Fprint(w, `{"detail": "value is not a valid list"}`)
				return
			}
			sent := sentList[0]
			b.mu.Lock()
			defer b.mu.Unlock()
			b.puts = append(b.puts, string(raw))
			var target map[string]interface{}
			for _, n := range b.order {
				if b.stored[n]["cloud_resource_id"] == sent["cloud_resource_id"] {
					target = b.stored[n]
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
			fs, hasFS := sent["file_storage"].(map[string]interface{})
			if !hasFS {
				target["file_storage"] = nil
			} else {
				b.derive(fs)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `null`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s/resources", r.Method, cloudID)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, b
}

func (b *derivingBackend) putBodies() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.puts...)
}

// storedFileStorage returns the file_storage the backend holds for the named
// resource.
func (b *derivingBackend) storedFileStorage(name string) (id, address, mountPath string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fs, _ := b.stored[name]["file_storage"].(map[string]interface{})
	id, _ = fs["file_storage_id"].(string)
	mountPath, _ = fs["mount_path"].(string)
	if targets, _ := fs["mount_targets"].([]interface{}); len(targets) > 0 {
		first, _ := targets[0].(map[string]interface{})
		address, _ = first["address"].(string)
	}
	return id, address, mountPath
}

const (
	derivedFSOperator = "tfacc-derived-fs@my-gcp-project.iam.gserviceaccount.com"
	derivedFSBucket   = "tfacc-derived-fs-bucket"
)

// derivedFSKind selects which resource a scenario drives; both share the
// file_storage schema and Update path.
type derivedFSKind struct {
	address string // terraform address
	config  func(serverURL, cloudID, resName, fileStorage, extra string) string
}

var derivedFSCloud = derivedFSKind{
	address: "anyscale_cloud.test",
	config: func(serverURL, cloudID, resName, fileStorage, extra string) string {
		return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = %[1]q
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = %[2]q
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = %[3]q
  }
%[4]s%[5]s}
`, resName, derivedFSOperator, derivedFSBucket, fileStorage, extra)
	},
}

var derivedFSCloudResource = derivedFSKind{
	address: "anyscale_cloud_resource.test",
	config: func(serverURL, cloudID, resName, fileStorage, extra string) string {
		return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud_resource" "test" {
  cloud_id       = %[1]q
  name           = %[2]q
  cloud_provider = "GCP"
  region         = "us-central1"
  compute_stack  = "K8S"

  kubernetes_config {
    anyscale_operator_iam_identity = %[3]q
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = %[4]q
  }
%[5]s%[6]s}
`, cloudID, resName, derivedFSOperator, derivedFSBucket, fileStorage, extra)
	},
}

func derivedFSBlock(id string) string {
	if id == "" {
		return ""
	}
	return fmt.Sprintf(`
  file_storage {
    file_storage_id = %q
  }
`, id)
}

var (
	fsMountTargets = tfjsonpath.New("file_storage").AtMapKey("mount_targets")
	fsMountPath    = tfjsonpath.New("file_storage").AtMapKey("mount_path")
)

// runDerivedFileStorageIDChange drives the headline scenario. Changing
// file_storage_id on a live resource whose config omits mount_targets and
// mount_path must make both unknown in the plan, so Update neither sends the
// old file system's address with the new ID nor records null; they must end
// up equal to what the backend derived for the new ID. The final step is the
// positive control on the same path: a change that leaves file_storage_id
// alone keeps the derived values, known and unchanged, with no PUT.
func runDerivedFileStorageIDChange(t *testing.T, kind derivedFSKind, cloudID, resName string) {
	server, backend := newDerivingBackend(t, cloudID, resName)
	// backendName is the name the mock stores the resource under: add_resource's request name,
	// which anyscale_cloud builds from compute stack, provider and region.
	backendName := resName
	if kind.address == derivedFSCloud.address {
		backendName = "k8s-gcp-us-central1"
	}

	cfg := func(id, extra string) string {
		return kind.config(server.URL, cloudID, resName, derivedFSBlock(id), extra)
	}
	assertDerived := func(id string) resource.TestCheckFunc {
		return resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(kind.address, "file_storage.file_storage_id", id),
			resource.TestCheckResourceAttr(kind.address, "file_storage.mount_targets.#", "1"),
			resource.TestCheckResourceAttr(kind.address, "file_storage.mount_targets.0.address", "ip-of-"+id),
			resource.TestCheckResourceAttr(kind.address, "file_storage.mount_path", "/share-of-"+id),
			func(*terraform.State) error {
				gotID, gotAddr, gotPath := backend.storedFileStorage(backendName)
				if gotID != id || gotAddr != "ip-of-"+id || gotPath != "/share-of-"+id {
					return fmt.Errorf("backend holds file_storage {%s, %s, %s}, want a consistent set for %s", gotID, gotAddr, gotPath, id)
				}
				return nil
			},
		)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:           cfg("fs-old", ""),
				Check:            assertDerived("fs-old"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				Config: cfg("fs-new", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(kind.address, plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue(kind.address, fsMountTargets),
						plancheck.ExpectUnknownValue(kind.address, fsMountPath),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					assertDerived("fs-new"),
					func(*terraform.State) error {
						puts := backend.putBodies()
						if len(puts) != 1 {
							return fmt.Errorf("want exactly 1 PUT /resources, got %d", len(puts))
						}
						if strings.Contains(puts[0], "ip-of-fs-old") {
							return fmt.Errorf("PUT carried the previous file system's mount target: %s", puts[0])
						}
						return nil
					},
				),
			},
			// Positive control: file_storage_id unchanged, so the derived values carry over from
			// state (known in the plan, no PUT) while another in-place change applies.
			{
				Config: cfg("fs-new", `
  timeouts {
    create = "45m"
  }
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(kind.address, plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue(kind.address, fsMountTargets, knownvalue.ListSizeExact(1)),
						plancheck.ExpectKnownValue(kind.address, fsMountPath, knownvalue.StringExact("/share-of-fs-new")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					assertDerived("fs-new"),
					func(*terraform.State) error {
						if n := len(backend.putBodies()); n != 1 {
							return fmt.Errorf("a change that leaves file_storage alone must not PUT /resources; %d PUTs in total, want 1", n)
						}
						return nil
					},
				),
			},
		},
	})
}

// runDerivedFileStorageBlockAdded covers the second variant: the resource has
// no file_storage, so there is no prior value to carry, and the block is then
// added with only file_storage_id. The derived values must be filled from the
// backend instead of being recorded as null.
func runDerivedFileStorageBlockAdded(t *testing.T, kind derivedFSKind, cloudID, resName string) {
	server, _ := newDerivingBackend(t, cloudID, resName)
	cfg := func(id string) string {
		return kind.config(server.URL, cloudID, resName, derivedFSBlock(id), "")
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(""),
				Check:  resource.TestCheckNoResourceAttr(kind.address, "file_storage"),
			},
			{
				Config: cfg("fs-added"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(kind.address, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(kind.address, "file_storage.mount_targets.#", "1"),
					resource.TestCheckResourceAttr(kind.address, "file_storage.mount_targets.0.address", "ip-of-fs-added"),
					resource.TestCheckResourceAttr(kind.address, "file_storage.mount_path", "/share-of-fs-added"),
				),
			},
		},
	})
}

// runFileStorageUpdateKeepsNullMountTargets covers state that holds a file_storage_id but a null
// mount_targets (the address was not derivable when the resource was created). An unrelated edit
// to the block then reaches Update with a known null in the plan; recording the value the backend
// derived in the PUT would change a planned null and fail the apply as inconsistent. Only slots
// the plan left unknown may be filled.
func runFileStorageUpdateKeepsNullMountTargets(t *testing.T, kind derivedFSKind, cloudID, resName string) {
	server, backend := newDerivingBackend(t, cloudID, resName)
	backend.noDeriveOnAdd.Store(true)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: kind.config(server.URL, cloudID, resName, derivedFSBlock("fs-a"), ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(kind.address, "file_storage.file_storage_id", "fs-a"),
					resource.TestCheckResourceAttr(kind.address, "file_storage.mount_targets.#", "0"),
				),
			},
			{
				PreConfig: func() { backend.noDeriveOnAdd.Store(false) },
				Config: kind.config(server.URL, cloudID, resName, `
  file_storage {
    file_storage_id = "fs-a"
    mount_path      = "/explicit"
  }
`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(kind.address, plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr(kind.address, "file_storage.mount_path", "/explicit"),
			},
		},
	})
}

func TestAccCloudResource_FileStorageUpdateKeepsNullMountTargets(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	runFileStorageUpdateKeepsNullMountTargets(t, derivedFSCloud, "cld_fs_nullmt_mock", UniqueName(t, "fsnullmt"))
}

func TestAccCloudResourceResource_FileStorageUpdateKeepsNullMountTargets(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	runFileStorageUpdateKeepsNullMountTargets(t, derivedFSCloudResource, "cld_fs_nullmt_res_mock", UniqueName(t, "fsnullmtres"))
}

func TestAccCloudResource_FileStorageIDChangeRederivesMountFields(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	runDerivedFileStorageIDChange(t, derivedFSCloud, "cld_derived_fs_change_mock", UniqueName(t, "fsderived"))
}

func TestAccCloudResourceResource_FileStorageIDChangeRederivesMountFields(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	runDerivedFileStorageIDChange(t, derivedFSCloudResource, "cld_derived_fs_change_res_mock", UniqueName(t, "fsderivedres"))
}

func TestAccCloudResource_FileStorageBlockAddedResolvesMountFields(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	runDerivedFileStorageBlockAdded(t, derivedFSCloud, "cld_derived_fs_add_mock", UniqueName(t, "fsadd"))
}

func TestAccCloudResourceResource_FileStorageBlockAddedResolvesMountFields(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	runDerivedFileStorageBlockAdded(t, derivedFSCloudResource, "cld_derived_fs_add_res_mock", UniqueName(t, "fsaddres"))
}

// TestAccCloudResource_ImportFailsClosedWhenResourcesListFails proves a failed
// GET /resources during import is an error, not a half-recovered state. Every
// config block is RequiresReplace, so importing without them would report
// success and the next plan would replace the live cloud. The second step is
// the control: with the listing healthy the same import succeeds and recovers
// the blocks.
func TestAccCloudResource_ImportFailsClosedWhenResourcesListFails(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_import_failclosed_mock"
	name := UniqueName(t, "importfail")
	server, backend := newDerivingBackend(t, cloudID, name)
	backend.stored["k8s-gcp-us-central1"] = map[string]interface{}{
		"name": "k8s-gcp-us-central1", "is_default": true, "cloud_resource_id": "cldrsrc_mock_default",
		"provider": "GCP", "compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": map[string]interface{}{
			"anyscale_operator_iam_identity": derivedFSOperator,
			"zones":                          []interface{}{"us-central1-a", "us-central1-b"},
		},
		"object_storage": map[string]interface{}{"bucket_name": derivedFSBucket},
	}
	backend.order = []string{"k8s-gcp-us-central1"}
	config := derivedFSCloud.config(server.URL, cloudID, name, "", "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				PreConfig:          func() { backend.failResourcesList.Store(true) },
				Config:             config,
				ResourceName:       "anyscale_cloud.test",
				ImportState:        true,
				ImportStateId:      cloudID,
				ImportStatePersist: true,
				ExpectError:        regexp.MustCompile(`(?s)could not be recovered|cannot be recovered`),
			},
			{
				PreConfig:          func() { backend.failResourcesList.Store(false) },
				Config:             config,
				ResourceName:       "anyscale_cloud.test",
				ImportState:        true,
				ImportStateId:      cloudID,
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("want 1 imported state, got %d", len(states))
					}
					if got := states[0].Attributes["kubernetes_config.anyscale_operator_iam_identity"]; got != derivedFSOperator {
						return fmt.Errorf("kubernetes_config not recovered on a healthy import: got %q", got)
					}
					return nil
				},
			},
		},
	})
}

// TestAccCloudResource_ImportToleratesHostedCloudResourcesList proves import still works for an
// Anyscale-hosted cloud, whose GET /resources the backend rejects with a 400 (there are no
// configuration blocks to recover). The first step is the control: a 500 on the same
// route must still fail the import.
func TestAccCloudResource_ImportToleratesHostedCloudResourcesList(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_import_hosted_mock"
	name := UniqueName(t, "importhosted")
	server, backend := newDerivingBackend(t, cloudID, name)
	config := derivedFSCloud.config(server.URL, cloudID, name, "", "")

	step := func(pre func()) resource.TestStep {
		return resource.TestStep{
			PreConfig:          pre,
			Config:             config,
			ResourceName:       "anyscale_cloud.test",
			ImportState:        true,
			ImportStateId:      cloudID,
			ImportStatePersist: true,
		}
	}
	failing := step(func() { backend.failResourcesList.Store(true) })
	failing.ExpectError = regexp.MustCompile(`(?s)cannot be recovered`)
	hosted := step(func() { backend.failResourcesList.Store(false); backend.hostedResources.Store(true) })
	hosted.ImportStateCheck = func(states []*terraform.InstanceState) error {
		if len(states) != 1 || states[0].ID != cloudID {
			return fmt.Errorf("want one imported state for %s, got %v", cloudID, states)
		}
		return nil
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps:                    []resource.TestStep{failing, hosted},
	})
}

// TestAccCloudResource_CreateLocalChecksPrecedePost proves a config whose
// required fields are missing is rejected at plan time and, were it applied,
// before any cloud is created. The first step must fail in a plan-only run:
// had the check lived only in Create, plan would be clean and the failure
// would come after POST /api/v2/clouds. The last steps are the controls: an
// unknown compute_stack is not an error at plan time, and a valid config
// creates exactly one cloud.
func TestAccCloudResource_CreateLocalChecksPrecedePost(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_create_preflight_mock"
	name := UniqueName(t, "preflight")
	server, backend := newDerivingBackend(t, cloudID, name)

	cfg := func(region, computeStack string) string {
		regionLine, stackLine := "", ""
		if region != "" {
			regionLine = fmt.Sprintf("  region         = %q\n", region)
		}
		if computeStack != "" {
			stackLine = fmt.Sprintf("  compute_stack  = %s\n", computeStack)
		}
		return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "terraform_data" "stack" {
  input = "K8S"
}

resource "anyscale_cloud" "test" {
  name           = %[1]q
  cloud_provider = "GCP"
%[2]s%[3]s
  kubernetes_config {
    anyscale_operator_iam_identity = %[4]q
    zones                          = ["us-central1-a", "us-central1-b"]
  }

  object_storage {
    bucket_name = %[5]q
  }
}
`, name, regionLine, stackLine, derivedFSOperator, derivedFSBucket)
	}
	noPosts := func(*terraform.State) error {
		if n := backend.posts.Load(); n != 0 {
			return fmt.Errorf("POST /api/v2/clouds was called %d time(s) for a config that cannot succeed", n)
		}
		return nil
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      cfg("", `"K8S"`), // region omitted: nothing to infer it from
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Region Could Not Be Determined`),
			},
			{
				Config:      cfg("us-central1", ""), // compute_stack omitted
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)compute_stack is required`),
			},
			{
				Config:      cfg("", `"K8S"`),
				ExpectError: regexp.MustCompile(`(?s)Region Could Not Be Determined`),
			},
			// Control: an unknown compute_stack is skipped at plan time rather than rejected.
			{
				Config:             cfg("us-central1", "terraform_data.stack.output"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true, // a create plan, not an error
				Check:              noPosts,
			},
			// Control: a valid config creates the cloud.
			{
				Config: cfg("us-central1", `"K8S"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "compute_stack", "K8S"),
					func(*terraform.State) error {
						if n := backend.posts.Load(); n != 1 {
							return fmt.Errorf("a valid config must create exactly one cloud; POST count = %d", n)
						}
						return nil
					},
				),
			},
		},
	})
}
