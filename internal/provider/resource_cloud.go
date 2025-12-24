package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// ResourceCloud returns the schema for the anyscale_cloud resource
func ResourceCloud() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceCloudCreate,
		ReadContext:   resourceCloudRead,
		UpdateContext: resourceCloudUpdate,
		DeleteContext: resourceCloudDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(30 * time.Minute),
			Update: schema.DefaultTimeout(30 * time.Minute),
			Delete: schema.DefaultTimeout(30 * time.Minute),
		},

		Schema: map[string]*schema.Schema{
			// ─── Common Fields (flat) ───────────────────────────
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The name of the cloud.",
			},
			"cloud_provider": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Cloud provider: AWS, GCP, Azure, or Generic.",
			},
			"compute_stack": {
				Type:        schema.TypeString,
				Optional:    true,
				Default:     "VM",
				ForceNew:    true,
				Description: "Compute stack type: VM or K8S.",
			},
			"region": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The region where the cloud is deployed.",
			},
			"is_private_cloud": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				ForceNew:    true,
				Description: "Whether this is a private cloud (private networking).",
			},
			"auto_add_user": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				Description: "Whether to automatically add users to this cloud.",
			},

			// ─── AWS Configuration (nested) ─────────────────────
			"aws_config": {
				Type:        schema.TypeList,
				Optional:    true,
				MaxItems:    1,
				Description: "AWS-specific configuration. Required when cloud_provider is AWS.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"vpc_id": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "The VPC ID where Anyscale resources will be deployed.",
						},
						"subnet_ids": {
							Type:        schema.TypeList,
							Optional:    true,
							ForceNew:    true,
							Description: "List of subnet IDs for Anyscale resources. Use this OR subnet_ids_to_az.",
							Elem:        &schema.Schema{Type: schema.TypeString},
						},
						"subnet_ids_to_az": {
							Type:        schema.TypeMap,
							Optional:    true,
							ForceNew:    true,
							Description: "Map of subnet ID to availability zone (e.g., {\"subnet-123\": \"us-east-2a\"}). Preferred over subnet_ids.",
							Elem:        &schema.Schema{Type: schema.TypeString},
						},
						"security_group_ids": {
							Type:        schema.TypeList,
							Required:    true,
							ForceNew:    true,
							Description: "List of security group IDs for Anyscale resources.",
							Elem:        &schema.Schema{Type: schema.TypeString},
						},
						"controlplane_iam_role_arn": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "IAM role ARN for Anyscale control plane (cross-account access).",
						},
						"dataplane_iam_role_arn": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "IAM role ARN for Anyscale data plane (cluster nodes).",
						},
						"external_id": {
							Type:        schema.TypeString,
							Optional:    true,
							ForceNew:    true,
							Description: "External ID for IAM role assumption (recommended for security).",
						},
						"memorydb_cluster_name": {
							Type:        schema.TypeString,
							Optional:    true,
							ForceNew:    true,
							Description: "MemoryDB cluster name for Ray GCS fault tolerance.",
						},
					},
				},
			},

			// ─── GCP Configuration (nested) ─────────────────────
			"gcp_config": {
				Type:        schema.TypeList,
				Optional:    true,
				MaxItems:    1,
				Description: "GCP-specific configuration. Required when cloud_provider is GCP.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"project_id": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "The GCP project ID.",
						},
						"host_project_id": {
							Type:        schema.TypeString,
							Optional:    true,
							ForceNew:    true,
							Description: "The host project ID for shared VPCs (optional).",
						},
						"provider_name": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "Workload Identity Federation provider name (e.g., projects/123456789/locations/global/workloadIdentityPools/anyscale-pool/providers/anyscale-provider).",
						},
						"vpc_name": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "The VPC network name.",
						},
						"subnet_names": {
							Type:        schema.TypeList,
							Required:    true,
							ForceNew:    true,
							Description: "List of subnet names within the VPC for Anyscale resources.",
							Elem:        &schema.Schema{Type: schema.TypeString},
						},
						"controlplane_service_account_email": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "Service account email for Anyscale control plane (cross-project access).",
						},
						"dataplane_service_account_email": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "Service account email for Ray cluster nodes (data plane).",
						},
						"firewall_policy_names": {
							Type:        schema.TypeList,
							Optional:    true,
							ForceNew:    true,
							Description: "List of firewall policy names.",
							Elem:        &schema.Schema{Type: schema.TypeString},
						},
						"memorystore_instance_name": {
							Type:        schema.TypeString,
							Optional:    true,
							ForceNew:    true,
							Description: "Memorystore instance name for Ray GCS fault tolerance.",
						},
					},
				},
			},

			// ─── Azure Configuration (nested) ───────────────────
			"azure_config": {
				Type:        schema.TypeList,
				Optional:    true,
				MaxItems:    1,
				Description: "Azure-specific configuration. Required when cloud_provider is Azure.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"subscription_id": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "The Azure subscription ID.",
						},
						"resource_group_name": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "The Azure resource group name.",
						},
						"vnet_name": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "The Azure VNet name.",
						},
						"subnet_name": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "The Azure subnet name.",
						},
						"managed_identity_id": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "The managed identity ID for Anyscale resources.",
						},
					},
				},
			},

			// ─── Kubernetes Configuration (nested) ──────────────
			"kubernetes_config": {
				Type:        schema.TypeList,
				Optional:    true,
				MaxItems:    1,
				Description: "Kubernetes-specific configuration. Required when compute_stack is K8S.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"cluster_name": {
							Type:        schema.TypeString,
							Optional:    true,
							Description: "The Kubernetes cluster name (EKS, GKE, AKS cluster name).",
						},
						"namespace": {
							Type:        schema.TypeString,
							Optional:    true,
							Default:     "anyscale",
							Description: "The Kubernetes namespace for Anyscale workloads.",
						},
						"kubeconfig_path": {
							Type:        schema.TypeString,
							Optional:    true,
							Description: "Path to kubeconfig file (for Generic K8S deployments).",
						},
						"context": {
							Type:        schema.TypeString,
							Optional:    true,
							Description: "Kubeconfig context to use (for Generic K8S deployments).",
						},
					},
				},
			},

			// ─── Object Storage (common abstraction) ────────────
			"object_storage": {
				Type:        schema.TypeList,
				Optional:    true,
				MaxItems:    1,
				Description: "Object storage configuration (S3, GCS, Azure Blob, or S3-compatible).",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"bucket_name": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "The bucket name (e.g., my-bucket for S3, gs://my-bucket for GCS).",
						},
						"region": {
							Type:        schema.TypeString,
							Optional:    true,
							ForceNew:    true,
							Description: "The bucket region (if different from cloud region).",
						},
						"endpoint": {
							Type:        schema.TypeString,
							Optional:    true,
							ForceNew:    true,
							Description: "Custom S3-compatible endpoint (for MinIO, etc.).",
						},
					},
				},
			},

			// ─── File Storage (EFS, Filestore, etc.) ──────────────
			"file_storage": {
				Type:        schema.TypeList,
				Optional:    true,
				MaxItems:    1,
				Description: "File storage configuration (EFS, Filestore, etc.).",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"file_storage_id": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "The file storage ID (EFS ID, Filestore name, etc.).",
						},
						"mount_path": {
							Type:        schema.TypeString,
							Optional:    true,
							Default:     "/mnt/shared",
							Description: "The mount path for the file storage.",
						},
						"mount_targets": {
							Type:        schema.TypeList,
							Optional:    true,
							Description: "List of mount targets with address and optional zone.",
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"address": {
										Type:        schema.TypeString,
										Required:    true,
										Description: "The IP address or DNS name of the mount target.",
									},
									"zone": {
										Type:        schema.TypeString,
										Optional:    true,
										Description: "The zone of the mount target (optional).",
									},
								},
							},
						},
					},
				},
			},

			// ─── Computed Fields ────────────────────────────────
			"cloud_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The unique cloud ID assigned by Anyscale.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The current status of the cloud (e.g., ready, pending).",
			},
			"state": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The current state of the cloud (e.g., ACTIVE, FAILED).",
			},
		},
	}
}

// ─── Helper Functions ───────────────────────────────────────────────────────

// ExpandAWSConfig extracts AWS configuration from Terraform state
func ExpandAWSConfig(d *schema.ResourceData) *AWSConfig {
	v, ok := d.GetOk("aws_config")
	if !ok || len(v.([]any)) == 0 {
		return nil
	}

	config := v.([]any)[0].(map[string]any)

	awsConfig := &AWSConfig{
		VPCID:             config["vpc_id"].(string),
		AnyscaleIAMRoleID: config["controlplane_iam_role_arn"].(string),
		ClusterIAMRoleID:  config["dataplane_iam_role_arn"].(string),
	}

	// Handle subnet_ids_to_az map (preferred) or subnet_ids list
	if subnetAZMap, ok := config["subnet_ids_to_az"].(map[string]any); ok && len(subnetAZMap) > 0 {
		// Extract subnet IDs and zones from the map
		awsConfig.SubnetIDs = make([]string, 0, len(subnetAZMap))
		awsConfig.Zones = make([]string, 0, len(subnetAZMap))
		for subnetID, az := range subnetAZMap {
			awsConfig.SubnetIDs = append(awsConfig.SubnetIDs, subnetID)
			awsConfig.Zones = append(awsConfig.Zones, az.(string))
		}
	} else if subnetIDs, ok := config["subnet_ids"].([]any); ok && len(subnetIDs) > 0 {
		// Fallback to subnet_ids list (without zones)
		awsConfig.SubnetIDs = make([]string, len(subnetIDs))
		for i, v := range subnetIDs {
			awsConfig.SubnetIDs[i] = v.(string)
		}
	}

	// Security Group IDs
	if sgIDs, ok := config["security_group_ids"].([]any); ok {
		awsConfig.SecurityGroupIDs = make([]string, len(sgIDs))
		for i, v := range sgIDs {
			awsConfig.SecurityGroupIDs[i] = v.(string)
		}
	}

	// Optional fields
	if externalID, ok := config["external_id"].(string); ok && externalID != "" {
		awsConfig.ExternalID = externalID
	}

	if memoryDBName, ok := config["memorydb_cluster_name"].(string); ok && memoryDBName != "" {
		awsConfig.MemoryDBClusterName = &memoryDBName
	}

	return awsConfig
}

// ExpandObjectStorage extracts object storage configuration from Terraform state
func ExpandObjectStorage(d *schema.ResourceData) *ObjectStorage {
	v, ok := d.GetOk("object_storage")
	if !ok || len(v.([]any)) == 0 {
		return nil
	}

	config := v.([]any)[0].(map[string]any)

	storage := &ObjectStorage{
		BucketName: config["bucket_name"].(string),
	}

	if region, ok := config["region"].(string); ok && region != "" {
		storage.Region = &region
	}

	if endpoint, ok := config["endpoint"].(string); ok && endpoint != "" {
		storage.Endpoint = &endpoint
	}

	return storage
}

// ExpandFileStorage extracts file storage configuration from Terraform state
func ExpandFileStorage(d *schema.ResourceData) *FileStorage {
	v, ok := d.GetOk("file_storage")
	if !ok || len(v.([]any)) == 0 {
		return nil
	}

	config := v.([]any)[0].(map[string]any)

	storage := &FileStorage{
		FileStorageID: config["file_storage_id"].(string),
	}

	if mountPath, ok := config["mount_path"].(string); ok && mountPath != "" {
		storage.MountPath = mountPath
	}

	// Handle mount_targets list of objects
	if mountTargets, ok := config["mount_targets"].([]any); ok && len(mountTargets) > 0 {
		storage.MountTargets = make([]MountTarget, len(mountTargets))
		for i, v := range mountTargets {
			if targetMap, ok := v.(map[string]any); ok {
				target := MountTarget{}
				if addr, ok := targetMap["address"].(string); ok {
					target.Address = addr
				}
				if zone, ok := targetMap["zone"].(string); ok {
					target.Zone = zone
				}
				storage.MountTargets[i] = target
			}
		}
	}

	return storage
}

// ExpandGCPConfig extracts GCP configuration from Terraform state
func ExpandGCPConfig(d *schema.ResourceData) *GCPConfig {
	v, ok := d.GetOk("gcp_config")
	if !ok || len(v.([]any)) == 0 {
		return nil
	}

	config := v.([]any)[0].(map[string]any)

	gcpConfig := &GCPConfig{}

	// Required fields - handle nil values safely
	if projectID, ok := config["project_id"].(string); ok {
		gcpConfig.ProjectID = projectID
	}
	if providerName, ok := config["provider_name"].(string); ok {
		gcpConfig.ProviderName = providerName
	}
	if vpcName, ok := config["vpc_name"].(string); ok {
		gcpConfig.VPCName = vpcName
	}
	if controlplaneSA, ok := config["controlplane_service_account_email"].(string); ok {
		gcpConfig.AnyscaleServiceAccountEmail = controlplaneSA
	}
	if dataplaneSA, ok := config["dataplane_service_account_email"].(string); ok {
		gcpConfig.ClusterServiceAccountEmail = dataplaneSA
	}

	// Handle subnet_names list - filter out nil values
	if subnetNames, ok := config["subnet_names"].([]any); ok && len(subnetNames) > 0 {
		var validNames []string
		for _, v := range subnetNames {
			if s, ok := v.(string); ok && s != "" {
				validNames = append(validNames, s)
			}
		}
		gcpConfig.SubnetNames = validNames
	}

	// Handle firewall_policy_names list - filter out nil values
	if fwPolicies, ok := config["firewall_policy_names"].([]any); ok && len(fwPolicies) > 0 {
		var validPolicies []string
		for _, v := range fwPolicies {
			if s, ok := v.(string); ok && s != "" {
				validPolicies = append(validPolicies, s)
			}
		}
		gcpConfig.FirewallPolicyNames = validPolicies
	}

	// Optional fields
	if hostProjectID, ok := config["host_project_id"].(string); ok && hostProjectID != "" {
		gcpConfig.HostProjectID = hostProjectID
	}

	if memorystoreName, ok := config["memorystore_instance_name"].(string); ok && memorystoreName != "" {
		gcpConfig.MemorystoreInstanceName = memorystoreName
	}

	return gcpConfig
}

// GetNetworkingMode determines networking mode based on is_private_cloud
func GetNetworkingMode(d *schema.ResourceData) string {
	if d.Get("is_private_cloud").(bool) {
		return "PRIVATE"
	}
	return "PUBLIC"
}

// ─── CRUD Operations ────────────────────────────────────────────────────────

func resourceCloudCreate(ctx context.Context, d *schema.ResourceData, m any) diag.Diagnostics {
	client := m.(*Client)

	name := d.Get("name").(string)
	provider := d.Get("cloud_provider").(string)
	region := d.Get("region").(string)
	computeStack := d.Get("compute_stack").(string)
	networkingMode := GetNetworkingMode(d)

	log.Printf("[INFO] Creating Anyscale Cloud: name=%s, provider=%s, region=%s, compute_stack=%s",
		name, provider, region, computeStack)

	// Get credentials based on provider type
	var credentials string
	switch strings.ToUpper(provider) {
	case "AWS":
		if awsConfig := ExpandAWSConfig(d); awsConfig != nil {
			credentials = awsConfig.AnyscaleIAMRoleID
		}
	case "GCP":
		if gcpConfig := ExpandGCPConfig(d); gcpConfig != nil {
			// For GCP, credentials must be a JSON object with provider_id, project_id, service_account_email
			gcpCreds := map[string]string{
				"provider_id":           gcpConfig.ProviderName,
				"project_id":            gcpConfig.ProjectID,
				"service_account_email": gcpConfig.AnyscaleServiceAccountEmail,
			}
			if gcpConfig.HostProjectID != "" {
				gcpCreds["host_project_id"] = gcpConfig.HostProjectID
			}
			credsJSON, err := json.Marshal(gcpCreds)
			if err != nil {
				return diag.FromErr(fmt.Errorf("failed to marshal GCP credentials: %w", err))
			}
			credentials = string(credsJSON)
		}
	}

	// Step 1: Create the cloud with required fields
	createReq := CreateCloudRequest{
		Name:        name,
		Provider:    provider,
		Region:      region,
		Credentials: credentials,
	}

	jsonData, err := json.Marshal(createReq)
	if err != nil {
		return diag.FromErr(err)
	}

	log.Printf("[DEBUG] POST /api/v2/clouds - Request: %s", string(jsonData))

	resp, err := client.DoRequest("POST", "/api/v2/clouds", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[ERROR] Failed to create cloud: %v", err)
		return diag.FromErr(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return diag.FromErr(err)
	}

	log.Printf("[DEBUG] POST /api/v2/clouds - Response Status: %d, Body: %s", resp.StatusCode, string(body))

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return diag.Errorf("failed to create cloud: %s - %s", resp.Status, string(body))
	}

	var cloudResp CloudResponse
	if err := json.Unmarshal(body, &cloudResp); err != nil {
		return diag.FromErr(err)
	}

	cloudID := cloudResp.Result.ID
	d.SetId(cloudID)
	d.Set("cloud_id", cloudID)

	log.Printf("[INFO] Cloud created successfully: cloud_id=%s", cloudID)

	// Step 2: Build and add cloud resource/deployment
	deployReq := CloudDeploymentRequest{
		Name:           fmt.Sprintf("%s-%s-%s", strings.ToLower(computeStack), strings.ToLower(provider), strings.ToLower(region)),
		Provider:       provider,
		ComputeStack:   computeStack,
		Region:         region,
		NetworkingMode: networkingMode,
	}

	// Add cloud-specific configuration based on provider
	switch strings.ToUpper(provider) {
	case "AWS":
		awsConfig := ExpandAWSConfig(d)
		if awsConfig == nil {
			return diag.Errorf("aws_config is required when cloud_provider is AWS")
		}
		deployReq.AWSConfig = awsConfig

		// Add object storage if configured
		if objStorage := ExpandObjectStorage(d); objStorage != nil {
			// Ensure S3 bucket has proper prefix
			bucketName := objStorage.BucketName
			if !strings.HasPrefix(bucketName, "s3://") {
				bucketName = "s3://" + bucketName
			}
			deployReq.ObjectStorage = &ObjectStorage{
				BucketName: bucketName,
				Region:     objStorage.Region,
				Endpoint:   objStorage.Endpoint,
			}
		}

		// Add file storage (EFS) if configured
		if fileStorage := ExpandFileStorage(d); fileStorage != nil {
			deployReq.FileStorage = fileStorage
		}

	case "GCP":
		gcpConfig := ExpandGCPConfig(d)
		if gcpConfig == nil {
			return diag.Errorf("gcp_config is required when cloud_provider is GCP")
		}
		deployReq.GCPConfig = gcpConfig

		// Add object storage if configured
		if objStorage := ExpandObjectStorage(d); objStorage != nil {
			// Ensure GCS bucket has proper prefix
			bucketName := objStorage.BucketName
			if !strings.HasPrefix(bucketName, "gs://") {
				bucketName = "gs://" + bucketName
			}
			deployReq.ObjectStorage = &ObjectStorage{
				BucketName: bucketName,
				Region:     objStorage.Region,
				Endpoint:   objStorage.Endpoint,
			}
		}

		// Add file storage (Filestore) if configured
		if fileStorage := ExpandFileStorage(d); fileStorage != nil {
			deployReq.FileStorage = fileStorage
		}

	case "AZURE":
		// Azure config expansion would go here
		log.Printf("[WARN] Azure configuration not fully implemented yet")

	case "GENERIC":
		// Generic K8S config expansion would go here
		log.Printf("[WARN] Generic configuration not fully implemented yet")
	}

	deployJSON, err := json.Marshal(deployReq)
	if err != nil {
		return diag.FromErr(err)
	}

	log.Printf("[INFO] Adding cloud resource/deployment to cloud_id=%s", cloudID)
	log.Printf("[DEBUG] PUT /api/v2/clouds/%s/add_resource - Request: %s", cloudID, string(deployJSON))

	deployResp, err := client.DoRequest("PUT", fmt.Sprintf("/api/v2/clouds/%s/add_resource", cloudID), bytes.NewBuffer(deployJSON))
	if err != nil {
		log.Printf("[ERROR] Failed to add cloud resource: %v", err)
		return diag.FromErr(err)
	}
	defer deployResp.Body.Close()

	deployBody, err := io.ReadAll(deployResp.Body)
	if err != nil {
		return diag.FromErr(err)
	}

	log.Printf("[DEBUG] PUT /api/v2/clouds/%s/add_resource - Response Status: %d, Body: %s", cloudID, deployResp.StatusCode, string(deployBody))

	if deployResp.StatusCode != http.StatusOK {
		return diag.Errorf("failed to add cloud resource: %s - %s", deployResp.Status, string(deployBody))
	}

	log.Printf("[INFO] Cloud resource added successfully, waiting for cloud to be ready...")

	// Get timeout from configuration
	createTimeout := d.Timeout(schema.TimeoutCreate)

	// Wait for cloud to be ready
	if err := waitForCloudReady(ctx, client, cloudID, createTimeout); err != nil {
		log.Printf("[ERROR] Failed waiting for cloud to be ready: %v", err)
		return diag.FromErr(err)
	}

	log.Printf("[INFO] Cloud is ready: cloud_id=%s", cloudID)

	return resourceCloudRead(ctx, d, m)
}

func resourceCloudRead(ctx context.Context, d *schema.ResourceData, m any) diag.Diagnostics {
	client := m.(*Client)
	var diags diag.Diagnostics

	cloudID := d.Id()

	log.Printf("[INFO] Reading Anyscale Cloud: cloud_id=%s", cloudID)

	resp, err := client.DoRequest("GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
	if err != nil {
		return diag.FromErr(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		log.Printf("[WARN] Cloud not found, removing from state: cloud_id=%s", cloudID)
		d.SetId("")
		return diags
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return diag.FromErr(err)
	}

	if resp.StatusCode != http.StatusOK {
		return diag.Errorf("failed to read cloud: %s - %s", resp.Status, string(body))
	}

	var cloudResp CloudResponse
	if err := json.Unmarshal(body, &cloudResp); err != nil {
		return diag.FromErr(err)
	}

	cloud := cloudResp.Result

	// Set computed and common fields
	d.Set("cloud_id", cloud.ID)
	d.Set("name", cloud.Name)
	d.Set("cloud_provider", cloud.Provider)
	d.Set("region", cloud.Region)
	d.Set("compute_stack", cloud.ComputeStack)
	d.Set("status", cloud.Status)
	d.Set("state", cloud.State)
	d.Set("is_private_cloud", cloud.IsPrivateCloud)
	d.Set("auto_add_user", cloud.AutoAddUser)

	log.Printf("[INFO] Cloud read successfully: cloud_id=%s, status=%s, state=%s", cloudID, cloud.Status, cloud.State)

	return diags
}

func resourceCloudUpdate(ctx context.Context, d *schema.ResourceData, m any) diag.Diagnostics {
	// client := m.(*Client) // Uncomment when implementing update API calls

	cloudID := d.Id()

	log.Printf("[INFO] Updating Anyscale Cloud: cloud_id=%s", cloudID)

	// Check what changed - most fields are ForceNew, so limited updates
	if d.HasChange("name") {
		// TODO: Implement name update via PATCH endpoint if API supports it
		log.Printf("[WARN] Cloud name update not yet implemented")
	}

	if d.HasChange("auto_add_user") {
		// TODO: Implement auto_add_user update if API supports it
		log.Printf("[WARN] auto_add_user update not yet implemented")
	}

	return resourceCloudRead(ctx, d, m)
}

func resourceCloudDelete(ctx context.Context, d *schema.ResourceData, m any) diag.Diagnostics {
	client := m.(*Client)
	var diags diag.Diagnostics

	cloudID := d.Id()

	log.Printf("[INFO] Deleting Anyscale Cloud: cloud_id=%s", cloudID)

	resp, err := client.DoRequest("DELETE", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
	if err != nil {
		log.Printf("[ERROR] Failed to delete cloud: %v", err)
		return diag.FromErr(err)
	}
	defer resp.Body.Close()

	log.Printf("[DEBUG] DELETE /api/v2/clouds/%s - Response Status: %d", cloudID, resp.StatusCode)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[ERROR] Failed to delete cloud: %s - %s", resp.Status, string(body))
		return diag.Errorf("failed to delete cloud: %s - %s", resp.Status, string(body))
	}

	log.Printf("[INFO] Cloud deleted successfully: cloud_id=%s", cloudID)

	d.SetId("")
	return diags
}

func waitForCloudReady(ctx context.Context, client *Client, cloudID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollCount := 0

	log.Printf("[INFO] Waiting for cloud %s to be ready (timeout: %v)", cloudID, timeout)

	for time.Now().Before(deadline) {
		pollCount++
		log.Printf("[DEBUG] Poll #%d - GET /api/v2/clouds/%s", pollCount, cloudID)

		resp, err := client.DoRequest("GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
		if err != nil {
			log.Printf("[ERROR] Failed to check cloud status: %v", err)
			return err
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}

		log.Printf("[DEBUG] Poll #%d - Response Status: %d", pollCount, resp.StatusCode)

		if resp.StatusCode != http.StatusOK {
			log.Printf("[ERROR] Poll #%d - Failed response: %s", pollCount, string(body))
			return fmt.Errorf("failed to check cloud status: %s - %s", resp.Status, string(body))
		}

		var cloudResp CloudResponse
		if err := json.Unmarshal(body, &cloudResp); err != nil {
			return err
		}

		status := cloudResp.Result.Status
		state := cloudResp.Result.State

		log.Printf("[INFO] Poll #%d - Cloud status: %s, state: %s", pollCount, status, state)

		// Also check cloud resources for debugging
		resourcesResp, err := client.DoRequest("GET", fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), nil)
		if err == nil {
			resourcesBody, _ := io.ReadAll(resourcesResp.Body)
			resourcesResp.Body.Close()
			log.Printf("[DEBUG] Poll #%d - Cloud resources: %s", pollCount, string(resourcesBody))
		}

		if status == "ready" && state == "ACTIVE" {
			log.Printf("[INFO] Cloud is ready after %d polls", pollCount)
			return nil
		}

		if status == "failed" || state == "FAILED" {
			log.Printf("[ERROR] Cloud creation failed - status: %s, state: %s", status, state)
			return fmt.Errorf("cloud creation failed with status: %s, state: %s", status, state)
		}

		log.Printf("[DEBUG] Cloud not ready yet, waiting 15 seconds before next poll...")

		select {
		case <-ctx.Done():
			log.Printf("[ERROR] Context cancelled while waiting for cloud")
			return ctx.Err()
		case <-time.After(15 * time.Second):
			// Continue polling
		}
	}

	log.Printf("[ERROR] Timeout after %d polls waiting for cloud to be ready", pollCount)
	return fmt.Errorf("timeout waiting for cloud to be ready")
}
