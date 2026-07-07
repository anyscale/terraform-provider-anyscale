package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

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
