package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestCloudResourceResourceStateUpgradeV1toV2_DropsStatus mirrors
// TestCloudResourceStateUpgradeV1toV2_DropsEnableSystemCluster
// (resource_cloud_upgrade_test.go) almost exactly - that test is this
// repo's own established, precedented shape for "prove a removed Computed
// attribute auto-migrates cleanly," cited directly by
// PR1-STATUS-REMOVAL-PLAN.md ("mirroring the enable_system_cluster/
// kubernetes_config CHANGELOG promise"). This is the LOAD-BEARING test for
// the removal per that plan doc - a schema-absence check alone (see
// TestCloudResourceStatusRemoved in schema_contract_test.go) proves the
// attribute is gone, but not that existing state upgrades without a
// spurious diff or a hard error; this test proves that by driving the REAL
// production UpgradeState function end to end, not a stand-in.
func TestCloudResourceResourceStateUpgradeV1toV2_DropsStatus(t *testing.T) {
	ctx := context.Background()

	awsConfig := types.ObjectValueMust(awsConfigAttrTypes(), map[string]attr.Value{
		"vpc_id":                      types.StringValue("vpc-statusv1v2"),
		"subnet_ids":                  types.ListNull(types.StringType),
		"subnet_ids_to_az":            types.MapValueMust(types.StringType, map[string]attr.Value{"subnet-1": types.StringValue("us-east-2a")}),
		"security_group_ids":          types.ListValueMust(types.StringType, []attr.Value{types.StringValue("sg-statusv1v2")}),
		"controlplane_iam_role_arn":   types.StringValue("arn:aws:iam::123456789012:role/control-statusv1v2"),
		"dataplane_iam_role_arn":      types.StringValue("arn:aws:iam::123456789012:role/data-statusv1v2"),
		"cluster_instance_profile_id": types.StringNull(),
		"external_id":                 types.StringValue("ext-id-statusv1v2"),
		"memorydb_cluster_name":       types.StringNull(),
		"memorydb_cluster_arn":        types.StringNull(),
		"memorydb_cluster_endpoint":   types.StringNull(),
	})

	// THE reproducing shape: status carries a REAL, non-null value in prior
	// state (a live cloud_resource that was actually read pre-removal would
	// have this populated from the same underlying value as operator_status),
	// not left null/zero-value - a fixture that leaves it null would go
	// green trivially and prove nothing about the drop actually happening.
	v1 := cloudResourceResourceModelV1{
		ID:               types.StringValue("cldrsrc_statusv1v2"),
		CloudID:          types.StringValue("cld_statusv1v2"),
		Name:             types.StringValue("status-v1-to-v2"),
		CloudProvider:    types.StringValue("AWS"),
		ComputeStack:     types.StringValue("VM"),
		Region:           types.StringValue("us-east-2"),
		IsPrivate:        types.BoolValue(false),
		AWSConfig:        awsConfig,
		GCPConfig:        types.ObjectNull(gcpConfigAttrTypes()),
		AzureConfig:      types.ObjectNull(azureConfigAttrTypes()),
		KubernetesConfig: types.ObjectNull(kubernetesConfigAttrTypes()),
		ObjectStorage:    types.ObjectNull(objectStorageAttrTypes()),
		FileStorage:      types.ObjectNull(fileStorageAttrTypes()),
		CloudResourceID:  types.StringValue("cldrsrc_statusv1v2"),
		Status:           types.StringValue("RUNNING"),
		OperatorStatus:   types.StringValue("RUNNING"),
		OperatorVersion:  types.StringValue("1.2.3"),
		ReportedAt:       types.StringValue("2026-07-22T00:00:00Z"),
		IsDefault:        types.BoolValue(false),
	}

	r := &CloudResourceResource{}
	upgraders := r.UpgradeState(ctx)
	upgrader, ok := upgraders[1]
	if !ok {
		t.Fatalf("UpgradeState() has no entry for schema version 1 - the v1->v2 upgrader dropping status is missing")
	}

	v1Schema := cloudResourceResourceSchemaV1()
	priorState := &tfsdk.State{
		Schema: *v1Schema,
		Raw:    tftypes.NewValue(v1Schema.Type().TerraformType(ctx), nil),
	}
	if diags := priorState.Set(ctx, &v1); diags.HasError() {
		t.Fatalf("failed to build v1 prior state fixture: %v", diags)
	}

	var v2SchemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &v2SchemaResp)
	if v2SchemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build v2 (current) schema: %v", v2SchemaResp.Diagnostics)
	}
	if v2SchemaResp.Schema.Version != 2 {
		t.Fatalf("current schema Version = %d, want 2 (bumped for the status removal)", v2SchemaResp.Schema.Version)
	}
	if _, present := v2SchemaResp.Schema.Attributes["status"]; present {
		t.Fatal("current schema still declares status - it should be fully removed")
	}

	req := resource.UpgradeStateRequest{State: priorState}
	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{
			Schema: v2SchemaResp.Schema,
			Raw:    tftypes.NewValue(v2SchemaResp.Schema.Type().TerraformType(ctx), nil),
		},
	}
	upgrader.StateUpgrader(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgradeCloudResourceResourceStateV1toV2() diagnostics: %v", resp.Diagnostics)
	}

	// THE load-bearing assertion: this must decode cleanly into the CURRENT
	// model (which has no Status field at all) with zero errors - if the
	// upgrader still tried to write a status value into the v2 state, this
	// Get would fail to decode against the v2 schema's Raw value, or
	// resp.Diagnostics above would already have caught a mismatch. A schema
	// that silently still accepted a status key (Version bumped but
	// attribute not actually dropped) would already have failed the
	// "present" check above; this proves the ACTUAL prior-state payload
	// survives the round trip, not just that the target schema looks right.
	var v2 CloudResourceResourceModel
	if diags := resp.State.Get(ctx, &v2); diags.HasError() {
		t.Fatalf("failed to decode upgraded v2 state: %v", diags)
	}

	if v2.Name.ValueString() != "status-v1-to-v2" {
		t.Errorf("Name = %v, want unchanged", v2.Name.ValueString())
	}
	if v2.OperatorStatus.ValueString() != "RUNNING" {
		t.Errorf("OperatorStatus = %v, want unchanged (\"RUNNING\") - status removal must not disturb its sibling", v2.OperatorStatus.ValueString())
	}
	if v2.CloudResourceID.ValueString() != "cldrsrc_statusv1v2" {
		t.Errorf("CloudResourceID = %v, want unchanged", v2.CloudResourceID.ValueString())
	}

	var awsModel AWSConfigModel
	if diags := v2.AWSConfig.As(ctx, &awsModel, basetypes.ObjectAsOptions{}); diags.HasError() {
		t.Fatalf("failed to decode upgraded AWSConfig: %v", diags)
	}
	if awsModel.VPCID.ValueString() != "vpc-statusv1v2" {
		t.Errorf("AWSConfig.VPCID = %v, want unchanged", awsModel.VPCID.ValueString())
	}
}
