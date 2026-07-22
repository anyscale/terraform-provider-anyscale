package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

// Ensure CloudResourceResource satisfies the state-upgrade interface.
var _ resource.ResourceWithUpgradeState = &CloudResourceResource{}

// UpgradeState is anyscale_cloud's UpgradeState (resource_cloud_upgrade.go)
// for anyscale_cloud_resource - same v0->v1 migration (mount_targets
// block->attribute + kubernetes_config inert-field removal), same reasons,
// applied to this resource's own schema shape.
func (r *CloudResourceResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema:   cloudResourceResourceSchemaV0(),
			StateUpgrader: upgradeCloudResourceResourceStateV0toV1,
		},
	}
}

// cloudResourceResourceSchemaV0 is a frozen copy of anyscale_cloud_resource's
// schema exactly as shipped through v0.16.x - see cloudResourceSchemaV0's
// doc comment for why this must byte-match real v0.16.x state and must not
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
// migration, same reasoning, this resource's own model type.
func upgradeCloudResourceResourceStateV0toV1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 0 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState CloudResourceResourceModel
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
