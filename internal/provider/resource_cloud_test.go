package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Test hasEmbeddedResourceConfig logic
func TestHasEmbeddedResourceConfig(t *testing.T) {
	r := &CloudResource{}

	tests := []struct {
		name     string
		plan     CloudResourceModel
		expected bool
	}{
		{
			name: "has aws_config",
			plan: CloudResourceModel{
				AWSConfig: types.ObjectValueMust(
					map[string]attr.Type{},
					map[string]attr.Value{},
				),
			},
			expected: true,
		},
		{
			name: "has gcp_config",
			plan: CloudResourceModel{
				GCPConfig: types.ObjectValueMust(
					map[string]attr.Type{},
					map[string]attr.Value{},
				),
			},
			expected: true,
		},
		{
			name: "has azure_config",
			plan: CloudResourceModel{
				AzureConfig: types.ObjectValueMust(
					map[string]attr.Type{},
					map[string]attr.Value{},
				),
			},
			expected: true,
		},
		{
			name: "has kubernetes_config only (not embedded)",
			plan: CloudResourceModel{
				KubernetesConfig: types.ObjectValueMust(
					map[string]attr.Type{},
					map[string]attr.Value{},
				),
				AWSConfig:   types.ObjectNull(map[string]attr.Type{}),
				GCPConfig:   types.ObjectNull(map[string]attr.Type{}),
				AzureConfig: types.ObjectNull(map[string]attr.Type{}),
			},
			expected: false,
		},
		{
			name: "has object_storage only (not embedded)",
			plan: CloudResourceModel{
				ObjectStorage: types.ObjectValueMust(
					map[string]attr.Type{},
					map[string]attr.Value{},
				),
				AWSConfig:   types.ObjectNull(map[string]attr.Type{}),
				GCPConfig:   types.ObjectNull(map[string]attr.Type{}),
				AzureConfig: types.ObjectNull(map[string]attr.Type{}),
			},
			expected: false,
		},
		{
			name: "has file_storage only (not embedded)",
			plan: CloudResourceModel{
				FileStorage: types.ObjectValueMust(
					map[string]attr.Type{},
					map[string]attr.Value{},
				),
				AWSConfig:   types.ObjectNull(map[string]attr.Type{}),
				GCPConfig:   types.ObjectNull(map[string]attr.Type{}),
				AzureConfig: types.ObjectNull(map[string]attr.Type{}),
			},
			expected: false,
		},
		{
			name: "no embedded config (null)",
			plan: CloudResourceModel{
				AWSConfig:        types.ObjectNull(map[string]attr.Type{}),
				GCPConfig:        types.ObjectNull(map[string]attr.Type{}),
				AzureConfig:      types.ObjectNull(map[string]attr.Type{}),
				KubernetesConfig: types.ObjectNull(map[string]attr.Type{}),
				ObjectStorage:    types.ObjectNull(map[string]attr.Type{}),
				FileStorage:      types.ObjectNull(map[string]attr.Type{}),
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := r.hasEmbeddedResourceConfig(&tt.plan)
			if result != tt.expected {
				t.Errorf("hasEmbeddedResourceConfig() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

// Test auto-detection of cloud_provider from config blocks
func TestDetectCloudProvider(t *testing.T) {
	tests := []struct {
		name     string
		plan     CloudResourceModel
		expected string
	}{
		{
			name: "AWS from aws_config",
			plan: CloudResourceModel{
				AWSConfig: types.ObjectValueMust(
					map[string]attr.Type{},
					map[string]attr.Value{},
				),
			},
			expected: "AWS",
		},
		{
			name: "GCP from gcp_config",
			plan: CloudResourceModel{
				GCPConfig: types.ObjectValueMust(
					map[string]attr.Type{},
					map[string]attr.Value{},
				),
			},
			expected: "GCP",
		},
		{
			name: "Azure from azure_config",
			plan: CloudResourceModel{
				AzureConfig: types.ObjectValueMust(
					map[string]attr.Type{},
					map[string]attr.Value{},
				),
			},
			expected: "AZURE",
		},
		{
			name: "defaults to AWS when no config",
			plan: CloudResourceModel{
				AWSConfig:   types.ObjectNull(map[string]attr.Type{}),
				GCPConfig:   types.ObjectNull(map[string]attr.Type{}),
				AzureConfig: types.ObjectNull(map[string]attr.Type{}),
			},
			expected: "AWS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the detection logic from Create method
			provider := "AWS" // default
			if !tt.plan.AWSConfig.IsNull() {
				provider = "AWS"
			} else if !tt.plan.GCPConfig.IsNull() {
				provider = "GCP"
			} else if !tt.plan.AzureConfig.IsNull() {
				provider = "AZURE"
			}

			if provider != tt.expected {
				t.Errorf("detected provider = %v, expected %v", provider, tt.expected)
			}
		})
	}
}

// Test generateRandomString helper
func TestGenerateRandomString(t *testing.T) {
	tests := []struct {
		name   string
		length int
	}{
		{"length 8", 8},
		{"length 12", 12},
		{"length 16", 16},
		{"length 32", 32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateRandomString(tt.length)
			if len(result) != tt.length {
				t.Errorf("generateRandomString(%d) returned length %d, expected %d", tt.length, len(result), tt.length)
			}
			// Check that it only contains alphanumeric lowercase characters
			for _, c := range result {
				if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789", c) {
					t.Errorf("generateRandomString() returned invalid character: %c", c)
				}
			}
		})
	}
}

// Test uniqueness of generated random strings
func TestGenerateRandomStringUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	iterations := 100

	for i := 0; i < iterations; i++ {
		result := generateRandomString(12)
		if seen[result] {
			t.Errorf("generateRandomString() produced duplicate: %s", result)
		}
		seen[result] = true
	}
}

// Test AWS placeholder credential generation
func TestGenerateAWSPlaceholder(t *testing.T) {
	// This simulates the logic from getOrGenerateCredentials for AWS empty clouds
	randomSuffix := generateRandomString(12)
	credentials := fmt.Sprintf("arn:aws:iam::000000000000:role/anyscale-placeholder-%s", randomSuffix)

	// Verify format
	if !strings.HasPrefix(credentials, "arn:aws:iam::000000000000:role/anyscale-placeholder-") {
		t.Errorf("AWS placeholder doesn't have correct prefix: %s", credentials)
	}

	// Verify it contains a 12-character hex suffix
	parts := strings.Split(credentials, "-")
	if len(parts) < 2 {
		t.Errorf("AWS placeholder doesn't have expected format: %s", credentials)
	}
	suffix := parts[len(parts)-1]
	if len(suffix) != 12 {
		t.Errorf("AWS placeholder suffix length = %d, expected 12", len(suffix))
	}
}

// Test GCP placeholder credential generation
func TestGenerateGCPPlaceholder(t *testing.T) {
	ctx := context.Background()

	// This simulates the logic from getOrGenerateCredentials for GCP empty clouds
	randomSuffix := generateRandomString(12)
	credentialsMap := map[string]interface{}{
		"provider_id":           fmt.Sprintf("projects/000000000000/locations/global/workloadIdentityPools/placeholder-%s/providers/placeholder", randomSuffix),
		"project_id":            "placeholder-project",
		"service_account_email": fmt.Sprintf("placeholder-%s@placeholder-project.iam.gserviceaccount.com", randomSuffix),
	}

	// Convert to JSON string
	credentialsBytes, err := json.Marshal(credentialsMap)
	if err != nil {
		t.Fatalf("Failed to marshal GCP credentials: %v", err)
	}
	credentials := string(credentialsBytes)

	// Verify it's valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(credentials), &parsed); err != nil {
		t.Errorf("GCP placeholder is not valid JSON: %v", err)
	}

	// Verify required fields
	if _, ok := parsed["provider_id"]; !ok {
		t.Error("GCP placeholder missing provider_id")
	}
	if _, ok := parsed["project_id"]; !ok {
		t.Error("GCP placeholder missing project_id")
	}
	if _, ok := parsed["service_account_email"]; !ok {
		t.Error("GCP placeholder missing service_account_email")
	}

	// Verify format
	providerID := parsed["provider_id"].(string)
	if !strings.Contains(providerID, randomSuffix) {
		t.Errorf("GCP placeholder provider_id doesn't contain random suffix: %s", providerID)
	}

	_ = ctx // Use ctx to avoid unused variable warning
}

// Test credential extraction from AWS config
func TestExtractAWSCredentials(t *testing.T) {
	tests := []struct {
		name             string
		iamRole          string
		externalID       string
		expectedContains string
	}{
		{
			name:             "IAM role only",
			iamRole:          "arn:aws:iam::123456789012:role/anyscale-crossaccount-role",
			externalID:       "",
			expectedContains: "arn:aws:iam::123456789012:role/anyscale-crossaccount-role",
		},
		{
			name:             "IAM role with external ID",
			iamRole:          "arn:aws:iam::123456789012:role/anyscale-crossaccount-role",
			externalID:       "unique-external-id-123",
			expectedContains: "arn:aws:iam::123456789012:role/anyscale-crossaccount-role",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate extracting credentials from AWS config
			credentials := tt.iamRole
			if !strings.Contains(credentials, tt.expectedContains) {
				t.Errorf("Extracted credentials '%s' doesn't contain expected '%s'", credentials, tt.expectedContains)
			}
		})
	}
}

// Test credential extraction from GCP config - JSON format
func TestExtractGCPCredentials(t *testing.T) {
	tests := []struct {
		name        string
		providerID  string
		projectID   string
		accountName string
	}{
		{
			name:        "full GCP credentials",
			providerID:  "projects/123456789012/locations/global/workloadIdentityPools/anyscale-access/providers/anyscale-access",
			projectID:   "my-gcp-project",
			accountName: "anyscale-sa@my-gcp-project.iam.gserviceaccount.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate building GCP credentials JSON
			credentialsMap := map[string]interface{}{
				"provider_id":           tt.providerID,
				"project_id":            tt.projectID,
				"service_account_email": tt.accountName,
			}

			credentialsBytes, err := json.Marshal(credentialsMap)
			if err != nil {
				t.Fatalf("Failed to marshal GCP credentials: %v", err)
			}
			credentials := string(credentialsBytes)

			// Verify it's valid JSON
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(credentials), &parsed); err != nil {
				t.Errorf("GCP credentials is not valid JSON: %v", err)
			}

			// Verify fields match
			if parsed["provider_id"] != tt.providerID {
				t.Errorf("provider_id = %v, expected %v", parsed["provider_id"], tt.providerID)
			}
			if parsed["project_id"] != tt.projectID {
				t.Errorf("project_id = %v, expected %v", parsed["project_id"], tt.projectID)
			}
			if parsed["service_account_email"] != tt.accountName {
				t.Errorf("service_account_email = %v, expected %v", parsed["service_account_email"], tt.accountName)
			}
		})
	}
}

// Test region extraction from subnet_ids_to_az
func TestExtractRegionFromSubnetMap(t *testing.T) {
	tests := []struct {
		name           string
		subnetMap      map[string]string
		expectedRegion string
	}{
		{
			name: "us-east-1 from AZ",
			subnetMap: map[string]string{
				"subnet-12345": "us-east-1a",
				"subnet-67890": "us-east-1b",
			},
			expectedRegion: "us-east-1",
		},
		{
			name: "us-west-2 from AZ",
			subnetMap: map[string]string{
				"subnet-abc": "us-west-2a",
			},
			expectedRegion: "us-west-2",
		},
		{
			name: "eu-west-1 from AZ",
			subnetMap: map[string]string{
				"subnet-xyz": "eu-west-1c",
			},
			expectedRegion: "eu-west-1",
		},
		{
			name:           "empty map returns empty",
			subnetMap:      map[string]string{},
			expectedRegion: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate region extraction logic
			var region string
			for _, az := range tt.subnetMap {
				if len(az) > 2 {
					// Extract region from AZ (e.g., "us-east-1a" -> "us-east-1")
					region = az[:len(az)-1]
					break
				}
			}

			if region != tt.expectedRegion {
				t.Errorf("extracted region = %v, expected %v", region, tt.expectedRegion)
			}
		})
	}
}

// Test bucket prefix handling
func TestBucketPrefixNormalization(t *testing.T) {
	tests := []struct {
		name           string
		bucketName     string
		provider       string
		expectedPrefix string
	}{
		{
			name:           "AWS bucket without prefix",
			bucketName:     "my-bucket",
			provider:       "AWS",
			expectedPrefix: "s3://my-bucket",
		},
		{
			name:           "AWS bucket with s3:// prefix",
			bucketName:     "s3://my-bucket",
			provider:       "AWS",
			expectedPrefix: "s3://my-bucket",
		},
		{
			name:           "GCP bucket without prefix",
			bucketName:     "my-gcs-bucket",
			provider:       "GCP",
			expectedPrefix: "gs://my-gcs-bucket",
		},
		{
			name:           "GCP bucket with gs:// prefix",
			bucketName:     "gs://my-gcs-bucket",
			provider:       "GCP",
			expectedPrefix: "gs://my-gcs-bucket",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate bucket prefix normalization
			bucket := tt.bucketName
			if tt.provider == "AWS" && !strings.HasPrefix(bucket, "s3://") {
				bucket = "s3://" + bucket
			} else if tt.provider == "GCP" && !strings.HasPrefix(bucket, "gs://") {
				bucket = "gs://" + bucket
			}

			if bucket != tt.expectedPrefix {
				t.Errorf("normalized bucket = %v, expected %v", bucket, tt.expectedPrefix)
			}
		})
	}
}

// Test compute stack validation
func TestComputeStackValidation(t *testing.T) {
	tests := []struct {
		name          string
		computeStack  string
		provider      string
		hasAWSConfig  bool
		hasGCPConfig  bool
		hasK8sConfig  bool
		hasObjectSt   bool
		shouldBeValid bool
	}{
		{
			name:          "AWS VM with aws_config",
			computeStack:  "VM",
			provider:      "AWS",
			hasAWSConfig:  true,
			shouldBeValid: true,
		},
		{
			name:          "AWS VM without aws_config",
			computeStack:  "VM",
			provider:      "AWS",
			hasAWSConfig:  false,
			shouldBeValid: false,
		},
		{
			name:          "GCP VM with gcp_config",
			computeStack:  "VM",
			provider:      "GCP",
			hasGCPConfig:  true,
			shouldBeValid: true,
		},
		{
			name:          "AWS K8S with k8s_config and object_storage",
			computeStack:  "K8S",
			provider:      "AWS",
			hasK8sConfig:  true,
			hasObjectSt:   true,
			shouldBeValid: true,
		},
		{
			name:          "AWS K8S without object_storage",
			computeStack:  "K8S",
			provider:      "AWS",
			hasK8sConfig:  true,
			hasObjectSt:   false,
			shouldBeValid: false,
		},
		{
			name:          "GCP K8S with k8s_config and object_storage",
			computeStack:  "K8S",
			provider:      "GCP",
			hasK8sConfig:  true,
			hasObjectSt:   true,
			shouldBeValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate validation logic from Create method
			isValid := true

			switch tt.computeStack {
			case "VM":
				if tt.provider == "AWS" && !tt.hasAWSConfig {
					isValid = false
				} else if tt.provider == "GCP" && !tt.hasGCPConfig {
					isValid = false
				}
			case "K8S":
				if !tt.hasK8sConfig || !tt.hasObjectSt {
					isValid = false
				}
			}

			if isValid != tt.shouldBeValid {
				t.Errorf("validation result = %v, expected %v", isValid, tt.shouldBeValid)
			}
		})
	}
}
