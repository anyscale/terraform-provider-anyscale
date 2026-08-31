package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// findCloudResourceByID returns the deployment in results matching cloudResourceID, or nil if
// none does.
func findCloudResourceByID(results []CloudDeploymentResult, cloudResourceID string) *CloudDeploymentResult {
	for i := range results {
		if results[i].CloudResourceID == cloudResourceID {
			return &results[i]
		}
	}
	return nil
}

// buildCloudResourceUpdateRequest assembles the body for PUT /api/v2/clouds/{cloud_id}/resources
// per D2's frozen spec (docs/decisions/cloud-file-storage-lifecycle/README.md): round-trip the
// live deployment's own scalars and its one owned provider-config block verbatim from a fresh
// GET, derive networking_mode from isPrivateCloud rather than echoing the GET, and take
// file_storage from the plan (nil clears it).
//
// live must come from a listCloudResources call made during this same Update - not a stale or
// cross-deployment copy - because G1.7 found the GET is not a lossless representation of the
// record: aws_config.anyscale_iam_role_id comes back blank whenever it isn't a valid ARN, and
// echoing that blank verbatim would erase the stored credential.
//
// wantProvider/wantComputeStack/wantRegion are the values Terraform already has on record (from
// state, not plan - these three are RequiresReplace elsewhere in the schema, so plan and state
// necessarily agree on them). A mismatch against live means live is not describing the
// deployment the caller thinks it is, so this refuses rather than silently overwriting under a
// possibly-wrong identity.
func buildCloudResourceUpdateRequest(
	live *CloudDeploymentResult,
	planFileStorage *FileStorage,
	isPrivateCloud bool,
	wantProvider, wantComputeStack, wantRegion string,
) (*CloudResourceUpdateRequest, error) {
	if live.Provider != wantProvider || live.ComputeStack != wantComputeStack || live.Region != wantRegion {
		return nil, fmt.Errorf(
			"cloud resource %s: live provider/compute_stack/region (%s/%s/%s) disagree with Terraform state (%s/%s/%s); refusing to update file_storage under a possibly-wrong identity",
			live.CloudResourceID, live.Provider, live.ComputeStack, live.Region, wantProvider, wantComputeStack, wantRegion,
		)
	}

	req := &CloudResourceUpdateRequest{
		CloudResourceID: live.CloudResourceID,
		Provider:        live.Provider,
		ComputeStack:    live.ComputeStack,
		Region:          live.Region,
		FileStorage:     planFileStorage,
	}

	if isPrivateCloud {
		private := "PRIVATE"
		req.NetworkingMode = &private
	}

	switch live.ComputeStack {
	case "K8S":
		req.KubernetesConfig = live.KubernetesConfig
	default: // "VM"
		switch live.Provider {
		case "AWS":
			if live.AWSConfig != nil {
				if live.AWSConfig.AnyscaleIAMRoleID == "" {
					return nil, fmt.Errorf(
						"cannot update file_storage on cloud resource %s in place: the Anyscale API did not return this deployment's Anyscale IAM role, so echoing its AWS configuration back would erase it; use the Anyscale CLI to make this change instead",
						live.CloudResourceID,
					)
				}
				req.AWSConfig = live.AWSConfig
			}
		case "GCP":
			req.GCPConfig = live.GCPConfig
		}
	}

	return req, nil
}

// updateCloudResourceFileStorage issues the resources PUT built by
// buildCloudResourceUpdateRequest. Callers must gate on an actual plan-vs-state file_storage diff
// first - every resources PUT unconditionally rewrites provider/compute_stack/region/
// networking_mode on the record, so an unneeded call is pure downside, never a no-op.
//
// The response body is intentionally discarded rather than parsed into a typed struct: its shape
// was not part of what Gate 1 confirmed, and every caller re-reads the deployment via
// listCloudResources/readCloudState/readCloudResource immediately afterward regardless.
func updateCloudResourceFileStorage(
	ctx context.Context,
	client *Client,
	cloudID string,
	live *CloudDeploymentResult,
	planFileStorage *FileStorage,
	isPrivateCloud bool,
	wantProvider, wantComputeStack, wantRegion string,
) error {
	reqBody, err := buildCloudResourceUpdateRequest(live, planFileStorage, isPrivateCloud, wantProvider, wantComputeStack, wantRegion)
	if err != nil {
		return err
	}

	body, err := MarshalRequestBody([]*CloudResourceUpdateRequest{reqBody})
	if err != nil {
		return fmt.Errorf("failed to marshal cloud resource update request: %w", err)
	}

	_, err = DoRequestRaw(ctx, client, http.MethodPut, fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), body, http.StatusOK)
	return err
}

// isAnyscaleManaged reports whether the deployment was created by `anyscale cloud setup` rather
// than registered. The backend refuses every update to such a resource except adding
// memorydb/memorystore and the GCP Deployment-Manager-to-Infrastructure-Manager migration
// (_validate_anyscale_managed_resource_update), so a file_storage change is always refused.
// Provenance is visible on the live spec, which is what makes the refusal a plan-time diagnostic
// instead of an apply-time surprise.
func isAnyscaleManaged(live *CloudDeploymentResult) bool {
	if live == nil {
		return false
	}
	if live.AWSConfig != nil && live.AWSConfig.CloudFormationID != nil && *live.AWSConfig.CloudFormationID != "" {
		return true
	}
	if live.GCPConfig != nil {
		if live.GCPConfig.DeploymentManagerID != nil && *live.GCPConfig.DeploymentManagerID != "" {
			return true
		}
		if live.GCPConfig.InfrastructureManagerID != nil && *live.GCPConfig.InfrastructureManagerID != "" {
			return true
		}
	}
	return false
}

const anyscaleManagedFileStorageRefusal = "`file_storage` cannot be changed in place on a cloud created with `anyscale cloud setup`. " +
	"The Anyscale API refuses every update to an Anyscale-managed cloud resource. Registered clouds " +
	"(`anyscale cloud register`) and Kubernetes clouds can be updated in place."

// addFileStorageUpdateError turns a failed resources PUT into a designed diagnostic. The three
// refusals the backend can raise all arrive as the same opaque 400, so they are told apart by their
// message body.
//
// The substrings below are taken from backend source, NOT from a captured response - G1.3 and G1.5
// were never run (see the Gate 1 results table in
// docs/decisions/cloud-file-storage-lifecycle/README.md). So a match adds guidance and a miss falls
// through to the generic branch; neither presents backend wording as the provider's own, and the
// API's real message is always included so an unmatched refusal is still actionable.
func addFileStorageUpdateError(diags *diag.Diagnostics, cloudResourceID string, err error) {
	var statusErr *UnexpectedStatusError
	if errors.As(err, &statusErr) {
		body := strings.ToLower(statusErr.Body)
		switch {
		case strings.Contains(body, "active clusters"):
			diags.AddError(
				"Cannot Update file_storage While Clusters Are Running",
				fmt.Sprintf("The Anyscale API refuses to update cloud resource %s while clusters are running on it. "+
					"Terminate them and apply again, or make the change with `anyscale cloud update`.\n\nAPI response: %s",
					cloudResourceID, statusErr.Body),
			)
			return
		case strings.Contains(body, "anyscale-managed"):
			diags.AddError(
				"Cannot Update file_storage on an Anyscale-Managed Cloud",
				fmt.Sprintf("%s\n\nAPI response: %s", anyscaleManagedFileStorageRefusal, statusErr.Body),
			)
			return
		case strings.Contains(body, "infrastructure manager") || strings.Contains(body, "infrastructure_manager_id"):
			// Unreachable if the round-trip echo is intact, since the provider never alters this
			// field. If it fires, the echo is broken - say so rather than blaming the practitioner.
			diags.AddError(
				"Cloud Resource Update Rejected an Unchanged Field",
				fmt.Sprintf("Updating file_storage on cloud resource %s was rejected over the Infrastructure Manager ID, "+
					"which this provider echoes back unchanged. That points at a provider bug rather than the "+
					"configuration; please report it.\n\nAPI response: %s", cloudResourceID, statusErr.Body),
			)
			return
		}
	}
	AddAPIError(diags, "update cloud file_storage", err)
}

// updateFileStorageIfChanged issues the resources PUT when planFileStorage differs from
// stateFileStorage, and does nothing otherwise. Shared verbatim by anyscale_cloud and
// anyscale_cloud_resource, whose Update paths differ only in which model holds these values.
func updateFileStorageIfChanged(
	ctx context.Context,
	client *Client,
	diags *diag.Diagnostics,
	cloudID, cloudResourceID string,
	planFileStorage, stateFileStorage types.Object,
	isPrivateCloud bool,
	wantProvider, wantComputeStack, wantRegion string,
) {
	if planFileStorage.Equal(stateFileStorage) {
		return
	}

	fileStorage, err := expandFileStorage(ctx, planFileStorage)
	if err != nil {
		diags.AddError("Update Error", fmt.Sprintf("failed to convert file_storage: %s", err))
		return
	}

	results, err := listCloudResources(ctx, client, cloudID)
	if err != nil {
		AddAPIError(diags, "list cloud resources before updating file_storage", err)
		return
	}
	live := findCloudResourceByID(results, cloudResourceID)
	if live == nil {
		diags.AddError("Update Error", fmt.Sprintf("cloud resource %s not found while updating file_storage", cloudResourceID))
		return
	}

	if err := updateCloudResourceFileStorage(ctx, client, cloudID, live, fileStorage, isPrivateCloud,
		wantProvider, wantComputeStack, wantRegion); err != nil {
		addFileStorageUpdateError(diags, cloudResourceID, err)
	}
}

// refuseFileStorageChangeOnManagedCloud raises the Anyscale-managed refusal at plan time, so the
// practitioner sees it before an apply starts writing. cloudResourceID may be empty or unknown on a
// plan that has not resolved it yet, in which case there is nothing to look up and the apply-time
// translation in addFileStorageUpdateError remains the backstop.
func refuseFileStorageChangeOnManagedCloud(
	ctx context.Context,
	client *Client,
	diags *diag.Diagnostics,
	cloudID, cloudResourceID string,
	planFileStorage, stateFileStorage types.Object,
) {
	if client == nil || cloudID == "" || cloudResourceID == "" {
		return
	}
	if planFileStorage.IsUnknown() || planFileStorage.Equal(stateFileStorage) {
		return
	}

	results, err := listCloudResources(ctx, client, cloudID)
	if err != nil {
		// Fail open: a plan-time refusal is a convenience over the apply-time translation, so a
		// transient read failure must not block a plan that would otherwise succeed.
		return
	}
	if isAnyscaleManaged(findCloudResourceByID(results, cloudResourceID)) {
		diags.AddError("Cannot Update file_storage on an Anyscale-Managed Cloud", anyscaleManagedFileStorageRefusal)
	}
}
