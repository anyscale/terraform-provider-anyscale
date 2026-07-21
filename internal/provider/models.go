package provider

import "encoding/json"

// Cloud API Models

// CreateCloudRequest is the request body for creating a cloud
type CreateCloudRequest struct {
	Name        string `json:"name"`
	Provider    string `json:"provider,omitempty"`
	Region      string `json:"region,omitempty"`
	Credentials string `json:"credentials,omitempty"`

	// IsPrivateCloud must be set here: the backend has no route to change it
	// after creation (update_cloud_sql_alchemy takes no such parameter), so
	// this POST is the only place it can ever be set - never omitempty,
	// matching the CLI's WriteCloud, which always sends it explicitly rather
	// than relying on the server-side false default.
	IsPrivateCloud bool `json:"is_private_cloud"`

	// IsPrivateServiceCloud mirrors the CLI exactly: register_gcp_cloud
	// always sends it (true or false); register_aws_cloud and
	// register_azure_or_generic_cloud never reference it at all. A pointer
	// with omitempty is required to express that distinction on the wire -
	// a plain bool would either drop an explicit GCP "false" or leak a
	// spurious value into every non-GCP request.
	IsPrivateServiceCloud *bool `json:"is_private_service_cloud,omitempty"`
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
	VPCID                    string   `json:"vpc_id"`
	SubnetIDs                []string `json:"subnet_ids"`
	Zones                    []string `json:"zones,omitempty"`
	SecurityGroupIDs         []string `json:"security_group_ids"`
	AnyscaleIAMRoleID        string   `json:"anyscale_iam_role_id"`
	ExternalID               string   `json:"external_id,omitempty"`
	ClusterIAMRoleID         string   `json:"cluster_iam_role_id"`
	ClusterInstanceProfileID *string  `json:"cluster_instance_profile_id,omitempty"`
	MemoryDBClusterName      *string  `json:"memorydb_cluster_name,omitempty"`
	MemoryDBClusterARN       *string  `json:"memorydb_cluster_arn,omitempty"`
	MemoryDBClusterEndpoint  *string  `json:"memorydb_cluster_endpoint,omitempty"`
	CloudFormationID         *string  `json:"cloudformation_id,omitempty"`
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

// AzureConfig represents Azure-specific cloud configuration. tenant_id is the
// ONLY field the Anyscale API accepts here (confirmed against
// backend/server/api/base/models/cloud_deployment.py's AzureConfig model) -
// Azure clouds are Kubernetes-only; there is no VM-shaped Azure config the
// way AWSConfig/GCPConfig carry VPC/subnet/IAM details, because AKS setup
// creates no VNet/subnet resources of its own and real auth is operator
// workload-identity federation, not this field. A prior version of this
// struct declared subscription_id/resource_group_name/vnet_name/subnet_name/
// managed_identity_id, none of which exist in the real request/response
// contract; every attempt to apply it hard-errored before ever reaching the
// API, so removing them is not a functional break for any real user.
type AzureConfig struct {
	TenantID string `json:"tenant_id,omitempty"`
}

// KubernetesConfig represents Kubernetes-specific cloud configuration for API requests.
// Note: Only anyscale_operator_iam_identity, zones, and redis_endpoint are accepted by the add_resource API.
// Other fields (namespace, ingress_host, etc.) are stored in Terraform state for reference
// but are not sent to the API.
type KubernetesConfig struct {
	// Required for K8s deployments - IAM role ARN (AWS) or service account email (GCP/Azure)
	AnyscaleOperatorIAMIdentity string `json:"anyscale_operator_iam_identity,omitempty"`

	// Optional - availability zones for the K8s cluster
	Zones []string `json:"zones,omitempty"`

	// Optional - Redis endpoint reachable from the data plane (e.g. "redis.ray-system.svc.cluster.local:6379").
	// Used for Ray GCS fault tolerance.
	RedisEndpoint string `json:"redis_endpoint,omitempty"`
}

// CloudDeploymentResponse represents the response from adding a cloud resource
type CloudDeploymentResponse struct {
	Result CloudDeploymentResult `json:"result"`
}

// CloudDeploymentResult is the actual deployment data
type CloudDeploymentResult struct {
	CloudResourceID         string                 `json:"cloud_resource_id"`
	Name                    string                 `json:"name"`
	Provider                string                 `json:"provider"`
	ComputeStack            string                 `json:"compute_stack"`
	Region                  string                 `json:"region"`
	NetworkingMode          string                 `json:"networking_mode"`
	ObjectStorage           *ObjectStorage         `json:"object_storage"`
	FileStorage             *FileStorage           `json:"file_storage"`
	AWSConfig               *AWSConfig             `json:"aws_config"`
	GCPConfig               *GCPConfig             `json:"gcp_config"`
	AzureConfig             *AzureConfig           `json:"azure_config"`
	KubernetesConfig        *KubernetesConfig      `json:"kubernetes_config"`
	CreatedAt               string                 `json:"created_at"`
	IsDefault               bool                   `json:"is_default"`
	OperatorStatus          *string                `json:"operator_status"`
	OperatorStatusDetails   *OperatorStatusDetails `json:"operator_status_details"`
	AutoAddUser             *bool                  `json:"auto_add_user,omitempty"`
	LineageTrackingEnabled  *bool                  `json:"lineage_tracking_enabled,omitempty"`
	IsAggregatedLogsEnabled *bool                  `json:"is_aggregated_logs_enabled,omitempty"`
}

// OperatorStatusDetails carries Kubernetes Anyscale Operator health details,
// present once a K8s cloud_resource's operator has reported in. Previously
// typed as *string on CloudDeploymentResult, which failed to decode as soon
// as the API returned this object (C4/F2 investigation) - the API has always
// returned an object here, never a string.
type OperatorStatusDetails struct {
	OperatorVersion *string               `json:"operator_version"`
	CheckResults    []OperatorCheckResult `json:"check_results"`
	ReportedAt      *string               `json:"reported_at"`
}

// OperatorCheckResult is a single named health check the operator reports,
// e.g. connectivity or permissions checks. Not yet surfaced as its own
// schema attribute (deferred per CLOUD-SYNC-DESIGN.md C4) - decoded here so
// it doesn't need touching again when it is.
type OperatorCheckResult struct {
	Name    *string `json:"name"`
	Status  *string `json:"status"`
	Details *string `json:"details"`
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
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	// DS-PROJ-1: genuinely Optional[str] server-side; shared by both data
	// sources and resource_project.go's Read - all three must map it via
	// StringPointerValue, never collapse a null cloud association to "".
	ParentCloudID   *string `json:"parent_cloud_id"`
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
	PermissionLevel string `json:"permission_level"` // "owner", "write", "readonly"
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

	// DS-OU-2: permission_level above is deprecated backend-side in favor of these
	// two - base_role is a required enum (never null); additional_roles is a
	// required list (can be empty, never null). Both traced against
	// product backend/server/api/product/models/organization_collaborators.py.
	BaseRole        string   `json:"base_role"`
	AdditionalRoles []string `json:"additional_roles"`
}

// OrganizationCollaboratorSingularResponse represents the response from GET
// /api/v2/organization_collaborators/{user_id} - the only read path that can
// return a real, non-empty additional_roles (architect ruling 1; the list/GET
// formatter hardcodes it to an empty slice unconditionally). Keyed by user_id,
// not identity_id - some collaborators have no user_id and cannot use this path
// at all; see hydrateCollaboratorRoles's graceful fallback.
type OrganizationCollaboratorSingularResponse struct {
	Result OrganizationCollaboratorResult `json:"result"`
}

// OrganizationCollaboratorsListResponse represents the response from listing collaborators
type OrganizationCollaboratorsListResponse struct {
	Results  []OrganizationCollaboratorResult `json:"results"`
	Metadata struct {
		Total           int     `json:"total"`
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
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

// Container Image / Application Template API Models (/api/v2)
//
// The Anyscale API calls this resource "application_template" (and its versioned
// builds "builds"); the provider's user-facing "container image" resources/data
// sources translate to/from this shape at the boundary (see resource_container_image_*.go
// and data_source_container_image*.go), keeping Terraform-facing attribute names
// (e.g. cluster_environment_id) stable across the ext/v0 -> api/v2 migration.

// CreateApplicationTemplateRequest is the request body for creating an application template from a Containerfile
// POST /api/v2/application_templates/
type CreateApplicationTemplateRequest struct {
	Name          string  `json:"name"`
	Containerfile string  `json:"containerfile,omitempty"`
	ProjectID     *string `json:"project_id,omitempty"`
}

// CreateBuildRequest is the request body for creating a new build for an existing application template
// POST /api/v2/builds/
type CreateBuildRequest struct {
	ApplicationTemplateID string  `json:"application_template_id"`
	Containerfile         string  `json:"containerfile,omitempty"`
	DockerImageName       *string `json:"docker_image_name,omitempty"`
	RegistryLoginSecret   *string `json:"registry_login_secret,omitempty"`
	RayVersion            *string `json:"ray_version,omitempty"`
}

// CreateBYODApplicationTemplateRequest is the request body for creating a BYOD application template
// POST /api/v2/application_templates/byod
type CreateBYODApplicationTemplateRequest struct {
	Name       string                                  `json:"name"`
	ConfigJSON CreateBYODApplicationTemplateConfigJSON `json:"config_json"`
	Anonymous  bool                                    `json:"anonymous,omitempty"`
}

// CreateBYODApplicationTemplateConfigJSON is the config_json for BYOD application template creation
type CreateBYODApplicationTemplateConfigJSON struct {
	DockerImage         string            `json:"docker_image"`
	RayVersion          string            `json:"ray_version"`
	EnvVars             map[string]string `json:"env_vars,omitempty"`
	RegistryLoginSecret *string           `json:"registry_login_secret,omitempty"`
}

// CreateBYODBuildRequest is the request body for creating a BYOD build
// POST /api/v2/builds/byod
type CreateBYODBuildRequest struct {
	ApplicationTemplateID string                        `json:"application_template_id"`
	ConfigJSON            CreateBYODAppConfigConfigJSON `json:"config_json"`
}

// CreateBYODAppConfigConfigJSON is the config_json for BYOD build creation
type CreateBYODAppConfigConfigJSON struct {
	DockerImage         string            `json:"docker_image"`
	RayVersion          string            `json:"ray_version"`
	EnvVars             map[string]string `json:"env_vars,omitempty"`
	RegistryLoginSecret *string           `json:"registry_login_secret,omitempty"`
}

// ApplicationTemplateResponse represents a single application template from the API
type ApplicationTemplateResponse struct {
	Result ApplicationTemplateResult `json:"result"`
}

// ApplicationTemplatesListResponse represents the response from listing application templates
// GET /api/v2/application_templates/
type ApplicationTemplatesListResponse struct {
	Results  []ApplicationTemplateResult `json:"results"`
	Metadata struct {
		Total           int     `json:"total"`
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// ApplicationTemplateResult represents an application template from the API.
// Matches both the /api/v2/application_templates/{id} and list endpoint responses.
// LatestBuild is only populated on those decorated responses (never on a bare
// create response), and only carries enough to resolve the latest build's ID
// contract-based -- full build detail still requires GET /api/v2/builds/{id}.
type ApplicationTemplateResult struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	ProjectID      *string          `json:"project_id,omitempty"`
	OrganizationID string           `json:"organization_id,omitempty"`
	CreatorID      string           `json:"creator_id"`
	CreatedAt      string           `json:"created_at"`
	LastModifiedAt string           `json:"last_modified_at,omitempty"`
	DeletedAt      *string          `json:"deleted_at,omitempty"`
	Anonymous      bool             `json:"anonymous"`
	IsDefault      bool             `json:"is_default"`
	LatestBuild    *MiniBuildResult `json:"latest_build,omitempty"`
	// DS-IMG-4: CloudID is genuinely Optional[str] server-side (AppConfig.cloud_id);
	// IsExperimental is a plain bool with a backend default, never null.
	CloudID        *string `json:"cloud_id,omitempty"`
	IsExperimental bool    `json:"is_experimental"`
}

// IsArchived returns true if the application template has been deleted/archived
func (a *ApplicationTemplateResult) IsArchived() bool {
	return a.DeletedAt != nil && *a.DeletedAt != ""
}

// MiniBuildResult is the summarized latest-build reference embedded on a decorated
// application template response.
type MiniBuildResult struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Status   string `json:"status"`
	// DS-IMG-2: both genuinely Optional[str] server-side (MiniBuild.docker_image_name/
	// cloud_id) - present on the SAME embedded object both DS already fetch for free,
	// so the plural gains a per-item image_uri with zero extra calls, and the singular
	// can populate image_uri without needing the second GET /builds/{id} call (still
	// made for digest/ray_version/etc, just no longer required for this one field).
	DockerImageName *string `json:"docker_image_name,omitempty"`
	CloudID         *string `json:"cloud_id,omitempty"`
}

// BuildResponse represents a single build from the API. Returned by both
// POST /api/v2/builds/ (create, bare) and GET /api/v2/builds/{id} (decorated get);
// ByodRayVersion is only populated by the latter.
type BuildResponse struct {
	Result BuildResult `json:"result"`
}

// BuildsListResponse represents the response from listing builds
type BuildsListResponse struct {
	Results  []BuildResult `json:"results"`
	Metadata struct {
		Total           int     `json:"total"`
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// BuildResult represents a build from the API.
type BuildResult struct {
	ID                    string  `json:"id"`
	ApplicationTemplateID string  `json:"application_template_id"`
	Containerfile         *string `json:"containerfile,omitempty"`
	DockerImageName       *string `json:"docker_image_name,omitempty"`
	RegistryLoginSecret   *string `json:"registry_login_secret,omitempty"`
	RayVersion            *string `json:"ray_version,omitempty"`
	ByodRayVersion        *string `json:"byod_ray_version,omitempty"`
	Revision              int     `json:"revision"`
	CreatorID             string  `json:"creator_id"`
	ErrorMessage          *string `json:"error_message,omitempty"`
	Status                string  `json:"status"` // pending, in_progress, succeeded, failed, pending_cancellation, canceled
	CreatedAt             string  `json:"created_at"`
	LastModifiedAt        string  `json:"last_modified_at"`
	IsBYOD                bool    `json:"is_byod"`
	CloudID               *string `json:"cloud_id,omitempty"`
	Digest                *string `json:"digest,omitempty"`
}

// ResolvedRayVersion returns the most specific Ray version available for this build:
// byod_ray_version when present, otherwise the plain ray_version field. Both ultimately
// trace back to the ray_version the client supplied at creation (BYOD's docker image
// content itself is never inspected server-side): byod_ray_version is the backend's
// legacy base-image round-trip of that value (see _get_byod_base_image /
// get_ray_version in the product backend), which is normally byte-identical to the
// original input but can differ for Ray 2.7.x, where the backend may silently prefer an
// "optimized" base-image variant. ray_version is the plain field set on
// Containerfile-based (non-BYOD) builds, parsed from the Containerfile's FROM line.
// Neither field is validated here -- an odd stored value is returned as-is rather than
// producing an error or null.
func (b *BuildResult) ResolvedRayVersion() *string {
	if b.ByodRayVersion != nil {
		return b.ByodRayVersion
	}
	return b.RayVersion
}

// Service API Models

// ServiceResponse represents a single service from the Anyscale API (GET /api/v2/services-v2/{id}).
type ServiceResponse struct {
	Result ServiceResult `json:"result"`
}

// ServicesListResponse represents the response from listing services (GET /api/v2/services-v2/).
//
// The backend's list item model (DecoratedListServiceAPIModel) is a strict superset of the get
// item model (DecoratedProductionServiceV2APIModel) - the only extra field is `type`, which this
// provider deliberately does not expose (redundant discriminator; this router only ever deals in
// V2 services). So ServiceResult below is shared, unchanged, by both this and ServiceResponse.
type ServicesListResponse struct {
	Results  []ServiceResult `json:"results"`
	Metadata struct {
		Total           int     `json:"total"`
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// ServiceResult is the actual service data, shared by both the singular GET and plural LIST
// responses (see ServicesListResponse's doc comment for why one struct covers both).
//
// Deliberately absent fields, each a documented gap rather than an oversight:
//   - auth_token: a live bearer credential for calling the deployed service. A Terraform data
//     source's output is always written to state in plaintext regardless of any Sensitive
//     marking, so this is excluded entirely rather than included-and-marked-Sensitive - the same
//     ruling this provider already applies to API keys generally.
//   - versions: deprecated upstream in favor of primary_version/canary_version.
//   - type (ServiceType): list-only field; redundant discriminator since this router is V2-only.
//   - creator (MiniUser object): flat CreatorID is kept instead, consistent with this provider's
//     existing choice to defer a nested creator object on anyscale_cloud for the same reason
//     (low value once creator_id is already surfaced).
type ServiceResult struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Description        *string `json:"description"`
	ProjectID          string  `json:"project_id"`
	CloudID            string  `json:"cloud_id"`
	CreatorID          string  `json:"creator_id"`
	CreatedAt          string  `json:"created_at"`
	EndedAt            *string `json:"ended_at"`
	Hostname           string  `json:"hostname"`
	BaseURL            string  `json:"base_url"`
	CurrentState       string  `json:"current_state"`
	GoalState          string  `json:"goal_state"`
	AutoRolloutEnabled bool    `json:"auto_rollout_enabled"`
	IsMultiVersion     bool    `json:"is_multi_version"`
	ErrorMessage       *string `json:"error_message"`

	// ServiceObservabilityURLs and PrimaryVersion are pointers, not value structs, despite
	// nominally being "always present" per the backend model: CONFIRMED via a real CI
	// acceptance-test run against a real service (TestAccServiceDataSource_ByID, "Path:
	// service_observability_urls", "Received null value") - not a theoretical concern, not a
	// race with a concurrent test. A value struct cannot represent that absence at all (JSON
	// null silently decodes to a zero-value struct with empty-string fields, which is wrong
	// data, not just a missing-null-handling crash) - a pointer makes the absence explicit and
	// checkable, matching CanaryVersion/ServiceStatusChecklist's already-correct pattern for
	// their own nullability.
	ServiceObservabilityURLs *ServiceObservabilityURLsResult `json:"service_observability_urls"`

	PrimaryVersion *ServiceVersionResult `json:"primary_version"`
	CanaryVersion  *ServiceVersionResult `json:"canary_version"`

	ServiceStatusChecklist *ServiceStatusChecklistResult `json:"service_status_checklist"`
}

// ServiceObservabilityURLsResult mirrors the backend's ServiceObservabilityUrls - all 4 fields
// are genuinely optional server-side (nullable dashboard URLs).
type ServiceObservabilityURLsResult struct {
	ServiceDashboardURL                  *string `json:"service_dashboard_url"`
	ServiceDashboardEmbeddingURL         *string `json:"service_dashboard_embedding_url"`
	ServeDeploymentDashboardURL          *string `json:"serve_deployment_dashboard_url"`
	ServeDeploymentDashboardEmbeddingURL *string `json:"serve_deployment_dashboard_embedding_url"`
}

// ServiceVersionResult mirrors the backend's ProductionServiceV2VersionModel, used for both
// PrimaryVersion and CanaryVersion. RayServeConfig is required upstream (always present, never
// JSON null) but kept as json.RawMessage rather than a typed struct or map[string]interface{}:
// the backend model is `extra=Extra.allow`, a fully open/dynamic blob with no fixed schema, and
// this is a Computed (read-only) field with no HCL-authoring side, so a plain JSON-text passthrough
// avoids both an unnecessary typed model and a re-marshal step.
//
// Deliberately absent: ray_gcs_external_storage_config, tracing_config - niche/advanced per-version
// configs, scoped out as a documented gap, follow up on demand.
type ServiceVersionResult struct {
	ID               string          `json:"id"`
	CreatedAt        string          `json:"created_at"`
	Version          string          `json:"version"`
	CurrentState     string          `json:"current_state"`
	Weight           int64           `json:"weight"`
	CurrentWeight    *int64          `json:"current_weight"`
	TargetWeight     *int64          `json:"target_weight"`
	BuildID          string          `json:"build_id"`
	ComputeConfigID  string          `json:"compute_config_id"`
	ProductionJobIDs []string        `json:"production_job_ids"`
	ConnectionIDs    []string        `json:"connection_ids"`
	RayServeConfig   json.RawMessage `json:"ray_serve_config"`
}

// ServiceStatusChecklistResult mirrors the backend's ServiceStatusChecklist. Modeled as lists
// rather than maps upstream (per the backend's own comment) to work around an old
// openapi-generator recursion limitation - kept as lists here too rather than reshaping into a
// map, so the provider's shape matches the wire shape exactly.
type ServiceStatusChecklistResult struct {
	Shared     []StatusChecklistItemResult `json:"shared"`
	PerVersion []VersionChecklistResult    `json:"per_version"`
}

// VersionChecklistResult is the per-version group of checklist items within a
// ServiceStatusChecklistResult.
type VersionChecklistResult struct {
	VersionID string                      `json:"version_id"`
	Items     []StatusChecklistItemResult `json:"items"`
}

// StatusChecklistItemResult mirrors the backend's StatusChecklistItem - one row in a service's
// per-component status breakdown (e.g. "Cluster", "Load Balancer").
type StatusChecklistItemResult struct {
	Kind       string  `json:"kind"`
	Label      string  `json:"label"`
	State      string  `json:"state"`
	Message    string  `json:"message"`
	VersionID  *string `json:"version_id"`
	ObservedAt *string `json:"observed_at"`
}
