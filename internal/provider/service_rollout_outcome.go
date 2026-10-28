package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Version tracking for anyscale_service. Design and traced wire shapes:
// docs/decisions/service-rollout-outcome/README.md.
//
// ray_serve_config is never refreshed on a normal Read (the server copy is normalized), so the
// only reliable signal that the live service no longer runs what state describes is version
// identity: every ROLLOUT apply creates a new service version, and an auto-rollback or an
// out-of-band CLI deploy/rollback leaves a different version as primary. The tracking lives in
// private state, not a user-visible attribute: it is bookkeeping for drift detection, not
// something a practitioner reads or wires elsewhere.

// serviceVersionTrackingKey is the private-state key holding serviceVersionTracking.
const serviceVersionTrackingKey = "service_version_tracking"

// serviceVersionTracking records which version state's deploy fields describe.
type serviceVersionTracking struct {
	// VersionID is the service version whose config state's deploy fields describe.
	VersionID string `json:"version_id"`
	// Pending is set while an apply's rollout has not been observed to settle (the wait timed
	// out or was cancelled). State's deploy fields then describe the pending config.
	Pending bool `json:"pending,omitempty"`
	// PendingVersionID is the version that apply created, when the apply response identified it.
	PendingVersionID string `json:"pending_version_id,omitempty"`
}

// getServiceVersionTracking returns nil when no tracking is stored: state from `terraform
// import` (before ImportState seeds it) or from a provider version that predates tracking.
func getServiceVersionTracking(ctx context.Context, p privateStateGetter) (*serviceVersionTracking, diag.Diagnostics) {
	raw, diags := p.GetKey(ctx, serviceVersionTrackingKey)
	if diags.HasError() || len(raw) == 0 {
		return nil, diags
	}
	var t serviceVersionTracking
	if err := json.Unmarshal(raw, &t); err != nil || t.VersionID == "" && !t.Pending {
		// Unreadable tracking is treated as absent and re-seeded, never as drift.
		return nil, diags
	}
	return &t, diags
}

func setServiceVersionTracking(ctx context.Context, p privateStateSetter, t *serviceVersionTracking) diag.Diagnostics {
	if t == nil {
		return p.SetKey(ctx, serviceVersionTrackingKey, nil)
	}
	raw, err := json.Marshal(t)
	if err != nil {
		var diags diag.Diagnostics
		diags.AddError("Failed to Encode Service Version Tracking", err.Error())
		return diags
	}
	return p.SetKey(ctx, serviceVersionTrackingKey, raw)
}

// serviceIsTerminated reports whether the service has been terminated (by `terraform destroy`'s
// first half, `anyscale service terminate`, or the backend auto-terminating a service whose
// primary cluster died).
func serviceIsTerminated(goalState types.String) bool {
	return goalState.ValueString() == serviceStateTerminated
}

// serviceRestartRequired is the single predicate for "the service is terminated but the
// configuration wants it running". Every terminated service in config wants to run today; a
// future argument that lets config ask for a stopped service gates it here.
func serviceRestartRequired(_ *ServiceResourceModel, state *ServiceResourceModel) bool {
	return serviceIsTerminated(state.GoalState)
}

// reconcileServiceVersionTracking decides, on Read, whether the live service still runs what
// state describes. It returns the tracking to store and whether state's deploy fields must be
// refreshed from the live primary version (drift).
//
//   - No tracking (legacy or imported state): seed it from a settled primary; never drift.
//   - Terminated service, missing primary, or a canary still rolling: leave everything alone.
//   - A pending apply whose rollout settled: success if its version (or, when the apply did not
//     identify one, any version other than the one it replaced) is primary; otherwise it was
//     rolled back and state's pending config is drift.
//   - Otherwise drift whenever the primary is not the tracked version (out-of-band deploy or
//     rollback).
func reconcileServiceVersionTracking(t *serviceVersionTracking, s *ServiceResult) (next *serviceVersionTracking, drift bool) {
	if s.GoalState == serviceStateTerminated || s.PrimaryVersion == nil || s.CanaryVersion != nil {
		return t, false
	}
	primaryID := s.PrimaryVersion.ID
	settled := &serviceVersionTracking{VersionID: primaryID}

	switch {
	case t == nil:
		return settled, false
	case t.Pending:
		if t.PendingVersionID != "" {
			return settled, primaryID != t.PendingVersionID
		}
		return settled, primaryID == t.VersionID
	default:
		return settled, primaryID != t.VersionID
	}
}

// serviceAppliedVersionID identifies the version a PUT /apply created from its 202 response:
// the canary for a rollout on a running service, the primary for a create or restart. For
// IN_PLACE the primary version is upgraded under its existing id. Returns "" when the response
// does not identify it; callers then fall back to "primary changed from preApplyPrimaryID".
func serviceAppliedVersionID(applied *ServiceResult, preApplyPrimaryID string, inPlace bool) string {
	switch {
	case inPlace:
		if applied.PrimaryVersion != nil {
			return applied.PrimaryVersion.ID
		}
		return preApplyPrimaryID
	case applied.CanaryVersion != nil:
		return applied.CanaryVersion.ID
	case applied.PrimaryVersion != nil && applied.PrimaryVersion.ID != preApplyPrimaryID:
		return applied.PrimaryVersion.ID
	default:
		return ""
	}
}

// serviceRolloutRolledBack reports whether a settled service (RUNNING, no canary) ended up on a
// version other than the one the apply created.
func serviceRolloutRolledBack(final *ServiceResult, newVersionID, preApplyPrimaryID string) bool {
	if final == nil || final.PrimaryVersion == nil {
		return false
	}
	if newVersionID != "" {
		return final.PrimaryVersion.ID != newVersionID
	}
	return preApplyPrimaryID != "" && final.PrimaryVersion.ID == preApplyPrimaryID
}

// serviceRolloutObserver remembers what the rollout wait saw: version names (the settled
// service no longer reports the failed version) and the most recent failure reason.
type serviceRolloutObserver struct {
	newVersionID string
	names        map[string]string
	reason       string
}

func newServiceRolloutObserver(newVersionID string) *serviceRolloutObserver {
	return &serviceRolloutObserver{newVersionID: newVersionID, names: map[string]string{}}
}

func (o *serviceRolloutObserver) observe(s *ServiceResult) {
	for _, v := range []*ServiceVersionResult{s.PrimaryVersion, s.CanaryVersion} {
		if v != nil && v.ID != "" {
			o.names[v.ID] = v.Version
		}
	}
	if s.ErrorMessage != nil && *s.ErrorMessage != "" {
		o.reason = *s.ErrorMessage
		return
	}
	if detail := serviceVersionChecklistFailureDetail(s.ServiceStatusChecklist, o.newVersionID); detail != "" {
		o.reason = detail
	}
}

// observeUntilRolledBack records s and reports whether the rollout is already decided as rolled
// back: the apply's version is known, no canary remains, and a different version is primary.
// Observed on a real backend: the rollback completes (canary gone, old version primary) while
// the service is still ROLLING_BACK, and the service can then report UNHEALTHY before RUNNING.
func (o *serviceRolloutObserver) observeUntilRolledBack(s *ServiceResult) bool {
	o.observe(s)
	return o.newVersionID != "" && s.CanaryVersion == nil && s.PrimaryVersion != nil &&
		s.PrimaryVersion.ID != o.newVersionID
}

// versionLabel renders `"name" (id)`, or just the id when the name was never observed.
func (o *serviceRolloutObserver) versionLabel(id string) string {
	if name := o.names[id]; name != "" {
		return fmt.Sprintf("%q (%s)", name, id)
	}
	return id
}

// serviceVersionChecklistFailureDetail is serviceChecklistFailureDetail restricted to one
// version's checklist group plus the shared items. An empty versionID scans every group.
func serviceVersionChecklistFailureDetail(checklist *ServiceStatusChecklistResult, versionID string) string {
	if checklist == nil {
		return ""
	}
	if versionID == "" {
		return serviceChecklistFailureDetail(checklist)
	}
	scoped := &ServiceStatusChecklistResult{Shared: checklist.Shared}
	for _, group := range checklist.PerVersion {
		if group.VersionID == versionID {
			scoped.PerVersion = append(scoped.PerVersion, group)
		}
	}
	return serviceChecklistFailureDetail(scoped)
}

// serviceRolledBackDetail is the diagnostic detail for a rollout the backend rolled back.
func serviceRolledBackDetail(serviceID string, o *serviceRolloutObserver, final *ServiceResult) string {
	var b strings.Builder
	newVersion := "the new version"
	if o.newVersionID != "" {
		newVersion = "new version " + o.versionLabel(o.newVersionID)
	}
	fmt.Fprintf(&b, "Anyscale rolled back the rollout of service %s: %s failed and version %s is serving again.",
		serviceID, newVersion, o.versionLabel(final.PrimaryVersion.ID))
	if o.reason != "" {
		fmt.Fprintf(&b, " Reason: %s.", strings.TrimSuffix(o.reason, "."))
	} else {
		fmt.Fprintf(&b, " The reason is in the service's ROLLING_BACK event: see the service's events in the "+
			"Anyscale console (or `anyscale service events --id %s`).", serviceID)
	}
	b.WriteString(" Terraform state keeps the previous configuration, so the next plan shows this change again " +
		"and applying it retries the rollout.")
	return b.String()
}

// serviceApplyVersionFields are the non-secret version fields of a PUT /apply response, logged
// at debug level so the response shape can be checked against a real backend.
func serviceApplyVersionFields(s *ServiceResult, preApplyPrimaryID, newVersionID string) map[string]any {
	fields := map[string]any{
		"service_id":           s.ID,
		"current_state":        s.CurrentState,
		"goal_state":           s.GoalState,
		"pre_apply_primary_id": preApplyPrimaryID,
		"new_version_id":       newVersionID,
	}
	if s.PrimaryVersion != nil {
		fields["primary_version_id"], fields["primary_version"] = s.PrimaryVersion.ID, s.PrimaryVersion.Version
	}
	if s.CanaryVersion != nil {
		fields["canary_version_id"], fields["canary_version"] = s.CanaryVersion.ID, s.CanaryVersion.Version
	}
	return fields
}

// serviceVersionIDFromObject reads primary_version.id / canary_version.id from state.
func serviceVersionIDFromObject(obj types.Object) string {
	if obj.IsNull() || obj.IsUnknown() {
		return ""
	}
	id, ok := obj.Attributes()["id"].(types.String)
	if !ok {
		return ""
	}
	return id.ValueString()
}

// serviceRayServeConfigFromVersion converts a version's server-side ray_serve_config into the
// attribute value, the same conversion ImportState uses.
func serviceRayServeConfigFromVersion(ctx context.Context, v *ServiceVersionResult) (types.Dynamic, error) {
	var rsConfigMap map[string]interface{}
	if err := json.Unmarshal(v.RayServeConfig, &rsConfigMap); err != nil {
		return types.DynamicNull(), fmt.Errorf("unmarshal ray_serve_config of version %s: %w", v.ID, err)
	}
	return InterfaceToDynamic(ctx, rsConfigMap)
}

// withPriorServiceDeployFields returns m with the deploy fields (what serviceDeployFieldsChanged
// compares) taken from prior, so a failed deploy leaves the change visible in the next plan.
func withPriorServiceDeployFields(m, prior ServiceResourceModel) ServiceResourceModel {
	m.RayServeConfig = prior.RayServeConfig
	m.BuildID = prior.BuildID
	m.ComputeConfigID = prior.ComputeConfigID
	m.ConnectionIDs = prior.ConnectionIDs
	m.Description = prior.Description
	m.QueryAuthTokenEnabled = prior.QueryAuthTokenEnabled
	return m
}
