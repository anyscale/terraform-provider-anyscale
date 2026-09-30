package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Mock-backed create + import round trip for anyscale_cloud_resource. The
// real-API tests in resource_cloud_resource_acc_test.go are REAL_INFRA-gated
// and put every config block in ImportStateVerifyIgnore, so nothing CI runs
// proves ImportState's block recovery (requiredImportConfigBlocks) agrees
// with what Create writes. This test does, with no block ignored.
//
// Wire shape carried into the mock, from the confirmed mocks in
// resource_cloud_c3_lifecycle_acc_test.go / resource_cloud_import_storage_acc_test.go
// and the CloudDeploymentResult struct:
//   - GET /clouds/{id}/resources echoes what add_resource was sent: parallel
//     subnet_ids + zones arrays (never subnet_ids_to_az), IAM ARNs under the
//     API names anyscale_iam_role_id / cluster_iam_role_id, and bucket_name
//     WITH its s3:// prefix.
//   - object_storage.region is not echoed (the backend never returns a region
//     equal to the resource's own region).
//   - file_storage.mount_targets is backend-derived from file_storage_id (EFS
//     auto-discovery) and returned by both add_resource and the list, though
//     the config never sets it - the derived-field shape import must absorb
//     without a diff.
//   - The cloud's own primary resource is listed alongside, is_default:true,
//     so the lookup-by-name is exercised against more than one entry.

const cloudResourceIVMountTargetsJSON = `[
	{"address": "10.0.1.10", "zone": "us-east-2a"},
	{"address": "10.0.2.10", "zone": "us-east-2b"}
]`

type cloudResourceIVMock struct {
	mu      sync.Mutex
	added   map[string]any // the added resource as the list returns it; nil until add_resource
	adds    int
	removes int
}

func newCloudResourceIVMockServer(t *testing.T, cloudID string) (*httptest.Server, *cloudResourceIVMock) {
	t.Helper()
	m := &cloudResourceIVMock{}
	mux := http.NewServeMux()

	primary := map[string]any{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_iv_primary",
		"provider": "AWS", "compute_stack": "VM", "region": "us-east-2", "networking_mode": "PUBLIC",
		"aws_config": map[string]any{
			"vpc_id": "vpc-iv-primary", "subnet_ids": []string{"subnet-iv-primary"}, "zones": []string{"us-east-2a"},
			"security_group_ids":   []string{"sg-iv-primary"},
			"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/iv-primary-crossaccount",
			"cluster_iam_role_id":  "arn:aws:iam::123456789012:role/iv-primary-node",
		},
		"object_storage": map[string]any{"bucket_name": "s3://iv-primary-bucket", "region": nil},
		"file_storage":   nil, "operator_status": nil, "operator_status_details": nil,
	}

	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on /api/v2/clouds/%s", r.Method, cloudID)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "name": "iv-parent", "provider": "AWS", "region": "us-east-2",
			"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": false
		}}`, cloudID)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on resources (this test never updates file_storage)", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		m.mu.Lock()
		results := []any{primary}
		if m.added != nil {
			results = append(results, m.added)
		}
		body, _ := json.Marshal(map[string]any{
			"results":  results,
			"metadata": map[string]any{"total": len(results), "next_paging_token": nil},
		})
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected method %s on add_resource", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var res map[string]any
		if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
			t.Errorf("add_resource body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Server-assigned and server-derived fields, as the real backend adds them.
		res["cloud_resource_id"] = "cldrsrc_iv_added"
		res["is_default"] = false
		res["created_at"] = "2026-01-01T00:00:00Z"
		res["operator_status"] = nil
		res["operator_status_details"] = nil
		if objStore, ok := res["object_storage"].(map[string]any); ok {
			objStore["region"] = nil
		}
		if fs, ok := res["file_storage"].(map[string]any); ok {
			if _, set := fs["mount_targets"]; !set {
				var mts []any
				_ = json.Unmarshal([]byte(cloudResourceIVMountTargetsJSON), &mts)
				fs["mount_targets"] = mts
			}
		}
		m.mu.Lock()
		m.added = res
		m.adds++
		body, _ := json.Marshal(map[string]any{"result": res})
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/remove_resource", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected method %s on remove_resource", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		m.mu.Lock()
		m.added = nil
		m.removes++
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, m
}

// TestAccCloudResourceResource_ImportStateVerify_MockServer proves an AWS VM
// anyscale_cloud_resource imports to exactly the state Create wrote, with
// aws_config, object_storage and file_storage all byte-compared (none
// ignored): subnet_ids_to_az rebuilt from the parallel subnet_ids/zones
// arrays, the s3:// prefix stripped from bucket_name, the unechoed
// object_storage.region left null, and the backend-derived
// file_storage.mount_targets recovered to the same value Create merged in.
func TestAccCloudResourceResource_ImportStateVerify_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_cloud_resource_iv_mock"
	const addr = "anyscale_cloud_resource.test"
	server, m := newCloudResourceIVMockServer(t, cloudID)

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud_resource" "test" {
  cloud_id      = %[1]q
  name          = "iv-secondary"
  compute_stack = "VM"
  region        = "us-east-2"

  aws_config {
    vpc_id = "vpc-iv-secondary"
    subnet_ids_to_az = {
      "subnet-iv-secondary-a" = "us-east-2a"
      "subnet-iv-secondary-b" = "us-east-2b"
    }
    security_group_ids        = ["sg-iv-secondary"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/iv-secondary-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/iv-secondary-node"
    external_id               = "iv-secondary-external-id"
  }

  object_storage {
    bucket_name = "iv-secondary-bucket"
  }

  file_storage {
    file_storage_id = "fs-iv-secondary"
  }
}
`, cloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", cloudID+":iv-secondary"),
					resource.TestCheckResourceAttr(addr, "cloud_resource_id", "cldrsrc_iv_added"),
					resource.TestCheckResourceAttr(addr, "cloud_provider", "AWS"),
					resource.TestCheckResourceAttr(addr, "is_default", "false"),
					resource.TestCheckResourceAttr(addr, "file_storage.mount_targets.#", "2"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
				// No ImportStateVerifyIgnore: every block must round-trip. The
				// check below names the recovered values so a regression reports
				// the field, not only a verify diff.
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported resource, got %d", len(states))
					}
					attrs := states[0].Attributes
					for k, want := range map[string]string{
						"cloud_id":          cloudID,
						"name":              "iv-secondary",
						"cloud_resource_id": "cldrsrc_iv_added",
						"aws_config.vpc_id": "vpc-iv-secondary",
						"aws_config.subnet_ids_to_az.subnet-iv-secondary-a": "us-east-2a",
						"aws_config.subnet_ids_to_az.subnet-iv-secondary-b": "us-east-2b",
						"aws_config.controlplane_iam_role_arn":              "arn:aws:iam::123456789012:role/iv-secondary-crossaccount",
						"aws_config.dataplane_iam_role_arn":                 "arn:aws:iam::123456789012:role/iv-secondary-node",
						"aws_config.external_id":                            "iv-secondary-external-id",
						"object_storage.bucket_name":                        "iv-secondary-bucket",
						"file_storage.file_storage_id":                      "fs-iv-secondary",
						"file_storage.mount_targets.#":                      "2",
					} {
						if got := attrs[k]; got != want {
							return fmt.Errorf("%s after import = %q, want %q", k, got, want)
						}
					}
					for _, k := range []string{"object_storage.region", "aws_config.subnet_ids.#"} {
						if v, ok := attrs[k]; ok && v != "" && v != "0" {
							return fmt.Errorf("%s after import = %q, want null", k, v)
						}
					}
					return nil
				},
			},
		},
	})

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.adds != 1 {
		t.Errorf("add_resource called %d times, want 1", m.adds)
	}
	if m.removes != 1 {
		t.Errorf("remove_resource called %d times, want 1 (destroy of the non-primary resource)", m.removes)
	}
}
