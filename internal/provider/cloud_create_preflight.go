package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// inferRegionFromAWSConfig derives a region from the availability zones in
// aws_config.subnet_ids_to_az (us-east-2a -> us-east-2). It returns "" when
// there is nothing to infer from. This is the only source the provider infers
// a region from.
func inferRegionFromAWSConfig(ctx context.Context, awsConfig types.Object) string {
	if awsConfig.IsNull() || awsConfig.IsUnknown() {
		return ""
	}
	var awsModel AWSConfigModel
	if diags := awsConfig.As(ctx, &awsModel, basetypes.ObjectAsOptions{}); diags.HasError() {
		return ""
	}
	if awsModel.SubnetIDsToAZ.IsNull() || awsModel.SubnetIDsToAZ.IsUnknown() {
		return ""
	}
	subnetMap := make(map[string]string)
	awsModel.SubnetIDsToAZ.ElementsAs(ctx, &subnetMap, false)
	for _, az := range subnetMap {
		if len(az) > 2 {
			return az[:len(az)-1]
		}
		break
	}
	return ""
}

// hasEmbeddedCloudConfig reports whether the cloud carries any embedded
// resource configuration (the all-in-one pattern). kubernetes_config counts on
// its own, since a Kubernetes cloud can be defined by it alone.
func hasEmbeddedCloudConfig(plan *CloudResourceModel) bool {
	return !plan.AWSConfig.IsNull() || !plan.GCPConfig.IsNull() || !plan.AzureConfig.IsNull() || !plan.KubernetesConfig.IsNull()
}

// detectCloudProvider mirrors the provider auto-detection Create applies when
// cloud_provider is unset: the first of aws_config, gcp_config, azure_config
// that is present, else AWS.
func detectCloudProvider(explicit string, awsConfig, gcpConfig, azureConfig types.Object) string {
	if explicit != "" {
		return explicit
	}
	switch {
	case !awsConfig.IsNull():
		return "AWS"
	case !gcpConfig.IsNull():
		return "GCP"
	case !azureConfig.IsNull():
		return "AZURE"
	}
	return "AWS"
}

// embeddedCreateRequirementError returns the first reason an all-in-one
// (embedded config) create cannot succeed, judged from local values alone, or
// ok=false when none applies. It is the single definition of those checks:
// Create runs it before POST /api/v2/clouds so a failure leaves no cloud
// behind, and ModifyPlan runs it on a create plan so the same failure shows up
// at plan time.
//
// provider, computeStack and region are the values Create would use, after
// provider detection and region inference. Callers pass "" for a value that is
// not known yet and skip the checks that depend on it.
func embeddedCreateRequirementError(ctx context.Context, data *CloudResourceModel, provider, computeStack, region string) (summary, detail string, ok bool) {
	if computeStack == "" {
		return "Missing Required Field", "compute_stack is required when using embedded config (aws_config, gcp_config, azure_config, or kubernetes_config)", true
	}
	if summary, detail, hasError := regionRequiredForCreateError(region); hasError {
		return summary, detail, true
	}

	// buildProviderConfig is pure: it only expands the blocks and enforces which of them each
	// provider and compute stack requires, so a scratch request exposes its errors without
	// calling the API.
	var scratch CloudDeploymentRequest
	if err := buildProviderConfig(ctx, &scratch, provider, computeStack, data.AWSConfig, data.GCPConfig, data.AzureConfig, data.KubernetesConfig, data.ObjectStorage, data.FileStorage); err != nil {
		return "Invalid Cloud Configuration", err.Error(), true
	}
	return "", "", false
}

// objectFullyKnown reports whether obj and everything nested in it is known.
func objectFullyKnown(ctx context.Context, obj attr.Value) bool {
	if obj.IsUnknown() {
		return false
	}
	v, err := obj.ToTerraformValue(ctx)
	if err != nil {
		return false
	}
	return v.IsFullyKnown()
}

// validateEmbeddedCreateConfig is the plan-time form of
// embeddedCreateRequirementError, run on a create plan against the raw config.
// A check whose inputs are not yet known is skipped, never failed: it still
// runs inside Create once the values resolve.
func validateEmbeddedCreateConfig(ctx context.Context, data *CloudResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics

	if !hasEmbeddedCloudConfig(data) {
		return diags // an empty cloud has no embedded config to require
	}
	if data.CloudProvider.IsUnknown() || data.ComputeStack.IsUnknown() || data.Region.IsUnknown() {
		return diags
	}
	blocks := []attr.Value{data.AWSConfig, data.GCPConfig, data.AzureConfig, data.KubernetesConfig, data.ObjectStorage, data.FileStorage}
	for _, b := range blocks {
		if !objectFullyKnown(ctx, b) {
			return diags
		}
	}

	provider := detectCloudProvider(strings.ToUpper(data.CloudProvider.ValueString()), data.AWSConfig, data.GCPConfig, data.AzureConfig)
	region := data.Region.ValueString()
	if region == "" {
		region = inferRegionFromAWSConfig(ctx, data.AWSConfig)
	}

	if summary, detail, bad := embeddedCreateRequirementError(ctx, data, provider, data.ComputeStack.ValueString(), region); bad {
		diags.AddError(summary, detail)
	}
	return diags
}
