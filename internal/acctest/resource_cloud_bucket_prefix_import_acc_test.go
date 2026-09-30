package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// The API always returns a GCS bucket gs://-prefixed, and import records that
// form, while a config may name the bucket bare. bucket_name is
// RequiresReplace, so without bucketNameSemanticEqualPlanModifier the first
// plan after importing such a cloud would replace it. These tests plan a bare
// config against the imported gs:// state through real Terraform Core.

func gcpBucketPrefixImportMockServer(t *testing.T, cloudID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "bucket-prefix-gcp", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_bucket_prefix",
		"compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "bucket-prefix-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a"]
		},
		"object_storage": {"bucket_name": "gs://bucket-prefix-gcs"}
	}]`

	// Only the harness's post-test destroy deletes; the planning steps under
	// test never write, and the mock rejects any other method.
	var deleted atomic.Bool
	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if deleted.Load() {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		case http.MethodDelete:
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results": %s, "metadata": {"total": 1, "next_paging_token": null}}`, resourcesJSON)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/machine_pools/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func testAccGCPBucketPrefixConfig(serverURL, bucket string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "bucket-prefix-gcp"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "bucket-prefix-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a"]
  }

  object_storage {
    bucket_name = %q
  }
}
`, bucket)
}

// TestAccCloudResource_ImportedGCSBucketPrefix_BareConfigPlansNoReplace is the
// Test B for the gs:// import divergence: cold-import the cloud (the state
// records gs://bucket-prefix-gcs), then plan a config naming the same bucket
// bare. The plan must not replace the cloud.
func TestAccCloudResource_ImportedGCSBucketPrefix_BareConfigPlansNoReplace(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_bucket_prefix_gcp"
	server := gcpBucketPrefixImportMockServer(t, cloudID)
	config := testAccGCPBucketPrefixConfig(server.URL, "bucket-prefix-gcs")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             config,
				ResourceName:       "anyscale_cloud.test",
				ImportState:        true,
				ImportStateId:      cloudID,
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported state, got %d", len(states))
					}
					if got := states[0].Attributes["object_storage.bucket_name"]; got != "gs://bucket-prefix-gcs" {
						return fmt.Errorf("imported object_storage.bucket_name = %q, want the API's gs:// form", got)
					}
					return nil
				},
			},
			// PlanOnly fails the step on any non-empty plan, so this asserts
			// the plan is empty: no replace, and no update either.
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// TestAccCloudResource_ImportedGCSBucketPrefix_DifferentBucketPlansReplace is
// the positive control for the test above: naming a genuinely different
// bucket against the same imported state must still plan a replacement, so
// the no-replace result cannot come from a check that never fires.
func TestAccCloudResource_ImportedGCSBucketPrefix_DifferentBucketPlansReplace(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_bucket_prefix_gcp"
	server := gcpBucketPrefixImportMockServer(t, cloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             testAccGCPBucketPrefixConfig(server.URL, "bucket-prefix-gcs"),
				ResourceName:       "anyscale_cloud.test",
				ImportState:        true,
				ImportStateId:      cloudID,
				ImportStatePersist: true,
			},
			{
				Config:             testAccGCPBucketPrefixConfig(server.URL, "some-other-bucket"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionReplace),
					},
				},
			},
		},
	})
}

// TestAccCloudResourceResource_ImportedGCSBucketPrefix_BareConfigPlansNoReplace
// is the same Test B for anyscale_cloud_resource, which carries its own
// bucket_name schema and modifier list.
func TestAccCloudResourceResource_ImportedGCSBucketPrefix_BareConfigPlansNoReplace(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_bucket_prefix_gcp_plural"
	const resourceName = "bucket-prefix-resource"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "bucket-prefix-gcp-plural", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S", "is_default": false
	}`, cloudID)
	resourcesJSON := fmt.Sprintf(`[{
		"name": %q, "is_default": false, "cloud_resource_id": "cldrsrc_bucket_prefix_plural",
		"compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "bucket-prefix-operator@my-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a"]
		},
		"object_storage": {"bucket_name": "gs://bucket-prefix-gcs"}
	}]`, resourceName)
	server := newCloudResourceMockServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_bucket_prefix_plural", resourceName)

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud_resource" "test" {
  cloud_id      = %[1]q
  name          = %[2]q
  compute_stack = "K8S"
  region        = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "bucket-prefix-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a"]
  }

  object_storage {
    bucket_name = "bucket-prefix-gcs"
  }
}
`, cloudID, resourceName)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             config,
				ResourceName:       "anyscale_cloud_resource.test",
				ImportState:        true,
				ImportStateId:      cloudID + ":" + resourceName,
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported state, got %d", len(states))
					}
					if got := states[0].Attributes["object_storage.bucket_name"]; got != "gs://bucket-prefix-gcs" {
						return fmt.Errorf("imported object_storage.bucket_name = %q, want the API's gs:// form", got)
					}
					return nil
				},
			},
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}
