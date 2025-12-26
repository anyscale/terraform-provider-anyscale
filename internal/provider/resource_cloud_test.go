package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// TestResourceCloudSchema verifies the schema structure
func TestResourceCloudSchema(t *testing.T) {
	s := ResourceCloud().Schema

	// Test required fields
	requiredFields := []string{"name", "cloud_provider", "region"}
	for _, field := range requiredFields {
		if _, ok := s[field]; !ok {
			t.Errorf("expected schema to have field %q", field)
			continue
		}
		if !s[field].Required {
			t.Errorf("expected field %q to be required", field)
		}
	}

	// Test optional fields with defaults
	optionalWithDefaults := map[string]any{
		"compute_stack":    "VM",
		"is_private_cloud": false,
		"auto_add_user":    false,
	}
	for field, expectedDefault := range optionalWithDefaults {
		if _, ok := s[field]; !ok {
			t.Errorf("expected schema to have field %q", field)
			continue
		}
		if s[field].Required {
			t.Errorf("expected field %q to be optional", field)
		}
		if s[field].Default != expectedDefault {
			t.Errorf("expected field %q to have default %v, got %v", field, expectedDefault, s[field].Default)
		}
	}

	// Test computed fields
	computedFields := []string{"cloud_id", "status", "state"}
	for _, field := range computedFields {
		if _, ok := s[field]; !ok {
			t.Errorf("expected schema to have field %q", field)
			continue
		}
		if !s[field].Computed {
			t.Errorf("expected field %q to be computed", field)
		}
	}

	// Test ForceNew fields
	forceNewFields := []string{"cloud_provider", "compute_stack", "region", "is_private_cloud"}
	for _, field := range forceNewFields {
		if _, ok := s[field]; !ok {
			t.Errorf("expected schema to have field %q", field)
			continue
		}
		if !s[field].ForceNew {
			t.Errorf("expected field %q to have ForceNew=true", field)
		}
	}

	// Test nested blocks exist
	nestedBlocks := []string{"aws_config", "gcp_config", "azure_config", "kubernetes_config", "object_storage", "file_storage"}
	for _, block := range nestedBlocks {
		if _, ok := s[block]; !ok {
			t.Errorf("expected schema to have nested block %q", block)
			continue
		}
		if s[block].Type != schema.TypeList {
			t.Errorf("expected block %q to be TypeList", block)
		}
		if s[block].MaxItems != 1 {
			t.Errorf("expected block %q to have MaxItems=1", block)
		}
	}
}

// TestResourceCloudAWSConfigSchema verifies aws_config nested schema
func TestResourceCloudAWSConfigSchema(t *testing.T) {
	s := ResourceCloud().Schema
	awsConfig, ok := s["aws_config"]
	if !ok {
		t.Fatal("expected schema to have aws_config")
	}

	elem, ok := awsConfig.Elem.(*schema.Resource)
	if !ok {
		t.Fatal("expected aws_config.Elem to be *schema.Resource")
	}

	// Test required AWS fields
	requiredAWSFields := []string{"vpc_id", "security_group_ids", "controlplane_iam_role_arn", "dataplane_iam_role_arn"}
	for _, field := range requiredAWSFields {
		if _, ok := elem.Schema[field]; !ok {
			t.Errorf("expected aws_config to have field %q", field)
			continue
		}
		if !elem.Schema[field].Required {
			t.Errorf("expected aws_config.%s to be required", field)
		}
	}

	// Test optional AWS fields
	optionalAWSFields := []string{"external_id", "memorydb_cluster_name", "memorydb_cluster_arn", "memorydb_cluster_endpoint", "subnet_ids_to_az"}
	for _, field := range optionalAWSFields {
		if _, ok := elem.Schema[field]; !ok {
			t.Errorf("expected aws_config to have field %q", field)
			continue
		}
		if elem.Schema[field].Required {
			t.Errorf("expected aws_config.%s to be optional", field)
		}
	}

	// Test list fields
	listFields := []string{"security_group_ids"}
	for _, field := range listFields {
		if elem.Schema[field].Type != schema.TypeList {
			t.Errorf("expected aws_config.%s to be TypeList", field)
		}
	}

	// Test map fields
	if elem.Schema["subnet_ids_to_az"].Type != schema.TypeMap {
		t.Errorf("expected aws_config.subnet_ids_to_az to be TypeMap")
	}
}

// TestResourceCloudObjectStorageSchema verifies object_storage nested schema
func TestResourceCloudObjectStorageSchema(t *testing.T) {
	s := ResourceCloud().Schema
	objStorage, ok := s["object_storage"]
	if !ok {
		t.Fatal("expected schema to have object_storage")
	}

	elem, ok := objStorage.Elem.(*schema.Resource)
	if !ok {
		t.Fatal("expected object_storage.Elem to be *schema.Resource")
	}

	// bucket_name is required
	if !elem.Schema["bucket_name"].Required {
		t.Error("expected object_storage.bucket_name to be required")
	}

	// region and endpoint are optional
	if elem.Schema["region"].Required {
		t.Error("expected object_storage.region to be optional")
	}
	if elem.Schema["endpoint"].Required {
		t.Error("expected object_storage.endpoint to be optional")
	}
}

// TestResourceCloudTimeouts verifies timeout configuration
func TestResourceCloudTimeouts(t *testing.T) {
	r := ResourceCloud()

	if r.Timeouts == nil {
		t.Fatal("expected resource to have timeouts configured")
	}

	if r.Timeouts.Create == nil {
		t.Error("expected Create timeout to be set")
	}
	if r.Timeouts.Update == nil {
		t.Error("expected Update timeout to be set")
	}
	if r.Timeouts.Delete == nil {
		t.Error("expected Delete timeout to be set")
	}
}

// TestGetNetworkingMode tests the networking mode helper
func TestGetNetworkingMode(t *testing.T) {
	tests := []struct {
		name           string
		isPrivateCloud bool
		expected       string
	}{
		{
			name:           "public cloud",
			isPrivateCloud: false,
			expected:       "PUBLIC",
		},
		{
			name:           "private cloud",
			isPrivateCloud: true,
			expected:       "PRIVATE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock ResourceData
			d := schema.TestResourceDataRaw(t, ResourceCloud().Schema, map[string]any{
				"name":             "test-cloud",
				"cloud_provider":   "AWS",
				"region":           "us-west-2",
				"is_private_cloud": tt.isPrivateCloud,
			})

			result := GetNetworkingMode(d)
			if result != tt.expected {
				t.Errorf("GetNetworkingMode() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestExpandAWSConfig tests the AWS config expansion helper
func TestExpandAWSConfig(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected *AWSConfig
	}{
		{
			name: "full aws config",
			input: map[string]any{
				"name":           "test-cloud",
				"cloud_provider": "AWS",
				"region":         "us-west-2",
				"aws_config": []any{
					map[string]any{
						"vpc_id":                    "vpc-123",
						"subnet_ids":                []any{"subnet-1", "subnet-2"},
						"security_group_ids":        []any{"sg-1"},
						"controlplane_iam_role_arn": "arn:aws:iam::123:role/controlplane",
						"dataplane_iam_role_arn":    "arn:aws:iam::123:role/dataplane",
						"external_id":               "ext-123",
					},
				},
			},
			expected: &AWSConfig{
				VPCID:             "vpc-123",
				SubnetIDs:         []string{"subnet-1", "subnet-2"},
				SecurityGroupIDs:  []string{"sg-1"},
				AnyscaleIAMRoleID: "arn:aws:iam::123:role/controlplane",
				ClusterIAMRoleID:  "arn:aws:iam::123:role/dataplane",
				ExternalID:        "ext-123",
			},
		},
		{
			name: "no aws config",
			input: map[string]any{
				"name":           "test-cloud",
				"cloud_provider": "GCP",
				"region":         "us-central1",
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := schema.TestResourceDataRaw(t, ResourceCloud().Schema, tt.input)

			result := ExpandAWSConfig(d)

			if tt.expected == nil {
				if result != nil {
					t.Errorf("ExpandAWSConfig() = %+v, want nil", result)
				}
				return
			}

			if result == nil {
				t.Fatal("ExpandAWSConfig() = nil, want non-nil")
			}

			if result.VPCID != tt.expected.VPCID {
				t.Errorf("VPCID = %q, want %q", result.VPCID, tt.expected.VPCID)
			}
			if result.AnyscaleIAMRoleID != tt.expected.AnyscaleIAMRoleID {
				t.Errorf("AnyscaleIAMRoleID = %q, want %q", result.AnyscaleIAMRoleID, tt.expected.AnyscaleIAMRoleID)
			}
			if result.ClusterIAMRoleID != tt.expected.ClusterIAMRoleID {
				t.Errorf("ClusterIAMRoleID = %q, want %q", result.ClusterIAMRoleID, tt.expected.ClusterIAMRoleID)
			}
			if result.ExternalID != tt.expected.ExternalID {
				t.Errorf("ExternalID = %q, want %q", result.ExternalID, tt.expected.ExternalID)
			}
			if len(result.SubnetIDs) != len(tt.expected.SubnetIDs) {
				t.Errorf("SubnetIDs length = %d, want %d", len(result.SubnetIDs), len(tt.expected.SubnetIDs))
			}
			if len(result.SecurityGroupIDs) != len(tt.expected.SecurityGroupIDs) {
				t.Errorf("SecurityGroupIDs length = %d, want %d", len(result.SecurityGroupIDs), len(tt.expected.SecurityGroupIDs))
			}
		})
	}
}

// TestExpandGCPConfig tests the GCP config expansion helper
func TestExpandGCPConfig(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected *GCPConfig
	}{
		{
			name: "full gcp config",
			input: map[string]any{
				"name":           "test-cloud",
				"cloud_provider": "GCP",
				"region":         "us-central1",
				"gcp_config": []any{
					map[string]any{
						"project_id":                        "my-project",
						"host_project_id":                   "host-project",
						"provider_name":                     "projects/123456789/locations/global/workloadIdentityPools/pool/providers/provider",
						"vpc_name":                          "my-vpc",
						"subnet_names":                      []any{"subnet-1", "subnet-2"},
						"controlplane_service_account_email": "cp-sa@my-project.iam.gserviceaccount.com",
						"dataplane_service_account_email":    "dp-sa@my-project.iam.gserviceaccount.com",
						"firewall_policy_names":              []any{"policy-1"},
						"memorystore_instance_name":          "my-memorystore",
						"memorystore_endpoint":               "10.0.0.1:6379",
					},
				},
			},
			expected: &GCPConfig{
				ProjectID:                   "my-project",
				HostProjectID:               "host-project",
				ProviderName:                "projects/123456789/locations/global/workloadIdentityPools/pool/providers/provider",
				VPCName:                     "my-vpc",
				SubnetNames:                 []string{"subnet-1", "subnet-2"},
				AnyscaleServiceAccountEmail: "cp-sa@my-project.iam.gserviceaccount.com",
				ClusterServiceAccountEmail:  "dp-sa@my-project.iam.gserviceaccount.com",
				FirewallPolicyNames:         []string{"policy-1"},
				MemorystoreInstanceName:     "my-memorystore",
				MemorystoreEndpoint:         "10.0.0.1:6379",
			},
		},
		{
			name: "minimal gcp config",
			input: map[string]any{
				"name":           "test-cloud",
				"cloud_provider": "GCP",
				"region":         "us-central1",
				"gcp_config": []any{
					map[string]any{
						"project_id":                        "my-project",
						"provider_name":                     "projects/123456789/locations/global/workloadIdentityPools/pool/providers/provider",
						"vpc_name":                          "my-vpc",
						"subnet_names":                      []any{"subnet-1"},
						"controlplane_service_account_email": "cp-sa@my-project.iam.gserviceaccount.com",
						"dataplane_service_account_email":    "dp-sa@my-project.iam.gserviceaccount.com",
					},
				},
			},
			expected: &GCPConfig{
				ProjectID:                   "my-project",
				ProviderName:                "projects/123456789/locations/global/workloadIdentityPools/pool/providers/provider",
				VPCName:                     "my-vpc",
				SubnetNames:                 []string{"subnet-1"},
				AnyscaleServiceAccountEmail: "cp-sa@my-project.iam.gserviceaccount.com",
				ClusterServiceAccountEmail:  "dp-sa@my-project.iam.gserviceaccount.com",
			},
		},
		{
			name: "no gcp config",
			input: map[string]any{
				"name":           "test-cloud",
				"cloud_provider": "AWS",
				"region":         "us-west-2",
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := schema.TestResourceDataRaw(t, ResourceCloud().Schema, tt.input)

			result := ExpandGCPConfig(d)

			if tt.expected == nil {
				if result != nil {
					t.Errorf("ExpandGCPConfig() = %+v, want nil", result)
				}
				return
			}

			if result == nil {
				t.Fatal("ExpandGCPConfig() = nil, want non-nil")
			}

			if result.ProjectID != tt.expected.ProjectID {
				t.Errorf("ProjectID = %q, want %q", result.ProjectID, tt.expected.ProjectID)
			}
			if result.HostProjectID != tt.expected.HostProjectID {
				t.Errorf("HostProjectID = %q, want %q", result.HostProjectID, tt.expected.HostProjectID)
			}
			if result.ProviderName != tt.expected.ProviderName {
				t.Errorf("ProviderName = %q, want %q", result.ProviderName, tt.expected.ProviderName)
			}
			if result.VPCName != tt.expected.VPCName {
				t.Errorf("VPCName = %q, want %q", result.VPCName, tt.expected.VPCName)
			}
			if len(result.SubnetNames) != len(tt.expected.SubnetNames) {
				t.Errorf("SubnetNames length = %d, want %d", len(result.SubnetNames), len(tt.expected.SubnetNames))
			}
			if result.AnyscaleServiceAccountEmail != tt.expected.AnyscaleServiceAccountEmail {
				t.Errorf("AnyscaleServiceAccountEmail = %q, want %q", result.AnyscaleServiceAccountEmail, tt.expected.AnyscaleServiceAccountEmail)
			}
			if result.ClusterServiceAccountEmail != tt.expected.ClusterServiceAccountEmail {
				t.Errorf("ClusterServiceAccountEmail = %q, want %q", result.ClusterServiceAccountEmail, tt.expected.ClusterServiceAccountEmail)
			}
			if len(result.FirewallPolicyNames) != len(tt.expected.FirewallPolicyNames) {
				t.Errorf("FirewallPolicyNames length = %d, want %d", len(result.FirewallPolicyNames), len(tt.expected.FirewallPolicyNames))
			}
			if result.MemorystoreInstanceName != tt.expected.MemorystoreInstanceName {
				t.Errorf("MemorystoreInstanceName = %q, want %q", result.MemorystoreInstanceName, tt.expected.MemorystoreInstanceName)
			}
			if result.MemorystoreEndpoint != tt.expected.MemorystoreEndpoint {
				t.Errorf("MemorystoreEndpoint = %q, want %q", result.MemorystoreEndpoint, tt.expected.MemorystoreEndpoint)
			}
		})
	}
}

// TestExpandObjectStorage tests the object storage expansion helper
func TestExpandObjectStorage(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected *ObjectStorage
	}{
		{
			name: "full object storage config",
			input: map[string]any{
				"name":           "test-cloud",
				"cloud_provider": "AWS",
				"region":         "us-west-2",
				"object_storage": []any{
					map[string]any{
						"bucket_name": "my-bucket",
						"region":      "us-west-2",
						"endpoint":    "https://s3.amazonaws.com",
					},
				},
			},
			expected: &ObjectStorage{
				BucketName: "my-bucket",
				Region:     strPtr("us-west-2"),
				Endpoint:   strPtr("https://s3.amazonaws.com"),
			},
		},
		{
			name: "minimal object storage config",
			input: map[string]any{
				"name":           "test-cloud",
				"cloud_provider": "AWS",
				"region":         "us-west-2",
				"object_storage": []any{
					map[string]any{
						"bucket_name": "my-bucket",
					},
				},
			},
			expected: &ObjectStorage{
				BucketName: "my-bucket",
			},
		},
		{
			name: "no object storage",
			input: map[string]any{
				"name":           "test-cloud",
				"cloud_provider": "AWS",
				"region":         "us-west-2",
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := schema.TestResourceDataRaw(t, ResourceCloud().Schema, tt.input)

			result := ExpandObjectStorage(d)

			if tt.expected == nil {
				if result != nil {
					t.Errorf("ExpandObjectStorage() = %+v, want nil", result)
				}
				return
			}

			if result == nil {
				t.Fatal("ExpandObjectStorage() = nil, want non-nil")
			}

			if result.BucketName != tt.expected.BucketName {
				t.Errorf("BucketName = %q, want %q", result.BucketName, tt.expected.BucketName)
			}
		})
	}
}

// Helper function to create string pointer
func strPtr(s string) *string {
	return &s
}
