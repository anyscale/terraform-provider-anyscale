package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
			// Regression test for F2/C12: aws_config/gcp_config are optional
			// for K8S clouds (see addCloudResource), so kubernetes_config
			// alone is a valid, complete all-in-one config - it must count as
			// embedded. Before C12 this asserted false, which is the exact
			// bug that misclassified K8S-only clouds as empty, skipped
			// addCloudResource entirely, and surfaced as "Provider produced
			// inconsistent result after apply: .compute_stack: was K8S, but
			// now VM" (F2).
			name: "has kubernetes_config only (embedded - K8S needs no aws/gcp_config)",
			plan: CloudResourceModel{
				KubernetesConfig: types.ObjectValueMust(
					map[string]attr.Type{},
					map[string]attr.Value{},
				),
				AWSConfig:   types.ObjectNull(map[string]attr.Type{}),
				GCPConfig:   types.ObjectNull(map[string]attr.Type{}),
				AzureConfig: types.ObjectNull(map[string]attr.Type{}),
			},
			expected: true,
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

// TestRegionRequiredForCreateError is a regression test for C13: a K8S-only
// all-in-one cloud (no aws_config, so no subnet-based inference, and no
// longer treated as empty since C12) with no explicit region would otherwise
// reach addCloudResource with region="" - a confusing API-level failure
// instead of a clear provider-level one.
func TestRegionRequiredForCreateError(t *testing.T) {
	if summary, detail, hasError := regionRequiredForCreateError(""); !hasError || summary == "" || detail == "" {
		t.Errorf("regionRequiredForCreateError(\"\") = (%q, %q, %v), want a non-empty error", summary, detail, hasError)
	}
	if summary, detail, hasError := regionRequiredForCreateError("us-east-1"); hasError || summary != "" || detail != "" {
		t.Errorf("regionRequiredForCreateError(\"us-east-1\") = (%q, %q, %v), want no error", summary, detail, hasError)
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

// TestGetOrGenerateCredentials_WasPlaceholderSignal is a regression test for
// C9: getOrGenerateCredentials used to fabricate a placeholder credential
// with no signal to the caller at all, so a broken all-in-one cloud (config
// present, credential un-derivable) applied silently. wasPlaceholder must
// distinguish "fabricated" from "real/derived" so the caller (Create) can
// warn only for the suspicious case and stay silent for a genuinely empty
// cloud.
func TestGetOrGenerateCredentials_WasPlaceholderSignal(t *testing.T) {
	ctx := context.Background()
	r := &CloudResource{}

	t.Run("explicit credentials: not a placeholder", func(t *testing.T) {
		plan := &CloudResourceModel{Credentials: types.StringValue("arn:aws:iam::123:role/real")}
		creds, wasPlaceholder, err := r.getOrGenerateCredentials(ctx, plan, "AWS", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if wasPlaceholder {
			t.Error("wasPlaceholder = true, want false - explicit credentials were provided")
		}
		if creds != "arn:aws:iam::123:role/real" {
			t.Errorf("creds = %v, want the explicit value", creds)
		}
	})

	t.Run("derived from aws_config: not a placeholder", func(t *testing.T) {
		awsObj, diags := flattenAWSConfig(ctx, &AWSConfig{AnyscaleIAMRoleID: "arn:aws:iam::123:role/derived"})
		if diags.HasError() {
			t.Fatalf("failed to build test aws_config: %v", diags)
		}
		plan := &CloudResourceModel{Credentials: types.StringNull(), AWSConfig: awsObj}
		creds, wasPlaceholder, err := r.getOrGenerateCredentials(ctx, plan, "AWS", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if wasPlaceholder {
			t.Error("wasPlaceholder = true, want false - a credential was derivable from aws_config")
		}
		if creds != "arn:aws:iam::123:role/derived" {
			t.Errorf("creds = %v, want the derived value", creds)
		}
	})

	t.Run("all-in-one with config present but no derivable role: placeholder AND suspicious", func(t *testing.T) {
		// aws_config is present (all-in-one, not empty cloud) but its
		// controlplane_iam_role_arn was left unset - exactly the
		// forgot-the-role case C9 exists to catch.
		awsObj, diags := flattenAWSConfig(ctx, &AWSConfig{VPCID: "vpc-123"})
		if diags.HasError() {
			t.Fatalf("failed to build test aws_config: %v", diags)
		}
		plan := &CloudResourceModel{Credentials: types.StringNull(), AWSConfig: awsObj}
		_, wasPlaceholder, err := r.getOrGenerateCredentials(ctx, plan, "AWS", false /* isEmptyCloud */)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !wasPlaceholder {
			t.Error("wasPlaceholder = false, want true - no role was derivable, a placeholder must have been generated")
		}
		// The caller decides whether to warn using wasPlaceholder && !isEmptyCloud;
		// this case has isEmptyCloud=false, so the caller WOULD warn - verified
		// separately, this test only pins the signal itself.
	})

	t.Run("pure empty cloud: placeholder but expected, not suspicious", func(t *testing.T) {
		plan := &CloudResourceModel{Credentials: types.StringNull(), AWSConfig: types.ObjectNull(awsConfigAttrTypes())}
		_, wasPlaceholder, err := r.getOrGenerateCredentials(ctx, plan, "AWS", true /* isEmptyCloud */)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !wasPlaceholder {
			t.Error("wasPlaceholder = false, want true - no config and no explicit credentials means a placeholder is generated")
		}
		// Again: the caller's isEmptyCloud=true here means it stays silent
		// despite wasPlaceholder=true - this is the BYOC/multi-resource cloud pattern case.
	})
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

// TestReadCloudState_ComputeStackFromDefaultResource is the regression proof
// for the user-reported import bug: GET /clouds/{id}'s own compute_stack is
// backend-derived and can disagree with the cloud's actual primary resource
// (defaulting to VM when the backend doesn't recognize one) - readCloudState
// must prefer the default resource's own compute_stack, the same source
// requiredImportConfigBlocks already trusts, falling back to the cloud-level
// field only when there is no default resource at all.
func TestReadCloudState_ComputeStackFromDefaultResource(t *testing.T) {
	ctx := context.Background()

	t.Run("cloud-level says VM, default resource says K8S - resource wins", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v2/clouds/cld_test":
				_ = json.NewEncoder(w).Encode(CloudResponse{Result: CloudResult{
					ID: "cld_test", Name: "test-cloud", Provider: "AWS", Region: "us-east-2",
					ComputeStack: "VM", // cloud-level derivation disagrees with the real resource below
				}})
			case "/api/v2/clouds/cld_test/resources":
				_ = json.NewEncoder(w).Encode(CloudDeploymentsResponse{Results: []CloudDeploymentResult{
					{IsDefault: true, ComputeStack: "K8S", CloudDeploymentID: "dep_1"},
				}})
			default:
				t.Errorf("unexpected request: %s", r.URL.Path)
			}
		}))
		defer server.Close()

		r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
		state := &CloudResourceModel{}
		if err := r.readCloudState(ctx, "cld_test", state); err != nil {
			t.Fatalf("readCloudState returned error: %v", err)
		}

		if got := state.ComputeStack.ValueString(); got != "K8S" {
			t.Errorf("ComputeStack = %q, want %q (from the default resource, not the cloud-level VM default)", got, "K8S")
		}
	})

	t.Run("genuinely empty cloud, zero resources - falls back to cloud-level field", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v2/clouds/cld_empty":
				_ = json.NewEncoder(w).Encode(CloudResponse{Result: CloudResult{
					ID: "cld_empty", Name: "empty-cloud", Provider: "AWS", Region: "us-east-2",
					ComputeStack: "VM",
				}})
			case "/api/v2/clouds/cld_empty/resources":
				_ = json.NewEncoder(w).Encode(CloudDeploymentsResponse{Results: []CloudDeploymentResult{}})
			default:
				t.Errorf("unexpected request: %s", r.URL.Path)
			}
		}))
		defer server.Close()

		r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
		state := &CloudResourceModel{}
		if err := r.readCloudState(ctx, "cld_empty", state); err != nil {
			t.Fatalf("readCloudState returned error: %v", err)
		}

		if got := state.ComputeStack.ValueString(); got != "VM" {
			t.Errorf("ComputeStack = %q, want %q (no default resource to consult, cloud-level fallback is correct)", got, "VM")
		}
	})

	t.Run("resources list call fails - falls back to cloud-level field, does not error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v2/clouds/cld_fail":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(CloudResponse{Result: CloudResult{
					ID: "cld_fail", Name: "fail-cloud", Provider: "AWS", Region: "us-east-2",
					ComputeStack: "K8S",
				}})
			case "/api/v2/clouds/cld_fail/resources":
				w.WriteHeader(http.StatusInternalServerError)
			default:
				t.Errorf("unexpected request: %s", r.URL.Path)
			}
		}))
		defer server.Close()

		r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
		state := &CloudResourceModel{}
		if err := r.readCloudState(ctx, "cld_fail", state); err != nil {
			t.Fatalf("readCloudState returned error: %v", err)
		}

		if got := state.ComputeStack.ValueString(); got != "K8S" {
			t.Errorf("ComputeStack = %q, want %q (resources call failed, must still fall back to the cloud-level value)", got, "K8S")
		}
	})

	// This is the exact shape architect flagged in re-review: the user's real
	// repro was a single EKS resource registered via the CLI (never
	// Terraform-created) then cold-imported. If that resource's is_default
	// flag is not true - unconfirmed either way against the real backend,
	// hence hardening rather than assuming - the original fix would silently
	// fall through to the cloud-level VM default and the user's bug would
	// persist even with that fix in place.
	t.Run("single resource, none flagged is_default (cold CLI-created import) - uses the sole resource anyway", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v2/clouds/cld_cli":
				_ = json.NewEncoder(w).Encode(CloudResponse{Result: CloudResult{
					ID: "cld_cli", Name: "cli-cloud", Provider: "AWS", Region: "us-east-2",
					ComputeStack: "VM", // cloud-level derivation flips to VM, same as the user's report
				}})
			case "/api/v2/clouds/cld_cli/resources":
				_ = json.NewEncoder(w).Encode(CloudDeploymentsResponse{Results: []CloudDeploymentResult{
					{IsDefault: false, ComputeStack: "K8S", CloudDeploymentID: "dep_cli"}, // NOT flagged default
				}})
			default:
				t.Errorf("unexpected request: %s", r.URL.Path)
			}
		}))
		defer server.Close()

		r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
		state := &CloudResourceModel{}
		if err := r.readCloudState(ctx, "cld_cli", state); err != nil {
			t.Fatalf("readCloudState returned error: %v", err)
		}

		if got := state.ComputeStack.ValueString(); got != "K8S" {
			t.Errorf("ComputeStack = %q, want %q (exactly one resource exists - no ambiguity, must be used regardless of is_default)", got, "K8S")
		}
	})

	t.Run("multiple resources, none flagged is_default - genuinely ambiguous, falls back to cloud-level (no guessing)", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v2/clouds/cld_multi":
				_ = json.NewEncoder(w).Encode(CloudResponse{Result: CloudResult{
					ID: "cld_multi", Name: "multi-cloud", Provider: "AWS", Region: "us-east-2",
					ComputeStack: "VM",
				}})
			case "/api/v2/clouds/cld_multi/resources":
				_ = json.NewEncoder(w).Encode(CloudDeploymentsResponse{Results: []CloudDeploymentResult{
					{IsDefault: false, ComputeStack: "K8S", CloudDeploymentID: "dep_a"},
					{IsDefault: false, ComputeStack: "VM", CloudDeploymentID: "dep_b"},
				}})
			default:
				t.Errorf("unexpected request: %s", r.URL.Path)
			}
		}))
		defer server.Close()

		r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
		state := &CloudResourceModel{}
		if err := r.readCloudState(ctx, "cld_multi", state); err != nil {
			t.Fatalf("readCloudState returned error: %v", err)
		}

		if got := state.ComputeStack.ValueString(); got != "VM" {
			t.Errorf("ComputeStack = %q, want %q (genuinely ambiguous with 2+ resources and no default flagged - must not guess which one, cloud-level fallback is correct here)", got, "VM")
		}
	})
}
