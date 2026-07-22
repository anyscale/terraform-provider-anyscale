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

// Real v0->v1 upgrader test (anyscale_cloud), per architect's completeness
// request: a minimal k8s-only seed can't catch a PriorSchema gap in
// aws_config/file_storage, since those fields would just decode as zero
// values either way. Two realistic seeds instead - a full VM cloud and a
// full K8S cloud - so every field group in cloudResourceSchemaV0 is
// exercised through the real UpgradeState() path.

func kubernetesConfigAttrTypesV0() map[string]attr.Type {
	return map[string]attr.Type{
		"anyscale_operator_iam_identity": types.StringType,
		"zones":                          types.ListType{ElemType: types.StringType},
		"redis_endpoint":                 types.StringType,
		"namespace":                      types.StringType,
		"ingress_host":                   types.StringType,
		"cluster_name":                   types.StringType,
		"context":                        types.StringType,
		"kubeconfig_path":                types.StringType,
	}
}

// upgradeCloudFixture runs a v0 CloudResourceModel through the real
// UpgradeState() map and returns the decoded v1 model.
func upgradeCloudFixture(t *testing.T, v0Model CloudResourceModel) CloudResourceModel {
	t.Helper()
	ctx := context.Background()

	r := &CloudResource{}
	upgraders := r.UpgradeState(ctx)
	upgrader, ok := upgraders[0]
	if !ok {
		t.Fatalf("UpgradeState() has no entry for schema version 0")
	}

	v0Schema := cloudResourceSchemaV0()
	priorState := &tfsdk.State{
		Schema: *v0Schema,
		Raw:    tftypes.NewValue(v0Schema.Type().TerraformType(ctx), nil),
	}
	if diags := priorState.Set(ctx, &v0Model); diags.HasError() {
		t.Fatalf("failed to build v0 prior state fixture: %v", diags)
	}

	var v1SchemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &v1SchemaResp)
	if v1SchemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build v1 schema: %v", v1SchemaResp.Diagnostics)
	}

	req := resource.UpgradeStateRequest{State: priorState}
	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{
			Schema: v1SchemaResp.Schema,
			Raw:    tftypes.NewValue(v1SchemaResp.Schema.Type().TerraformType(ctx), nil),
		},
	}
	upgrader.StateUpgrader(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgradeCloudResourceStateV0toV1() diagnostics: %v", resp.Diagnostics)
	}

	var v1Model CloudResourceModel
	if diags := resp.State.Get(ctx, &v1Model); diags.HasError() {
		t.Fatalf("failed to decode upgraded v1 state: %v", diags)
	}
	return v1Model
}

func TestCloudResourceStateUpgradeV0toV1_FullVMCloud(t *testing.T) {
	ctx := context.Background()

	awsConfig := types.ObjectValueMust(awsConfigAttrTypes(), map[string]attr.Value{
		"vpc_id":                      types.StringValue("vpc-real"),
		"subnet_ids":                  types.ListNull(types.StringType),
		"subnet_ids_to_az":            types.MapValueMust(types.StringType, map[string]attr.Value{"subnet-1": types.StringValue("us-east-2a")}),
		"security_group_ids":          types.ListValueMust(types.StringType, []attr.Value{types.StringValue("sg-real")}),
		"controlplane_iam_role_arn":   types.StringValue("arn:aws:iam::123456789012:role/control"),
		"dataplane_iam_role_arn":      types.StringValue("arn:aws:iam::123456789012:role/data"),
		"cluster_instance_profile_id": types.StringNull(),
		"external_id":                 types.StringValue("ext-id-real"),
		"memorydb_cluster_name":       types.StringValue("memorydb-real"),
		"memorydb_cluster_arn":        types.StringValue("arn:aws:memorydb:us-east-2:123456789012:cluster/memorydb-real"),
		"memorydb_cluster_endpoint":   types.StringValue("memorydb-real.abc.clustercfg.memorydb.us-east-2.amazonaws.com:6379"),
	})
	objectStorage := types.ObjectValueMust(objectStorageAttrTypes(), map[string]attr.Value{
		"bucket_name": types.StringValue("my-real-bucket"),
		"region":      types.StringNull(),
		"endpoint":    types.StringNull(),
	})
	mountTarget := types.ObjectValueMust(mountTargetAttrTypes(), map[string]attr.Value{
		"address": types.StringValue("fs-real.efs.us-east-2.amazonaws.com"),
		"zone":    types.StringValue("us-east-2a"),
	})
	fileStorage := types.ObjectValueMust(fileStorageAttrTypes(), map[string]attr.Value{
		"file_storage_id":             types.StringValue("fs-real"),
		"mount_path":                  types.StringValue("/mnt/shared"),
		"persistent_volume_claim":     types.StringNull(),
		"csi_ephemeral_volume_driver": types.StringNull(),
		"mount_targets":               types.ListValueMust(types.ObjectType{AttrTypes: mountTargetAttrTypes()}, []attr.Value{mountTarget}),
	})

	v0 := CloudResourceModel{
		ID:                    types.StringValue("cld_real"),
		Name:                  types.StringValue("real-vm-cloud"),
		CloudProvider:         types.StringValue("AWS"),
		ComputeStack:          types.StringValue("VM"),
		Region:                types.StringValue("us-east-2"),
		IsPrivateCloud:        types.BoolValue(true),
		AutoAddUser:           types.BoolValue(false),
		Credentials:           types.StringValue("cred-placeholder"),
		EnableLineageTracking: types.BoolValue(false),
		EnableLogIngestion:    types.BoolValue(false),
		EnableSystemCluster:   types.BoolNull(),
		AWSConfig:             awsConfig,
		GCPConfig:             types.ObjectNull(gcpConfigAttrTypes()),
		AzureConfig:           types.ObjectNull(azureConfigAttrTypes()),
		KubernetesConfig:      types.ObjectNull(kubernetesConfigAttrTypesV0()),
		ObjectStorage:         objectStorage,
		FileStorage:           fileStorage,
		IsEmptyCloud:          types.BoolValue(false),
		IsDefault:             types.BoolValue(false),
		CloudResourceID:       types.StringValue("cldrsrc_real"),
	}

	v1 := upgradeCloudFixture(t, v0)

	if v1.KubernetesConfig.IsNull() != true {
		t.Errorf("KubernetesConfig = %v, want null (VM cloud never had one)", v1.KubernetesConfig)
	}

	var awsModel AWSConfigModel
	if diags := v1.AWSConfig.As(ctx, &awsModel, basetypes.ObjectAsOptions{}); diags.HasError() {
		t.Fatalf("failed to decode upgraded AWSConfig: %v", diags)
	}
	if awsModel.MemoryDBClusterARN.ValueString() != "arn:aws:memorydb:us-east-2:123456789012:cluster/memorydb-real" {
		t.Errorf("AWSConfig.MemoryDBClusterARN = %v, want unchanged", awsModel.MemoryDBClusterARN.ValueString())
	}
	if awsModel.MemoryDBClusterEndpoint.ValueString() != "memorydb-real.abc.clustercfg.memorydb.us-east-2.amazonaws.com:6379" {
		t.Errorf("AWSConfig.MemoryDBClusterEndpoint = %v, want unchanged", awsModel.MemoryDBClusterEndpoint.ValueString())
	}
	if awsModel.VPCID.ValueString() != "vpc-real" {
		t.Errorf("AWSConfig.VPCID = %v, want unchanged", awsModel.VPCID.ValueString())
	}

	var fsModel FileStorageModel
	if diags := v1.FileStorage.As(ctx, &fsModel, basetypes.ObjectAsOptions{}); diags.HasError() {
		t.Fatalf("failed to decode upgraded FileStorage: %v", diags)
	}
	if fsModel.MountTargets.IsNull() || len(fsModel.MountTargets.Elements()) != 1 {
		t.Errorf("FileStorage.MountTargets = %v, want 1 element carried through unchanged", fsModel.MountTargets)
	}
	if fsModel.FileStorageID.ValueString() != "fs-real" {
		t.Errorf("FileStorage.FileStorageID = %v, want unchanged", fsModel.FileStorageID.ValueString())
	}

	var osModel ObjectStorageModel
	if diags := v1.ObjectStorage.As(ctx, &osModel, basetypes.ObjectAsOptions{}); diags.HasError() {
		t.Fatalf("failed to decode upgraded ObjectStorage: %v", diags)
	}
	if osModel.BucketName.ValueString() != "my-real-bucket" {
		t.Errorf("ObjectStorage.BucketName = %v, want unchanged", osModel.BucketName.ValueString())
	}
}

func TestCloudResourceStateUpgradeV0toV1_FullK8SCloud(t *testing.T) {
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

	v0 := CloudResourceModel{
		ID:                    types.StringValue("cld_k8s_real"),
		Name:                  types.StringValue("real-k8s-cloud"),
		CloudProvider:         types.StringValue("AWS"),
		ComputeStack:          types.StringValue("K8S"),
		Region:                types.StringValue("us-east-2"),
		IsPrivateCloud:        types.BoolValue(false),
		AutoAddUser:           types.BoolValue(false),
		Credentials:           types.StringValue("cred-placeholder"),
		EnableLineageTracking: types.BoolValue(false),
		EnableLogIngestion:    types.BoolValue(false),
		EnableSystemCluster:   types.BoolNull(),
		AWSConfig:             types.ObjectNull(awsConfigAttrTypes()),
		GCPConfig:             types.ObjectNull(gcpConfigAttrTypes()),
		AzureConfig:           types.ObjectNull(azureConfigAttrTypes()),
		KubernetesConfig:      k8sConfigV0,
		ObjectStorage:         types.ObjectNull(objectStorageAttrTypes()),
		FileStorage:           types.ObjectNull(fileStorageAttrTypes()),
		IsEmptyCloud:          types.BoolValue(false),
		IsDefault:             types.BoolValue(false),
		CloudResourceID:       types.StringValue("cldrsrc_k8s_real"),
	}

	v1 := upgradeCloudFixture(t, v0)

	if v1.KubernetesConfig.IsNull() {
		t.Fatal("KubernetesConfig is null, want the 3 surviving attrs carried through")
	}
	attrs := v1.KubernetesConfig.Attributes()
	if len(attrs) != 3 {
		t.Errorf("KubernetesConfig has %d attributes, want exactly 3 (the 5 removed fields must be gone, not just null): %v", len(attrs), attrs)
	}
	for _, removed := range []string{"namespace", "ingress_host", "cluster_name", "context", "kubeconfig_path"} {
		if _, present := attrs[removed]; present {
			t.Errorf("KubernetesConfig still has %q - it must be dropped, not carried through", removed)
		}
	}
	if got := attrs["anyscale_operator_iam_identity"].(types.String).ValueString(); got != "arn:aws:iam::123456789012:role/operator" {
		t.Errorf("anyscale_operator_iam_identity = %v, want unchanged", got)
	}
	if got := attrs["redis_endpoint"].(types.String).ValueString(); got != "redis.ray-system.svc.cluster.local:6379" {
		t.Errorf("redis_endpoint = %v, want unchanged", got)
	}
	zones := attrs["zones"].(types.List).Elements()
	if len(zones) != 2 {
		t.Errorf("zones has %d elements, want 2 (unchanged)", len(zones))
	}

	if !v1.AWSConfig.IsNull() || !v1.GCPConfig.IsNull() {
		t.Error("AWSConfig/GCPConfig must stay null for a K8S cloud that never had them")
	}
}
