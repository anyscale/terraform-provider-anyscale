package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// In-place updates for cloud config-block fields, beyond the file_storage
// and AWS subnet paths that shipped earlier. The design lives in
// docs/decisions/cloud-resource-inplace-update/README.md.
//
// The backend's resources PUT decodes the request into a full CloudDeployment
// and assigns every field of a SENT block unconditionally, so absent, null
// and empty all clear the stored value; only an entirely omitted BLOCK is
// preserved (cloud_deployment_model.go). Two consequences drive everything
// here:
//
//   - a removal is sent by leaving the field OUT of the block, so this file
//     never writes an explicit empty value; and
//   - the echoed live block must be LOSSLESS: a field GET returns that the
//     request omits is cleared, including fields this provider's schema and
//     structs do not model (kubernetes_config.custom_image_registry,
//     object_storage.s3_addressing_style). The PUT body is therefore built
//     from the deployment's own raw JSON, not from the typed structs.

// inPlaceChanges maps a block's wire keys to their planned value. A value
// present with send=false means REMOVAL: the key is deleted from the sent
// block (absent clears).
type inPlaceChanges map[string]map[string]inPlaceChange

// inPlaceChange is one changed in-place attribute.
type inPlaceChange struct {
	send  bool
	str   string
	list  []string
	isStr bool
}

// any reports whether at least one in-place attribute changed.
func (c inPlaceChanges) any() bool {
	for _, attrs := range c {
		if len(attrs) > 0 {
			return true
		}
	}
	return false
}

// block returns the overlay map for one block, creating it if needed.
func (c inPlaceChanges) block(block string) map[string]inPlaceChange {
	m, ok := c[block]
	if !ok {
		m = map[string]inPlaceChange{}
		c[block] = m
	}
	return m
}

// inPlaceBlocks holds the plan and state objects of the four blocks the
// in-place contract covers, so the walking functions take one argument
// instead of eight.
type inPlaceBlocks struct {
	planAWS, stateAWS                   types.Object
	planGCP, stateGCP                   types.Object
	planKubernetes, stateKubernetes     types.Object
	planObjectStorage, stateObjectStore types.Object
}

// walkable reports whether a plan/state object pair can be compared.
func walkable(plan, state types.Object) bool {
	return !plan.IsNull() && !plan.IsUnknown() && !state.IsNull() && !state.IsUnknown()
}

// plannedInPlaceChanges walks the four blocks and reports each in-place
// attribute whose plan differs from state. A block that is null or unknown
// in the plan contributes nothing: a removed BLOCK is not an in-place change
// this contract covers - the identity attributes inside it still carry
// RequiresReplace, so that plan stays a replacement (or a primary-guard
// error) exactly as before. Only plan-vs-state diffs count, never drift: a
// live record that moved without a config change is Read refresh's
// business, and must not cause a PUT.
func plannedInPlaceChanges(ctx context.Context, b inPlaceBlocks) (inPlaceChanges, diag.Diagnostics) {
	var d diag.Diagnostics
	changes := inPlaceChanges{}

	if walkable(b.planAWS, b.stateAWS) {
		var pm, sm AWSConfigModel
		d.Append(b.planAWS.As(ctx, &pm, basetypes.ObjectAsOptions{})...)
		d.Append(b.stateAWS.As(ctx, &sm, basetypes.ObjectAsOptions{})...)
		if !d.HasError() {
			attrs := changes.block("aws_config")
			stringChange(attrs, planString(pm.VPCID, sm.VPCID), "vpc_id")
			stringChange(attrs, planString(pm.DataplaneIAMRoleARN, sm.DataplaneIAMRoleARN), "cluster_iam_role_id")
			stringChange(attrs, planString(pm.ClusterInstanceProfileID, sm.ClusterInstanceProfileID), "cluster_instance_profile_id")
			listChange(ctx, attrs, pm.SecurityGroupIDs, sm.SecurityGroupIDs, "security_group_ids", &d)
		}
	}
	if walkable(b.planGCP, b.stateGCP) {
		var pm, sm GCPConfigModel
		d.Append(b.planGCP.As(ctx, &pm, basetypes.ObjectAsOptions{})...)
		d.Append(b.stateGCP.As(ctx, &sm, basetypes.ObjectAsOptions{})...)
		if !d.HasError() {
			attrs := changes.block("gcp_config")
			stringChange(attrs, planString(pm.DataplaneServiceAccountEmail, sm.DataplaneServiceAccountEmail), "cluster_service_account_email")
			listChange(ctx, attrs, pm.SubnetNames, sm.SubnetNames, "subnet_names", &d)
			listChange(ctx, attrs, pm.FirewallPolicyNames, sm.FirewallPolicyNames, "firewall_policy_names", &d)
		}
	}

	if walkable(b.planKubernetes, b.stateKubernetes) {
		var pm, sm KubernetesConfigModel
		d.Append(b.planKubernetes.As(ctx, &pm, basetypes.ObjectAsOptions{})...)
		d.Append(b.stateKubernetes.As(ctx, &sm, basetypes.ObjectAsOptions{})...)
		if !d.HasError() {
			attrs := changes.block("kubernetes_config")
			stringChange(attrs, planString(pm.AnyscaleOperatorIAMIdentity, sm.AnyscaleOperatorIAMIdentity), "anyscale_operator_iam_identity")
			stringChange(attrs, planString(pm.RedisEndpoint, sm.RedisEndpoint), "redis_endpoint")
			listChange(ctx, attrs, pm.Zones, sm.Zones, "zones", &d)
		}
	}
	if walkable(b.planObjectStorage, b.stateObjectStore) {
		var pm, sm ObjectStorageModel
		d.Append(b.planObjectStorage.As(ctx, &pm, basetypes.ObjectAsOptions{})...)
		d.Append(b.stateObjectStore.As(ctx, &sm, basetypes.ObjectAsOptions{})...)
		if !d.HasError() {
			attrs := changes.block("object_storage")
			stringChange(attrs, planString(pm.BucketName, sm.BucketName), "bucket_name")
			stringChange(attrs, planString(pm.Region, sm.Region), "region")
			stringChange(attrs, planString(pm.Endpoint, sm.Endpoint), "endpoint")
		}
	}

	// Blocks with no changed attributes must not appear in the overlay.
	for name, attrs := range changes {
		if len(attrs) == 0 {
			delete(changes, name)
		}
	}
	return changes, d
}

// stringChange records a changed scalar under its wire key.
func stringChange(b map[string]inPlaceChange, ch *stringChangeResult, api string) {
	if ch == nil || !ch.changed {
		return
	}
	if ch.removed {
		b[api] = inPlaceChange{}
		return
	}
	b[api] = inPlaceChange{send: true, str: ch.value, isStr: true}
}

// listChange records a changed list under its wire key: send the plan's
// order, or a removal when the plan is null. The Read refresh keeps a
// same-set state order, so a pure reorder by the practitioner still lands
// here, and an out-of-band reorder never does.
func listChange(ctx context.Context, b map[string]inPlaceChange, plan, state types.List, api string, d *diag.Diagnostics) {
	if plan.Equal(state) {
		return
	}
	if plan.IsNull() {
		b[api] = inPlaceChange{}
		return
	}
	var items []string
	d.Append(plan.ElementsAs(ctx, &items, false)...)
	if d.HasError() {
		return
	}
	b[api] = inPlaceChange{send: true, list: items}
}

// stringChangeResult is what planString computes for one scalar.
type stringChangeResult struct {
	changed bool
	removed bool
	value   string
}

// planString diffs one scalar pair.
func planString(plan, state types.String) *stringChangeResult {
	if plan.Equal(state) {
		return nil
	}
	if plan.IsNull() {
		return &stringChangeResult{changed: true, removed: true}
	}
	return &stringChangeResult{changed: true, value: plan.ValueString()}
}

// refreshStateString refreshes ONE non-null state attribute from a live
// value. A null (unmanaged) state attribute is never adopted from the live
// record; a non-null one follows the live value, including back down to
// null when the live record cleared it - that is the drift the next apply
// reverts.
func refreshStateString(attrs map[string]attr.Value, name string, state types.String, live string, liveSet bool) {
	if state.IsNull() || state.IsUnknown() {
		return
	}
	if !liveSet {
		attrs[name] = types.StringNull()
		return
	}
	attrs[name] = types.StringValue(live)
}

// refreshStateList refreshes one non-null state list from a live list. A
// same-set live list keeps the STATE's element order (an out-of-band reorder
// never becomes a diff); a changed set takes the live order.
func refreshStateList(ctx context.Context, attrs map[string]attr.Value, name string, state types.List, live []string, d *diag.Diagnostics) {
	if state.IsNull() || state.IsUnknown() {
		return
	}
	var stateItems []string
	d.Append(state.ElementsAs(ctx, &stateItems, false)...)
	if d.HasError() {
		return
	}
	if stringSetsEqual(stateItems, live) {
		return // keep the state's order
	}
	lv, ld := stringListOrNull(ctx, live)
	d.Append(ld...)
	attrs[name] = lv
}

// refreshStateStringPtr is refreshStateString for live fields the API types
// as a pointer (absent vs. empty is the null-vs-value distinction).
func refreshStateStringPtr(attrs map[string]attr.Value, name string, state types.String, live *string) {
	if live == nil {
		refreshStateString(attrs, name, state, "", false)
		return
	}
	refreshStateString(attrs, name, state, *live, true)
}

// refreshInPlaceAttributes refreshes the in-place attributes of the four
// blocks from the live deployment, in Read ONLY - never in the Create/Update
// readback, where the config blocks are deliberately not refreshed (the C12
// inconsistent-apply rule). Only state blocks that are non-null are touched.
func refreshInPlaceAttributes(ctx context.Context, b inPlaceBlocks, live *CloudDeploymentResult) (inPlaceBlocks, diag.Diagnostics) {
	var d diag.Diagnostics
	out := b

	if live != nil && live.AWSConfig != nil && !b.stateAWS.IsNull() && !b.stateAWS.IsUnknown() {
		var sm AWSConfigModel
		d.Append(b.stateAWS.As(ctx, &sm, basetypes.ObjectAsOptions{})...)
		if !d.HasError() {
			attrs := map[string]attr.Value{}
			for k, v := range b.stateAWS.Attributes() {
				attrs[k] = v
			}
			refreshStateString(attrs, "vpc_id", sm.VPCID, live.AWSConfig.VPCID, live.AWSConfig.VPCID != "")
			refreshStateList(ctx, attrs, "security_group_ids", sm.SecurityGroupIDs, live.AWSConfig.SecurityGroupIDs, &d)
			refreshStateString(attrs, "dataplane_iam_role_arn", sm.DataplaneIAMRoleARN, live.AWSConfig.ClusterIAMRoleID, live.AWSConfig.ClusterIAMRoleID != "")
			refreshStateStringPtr(attrs, "cluster_instance_profile_id", sm.ClusterInstanceProfileID, live.AWSConfig.ClusterInstanceProfileID)
			obj, od := types.ObjectValue(awsConfigAttrTypes(), attrs)
			d.Append(od...)
			if !d.HasError() {
				out.stateAWS = obj
			}
		}
	}
	if live != nil && live.GCPConfig != nil && !b.stateGCP.IsNull() && !b.stateGCP.IsUnknown() {
		var sm GCPConfigModel
		d.Append(b.stateGCP.As(ctx, &sm, basetypes.ObjectAsOptions{})...)
		if !d.HasError() {
			attrs := map[string]attr.Value{}
			for k, v := range b.stateGCP.Attributes() {
				attrs[k] = v
			}
			refreshStateList(ctx, attrs, "subnet_names", sm.SubnetNames, live.GCPConfig.SubnetNames, &d)
			refreshStateList(ctx, attrs, "firewall_policy_names", sm.FirewallPolicyNames, live.GCPConfig.FirewallPolicyNames, &d)
			refreshStateString(attrs, "dataplane_service_account_email", sm.DataplaneServiceAccountEmail, live.GCPConfig.ClusterServiceAccountEmail, live.GCPConfig.ClusterServiceAccountEmail != "")
			obj, od := types.ObjectValue(gcpConfigAttrTypes(), attrs)
			d.Append(od...)
			if !d.HasError() {
				out.stateGCP = obj
			}
		}
	}
	if live != nil && live.KubernetesConfig != nil && !b.stateKubernetes.IsNull() && !b.stateKubernetes.IsUnknown() {
		var sm KubernetesConfigModel
		d.Append(b.stateKubernetes.As(ctx, &sm, basetypes.ObjectAsOptions{})...)
		if !d.HasError() {
			attrs := map[string]attr.Value{}
			for k, v := range b.stateKubernetes.Attributes() {
				attrs[k] = v
			}
			refreshStateString(attrs, "anyscale_operator_iam_identity", sm.AnyscaleOperatorIAMIdentity, live.KubernetesConfig.AnyscaleOperatorIAMIdentity, live.KubernetesConfig.AnyscaleOperatorIAMIdentity != "")
			refreshStateString(attrs, "redis_endpoint", sm.RedisEndpoint, live.KubernetesConfig.RedisEndpoint, live.KubernetesConfig.RedisEndpoint != "")
			refreshStateList(ctx, attrs, "zones", sm.Zones, live.KubernetesConfig.Zones, &d)
			obj, od := types.ObjectValue(kubernetesConfigAttrTypes(), attrs)
			d.Append(od...)
			if !d.HasError() {
				out.stateKubernetes = obj
			}
		}
	}
	if live != nil && live.ObjectStorage != nil && !b.stateObjectStore.IsNull() && !b.stateObjectStore.IsUnknown() {
		var sm ObjectStorageModel
		d.Append(b.stateObjectStore.As(ctx, &sm, basetypes.ObjectAsOptions{})...)
		if !d.HasError() {
			attrs := map[string]attr.Value{}
			for k, v := range b.stateObjectStore.Attributes() {
				attrs[k] = v
			}
			// bucket_name: a scheme-only difference (s3://, gs://) is not a
			// drift - keep the state's own representation, matching
			// bucketNameSemanticEqualPlanModifier on the plan side.
			if sm.BucketName.IsNull() || sm.BucketName.IsUnknown() || live.ObjectStorage.BucketName == "" ||
				stripAnyBucketSchemePrefix(sm.BucketName.ValueString()) != stripAnyBucketSchemePrefix(live.ObjectStorage.BucketName) {
				refreshStateString(attrs, "bucket_name", sm.BucketName, live.ObjectStorage.BucketName, live.ObjectStorage.BucketName != "")
			}
			// region: the backend reconciles region-or-null against the
			// deployment's own region, so a NULL live region means "unset
			// or equal". The effective live region is the deployment's own
			// region - refresh against that, or a config naming its own
			// region re-diffs forever (criterion 12, contract 23d6bb9).
			effectiveRegion := live.Region
			if live.ObjectStorage.Region != nil {
				effectiveRegion = *live.ObjectStorage.Region
			}
			refreshStateString(attrs, "region", sm.Region, effectiveRegion, true)
			refreshStateStringPtr(attrs, "endpoint", sm.Endpoint, live.ObjectStorage.Endpoint)
			obj, od := types.ObjectValue(objectStorageAttrTypes(), attrs)
			d.Append(od...)
			if !d.HasError() {
				out.stateObjectStore = obj
			}
		}
	}
	return out, d
}

// copyPlannedInPlaceValuesIntoState carries the planned in-place values into
// the state objects after a successful PUT. The anyscale_cloud_resource
// Update's readback deliberately leaves the config blocks untouched (the C12
// rule), so without this, state keeps the pre-update values and Terraform
// rejects the apply as inconsistent. The anyscale_cloud Update does not need
// this: it records the PLAN object itself, which already carries the new
// values.
//
// Only CHANGED attributes are copied; a block with no in-place change is
// left exactly as the caller passed it, so an unchanged state block (e.g.
// the imported subnet form) is never swapped in over the planned one.
func copyPlannedInPlaceValuesIntoState(ctx context.Context, b inPlaceBlocks) (carried inPlaceBlocks, d diag.Diagnostics) {
	carried = b
	changes, d := plannedInPlaceChanges(ctx, b)
	if d.HasError() {
		return carried, d
	}

	copyBlock := func(block, planKey, stateKey string) {
		changedAttrs, ok := changes[block]
		plan, state := b.planAWS, b.stateAWS
		switch block {
		case "gcp_config":
			plan, state = b.planGCP, b.stateGCP
		case "kubernetes_config":
			plan, state = b.planKubernetes, b.stateKubernetes
		case "object_storage":
			plan, state = b.planObjectStorage, b.stateObjectStore
		}
		_ = planKey
		_ = stateKey
		if !ok || plan.IsNull() || plan.IsUnknown() || state.IsNull() || state.IsUnknown() {
			return
		}
		merged := map[string]attr.Value{}
		for k, v := range state.Attributes() {
			merged[k] = v
		}
		planAttrs := plan.Attributes()
		for apiKey := range changedAttrs {
			name := apiKeyToAttrName(block, apiKey)
			if name == "" {
				continue
			}
			merged[name] = planAttrs[name]
		}
		obj, od := types.ObjectValue(blockAttrTypes[block](), merged)
		d.Append(od...)
		if !od.HasError() {
			setCarriedBlock(&carried, block, obj)
		}
	}
	copyBlock("aws_config", "", "")
	copyBlock("gcp_config", "", "")
	copyBlock("kubernetes_config", "", "")
	copyBlock("object_storage", "", "")
	return carried, d
}

// blockAttrTypes maps each in-place block to its attribute-type constructor,
// the same helpers the flatten path uses to rebuild block objects.
var blockAttrTypes = map[string]func() map[string]attr.Type{
	"aws_config":        awsConfigAttrTypes,
	"gcp_config":        gcpConfigAttrTypes,
	"kubernetes_config": kubernetesConfigAttrTypes,
	"object_storage":    objectStorageAttrTypes,
}

// attrNameByAPI maps a block's wire key back to the Terraform attribute
// name for the changed-value copy.
var attrNameByAPI = map[string]map[string]string{
	"aws_config": {
		"vpc_id":                      "vpc_id",
		"security_group_ids":          "security_group_ids",
		"cluster_iam_role_id":         "dataplane_iam_role_arn",
		"cluster_instance_profile_id": "cluster_instance_profile_id",
	},
	"gcp_config": {
		"subnet_names":                  "subnet_names",
		"firewall_policy_names":         "firewall_policy_names",
		"cluster_service_account_email": "dataplane_service_account_email",
	},
	"kubernetes_config": {
		"zones":                          "zones",
		"anyscale_operator_iam_identity": "anyscale_operator_iam_identity",
		"redis_endpoint":                 "redis_endpoint",
	},
	"object_storage": {
		"bucket_name": "bucket_name",
		"region":      "region",
		"endpoint":    "endpoint",
	},
}

func apiKeyToAttrName(block, api string) string {
	return attrNameByAPI[block][api]
}

func setCarriedBlock(carried *inPlaceBlocks, block string, obj types.Object) {
	switch block {
	case "aws_config":
		carried.stateAWS = obj
	case "gcp_config":
		carried.stateGCP = obj
	case "kubernetes_config":
		carried.stateKubernetes = obj
	case "object_storage":
		carried.stateObjectStore = obj
	}
}

// subnetIDsChanged reports whether either AWS subnet form differs between
// plan and state. Both blocks are already known walkable here.
func subnetIDsChanged(ctx context.Context, plan, state types.Object) (bool, diag.Diagnostics) {
	var d diag.Diagnostics
	var pm, sm AWSConfigModel
	d.Append(plan.As(ctx, &pm, basetypes.ObjectAsOptions{})...)
	d.Append(state.As(ctx, &sm, basetypes.ObjectAsOptions{})...)
	if d.HasError() {
		return false, d
	}
	return !pm.SubnetIDs.Equal(sm.SubnetIDs) || !pm.SubnetIDsToAZ.Equal(sm.SubnetIDsToAZ), d
}

// refuseUnsupportedInPlaceChanges raises the contract's plan-time errors:
//   - a vpc_id change with no subnet change (either form): subnets belong to
//     one VPC, so such a config cannot be right and the provider cannot
//     verify membership to fix it;
//   - removing vpc_id (the VM stack needs one);
//   - removing anyscale_operator_iam_identity (K8S needs it);
//   - removing the whole kubernetes_config or object_storage block: every
//     attribute in both updates in place now, so a null block would
//     otherwise be a silently skipped update - a perpetual diff. (Removing
//     aws_config/gcp_config/azure_config keeps planning replace via their
//     identity attributes.)
//
// No API calls - pure plan/state comparison, so it cannot fail open.
func refuseUnsupportedInPlaceChanges(ctx context.Context, b inPlaceBlocks, diags *diag.Diagnostics) {
	if walkable(b.planAWS, b.stateAWS) {
		var pm, sm AWSConfigModel
		var d diag.Diagnostics
		d.Append(b.planAWS.As(ctx, &pm, basetypes.ObjectAsOptions{})...)
		d.Append(b.stateAWS.As(ctx, &sm, basetypes.ObjectAsOptions{})...)
		if d.HasError() {
			diags.Append(d...)
		} else {
			vpcDiffers := !pm.VPCID.Equal(sm.VPCID)
			switch {
			case vpcDiffers && pm.VPCID.IsNull():
				diags.AddError(
					"Cannot Remove vpc_id",
					"aws_config.vpc_id cannot be removed: an AWS VM cloud resource needs a VPC. "+
						"To move the resource to another VPC, change vpc_id together with its subnets in config, or recreate the resource out of band and re-import it.",
				)
			case vpcDiffers:
				subnetsChanged, sd := subnetIDsChanged(ctx, b.planAWS, b.stateAWS)
				if sd.HasError() {
					diags.Append(sd...)
				} else if !subnetsChanged {
					diags.AddError(
						"vpc_id Changed Without Its Subnets",
						"A vpc_id change with no change to aws_config.subnet_ids or aws_config.subnet_ids_to_az cannot be right: the subnets belong to one VPC. "+
							"Change the VPC together with the subnets that live in it. The provider does not verify VPC membership; if the subnets really did move VPCs out of band, change a subnet's zone in subnet_ids_to_az to signal it.",
					)
				}
			}
		}
	}

	if walkable(b.planGCP, b.stateGCP) {
		var pm, sm GCPConfigModel
		var d diag.Diagnostics
		d.Append(b.planGCP.As(ctx, &pm, basetypes.ObjectAsOptions{})...)
		d.Append(b.stateGCP.As(ctx, &sm, basetypes.ObjectAsOptions{})...)
		if d.HasError() {
			diags.Append(d...)
		} else if pm.SubnetNames.IsNull() && !sm.SubnetNames.IsNull() {
			// The backend KEEPS the old subnets when a sent block carries an
			// empty subnet list (the same exception class as vpc_id), so a
			// plan 'subnet_names -> null' would silently change nothing and
			// never converge.
			diags.AddError(
				"Cannot Remove subnet_names",
				"gcp_config.subnet_names cannot be removed: a GCP VM cloud resource needs its subnets, and the Anyscale API keeps the old subnet list when it is sent empty, so the removal would never take effect. "+
					"To move the resource to other subnets, change the list in config.",
			)
		}
	}

	if walkable(b.planKubernetes, b.stateKubernetes) {
		var pm, sm KubernetesConfigModel
		var d diag.Diagnostics
		d.Append(b.planKubernetes.As(ctx, &pm, basetypes.ObjectAsOptions{})...)
		d.Append(b.stateKubernetes.As(ctx, &sm, basetypes.ObjectAsOptions{})...)
		if d.HasError() {
			diags.Append(d...)
		} else if pm.AnyscaleOperatorIAMIdentity.IsNull() && !sm.AnyscaleOperatorIAMIdentity.IsNull() {
			diags.AddError(
				"Cannot Remove anyscale_operator_iam_identity",
				"kubernetes_config.anyscale_operator_iam_identity cannot be removed: an Anyscale Operator must be installed for a Kubernetes cloud. "+
					"Set the role (AWS) or service-account email (GCP, Azure) to keep the cloud working.",
			)
		}
	} else {
		blockRemoved("kubernetes_config", b.planKubernetes, b.stateKubernetes, diags)
	}

	if !walkable(b.planObjectStorage, b.stateObjectStore) {
		blockRemoved("object_storage", b.planObjectStorage, b.stateObjectStore, diags)
	}
}

// blockRemoved raises the whole-block removal error for the two blocks
// without an identity attribute. A null STATE block means the plan creates
// the block, which is fine; an UNKNOWN state block means there is nothing
// decided yet to compare against.
func blockRemoved(name string, plan, state types.Object, diags *diag.Diagnostics) {
	if state.IsNull() || state.IsUnknown() || plan.IsUnknown() || !plan.IsNull() {
		return
	}
	diags.AddError(
		"Cannot Remove the "+name+" Block",
		"The block cannot be removed: every attribute in it now updates in place and the Anyscale API preserves an omitted block, so a removed block would never converge. "+
			"Keep the block in config; clear it field by field instead if that is what you want (the API clears omitted fields). To recreate the resource, use `terraform state rm` and import after recreating it by hand.",
	)
}
