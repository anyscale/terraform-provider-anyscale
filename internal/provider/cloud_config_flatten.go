package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// This file converts API config structs (AWSConfig, GCPConfig, ...) into the
// Terraform Object values used by anyscale_cloud/anyscale_cloud_resource's
// aws_config/gcp_config/kubernetes_config/object_storage/file_storage blocks -
// the reverse of expandAWSConfig etc. in resource_cloud_resource.go.
//
// C3-v2: config-block recovery from the API happens ONLY in ImportState (see
// requiredImportConfigBlocks below), never in Create/Read. These blocks are
// not Computed, so Terraform requires them to equal exactly what the plan
// configured - populating one during Create/Read that the user's .tf never
// set is a hard "provider produced inconsistent result" error, not a
// harmless enhancement. ImportState runs once, before that consistency
// machinery is in the loop, which is why it's the only safe place for this.

// resolveIsEmptyCloud derives is_empty_cloud from "does this cloud have zero
// resources attached right now", but ONLY while current is null/unknown (a
// fresh import, since Create always sets it explicitly and Read never
// touched it before C3). Once resolved - true or false - it must never be
// re-derived: a live empty cloud that later gets a resource attached would
// otherwise flip to non-empty on its next refresh, which would incorrectly
// un-gate config-block population onto a cloud whose own .tf never had one.
func resolveIsEmptyCloud(current types.Bool, resourceCount int) types.Bool {
	if current.IsNull() || current.IsUnknown() {
		return types.BoolValue(resourceCount == 0)
	}
	return current
}

// stringOrNull returns a null String for an empty API value, matching how an
// omitted Optional attribute looks in state - an empty string and "unset"
// aren't the same, and flattening "" to StringValue("") would produce a diff
// against a config that simply never set the attribute.
func stringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// stringPtrOrNull is stringOrNull for the *string API fields.
func stringPtrOrNull(s *string) types.String {
	if s == nil || *s == "" {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

// stringListOrNull builds a List from a string slice, or a null List for an
// empty/nil slice - an Optional list attribute the user never set is null,
// not an empty list, and the two are not plan-equivalent.
func stringListOrNull(ctx context.Context, items []string) (types.List, diag.Diagnostics) {
	if len(items) == 0 {
		return types.ListNull(types.StringType), nil
	}
	return types.ListValueFrom(ctx, types.StringType, items)
}

func awsConfigAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"vpc_id":                      types.StringType,
		"subnet_ids":                  types.ListType{ElemType: types.StringType},
		"subnet_ids_to_az":            types.MapType{ElemType: types.StringType},
		"security_group_ids":          types.ListType{ElemType: types.StringType},
		"controlplane_iam_role_arn":   types.StringType,
		"dataplane_iam_role_arn":      types.StringType,
		"cluster_instance_profile_id": types.StringType,
		"external_id":                 types.StringType,
		"memorydb_cluster_name":       types.StringType,
		"memorydb_cluster_arn":        types.StringType,
		"memorydb_cluster_endpoint":   types.StringType,
	}
}

func gcpConfigAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"project_id":                         types.StringType,
		"host_project_id":                    types.StringType,
		"provider_name":                      types.StringType,
		"vpc_name":                           types.StringType,
		"subnet_names":                       types.ListType{ElemType: types.StringType},
		"controlplane_service_account_email": types.StringType,
		"dataplane_service_account_email":    types.StringType,
		"firewall_policy_names":              types.ListType{ElemType: types.StringType},
		"memorystore_instance_name":          types.StringType,
		"memorystore_endpoint":               types.StringType,
	}
}

func azureConfigAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"tenant_id": types.StringType,
	}
}

func kubernetesConfigAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"anyscale_operator_iam_identity": types.StringType,
		"zones":                          types.ListType{ElemType: types.StringType},
		"redis_endpoint":                 types.StringType,
		"namespace":                      types.StringType,
		"ingress_host":                   types.StringType,
		"cluster_name":                   types.StringType,
		"context":                        types.StringType,
		"kubeconfig_path":                types.StringType,
	}
}

func objectStorageAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"bucket_name": types.StringType,
		"region":      types.StringType,
		"endpoint":    types.StringType,
	}
}

func mountTargetAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"address": types.StringType,
		"zone":    types.StringType,
	}
}

func fileStorageAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"file_storage_id":             types.StringType,
		"mount_path":                  types.StringType,
		"persistent_volume_claim":     types.StringType,
		"csi_ephemeral_volume_driver": types.StringType,
		"mount_targets":               types.ListType{ElemType: types.ObjectType{AttrTypes: mountTargetAttrTypes()}},
	}
}

// flattenAWSConfig populates aws_config from the API's AWSConfig. The API
// only carries parallel subnet_ids/zones arrays, not a map - subnet_ids_to_az
// is populated (it's the lossless representation and the schema already
// documents it as "Preferred over subnet_ids"), and subnet_ids is left null.
// A caller repopulating a block that was originally written with plain
// subnet_ids will see a diff on the subnet fields; there is no server-side
// signal for which shape the user's config used.
func flattenAWSConfig(ctx context.Context, cfg *AWSConfig) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	if cfg == nil {
		return types.ObjectNull(awsConfigAttrTypes()), diags
	}

	securityGroupIDs, d := stringListOrNull(ctx, cfg.SecurityGroupIDs)
	diags.Append(d...)

	subnetIDsToAZ := types.MapNull(types.StringType)
	if len(cfg.SubnetIDs) > 0 && len(cfg.SubnetIDs) == len(cfg.Zones) {
		azByID := make(map[string]string, len(cfg.SubnetIDs))
		for i, id := range cfg.SubnetIDs {
			azByID[id] = cfg.Zones[i]
		}
		mapVal, d := types.MapValueFrom(ctx, types.StringType, azByID)
		diags.Append(d...)
		subnetIDsToAZ = mapVal
	}

	attrs := map[string]attr.Value{
		"vpc_id":                      stringOrNull(cfg.VPCID),
		"subnet_ids":                  types.ListNull(types.StringType),
		"subnet_ids_to_az":            subnetIDsToAZ,
		"security_group_ids":          securityGroupIDs,
		"controlplane_iam_role_arn":   stringOrNull(cfg.AnyscaleIAMRoleID),
		"dataplane_iam_role_arn":      stringOrNull(cfg.ClusterIAMRoleID),
		"cluster_instance_profile_id": stringPtrOrNull(cfg.ClusterInstanceProfileID),
		"external_id":                 stringOrNull(cfg.ExternalID),
		"memorydb_cluster_name":       stringPtrOrNull(cfg.MemoryDBClusterName),
		"memorydb_cluster_arn":        stringPtrOrNull(cfg.MemoryDBClusterARN),
		"memorydb_cluster_endpoint":   stringPtrOrNull(cfg.MemoryDBClusterEndpoint),
	}

	obj, d := types.ObjectValue(awsConfigAttrTypes(), attrs)
	diags.Append(d...)
	return obj, diags
}

// flattenGCPConfig populates gcp_config from the API's GCPConfig.
func flattenGCPConfig(ctx context.Context, cfg *GCPConfig) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	if cfg == nil {
		return types.ObjectNull(gcpConfigAttrTypes()), diags
	}

	subnetNames, d := stringListOrNull(ctx, cfg.SubnetNames)
	diags.Append(d...)
	firewallPolicyNames, d := stringListOrNull(ctx, cfg.FirewallPolicyNames)
	diags.Append(d...)

	attrs := map[string]attr.Value{
		"project_id":                         stringOrNull(cfg.ProjectID),
		"host_project_id":                    stringOrNull(cfg.HostProjectID),
		"provider_name":                      stringOrNull(cfg.ProviderName),
		"vpc_name":                           stringOrNull(cfg.VPCName),
		"subnet_names":                       subnetNames,
		"controlplane_service_account_email": stringOrNull(cfg.AnyscaleServiceAccountEmail),
		"dataplane_service_account_email":    stringOrNull(cfg.ClusterServiceAccountEmail),
		"firewall_policy_names":              firewallPolicyNames,
		"memorystore_instance_name":          stringOrNull(cfg.MemorystoreInstanceName),
		"memorystore_endpoint":               stringOrNull(cfg.MemorystoreEndpoint),
	}

	obj, d := types.ObjectValue(gcpConfigAttrTypes(), attrs)
	diags.Append(d...)
	return obj, diags
}

// Note: there is no flattenAzureConfig. azure_config is optional even for
// K8S (matching aws_config/gcp_config's treatment there), so
// requiredImportConfigBlocks' required-blocks-only design never recovers it
// at import - the same reason aws_config/gcp_config have no flatten call
// site of their own on the K8S branch. azureConfigAttrTypes() is still used
// directly (e.g. to build a null Object) without needing a flatten function.

// flattenKubernetesConfig populates kubernetes_config from the API's
// KubernetesConfig. Only anyscale_operator_iam_identity/zones/redis_endpoint
// are ever sent to or returned by the API (see KubernetesConfig's doc
// comment in models.go) - namespace/ingress_host/cluster_name/context/
// kubeconfig_path are pure Terraform-side bookkeeping the API has never seen
// (and, per C5, are being deprecated as no-ops). namespace gets the schema's
// own documented default ("anyscale") since there's no other source of truth
// for it; the other four have none and are left null.
func flattenKubernetesConfig(ctx context.Context, cfg *KubernetesConfig) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	if cfg == nil {
		return types.ObjectNull(kubernetesConfigAttrTypes()), diags
	}

	zones, d := stringListOrNull(ctx, cfg.Zones)
	diags.Append(d...)

	attrs := map[string]attr.Value{
		"anyscale_operator_iam_identity": stringOrNull(cfg.AnyscaleOperatorIAMIdentity),
		"zones":                          zones,
		"redis_endpoint":                 stringOrNull(cfg.RedisEndpoint),
		"namespace":                      types.StringValue("anyscale"),
		"ingress_host":                   types.StringNull(),
		"cluster_name":                   types.StringNull(),
		"context":                        types.StringNull(),
		"kubeconfig_path":                types.StringNull(),
	}

	obj, d := types.ObjectValue(kubernetesConfigAttrTypes(), attrs)
	diags.Append(d...)
	return obj, diags
}

// stripBucketPrefix removes a cloud-storage URI scheme prefix the way a user
// following the schema's own documented convention would have typed the
// bucket name for that provider: bare for AWS ("my-bucket"), kept for GCP
// ("gs://my-bucket") - see object_storage.bucket_name's MarkdownDescription.
// The API always returns it prefixed (add_resource normalizes it that way
// before sending), so populating the raw API value verbatim for AWS would
// permanently diff against a bare-written config.
//
// Azure is deliberately NOT special-cased here (falls through to the final
// `return bucketName` below, same as GCP): its bucket is always a full
// abfss://container@account.dfs.core.windows.net URI - the mirror of
// buildProviderConfig's AZURE case never prepending a scheme on write - so
// the round-trip must leave it completely untouched in both directions.
func stripBucketPrefix(provider, bucketName string) string {
	if strings.EqualFold(provider, "AWS") {
		return strings.TrimPrefix(bucketName, "s3://")
	}
	return bucketName
}

// flattenObjectStorage populates object_storage from the API's ObjectStorage.
// provider decides whether bucket_name is unprefixed to match how a user
// would naturally write it (see stripBucketPrefix). cloudRegion is the
// resource's own region (CloudDeploymentResult.Region).
//
// L1: the backend defaults an unset bucket region to the cloud's own region
// and returns that derived value indistinguishably from a genuinely
// user-configured one - confirmed against a real customer report where the
// config set only bucket_name. object_storage.region is Optional and
// RequiresReplace, not Computed, so writing that derived value into state
// at import would force a replace against a config that never set region -
// the same destroy-and-recreate this whole fix exists to remove, just
// relocated to a different attribute. Recover region ONLY when it genuinely
// differs from cloudRegion, matching the schema's own documented meaning
// ("the bucket region, if different from cloud region"); a user who set a
// real, different region still round-trips it.
func flattenObjectStorage(cfg *ObjectStorage, provider, cloudRegion string) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	if cfg == nil {
		return types.ObjectNull(objectStorageAttrTypes()), diags
	}

	region := cfg.Region
	if region != nil && *region == cloudRegion {
		region = nil
	}

	attrs := map[string]attr.Value{
		"bucket_name": stringOrNull(stripBucketPrefix(provider, cfg.BucketName)),
		"region":      stringPtrOrNull(region),
		"endpoint":    stringPtrOrNull(cfg.Endpoint),
	}

	obj, d := types.ObjectValue(objectStorageAttrTypes(), attrs)
	diags.Append(d...)
	return obj, diags
}

// fileStorageDefaultMountPath is file_storage.mount_path's schema Default
// (stringdefault.StaticString in resource_cloud.go/resource_cloud_resource.go)
// and also what flattenFileStorage resolves to for a provider with no real
// backend field for it (see flattenFileStorage) - one constant so the two
// can never drift apart.
const fileStorageDefaultMountPath = "/mnt/shared"

// flattenFileStorage populates file_storage from the API's FileStorage.
//
// L2: mount_path is Optional+Computed with a static Default, unlike CC12's
// purely-Optional ambiguous fields (compute_config_helpers.go's
// nullAmbiguousImportFields) - nulling it here is NOT the safe move the way
// it is for a plain Optional field. ImportStateVerify runs directly against
// whatever this function writes, with no intervening plan (Defaults are a
// PlanResourceChange-only mechanism - terraform-plugin-framework's
// TransformDefaults, internal/fwschemadata/data_default.go - never invoked
// by ImportResourceState or ReadResource), so a null written here would
// still read back as null immediately after import, not as "/mnt/shared" -
// a real mismatch against the freshly-created state a customer's own
// ImportStateVerify would catch. AWS has no backend field for mount_path at
// all (validateMountPathSupported rejects a user ever setting one), so the
// API value is empty - resolve straight to fileStorageDefaultMountPath
// there, the same value a config that never sets it would already show.
// GCP/Azure/Generic DO carry a real value - recover it verbatim, or a later
// plan diffs against backend-only drift. Net rule, same one architect
// stated for the contract: recover mount_path only when the API actually
// returns a non-empty value, else resolve to the default directly.
//
// L3: mount_targets is deliberately NOT recovered here (v0.15.2/PR #180
// recovered it; this reverts that one field, everything else in this
// function is unchanged). It is an Optional sub-block a valid import-target
// config may legitimately omit - the addresses are backend-discovered
// per-AZ EFS/Filestore mount targets, not reliably expressible in HCL, the
// same "recovered vs. genuinely absent" ambiguity C3-v2 already avoids by
// never recovering optional blocks. Recovering it verbatim (the v0.15.2
// behavior) left state with real addresses against a config that only set
// file_storage_id, and since mount_targets carries
// listplanmodifier.RequiresReplace(), every subsequent plan proposed
// destroying and recreating the live, in-use cloud - a real customer
// report. Leaving it null here matches a config that never sets it: null
// state, null config, no diff, ever (file_storage is never Read-refreshed -
// see mount_path's own note above - so state can't drift back to non-null
// later). The create path is untouched: expandFileStorage still sends a
// config-supplied mount_targets to the backend, and changing it still
// forces replacement - only import recovery is disabled.
//
// persistent_volume_claim/csi_ephemeral_volume_driver have no Default and no
// AWS-specific quirk - still recovered exactly as the API carries them,
// nil-safe, same discipline as every other flatten* function in this file.
func flattenFileStorage(ctx context.Context, cfg *FileStorage) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	if cfg == nil {
		return types.ObjectNull(fileStorageAttrTypes()), diags
	}

	mountPath := types.StringValue(fileStorageDefaultMountPath)
	if cfg.MountPath != "" {
		mountPath = types.StringValue(cfg.MountPath)
	}

	// See the L3 doc comment above: mount_targets is never populated from
	// cfg here, regardless of what the API returned - deliberate, not an
	// oversight.
	mountTargets := types.ListNull(types.ObjectType{AttrTypes: mountTargetAttrTypes()})

	attrs := map[string]attr.Value{
		"file_storage_id":             stringOrNull(cfg.FileStorageID),
		"mount_path":                  mountPath,
		"persistent_volume_claim":     stringOrNull(cfg.PersistentVolumeClaim),
		"csi_ephemeral_volume_driver": stringOrNull(cfg.CSIEphemeralVolumeDriver),
		"mount_targets":               mountTargets,
	}

	obj, d := types.ObjectValue(fileStorageAttrTypes(), attrs)
	diags.Append(d...)
	return obj, diags
}

// requiredImportConfigBlocks recovers every config block ImportState can
// safely populate for a valid anyscale_cloud or anyscale_cloud_resource
// config, based on compute stack and provider, flattened from the given
// resource's API data. Returns an empty map if there's nothing to recover
// (nil resource, e.g. a genuinely empty cloud) - never an error on its own.
//
// Provider config is still compute-stack-gated (see C3-v2): VM gets
// aws_config OR gcp_config; K8S gets kubernetes_config. Recovering the
// OTHER provider's block (e.g. aws_config on a K8S cloud, where it's
// optional) would reintroduce the ambiguity C3-v2 was written to avoid: a
// later Read has no way to tell "recovered at import" apart from
// "genuinely absent" for an optional block, but for a compute-stack-
// required block that distinction never arises, since a valid config could
// never have left it unset in the first place.
//
// object_storage and file_storage are recovered on EVERY compute stack now,
// whenever the API actually returns data for them - this is the fix for a
// real customer report (AWS VM cloud, object_storage/file_storage
// configured, import forced a destroy-and-recreate because both are
// ForceNew and neither was recovered for VM). This does NOT reopen C3-v2's
// ambiguity concern the way recovering aws_config on K8S would: both
// flatten functions already return ObjectNull for a nil cfg, so a live
// resource that genuinely has no storage configured still comes back null,
// matching an omitted block exactly - the only residual risk is the
// opposite direction (a cloud whose storage was auto-provisioned and never
// declared in .tf now sees a one-time reconcile diff instead of silence),
// which is a plan diff to review, not a destructive replace, and is called
// out explicitly in the changelog/docs for this fix rather than buried.
// object_storage additionally guards against the backend's region auto-fill
// (see flattenObjectStorage's own doc, L1) and file_storage's mount_path
// against collapsing the schema's Computed default (see flattenFileStorage,
// L2) - both are real, verified landmines a naive "recover whatever the API
// returns" change would have hit.
//
// Shared by both resources' ImportState: the decision (which block, from
// which struct field) is identical, only the surrounding API calls to reach
// a *CloudDeploymentResult differ between a cloud's default resource and a
// cloud_resource's own named lookup.
func requiredImportConfigBlocks(ctx context.Context, provider string, defaultResource *CloudDeploymentResult) (map[string]types.Object, diag.Diagnostics) {
	blocks := map[string]types.Object{}
	var diags diag.Diagnostics

	if defaultResource == nil {
		return blocks, diags
	}

	if defaultResource.ComputeStack == "K8S" {
		if defaultResource.KubernetesConfig != nil {
			obj, d := flattenKubernetesConfig(ctx, defaultResource.KubernetesConfig)
			diags.Append(d...)
			blocks["kubernetes_config"] = obj
		}
	} else {
		switch strings.ToUpper(provider) {
		case "AWS":
			if defaultResource.AWSConfig != nil {
				obj, d := flattenAWSConfig(ctx, defaultResource.AWSConfig)
				diags.Append(d...)
				blocks["aws_config"] = obj
			}
		case "GCP":
			if defaultResource.GCPConfig != nil {
				obj, d := flattenGCPConfig(ctx, defaultResource.GCPConfig)
				diags.Append(d...)
				blocks["gcp_config"] = obj
			}
		}
	}

	if defaultResource.ObjectStorage != nil {
		obj, d := flattenObjectStorage(defaultResource.ObjectStorage, provider, defaultResource.Region)
		diags.Append(d...)
		blocks["object_storage"] = obj
	}
	if defaultResource.FileStorage != nil {
		obj, d := flattenFileStorage(ctx, defaultResource.FileStorage)
		diags.Append(d...)
		blocks["file_storage"] = obj
	}

	return blocks, diags
}
