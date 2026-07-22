package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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
		2: {
			PriorSchema:   cloudResourceSchemaV2(),
			StateUpgrader: upgradeCloudResourceStateV2toV3,
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

// toCloudResourceModel drops EnableSystemCluster, initializes Timeouts null
// (PR2, additive - no prior state ever had a value for it, so it is always
// initialized null here rather than carried from m), and renames
// EnableLineageTracking/EnableLogIngestion to LineageTrackingEnabled/
// IsAggregatedLogsEnabled (values carry across unchanged under the new field
// names - see cloudResourceModelV2.toCloudResourceModel() for the same rename
// applied one version later) - together the only differences between this
// struct and the current (v3) CloudResourceModel.
func (m cloudResourceModelV1) toCloudResourceModel() CloudResourceModel {
	return CloudResourceModel{
		ID:                      m.ID,
		Name:                    m.Name,
		CloudProvider:           m.CloudProvider,
		ComputeStack:            m.ComputeStack,
		Region:                  m.Region,
		IsPrivateCloud:          m.IsPrivateCloud,
		AutoAddUser:             m.AutoAddUser,
		Credentials:             m.Credentials,
		LineageTrackingEnabled:  m.EnableLineageTracking,
		IsAggregatedLogsEnabled: m.EnableLogIngestion,
		AWSConfig:               m.AWSConfig,
		GCPConfig:               m.GCPConfig,
		AzureConfig:             m.AzureConfig,
		KubernetesConfig:        m.KubernetesConfig,
		ObjectStorage:           m.ObjectStorage,
		FileStorage:             m.FileStorage,
		IsEmptyCloud:            m.IsEmptyCloud,
		IsDefault:               m.IsDefault,
		CloudResourceID:         m.CloudResourceID,
		Timeouts:                timeouts.Value{Object: types.ObjectNull(map[string]attr.Type{"create": types.StringType})},
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

// cloudResourceModelV2 is a frozen copy of CloudResourceModel exactly as it
// stood between the enable_system_cluster removal (v1->v2, PR #197) and the
// enable_lineage_tracking/enable_log_ingestion rename (v2->v3, this release):
// still EnableLineageTracking/EnableLogIngestion under their original tfsdk
// tags, rather than the renamed LineageTrackingEnabled/IsAggregatedLogsEnabled
// the live CloudResourceModel uses from v3 onward. Exists solely so
// UpgradeState can decode v2 state; do not evolve it going forward - it is a
// historical snapshot, not a second copy of the live model.
type cloudResourceModelV2 struct {
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

// toCloudResourceModel maps cloudResourceModelV2 onto the current (v3)
// CloudResourceModel. The rename (EnableLineageTracking/EnableLogIngestion
// carry across unchanged as LineageTrackingEnabled/IsAggregatedLogsEnabled -
// same boolean value, new field/attribute name) is a pure rename, not a drop.
// Timeouts is a genuine gap this struct has no source for (PR2, additive -
// v2 state predates it entirely), so it is always initialized null here
// rather than carried from m, same reasoning as cloudResourceModelV1's own
// conversion above - found by inspection while merging PR2+PR3, not part of
// either PR's own conflicting lines (this whole function is new in PR3, so
// git had nothing to flag it against).
func (m cloudResourceModelV2) toCloudResourceModel() CloudResourceModel {
	return CloudResourceModel{
		ID:                      m.ID,
		Name:                    m.Name,
		CloudProvider:           m.CloudProvider,
		ComputeStack:            m.ComputeStack,
		Region:                  m.Region,
		IsPrivateCloud:          m.IsPrivateCloud,
		AutoAddUser:             m.AutoAddUser,
		Credentials:             m.Credentials,
		LineageTrackingEnabled:  m.EnableLineageTracking,
		IsAggregatedLogsEnabled: m.EnableLogIngestion,
		AWSConfig:               m.AWSConfig,
		GCPConfig:               m.GCPConfig,
		AzureConfig:             m.AzureConfig,
		KubernetesConfig:        m.KubernetesConfig,
		ObjectStorage:           m.ObjectStorage,
		FileStorage:             m.FileStorage,
		IsEmptyCloud:            m.IsEmptyCloud,
		IsDefault:               m.IsDefault,
		CloudResourceID:         m.CloudResourceID,
		Timeouts:                timeouts.Value{Object: types.ObjectNull(map[string]attr.Type{"create": types.StringType})},
	}
}

// cloudResourceSchemaV2 is a frozen copy of anyscale_cloud's schema exactly
// as shipped between the enable_system_cluster removal (v1->v2, PR #197) and
// the enable_lineage_tracking/enable_log_ingestion ->
// lineage_tracking_enabled/is_aggregated_logs_enabled rename (v2->v3, this
// release): identical in structure to cloudResourceSchemaV1 minus
// enable_system_cluster (already removed by the v1->v2 upgrade), and still
// declaring enable_lineage_tracking/enable_log_ingestion under their
// pre-rename names. Exists solely so UpgradeState can decode v2 state; do not
// evolve it going forward - see cloudResourceSchemaV0's doc comment for why
// only attribute names, types, and block-vs-attribute structure matter here,
// not Optional/Computed/Default/PlanModifiers/Validators.
func cloudResourceSchemaV2() *schema.Schema {
	return &schema.Schema{
		Version:             2,
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
				MarkdownDescription: "Compute stack type: VM or K8S. Required when using embedded config (aws_config, gcp_config, or kubernetes_config). When omitted, this reflects the compute stack of the cloud's primary resource as reported by the API (typically VM).",
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
				MarkdownDescription: "The region where the cloud is deployed. Auto-detected from config or defaults to us-east-1 for empty clouds. For AWS, Anyscale does not support the China or GovCloud partitions.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"is_private_cloud": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Whether to register this cloud as private - the Terraform equivalent of the Anyscale CLI's `anyscale cloud register --private-network` flag, which places Ray clusters in private subnets. This is a self-asserted flag, not a verified connectivity check: the value you set here is sent to the API as-is, and neither the provider nor the Anyscale backend validates, configures, or provisions any VPN or PrivateLink connectivity because of it. Setting `true` without real private connectivity already in place will not fail at plan or apply time - it only means private clusters may end up unreachable, which is your own responsibility to arrange separately, not something this attribute gates. Changing this value after creation requires replacement: the backend itself has no route to update an existing cloud's `is_private_cloud`, so there's no in-place alternative to fall back on.",
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
				MarkdownDescription: "Cloud credentials. For AWS: the IAM role ARN. For GCP: JSON with provider_id, project_id, service_account_email. Required when using the multi-resource cloud pattern (empty cloud + cloud_resource).",
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

			// ─── Computed Fields ──────────────────────────────────
			"is_empty_cloud": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this cloud was created without embedded resource configuration. Use anyscale_cloud_resource to attach resources separately.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},

			"is_default": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this cloud is the organization's default cloud. Read-only: which cloud is the org default is managed by Anyscale (e.g. via the console or CLI), not by this resource, and there is no API this resource can call to set or change it. Deliberately has no plan modifier, unlike `is_empty_cloud`/`cloud_resource_id` above: the org default can move to a different cloud out of band at any time, so pinning this to the prior state (via `UseStateForUnknown`) would risk a `Provider produced inconsistent result after apply` error if the default changed between plan and apply. Terraform reflects whichever cloud is the current org default on every refresh, so drift here is expected and simply means the default moved - it is not a bug.",
			},

			"cloud_resource_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique cloud resource ID assigned by Anyscale when this cloud's default resource was registered. This is what you pass to the Anyscale operator during installation for a K8S cloud (as `global.cloudDeploymentId` in the operator's Helm values, despite the key's name - the value is this resource id). Populated on both this all-in-one pattern and the multi-resource `anyscale_cloud_resource` pattern. Stable for the life of the cloud - unlike `is_default` above, it does not move out of band, so the provider pins it to the prior state between applies.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},

		Blocks: map[string]schema.Block{
			// ─── AWS Configuration ────────────────────────────────
			"aws_config": schema.SingleNestedBlock{
				MarkdownDescription: "AWS-specific configuration. Required when cloud_provider is AWS and using all-in-one pattern. See the [Anyscale AWS cloud configuration documentation](https://docs.anyscale.com/clouds/aws/configure) for the full set of resources Anyscale expects (VPC, subnets, IAM roles, security groups) and how they map to the fields below.",
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
				MarkdownDescription: "GCP-specific configuration. Required when cloud_provider is GCP and using all-in-one pattern. See the [Anyscale GCP cloud configuration documentation](https://docs.anyscale.com/clouds/gcp/configure) for the full set of resources Anyscale expects (VPC, subnets, service accounts, firewall policies) and how they map to the fields below.",
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
						MarkdownDescription: "List of subnet names within the VPC for Anyscale resources. VM compute only - GKE networking comes entirely from `kubernetes_config.zones`, so setting this on a Kubernetes cloud is rejected at plan time rather than silently corrupting the registered networking (the backend applies this field unconditionally after the Kubernetes zone list is written, discarding it). Genuinely supports more than one subnet on VM compute - Anyscale spreads instances across whichever are configured, this is not a modeling mismatch.",
						PlanModifiers: []planmodifier.List{
							listplanmodifier.RequiresReplace(),
						},
					},
					"controlplane_service_account_email": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Service account email for Anyscale control plane (cross-project access). See the [Anyscale Google Cloud IAM documentation](https://docs.anyscale.com/iam/google-cloud) for the roles this service account needs.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"dataplane_service_account_email": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Service account email for Ray cluster nodes (data plane). See the [Anyscale Google Cloud IAM documentation](https://docs.anyscale.com/iam/google-cloud) for the roles this service account needs.",
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
						MarkdownDescription: "The bucket region (if different from cloud region). A configuration that sets this to the same value as the cloud's own region is treated as equivalent to a null recovered value for plan purposes, so it will not force replacement - the Anyscale API cannot tell \"never set\" apart from \"explicitly set to the cloud's own region\" once stored, so there is no matching value to compare against otherwise. A cloud that already has a null value in state from an older provider version reconciles this with a one-time in-place update on its next plan, never a replace. A genuinely different bucket region round-trips normally via the real API value, and a real change to it still requires replacement.",
						PlanModifiers: []planmodifier.String{
							// regionSemanticEqualPlanModifier replaces the
							// plain stringplanmodifier.RequiresReplace() other
							// attributes in this block use - it implements
							// requires-replace-with-an-exception directly
							// (see its own doc comment in cloud_helpers.go for
							// why composing with a separate RequiresReplace()
							// does not work here). Do not add
							// stringplanmodifier.RequiresReplace() alongside
							// it - that would force replacement unconditionally
							// again, defeating the exception this exists for.
							regionSemanticEqualPlanModifier{},
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

// upgradeCloudResourceStateV2toV3 renames enable_lineage_tracking and
// enable_log_ingestion to the backend's own field names,
// lineage_tracking_enabled and is_aggregated_logs_enabled - the same names
// the plural anyscale_clouds data source already used before this release
// (see the live schema's Version 3 MarkdownDescription note and CHANGELOG for
// the full rationale). Unlike upgradeCloudResourceStateV0toV1 and
// upgradeCloudResourceStateV1toV2 (both drops), this is a pure rename: no
// value is lost, both booleans carry across unchanged under their new names -
// see cloudResourceModelV2.toCloudResourceModel(). Decodes into
// cloudResourceModelV2 (not CloudResourceModel directly) because v2's real
// stored schema still declares the old attribute names, and
// terraform-plugin-framework's struct/object reflection requires an exact
// field-for-field match - see cloudResourceModelV1's doc comment for the
// verified error this produces otherwise.
func upgradeCloudResourceStateV2toV3(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 2 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState cloudResourceModelV2
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState := priorState.toCloudResourceModel()

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}
