package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// This file closes the framework-level half of C3's acceptance gates that
// resource_cloud_c3_test.go (function-level, calls readCloudState/
// readCloudResource directly) cannot: plan-emptiness is a terraform-FRAMEWORK
// property, not just a mapping-function property. A plan modifier or attr
// interaction can still diff even when the mapping function is provably
// correct in isolation, so these run the real create -> apply -> plan(empty)
// -> import -> plan(empty) lifecycle through resource.Test, backed by an
// httptest mock server (the provider's api_url points at it) instead of a
// real API. No real AWS/GCP infra, no ANYSCALE_TEST_REAL_INFRA gate needed -
// this runs in ordinary CI.
//
// The mock deliberately returns REALISTIC (not idealized-echo) shapes for the
// three C3 round-trip hazards: subnet_ids_to_az is derived from parallel
// subnet_ids+zones arrays (the API never returns subnet_ids_to_az itself);
// object_storage.bucket_name comes back WITH its provider prefix even though
// the schema stores it bare; GCP is analogous. An echo-mock that just replays
// whatever the config sent would pass trivially and prove nothing.

// newC3MockCloudServer serves a single fixed cloud consistently across every
// step of a resource.Test lifecycle (create, refresh, import, destroy).
// cloudJSON is the `result` body for GET/POST /clouds; resourcesJSON is the
// `results` array body for GET /clouds/{id}/resources.
func newC3MockCloudServer(t *testing.T, cloudID, cloudJSON, resourcesJSON string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.Method {
		case http.MethodPost:
			_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
		case http.MethodGet:
			// findCloudByName's existing-cloud check: report none, so Create
			// always takes the fresh-create path instead of adopting.
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
		// add_resource's own response is only used for its cloud_deployment_id;
		// the resources listing above is what config-block population reads.
		_, _ = fmt.Fprint(w, `{"result": {"cloud_deployment_id": "cldrsrc_mock_default"}}`)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func testAccProviderBlock(serverURL string) string {
	return fmt.Sprintf(`
provider "anyscale" {
  api_url = %[1]q
  token   = "mock-token"
}
`, serverURL)
}

// TestAccCloudLifecycle_AWS_MockServer proves C3's headline gates for the AWS
// all-in-one pattern against a mock backend: fresh create -> apply -> plan
// empty; import -> populates cleanly, ImportStateVerify matches, no replace.
func TestAccCloudLifecycle_AWS_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_c3_aws_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "c3-aws-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"
	}`, cloudID)
	// Realistic hazard shapes: parallel subnet_ids+zones (never subnet_ids_to_az),
	// bucket_name WITH its s3:// prefix even though the schema stores it bare.
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_deployment_id": "cldrsrc_mock_default",
		"compute_stack": "VM", "region": "us-east-2",
		"aws_config": {
			"vpc_id": "vpc-real123",
			"subnet_ids": ["subnet-real1", "subnet-real2"],
			"zones": ["us-east-2a", "us-east-2b"],
			"security_group_ids": ["sg-real1"],
			"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/real-crossaccount",
			"cluster_iam_role_id": "arn:aws:iam::123456789012:role/real-cluster-node",
			"external_id": "real-external-id"
		},
		"object_storage": {"bucket_name": "s3://real-bucket-name"}
	}]`

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "c3-aws-mock"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"

  aws_config {
    vpc_id             = "vpc-real123"
    subnet_ids_to_az = {
      "subnet-real1" = "us-east-2a"
      "subnet-real2" = "us-east-2b"
    }
    security_group_ids        = ["sg-real1"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/real-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/real-cluster-node"
    external_id                = "real-external-id"
  }

  object_storage {
    bucket_name = "real-bucket-name"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "aws_config.vpc_id", "vpc-real123"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "object_storage.bucket_name", "real-bucket-name"),
				),
				// Headline C3 gate: a config populated at create against a
				// realistically-shaped (hazard-laden) API response must not
				// diff on the very next plan.
				ExpectNonEmptyPlan: false,
			},
			{
				ResourceName:      "anyscale_cloud.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
				},
			},
		},
	})
}

// TestAccCloudLifecycle_GCP_MockServer is the GCP analogue: proves the
// bucket_name gs:// hazard and GCP config round-trip cleanly.
func TestAccCloudLifecycle_GCP_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_c3_gcp_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "c3-gcp-mock", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"
	}`, cloudID)
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_deployment_id": "cldrsrc_mock_default",
		"compute_stack": "VM", "region": "us-central1",
		"gcp_config": {
			"project_id": "real-gcp-project",
			"vpc_name": "real-vpc",
			"subnet_names": ["real-subnet-1"],
			"anyscale_service_account_email": "real-cp@real-gcp-project.iam.gserviceaccount.com",
			"cluster_service_account_email": "real-dp@real-gcp-project.iam.gserviceaccount.com"
		},
		"object_storage": {"bucket_name": "gs://real-gcs-bucket"}
	}]`

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "c3-gcp-mock"
  cloud_provider = "GCP"
  compute_stack  = "VM"
  region         = "us-central1"

  gcp_config {
    project_id                         = "real-gcp-project"
    vpc_name                           = "real-vpc"
    subnet_names                       = ["real-subnet-1"]
    controlplane_service_account_email = "real-cp@real-gcp-project.iam.gserviceaccount.com"
    dataplane_service_account_email    = "real-dp@real-gcp-project.iam.gserviceaccount.com"
  }

  object_storage {
    bucket_name = "gs://real-gcs-bucket"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "gcp_config.project_id", "real-gcp-project"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "object_storage.bucket_name", "gs://real-gcs-bucket"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				ResourceName:      "anyscale_cloud.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
				},
			},
		},
	})
}

// TestAccCloudResource_K8S_NoRegion_ClearError is an acceptance-level proof
// for C13: a K8S-only all-in-one cloud (no aws_config, so no subnet-based
// region inference, and no longer treated as empty since C12) with no
// explicit region must fail with a clear provider-level error instead of
// silently sending region="" through to a real add_resource call. Runs
// through the real Create() framework path (not just forge's direct-function
// unit test on regionRequiredForCreateError) against a mock server, so it
// also proves the error surfaces correctly as a plan/apply-time diagnostic.
func TestAccCloudResource_K8S_NoRegion_ClearError(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_c13_noregion_mock"
	// region is intentionally absent/empty here - this is what a create
	// request looks like right before the C13 guard would otherwise let
	// region="" flow through to add_resource.
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "c13-noregion-mock", "provider": "AWS", "region": "",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `[]`

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "c13-noregion-mock"
  cloud_provider = "AWS"
  compute_stack  = "K8S"
  # region deliberately omitted

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::123456789012:role/real-k8s-operator"
  }

  object_storage {
    bucket_name = "real-k8s-bucket"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile("region could not be determined"),
			},
		},
	})
}

// TestAccCloudLifecycle_K8S_MockServer, using the all-in-one pattern (a
// single anyscale_cloud resource with an embedded kubernetes_config block).
func TestAccCloudLifecycle_K8S_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_c3_k8s_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "c3-k8s-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_deployment_id": "cldrsrc_mock_default",
		"compute_stack": "K8S", "region": "us-east-2",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "arn:aws:iam::123456789012:role/real-k8s-operator",
			"zones": ["us-east-2a", "us-east-2b"]
		},
		"object_storage": {"bucket_name": "s3://real-k8s-bucket"}
	}]`

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "c3-k8s-mock"
  cloud_provider = "AWS"
  compute_stack  = "K8S"
  region         = "us-east-2"

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::123456789012:role/real-k8s-operator"
    zones                          = ["us-east-2a", "us-east-2b"]
  }

  object_storage {
    bucket_name = "real-k8s-bucket"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "compute_stack", "K8S"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "object_storage.bucket_name", "real-k8s-bucket"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "cloud_deployment_id", "cldrsrc_mock_default"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				ResourceName:      "anyscale_cloud.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud",
				},
			},
		},
	})
}
