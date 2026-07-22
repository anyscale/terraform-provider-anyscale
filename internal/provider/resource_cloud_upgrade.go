package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure CloudResource satisfies the state-upgrade interface.
var _ resource.ResourceWithUpgradeState = &CloudResource{}

// UpgradeState implements the v0 -> v1 migration for the mount_targets
// block-to-attribute conversion and the kubernetes_config inert-field
// removal (both land in the same release, so both are handled by the same
// version bump). This is the provider's first state upgrader for
// anyscale_cloud specifically (resource_compute_config_upgrade.go's
// precedent is for a different resource).
func (r *CloudResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema:   cloudResourceSchemaV0(),
			StateUpgrader: upgradeCloudResourceStateV0toV1,
		},
		1: {
			PriorSchema:   cloudResourceSchemaV1(),
			StateUpgrader: upgradeCloudResourceStateV1toV2,
		},
	}
}

// cloudResourceSchemaV0 is a frozen copy of anyscale_cloud's schema exactly
// as shipped through v0.16.x, before the mount_targets block->attribute
// conversion and the kubernetes_config inert-field removal. It exists
// solely so UpgradeState can decode v0 state; do not evolve it going
// forward - it is a historical snapshot, not a second copy of the live
// schema. Only attribute names, types, and block-vs-attribute structure
// affect decoding - Optional/Computed/Default/PlanModifiers/Validators are
// never consulted during UpgradeState, so this snapshot mirrors the
// pre-Group-B schema's flags for convenience rather than reproducing true
// v0.16.x's exactly (e.g. memorydb_cluster_arn/endpoint were Optional-only
// there, Computed here). The names/types/structure must still be exactly
// right, since a wrong one means the framework hard-fails decoding EVERY
// existing cloud's state at upgrade, not just the ones with data in the
// fields that later change.
func cloudResourceSchemaV0() *schema.Schema {
	return &schema.Schema{
		Version: 0,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					cloudNameImmutablePlanModifier{},
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
			"is_private_cloud": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"auto_add_user": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"credentials": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"enable_lineage_tracking": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"enable_log_ingestion": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"enable_system_cluster": schema.BoolAttribute{
				Optional: true,
			},
			"is_empty_cloud": schema.BoolAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"is_default": schema.BoolAttribute{
				Computed: true,
			},
			"cloud_resource_id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
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
					"controlplane_iam_role_arn": schema.StringAttribute{Optional: true},
					"dataplane_iam_role_arn":    schema.StringAttribute{Optional: true},
					"cluster_instance_profile_id": schema.StringAttribute{
						Optional: true,
					},
					"external_id": schema.StringAttribute{Optional: true},
					"memorydb_cluster_name": schema.StringAttribute{
						Optional: true,
					},
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
			// kubernetes_config as it existed at v0: 8 attributes, including
			// the 5 inert bookkeeping fields this release removes.
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
			// file_storage as it existed at v0: mount_targets is a
			// ListNestedBlock (this release converts it to a
			// ListNestedAttribute of the identical element type - see
			// mergeMountTargets's doc comment for why that conversion needs
			// no value transform, only this schema-decode compatibility).
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

// cloudResourceModelV1 mirrors CloudResourceModel exactly, plus the
// enable_system_cluster field that lived on the live schema through
// version 1 (removed at v1->v2, see upgradeCloudResourceStateV1toV2).
// CloudResourceModel itself has no field for it, and
// terraform-plugin-framework's struct/object reflection requires an exact
// field-for-field match in BOTH directions - verified directly: decoding a
// schema attribute with no matching struct field produces a hard "Object
// defines fields not found in struct" error, not a silent skip. So both the
// v0->v1 and v1->v2 upgraders decode into this struct (v0's and v1's real
// stored schemas both still have enable_system_cluster; only the nested
// kubernetes_config/mount_targets shapes differ between v0 and v1, and
// those live in dynamically-typed types.Object fields that need no
// separate struct per version) rather than into CloudResourceModel
// directly.
type cloudResourceModelV1 struct {
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

	AWSConfig        types.Object `tfsdk:"aws_config"`
	GCPConfig        types.Object `tfsdk:"gcp_config"`
	AzureConfig      types.Object `tfsdk:"azure_config"`
	KubernetesConfig types.Object `tfsdk:"kubernetes_config"`

	ObjectStorage types.Object `tfsdk:"object_storage"`
	FileStorage   types.Object `tfsdk:"file_storage"`

	IsEmptyCloud    types.Bool   `tfsdk:"is_empty_cloud"`
	IsDefault       types.Bool   `tfsdk:"is_default"`
	CloudResourceID types.String `tfsdk:"cloud_resource_id"`
}

// toCloudResourceModel drops EnableSystemCluster - the only difference
// between this struct and the current (v2) CloudResourceModel.
func (m cloudResourceModelV1) toCloudResourceModel() CloudResourceModel {
	return CloudResourceModel{
		ID:                    m.ID,
		Name:                  m.Name,
		CloudProvider:         m.CloudProvider,
		ComputeStack:          m.ComputeStack,
		Region:                m.Region,
		IsPrivateCloud:        m.IsPrivateCloud,
		AutoAddUser:           m.AutoAddUser,
		Credentials:           m.Credentials,
		EnableLineageTracking: m.EnableLineageTracking,
		EnableLogIngestion:    m.EnableLogIngestion,
		AWSConfig:             m.AWSConfig,
		GCPConfig:             m.GCPConfig,
		AzureConfig:           m.AzureConfig,
		KubernetesConfig:      m.KubernetesConfig,
		ObjectStorage:         m.ObjectStorage,
		FileStorage:           m.FileStorage,
		IsEmptyCloud:          m.IsEmptyCloud,
		IsDefault:             m.IsDefault,
		CloudResourceID:       m.CloudResourceID,
	}
}

// upgradeCloudResourceStateV0toV1 drops the 5 removed kubernetes_config
// attributes and enable_system_cluster (removed later at v1->v2, but this
// function's output must target the CURRENT live schema directly - the
// framework calls exactly one upgrader per stored version, never chaining
// v0->v1->v2 - so it has to drop enable_system_cluster too, despite its
// name), carrying every other field - including file_storage (whose
// mount_targets is now a ListNestedAttribute, but decodes to the identical
// types.Object shape regardless, per the verified Block/Attribute
// type-identity) - through unchanged.
func upgradeCloudResourceStateV0toV1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 0 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState cloudResourceModelV1
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	upgradedKubernetesConfig, kubeDiags := upgradeKubernetesConfigV0toV1(priorState.KubernetesConfig)
	resp.Diagnostics.Append(kubeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState := priorState.toCloudResourceModel()
	newState.KubernetesConfig = upgradedKubernetesConfig
	// FileStorage (mount_targets included), AWSConfig, GCPConfig,
	// AzureConfig, ObjectStorage, and every other field pass through
	// unchanged via toCloudResourceModel(); enable_system_cluster is
	// dropped by the same call.

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// upgradeKubernetesConfigV0toV1 drops the 5 removed inert bookkeeping
// attributes (namespace/ingress_host/cluster_name/context/kubeconfig_path)
// from a v0 kubernetes_config Object, carrying the 3 real attributes
// (anyscale_operator_iam_identity/zones/redis_endpoint) through unchanged.
// There is nothing to migrate for the 5 dropped attributes: they were never
// sent to the Anyscale API (pure Terraform-side bookkeeping - see
// kubernetesConfigAttrTypes's doc comment in cloud_config_flatten.go), so no
// value ever stored for them carried real information to preserve -
// dropping them is the entire migration, not a lossy approximation of one.
func upgradeKubernetesConfigV0toV1(v0 types.Object) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	if v0.IsNull() || v0.IsUnknown() {
		return types.ObjectNull(kubernetesConfigAttrTypes()), diags
	}

	v0Attrs := v0.Attributes()
	newAttrs := make(map[string]attr.Value, len(kubernetesConfigAttrTypes()))
	for _, k := range []string{"anyscale_operator_iam_identity", "zones", "redis_endpoint"} {
		v, ok := v0Attrs[k]
		if !ok {
			diags.AddError(
				"State Upgrade Error",
				"kubernetes_config v0 state is missing expected attribute \""+k+"\". This is a bug in the provider; please report it.",
			)
			return types.ObjectNull(kubernetesConfigAttrTypes()), diags
		}
		newAttrs[k] = v
	}

	obj, d := types.ObjectValue(kubernetesConfigAttrTypes(), newAttrs)
	diags.Append(d...)
	return obj, diags
}

// cloudResourceSchemaV1 is a frozen copy of anyscale_cloud's schema exactly
// as shipped between the mount_targets/kubernetes_config change (v0->v1,
// PR #195) and the enable_system_cluster removal (v1->v2, this release):
// kubernetes_config already narrowed to its 3 real attributes,
// file_storage.mount_targets already a ListNestedAttribute, but
// enable_system_cluster still present. Exists solely so UpgradeState can
// decode v1 state; do not evolve it going forward - see
// cloudResourceSchemaV0's doc comment for why only attribute
// names/types/block-vs-attribute structure matter here, not
// Optional/Computed/Default/PlanModifiers/Validators.
func cloudResourceSchemaV1() *schema.Schema {
	return &schema.Schema{
		Version: 1,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					cloudNameImmutablePlanModifier{},
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
			"is_private_cloud": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"auto_add_user": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"credentials": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"enable_lineage_tracking": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"enable_log_ingestion": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"enable_system_cluster": schema.BoolAttribute{
				Optional: true,
			},
			"is_empty_cloud": schema.BoolAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"is_default": schema.BoolAttribute{
				Computed: true,
			},
			"cloud_resource_id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
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
					"controlplane_iam_role_arn": schema.StringAttribute{Optional: true},
					"dataplane_iam_role_arn":    schema.StringAttribute{Optional: true},
					"cluster_instance_profile_id": schema.StringAttribute{
						Optional: true,
					},
					"external_id": schema.StringAttribute{Optional: true},
					"memorydb_cluster_name": schema.StringAttribute{
						Optional: true,
					},
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
			// kubernetes_config as it existed at v1: already narrowed to
			// its 3 real attributes by the v0->v1 upgrade.
			"kubernetes_config": schema.SingleNestedBlock{
				Attributes: map[string]schema.Attribute{
					"anyscale_operator_iam_identity": schema.StringAttribute{Optional: true},
					"zones": schema.ListAttribute{
						ElementType: types.StringType,
						Optional:    true,
					},
					"redis_endpoint": schema.StringAttribute{Optional: true},
				},
			},
			"object_storage": schema.SingleNestedBlock{
				Attributes: map[string]schema.Attribute{
					"bucket_name": schema.StringAttribute{Optional: true},
					"region":      schema.StringAttribute{Optional: true},
					"endpoint":    schema.StringAttribute{Optional: true},
				},
			},
			// file_storage as it existed at v1: mount_targets is already a
			// ListNestedAttribute (converted from a Block by v0->v1).
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
					"mount_targets": schema.ListNestedAttribute{
						Optional: true,
						Computed: true,
						NestedObject: schema.NestedAttributeObject{
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

// upgradeCloudResourceStateV1toV2 drops enable_system_cluster (removed in
// favor of the dedicated anyscale_system_cluster resource, which owns
// enabling and starting the System Cluster together) and carries every
// other field through unchanged. There is nothing to migrate for the
// dropped field: enable_system_cluster was Optional-only with no reliable
// read-back (see its former schema doc), so whatever value a prior config
// last wrote is not a signal this provider can or should carry forward -
// the new resource re-establishes the real enabled/running state directly
// from the API on its own first apply, rather than inheriting a guess from
// a write-only bit. Decodes into cloudResourceModelV1 (not CloudResourceModel
// directly) because v1's real stored schema still declares
// enable_system_cluster - decoding straight into the now-fieldless
// CloudResourceModel produces a hard "Object defines fields not found in
// struct" error, verified via TestCloudResourceStateUpgradeV1toV2_DropsEnableSystemCluster.
func upgradeCloudResourceStateV1toV2(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 1 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState cloudResourceModelV1
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState := priorState.toCloudResourceModel()

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}
