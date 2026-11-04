package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
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
// its own: aws_config/gcp_config are optional for K8S clouds (see
// addCloudResource), so a K8S cloud can be defined by kubernetes_config alone.
// Leaving it out misclassifies such a cloud as empty, so Create takes the
// empty-cloud branch, never calls addCloudResource, and the cloud rolls up to
// VM on read ("Provider produced inconsistent result after apply:
// .compute_stack: was K8S, but now VM").
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

// cloudNameLookup resolves a cloud name to the ID of the existing cloud that
// Create would adopt: ("", nil) when none exists, a *multipleCloudsWithNameError
// when several do.
type cloudNameLookup func(ctx context.Context, name string) (string, error)

// validateEmbeddedCreateConfig is the plan-time form of
// embeddedCreateRequirementError, run on a create plan against the raw config.
// A check whose inputs are not yet known is skipped, never failed: it still
// runs inside Create once the values resolve.
//
// Create adopts an existing cloud of the same name before it runs these checks,
// so a plan that would otherwise fail is exempt when that cloud exists. The
// lookup runs only when a check would fail, so a valid config costs no API
// call. A lookup that fails or finds several clouds fails the plan with the
// error Create would raise, rather than guessing that the cloud is absent.
// lookup is nil when the provider client is not configured yet (its own
// settings are unknown at plan time); the failing check is then skipped.
func validateEmbeddedCreateConfig(ctx context.Context, data *CloudResourceModel, lookup cloudNameLookup) diag.Diagnostics {
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

	summary, detail, bad := embeddedCreateRequirementError(ctx, data, provider, data.ComputeStack.ValueString(), region)
	if !bad {
		return diags
	}
	if data.Name.IsUnknown() || lookup == nil {
		return diags
	}

	existingID, err := lookup(ctx, data.Name.ValueString())
	var ambiguous *multipleCloudsWithNameError
	switch {
	case errors.As(err, &ambiguous):
		diags.AddError("Multiple Clouds Found", ambiguous.Error())
	case err != nil:
		diags.AddError("Cloud Lookup Failed",
			fmt.Sprintf("Cannot tell whether a cloud named %q already exists (Create adopts one instead of creating, "+
				"which skips this check): %s", data.Name.ValueString(), err.Error()))
	case existingID == "":
		diags.AddError(summary, detail)
	}
	return diags
}

// cloudExternalIDPrefix is the prefix POST /api/v2/clouds requires of a
// cloud-level external_id (the CreateCloud model's validator,
// backend/server/api/base/models/clouds.py).
const cloudExternalIDPrefix = "org_"

// cloudLevelExternalID returns the aws_config.external_id that Create sends as
// the cloud's own external_id: set only when it is known, non-empty, and starts
// with the organization ID, since the API refuses any other value there.
func cloudLevelExternalID(ctx context.Context, awsConfig types.Object) (string, bool) {
	externalID, known := awsConfigExternalID(ctx, awsConfig)
	if !known || !strings.HasPrefix(externalID, cloudExternalIDPrefix) {
		return "", false
	}
	return externalID, true
}

// warnCreateExternalIDNotRecorded warns on a create plan whose
// aws_config.external_id Create will not send as the cloud's own external_id
// because it does not start with the organization ID. The services load
// balancer assumes the control-plane role with the cloud's external_id, so
// services on that cloud cannot run. Clusters are unaffected, and the create
// itself behaves as before.
func warnCreateExternalIDNotRecorded(ctx context.Context, data *CloudResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	externalID, known := awsConfigExternalID(ctx, data.AWSConfig)
	if !known || externalID == "" || strings.HasPrefix(externalID, cloudExternalIDPrefix) {
		return diags
	}
	diags.AddAttributeWarning(path.Root("aws_config").AtName("external_id"),
		"External ID Not Recorded On The Cloud",
		fmt.Sprintf("aws_config.external_id %q does not start with your organization ID, so it is not recorded "+
			"on the cloud itself (the API accepts only values like %q there). Clusters still work, but services "+
			"on a new cloud cannot reach their load balancer. Use an external ID that starts with your organization "+
			"ID, in both this setting and the control-plane role's trust policy.", externalID, "org_abcd-1234"))
	return diags
}

// awsConfigExternalID returns aws_config.external_id. known is false when the
// block or the value is unknown; a null block or value is known and "".
func awsConfigExternalID(ctx context.Context, awsConfig types.Object) (externalID string, known bool) {
	if awsConfig.IsUnknown() {
		return "", false
	}
	if awsConfig.IsNull() {
		return "", true
	}
	v, ok := awsConfig.Attributes()["external_id"].(types.String)
	if !ok || v.IsUnknown() {
		return "", !ok
	}
	return v.ValueString(), true
}

// warnEmptyCloudDefaults warns, on create, about an empty VM cloud that leaves cloud_provider,
// region or credentials unset. Create then records the cloud as AWS, in us-east-1, with a
// placeholder control-plane role, and the cloud keeps those values: changing any of them later
// replaces the cloud. Anyscale creates a service's load balancer with the cloud's own
// provider, region and role, not the attached anyscale_cloud_resource's, so services on such a
// cloud fail while clusters work. Only null config values count as unset: an unknown value is
// one the practitioner set.
func warnEmptyCloudDefaults(config *CloudResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if hasEmbeddedCloudConfig(config) {
		return diags
	}
	if !config.ComputeStack.IsNull() && (config.ComputeStack.IsUnknown() || config.ComputeStack.ValueString() != "VM") {
		return diags
	}
	var unset []string
	if config.CloudProvider.IsNull() {
		unset = append(unset, "cloud_provider (defaults to AWS)")
	}
	if config.Region.IsNull() {
		unset = append(unset, "region (defaults to us-east-1)")
	}
	if config.Credentials.IsNull() {
		unset = append(unset, "credentials (a placeholder role is used)")
	}
	if len(unset) == 0 {
		return diags
	}
	diags.AddWarning("Empty Cloud Uses Placeholder Settings", fmt.Sprintf(
		"This empty cloud does not set %s. Anyscale keeps these values on the cloud, and changing any of them "+
			"later replaces the cloud. Services on this cloud cannot create load balancers, because Anyscale "+
			"creates them with the cloud's own provider, region and credentials; clusters are unaffected. Set them "+
			"to match the anyscale_cloud_resource you attach. See the split pattern section of the provider's "+
			"Cloud Resources guide.",
		strings.Join(unset, ", ")))
	return diags
}
