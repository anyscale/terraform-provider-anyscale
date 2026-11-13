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

// buildCloudResourceUpdateBody assembles the body object for
// PUT /api/v2/clouds/{cloud_id}/resources, per the designs in
// docs/decisions/cloud-file-storage-lifecycle/README.md and
// docs/decisions/cloud-resource-inplace-update/README.md.
//
// The model is "live record + overlay": the one owned provider-config block
// and, when it changed, object_storage are echoed from the deployment's OWN
// raw JSON (liveRaw - the raw result object of the same listing that yielded
// live) with only the changed keys overlaid. The echo must be raw, not typed:
// the backend assigns every field of a SENT block unconditionally (absent,
// null and empty all clear), so a field the typed structs drop would be
// silently cleared - kubernetes_config.custom_image_registry and
// object_storage.s3_addressing_style were lost that way. file_storage comes
// from the plan (nil clears it), networking_mode derives from isPrivateCloud
// rather than echoing the GET, and the top level carries only the frozen
// key set (the backend's CloudDeployment model rejects unknown keys).
//
// live and liveRaw must come from a listCloudResources call made during this
// same Update - not a stale or cross-deployment copy - because the GET is
// not a lossless representation of the record: aws_config.anyscale_iam_role_id
// comes back blank whenever it isn't a valid ARN, and echoing that blank
// verbatim would erase the stored credential.
//
// wantProvider/wantComputeStack/wantRegion are the values Terraform already
// has on record (from state, not plan - these three are RequiresReplace
// elsewhere in the schema, so plan and state necessarily agree on them). A
// mismatch against live means live is not describing the deployment the
// caller thinks it is, so this refuses rather than silently overwriting
// under a possibly-wrong identity.
func buildCloudResourceUpdateBody(
	live *CloudDeploymentResult,
	liveRaw map[string]any,
	planFileStorage *FileStorage,
	subnets *subnetUpdate,
	changes inPlaceChanges,
	isPrivateCloud bool,
	wantProvider, wantComputeStack, wantRegion string,
) (map[string]any, error) {
	if live.Provider != wantProvider || live.ComputeStack != wantComputeStack || live.Region != wantRegion {
		return nil, fmt.Errorf(
			"cloud resource %s: live provider/compute_stack/region (%s/%s/%s) disagree with Terraform state (%s/%s/%s); refusing to update under a possibly-wrong identity",
			live.CloudResourceID, live.Provider, live.ComputeStack, live.Region, wantProvider, wantComputeStack, wantRegion,
		)
	}

	body := map[string]any{
		"cloud_resource_id": live.CloudResourceID,
		"provider":          live.Provider,
		"compute_stack":     live.ComputeStack,
		"region":            live.Region,
		"networking_mode":   nil, // literal null on a public deployment (D2)
		"file_storage":      planFileStorage,
	}
	if isPrivateCloud {
		body["networking_mode"] = "PRIVATE"
	}

	// AWS: subnet changes replace ONLY the parallel subnet_ids/zones arrays,
	// always both together - subnet_ids without zones makes the backend
	// derive zones by positional pairing and store the WRONG pairs (live
	// testing on real AWS, 2026-11-09). The plan cannot reach here that way:
	// subnet_ids set changes are refused at plan time.
	awsChange := subnets != nil || len(changes["aws_config"]) > 0
	if awsChange {
		if live.AWSConfig == nil {
			return nil, fmt.Errorf(
				"cannot update cloud resource %s in place: the Anyscale API returned no aws_config for it, so there is no live configuration to overlay the change onto; use the Anyscale CLI to make this change instead",
				live.CloudResourceID,
			)
		}
		if live.AWSConfig.AnyscaleIAMRoleID == "" {
			return nil, fmt.Errorf(
				"cannot update cloud resource %s in place: the Anyscale API did not return this deployment's Anyscale IAM role, so echoing its AWS configuration back would erase it; use the Anyscale CLI to make this change instead",
				live.CloudResourceID,
			)
		}
		base, err := rawBlockForOverlay(liveRaw, "aws_config", live.CloudResourceID)
		if err != nil {
			return nil, err
		}
		if subnets != nil {
			base["subnet_ids"] = subnets.ids
			base["zones"] = subnets.zones
		}
		applyBlockOverlay(base, changes.block("aws_config"))
		body["aws_config"] = base
	}

	if len(changes["gcp_config"]) > 0 {
		if live.GCPConfig == nil {
			return nil, fmt.Errorf(
				"cannot update cloud resource %s in place: the Anyscale API returned no gcp_config for it, so there is no live configuration to overlay the change onto; use the Anyscale CLI to make this change instead",
				live.CloudResourceID,
			)
		}
		base, err := rawBlockForOverlay(liveRaw, "gcp_config", live.CloudResourceID)
		if err != nil {
			return nil, err
		}
		applyBlockOverlay(base, changes.block("gcp_config"))
		body["gcp_config"] = base
	}

	if len(changes["kubernetes_config"]) > 0 {
		if live.KubernetesConfig == nil {
			return nil, fmt.Errorf(
				"cannot update cloud resource %s in place: the Anyscale API returned no kubernetes_config for it, so there is no live configuration to overlay the change onto; use the Anyscale CLI to make this change instead",
				live.CloudResourceID,
			)
		}
		base, err := rawBlockForOverlay(liveRaw, "kubernetes_config", live.CloudResourceID)
		if err != nil {
			return nil, err
		}
		applyBlockOverlay(base, changes.block("kubernetes_config"))
		body["kubernetes_config"] = base
	}

	// object_storage is sent only when it changed, overlaid on the live
	// block (contract rule 1). A bare bucket-name change still carries the
	// live region/endpoint/s3_addressing_style back unchanged.
	if len(changes["object_storage"]) > 0 {
		base, _ := rawBlockForOverlay(liveRaw, "object_storage", live.CloudResourceID)
		if base == nil {
			base = map[string]any{}
		}
		applyBlockOverlay(base, changes.block("object_storage"))
		body["object_storage"] = base
	}

	return body, nil
}

// rawBlockForOverlay returns a shallow copy of the deployment's raw JSON
// block by key, for overlaying changed keys onto. A missing BLOCK is an
// error for the caller to report - except object_storage, where a live cloud
// without one is legitimate and an empty map starts it (the override's nil
// return distinguishes those cases).
func rawBlockForOverlay(liveRaw map[string]any, block, cloudResourceID string) (map[string]any, error) {
	v, ok := liveRaw[block]
	if !ok || v == nil {
		return nil, fmt.Errorf(
			"cannot update cloud resource %s in place: the Anyscale API returned no %s block to overlay the change onto; use the Anyscale CLI to make this change instead",
			cloudResourceID, block,
		)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(
			"cannot update cloud resource %s in place: the Anyscale API's %s block did not decode as a JSON object; use the Anyscale CLI to make this change instead",
			cloudResourceID, block,
		)
	}
	out := make(map[string]any, len(m)+2)
	for k, val := range m {
		out[k] = val
	}
	return out, nil
}

// applyBlockOverlay writes one block's planned changes into the echoed raw
// block. A removal DELETES the key: the backend clears a field that a sent
// block leaves out.
func applyBlockOverlay(base map[string]any, overlay map[string]inPlaceChange) {
	for key, ch := range overlay {
		switch {
		case !ch.send:
			delete(base, key)
		case ch.isStr:
			base[key] = ch.str
		default:
			base[key] = ch.list
		}
	}
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

// refuseInPlaceCloudChangeOnManagedCloud raises the Anyscale-managed
// refusal at plan time, so the practitioner sees it before an apply starts
// writing. It covers every field that updates in place: file_storage, the
// AWS subnet forms, and the eleven in-place block attributes. cloudResourceID
// may be empty or unknown on a plan that has not resolved it yet, in which
// case there is nothing to look up and the apply-time translation in
// addCloudResourceUpdateError remains the backstop.
func refuseInPlaceCloudChangeOnManagedCloud(
	ctx context.Context,
	client *Client,
	diags *diag.Diagnostics,
	cloudID, cloudResourceID string,
	planFileStorage, stateFileStorage types.Object,
	blocks inPlaceBlocks,
) {
	if client == nil || cloudID == "" || cloudResourceID == "" {
		return
	}
	if planFileStorage.IsUnknown() && blocks.planAWS.IsUnknown() &&
		blocks.planGCP.IsUnknown() && blocks.planKubernetes.IsUnknown() &&
		blocks.planObjectStorage.IsUnknown() {
		return
	}
	_, subnetsNeeded, d := plannedSubnetUpdate(ctx, blocks.planAWS, blocks.stateAWS)
	diags.Append(d...)
	if d.HasError() {
		return
	}
	changes, cd := plannedInPlaceChanges(ctx, blocks)
	diags.Append(cd...)
	if cd.HasError() {
		return
	}
	fileStorageChanged := !planFileStorage.IsUnknown() && !planFileStorage.Equal(stateFileStorage)
	if !subnetsNeeded && !changes.any() && !fileStorageChanged {
		return
	}

	results, err := listCloudResources(ctx, client, cloudID)
	if err != nil {
		// Fail open: a plan-time refusal is a convenience over the apply-time translation, so a
		// transient read failure must not block a plan that would otherwise succeed.
		return
	}
	if isAnyscaleManaged(findCloudResourceByID(results, cloudResourceID)) {
		diags.AddError("Cannot Update an Anyscale-Managed Cloud", anyscaleManagedCloudUpdateRefusal)
	}
}
