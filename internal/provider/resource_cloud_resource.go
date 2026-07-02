package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &CloudResourceResource{}
	_ resource.ResourceWithConfigure   = &CloudResourceResource{}
	_ resource.ResourceWithImportState = &CloudResourceResource{}
)

// NewCloudResourceResource returns a new cloud resource resource.
func NewCloudResourceResource() resource.Resource {
	return &CloudResourceResource{}
}

// CloudResourceResource defines the resource implementation.
type CloudResourceResource struct {
	client *Client
}

// CloudResourceResourceModel describes the resource data model.
type CloudResourceResourceModel struct {
	// Parent reference - can specify either cloud_id or cloud_name
	CloudID   types.String `tfsdk:"cloud_id"`
	CloudName types.String `tfsdk:"cloud_name"`

	// Resource identity
	Name types.String `tfsdk:"name"`

	// Compute configuration
	CloudProvider types.String `tfsdk:"cloud_provider"`
	ComputeStack  types.String `tfsdk:"compute_stack"`
	Region        types.String `tfsdk:"region"`
	IsPrivate     types.Bool   `tfsdk:"is_private"`

	// Provider-specific configurations (nested)
	AWSConfig        types.Object `tfsdk:"aws_config"`
	GCPConfig        types.Object `tfsdk:"gcp_config"`
	KubernetesConfig types.Object `tfsdk:"kubernetes_config"`

	// Storage configurations
	ObjectStorage types.Object `tfsdk:"object_storage"`
	FileStorage   types.Object `tfsdk:"file_storage"`

	// Computed fields
	CloudResourceID   types.String `tfsdk:"cloud_resource_id"`
	CloudDeploymentID types.String `tfsdk:"cloud_deployment_id"`
	Status            types.String `tfsdk:"status"`
	IsDefault         types.Bool   `tfsdk:"is_default"`

	// Internal
	ID types.String `tfsdk:"id"`
}

// AWSConfigModel represents AWS-specific configuration.
type AWSConfigModel struct {
	VPCID                   types.String `tfsdk:"vpc_id"`
	SubnetIDs               types.List   `tfsdk:"subnet_ids"`
	SubnetIDsToAZ           types.Map    `tfsdk:"subnet_ids_to_az"`
	SecurityGroupIDs        types.List   `tfsdk:"security_group_ids"`
	ControlplaneIAMRoleARN  types.String `tfsdk:"controlplane_iam_role_arn"`
	DataplaneIAMRoleARN     types.String `tfsdk:"dataplane_iam_role_arn"`
	ExternalID              types.String `tfsdk:"external_id"`
	MemoryDBClusterName     types.String `tfsdk:"memorydb_cluster_name"`
	MemoryDBClusterARN      types.String `tfsdk:"memorydb_cluster_arn"`
	MemoryDBClusterEndpoint types.String `tfsdk:"memorydb_cluster_endpoint"`
}

// GCPConfigModel represents GCP-specific configuration.
type GCPConfigModel struct {
	ProjectID                       types.String `tfsdk:"project_id"`
	HostProjectID                   types.String `tfsdk:"host_project_id"`
	ProviderName                    types.String `tfsdk:"provider_name"`
	VPCName                         types.String `tfsdk:"vpc_name"`
	SubnetNames                     types.List   `tfsdk:"subnet_names"`
	ControlplaneServiceAccountEmail types.String `tfsdk:"controlplane_service_account_email"`
	DataplaneServiceAccountEmail    types.String `tfsdk:"dataplane_service_account_email"`
	FirewallPolicyNames             types.List   `tfsdk:"firewall_policy_names"`
	MemorystoreInstanceName         types.String `tfsdk:"memorystore_instance_name"`
	MemorystoreEndpoint             types.String `tfsdk:"memorystore_endpoint"`
}

// KubernetesConfigModel represents Kubernetes-specific configuration.
type KubernetesConfigModel struct {
	AnyscaleOperatorIAMIdentity types.String `tfsdk:"anyscale_operator_iam_identity"`
	Zones                       types.List   `tfsdk:"zones"`
	RedisEndpoint               types.String `tfsdk:"redis_endpoint"`
	Namespace                   types.String `tfsdk:"namespace"`
	IngressHost                 types.String `tfsdk:"ingress_host"`
	ClusterName                 types.String `tfsdk:"cluster_name"`
	Context                     types.String `tfsdk:"context"`
	KubeconfigPath              types.String `tfsdk:"kubeconfig_path"`
}

// ObjectStorageModel represents object storage configuration.
type ObjectStorageModel struct {
	BucketName types.String `tfsdk:"bucket_name"`
	Region     types.String `tfsdk:"region"`
	Endpoint   types.String `tfsdk:"endpoint"`
}

// FileStorageModel represents file storage configuration.
type FileStorageModel struct {
	FileStorageID types.String `tfsdk:"file_storage_id"`
	MountPath     types.String `tfsdk:"mount_path"`
	MountTargets  types.List   `tfsdk:"mount_targets"`
}

// MountTargetModel represents a mount target.
type MountTargetModel struct {
	Address types.String `tfsdk:"address"`
	Zone    types.String `tfsdk:"zone"`
}

// Metadata returns the resource type name.
func (r *CloudResourceResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_resource"
}

// Schema defines the resource schema.
func (r *CloudResourceResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Anyscale Cloud Resource deployment. This attaches infrastructure configuration to an existing Anyscale Cloud.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Composite identifier in format cloud_id:resource_name",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			// ─── Parent Reference ─────────────────────────────────
			"cloud_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The cloud ID to attach this resource to. Either `cloud_id` or `cloud_name` can be specified.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cloud_name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The cloud name to attach this resource to. Either `cloud_id` or `cloud_name` can be specified. If provided, will be resolved to cloud_id.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			// ─── Resource Identity ────────────────────────────────
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The name of the cloud resource. Auto-generated if not provided.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			// ─── Compute Configuration ────────────────────────────
			"cloud_provider": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Cloud provider: AWS or GCP. Required for K8S compute_stack when aws_config/gcp_config is not provided. Inferred from aws_config/gcp_config if not specified.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"compute_stack": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Compute stack type: VM or K8S. When omitted, this reflects the compute stack of the cloud's primary resource as reported by the API (typically VM).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"region": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The region for this cloud resource. Inferred from the cloud/provider configuration when not specified.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"is_private": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Whether this is a private resource (private networking).",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},

			// ─── Computed Fields ──────────────────────────────────
			"cloud_resource_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique cloud resource ID assigned by Anyscale.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"cloud_deployment_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The cloud deployment ID assigned by Anyscale.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The current status of the cloud resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"is_default": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this is the default resource for the cloud.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},

		Blocks: map[string]schema.Block{
			// ─── AWS Configuration ────────────────────────────────
			"aws_config": schema.SingleNestedBlock{
				MarkdownDescription: "AWS-specific configuration.",
				Attributes: map[string]schema.Attribute{
					"vpc_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The VPC ID where Anyscale resources will be deployed.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"subnet_ids": schema.ListAttribute{
						ElementType:         types.StringType,
						Optional:            true,
						MarkdownDescription: "List of subnet IDs for Anyscale resources. Use this OR subnet_ids_to_az.",
						PlanModifiers: []planmodifier.List{
							listplanmodifier.RequiresReplace(),
						},
					},
					"subnet_ids_to_az": schema.MapAttribute{
						ElementType:         types.StringType,
						Optional:            true,
						MarkdownDescription: "Map of subnet ID to availability zone (e.g., {\"subnet-123\": \"us-east-2a\"}). Preferred over subnet_ids.",
						PlanModifiers: []planmodifier.Map{
							mapplanmodifier.RequiresReplace(),
						},
					},
					"security_group_ids": schema.ListAttribute{
						ElementType:         types.StringType,
						Optional:            true,
						MarkdownDescription: "List of security group IDs for Anyscale resources.",
						PlanModifiers: []planmodifier.List{
							listplanmodifier.RequiresReplace(),
						},
					},
					"controlplane_iam_role_arn": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "IAM role ARN for Anyscale control plane (cross-account access).",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"dataplane_iam_role_arn": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "IAM role ARN for Anyscale data plane (cluster nodes).",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"external_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "External ID for IAM role assumption (recommended for security).",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"memorydb_cluster_name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "MemoryDB cluster name for Ray GCS fault tolerance.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"memorydb_cluster_arn": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "MemoryDB cluster ARN.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"memorydb_cluster_endpoint": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "MemoryDB cluster endpoint address.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
				},
			},

			// ─── GCP Configuration ────────────────────────────────
			"gcp_config": schema.SingleNestedBlock{
				MarkdownDescription: "GCP-specific configuration.",
				Attributes: map[string]schema.Attribute{
					"project_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The GCP project ID.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"host_project_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The host project ID for shared VPCs (optional).",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"provider_name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Workload Identity Federation provider name.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"vpc_name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The VPC network name.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"subnet_names": schema.ListAttribute{
						ElementType:         types.StringType,
						Optional:            true,
						MarkdownDescription: "List of subnet names within the VPC for Anyscale resources.",
						PlanModifiers: []planmodifier.List{
							listplanmodifier.RequiresReplace(),
						},
					},
					"controlplane_service_account_email": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Service account email for Anyscale control plane.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"dataplane_service_account_email": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Service account email for Ray cluster nodes.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"firewall_policy_names": schema.ListAttribute{
						ElementType:         types.StringType,
						Optional:            true,
						MarkdownDescription: "List of firewall policy names.",
						PlanModifiers: []planmodifier.List{
							listplanmodifier.RequiresReplace(),
						},
					},
					"memorystore_instance_name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Memorystore instance name for Ray GCS fault tolerance.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"memorystore_endpoint": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Memorystore endpoint address.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
				},
			},

			// ─── Kubernetes Configuration ─────────────────────────
			"kubernetes_config": schema.SingleNestedBlock{
				MarkdownDescription: "Kubernetes-specific configuration. Required when compute_stack is K8S.",
				Attributes: map[string]schema.Attribute{
					"anyscale_operator_iam_identity": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The IAM identity for the Anyscale operator. For AWS EKS: IAM role ARN. For GCP GKE: service account email. For Azure AKS: managed identity client ID.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"zones": schema.ListAttribute{
						ElementType:         types.StringType,
						Optional:            true,
						MarkdownDescription: "List of availability zones for the Kubernetes cluster.",
						PlanModifiers: []planmodifier.List{
							listplanmodifier.RequiresReplace(),
						},
					},
					"redis_endpoint": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Endpoint of a Redis service reachable from the data plane (e.g. `redis.ray-system.svc.cluster.local:6379`). Used for Ray GCS fault tolerance.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"namespace": schema.StringAttribute{
						Optional:            true,
						Computed:            true,
						Default:             stringdefault.StaticString("anyscale"),
						MarkdownDescription: "The Kubernetes namespace for Anyscale workloads.",
					},
					"ingress_host": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The ingress host for the Anyscale operator (e.g., anyscale.example.com).",
					},
					"cluster_name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The Kubernetes cluster name (EKS, GKE, AKS cluster name).",
					},
					"context": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Kubeconfig context to use (for Generic K8S deployments).",
					},
					"kubeconfig_path": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Path to kubeconfig file (for Generic K8S deployments).",
					},
				},
			},

			// ─── Object Storage ───────────────────────────────────
			"object_storage": schema.SingleNestedBlock{
				MarkdownDescription: "Object storage configuration (S3, GCS).",
				Attributes: map[string]schema.Attribute{
					"bucket_name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The bucket name (e.g., my-bucket for S3, gs://my-bucket for GCS).",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"region": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The bucket region (if different from cloud region).",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"endpoint": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Custom S3-compatible endpoint (for MinIO, etc.).",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
				},
			},

			// ─── File Storage ─────────────────────────────────────
			"file_storage": schema.SingleNestedBlock{
				MarkdownDescription: "File storage configuration (EFS, Filestore, etc.).",
				Attributes: map[string]schema.Attribute{
					"file_storage_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The file storage ID (EFS ID, Filestore name, etc.).",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"mount_path": schema.StringAttribute{
						Optional:            true,
						Computed:            true,
						Default:             stringdefault.StaticString("/mnt/shared"),
						MarkdownDescription: "The mount path for the file storage.",
					},
				},
				Blocks: map[string]schema.Block{
					"mount_targets": schema.ListNestedBlock{
						MarkdownDescription: "List of mount targets with address and optional zone.",
						NestedObject: schema.NestedBlockObject{
							Attributes: map[string]schema.Attribute{
								"address": schema.StringAttribute{
									Optional:            true,
									MarkdownDescription: "The IP address or DNS name of the mount target.",
								},
								"zone": schema.StringAttribute{
									Optional:            true,
									MarkdownDescription: "The zone of the mount target (optional).",
								},
							},
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *CloudResourceResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		AddConfigError(&resp.Diagnostics,
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}

	r.client = client
}

// Create creates the resource and sets the initial Terraform state.
func (r *CloudResourceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan CloudResourceResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Resolve cloud_name to cloud_id if needed
	cloudID := plan.CloudID.ValueString()
	if (plan.CloudID.IsNull() || plan.CloudID.IsUnknown()) && !plan.CloudName.IsNull() {
		cloudName := plan.CloudName.ValueString()
		tflog.Info(ctx, "Resolving cloud_name to cloud_id", map[string]any{"cloud_name": cloudName})

		resolvedID, err := ResolveCloudNameToID(ctx, r.client, cloudName)
		if err != nil {
			AddConfigError(&resp.Diagnostics,
				"Cloud Name Resolution Failed",
				fmt.Sprintf("Failed to resolve cloud name '%s' to ID: %s", cloudName, err.Error()))
			return
		}
		cloudID = resolvedID
		plan.CloudID = types.StringValue(cloudID)
	}

	region := plan.Region.ValueString()
	computeStack := plan.ComputeStack.ValueString()
	isPrivate := plan.IsPrivate.ValueBool()

	networkingMode := "PUBLIC"
	if isPrivate {
		networkingMode = "PRIVATE"
	}

	// Determine cloud provider from explicit field or infer from config blocks
	provider := plan.CloudProvider.ValueString()
	if provider == "" {
		if !plan.AWSConfig.IsNull() {
			provider = "AWS"
		} else if !plan.GCPConfig.IsNull() {
			provider = "GCP"
		}
	}

	if provider == "" {
		AddConfigError(&resp.Diagnostics,
			"Provider Required",
			"cloud_provider must be specified, or aws_config/gcp_config must be provided to infer the provider")
		return
	}

	// Set inferred provider in state
	plan.CloudProvider = types.StringValue(provider)

	// Generate or use provided name
	name := plan.Name.ValueString()
	if name == "" {
		name = fmt.Sprintf("%s-%s-%s",
			strings.ToLower(computeStack),
			strings.ToLower(provider),
			strings.ToLower(region))
	}

	tflog.Info(ctx, "Creating Anyscale Cloud Resource",
		map[string]any{
			"cloud_id":      cloudID,
			"name":          name,
			"provider":      provider,
			"region":        region,
			"compute_stack": computeStack,
		})

	// Check if there's an existing default resource that we should update
	existingDefault, err := r.findDefaultCloudResource(ctx, cloudID)
	if err != nil {
		tflog.Warn(ctx, "Failed to check for existing default resource", map[string]any{"error": err.Error()})
	} else if existingDefault != nil {
		tflog.Info(ctx, "Found existing default resource, will update it instead of creating new", map[string]any{"name": existingDefault.Name})
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
	if err := r.addProviderConfig(ctx, &deployReq, &plan, provider, computeStack, &resp.Diagnostics); err != nil {
		AddConfigError(&resp.Diagnostics, "Configuration Error", err.Error())
		return
	}

	if resp.Diagnostics.HasError() {
		return
	}

	reqBody, err := MarshalRequestBody(deployReq)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "cloud resource request", err)
		return
	}

	// Log sanitized request (redact sensitive fields like credentials)
	jsonData, _ := json.Marshal(deployReq)
	tflog.Debug(ctx, "PUT /api/v2/clouds/"+cloudID+"/add_resource", map[string]any{"request": SanitizeJSONForLog(string(jsonData))})

	deployResp, err := DoRequestAndParse[CloudDeploymentResponse](
		ctx,
		r.client,
		"PUT",
		fmt.Sprintf("/api/v2/clouds/%s/add_resource", cloudID),
		reqBody,
		http.StatusOK,
		http.StatusCreated,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "add cloud resource", err)
		return
	}

	// Set state from response
	resourceName := deployResp.Result.Name
	plan.ID = types.StringValue(fmt.Sprintf("%s:%s", cloudID, resourceName))
	plan.Name = types.StringValue(resourceName)
	plan.CloudResourceID = types.StringValue(deployResp.Result.CloudResourceID)
	plan.CloudDeploymentID = types.StringValue(deployResp.Result.CloudDeploymentID)

	// Initialize Status to known null - will be updated by readCloudResource if available
	if plan.Status.IsUnknown() {
		plan.Status = types.StringNull()
	}

	// compute_stack/region may still be unknown here (e.g. omitted in config);
	// the create response already reports the backend's resolved values.
	if plan.ComputeStack.IsUnknown() {
		plan.ComputeStack = types.StringValue(deployResp.Result.ComputeStack)
	}
	if plan.Region.IsUnknown() {
		plan.Region = types.StringValue(deployResp.Result.Region)
	}

	// Persist state now that the cloud resource exists remotely, before any
	// subsequent step (wait, read-back) that can fail. Without this, a
	// mid-create failure below would leave the resource orphaned in the
	// backend with no Terraform record to destroy it.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "Cloud resource created successfully", map[string]any{"id": plan.ID.ValueString()})

	// Wait for the parent cloud to become ready
	createTimeout := 30 * time.Minute
	if err := waitForCloudReady(ctx, r.client, cloudID, createTimeout); err != nil {
		tflog.Error(ctx, "Failed waiting for parent cloud to be ready", map[string]any{"error": err.Error()})
		AddAPIError(&resp.Diagnostics, "wait for cloud to be ready", err)
		return
	}

	// Read back the resource to get all computed fields
	if err := r.readCloudResource(ctx, cloudID, resourceName, &plan); err != nil {
		AddAPIError(&resp.Diagnostics, "read cloud resource after creation", err)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *CloudResourceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state CloudResourceResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID, resourceName, err := parseCloudResourceID(state.ID.ValueString())
	if err != nil {
		AddConfigError(&resp.Diagnostics, "Parse Error", err.Error())
		return
	}

	tflog.Info(ctx, "Reading Anyscale Cloud Resource", map[string]any{"cloud_id": cloudID, "name": resourceName})

	if err := r.readCloudResource(ctx, cloudID, resourceName, &state); err != nil {
		if strings.Contains(err.Error(), "not found") {
			tflog.Warn(ctx, "Cloud resource not found, removing from state", map[string]any{"cloud_id": cloudID, "name": resourceName})
			resp.State.RemoveResource(ctx)
			return
		}
		AddAPIError(&resp.Diagnostics, "read cloud resource", err)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *CloudResourceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Most fields are ForceNew, so limited updates possible
	tflog.Info(ctx, "Cloud resource update called - most fields are ForceNew")

	var state CloudResourceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Just re-read to refresh state
	cloudID, resourceName, err := parseCloudResourceID(state.ID.ValueString())
	if err != nil {
		AddConfigError(&resp.Diagnostics, "Parse Error", err.Error())
		return
	}

	if err := r.readCloudResource(ctx, cloudID, resourceName, &state); err != nil {
		AddAPIError(&resp.Diagnostics, "read cloud resource", err)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *CloudResourceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state CloudResourceResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID, resourceName, err := parseCloudResourceID(state.ID.ValueString())
	if err != nil {
		AddConfigError(&resp.Diagnostics, "Parse Error", err.Error())
		return
	}

	tflog.Info(ctx, "Deleting Anyscale Cloud Resource", map[string]any{"cloud_id": cloudID, "name": resourceName})

	// Check if this is the default/primary resource
	if state.IsDefault.ValueBool() {
		tflog.Info(ctx, "Cloud resource is the primary resource - it will be deleted when the cloud is deleted", map[string]any{"name": resourceName})
		return
	}

	deleteURL := fmt.Sprintf("/api/v2/clouds/%s/remove_resource?cloud_resource_name=%s",
		cloudID, url.QueryEscape(resourceName))

	bodyBytes, err := DoRequestRaw(ctx, r.client, "DELETE", deleteURL, nil,
		http.StatusOK, http.StatusNoContent, http.StatusNotFound)
	if err != nil {
		bodyStr := string(bodyBytes)
		tflog.Error(ctx, "Failed to delete cloud resource", map[string]any{"error": err.Error(), "body": bodyStr})

		// Handle the case where the API tells us this is a primary resource
		if strings.Contains(bodyStr, "primary resource") {
			tflog.Info(ctx, "Cloud resource is the primary resource - it will be deleted when the cloud is deleted", map[string]any{"name": resourceName})
			return
		}

		AddAPIError(&resp.Diagnostics, "delete cloud resource", err)
		return
	}

	tflog.Info(ctx, "Cloud resource deleted successfully", map[string]any{"cloud_id": cloudID, "name": resourceName})
}

// ImportState imports an existing resource into Terraform state.
func (r *CloudResourceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// ID format: cloud_id:resource_name
	cloudID, resourceName, err := parseCloudResourceID(req.ID)
	if err != nil {
		AddConfigError(&resp.Diagnostics,
			"Import Error",
			fmt.Sprintf("Invalid import ID format. Expected 'cloud_id:resource_name', got '%s'", req.ID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cloud_id"), cloudID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), resourceName)...)
}

// ─── Helper Functions ─────────────────────────────────────────────────────────

// parseCloudResourceID parses a composite ID in format "cloud_id:resource_name"
func parseCloudResourceID(id string) (cloudID, resourceName string, err error) {
	parts := strings.SplitN(id, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid cloud resource ID format: expected 'cloud_id:resource_name', got '%s'", id)
	}
	return parts[0], parts[1], nil
}

// findDefaultCloudResource checks if the cloud has a single default resource
func (r *CloudResourceResource) findDefaultCloudResource(ctx context.Context, cloudID string) (*CloudDeploymentResult, error) {
	deploymentsResp, err := DoRequestAndParse[CloudDeploymentsResponse](
		ctx,
		r.client,
		"GET",
		fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list cloud resources: %w", err)
	}

	if len(deploymentsResp.Results) == 1 && deploymentsResp.Results[0].IsDefault {
		tflog.Debug(ctx, "Found single default resource", map[string]any{"name": deploymentsResp.Results[0].Name})
		return &deploymentsResp.Results[0], nil
	}

	tflog.Debug(ctx, "Cloud has multiple resources or no default", map[string]any{"count": len(deploymentsResp.Results)})
	return nil, nil
}

// readCloudResource reads a cloud resource from the API and updates the state model
func (r *CloudResourceResource) readCloudResource(ctx context.Context, cloudID, resourceName string, state *CloudResourceResourceModel) error {
	deploymentsResp, err := DoRequestAndParse[CloudDeploymentsResponse](
		ctx,
		r.client,
		"GET",
		fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID),
		nil,
		http.StatusOK,
		http.StatusNotFound,
	)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return fmt.Errorf("cloud not found")
		}
		return fmt.Errorf("failed to read cloud resources: %w", err)
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
		return fmt.Errorf("cloud resource not found")
	}

	// Update state from API response
	state.CloudID = types.StringValue(cloudID)
	state.Name = types.StringValue(foundResource.Name)
	state.CloudResourceID = types.StringValue(foundResource.CloudResourceID)
	state.CloudDeploymentID = types.StringValue(foundResource.CloudDeploymentID)
	state.ComputeStack = types.StringValue(foundResource.ComputeStack)
	state.Region = types.StringValue(foundResource.Region)
	state.IsDefault = types.BoolValue(foundResource.IsDefault)
	if foundResource.Provider != "" {
		state.CloudProvider = types.StringValue(foundResource.Provider)
	}

	if foundResource.OperatorStatus != nil {
		state.Status = types.StringValue(*foundResource.OperatorStatus)
	} else {
		state.Status = types.StringNull()
	}

	if foundResource.NetworkingMode == "PRIVATE" {
		state.IsPrivate = types.BoolValue(true)
	} else {
		state.IsPrivate = types.BoolValue(false)
	}

	tflog.Info(ctx, "Cloud resource read successfully", map[string]any{"cloud_id": cloudID, "name": resourceName})
	return nil
}

// addProviderConfig adds provider-specific configuration to the deployment request
func (r *CloudResourceResource) addProviderConfig(ctx context.Context, deployReq *CloudDeploymentRequest, plan *CloudResourceResourceModel, provider, computeStack string, diags *diag.Diagnostics) error {
	switch provider {
	case "AWS":
		if computeStack == "K8S" {
			// K8S requires: kubernetes_config, object_storage
			// aws_config is optional for K8S
			if plan.KubernetesConfig.IsNull() {
				return fmt.Errorf("kubernetes_config is required when compute_stack is K8S")
			}

			k8sConfig, err := expandKubernetesConfig(ctx, plan.KubernetesConfig)
			if err != nil {
				return fmt.Errorf("failed to expand kubernetes_config: %w", err)
			}

			if k8sConfig == nil || k8sConfig.AnyscaleOperatorIAMIdentity == "" {
				return fmt.Errorf("kubernetes_config.anyscale_operator_iam_identity is required for AWS K8S clouds")
			}
			deployReq.KubernetesConfig = k8sConfig

			// object_storage is required for K8S
			if plan.ObjectStorage.IsNull() {
				return fmt.Errorf("object_storage is required when compute_stack is K8S")
			}

			objStorage, err := expandObjectStorage(ctx, plan.ObjectStorage)
			if err != nil {
				return fmt.Errorf("failed to expand object_storage: %w", err)
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
			if !plan.AWSConfig.IsNull() {
				awsConfig, err := expandAWSConfig(ctx, plan.AWSConfig)
				if err != nil {
					return fmt.Errorf("failed to expand aws_config: %w", err)
				}
				deployReq.AWSConfig = awsConfig
			}

			// file_storage (EFS) is optional
			if !plan.FileStorage.IsNull() {
				fileStorage, err := expandFileStorage(ctx, plan.FileStorage)
				if err != nil {
					return fmt.Errorf("failed to expand file_storage: %w", err)
				}
				deployReq.FileStorage = fileStorage
			}
		} else {
			// VM compute stack - aws_config is required
			if plan.AWSConfig.IsNull() {
				return fmt.Errorf("aws_config is required when using AWS provider with VM compute_stack")
			}

			awsConfig, err := expandAWSConfig(ctx, plan.AWSConfig)
			if err != nil {
				return fmt.Errorf("failed to expand aws_config: %w", err)
			}
			deployReq.AWSConfig = awsConfig

			// Add object storage with S3 prefix
			if !plan.ObjectStorage.IsNull() {
				objStorage, err := expandObjectStorage(ctx, plan.ObjectStorage)
				if err != nil {
					return fmt.Errorf("failed to expand object_storage: %w", err)
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
			}

			// Add file storage (EFS)
			if !plan.FileStorage.IsNull() {
				fileStorage, err := expandFileStorage(ctx, plan.FileStorage)
				if err != nil {
					return fmt.Errorf("failed to expand file_storage: %w", err)
				}
				deployReq.FileStorage = fileStorage
			}
		}

	case "GCP":
		if computeStack == "K8S" {
			// K8S requires: kubernetes_config, object_storage
			// gcp_config is optional for K8S
			if plan.KubernetesConfig.IsNull() {
				return fmt.Errorf("kubernetes_config is required when compute_stack is K8S")
			}

			k8sConfig, err := expandKubernetesConfig(ctx, plan.KubernetesConfig)
			if err != nil {
				return fmt.Errorf("failed to expand kubernetes_config: %w", err)
			}

			if k8sConfig == nil || k8sConfig.AnyscaleOperatorIAMIdentity == "" {
				return fmt.Errorf("kubernetes_config.anyscale_operator_iam_identity is required for GCP K8S clouds")
			}
			deployReq.KubernetesConfig = k8sConfig

			// object_storage is required for K8S
			if plan.ObjectStorage.IsNull() {
				return fmt.Errorf("object_storage is required when compute_stack is K8S")
			}

			objStorage, err := expandObjectStorage(ctx, plan.ObjectStorage)
			if err != nil {
				return fmt.Errorf("failed to expand object_storage: %w", err)
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
			if !plan.GCPConfig.IsNull() {
				gcpConfig, err := expandGCPConfig(ctx, plan.GCPConfig)
				if err != nil {
					return fmt.Errorf("failed to expand gcp_config: %w", err)
				}
				deployReq.GCPConfig = gcpConfig
			}

			// file_storage (Filestore) is optional
			if !plan.FileStorage.IsNull() {
				fileStorage, err := expandFileStorage(ctx, plan.FileStorage)
				if err != nil {
					return fmt.Errorf("failed to expand file_storage: %w", err)
				}
				deployReq.FileStorage = fileStorage
			}
		} else {
			// VM compute stack - gcp_config is required
			if plan.GCPConfig.IsNull() {
				return fmt.Errorf("gcp_config is required when using GCP provider with VM compute_stack")
			}

			gcpConfig, err := expandGCPConfig(ctx, plan.GCPConfig)
			if err != nil {
				return fmt.Errorf("failed to expand gcp_config: %w", err)
			}
			deployReq.GCPConfig = gcpConfig

			// Add object storage with GCS prefix
			if !plan.ObjectStorage.IsNull() {
				objStorage, err := expandObjectStorage(ctx, plan.ObjectStorage)
				if err != nil {
					return fmt.Errorf("failed to expand object_storage: %w", err)
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
			}

			// Add file storage (Filestore)
			if !plan.FileStorage.IsNull() {
				fileStorage, err := expandFileStorage(ctx, plan.FileStorage)
				if err != nil {
					return fmt.Errorf("failed to expand file_storage: %w", err)
				}
				deployReq.FileStorage = fileStorage
			}
		}
	}

	return nil
}

// expandAWSConfig extracts AWS configuration from the Terraform plan
func expandAWSConfig(ctx context.Context, obj types.Object) (*AWSConfig, error) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}

	var awsModel AWSConfigModel
	diags := obj.As(ctx, &awsModel, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, fmt.Errorf("failed to convert aws_config: %v", diags)
	}

	awsConfig := &AWSConfig{
		VPCID:             awsModel.VPCID.ValueString(),
		AnyscaleIAMRoleID: awsModel.ControlplaneIAMRoleARN.ValueString(),
		ClusterIAMRoleID:  awsModel.DataplaneIAMRoleARN.ValueString(),
	}

	// Handle subnet_ids_to_az map (preferred) or subnet_ids list
	if !awsModel.SubnetIDsToAZ.IsNull() {
		subnetAZMap := make(map[string]string)
		diags = awsModel.SubnetIDsToAZ.ElementsAs(ctx, &subnetAZMap, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to convert subnet_ids_to_az: %v", diags)
		}
		if len(subnetAZMap) > 0 {
			awsConfig.SubnetIDs = make([]string, 0, len(subnetAZMap))
			awsConfig.Zones = make([]string, 0, len(subnetAZMap))
			for subnetID, az := range subnetAZMap {
				awsConfig.SubnetIDs = append(awsConfig.SubnetIDs, subnetID)
				awsConfig.Zones = append(awsConfig.Zones, az)
			}
		}
	} else if !awsModel.SubnetIDs.IsNull() {
		var subnetIDs []string
		diags = awsModel.SubnetIDs.ElementsAs(ctx, &subnetIDs, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to convert subnet_ids: %v", diags)
		}
		awsConfig.SubnetIDs = subnetIDs
	}

	// Security Group IDs
	if !awsModel.SecurityGroupIDs.IsNull() {
		var sgIDs []string
		diags = awsModel.SecurityGroupIDs.ElementsAs(ctx, &sgIDs, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to convert security_group_ids: %v", diags)
		}
		awsConfig.SecurityGroupIDs = sgIDs
	}

	// Optional fields
	if !awsModel.ExternalID.IsNull() {
		awsConfig.ExternalID = awsModel.ExternalID.ValueString()
	}
	if !awsModel.MemoryDBClusterName.IsNull() {
		name := awsModel.MemoryDBClusterName.ValueString()
		awsConfig.MemoryDBClusterName = &name
	}
	if !awsModel.MemoryDBClusterARN.IsNull() {
		arn := awsModel.MemoryDBClusterARN.ValueString()
		awsConfig.MemoryDBClusterARN = &arn
	}
	if !awsModel.MemoryDBClusterEndpoint.IsNull() {
		endpoint := awsModel.MemoryDBClusterEndpoint.ValueString()
		awsConfig.MemoryDBClusterEndpoint = &endpoint
	}

	return awsConfig, nil
}

// expandGCPConfig extracts GCP configuration from the Terraform plan
func expandGCPConfig(ctx context.Context, obj types.Object) (*GCPConfig, error) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}

	var gcpModel GCPConfigModel
	diags := obj.As(ctx, &gcpModel, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, fmt.Errorf("failed to convert gcp_config: %v", diags)
	}

	gcpConfig := &GCPConfig{
		ProjectID:                   gcpModel.ProjectID.ValueString(),
		ProviderName:                gcpModel.ProviderName.ValueString(),
		VPCName:                     gcpModel.VPCName.ValueString(),
		AnyscaleServiceAccountEmail: gcpModel.ControlplaneServiceAccountEmail.ValueString(),
		ClusterServiceAccountEmail:  gcpModel.DataplaneServiceAccountEmail.ValueString(),
	}

	// Subnet names
	if !gcpModel.SubnetNames.IsNull() {
		var subnetNames []string
		diags = gcpModel.SubnetNames.ElementsAs(ctx, &subnetNames, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to convert subnet_names: %v", diags)
		}
		gcpConfig.SubnetNames = subnetNames
	}

	// Firewall policy names
	if !gcpModel.FirewallPolicyNames.IsNull() {
		var fwPolicies []string
		diags = gcpModel.FirewallPolicyNames.ElementsAs(ctx, &fwPolicies, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to convert firewall_policy_names: %v", diags)
		}
		gcpConfig.FirewallPolicyNames = fwPolicies
	}

	// Optional fields
	if !gcpModel.HostProjectID.IsNull() {
		gcpConfig.HostProjectID = gcpModel.HostProjectID.ValueString()
	}
	if !gcpModel.MemorystoreInstanceName.IsNull() {
		gcpConfig.MemorystoreInstanceName = gcpModel.MemorystoreInstanceName.ValueString()
	}
	if !gcpModel.MemorystoreEndpoint.IsNull() {
		gcpConfig.MemorystoreEndpoint = gcpModel.MemorystoreEndpoint.ValueString()
	}

	return gcpConfig, nil
}

// expandKubernetesConfig extracts Kubernetes configuration from the Terraform plan
func expandKubernetesConfig(ctx context.Context, obj types.Object) (*KubernetesConfig, error) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}

	var k8sModel KubernetesConfigModel
	diags := obj.As(ctx, &k8sModel, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, fmt.Errorf("failed to convert kubernetes_config: %v", diags)
	}

	k8sConfig := &KubernetesConfig{
		AnyscaleOperatorIAMIdentity: k8sModel.AnyscaleOperatorIAMIdentity.ValueString(),
		RedisEndpoint:               k8sModel.RedisEndpoint.ValueString(),
	}

	// Zones
	if !k8sModel.Zones.IsNull() {
		var zones []string
		diags = k8sModel.Zones.ElementsAs(ctx, &zones, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to convert zones: %v", diags)
		}
		k8sConfig.Zones = zones
	}

	return k8sConfig, nil
}

// expandObjectStorage extracts object storage configuration from the Terraform plan
func expandObjectStorage(ctx context.Context, obj types.Object) (*ObjectStorage, error) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}

	var storageModel ObjectStorageModel
	diags := obj.As(ctx, &storageModel, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, fmt.Errorf("failed to convert object_storage: %v", diags)
	}

	storage := &ObjectStorage{
		BucketName: storageModel.BucketName.ValueString(),
	}

	if !storageModel.Region.IsNull() {
		region := storageModel.Region.ValueString()
		storage.Region = &region
	}
	if !storageModel.Endpoint.IsNull() {
		endpoint := storageModel.Endpoint.ValueString()
		storage.Endpoint = &endpoint
	}

	return storage, nil
}

// expandFileStorage extracts file storage configuration from the Terraform plan
func expandFileStorage(ctx context.Context, obj types.Object) (*FileStorage, error) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}

	var storageModel FileStorageModel
	diags := obj.As(ctx, &storageModel, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, fmt.Errorf("failed to convert file_storage: %v", diags)
	}

	storage := &FileStorage{
		FileStorageID: storageModel.FileStorageID.ValueString(),
	}

	if !storageModel.MountPath.IsNull() {
		storage.MountPath = storageModel.MountPath.ValueString()
	}

	if !storageModel.MountTargets.IsNull() {
		var mountTargetModels []MountTargetModel
		diags = storageModel.MountTargets.ElementsAs(ctx, &mountTargetModels, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to convert mount_targets: %v", diags)
		}

		storage.MountTargets = make([]MountTarget, len(mountTargetModels))
		for i, model := range mountTargetModels {
			storage.MountTargets[i] = MountTarget{
				Address: model.Address.ValueString(),
				Zone:    model.Zone.ValueString(),
			}
		}
	}

	return storage, nil
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

	tflog.Info(ctx, "Waiting for cloud to be ready", map[string]any{"cloud_id": cloudID, "timeout": timeout.String()})

	for time.Now().Before(deadline) {
		pollCount++
		tflog.Debug(ctx, "Polling cloud status", map[string]any{"poll_count": pollCount, "cloud_id": cloudID})

		bodyBytes, err := DoRequestRaw(ctx, client, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil,
			http.StatusOK, http.StatusTooManyRequests)
		if err != nil {
			// Handle rate limiting (429) with backoff
			if strings.Contains(err.Error(), "429") {
				tflog.Warn(ctx, "Rate limited, backing off", map[string]any{"poll_count": pollCount, "backoff": currentBackoff.String()})
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
			tflog.Error(ctx, "Failed to check cloud status", map[string]any{"error": err.Error()})
			return fmt.Errorf("failed to check cloud status: %w", err)
		}

		var cloudResp CloudResponse
		if err := json.Unmarshal(bodyBytes, &cloudResp); err != nil {
			return fmt.Errorf("failed to parse cloud response: %w", err)
		}

		status := cloudResp.Result.Status
		state := cloudResp.Result.State

		tflog.Info(ctx, "Cloud status check", map[string]any{"poll_count": pollCount, "status": status, "state": state})

		// Also check cloud resources for debugging
		resourcesBody, err := DoRequestRaw(ctx, client, "GET", fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), nil)
		if err == nil {
			tflog.Debug(ctx, "Cloud resources", map[string]any{"poll_count": pollCount, "resources": string(resourcesBody)})
		}

		if status == "ready" && state == "ACTIVE" {
			tflog.Info(ctx, "Cloud is ready", map[string]any{"poll_count": pollCount})
			return nil
		}

		if status == "failed" || state == "FAILED" {
			tflog.Error(ctx, "Cloud creation failed", map[string]any{"status": status, "state": state})
			return fmt.Errorf("cloud creation failed with status: %s, state: %s", status, state)
		}

		tflog.Debug(ctx, "Cloud not ready yet, waiting before next poll", map[string]any{"backoff": currentBackoff.String()})

		select {
		case <-ctx.Done():
			tflog.Error(ctx, "Context cancelled while waiting for cloud")
			return ctx.Err()
		case <-time.After(currentBackoff):
			// Increase backoff for next iteration
			currentBackoff = time.Duration(float64(currentBackoff) * backoffFactor)
			if currentBackoff > maxBackoff {
				currentBackoff = maxBackoff
			}
		}
	}

	tflog.Error(ctx, "Timeout waiting for cloud to be ready", map[string]any{"poll_count": pollCount})
	return fmt.Errorf("timeout waiting for cloud to be ready")
}
