package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Characterization fixtures for TestBuildProviderConfig_PinsRequestJSON. Every
// optional field is set so the pinned JSON covers each field expand* copies.
// The minimal fixtures (testAWSConfigObj, testObjectStorageObj, ...) live in
// cloud_helpers_test.go and are reused where a minimal value is enough.

func pinStrList(vals ...string) types.List {
	elems := make([]attr.Value, len(vals))
	for i, v := range vals {
		elems[i] = types.StringValue(v)
	}
	return types.ListValueMust(types.StringType, elems)
}

// pinFullAWSConfigObj sets subnet_ids_to_az to a single entry: expandAWSConfig
// ranges over the map, so with more than one entry the order of subnet_ids and
// zones in the request is nondeterministic and cannot be pinned.
func pinFullAWSConfigObj() types.Object {
	return types.ObjectValueMust(awsConfigAttrTypes(), map[string]attr.Value{
		"vpc_id":                      types.StringValue("vpc-full"),
		"subnet_ids":                  pinStrList("subnet-ignored-when-map-set"),
		"subnet_ids_to_az":            types.MapValueMust(types.StringType, map[string]attr.Value{"subnet-aaa": types.StringValue("us-west-2a")}),
		"security_group_ids":          pinStrList("sg-1", "sg-2"),
		"controlplane_iam_role_arn":   types.StringValue("arn:aws:iam::123456789012:role/cp"),
		"dataplane_iam_role_arn":      types.StringValue("arn:aws:iam::123456789012:role/dp"),
		"cluster_instance_profile_id": types.StringValue("arn:aws:iam::123456789012:instance-profile/ip"),
		"external_id":                 types.StringValue("ext-123"),
		"memorydb_cluster_name":       types.StringValue("mdb-name"),
		"memorydb_cluster_arn":        types.StringValue("arn:aws:memorydb:us-west-2:123456789012:cluster/mdb"),
		"memorydb_cluster_endpoint":   types.StringValue("mdb.example:6379"),
	})
}

func pinFullGCPConfigObj() types.Object {
	return types.ObjectValueMust(gcpConfigAttrTypes(), map[string]attr.Value{
		"project_id":                         types.StringValue("proj-full"),
		"host_project_id":                    types.StringValue("host-proj"),
		"provider_name":                      types.StringValue("projects/1/locations/global/workloadIdentityPools/p/providers/q"),
		"vpc_name":                           types.StringValue("vpc-gcp"),
		"subnet_names":                       pinStrList("subnet-a", "subnet-b"),
		"controlplane_service_account_email": types.StringValue("cp@proj.iam.gserviceaccount.com"),
		"dataplane_service_account_email":    types.StringValue("dp@proj.iam.gserviceaccount.com"),
		"firewall_policy_names":              pinStrList("fw-1", "fw-2"),
		"memorystore_instance_name":          types.StringValue("projects/proj/locations/us-central1/instances/ms"),
		"memorystore_endpoint":               types.StringValue("10.0.0.5:6379"),
	})
}

func pinFullKubernetesConfigObj() types.Object {
	return types.ObjectValueMust(kubernetesConfigAttrTypes(), map[string]attr.Value{
		"anyscale_operator_iam_identity": types.StringValue("operator-identity"),
		"zones":                          pinStrList("zone-b", "zone-a"),
		"redis_endpoint":                 types.StringValue("redis.example:6379"),
	})
}

func pinFullObjectStorageObj(bucket string) types.Object {
	return types.ObjectValueMust(objectStorageAttrTypes(), map[string]attr.Value{
		"bucket_name": types.StringValue(bucket),
		"region":      types.StringValue("us-west-2"),
		"endpoint":    types.StringValue("https://storage.example"),
	})
}

func pinFullFileStorageObj() types.Object {
	mtType := types.ObjectType{AttrTypes: mountTargetAttrTypes()}
	return types.ObjectValueMust(fileStorageAttrTypes(), map[string]attr.Value{
		"file_storage_id":             types.StringValue("fs-full"),
		"mount_path":                  types.StringValue("/mnt/shared"),
		"persistent_volume_claim":     types.StringValue("pvc-1"),
		"csi_ephemeral_volume_driver": types.StringValue("csi.example"),
		"mount_targets": types.ListValueMust(mtType, []attr.Value{
			types.ObjectValueMust(mountTargetAttrTypes(), map[string]attr.Value{
				"address": types.StringValue("10.0.1.1"),
				"zone":    types.StringValue("us-west-2a"),
			}),
			types.ObjectValueMust(mountTargetAttrTypes(), map[string]attr.Value{
				"address": types.StringValue("10.0.1.2"),
				"zone":    types.StringNull(),
			}),
		}),
	})
}

// TestBuildProviderConfig_PinsRequestJSON pins buildProviderConfig's exact
// observable output - the returned error string and the marshaled request,
// including any fields written before an error return - so a refactor of the
// function can be shown to be behavior-preserving. Expected values were
// captured from the implementation; they describe current behavior, not a
// specification.
func TestBuildProviderConfig_PinsRequestJSON(t *testing.T) {
	ctx := context.Background()
	n := nullObj()

	const azureTenant = "11111111-1111-1111-1111-111111111111"
	const abfss = "abfss://container@account.dfs.core.windows.net"

	type tc struct {
		name                                string
		provider, stack                     string
		aws, gcp, azure, k8s, objSt, fileSt types.Object
		wantErr                             string // "" means nil
		wantJSON                            string
	}

	cases := []tc{
		// ---- AWS K8S ----
		{name: "AWS K8S full", provider: "AWS", stack: "K8S", aws: pinFullAWSConfigObj(), gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj("aws-bucket"), fileSt: pinFullFileStorageObj(), wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"s3://aws-bucket","region":"us-west-2","endpoint":"https://storage.example"},"file_storage":{"file_storage_id":"fs-full","mount_path":"/mnt/shared","mount_targets":[{"address":"10.0.1.1","zone":"us-west-2a"},{"address":"10.0.1.2"}],"persistent_volume_claim":"pvc-1","csi_ephemeral_volume_driver":"csi.example"},"aws_config":{"vpc_id":"vpc-full","subnet_ids":["subnet-aaa"],"zones":["us-west-2a"],"security_group_ids":["sg-1","sg-2"],"anyscale_iam_role_id":"arn:aws:iam::123456789012:role/cp","external_id":"ext-123","cluster_iam_role_id":"arn:aws:iam::123456789012:role/dp","cluster_instance_profile_id":"arn:aws:iam::123456789012:instance-profile/ip","memorydb_cluster_name":"mdb-name","memorydb_cluster_arn":"arn:aws:memorydb:us-west-2:123456789012:cluster/mdb","memorydb_cluster_endpoint":"mdb.example:6379"},"kubernetes_config":{"anyscale_operator_iam_identity":"operator-identity","zones":["zone-b","zone-a"],"redis_endpoint":"redis.example:6379"}}`},
		{name: "AWS K8S aws_config unset", provider: "AWS", stack: "K8S", aws: n, gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj("aws-bucket"), fileSt: pinFullFileStorageObj(), wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"s3://aws-bucket","region":"us-west-2","endpoint":"https://storage.example"},"file_storage":{"file_storage_id":"fs-full","mount_path":"/mnt/shared","mount_targets":[{"address":"10.0.1.1","zone":"us-west-2a"},{"address":"10.0.1.2"}],"persistent_volume_claim":"pvc-1","csi_ephemeral_volume_driver":"csi.example"},"kubernetes_config":{"anyscale_operator_iam_identity":"operator-identity","zones":["zone-b","zone-a"],"redis_endpoint":"redis.example:6379"}}`},
		{name: "AWS K8S file_storage unset", provider: "AWS", stack: "K8S", aws: pinFullAWSConfigObj(), gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj("aws-bucket"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"s3://aws-bucket","region":"us-west-2","endpoint":"https://storage.example"},"aws_config":{"vpc_id":"vpc-full","subnet_ids":["subnet-aaa"],"zones":["us-west-2a"],"security_group_ids":["sg-1","sg-2"],"anyscale_iam_role_id":"arn:aws:iam::123456789012:role/cp","external_id":"ext-123","cluster_iam_role_id":"arn:aws:iam::123456789012:role/dp","cluster_instance_profile_id":"arn:aws:iam::123456789012:instance-profile/ip","memorydb_cluster_name":"mdb-name","memorydb_cluster_arn":"arn:aws:memorydb:us-west-2:123456789012:cluster/mdb","memorydb_cluster_endpoint":"mdb.example:6379"},"kubernetes_config":{"anyscale_operator_iam_identity":"operator-identity","zones":["zone-b","zone-a"],"redis_endpoint":"redis.example:6379"}}`},
		{name: "AWS K8S kubernetes_config and object_storage both null", provider: "AWS", stack: "K8S", aws: n, gcp: n, azure: n, k8s: n, objSt: n, fileSt: n, wantErr: "kubernetes_config is required when cloud_provider is AWS and compute_stack is K8S", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "AWS K8S missing operator identity", provider: "AWS", stack: "K8S", aws: n, gcp: n, azure: n, k8s: testKubernetesConfigObjMissingIdentity(), objSt: testObjectStorageObj("b"), fileSt: n, wantErr: "kubernetes_config.anyscale_operator_iam_identity is required for AWS K8S clouds", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "AWS K8S missing object_storage", provider: "AWS", stack: "K8S", aws: pinFullAWSConfigObj(), gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: n, fileSt: pinFullFileStorageObj(), wantErr: "object_storage is required when cloud_provider is AWS and compute_stack is K8S", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","kubernetes_config":{"anyscale_operator_iam_identity":"operator-identity","zones":["zone-b","zone-a"],"redis_endpoint":"redis.example:6379"}}`},

		// ---- AWS VM ----
		{name: "AWS VM full", provider: "AWS", stack: "VM", aws: pinFullAWSConfigObj(), gcp: n, azure: n, k8s: n, objSt: pinFullObjectStorageObj("aws-bucket"), fileSt: pinFullFileStorageObj(), wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"s3://aws-bucket","region":"us-west-2","endpoint":"https://storage.example"},"file_storage":{"file_storage_id":"fs-full","mount_path":"/mnt/shared","mount_targets":[{"address":"10.0.1.1","zone":"us-west-2a"},{"address":"10.0.1.2"}],"persistent_volume_claim":"pvc-1","csi_ephemeral_volume_driver":"csi.example"},"aws_config":{"vpc_id":"vpc-full","subnet_ids":["subnet-aaa"],"zones":["us-west-2a"],"security_group_ids":["sg-1","sg-2"],"anyscale_iam_role_id":"arn:aws:iam::123456789012:role/cp","external_id":"ext-123","cluster_iam_role_id":"arn:aws:iam::123456789012:role/dp","cluster_instance_profile_id":"arn:aws:iam::123456789012:instance-profile/ip","memorydb_cluster_name":"mdb-name","memorydb_cluster_arn":"arn:aws:memorydb:us-west-2:123456789012:cluster/mdb","memorydb_cluster_endpoint":"mdb.example:6379"}}`},
		{name: "AWS VM object_storage unset", provider: "AWS", stack: "VM", aws: pinFullAWSConfigObj(), gcp: n, azure: n, k8s: n, objSt: n, fileSt: pinFullFileStorageObj(), wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","file_storage":{"file_storage_id":"fs-full","mount_path":"/mnt/shared","mount_targets":[{"address":"10.0.1.1","zone":"us-west-2a"},{"address":"10.0.1.2"}],"persistent_volume_claim":"pvc-1","csi_ephemeral_volume_driver":"csi.example"},"aws_config":{"vpc_id":"vpc-full","subnet_ids":["subnet-aaa"],"zones":["us-west-2a"],"security_group_ids":["sg-1","sg-2"],"anyscale_iam_role_id":"arn:aws:iam::123456789012:role/cp","external_id":"ext-123","cluster_iam_role_id":"arn:aws:iam::123456789012:role/dp","cluster_instance_profile_id":"arn:aws:iam::123456789012:instance-profile/ip","memorydb_cluster_name":"mdb-name","memorydb_cluster_arn":"arn:aws:memorydb:us-west-2:123456789012:cluster/mdb","memorydb_cluster_endpoint":"mdb.example:6379"}}`},
		{name: "AWS VM file_storage unset", provider: "AWS", stack: "VM", aws: pinFullAWSConfigObj(), gcp: n, azure: n, k8s: n, objSt: pinFullObjectStorageObj("aws-bucket"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"s3://aws-bucket","region":"us-west-2","endpoint":"https://storage.example"},"aws_config":{"vpc_id":"vpc-full","subnet_ids":["subnet-aaa"],"zones":["us-west-2a"],"security_group_ids":["sg-1","sg-2"],"anyscale_iam_role_id":"arn:aws:iam::123456789012:role/cp","external_id":"ext-123","cluster_iam_role_id":"arn:aws:iam::123456789012:role/dp","cluster_instance_profile_id":"arn:aws:iam::123456789012:instance-profile/ip","memorydb_cluster_name":"mdb-name","memorydb_cluster_arn":"arn:aws:memorydb:us-west-2:123456789012:cluster/mdb","memorydb_cluster_endpoint":"mdb.example:6379"}}`},
		{name: "AWS VM minimal aws_config subnet_ids list", provider: "AWS", stack: "VM", aws: testAWSConfigObj("vpc-min"), gcp: n, azure: n, k8s: n, objSt: n, fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","aws_config":{"vpc_id":"vpc-min","subnet_ids":["subnet-111"],"security_group_ids":["sg-111"],"anyscale_iam_role_id":"arn:aws:iam::123456789012:role/cp","cluster_iam_role_id":"arn:aws:iam::123456789012:role/dp"}}`},
		{name: "AWS VM missing aws_config", provider: "AWS", stack: "VM", aws: n, gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj("aws-bucket"), fileSt: pinFullFileStorageObj(), wantErr: "aws_config is required when cloud_provider is AWS and compute_stack is VM", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "AWS VM ignores kubernetes_config and gcp_config", provider: "AWS", stack: "VM", aws: testAWSConfigObj("vpc-1"), gcp: pinFullGCPConfigObj(), azure: testAzureConfigObj(azureTenant), k8s: pinFullKubernetesConfigObj(), objSt: n, fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","aws_config":{"vpc_id":"vpc-1","subnet_ids":["subnet-111"],"security_group_ids":["sg-111"],"anyscale_iam_role_id":"arn:aws:iam::123456789012:role/cp","cluster_iam_role_id":"arn:aws:iam::123456789012:role/dp"}}`},

		// ---- GCP K8S ----
		{name: "GCP K8S full", provider: "GCP", stack: "K8S", aws: n, gcp: pinFullGCPConfigObj(), azure: n, k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj("gcp-bucket"), fileSt: pinFullFileStorageObj(), wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"gs://gcp-bucket","region":"us-west-2","endpoint":"https://storage.example"},"file_storage":{"file_storage_id":"fs-full","mount_path":"/mnt/shared","mount_targets":[{"address":"10.0.1.1","zone":"us-west-2a"},{"address":"10.0.1.2"}],"persistent_volume_claim":"pvc-1","csi_ephemeral_volume_driver":"csi.example"},"gcp_config":{"project_id":"proj-full","host_project_id":"host-proj","provider_name":"projects/1/locations/global/workloadIdentityPools/p/providers/q","vpc_name":"vpc-gcp","subnet_names":["subnet-a","subnet-b"],"anyscale_service_account_email":"cp@proj.iam.gserviceaccount.com","cluster_service_account_email":"dp@proj.iam.gserviceaccount.com","firewall_policy_names":["fw-1","fw-2"],"memorystore_instance_name":"projects/proj/locations/us-central1/instances/ms","memorystore_endpoint":"10.0.0.5:6379"},"kubernetes_config":{"anyscale_operator_iam_identity":"operator-identity","zones":["zone-b","zone-a"],"redis_endpoint":"redis.example:6379"}}`},
		{name: "GCP K8S gcp_config unset", provider: "GCP", stack: "K8S", aws: n, gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj("gcp-bucket"), fileSt: pinFullFileStorageObj(), wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"gs://gcp-bucket","region":"us-west-2","endpoint":"https://storage.example"},"file_storage":{"file_storage_id":"fs-full","mount_path":"/mnt/shared","mount_targets":[{"address":"10.0.1.1","zone":"us-west-2a"},{"address":"10.0.1.2"}],"persistent_volume_claim":"pvc-1","csi_ephemeral_volume_driver":"csi.example"},"kubernetes_config":{"anyscale_operator_iam_identity":"operator-identity","zones":["zone-b","zone-a"],"redis_endpoint":"redis.example:6379"}}`},
		{name: "GCP K8S file_storage unset", provider: "GCP", stack: "K8S", aws: n, gcp: pinFullGCPConfigObj(), azure: n, k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj("gcp-bucket"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"gs://gcp-bucket","region":"us-west-2","endpoint":"https://storage.example"},"gcp_config":{"project_id":"proj-full","host_project_id":"host-proj","provider_name":"projects/1/locations/global/workloadIdentityPools/p/providers/q","vpc_name":"vpc-gcp","subnet_names":["subnet-a","subnet-b"],"anyscale_service_account_email":"cp@proj.iam.gserviceaccount.com","cluster_service_account_email":"dp@proj.iam.gserviceaccount.com","firewall_policy_names":["fw-1","fw-2"],"memorystore_instance_name":"projects/proj/locations/us-central1/instances/ms","memorystore_endpoint":"10.0.0.5:6379"},"kubernetes_config":{"anyscale_operator_iam_identity":"operator-identity","zones":["zone-b","zone-a"],"redis_endpoint":"redis.example:6379"}}`},
		{name: "GCP K8S kubernetes_config and object_storage both null", provider: "GCP", stack: "K8S", aws: n, gcp: n, azure: n, k8s: n, objSt: n, fileSt: n, wantErr: "kubernetes_config is required when cloud_provider is GCP and compute_stack is K8S", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "GCP K8S missing operator identity", provider: "GCP", stack: "K8S", aws: n, gcp: n, azure: n, k8s: testKubernetesConfigObjMissingIdentity(), objSt: testObjectStorageObj("b"), fileSt: n, wantErr: "kubernetes_config.anyscale_operator_iam_identity is required for GCP K8S clouds", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "GCP K8S missing object_storage", provider: "GCP", stack: "K8S", aws: n, gcp: pinFullGCPConfigObj(), azure: n, k8s: pinFullKubernetesConfigObj(), objSt: n, fileSt: n, wantErr: "object_storage is required when cloud_provider is GCP and compute_stack is K8S", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","kubernetes_config":{"anyscale_operator_iam_identity":"operator-identity","zones":["zone-b","zone-a"],"redis_endpoint":"redis.example:6379"}}`},

		// ---- GCP VM ----
		{name: "GCP VM full", provider: "GCP", stack: "VM", aws: n, gcp: pinFullGCPConfigObj(), azure: n, k8s: n, objSt: pinFullObjectStorageObj("gcp-bucket"), fileSt: pinFullFileStorageObj(), wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"gs://gcp-bucket","region":"us-west-2","endpoint":"https://storage.example"},"file_storage":{"file_storage_id":"fs-full","mount_path":"/mnt/shared","mount_targets":[{"address":"10.0.1.1","zone":"us-west-2a"},{"address":"10.0.1.2"}],"persistent_volume_claim":"pvc-1","csi_ephemeral_volume_driver":"csi.example"},"gcp_config":{"project_id":"proj-full","host_project_id":"host-proj","provider_name":"projects/1/locations/global/workloadIdentityPools/p/providers/q","vpc_name":"vpc-gcp","subnet_names":["subnet-a","subnet-b"],"anyscale_service_account_email":"cp@proj.iam.gserviceaccount.com","cluster_service_account_email":"dp@proj.iam.gserviceaccount.com","firewall_policy_names":["fw-1","fw-2"],"memorystore_instance_name":"projects/proj/locations/us-central1/instances/ms","memorystore_endpoint":"10.0.0.5:6379"}}`},
		{name: "GCP VM object_storage unset", provider: "GCP", stack: "VM", aws: n, gcp: pinFullGCPConfigObj(), azure: n, k8s: n, objSt: n, fileSt: pinFullFileStorageObj(), wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","file_storage":{"file_storage_id":"fs-full","mount_path":"/mnt/shared","mount_targets":[{"address":"10.0.1.1","zone":"us-west-2a"},{"address":"10.0.1.2"}],"persistent_volume_claim":"pvc-1","csi_ephemeral_volume_driver":"csi.example"},"gcp_config":{"project_id":"proj-full","host_project_id":"host-proj","provider_name":"projects/1/locations/global/workloadIdentityPools/p/providers/q","vpc_name":"vpc-gcp","subnet_names":["subnet-a","subnet-b"],"anyscale_service_account_email":"cp@proj.iam.gserviceaccount.com","cluster_service_account_email":"dp@proj.iam.gserviceaccount.com","firewall_policy_names":["fw-1","fw-2"],"memorystore_instance_name":"projects/proj/locations/us-central1/instances/ms","memorystore_endpoint":"10.0.0.5:6379"}}`},
		{name: "GCP VM file_storage unset", provider: "GCP", stack: "VM", aws: n, gcp: pinFullGCPConfigObj(), azure: n, k8s: n, objSt: pinFullObjectStorageObj("gcp-bucket"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"gs://gcp-bucket","region":"us-west-2","endpoint":"https://storage.example"},"gcp_config":{"project_id":"proj-full","host_project_id":"host-proj","provider_name":"projects/1/locations/global/workloadIdentityPools/p/providers/q","vpc_name":"vpc-gcp","subnet_names":["subnet-a","subnet-b"],"anyscale_service_account_email":"cp@proj.iam.gserviceaccount.com","cluster_service_account_email":"dp@proj.iam.gserviceaccount.com","firewall_policy_names":["fw-1","fw-2"],"memorystore_instance_name":"projects/proj/locations/us-central1/instances/ms","memorystore_endpoint":"10.0.0.5:6379"}}`},
		{name: "GCP VM missing gcp_config", provider: "GCP", stack: "VM", aws: n, gcp: n, azure: n, k8s: n, objSt: pinFullObjectStorageObj("gcp-bucket"), fileSt: n, wantErr: "gcp_config is required when cloud_provider is GCP and compute_stack is VM", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},

		// ---- AZURE ----
		{name: "AZURE K8S full", provider: "AZURE", stack: "K8S", aws: n, gcp: n, azure: testAzureConfigObj(azureTenant), k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj(abfss), fileSt: pinFullFileStorageObj(), wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"abfss://container@account.dfs.core.windows.net","region":"us-west-2","endpoint":"https://storage.example"},"file_storage":{"file_storage_id":"fs-full","mount_path":"/mnt/shared","mount_targets":[{"address":"10.0.1.1","zone":"us-west-2a"},{"address":"10.0.1.2"}],"persistent_volume_claim":"pvc-1","csi_ephemeral_volume_driver":"csi.example"},"azure_config":{"tenant_id":"11111111-1111-1111-1111-111111111111"},"kubernetes_config":{"anyscale_operator_iam_identity":"operator-identity","zones":["zone-b","zone-a"],"redis_endpoint":"redis.example:6379"}}`},
		{name: "AZURE K8S azure_config unset", provider: "AZURE", stack: "K8S", aws: n, gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj(abfss), fileSt: pinFullFileStorageObj(), wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"abfss://container@account.dfs.core.windows.net","region":"us-west-2","endpoint":"https://storage.example"},"file_storage":{"file_storage_id":"fs-full","mount_path":"/mnt/shared","mount_targets":[{"address":"10.0.1.1","zone":"us-west-2a"},{"address":"10.0.1.2"}],"persistent_volume_claim":"pvc-1","csi_ephemeral_volume_driver":"csi.example"},"kubernetes_config":{"anyscale_operator_iam_identity":"operator-identity","zones":["zone-b","zone-a"],"redis_endpoint":"redis.example:6379"}}`},
		{name: "AZURE K8S file_storage unset", provider: "AZURE", stack: "K8S", aws: n, gcp: n, azure: testAzureConfigObj(azureTenant), k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj(abfss), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"abfss://container@account.dfs.core.windows.net","region":"us-west-2","endpoint":"https://storage.example"},"azure_config":{"tenant_id":"11111111-1111-1111-1111-111111111111"},"kubernetes_config":{"anyscale_operator_iam_identity":"operator-identity","zones":["zone-b","zone-a"],"redis_endpoint":"redis.example:6379"}}`},
		{name: "AZURE K8S object_storage unset", provider: "AZURE", stack: "K8S", aws: n, gcp: n, azure: testAzureConfigObj(azureTenant), k8s: pinFullKubernetesConfigObj(), objSt: n, fileSt: pinFullFileStorageObj(), wantErr: "object_storage is required when cloud_provider is AZURE", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","kubernetes_config":{"anyscale_operator_iam_identity":"operator-identity","zones":["zone-b","zone-a"],"redis_endpoint":"redis.example:6379"}}`},
		{name: "AZURE K8S kubernetes_config unset", provider: "AZURE", stack: "K8S", aws: n, gcp: n, azure: testAzureConfigObj(azureTenant), k8s: n, objSt: pinFullObjectStorageObj(abfss), fileSt: n, wantErr: "kubernetes_config is required when cloud_provider is AZURE", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "AZURE K8S missing operator identity", provider: "AZURE", stack: "K8S", aws: n, gcp: n, azure: n, k8s: testKubernetesConfigObjMissingIdentity(), objSt: pinFullObjectStorageObj(abfss), fileSt: n, wantErr: "kubernetes_config.anyscale_operator_iam_identity is required for AZURE K8S clouds", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "AZURE VM guard", provider: "AZURE", stack: "VM", aws: n, gcp: n, azure: testAzureConfigObj(azureTenant), k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj(abfss), fileSt: n, wantErr: "azure clouds only support compute_stack = \"K8S\" - Anyscale does not support Azure VM clouds", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "AZURE empty stack guard", provider: "AZURE", stack: "", aws: n, gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj(abfss), fileSt: n, wantErr: "azure clouds only support compute_stack = \"K8S\" - Anyscale does not support Azure VM clouds", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "AZURE lowercase k8s stack guard", provider: "azure", stack: "k8s", aws: n, gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj(abfss), fileSt: n, wantErr: "azure clouds only support compute_stack = \"K8S\" - Anyscale does not support Azure VM clouds", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},

		// ---- Other providers ----
		{name: "unrecognized provider OCI", provider: "OCI", stack: "K8S", aws: pinFullAWSConfigObj(), gcp: pinFullGCPConfigObj(), azure: n, k8s: pinFullKubernetesConfigObj(), objSt: pinFullObjectStorageObj("x"), fileSt: pinFullFileStorageObj(), wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "empty provider", provider: "", stack: "VM", aws: pinFullAWSConfigObj(), gcp: n, azure: n, k8s: n, objSt: n, fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "GENERIC", provider: "GENERIC", stack: "VM", aws: pinFullAWSConfigObj(), gcp: n, azure: n, k8s: n, objSt: pinFullObjectStorageObj("x"), fileSt: n, wantErr: "generic clouds are not yet supported by this provider", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "generic lowercase", provider: "generic", stack: "K8S", aws: n, gcp: n, azure: n, k8s: n, objSt: n, fileSt: n, wantErr: "generic clouds are not yet supported by this provider", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},

		// ---- compute_stack matching is exact "K8S" for AWS/GCP ----
		{name: "AWS empty stack takes VM path", provider: "AWS", stack: "", aws: testAWSConfigObj("vpc-1"), gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: n, fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","aws_config":{"vpc_id":"vpc-1","subnet_ids":["subnet-111"],"security_group_ids":["sg-111"],"anyscale_iam_role_id":"arn:aws:iam::123456789012:role/cp","cluster_iam_role_id":"arn:aws:iam::123456789012:role/dp"}}`},
		{name: "AWS empty stack without aws_config", provider: "AWS", stack: "", aws: n, gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: testObjectStorageObj("b"), fileSt: n, wantErr: "aws_config is required when cloud_provider is AWS and compute_stack is VM", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "AWS lowercase k8s takes VM path", provider: "AWS", stack: "k8s", aws: n, gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: testObjectStorageObj("b"), fileSt: n, wantErr: "aws_config is required when cloud_provider is AWS and compute_stack is VM", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},
		{name: "GCP empty stack takes VM path", provider: "GCP", stack: "", aws: n, gcp: testGCPConfigObj("p"), azure: n, k8s: pinFullKubernetesConfigObj(), objSt: n, fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","gcp_config":{"project_id":"p","provider_name":"projects/123/locations/global/workloadIdentityPools/pool/providers/provider","vpc_name":"anyscale-vpc","subnet_names":["subnet-a"],"anyscale_service_account_email":"cp@my-project.iam.gserviceaccount.com","cluster_service_account_email":"dp@my-project.iam.gserviceaccount.com"}}`},
		{name: "GCP lowercase k8s takes VM path", provider: "GCP", stack: "k8s", aws: n, gcp: n, azure: n, k8s: pinFullKubernetesConfigObj(), objSt: testObjectStorageObj("b"), fileSt: n, wantErr: "gcp_config is required when cloud_provider is GCP and compute_stack is VM", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":""}`},

		// ---- provider case normalization ----
		{name: "lowercase aws K8S", provider: "aws", stack: "K8S", aws: n, gcp: n, azure: n, k8s: testKubernetesConfigObj("op"), objSt: testObjectStorageObj("b"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"s3://b","region":"us-west-2"},"kubernetes_config":{"anyscale_operator_iam_identity":"op","zones":["us-east-2a"]}}`},
		{name: "mixed-case Gcp VM", provider: "Gcp", stack: "VM", aws: n, gcp: testGCPConfigObj("p"), azure: n, k8s: n, objSt: n, fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","gcp_config":{"project_id":"p","provider_name":"projects/123/locations/global/workloadIdentityPools/pool/providers/provider","vpc_name":"anyscale-vpc","subnet_names":["subnet-a"],"anyscale_service_account_email":"cp@my-project.iam.gserviceaccount.com","cluster_service_account_email":"dp@my-project.iam.gserviceaccount.com"}}`},

		// ---- bucket prefixing is per provider ----
		{name: "AWS VM gs bucket gets s3 prefix", provider: "AWS", stack: "VM", aws: testAWSConfigObj("vpc-1"), gcp: n, azure: n, k8s: n, objSt: testObjectStorageObj("gs://x"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"s3://gs://x","region":"us-west-2"},"aws_config":{"vpc_id":"vpc-1","subnet_ids":["subnet-111"],"security_group_ids":["sg-111"],"anyscale_iam_role_id":"arn:aws:iam::123456789012:role/cp","cluster_iam_role_id":"arn:aws:iam::123456789012:role/dp"}}`},
		{name: "AWS K8S gs bucket gets s3 prefix", provider: "AWS", stack: "K8S", aws: n, gcp: n, azure: n, k8s: testKubernetesConfigObj("op"), objSt: testObjectStorageObj("gs://x"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"s3://gs://x","region":"us-west-2"},"kubernetes_config":{"anyscale_operator_iam_identity":"op","zones":["us-east-2a"]}}`},
		{name: "AWS VM s3 bucket unchanged", provider: "AWS", stack: "VM", aws: testAWSConfigObj("vpc-1"), gcp: n, azure: n, k8s: n, objSt: testObjectStorageObj("s3://x"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"s3://x","region":"us-west-2"},"aws_config":{"vpc_id":"vpc-1","subnet_ids":["subnet-111"],"security_group_ids":["sg-111"],"anyscale_iam_role_id":"arn:aws:iam::123456789012:role/cp","cluster_iam_role_id":"arn:aws:iam::123456789012:role/dp"}}`},
		{name: "AWS K8S s3 bucket unchanged", provider: "AWS", stack: "K8S", aws: n, gcp: n, azure: n, k8s: testKubernetesConfigObj("op"), objSt: testObjectStorageObj("s3://x"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"s3://x","region":"us-west-2"},"kubernetes_config":{"anyscale_operator_iam_identity":"op","zones":["us-east-2a"]}}`},
		{name: "GCP VM s3 bucket gets gs prefix", provider: "GCP", stack: "VM", aws: n, gcp: testGCPConfigObj("p"), azure: n, k8s: n, objSt: testObjectStorageObj("s3://x"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"gs://s3://x","region":"us-west-2"},"gcp_config":{"project_id":"p","provider_name":"projects/123/locations/global/workloadIdentityPools/pool/providers/provider","vpc_name":"anyscale-vpc","subnet_names":["subnet-a"],"anyscale_service_account_email":"cp@my-project.iam.gserviceaccount.com","cluster_service_account_email":"dp@my-project.iam.gserviceaccount.com"}}`},
		{name: "GCP K8S s3 bucket gets gs prefix", provider: "GCP", stack: "K8S", aws: n, gcp: n, azure: n, k8s: testKubernetesConfigObj("op"), objSt: testObjectStorageObj("s3://x"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"gs://s3://x","region":"us-west-2"},"kubernetes_config":{"anyscale_operator_iam_identity":"op","zones":["us-east-2a"]}}`},
		{name: "GCP VM gs bucket unchanged", provider: "GCP", stack: "VM", aws: n, gcp: testGCPConfigObj("p"), azure: n, k8s: n, objSt: testObjectStorageObj("gs://x"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"gs://x","region":"us-west-2"},"gcp_config":{"project_id":"p","provider_name":"projects/123/locations/global/workloadIdentityPools/pool/providers/provider","vpc_name":"anyscale-vpc","subnet_names":["subnet-a"],"anyscale_service_account_email":"cp@my-project.iam.gserviceaccount.com","cluster_service_account_email":"dp@my-project.iam.gserviceaccount.com"}}`},
		{name: "GCP K8S gs bucket unchanged", provider: "GCP", stack: "K8S", aws: n, gcp: n, azure: n, k8s: testKubernetesConfigObj("op"), objSt: testObjectStorageObj("gs://x"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"gs://x","region":"us-west-2"},"kubernetes_config":{"anyscale_operator_iam_identity":"op","zones":["us-east-2a"]}}`},
		{name: "AZURE bare bucket gets no prefix", provider: "AZURE", stack: "K8S", aws: n, gcp: n, azure: n, k8s: testKubernetesConfigObj("op"), objSt: testObjectStorageObj("bare-bucket"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"bare-bucket","region":"us-west-2"},"kubernetes_config":{"anyscale_operator_iam_identity":"op","zones":["us-east-2a"]}}`},
		{name: "AZURE s3 bucket gets no prefix", provider: "AZURE", stack: "K8S", aws: n, gcp: n, azure: n, k8s: testKubernetesConfigObj("op"), objSt: testObjectStorageObj("s3://x"), fileSt: n, wantErr: "", wantJSON: `{"name":"","provider":"","compute_stack":"","region":"","networking_mode":"","object_storage":{"bucket_name":"s3://x","region":"us-west-2"},"kubernetes_config":{"anyscale_operator_iam_identity":"op","zones":["us-east-2a"]}}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deployReq := &CloudDeploymentRequest{}
			err := buildProviderConfig(ctx, deployReq, c.provider, c.stack, c.aws, c.gcp, c.azure, c.k8s, c.objSt, c.fileSt)

			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if gotErr != c.wantErr {
				t.Errorf("error:\n got: %q\nwant: %q", gotErr, c.wantErr)
			}

			b, mErr := json.Marshal(deployReq)
			if mErr != nil {
				t.Fatalf("json.Marshal: %v", mErr)
			}
			if string(b) != c.wantJSON {
				t.Errorf("request JSON:\n got: %s\nwant: %s", b, c.wantJSON)
			}
		})
	}
}
