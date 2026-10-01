package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// subnetFormsServer serves one AWS VM cloud whose default resource carries the
// subnets the way the real API does: parallel subnet_ids and zones arrays,
// never subnet_ids_to_az. Import therefore recovers subnet_ids_to_az and
// leaves subnet_ids null.
func subnetFormsServer(t *testing.T, cloudID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	cloudJSON := fmt.Sprintf(`{"id": %q, "name": "subnet-forms", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"}`, cloudID)
	resourcesJSON := `[{"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_subnet_forms",
		"provider": "AWS", "compute_stack": "VM", "region": "us-east-2",
		"aws_config": {"vpc_id": "vpc-forms", "subnet_ids": ["subnet-a", "subnet-b"], "zones": ["us-east-2a", "us-east-2b"],
		  "security_group_ids": ["sg-forms"]},
		"object_storage": {"bucket_name": "s3://forms-bucket"}}]`
	var deleted atomic.Bool
	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case deleted.Load():
			w.WriteHeader(http.StatusNotFound)
		default:
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		}
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"results": %s, "metadata": {"total": 1, "next_paging_token": null}}`, resourcesJSON)
	})
	// A replacement recreates the cloud / resource; the mock accepts it and serves the same listing.
	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			deleted.Store(false)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
			return
		}
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"result": {"name": "default", "cloud_resource_id": "cldrsrc_subnet_forms"}}`)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/remove_resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v2/machine_pools/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"result": {"machine_pools": []}}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// subnetFormsCase describes one resource type under test.
type subnetFormsCase struct {
	address  string
	importID func(cloudID string) string
	config   func(url, cloudID, subnets string) string
}

var subnetFormsCloud = subnetFormsCase{
	address:  "anyscale_cloud.test",
	importID: func(cloudID string) string { return cloudID },
	config: func(url, cloudID, subnets string) string {
		return testAccProviderBlock(url) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "subnet-forms"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"

  aws_config {
    vpc_id             = "vpc-forms"
    %s
    security_group_ids = ["sg-forms"]
  }

  object_storage {
    bucket_name = "s3://forms-bucket"
  }
}
`, subnets)
	},
}

var subnetFormsCloudResource = subnetFormsCase{
	address:  "anyscale_cloud_resource.test",
	importID: func(cloudID string) string { return cloudID + ":default" },
	config: func(url, cloudID, subnets string) string {
		return testAccProviderBlock(url) + fmt.Sprintf(`
resource "anyscale_cloud_resource" "test" {
  cloud_id       = %q
  name           = "default"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"

  aws_config {
    vpc_id             = "vpc-forms"
    %s
    security_group_ids = ["sg-forms"]
  }

  object_storage {
    bucket_name = "s3://forms-bucket"
  }
}
`, cloudID, subnets)
	},
}

const (
	subnetListForm = `subnet_ids = ["subnet-a", "subnet-b"]`
	subnetMapForm  = `subnet_ids_to_az = { "subnet-a" = "us-east-2a", "subnet-b" = "us-east-2b" }`
	subnetOther    = `subnet_ids = ["subnet-a", "subnet-z"]`
)

// runSubnetForms imports the cloud (recovering subnet_ids_to_az only) and then
// plans three configs against that state:
//   - the list form naming the same subnets must plan an in-place update, not a
//     provider error and not a replacement, and settle to an empty plan;
//   - the map form is the control that already worked: no changes;
//   - a list naming different subnets is the control that a real change still
//     replaces.
func runSubnetForms(t *testing.T, c subnetFormsCase, cloudID string) {
	// Each subtest gets its own server: destroy marks the mock's cloud deleted.
	importStep := func(url, subnets string) resource.TestStep {
		return resource.TestStep{
			Config: c.config(url, cloudID, subnets), ResourceName: c.address,
			ImportState: true, ImportStateId: c.importID(cloudID), ImportStatePersist: true,
		}
	}

	t.Run("map form plans clean", func(t *testing.T) {
		url := subnetFormsServer(t, cloudID).URL
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: ProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				importStep(url, subnetMapForm),
				{Config: c.config(url, cloudID, subnetMapForm), PlanOnly: true},
			},
		})
	})

	t.Run("list form for the same subnets updates in place", func(t *testing.T) {
		url := subnetFormsServer(t, cloudID).URL
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: ProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				importStep(url, subnetListForm),
				{
					Config: c.config(url, cloudID, subnetListForm),
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(c.address, plancheck.ResourceActionUpdate)},
						PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
					},
					Check: resource.TestCheckResourceAttr(c.address, "aws_config.subnet_ids.#", "2"),
				},
			},
		})
	})

	t.Run("list form for different subnets still replaces", func(t *testing.T) {
		url := subnetFormsServer(t, cloudID).URL
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: ProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				importStep(url, subnetOther),
				{
					Config: c.config(url, cloudID, subnetOther),
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(c.address, plancheck.ResourceActionDestroyBeforeCreate)},
					},
				},
			},
		})
	})
}

func TestAccCloudResource_ImportedSubnetListFormUpdatesInPlace(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	runSubnetForms(t, subnetFormsCloud, "cld_subnet_forms_cloud_mock")
}

func TestAccCloudResourceResource_ImportedSubnetListFormUpdatesInPlace(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	runSubnetForms(t, subnetFormsCloudResource, "cld_subnet_forms_res_mock")
}
