package provider

import (
	"context"
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
