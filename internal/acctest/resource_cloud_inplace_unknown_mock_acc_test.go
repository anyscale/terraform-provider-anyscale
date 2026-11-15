package acctest

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// Unknown values in the in-place update path, against the same mocks as the
// in-place suites (subnetsInPlaceServer, inplaceMixedServer). A configuration
// may reference a resource the same apply creates (terraform_data stands in
// for, say, a new aws_security_group): at plan time that reference is
// UNKNOWN, and the contract is
// docs/decisions/cloud-resource-inplace-update/README.md:
//
//   - a plan must never fail on an unknown in an in-place attribute, and
//     destroy must keep working while the config still holds the reference
//     (the reference resolves only in an apply that never ran);
//   - an unknown records "changed, value not yet known"; the apply PUTs the
//     resolved value in config order;
//   - plan-time refusals fire only on decided values; unknowns defer to the
//     Update-time re-check, which raises the same diagnostics before the
//     first write;
//   - the Anyscale-managed refusal fires on any in-place change, decided
//     or not.

// unknownReferral appends the terraform_data resource every change-config
// here resolves its unknown from. input is one HCL literal.
func unknownReferral(input string) string {
	return fmt.Sprintf(`
resource "terraform_data" "probe" {
  input = %s
}
`, input)
}

// ─── A/B: security_group_ids gains an unknown, whole or one element ────────

// TestAccCloudResource_InPlaceUnknownSecurityGroupElement_MockServer: an
// unknown element in security_group_ids plans an update in place (never a
// conversion error), the apply PUTs the resolved list in config order, and
// the following plan is empty.
func TestAccCloudResource_InPlaceUnknownSecurityGroupElement_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	base := inPlaceCloudConfig(server.URL, "sg-realct1", "", "")
	change := strings.Replace(base,
		`security_group_ids          = ["sg-realct1"]`,
		`security_group_ids          = ["sg-realct1", terraform_data.probe.output]`, 1) +
		unknownReferral(`"sg-newgroup99"`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config: change,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: change,
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
	sgs := fmt.Sprint(aws["security_group_ids"])
	if !strings.Contains(sgs, "sg-realct1") || !strings.Contains(sgs, "sg-newgroup99") {
		t.Errorf("PUT security_group_ids = %v, want both groups", sgs)
	}
	if strings.Index(sgs, "sg-realct1") > strings.Index(sgs, "sg-newgroup99") {
		t.Errorf("PUT security_group_ids = %v, want config order [sg-realct1, sg-newgroup99]", sgs)
	}
	if aws["vpc_id"] != "vpc-realct123" {
		t.Errorf("PUT vpc_id = %v, want the live vpc-realct123 unchanged", aws["vpc_id"])
	}
}

// TestAccCloudResource_InPlaceUnknownWholeSecurityGroupList_MockServer: the
// whole security_group_ids list unknown behaves the same: update in place,
// the apply PUTs the resolved values.
func TestAccCloudResource_InPlaceUnknownWholeSecurityGroupList_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	base := inPlaceCloudConfig(server.URL, "sg-realct1", "", "")
	change := strings.Replace(base,
		`security_group_ids          = ["sg-realct1"]`,
		`security_group_ids          = terraform_data.probe.output`, 1) +
		unknownReferral(`["sg-realct1", "sg-newgroup99"]`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config: change,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: change,
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
	sgs := fmt.Sprint(aws["security_group_ids"])
	if !strings.Contains(sgs, "sg-realct1") || !strings.Contains(sgs, "sg-newgroup99") {
		t.Errorf("PUT security_group_ids = %v, want both groups", sgs)
	}
}

// ─── C: the GCP and K8S lists carry the same contract ──────────────────────

// TestAccCloudResource_InPlaceUnknownElementGCPLists_MockServer: one unknown
// element in gcp_config.subnet_names or gcp_config.firewall_policy_names
// plans an update in place, and the apply PUTs the resolved list.
func TestAccCloudResource_InPlaceUnknownElementGCPLists_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	t.Run("subnet_names", func(t *testing.T) {
		SkipIfNotAcceptanceTest(t)
		server, mock := newInplaceMixedServer(t, "VM")
		base := inplaceGCPConfig(server.URL, `"fw-existing"`)
		change := strings.Replace(base,
			`subnet_names                  = ["subnet-gcp-one", "subnet-gcp-two"]`,
			`subnet_names                  = ["subnet-gcp-one", "subnet-gcp-two", terraform_data.probe.output]`, 1) +
			unknownReferral(`"subnet-gcp-new"`)
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: ProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: base},
				{
					Config: change,
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply: []plancheck.PlanCheck{
							plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
						},
					},
				},
				{
					Config: change,
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply: []plancheck.PlanCheck{
							plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionNoop),
						},
					},
				},
			},
		})
		if got := mock.putCount(); got != 1 {
			t.Fatalf("resources PUTs = %d, want exactly 1", got)
		}
		gcp := putBlock(mock.lastPutBody(), "gcp_config")
		if gcp == nil {
			t.Fatal("PUT carried no gcp_config")
		}
		if got := fmt.Sprint(gcp["subnet_names"]); !strings.Contains(got, "subnet-gcp-new") {
			t.Errorf("PUT subnet_names = %v, want the resolved subnet-gcp-new", got)
		}
	})
	t.Run("firewall_policy_names", func(t *testing.T) {
		SkipIfNotAcceptanceTest(t)
		server, mock := newInplaceMixedServer(t, "VM")
		base := inplaceGCPConfig(server.URL, `"fw-existing"`)
		change := strings.Replace(base,
			`firewall_policy_names = ["fw-existing"]`,
			`firewall_policy_names = ["fw-existing", terraform_data.probe.output]`, 1) +
			unknownReferral(`"fw-new-policy"`)
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: ProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: base},
				{
					Config: change,
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply: []plancheck.PlanCheck{
							plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
						},
					},
				},
				{
					Config: change,
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply: []plancheck.PlanCheck{
							plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionNoop),
						},
					},
				},
			},
		})
		if got := mock.putCount(); got != 1 {
			t.Fatalf("resources PUTs = %d, want exactly 1", got)
		}
		gcp := putBlock(mock.lastPutBody(), "gcp_config")
		if gcp == nil {
			t.Fatal("PUT carried no gcp_config")
		}
		if got := fmt.Sprint(gcp["firewall_policy_names"]); !strings.Contains(got, "fw-new-policy") {
			t.Errorf("PUT firewall_policy_names = %v, want the resolved fw-new-policy", got)
		}
	})
}

// TestAccCloudResource_InPlaceUnknownElementZones_MockServer: an unknown
// element in kubernetes_config.zones plans an update in place, and the
// lossless echo still carries the unmodeled custom_image_registry.
func TestAccCloudResource_InPlaceUnknownElementZones_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newInplaceMixedServer(t, "K8S")
	base := inplaceK8SConfig(server.URL, `"us-central1-a"`)
	change := inplaceK8SConfig(server.URL, `"us-central1-a", terraform_data.probe.output`) +
		unknownReferral(`"us-central1-b"`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config: change,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: change,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Fatalf("resources PUTs = %d, want exactly 1", got)
	}
	k8s := putBlock(mock.lastPutBody(), "kubernetes_config")
	if k8s == nil {
		t.Fatal("PUT carried no kubernetes_config")
	}
	if got := fmt.Sprint(k8s["zones"]); got != "[us-central1-a us-central1-b]" {
		t.Errorf("PUT zones = %v, want both resolved zones", got)
	}
	if k8s["custom_image_registry"] != "us-docker.pkg.dev/my-gcp-project/anyscale" {
		t.Errorf("PUT custom_image_registry = %v, want the live value carried back", k8s["custom_image_registry"])
	}
}

// ─── D: vpc_id unknown defers the refusal to the apply ─────────────────────

// TestAccCloudResource_UnknownVPCChangeDeferredToApply_MockServer: an unknown
// vpc_id must plan cleanly (a plan-time refusal here is the bug this guards
// against - the value is not decided yet), and when it resolves to another
// VPC with unchanged subnets, the APPLY raises the deferred "vpc_id Changed
// Without Its Subnets" - before any PUT, which the mock confirms stays at
// zero.
func TestAccCloudResource_UnknownVPCChangeDeferredToApply_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	base := inPlaceCloudConfig(server.URL, "sg-realct1", "", "")
	change := strings.Replace(base, `"vpc-realct123"`, "terraform_data.probe.output", 1) +
		unknownReferral(`"vpc-other999"`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				// The plan itself must succeed: the refusal deferred to apply.
				Config:             change,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// The apply, on the resolved value, raises the same refusal
				// the plan deferral owes - before the first write.
				Config:      change,
				ExpectError: regexp.MustCompile(`(?s)vpc_id Changed Without Its Subnets`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs = %d, want 0 (the refusal must precede the first write)", got)
	}
}

// TestAccCloudResource_UnknownVPCResolvesToSameValue_MockServer: the same
// deferral must not over-refuse: an unknown vpc_id that resolves to the SAME
// value is a no-op - the plan is fine, the apply writes nothing, and the
// re-plan is empty.
func TestAccCloudResource_UnknownVPCResolvesToSameValue_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	base := inPlaceCloudConfig(server.URL, "sg-realct1", "", "")
	change := strings.Replace(base, `"vpc-realct123"`, "terraform_data.probe.output", 1) +
		unknownReferral(`"vpc-realct123"`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config: change,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: change,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs = %d, want 0 (same resolved vpc_id is a no-op)", got)
	}
}

// ─── E: an unknown subnet_ids_to_az value plans fine ───────────────────────

// TestAccCloudResource_UnknownSubnetZonePlansFine_MockServer: an unknown zone
// value in subnet_ids_to_az must not fail the plan (the map conversion is as
// unable to take an unknown as the list one). Resolving to the same zone is
// a no-op apply with no PUT.
func TestAccCloudResource_UnknownSubnetZonePlansFine_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	base := inPlaceCloudConfig(server.URL, "sg-realct1", "", "")
	change := strings.Replace(base,
		`"subnet-realct1" = "us-east-2a"`,
		`"subnet-realct1" = terraform_data.probe.output`, 1) +
		unknownReferral(`"us-east-2a"`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config: change,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: change,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs = %d, want 0 (same resolved zone is a no-op)", got)
	}
}

// ─── G: an Anyscale-managed cloud refuses even an undecided change ─────────

// TestAccCloudResource_ManagedCloudUnknownChangeRefused_MockServer: the
// managed-cloud refusal fires on an in-place change regardless of whether the
// new value is decided: an unknown security-group element on a managed cloud
// still plans the "Cannot Update an Anyscale-Managed Cloud" error, not a
// conversion error and not a clean plan.
func TestAccCloudResource_ManagedCloudUnknownChangeRefused_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	mock.managed = true
	base := inPlaceCloudConfig(server.URL, "sg-realct1", "", "")
	change := strings.Replace(base,
		`security_group_ids          = ["sg-realct1"]`,
		`security_group_ids          = ["sg-realct1", terraform_data.probe.output]`, 1) +
		unknownReferral(`"sg-newgroup99"`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config:             change,
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

// ─── F: destroy works while the config holds an unknown reference ─────────

// TestAccCloudResource_DestroyWithUnknownConfigReferral_MockServerCLI: the
// hostage case. After an apply, the config is edited to reference a resource
// that was never applied (terraform_data is the stand-in), so every later
// plan - destroy's included - meets an unknown in security_group_ids. The
// real-infra gate reproduced a conversion error that blocked destroy; destroy
// must now succeed and tear everything down. Run through the terraform CLI
// because resource.Test always ends from the last applied state, which never
// holds this shape.
func TestAccCloudResource_DestroyWithUnknownConfigReferral_MockServerCLI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, _ := newSubnetsInPlaceServer(t)
	base := inPlaceCloudConfig(server.URL, "sg-realct1", "", "")
	referencing := strings.Replace(base,
		`security_group_ids          = ["sg-realct1"]`,
		`security_group_ids          = ["sg-realct1", terraform_data.probe.output]`, 1) +
		unknownReferral(`"sg-neverapplied99"`)
	// writeCLIWorkspace... supplies its own preamble; hcl here repeats the
	// provider block so the config carries the mock API endpoint.
	run, rewrite := cliDevWorkspaceEditable(t, base)
	if _, raw, err := run("init", "-input=false", "-no-color"); err != nil {
		t.Fatalf("init failed: %s\n%s", err, raw)
	}
	if _, raw, err := run("apply", "-auto-approve", "-input=false", "-no-color"); err != nil {
		t.Fatalf("baseline apply failed: %s\n%s", err, raw)
	}
	rewrite(referencing)
	_, raw, err := run("destroy", "-auto-approve", "-input=false", "-no-color")
	if err != nil {
		t.Fatalf("destroy with an unknown-referencing config failed (the hostage case): %s\n%s", err, raw)
	}
	if strings.Contains(raw, "Value Conversion Error") {
		t.Fatalf("destroy hit a conversion error on the unknown reference:\n%s", raw)
	}
	if _, raw, err := run("state", "list"); err != nil || strings.TrimSpace(raw) != "" {
		t.Errorf("state not empty after destroy: err=%v\n%s", err, raw)
	}
}

// ─── the split pattern and the deferred subnet list-form refusal ──────────

// TestAccCloudResourceResource_InPlaceUnknownSecurityGroupElement_MockServer:
// anyscale_cloud_resource walks the same plan-time path from its own
// ModifyPlan, so the unknown element must plan and apply there too, with
// the same one-PUT, config-order PUT.
func TestAccCloudResourceResource_InPlaceUnknownSecurityGroupElement_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	mock.resourceName = "primary-resource-mock"
	base := inPlaceCloudResourceConfig(server.URL, mock.cloudID, "sg-realct1")
	change := strings.Replace(base,
		`security_group_ids          = ["sg-realct1"]`,
		`security_group_ids          = ["sg-realct1", terraform_data.probe.output]`, 1) +
		unknownReferral(`"sg-newgroup99"`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config: change,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud_resource.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: change,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud_resource.test", plancheck.ResourceActionNoop),
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
	sgs := fmt.Sprint(aws["security_group_ids"])
	if !strings.Contains(sgs, "sg-realct1") || !strings.Contains(sgs, "sg-newgroup99") {
		t.Errorf("PUT security_group_ids = %v, want both groups", sgs)
	}
	if strings.Index(sgs, "sg-realct1") > strings.Index(sgs, "sg-newgroup99") {
		t.Errorf("PUT security_group_ids = %v, want config order [sg-realct1, sg-newgroup99]", sgs)
	}
}

// TestAccCloudResource_UnknownSubnetListSetChangeDeferredToApply_MockServer:
// an unknown subnet_ids element defers the list-form set-change refusal;
// resolving to a different set, the APPLY raises the plan-time diagnostic -
// "Changing subnets in place requires aws_config.subnet_ids_to_az" - and the
// mock records zero PUTs.
func TestAccCloudResource_UnknownSubnetListSetChangeDeferredToApply_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	base := inPlaceCloudConfig(server.URL, "sg-realct1", "", "")
	listForm := strings.Replace(base,
		`subnet_ids_to_az = {
      "subnet-realct1" = "us-east-2a"
      "subnet-realct2" = "us-east-2b"
    }`,
		`subnet_ids        = ["subnet-realct1", "subnet-realct2"]`, 1)
	change := strings.Replace(listForm,
		`subnet_ids        = ["subnet-realct1", "subnet-realct2"]`,
		`subnet_ids        = ["subnet-realct1", terraform_data.probe.output]`, 1) +
		unknownReferral(`"subnet-other999"`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: listForm},
			{
				// The plan must succeed: the refusal defers.
				Config:             change,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// The apply, on the resolved set, raises the deferred
				// refusal before the first write.
				Config:      change,
				ExpectError: regexp.MustCompile(`(?s)Changing subnets in place requires aws_config.subnet_ids_to_az`),
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs = %d, want 0 (the refusal must precede the first write)", got)
	}
}

// ─── object_storage scalars follow the same rule ───────────────────────────

// TestAccCloudResource_InPlaceUnknownObjectStorageEndpoint_MockServer: an
// unknown object_storage.endpoint plans an in-place update and the apply
// PUTs the resolved value, with the lossless object_storage echo intact.
func TestAccCloudResource_InPlaceUnknownObjectStorageEndpoint_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	base := inPlaceCloudConfig(server.URL, "sg-realct1", "", "")
	change := strings.Replace(base,
		`object_storage {
    bucket_name = "real-ct-bucket"`,
		`object_storage {
    bucket_name = "real-ct-bucket"
    endpoint    = terraform_data.probe.output`, 1) +
		unknownReferral(`"https://os-endpoint.example"`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config: change,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: change,
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
	os := putObjectStorage(mock.lastPutBody())
	if os == nil {
		t.Fatal("PUT carried no object_storage despite the endpoint change")
	}
	if os["endpoint"] != "https://os-endpoint.example" {
		t.Errorf("PUT object_storage.endpoint = %v, want the resolved value", os["endpoint"])
	}
	if os["bucket_name"] != "s3://real-ct-bucket" {
		t.Errorf("PUT bucket_name = %v, want the live value carried back", os["bucket_name"])
	}
}
