package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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
func (r *ServiceResource) waitForHeldCanary(ctx context.Context, plan *ServiceResourceModel, serviceID, canaryID string, percent int64, timeout time.Duration, resp *resource.UpdateResponse) {
	final, err := waitForServiceHold(ctx, r.client, serviceID, canaryID, percent, timeout)
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
		r.waitForHeldCanary(ctx, plan, serviceID, heldID, percent, timeout, resp)
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
	final, err := waitForServiceRollout(ctx, r.client, serviceID, timeout, observer.observeUntilRolledBack)
	if final != nil {
		resp.Diagnostics.Append(populateServiceResourceModelComputed(ctx, plan, final)...)
		refreshServiceTagsIntoModel(ctx, r.client, serviceID, &plan.Tags)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
	if err != nil {
		AddAPIError(&resp.Diagnostics, "wait for service rollout", err)
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
