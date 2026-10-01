package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Create adopts an existing cloud of the same name before it runs any
// compute_stack or region check, so a config that omits them applies cleanly
// against a cloud that already exists. The plan-time form of those checks must
// agree: it may reject the config only when Create would also have to create.

const adoptByNameCloudName = "adopt-by-name"

// adoptByNameMode selects how the mock answers GET /api/v2/clouds.
type adoptByNameMode int

const (
	adoptNone      adoptByNameMode = iota // no cloud has the name
	adoptOne                              // exactly one cloud has the name
	adoptDuplicate                        // two clouds have the name
	adoptListFails                        // the listing returns 500
)

// adoptByNameServer serves the cloud listing per mode, plus the endpoints a read
// of the adopted cloud touches. posts counts POST /api/v2/clouds.
func adoptByNameServer(t *testing.T, mode adoptByNameMode) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	const id = "cld_adopt_by_name_mock"
	var posts atomic.Int32
	cloudJSON := func(cloudID string) string {
		return fmt.Sprintf(`{"id": %q, "name": %q, "provider": "AWS", "region": "us-east-2",
			"status": "ready", "state": "ACTIVE", "compute_stack": "VM"}`, cloudID, adoptByNameCloudName)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "unexpected create"}}`)
			return
		}
		switch mode {
		case adoptListFails:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "listing unavailable"}}`)
		case adoptOne:
			_, _ = fmt.Fprintf(w, `{"results": [%s], "metadata": {"total": 1, "next_paging_token": null}}`, cloudJSON(id))
		case adoptDuplicate:
			_, _ = fmt.Fprintf(w, `{"results": [%s, %s], "metadata": {"total": 2, "next_paging_token": null}}`,
				cloudJSON(id), cloudJSON("cld_adopt_by_name_dup"))
		default:
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+id, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON(id))
	})
	mux.HandleFunc("/api/v2/clouds/"+id+"/resources", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"results": [{"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_adopt_mock",
			"provider": "AWS", "compute_stack": "VM", "region": "us-east-2",
			"aws_config": {"vpc_id": "vpc-adopt", "subnet_ids": ["subnet-a"], "zones": ["us-east-2a"], "security_group_ids": ["sg-adopt"]},
			"object_storage": {"bucket_name": "s3://adopt-bucket"}}],
			"metadata": {"total": 1, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/machine_pools/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"result": {"machine_pools": []}}`)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, &posts
}

// adoptByNameConfig is an embedded-config cloud with compute_stack and region
// omitted and nothing to infer a region from.
func adoptByNameConfig(url string) string {
	return testAccProviderBlock(url) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = %q
  cloud_provider = "AWS"

  aws_config {
    vpc_id             = "vpc-adopt"
    subnet_ids         = ["subnet-a"]
    security_group_ids = ["sg-adopt"]
  }

  object_storage {
    bucket_name = "s3://adopt-bucket"
  }
}
`, adoptByNameCloudName)
}

func adoptNoPosts(posts *atomic.Int32) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if n := posts.Load(); n != 0 {
			return fmt.Errorf("POST /api/v2/clouds called %d times, want 0", n)
		}
		return nil
	}
}

// An existing cloud with the name is adopted: the omitted compute_stack and
// region are not an error, and nothing is created.
func TestAccCloudResource_AdoptByName_OmittedComputeStackApplies(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, posts := adoptByNameServer(t, adoptOne)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: adoptByNameConfig(server.URL),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("anyscale_cloud.test", "id", "cld_adopt_by_name_mock"),
				resource.TestCheckResourceAttr("anyscale_cloud.test", "compute_stack", "VM"),
				adoptNoPosts(posts),
			),
		}},
	})
}

// Control: with no cloud of that name the requirement still applies, at plan
// time, and nothing is created.
func TestAccCloudResource_AdoptByName_NotFoundKeepsRequirementError(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, posts := adoptByNameServer(t, adoptNone)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      adoptByNameConfig(server.URL),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`compute_stack is required`),
		}},
	})
	if n := posts.Load(); n != 0 {
		t.Fatalf("POST /api/v2/clouds called %d times, want 0", n)
	}
}

// A listing that fails cannot show the cloud is absent, so the plan fails with
// the lookup error instead of guessing.
func TestAccCloudResource_AdoptByName_LookupFailureFailsPlan(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, posts := adoptByNameServer(t, adoptListFails)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      adoptByNameConfig(server.URL),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`(?s)Cloud Lookup Failed.*failed\s+to\s+list\s+clouds`),
		}},
	})
	if n := posts.Load(); n != 0 {
		t.Fatalf("POST /api/v2/clouds called %d times, want 0", n)
	}
}

// Two clouds with the name is the same error Create raises.
func TestAccCloudResource_AdoptByName_AmbiguousNameFailsPlan(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, posts := adoptByNameServer(t, adoptDuplicate)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      adoptByNameConfig(server.URL),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`Multiple Clouds Found`),
		}},
	})
	if n := posts.Load(); n != 0 {
		t.Fatalf("POST /api/v2/clouds called %d times, want 0", n)
	}
}
