package acctest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// In-place updates for the cloud config-block attributes beyond subnets and
// file_storage, against the in-place contract
// (docs/decisions/cloud-resource-inplace-update/README.md). The mock is
// subnetsInPlaceServer, which stores the mutable fields, models the backend's
// absent-key-clears semantics, and captures every resources PUT body.
//
// Wire shape assertions are the point of these tests: the PUT body must be
// the live block echoed with ONLY the changed keys overlaid (a key the
// request omits is cleared, and a block the request omits is preserved), so
// a regression to a struct-built body that drops unmodeled live fields
// fails here, not in production.

// inPlaceCloudConfig builds an anyscale_cloud with the given in-place
// attribute values. profile sets aws_config.cluster_instance_profile_id;
// empty = omitted. endpoint sets object_storage.endpoint; empty = omitted.
func inPlaceCloudConfig(apiURL, securityGroup, profile, endpoint string) string {
	profileAttr := ""
	if profile != "" {
		profileAttr = fmt.Sprintf("\n    cluster_instance_profile_id = %q", profile)
	}
	endpointAttr := ""
	if endpoint != "" {
		endpointAttr = fmt.Sprintf("\n    endpoint = %q", endpoint)
	}
	return testAccProviderBlock(apiURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "createtime-mock"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"

  aws_config {
    vpc_id                      = "vpc-realct123"
    security_group_ids          = [%s]
    controlplane_iam_role_arn   = "arn:aws:iam::123456789012:role/real-crossaccount"
    dataplane_iam_role_arn      = "arn:aws:iam::123456789012:role/real-cluster-node"
    external_id                 = "org_real-external-id-ct"
%s
    subnet_ids_to_az = {
      "subnet-realct1" = "us-east-2a"
      "subnet-realct2" = "us-east-2b"
    }
  }

  object_storage {
    bucket_name = "real-ct-bucket"%s
  }
}
`, quotedList(securityGroup), profileAttr, endpointAttr)
}

func inPlaceCloudResourceConfig(apiURL, cloudID, securityGroup string) string {
	return testAccProviderBlock(apiURL) + fmt.Sprintf(`
resource "anyscale_cloud" "parent" {
  cloud_provider = "AWS"
  region         = "us-east-2"
  name           = "createtime-mock"
}

resource "anyscale_cloud_resource" "test" {
  cloud_id      = %q
  name          = "primary-resource-mock"
  region        = "us-east-2"
  compute_stack = "VM"

  aws_config {
    vpc_id                      = "vpc-realct123"
    security_group_ids          = [%s]
    controlplane_iam_role_arn   = "arn:aws:iam::123456789012:role/real-crossaccount"
    dataplane_iam_role_arn      = "arn:aws:iam::123456789012:role/real-cluster-node"
    external_id                 = "org_real-external-id-ct"

    subnet_ids_to_az = {
      "subnet-realct1" = "us-east-2a"
      "subnet-realct2" = "us-east-2b"
    }
  }
}
`, cloudID, quotedList(securityGroup))
}

func quotedList(items ...string) string {
	if len(items) == 0 {
		return ""
	}
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(quoted, ", ")
}

// putAWSConfig parses the last PUT body's aws_config object, or nil.
func putAWSConfig(body string) map[string]any {
	var reqs []struct {
		AWSConfig map[string]any `json:"aws_config"`
	}
	if json.Unmarshal([]byte(body), &reqs) != nil || len(reqs) != 1 {
		return nil
	}
	return reqs[0].AWSConfig
}

// putObjectStorage parses the last PUT body's object_storage object, or nil.
func putObjectStorage(body string) map[string]any {
	var reqs []struct {
		ObjectStorage map[string]any `json:"object_storage"`
	}
	if json.Unmarshal([]byte(body), &reqs) != nil || len(reqs) != 1 {
		return nil
	}
	return reqs[0].ObjectStorage
}

// ─── I1: a security-group change updates in place, one lossless PUT ────────

// TestAccCloudResource_InPlaceSecurityGroupChange_MockServer proves the
// headline behavior for a plain in-place attribute: the change plans an
// update (never a replace), exactly one PUT carries the new value, the PUT's
// aws_config is the live block with only that key overlaid (unmodeled-by-
// Terraform live values like cluster_instance_profile_id ride through), and
// the following plan is empty.
func TestAccCloudResource_InPlaceSecurityGroupChange_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: inPlaceCloudConfig(server.URL, "sg-realct1", "arn:aws:iam::123456789012:instance-profile/real-node-profile", "")},
			{
				Config: inPlaceCloudConfig(server.URL, "sg-newgroup2", "arn:aws:iam::123456789012:instance-profile/real-node-profile", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: inPlaceCloudConfig(server.URL, "sg-newgroup2", "arn:aws:iam::123456789012:instance-profile/real-node-profile", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Fatalf("resources PUTs = %d, want exactly 1 (the security-group change)", got)
	}
	aws := putAWSConfig(mock.lastPutBody())
	if aws == nil {
		t.Fatal("PUT carried no aws_config")
	}
	if got := fmt.Sprint(aws["security_group_ids"]); !strings.Contains(got, "sg-newgroup2") {
		t.Errorf("PUT security_group_ids = %v, want sg-newgroup2", aws["security_group_ids"])
	}
	if aws["cluster_instance_profile_id"] == nil {
		t.Error("PUT dropped cluster_instance_profile_id - the echo must carry every live field, or the backend clears it")
	}
	if aws["vpc_id"] != "vpc-realct123" {
		t.Errorf("PUT vpc_id = %v, want the live vpc-realct123 unchanged", aws["vpc_id"])
	}
	if _, ok := aws["subnet_ids"]; !ok {
		t.Error("PUT dropped subnet_ids - subnets belong to the same deployment; the plan did not change them")
	}
	// The contract names object_storage as sent only when it changed: no
	// object_storage in this PUT.
	if os := putObjectStorage(mock.lastPutBody()); os != nil {
		t.Errorf("PUT carried object_storage %v with no object_storage change; the backend preserves an omitted block", os)
	}
}

// TestAccCloudResourceResource_InPlaceSecurityGroupChange_MockServer mirrors
// I1 on anyscale_cloud_resource: same one-PUT, lossless-rehearsal contract.
func TestAccCloudResourceResource_InPlaceSecurityGroupChange_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	mock.resourceName = "primary-resource-mock"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: inPlaceCloudResourceConfig(server.URL, mock.cloudID, "sg-realct1")},
			{
				Config: inPlaceCloudResourceConfig(server.URL, mock.cloudID, "sg-newgroup2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud_resource.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Fatalf("resources PUTs = %d, want exactly 1", got)
	}
}

// ─── I2: removal sends the ABSENT key, and the backend stores it cleared ──

// TestAccCloudResource_InPlaceRemovalClears_MockServer: removing
// cluster_instance_profile_id from config plans an update whose PUT OMITS the
// key (absent clears - never an explicit empty value), the mock stores the
// cleared deployment, and the following plan is empty.
func TestAccCloudResource_InPlaceRemovalClears_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: inPlaceCloudConfig(server.URL, "sg-realct1", "arn:aws:iam::123456789012:instance-profile/real-node-profile", "")},
			{
				Config: inPlaceCloudConfig(server.URL, "sg-realct1", "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: inPlaceCloudConfig(server.URL, "sg-realct1", "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Fatalf("resources PUTs = %d, want exactly 1 (the removal)", got)
	}
	aws := putAWSConfig(mock.lastPutBody())
	if aws == nil {
		t.Fatal("PUT carried no aws_config")
	}
	if _, ok := aws["cluster_instance_profile_id"]; ok {
		t.Errorf("PUT carried cluster_instance_profile_id %q; a removal must leave the key OUT (absent clears)", aws["cluster_instance_profile_id"])
	}
	if got := fmt.Sprint(aws["security_group_ids"]); !strings.Contains(got, "sg-realct1") {
		t.Errorf("PUT security_group_ids = %v, want the unchanged live value carried", aws["security_group_ids"])
	}
	if mock.clusterInstanceProfile != nil {
		t.Errorf("mock live profile after the PUT = %q, want cleared", *mock.clusterInstanceProfile)
	}
}

// ─── I3: out-of-band change surfaces as drift and the apply reverts it ────

// TestAccCloudResource_InPlaceDriftShowsUpdateAndReverts_MockServer: a
// security group changed with `anyscale cloud update` shows as an in-place
// update on the next plan (Read refreshes non-null state attributes), and
// the apply reverts it with one PUT.
func TestAccCloudResource_InPlaceDriftShowsUpdateAndReverts_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: inPlaceCloudConfig(server.URL, "sg-realct1", "", "")},
			{
				PreConfig: func() {
					// The out-of-band change: someone re-pointed the
					// security groups with the Anyscale CLI.
					mock.setLiveSecurityGroups([]string{"sg-drifted9"})
				},
				Config: inPlaceCloudConfig(server.URL, "sg-realct1", "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Fatalf("resources PUTs = %d, want exactly 1 (the revert)", got)
	}
	aws := putAWSConfig(mock.lastPutBody())
	if got := fmt.Sprint(aws["security_group_ids"]); !strings.Contains(got, "sg-realct1") || strings.Contains(got, "sg-drifted9") {
		t.Errorf("PUT security_group_ids = %v, want the config value restored", aws["security_group_ids"])
	}
	if got := fmt.Sprint(mock.liveSecurityGroups()); got != "[sg-realct1]" {
		t.Errorf("live security groups after the revert = %v, want [sg-realct1]", mock.liveSecurityGroups())
	}
}

// ─── I4: an object_storage change rides the same PUT, losslessly ───────────

// TestAccCloudResource_InPlaceObjectStorageEchoesUnmodeledFields_MockServer:
// setting object_storage.endpoint overlays the change onto the live
// object_storage block, carrying the provider-unmodeled s3_addressing_style
// back unchanged - the exact field the old typed echo dropped and the
// backend cleared (criterion 11).
func TestAccCloudResource_InPlaceObjectStorageEchoesUnmodeledFields_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: inPlaceCloudConfig(server.URL, "sg-realct1", "", "")},
			{
				Config: inPlaceCloudConfig(server.URL, "sg-realct1", "", "https://minio.example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: inPlaceCloudConfig(server.URL, "sg-realct1", "", "https://minio.example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Fatalf("resources PUTs = %d, want exactly 1 (the endpoint change)", got)
	}
	osBlock := putObjectStorage(mock.lastPutBody())
	if osBlock == nil {
		t.Fatal("PUT carried no object_storage despite the endpoint change")
	}
	if osBlock["endpoint"] != "https://minio.example.com" {
		t.Errorf("PUT object_storage.endpoint = %v, want the new value", osBlock["endpoint"])
	}
	if osBlock["s3_addressing_style"] != "virtual" {
		t.Errorf("PUT object_storage.s3_addressing_style = %v, want the live value carried back (virtual)", osBlock["s3_addressing_style"])
	}
	if osBlock["bucket_name"] == nil || !strings.Contains(fmt.Sprint(osBlock["bucket_name"]), "real-ct-bucket") {
		t.Errorf("PUT object_storage.bucket_name = %v, want the live bucket carried", osBlock["bucket_name"])
	}
	if mock.objectStorage["s3_addressing_style"] != "virtual" {
		t.Errorf("mock stored object_storage %v; the unmodeled style must survive the update", mock.objectStorage)
	}
}

// ─── I5: managed clouds are refused at plan time ───────────────────────────

// TestAccCloudResource_InPlaceManagedCloudChangeRefused_MockServer: on an
// Anyscale-managed cloud, an in-place attribute change fails the plan with
// the managed-cloud refusal and no PUT is ever issued.
func TestAccCloudResource_InPlaceManagedCloudChangeRefused_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	mock.managed = true
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: inPlaceCloudConfig(server.URL, "sg-realct1", "", "")},
			{
				Config:             inPlaceCloudConfig(server.URL, "sg-newgroup2", "", ""),
				ExpectError:        regexp.MustCompile(`(?s)Cannot Update an Anyscale-Managed Cloud.*anyscale\s+cloud\s+setup`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs after a plan-time refusal = %d, want 0", got)
	}
}

// ─── I6: vpc_id rules are refused at plan time ────────────────────────────

// TestAccCloudResource_VPCChangeWithoutSubnetsRefused_MockServer: vpc_id
// may only change together with its subnets; changing it alone fails the
// plan with the VPC-membership error, and no PUT is issued.
func TestAccCloudResource_VPCChangeWithoutSubnetsRefused_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: inPlaceCloudConfig(server.URL, "sg-realct1", "", "")},
			{
				Config:             strings.Replace(inPlaceCloudConfig(server.URL, "sg-realct1", "", ""), "vpc-realct123", "vpc-other999", 1),
				ExpectError:        regexp.MustCompile("(?s)vpc_id Changed Without Its Subnets"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs after a plan-time refusal = %d, want 0", got)
	}
}

// ─── I7: vpc_id removal is refused at plan time ────────────────────────────

// TestAccCloudResource_InPlaceVPCRemovalRefused completes criterion 7's
// removal refusals on the AWS side: removing vpc_id (a VM stack needs one)
// must fail the plan, and no PUT may be issued.
func TestAccCloudResource_InPlaceVPCRemovalRefused_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	base := inPlaceCloudConfig(server.URL, "sg-realct1", "", "")
	noVPC := strings.Replace(base,
		"    vpc_id                      = \"vpc-realct123\"\n", "", 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config:             noVPC,
				ExpectError:        regexp.MustCompile(`(?s)Cannot Remove vpc_id`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs after the refusal = %d, want 0", got)
	}
}
