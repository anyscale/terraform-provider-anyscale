package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
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

// TestAccCloudResource_Lifecycle_AWS_MockServer proves C3's headline gates for the AWS
// all-in-one pattern against a mock backend: fresh create -> apply -> plan
// empty; import -> populates cleanly, ImportStateVerify matches, no replace.
func TestAccCloudResource_Lifecycle_AWS_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_c3_aws_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "c3-aws-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"
	}`, cloudID)
	// Realistic hazard shapes: parallel subnet_ids+zones (never subnet_ids_to_az),
	// bucket_name WITH its s3:// prefix even though the schema stores it bare.
	// cluster_instance_profile_id (C6) is included to prove it round-trips
	// through the same import-only recovery path as the pre-existing fields.
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
			"cluster_instance_profile_id": "arn:aws:iam::123456789012:instance-profile/real-cluster-node",
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
    security_group_ids          = ["sg-real1"]
    controlplane_iam_role_arn   = "arn:aws:iam::123456789012:role/real-crossaccount"
    dataplane_iam_role_arn      = "arn:aws:iam::123456789012:role/real-cluster-node"
    cluster_instance_profile_id = "arn:aws:iam::123456789012:instance-profile/real-cluster-node"
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
					resource.TestCheckResourceAttr("anyscale_cloud.test", "aws_config.cluster_instance_profile_id", "arn:aws:iam::123456789012:instance-profile/real-cluster-node"),
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
					"object_storage", // optional for VM (only aws_config/gcp_config is compute-stack-required); not recovered at import by design (C3-v2)
				},
			},
		},
	})
}

// TestAccCloudResource_Lifecycle_GCP_MockServer is the GCP analogue: proves the
// bucket_name gs:// hazard and GCP config round-trip cleanly.
func TestAccCloudResource_Lifecycle_GCP_MockServer(t *testing.T) {
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
					"object_storage", // optional for VM (only aws_config/gcp_config is compute-stack-required); not recovered at import by design (C3-v2)
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

// TestAccCloudResource_Lifecycle_K8S_MockServer, using the all-in-one pattern (a
// single anyscale_cloud resource with an embedded kubernetes_config block).
// This is the AWS (EKS-shaped) case; TestAccCloudResource_Lifecycle_GCP_K8S_MockServer
// below is the GCP (GKE-shaped) analogue.
func TestAccCloudResource_Lifecycle_K8S_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_c3_k8s_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "c3-k8s-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	// file_storage.persistent_volume_claim (C6) is set here to prove it
	// applies cleanly on create, but is deliberately NOT asserted after
	// import: file_storage is optional even for K8S (only kubernetes_config
	// + object_storage are the compute-stack-required blocks C3-v2
	// recovers), so it is never recovered at import by design - see
	// ImportStateVerifyIgnore below. kubernetes_config.redis_endpoint, by
	// contrast, IS inside a recovered block, so it is asserted after import
	// too (not added to the ignore list) - that is real round-trip proof,
	// not just a create-time echo.
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_deployment_id": "cldrsrc_mock_default",
		"compute_stack": "K8S", "region": "us-east-2",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "arn:aws:iam::123456789012:role/real-k8s-operator",
			"zones": ["us-east-2a", "us-east-2b"],
			"redis_endpoint": "redis.ray-system.svc.cluster.local:6379"
		},
		"object_storage": {"bucket_name": "s3://real-k8s-bucket"},
		"file_storage": {"persistent_volume_claim": "real-pvc-name"}
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
    redis_endpoint                 = "redis.ray-system.svc.cluster.local:6379"
  }

  object_storage {
    bucket_name = "real-k8s-bucket"
  }

  file_storage {
    persistent_volume_claim = "real-pvc-name"
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
					resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.persistent_volume_claim", "real-pvc-name"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "kubernetes_config.redis_endpoint", "redis.ray-system.svc.cluster.local:6379"),
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
					"file_storage", // optional even for K8S; not recovered at import by design (C3-v2)
				},
			},
		},
	})
}

// TestAccCloudResource_Lifecycle_GCP_K8S_MockServer is the GCP (GKE-shaped)
// analogue of TestAccCloudResource_Lifecycle_K8S_MockServer above. It
// exercises file_storage.csi_ephemeral_volume_driver instead of
// persistent_volume_claim - the backend rejects setting both on the same
// file_storage block (see cloud_helpers_test.go / K10).
//
// Honesty check on what this actually proves (verified by mutation-testing
// flattenFileStorage): like the pre-existing persistent_volume_claim
// assertion in the AWS test above, the csi_ephemeral_volume_driver Check
// below only proves clean create-time materialization (expand + no
// plan-modifier drift) - NOT a flatten/import round-trip, since file_storage
// is deliberately excluded from C3-v2's import recovery (requiredImportConfigBlocks)
// and therefore also from ImportStateVerifyIgnore's complement here. Injecting
// a regression into flattenFileStorage's csi mapping does NOT fail this test.
// The genuine flatten-correctness proof for both fields lives at the unit
// level (TestFlattenFileStorage_PVCAndCSIDriver, cloud_config_flatten_test.go).
// kubernetes_config.redis_endpoint below is different: kubernetes_config IS a
// C3-v2-recovered block, so it is NOT in ImportStateVerifyIgnore and its
// import-time assertion is a real round-trip proof (confirmed by mutation
// test: breaking flattenKubernetesConfig's redis_endpoint mapping does fail
// this test, at the ImportStateVerify step specifically).
func TestAccCloudResource_Lifecycle_GCP_K8S_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_c3_gcp_k8s_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "c3-gcp-k8s-mock", "provider": "GCP", "region": "us-central1",
		"status": "ready", "state": "ACTIVE", "compute_stack": "K8S"
	}`, cloudID)
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_deployment_id": "cldrsrc_mock_default",
		"compute_stack": "K8S", "region": "us-central1",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "real-gke-operator@real-gcp-project.iam.gserviceaccount.com",
			"zones": ["us-central1-a", "us-central1-b"],
			"redis_endpoint": "redis.ray-system.svc.cluster.local:6379"
		},
		"object_storage": {"bucket_name": "gs://real-gke-bucket"},
		"file_storage": {"csi_ephemeral_volume_driver": "ephemeral.csi.example.com"}
	}]`

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "c3-gcp-k8s-mock"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "real-gke-operator@real-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a", "us-central1-b"]
    redis_endpoint                 = "redis.ray-system.svc.cluster.local:6379"
  }

  object_storage {
    bucket_name = "gs://real-gke-bucket"
  }

  file_storage {
    csi_ephemeral_volume_driver = "ephemeral.csi.example.com"
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
					resource.TestCheckResourceAttr("anyscale_cloud.test", "object_storage.bucket_name", "gs://real-gke-bucket"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "file_storage.csi_ephemeral_volume_driver", "ephemeral.csi.example.com"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "kubernetes_config.redis_endpoint", "redis.ray-system.svc.cluster.local:6379"),
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
					"file_storage", // optional even for K8S; not recovered at import by design (C3-v2)
				},
			},
		},
	})
}

// This section is the MOCK-LEVEL regression guard for BUG C, the
// user-reported cold-import compute_stack issue: a K8S cloud created OUTSIDE
// Terraform (via the Anyscale CLI) then imported cold showed compute_stack =
// VM instead of K8S. Forge's fix (resource_cloud.go's readCloudState) sources
// compute_stack from findDefaultInCloudResources - the same primary-resource
// lookup requiredImportConfigBlocks already trusts - falling back to the
// cloud-level derived field (GET /clouds/{id}'s own compute_stack, which
// defaults to VM when the backend doesn't recognize a primary resource) only
// when no resource is flagged default; findDefaultInCloudResources itself was
// further hardened to treat a lone unflagged resource as unambiguous (no
// is_default anywhere, but exactly one resource exists) rather than falling
// through to the cloud-level value.
//
// IMPORTANT SCOPE NOTE (architect's correction, do not overclaim): these two
// mock variants prove the hardening logic itself is correct for the shapes
// they construct - they do NOT prove this is the user's actual root cause.
// Forge's own backend trace raised a real possibility that a single-resource
// cloud's is_default flag and the cloud-level derivation may be computed from
// the SAME underlying selection, in which case is_default=false may not even
// be a shape the real backend ever produces - and the user's real cause could
// instead be the resource-level ComputeStack field itself being empty/wrong
// for some clouds (e.g. older or CLI-registered ones), which no mock of mine
// can rule in or out. THE ARBITER for whether BUG C is actually fixed for the
// user is Shipwright's real CLI-create-then-cold-import repro against the
// integrated binary, not these tests. Treat these as "the hardening works as
// designed," not "the user's bug is closed."

// coldImportComputeStackMockServer is like newC3MockCloudServer but the cloud-
// level GET deliberately disagrees with the resource-level one: compute_stack
// is "VM" at /clouds/{id} (simulating the backend's derivation defaulting to
// VM because it either found no primary resource, or - the case this repro
// exists to distinguish - found one but the caller passes isDefault=false),
// while the sole resource in /clouds/{id}/resources actually reports "K8S".
// A correct cold import must still yield compute_stack = "K8S": that's the
// resource actually running, not whatever the cloud-level field derived.
func coldImportComputeStackMockServer(t *testing.T, cloudID string, isDefault bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "cold-import-k8s", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"
	}`, cloudID)
	resourcesJSON := fmt.Sprintf(`[{
		"name": "default", "is_default": %[1]t, "cloud_deployment_id": "cldrsrc_cold_import",
		"compute_stack": "K8S", "region": "us-east-2",
		"kubernetes_config": {
			"anyscale_operator_iam_identity": "arn:aws:iam::123456789012:role/cold-import-operator",
			"zones": ["us-east-2a"]
		},
		"object_storage": {"bucket_name": "s3://cold-import-bucket"}
	}]`, isDefault)

	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results": %s, "metadata": {"total": 1, "next_paging_token": null}}`, resourcesJSON)
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// testAccColdImportComputeStackCheck is shared by both variants below: assert
// the cold-imported anyscale_cloud has compute_stack = K8S (the resource's
// real stack), not VM (the cloud-level derivation's default).
func testAccColdImportComputeStackCheck(cloudID string) func([]*terraform.InstanceState) error {
	return func(states []*terraform.InstanceState) error {
		if len(states) != 1 {
			return fmt.Errorf("expected 1 imported resource, got %d", len(states))
		}
		s := states[0]
		if s.Attributes["id"] != cloudID {
			return fmt.Errorf("id = %q, want %q", s.Attributes["id"], cloudID)
		}
		if got := s.Attributes["compute_stack"]; got != "K8S" {
			return fmt.Errorf("compute_stack = %q, want %q - a cold import of a real K8S cloud must not report VM just because the cloud-level GET's derived field disagrees with the actual resource", got, "K8S")
		}
		return nil
	}
}

// TestAccCloudResource_ColdImport_ComputeStack_IsDefaultTrue is variant 1:
// the sole resource IS flagged is_default: true, matching
// findDefaultInCloudResources' only matching condition. This is expected to
// PASS against Forge's landed fix.
func TestAccCloudResource_ColdImport_ComputeStack_IsDefaultTrue(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_cold_import_default_true"
	server := coldImportComputeStackMockServer(t, cloudID, true)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "cold-import-k8s"
  cloud_provider = "AWS"
  compute_stack  = "K8S"
  region         = "us-east-2"

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::123456789012:role/cold-import-operator"
    zones                          = ["us-east-2a"]
  }

  object_storage {
    bucket_name = "cold-import-bucket"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:           config,
				ResourceName:     "anyscale_cloud.test",
				ImportState:      true,
				ImportStateId:    cloudID,
				ImportStateCheck: testAccColdImportComputeStackCheck(cloudID),
			},
		},
	})
}

// TestAccCloudResource_ColdImport_ComputeStack_IsDefaultFalse is variant 2:
// the sole resource is NOT flagged default (is_default: false). Confirmed via
// mutation test (see the sibling True variant's history in git-log if this
// comment predates it) that WITHOUT Forge's "exactly one resource is
// unambiguous" hardening, this variant fails (reports VM) while the
// IsDefaultTrue variant alone would still pass - proving the hardening, not
// just the base fix, is what this specific case needs. Passes now that the
// hardening has landed. See the file-section comment above for why this
// still isn't proof the user's real case is resolved.
func TestAccCloudResource_ColdImport_ComputeStack_IsDefaultFalse(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_cold_import_default_false"
	server := coldImportComputeStackMockServer(t, cloudID, false)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name           = "cold-import-k8s"
  cloud_provider = "AWS"
  compute_stack  = "K8S"
  region         = "us-east-2"

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::123456789012:role/cold-import-operator"
    zones                          = ["us-east-2a"]
  }

  object_storage {
    bucket_name = "cold-import-bucket"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:           config,
				ResourceName:     "anyscale_cloud.test",
				ImportState:      true,
				ImportStateId:    cloudID,
				ImportStateCheck: testAccColdImportComputeStackCheck(cloudID),
			},
		},
	})
}
