package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
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
// pattern on top of the base config fields. This is the common single-resource
// case: exactly 0 or 1 deployment_configs entries, always overriding with
// index 0.
func resolveEffectiveComputeConfig(configData computeTemplateConfig) effectiveComputeConfig {
	eff := baseEffectiveComputeConfig(configData)

	if len(configData.DeploymentConfigs) == 0 {
		return eff
	}

	return applyDeploymentConfigOverride(eff, configData.DeploymentConfigs[0])
}

// resolveEffectiveComputeConfigWithOverride is resolveEffectiveComputeConfig's
// Option C (multi-resource) sibling: applies overrideEntry as the "primary"
// override instead of unconditionally using index 0. Callers use this once
// splitDeploymentConfigsForRead has determined - by name against prior state,
// not by position - which of 2+ deployment_configs entries is primary, since
// the backend's response order is not guaranteed to put it first.
func resolveEffectiveComputeConfigWithOverride(configData computeTemplateConfig, overrideEntry cloudDeploymentComputeConfig) effectiveComputeConfig {
	return applyDeploymentConfigOverride(baseEffectiveComputeConfig(configData), overrideEntry)
}

// baseEffectiveComputeConfig builds the un-overridden base: the compute
// template's own top-level fields, before any deployment_configs entry is
// applied on top.
func baseEffectiveComputeConfig(configData computeTemplateConfig) effectiveComputeConfig {
	return effectiveComputeConfig{
		AllowedAZs:      configData.AllowedAZs,
		Flags:           configData.Flags,
		AutoSelect:      configData.AutoSelectWorkerConfig,
		HeadNodeType:    configData.HeadNodeType,
		WorkerNodeTypes: configData.WorkerNodeTypes,
		AdvancedConfig:  firstNonNil(configData.AdvancedConfigurationsJSON, configData.AWSAdvancedConfigurations, configData.GCPAdvancedConfigurations),
	}
}

// applyDeploymentConfigOverride applies a single deployment_configs entry's
// fields on top of a base effectiveComputeConfig, per the API's
// deployment_configs[N] override pattern.
func applyDeploymentConfigOverride(eff effectiveComputeConfig, deploymentConfig cloudDeploymentComputeConfig) effectiveComputeConfig {
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

// memoryUnitMultipliers maps the Kubernetes-quantity-style unit suffixes our
// schema documentation promises ("4Gi", "1024Mi") to their byte multiplier.
// Binary (power-of-1024) suffixes use a trailing "i"; decimal (power-of-1000)
// suffixes don't. Longer suffixes are checked first so "Mi" doesn't
// short-match on a bare "M" prefix.
var memoryUnitSuffixes = []struct {
	suffix     string
	multiplier float64
}{
	{"Ei", 1 << 60}, {"Pi", 1 << 50}, {"Ti", 1 << 40}, {"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10},
	{"E", 1e18}, {"P", 1e15}, {"T", 1e12}, {"G", 1e9}, {"M", 1e6}, {"k", 1e3},
}

var memoryPlainNumberPattern = regexp.MustCompile(`^\d+(\.\d+)?$`)

// parseMemoryToBytes converts a required_resources.memory value to the plain
// integer byte count the real API requires (F2: the API 422s on a unit string
// like "4Gi" even though our own schema documents that format, matching the
// SDK's _parse_memory_string convention). A bare number (with or without a
// decimal point) is treated as already-bytes and passed through unchanged.
func parseMemoryToBytes(s string) (int64, error) {
	if memoryPlainNumberPattern.MatchString(s) {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid memory value %q: %w", s, err)
		}
		return int64(f), nil
	}

	for _, u := range memoryUnitSuffixes {
		if amount, ok := trimSuffixFloat(s, u.suffix); ok {
			return int64(amount * u.multiplier), nil
		}
	}

	return 0, fmt.Errorf(
		"invalid memory format %q: expected a plain byte count or a value with a unit suffix (Ki, Mi, Gi, Ti, Pi, Ei, k, M, G, T, P, E), e.g. \"4Gi\" or \"1024Mi\"",
		s,
	)
}

// trimSuffixFloat returns (value, true) if s is a decimal number immediately
// followed by suffix, else (0, false).
func trimSuffixFloat(s, suffix string) (float64, bool) {
	if len(s) <= len(suffix) || s[len(s)-len(suffix):] != suffix {
		return 0, false
	}
	numPart := s[:len(s)-len(suffix)]
	if !memoryPlainNumberPattern.MatchString(numPart) {
		return 0, false
	}
	f, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// workerNodeName extracts the "name" attribute from a worker_nodes list
// element as a plain string, returning ("", false) if the element isn't a
// types.Object or its name is null/unknown/absent.
func workerNodeName(v attr.Value) (string, bool) {
	obj, ok := v.(types.Object)
	if !ok {
		return "", false
	}
	nameAttr, ok := obj.Attributes()["name"].(types.String)
	if !ok || nameAttr.IsNull() || nameAttr.IsUnknown() {
		return "", false
	}
	return nameAttr.ValueString(), true
}

// reorderWorkersToMatchPrior implements F6: reorders apiWorkers to match
// priorWorkers' order by matching each element's "name" attribute, so a
// worker_nodes list whose backend-returned order differs from the user's own
// configured order doesn't show a spurious diff (worker order is not
// semantically meaningful to the backend - Ray's autoscaler indexes node
// types by name, not position - so preserving the user's own order is purely
// a Terraform-UX concern, not a correctness one).
//
// Reorders ONLY when every name is unique within apiWorkers AND within
// priorWorkers individually (the uniqueness guard, matching the same
// principle F5 uses for name derivation) - on any collision or unmatchable
// (null/unknown) name on either side, this returns apiWorkers completely
// unchanged rather than guess at a pairing, falling back to the API's own
// order exactly as before this fix.
func reorderWorkersToMatchPrior(ctx context.Context, apiWorkers types.List, priorWorkers types.List) types.List {
	apiElems := apiWorkers.Elements()
	priorElems := priorWorkers.Elements()
	if len(apiElems) == 0 || len(priorElems) == 0 {
		return apiWorkers
	}

	apiByName := make(map[string]attr.Value, len(apiElems))
	for _, v := range apiElems {
		name, ok := workerNodeName(v)
		if !ok {
			return apiWorkers // unmatchable name on the API side - bail out
		}
		if _, dup := apiByName[name]; dup {
			return apiWorkers // duplicate name on the API side - bail out
		}
		apiByName[name] = v
	}

	priorNames := make(map[string]struct{}, len(priorElems))
	orderedPriorNames := make([]string, 0, len(priorElems))
	for _, v := range priorElems {
		name, ok := workerNodeName(v)
		if !ok {
			return apiWorkers // unmatchable name on the prior side - bail out
		}
		if _, dup := priorNames[name]; dup {
			return apiWorkers // duplicate name on the prior side - bail out
		}
		priorNames[name] = struct{}{}
		orderedPriorNames = append(orderedPriorNames, name)
	}

	reordered := make([]attr.Value, 0, len(apiElems))
	placed := make(map[string]struct{}, len(apiElems))
	for _, name := range orderedPriorNames {
		if v, ok := apiByName[name]; ok {
			reordered = append(reordered, v)
			placed[name] = struct{}{}
		}
	}
	// Any API worker whose name wasn't in prior state (newly added this
	// apply) has no prior position to match - append in the API's own order.
	for _, v := range apiElems {
		name, _ := workerNodeName(v)
		if _, alreadyPlaced := placed[name]; !alreadyPlaced {
			reordered = append(reordered, v)
		}
	}

	listVal, listDiags := types.ListValue(apiWorkers.ElementType(ctx), reordered)
	if listDiags.HasError() {
		return apiWorkers
	}
	return listVal
}

// disambiguateDefaultedWorkerNames implements F5: two worker groups sharing
// an instance_type that both leave name unset would otherwise derive the
// identical instance_type-based default (workerNodeConfigToAPI), colliding
// in the backend's name-keyed available_node_types dict at cluster launch.
// Storage itself tolerates the duplicate (live-confirmed), so this is a
// defensive fix, not a correctness requirement at the API layer.
//
// Only indices in defaultedIndices are eligible for renaming - a name the
// user set explicitly is left untouched even if it collides, since silently
// overriding an explicitly-configured value is not this fix's call to make
// (and would itself risk a plan/apply inconsistency). workers is mutated in
// place; iteration order must match the user's own worker_nodes list order
// so the result is deterministic across runs, not dependent on map order.
func disambiguateDefaultedWorkerNames(workers []map[string]interface{}, defaultedIndices []int) {
	if len(defaultedIndices) == 0 {
		return
	}

	defaulted := make(map[int]struct{}, len(defaultedIndices))
	for _, i := range defaultedIndices {
		defaulted[i] = struct{}{}
	}

	seen := make(map[string]struct{}, len(workers))
	for i, cfg := range workers {
		name, ok := cfg["name"].(string)
		if !ok {
			continue
		}

		if _, isDefaulted := defaulted[i]; !isDefaulted {
			seen[name] = struct{}{}
			continue
		}
		if _, collides := seen[name]; !collides {
			seen[name] = struct{}{}
			continue
		}

		for suffix := 2; ; suffix++ {
			candidate := fmt.Sprintf("%s-%d", name, suffix)
			if _, taken := seen[candidate]; !taken {
				cfg["name"] = candidate
				seen[candidate] = struct{}{}
				break
			}
		}
	}
}

// nameVersionImportIDPattern validates the name portion of a name:version
// import id (A2/GAP-4) AFTER the caller has already confirmed the whole
// string contains exactly one colon - mirrors the CLI's own grammar
// (parse_cluster_compute_name_version, cluster_compute.py:59-61): the name
// may contain spaces but never a colon, and the version is a positive
// integer. The colon+version suffix is mandatory here (not optional the way
// the CLI's own pattern has it), since this function is only ever called
// once a colon is already known to be present.
var nameVersionImportIDPattern = regexp.MustCompile(`^([^\s:]+(?:\s+[^\s:]+)*):([1-9][0-9]*)$`)

// computeConfigImportIDKind classifies a terraform import id for
// anyscale_compute_config (A2/GAP-4).
type computeConfigImportIDKind int

const (
	importIDKindCptID computeConfigImportIDKind = iota
	importIDKindNameVersion
	importIDKindMalformed
)

// classifyComputeConfigImportID implements A2/GAP-4's client-side id
// classification, run BEFORE any API call: the backend gives a malformed id
// and a well-formed-but-nonexistent one the IDENTICAL "not found" response
// (live-confirmed), so a distinct "malformed" diagnostic can only come from
// classifying the string itself, never from inspecting the API response
// after the fact. cpt_ ids are guaranteed colon-free by construction
// (shared_anyscale_utils id_gen.py's charset), so "exactly one colon" is an
// unambiguous discriminator against name:version with no mis-parse risk.
func classifyComputeConfigImportID(id string) (kind computeConfigImportIDKind, name string, version int64) {
	if strings.Count(id, ":") == 1 {
		m := nameVersionImportIDPattern.FindStringSubmatch(id)
		if m == nil {
			return importIDKindMalformed, "", 0
		}
		v, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			return importIDKindMalformed, "", 0
		}
		return importIDKindNameVersion, m[1], v
	}
	if strings.HasPrefix(id, "cpt_") {
		return importIDKindCptID, "", 0
	}
	return importIDKindMalformed, "", 0
}

// resolveComputeConfigImportID resolves a name:version-shaped import id to
// its cpt_ config_id via the compute_templates search endpoint (A2/GAP-4).
// Unlike findComputeConfigByName (which silently picks the most recent
// match on a name collision - the right behavior for a plain "give me the
// latest" data source lookup), this ERRORS on an ambiguous match across more
// than one cloud: import must resolve to exactly one real config, never
// guess, since a compute config name is scoped to (name, cloud_id), not
// globally unique. Returns ("", nil) if nothing matches (not found).
func resolveComputeConfigImportID(ctx context.Context, client *Client, name string, version int64) (string, error) {
	searchPayload := map[string]interface{}{
		"name":              map[string]string{"equals": name},
		"include_anonymous": false,
		"archive_status":    "ALL",
		"version":           version,
	}

	results, err := searchComputeTemplatesPaged(ctx, client, searchPayload)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "", nil
	}

	distinctClouds := map[string]struct{}{}
	for _, r := range results {
		if r.Config.CloudID != "" {
			distinctClouds[r.Config.CloudID] = struct{}{}
		}
	}
	if len(distinctClouds) > 1 {
		return "", fmt.Errorf(
			"name %q at version %d exists in %d different clouds; import by the version-specific config_id (cpt_...) instead to disambiguate",
			name, version, len(distinctClouds),
		)
	}

	return results[0].ID, nil
}

// resolveCloudIDToName implements A3: the wire has no cloud-name field
// (only cloud_id), so a cloud_name-based config gets a benign one-time
// null->configured diff on the very first plan after import unless
// ImportState reverse-resolves it here. Best-effort: returns ("", false) on
// any failure (network error, unexpected status, or a genuinely gone cloud)
// rather than blocking the import - the same "not critical" tolerance the
// data source's own equivalent lookup already uses, just recovered once here
// at import time instead of every Read.
func resolveCloudIDToName(ctx context.Context, client *Client, cloudID string) (string, bool) {
	if cloudID == "" {
		return "", false
	}
	cloudResp, err := DoRequestAndParse[CloudResponse](
		ctx, client, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil, http.StatusOK,
	)
	if err != nil || cloudResp.Result.Name == "" {
		return "", false
	}
	return cloudResp.Result.Name, true
}

// additionalResourceToDeploymentConfig converts one additional_resources
// entry (Option C) into a deployment_configs wire entry, mirroring
// buildComputeConfigRequest's own per-entry folding (zones->AllowedAZs,
// min/max_resources+cross-zone-scaling folded into flags) - but reads
// advanced_instance_config/flags as JSON STRINGS rather than Dynamic, since
// this entry lives inside a list (same constraint as per-node fields).
func additionalResourceToDeploymentConfig(ctx context.Context, entry AdditionalResourceModel) (cloudDeploymentComputeConfig, error) {
	deploymentConfig := cloudDeploymentComputeConfig{
		CloudDeployment: entry.CloudResource.ValueString(),
	}

	if !entry.Zones.IsNull() {
		zonesResult, zonesDiags := StringListToInterface(ctx, entry.Zones)
		if zonesDiags.HasError() {
			return deploymentConfig, fmt.Errorf("failed to convert additional_resources zones: %v", zonesDiags)
		}
		if len(zonesResult) > 0 {
			deploymentConfig.AllowedAZs = zonesResult
		}
	}

	if !entry.AutoSelectWorkerConfig.IsNull() {
		deploymentConfig.AutoSelectWorkerConfig = entry.AutoSelectWorkerConfig.ValueBool()
	}

	if !entry.HeadNode.IsNull() {
		headNodeConfig, err := nodeConfigToAPI(ctx, entry.HeadNode)
		if err != nil {
			return deploymentConfig, fmt.Errorf("failed to convert additional_resources head_node: %w", err)
		}
		deploymentConfig.HeadNodeType = headNodeConfig
	}

	if !entry.WorkerNodes.IsNull() {
		workerElements := entry.WorkerNodes.Elements()
		workerConfigs := make([]map[string]interface{}, 0, len(workerElements))
		var defaultedNameIndices []int
		for _, workerNodeValue := range workerElements {
			workerNodeObj, ok := workerNodeValue.(types.Object)
			if !ok {
				return deploymentConfig, fmt.Errorf("expected types.Object for an additional_resources worker node")
			}
			workerConfig, err := workerNodeConfigToAPI(ctx, workerNodeObj)
			if err != nil {
				return deploymentConfig, fmt.Errorf("failed to convert additional_resources worker node: %w", err)
			}
			if workerConfig != nil {
				// F5 follow-up: same IsUnknown gap as the primary path's copy
				// of this logic (resource_compute_config.go) - a fresh
				// Create leaves an omitted name Unknown, not null.
				if nameAttr, ok := workerNodeObj.Attributes()["name"].(types.String); ok && (nameAttr.IsNull() || nameAttr.IsUnknown()) {
					defaultedNameIndices = append(defaultedNameIndices, len(workerConfigs))
				}
				workerConfigs = append(workerConfigs, workerConfig)
			}
		}
		disambiguateDefaultedWorkerNames(workerConfigs, defaultedNameIndices)
		if len(workerConfigs) > 0 {
			deploymentConfig.WorkerNodeTypes = workerConfigs
		}
	}

	if !entry.AdvancedInstanceConfig.IsNull() && entry.AdvancedInstanceConfig.ValueString() != "" {
		var advancedConfig map[string]interface{}
		if err := json.Unmarshal([]byte(entry.AdvancedInstanceConfig.ValueString()), &advancedConfig); err == nil {
			deploymentConfig.AdvancedConfigurationsJSON = advancedConfig
		}
	}

	flags := make(map[string]interface{})
	if !entry.Flags.IsNull() && entry.Flags.ValueString() != "" {
		if err := json.Unmarshal([]byte(entry.Flags.ValueString()), &flags); err != nil {
			return deploymentConfig, fmt.Errorf("failed to parse additional_resources flags JSON: %w", err)
		}
	}

	if !entry.EnableCrossZoneScaling.IsNull() {
		flags["allow-cross-zone-autoscaling"] = entry.EnableCrossZoneScaling.ValueBool()
	}

	if !entry.MinResources.IsNull() {
		minResourcesMap := make(map[string]interface{})
		for key, value := range entry.MinResources.Elements() {
			if float64Val, ok := value.(types.Float64); ok && !float64Val.IsNull() {
				minResourcesMap[key] = float64Val.ValueFloat64()
			}
		}
		if len(minResourcesMap) > 0 {
			flags["min_resources"] = minResourcesMap
		}
	}

	if !entry.MaxResources.IsNull() {
		maxResourcesMap := make(map[string]interface{})
		for key, value := range entry.MaxResources.Elements() {
			if float64Val, ok := value.(types.Float64); ok && !float64Val.IsNull() {
				maxResourcesMap[key] = float64Val.ValueFloat64()
			}
		}
		if len(maxResourcesMap) > 0 {
			flags["max_resources"] = maxResourcesMap
		}
	}

	if len(flags) > 0 {
		deploymentConfig.Flags = flags
	}

	return deploymentConfig, nil
}

// apiDeploymentConfigToAdditionalResource converts one deployment_configs
// wire entry into an additional_resources types.Object (Option C), unfolding
// the same flags-encoded fields (min_resources/max_resources/cross-zone
// scaling) the top-level Read path unfolds, and masking node fields against
// a prior entry the same way maskNodeFromPrior does for head_node/worker_nodes.
//
// priorEntry may be nil (a brand-new entry with no prior to mask against -
// e.g. first read after this entry was added, or a cold import with no
// additional_resources in state at all yet).
//
// forImport mirrors CC12: advanced_instance_config and flags (this entry's
// own custom, non-synthetic flags) are write-only-ish and are never
// refreshed from the API on an ordinary Read (they stay on whatever prior
// state/ImportState already seeded) - forImport=true is the one path that
// recovers them directly from the API, exactly once, matching the top-level
// behavior in ImportState.
func apiDeploymentConfigToAdditionalResource(ctx context.Context, entry cloudDeploymentComputeConfig, priorEntry *AdditionalResourceModel, forImport bool) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics

	attrs := map[string]attr.Value{
		"cloud_resource":            types.StringNull(),
		"zones":                     types.ListNull(types.StringType),
		"min_resources":             types.MapNull(types.Float64Type),
		"max_resources":             types.MapNull(types.Float64Type),
		"enable_cross_zone_scaling": types.BoolValue(false),
		"advanced_instance_config":  types.StringNull(),
		"flags":                     types.StringNull(),
		"auto_select_worker_config": types.BoolValue(entry.AutoSelectWorkerConfig),
		"head_node":                 types.ObjectNull(nodeConfigAttrTypes()),
		"worker_nodes":              types.ListNull(types.ObjectType{AttrTypes: workerNodeConfigAttrTypes()}),
	}

	if entry.CloudDeployment != "" {
		attrs["cloud_resource"] = types.StringValue(entry.CloudDeployment)
	}

	// Mirrors the top-level Read's zones handling: when the API sends no
	// allowed_azs for this entry at all, leave whatever the prior entry had
	// rather than nulling it - a transient/absent field this cycle shouldn't
	// blow away a previously-derived value. A brand-new entry (priorEntry
	// nil, e.g. cold import) has no prior to fall back to, so it starts null.
	if priorEntry != nil {
		attrs["zones"] = priorEntry.Zones
	}
	if len(entry.AllowedAZs) > 0 {
		if len(entry.AllowedAZs) == 1 && strings.EqualFold(entry.AllowedAZs[0], "any") {
			attrs["zones"] = types.ListNull(types.StringType)
		} else {
			azInterfaces := make([]interface{}, 0, len(entry.AllowedAZs))
			for _, az := range entry.AllowedAZs {
				azInterfaces = append(azInterfaces, az)
			}
			zonesList, zonesDiags := InterfaceListToString(ctx, azInterfaces)
			diags.Append(zonesDiags...)
			attrs["zones"] = zonesList
		}
	}

	priorMinResources := types.MapNull(types.Float64Type)
	priorMaxResources := types.MapNull(types.Float64Type)
	priorHeadNode := types.ObjectNull(nodeConfigAttrTypes())
	priorWorkerNodes := types.ListNull(types.ObjectType{AttrTypes: workerNodeConfigAttrTypes()})
	if priorEntry != nil {
		priorMinResources = priorEntry.MinResources
		priorMaxResources = priorEntry.MaxResources
		priorHeadNode = priorEntry.HeadNode
		priorWorkerNodes = priorEntry.WorkerNodes
	}

	if entry.Flags != nil {
		if minResources, ok := entry.Flags["min_resources"].(map[string]interface{}); ok {
			minMap, minDiags := InterfaceMapToFloat64(ctx, minResources)
			diags.Append(minDiags...)
			attrs["min_resources"] = restoreMapKeyCasing(ctx, minMap, priorMinResources)
		}
		if maxResources, ok := entry.Flags["max_resources"].(map[string]interface{}); ok {
			maxMap, maxDiags := InterfaceMapToFloat64(ctx, maxResources)
			diags.Append(maxDiags...)
			attrs["max_resources"] = restoreMapKeyCasing(ctx, maxMap, priorMaxResources)
		}
		// CC14 applies per-entry too: resolve unconditionally, not just when
		// present, so ImportState (no prior to lean on) still settles on a
		// real false instead of a permanent phantom-null diff.
		if enableCrossZone, ok := entry.Flags["allow-cross-zone-autoscaling"].(bool); ok {
			attrs["enable_cross_zone_scaling"] = types.BoolValue(enableCrossZone)
		}
	}

	if forImport {
		if entry.AdvancedConfigurationsJSON != nil {
			if jsonBytes, err := json.Marshal(entry.AdvancedConfigurationsJSON); err == nil {
				attrs["advanced_instance_config"] = types.StringValue(string(jsonBytes))
			}
		}
		if userFlags := userFlagsFrom(entry.Flags); len(userFlags) > 0 {
			if jsonBytes, err := json.Marshal(userFlags); err == nil {
				attrs["flags"] = types.StringValue(string(jsonBytes))
			}
		}
	} else if priorEntry != nil {
		attrs["advanced_instance_config"] = priorEntry.AdvancedInstanceConfig
		attrs["flags"] = priorEntry.Flags
	}

	// GAP-3 applies per-entry too: at import (forImport), recover
	// resources/required_resources/labels/required_labels/cloud_deployment
	// unmasked, same as the top-level ImportState now does - see the GAP-3
	// comment there for why these are safe to recover rather than null.
	if entry.HeadNodeType != nil {
		headNodeObj, headNodeDiags := apiNodeTypeToTerraform(ctx, entry.HeadNodeType)
		diags.Append(headNodeDiags...)
		if !headNodeDiags.HasError() {
			if forImport {
				attrs["head_node"] = headNodeObj
			} else {
				attrs["head_node"] = maskNodeFromPrior(ctx, headNodeObj, priorHeadNode, &diags)
			}
		}
	}

	if len(entry.WorkerNodeTypes) > 0 {
		workerInterfaces := make([]interface{}, 0, len(entry.WorkerNodeTypes))
		for _, w := range entry.WorkerNodeTypes {
			workerInterfaces = append(workerInterfaces, w)
		}
		workerNodesList, workerDiags := apiWorkerNodeTypesToTerraform(ctx, workerInterfaces)
		diags.Append(workerDiags...)
		if !workerDiags.HasError() {
			if forImport {
				attrs["worker_nodes"] = workerNodesList
			} else {
				attrs["worker_nodes"] = maskWorkerNodesFromPrior(ctx, workerNodesList, priorWorkerNodes, &diags)
			}
		}
	}

	obj, objDiags := types.ObjectValue(additionalResourceAttrTypes(), attrs)
	diags.Append(objDiags...)
	return obj, diags
}

// additionalResourceRolePrior captures what a prior additional_resources
// entry (or the top-level primary) looked like, keyed by cloud_resource
// name, for splitDeploymentConfigsForRead's matching.
type additionalResourceRolePrior struct {
	isPrimary bool
	entry     *AdditionalResourceModel // nil for the primary role
}

// splitDeploymentConfigsForRead implements Option C's read-side matching
// (F7): when a compute config has more than one deployment_configs entry,
// splits them into (primary, additional) by matching each entry's
// cloud_deployment name against PRIOR STATE (top-level cloud_resource, plus
// each additional_resources entry's own cloud_resource) - robust to peer
// reordering by the backend, the same idiom F6 uses for worker_nodes.
// cloud_resource names are inherently unique (anyscale_cloud_resource keeps
// them unique per cloud), so unlike F6's worker-name case, no uniqueness
// GUARD is needed here - only an ok=false result when an entry cannot be
// placed at all.
//
// Unmatched entries (new since last apply, or a cold import/first read with
// no prior state at all) are assigned deterministically: the first such
// entry (in the response's own order) becomes primary if none matched yet,
// the rest become additional (name-sorted for stable diffs).
//
// Returns ok=false (F7: caller should emit a loud diagnostic rather than
// silently guess) only when an entry's cloud_deployment name is itself
// empty - a shape this provider's own Create/Update never produces (every
// entry we send always sets cloud_deployment), so this should only occur
// for a multi-resource config created some other way.
func splitDeploymentConfigsForRead(
	entries []cloudDeploymentComputeConfig,
	priorCloudResource string,
	priorAdditionalResources map[string]*AdditionalResourceModel,
	priorAdditionalOrder []string,
) (primary cloudDeploymentComputeConfig, additional []cloudDeploymentComputeConfig, ok bool) {
	if len(entries) <= 1 {
		if len(entries) == 1 {
			return entries[0], nil, true
		}
		return cloudDeploymentComputeConfig{}, nil, true
	}

	priorRoles := map[string]additionalResourceRolePrior{}
	if priorCloudResource != "" {
		priorRoles[priorCloudResource] = additionalResourceRolePrior{isPrimary: true}
	}
	for name, e := range priorAdditionalResources {
		if name != "" {
			priorRoles[name] = additionalResourceRolePrior{entry: e}
		}
	}

	var primaryEntry *cloudDeploymentComputeConfig
	var additionalEntries []cloudDeploymentComputeConfig
	var unmatched []cloudDeploymentComputeConfig

	for i := range entries {
		e := entries[i]
		if e.CloudDeployment == "" {
			return cloudDeploymentComputeConfig{}, nil, false
		}
		role, known := priorRoles[e.CloudDeployment]
		switch {
		case known && role.isPrimary && primaryEntry == nil:
			entryCopy := e
			primaryEntry = &entryCopy
		case known && !role.isPrimary:
			additionalEntries = append(additionalEntries, e)
		default:
			unmatched = append(unmatched, e)
		}
	}

	if primaryEntry == nil && len(unmatched) > 0 {
		primaryEntry = &unmatched[0]
		unmatched = unmatched[1:]
	}
	additionalEntries = append(additionalEntries, unmatched...)

	if primaryEntry == nil {
		return cloudDeploymentComputeConfig{}, nil, false
	}

	// additional_resources is a plain (non-Computed) list: Terraform Core
	// requires the final state to preserve list ORDER exactly wherever the
	// value isn't allowed to just change on its own. Sorting here instead of
	// matching prior order would manufacture a spurious diff on every Read
	// whose config order isn't already alphabetical by cloud_resource - and
	// for Create/Update, where "prior" is the plan's own order, it would
	// outright crash apply with "provider produced inconsistent result",
	// the exact class of bug this quest started by chasing down (F4).
	additionalEntries = reorderDeploymentConfigsToMatchPrior(additionalEntries, priorAdditionalOrder)

	return *primaryEntry, additionalEntries, true
}

// reorderDeploymentConfigsToMatchPrior reorders the "additional" bucket
// (role classification already decided by the caller) to match
// priorAdditionalOrder - the order cloud_resource names appeared in prior's
// additional_resources list - mirroring reorderWorkersToMatchPrior's
// match-by-name-or-bail idiom for worker_nodes (F6). For Read/ImportState,
// "prior" is state's previous additional_resources, so this stops a
// BACKEND response-order change from becoming a spurious plan diff. For
// Create/Update, "prior" is the PLAN's own additional_resources (see
// populateComputedFieldsFromResponse), so this is what keeps the final
// state's order identical to what was planned - required for a non-Computed
// list attribute, not just cosmetic.
//
// A user's OWN reorder of additional_resources in their .tf is NOT
// suppressed by this, same as worker_nodes/F6 - that is expected, ordinary
// List-attribute diffing, not something either mechanism tries to hide.
//
// Falls back to entries unchanged (natural collection order: prior-matched
// entries in response order, then genuinely new ones) on any ambiguity - a
// duplicate cloud_resource on either side, or an entry with no resolvable
// name - never guesses. cloud_resource uniqueness is enforced by
// validateCloudResourceUniqueness, so a duplicate should not occur in
// practice; the fallback exists for defense (e.g. state written by a
// pre-uniqueness-check version of this provider), matching
// reorderWorkersToMatchPrior's own defensive fallback.
func reorderDeploymentConfigsToMatchPrior(entries []cloudDeploymentComputeConfig, priorOrder []string) []cloudDeploymentComputeConfig {
	if len(entries) == 0 || len(priorOrder) == 0 {
		return entries
	}

	byName := make(map[string]cloudDeploymentComputeConfig, len(entries))
	for _, e := range entries {
		if e.CloudDeployment == "" {
			return entries
		}
		if _, dup := byName[e.CloudDeployment]; dup {
			return entries
		}
		byName[e.CloudDeployment] = e
	}

	priorSeen := make(map[string]struct{}, len(priorOrder))
	for _, name := range priorOrder {
		if _, dup := priorSeen[name]; dup {
			return entries
		}
		priorSeen[name] = struct{}{}
	}

	reordered := make([]cloudDeploymentComputeConfig, 0, len(entries))
	placed := make(map[string]struct{}, len(entries))
	for _, name := range priorOrder {
		if e, ok := byName[name]; ok {
			reordered = append(reordered, e)
			placed[name] = struct{}{}
		}
	}
	for _, e := range entries {
		if _, ok := placed[e.CloudDeployment]; !ok {
			reordered = append(reordered, e)
		}
	}

	return reordered
}

// additionalResourcesToPriorMap decodes a state additional_resources list
// into a map keyed by cloud_resource name (for splitDeploymentConfigsForRead's
// matching and for looking up each entry's own prior when flattening it back)
// and a slice of those same names in their ORIGINAL LIST ORDER, for
// reorderDeploymentConfigsToMatchPrior. An entry with an empty cloud_resource
// is skipped - not reachable for anything this provider itself wrote, since
// cloud_resource is Required in the additional_resources schema, but defends
// against a decode of a corrupted/hand-edited state file rather than
// panicking on one.
func additionalResourcesToPriorMap(ctx context.Context, list types.List, diags *diag.Diagnostics) (map[string]*AdditionalResourceModel, []string) {
	result := map[string]*AdditionalResourceModel{}
	var order []string
	if list.IsNull() || list.IsUnknown() {
		return result, order
	}

	for _, v := range list.Elements() {
		obj, ok := v.(types.Object)
		if !ok || obj.IsNull() || obj.IsUnknown() {
			continue
		}
		var model AdditionalResourceModel
		diags.Append(obj.As(ctx, &model, basetypes.ObjectAsOptions{})...)
		name := model.CloudResource.ValueString()
		if name == "" {
			continue
		}
		modelCopy := model
		result[name] = &modelCopy
		order = append(order, name)
	}

	return result, order
}

// buildAdditionalResourcesList converts the "additional" (non-primary)
// deployment_configs entries splitDeploymentConfigsForRead identified into the
// additional_resources state list, flattening each via
// apiDeploymentConfigToAdditionalResource and matching each against its own
// prior entry (by cloud_resource name, via priorByName) for
// masking/flags-carry-forward. A brand-new entry with no prior match flattens
// with priorEntry=nil, same as apiDeploymentConfigToAdditionalResource's own
// cold-start default.
func buildAdditionalResourcesList(ctx context.Context, entries []cloudDeploymentComputeConfig, priorByName map[string]*AdditionalResourceModel, forImport bool, diags *diag.Diagnostics) types.List {
	elemType := types.ObjectType{AttrTypes: additionalResourceAttrTypes()}
	if len(entries) == 0 {
		return types.ListNull(elemType)
	}

	objs := make([]attr.Value, 0, len(entries))
	for _, entry := range entries {
		prior := priorByName[entry.CloudDeployment]
		obj, objDiags := apiDeploymentConfigToAdditionalResource(ctx, entry, prior, forImport)
		diags.Append(objDiags...)
		objs = append(objs, obj)
	}

	listVal, listDiags := types.ListValue(elemType, objs)
	diags.Append(listDiags...)
	return listVal
}
