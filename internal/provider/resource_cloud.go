package provider

import (
	"bytes"
	"context"
	"crypto/rand"
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

// generateRandomString generates a random alphanumeric string of the given length
func generateRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	rand.Read(b)
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}

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
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
				Description: "Cloud provider: AWS, GCP, Azure, or Generic. Auto-detected from aws_config/gcp_config, or defaults to AWS for empty clouds.",
			},
			"compute_stack": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Compute stack type: VM or K8S. Required when using embedded config (aws_config/gcp_config).",
			},
			"region": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
				Description: "The region where the cloud is deployed. Auto-detected from config or defaults to us-east-1 for empty clouds.",
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
			"credentials": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Cloud credentials. For AWS: the IAM role ARN. For GCP: the Workload Identity Provider name. Required when using split pattern (empty cloud + cloud_resource).",
			},
			"enable_lineage_tracking": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				Description: "Whether to enable lineage tracking for this cloud.",
			},
			"enable_log_ingestion": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				Description: "Whether to enable aggregated log ingestion for this cloud.",
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
						"memorystore_endpoint": {
							Type:        schema.TypeString,
							Optional:    true,
							ForceNew:    true,
							Description: "Memorystore endpoint address.",
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
						"anyscale_operator_iam_identity": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "The IAM identity for the Anyscale operator. For AWS EKS: IAM role ARN. For GCP GKE: service account email. For Azure AKS: managed identity client ID.",
						},
						"zones": {
							Type:        schema.TypeList,
							Optional:    true,
							ForceNew:    true,
							Elem:        &schema.Schema{Type: schema.TypeString},
							Description: "List of availability zones for the Kubernetes cluster.",
						},
						"namespace": {
							Type:        schema.TypeString,
							Optional:    true,
							Default:     "anyscale",
							Description: "The Kubernetes namespace for Anyscale workloads.",
						},
						"ingress_host": {
							Type:        schema.TypeString,
							Optional:    true,
							Description: "The ingress host for the Anyscale operator (e.g., anyscale.example.com).",
						},
						"cluster_name": {
							Type:        schema.TypeString,
							Optional:    true,
							Description: "The Kubernetes cluster name (EKS, GKE, AKS cluster name).",
						},
						"context": {
							Type:        schema.TypeString,
							Optional:    true,
							Description: "Kubeconfig context to use (for Generic K8S deployments).",
						},
						"kubeconfig_path": {
							Type:        schema.TypeString,
							Optional:    true,
							Description: "Path to kubeconfig file (for Generic K8S deployments).",
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
			"is_empty_cloud": {
				Type:        schema.TypeBool,
				Computed:    true,
				Description: "Whether this cloud was created without embedded resource configuration. Use anyscale_cloud_resource to attach resources separately.",
			},
			"cloud_deployment_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The cloud deployment ID. For K8S clouds, pass this to the Anyscale operator during installation.",
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

	if memoryDBARN, ok := config["memorydb_cluster_arn"].(string); ok && memoryDBARN != "" {
		awsConfig.MemoryDBClusterARN = &memoryDBARN
	}

	if memoryDBEndpoint, ok := config["memorydb_cluster_endpoint"].(string); ok && memoryDBEndpoint != "" {
		awsConfig.MemoryDBClusterEndpoint = &memoryDBEndpoint
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

	if memorystoreEndpoint, ok := config["memorystore_endpoint"].(string); ok && memorystoreEndpoint != "" {
		gcpConfig.MemorystoreEndpoint = memorystoreEndpoint
	}

	return gcpConfig
}

// ExpandKubernetesConfig extracts kubernetes_config and returns only the fields
// that are accepted by the Anyscale API (anyscale_operator_iam_identity and zones).
// Other fields like namespace, ingress_host, etc. are stored in Terraform state
// but not sent to the API.
func ExpandKubernetesConfig(d *schema.ResourceData) *KubernetesConfig {
	v, ok := d.GetOk("kubernetes_config")
	if !ok || len(v.([]any)) == 0 {
		return nil
	}

	config := v.([]any)[0].(map[string]any)

	k8sConfig := &KubernetesConfig{}

	// Only populate fields accepted by the API
	if iamIdentity, ok := config["anyscale_operator_iam_identity"].(string); ok && iamIdentity != "" {
		k8sConfig.AnyscaleOperatorIAMIdentity = iamIdentity
	}

	// Handle zones list
	if zones, ok := config["zones"].([]any); ok && len(zones) > 0 {
		k8sConfig.Zones = make([]string, len(zones))
		for i, z := range zones {
			k8sConfig.Zones[i] = z.(string)
		}
	}

	return k8sConfig
}

// GetNetworkingMode determines networking mode based on is_private_cloud
func GetNetworkingMode(d *schema.ResourceData) string {
	if d.Get("is_private_cloud").(bool) {
		return "PRIVATE"
	}
	return "PUBLIC"
}

// hasEmbeddedResourceConfig checks if the cloud has any embedded resource configuration
// If false, this is an "empty" cloud that expects resources to be added via anyscale_cloud_resource
func hasEmbeddedResourceConfig(d *schema.ResourceData) bool {
	_, hasAWS := d.GetOk("aws_config")
	_, hasGCP := d.GetOk("gcp_config")
	_, hasAzure := d.GetOk("azure_config")
	_, hasK8s := d.GetOk("kubernetes_config")
	_, hasObjStorage := d.GetOk("object_storage")
	_, hasFileStorage := d.GetOk("file_storage")
	return hasAWS || hasGCP || hasAzure || hasK8s || hasObjStorage || hasFileStorage
}

// findCloudByName searches for an existing cloud with the given name.
// Returns the cloud ID if found, empty string if not found.
func findCloudByName(client *Client, name string) (string, error) {
	// Use name filter query parameter if supported, otherwise list and filter
	resp, err := client.DoRequest("GET", fmt.Sprintf("/api/v2/clouds?name=%s", url.QueryEscape(name)), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		// If name filter not supported, try listing all and filtering
		resp2, err := client.DoRequest("GET", "/api/v2/clouds", nil)
		if err != nil {
			return "", err
		}
		defer resp2.Body.Close()

		body, err = io.ReadAll(resp2.Body)
		if err != nil {
			return "", err
		}

		if resp2.StatusCode != http.StatusOK {
			return "", fmt.Errorf("failed to list clouds: %s", string(body))
		}
	}

	var cloudsResp CloudsListResponse
	if err := json.Unmarshal(body, &cloudsResp); err != nil {
		return "", err
	}

	for _, cloud := range cloudsResp.Results {
		if cloud.Name == name {
			return cloud.ID, nil
		}
	}

	return "", nil
}

// ─── CRUD Operations ────────────────────────────────────────────────────────

func resourceCloudCreate(ctx context.Context, d *schema.ResourceData, m any) diag.Diagnostics {
	client := m.(*Client)

	name := d.Get("name").(string)
	provider := d.Get("cloud_provider").(string)
	region := d.Get("region").(string)
	computeStack := d.Get("compute_stack").(string)
	networkingMode := GetNetworkingMode(d)

	// Check if this is an empty cloud pattern (no embedded config)
	isEmptyCloud := !hasEmbeddedResourceConfig(d)

	// Auto-detect or use placeholder values for empty cloud pattern
	if provider == "" {
		// Try to detect from config blocks
		if _, ok := d.GetOk("aws_config"); ok {
			provider = "AWS"
		} else if _, ok := d.GetOk("gcp_config"); ok {
			provider = "GCP"
		} else if _, ok := d.GetOk("azure_config"); ok {
			provider = "Azure"
		} else {
			// Default to AWS for empty cloud pattern
			provider = "AWS"
			log.Printf("[INFO] No cloud_provider specified, using placeholder: %s", provider)
		}
		d.Set("cloud_provider", provider)
	}

	if region == "" {
		// Use placeholder region for empty cloud pattern
		if isEmptyCloud {
			region = "us-east-1"
			log.Printf("[INFO] No region specified for empty cloud, using placeholder: %s", region)
		}
		d.Set("region", region)
	}

	log.Printf("[INFO] Creating Anyscale Cloud: name=%s, provider=%s, region=%s, compute_stack=%s",
		name, provider, region, computeStack)

	// Check if a cloud with this name already exists (handles interrupted creates)
	existingCloudID, err := findCloudByName(client, name)
	if err != nil {
		log.Printf("[WARN] Failed to check for existing cloud: %v", err)
		// Continue with creation - the API will return an error if name exists
	} else if existingCloudID != "" {
		log.Printf("[INFO] Found existing cloud with name %s, adopting id=%s", name, existingCloudID)
		d.SetId(existingCloudID)
		// Check if this is an empty cloud or has resources
		isEmptyCloud := !hasEmbeddedResourceConfig(d)
		d.Set("is_empty_cloud", isEmptyCloud)
		return resourceCloudRead(ctx, d, m)
	}

	// Get credentials - check explicit field first, then extract from config blocks, then use placeholder
	var credentials string
	if creds, ok := d.GetOk("credentials"); ok {
		credentials = creds.(string)
	} else {
		// Try to extract from config blocks (all-in-one pattern)
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
	}

	// If still no credentials, generate unique placeholder for empty cloud pattern
	if credentials == "" {
		uniqueSuffix := generateRandomString(12)
		switch strings.ToUpper(provider) {
		case "AWS":
			credentials = fmt.Sprintf("arn:aws:iam::000000000000:role/anyscale-placeholder-%s", uniqueSuffix)
		case "GCP":
			placeholderCreds := map[string]string{
				"provider_id":           fmt.Sprintf("projects/000000000000/locations/global/workloadIdentityPools/placeholder-%s/providers/placeholder", uniqueSuffix),
				"project_id":            "placeholder-project",
				"service_account_email": fmt.Sprintf("placeholder-%s@placeholder-project.iam.gserviceaccount.com", uniqueSuffix),
			}
			credsJSON, _ := json.Marshal(placeholderCreds)
			credentials = string(credsJSON)
		default:
			credentials = fmt.Sprintf("placeholder-%s", uniqueSuffix)
		}
		log.Printf("[INFO] Using placeholder credentials for empty cloud pattern")
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

	log.Printf("[INFO] Cloud created successfully: id=%s", cloudID)

	// Set empty cloud flag (already computed at start of function)
	d.Set("is_empty_cloud", isEmptyCloud)

	if isEmptyCloud {
		// Skip add_resource call - resources will be added via anyscale_cloud_resource
		log.Printf("[INFO] Created empty cloud %s - resources should be added via anyscale_cloud_resource", cloudID)
		return resourceCloudRead(ctx, d, m)
	}

	// For all-in-one pattern, compute_stack is required
	if computeStack == "" {
		return diag.Errorf("compute_stack is required when using embedded config (aws_config/gcp_config)")
	}

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
		// For K8S compute stack, aws_config is NOT required
		// For VM compute stack, aws_config IS required
		if computeStack == "K8S" {
			// K8S requires: kubernetes_config, object_storage
			// aws_config is optional for K8S
			k8sConfig := ExpandKubernetesConfig(d)
			if k8sConfig == nil {
				return diag.Errorf("kubernetes_config is required when compute_stack is K8S")
			}
			if k8sConfig.AnyscaleOperatorIAMIdentity == "" {
				return diag.Errorf("kubernetes_config.anyscale_operator_iam_identity is required for AWS K8S clouds")
			}
			deployReq.KubernetesConfig = k8sConfig

			// object_storage is required for K8S
			objStorage := ExpandObjectStorage(d)
			if objStorage == nil {
				return diag.Errorf("object_storage is required when compute_stack is K8S")
			}
			bucketName := objStorage.BucketName
			if !strings.HasPrefix(bucketName, "s3://") {
				bucketName = "s3://" + bucketName
			}
			deployReq.ObjectStorage = &ObjectStorage{
				BucketName: bucketName,
				Region:     objStorage.Region,
				Endpoint:   objStorage.Endpoint,
			}

			// aws_config is optional for K8S - add if provided
			if awsConfig := ExpandAWSConfig(d); awsConfig != nil {
				deployReq.AWSConfig = awsConfig
			}

			// file_storage (EFS) is optional
			if fileStorage := ExpandFileStorage(d); fileStorage != nil {
				deployReq.FileStorage = fileStorage
			}
		} else {
			// VM compute stack - aws_config is required
			awsConfig := ExpandAWSConfig(d)
			if awsConfig == nil {
				return diag.Errorf("aws_config is required when cloud_provider is AWS and compute_stack is VM")
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
		}

	case "GCP":
		// For K8S compute stack, gcp_config is NOT required
		// For VM compute stack, gcp_config IS required
		if computeStack == "K8S" {
			// K8S requires: kubernetes_config, object_storage
			// gcp_config is optional for K8S
			k8sConfig := ExpandKubernetesConfig(d)
			if k8sConfig == nil {
				return diag.Errorf("kubernetes_config is required when compute_stack is K8S")
			}
			if k8sConfig.AnyscaleOperatorIAMIdentity == "" {
				return diag.Errorf("kubernetes_config.anyscale_operator_iam_identity is required for GCP K8S clouds")
			}
			deployReq.KubernetesConfig = k8sConfig

			// object_storage is required for K8S
			objStorage := ExpandObjectStorage(d)
			if objStorage == nil {
				return diag.Errorf("object_storage is required when compute_stack is K8S")
			}
			bucketName := objStorage.BucketName
			if !strings.HasPrefix(bucketName, "gs://") {
				bucketName = "gs://" + bucketName
			}
			deployReq.ObjectStorage = &ObjectStorage{
				BucketName: bucketName,
				Region:     objStorage.Region,
				Endpoint:   objStorage.Endpoint,
			}

			// gcp_config is optional for K8S - add if provided
			if gcpConfig := ExpandGCPConfig(d); gcpConfig != nil {
				deployReq.GCPConfig = gcpConfig
			}

			// file_storage (Filestore) is optional
			if fileStorage := ExpandFileStorage(d); fileStorage != nil {
				deployReq.FileStorage = fileStorage
			}
		} else {
			// VM compute stack - gcp_config is required
			gcpConfig := ExpandGCPConfig(d)
			if gcpConfig == nil {
				return diag.Errorf("gcp_config is required when cloud_provider is GCP and compute_stack is VM")
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
		}

	case "AZURE":
		// Azure config expansion would go here
		log.Printf("[WARN] Azure configuration not fully implemented yet")

	case "GENERIC":
		// Generic K8S config expansion would go here
		log.Printf("[WARN] Generic configuration not fully implemented yet")
	}

	// Set cloud-level boolean settings
	if v, ok := d.GetOk("auto_add_user"); ok {
		autoAddUser := v.(bool)
		deployReq.AutoAddUser = &autoAddUser
	}
	if v, ok := d.GetOk("enable_lineage_tracking"); ok {
		lineageTracking := v.(bool)
		deployReq.LineageTrackingEnabled = &lineageTracking
	}
	if v, ok := d.GetOk("enable_log_ingestion"); ok {
		logIngestion := v.(bool)
		deployReq.IsAggregatedLogsEnabled = &logIngestion
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

	// Parse response to get cloud_deployment_id
	var deployResult CloudDeploymentResponse
	if err := json.Unmarshal(deployBody, &deployResult); err != nil {
		log.Printf("[WARN] Failed to parse add_resource response: %v", err)
	} else if deployResult.Result.CloudDeploymentID != "" {
		d.Set("cloud_deployment_id", deployResult.Result.CloudDeploymentID)
		log.Printf("[INFO] Cloud deployment ID: %s", deployResult.Result.CloudDeploymentID)
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
	d.Set("name", cloud.Name)
	d.Set("cloud_provider", cloud.Provider)
	d.Set("region", cloud.Region)
	d.Set("compute_stack", cloud.ComputeStack)
	d.Set("is_private_cloud", cloud.IsPrivateCloud)
	d.Set("auto_add_user", cloud.AutoAddUser)
	d.Set("enable_lineage_tracking", cloud.LineageTrackingEnabled)
	d.Set("enable_log_ingestion", cloud.IsAggregatedLogsEnabled)

	// Set is_empty_cloud based on whether this cloud has embedded resource config
	isEmptyCloud := !hasEmbeddedResourceConfig(d)
	d.Set("is_empty_cloud", isEmptyCloud)

	// For non-empty clouds, fetch cloud_deployment_id from resources endpoint
	if !isEmptyCloud {
		resourcesResp, err := client.DoRequest("GET", fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), nil)
		if err == nil {
			defer resourcesResp.Body.Close()
			resourcesBody, _ := io.ReadAll(resourcesResp.Body)
			if resourcesResp.StatusCode == http.StatusOK {
				var resourcesResult CloudDeploymentsResponse
				if err := json.Unmarshal(resourcesBody, &resourcesResult); err == nil {
					// Find the default/primary resource to get deployment ID
					for _, res := range resourcesResult.Results {
						if res.CloudDeploymentID != "" {
							d.Set("cloud_deployment_id", res.CloudDeploymentID)
							log.Printf("[INFO] Cloud deployment ID from resources: %s", res.CloudDeploymentID)
							break
						}
					}
				}
			}
		}
	}

	log.Printf("[INFO] Cloud read successfully: id=%s, status=%s, state=%s", cloudID, cloud.Status, cloud.State)

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

// waitForCloudReady polls for cloud readiness using exponential backoff.
// It waits until the cloud state is ACTIVE and status is ready, or until timeout.
func waitForCloudReady(ctx context.Context, client *Client, cloudID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollCount := 0

	// Exponential backoff configuration
	const (
		initialBackoff = 5 * time.Second
		maxBackoff     = 60 * time.Second
		backoffFactor  = 2.0
	)
	currentBackoff := initialBackoff

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

		// Handle rate limiting (429) with backoff
		if resp.StatusCode == http.StatusTooManyRequests {
			log.Printf("[WARN] Poll #%d - Rate limited, backing off for %v", pollCount, currentBackoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(currentBackoff):
				currentBackoff = time.Duration(float64(currentBackoff) * backoffFactor)
				if currentBackoff > maxBackoff {
					currentBackoff = maxBackoff
				}
				continue
			}
		}

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

		log.Printf("[DEBUG] Cloud not ready yet, waiting %v before next poll...", currentBackoff)

		select {
		case <-ctx.Done():
			log.Printf("[ERROR] Context cancelled while waiting for cloud")
			return ctx.Err()
		case <-time.After(currentBackoff):
			// Increase backoff for next iteration
			currentBackoff = time.Duration(float64(currentBackoff) * backoffFactor)
			if currentBackoff > maxBackoff {
				currentBackoff = maxBackoff
			}
		}
	}

	log.Printf("[ERROR] Timeout after %d polls waiting for cloud to be ready", pollCount)
	return fmt.Errorf("timeout waiting for cloud to be ready")
}
