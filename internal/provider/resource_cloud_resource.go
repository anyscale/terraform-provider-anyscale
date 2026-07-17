package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	_ resource.Resource                   = &CloudResourceResource{}
	_ resource.ResourceWithConfigure      = &CloudResourceResource{}
	_ resource.ResourceWithImportState    = &CloudResourceResource{}
	_ resource.ResourceWithValidateConfig = &CloudResourceResource{}
)

// statusDeprecationMessage: status and operator_status are set from the same
// underlying value in readCloudResource; status is also always null for VM
// cloud resources, making operator_status the clearer name. cloud_resource
// only - anyscale_cloud/its data source's status/state fields are the
// distinct cloud lifecycle status, not an operator_status duplicate.
const statusDeprecationMessage = "Duplicates `operator_status` (identical value; always null for VM cloud resources). Will be removed in a future major release - use `operator_status` instead."

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
	AzureConfig      types.Object `tfsdk:"azure_config"`
	KubernetesConfig types.Object `tfsdk:"kubernetes_config"`

	// Storage configurations
	ObjectStorage types.Object `tfsdk:"object_storage"`
	FileStorage   types.Object `tfsdk:"file_storage"`

	// Computed fields
	CloudResourceID   types.String `tfsdk:"cloud_resource_id"`
	CloudDeploymentID types.String `tfsdk:"cloud_deployment_id"`
	Status            types.String `tfsdk:"status"`
	OperatorStatus    types.String `tfsdk:"operator_status"`
	OperatorVersion   types.String `tfsdk:"operator_version"`
	ReportedAt        types.String `tfsdk:"reported_at"`
	IsDefault         types.Bool   `tfsdk:"is_default"`

	// Internal
	ID types.String `tfsdk:"id"`
}

// AWSConfigModel represents AWS-specific configuration.
type AWSConfigModel struct {
	VPCID                    types.String `tfsdk:"vpc_id"`
	SubnetIDs                types.List   `tfsdk:"subnet_ids"`
	SubnetIDsToAZ            types.Map    `tfsdk:"subnet_ids_to_az"`
	SecurityGroupIDs         types.List   `tfsdk:"security_group_ids"`
	ControlplaneIAMRoleARN   types.String `tfsdk:"controlplane_iam_role_arn"`
	DataplaneIAMRoleARN      types.String `tfsdk:"dataplane_iam_role_arn"`
	ClusterInstanceProfileID types.String `tfsdk:"cluster_instance_profile_id"`
	ExternalID               types.String `tfsdk:"external_id"`
	MemoryDBClusterName      types.String `tfsdk:"memorydb_cluster_name"`
	MemoryDBClusterARN       types.String `tfsdk:"memorydb_cluster_arn"`
	MemoryDBClusterEndpoint  types.String `tfsdk:"memorydb_cluster_endpoint"`
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
	FileStorageID            types.String `tfsdk:"file_storage_id"`
	MountPath                types.String `tfsdk:"mount_path"`
	PersistentVolumeClaim    types.String `tfsdk:"persistent_volume_claim"`
	CSIEphemeralVolumeDriver types.String `tfsdk:"csi_ephemeral_volume_driver"`
	MountTargets             types.List   `tfsdk:"mount_targets"`
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
				MarkdownDescription: "Composite identifier in format cloud_id:name",
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
				Required:            true,
				MarkdownDescription: "The name of the cloud resource. Must be a non-empty string, distinct among resources on the same cloud. Part of the resource's identity - used in the `cloud_id:name` import ID - so changing it requires replacing the resource. If Terraform state is lost, re-applying does not recover the existing resource: a configuration with the same name fails with a duplicate-name error. Use `terraform import` to recover state instead.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},

			// ─── Compute Configuration ────────────────────────────
			"cloud_provider": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Cloud provider: AWS, GCP, or AZURE. Required for K8S compute_stack when aws_config/gcp_config/azure_config is not provided. Inferred from aws_config/gcp_config/azure_config if not specified. AWS and GCP support both VM and K8S compute stacks; AZURE supports K8S only (AKS) - Anyscale does not support Azure VM clouds, and setting azure_config with any other compute_stack is a plan-time error. GENERIC is not yet supported by this provider.",
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
				Validators: []validator.String{
					stringvalidator.OneOf("VM", "K8S"),
				},
			},

			"region": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The region for this cloud resource. Inferred from the cloud/provider configuration when not specified. For AWS, Anyscale does not support the China or GovCloud partitions.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"is_private": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Whether this is a private resource. Implies customer-managed networking paths (e.g. VPN, PrivateLink) between users, clusters, and the control plane - a real infrastructure commitment, not just a network visibility flag.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},

			// ─── Computed Fields ──────────────────────────────────
			"cloud_resource_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique cloud resource ID assigned by Anyscale when this resource deployment was registered - the populated identifier that `cloud_deployment_id` was originally meant to be. This is what you pass to the Anyscale operator during installation for a K8S cloud (as `global.cloudDeploymentId` in the operator's Helm values, despite the key's name - the value is this resource id). `anyscale_cloud`'s own `cloud_resource_id` attribute exposes the same populated identifier for the all-in-one pattern. Stable for the life of this resource deployment - it does not move out of band between applies.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"cloud_deployment_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The cloud deployment ID. The Anyscale API no longer populates this field; use `cloud_resource_id` instead.",
				DeprecationMessage:  cloudDeploymentIDDeprecationMessage,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The operator status of the cloud resource. Duplicates `operator_status` (identical value; null for VM); use `operator_status` instead.",
				DeprecationMessage:  statusDeprecationMessage,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"operator_status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The status of the Anyscale Operator (Kubernetes cloud resources only; null for VM). Same underlying value as `status`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"operator_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The version of the Anyscale Operator that last reported status (Kubernetes cloud resources only; null for VM, or if the operator has not yet reported).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"reported_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the Anyscale Operator last reported status (Kubernetes cloud resources only; null for VM, or if the operator has not yet reported).",
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
				MarkdownDescription: "AWS-specific configuration. See the [Anyscale AWS cloud configuration documentation](https://docs.anyscale.com/clouds/aws/configure) for the full set of resources Anyscale expects (VPC, subnets, IAM roles, security groups) and how they map to the fields below.",
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
						MarkdownDescription: "List of subnet IDs for Anyscale resources. Use this OR subnet_ids_to_az. VM compute only - EKS networking comes entirely from `kubernetes_config.zones`, so setting this on a Kubernetes cloud is rejected at plan time. Left unchecked, this alone would risk a confusing subnet-and-zone-count mismatch; combined with `subnet_ids_to_az` it would silently corrupt the registered networking instead.",
						PlanModifiers: []planmodifier.List{
							listplanmodifier.RequiresReplace(),
						},
					},
					"subnet_ids_to_az": schema.MapAttribute{
						ElementType:         types.StringType,
						Optional:            true,
						MarkdownDescription: "Map of subnet ID to availability zone (e.g., {\"subnet-123\": \"us-east-2a\"}). Preferred over subnet_ids. VM compute only - EKS networking comes entirely from `kubernetes_config.zones`, so setting this on a Kubernetes cloud is rejected at plan time rather than silently corrupting the registered networking (the backend applies this unconditionally after the Kubernetes zone list is written).",
						PlanModifiers: []planmodifier.Map{
							mapplanmodifier.RequiresReplace(),
						},
					},
					"security_group_ids": schema.ListAttribute{
						ElementType:         types.StringType,
						Optional:            true,
						MarkdownDescription: "List of security group IDs for Anyscale resources. Missing the required rules causes the cluster to fail silently rather than erroring at plan or apply time - Anyscale needs at minimum an inbound rule for port 443 and a self-referencing rule allowing all traffic.",
						PlanModifiers: []planmodifier.List{
							listplanmodifier.RequiresReplace(),
						},
					},
					"controlplane_iam_role_arn": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "IAM role ARN for Anyscale control plane (cross-account access). See the [Anyscale AWS IAM documentation](https://docs.anyscale.com/iam/aws) for the trust policy and permissions this role needs.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"dataplane_iam_role_arn": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "IAM role ARN for Anyscale data plane (cluster nodes). See the [Anyscale AWS IAM documentation](https://docs.anyscale.com/iam/aws) for the trust policy and permissions this role needs.",
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
						MarkdownDescription: "External ID for IAM role assumption (recommended for security). Anyscale's external IDs follow a fixed format: the organization ID, a hyphen, then a random string (e.g. `org_1234567890abcdef-1234567890abcdef`). See the [Anyscale AWS IAM documentation](https://docs.anyscale.com/iam/aws) for the full trust policy.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"memorydb_cluster_name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "MemoryDB cluster name for Ray GCS fault tolerance. See the [Anyscale head node fault tolerance documentation](https://docs.anyscale.com/administration/resource-management/head-node-fault-tolerance) for cluster requirements.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"memorydb_cluster_arn": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "MemoryDB cluster ARN. See the [Anyscale head node fault tolerance documentation](https://docs.anyscale.com/administration/resource-management/head-node-fault-tolerance) for cluster requirements.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"memorydb_cluster_endpoint": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "MemoryDB cluster endpoint address, formatted as `<name>.<random>.clustercfg.memorydb.<region>.amazonaws.com:6379`. Requires TLS - use a `rediss://` prefix when connecting. Conflicts with `kubernetes_config.redis_endpoint` - the backend rejects more than one GCS fault-tolerance backing store on the same cloud. See the [Anyscale head node fault tolerance documentation](https://docs.anyscale.com/administration/resource-management/head-node-fault-tolerance) for full cluster requirements.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
				},
			},

			// ─── GCP Configuration ────────────────────────────────
			"gcp_config": schema.SingleNestedBlock{
				MarkdownDescription: "GCP-specific configuration. See the [Anyscale GCP cloud configuration documentation](https://docs.anyscale.com/clouds/gcp/configure) for the full set of resources Anyscale expects (VPC, subnets, service accounts, firewall policies) and how they map to the fields below.",
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
						MarkdownDescription: "List of subnet names within the VPC for Anyscale resources. VM compute only - GKE networking comes entirely from `kubernetes_config.zones`, so setting this on a Kubernetes cloud is rejected at plan time rather than silently corrupting the registered networking (the backend applies this field unconditionally after the Kubernetes zone list is written, discarding it). Genuinely supports more than one subnet on VM compute - Anyscale spreads instances across whichever are configured, this is not a modeling mismatch.",
						PlanModifiers: []planmodifier.List{
							listplanmodifier.RequiresReplace(),
						},
					},
					"controlplane_service_account_email": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Service account email for Anyscale control plane. See the [Anyscale Google Cloud IAM documentation](https://docs.anyscale.com/iam/google-cloud) for the roles this service account needs.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"dataplane_service_account_email": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Service account email for Ray cluster nodes. See the [Anyscale Google Cloud IAM documentation](https://docs.anyscale.com/iam/google-cloud) for the roles this service account needs.",
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
						MarkdownDescription: "Memorystore endpoint address. Unlike AWS MemoryDB, Memorystore does not support TLS for this connection. Conflicts with `kubernetes_config.redis_endpoint` - the backend rejects more than one GCS fault-tolerance backing store on the same cloud. See the [Anyscale head node fault tolerance documentation](https://docs.anyscale.com/administration/resource-management/head-node-fault-tolerance) for full cluster requirements.",
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
				MarkdownDescription: "Kubernetes-specific configuration. Required when compute_stack is K8S. See the [Anyscale Kubernetes documentation](https://docs.anyscale.com/clouds/kubernetes) for cluster requirements and how these fields map to the Anyscale Operator installation.",
				Attributes: map[string]schema.Attribute{
					"anyscale_operator_iam_identity": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "The IAM identity for the Anyscale operator. For AWS EKS: IAM role ARN (see the [Anyscale EKS IAM documentation](https://docs.anyscale.com/iam/eks)). For GCP GKE: service account email (see the [Anyscale GKE IAM documentation](https://docs.anyscale.com/iam/gke)). For Azure AKS: the managed identity's principal ID (not its client ID - the reference AKS setup flow distinguishes the two: principal ID here, client ID only in the operator's own values.yaml).",
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
				MarkdownDescription: "Object storage configuration (S3, GCS, Azure Blob, or S3-compatible). See the Anyscale documentation for bucket setup: [S3](https://docs.anyscale.com/storage/s3) for AWS, [GCS](https://docs.anyscale.com/storage/gcs) for GCP, [Azure Blob/ADLS](https://docs.anyscale.com/clouds/azure/storage) for Azure.",
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
				MarkdownDescription: "File storage configuration (EFS, Filestore, etc.). If omitted, Anyscale falls back to using the object storage bucket for shared storage. On GCP, Filestore is optional and not created by default, and must be in the same region as the cloud's VPC when used. See the [Anyscale shared storage documentation](https://docs.anyscale.com/storage/shared) for how this is used across a cluster.",
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
						MarkdownDescription: "The mount path for the file storage. Only meaningful for GCP Filestore and Azure/Generic NFS-backed clouds - AWS EFS-backed clouds have no backend field for it, and the value is rejected at plan time if set there (see the provider's own validation error for details). Mutually exclusive with `persistent_volume_claim` and `csi_ephemeral_volume_driver` (the Kubernetes-native shared-storage mechanisms, which have no mount_path field on the backend either) - at most one of mount_path / persistent_volume_claim / csi_ephemeral_volume_driver may be set. Changing this requires replacement; the provider has no in-place update path for it.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
						Validators: []validator.String{
							stringvalidator.ConflictsWith(
								path.MatchRoot("file_storage").AtName("persistent_volume_claim"),
								path.MatchRoot("file_storage").AtName("csi_ephemeral_volume_driver"),
							),
						},
					},
					"persistent_volume_claim": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Name of a Kubernetes PersistentVolumeClaim to mount for shared storage (Kubernetes cloud resources only). Mutually exclusive with `csi_ephemeral_volume_driver` - the backend rejects both being set. Also mutually exclusive with `mount_path`, which has no effect once this is set.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
						Validators: []validator.String{
							stringvalidator.ConflictsWith(
								path.MatchRoot("file_storage").AtName("csi_ephemeral_volume_driver"),
								path.MatchRoot("file_storage").AtName("mount_path"),
							),
						},
					},
					"csi_ephemeral_volume_driver": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "CSI driver name for an ephemeral inline volume to use for shared storage (Kubernetes cloud resources only). Mutually exclusive with `persistent_volume_claim` - the backend rejects both being set. Also mutually exclusive with `mount_path`, which has no effect once this is set.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
						Validators: []validator.String{
							stringvalidator.ConflictsWith(
								path.MatchRoot("file_storage").AtName("persistent_volume_claim"),
								path.MatchRoot("file_storage").AtName("mount_path"),
							),
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

// ValidateConfig is CloudResource.ValidateConfig's counterpart for the
// multi-resource cloud (empty cloud + separately-attached resource) pattern (K9). Unlike
// CloudResource, this resource has no azure_config block and no is-empty
// escape hatch - Create always resolves a provider and always calls
// buildProviderConfig unconditionally (see Create below), so the only way to
// reach the AZURE/GENERIC dead end here is an explicit cloud_provider value;
// no hasEmbeddedResourceConfig-style gating is needed.
func (r *CloudResourceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data CloudResourceResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	provider := strings.ToUpper(data.CloudProvider.ValueString())
	if provider == "" {
		// mirrors Create()'s own auto-detect order (AWS, then GCP, then
		// Azure) - needed so the AWS mount_path check below fires even when
		// a user relies on aws_config presence alone, without also setting
		// cloud_provider explicitly.
		switch {
		case !data.AWSConfig.IsNull():
			provider = "AWS"
		case !data.GCPConfig.IsNull():
			provider = "GCP"
		case !data.AzureConfig.IsNull():
			provider = "AZURE"
		}
	}

	switch provider {
	case "AZURE":
		resp.Diagnostics.Append(validateAzureK8SOnly(ctx, data.ComputeStack.ValueString(), data.ObjectStorage)...)
	case "GENERIC":
		resp.Diagnostics.AddAttributeError(path.Root("cloud_provider"), "Generic Clouds Not Yet Supported", genericCloudNotSupportedMessage)
	}

	if !data.FileStorage.IsNull() && !data.FileStorage.IsUnknown() {
		var fsModel FileStorageModel
		fsDiags := data.FileStorage.As(ctx, &fsModel, basetypes.ObjectAsOptions{})
		resp.Diagnostics.Append(fsDiags...)
		if !fsDiags.HasError() {
			resp.Diagnostics.Append(validateMountPathSupported(provider, &fsModel)...)
		}
	}

	if !data.GCPConfig.IsNull() && !data.GCPConfig.IsUnknown() {
		var gcpModel GCPConfigModel
		gcpDiags := data.GCPConfig.As(ctx, &gcpModel, basetypes.ObjectAsOptions{})
		resp.Diagnostics.Append(gcpDiags...)
		if !gcpDiags.HasError() {
			resp.Diagnostics.Append(validateSubnetNamesSupported(data.ComputeStack.ValueString(), &gcpModel)...)
		}
	}

	if !data.AWSConfig.IsNull() && !data.AWSConfig.IsUnknown() {
		var awsModel AWSConfigModel
		awsDiags := data.AWSConfig.As(ctx, &awsModel, basetypes.ObjectAsOptions{})
		resp.Diagnostics.Append(awsDiags...)
		if !awsDiags.HasError() {
			resp.Diagnostics.Append(validateSubnetIDsSupported(data.ComputeStack.ValueString(), &awsModel)...)
		}
	}
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
		} else if !plan.AzureConfig.IsNull() {
			provider = "AZURE"
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

	name := plan.Name.ValueString()

	tflog.Info(ctx, "Creating Anyscale Cloud Resource",
		map[string]any{
			"cloud_id":      cloudID,
			"name":          name,
			"provider":      provider,
			"region":        region,
			"compute_stack": computeStack,
		})

	// Build deployment request
	deployReq := CloudDeploymentRequest{
		Name:           name,
		Provider:       provider,
		ComputeStack:   computeStack,
		Region:         region,
		NetworkingMode: networkingMode,
	}

	// Add provider-specific configuration
	if err := buildProviderConfig(ctx, &deployReq, provider, computeStack, plan.AWSConfig, plan.GCPConfig, plan.AzureConfig, plan.KubernetesConfig, plan.ObjectStorage, plan.FileStorage); err != nil {
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

	// add_resource registers real cloud infrastructure server-side and can
	// legitimately run well past DoRequest's default deadline.
	addResourceCtx, cancel := context.WithTimeout(ctx, addResourceRequestTimeout)
	defer cancel()

	deployResp, err := DoRequestAndParse[CloudDeploymentResponse](
		addResourceCtx,
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

	// Same reasoning for the remaining operator/default fields: none of them
	// are set yet at this point, so without this they'd still be Unknown at
	// the early State.Set below - Terraform Core rejects a post-apply state
	// with Unknown attributes, so a failure between here and the read-back
	// would produce an "invalid result object" diagnostic per field left
	// this way, independent of whatever caused that failure.
	if plan.OperatorStatus.IsUnknown() {
		plan.OperatorStatus = types.StringNull()
	}
	if plan.OperatorVersion.IsUnknown() {
		plan.OperatorVersion = types.StringNull()
	}
	if plan.ReportedAt.IsUnknown() {
		plan.ReportedAt = types.StringNull()
	}
	if plan.IsDefault.IsUnknown() {
		plan.IsDefault = types.BoolValue(false)
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
//
// C3-v2: this is the ONLY place that recovers aws_config/gcp_config/
// kubernetes_config/object_storage from the API - never Create or Read (see
// readCloudResource). Only the compute-stack-REQUIRED block(s) are
// recovered, for the same reason as anyscale_cloud's ImportState: a
// cloud_resource is never "empty" the way a cloud can be, but the
// Create-time plan-consistency hazard applies here identically, since these
// blocks are not Computed either.
func (r *CloudResourceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// ID format: cloud_id:name
	cloudID, resourceName, err := parseCloudResourceID(req.ID)
	if err != nil {
		AddConfigError(&resp.Diagnostics,
			"Import Error",
			fmt.Sprintf("Invalid import ID format. Expected 'cloud_id:name', got '%s'", req.ID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cloud_id"), cloudID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), resourceName)...)
	if resp.Diagnostics.HasError() {
		return
	}

	results, err := listCloudResources(ctx, r.client, cloudID)
	if err != nil {
		tflog.Warn(ctx, "Failed to list cloud resources during import; config blocks will not be recovered - the subsequent Read will surface any real error", map[string]any{"cloud_id": cloudID, "error": err.Error()})
		return
	}

	var found *CloudDeploymentResult
	for i := range results {
		if results[i].Name == resourceName {
			found = &results[i]
			break
		}
	}
	if found == nil {
		return // subsequent Read surfaces the not-found error
	}

	blocks, diags := requiredImportConfigBlocks(ctx, found.Provider, found)
	resp.Diagnostics.Append(diags...)
	for attrName, obj := range blocks {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(attrName), obj)...)
	}
}

// ─── Helper Functions ─────────────────────────────────────────────────────────

// parseCloudResourceID parses a composite ID in format "cloud_id:name"
func parseCloudResourceID(id string) (cloudID, resourceName string, err error) {
	parts := strings.SplitN(id, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid cloud resource ID format: expected 'cloud_id:name', got '%s'", id)
	}
	return parts[0], parts[1], nil
}

// readCloudResource reads a cloud resource from the API and updates the state model
func (r *CloudResourceResource) readCloudResource(ctx context.Context, cloudID, resourceName string, state *CloudResourceResourceModel) error {
	// listCloudResources pages through every page rather than just the first:
	// Read calls this and removes the resource from state on a "not found", so
	// a resource whose name only appears past page 1 would otherwise be
	// phantom-deleted from state - the same bug class task d35713ef fixed for
	// organization_collaborator.
	results, err := listCloudResources(ctx, r.client, cloudID)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return fmt.Errorf("cloud not found")
		}
		return fmt.Errorf("failed to read cloud resources: %w", err)
	}

	// Find the resource by name
	var foundResource *CloudDeploymentResult
	for _, r := range results {
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
		state.OperatorStatus = types.StringValue(*foundResource.OperatorStatus)
	} else {
		state.Status = types.StringNull()
		state.OperatorStatus = types.StringNull()
	}

	// C4: operator_version/reported_at are only present once a K8s
	// resource's operator has reported in at least once - null for VM,
	// and null for a K8s resource that hasn't reported yet.
	if foundResource.OperatorStatusDetails != nil {
		state.OperatorVersion = stringPtrOrNull(foundResource.OperatorStatusDetails.OperatorVersion)
		state.ReportedAt = stringPtrOrNull(foundResource.OperatorStatusDetails.ReportedAt)
	} else {
		state.OperatorVersion = types.StringNull()
		state.ReportedAt = types.StringNull()
	}

	if foundResource.NetworkingMode == "PRIVATE" {
		state.IsPrivate = types.BoolValue(true)
	} else {
		state.IsPrivate = types.BoolValue(false)
	}

	tflog.Info(ctx, "Cloud resource read successfully", map[string]any{"cloud_id": cloudID, "name": resourceName})
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
	if !awsModel.ClusterInstanceProfileID.IsNull() {
		profileID := awsModel.ClusterInstanceProfileID.ValueString()
		awsConfig.ClusterInstanceProfileID = &profileID
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

// expandAzureConfig extracts Azure configuration from the Terraform plan.
// tenant_id is the only field - see AzureConfig in models.go for why.
func expandAzureConfig(ctx context.Context, obj types.Object) (*AzureConfig, error) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}

	var azureModel AzureConfigModel
	diags := obj.As(ctx, &azureModel, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, fmt.Errorf("failed to convert azure_config: %v", diags)
	}

	return &AzureConfig{
		TenantID: azureModel.TenantID.ValueString(),
	}, nil
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

	if !storageModel.PersistentVolumeClaim.IsNull() {
		storage.PersistentVolumeClaim = storageModel.PersistentVolumeClaim.ValueString()
	}
	if !storageModel.CSIEphemeralVolumeDriver.IsNull() {
		storage.CSIEphemeralVolumeDriver = storageModel.CSIEphemeralVolumeDriver.ValueString()
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
	cloudPath := fmt.Sprintf("/api/v2/clouds/%s", cloudID)

	tflog.Info(ctx, "Waiting for cloud to be ready", map[string]any{"cloud_id": cloudID, "timeout": timeout.String()})

	for time.Now().Before(deadline) {
		pollCount++
		tflog.Debug(ctx, "Polling cloud status", map[string]any{"poll_count": pollCount, "cloud_id": cloudID})

		bodyBytes, err := DoRequestRaw(ctx, client, "GET", cloudPath, nil,
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
