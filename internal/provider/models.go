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
	AvailabilityZones              []string    `json:"availability_zones"`
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

// Project API Models

// CreateProjectRequest is the request body for creating a project
type CreateProjectRequest struct {
	Name                   string  `json:"name"`
	ParentCloudID          string  `json:"parent_cloud_id"`
	Description            *string `json:"description,omitempty"`
	InitialClusterConfigID *string `json:"cluster_config,omitempty"` // Note: API uses 'cluster_config' not 'initial_cluster_config'
}

// ProjectResponse represents a single project from the Anyscale API
type ProjectResponse struct {
	Result ProjectResult `json:"result"`
}

// ProjectsListResponse represents the response from listing projects
type ProjectsListResponse struct {
	Results  []ProjectResult `json:"results"`
	Metadata struct {
		Total           int     `json:"total"`
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// ProjectResult is the actual project data
type ProjectResult struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Description     *string `json:"description"`
	ParentCloudID   string  `json:"parent_cloud_id"`
	CreatorID       *string `json:"creator_id,omitempty"`
	CreatedAt       string  `json:"created_at"`
	LastUsedCloudID *string `json:"last_used_cloud_id,omitempty"`
	IsDefault       bool    `json:"is_default"`
	DirectoryName   string  `json:"directory_name"`
}

// ProjectCollaboratorBatchRequest for batch creating collaborators
type ProjectCollaboratorBatchRequest []ProjectCollaboratorEntry

// ProjectCollaboratorEntry represents a single collaborator for request
type ProjectCollaboratorEntry struct {
	Value struct {
		Email string `json:"email"`
	} `json:"value"`
	PermissionLevel string `json:"permission_level"` // "owner", "writer", "readonly"
}

// ProjectCollaboratorListResponse for listing collaborators
type ProjectCollaboratorListResponse struct {
	Results  []ProjectCollaboratorResult `json:"results"`
	Metadata struct {
		Total           int     `json:"total"`
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// ProjectCollaboratorResult represents a collaborator from the API
type ProjectCollaboratorResult struct {
	ID    string `json:"id"` // This is the identity ID
	Value struct {
		ID    string `json:"id"` // This is the user ID
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"value"`
	PermissionLevel string `json:"permission_level"`
}

// ProjectCollaboratorUpdateRequest for updating a single collaborator's permission
type ProjectCollaboratorUpdateRequest struct {
	PermissionLevel string `json:"permission_level"`
}

// Organization Invitation API Models

// CreateOrganizationInvitationRequest is the request body for creating an invitation
type CreateOrganizationInvitationRequest struct {
	Email string `json:"email"`
}

// OrganizationInvitationResponse represents a single invitation from the API
type OrganizationInvitationResponse struct {
	Result OrganizationInvitationResult `json:"result"`
}

// OrganizationInvitationsListResponse represents the response from listing invitations
type OrganizationInvitationsListResponse struct {
	Results  []OrganizationInvitationResult `json:"results"`
	Metadata struct {
		Total           int     `json:"total"`
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// OrganizationInvitationResult represents an invitation
type OrganizationInvitationResult struct {
	ID             string  `json:"id"` // invitation_id
	Email          string  `json:"email"`
	OrganizationID string  `json:"organization_id"`
	CreatedAt      string  `json:"created_at"`
	ExpiresAt      string  `json:"expires_at"`
	AcceptedAt     *string `json:"accepted_at"` // null if not accepted
}

// Organization Collaborator API Models

// UpdateOrganizationCollaboratorRequest is the request body for updating a collaborator
type UpdateOrganizationCollaboratorRequest struct {
	PermissionLevel string `json:"permission_level"` // "owner" or "collaborator"
}

// OrganizationCollaboratorResult represents a collaborator from the API
// Note: The data source already has models for this, but we define it here for completeness
type OrganizationCollaboratorResult struct {
	ID              string  `json:"id"`      // identity_id
	UserID          *string `json:"user_id"` // Can be null for some user types
	Email           string  `json:"email"`
	Name            *string `json:"name"`             // Can be null
	PermissionLevel string  `json:"permission_level"` // "owner" or "collaborator"
	CreatedAt       string  `json:"created_at"`
}

// OrganizationCollaboratorsListResponse represents the response from listing collaborators
type OrganizationCollaboratorsListResponse struct {
	Results  []OrganizationCollaboratorResult `json:"results"`
	Metadata struct {
		Total           int     `json:"total"`
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// Policy Binding API Models

// SetPolicyBindingRequest is the request body for setting policy bindings
type SetPolicyBindingRequest struct {
	Bindings []PolicyBindingEntry `json:"bindings"`
}

// PolicyBindingEntry represents a single role binding
type PolicyBindingEntry struct {
	RoleName   string   `json:"role_name"`
	Principals []string `json:"principals"` // List of user group IDs (ug_*)
}

// PolicyBindingResponse represents a single policy binding from the API
type PolicyBindingResponse struct {
	Result PolicyBindingResult `json:"result"`
}

// PolicyBindingResult represents the policy data
type PolicyBindingResult struct {
	Bindings   []PolicyBindingEntry `json:"bindings"`
	SyncStatus *string              `json:"sync_status,omitempty"`
}

// PolicyBindingsListResponse represents the response from listing all policies
type PolicyBindingsListResponse struct {
	Results []PolicyBindingWithMetadata `json:"results"`
}

// PolicyBindingWithMetadata includes resource identification
type PolicyBindingWithMetadata struct {
	ResourceID   string               `json:"resource_id"`
	ResourceType string               `json:"resource_type"`
	Bindings     []PolicyBindingEntry `json:"bindings"`
	SyncStatus   string               `json:"sync_status"`
}

// Machine Pool API Models

// CreateMachinePoolRequest is the request body for creating a machine pool
type CreateMachinePoolRequest struct {
	MachinePoolName               string `json:"machine_pool_name"`
	EnableRootlessDataplaneConfig bool   `json:"enable_rootless_dataplane_config,omitempty"`
}

// CreateMachinePoolResponse represents the response from creating a machine pool
type CreateMachinePoolResponse struct {
	Result struct {
		MachinePool MachinePoolResult `json:"machine_pool"`
	} `json:"result"`
}

// UpdateMachinePoolRequest is the request body for updating a machine pool
type UpdateMachinePoolRequest struct {
	MachinePoolName string         `json:"machine_pool_name"`
	Spec            map[string]any `json:"spec,omitempty"`
}

// UpdateMachinePoolResponse represents the response from updating a machine pool
type UpdateMachinePoolResponse struct {
	Result struct{} `json:"result"`
}

// DeleteMachinePoolRequest is the request body for deleting a machine pool
type DeleteMachinePoolRequest struct {
	MachinePoolName string `json:"machine_pool_name"`
}

// DeleteMachinePoolResponse represents the response from deleting a machine pool
type DeleteMachinePoolResponse struct {
	Result struct{} `json:"result"`
}

// MachinePoolResult represents a machine pool from the API
type MachinePoolResult struct {
	MachinePoolID                 string              `json:"machine_pool_id"`
	MachinePoolName               string              `json:"machine_pool_name"`
	OrganizationID                string              `json:"organization_id"`
	CloudIDs                      []string            `json:"cloud_ids"`
	CloudResourceIDs              map[string][]string `json:"cloud_resource_ids,omitempty"`
	EnableRootlessDataplaneConfig bool                `json:"enable_rootless_dataplane_config"`
	Spec                          map[string]any      `json:"spec,omitempty"`
}

// ListMachinePoolsResponse represents the response from listing machine pools
type ListMachinePoolsResponse struct {
	Result struct {
		MachinePools []MachinePoolResult `json:"machine_pools"`
	} `json:"result"`
}

// AttachMachinePoolToCloudRequest is the request body for attaching a machine pool to a cloud
type AttachMachinePoolToCloudRequest struct {
	MachinePoolName string  `json:"machine_pool_name"`
	CloudID         string  `json:"cloud_id"`
	CloudResourceID *string `json:"cloud_resource_id,omitempty"`
}

// AttachMachinePoolToCloudResponse represents the response from attaching a machine pool to a cloud
type AttachMachinePoolToCloudResponse struct {
	Result struct{} `json:"result"`
}

// DetachMachinePoolFromCloudRequest is the request body for detaching a machine pool from a cloud
type DetachMachinePoolFromCloudRequest struct {
	MachinePoolName string  `json:"machine_pool_name"`
	CloudID         string  `json:"cloud_id"`
	CloudResourceID *string `json:"cloud_resource_id,omitempty"`
}

// DetachMachinePoolFromCloudResponse represents the response from detaching a machine pool from a cloud
type DetachMachinePoolFromCloudResponse struct {
	Result struct{} `json:"result"`
}

// Container Image / Cluster Environment API Models (/ext/v0)

// CreateClusterEnvironmentRequest is the request body for creating a cluster environment
// POST /ext/v0/cluster_environments/
type CreateClusterEnvironmentRequest struct {
	Name          string  `json:"name"`
	Containerfile string  `json:"containerfile,omitempty"`
	ProjectID     *string `json:"project_id,omitempty"`
}

// CreateClusterEnvironmentBuildRequest is the request body for creating a new build for an existing cluster environment
// POST /ext/v0/cluster_environment_builds/
type CreateClusterEnvironmentBuildRequest struct {
	ClusterEnvironmentID string  `json:"cluster_environment_id"`
	Containerfile        string  `json:"containerfile,omitempty"`
	DockerImageName      *string `json:"docker_image_name,omitempty"`
	RegistryLoginSecret  *string `json:"registry_login_secret,omitempty"`
	RayVersion           *string `json:"ray_version,omitempty"`
}

// CreateBYODClusterEnvironmentRequest is the request body for creating a BYOD cluster environment
// POST /ext/v0/cluster_environments/byod
type CreateBYODClusterEnvironmentRequest struct {
	Name       string                                 `json:"name"`
	ConfigJSON CreateBYODClusterEnvironmentConfigJSON `json:"config_json"`
	Anonymous  bool                                   `json:"anonymous,omitempty"`
}

// CreateBYODClusterEnvironmentConfigJSON is the config_json for BYOD cluster environment creation
type CreateBYODClusterEnvironmentConfigJSON struct {
	DockerImage         string            `json:"docker_image"`
	RayVersion          string            `json:"ray_version"`
	EnvVars             map[string]string `json:"env_vars,omitempty"`
	RegistryLoginSecret *string           `json:"registry_login_secret,omitempty"`
}

// CreateBYODClusterEnvironmentBuildRequest is the request body for creating a BYOD build
// POST /ext/v0/cluster_environment_builds/byod
type CreateBYODClusterEnvironmentBuildRequest struct {
	ClusterEnvironmentID string                        `json:"cluster_environment_id"`
	ConfigJSON           CreateBYODAppConfigConfigJSON `json:"config_json"`
}

// CreateBYODAppConfigConfigJSON is the config_json for BYOD build creation
type CreateBYODAppConfigConfigJSON struct {
	DockerImage         string            `json:"docker_image"`
	RayVersion          string            `json:"ray_version"`
	EnvVars             map[string]string `json:"env_vars,omitempty"`
	RegistryLoginSecret *string           `json:"registry_login_secret,omitempty"`
}

// ClusterEnvironmentResponse represents a single cluster environment from the API
type ClusterEnvironmentResponse struct {
	Result ClusterEnvironmentResult `json:"result"`
}

// ClusterEnvironmentsListResponse represents the response from listing cluster environments
type ClusterEnvironmentsListResponse struct {
	Results  []ClusterEnvironmentResult `json:"results"`
	Metadata struct {
		Total           int     `json:"total"`
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// ClusterEnvironmentResult represents a cluster environment from the API
type ClusterEnvironmentResult struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	ProjectID      *string          `json:"project_id,omitempty"`
	CreatorID      string           `json:"creator_id"`
	CreatedAt      string           `json:"created_at"`
	LastModifiedAt string           `json:"last_modified_at,omitempty"`
	IsArchived     bool             `json:"is_archived"`
	IsAnonymous    bool             `json:"is_anonymous"`
	LatestBuild    *LatestBuildInfo `json:"latest_build,omitempty"`
	// Legacy fields for compatibility with list endpoint
	LatestBuildID     *string `json:"latest_build_id,omitempty"`
	LatestBuildStatus *string `json:"latest_build_status,omitempty"`
}

// LatestBuildInfo represents the nested latest_build object in the cluster environment response
type LatestBuildInfo struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Status   string `json:"status"`
}

// ClusterEnvironmentBuildResponse represents a single cluster environment build from the API
// GET /ext/v0/cluster_environment_builds/{id}
type ClusterEnvironmentBuildResponse struct {
	Result ClusterEnvironmentBuildResult `json:"result"`
}

// ClusterEnvironmentBuildOperationResponse represents the response from creating a build (async operation)
// POST /ext/v0/cluster_environment_builds/
type ClusterEnvironmentBuildOperationResponse struct {
	Result ClusterEnvironmentBuildResult `json:"result"`
}

// ClusterEnvironmentBuildsListResponse represents the response from listing builds
// GET /ext/v0/cluster_environment_builds/?cluster_environment_id=...
type ClusterEnvironmentBuildsListResponse struct {
	Results  []ClusterEnvironmentBuildResult `json:"results"`
	Metadata struct {
		Total           int     `json:"total"`
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// ClusterEnvironmentBuildResult represents a cluster environment build from the API
type ClusterEnvironmentBuildResult struct {
	ID                   string  `json:"id"`
	ClusterEnvironmentID string  `json:"cluster_environment_id"`
	Containerfile        *string `json:"containerfile,omitempty"`
	DockerImageName      *string `json:"docker_image_name,omitempty"`
	RegistryLoginSecret  *string `json:"registry_login_secret,omitempty"`
	RayVersion           *string `json:"ray_version,omitempty"`
	Revision             int     `json:"revision"`
	CreatorID            string  `json:"creator_id"`
	ErrorMessage         *string `json:"error_message,omitempty"`
	Status               string  `json:"status"` // pending, in_progress, succeeded, failed, pending_cancellation, cancelled
	CreatedAt            string  `json:"created_at"`
	LastModifiedAt       string  `json:"last_modified_at"`
	IsBYOD               bool    `json:"is_byod"`
	CloudID              *string `json:"cloud_id,omitempty"`
	Digest               *string `json:"digest,omitempty"`
}

// ClusterEnvironmentsSearchQuery is the request body for POST /ext/v0/cluster_environments/search
type ClusterEnvironmentsSearchQuery struct {
	ProjectID        *string    `json:"project_id,omitempty"`
	CreatorID        *string    `json:"creator_id,omitempty"`
	Name             *TextQuery `json:"name,omitempty"`
	ImageName        *TextQuery `json:"image_name,omitempty"`
	Paging           PageQuery  `json:"paging"`
	IncludeArchived  bool       `json:"include_archived"`
	IncludeAnonymous bool       `json:"include_anonymous"`
}

// TextQuery represents a text search filter
type TextQuery struct {
	Contains string `json:"contains"`
}

// PageQuery represents pagination parameters
type PageQuery struct {
	Count       int     `json:"count,omitempty"`
	PagingToken *string `json:"paging_token,omitempty"`
}
