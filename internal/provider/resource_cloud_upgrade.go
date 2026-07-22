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

// upgradeCloudResourceStateV0toV1 drops the 5 removed kubernetes_config
// attributes and carries every other field - including file_storage (whose
// mount_targets is now a ListNestedAttribute, but decodes to the identical
// types.Object shape regardless, per the verified Block/Attribute
// type-identity) - through completely unchanged.
func upgradeCloudResourceStateV0toV1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 0 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState CloudResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	upgradedKubernetesConfig, kubeDiags := upgradeKubernetesConfigV0toV1(priorState.KubernetesConfig)
	resp.Diagnostics.Append(kubeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState := priorState
	newState.KubernetesConfig = upgradedKubernetesConfig
	// FileStorage (mount_targets included), AWSConfig, GCPConfig,
	// AzureConfig, ObjectStorage, and every scalar field pass through
	// unchanged - copied by newState := priorState above.

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// upgradeKubernetesConfigV0toV1 drops the 5 removed inert bookkeeping
// attributes (namespace/ingress_host/cluster_name/context/kubeconfig_path)
// from a v0 kubernetes_config Object, carrying the 3 real attributes
// (anyscale_operator_iam_identity/zones/redis_endpoint) through unchanged.
// There is nothing to migrate for the 5 dropped attributes: they were never
// sent to the Anyscale API (see kubernetesConfigInertFieldDeprecationMessage),
// so no value ever stored for them carried real information to preserve -
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
