package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// kubernetesConfigInertFieldDeprecationMessage is shared by both resources'
// kubernetes_config.{namespace,ingress_host,cluster_name,context,
// kubeconfig_path} attributes (C5): expandKubernetesConfig only ever sends
// anyscale_operator_iam_identity/zones/redis_endpoint to the API, so these
// five have never had any effect - they're deprecated, not removed, since
// removing a schema attribute outright is a breaking change on its own
// (batched into a future major with migration notes instead; see
// CLOUD-SYNC-DESIGN.md C5).
const kubernetesConfigInertFieldDeprecationMessage = "not sent to the Anyscale API; has no effect. Will be removed in a future major release - remove from your configuration."

// cloudDeploymentIDDeprecationMessage is shared by anyscale_cloud,
// anyscale_cloud_resource, and the anyscale_cloud data source's
// cloud_deployment_id attributes: the Anyscale API marks the underlying field
// deprecated in favor of cloud_resource_id and no longer populates it. Named
// explicitly rather than assuming a sibling cloud_resource_id attribute,
// since only anyscale_cloud_resource actually has one.
const cloudDeploymentIDDeprecationMessage = "Deprecated by the Anyscale API; the backend no longer populates this field. Will be removed in a future major release - use `anyscale_cloud_resource`'s `cloud_resource_id` instead."

// azureCloudNotSupportedMessage and genericCloudNotSupportedMessage are the
// single source of truth for the AZURE/GENERIC rejection, shared between each
// resource's ValidateConfig (plan-time guard, added for K9 - catches the
// common case where the value is already known in the config) and
// buildProviderConfig's runtime check below (kept as the last line of
// defense for a value that's still unknown at plan time, e.g. interpolated
// from another resource's computed output). Both call sites must produce the
// exact same text so the two behave as one guard with two trigger points,
// not two different messages a user could see for the same limitation.
//
// The Anyscale Platform API genuinely supports AZURE (Kubernetes compute
// stack only) and GENERIC clouds - this is a provider-side gap, not a
// platform limitation. See K8S-CLOUD-CONTRACT.md K8/K9 for the disposition
// trail; the message deliberately does not promise a timeline.
const genericCloudNotSupportedMessage = "generic clouds are not yet supported by this provider"

// validateAzureK8SOnly is the plan-time guard shared by CloudResource and
// CloudResourceResource's ValidateConfig for the AZURE provider (K9/AKS):
// Anyscale does not support Azure VM clouds (confirmed against the backend's
// own validator, which rejects AZURE/GENERIC for any compute_stack other than
// K8S), and an Azure cloud's object storage bucket must be a full abfss://
// URI - unlike AWS/GCP, buildProviderConfig's AZURE case does not auto-prepend
// a scheme, so a bare bucket name would be sent to the API verbatim and
// silently fail to resolve as valid Azure storage rather than erroring
// clearly. Surfacing both checks here, at plan time, means a user seeing this
// error never gets as far as a real (and, for the compute_stack case, always
// rejected) POST /api/v2/clouds call.
func validateAzureK8SOnly(ctx context.Context, computeStack string, objectStorage types.Object) diag.Diagnostics {
	var diags diag.Diagnostics

	if computeStack != "K8S" {
		diags.AddAttributeError(
			path.Root("compute_stack"),
			"Azure Requires Kubernetes Compute Stack",
			"azure clouds only support compute_stack = \"K8S\" - Anyscale does not support Azure VM clouds. Set compute_stack = \"K8S\", or use a different cloud_provider for a VM cloud.",
		)
		return diags
	}

	if objectStorage.IsNull() || objectStorage.IsUnknown() {
		return diags
	}

	var osModel ObjectStorageModel
	asDiags := objectStorage.As(ctx, &osModel, basetypes.ObjectAsOptions{})
	diags.Append(asDiags...)
	if asDiags.HasError() || osModel.BucketName.IsNull() || osModel.BucketName.IsUnknown() {
		return diags
	}

	if bucket := osModel.BucketName.ValueString(); bucket != "" && !strings.HasPrefix(bucket, "abfss://") {
		diags.AddAttributeError(
			path.Root("object_storage").AtName("bucket_name"),
			"Azure Object Storage Requires abfss:// URI",
			fmt.Sprintf("azure object storage must be a full abfss:// URI (e.g. abfss://<container>@<account>.dfs.core.windows.net); got %q. Unlike AWS/GCP, this provider does not auto-prepend a scheme for Azure - pass the complete URI the same way you would to `anyscale cloud register --cloud-storage-bucket-name`. See https://docs.anyscale.com/reference/cli/cloud#cloud-cli for details.", bucket),
		)
	}

	return diags
}

// ResolveCloudNameToID converts a cloud name to a cloud ID by querying the Anyscale API.
// If multiple clouds have the same name, it returns the most recently created one.
//
// This function consolidates the cloud name resolution logic that was previously
// duplicated across multiple resources and data sources.
//
// Example usage:
//
//	cloudID, err := ResolveCloudNameToID(ctx, r.client, cloudName)
//	if err != nil {
//	    resp.Diagnostics.AddError(
//	        "Cloud Name Resolution Failed",
//	        fmt.Sprintf("Failed to resolve cloud name '%s' to ID: %s", cloudName, err.Error()),
//	    )
//	    return
//	}
func ResolveCloudNameToID(ctx context.Context, client *Client, cloudName string) (string, error) {
	tflog.Debug(ctx, "Resolving cloud name to ID", map[string]any{"cloud_name": cloudName})

	// Fetch all clouds
	cloudsResp, err := DoRequestAndParse[CloudsListResponse](
		ctx,
		client,
		"GET",
		"/api/v2/clouds",
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("failed to list clouds: %w", err)
	}

	// Find matching cloud(s)
	var matchedCloudID string
	var latestCreatedAt string
	matchCount := 0

	for _, cloud := range cloudsResp.Results {
		if cloud.Name == cloudName {
			matchCount++
			// Select the most recently created cloud
			if matchedCloudID == "" || cloud.CreatedAt > latestCreatedAt {
				matchedCloudID = cloud.ID
				latestCreatedAt = cloud.CreatedAt
			}
		}
	}

	if matchedCloudID == "" {
		return "", fmt.Errorf("no cloud found with name '%s'", cloudName)
	}

	// Warn if multiple clouds have the same name
	WarnIfMultipleMatches(ctx, "cloud", cloudName, matchCount, matchedCloudID)

	tflog.Info(ctx, "Resolved cloud name to ID", map[string]any{
		"cloud_name": cloudName,
		"cloud_id":   matchedCloudID,
	})

	return matchedCloudID, nil
}

// listCloudResources pages through every cloud resource (deployment) attached
// to a cloud. Both anyscale_cloud_resource and the anyscale_cloud data source
// need this same listing - centralizing it means there's exactly one place
// that paginates GET /clouds/{id}/resources instead of several copies that
// could drift (e.g. one paginating, one only reading page 1).
func listCloudResources(ctx context.Context, client *Client, cloudID string) ([]CloudDeploymentResult, error) {
	results, err := PaginatedRequest(
		ctx, client, fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), nil,
		func(body []byte) ([]CloudDeploymentResult, *string, error) {
			var deploymentsResp CloudDeploymentsResponse
			if err := json.Unmarshal(body, &deploymentsResp); err != nil {
				return nil, nil, fmt.Errorf("failed to unmarshal cloud resources: %w", err)
			}
			return deploymentsResp.Results, deploymentsResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list cloud resources: %w", err)
	}
	return results, nil
}

// findDefaultInCloudResources returns the resource flagged as the cloud's
// primary/default deployment, or nil if none is (a brand-new empty cloud has
// zero resources at all; a cloud with only non-default resources is possible
// in principle, though not through this provider).
func findDefaultInCloudResources(results []CloudDeploymentResult) *CloudDeploymentResult {
	for i := range results {
		if results[i].IsDefault {
			return &results[i]
		}
	}
	return nil
}

// buildProviderConfig populates deployReq's AWSConfig/GCPConfig/KubernetesConfig/
// ObjectStorage/FileStorage fields for the given provider and compute_stack, applying the
// same AWS/GCP x VM/K8S required-vs-optional rules that anyscale_cloud and
// anyscale_cloud_resource both need. Consolidates what was previously near-identical
// branching duplicated in resource_cloud.go's addCloudResource and
// resource_cloud_resource.go's addProviderConfig (workbench #6) - resource-agnostic, so
// both resources' Create paths call this directly rather than keeping their own copies.
//
// provider is normalized via strings.ToUpper so a lowercase/mixed-case value (e.g. "aws")
// still matches - see the case-normalization bugfix this consolidation builds on.
func buildProviderConfig(ctx context.Context, deployReq *CloudDeploymentRequest, provider, computeStack string, awsConfig, gcpConfig, azureConfig, kubernetesConfig, objectStorage, fileStorage types.Object) error {
	switch strings.ToUpper(provider) {
	case "AWS":
		if computeStack == "K8S" {
			// K8S: kubernetes_config + object_storage required, aws_config optional
			if kubernetesConfig.IsNull() {
				return fmt.Errorf("kubernetes_config is required when cloud_provider is AWS and compute_stack is K8S")
			}

			k8sConfig, err := expandKubernetesConfig(ctx, kubernetesConfig)
			if err != nil {
				return fmt.Errorf("failed to expand kubernetes_config: %w", err)
			}
			if k8sConfig == nil || k8sConfig.AnyscaleOperatorIAMIdentity == "" {
				return fmt.Errorf("kubernetes_config.anyscale_operator_iam_identity is required for AWS K8S clouds")
			}
			deployReq.KubernetesConfig = k8sConfig

			if objectStorage.IsNull() {
				return fmt.Errorf("object_storage is required when cloud_provider is AWS and compute_stack is K8S")
			}

			objStorage, err := expandObjectStorage(ctx, objectStorage)
			if err != nil {
				return fmt.Errorf("failed to expand object_storage: %w", err)
			}
			bucketName := objStorage.BucketName
			if !strings.HasPrefix(bucketName, "s3://") {
				bucketName = "s3://" + bucketName
			}
			deployReq.ObjectStorage = &ObjectStorage{
				BucketName: bucketName,
				Region:     objStorage.Region,
				Endpoint:   objStorage.Endpoint,
			}

			// aws_config is optional for K8S
			if !awsConfig.IsNull() {
				expanded, err := expandAWSConfig(ctx, awsConfig)
				if err != nil {
					return fmt.Errorf("failed to expand aws_config: %w", err)
				}
				deployReq.AWSConfig = expanded
			}

			if !fileStorage.IsNull() {
				expanded, err := expandFileStorage(ctx, fileStorage)
				if err != nil {
					return fmt.Errorf("failed to expand file_storage: %w", err)
				}
				deployReq.FileStorage = expanded
			}
		} else {
			// VM: aws_config required
			if awsConfig.IsNull() {
				return fmt.Errorf("aws_config is required when cloud_provider is AWS and compute_stack is VM")
			}

			expanded, err := expandAWSConfig(ctx, awsConfig)
			if err != nil {
				return fmt.Errorf("failed to expand aws_config: %w", err)
			}
			deployReq.AWSConfig = expanded

			// object_storage and file_storage optional
			if !objectStorage.IsNull() {
				objStorage, err := expandObjectStorage(ctx, objectStorage)
				if err != nil {
					return fmt.Errorf("failed to expand object_storage: %w", err)
				}
				bucketName := objStorage.BucketName
				if !strings.HasPrefix(bucketName, "s3://") {
					bucketName = "s3://" + bucketName
				}
				deployReq.ObjectStorage = &ObjectStorage{
					BucketName: bucketName,
					Region:     objStorage.Region,
					Endpoint:   objStorage.Endpoint,
				}
			}

			if !fileStorage.IsNull() {
				expanded, err := expandFileStorage(ctx, fileStorage)
				if err != nil {
					return fmt.Errorf("failed to expand file_storage: %w", err)
				}
				deployReq.FileStorage = expanded
			}
		}

	case "GCP":
		if computeStack == "K8S" {
			// K8S: kubernetes_config + object_storage required, gcp_config optional
			if kubernetesConfig.IsNull() {
				return fmt.Errorf("kubernetes_config is required when cloud_provider is GCP and compute_stack is K8S")
			}

			k8sConfig, err := expandKubernetesConfig(ctx, kubernetesConfig)
			if err != nil {
				return fmt.Errorf("failed to expand kubernetes_config: %w", err)
			}
			if k8sConfig == nil || k8sConfig.AnyscaleOperatorIAMIdentity == "" {
				return fmt.Errorf("kubernetes_config.anyscale_operator_iam_identity is required for GCP K8S clouds")
			}
			deployReq.KubernetesConfig = k8sConfig

			if objectStorage.IsNull() {
				return fmt.Errorf("object_storage is required when cloud_provider is GCP and compute_stack is K8S")
			}

			objStorage, err := expandObjectStorage(ctx, objectStorage)
			if err != nil {
				return fmt.Errorf("failed to expand object_storage: %w", err)
			}
			bucketName := objStorage.BucketName
			if !strings.HasPrefix(bucketName, "gs://") {
				bucketName = "gs://" + bucketName
			}
			deployReq.ObjectStorage = &ObjectStorage{
				BucketName: bucketName,
				Region:     objStorage.Region,
				Endpoint:   objStorage.Endpoint,
			}

			// gcp_config is optional for K8S
			if !gcpConfig.IsNull() {
				expanded, err := expandGCPConfig(ctx, gcpConfig)
				if err != nil {
					return fmt.Errorf("failed to expand gcp_config: %w", err)
				}
				deployReq.GCPConfig = expanded
			}

			if !fileStorage.IsNull() {
				expanded, err := expandFileStorage(ctx, fileStorage)
				if err != nil {
					return fmt.Errorf("failed to expand file_storage: %w", err)
				}
				deployReq.FileStorage = expanded
			}
		} else {
			// VM: gcp_config required
			if gcpConfig.IsNull() {
				return fmt.Errorf("gcp_config is required when cloud_provider is GCP and compute_stack is VM")
			}

			expanded, err := expandGCPConfig(ctx, gcpConfig)
			if err != nil {
				return fmt.Errorf("failed to expand gcp_config: %w", err)
			}
			deployReq.GCPConfig = expanded

			// object_storage and file_storage optional
			if !objectStorage.IsNull() {
				objStorage, err := expandObjectStorage(ctx, objectStorage)
				if err != nil {
					return fmt.Errorf("failed to expand object_storage: %w", err)
				}
				bucketName := objStorage.BucketName
				if !strings.HasPrefix(bucketName, "gs://") {
					bucketName = "gs://" + bucketName
				}
				deployReq.ObjectStorage = &ObjectStorage{
					BucketName: bucketName,
					Region:     objStorage.Region,
					Endpoint:   objStorage.Endpoint,
				}
			}

			if !fileStorage.IsNull() {
				expanded, err := expandFileStorage(ctx, fileStorage)
				if err != nil {
					return fmt.Errorf("failed to expand file_storage: %w", err)
				}
				deployReq.FileStorage = expanded
			}
		}

	case "AZURE":
		// Azure is Kubernetes-only (confirmed against the backend's own
		// validator, which rejects AZURE for any compute_stack other than
		// K8S) - this defensive check is the last line of defense behind
		// ValidateConfig's plan-time guard (validateAzureK8SOnly), which
		// catches the common case where compute_stack is already known in
		// the config; this one covers a value that was still unknown at
		// plan time (e.g. interpolated from another resource).
		if computeStack != "K8S" {
			return fmt.Errorf("azure clouds only support compute_stack = \"K8S\" - Anyscale does not support Azure VM clouds")
		}

		if kubernetesConfig.IsNull() {
			return fmt.Errorf("kubernetes_config is required when cloud_provider is AZURE")
		}

		k8sConfig, err := expandKubernetesConfig(ctx, kubernetesConfig)
		if err != nil {
			return fmt.Errorf("failed to expand kubernetes_config: %w", err)
		}
		if k8sConfig == nil || k8sConfig.AnyscaleOperatorIAMIdentity == "" {
			return fmt.Errorf("kubernetes_config.anyscale_operator_iam_identity is required for AZURE K8S clouds")
		}
		deployReq.KubernetesConfig = k8sConfig

		if objectStorage.IsNull() {
			return fmt.Errorf("object_storage is required when cloud_provider is AZURE")
		}

		objStorage, err := expandObjectStorage(ctx, objectStorage)
		if err != nil {
			return fmt.Errorf("failed to expand object_storage: %w", err)
		}
		// Azure object storage is a full abfss://container@account.dfs.core.windows.net
		// URI, never a bare bucket name - unlike AWS/GCP, there is no scheme
		// to auto-prepend here. ValidateConfig's validateAzureK8SOnly already
		// rejects a bucket_name that doesn't start with abfss:// at plan
		// time; pass it through completely verbatim.
		deployReq.ObjectStorage = &ObjectStorage{
			BucketName: objStorage.BucketName,
			Region:     objStorage.Region,
			Endpoint:   objStorage.Endpoint,
		}

		// azure_config is optional (tenant_id has no equivalent requirement
		// the way anyscale_operator_iam_identity does above).
		if !azureConfig.IsNull() {
			expanded, err := expandAzureConfig(ctx, azureConfig)
			if err != nil {
				return fmt.Errorf("failed to expand azure_config: %w", err)
			}
			deployReq.AzureConfig = expanded
		}

		if !fileStorage.IsNull() {
			expanded, err := expandFileStorage(ctx, fileStorage)
			if err != nil {
				return fmt.Errorf("failed to expand file_storage: %w", err)
			}
			deployReq.FileStorage = expanded
		}

	case "GENERIC":
		return fmt.Errorf("%s", genericCloudNotSupportedMessage)
	}

	return nil
}

// bucketSchemePrefixes are the recognized cloud-storage URI scheme prefixes
// that stripBucketPrefix (cloud_config_flatten.go) and
// bucketNameSemanticEqualPlanModifier both treat as optional/equivalent to
// their bare form - s3:// for AWS, gs:// for GCP. Azure's abfss:// is a
// compound URI (container@account.dfs.core.windows.net) with no bare-name
// equivalent, so it is deliberately excluded here and always compared
// exactly - see buildProviderConfig's AZURE case, which never prepends or
// strips a scheme for it either.
var bucketSchemePrefixes = []string{"s3://", "gs://"}

// stripAnyBucketSchemePrefix removes a recognized cloud-storage scheme
// prefix from bucketName if present, otherwise returns it unchanged.
func stripAnyBucketSchemePrefix(bucketName string) string {
	for _, prefix := range bucketSchemePrefixes {
		if stripped := strings.TrimPrefix(bucketName, prefix); stripped != bucketName {
			return stripped
		}
	}
	return bucketName
}

// bucketNameSemanticEqualPlanModifier is the fix for the object_storage.bucket_name
// import round-trip bug found during the real GKE acceptance run: bucket_name is
// RequiresReplace, but it is Optional (not Computed), so Create can never legally
// rewrite it away from exactly what the user typed - state after a fresh apply
// holds the bare or scheme-prefixed form verbatim from config. ImportState,
// though, recovers the API's own canonical form via flattenObjectStorage's
// stripBucketPrefix, which only un-prefixes AWS's s3:// and never touches GCP's
// gs:// (see its doc comment: the API always returns GCP buckets gs://-prefixed,
// by design, matching the schema's own documented convention that GCP users
// write the prefix themselves). A GCP cloud whose bucket was written bare -
// exactly what the flagship gcp-gke-basic example's module output produced -
// therefore diverges the moment it is imported: state says "my-bucket", the
// freshly-imported value says "gs://my-bucket". Same bucket, and without this
// modifier RequiresReplace would force a destroy-and-recreate of a live, working
// cloud over nothing but a scheme prefix.
//
// This deliberately does NOT canonicalize to one form (always-prepend or
// always-strip): doing so would spuriously replace existing clouds whose config
// already uses the OTHER form. Instead, when the planned config value and the
// prior state value name the same bucket (differ only by a recognized scheme
// prefix), this keeps the state value - so RequiresReplace, which runs
// afterward in the same attribute's PlanModifiers list and compares plan
// against state, sees no change at all. Must be ordered BEFORE
// stringplanmodifier.RequiresReplace() in bucket_name's PlanModifiers list to
// take effect (plan modifiers thread PlanValue from one into the next).
type bucketNameSemanticEqualPlanModifier struct{}

func (m bucketNameSemanticEqualPlanModifier) Description(ctx context.Context) string {
	return "Treats a bucket name with or without its cloud-storage scheme prefix (s3://, gs://) as the same value, so a scheme-only difference between state and config does not force replacement."
}

func (m bucketNameSemanticEqualPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m bucketNameSemanticEqualPlanModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() || req.PlanValue.IsNull() || req.PlanValue.IsUnknown() {
		return
	}
	if req.PlanValue.Equal(req.StateValue) {
		return
	}
	if stripAnyBucketSchemePrefix(req.PlanValue.ValueString()) == stripAnyBucketSchemePrefix(req.StateValue.ValueString()) {
		resp.PlanValue = req.StateValue
	}
}
