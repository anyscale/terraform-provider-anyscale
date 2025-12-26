package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// ResourceCloudResource returns the schema for the anyscale_cloud_resource resource
func ResourceCloudResource() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceCloudResourceCreate,
		ReadContext:   resourceCloudResourceRead,
		UpdateContext: resourceCloudResourceUpdate,
		DeleteContext: resourceCloudResourceDelete,
		Importer: &schema.ResourceImporter{
			StateContext: resourceCloudResourceImportState,
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(30 * time.Minute),
			Update: schema.DefaultTimeout(30 * time.Minute),
			Delete: schema.DefaultTimeout(30 * time.Minute),
		},

		Schema: map[string]*schema.Schema{
			// ─── Reference to Parent Cloud ─────────────────────────
			"cloud_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The cloud ID to attach this resource to.",
			},

			// ─── Resource Identity ─────────────────────────────────
			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
				Description: "The name of the cloud resource. Auto-generated if not provided.",
			},

			// ─── Compute Configuration ─────────────────────────────
			"compute_stack": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Compute stack type: VM or K8S.",
			},
			"region": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The region for this cloud resource.",
			},
			"is_private": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				ForceNew:    true,
				Description: "Whether this is a private resource (private networking).",
			},

			// ─── AWS Configuration (nested) ─────────────────────
			"aws_config": {
				Type:        schema.TypeList,
				Optional:    true,
				MaxItems:    1,
				Description: "AWS-specific configuration.",
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
						"memorydb_cluster_arn": {
							Type:        schema.TypeString,
							Optional:    true,
							ForceNew:    true,
							Description: "MemoryDB cluster ARN.",
						},
						"memorydb_cluster_endpoint": {
							Type:        schema.TypeString,
							Optional:    true,
							ForceNew:    true,
							Description: "MemoryDB cluster endpoint address.",
						},
					},
				},
			},

			// ─── GCP Configuration (nested) ─────────────────────
			"gcp_config": {
				Type:        schema.TypeList,
				Optional:    true,
				MaxItems:    1,
				Description: "GCP-specific configuration.",
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
							Description: "Workload Identity Federation provider name.",
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
							Description: "Service account email for Anyscale control plane.",
						},
						"dataplane_service_account_email": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "Service account email for Ray cluster nodes.",
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
						"memorystore_endpoint": {
							Type:        schema.TypeString,
							Optional:    true,
							ForceNew:    true,
							Description: "Memorystore endpoint address.",
						},
					},
				},
			},

			// ─── Object Storage (common abstraction) ────────────
			"object_storage": {
				Type:        schema.TypeList,
				Optional:    true,
				MaxItems:    1,
				Description: "Object storage configuration (S3, GCS).",
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
			"cloud_resource_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The unique cloud resource ID assigned by Anyscale.",
			},
			"cloud_deployment_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The cloud deployment ID assigned by Anyscale.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The current status of the cloud resource.",
			},
			"is_default": {
				Type:        schema.TypeBool,
				Computed:    true,
				Description: "Whether this is the default resource for the cloud.",
			},
		},
	}
}

// ─── Helper Functions ───────────────────────────────────────────────────────

// parseCloudResourceID parses a composite ID in format "cloud_id:resource_name"
func parseCloudResourceID(id string) (cloudID, resourceName string, err error) {
	parts := strings.SplitN(id, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid cloud resource ID format: expected 'cloud_id:resource_name', got '%s'", id)
	}
	return parts[0], parts[1], nil
}

// generateResourceName creates a resource name based on config
func generateResourceName(computeStack, provider, region string) string {
	return fmt.Sprintf("%s-%s-%s",
		strings.ToLower(computeStack),
		strings.ToLower(provider),
		strings.ToLower(region))
}

// getProviderFromResourceData infers the cloud provider from aws_config or gcp_config
func getProviderFromResourceData(d *schema.ResourceData) string {
	if _, ok := d.GetOk("aws_config"); ok {
		return "AWS"
	}
	if _, ok := d.GetOk("gcp_config"); ok {
		return "GCP"
	}
	return ""
}

// getNetworkingModeFromResource determines networking mode based on is_private
func getNetworkingModeFromResource(d *schema.ResourceData) string {
	if d.Get("is_private").(bool) {
		return "PRIVATE"
	}
	return "PUBLIC"
}

// ExpandAWSConfigFromResource extracts AWS configuration from cloud_resource schema
func ExpandAWSConfigFromResource(d *schema.ResourceData) *AWSConfig {
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
		awsConfig.SubnetIDs = make([]string, 0, len(subnetAZMap))
		awsConfig.Zones = make([]string, 0, len(subnetAZMap))
		for subnetID, az := range subnetAZMap {
			awsConfig.SubnetIDs = append(awsConfig.SubnetIDs, subnetID)
			awsConfig.Zones = append(awsConfig.Zones, az.(string))
		}
	} else if subnetIDs, ok := config["subnet_ids"].([]any); ok && len(subnetIDs) > 0 {
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
	if memoryDBARN, ok := config["memorydb_cluster_arn"].(string); ok && memoryDBARN != "" {
		awsConfig.MemoryDBClusterARN = &memoryDBARN
	}
	if memoryDBEndpoint, ok := config["memorydb_cluster_endpoint"].(string); ok && memoryDBEndpoint != "" {
		awsConfig.MemoryDBClusterEndpoint = &memoryDBEndpoint
	}

	return awsConfig
}

// ExpandGCPConfigFromResource extracts GCP configuration from cloud_resource schema
func ExpandGCPConfigFromResource(d *schema.ResourceData) *GCPConfig {
	v, ok := d.GetOk("gcp_config")
	if !ok || len(v.([]any)) == 0 {
		return nil
	}

	config := v.([]any)[0].(map[string]any)

	gcpConfig := &GCPConfig{}

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

	if subnetNames, ok := config["subnet_names"].([]any); ok && len(subnetNames) > 0 {
		var validNames []string
		for _, v := range subnetNames {
			if s, ok := v.(string); ok && s != "" {
				validNames = append(validNames, s)
			}
		}
		gcpConfig.SubnetNames = validNames
	}

	if fwPolicies, ok := config["firewall_policy_names"].([]any); ok && len(fwPolicies) > 0 {
		var validPolicies []string
		for _, v := range fwPolicies {
			if s, ok := v.(string); ok && s != "" {
				validPolicies = append(validPolicies, s)
			}
		}
		gcpConfig.FirewallPolicyNames = validPolicies
	}

	if hostProjectID, ok := config["host_project_id"].(string); ok && hostProjectID != "" {
		gcpConfig.HostProjectID = hostProjectID
	}
	if memorystoreName, ok := config["memorystore_instance_name"].(string); ok && memorystoreName != "" {
		gcpConfig.MemorystoreInstanceName = memorystoreName
	}
	if memorystoreEndpoint, ok := config["memorystore_endpoint"].(string); ok && memorystoreEndpoint != "" {
		gcpConfig.MemorystoreEndpoint = memorystoreEndpoint
	}

	return gcpConfig
}

// ExpandObjectStorageFromResource extracts object storage configuration from cloud_resource schema
func ExpandObjectStorageFromResource(d *schema.ResourceData) *ObjectStorage {
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

// ExpandFileStorageFromResource extracts file storage configuration from cloud_resource schema
func ExpandFileStorageFromResource(d *schema.ResourceData) *FileStorage {
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

// findDefaultCloudResource checks if the cloud has a single default resource.
// This is used to detect if we should update an existing default resource instead of creating a new one.
// Returns the default resource if found, nil if not found or if there are multiple resources.
func findDefaultCloudResource(client *Client, cloudID string) (*CloudDeploymentResult, error) {
	resp, err := client.DoRequest("GET", fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list cloud resources: %s - %s", resp.Status, string(body))
	}

	var deploymentsResp CloudDeploymentsResponse
	if err := json.Unmarshal(body, &deploymentsResp); err != nil {
		return nil, err
	}

	// Only return the default resource if there's exactly one resource
	// This indicates we're dealing with a fresh empty cloud with just the placeholder
	if len(deploymentsResp.Results) == 1 && deploymentsResp.Results[0].IsDefault {
		log.Printf("[DEBUG] Found single default resource: %s", deploymentsResp.Results[0].Name)
		return &deploymentsResp.Results[0], nil
	}

	log.Printf("[DEBUG] Cloud has %d resources, not updating default", len(deploymentsResp.Results))
	return nil, nil
}

// ─── CRUD Operations ────────────────────────────────────────────────────────

func resourceCloudResourceCreate(ctx context.Context, d *schema.ResourceData, m any) diag.Diagnostics {
	client := m.(*Client)

	cloudID := d.Get("cloud_id").(string)
	region := d.Get("region").(string)
	computeStack := d.Get("compute_stack").(string)
	networkingMode := getNetworkingModeFromResource(d)
	provider := getProviderFromResourceData(d)

	if provider == "" {
		return diag.Errorf("either aws_config or gcp_config must be specified")
	}

	// Generate or use provided name
	name := d.Get("name").(string)
	if name == "" {
		name = generateResourceName(computeStack, provider, region)
	}

	log.Printf("[INFO] Creating Anyscale Cloud Resource: cloud_id=%s, name=%s, provider=%s, region=%s",
		cloudID, name, provider, region)

	// Check if there's an existing default resource that we should update instead of create
	// This handles the case where an empty cloud was created with a placeholder default resource
	existingDefault, err := findDefaultCloudResource(client, cloudID)
	if err != nil {
		log.Printf("[WARN] Failed to check for existing default resource: %v", err)
		// Continue with creation - the API will handle conflicts
	} else if existingDefault != nil {
		log.Printf("[INFO] Found existing default resource %s, will update it instead of creating new", existingDefault.Name)
		// Use the existing default resource's name
		name = existingDefault.Name
	}

	// Build deployment request
	deployReq := CloudDeploymentRequest{
		Name:           name,
		Provider:       provider,
		ComputeStack:   computeStack,
		Region:         region,
		NetworkingMode: networkingMode,
	}

	// Add provider-specific configuration
	switch provider {
	case "AWS":
		awsConfig := ExpandAWSConfigFromResource(d)
		if awsConfig == nil {
			return diag.Errorf("aws_config is required when using AWS provider")
		}
		deployReq.AWSConfig = awsConfig

		// Add object storage with S3 prefix
		if objStorage := ExpandObjectStorageFromResource(d); objStorage != nil {
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

		// Add file storage (EFS)
		if fileStorage := ExpandFileStorageFromResource(d); fileStorage != nil {
			deployReq.FileStorage = fileStorage
		}

	case "GCP":
		gcpConfig := ExpandGCPConfigFromResource(d)
		if gcpConfig == nil {
			return diag.Errorf("gcp_config is required when using GCP provider")
		}
		deployReq.GCPConfig = gcpConfig

		// Add object storage with GCS prefix
		if objStorage := ExpandObjectStorageFromResource(d); objStorage != nil {
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

		// Add file storage (Filestore)
		if fileStorage := ExpandFileStorageFromResource(d); fileStorage != nil {
			deployReq.FileStorage = fileStorage
		}
	}

	jsonData, err := json.Marshal(deployReq)
	if err != nil {
		return diag.FromErr(err)
	}

	log.Printf("[DEBUG] PUT /api/v2/clouds/%s/add_resource - Request: %s", cloudID, string(jsonData))

	resp, err := client.DoRequest("PUT", fmt.Sprintf("/api/v2/clouds/%s/add_resource", cloudID), bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[ERROR] Failed to add cloud resource: %v", err)
		return diag.FromErr(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return diag.FromErr(err)
	}

	log.Printf("[DEBUG] PUT /api/v2/clouds/%s/add_resource - Response Status: %d, Body: %s", cloudID, resp.StatusCode, string(body))

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return diag.Errorf("failed to add cloud resource: %s - %s", resp.Status, string(body))
	}

	var deployResp CloudDeploymentResponse
	if err := json.Unmarshal(body, &deployResp); err != nil {
		return diag.FromErr(err)
	}

	// Set composite ID: cloud_id:resource_name
	resourceName := deployResp.Result.Name
	d.SetId(fmt.Sprintf("%s:%s", cloudID, resourceName))
	d.Set("name", resourceName)
	d.Set("cloud_resource_id", deployResp.Result.CloudResourceID)
	d.Set("cloud_deployment_id", deployResp.Result.CloudDeploymentID)

	log.Printf("[INFO] Cloud resource created successfully: id=%s", d.Id())

	// Wait for the parent cloud to become ready
	// The cloud transitions from CREATING/pending to ACTIVE/ready after the first resource is attached
	createTimeout := d.Timeout(schema.TimeoutCreate)
	if err := waitForCloudReady(ctx, client, cloudID, createTimeout); err != nil {
		log.Printf("[ERROR] Failed waiting for parent cloud to be ready: %v", err)
		return diag.FromErr(err)
	}

	return resourceCloudResourceRead(ctx, d, m)
}

func resourceCloudResourceRead(ctx context.Context, d *schema.ResourceData, m any) diag.Diagnostics {
	client := m.(*Client)
	var diags diag.Diagnostics

	cloudID, resourceName, err := parseCloudResourceID(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	log.Printf("[INFO] Reading Anyscale Cloud Resource: cloud_id=%s, name=%s", cloudID, resourceName)

	// Get all resources for the cloud
	resp, err := client.DoRequest("GET", fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), nil)
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
		return diag.Errorf("failed to read cloud resources: %s - %s", resp.Status, string(body))
	}

	var deploymentsResp CloudDeploymentsResponse
	if err := json.Unmarshal(body, &deploymentsResp); err != nil {
		return diag.FromErr(err)
	}

	// Find the resource by name
	var foundResource *CloudDeploymentResult
	for _, r := range deploymentsResp.Results {
		if r.Name == resourceName {
			foundResource = &r
			break
		}
	}

	if foundResource == nil {
		log.Printf("[WARN] Cloud resource not found, removing from state: cloud_id=%s, name=%s", cloudID, resourceName)
		d.SetId("")
		return diags
	}

	// Set state from API response
	d.Set("cloud_id", cloudID)
	d.Set("name", foundResource.Name)
	d.Set("cloud_resource_id", foundResource.CloudResourceID)
	d.Set("cloud_deployment_id", foundResource.CloudDeploymentID)
	d.Set("compute_stack", foundResource.ComputeStack)
	d.Set("region", foundResource.Region)
	d.Set("is_default", foundResource.IsDefault)

	if foundResource.OperatorStatus != nil {
		d.Set("status", *foundResource.OperatorStatus)
	}

	if foundResource.NetworkingMode == "PRIVATE" {
		d.Set("is_private", true)
	} else {
		d.Set("is_private", false)
	}

	log.Printf("[INFO] Cloud resource read successfully: cloud_id=%s, name=%s", cloudID, resourceName)

	return diags
}

func resourceCloudResourceUpdate(ctx context.Context, d *schema.ResourceData, m any) diag.Diagnostics {
	// Most fields are ForceNew, so limited updates possible
	log.Printf("[INFO] Cloud resource update called - most fields are ForceNew")
	return resourceCloudResourceRead(ctx, d, m)
}

func resourceCloudResourceDelete(ctx context.Context, d *schema.ResourceData, m any) diag.Diagnostics {
	client := m.(*Client)
	var diags diag.Diagnostics

	cloudID, resourceName, err := parseCloudResourceID(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	log.Printf("[INFO] Deleting Anyscale Cloud Resource: cloud_id=%s, name=%s", cloudID, resourceName)

	// Check if this is the default/primary resource
	// Primary resources cannot be deleted independently - they are deleted with the cloud
	if d.Get("is_default").(bool) {
		log.Printf("[INFO] Cloud resource %s is the primary resource - it will be deleted when the cloud is deleted", resourceName)
		d.SetId("")
		return diags
	}

	// DELETE /api/v2/clouds/{cloud_id}/remove_resource?cloud_resource_name=...
	deleteURL := fmt.Sprintf("/api/v2/clouds/%s/remove_resource?cloud_resource_name=%s",
		cloudID, url.QueryEscape(resourceName))

	resp, err := client.DoRequest("DELETE", deleteURL, nil)
	if err != nil {
		log.Printf("[ERROR] Failed to delete cloud resource: %v", err)
		return diag.FromErr(err)
	}
	defer resp.Body.Close()

	log.Printf("[DEBUG] DELETE %s - Response Status: %d", deleteURL, resp.StatusCode)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)
		log.Printf("[ERROR] Failed to delete cloud resource: %s - %s", resp.Status, bodyStr)

		// Handle the case where the API tells us this is a primary resource
		// (in case is_default wasn't properly set in state)
		if resp.StatusCode == http.StatusBadRequest && strings.Contains(bodyStr, "primary resource") {
			log.Printf("[INFO] Cloud resource %s is the primary resource - it will be deleted when the cloud is deleted", resourceName)
			d.SetId("")
			return diags
		}

		return diag.Errorf("failed to delete cloud resource: %s - %s", resp.Status, bodyStr)
	}

	log.Printf("[INFO] Cloud resource deleted successfully: cloud_id=%s, name=%s", cloudID, resourceName)

	d.SetId("")
	return diags
}

func resourceCloudResourceImportState(ctx context.Context, d *schema.ResourceData, m any) ([]*schema.ResourceData, error) {
	// ID format: cloud_id:resource_name
	cloudID, resourceName, err := parseCloudResourceID(d.Id())
	if err != nil {
		return nil, fmt.Errorf("invalid import ID format. Expected 'cloud_id:resource_name', got '%s'", d.Id())
	}

	d.Set("cloud_id", cloudID)
	d.Set("name", resourceName)

	return []*schema.ResourceData{d}, nil
}
