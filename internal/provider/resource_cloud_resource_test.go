package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestResourceCloudResourceSchema(t *testing.T) {
	s := ResourceCloudResource().Schema

	// Test required fields
	requiredFields := []string{"cloud_id", "region", "compute_stack"}
	for _, field := range requiredFields {
		if _, ok := s[field]; !ok {
			t.Errorf("Expected schema to have field %s", field)
			continue
		}
		if !s[field].Required {
			t.Errorf("Expected field %s to be required", field)
		}
	}

	// Test optional fields
	optionalFields := []string{"name", "is_private", "aws_config", "gcp_config", "object_storage", "file_storage"}
	for _, field := range optionalFields {
		if _, ok := s[field]; !ok {
			t.Errorf("Expected schema to have field %s", field)
			continue
		}
		if s[field].Required {
			t.Errorf("Expected field %s to be optional", field)
		}
	}

	// Test computed fields
	computedFields := []string{"cloud_resource_id", "cloud_deployment_id", "status", "is_default"}
	for _, field := range computedFields {
		if _, ok := s[field]; !ok {
			t.Errorf("Expected schema to have field %s", field)
			continue
		}
		if !s[field].Computed {
			t.Errorf("Expected field %s to be computed", field)
		}
	}

	// Test ForceNew fields
	forceNewFields := []string{"cloud_id", "name", "compute_stack", "region"}
	for _, field := range forceNewFields {
		if _, ok := s[field]; !ok {
			t.Errorf("Expected schema to have field %s", field)
			continue
		}
		if !s[field].ForceNew {
			t.Errorf("Expected field %s to have ForceNew", field)
		}
	}
}

func TestResourceCloudResourceAWSConfigSchema(t *testing.T) {
	s := ResourceCloudResource().Schema

	awsConfig, ok := s["aws_config"]
	if !ok {
		t.Fatal("Expected schema to have aws_config field")
	}

	if awsConfig.Type != schema.TypeList {
		t.Error("Expected aws_config to be TypeList")
	}

	if awsConfig.MaxItems != 1 {
		t.Error("Expected aws_config MaxItems to be 1")
	}

	// Check nested schema
	elemResource, ok := awsConfig.Elem.(*schema.Resource)
	if !ok {
		t.Fatal("Expected aws_config.Elem to be *schema.Resource")
	}

	requiredAWSFields := []string{"vpc_id", "security_group_ids", "controlplane_iam_role_arn", "dataplane_iam_role_arn"}
	for _, field := range requiredAWSFields {
		if _, ok := elemResource.Schema[field]; !ok {
			t.Errorf("Expected aws_config to have field %s", field)
			continue
		}
		if !elemResource.Schema[field].Required {
			t.Errorf("Expected aws_config.%s to be required", field)
		}
	}

	optionalAWSFields := []string{"subnet_ids", "subnet_ids_to_az", "external_id", "memorydb_cluster_name", "memorydb_cluster_arn", "memorydb_cluster_endpoint"}
	for _, field := range optionalAWSFields {
		if _, ok := elemResource.Schema[field]; !ok {
			t.Errorf("Expected aws_config to have optional field %s", field)
			continue
		}
		if elemResource.Schema[field].Required {
			t.Errorf("Expected aws_config.%s to be optional", field)
		}
	}
}

func TestResourceCloudResourceGCPConfigSchema(t *testing.T) {
	s := ResourceCloudResource().Schema

	gcpConfig, ok := s["gcp_config"]
	if !ok {
		t.Fatal("Expected schema to have gcp_config field")
	}

	if gcpConfig.Type != schema.TypeList {
		t.Error("Expected gcp_config to be TypeList")
	}

	// Check nested schema
	elemResource, ok := gcpConfig.Elem.(*schema.Resource)
	if !ok {
		t.Fatal("Expected gcp_config.Elem to be *schema.Resource")
	}

	requiredGCPFields := []string{"project_id", "provider_name", "vpc_name", "subnet_names", "controlplane_service_account_email", "dataplane_service_account_email"}
	for _, field := range requiredGCPFields {
		if _, ok := elemResource.Schema[field]; !ok {
			t.Errorf("Expected gcp_config to have field %s", field)
			continue
		}
		if !elemResource.Schema[field].Required {
			t.Errorf("Expected gcp_config.%s to be required", field)
		}
	}

	optionalGCPFields := []string{"host_project_id", "firewall_policy_names", "memorystore_instance_name", "memorystore_endpoint"}
	for _, field := range optionalGCPFields {
		if _, ok := elemResource.Schema[field]; !ok {
			t.Errorf("Expected gcp_config to have optional field %s", field)
			continue
		}
		if elemResource.Schema[field].Required {
			t.Errorf("Expected gcp_config.%s to be optional", field)
		}
	}
}

func TestResourceCloudResourceTimeouts(t *testing.T) {
	r := ResourceCloudResource()

	if r.Timeouts == nil {
		t.Fatal("Expected resource to have timeouts")
	}

	if r.Timeouts.Create == nil {
		t.Error("Expected Create timeout to be set")
	}

	if r.Timeouts.Update == nil {
		t.Error("Expected Update timeout to be set")
	}

	if r.Timeouts.Delete == nil {
		t.Error("Expected Delete timeout to be set")
	}
}

func TestParseCloudResourceID(t *testing.T) {
	tests := []struct {
		name         string
		id           string
		wantCloudID  string
		wantResource string
		wantErr      bool
	}{
		{
			name:         "valid ID",
			id:           "cld_123:vm-aws-us-west-2",
			wantCloudID:  "cld_123",
			wantResource: "vm-aws-us-west-2",
			wantErr:      false,
		},
		{
			name:         "valid ID with colons in resource name",
			id:           "cld_abc:resource:with:colons",
			wantCloudID:  "cld_abc",
			wantResource: "resource:with:colons",
			wantErr:      false,
		},
		{
			name:    "invalid ID - no separator",
			id:      "cld_123_vm-aws-us-west-2",
			wantErr: true,
		},
		{
			name:    "invalid ID - empty",
			id:      "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloudID, resourceName, err := parseCloudResourceID(tt.id)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if cloudID != tt.wantCloudID {
				t.Errorf("cloudID = %s, want %s", cloudID, tt.wantCloudID)
			}

			if resourceName != tt.wantResource {
				t.Errorf("resourceName = %s, want %s", resourceName, tt.wantResource)
			}
		})
	}
}

func TestGenerateResourceName(t *testing.T) {
	tests := []struct {
		computeStack string
		provider     string
		region       string
		want         string
	}{
		{"VM", "AWS", "us-west-2", "vm-aws-us-west-2"},
		{"K8S", "GCP", "us-central1", "k8s-gcp-us-central1"},
		{"vm", "aws", "US-EAST-1", "vm-aws-us-east-1"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := generateResourceName(tt.computeStack, tt.provider, tt.region)
			if got != tt.want {
				t.Errorf("generateResourceName(%s, %s, %s) = %s, want %s",
					tt.computeStack, tt.provider, tt.region, got, tt.want)
			}
		})
	}
}

func TestGetNetworkingModeFromResource(t *testing.T) {
	tests := []struct {
		name      string
		isPrivate bool
		want      string
	}{
		{"public", false, "PUBLIC"},
		{"private", true, "PRIVATE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := schema.TestResourceDataRaw(t, ResourceCloudResource().Schema, map[string]any{
				"cloud_id":   "cld_123",
				"region":     "us-west-2",
				"is_private": tt.isPrivate,
			})

			got := getNetworkingModeFromResource(d)
			if got != tt.want {
				t.Errorf("getNetworkingModeFromResource() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGetProviderFromResourceData(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
		want   string
	}{
		{
			name: "AWS config",
			config: map[string]any{
				"cloud_id": "cld_123",
				"region":   "us-west-2",
				"aws_config": []any{
					map[string]any{
						"vpc_id":                    "vpc-123",
						"security_group_ids":        []any{"sg-123"},
						"controlplane_iam_role_arn": "arn:aws:iam::123:role/control",
						"dataplane_iam_role_arn":    "arn:aws:iam::123:role/data",
					},
				},
			},
			want: "AWS",
		},
		{
			name: "GCP config",
			config: map[string]any{
				"cloud_id": "cld_123",
				"region":   "us-central1",
				"gcp_config": []any{
					map[string]any{
						"project_id":                          "my-project",
						"provider_name":                       "projects/123/providers/p",
						"vpc_name":                            "my-vpc",
						"subnet_names":                        []any{"subnet-1"},
						"controlplane_service_account_email":  "control@proj.iam",
						"dataplane_service_account_email":     "data@proj.iam",
					},
				},
			},
			want: "GCP",
		},
		{
			name: "no config",
			config: map[string]any{
				"cloud_id": "cld_123",
				"region":   "us-west-2",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := schema.TestResourceDataRaw(t, ResourceCloudResource().Schema, tt.config)

			got := getProviderFromResourceData(d)
			if got != tt.want {
				t.Errorf("getProviderFromResourceData() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestHasEmbeddedResourceConfig(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
		want   bool
	}{
		{
			name: "has AWS config",
			config: map[string]any{
				"name":           "test-cloud",
				"cloud_provider": "AWS",
				"region":         "us-west-2",
				"aws_config": []any{
					map[string]any{
						"vpc_id":                    "vpc-123",
						"security_group_ids":        []any{"sg-123"},
						"controlplane_iam_role_arn": "arn:aws:iam::123:role/control",
						"dataplane_iam_role_arn":    "arn:aws:iam::123:role/data",
					},
				},
			},
			want: true,
		},
		{
			name: "has object_storage only",
			config: map[string]any{
				"name":           "test-cloud",
				"cloud_provider": "AWS",
				"region":         "us-west-2",
				"object_storage": []any{
					map[string]any{
						"bucket_name": "my-bucket",
					},
				},
			},
			want: true,
		},
		{
			name: "empty cloud - no config blocks",
			config: map[string]any{
				"name":           "test-cloud",
				"cloud_provider": "AWS",
				"region":         "us-west-2",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := schema.TestResourceDataRaw(t, ResourceCloud().Schema, tt.config)

			got := hasEmbeddedResourceConfig(d)
			if got != tt.want {
				t.Errorf("hasEmbeddedResourceConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}
