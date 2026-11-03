package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// canary_percent: a deploy can hold the new version at a manual traffic share instead of rolling
// it to 100%. See docs/decisions/service-canary-percent.

// serviceCanaryCutover is the canary_percent that moves all traffic to the canary at once. The
// rollout then completes only where Anyscale auto-completes manual rollouts, so the wait for it is
// the completion wait, not the hold wait.
const serviceCanaryCutover = 100

// serviceHoldPercent returns the share to hold a deploy's new version at, and false when the
// rollout should complete (canary_percent null, or the 100 cutover).
func serviceHoldPercent(plan *ServiceResourceModel) (int64, bool) {
	if plan.CanaryPercent.IsNull() || plan.CanaryPercent.IsUnknown() || plan.CanaryPercent.ValueInt64() == serviceCanaryCutover {
		return 0, false
	}
	return plan.CanaryPercent.ValueInt64(), true
}

// serviceHeldCanaryID returns the id of the canary this resource holds, or "": the canary in
// state, when tracking records it as held.
func serviceHeldCanaryID(state *ServiceResourceModel, tracking *serviceVersionTracking) string {
	if tracking == nil || tracking.HeldVersionID == "" || !serviceCanaryHeld(state) {
		return ""
	}
	if serviceVersionIDFromObject(state.CanaryVersion) != tracking.HeldVersionID {
		return ""
	}
	return tracking.HeldVersionID
}

// serviceApplyFingerprint identifies the config an apply body deploys, ignoring the fields that
// do not select a version (its name and the traffic share). Anyscale matches versions by a hash of
// their config, so two bodies with the same fingerprint deploy the same version.
func serviceApplyFingerprint(body *applyServiceRequest) (string, error) {
	b := *body
	b.Version = nil
	b.CanaryPercent = nil
	raw, err := json.Marshal(&b)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// rollbackService rolls back a service's in-flight rollout: the canary is removed and the primary
// keeps all traffic.
func rollbackService(ctx context.Context, client *Client, serviceID string) error {
	tflog.Info(ctx, "Rolling back the held canary", map[string]any{"service_id": serviceID})
	_, err := DoRequestRaw(ctx, client, "POST", fmt.Sprintf("/api/v2/services-v2/%s/rollback", serviceID), nil,
		http.StatusOK, http.StatusAccepted)
	return err
}

// serviceHoldSettled reports whether a held canary is serving its share: the canary is healthy
// (its version and every checklist row for it RUNNING) and its settled share, current_weight,
// equals both its target and the planned percent. current_weight is null on the first polls after
// an apply, and null is not settled even at 0.
func serviceHoldSettled(s *ServiceResult, percent int64) bool {
	c := s.CanaryVersion
	if c == nil || c.CurrentState != serviceStateRunning || c.CurrentWeight == nil || c.TargetWeight == nil ||
		*c.CurrentWeight != percent || *c.TargetWeight != percent {
		return false
	}
	if s.ServiceStatusChecklist != nil {
		for _, group := range s.ServiceStatusChecklist.PerVersion {
			if group.VersionID != c.ID {
				continue
			}
			for _, item := range group.Items {
				if item.State != serviceStateRunning {
					return false
				}
			}
		}
	}
	return true
}

// waitForServiceHold waits for the canary canaryID to serve percent of traffic. It fails when the
// service is terminated or the canary is gone (rolled back or completed outside this apply), and
// on timeout.
func waitForServiceHold(ctx context.Context, client *Client, serviceID, canaryID string, percent int64, timeout time.Duration) (*ServiceResult, error) {
	return waitForServiceHoldWithTiming(ctx, client, serviceID, canaryID, percent, timeout, defaultServiceRolloutPollInterval)
}

func waitForServiceHoldWithTiming(ctx context.Context, client *Client, serviceID, canaryID string, percent int64, timeout, interval time.Duration) (*ServiceResult, error) {
	deadline := time.Now().Add(timeout)
	for {
		service, err := getServiceByID(ctx, client, serviceID)
		if err != nil {
			return nil, err
		}
		switch {
		case service.GoalState == serviceStateTerminated:
			return service, fmt.Errorf("service was terminated (current_state %s) while waiting for its canary to hold %d%% of traffic",
				service.CurrentState, percent)
		case service.CanaryVersion == nil || (canaryID != "" && service.CanaryVersion.ID != canaryID):
			return service, fmt.Errorf("the canary version %s is no longer present (current_state %s): it was rolled back or completed outside this apply",
				canaryID, service.CurrentState)
		case serviceHoldSettled(service, percent):
			return service, nil
		case serviceErrorStates[service.CanaryVersion.CurrentState]:
			return service, fmt.Errorf("%w: canary %s is %s", errServiceCanaryFailed, service.CanaryVersion.ID, service.CanaryVersion.CurrentState)
		}
		if time.Now().After(deadline) {
			c := service.CanaryVersion
			return service, fmt.Errorf("timed out after %s waiting for canary %s to hold %d%% of traffic (canary %s, current_weight %s, target_weight %s)",
				timeout, c.ID, percent, c.CurrentState, formatWeight(c.CurrentWeight), formatWeight(c.TargetWeight))
		}
		select {
		case <-ctx.Done():
			return service, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func formatWeight(w *int64) string {
	if w == nil {
		return "null"
	}
	return fmt.Sprint(*w)
}

// waitForHeldCanary finishes an apply that holds canaryID at percent: it waits for the share to
// settle and records the result in state. Tracking (pending, held) was saved before the wait.
func (r *ServiceResource) waitForHeldCanary(ctx context.Context, plan, prior *ServiceResourceModel, serviceID, canaryID string, percent int64, timeout time.Duration, resp *resource.UpdateResponse) {
	final, err := waitForServiceHold(ctx, r.client, serviceID, canaryID, percent, timeout)
	if errors.Is(err, errServiceCanaryFailed) {
		r.rollBackFailedCanary(ctx, plan, prior, canaryID, final, timeout, resp)
		return
	}
	if final != nil {
		resp.Diagnostics.Append(populateServiceResourceModelComputed(ctx, plan, final)...)
		refreshServiceTagsIntoModel(ctx, r.client, serviceID, &plan.Tags)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
	if err != nil {
		AddAPIError(&resp.Diagnostics, "wait for canary to hold its traffic share", err)
	}
}

// rollBackHeldCanaryForDeploy rolls back the held canary before a deploy of new config, and
// reports whether the apply is finished: when the planned config is the one deployed before the
// hold, the rollback already restored it, and re-sending it would start a new version.
func (r *ServiceResource) rollBackHeldCanaryForDeploy(ctx context.Context, plan *ServiceResourceModel, applyBody *applyServiceRequest,
	tracking *serviceVersionTracking, timeout time.Duration, resp *resource.UpdateResponse) bool {
	serviceID := plan.ID.ValueString()
	if err := rollbackService(ctx, r.client, serviceID); err != nil {
		AddAPIError(&resp.Diagnostics, "roll back the held canary", err)
		return true
	}
	rolled, err := waitForServiceRollout(ctx, r.client, serviceID, timeout, nil)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "wait for the held canary's rollback", err)
		return true
	}
	fp, err := serviceApplyFingerprint(applyBody)
	if err != nil || fp != tracking.PreCanaryFingerprint || rolled.PrimaryVersion == nil {
		return false
	}
	// The fingerprint ignores the name: a new configured name still needs the PUT.
	if !plan.VersionName.IsNull() && !plan.VersionName.IsUnknown() && plan.VersionName.ValueString() != rolled.PrimaryVersion.Version {
		return false
	}
	tflog.Info(ctx, "Planned config is the one deployed before the held canary; the rollback restored it", map[string]any{"service_id": serviceID})
	if plan.VersionName.IsUnknown() {
		plan.VersionName = types.StringValue(rolled.PrimaryVersion.Version)
	}
	resp.Diagnostics.Append(populateServiceResourceModelComputed(ctx, plan, rolled)...)
	refreshServiceTagsIntoModel(ctx, r.client, serviceID, &plan.Tags)
	resp.Diagnostics.Append(setServiceVersionTracking(ctx, resp.Private, &serviceVersionTracking{VersionID: rolled.PrimaryVersion.ID})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	return true
}

// shiftHeldCanary applies a canary_percent change while this resource holds a canary and no
// deploy field changed: it re-sends the held version's config (state's deploy fields) with the new
// share. Anyscale matches the config to the held version, so the canary moves instead of a new
// version starting. The name is not sent: Anyscale ignores it for a matching version. A null
// share completes the rollout with the automatic ramp; 100 cuts over at once.
func (r *ServiceResource) shiftHeldCanary(ctx context.Context, plan, state *ServiceResourceModel, tracking *serviceVersionTracking, heldID string, resp *resource.UpdateResponse) {
	serviceID := state.ID.ValueString()
	timeout, diags := plan.Timeouts.Update(ctx, defaultServiceRolloutTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	held := *state
	held.VersionName = types.StringNull()
	body, err := buildApplyServiceRequest(ctx, &held)
	if err != nil {
		AddConfigError(&resp.Diagnostics, "Invalid Service Configuration", err.Error())
		return
	}
	if !plan.CanaryPercent.IsNull() {
		v := plan.CanaryPercent.ValueInt64()
		body.CanaryPercent = &v
	}
	reqBody, err := MarshalRequestBody(body)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "service apply request", err)
		return
	}
	tflog.Info(ctx, "Shifting the held canary's traffic share", map[string]any{
		"service_id": serviceID, "canary_version_id": heldID, "canary_percent": plan.CanaryPercent.String(),
	})
	applyResp, err := DoRequestAndParse[ServiceResponse](ctx, r.client, "PUT", "/api/v2/services-v2/apply", reqBody, http.StatusAccepted)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "shift canary traffic", err)
		return
	}

	if percent, hold := serviceHoldPercent(plan); hold {
		r.waitForHeldCanary(ctx, plan, state, serviceID, heldID, percent, timeout, resp)
		return
	}

	// Completing: the held version becomes primary. Tracking keeps it pending until then.
	pending := &serviceVersionTracking{VersionID: tracking.VersionID, Pending: true, PendingVersionID: heldID}
	resp.Diagnostics.Append(setServiceVersionTracking(ctx, resp.Private, pending)...)
	resp.Diagnostics.Append(populateServiceResourceModelComputed(ctx, plan, &applyResp.Result)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	observer := newServiceRolloutObserver(heldID)
	observer.observe(&applyResp.Result)
	final, err := waitForServiceRollout(ctx, r.client, serviceID, timeout, serviceCutoverObserver(observer.observeUntilRolledBack))
	if final != nil {
		resp.Diagnostics.Append(populateServiceResourceModelComputed(ctx, plan, final)...)
		refreshServiceTagsIntoModel(ctx, r.client, serviceID, &plan.Tags)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
	if err != nil {
		AddAPIError(&resp.Diagnostics, "wait for service rollout", err)
		return
	}
	if final.CanaryVersion != nil {
		addServiceCutoverNotCompletedError(&resp.Diagnostics, serviceID, final)
		return
	}
	if final.PrimaryVersion == nil {
		return
	}
	resp.Diagnostics.Append(setServiceVersionTracking(ctx, resp.Private, &serviceVersionTracking{VersionID: final.PrimaryVersion.ID})...)
	if serviceRolloutRolledBack(final, heldID, tracking.VersionID) {
		rolledBack := withPriorServiceDeployFields(*plan, *state)
		resp.Diagnostics.Append(resp.State.Set(ctx, &rolledBack)...)
		resp.Diagnostics.AddError("Service Rollout Rolled Back", serviceRolledBackDetail(serviceID, observer, final))
	}
}

// errServiceCanaryFailed marks a held canary that entered an error state. Anyscale never rolls a
// held canary back on its own (a manual rollout yields control to the user), so the provider does.
var errServiceCanaryFailed = errors.New("the held canary version failed")

// serviceCutoverGrace is how long a canary may stay present after it serves 100% of traffic
// before the cutover is reported as not completing. Measured on the test cloud: completion follows
// about 1.5 minutes after the cutover. A var so tests can shorten it.
var serviceCutoverGrace = 5 * time.Minute

// serviceCutoverObserver ends a rollout wait (successfully, from the poller's view) once the
// canary has served 100% for longer than serviceCutoverGrace: where Anyscale does not complete
// manual rollouts, it never will. The caller then reports the canary still present.
func serviceCutoverObserver(next func(*ServiceResult) bool) func(*ServiceResult) bool {
	var fullSince time.Time
	return func(s *ServiceResult) bool {
		if next != nil && next(s) {
			return true
		}
		c := s.CanaryVersion
		if c == nil || c.CurrentWeight == nil || *c.CurrentWeight != serviceCanaryCutover {
			fullSince = time.Time{}
			return false
		}
		if fullSince.IsZero() {
			fullSince = time.Now()
		}
		return time.Since(fullSince) > serviceCutoverGrace
	}
}

// addServiceCutoverNotCompletedError reports a canary still present after the 100 cutover's grace.
func addServiceCutoverNotCompletedError(diags *diag.Diagnostics, serviceID string, final *ServiceResult) {
	diags.AddAttributeError(path.Root("canary_percent"), "Canary Rollout Not Completed",
		fmt.Sprintf("Service %s's canary version %s serves 100%% of traffic, but Anyscale has not completed the rollout after %s; "+
			"it does not complete manual rollouts for this cloud. Remove canary_percent from the configuration and apply again "+
			"to finish the rollout.", serviceID, final.CanaryVersion.ID, serviceCutoverGrace))
}

// rollBackFailedCanary handles a held canary that failed during the apply holding it: it rolls the
// canary back, waits for the primary to keep all traffic, and returns state with the prior deploy
// fields restored and tracking that lets Read refresh ray_serve_config from the live primary, so
// the next plan proposes the change again. The returned error describes the outcome.
func (r *ServiceResource) rollBackFailedCanary(ctx context.Context, plan, prior *ServiceResourceModel, canaryID string,
	failed *ServiceResult, timeout time.Duration, resp *resource.UpdateResponse) {
	serviceID := plan.ID.ValueString()
	detail := serviceFailureDetail(failed)
	if err := rollbackService(ctx, r.client, serviceID); err != nil {
		AddAPIError(&resp.Diagnostics, "roll back the failed canary", err)
		return
	}
	final, err := waitForServiceRollout(ctx, r.client, serviceID, timeout, nil)
	restored := withPriorServiceDeployFields(*plan, *prior)
	restored.CanaryPercent = prior.CanaryPercent
	if final != nil {
		resp.Diagnostics.Append(populateServiceResourceModelComputed(ctx, &restored, final)...)
		if final.PrimaryVersion != nil {
			resp.Diagnostics.Append(setServiceVersionTracking(ctx, resp.Private, &serviceVersionTracking{
				VersionID: final.PrimaryVersion.ID, Pending: true, PendingVersionID: canaryID,
			})...)
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &restored)...)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "wait for the failed canary's rollback", err)
		return
	}
	msg := fmt.Sprintf("The held canary version %s of service %s failed (%s), and Anyscale does not roll back a held canary, "+
		"so Terraform rolled it back. The previous version keeps all traffic. Fix the configuration and apply again.",
		canaryID, serviceID, failed.CanaryVersion.CurrentState)
	if detail != "" {
		msg += " Failure: " + detail
	}
	resp.Diagnostics.AddError("Service Rollout Rolled Back", msg)
}
