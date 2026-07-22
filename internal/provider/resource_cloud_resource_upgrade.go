package provider

import (
	"context"

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
)

// Ensure CloudResourceResource satisfies the state-upgrade interface.
var _ resource.ResourceWithUpgradeState = &CloudResourceResource{}

// UpgradeState is anyscale_cloud's UpgradeState (resource_cloud_upgrade.go)
// for anyscale_cloud_resource - same v0->v1 migration (mount_targets
// block->attribute + kubernetes_config inert-field removal) and the same
// v1->v2 migration (status removal), same reasons, applied to this
// resource's own schema shape.
func (r *CloudResourceResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema:   cloudResourceResourceSchemaV0(),
			StateUpgrader: upgradeCloudResourceResourceStateV0toV1,
		},
		1: {
			PriorSchema:   cloudResourceResourceSchemaV1(),
			StateUpgrader: upgradeCloudResourceResourceStateV1toV2,
		},
	}
}

// cloudResourceResourceModelV1 mirrors CloudResourceResourceModel exactly,
// plus the status field that lived on the live schema through version 1
// (removed at v1->v2, see upgradeCloudResourceResourceStateV1toV2).
// CloudResourceResourceModel itself has no field for it, and
// terraform-plugin-framework's struct/object reflection requires an exact
// field-for-field match in both directions - verified directly for the
// analogous enable_system_cluster removal, see cloudResourceModelV1's own
// doc comment (resource_cloud_upgrade.go). So both the v0->v1 and v1->v2
// upgraders decode into this struct (v0's and v1's real stored schemas both
// still have status; only the kubernetes_config/mount_targets shapes differ
// between v0 and v1, and those live in dynamically-typed types.Object
// fields that need no separate struct per version) rather than into
// CloudResourceResourceModel directly.
type cloudResourceResourceModelV1 struct {
	CloudID   types.String `tfsdk:"cloud_id"`
	CloudName types.String `tfsdk:"cloud_name"`

	Name types.String `tfsdk:"name"`

	CloudProvider types.String `tfsdk:"cloud_provider"`
	ComputeStack  types.String `tfsdk:"compute_stack"`
	Region        types.String `tfsdk:"region"`
	IsPrivate     types.Bool   `tfsdk:"is_private"`

	AWSConfig        types.Object `tfsdk:"aws_config"`
	GCPConfig        types.Object `tfsdk:"gcp_config"`
	AzureConfig      types.Object `tfsdk:"azure_config"`
	KubernetesConfig types.Object `tfsdk:"kubernetes_config"`

	ObjectStorage types.Object `tfsdk:"object_storage"`
	FileStorage   types.Object `tfsdk:"file_storage"`

	CloudResourceID types.String `tfsdk:"cloud_resource_id"`
	Status          types.String `tfsdk:"status"`
	OperatorStatus  types.String `tfsdk:"operator_status"`
	OperatorVersion types.String `tfsdk:"operator_version"`
	ReportedAt      types.String `tfsdk:"reported_at"`
	IsDefault       types.Bool   `tfsdk:"is_default"`

	ID types.String `tfsdk:"id"`
}

// toCloudResourceResourceModel drops Status - the only difference between
// this struct and the current (v2) CloudResourceResourceModel.
func (m cloudResourceResourceModelV1) toCloudResourceResourceModel() CloudResourceResourceModel {
	return CloudResourceResourceModel{
		CloudID:          m.CloudID,
		CloudName:        m.CloudName,
		Name:             m.Name,
		CloudProvider:    m.CloudProvider,
		ComputeStack:     m.ComputeStack,
		Region:           m.Region,
		IsPrivate:        m.IsPrivate,
		AWSConfig:        m.AWSConfig,
		GCPConfig:        m.GCPConfig,
		AzureConfig:      m.AzureConfig,
		KubernetesConfig: m.KubernetesConfig,
		ObjectStorage:    m.ObjectStorage,
		FileStorage:      m.FileStorage,
		CloudResourceID:  m.CloudResourceID,
		OperatorStatus:   m.OperatorStatus,
		OperatorVersion:  m.OperatorVersion,
		ReportedAt:       m.ReportedAt,
		IsDefault:        m.IsDefault,
		ID:               m.ID,
	}
}

// cloudResourceResourceSchemaV0 is a frozen copy of anyscale_cloud_resource's
// schema exactly as shipped through v0.16.x - see cloudResourceSchemaV0's
// doc comment for why flags (Optional/Computed/Default/etc) don't need to
// match true v0.16.x but names/types/structure must, and why this must not
// evolve alongside the live schema.
func cloudResourceResourceSchemaV0() *schema.Schema {
	return &schema.Schema{
		Version: 0,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cloud_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cloud_name": schema.StringAttribute{
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"cloud_provider": schema.StringAttribute{
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"compute_stack": schema.StringAttribute{
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("VM", "K8S"),
				},
			},
			"region": schema.StringAttribute{
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"is_private": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"cloud_resource_id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Computed:           true,
				DeprecationMessage: statusDeprecationMessage,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"operator_status": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"operator_version": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"reported_at": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"is_default": schema.BoolAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			"aws_config": schema.SingleNestedBlock{
				Attributes: map[string]schema.Attribute{
					"vpc_id": schema.StringAttribute{Optional: true},
					"subnet_ids": schema.ListAttribute{
						ElementType: types.StringType,
						Optional:    true,
					},
					"subnet_ids_to_az": schema.MapAttribute{
						ElementType: types.StringType,
						Optional:    true,
					},
					"security_group_ids": schema.ListAttribute{
						ElementType: types.StringType,
						Optional:    true,
					},
					"controlplane_iam_role_arn":   schema.StringAttribute{Optional: true},
					"dataplane_iam_role_arn":      schema.StringAttribute{Optional: true},
					"cluster_instance_profile_id": schema.StringAttribute{Optional: true},
					"external_id":                 schema.StringAttribute{Optional: true},
					"memorydb_cluster_name":       schema.StringAttribute{Optional: true},
					"memorydb_cluster_arn": schema.StringAttribute{
						Optional: true,
						Computed: true,
					},
					"memorydb_cluster_endpoint": schema.StringAttribute{
						Optional: true,
						Computed: true,
					},
				},
			},
			"gcp_config": schema.SingleNestedBlock{
				Attributes: map[string]schema.Attribute{
					"project_id":      schema.StringAttribute{Optional: true},
					"host_project_id": schema.StringAttribute{Optional: true},
					"provider_name":   schema.StringAttribute{Optional: true},
					"vpc_name":        schema.StringAttribute{Optional: true},
					"subnet_names": schema.ListAttribute{
						ElementType: types.StringType,
						Optional:    true,
					},
					"controlplane_service_account_email": schema.StringAttribute{Optional: true},
					"dataplane_service_account_email":    schema.StringAttribute{Optional: true},
					"firewall_policy_names": schema.ListAttribute{
						ElementType: types.StringType,
						Optional:    true,
					},
					"memorystore_instance_name": schema.StringAttribute{Optional: true},
					"memorystore_endpoint": schema.StringAttribute{
						Optional: true,
						Computed: true,
					},
				},
			},
			"azure_config": schema.SingleNestedBlock{
				Attributes: map[string]schema.Attribute{
					"tenant_id": schema.StringAttribute{Optional: true},
				},
			},
			"kubernetes_config": schema.SingleNestedBlock{
				Attributes: map[string]schema.Attribute{
					"anyscale_operator_iam_identity": schema.StringAttribute{Optional: true},
					"zones": schema.ListAttribute{
						ElementType: types.StringType,
						Optional:    true,
					},
					"redis_endpoint": schema.StringAttribute{Optional: true},
					"namespace": schema.StringAttribute{
						Optional: true,
						Computed: true,
						Default:  stringdefault.StaticString("anyscale"),
					},
					"ingress_host":    schema.StringAttribute{Optional: true},
					"cluster_name":    schema.StringAttribute{Optional: true},
					"context":         schema.StringAttribute{Optional: true},
					"kubeconfig_path": schema.StringAttribute{Optional: true},
				},
			},
			"object_storage": schema.SingleNestedBlock{
				Attributes: map[string]schema.Attribute{
					"bucket_name": schema.StringAttribute{Optional: true},
					"region":      schema.StringAttribute{Optional: true},
					"endpoint":    schema.StringAttribute{Optional: true},
				},
			},
			"file_storage": schema.SingleNestedBlock{
				Attributes: map[string]schema.Attribute{
					"file_storage_id": schema.StringAttribute{Optional: true},
					"mount_path": schema.StringAttribute{
						Optional: true,
						Computed: true,
						Default:  stringdefault.StaticString(fileStorageDefaultMountPath),
					},
					"persistent_volume_claim":     schema.StringAttribute{Optional: true},
					"csi_ephemeral_volume_driver": schema.StringAttribute{Optional: true},
				},
				Blocks: map[string]schema.Block{
					"mount_targets": schema.ListNestedBlock{
						NestedObject: schema.NestedBlockObject{
							Attributes: map[string]schema.Attribute{
								"address": schema.StringAttribute{Optional: true},
								"zone":    schema.StringAttribute{Optional: true},
							},
						},
					},
				},
			},
		},
	}
}

// upgradeCloudResourceResourceStateV0toV1 is
// upgradeCloudResourceStateV0toV1 for anyscale_cloud_resource - same
// migration, same reasoning, this resource's own model type. Decodes into
// cloudResourceResourceModelV1 (not CloudResourceResourceModel directly)
// because the framework calls exactly one upgrader per stored version,
// never chaining v0->v1->v2 (see upgradeCloudResourceStateV0toV1's own
// comment in resource_cloud_upgrade.go) - this function's output must
// target the CURRENT live schema directly, so it has to drop status too,
// despite its name, the same way that function drops enable_system_cluster.
func upgradeCloudResourceResourceStateV0toV1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 0 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState cloudResourceResourceModelV1
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	upgradedKubernetesConfig, kubeDiags := upgradeKubernetesConfigV0toV1(priorState.KubernetesConfig)
	resp.Diagnostics.Append(kubeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState := priorState.toCloudResourceResourceModel()
	newState.KubernetesConfig = upgradedKubernetesConfig
	// FileStorage (mount_targets included), AWSConfig, GCPConfig,
	// AzureConfig, ObjectStorage, and every other field pass through
	// unchanged via toCloudResourceResourceModel(); status is dropped by
	// the same call.

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// upgradeCloudResourceResourceStateV1toV2 drops status, carrying every
// other field through unchanged. There is nothing to migrate for the
// dropped field: status and operator_status were always set from the same
// underlying value (see readCloudResource), so operator_status already
// carries forward the exact information status used to duplicate - nothing
// is lost, just a redundant read-only output. Decodes into
// cloudResourceResourceModelV1 (not CloudResourceResourceModel directly)
// because v1's real stored schema still declares status - decoding straight
// into the now-fieldless CloudResourceResourceModel produces a hard "Object
// defines fields not found in struct" error, verified via
// TestCloudResourceResourceStateUpgradeV1toV2_DropsStatus.
func upgradeCloudResourceResourceStateV1toV2(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 1 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState cloudResourceResourceModelV1
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState := priorState.toCloudResourceResourceModel()

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// cloudResourceResourceSchemaV1 is a frozen copy of anyscale_cloud_resource's
// schema exactly as it existed at schema Version 1 (after the v0->v1
// mount_targets/kubernetes_config migration, before the v1->v2 status
// removal below) - status still declared here, DeprecationMessage
// included, matching what a real v1 state was actually validated against.
// Must not evolve alongside the live schema - see cloudResourceSchemaV1's
// own doc comment (resource_cloud_upgrade.go) for why.
func cloudResourceResourceSchemaV1() *schema.Schema {
	return &schema.Schema{
		Version:             1,
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
				MarkdownDescription: "Whether to register this specific resource deployment as private - the same concept as `is_private_cloud` on the `anyscale_cloud` resource (see its schema description for the full explanation of what \"private\" does and does not mean), scoped to this one resource rather than the cloud as a whole. This is a self-asserted flag: setting `true` does not itself verify, configure, or provision any VPN or PrivateLink connectivity - arranging that separately remains your own responsibility. Changing this value after creation requires replacement.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},

			// ─── Computed Fields ──────────────────────────────────
			"cloud_resource_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique cloud resource ID assigned by Anyscale when this resource deployment was registered. This is what you pass to the Anyscale operator during installation for a K8S cloud (as `global.cloudDeploymentId` in the operator's Helm values, despite the key's name - the value is this resource id). `anyscale_cloud`'s own `cloud_resource_id` attribute exposes the same populated identifier for the all-in-one pattern. Stable for the life of this resource deployment - it does not move out of band between applies.",
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
							awsSubnetIDsSemanticEqualPlanModifier{},
							listplanmodifier.RequiresReplace(),
						},
					},
					"subnet_ids_to_az": schema.MapAttribute{
						ElementType:         types.StringType,
						Optional:            true,
						MarkdownDescription: "Map of subnet ID to availability zone (e.g., {\"subnet-123\": \"us-east-2a\"}). Preferred over subnet_ids. VM compute only - EKS networking comes entirely from `kubernetes_config.zones`, so setting this on a Kubernetes cloud is rejected at plan time rather than silently corrupting the registered networking (the backend applies this unconditionally after the Kubernetes zone list is written).",
						PlanModifiers: []planmodifier.Map{
							awsSubnetIDsToAZSemanticEqualPlanModifier{},
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
						Computed:            true,
						MarkdownDescription: "MemoryDB cluster ARN. Derived automatically from `memorydb_cluster_name` when left unset - the Anyscale API returns the cluster's real ARN once it exists, and the provider records it in state at create time and recovers it at import; set it explicitly only if you have a specific reason to pin a value yourself. See the [Anyscale head node fault tolerance documentation](https://docs.anyscale.com/administration/resource-management/head-node-fault-tolerance) for cluster requirements.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.UseStateForUnknown(),
							stringplanmodifier.RequiresReplace(),
						},
					},
					"memorydb_cluster_endpoint": schema.StringAttribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "MemoryDB cluster endpoint address, formatted as `<name>.<random>.clustercfg.memorydb.<region>.amazonaws.com:6379`. Requires TLS - use a `rediss://` prefix when connecting. Derived automatically from `memorydb_cluster_name` when left unset, the same way as `memorydb_cluster_arn` above; set it explicitly only if you have a specific reason to pin a value yourself. Conflicts with `kubernetes_config.redis_endpoint` - the backend rejects more than one GCS fault-tolerance backing store on the same cloud. See the [Anyscale head node fault tolerance documentation](https://docs.anyscale.com/administration/resource-management/head-node-fault-tolerance) for full cluster requirements.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.UseStateForUnknown(),
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
						Computed:            true,
						MarkdownDescription: "Memorystore endpoint address. Unlike AWS MemoryDB, Memorystore does not support TLS for this connection. Derived automatically from `memorystore_instance_name` when left unset, the same way as MemoryDB's arn/endpoint fields above; set it explicitly only if you have a specific reason to pin a value yourself. Conflicts with `kubernetes_config.redis_endpoint` - the backend rejects more than one GCS fault-tolerance backing store on the same cloud. See the [Anyscale head node fault tolerance documentation](https://docs.anyscale.com/administration/resource-management/head-node-fault-tolerance) for full cluster requirements.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.UseStateForUnknown(),
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
						MarkdownDescription: "The IAM identity for the Anyscale operator. For AWS EKS: the ARN of an IAM role whose trust policy allows `pods.eks.amazonaws.com`, wired to the operator via an `aws_eks_pod_identity_association` (see the [Anyscale EKS IAM documentation](https://docs.anyscale.com/iam/eks)) - a node group's IAM role will NOT work here, since node roles trust `ec2.amazonaws.com` instead; the provider cannot see a role's trust policy, so getting this wrong fails the operator's own authentication at runtime, not at `terraform plan`. For GCP GKE: service account email (see the [Anyscale GKE IAM documentation](https://docs.anyscale.com/iam/gke)). For Azure AKS: the managed identity's principal ID (not its client ID - the reference AKS setup flow distinguishes the two: principal ID here, client ID only in the operator's own values.yaml).",
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
				},
			},

			// ─── Object Storage ───────────────────────────────────
			"object_storage": schema.SingleNestedBlock{
				MarkdownDescription: "Object storage configuration (S3, GCS, Azure Blob, or S3-compatible). Recovered automatically when importing an existing cloud/resource, whenever the live resource actually has one configured. See the Anyscale documentation for bucket setup: [S3](https://docs.anyscale.com/storage/s3) for AWS, [GCS](https://docs.anyscale.com/storage/gcs) for GCP, [Azure Blob/ADLS](https://docs.anyscale.com/clouds/azure/storage) for Azure.",
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
						MarkdownDescription: "The bucket region (if different from cloud region). Only recovered at import when it genuinely differs from the cloud's own region - the backend fills in the cloud's own region by default even when this was never set, so a matching value is deliberately left null rather than copied back verbatim.",
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
				MarkdownDescription: "File storage configuration (EFS, Filestore, etc.). If omitted, Anyscale falls back to using the object storage bucket for shared storage. On GCP, Filestore is optional and not created by default, and must be in the same region as the cloud's VPC when used. Recovered automatically when importing an existing cloud/resource, whenever the live resource actually has one configured. See the [Anyscale shared storage documentation](https://docs.anyscale.com/storage/shared) for how this is used across a cluster.",
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
						Default:             stringdefault.StaticString(fileStorageDefaultMountPath),
						MarkdownDescription: "The mount path for the file storage. Only meaningful for GCP Filestore and Azure/Generic NFS-backed clouds - AWS EFS-backed clouds have no backend field for it and reject the value at plan time. Recovered from the live value at import time on GCP, Azure, and Generic, where the backend genuinely stores one; left to the schema default on AWS, since no backend field exists there to recover from. Known limitation on GCP: if `mount_targets` isn't also set, Anyscale auto-discovers the Filestore share name and silently overwrites this value - GCP still ends up with a valid path, just not necessarily this one. Because `file_storage` isn't refreshed from the API on any later read (a prior refresh attempt caused a state-consistency regression), Terraform state keeps showing whatever value import recovered even after the backend overwrites it again, and `terraform plan` won't surface that later drift. Mutually exclusive with `persistent_volume_claim` and `csi_ephemeral_volume_driver` (neither has a backend mount_path field either) - set at most one. Changing this requires replacement; the provider has no in-place update path for it.",
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
					"mount_targets": schema.ListNestedAttribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "List of mount targets with address and optional zone. Changing this list requires replacement; the provider has no in-place update path for it. This is the NFS-style mount mechanism; mutually exclusive with `persistent_volume_claim` and `csi_ephemeral_volume_driver` (the Kubernetes-native shared-storage mechanisms) - do not set both. Derived automatically from `file_storage_id` when left unset - the backend auto-discovers the real address (and zone, where applicable) once the EFS/Filestore resource exists, and the provider records it in state at create time and recovers it at import; set it explicitly only if you have a specific reason to pin a value yourself, such as referencing a sibling EFS/Filestore module output at create time (see the aws-vm/gcp-vm examples). Because `file_storage` isn't refreshed from the API on any later read, the recovered value is a frozen create/import-time snapshot - if the backend-discovered address ever changes out of band, Terraform state and `terraform plan` won't surface that later drift.",
						PlanModifiers: []planmodifier.List{
							listplanmodifier.UseStateForUnknown(),
							listplanmodifier.RequiresReplace(),
						},
						NestedObject: schema.NestedAttributeObject{
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
