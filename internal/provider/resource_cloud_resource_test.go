package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestParseCloudResourceID(t *testing.T) {
	tests := []struct {
		name        string
		id          string
		wantCloudID string
		wantResName string
		wantErr     bool
	}{
		{
			name:        "valid format",
			id:          "cld_123:vm-aws-us-east-2",
			wantCloudID: "cld_123",
			wantResName: "vm-aws-us-east-2",
			wantErr:     false,
		},
		{
			name:        "resource name with colons",
			id:          "cld_abc:k8s-gcp-us-central1:custom",
			wantCloudID: "cld_abc",
			wantResName: "k8s-gcp-us-central1:custom",
			wantErr:     false,
		},
		{
			name:    "missing delimiter",
			id:      "cld_123",
			wantErr: true,
		},
		{
			name:    "empty string",
			id:      "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCloudID, gotResName, err := parseCloudResourceID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseCloudResourceID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if gotCloudID != tt.wantCloudID {
					t.Errorf("parseCloudResourceID() gotCloudID = %v, want %v", gotCloudID, tt.wantCloudID)
				}
				if gotResName != tt.wantResName {
					t.Errorf("parseCloudResourceID() gotResName = %v, want %v", gotResName, tt.wantResName)
				}
			}
		})
	}
}

// stringPtrsEqual compares two *string for equality, treating nil and a
// pointer to "" as distinct (matching how stringPtrOrNull/expand* treat an
// absent value differently from an explicit empty string).
func stringPtrsEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// stringPtrDeref renders a *string for a test failure message.
func stringPtrDeref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestExpandAWSConfig(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		obj     types.Object
		want    *AWSConfig
		wantErr bool
	}{
		{
			name: "full config with subnet_ids_to_az",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"vpc_id":                      types.StringType,
					"subnet_ids":                  types.ListType{ElemType: types.StringType},
					"subnet_ids_to_az":            types.MapType{ElemType: types.StringType},
					"security_group_ids":          types.ListType{ElemType: types.StringType},
					"controlplane_iam_role_arn":   types.StringType,
					"dataplane_iam_role_arn":      types.StringType,
					"cluster_instance_profile_id": types.StringType,
					"external_id":                 types.StringType,
					"memorydb_cluster_name":       types.StringType,
					"memorydb_cluster_arn":        types.StringType,
					"memorydb_cluster_endpoint":   types.StringType,
				},
				map[string]attr.Value{
					"vpc_id":     types.StringValue("vpc-123"),
					"subnet_ids": types.ListNull(types.StringType),
					"subnet_ids_to_az": types.MapValueMust(
						types.StringType,
						map[string]attr.Value{
							"subnet-111": types.StringValue("us-east-2a"),
							"subnet-222": types.StringValue("us-east-2b"),
						},
					),
					"security_group_ids": types.ListValueMust(
						types.StringType,
						[]attr.Value{
							types.StringValue("sg-111"),
							types.StringValue("sg-222"),
						},
					),
					"controlplane_iam_role_arn":   types.StringValue("arn:aws:iam::123456789012:role/anyscale-controlplane"),
					"dataplane_iam_role_arn":      types.StringValue("arn:aws:iam::123456789012:role/anyscale-dataplane"),
					"cluster_instance_profile_id": types.StringValue("arn:aws:iam::123456789012:instance-profile/anyscale-dataplane"),
					"external_id":                 types.StringValue("external-123"),
					"memorydb_cluster_name":       types.StringValue("anyscale-memorydb"),
					"memorydb_cluster_arn":        types.StringValue("arn:aws:memorydb:us-east-2:123456789012:cluster/anyscale-memorydb"),
					"memorydb_cluster_endpoint":   types.StringValue("anyscale-memorydb.abc123.memorydb.us-east-2.amazonaws.com:6379"),
				},
			),
			want: &AWSConfig{
				VPCID:             "vpc-123",
				SubnetIDs:         []string{"subnet-111", "subnet-222"},
				Zones:             []string{"us-east-2a", "us-east-2b"},
				SecurityGroupIDs:  []string{"sg-111", "sg-222"},
				AnyscaleIAMRoleID: "arn:aws:iam::123456789012:role/anyscale-controlplane",
				ClusterIAMRoleID:  "arn:aws:iam::123456789012:role/anyscale-dataplane",
				ClusterInstanceProfileID: func() *string {
					s := "arn:aws:iam::123456789012:instance-profile/anyscale-dataplane"
					return &s
				}(),
				ExternalID: "external-123",
				MemoryDBClusterName: func() *string {
					s := "anyscale-memorydb"
					return &s
				}(),
				MemoryDBClusterARN: func() *string {
					s := "arn:aws:memorydb:us-east-2:123456789012:cluster/anyscale-memorydb"
					return &s
				}(),
				MemoryDBClusterEndpoint: func() *string {
					s := "anyscale-memorydb.abc123.memorydb.us-east-2.amazonaws.com:6379"
					return &s
				}(),
			},
			wantErr: false,
		},
		{
			name: "minimal config with subnet_ids list",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"vpc_id":                      types.StringType,
					"subnet_ids":                  types.ListType{ElemType: types.StringType},
					"subnet_ids_to_az":            types.MapType{ElemType: types.StringType},
					"security_group_ids":          types.ListType{ElemType: types.StringType},
					"controlplane_iam_role_arn":   types.StringType,
					"dataplane_iam_role_arn":      types.StringType,
					"cluster_instance_profile_id": types.StringType,
					"external_id":                 types.StringType,
					"memorydb_cluster_name":       types.StringType,
					"memorydb_cluster_arn":        types.StringType,
					"memorydb_cluster_endpoint":   types.StringType,
				},
				map[string]attr.Value{
					"vpc_id": types.StringValue("vpc-456"),
					"subnet_ids": types.ListValueMust(
						types.StringType,
						[]attr.Value{
							types.StringValue("subnet-aaa"),
							types.StringValue("subnet-bbb"),
						},
					),
					"subnet_ids_to_az": types.MapNull(types.StringType),
					"security_group_ids": types.ListValueMust(
						types.StringType,
						[]attr.Value{
							types.StringValue("sg-aaa"),
						},
					),
					"controlplane_iam_role_arn":   types.StringValue("arn:aws:iam::999:role/cp"),
					"dataplane_iam_role_arn":      types.StringValue("arn:aws:iam::999:role/dp"),
					"cluster_instance_profile_id": types.StringNull(),
					"external_id":                 types.StringNull(),
					"memorydb_cluster_name":       types.StringNull(),
					"memorydb_cluster_arn":        types.StringNull(),
					"memorydb_cluster_endpoint":   types.StringNull(),
				},
			),
			want: &AWSConfig{
				VPCID:             "vpc-456",
				SubnetIDs:         []string{"subnet-aaa", "subnet-bbb"},
				SecurityGroupIDs:  []string{"sg-aaa"},
				AnyscaleIAMRoleID: "arn:aws:iam::999:role/cp",
				ClusterIAMRoleID:  "arn:aws:iam::999:role/dp",
			},
			wantErr: false,
		},
		{
			name:    "null object",
			obj:     types.ObjectNull(map[string]attr.Type{}),
			want:    nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandAWSConfig(ctx, tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("expandAWSConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.want == nil && got != nil {
				t.Errorf("expandAWSConfig() = %v, want nil", got)
				return
			}
			if tt.want != nil {
				if got == nil {
					t.Errorf("expandAWSConfig() = nil, want non-nil")
					return
				}
				if got.VPCID != tt.want.VPCID {
					t.Errorf("expandAWSConfig() VPCID = %v, want %v", got.VPCID, tt.want.VPCID)
				}
				if got.AnyscaleIAMRoleID != tt.want.AnyscaleIAMRoleID {
					t.Errorf("expandAWSConfig() AnyscaleIAMRoleID = %v, want %v", got.AnyscaleIAMRoleID, tt.want.AnyscaleIAMRoleID)
				}
				if got.ClusterIAMRoleID != tt.want.ClusterIAMRoleID {
					t.Errorf("expandAWSConfig() ClusterIAMRoleID = %v, want %v", got.ClusterIAMRoleID, tt.want.ClusterIAMRoleID)
				}
				if !stringPtrsEqual(got.ClusterInstanceProfileID, tt.want.ClusterInstanceProfileID) {
					t.Errorf("expandAWSConfig() ClusterInstanceProfileID = %v, want %v", stringPtrDeref(got.ClusterInstanceProfileID), stringPtrDeref(tt.want.ClusterInstanceProfileID))
				}
				// Verify SubnetIDs (order may vary for map-based input)
				if len(got.SubnetIDs) != len(tt.want.SubnetIDs) {
					t.Errorf("expandAWSConfig() SubnetIDs length = %v, want %v", len(got.SubnetIDs), len(tt.want.SubnetIDs))
				}
				// Verify SecurityGroupIDs
				if len(got.SecurityGroupIDs) != len(tt.want.SecurityGroupIDs) {
					t.Errorf("expandAWSConfig() SecurityGroupIDs length = %v, want %v", len(got.SecurityGroupIDs), len(tt.want.SecurityGroupIDs))
				}
			}
		})
	}
}

func TestExpandGCPConfig(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		obj     types.Object
		want    *GCPConfig
		wantErr bool
	}{
		{
			name: "full config",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"project_id":                         types.StringType,
					"host_project_id":                    types.StringType,
					"provider_name":                      types.StringType,
					"vpc_name":                           types.StringType,
					"subnet_names":                       types.ListType{ElemType: types.StringType},
					"controlplane_service_account_email": types.StringType,
					"dataplane_service_account_email":    types.StringType,
					"firewall_policy_names":              types.ListType{ElemType: types.StringType},
					"memorystore_instance_name":          types.StringType,
					"memorystore_endpoint":               types.StringType,
				},
				map[string]attr.Value{
					"project_id":      types.StringValue("my-project"),
					"host_project_id": types.StringValue("host-project"),
					"provider_name":   types.StringValue("projects/123/locations/global/workloadIdentityPools/pool/providers/provider"),
					"vpc_name":        types.StringValue("anyscale-vpc"),
					"subnet_names": types.ListValueMust(
						types.StringType,
						[]attr.Value{
							types.StringValue("anyscale-subnet-1"),
							types.StringValue("anyscale-subnet-2"),
						},
					),
					"controlplane_service_account_email": types.StringValue("anyscale-cp@my-project.iam.gserviceaccount.com"),
					"dataplane_service_account_email":    types.StringValue("anyscale-dp@my-project.iam.gserviceaccount.com"),
					"firewall_policy_names": types.ListValueMust(
						types.StringType,
						[]attr.Value{
							types.StringValue("anyscale-fw-policy"),
						},
					),
					"memorystore_instance_name": types.StringValue("anyscale-memorystore"),
					"memorystore_endpoint":      types.StringValue("10.0.0.3:6379"),
				},
			),
			want: &GCPConfig{
				ProjectID:                   "my-project",
				HostProjectID:               "host-project",
				ProviderName:                "projects/123/locations/global/workloadIdentityPools/pool/providers/provider",
				VPCName:                     "anyscale-vpc",
				SubnetNames:                 []string{"anyscale-subnet-1", "anyscale-subnet-2"},
				AnyscaleServiceAccountEmail: "anyscale-cp@my-project.iam.gserviceaccount.com",
				ClusterServiceAccountEmail:  "anyscale-dp@my-project.iam.gserviceaccount.com",
				FirewallPolicyNames:         []string{"anyscale-fw-policy"},
				MemorystoreInstanceName:     "anyscale-memorystore",
				MemorystoreEndpoint:         "10.0.0.3:6379",
			},
			wantErr: false,
		},
		{
			name: "minimal config",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"project_id":                         types.StringType,
					"host_project_id":                    types.StringType,
					"provider_name":                      types.StringType,
					"vpc_name":                           types.StringType,
					"subnet_names":                       types.ListType{ElemType: types.StringType},
					"controlplane_service_account_email": types.StringType,
					"dataplane_service_account_email":    types.StringType,
					"firewall_policy_names":              types.ListType{ElemType: types.StringType},
					"memorystore_instance_name":          types.StringType,
					"memorystore_endpoint":               types.StringType,
				},
				map[string]attr.Value{
					"project_id":      types.StringValue("project-123"),
					"host_project_id": types.StringNull(),
					"provider_name":   types.StringValue("projects/456/locations/global/workloadIdentityPools/test/providers/test"),
					"vpc_name":        types.StringValue("vpc-test"),
					"subnet_names": types.ListValueMust(
						types.StringType,
						[]attr.Value{
							types.StringValue("subnet-test"),
						},
					),
					"controlplane_service_account_email": types.StringValue("cp@project-123.iam.gserviceaccount.com"),
					"dataplane_service_account_email":    types.StringValue("dp@project-123.iam.gserviceaccount.com"),
					"firewall_policy_names":              types.ListNull(types.StringType),
					"memorystore_instance_name":          types.StringNull(),
					"memorystore_endpoint":               types.StringNull(),
				},
			),
			want: &GCPConfig{
				ProjectID:                   "project-123",
				ProviderName:                "projects/456/locations/global/workloadIdentityPools/test/providers/test",
				VPCName:                     "vpc-test",
				SubnetNames:                 []string{"subnet-test"},
				AnyscaleServiceAccountEmail: "cp@project-123.iam.gserviceaccount.com",
				ClusterServiceAccountEmail:  "dp@project-123.iam.gserviceaccount.com",
			},
			wantErr: false,
		},
		{
			name:    "null object",
			obj:     types.ObjectNull(map[string]attr.Type{}),
			want:    nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandGCPConfig(ctx, tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("expandGCPConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.want == nil && got != nil {
				t.Errorf("expandGCPConfig() = %v, want nil", got)
				return
			}
			if tt.want != nil {
				if got == nil {
					t.Errorf("expandGCPConfig() = nil, want non-nil")
					return
				}
				if got.ProjectID != tt.want.ProjectID {
					t.Errorf("expandGCPConfig() ProjectID = %v, want %v", got.ProjectID, tt.want.ProjectID)
				}
				if got.VPCName != tt.want.VPCName {
					t.Errorf("expandGCPConfig() VPCName = %v, want %v", got.VPCName, tt.want.VPCName)
				}
				if got.AnyscaleServiceAccountEmail != tt.want.AnyscaleServiceAccountEmail {
					t.Errorf("expandGCPConfig() AnyscaleServiceAccountEmail = %v, want %v", got.AnyscaleServiceAccountEmail, tt.want.AnyscaleServiceAccountEmail)
				}
			}
		})
	}
}

func TestExpandKubernetesConfig(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		obj     types.Object
		want    *KubernetesConfig
		wantErr bool
	}{
		{
			name: "full config",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"anyscale_operator_iam_identity": types.StringType,
					"zones":                          types.ListType{ElemType: types.StringType},
					"redis_endpoint":                 types.StringType,
				},
				map[string]attr.Value{
					"anyscale_operator_iam_identity": types.StringValue("arn:aws:iam::123456789012:role/anyscale-operator"),
					"zones": types.ListValueMust(
						types.StringType,
						[]attr.Value{
							types.StringValue("us-east-2a"),
							types.StringValue("us-east-2b"),
						},
					),
					"redis_endpoint": types.StringValue("redis.ray-system.svc.cluster.local:6379"),
				},
			),
			want: &KubernetesConfig{
				AnyscaleOperatorIAMIdentity: "arn:aws:iam::123456789012:role/anyscale-operator",
				Zones:                       []string{"us-east-2a", "us-east-2b"},
				RedisEndpoint:               "redis.ray-system.svc.cluster.local:6379",
			},
			wantErr: false,
		},
		{
			name: "minimal config",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"anyscale_operator_iam_identity": types.StringType,
					"zones":                          types.ListType{ElemType: types.StringType},
					"redis_endpoint":                 types.StringType,
				},
				map[string]attr.Value{
					"anyscale_operator_iam_identity": types.StringValue("operator@project.iam.gserviceaccount.com"),
					"zones":                          types.ListNull(types.StringType),
					"redis_endpoint":                 types.StringNull(),
				},
			),
			want: &KubernetesConfig{
				AnyscaleOperatorIAMIdentity: "operator@project.iam.gserviceaccount.com",
			},
			wantErr: false,
		},
		{
			name:    "null object",
			obj:     types.ObjectNull(map[string]attr.Type{}),
			want:    nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandKubernetesConfig(ctx, tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("expandKubernetesConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.want == nil && got != nil {
				t.Errorf("expandKubernetesConfig() = %v, want nil", got)
				return
			}
			if tt.want != nil {
				if got == nil {
					t.Errorf("expandKubernetesConfig() = nil, want non-nil")
					return
				}
				if got.AnyscaleOperatorIAMIdentity != tt.want.AnyscaleOperatorIAMIdentity {
					t.Errorf("expandKubernetesConfig() AnyscaleOperatorIAMIdentity = %v, want %v", got.AnyscaleOperatorIAMIdentity, tt.want.AnyscaleOperatorIAMIdentity)
				}
				if len(got.Zones) != len(tt.want.Zones) {
					t.Errorf("expandKubernetesConfig() Zones length = %v, want %v", len(got.Zones), len(tt.want.Zones))
				}
				if got.RedisEndpoint != tt.want.RedisEndpoint {
					t.Errorf("expandKubernetesConfig() RedisEndpoint = %v, want %v", got.RedisEndpoint, tt.want.RedisEndpoint)
				}
			}
		})
	}
}

func TestExpandObjectStorage(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		obj     types.Object
		want    *ObjectStorage
		wantErr bool
	}{
		{
			name: "full S3 config",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"bucket_name": types.StringType,
					"region":      types.StringType,
					"endpoint":    types.StringType,
				},
				map[string]attr.Value{
					"bucket_name": types.StringValue("my-anyscale-bucket"),
					"region":      types.StringValue("us-west-2"),
					"endpoint":    types.StringValue("https://s3.us-west-2.amazonaws.com"),
				},
			),
			want: &ObjectStorage{
				BucketName: "my-anyscale-bucket",
				Region: func() *string {
					s := "us-west-2"
					return &s
				}(),
				Endpoint: func() *string {
					s := "https://s3.us-west-2.amazonaws.com"
					return &s
				}(),
			},
			wantErr: false,
		},
		{
			name: "minimal GCS config",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"bucket_name": types.StringType,
					"region":      types.StringType,
					"endpoint":    types.StringType,
				},
				map[string]attr.Value{
					"bucket_name": types.StringValue("gs://my-gcs-bucket"),
					"region":      types.StringNull(),
					"endpoint":    types.StringNull(),
				},
			),
			want: &ObjectStorage{
				BucketName: "gs://my-gcs-bucket",
			},
			wantErr: false,
		},
		{
			name:    "null object",
			obj:     types.ObjectNull(map[string]attr.Type{}),
			want:    nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandObjectStorage(ctx, tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("expandObjectStorage() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.want == nil && got != nil {
				t.Errorf("expandObjectStorage() = %v, want nil", got)
				return
			}
			if tt.want != nil {
				if got == nil {
					t.Errorf("expandObjectStorage() = nil, want non-nil")
					return
				}
				if got.BucketName != tt.want.BucketName {
					t.Errorf("expandObjectStorage() BucketName = %v, want %v", got.BucketName, tt.want.BucketName)
				}
			}
		})
	}
}

func TestExpandAzureConfig(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		obj     types.Object
		want    *AzureConfig
		wantErr bool
	}{
		{
			name: "tenant_id set",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"tenant_id": types.StringType,
				},
				map[string]attr.Value{
					"tenant_id": types.StringValue("11111111-1111-1111-1111-111111111111"),
				},
			),
			want: &AzureConfig{
				TenantID: "11111111-1111-1111-1111-111111111111",
			},
			wantErr: false,
		},
		{
			name: "tenant_id unset - azure_config is optional per the AKS design",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"tenant_id": types.StringType,
				},
				map[string]attr.Value{
					"tenant_id": types.StringNull(),
				},
			),
			want: &AzureConfig{
				TenantID: "",
			},
			wantErr: false,
		},
		{
			name:    "null object",
			obj:     types.ObjectNull(map[string]attr.Type{}),
			want:    nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandAzureConfig(ctx, tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("expandAzureConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.want == nil && got != nil {
				t.Errorf("expandAzureConfig() = %v, want nil", got)
				return
			}
			if tt.want != nil {
				if got == nil {
					t.Errorf("expandAzureConfig() = nil, want non-nil")
					return
				}
				if got.TenantID != tt.want.TenantID {
					t.Errorf("expandAzureConfig() TenantID = %v, want %v", got.TenantID, tt.want.TenantID)
				}
			}
		})
	}
}

func TestExpandFileStorage(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		obj     types.Object
		want    *FileStorage
		wantErr bool
	}{
		{
			name: "full EFS config with mount targets",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"file_storage_id":             types.StringType,
					"mount_path":                  types.StringType,
					"persistent_volume_claim":     types.StringType,
					"csi_ephemeral_volume_driver": types.StringType,
					"mount_targets": types.ListType{
						ElemType: types.ObjectType{
							AttrTypes: map[string]attr.Type{
								"address": types.StringType,
								"zone":    types.StringType,
							},
						},
					},
				},
				map[string]attr.Value{
					"file_storage_id":             types.StringValue("fs-12345678"),
					"mount_path":                  types.StringValue("/mnt/efs"),
					"persistent_volume_claim":     types.StringValue("ray-shared-pvc"),
					"csi_ephemeral_volume_driver": types.StringNull(),
					"mount_targets": types.ListValueMust(
						types.ObjectType{
							AttrTypes: map[string]attr.Type{
								"address": types.StringType,
								"zone":    types.StringType,
							},
						},
						[]attr.Value{
							types.ObjectValueMust(
								map[string]attr.Type{
									"address": types.StringType,
									"zone":    types.StringType,
								},
								map[string]attr.Value{
									"address": types.StringValue("fs-12345678.efs.us-east-2.amazonaws.com"),
									"zone":    types.StringValue("us-east-2a"),
								},
							),
							types.ObjectValueMust(
								map[string]attr.Type{
									"address": types.StringType,
									"zone":    types.StringType,
								},
								map[string]attr.Value{
									"address": types.StringValue("fs-12345678.efs.us-east-2.amazonaws.com"),
									"zone":    types.StringValue("us-east-2b"),
								},
							),
						},
					),
				},
			),
			want: &FileStorage{
				FileStorageID:         "fs-12345678",
				MountPath:             "/mnt/efs",
				PersistentVolumeClaim: "ray-shared-pvc",
				MountTargets: []MountTarget{
					{
						Address: "fs-12345678.efs.us-east-2.amazonaws.com",
						Zone:    "us-east-2a",
					},
					{
						Address: "fs-12345678.efs.us-east-2.amazonaws.com",
						Zone:    "us-east-2b",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "minimal config without mount targets",
			obj: types.ObjectValueMust(
				map[string]attr.Type{
					"file_storage_id":             types.StringType,
					"mount_path":                  types.StringType,
					"persistent_volume_claim":     types.StringType,
					"csi_ephemeral_volume_driver": types.StringType,
					"mount_targets": types.ListType{
						ElemType: types.ObjectType{
							AttrTypes: map[string]attr.Type{
								"address": types.StringType,
								"zone":    types.StringType,
							},
						},
					},
				},
				map[string]attr.Value{
					"file_storage_id":             types.StringValue("filestore-instance"),
					"mount_path":                  types.StringValue("/mnt/shared"),
					"persistent_volume_claim":     types.StringNull(),
					"csi_ephemeral_volume_driver": types.StringValue("ephemeral.csi.example.com"),
					"mount_targets": types.ListNull(types.ObjectType{
						AttrTypes: map[string]attr.Type{
							"address": types.StringType,
							"zone":    types.StringType,
						},
					}),
				},
			),
			want: &FileStorage{
				FileStorageID:            "filestore-instance",
				MountPath:                "/mnt/shared",
				CSIEphemeralVolumeDriver: "ephemeral.csi.example.com",
			},
			wantErr: false,
		},
		{
			name:    "null object",
			obj:     types.ObjectNull(map[string]attr.Type{}),
			want:    nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandFileStorage(ctx, tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("expandFileStorage() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.want == nil && got != nil {
				t.Errorf("expandFileStorage() = %v, want nil", got)
				return
			}
			if tt.want != nil {
				if got == nil {
					t.Errorf("expandFileStorage() = nil, want non-nil")
					return
				}
				if got.FileStorageID != tt.want.FileStorageID {
					t.Errorf("expandFileStorage() FileStorageID = %v, want %v", got.FileStorageID, tt.want.FileStorageID)
				}
				if got.MountPath != tt.want.MountPath {
					t.Errorf("expandFileStorage() MountPath = %v, want %v", got.MountPath, tt.want.MountPath)
				}
				if got.PersistentVolumeClaim != tt.want.PersistentVolumeClaim {
					t.Errorf("expandFileStorage() PersistentVolumeClaim = %v, want %v", got.PersistentVolumeClaim, tt.want.PersistentVolumeClaim)
				}
				if got.CSIEphemeralVolumeDriver != tt.want.CSIEphemeralVolumeDriver {
					t.Errorf("expandFileStorage() CSIEphemeralVolumeDriver = %v, want %v", got.CSIEphemeralVolumeDriver, tt.want.CSIEphemeralVolumeDriver)
				}
				if len(got.MountTargets) != len(tt.want.MountTargets) {
					t.Errorf("expandFileStorage() MountTargets length = %v, want %v", len(got.MountTargets), len(tt.want.MountTargets))
				}
			}
		})
	}
}

// The case-normalization regression test that lived here against addProviderConfig
// (TestAddProviderConfig_LowercaseProviderNormalizes, added in the PR that fixed the bug) moved
// to TestBuildProviderConfig_RequiredCombos in cloud_helpers_test.go's "lowercase and mixed-case
// provider still match" subtest, once addProviderConfig itself was consolidated into
// buildProviderConfig (workbench #6) - same coverage, now against the function that actually
// exists.
