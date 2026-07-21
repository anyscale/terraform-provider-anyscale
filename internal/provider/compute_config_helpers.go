package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// effectiveComputeConfig holds the resolved top-level config data for a
// compute template, honoring the deployment_configs[0] override pattern the
// Anyscale API uses: per-deployment values take precedence over the
// top-level config when present. Read and ImportState both need this same
// resolution, so it lives here once instead of being duplicated.
//
// idle_termination_minutes and maximum_uptime_minutes are deliberately not
// part of this struct: they live only on the top-level config, never on a
// per-deployment override, so callers read them straight off
// computeTemplateConfig instead.
type effectiveComputeConfig struct {
	AllowedAZs      []string
	Flags           map[string]interface{}
	AutoSelect      bool
	HeadNodeType    map[string]interface{}
	WorkerNodeTypes []map[string]interface{}
	CloudDeployment string
	// AdvancedConfig mirrors getAdvancedConfigJSON's generic/aws/gcp precedence,
	// applied to the top-level config instead of a single node.
	AdvancedConfig map[string]interface{}
}

// resolveEffectiveComputeConfig applies the deployment_configs[0] override
// pattern on top of the base config fields.
func resolveEffectiveComputeConfig(configData computeTemplateConfig) effectiveComputeConfig {
	eff := effectiveComputeConfig{
		AllowedAZs:      configData.AllowedAZs,
		Flags:           configData.Flags,
		AutoSelect:      configData.AutoSelectWorkerConfig,
		HeadNodeType:    configData.HeadNodeType,
		WorkerNodeTypes: configData.WorkerNodeTypes,
		AdvancedConfig:  firstNonNil(configData.AdvancedConfigurationsJSON, configData.AWSAdvancedConfigurations, configData.GCPAdvancedConfigurations),
	}

	if len(configData.DeploymentConfigs) == 0 {
		return eff
	}

	deploymentConfig := configData.DeploymentConfigs[0]
	if len(deploymentConfig.AllowedAZs) > 0 {
		eff.AllowedAZs = deploymentConfig.AllowedAZs
	}
	if deploymentConfig.Flags != nil {
		eff.Flags = deploymentConfig.Flags
	}
	eff.AutoSelect = deploymentConfig.AutoSelectWorkerConfig
	if deploymentConfig.HeadNodeType != nil {
		eff.HeadNodeType = deploymentConfig.HeadNodeType
	}
	if len(deploymentConfig.WorkerNodeTypes) > 0 {
		eff.WorkerNodeTypes = deploymentConfig.WorkerNodeTypes
	}
	eff.CloudDeployment = deploymentConfig.CloudDeployment
	if deploymentConfig.AdvancedConfigurationsJSON != nil {
		eff.AdvancedConfig = deploymentConfig.AdvancedConfigurationsJSON
	}

	return eff
}

// firstNonNil returns the first non-nil map, matching getAdvancedConfigJSON's
// generic/aws/gcp precedence for the per-node case.
func firstNonNil(maps ...map[string]interface{}) map[string]interface{} {
	for _, m := range maps {
		if m != nil {
			return m
		}
	}
	return nil
}

// syntheticFlagKeys are the merged-flags entries that surface as their own
// typed attributes (min_resources, max_resources, enable_cross_zone_scaling)
// rather than through the top-level flags attribute. Shared by ImportState
// and the data source's Read, both of which recover a user-facing flags
// value straight from the API with no prior state to fall back on.
var syntheticFlagKeys = map[string]struct{}{
	"min_resources":                {},
	"max_resources":                {},
	"allow-cross-zone-autoscaling": {},
}

// userFlagsFrom strips syntheticFlagKeys out of a merged flags map, leaving
// only the entries a user's own top-level flags attribute should reflect.
func userFlagsFrom(flags map[string]interface{}) map[string]interface{} {
	if len(flags) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(flags))
	for k, v := range flags {
		if _, ok := syntheticFlagKeys[k]; ok {
			continue
		}
		out[k] = v
	}
	return out
}

// importAmbiguousNodeFields are head_node/worker_nodes sub-attributes
// ImportState cannot recover unambiguously: there is no prior state yet to
// tell "the user never configured this" apart from "the API auto-filled it",
// the same ambiguity maskNodeFromPrior resolves against a real prior by
// nulling. flags and advanced_instance_config are deliberately excluded --
// CC12 recovers those from the API instead, since ordinary Read never reads
// them back on any later refresh, so import is the only unambiguous chance.
var importAmbiguousNodeFields = []string{"resources", "required_resources", "labels", "cloud_deployment"}

// nullAmbiguousImportFields nulls importAmbiguousNodeFields on a freshly
// converted API node object, leaving flags/advanced_instance_config (and
// instance_type and any worker-specific fields) at their real API values.
func nullAmbiguousImportFields(ctx context.Context, apiNode types.Object, diags *diag.Diagnostics) types.Object {
	if apiNode.IsNull() || apiNode.IsUnknown() {
		return apiNode
	}

	apiAttrs := apiNode.Attributes()
	masked := make(map[string]attr.Value, len(apiAttrs))
	for k, v := range apiAttrs {
		masked[k] = v
	}

	for _, name := range importAmbiguousNodeFields {
		if v, ok := masked[name]; ok {
			masked[name] = nullValueOf(v)
		}
	}

	obj, objDiags := types.ObjectValue(apiNode.AttributeTypes(ctx), masked)
	diags.Append(objDiags...)
	return obj
}

// nullAmbiguousImportFieldsList applies nullAmbiguousImportFields elementwise
// to a worker_nodes list, mirroring maskWorkerNodesFromPrior's shape.
func nullAmbiguousImportFieldsList(ctx context.Context, workers types.List, diags *diag.Diagnostics) types.List {
	if workers.IsNull() || workers.IsUnknown() {
		return workers
	}

	elems := workers.Elements()
	if len(elems) == 0 {
		return workers
	}

	masked := make([]attr.Value, 0, len(elems))
	for _, v := range elems {
		obj, ok := v.(types.Object)
		if !ok {
			masked = append(masked, v)
			continue
		}
		masked = append(masked, nullAmbiguousImportFields(ctx, obj, diags))
	}

	listVal, listDiags := types.ListValue(workers.ElementType(ctx), masked)
	diags.Append(listDiags...)
	return listVal
}
