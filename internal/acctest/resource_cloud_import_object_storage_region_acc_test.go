package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// newCloudResourceMockServer is newC3MockCloudServer's counterpart for the
// standalone anyscale_cloud_resource type: CloudResourceResource.Create
// reads deployResp.Result.Name from the add_resource response itself
// (resource_cloud_resource.go, "resourceName := deployResp.Result.Name") to
// look itself up afterward via readCloudResource - a different shape from
// anyscale_cloud's own Create, which never needs add_resource's Name because
// it re-reads the whole cloud by ID instead. newC3MockCloudServer's
// add_resource handler was built for that other shape and omits "name"
// entirely, which is harmless for anyscale_cloud but makes
// CloudResourceResource.Create's own post-create lookup fail with "cloud
// resource not found" (verified by tracing the exact failure, not guessed).
// This mirrors newC3MockCloudServer's endpoints with that one field added -
// matching the real backend, which echoes the request's own name back
// (verified: add_cloud_resource never touches its parsed request beyond
// zones/mount_targets/memorydb fields, so name passes through as-given).
func newCloudResourceMockServer(t *testing.T, cloudID, cloudJSON, resourcesJSON, cloudResourceID, resourceName string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": %s}`, cloudJSON)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results": %s, "metadata": {"total": 1, "next_paging_token": null}}`, resourcesJSON)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {"name": %q, "cloud_resource_id": %q}}`, resourceName, cloudResourceID)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/remove_resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccCloudResource_ObjectStorageRegionSemanticEqualOnImport_AWSVM is the
// regression test for the object_storage.region "explicit-equal" import
// replace-loop (WORKBENCH "Import gap: object_storage.region explicit-equal";
// OBJECT-STORAGE-REGION-DESIGN-CORRECTION.md - Fix C).
//
// Distinct from TestAccCloudResource_ImportRecoversStorageBlocks_AWSVM
// (resource_cloud_import_storage_acc_test.go), which OMITS region from
// config entirely - THIS test's config EXPLICITLY sets region to the same
// value as the cloud's own region, the case the backend itself cannot tell
// apart from "never set": product backend's toExternalCloudDeployment only
// emits object_storage.region when the stored bucket region differs from
// the stored deployment region - when they are equal (verified directly
// against source AND a live cloud's real API response) the API returns
// region as JSON null, regardless of why they are equal. So this mock
// deliberately returns object_storage WITHOUT a real region value (null),
// matching the real API precisely - a mock that invents a "us-east-2" value
// here (the withdrawn design's own test did exactly this) is unrealistic and
// would pass without proving anything, since the real backend never sends
// that value back for this case.
//
// Step 1 (Create): config sets region explicitly, so state gets the literal
// configured value ("us-east-2") - Optional (not Computed) just persists
// whatever config set, same as any other Optional attribute.
// Step 2 (Import): recovers object_storage.region as null (the mock's real
// shape) - this legitimately DIFFERS from step 1's "us-east-2", so region is
// excluded from ImportStateVerify's byte-compare, and ImportStateCheck
// asserts the null value directly rather than leaving it as an implicit
// exclusion - this is not a bug to chase, it is the exact condition the fix
// targets, and proving IMPORT recovers it as null (matching the real API) is
// this test's job.
//
// This test does NOT itself prove "a subsequent plan against the imported
// state is a no-op" - ImportState/ImportStateVerify steps run against their
// own temporary state for comparison purposes; they do not feed their
// result forward as the baseline for a later TestStep in the same test
// (verified empirically: a deliberately broken build - regionSemanticEqualPlanModifier
// removed - still passed this test's original 3-step form, because step 3's
// plan was actually comparing against step 1's state, not step 2's import).
// TestAccCloudResource_ObjectStorageRegionSelfHealsFromNullState_AWSVM is
// where the "state has null, config sets the cloud's own region, no replace
// results" mechanism is actually proven and mutation-tested, using two
// Config steps (which DO carry state forward correctly) instead of Import.
// The two tests together cover the full bug: this one proves import
// produces the ambiguous null shape; that one proves planning against that
// shape doesn't replace.
func TestAccCloudResource_ObjectStorageRegionSemanticEqualOnImport_AWSVM(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_storage_region_import_aws_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "storage-region-import-aws-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": false,
		"is_private_cloud": true
	}`, cloudID)
	// THE reproducing shape: object_storage.region is JSON null in the
	// mock's resources-list response - exactly what the real backend
	// returns when the stored bucket region equals the deployment region
	// ("us-east-2" here, matching cloudJSON's own region above), confirmed
	// against a live cloud's actual API response, not assumed. A fixture
	// that instead hardcodes a real value here would be unrealistic and
	// would mask this exact bug.
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_storage_region_mock_default",
		"compute_stack": "VM", "region": "us-east-2",
		"aws_config": {
			"vpc_id": "vpc-storageregiontest",
			"subnet_ids": ["subnet-storageregiontest1", "subnet-storageregiontest2"],
			"zones": ["us-east-2a", "us-east-2b"],
			"security_group_ids": ["sg-storageregiontest"],
			"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/storageregiontest-crossaccount",
			"cluster_iam_role_id": "arn:aws:iam::123456789012:role/storageregiontest-cluster-node",
			"external_id": "storageregiontest-external-id"
		},
		"object_storage": {"bucket_name": "s3://my-region-bucket", "region": null}
	}]`

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_storage_region_mock_default")
	resourceName := "anyscale_cloud.test"
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name             = "storage-region-import-aws-mock"
  cloud_provider   = "AWS"
  compute_stack    = "VM"
  region           = "us-east-2"
  is_private_cloud = true

  aws_config {
    vpc_id            = "vpc-storageregiontest"
    subnet_ids_to_az = {
      "subnet-storageregiontest1" = "us-east-2a"
      "subnet-storageregiontest2" = "us-east-2b"
    }
    security_group_ids        = ["sg-storageregiontest"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/storageregiontest-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/storageregiontest-cluster-node"
    external_id               = "storageregiontest-external-id"
  }

  # THE explicit-equal case: region set on purpose, to the same value as
  # the cloud's own region. Not an oversight - do not "clean up" to match
  # the omitted-region test next door; that would test a different (already
  # fine) case.
  object_storage {
    bucket_name = "my-region-bucket"
    region      = "us-east-2"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Establish real applied state - region holds the literal
				// configured value, since Optional (non-Computed) attributes
				// simply persist whatever config sets.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "object_storage.bucket_name", "my-region-bucket"),
					resource.TestCheckResourceAttr(resourceName, "object_storage.region", "us-east-2"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				// THE regression proof for this test: import must recover
				// region as null (the mock's real shape), legitimately
				// different from step 1's "us-east-2" - excluded from the
				// strict byte-compare, and explicitly asserted via
				// ImportStateCheck rather than left as a silent exclusion,
				// since proving the ambiguous shape lands correctly is the
				// entire point. See TestAccCloudResource_ObjectStorageRegionSelfHealsFromNullState_AWSVM
				// for the "does a later plan against this replace" half.
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"credentials", "is_empty_cloud", "object_storage.region",
				},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					if got, ok := states[0].Attributes["object_storage.region"]; ok && got != "" {
						return fmt.Errorf(`object_storage.region = %q, want unset/null - the real backend never returns a region equal to the cloud's own region`, got)
					}
					return nil
				},
			},
		},
	})
}

// TestAccCloudResource_ObjectStorageRegionSelfHealsFromNullState_AWSVM proves
// the self-heal half of Fix C directly, distinct from the import-based
// lifecycle test above: a resource whose state ALREADY has
// object_storage.region=null (e.g. imported under a prior, buggy provider
// version, or - as modeled here - created with region genuinely omitted)
// picks up region equal to the cloud's own region on its very next plan with
// NO re-import required, and - the actual bug bar - without a REPLACE. This
// is the concrete improvement over the withdrawn Optional+Computed design,
// which could only ever have self-healed via a fresh re-import.
//
// "Self-heal" here means no replace, not a literal zero-diff plan: Terraform
// Core requires the planned value to reflect config exactly for a
// non-Computed attribute (verified directly against a real acceptance run -
// see regionSemanticEqualPlanModifier's own doc comment), so a state that
// genuinely has null where config says "us-east-2" still shows as one
// in-place update reconciling that difference - regionSemanticEqualPlanModifier
// only ever controls whether that update is also a REPLACE, never whether
// there is a diff line at all. That one-time update is the harmless
// reconcile this codebase already documents elsewhere for a comparable case
// (requiredImportConfigBlocks's own doc comment: "a plan diff to review, not
// a destructive replace") - asserted below via plancheck.ResourceActionUpdate,
// not ResourceActionNoop.
func TestAccCloudResource_ObjectStorageRegionSelfHealsFromNullState_AWSVM(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_storage_region_selfheal_aws_mock"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "storage-region-selfheal-aws-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": false,
		"is_private_cloud": true
	}`, cloudID)
	resourcesJSON := `[{
		"name": "default", "is_default": true, "cloud_resource_id": "cldrsrc_storage_region_selfheal_default",
		"compute_stack": "VM", "region": "us-east-2",
		"aws_config": {
			"vpc_id": "vpc-storageregionselfheal",
			"subnet_ids": ["subnet-storageregionselfheal1", "subnet-storageregionselfheal2"],
			"zones": ["us-east-2a", "us-east-2b"],
			"security_group_ids": ["sg-storageregionselfheal"],
			"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/storageregionselfheal-crossaccount",
			"cluster_iam_role_id": "arn:aws:iam::123456789012:role/storageregionselfheal-cluster-node",
			"external_id": "storageregionselfheal-external-id"
		},
		"object_storage": {"bucket_name": "s3://my-selfheal-bucket", "region": null}
	}]`

	server := newC3MockCloudServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_storage_region_selfheal_default")
	resourceName := "anyscale_cloud.test"
	baseConfig := testAccProviderBlock(server.URL) + `
resource "anyscale_cloud" "test" {
  name             = "storage-region-selfheal-aws-mock"
  cloud_provider   = "AWS"
  compute_stack    = "VM"
  region           = "us-east-2"
  is_private_cloud = true

  aws_config {
    vpc_id            = "vpc-storageregionselfheal"
    subnet_ids_to_az = {
      "subnet-storageregionselfheal1" = "us-east-2a"
      "subnet-storageregionselfheal2" = "us-east-2b"
    }
    security_group_ids        = ["sg-storageregionselfheal"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/storageregionselfheal-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/storageregionselfheal-cluster-node"
    external_id               = "storageregionselfheal-external-id"
  }

  object_storage {
    bucket_name = "my-selfheal-bucket"%s
  }
}
`
	// Step 1: region genuinely OMITTED from config - state lands on null,
	// the same starting shape a cloud imported under a prior buggy provider
	// version would have (state region=null either way, regardless of how
	// it got there).
	configWithoutRegion := fmt.Sprintf(baseConfig, "")
	// Step 2: config now ADDS region, set equal to the cloud's own region -
	// state is STILL null going into this plan (nothing between steps
	// refreshes object_storage from the API - it is deliberately not
	// Read-refreshed, C12). This is the self-heal moment: no re-import, just
	// the next plan.
	configWithRegion := fmt.Sprintf(baseConfig, `
    region      = "us-east-2"`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configWithoutRegion,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "object_storage.bucket_name", "my-selfheal-bucket"),
					resource.TestCheckNoResourceAttr(resourceName, "object_storage.region"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				// THE self-heal proof: state still has region=null from step
				// 1, config now sets region="us-east-2" (the cloud's own
				// region) - must plan as an in-place update reconciling that
				// one attribute, NEVER a replace, with no import in between.
				// Update, not Noop: a null-vs-"us-east-2" difference is a
				// real diff Terraform must show; the fix is that it never
				// escalates to a destroy-and-recreate (see the function doc
				// comment for why literal no-op is not the right bar here).
				Config: configWithRegion,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr(resourceName, "object_storage.region", "us-east-2"),
			},
		},
	})
}

// TestAccCloudResourceResource_ObjectStorageRegionSemanticEqualOnImport_AWSVM
// is the anyscale_cloud_resource (standalone/plural) companion to
// TestAccCloudResource_ObjectStorageRegionSemanticEqualOnImport_AWSVM above -
// same explicit-equal reproducing shape, same requiredImportConfigBlocks
// call underneath (CloudResourceResource.ImportState shares that function
// with CloudResource, per its own doc comment), proved independently rather
// than assumed from the sibling resource type. Per this repo's cross-stack-
// fixture discipline (a sibling fixture has caught bugs the primary type
// missed before - it did again here: this resource type needed its own mock,
// see newCloudResourceMockServer, and its own import round-trips less of
// object_storage/aws_config than the singular type does, matching this
// resource type's own established real-infra precedent), the two resource
// types get independent proof rather than relying on the singular test
// alone. Like its singular sibling, this only proves import recovers region
// as null - TestAccCloudResource_ObjectStorageRegionSelfHealsFromNullState_AWSVM
// proves the "no replace" half, and that mechanism is resource-type-agnostic
// (the same regionSemanticEqualPlanModifier, pinned for both resources by
// TestObjectStorageRegionSemanticEqualWithCorrectModifierOrder), so it is not
// duplicated here.
func TestAccCloudResourceResource_ObjectStorageRegionSemanticEqualOnImport_AWSVM(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_storage_region_import_aws_mock_plural"
	cloudJSON := fmt.Sprintf(`{
		"id": %[1]q, "name": "storage-region-import-aws-mock-plural", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM", "is_default": false,
		"is_private_cloud": true
	}`, cloudID)
	resourcesJSON := `[{
		"name": "test-resource", "is_default": false, "cloud_resource_id": "cldrsrc_storage_region_mock_plural",
		"compute_stack": "VM", "region": "us-east-2",
		"aws_config": {
			"vpc_id": "vpc-storageregiontestplural",
			"subnet_ids": ["subnet-storageregiontestplural1", "subnet-storageregiontestplural2"],
			"zones": ["us-east-2a", "us-east-2b"],
			"security_group_ids": ["sg-storageregiontestplural"],
			"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/storageregiontestplural-crossaccount",
			"cluster_iam_role_id": "arn:aws:iam::123456789012:role/storageregiontestplural-cluster-node",
			"external_id": "storageregiontestplural-external-id"
		},
		"object_storage": {"bucket_name": "s3://my-region-bucket-plural", "region": null}
	}]`

	server := newCloudResourceMockServer(t, cloudID, cloudJSON, resourcesJSON, "cldrsrc_storage_region_mock_plural", "test-resource")
	resourceName := "anyscale_cloud_resource.test"
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud_resource" "test" {
  cloud_id      = %[1]q
  name          = "test-resource"
  compute_stack = "VM"
  region        = "us-east-2"

  aws_config {
    vpc_id            = "vpc-storageregiontestplural"
    subnet_ids_to_az = {
      "subnet-storageregiontestplural1" = "us-east-2a"
      "subnet-storageregiontestplural2" = "us-east-2b"
    }
    security_group_ids        = ["sg-storageregiontestplural"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/storageregiontestplural-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/storageregiontestplural-cluster-node"
    external_id               = "storageregiontestplural-external-id"
  }

  # THE explicit-equal case, same as the singular resource's test - region
  # set on purpose, to the same value as the cloud's own region.
  object_storage {
    bucket_name = "my-region-bucket-plural"
    region      = "us-east-2"
  }
}
`, cloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "object_storage.bucket_name", "my-region-bucket-plural"),
					resource.TestCheckResourceAttr(resourceName, "object_storage.region", "us-east-2"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				// aws_config and object_storage as a whole are excluded from
				// the strict verify, matching this resource type's own
				// established real-infra precedent
				// (resource_cloud_resource_acc_test.go: "write-only block:
				// nested attrs are not echoed back by /clouds/{id}/resources"
				// for a non-default cloud_resource) - not specific to this
				// fix, a pre-existing characteristic of importing a
				// standalone (non-default) anyscale_cloud_resource.
				// ImportStateCheck still asserts region directly - THE
				// regression proof for this test, same as the singular
				// resource's own.
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: testAccCloudResourceImportStateIdFunc(resourceName),
				ImportStateVerifyIgnore: []string{
					"aws_config", "cloud_provider", "object_storage",
				},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					if got, ok := states[0].Attributes["object_storage.region"]; ok && got != "" {
						return fmt.Errorf(`object_storage.region = %q, want unset/null - the real backend never returns a region equal to the cloud's own region`, got)
					}
					return nil
				},
			},
		},
	})
}
