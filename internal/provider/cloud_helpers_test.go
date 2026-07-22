package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestResolveCloudNameToID(t *testing.T) {
	ctx := context.Background()

	t.Run("single matching cloud", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v2/clouds" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"results": [
					{
						"id": "cloud-123",
						"name": "test-cloud",
						"provider": "aws",
						"created_at": "2024-01-01T00:00:00Z"
					},
					{
						"id": "cloud-456",
						"name": "other-cloud",
						"provider": "gcp",
						"created_at": "2024-01-02T00:00:00Z"
					}
				],
				"metadata": {
					"total": 2,
					"next_paging_token": null
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		cloudID, err := ResolveCloudNameToID(ctx, client, "test-cloud")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if cloudID != "cloud-123" {
			t.Errorf("expected cloud ID 'cloud-123', got '%s'", cloudID)
		}
	})

	t.Run("multiple clouds with same name - returns most recent", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"results": [
					{
						"id": "cloud-old",
						"name": "duplicate-cloud",
						"provider": "aws",
						"created_at": "2024-01-01T00:00:00Z"
					},
					{
						"id": "cloud-new",
						"name": "duplicate-cloud",
						"provider": "aws",
						"created_at": "2024-01-15T00:00:00Z"
					},
					{
						"id": "cloud-middle",
						"name": "duplicate-cloud",
						"provider": "aws",
						"created_at": "2024-01-10T00:00:00Z"
					}
				],
				"metadata": {
					"total": 3,
					"next_paging_token": null
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		cloudID, err := ResolveCloudNameToID(ctx, client, "duplicate-cloud")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if cloudID != "cloud-new" {
			t.Errorf("expected most recent cloud ID 'cloud-new', got '%s'", cloudID)
		}
	})

	t.Run("cloud not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"results": [
					{
						"id": "cloud-123",
						"name": "existing-cloud",
						"provider": "aws",
						"created_at": "2024-01-01T00:00:00Z"
					}
				],
				"metadata": {
					"total": 1,
					"next_paging_token": null
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		_, err := ResolveCloudNameToID(ctx, client, "nonexistent-cloud")
		if err == nil {
			t.Fatal("expected error for nonexistent cloud, got nil")
		}

		if !strings.Contains(err.Error(), "no cloud found with name 'nonexistent-cloud'") {
			t.Errorf("expected error about cloud not found, got: %v", err)
		}
	})

	t.Run("API error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": "internal server error"}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		_, err := ResolveCloudNameToID(ctx, client, "test-cloud")
		if err == nil {
			t.Fatal("expected error for API failure, got nil")
		}

		if !strings.Contains(err.Error(), "failed to list clouds") {
			t.Errorf("expected error about listing clouds, got: %v", err)
		}
	})

	t.Run("invalid JSON response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`invalid json`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		_, err := ResolveCloudNameToID(ctx, client, "test-cloud")
		if err == nil {
			t.Fatal("expected error for invalid JSON, got nil")
		}

		if !strings.Contains(err.Error(), "failed to list clouds") {
			t.Errorf("expected error about listing clouds, got: %v", err)
		}
	})

	t.Run("empty results", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"results": [],
				"metadata": {
					"total": 0,
					"next_paging_token": null
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		_, err := ResolveCloudNameToID(ctx, client, "any-cloud")
		if err == nil {
			t.Fatal("expected error for no results, got nil")
		}

		if !strings.Contains(err.Error(), "no cloud found") {
			t.Errorf("expected error about cloud not found, got: %v", err)
		}
	})

	t.Run("case sensitive matching", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"results": [
					{
						"id": "cloud-123",
						"name": "Test-Cloud",
						"provider": "aws",
						"created_at": "2024-01-01T00:00:00Z"
					}
				],
				"metadata": {
					"total": 1,
					"next_paging_token": null
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")

		// Should not match with different case
		_, err := ResolveCloudNameToID(ctx, client, "test-cloud")
		if err == nil {
			t.Fatal("expected error for case mismatch, got nil")
		}

		// Should match with exact case
		cloudID, err := ResolveCloudNameToID(ctx, client, "Test-Cloud")
		if err != nil {
			t.Fatalf("unexpected error for exact case match: %v", err)
		}

		if cloudID != "cloud-123" {
			t.Errorf("expected cloud ID 'cloud-123', got '%s'", cloudID)
		}
	})
}

// --- buildProviderConfig test fixtures ---
//
// Minimal-but-valid objects for each config type, reused across combos below. Field shapes
// mirror TestExpandAWSConfig/TestExpandGCPConfig/TestExpandKubernetesConfig/
// TestExpandObjectStorage/TestExpandFileStorage in resource_cloud_resource_test.go.

func testAWSConfigObj(vpcID string) types.Object {
	return types.ObjectValueMust(
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
			"vpc_id":                      types.StringValue(vpcID),
			"subnet_ids":                  types.ListValueMust(types.StringType, []attr.Value{types.StringValue("subnet-111")}),
			"subnet_ids_to_az":            types.MapNull(types.StringType),
			"security_group_ids":          types.ListValueMust(types.StringType, []attr.Value{types.StringValue("sg-111")}),
			"controlplane_iam_role_arn":   types.StringValue("arn:aws:iam::123456789012:role/cp"),
			"dataplane_iam_role_arn":      types.StringValue("arn:aws:iam::123456789012:role/dp"),
			"cluster_instance_profile_id": types.StringNull(),
			"external_id":                 types.StringNull(),
			"memorydb_cluster_name":       types.StringNull(),
			"memorydb_cluster_arn":        types.StringNull(),
			"memorydb_cluster_endpoint":   types.StringNull(),
		},
	)
}

func testGCPConfigObj(projectID string) types.Object {
	return types.ObjectValueMust(
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
			"project_id":                         types.StringValue(projectID),
			"host_project_id":                    types.StringNull(),
			"provider_name":                      types.StringValue("projects/123/locations/global/workloadIdentityPools/pool/providers/provider"),
			"vpc_name":                           types.StringValue("anyscale-vpc"),
			"subnet_names":                       types.ListValueMust(types.StringType, []attr.Value{types.StringValue("subnet-a")}),
			"controlplane_service_account_email": types.StringValue("cp@my-project.iam.gserviceaccount.com"),
			"dataplane_service_account_email":    types.StringValue("dp@my-project.iam.gserviceaccount.com"),
			"firewall_policy_names":              types.ListNull(types.StringType),
			"memorystore_instance_name":          types.StringNull(),
			"memorystore_endpoint":               types.StringNull(),
		},
	)
}

func testAzureConfigObj(tenantID string) types.Object {
	return types.ObjectValueMust(
		map[string]attr.Type{
			"tenant_id": types.StringType,
		},
		map[string]attr.Value{
			"tenant_id": types.StringValue(tenantID),
		},
	)
}

// testKubernetesConfigObj builds a kubernetes_config fixture with only the
// 3 real, API-backed fields - the other 5 were removed by task #8 (see
// TestFlattenKubernetesConfig_APIBackedFieldsPopulate for why).
func testKubernetesConfigObj(operatorIdentity string) types.Object {
	return types.ObjectValueMust(
		map[string]attr.Type{
			"anyscale_operator_iam_identity": types.StringType,
			"zones":                          types.ListType{ElemType: types.StringType},
			"redis_endpoint":                 types.StringType,
		},
		map[string]attr.Value{
			"anyscale_operator_iam_identity": types.StringValue(operatorIdentity),
			"zones":                          types.ListValueMust(types.StringType, []attr.Value{types.StringValue("us-east-2a")}),
			"redis_endpoint":                 types.StringNull(),
		},
	)
}

// testKubernetesConfigObjMissingIdentity has anyscale_operator_iam_identity set to "" rather
// than being null - expandKubernetesConfig still returns a non-nil *KubernetesConfig, so this
// exercises buildProviderConfig's own explicit check for an empty identity, not just a null object.
func testKubernetesConfigObjMissingIdentity() types.Object {
	return testKubernetesConfigObj("")
}

func testObjectStorageObj(bucketName string) types.Object {
	return types.ObjectValueMust(
		map[string]attr.Type{
			"bucket_name": types.StringType,
			"region":      types.StringType,
			"endpoint":    types.StringType,
		},
		map[string]attr.Value{
			"bucket_name": types.StringValue(bucketName),
			"region":      types.StringValue("us-west-2"),
			"endpoint":    types.StringNull(),
		},
	)
}

func testFileStorageObj(id string) types.Object {
	return types.ObjectValueMust(
		map[string]attr.Type{
			"file_storage_id":             types.StringType,
			"mount_path":                  types.StringType,
			"persistent_volume_claim":     types.StringType,
			"csi_ephemeral_volume_driver": types.StringType,
			"mount_targets":               types.ListType{ElemType: types.ObjectType{AttrTypes: map[string]attr.Type{"address": types.StringType, "zone": types.StringType}}},
		},
		map[string]attr.Value{
			"file_storage_id":             types.StringValue(id),
			"mount_path":                  types.StringNull(),
			"persistent_volume_claim":     types.StringNull(),
			"csi_ephemeral_volume_driver": types.StringNull(),
			"mount_targets":               types.ListNull(types.ObjectType{AttrTypes: map[string]attr.Type{"address": types.StringType, "zone": types.StringType}}),
		},
	)
}

func nullObj() types.Object {
	return types.ObjectNull(map[string]attr.Type{})
}

// TestBuildProviderConfig_RequiredCombos proves buildProviderConfig populates the right
// deployReq fields for every AWS/GCP x VM/K8S combo, matching the behavior previously
// duplicated across resource_cloud.go's addCloudResource and resource_cloud_resource.go's
// addProviderConfig (workbench #6).
func TestBuildProviderConfig_RequiredCombos(t *testing.T) {
	ctx := context.Background()

	t.Run("AWS VM: aws_config required, populates AWSConfig", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AWS", "VM", testAWSConfigObj("vpc-aws-vm"), nullObj(), nullObj(), nullObj(), nullObj(), nullObj())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deployReq.AWSConfig == nil || deployReq.AWSConfig.VPCID != "vpc-aws-vm" {
			t.Errorf("AWSConfig not populated correctly, got %+v", deployReq.AWSConfig)
		}
		if deployReq.ObjectStorage != nil || deployReq.FileStorage != nil || deployReq.GCPConfig != nil || deployReq.KubernetesConfig != nil {
			t.Errorf("only AWSConfig should be populated, got ObjectStorage=%v FileStorage=%v GCPConfig=%v KubernetesConfig=%v",
				deployReq.ObjectStorage, deployReq.FileStorage, deployReq.GCPConfig, deployReq.KubernetesConfig)
		}
	})

	t.Run("AWS VM: missing aws_config errors with canonical wording", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AWS", "VM", nullObj(), nullObj(), nullObj(), nullObj(), nullObj(), nullObj())
		wantErr := "aws_config is required when cloud_provider is AWS and compute_stack is VM"
		if err == nil || err.Error() != wantErr {
			t.Errorf("error = %v, want %q", err, wantErr)
		}
	})

	t.Run("AWS VM: optional object_storage and file_storage populate with s3 prefix", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AWS", "VM", testAWSConfigObj("vpc-1"), nullObj(), nullObj(), nullObj(), testObjectStorageObj("my-bucket"), testFileStorageObj("fs-1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deployReq.ObjectStorage == nil || deployReq.ObjectStorage.BucketName != "s3://my-bucket" {
			t.Errorf("ObjectStorage bucket not s3-prefixed correctly, got %+v", deployReq.ObjectStorage)
		}
		if deployReq.FileStorage == nil || deployReq.FileStorage.FileStorageID != "fs-1" {
			t.Errorf("FileStorage not populated correctly, got %+v", deployReq.FileStorage)
		}
	})

	t.Run("AWS VM: bucket_name already s3-prefixed is not double-prefixed", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AWS", "VM", testAWSConfigObj("vpc-1"), nullObj(), nullObj(), nullObj(), testObjectStorageObj("s3://already-prefixed"), nullObj())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deployReq.ObjectStorage == nil || deployReq.ObjectStorage.BucketName != "s3://already-prefixed" {
			t.Errorf("bucket_name got double-prefixed or mishandled, got %+v", deployReq.ObjectStorage)
		}
	})

	t.Run("AWS K8S: kubernetes_config and object_storage required, aws_config optional", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AWS", "K8S", nullObj(), nullObj(), nullObj(), testKubernetesConfigObj("arn:aws:iam::123:role/operator"), testObjectStorageObj("k8s-bucket"), nullObj())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deployReq.KubernetesConfig == nil || deployReq.KubernetesConfig.AnyscaleOperatorIAMIdentity != "arn:aws:iam::123:role/operator" {
			t.Errorf("KubernetesConfig not populated correctly, got %+v", deployReq.KubernetesConfig)
		}
		if deployReq.ObjectStorage == nil || deployReq.ObjectStorage.BucketName != "s3://k8s-bucket" {
			t.Errorf("ObjectStorage not populated correctly, got %+v", deployReq.ObjectStorage)
		}
		if deployReq.AWSConfig != nil {
			t.Errorf("aws_config was not provided, AWSConfig should stay nil, got %+v", deployReq.AWSConfig)
		}
	})

	t.Run("AWS K8S: missing kubernetes_config errors", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AWS", "K8S", nullObj(), nullObj(), nullObj(), nullObj(), testObjectStorageObj("b"), nullObj())
		wantErr := "kubernetes_config is required when cloud_provider is AWS and compute_stack is K8S"
		if err == nil || err.Error() != wantErr {
			t.Errorf("error = %v, want %q", err, wantErr)
		}
	})

	t.Run("AWS K8S: missing object_storage errors", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AWS", "K8S", nullObj(), nullObj(), nullObj(), testKubernetesConfigObj("arn:aws:iam::123:role/operator"), nullObj(), nullObj())
		wantErr := "object_storage is required when cloud_provider is AWS and compute_stack is K8S"
		if err == nil || err.Error() != wantErr {
			t.Errorf("error = %v, want %q", err, wantErr)
		}
	})

	t.Run("AWS K8S: empty anyscale_operator_iam_identity errors", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AWS", "K8S", nullObj(), nullObj(), nullObj(), testKubernetesConfigObjMissingIdentity(), testObjectStorageObj("b"), nullObj())
		wantErr := "kubernetes_config.anyscale_operator_iam_identity is required for AWS K8S clouds"
		if err == nil || err.Error() != wantErr {
			t.Errorf("error = %v, want %q", err, wantErr)
		}
	})

	t.Run("GCP VM: gcp_config required, populates GCPConfig with gs prefix", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "GCP", "VM", nullObj(), testGCPConfigObj("my-gcp-project"), nullObj(), nullObj(), testObjectStorageObj("gcp-bucket"), nullObj())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deployReq.GCPConfig == nil || deployReq.GCPConfig.ProjectID != "my-gcp-project" {
			t.Errorf("GCPConfig not populated correctly, got %+v", deployReq.GCPConfig)
		}
		if deployReq.ObjectStorage == nil || deployReq.ObjectStorage.BucketName != "gs://gcp-bucket" {
			t.Errorf("ObjectStorage bucket not gs-prefixed correctly, got %+v", deployReq.ObjectStorage)
		}
	})

	t.Run("GCP VM: missing gcp_config errors with canonical wording", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "GCP", "VM", nullObj(), nullObj(), nullObj(), nullObj(), nullObj(), nullObj())
		wantErr := "gcp_config is required when cloud_provider is GCP and compute_stack is VM"
		if err == nil || err.Error() != wantErr {
			t.Errorf("error = %v, want %q", err, wantErr)
		}
	})

	t.Run("GCP K8S: kubernetes_config and object_storage required, gcp_config optional", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "GCP", "K8S", nullObj(), nullObj(), nullObj(), testKubernetesConfigObj("operator@my-project.iam.gserviceaccount.com"), testObjectStorageObj("k8s-gcp-bucket"), nullObj())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deployReq.ObjectStorage == nil || deployReq.ObjectStorage.BucketName != "gs://k8s-gcp-bucket" {
			t.Errorf("ObjectStorage not populated correctly, got %+v", deployReq.ObjectStorage)
		}
		if deployReq.GCPConfig != nil {
			t.Errorf("gcp_config was not provided, GCPConfig should stay nil, got %+v", deployReq.GCPConfig)
		}
	})

	t.Run("lowercase and mixed-case provider still match (case-normalization)", func(t *testing.T) {
		for _, p := range []string{"aws", "Aws", "AWS"} {
			deployReq := &CloudDeploymentRequest{}
			err := buildProviderConfig(ctx, deployReq, p, "VM", testAWSConfigObj("vpc-case"), nullObj(), nullObj(), nullObj(), nullObj(), nullObj())
			if err != nil {
				t.Errorf("provider %q: unexpected error: %v", p, err)
			}
			if deployReq.AWSConfig == nil {
				t.Errorf("provider %q: AWSConfig not populated", p)
			}
		}
	})

	t.Run("AZURE VM errors - Anyscale does not support Azure VM clouds", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AZURE", "VM", nullObj(), nullObj(), testAzureConfigObj("11111111-1111-1111-1111-111111111111"), nullObj(), nullObj(), nullObj())
		wantErr := "azure clouds only support compute_stack = \"K8S\" - Anyscale does not support Azure VM clouds"
		if err == nil || err.Error() != wantErr {
			t.Errorf("error = %v, want %q", err, wantErr)
		}
	})

	t.Run("AZURE K8S: kubernetes_config and object_storage required, azure_config optional, no bucket scheme prepended", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		bucket := "abfss://container@account.dfs.core.windows.net"
		err := buildProviderConfig(ctx, deployReq, "AZURE", "K8S", nullObj(), nullObj(), testAzureConfigObj("11111111-1111-1111-1111-111111111111"), testKubernetesConfigObj("00000000-0000-0000-0000-000000000000"), testObjectStorageObj(bucket), nullObj())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deployReq.KubernetesConfig == nil || deployReq.KubernetesConfig.AnyscaleOperatorIAMIdentity != "00000000-0000-0000-0000-000000000000" {
			t.Errorf("KubernetesConfig not populated correctly, got %+v", deployReq.KubernetesConfig)
		}
		if deployReq.ObjectStorage == nil || deployReq.ObjectStorage.BucketName != bucket {
			t.Errorf("ObjectStorage bucket was mangled - Azure must be passed through verbatim, got %+v", deployReq.ObjectStorage)
		}
		if deployReq.AzureConfig == nil || deployReq.AzureConfig.TenantID != "11111111-1111-1111-1111-111111111111" {
			t.Errorf("AzureConfig not populated correctly, got %+v", deployReq.AzureConfig)
		}
		if deployReq.AWSConfig != nil || deployReq.GCPConfig != nil {
			t.Errorf("only Azure fields should be populated, got AWSConfig=%+v GCPConfig=%+v", deployReq.AWSConfig, deployReq.GCPConfig)
		}
	})

	t.Run("AZURE K8S: azure_config is optional, omitting it leaves AzureConfig nil", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AZURE", "K8S", nullObj(), nullObj(), nullObj(), testKubernetesConfigObj("00000000-0000-0000-0000-000000000000"), testObjectStorageObj("abfss://container@account.dfs.core.windows.net"), nullObj())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deployReq.AzureConfig != nil {
			t.Errorf("azure_config was not provided, AzureConfig should stay nil, got %+v", deployReq.AzureConfig)
		}
	})

	t.Run("AZURE K8S: missing kubernetes_config errors", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AZURE", "K8S", nullObj(), nullObj(), nullObj(), nullObj(), testObjectStorageObj("abfss://container@account.dfs.core.windows.net"), nullObj())
		wantErr := "kubernetes_config is required when cloud_provider is AZURE"
		if err == nil || err.Error() != wantErr {
			t.Errorf("error = %v, want %q", err, wantErr)
		}
	})

	t.Run("AZURE K8S: missing object_storage errors", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AZURE", "K8S", nullObj(), nullObj(), nullObj(), testKubernetesConfigObj("00000000-0000-0000-0000-000000000000"), nullObj(), nullObj())
		wantErr := "object_storage is required when cloud_provider is AZURE"
		if err == nil || err.Error() != wantErr {
			t.Errorf("error = %v, want %q", err, wantErr)
		}
	})

	t.Run("AZURE K8S: empty anyscale_operator_iam_identity errors", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AZURE", "K8S", nullObj(), nullObj(), nullObj(), testKubernetesConfigObjMissingIdentity(), testObjectStorageObj("abfss://container@account.dfs.core.windows.net"), nullObj())
		wantErr := "kubernetes_config.anyscale_operator_iam_identity is required for AZURE K8S clouds"
		if err == nil || err.Error() != wantErr {
			t.Errorf("error = %v, want %q", err, wantErr)
		}
	})

	t.Run("GENERIC returns the not-supported error", func(t *testing.T) {
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "GENERIC", "VM", nullObj(), nullObj(), nullObj(), nullObj(), nullObj(), nullObj())
		wantErr := "generic clouds are not yet supported by this provider"
		if err == nil || err.Error() != wantErr {
			t.Errorf("error = %v, want %q", err, wantErr)
		}
	})

	t.Run("expand failure is wrapped with context", func(t *testing.T) {
		// An object missing a required attribute key fails obj.As() inside expandAWSConfig,
		// proving buildProviderConfig wraps that failure rather than returning it bare.
		malformed := types.ObjectValueMust(
			map[string]attr.Type{"vpc_id": types.StringType},
			map[string]attr.Value{"vpc_id": types.StringValue("vpc-1")},
		)
		deployReq := &CloudDeploymentRequest{}
		err := buildProviderConfig(ctx, deployReq, "AWS", "VM", malformed, nullObj(), nullObj(), nullObj(), nullObj(), nullObj())
		if err == nil || !strings.Contains(err.Error(), "failed to expand aws_config") {
			t.Errorf("error = %v, want it wrapped with 'failed to expand aws_config'", err)
		}
	})
}

// TestBucketNameSemanticEqualPlanModifier is the regression proof for the
// object_storage.bucket_name import round-trip bug found during the real GKE
// acceptance run: a GCP cloud whose bucket was written bare ("my-bucket")
// diverged from the API's canonical gs://-prefixed form the moment it was
// imported, forcing a spurious RequiresReplace since bucket_name is
// Optional (not Computed) and stripBucketPrefix never un-prefixes GCP.
func TestBucketNameSemanticEqualPlanModifier(t *testing.T) {
	ctx := context.Background()
	m := bucketNameSemanticEqualPlanModifier{}

	runModifier := func(stateValue, planValue types.String) types.String {
		req := planmodifier.StringRequest{
			StateValue: stateValue,
			PlanValue:  planValue,
		}
		resp := &planmodifier.StringResponse{PlanValue: planValue}
		m.PlanModifyString(ctx, req, resp)
		return resp.PlanValue
	}

	t.Run("bare state, gs:// config - same GCP bucket - keeps state (no replace)", func(t *testing.T) {
		got := runModifier(types.StringValue("my-bucket"), types.StringValue("gs://my-bucket"))
		if !got.Equal(types.StringValue("my-bucket")) {
			t.Errorf("PlanValue = %v, want unchanged state value %q", got, "my-bucket")
		}
	})

	t.Run("gs:// state, bare config - same GCP bucket - keeps state (no replace)", func(t *testing.T) {
		got := runModifier(types.StringValue("gs://my-bucket"), types.StringValue("my-bucket"))
		if !got.Equal(types.StringValue("gs://my-bucket")) {
			t.Errorf("PlanValue = %v, want unchanged state value %q", got, "gs://my-bucket")
		}
	})

	t.Run("bare state, s3:// config - same AWS bucket - keeps state (no replace)", func(t *testing.T) {
		got := runModifier(types.StringValue("my-bucket"), types.StringValue("s3://my-bucket"))
		if !got.Equal(types.StringValue("my-bucket")) {
			t.Errorf("PlanValue = %v, want unchanged state value %q", got, "my-bucket")
		}
	})

	t.Run("genuinely different bucket names - does not mask a real change", func(t *testing.T) {
		got := runModifier(types.StringValue("my-bucket"), types.StringValue("gs://a-totally-different-bucket"))
		if !got.Equal(types.StringValue("gs://a-totally-different-bucket")) {
			t.Errorf("PlanValue = %v, want the new planned value %q (a real change must still show)", got, "gs://a-totally-different-bucket")
		}
	})

	t.Run("identical values - no-op, still equal", func(t *testing.T) {
		got := runModifier(types.StringValue("gs://my-bucket"), types.StringValue("gs://my-bucket"))
		if !got.Equal(types.StringValue("gs://my-bucket")) {
			t.Errorf("PlanValue = %v, want %q", got, "gs://my-bucket")
		}
	})

	t.Run("null/unknown state or plan - no-op, does not panic", func(t *testing.T) {
		got := runModifier(types.StringNull(), types.StringValue("gs://my-bucket"))
		if !got.Equal(types.StringValue("gs://my-bucket")) {
			t.Errorf("PlanValue = %v, want the plan value unchanged when state is null", got)
		}
		got = runModifier(types.StringValue("my-bucket"), types.StringUnknown())
		if !got.Equal(types.StringUnknown()) {
			t.Errorf("PlanValue = %v, want unknown preserved", got)
		}
	})
}

func TestStripAnyBucketSchemePrefix(t *testing.T) {
	cases := map[string]string{
		"my-bucket":                        "my-bucket",
		"gs://my-bucket":                   "my-bucket",
		"s3://my-bucket":                   "my-bucket",
		"abfss://c@a.dfs.core.windows.net": "abfss://c@a.dfs.core.windows.net", // Azure: no bare form, left untouched
	}
	for input, want := range cases {
		if got := stripAnyBucketSchemePrefix(input); got != want {
			t.Errorf("stripAnyBucketSchemePrefix(%q) = %q, want %q", input, got, want)
		}
	}
}
