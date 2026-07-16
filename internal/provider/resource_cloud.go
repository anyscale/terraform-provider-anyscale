package provider

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                   = &CloudResource{}
	_ resource.ResourceWithConfigure      = &CloudResource{}
	_ resource.ResourceWithImportState    = &CloudResource{}
	_ resource.ResourceWithValidateConfig = &CloudResource{}
)

// NewCloudResource returns a new cloud resource.
func NewCloudResource() resource.Resource {
	return &CloudResource{}
}

// CloudResource defines the resource implementation.
type CloudResource struct {
	client *Client
}

// CloudResourceModel describes the resource data model.
type CloudResourceModel struct {
	// Common fields
	ID                    types.String `tfsdk:"id"`
	Name                  types.String `tfsdk:"name"`
	CloudProvider         types.String `tfsdk:"cloud_provider"`
	ComputeStack          types.String `tfsdk:"compute_stack"`
	Region                types.String `tfsdk:"region"`
	IsPrivateCloud        types.Bool   `tfsdk:"is_private_cloud"`
	AutoAddUser           types.Bool   `tfsdk:"auto_add_user"`
	Credentials           types.String `tfsdk:"credentials"`
	EnableLineageTracking types.Bool   `tfsdk:"enable_lineage_tracking"`
	EnableLogIngestion    types.Bool   `tfsdk:"enable_log_ingestion"`
	EnableSystemCluster   types.Bool   `tfsdk:"enable_system_cluster"`

	// Provider-specific configurations (nested)
	AWSConfig        types.Object `tfsdk:"aws_config"`
	GCPConfig        types.Object `tfsdk:"gcp_config"`
	AzureConfig      types.Object `tfsdk:"azure_config"`
	KubernetesConfig types.Object `tfsdk:"kubernetes_config"`

	// Storage configurations
	ObjectStorage types.Object `tfsdk:"object_storage"`
	FileStorage   types.Object `tfsdk:"file_storage"`

	// Computed fields
	IsEmptyCloud      types.Bool   `tfsdk:"is_empty_cloud"`
	CloudDeploymentID types.String `tfsdk:"cloud_deployment_id"`
}

// AzureConfigModel represents Azure-specific configuration. See AzureConfig
// in models.go for why tenant_id is the only field.
type AzureConfigModel struct {
	TenantID types.String `tfsdk:"tenant_id"`
}

// cloudNameImmutablePlanModifier enforces that a cloud's name cannot change
// after creation, as a clear plan-time error instead of either of the two
// wrong outcomes: RequiresReplace would destroy a live cloud on a mere
// upgrade for anyone whose .tf already has a stale/mismatched name (they are
// currently protected by Update's apply-time 405, silently relying on it);
// letting it through to Update would just 405 again with no useful message,
// since the API has no endpoint that renames a cloud at all.
type cloudNameImmutablePlanModifier struct{}

func (m cloudNameImmutablePlanModifier) Description(ctx context.Context) string {
	return "Cloud name is immutable after creation; changing it is a plan-time error, not an update or a replacement."
}

func (m cloudNameImmutablePlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m cloudNameImmutablePlanModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// No established prior name to protect: a fresh create, or state not yet
	// populated (e.g. immediately post-import before the first Read).
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() {
		return
	}
	if req.PlanValue.IsUnknown() {
		return
	}
	if req.PlanValue.ValueString() != req.StateValue.ValueString() {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Cloud Name Is Immutable",
			fmt.Sprintf(
				"cloud name is immutable after creation; to rename, destroy and recreate deliberately. current name: %q, requested name: %q.",
				req.StateValue.ValueString(), req.PlanValue.ValueString(),
			),
		)
	}
}

// Metadata returns the resource type name.
func (r *CloudResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud"
}

// Schema defines the resource schema.
func (r *CloudResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Anyscale Cloud. Supports both all-in-one pattern (embedded configs) and empty cloud pattern (resources added separately via anyscale_cloud_resource). If a cloud with the same `name` already exists at apply time (for example, recovering from an interrupted create), this resource adopts it into Terraform state instead of creating a duplicate. If more than one cloud shares that name, create fails instead of guessing which one to adopt - the error identifies the candidates and explains how to resolve the ambiguity (rename or delete the duplicates, or import the specific cloud you intend to manage).",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the cloud.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			// ─── Common Fields ────────────────────────────────────
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The name of the cloud. Immutable after creation: the API has no endpoint to rename a cloud, so changing this produces a plan-time error rather than an update or a replacement.",
				PlanModifiers: []planmodifier.String{
					cloudNameImmutablePlanModifier{},
				},
			},

			"cloud_provider": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Cloud provider: AWS, GCP, or AZURE. Auto-detected from aws_config/gcp_config/azure_config, or defaults to AWS for empty clouds. AWS and GCP support both VM and K8S compute stacks; AZURE supports K8S only (AKS) - Anyscale does not support Azure VM clouds, and setting azure_config with any other compute_stack is a plan-time error. GENERIC is not yet supported by this provider.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"compute_stack": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Compute stack type: VM or K8S. Required when using embedded config (aws_config/gcp_config). When omitted, this reflects the compute stack of the cloud's primary resource as reported by the API (typically VM).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("VM", "K8S"),
				},
			},

			"region": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The region where the cloud is deployed. Auto-detected from config or defaults to us-east-1 for empty clouds.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"is_private_cloud": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Whether this is a private cloud (private networking).",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},

			"auto_add_user": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Whether to automatically add users to this cloud.",
			},

			"credentials": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Cloud credentials. For AWS: the IAM role ARN. For GCP: JSON with provider_id, project_id, service_account_email. Required when using split pattern (empty cloud + cloud_resource).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			"enable_lineage_tracking": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Whether to enable lineage tracking for this cloud.",
			},

			"enable_log_ingestion": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Whether to enable aggregated log ingestion for this cloud.",
			},

			"enable_system_cluster": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Whether to enable the system cluster for this cloud (powers task/actor observability dashboards; see `anyscale cloud config update --enable-system-cluster`). " +
					"Deliberately NOT Computed, unlike the other cloud-level booleans above: the Anyscale API has no side-effect-free way to read back whether the system cluster is currently enabled - the only readable field on a cloud is an opaque config ID that, once created, stays non-null regardless of the current enabled/disabled state, and the one endpoint that resolves the true value has a real side effect (it provisions a cluster) and requires broader permissions.",
			},

			// ─── Computed Fields ──────────────────────────────────
			"is_empty_cloud": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this cloud was created without embedded resource configuration. Use anyscale_cloud_resource to attach resources separately.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},

			"cloud_deployment_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The cloud deployment ID. For K8S clouds, pass this to the Anyscale operator during installation. The Anyscale API no longer populates this field; use `anyscale_cloud_resource`'s `cloud_resource_id` instead.",
				DeprecationMessage:  cloudDeploymentIDDeprecationMessage,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},

		Blocks: map[string]schema.Block{
			// ─── AWS Configuration ────────────────────────────────
			"aws_config": schema.SingleNestedBlock{
				MarkdownDescription: "AWS-specific configuration. Required when cloud_provider is AWS and using all-in-one pattern.",
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
					"cluster_instance_profile_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "IAM instance profile ARN attached to Ray cluster nodes. Defaults to the instance profile with the same name as `dataplane_iam_role_arn` when unset - set this explicitly only if your IAM tooling generates a profile name that differs from the role name.",
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
						MarkdownDescription: "MemoryDB cluster endpoint address. Conflicts with `kubernetes_config.redis_endpoint` - the backend rejects more than one GCS fault-tolerance backing store on the same cloud.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
				},
			},

			// ─── GCP Configuration ────────────────────────────────
			"gcp_config": schema.SingleNestedBlock{
				MarkdownDescription: "GCP-specific configuration. Required when cloud_provider is GCP and using all-in-one pattern.",
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
						MarkdownDescription: "Workload Identity Federation provider name (e.g., projects/123456789/locations/global/workloadIdentityPools/anyscale-pool/providers/anyscale-provider).",
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
						MarkdownDescription: "Service account email for Anyscale control plane (cross-project access).",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"dataplane_service_account_email": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Service account email for Ray cluster nodes (data plane).",
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
						MarkdownDescription: "Memorystore endpoint address. Conflicts with `kubernetes_config.redis_endpoint` - the backend rejects more than one GCS fault-tolerance backing store on the same cloud.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
				},
			},

			// ─── Azure Configuration ──────────────────────────────
			"azure_config": schema.SingleNestedBlock{
				MarkdownDescription: "Azure-specific configuration. Required when cloud_provider is AZURE. Azure clouds are Kubernetes-only (AKS) - Anyscale does not support Azure VM clouds, so compute_stack must be \"K8S\"; setting azure_config with any other compute_stack is a plan-time error. Unlike aws_config/gcp_config, this has a single field: AKS setup creates no VNet/subnet resources of its own, and real authentication is operator workload-identity federation (see kubernetes_config.anyscale_operator_iam_identity), not network or IAM-role wiring.",
				Attributes: map[string]schema.Attribute{
					"tenant_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The Azure tenant ID (maps to the Anyscale API's AzureConfig.tenant_id, and the CLI's `--azure-tenant-id`).",
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
						MarkdownDescription: "The IAM identity for the Anyscale operator. For AWS EKS: IAM role ARN. For GCP GKE: service account email. For Azure AKS: the managed identity's principal ID (not its client ID - the reference AKS setup flow distinguishes the two: principal ID here, client ID only in the operator's own values.yaml).",
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
						MarkdownDescription: "Endpoint of a Redis service reachable from the data plane (e.g. `redis.ray-system.svc.cluster.local:6379`). Used for Ray GCS fault tolerance. Conflicts with `aws_config.memorydb_cluster_endpoint` and `gcp_config.memorystore_endpoint` - the backend rejects more than one GCS fault-tolerance backing store on the same cloud.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
						Validators: []validator.String{
							stringvalidator.ConflictsWith(
								path.MatchRoot("aws_config").AtName("memorydb_cluster_endpoint"),
								path.MatchRoot("gcp_config").AtName("memorystore_endpoint"),
							),
						},
					},
					"namespace": schema.StringAttribute{
						Optional:            true,
						Computed:            true,
						Default:             stringdefault.StaticString("anyscale"),
						DeprecationMessage:  kubernetesConfigInertFieldDeprecationMessage,
						MarkdownDescription: "The Kubernetes namespace for Anyscale workloads. Changing this requires replacement; the provider has no in-place update path for it.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"ingress_host": schema.StringAttribute{
						Optional:            true,
						DeprecationMessage:  kubernetesConfigInertFieldDeprecationMessage,
						MarkdownDescription: "The ingress host for the Anyscale operator (e.g., anyscale.example.com). Changing this requires replacement; the provider has no in-place update path for it.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"cluster_name": schema.StringAttribute{
						Optional:            true,
						DeprecationMessage:  kubernetesConfigInertFieldDeprecationMessage,
						MarkdownDescription: "The Kubernetes cluster name (EKS, GKE, AKS cluster name). Changing this requires replacement; the provider has no in-place update path for it.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"context": schema.StringAttribute{
						Optional:            true,
						DeprecationMessage:  kubernetesConfigInertFieldDeprecationMessage,
						MarkdownDescription: "Kubeconfig context to use (for Generic K8S deployments). Changing this requires replacement; the provider has no in-place update path for it.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"kubeconfig_path": schema.StringAttribute{
						Optional:            true,
						DeprecationMessage:  kubernetesConfigInertFieldDeprecationMessage,
						MarkdownDescription: "Path to kubeconfig file (for Generic K8S deployments). Changing this requires replacement; the provider has no in-place update path for it.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
				},
			},

			// ─── Object Storage ───────────────────────────────────
			"object_storage": schema.SingleNestedBlock{
				MarkdownDescription: "Object storage configuration (S3, GCS, Azure Blob, or S3-compatible).",
				Attributes: map[string]schema.Attribute{
					"bucket_name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The bucket name (e.g., my-bucket for S3, gs://my-bucket for GCS). A bare name and its scheme-prefixed form (s3://, gs://) are treated as the same bucket for plan purposes, so importing a cloud whose bucket was written without the prefix does not force replacement.",
						PlanModifiers: []planmodifier.String{
							bucketNameSemanticEqualPlanModifier{},
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
						MarkdownDescription: "The mount path for the file storage. Changing this requires replacement; the provider has no in-place update path for it.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"persistent_volume_claim": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Name of a Kubernetes PersistentVolumeClaim to mount for shared storage (Kubernetes cloud resources only). Mutually exclusive with `csi_ephemeral_volume_driver` - the backend rejects both being set.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
						Validators: []validator.String{
							stringvalidator.ConflictsWith(path.MatchRoot("file_storage").AtName("csi_ephemeral_volume_driver")),
						},
					},
					"csi_ephemeral_volume_driver": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "CSI driver name for an ephemeral inline volume to use for shared storage (Kubernetes cloud resources only). Mutually exclusive with `persistent_volume_claim` - the backend rejects both being set.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
						Validators: []validator.String{
							stringvalidator.ConflictsWith(path.MatchRoot("file_storage").AtName("persistent_volume_claim")),
						},
					},
				},
				Blocks: map[string]schema.Block{
					"mount_targets": schema.ListNestedBlock{
						MarkdownDescription: "List of mount targets with address and optional zone. Changing this list requires replacement; the provider has no in-place update path for it. This is the NFS-style mount mechanism; mutually exclusive with `persistent_volume_claim` and `csi_ephemeral_volume_driver` (the Kubernetes-native shared-storage mechanisms) - do not set both.",
						PlanModifiers: []planmodifier.List{
							listplanmodifier.RequiresReplace(),
						},
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
func (r *CloudResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = client
}

// ValidateConfig rejects, at plan time, any configuration that would
// otherwise sail through `terraform plan` clean and only fail deep inside
// buildProviderConfig - by which point Create has already run the POST
// /api/v2/clouds step and persisted a real (permanently resource-less) cloud
// to state (K9). It intentionally mirrors buildProviderConfig's own
// AZURE/GENERIC rejection rather than replacing it: this is a plan-time
// preview for the common case where the value is already known in the
// config; buildProviderConfig's runtime check remains the last line of
// defense for a provider value that's still unknown at plan time (e.g.
// interpolated from another resource's computed output).
//
// Scoped to exactly the configurations that would reach buildProviderConfig:
// hasEmbeddedResourceConfig must be true (an empty cloud - no aws_config/
// gcp_config/azure_config/kubernetes_config at all - never calls
// addCloudResource, so cloud_provider=AZURE/GENERIC on a genuinely empty
// cloud is harmless and left alone here, matching today's real, working
// behavior for that pattern).
func (r *CloudResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data CloudResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !r.hasEmbeddedResourceConfig(&data) {
		return
	}

	provider := strings.ToUpper(data.CloudProvider.ValueString())
	if provider == "" && !data.AzureConfig.IsNull() {
		provider = "AZURE" // mirrors Create()'s own auto-detect order
	}

	switch provider {
	case "AZURE":
		resp.Diagnostics.Append(validateAzureK8SOnly(ctx, data.ComputeStack.ValueString(), data.ObjectStorage)...)
	case "GENERIC":
		resp.Diagnostics.AddAttributeError(path.Root("cloud_provider"), "Generic Clouds Not Yet Supported", genericCloudNotSupportedMessage)
	}
}

// ─── Helper Functions ─────────────────────────────────────────────────────────

// generateRandomString generates a random alphanumeric string of the given length
func generateRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		// Fallback to a simple timestamp-based string on error
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}

// hasEmbeddedResourceConfig checks if the cloud has embedded resource
// configuration (all-in-one pattern). kubernetes_config counts on its own,
// separate from aws_config/gcp_config/azure_config: aws_config/gcp_config are
// optional for K8S clouds (see addCloudResource), so a K8S cloud can be
// defined by kubernetes_config alone. Omitting it here (as this function did
// before - F2/C12) misclassified such a cloud as empty, so Create took the
// empty-cloud branch and never called addCloudResource at all - no K8S
// resource was ever created, and the cloud rolled up to VM on read
// ("Provider produced inconsistent result after apply: .compute_stack: was
// K8S, but now VM").
func (r *CloudResource) hasEmbeddedResourceConfig(plan *CloudResourceModel) bool {
	return !plan.AWSConfig.IsNull() || !plan.GCPConfig.IsNull() || !plan.AzureConfig.IsNull() || !plan.KubernetesConfig.IsNull()
}

// regionRequiredForCreateError returns a diagnostic-ready error for an
// all-in-one create whose region could not be determined by the time
// addCloudResource is about to be called - see C13. A non-empty region
// produces no error.
func regionRequiredForCreateError(region string) (summary, detail string, hasError bool) {
	if region != "" {
		return "", "", false
	}
	return "Region Could Not Be Determined", "region could not be determined; set region explicitly on the anyscale_cloud.", true
}

// multipleCloudsWithNameError signals findCloudByName's ambiguity case: 2+ clouds share the
// given name, and the adopt path must hard-stop rather than guess which one to attach
// Terraform management to. Distinguished from findCloudByName's other errors (network, parse,
// non-2xx) via errors.As, not string-matching, so Create() can treat this case as a hard error
// while still treating everything else as the existing lenient "warn and fall through to a
// fresh create" case.
//
// Every other by-name site in this provider (data_source_cloud.go, cloud_helpers.go's
// ResolveCloudNameToID, data_source_compute_config.go, api_helpers.go's PickMostRecentMatch
// itself) warns and silently picks the most recent match. This is deliberately the one
// exception: this lookup gates an ADOPT decision (Create attaches Terraform management to
// whatever ID comes back), so picking the wrong one among duplicates means silently managing
// the wrong live cloud - higher stakes than a read. See .crystl/quest/spec.json's
// resource_adopt_path design_decision.
type multipleCloudsWithNameError struct {
	name string
	ids  []string
}

func (e *multipleCloudsWithNameError) Error() string {
	return fmt.Sprintf(
		"found %d clouds named %q (ids: %s); refusing to guess which one to adopt - delete or "+
			"rename the duplicates, or if this create is recovering an interrupted apply, "+
			"import the specific cloud you intend to manage with "+
			"`terraform import anyscale_cloud.<name> <id>`",
		len(e.ids), e.name, strings.Join(e.ids, ", "),
	)
}

// findCloudByName looks for an existing cloud with the given name, paginating through every
// page of GET /api/v2/clouds (mirrors CloudDataSource.findCloudByName's PaginatedRequest/
// CloudsListResponse mechanics in data_source_cloud.go). Returns ("", nil) if no cloud has
// this name, (id, nil) if exactly one does, or a *multipleCloudsWithNameError if 2+ do.
func (r *CloudResource) findCloudByName(ctx context.Context, name string) (string, error) {
	results, err := PaginatedRequest(ctx, r.client, "/api/v2/clouds", nil,
		func(body []byte) ([]CloudResult, *string, error) {
			var cloudsResp CloudsListResponse
			if err := json.Unmarshal(body, &cloudsResp); err != nil {
				return nil, nil, fmt.Errorf("failed to parse clouds response: %w", err)
			}
			return cloudsResp.Results, cloudsResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return "", fmt.Errorf("failed to list clouds: %w", err)
	}

	var matchIDs []string
	for _, cloud := range results {
		if cloud.Name == name {
			matchIDs = append(matchIDs, cloud.ID)
		}
	}

	switch len(matchIDs) {
	case 0:
		return "", nil
	case 1:
		return matchIDs[0], nil
	default:
		return "", &multipleCloudsWithNameError{name: name, ids: matchIDs}
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *CloudResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan CloudResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.Name.ValueString()

	// Determine if this is an empty cloud (no embedded config)
	isEmptyCloud := !r.hasEmbeddedResourceConfig(&plan)

	// Auto-detect cloud provider from config blocks
	provider := plan.CloudProvider.ValueString()
	if provider == "" {
		if !plan.AWSConfig.IsNull() {
			provider = "AWS"
		} else if !plan.GCPConfig.IsNull() {
			provider = "GCP"
		} else if !plan.AzureConfig.IsNull() {
			provider = "AZURE"
		} else {
			// Default to AWS for empty clouds
			provider = "AWS"
		}
		plan.CloudProvider = types.StringValue(provider)
	}

	// Auto-detect or default region
	region := plan.Region.ValueString()
	computeStack := plan.ComputeStack.ValueString()

	// Extract region from config blocks if not explicitly set
	if region == "" {
		if !plan.AWSConfig.IsNull() {
			// Try to infer from subnet_ids_to_az
			var awsModel AWSConfigModel
			diags := plan.AWSConfig.As(ctx, &awsModel, basetypes.ObjectAsOptions{})
			if !diags.HasError() && !awsModel.SubnetIDsToAZ.IsNull() {
				// Get first AZ and extract region
				subnetMap := make(map[string]string)
				awsModel.SubnetIDsToAZ.ElementsAs(ctx, &subnetMap, false)
				for _, az := range subnetMap {
					if len(az) > 2 {
						region = az[:len(az)-1] // Remove last char (e.g., us-east-2a -> us-east-2)
					}
					break
				}
			}
		}
		// Use placeholder region for empty cloud pattern
		if region == "" && isEmptyCloud {
			region = "us-east-1"
			tflog.Info(ctx, "No region specified for empty cloud, using placeholder", map[string]any{"region": region})
		}
		plan.Region = types.StringValue(region)
	}

	tflog.Info(ctx, "Creating Anyscale Cloud", map[string]any{
		"name":          name,
		"provider":      provider,
		"region":        region,
		"compute_stack": computeStack,
		"is_empty":      isEmptyCloud,
	})

	// Check if a cloud with this name already exists (handles interrupted creates)
	existingCloudID, err := r.findCloudByName(ctx, name)
	var ambiguousCloudErr *multipleCloudsWithNameError
	if errors.As(err, &ambiguousCloudErr) {
		resp.Diagnostics.AddError("Multiple Clouds Found", ambiguousCloudErr.Error())
		return
	} else if err != nil {
		tflog.Warn(ctx, "Failed to check for existing cloud", map[string]any{"error": err.Error()})
	} else if existingCloudID != "" {
		tflog.Info(ctx, "Found existing cloud, adopting", map[string]any{"name": name, "id": existingCloudID})
		plan.ID = types.StringValue(existingCloudID)
		plan.IsEmptyCloud = types.BoolValue(isEmptyCloud)

		// Read the existing cloud to populate state
		if err := r.readCloudState(ctx, existingCloudID, &plan); err != nil {
			resp.Diagnostics.AddError("Read Error", fmt.Sprintf("Failed to read existing cloud: %s", err.Error()))
			return
		}

		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	// Get or generate credentials
	credentials, wasPlaceholder, err := r.getOrGenerateCredentials(ctx, &plan, provider, isEmptyCloud)
	if err != nil {
		resp.Diagnostics.AddError("Credentials Error", err.Error())
		return
	}
	// C9: a placeholder is expected and silent for a pure empty cloud (BYOC -
	// real credentials attach later via anyscale_cloud_resource). It's
	// suspicious for an all-in-one cloud: the user supplied a config block
	// but we still couldn't derive a credential from it, most likely a
	// forgotten IAM role/service-account field - warn instead of silently
	// submitting a cloud that can never actually provision anything.
	if wasPlaceholder && !isEmptyCloud {
		resp.Diagnostics.AddWarning(
			"Placeholder Credentials Generated",
			"No credentials were provided, and none could be derived from the aws_config/gcp_config/azure_config block; "+
				"a placeholder credential was generated so the apply could proceed. This cloud will not have valid "+
				"infrastructure access until you set the credentials attribute explicitly, or supply the field the "+
				"provider derives it from (e.g. aws_config.controlplane_iam_role_arn, or the GCP/Azure equivalents).",
		)
	}

	// Step 1: Create the cloud with minimal required fields
	createReq := CreateCloudRequest{
		Name:        name,
		Provider:    provider,
		Region:      region,
		Credentials: credentials,
	}

	jsonData, err := json.Marshal(createReq)
	if err != nil {
		resp.Diagnostics.AddError("JSON Marshal Error", err.Error())
		return
	}

	// Log sanitized request (redact sensitive fields like credentials)
	tflog.Debug(ctx, "POST /api/v2/clouds", map[string]any{"request": SanitizeJSONForLog(string(jsonData))})

	httpResp, err := r.client.DoRequest(ctx, "POST", "/api/v2/clouds", strings.NewReader(string(jsonData)))
	if err != nil {
		tflog.Error(ctx, "Failed to create cloud", map[string]any{"error": err.Error()})
		resp.Diagnostics.AddError("API Request Failed", err.Error())
		return
	}
	defer func() {
		if closeErr := httpResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		resp.Diagnostics.AddError("Response Read Error", err.Error())
		return
	}

	tflog.Debug(ctx, "POST /api/v2/clouds response", map[string]any{"status": httpResp.StatusCode, "body": string(body)})

	if httpResp.StatusCode != http.StatusCreated && httpResp.StatusCode != http.StatusOK {
		resp.Diagnostics.AddError(
			"Cloud Creation Failed",
			fmt.Sprintf("Failed to create cloud: %s - %s", httpResp.Status, string(body)),
		)
		return
	}

	var cloudResp CloudResponse
	if err := json.Unmarshal(body, &cloudResp); err != nil {
		resp.Diagnostics.AddError("JSON Unmarshal Error", err.Error())
		return
	}

	cloudID := cloudResp.Result.ID
	plan.ID = types.StringValue(cloudID)
	plan.IsEmptyCloud = types.BoolValue(isEmptyCloud)

	// Initialize CloudDeploymentID to known null - will be updated by addCloudResource if deployment succeeds
	if plan.CloudDeploymentID.IsUnknown() {
		plan.CloudDeploymentID = types.StringNull()
	}

	// compute_stack may still be unknown here (e.g. omitted on an empty cloud).
	// The create response already reports the backend's resolved value, so use
	// it directly instead of guessing - the partial state saved below then
	// matches what readCloudState would report anyway.
	if plan.ComputeStack.IsUnknown() {
		if cloudResp.Result.ComputeStack != "" {
			plan.ComputeStack = types.StringValue(cloudResp.Result.ComputeStack)
		} else {
			plan.ComputeStack = types.StringValue("VM")
		}
	}

	// Persist state now that the cloud exists remotely, before any subsequent
	// step (add_resource, wait, read-back) that can fail. Without this, a
	// mid-create failure below would leave the cloud orphaned in the backend
	// with no Terraform record to destroy it.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "Cloud created successfully", map[string]any{"id": cloudID, "name": name})

	if isEmptyCloud {
		// Skip add_resource call - resources will be added via anyscale_cloud_resource
		tflog.Info(ctx, "Created empty cloud - resources should be added via anyscale_cloud_resource", map[string]any{"id": cloudID})

		// Read back to get final state
		if err := r.readCloudState(ctx, cloudID, &plan); err != nil {
			resp.Diagnostics.AddError("Read Error", err.Error())
			return
		}

		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	// For all-in-one pattern, compute_stack is required
	if computeStack == "" {
		resp.Diagnostics.AddError(
			"Missing Required Field",
			"compute_stack is required when using embedded config (aws_config/gcp_config)",
		)
		return
	}

	// C13: region auto-detection only has a source to infer from for AWS
	// (subnet_ids_to_az) and only defaults a placeholder for the empty-cloud
	// pattern - a K8S-only cloud (no aws_config/gcp_config) with no explicit
	// region has neither, and plan.Region would otherwise still be an empty
	// string here. Guard rather than send Region: "" to add_resource: a
	// clear error here is far better than an opaque API failure.
	// Deliberately NOT inferring from kubernetes_config.zones
	// (region-from-zone parsing is provider-specific and error-prone - AWS
	// "us-west-2a" vs GCP "us-central1-a") and NOT making region Required on
	// the schema, which would break AWS users who rely on subnet inference
	// and never hit this path at all.
	if summary, detail, hasError := regionRequiredForCreateError(plan.Region.ValueString()); hasError {
		resp.Diagnostics.AddError(summary, detail)
		return
	}

	// Step 2: Build and add cloud resource/deployment
	if err := r.addCloudResource(ctx, &plan, cloudID, provider, computeStack); err != nil {
		resp.Diagnostics.AddError("Add Resource Failed", err.Error())
		return
	}

	// Step 3: Wait for cloud to be ready
	createTimeout := 30 * time.Minute
	if err := waitForCloudReady(ctx, r.client, cloudID, createTimeout); err != nil {
		tflog.Error(ctx, "Failed waiting for cloud to be ready", map[string]any{"error": err.Error()})
		resp.Diagnostics.AddError("Wait Error", err.Error())
		return
	}

	tflog.Info(ctx, "Cloud is ready", map[string]any{"id": cloudID})

	// Step 4: apply the cloud-level boolean toggles the user configured.
	// These are NOT part of CreateCloudRequest/add_resource - the backend only
	// exposes them via their own single-field PUT routes (same ones Update
	// calls) - so without this step, setting any of them in the very first
	// apply's config would never actually reach the API: the plan would show
	// the configured value, but the final readCloudState below would then
	// overwrite it with the backend's untouched default, producing "Provider
	// produced inconsistent result after apply" on the first apply for any
	// cloud that sets one of these. A freshly created cloud always starts
	// with all four false/unset, so comparing plan against that fixed
	// zero-value baseline (rather than a real prior state, which doesn't
	// exist yet) reuses the exact same diff-and-call logic Update() already
	// uses and has already been proven against.
	zeroValueState := CloudResourceModel{
		AutoAddUser:           types.BoolValue(false),
		EnableLineageTracking: types.BoolValue(false),
		EnableLogIngestion:    types.BoolValue(false),
		EnableSystemCluster:   types.BoolNull(),
	}
	if err := r.updateMutableFields(ctx, cloudID, plan, zeroValueState); err != nil {
		resp.Diagnostics.AddError("Failed to Apply Cloud Settings", fmt.Sprintf("Cloud %s was created, but applying its configured settings failed: %s", cloudID, err.Error()))
		return
	}

	// Read back final state
	if err := r.readCloudState(ctx, cloudID, &plan); err != nil {
		resp.Diagnostics.AddError("Read Error", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *CloudResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state CloudResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := state.ID.ValueString()
	tflog.Info(ctx, "Reading Anyscale Cloud", map[string]any{"id": cloudID})

	if err := r.readCloudState(ctx, cloudID, &state); err != nil {
		if strings.Contains(err.Error(), "not found") {
			tflog.Warn(ctx, "Cloud not found, removing from state", map[string]any{"id": cloudID})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read Error", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *CloudResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state CloudResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := plan.ID.ValueString()
	tflog.Info(ctx, "Updating Anyscale Cloud", map[string]any{"id": cloudID})

	if err := r.updateMutableFields(ctx, cloudID, plan, state); err != nil {
		AddAPIError(&resp.Diagnostics, "update cloud", err)
		return
	}

	tflog.Info(ctx, "Cloud updated successfully", map[string]any{"id": cloudID})

	// Read back updated state
	if err := r.readCloudState(ctx, cloudID, &plan); err != nil {
		AddAPIError(&resp.Diagnostics, "read cloud after update", err)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// updateMutableFields calls whichever of the cloud's three single-field PUT
// routes correspond to an actually-changed value between plan and state.
// There is no general PATCH on this resource (confirmed against the API
// reference: /clouds/{id} only supports GET and DELETE) - each boolean lives
// behind its own route, so each is only called when it changed, both to
// avoid redundant API calls when nothing changed and because a user might
// have permission for one of these routes but not another.
//
// name is deliberately absent here: it has no update endpoint at all, and
// the cloudNameImmutablePlanModifier on its schema attribute raises a
// plan-time error before Update is ever called with a changed name.
func (r *CloudResource) updateMutableFields(ctx context.Context, cloudID string, plan, state CloudResourceModel) error {
	if !plan.AutoAddUser.Equal(state.AutoAddUser) {
		if err := r.updateCloudBoolField(ctx, cloudID, "auto_add_user", plan.AutoAddUser.ValueBool()); err != nil {
			return fmt.Errorf("update auto_add_user: %w", err)
		}
	}
	if !plan.EnableLineageTracking.Equal(state.EnableLineageTracking) {
		if err := r.updateCloudBoolField(ctx, cloudID, "lineage_tracking_enabled", plan.EnableLineageTracking.ValueBool()); err != nil {
			return fmt.Errorf("update lineage_tracking_enabled: %w", err)
		}
	}
	if !plan.EnableLogIngestion.Equal(state.EnableLogIngestion) {
		if err := r.updateCloudAggregatedLogsConfig(ctx, cloudID, plan.EnableLogIngestion.ValueBool()); err != nil {
			return fmt.Errorf("update is_aggregated_logs_enabled: %w", err)
		}
	}
	// enable_system_cluster is Optional-only, not Computed (see schema doc -
	// no side-effect-free read exists for it), so unlike the three booleans
	// above, plan can genuinely be null (never configured). Only call the
	// endpoint when the user has actually asserted a value - null must never
	// be coerced to false, whether at Create (a fresh zero-state comparison)
	// or Update (config removed after previously being set).
	if !plan.EnableSystemCluster.IsNull() && !plan.EnableSystemCluster.Equal(state.EnableSystemCluster) {
		if err := r.updateCloudSystemClusterConfig(ctx, cloudID, plan.EnableSystemCluster.ValueBool()); err != nil {
			return fmt.Errorf("update system_cluster_config: %w", err)
		}
	}
	return nil
}

// Backoff for the transient auto_add_user 409 retry in updateCloudBoolField,
// same shape as waitForCloudReady (resource_cloud_resource.go): 5s initial,
// doubling, 60s cap, 3 minutes total. These are vars rather than consts
// purely so tests can override them to milliseconds for the duration of a
// test (save, set, defer-restore) instead of a unit test actually waiting
// out real minutes; production code never changes them.
var (
	autoAddUserRetryInitialBackoff = 5 * time.Second
	autoAddUserRetryMaxBackoff     = 60 * time.Second
	autoAddUserRetryTotalCap       = 3 * time.Minute
)

const autoAddUserRetryBackoffFactor = 2.0

// updateCloudBoolField calls one of the cloud's single-boolean PUT routes
// (auto_add_user or lineage_tracking_enabled). Both take the new value as a
// query parameter with an empty body - confirmed against the generated
// OpenAPI client (the ground truth for the wire format), since neither
// route accepts a JSON request body.
//
// auto_add_user's backend check queries pending auto-add-user reconciliation
// org-wide, not scoped to this cloud (confirmed against the product repo,
// read only), so it can 409 with a transient "still being applied, try
// again" error even when this specific cloud has no in-flight change of its
// own - a real user can hit this on an ordinary terraform apply, not just
// concurrent CI runs. Retrying that one documented condition here fixes the
// real UX, not just a test flake; every other error, including any other
// 409, still propagates on the first attempt. Sharing this retry with
// lineage_tracking_enabled is fine: it keys on the specific error message,
// not the field name, so it only ever engages for the auto_add_user case.
//
// Retrying is safe here ONLY because this specific PUT is idempotent -
// setting the same boolean value twice has no side effect - and because the
// retry condition below matches narrowly on the one documented transient
// 409, not on 409s or errors in general. This is not a general-purpose
// retry-on-failure; do not widen the match to make other errors retry too.
func (r *CloudResource) updateCloudBoolField(ctx context.Context, cloudID, fieldName string, value bool) error {
	path := fmt.Sprintf("/api/v2/clouds/%s/%s?%s=%t", cloudID, fieldName, fieldName, value)

	deadline := time.Now().Add(autoAddUserRetryTotalCap)
	currentBackoff := autoAddUserRetryInitialBackoff

	for {
		tflog.Debug(ctx, "PUT "+path)
		_, err := DoRequestRaw(ctx, r.client, "PUT", path, nil, http.StatusOK, http.StatusNoContent)
		if err == nil {
			return nil
		}
		if !isTransientAutoAddUserConflict(err) || !time.Now().Before(deadline) {
			return err
		}

		tflog.Warn(ctx, "Transient auto_add_user conflict, retrying", map[string]any{
			"cloud_id": cloudID,
			"field":    fieldName,
			"backoff":  currentBackoff.String(),
		})

		select {
		case <-ctx.Done():
			return err
		case <-time.After(currentBackoff):
		}

		currentBackoff = time.Duration(float64(currentBackoff) * autoAddUserRetryBackoffFactor)
		if currentBackoff > autoAddUserRetryMaxBackoff {
			currentBackoff = autoAddUserRetryMaxBackoff
		}
	}
}

// isTransientAutoAddUserConflict reports whether err is the documented,
// explicitly-retryable 409 from the backend's org-wide auto-add-user
// reconciliation check (see updateCloudBoolField). Any other error,
// including a differently-worded 409, must propagate immediately rather
// than be retried.
func isTransientAutoAddUserConflict(err error) bool {
	msg := err.Error()
	if !strings.Contains(msg, "status 409") {
		return false
	}
	return strings.Contains(msg, "still being applied") || strings.Contains(msg, "try again")
}

// updateCloudAggregatedLogsConfig calls the aggregated-logs PUT route. Its
// query parameter is named is_enabled, not is_aggregated_logs_enabled - a
// real naming mismatch confirmed against the backend router; using the
// schema's own field name here would silently no-op against the real API.
func (r *CloudResource) updateCloudAggregatedLogsConfig(ctx context.Context, cloudID string, enabled bool) error {
	path := fmt.Sprintf("/api/v2/clouds/%s/update_customer_aggregated_logs_config?is_enabled=%t", cloudID, enabled)
	tflog.Debug(ctx, "PUT "+path)
	_, err := DoRequestRaw(ctx, r.client, "PUT", path, nil, http.StatusOK, http.StatusNoContent)
	return err
}

// updateCloudSystemClusterConfig calls the system-cluster config PUT route.
// Its query parameter is named is_enabled, not enable_system_cluster or
// system_cluster - same real path-vs-query-param naming mismatch as
// updateCloudAggregatedLogsConfig, confirmed against clouds_router.py's
// update_system_cluster_config. Deliberately does NOT call the separate
// POST /{cloud_id}/terminate route: that terminates the actual running
// cluster (heavier, async, requires an existing cluster) and is a distinct
// concept from this config-level enable/disable toggle - bool=false must
// only ever reach this endpoint, never terminate.
func (r *CloudResource) updateCloudSystemClusterConfig(ctx context.Context, cloudID string, enabled bool) error {
	path := fmt.Sprintf("/api/v2/clouds/%s/update_system_cluster_config?is_enabled=%t", cloudID, enabled)
	tflog.Debug(ctx, "PUT "+path)
	_, err := DoRequestRaw(ctx, r.client, "PUT", path, nil, http.StatusOK, http.StatusNoContent)
	return err
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *CloudResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state CloudResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := state.ID.ValueString()
	tflog.Info(ctx, "Deleting Anyscale Cloud", map[string]any{"id": cloudID})

	// Before deleting the cloud, detach any machine pools that are attached to it
	if err := r.detachMachinePoolsFromCloud(ctx, cloudID); err != nil {
		tflog.Warn(ctx, "Failed to detach machine pools from cloud", map[string]any{
			"cloud_id": cloudID,
			"error":    err.Error(),
		})
		// Continue with deletion - the API will tell us if we can't delete
	}

	_, err := DoRequestRaw(ctx, r.client, "DELETE", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil,
		http.StatusOK, http.StatusNoContent, http.StatusNotFound)
	if err != nil {
		tflog.Error(ctx, "Failed to delete cloud", map[string]any{"error": err.Error()})
		AddAPIError(&resp.Diagnostics, "delete cloud", err)
		return
	}

	tflog.Info(ctx, "Cloud deleted successfully", map[string]any{"id": cloudID})
}

// detachMachinePoolsFromCloud detaches all machine pools attached to the given cloud.
func (r *CloudResource) detachMachinePoolsFromCloud(ctx context.Context, cloudID string) error {
	tflog.Debug(ctx, "Listing machine pools to check for attachments", map[string]any{"cloud_id": cloudID})

	// List all machine pools
	listResp, err := DoRequestAndParse[ListMachinePoolsResponse](
		ctx,
		r.client,
		"GET",
		"/api/v2/machine_pools/",
		nil,
		http.StatusOK,
	)
	if err != nil {
		return fmt.Errorf("failed to list machine pools: %w", err)
	}

	// Find and detach pools attached to this cloud
	for _, pool := range listResp.Result.MachinePools {
		for _, attachedCloudID := range pool.CloudIDs {
			if attachedCloudID == cloudID {
				tflog.Info(ctx, "Detaching machine pool from cloud", map[string]any{
					"pool":     pool.MachinePoolName,
					"cloud_id": cloudID,
				})

				detachReq := DetachMachinePoolFromCloudRequest{
					MachinePoolName: pool.MachinePoolName,
					CloudID:         cloudID,
				}

				reqBody, err := MarshalRequestBody(detachReq)
				if err != nil {
					return fmt.Errorf("failed to marshal detach request: %w", err)
				}

				_, err = DoRequestRaw(
					ctx,
					r.client,
					"POST",
					"/api/v2/machine_pools/detach",
					reqBody,
					http.StatusOK,
					http.StatusNotFound,
				)
				if err != nil {
					return fmt.Errorf("failed to detach machine pool %s: %w", pool.MachinePoolName, err)
				}

				tflog.Info(ctx, "Machine pool detached from cloud", map[string]any{
					"pool":     pool.MachinePoolName,
					"cloud_id": cloudID,
				})
				break // Move to next pool
			}
		}
	}

	return nil
}

// ImportState imports an existing resource into Terraform state.
//
// C3-v2: this is the ONLY place that recovers aws_config/gcp_config/
// kubernetes_config/object_storage from the API - never Create or Read (see
// backfillComputedCloudFields). ImportState runs once, before Terraform's
// plan-consistency machinery is in the loop, so setting a non-Computed
// attribute here carries none of the "provider produced inconsistent result"
// risk that populating it in Create/Read does.
//
// Only the compute-stack-REQUIRED block(s) are recovered - VM gets aws_config
// or gcp_config (whichever the provider is), K8S gets kubernetes_config AND
// object_storage (both required for K8S). Optional/auxiliary blocks
// (file_storage anywhere; object_storage for VM; aws_config/gcp_config for
// K8S) are deliberately left null: recovering an optional block the user
// never had is exactly the ambiguity C3-v2 exists to avoid, since a later
// Read can never safely distinguish "recovered at import" from "genuinely
// absent" the way it could get away with for the always-required blocks.
// Add optional blocks to your .tf after import and reconcile manually if
// you used them (they're RequiresReplace).
func (r *CloudResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	cloudID := req.ID
	tflog.Info(ctx, "Importing Anyscale Cloud", map[string]any{"id": cloudID})

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), cloudID)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudResp, err := DoRequestAndParse[CloudResponse](ctx, r.client, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil, http.StatusOK)
	if err != nil {
		tflog.Warn(ctx, "Failed to read cloud during import; config blocks will not be recovered - the subsequent Read will surface any real error", map[string]any{"cloud_id": cloudID, "error": err.Error()})
		return
	}

	resources, err := listCloudResources(ctx, r.client, cloudID)
	if err != nil {
		tflog.Warn(ctx, "Failed to list cloud resources during import; config blocks will not be recovered", map[string]any{"cloud_id": cloudID, "error": err.Error()})
		return
	}

	defaultResource := findDefaultInCloudResources(resources)
	blocks, diags := requiredImportConfigBlocks(ctx, cloudResp.Result.Provider, defaultResource)
	resp.Diagnostics.Append(diags...)
	for attrName, obj := range blocks {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(attrName), obj)...)
	}
}

// ─── Helper Functions (continued) ─────────────────────────────────────────────

// getOrGenerateCredentials extracts credentials from config or generates placeholder
// getOrGenerateCredentials resolves credentials in priority order: explicit
// plan.Credentials, then derived from the provider's config block, then a
// fabricated placeholder as a last resort (needed for the empty-cloud
// pattern, where credentials are legitimately unknown until a
// anyscale_cloud_resource is attached later).
//
// wasPlaceholder tells the caller whether the last resort fired, so it can
// decide whether to warn (C9): a fabricated credential is expected and
// silent for a pure empty cloud, but suspicious - almost certainly a
// forgotten role/service-account field - when the user DID supply a config
// block and we still couldn't derive anything from it.
func (r *CloudResource) getOrGenerateCredentials(ctx context.Context, plan *CloudResourceModel, provider string, isEmptyCloud bool) (credentials string, wasPlaceholder bool, err error) {
	// Check explicit credentials field first
	if !plan.Credentials.IsNull() && plan.Credentials.ValueString() != "" {
		return plan.Credentials.ValueString(), false, nil
	}

	// Try to extract from config blocks (all-in-one pattern)
	switch strings.ToUpper(provider) {
	case "AWS":
		if !plan.AWSConfig.IsNull() {
			awsConfig, err := expandAWSConfig(ctx, plan.AWSConfig)
			if err != nil {
				return "", false, err
			}
			if awsConfig != nil && awsConfig.AnyscaleIAMRoleID != "" {
				return awsConfig.AnyscaleIAMRoleID, false, nil
			}
		}
	case "GCP":
		if !plan.GCPConfig.IsNull() {
			gcpConfig, err := expandGCPConfig(ctx, plan.GCPConfig)
			if err != nil {
				return "", false, err
			}
			if gcpConfig != nil {
				// For GCP, credentials must be a JSON object
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
					return "", false, fmt.Errorf("failed to marshal GCP credentials: %w", err)
				}
				return string(credsJSON), false, nil
			}
		}
	case "AZURE":
		// Azure clouds are Kubernetes-only, so unlike AWS/GCP there is no
		// azure_config field to derive a credential from (azure_config is
		// tenant_id only, which isn't a credential) - the POST /api/v2/clouds
		// credential is largely ceremonial for K8S clouds anyway (real auth
		// is operator workload-identity federation), so mirror the AWS case
		// above and derive it from the operator identity, confirmed against
		// the backend to accept an arbitrary string here (no Azure-specific
		// credentials parser exists - see clouds_resource.py's
		// create_cloud_without_permissions, which only special-cases GCP).
		if !plan.KubernetesConfig.IsNull() {
			var k8sModel KubernetesConfigModel
			diags := plan.KubernetesConfig.As(ctx, &k8sModel, basetypes.ObjectAsOptions{})
			if !diags.HasError() && !k8sModel.AnyscaleOperatorIAMIdentity.IsNull() && k8sModel.AnyscaleOperatorIAMIdentity.ValueString() != "" {
				return k8sModel.AnyscaleOperatorIAMIdentity.ValueString(), false, nil
			}
		}
	}

	// Generate unique placeholder for empty cloud pattern
	uniqueSuffix := generateRandomString(12)
	switch strings.ToUpper(provider) {
	case "AWS":
		return fmt.Sprintf("arn:aws:iam::000000000000:role/anyscale-placeholder-%s", uniqueSuffix), true, nil
	case "GCP":
		placeholderCreds := map[string]string{
			"provider_id":           fmt.Sprintf("projects/000000000000/locations/global/workloadIdentityPools/placeholder-%s/providers/placeholder", uniqueSuffix),
			"project_id":            "placeholder-project",
			"service_account_email": fmt.Sprintf("placeholder-%s@placeholder-project.iam.gserviceaccount.com", uniqueSuffix),
		}
		credsJSON, _ := json.Marshal(placeholderCreds)
		return string(credsJSON), true, nil
	default:
		return fmt.Sprintf("placeholder-%s", uniqueSuffix), true, nil
	}
}

// addCloudResource adds a cloud resource/deployment to an existing cloud
func (r *CloudResource) addCloudResource(ctx context.Context, plan *CloudResourceModel, cloudID, provider, computeStack string) error {
	region := plan.Region.ValueString()
	isPrivate := plan.IsPrivateCloud.ValueBool()

	networkingMode := "PUBLIC"
	if isPrivate {
		networkingMode = "PRIVATE"
	}

	deployReq := CloudDeploymentRequest{
		Name:           fmt.Sprintf("%s-%s-%s", strings.ToLower(computeStack), strings.ToLower(provider), strings.ToLower(region)),
		Provider:       provider,
		ComputeStack:   computeStack,
		Region:         region,
		NetworkingMode: networkingMode,
	}

	// Add provider-specific configuration
	if err := buildProviderConfig(ctx, &deployReq, provider, computeStack, plan.AWSConfig, plan.GCPConfig, plan.AzureConfig, plan.KubernetesConfig, plan.ObjectStorage, plan.FileStorage); err != nil {
		return err
	}

	// Note: Cloud-level settings (auto_add_user, enable_lineage_tracking, enable_log_ingestion)
	// are set during cloud creation (POST /api/v2/clouds), NOT during add_resource (PUT /api/v2/clouds/{id}/add_resource)

	deployJSON, err := json.Marshal(deployReq)
	if err != nil {
		return err
	}

	tflog.Info(ctx, "Adding cloud resource/deployment", map[string]any{"cloud_id": cloudID})
	// Log sanitized request (redact sensitive fields)
	tflog.Debug(ctx, "PUT /api/v2/clouds/"+cloudID+"/add_resource", map[string]any{"request": SanitizeJSONForLog(string(deployJSON))})

	// add_resource registers real cloud infrastructure server-side and can
	// legitimately run well past DoRequest's default deadline.
	addResourceCtx, cancel := context.WithTimeout(ctx, addResourceRequestTimeout)
	defer cancel()

	deployResp, err := r.client.DoRequest(addResourceCtx, "PUT", fmt.Sprintf("/api/v2/clouds/%s/add_resource", cloudID), strings.NewReader(string(deployJSON)))
	if err != nil {
		tflog.Error(ctx, "Failed to add cloud resource", map[string]any{"error": err.Error()})
		return err
	}
	defer func() {
		if closeErr := deployResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	deployBody, err := io.ReadAll(deployResp.Body)
	if err != nil {
		return err
	}

	tflog.Debug(ctx, "PUT /api/v2/clouds/"+cloudID+"/add_resource response", map[string]any{"status": deployResp.StatusCode, "body": string(deployBody)})

	if deployResp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to add cloud resource: %s - %s", deployResp.Status, string(deployBody))
	}

	// Parse response to get cloud_deployment_id
	var deployResult CloudDeploymentResponse
	if err := json.Unmarshal(deployBody, &deployResult); err != nil {
		tflog.Warn(ctx, "Failed to parse add_resource response", map[string]any{"error": err.Error()})
	} else if deployResult.Result.CloudDeploymentID != "" {
		plan.CloudDeploymentID = types.StringValue(deployResult.Result.CloudDeploymentID)
		tflog.Info(ctx, "Cloud deployment ID assigned", map[string]any{"deployment_id": deployResult.Result.CloudDeploymentID})
	}

	tflog.Info(ctx, "Cloud resource added successfully", map[string]any{"cloud_id": cloudID})
	return nil
}

// readCloudState reads the cloud from the API and updates the state model
func (r *CloudResource) readCloudState(ctx context.Context, cloudID string, state *CloudResourceModel) error {
	resp, err := r.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("cloud not found")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to read cloud: %s - %s", resp.Status, string(body))
	}

	var cloudResp CloudResponse
	if err := json.Unmarshal(body, &cloudResp); err != nil {
		return err
	}

	// Update state with API response (only update fields that aren't ForceNew or that we track)
	state.ID = types.StringValue(cloudResp.Result.ID)
	state.Name = types.StringValue(cloudResp.Result.Name)
	state.CloudProvider = types.StringValue(cloudResp.Result.Provider)
	state.Region = types.StringValue(cloudResp.Result.Region)

	// Refresh cloud-level boolean settings from the API. These are Optional+Computed
	// with a Default of false; rehydrating them keeps import round-tripping lossless.
	state.IsPrivateCloud = types.BoolValue(cloudResp.Result.IsPrivateCloud)
	state.AutoAddUser = types.BoolValue(cloudResp.Result.AutoAddUser)
	state.EnableLineageTracking = types.BoolValue(cloudResp.Result.LineageTrackingEnabled)
	state.EnableLogIngestion = types.BoolValue(cloudResp.Result.IsAggregatedLogsEnabled)

	// If CloudDeploymentID is still unknown/null, set it to null explicitly
	if state.CloudDeploymentID.IsUnknown() {
		state.CloudDeploymentID = types.StringNull()
	}

	// C3 v2: backfill ONLY the two Computed fields (is_empty_cloud,
	// cloud_deployment_id) from the cloud's resources. Config blocks
	// (aws_config/gcp_config/kubernetes_config/object_storage/file_storage)
	// are NOT Computed, so they may only ever equal what Create/Update saw in
	// the plan - populating them here, in the shared Create/Read path, is
	// exactly what caused the C12-exposed regression: a K8S-only create
	// (aws_config/gcp_config genuinely absent, optional for K8S) got
	// aws_config injected on the very first post-create Read, and Terraform
	// hard-errored with "inconsistent result after apply: .aws_config was
	// absent, but now present" - a fresh create's first Read starts with
	// null blocks exactly like a fresh import does, and this function had no
	// way to tell the two apart. Config-block recovery now lives ONLY in
	// ImportState (see there), which runs once, before Terraform's own
	// plan-consistency machinery is in the loop at all.
	resources, err := listCloudResources(ctx, r.client, cloudID)
	if err != nil {
		tflog.Warn(ctx, "Failed to list cloud resources; skipping Computed-field backfill and compute_stack correction this read", map[string]any{"cloud_id": cloudID, "error": err.Error()})
		// compute_stack (see below): no resources available to consult this
		// read, fall back to the cloud-level derived value.
		if cloudResp.Result.ComputeStack != "" {
			state.ComputeStack = types.StringValue(cloudResp.Result.ComputeStack)
		}
	} else {
		r.backfillComputedCloudFields(state, resources)

		// compute_stack: GET /clouds/{id}'s own compute_stack is backend-DERIVED
		// from whatever resource the backend considers primary, defaulting to VM
		// if it doesn't recognize one - correct for a cloud actually created VM,
		// but a real risk for a cold or non-standard import of a K8S cloud
		// (confirmed: a standard Terraform-created-then-imported K8S cloud
		// round-trips fine today, because the backend's own derivation agrees
		// with our default-resource pick in that common case - this is
		// defense-in-depth for when it doesn't). Source from the SAME
		// default-resource lookup requiredImportConfigBlocks already trusts as
		// authoritative for recovering kubernetes_config/object_storage, rather
		// than re-deriving from the cloud-level field, whenever a default
		// resource exists. Only fall back to the cloud-level derived value for
		// a genuinely empty cloud (zero resources - nothing to consult, and the
		// cloud-level VM default is correct there).
		defaultResource := findDefaultInCloudResources(resources)
		// Hardening: if nothing is flagged is_default but there is EXACTLY one
		// resource, there is no ambiguity to resolve - use it directly. This
		// guards a cold-imported cloud (e.g. registered via the CLI, never
		// Terraform-created) whose single resource might not carry the
		// is_default flag the same way a Terraform-created cloud's does;
		// without this, such a cloud would silently fall through to the
		// cloud-level field below and could still reproduce the VM
		// misclassification this fix exists to prevent.
		if defaultResource == nil && len(resources) == 1 {
			defaultResource = &resources[0]
		}
		if defaultResource != nil && defaultResource.ComputeStack != "" {
			state.ComputeStack = types.StringValue(defaultResource.ComputeStack)
		} else if cloudResp.Result.ComputeStack != "" {
			state.ComputeStack = types.StringValue(cloudResp.Result.ComputeStack)
		}
	}

	tflog.Info(ctx, "Cloud state read successfully", map[string]any{"id": cloudID, "name": cloudResp.Result.Name})
	return nil
}

// backfillComputedCloudFields fills in is_empty_cloud and cloud_deployment_id
// from the cloud's resources. Both are Computed, so the provider may set them
// at any time without risking a plan-consistency error - unlike the
// non-Computed config blocks (see C3-v2; this function deliberately does not
// touch them).
//
// is_empty_cloud is sticky: it's derived from "zero resources attached" only
// while still null/unknown (a fresh import never ran Create, so it starts
// that way); once resolved - true OR false - it is never re-derived. Without
// this, an intentionally-empty cloud that later gets a anyscale_cloud_resource
// attached would flip empty->non-empty on its next refresh.
func (r *CloudResource) backfillComputedCloudFields(state *CloudResourceModel, resources []CloudDeploymentResult) {
	state.IsEmptyCloud = resolveIsEmptyCloud(state.IsEmptyCloud, len(resources))
	if state.IsEmptyCloud.ValueBool() {
		return
	}

	defaultResource := findDefaultInCloudResources(resources)
	if defaultResource == nil {
		return
	}

	if state.CloudDeploymentID.IsNull() && defaultResource.CloudDeploymentID != "" {
		state.CloudDeploymentID = types.StringValue(defaultResource.CloudDeploymentID)
	}
}
