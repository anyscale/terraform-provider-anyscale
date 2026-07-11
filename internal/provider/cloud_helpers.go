package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/types"
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
func buildProviderConfig(ctx context.Context, deployReq *CloudDeploymentRequest, provider, computeStack string, awsConfig, gcpConfig, kubernetesConfig, objectStorage, fileStorage types.Object) error {
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
		return fmt.Errorf("azure clouds are not yet supported by this provider; azure_config cannot be applied")

	case "GENERIC":
		return fmt.Errorf("generic clouds are not yet supported by this provider")
	}

	return nil
}
