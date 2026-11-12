package provider

import (
	"context"
	"errors"
	"fmt"
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

// findCloudResourceByName returns the first deployment in results named name, or nil if none
// is.
func findCloudResourceByName(results []CloudDeploymentResult, name string) *CloudDeploymentResult {
	for i := range results {
		if results[i].Name == name {
			return &results[i]
		}
	}
	return nil
}

// buildCloudResourceUpdateRequest assembles the body for PUT /api/v2/clouds/{cloud_id}/resources
// per the design in docs/decisions/cloud-file-storage-lifecycle/README.md: round-trip the
// live deployment's own scalars and its one owned provider-config block verbatim from a fresh
// GET, derive networking_mode from isPrivateCloud rather than echoing the GET, and take
// file_storage from the plan (nil clears it).
//
// live must come from a listCloudResources call made during this same Update - not a stale or
// cross-deployment copy - because the GET is not a lossless representation of the
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
	subnets *subnetUpdate,
	isPrivateCloud bool,
	wantProvider, wantComputeStack, wantRegion string,
) (*CloudResourceUpdateRequest, error) {
	if live.Provider != wantProvider || live.ComputeStack != wantComputeStack || live.Region != wantRegion {
		return nil, fmt.Errorf(
			"cloud resource %s: live provider/compute_stack/region (%s/%s/%s) disagree with Terraform state (%s/%s/%s); refusing to update under a possibly-wrong identity",
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
						"cannot update cloud resource %s in place: the Anyscale API did not return this deployment's Anyscale IAM role, so echoing its AWS configuration back would erase it; use the Anyscale CLI to make this change instead",
						live.CloudResourceID,
					)
				}
				// Subnet changes replace ONLY the parallel subnet_ids/zones
				// arrays, and always both together - subnet_ids without
				// zones makes the backend derive zones by positional pairing
				// and store the WRONG pairs (live testing on real AWS,
				// 2026-11-09). The plan cannot reach here that way:
				// subnet_ids set changes are refused at plan time.
				req.AWSConfig = applySubnetsToLiveAWSConfig(live.AWSConfig, subnets)
			}
		case "GCP":
			req.GCPConfig = live.GCPConfig
		}
	}

	return req, nil
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

const anyscaleManagedCloudUpdateRefusal = "This change cannot be made in place on a cloud created with `anyscale cloud setup`. " +
	"The Anyscale API refuses every update to an Anyscale-managed cloud resource. Registered clouds " +
	"(`anyscale cloud register`) and Kubernetes clouds can be updated in place."

// addCloudResourceUpdateError turns a failed resources PUT into a designed diagnostic. The
// refusals the backend can raise all arrive as the same opaque 400, so they are told apart by
// their message body. It serves both the file_storage and the AWS subnet in-place updates.
//
// The substrings below are taken from backend source rather than a captured
// response (the count-mismatch message excepted: that one is verbatim from a
// real API run, 2026-11-09). So a match adds guidance and a miss
// falls through to the generic branch; neither presents backend wording as the provider's own, and
// the API's real message is always included so an unmatched refusal is still actionable.
func addCloudResourceUpdateError(diags *diag.Diagnostics, cloudResourceID string, err error) {
	var statusErr *UnexpectedStatusError
	if errors.As(err, &statusErr) {
		body := strings.ToLower(statusErr.Body)
		switch {
		case strings.Contains(body, "active clusters"):
			diags.AddError(
				"Cannot Update the Cloud While Clusters Are Running",
				fmt.Sprintf("The Anyscale API refuses to update cloud resource %s while clusters are running on it. "+
					"Terminate them and apply again, or make the change with `anyscale cloud update`.\n\nAPI response: %s",
					cloudResourceID, statusErr.Body),
			)
			return
		case strings.Contains(body, "anyscale-managed"):
			diags.AddError(
				"Cannot Update an Anyscale-Managed Cloud",
				fmt.Sprintf("%s\n\nAPI response: %s", anyscaleManagedCloudUpdateRefusal, statusErr.Body),
			)
			return
		case strings.Contains(body, "infrastructure manager") || strings.Contains(body, "infrastructure_manager_id"):
			// Unreachable if the round-trip echo is intact, since the provider never alters this
			// field. If it fires, the echo is broken - say so rather than blaming the practitioner.
			diags.AddError(
				"Cloud Resource Update Rejected an Unchanged Field",
				fmt.Sprintf("Updating cloud resource %s was rejected over the Infrastructure Manager ID, "+
					"which this provider echoes back unchanged. That points at a provider bug rather than the "+
					"configuration; please report it.\n\nAPI response: %s", cloudResourceID, statusErr.Body),
			)
			return
		case strings.Contains(statusErr.Body, "subnet IDs do not match zones"):
			// Count-mismatch 400 surfaces verbatim (real API capture,
			// 2026-11-09); the config gives the practitioner the same check
			// the backend ran.
			diags.AddError(
				"Cannot Update Subnets",
				fmt.Sprintf("The Anyscale API refused the subnet update on cloud resource %s. "+
					"Check that every entry in aws_config.subnet_ids_to_az names a subnet and a zone.\n\nAPI response: %s",
					cloudResourceID, statusErr.Body),
			)
			return
		}
	}
	AddAPIError(diags, "update cloud resource", err)
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
		// Title is operation-neutral like the apply-time translation; the
		// body stays file_storage-specific because this plan-time check only
		// fires on a file_storage change.
		diags.AddError("Cannot Update an Anyscale-Managed Cloud", anyscaleManagedFileStorageRefusal)
	}
}

// refuseSubnetIDsToAZChangeOnManagedCloud raises the same plan-time refusal for an
// aws_config.subnet_ids_to_az change: the backend refuses every update to an
// Anyscale-managed cloud, and unlike file_storage the apply-time error for a subnet
// PUT arrives as an opaque 400 - knowing before the apply starts writing is the point.
// It fires only when the plan's map is known and differs from state; a null or unknown
// plan map does not name a change to check. Fail-open refusal terms match
// refuseFileStorageChangeOnManagedCloud above.
func refuseSubnetIDsToAZChangeOnManagedCloud(
	ctx context.Context,
	client *Client,
	diags *diag.Diagnostics,
	cloudID, cloudResourceID string,
	planAWSConfig, stateAWSConfig types.Object,
) {
	if client == nil || cloudID == "" || cloudResourceID == "" {
		return
	}
	planSubnets, ok := objectMapAttr(planAWSConfig, "subnet_ids_to_az")
	if !ok || planSubnets.IsUnknown() || planSubnets.IsNull() {
		return
	}
	stateSubnets, _ := objectMapAttr(stateAWSConfig, "subnet_ids_to_az")
	if planSubnets.Equal(stateSubnets) {
		return
	}

	results, err := listCloudResources(ctx, client, cloudID)
	if err != nil {
		// Fail open: same terms as the file_storage check - a transient read failure
		// must not block a plan that would otherwise succeed.
		return
	}
	if isAnyscaleManaged(findCloudResourceByID(results, cloudResourceID)) {
		diags.AddError("Cannot Update an Anyscale-Managed Cloud", anyscaleManagedCloudUpdateRefusal)
	}
}

// objectMapAttr reads a string-map attribute out of a config block object,
// reporting false when the block is absent or the field is not a map.
func objectMapAttr(obj types.Object, name string) (types.Map, bool) {
	if obj.IsNull() || obj.IsUnknown() {
		return types.MapNull(types.StringType), false
	}
	v, ok := obj.Attributes()[name]
	if !ok {
		return types.MapNull(types.StringType), false
	}
	m, ok := v.(types.Map)
	return m, ok
}
