package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCreateCloudRequestJSON tests JSON marshaling of CreateCloudRequest
func TestCreateCloudRequestJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    CreateCloudRequest
		expected string
	}{
		{
			name: "full request",
			input: CreateCloudRequest{
				Name:     "my-cloud",
				Provider: "AWS",
			},
			expected: `{"name":"my-cloud","provider":"AWS","is_private_cloud":false,"is_private_service_cloud":false}`,
		},
		{
			name: "name only",
			input: CreateCloudRequest{
				Name: "my-cloud",
			},
			expected: `{"name":"my-cloud","is_private_cloud":false,"is_private_service_cloud":false}`,
		},
		{
			// The whole is_private_cloud fix hinges on this field never being
			// omitted - unlike Provider/Region/Credentials above, it must
			// always appear on the wire, true or false, since the backend has
			// no other route to receive it.
			name: "private cloud",
			input: CreateCloudRequest{
				Name:                  "my-cloud",
				Provider:              "AWS",
				IsPrivateCloud:        true,
				IsPrivateServiceCloud: true,
			},
			expected: `{"name":"my-cloud","provider":"AWS","is_private_cloud":true,"is_private_service_cloud":true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if string(data) != tt.expected {
				t.Errorf("json.Marshal() = %s, want %s", string(data), tt.expected)
			}
		})
	}
}

// TestCloudResponseJSON tests JSON unmarshaling of CloudResponse
func TestCloudResponseJSON(t *testing.T) {
	jsonInput := `{
		"result": {
			"id": "cld_123",
			"name": "my-cloud",
			"provider": "AWS",
			"compute_stack": "VM",
			"region": "us-west-2",
			"status": "ready",
			"state": "ACTIVE",
			"is_private_cloud": false,
			"auto_add_user": true
		}
	}`

	var resp CloudResponse
	err := json.Unmarshal([]byte(jsonInput), &resp)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if resp.Result.ID != "cld_123" {
		t.Errorf("ID = %q, want %q", resp.Result.ID, "cld_123")
	}
	if resp.Result.Name != "my-cloud" {
		t.Errorf("Name = %q, want %q", resp.Result.Name, "my-cloud")
	}
	if resp.Result.Provider != "AWS" {
		t.Errorf("Provider = %q, want %q", resp.Result.Provider, "AWS")
	}
	if resp.Result.ComputeStack != "VM" {
		t.Errorf("ComputeStack = %q, want %q", resp.Result.ComputeStack, "VM")
	}
	if resp.Result.Region != "us-west-2" {
		t.Errorf("Region = %q, want %q", resp.Result.Region, "us-west-2")
	}
	if resp.Result.Status != "ready" {
		t.Errorf("Status = %q, want %q", resp.Result.Status, "ready")
	}
	if resp.Result.State != "ACTIVE" {
		t.Errorf("State = %q, want %q", resp.Result.State, "ACTIVE")
	}
	if resp.Result.IsPrivateCloud != false {
		t.Errorf("IsPrivateCloud = %v, want %v", resp.Result.IsPrivateCloud, false)
	}
	if resp.Result.AutoAddUser != true {
		t.Errorf("AutoAddUser = %v, want %v", resp.Result.AutoAddUser, true)
	}
}

// TestCloudDeploymentRequestJSON tests JSON marshaling of CloudDeploymentRequest
func TestCloudDeploymentRequestJSON(t *testing.T) {
	region := "us-west-2"
	req := CloudDeploymentRequest{
		Name:           "vm-aws-us-west-2",
		Provider:       "AWS",
		ComputeStack:   "VM",
		Region:         "us-west-2",
		NetworkingMode: "PUBLIC",
		ObjectStorage: &ObjectStorage{
			BucketName: "s3://my-bucket",
			Region:     &region,
		},
		AWSConfig: &AWSConfig{
			VPCID:             "vpc-123",
			SubnetIDs:         []string{"subnet-1", "subnet-2"},
			SecurityGroupIDs:  []string{"sg-1"},
			AnyscaleIAMRoleID: "arn:aws:iam::123:role/controlplane",
			ClusterIAMRoleID:  "arn:aws:iam::123:role/dataplane",
			ExternalID:        "ext-123",
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	// Unmarshal to verify roundtrip
	var decoded CloudDeploymentRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded.Name != req.Name {
		t.Errorf("Name = %q, want %q", decoded.Name, req.Name)
	}
	if decoded.Provider != req.Provider {
		t.Errorf("Provider = %q, want %q", decoded.Provider, req.Provider)
	}
	if decoded.ComputeStack != req.ComputeStack {
		t.Errorf("ComputeStack = %q, want %q", decoded.ComputeStack, req.ComputeStack)
	}
	if decoded.NetworkingMode != req.NetworkingMode {
		t.Errorf("NetworkingMode = %q, want %q", decoded.NetworkingMode, req.NetworkingMode)
	}

	// Check nested objects
	if decoded.ObjectStorage == nil {
		t.Fatal("ObjectStorage is nil")
	}
	if decoded.ObjectStorage.BucketName != "s3://my-bucket" {
		t.Errorf("ObjectStorage.BucketName = %q, want %q", decoded.ObjectStorage.BucketName, "s3://my-bucket")
	}

	if decoded.AWSConfig == nil {
		t.Fatal("AWSConfig is nil")
	}
	if decoded.AWSConfig.VPCID != "vpc-123" {
		t.Errorf("AWSConfig.VPCID = %q, want %q", decoded.AWSConfig.VPCID, "vpc-123")
	}
	if len(decoded.AWSConfig.SubnetIDs) != 2 {
		t.Errorf("AWSConfig.SubnetIDs length = %d, want 2", len(decoded.AWSConfig.SubnetIDs))
	}
}

// TestAWSConfigJSON tests AWS config JSON marshaling
func TestAWSConfigJSON(t *testing.T) {
	memoryDBName := "my-memorydb"
	config := AWSConfig{
		VPCID:               "vpc-123",
		SubnetIDs:           []string{"subnet-1", "subnet-2"},
		SecurityGroupIDs:    []string{"sg-1", "sg-2"},
		AnyscaleIAMRoleID:   "arn:aws:iam::123:role/anyscale",
		ClusterIAMRoleID:    "arn:aws:iam::123:role/cluster",
		ExternalID:          "ext-id-123",
		MemoryDBClusterName: &memoryDBName,
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded AWSConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded.VPCID != config.VPCID {
		t.Errorf("VPCID = %q, want %q", decoded.VPCID, config.VPCID)
	}
	if decoded.ExternalID != config.ExternalID {
		t.Errorf("ExternalID = %q, want %q", decoded.ExternalID, config.ExternalID)
	}
	if decoded.MemoryDBClusterName == nil || *decoded.MemoryDBClusterName != memoryDBName {
		t.Errorf("MemoryDBClusterName = %v, want %q", decoded.MemoryDBClusterName, memoryDBName)
	}
}

// TestGCPConfigJSON tests GCP config JSON marshaling
func TestGCPConfigJSON(t *testing.T) {
	config := GCPConfig{
		ProjectID:                   "my-project",
		HostProjectID:               "host-project",
		ProviderName:                "projects/123456789/locations/global/workloadIdentityPools/anyscale-pool/providers/anyscale-provider",
		VPCName:                     "my-vpc",
		SubnetNames:                 []string{"subnet-1", "subnet-2"},
		AnyscaleServiceAccountEmail: "anyscale-sa@my-project.iam.gserviceaccount.com",
		ClusterServiceAccountEmail:  "cluster-sa@my-project.iam.gserviceaccount.com",
		FirewallPolicyNames:         []string{"policy-1", "policy-2"},
		MemorystoreInstanceName:     "my-memorystore",
		MemorystoreEndpoint:         "10.0.0.1:6379",
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded GCPConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded.ProjectID != config.ProjectID {
		t.Errorf("ProjectID = %q, want %q", decoded.ProjectID, config.ProjectID)
	}
	if decoded.HostProjectID != config.HostProjectID {
		t.Errorf("HostProjectID = %q, want %q", decoded.HostProjectID, config.HostProjectID)
	}
	if decoded.ProviderName != config.ProviderName {
		t.Errorf("ProviderName = %q, want %q", decoded.ProviderName, config.ProviderName)
	}
	if decoded.VPCName != config.VPCName {
		t.Errorf("VPCName = %q, want %q", decoded.VPCName, config.VPCName)
	}
	if len(decoded.SubnetNames) != 2 {
		t.Errorf("SubnetNames length = %d, want 2", len(decoded.SubnetNames))
	}
	if decoded.AnyscaleServiceAccountEmail != config.AnyscaleServiceAccountEmail {
		t.Errorf("AnyscaleServiceAccountEmail = %q, want %q", decoded.AnyscaleServiceAccountEmail, config.AnyscaleServiceAccountEmail)
	}
	if decoded.ClusterServiceAccountEmail != config.ClusterServiceAccountEmail {
		t.Errorf("ClusterServiceAccountEmail = %q, want %q", decoded.ClusterServiceAccountEmail, config.ClusterServiceAccountEmail)
	}
	if len(decoded.FirewallPolicyNames) != 2 {
		t.Errorf("FirewallPolicyNames length = %d, want 2", len(decoded.FirewallPolicyNames))
	}
	if decoded.MemorystoreInstanceName != config.MemorystoreInstanceName {
		t.Errorf("MemorystoreInstanceName = %q, want %q", decoded.MemorystoreInstanceName, config.MemorystoreInstanceName)
	}
	if decoded.MemorystoreEndpoint != config.MemorystoreEndpoint {
		t.Errorf("MemorystoreEndpoint = %q, want %q", decoded.MemorystoreEndpoint, config.MemorystoreEndpoint)
	}
}

// TestAzureConfigJSON tests Azure config JSON marshaling. tenant_id is the
// only field the Anyscale API accepts here - see AzureConfig's doc comment.
func TestAzureConfigJSON(t *testing.T) {
	config := AzureConfig{
		TenantID: "11111111-1111-1111-1111-111111111111",
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	if !strings.Contains(string(data), `"tenant_id":"11111111-1111-1111-1111-111111111111"`) {
		t.Errorf("marshaled JSON = %s, want it to contain tenant_id", data)
	}

	var decoded AzureConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded.TenantID != config.TenantID {
		t.Errorf("TenantID = %q, want %q", decoded.TenantID, config.TenantID)
	}
}

// TestKubernetesConfigJSON tests Kubernetes config JSON marshaling
// Note: KubernetesConfig only includes fields accepted by the Anyscale API
func TestKubernetesConfigJSON(t *testing.T) {
	config := KubernetesConfig{
		AnyscaleOperatorIAMIdentity: "arn:aws:iam::123456789012:role/anyscale-operator",
		Zones:                       []string{"us-east-1a", "us-east-1b"},
		RedisEndpoint:               "redis.ray-system.svc.cluster.local:6379",
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded KubernetesConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded.AnyscaleOperatorIAMIdentity != config.AnyscaleOperatorIAMIdentity {
		t.Errorf("AnyscaleOperatorIAMIdentity = %q, want %q", decoded.AnyscaleOperatorIAMIdentity, config.AnyscaleOperatorIAMIdentity)
	}
	if len(decoded.Zones) != len(config.Zones) {
		t.Errorf("Zones length = %d, want %d", len(decoded.Zones), len(config.Zones))
	}
	if decoded.RedisEndpoint != config.RedisEndpoint {
		t.Errorf("RedisEndpoint = %q, want %q", decoded.RedisEndpoint, config.RedisEndpoint)
	}

	// Verify omitempty when RedisEndpoint is empty
	emptyData, err := json.Marshal(KubernetesConfig{AnyscaleOperatorIAMIdentity: "x"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(emptyData), "redis_endpoint") {
		t.Errorf("empty RedisEndpoint should be omitted; got %s", emptyData)
	}
}

// TestObjectStorageJSON tests object storage JSON marshaling
func TestObjectStorageJSON(t *testing.T) {
	region := "us-west-2"
	endpoint := "https://s3.amazonaws.com"

	config := ObjectStorage{
		BucketName: "my-bucket",
		Region:     &region,
		Endpoint:   &endpoint,
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded ObjectStorage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded.BucketName != config.BucketName {
		t.Errorf("BucketName = %q, want %q", decoded.BucketName, config.BucketName)
	}
	if decoded.Region == nil || *decoded.Region != region {
		t.Errorf("Region = %v, want %q", decoded.Region, region)
	}
	if decoded.Endpoint == nil || *decoded.Endpoint != endpoint {
		t.Errorf("Endpoint = %v, want %q", decoded.Endpoint, endpoint)
	}
}

// TestFileStorageJSON tests file storage JSON marshaling
func TestFileStorageJSON(t *testing.T) {
	config := FileStorage{
		FileStorageID: "fs-123",
		MountPath:     "/mnt/shared",
		MountTargets: []MountTarget{
			{Address: "10.0.0.1", Zone: "us-east-1a"},
			{Address: "10.0.0.2", Zone: "us-east-1b"},
		},
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded FileStorage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded.FileStorageID != config.FileStorageID {
		t.Errorf("FileStorageID = %q, want %q", decoded.FileStorageID, config.FileStorageID)
	}
	if decoded.MountPath != config.MountPath {
		t.Errorf("MountPath = %q, want %q", decoded.MountPath, config.MountPath)
	}
	if len(decoded.MountTargets) != 2 {
		t.Errorf("MountTargets length = %d, want 2", len(decoded.MountTargets))
	}
	if decoded.MountTargets[0].Address != "10.0.0.1" {
		t.Errorf("MountTargets[0].Address = %q, want %q", decoded.MountTargets[0].Address, "10.0.0.1")
	}
	if decoded.MountTargets[0].Zone != "us-east-1a" {
		t.Errorf("MountTargets[0].Zone = %q, want %q", decoded.MountTargets[0].Zone, "us-east-1a")
	}
}

// TestCloudDeploymentsResponseJSON tests pagination response
func TestCloudDeploymentsResponseJSON(t *testing.T) {
	jsonInput := `{
		"results": [
			{
				"cloud_resource_id": "cr-1",
				"name": "deployment-1",
				"provider": "AWS",
				"compute_stack": "VM",
				"region": "us-west-2",
				"networking_mode": "PUBLIC"
			}
		],
		"metadata": {
			"total": 1,
			"next_paging_token": "token123"
		}
	}`

	var resp CloudDeploymentsResponse
	err := json.Unmarshal([]byte(jsonInput), &resp)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(resp.Results) != 1 {
		t.Errorf("Results length = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].CloudResourceID != "cr-1" {
		t.Errorf("CloudResourceID = %q, want %q", resp.Results[0].CloudResourceID, "cr-1")
	}
	if resp.Metadata.Total != 1 {
		t.Errorf("Metadata.Total = %d, want 1", resp.Metadata.Total)
	}
	if resp.Metadata.NextPagingToken == nil || *resp.Metadata.NextPagingToken != "token123" {
		t.Errorf("NextPagingToken = %v, want %q", resp.Metadata.NextPagingToken, "token123")
	}
}

// TestBuildResultResolvedRayVersion tests F4's resolution order: a BYOD build's
// byod_ray_version (parsed from the docker image tag, may differ from what the user
// requested) takes precedence over the plain ray_version field when both are present,
// since only byod_ray_version reflects what the image actually resolved to.
func TestBuildResultResolvedRayVersion(t *testing.T) {
	tests := []struct {
		name           string
		byodRayVersion *string
		rayVersion     *string
		want           *string
	}{
		{
			name:           "byod_ray_version present takes precedence over ray_version",
			byodRayVersion: strPtr("2.9.3"),
			rayVersion:     strPtr("2.9.0"),
			want:           strPtr("2.9.3"),
		},
		{
			name:           "only ray_version present (non-BYOD build)",
			byodRayVersion: nil,
			rayVersion:     strPtr("2.44.0"),
			want:           strPtr("2.44.0"),
		},
		{
			name:           "only byod_ray_version present",
			byodRayVersion: strPtr("2.9.3"),
			rayVersion:     nil,
			want:           strPtr("2.9.3"),
		},
		{
			name:           "neither present",
			byodRayVersion: nil,
			rayVersion:     nil,
			want:           nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildResult{
				ByodRayVersion: tt.byodRayVersion,
				RayVersion:     tt.rayVersion,
			}

			got := result.ResolvedRayVersion()
			if tt.want == nil {
				if got != nil {
					t.Errorf("ResolvedRayVersion() = %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != *tt.want {
				t.Errorf("ResolvedRayVersion() = %v, want %v", got, *tt.want)
			}
		})
	}
}
