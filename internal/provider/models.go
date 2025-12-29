package provider

// Cloud API Models

// CreateCloudRequest is the request body for creating a cloud
type CreateCloudRequest struct {
	Name        string `json:"name"`
	Provider    string `json:"provider,omitempty"`
	Region      string `json:"region,omitempty"`
	Credentials string `json:"credentials,omitempty"`
}

// CloudResponse represents a cloud in the Anyscale API
type CloudResponse struct {
	Result CloudResult `json:"result"`
}

// CloudsListResponse represents the response from listing clouds
type CloudsListResponse struct {
	Results  []CloudResult `json:"results"`
	Metadata struct {
		Total           int     `json:"total"`
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// CloudResult is the actual cloud data
type CloudResult struct {
	ID                             string      `json:"id"`
	Name                           string      `json:"name"`
	Provider                       string      `json:"provider"`
	ComputeStack                   string      `json:"compute_stack"`
	Region                         string      `json:"region"`
	Credentials                    string      `json:"credentials"`
	Config                         CloudConfig `json:"config"`
	IsK8s                          bool        `json:"is_k8s"`
	IsAIOA                         bool        `json:"is_aioa"`
	AvailabilityZones              *string     `json:"availability_zones"`
	IsBringYourOwnResource         bool        `json:"is_bring_your_own_resource"`
	IsPrivateCloud                 bool        `json:"is_private_cloud"`
	ClusterManagementStackVersion  string      `json:"cluster_management_stack_version"`
	IsPrivateServiceCloud          bool        `json:"is_private_service_cloud"`
	AutoAddUser                    bool        `json:"auto_add_user"`
	LineageTrackingEnabled         bool        `json:"lineage_tracking_enabled"`
	ExternalID                     *string     `json:"external_id"`
	Type                           string      `json:"type"`
	CreatorID                      string      `json:"creator_id"`
	CreatedAt                      string      `json:"created_at"`
	Status                         string      `json:"status"`
	State                          string      `json:"state"`
	Version                        string      `json:"version"`
	IsDefault                      bool        `json:"is_default"`
	CustomerAggregatedLogsConfigID string      `json:"customer_aggregated_logs_config_id"`
	IsAggregatedLogsEnabled        bool        `json:"is_aggregated_logs_enabled"`
	SystemClusterConfigID          *string     `json:"system_cluster_config_id"`
}

// CloudConfig represents cloud configuration
type CloudConfig struct {
	MaxStoppedInstances       int     `json:"max_stopped_instances"`
	VPCPeeringIPRange         *string `json:"vpc_peering_ip_range"`
	VPCPeeringTargetProjectID *string `json:"vpc_peering_target_project_id"`
	VPCPeeringTargetVPCID     *string `json:"vpc_peering_target_vpc_id"`
}

// CloudDeploymentRequest is the request body for adding a cloud resource
type CloudDeploymentRequest struct {
	Name             string            `json:"name"`
	Provider         string            `json:"provider"`
	ComputeStack     string            `json:"compute_stack"`
	Region           string            `json:"region"`
	NetworkingMode   string            `json:"networking_mode"`
	ObjectStorage    *ObjectStorage    `json:"object_storage,omitempty"`
	FileStorage      *FileStorage      `json:"file_storage,omitempty"`
	AWSConfig        *AWSConfig        `json:"aws_config,omitempty"`
	GCPConfig        *GCPConfig        `json:"gcp_config,omitempty"`
	AzureConfig      *AzureConfig      `json:"azure_config,omitempty"`
	KubernetesConfig *KubernetesConfig `json:"kubernetes_config,omitempty"`
	// Note: Cloud-level settings (auto_add_user, lineage_tracking, log_ingestion)
	// are set during cloud creation (POST /api/v2/clouds), NOT during add_resource
}

// ObjectStorage represents object storage configuration (S3, GCS, Azure Blob, S3-compatible)
type ObjectStorage struct {
	BucketName string  `json:"bucket_name"`
	Region     *string `json:"region,omitempty"`
	Endpoint   *string `json:"endpoint,omitempty"`
}

// FileStorage represents file storage configuration (EFS, Filestore, etc.)
type FileStorage struct {
	FileStorageID            string        `json:"file_storage_id"`
	MountPath                string        `json:"mount_path,omitempty"`
	MountTargets             []MountTarget `json:"mount_targets,omitempty"`
	PersistentVolumeClaim    string        `json:"persistent_volume_claim,omitempty"`
	CSIEphemeralVolumeDriver string        `json:"csi_ephemeral_volume_driver,omitempty"`
}

// MountTarget represents a mount target with address and zone
type MountTarget struct {
	Address string `json:"address"`
	Zone    string `json:"zone,omitempty"`
}

// AWSConfig represents AWS-specific cloud configuration
type AWSConfig struct {
	VPCID                   string   `json:"vpc_id"`
	SubnetIDs               []string `json:"subnet_ids"`
	Zones                   []string `json:"zones,omitempty"`
	SecurityGroupIDs        []string `json:"security_group_ids"`
	AnyscaleIAMRoleID       string   `json:"anyscale_iam_role_id"`
	ExternalID              string   `json:"external_id,omitempty"`
	ClusterIAMRoleID        string   `json:"cluster_iam_role_id"`
	MemoryDBClusterName     *string  `json:"memorydb_cluster_name,omitempty"`
	MemoryDBClusterARN      *string  `json:"memorydb_cluster_arn,omitempty"`
	MemoryDBClusterEndpoint *string  `json:"memorydb_cluster_endpoint,omitempty"`
	CloudFormationID        *string  `json:"cloudformation_id,omitempty"`
}

// GCPConfig represents GCP-specific cloud configuration
type GCPConfig struct {
	ProjectID                   string   `json:"project_id"`
	HostProjectID               string   `json:"host_project_id,omitempty"`
	ProviderName                string   `json:"provider_name"`
	VPCName                     string   `json:"vpc_name"`
	SubnetNames                 []string `json:"subnet_names"`
	AnyscaleServiceAccountEmail string   `json:"anyscale_service_account_email"`
	ClusterServiceAccountEmail  string   `json:"cluster_service_account_email"`
	FirewallPolicyNames         []string `json:"firewall_policy_names,omitempty"`
	MemorystoreInstanceName     string   `json:"memorystore_instance_name,omitempty"`
	MemorystoreEndpoint         string   `json:"memorystore_endpoint,omitempty"`
}

// AzureConfig represents Azure-specific cloud configuration
type AzureConfig struct {
	SubscriptionID    string `json:"subscription_id"`
	ResourceGroupName string `json:"resource_group_name"`
	VNetName          string `json:"vnet_name"`
	SubnetName        string `json:"subnet_name"`
	ManagedIdentityID string `json:"managed_identity_id"`
}

// KubernetesConfig represents Kubernetes-specific cloud configuration for API requests.
// Note: Only anyscale_operator_iam_identity and zones are accepted by the add_resource API.
// Other fields (namespace, ingress_host, etc.) are stored in Terraform state for reference
// but are not sent to the API.
type KubernetesConfig struct {
	// Required for K8s deployments - IAM role ARN (AWS) or service account email (GCP/Azure)
	AnyscaleOperatorIAMIdentity string `json:"anyscale_operator_iam_identity,omitempty"`

	// Optional - availability zones for the K8s cluster
	Zones []string `json:"zones,omitempty"`
}

// KubernetesConfigFull represents the full Kubernetes configuration including
// fields stored in Terraform state but not sent to the API.
type KubernetesConfigFull struct {
	KubernetesConfig

	// The following fields are stored in Terraform state for reference/outputs
	// but are NOT sent to the Anyscale API

	// Namespace for Anyscale operator (defaults to "anyscale")
	Namespace string `json:"-"`

	// Ingress settings
	IngressHost string `json:"-"`

	// Cloud-specific cluster identifiers
	ClusterName string `json:"-"` // AWS EKS cluster name
	Context     string `json:"-"` // K8s context name

	// Legacy/generic fields
	KubeconfigPath string `json:"-"`
}

// CloudDeploymentResponse represents the response from adding a cloud resource
type CloudDeploymentResponse struct {
	Result CloudDeploymentResult `json:"result"`
}

// CloudDeploymentResult is the actual deployment data
type CloudDeploymentResult struct {
	CloudResourceID         string            `json:"cloud_resource_id"`
	CloudDeploymentID       string            `json:"cloud_deployment_id"`
	Name                    string            `json:"name"`
	Provider                string            `json:"provider"`
	ComputeStack            string            `json:"compute_stack"`
	Region                  string            `json:"region"`
	NetworkingMode          string            `json:"networking_mode"`
	ObjectStorage           *ObjectStorage    `json:"object_storage"`
	FileStorage             *FileStorage      `json:"file_storage"`
	AWSConfig               *AWSConfig        `json:"aws_config"`
	GCPConfig               *GCPConfig        `json:"gcp_config"`
	AzureConfig             *AzureConfig      `json:"azure_config"`
	KubernetesConfig        *KubernetesConfig `json:"kubernetes_config"`
	CreatedAt               string            `json:"created_at"`
	IsDefault               bool              `json:"is_default"`
	OperatorStatus          *string           `json:"operator_status"`
	OperatorStatusDetails   *string           `json:"operator_status_details"`
	AutoAddUser             *bool             `json:"auto_add_user,omitempty"`
	LineageTrackingEnabled  *bool             `json:"lineage_tracking_enabled,omitempty"`
	IsAggregatedLogsEnabled *bool             `json:"is_aggregated_logs_enabled,omitempty"`
}

// CloudDeploymentsResponse represents the response from listing cloud deployments
type CloudDeploymentsResponse struct {
	Results  []CloudDeploymentResult `json:"results"`
	Metadata DeploymentMetadata      `json:"metadata"`
}

// DeploymentMetadata represents pagination metadata
type DeploymentMetadata struct {
	Total           int     `json:"total"`
	NextPagingToken *string `json:"next_paging_token"`
}
