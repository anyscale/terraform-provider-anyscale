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

// upgradeCloudResourceResourceFixture runs a v0 anyscale_cloud_resource state
// through the real UpgradeState() map and returns the decoded current model.
// It is the anyscale_cloud_resource counterpart of upgradeCloudFixture.
func upgradeCloudResourceResourceFixture(t *testing.T, v0Model cloudResourceResourceModelV1) CloudResourceResourceModel {
	t.Helper()
	ctx := context.Background()

	r := &CloudResourceResource{}
	upgrader, ok := r.UpgradeState(ctx)[0]
	if !ok {
		t.Fatalf("UpgradeState() has no entry for schema version 0")
	}

	priorState := &tfsdk.State{
		Schema: *upgrader.PriorSchema,
		Raw:    tftypes.NewValue(upgrader.PriorSchema.Type().TerraformType(ctx), nil),
	}
	if diags := priorState.Set(ctx, &v0Model); diags.HasError() {
		t.Fatalf("failed to build v0 prior state fixture: %v", diags)
	}

	var current resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &current)
	if current.Diagnostics.HasError() {
		t.Fatalf("failed to build current schema: %v", current.Diagnostics)
	}

	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{
			Schema: current.Schema,
			Raw:    tftypes.NewValue(current.Schema.Type().TerraformType(ctx), nil),
		},
	}
	upgrader.StateUpgrader(ctx, resource.UpgradeStateRequest{State: priorState}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgradeCloudResourceResourceStateV0toV1() diagnostics: %v", resp.Diagnostics)
	}

	var upgraded CloudResourceResourceModel
	if diags := resp.State.Get(ctx, &upgraded); diags.HasError() {
		t.Fatalf("failed to decode upgraded state: %v", diags)
	}
	return upgraded
}

func TestCloudResourceResourceStateUpgradeV0_FullK8SCloudResource(t *testing.T) {
	k8sConfigV0 := types.ObjectValueMust(kubernetesConfigAttrTypesV0(), map[string]attr.Value{
		"anyscale_operator_iam_identity": types.StringValue("arn:aws:iam::123456789012:role/operator"),
		"zones":                          types.ListValueMust(types.StringType, []attr.Value{types.StringValue("us-east-2a"), types.StringValue("us-east-2b")}),
		"redis_endpoint":                 types.StringValue("redis.ray-system.svc.cluster.local:6379"),
		"namespace":                      types.StringValue("custom-ns"),
		"ingress_host":                   types.StringValue("anyscale.example.com"),
		"cluster_name":                   types.StringValue("my-eks-cluster"),
		"context":                        types.StringValue("my-context"),
		"kubeconfig_path":                types.StringValue("/tmp/kubeconfig"),
	})

	got := upgradeCloudResourceResourceFixture(t, cloudResourceResourceModelV1{
		ID:               types.StringValue("cldrsrc_k8s_v0"),
		CloudID:          types.StringValue("cld_k8s_v0"),
		Name:             types.StringValue("k8s-v0"),
		CloudProvider:    types.StringValue("AWS"),
		ComputeStack:     types.StringValue("K8S"),
		Region:           types.StringValue("us-east-2"),
		IsPrivate:        types.BoolValue(false),
		AWSConfig:        types.ObjectNull(awsConfigAttrTypes()),
		GCPConfig:        types.ObjectNull(gcpConfigAttrTypes()),
		AzureConfig:      types.ObjectNull(azureConfigAttrTypes()),
		KubernetesConfig: k8sConfigV0,
		ObjectStorage:    types.ObjectNull(objectStorageAttrTypes()),
		FileStorage:      types.ObjectNull(fileStorageAttrTypes()),
		CloudResourceID:  types.StringValue("cldrsrc_k8s_v0"),
		Status:           types.StringValue("RUNNING"),
		OperatorStatus:   types.StringValue("RUNNING"),
		OperatorVersion:  types.StringValue("1.2.3"),
		ReportedAt:       types.StringValue("2026-07-22T00:00:00Z"),
		IsDefault:        types.BoolValue(false),
	})

	if got.KubernetesConfig.IsNull() {
		t.Fatal("KubernetesConfig is null, want the 3 surviving attributes carried through")
	}
	attrs := got.KubernetesConfig.Attributes()
	if len(attrs) != 3 {
		t.Errorf("KubernetesConfig has %d attributes, want exactly 3: %v", len(attrs), attrs)
	}
	for _, removed := range []string{"namespace", "ingress_host", "cluster_name", "context", "kubeconfig_path"} {
		if _, present := attrs[removed]; present {
			t.Errorf("KubernetesConfig still has %q; it must be dropped", removed)
		}
	}
	if v := attrs["anyscale_operator_iam_identity"].(types.String).ValueString(); v != "arn:aws:iam::123456789012:role/operator" {
		t.Errorf("anyscale_operator_iam_identity = %q, want unchanged", v)
	}
	if v := attrs["redis_endpoint"].(types.String).ValueString(); v != "redis.ray-system.svc.cluster.local:6379" {
		t.Errorf("redis_endpoint = %q, want unchanged", v)
	}
	if zones := attrs["zones"].(types.List).Elements(); len(zones) != 2 {
		t.Errorf("zones has %d elements, want 2", len(zones))
	}
	if got.OperatorStatus.ValueString() != "RUNNING" || got.CloudResourceID.ValueString() != "cldrsrc_k8s_v0" {
		t.Errorf("OperatorStatus/CloudResourceID = %q/%q, want unchanged", got.OperatorStatus.ValueString(), got.CloudResourceID.ValueString())
	}
	if !got.AWSConfig.IsNull() || !got.FileStorage.IsNull() {
		t.Error("AWSConfig/FileStorage must stay null for a K8S cloud resource that never had them")
	}
}

func TestCloudResourceResourceStateUpgradeV0_FullVMCloudResource(t *testing.T) {
	ctx := context.Background()

	awsConfig := types.ObjectValueMust(awsConfigAttrTypes(), map[string]attr.Value{
		"vpc_id":                      types.StringValue("vpc-v0"),
		"subnet_ids":                  types.ListNull(types.StringType),
		"subnet_ids_to_az":            types.MapValueMust(types.StringType, map[string]attr.Value{"subnet-1": types.StringValue("us-east-2a")}),
		"security_group_ids":          types.ListValueMust(types.StringType, []attr.Value{types.StringValue("sg-v0")}),
		"controlplane_iam_role_arn":   types.StringValue("arn:aws:iam::123456789012:role/control-v0"),
		"dataplane_iam_role_arn":      types.StringValue("arn:aws:iam::123456789012:role/data-v0"),
		"cluster_instance_profile_id": types.StringNull(),
		"external_id":                 types.StringValue("ext-id-v0"),
		"memorydb_cluster_name":       types.StringValue("memorydb-v0"),
		"memorydb_cluster_arn":        types.StringValue("arn:aws:memorydb:us-east-2:123456789012:cluster/memorydb-v0"),
		"memorydb_cluster_endpoint":   types.StringValue("memorydb-v0.abc.clustercfg.memorydb.us-east-2.amazonaws.com:6379"),
	})
	objectStorage := types.ObjectValueMust(objectStorageAttrTypes(), map[string]attr.Value{
		"bucket_name": types.StringValue("bucket-v0"),
		"region":      types.StringNull(),
		"endpoint":    types.StringNull(),
	})
	mountTarget := types.ObjectValueMust(mountTargetAttrTypes(), map[string]attr.Value{
		"address": types.StringValue("fs-v0.efs.us-east-2.amazonaws.com"),
		"zone":    types.StringValue("us-east-2a"),
	})
	fileStorage := types.ObjectValueMust(fileStorageAttrTypes(), map[string]attr.Value{
		"file_storage_id":             types.StringValue("fs-v0"),
		"mount_path":                  types.StringValue("/mnt/shared"),
		"persistent_volume_claim":     types.StringNull(),
		"csi_ephemeral_volume_driver": types.StringNull(),
		"mount_targets":               types.ListValueMust(types.ObjectType{AttrTypes: mountTargetAttrTypes()}, []attr.Value{mountTarget}),
	})

	got := upgradeCloudResourceResourceFixture(t, cloudResourceResourceModelV1{
		ID:               types.StringValue("cldrsrc_vm_v0"),
		CloudID:          types.StringValue("cld_vm_v0"),
		Name:             types.StringValue("vm-v0"),
		CloudProvider:    types.StringValue("AWS"),
		ComputeStack:     types.StringValue("VM"),
		Region:           types.StringValue("us-east-2"),
		IsPrivate:        types.BoolValue(true),
		AWSConfig:        awsConfig,
		GCPConfig:        types.ObjectNull(gcpConfigAttrTypes()),
		AzureConfig:      types.ObjectNull(azureConfigAttrTypes()),
		KubernetesConfig: types.ObjectNull(kubernetesConfigAttrTypesV0()),
		ObjectStorage:    objectStorage,
		FileStorage:      fileStorage,
		CloudResourceID:  types.StringValue("cldrsrc_vm_v0"),
		Status:           types.StringValue("RUNNING"),
		OperatorStatus:   types.StringValue("RUNNING"),
		OperatorVersion:  types.StringNull(),
		ReportedAt:       types.StringNull(),
		IsDefault:        types.BoolValue(true),
	})

	if !got.KubernetesConfig.IsNull() {
		t.Errorf("KubernetesConfig = %v, want null (VM cloud resource never had one)", got.KubernetesConfig)
	}

	var awsModel AWSConfigModel
	if diags := got.AWSConfig.As(ctx, &awsModel, basetypes.ObjectAsOptions{}); diags.HasError() {
		t.Fatalf("failed to decode upgraded AWSConfig: %v", diags)
	}
	if awsModel.VPCID.ValueString() != "vpc-v0" || awsModel.MemoryDBClusterARN.ValueString() != "arn:aws:memorydb:us-east-2:123456789012:cluster/memorydb-v0" {
		t.Errorf("AWSConfig = %+v, want vpc_id and memorydb_cluster_arn unchanged", awsModel)
	}

	var fsModel FileStorageModel
	if diags := got.FileStorage.As(ctx, &fsModel, basetypes.ObjectAsOptions{}); diags.HasError() {
		t.Fatalf("failed to decode upgraded FileStorage: %v", diags)
	}
	if fsModel.FileStorageID.ValueString() != "fs-v0" {
		t.Errorf("FileStorage.FileStorageID = %q, want unchanged", fsModel.FileStorageID.ValueString())
	}
	if fsModel.MountTargets.IsNull() || len(fsModel.MountTargets.Elements()) != 1 {
		t.Errorf("FileStorage.MountTargets = %v, want 1 element carried through", fsModel.MountTargets)
	}

	var osModel ObjectStorageModel
	if diags := got.ObjectStorage.As(ctx, &osModel, basetypes.ObjectAsOptions{}); diags.HasError() {
		t.Fatalf("failed to decode upgraded ObjectStorage: %v", diags)
	}
	if osModel.BucketName.ValueString() != "bucket-v0" {
		t.Errorf("ObjectStorage.BucketName = %q, want unchanged", osModel.BucketName.ValueString())
	}
	if got.IsDefault.ValueBool() != true || got.IsPrivate.ValueBool() != true {
		t.Errorf("IsDefault/IsPrivate = %v/%v, want true/true", got.IsDefault, got.IsPrivate)
	}
}
