package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// In-place AWS subnet updates (see the in-place subnets decision doc and the
// pr-subnets-inplace contract, 2026-11-09). The resources PUT
// (PUT /api/v2/clouds/{id}/resources, the same route `anyscale cloud update`
// uses) accepts subnet_ids + zones; the backend stores them,
// regrouped and zone-sorted. Sending subnet_ids WITHOUT zones makes the
// backend derive zones from AWS in describe-subnets response order and pair
// them positionally with the request's subnet order - live-proven to
// mis-pair them (Gate 1 s2, real AWS 2026-11-09). So this path only ever
// sends both arrays together, paired by the practitioner's own
// subnet_ids_to_az map; subnet_ids-only configuration can never reach it
// (its set changes are refused at plan time - see awsSubnetIDsPlanModifier).

// subnetUpdate pairs a plan's subnet_ids_to_az map into the parallel arrays
// the resources PUT carries. ids and zones are index-aligned, in the map's
// key-iteration order; the backend re-sorts by zone after storing, so array
// order here is irrelevant, only the pairing is.
type subnetUpdate struct {
	ids   []string
	zones []string
}

// plannedSubnetUpdate extracts the plan's explicit subnet pairs when the map
// form is set, and decides whether an Update PUT must carry them.
//
// needed is true when:
//   - the map differs from the state's effective map (the subnet set or any
//     AZ pairing changed), or
//   - the state has no map recorded at all but the plan does - the
//     list->map form switch. This PUTs even when the subnets are the same
//     set: a list-form cloud may hold backend-derived zones that are paired
//     with the wrong subnets, and one apply of the map writes the correct
//     pairs.
//
// A plan without the map form (subnet_ids list or neither) never PUTs
// subnets: the list form cannot name zones, and its set changes are refused
// at plan time.
func plannedSubnetUpdate(ctx context.Context, planAWS, stateAWS types.Object) (update *subnetUpdate, needed bool, d diag.Diagnostics) {
	var planModel, stateModel AWSConfigModel
	if planAWS.IsNull() || planAWS.IsUnknown() {
		return nil, false, d
	}
	d.Append(planAWS.As(ctx, &planModel, basetypes.ObjectAsOptions{})...)
	if d.HasError() {
		return nil, false, d
	}
	if planModel.SubnetIDsToAZ.IsNull() || planModel.SubnetIDsToAZ.IsUnknown() {
		return nil, false, d
	}
	var planMap map[string]string
	d.Append(planModel.SubnetIDsToAZ.ElementsAs(ctx, &planMap, false)...)
	if d.HasError() || len(planMap) == 0 {
		return nil, false, d
	}

	u := &subnetUpdate{ids: make([]string, 0, len(planMap)), zones: make([]string, 0, len(planMap))}
	for id, az := range planMap {
		u.ids = append(u.ids, id)
		u.zones = append(u.zones, az)
	}

	if !stateAWS.IsNull() && !stateAWS.IsUnknown() {
		d.Append(stateAWS.As(ctx, &stateModel, basetypes.ObjectAsOptions{})...)
		if d.HasError() {
			return nil, false, d
		}
		if !stateModel.SubnetIDsToAZ.IsNull() && !stateModel.SubnetIDsToAZ.IsUnknown() {
			var stateMap map[string]string
			d.Append(stateModel.SubnetIDsToAZ.ElementsAs(ctx, &stateMap, false)...)
			if d.HasError() {
				return nil, false, d
			}
			if mapStringStringEqual(planMap, stateMap) {
				return u, false, d
			}
		}
	}
	// A differing map, or no map in state (form switch): PUT.
	return u, true, d
}

// applySubnetsToLiveAWSConfig returns a copy of live's AWSConfig with the
// subnet arrays replaced by the plan's explicitly-paired ones. The rest is
// echoed verbatim, exactly as the file_storage update does; nothing else in
// the block changes in place.
func applySubnetsToLiveAWSConfig(live *AWSConfig, u *subnetUpdate) *AWSConfig {
	if live == nil || u == nil {
		return live
	}
	updated := *live
	updated.SubnetIDs = u.ids
	updated.Zones = u.zones
	return &updated
}

// refreshSubnetDrift rewrites ONLY the subnet attribute the state already
// uses, from the live deployment's parallel arrays. Read-only drift refresh:
//   - map form in state: the map is rebuilt from the live pairs;
//   - list form in state: the live set, in the state's element order when
//     the set is unchanged (a reorder the plan already normalizes) and in
//     the API's (zone-sorted) order when the set actually drifted;
//   - the other form is left exactly as it is - never materialized from the
//     one being refreshed.
//
// A null aws_config (empty cloud, non-AWS stack) or a live deployment with
// no aws_config returns the block untouched. This runs ONLY from Read -
// never from the Create/Update readback (the config blocks are not Read-
// refreshed there; populating one there is the C12 inconsistent-apply bug).
func refreshSubnetDrift(ctx context.Context, awsConfig types.Object, live *CloudDeploymentResult) (types.Object, diag.Diagnostics) {
	var d diag.Diagnostics
	if awsConfig.IsNull() || awsConfig.IsUnknown() || live == nil || live.AWSConfig == nil {
		return awsConfig, d
	}
	if len(live.AWSConfig.SubnetIDs) == 0 {
		// Nothing live to refresh from (or the backend cleared it); leave
		// the recorded form alone rather than writing an empty list/map.
		return awsConfig, d
	}

	var model AWSConfigModel
	d.Append(awsConfig.As(ctx, &model, basetypes.ObjectAsOptions{})...)
	if d.HasError() {
		return awsConfig, d
	}

	// Attributes() returns the underlying map; copy it before mutating.
	srcAttrs := awsConfig.Attributes()
	attrs := make(map[string]attr.Value, len(srcAttrs))
	for k, v := range srcAttrs {
		attrs[k] = v
	}

	if !model.SubnetIDsToAZ.IsNull() && !model.SubnetIDsToAZ.IsUnknown() && len(model.SubnetIDsToAZ.Elements()) > 0 {
		azByID := make(map[string]string, len(live.AWSConfig.SubnetIDs))
		if len(live.AWSConfig.SubnetIDs) == len(live.AWSConfig.Zones) {
			for i, id := range live.AWSConfig.SubnetIDs {
				azByID[id] = live.AWSConfig.Zones[i]
			}
			mapVal, md := types.MapValueFrom(ctx, types.StringType, azByID)
			d.Append(md...)
			if !d.HasError() {
				attrs["subnet_ids_to_az"] = mapVal
			}
		}
	} else if !model.SubnetIDs.IsNull() && !model.SubnetIDs.IsUnknown() && len(model.SubnetIDs.Elements()) > 0 {
		liveIDs := make([]string, len(live.AWSConfig.SubnetIDs))
		copy(liveIDs, live.AWSConfig.SubnetIDs)
		if !d.HasError() {
			var stateIDs []string
			d.Append(model.SubnetIDs.ElementsAs(ctx, &stateIDs, false)...)
			if !d.HasError() && stringSetsEqual(stateIDs, liveIDs) {
				// Same set: keep the state's element order so a reorder the
				// plan already normalizes does not resurrect as a diff.
				attrs["subnet_ids"] = model.SubnetIDs
			} else {
				listVal, ld := types.ListValueFrom(ctx, types.StringType, live.AWSConfig.SubnetIDs)
				d.Append(ld...)
				if !d.HasError() {
					attrs["subnet_ids"] = listVal
				}
			}
		}
	}

	refreshed, od := types.ObjectValue(awsConfigAttrTypes(), attrs)
	d.Append(od...)
	if d.HasError() {
		return awsConfig, d
	}
	return refreshed, d
}

func mapStringStringEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// updateCloudResourceIfChanged supersedes updateFileStorageIfChanged: it
// issues ONE resources PUT whenever file_storage OR the plan's explicit
// subnet pairs differ, carrying both in the same request (see the
// contract: one PUT carries subnet AND file_storage changes together; the
// PUT rewrites provider/compute_stack/region/networking_mode on the record,
// so an unneeded call is pure downside).
//
// Callers gate on plan-vs-state, never on drift alone: if neither value
// differs, no PUT happens even when the live record has drifted, and the
// caller keeps its state. The plan's file_storage is sent even when only
// the subnets changed (it then equals state's value - applying the
// configured value, the provider's normal semantics).
//
// Returns whether a PUT was made and, if so, the deployment's file_storage
// as the backend now holds it (the PUT response is null; values are read
// back by relisting - same caveat as the file_storage path).
func updateCloudResourceIfChanged(
	ctx context.Context,
	client *Client,
	diags *diag.Diagnostics,
	cloudID, cloudResourceID string,
	planFileStorage, stateFileStorage types.Object,
	planAWS, stateAWS types.Object,
	isPrivateCloud bool,
	wantProvider, wantComputeStack, wantRegion string,
) (updated bool, live *FileStorage) {
	subnets, subnetsNeeded, d := plannedSubnetUpdate(ctx, planAWS, stateAWS)
	diags.Append(d...)
	fileStorageChanged := !planFileStorage.Equal(stateFileStorage)
	if diags.HasError() || (!subnetsNeeded && !fileStorageChanged) {
		return false, nil
	}

	fileStorage, err := expandFileStorage(ctx, planFileStorage)
	if err != nil {
		diags.AddError("Update Error", fmt.Sprintf("failed to convert file_storage: %s", err))
		return false, nil
	}

	results, err := listCloudResources(ctx, client, cloudID)
	if err != nil {
		AddAPIError(diags, "list cloud resources before updating", err)
		return false, nil
	}
	current := findCloudResourceByID(results, cloudResourceID)
	if current == nil {
		diags.AddError("Update Error", fmt.Sprintf("cloud resource %s not found while updating", cloudResourceID))
		return false, nil
	}

	if err := updateCloudResource(ctx, client, cloudID, current, fileStorage, subnets, isPrivateCloud,
		wantProvider, wantComputeStack, wantRegion); err != nil {
		addCloudResourceUpdateError(diags, cloudResourceID, err)
		return false, nil
	}

	results, err = listCloudResources(ctx, client, cloudID)
	if err != nil {
		AddAPIError(diags, "list cloud resources after updating", err)
		return true, nil
	}
	after := findCloudResourceByID(results, cloudResourceID)
	if after == nil {
		diags.AddError("Update Error", fmt.Sprintf("cloud resource %s not found after updating", cloudResourceID))
		return true, nil
	}
	return true, after.FileStorage
}

// updateCloudResource issues the resources PUT with the request body built
// by buildCloudResourceUpdateRequest. Callers must gate on an actual
// plan-vs-state diff first.
func updateCloudResource(
	ctx context.Context,
	client *Client,
	cloudID string,
	live *CloudDeploymentResult,
	planFileStorage *FileStorage,
	subnets *subnetUpdate,
	isPrivateCloud bool,
	wantProvider, wantComputeStack, wantRegion string,
) error {
	reqBody, err := buildCloudResourceUpdateRequest(live, planFileStorage, subnets, isPrivateCloud, wantProvider, wantComputeStack, wantRegion)
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
