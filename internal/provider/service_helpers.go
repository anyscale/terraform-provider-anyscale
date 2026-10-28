package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Service current_state values the rollout/termination wait loop treats as terminal-success
// when they match the caller's target, or classifies below. Traced against
// ServiceEventCurrentState in services/dao/event_models.py.
const (
	serviceStateRunning    = "RUNNING"
	serviceStateTerminated = "TERMINATED"
)

// serviceErrorStates are is_error buckets (traced ServiceEventCurrentState.is_error). Terminal
// only when the wait target is RUNNING (Create/Update): the rollout failed and won't self-heal.
// NOT terminal when the target is TERMINATED (Delete) - a service being torn down can legitimately
// pass through an error state on its way to gone, and its health no longer matters. See
// evaluateServiceState.
var serviceErrorStates = map[string]bool{
	"UNHEALTHY":          true,
	"SYSTEM_FAILURE":     true,
	"USER_ERROR_FAILURE": true,
}

// serviceContinueStates are non-terminal buckets the wait loop keeps polling through,
// regardless of which target it is waiting for (is_updating plus TERMINATING).
var serviceContinueStates = map[string]bool{
	"STARTING":     true,
	"UPDATING":     true,
	"ROLLING_OUT":  true,
	"ROLLING_BACK": true,
	"TERMINATING":  true,
}

// defaultServiceRolloutPollInterval is the real poll interval used in production. Kept
// separate from the timeout (which is caller-supplied - see waitForServiceState) so unit
// tests can drive both timeout and interval down to milliseconds via
// waitForServiceStateWithTiming directly, without eating real wall-clock time on the
// SYSTEM_FAILURE/timeout paths. Mirrors waitForBuildDigestWithTiming's split
// (container_image_helpers.go), not waitForCloudReady's internally-hardcoded backoff.
const defaultServiceRolloutPollInterval = 10 * time.Second

// evaluateServiceState classifies a service's current_state against the wait loop's target
// (serviceStateRunning for Create/Update, serviceStateTerminated for Delete). done=true means
// stop polling. err is set only when target is RUNNING and current_state is a terminal failure
// bucket (error_message surfaced) - an error bucket observed while waiting for TERMINATED is
// NOT terminal here: the service is already being torn down, so its health no longer matters,
// and hard-erroring would make Delete itself fail on exactly the resources most in need of being
// deleted (a Create-tainted service's only recovery path is destroy-then-recreate). err is nil
// for terminal success, while still in progress, or an unrecognized/unexpected state (treated as CONTINUE rather than a hard error, so a
// backend adding a new benign transitional state does not break every apply against an otherwise-healthy service; the
// caller's timeout still backstops a genuinely stuck or new-terminal state, and logs a warning
// so the gap stays visible - see waitForServiceStateWithTiming).
//
// Waiting for RUNNING additionally requires no canary: a rollout is settled only once its canary
// has been promoted or rolled back (see serviceRolloutRolledBack). A goal_state of TERMINATED
// while waiting for RUNNING is terminal: the service was terminated (by a user, or by the backend
// after its primary cluster died) and will not reach RUNNING on its own.
//
// A service-level error bucket is terminal only when a component confirms it (see
// serviceErrorConfirmed). Observed on a real backend: a restarted service reports UNHEALTHY within
// seconds of PUT /apply and stays UNHEALTHY after its version and every checklist item are
// RUNNING, because the service health probe holds RUNNING back until it has seen it several times
// in a row after any error (HEALTH_PROBE_JAIL_THRESHOLD, services/actors/health_probe.py).
func evaluateServiceState(service *ServiceResult, target string) (done bool, err error) {
	switch {
	case service.CurrentState == target && (target != serviceStateRunning || service.CanaryVersion == nil):
		return true, nil
	case target == serviceStateRunning && service.GoalState == serviceStateTerminated:
		return true, fmt.Errorf("service was terminated (current_state %s) while waiting for it to reach %s",
			service.CurrentState, target)
	case target != serviceStateTerminated && serviceErrorStates[service.CurrentState] && serviceErrorConfirmed(service):
		if service.ErrorMessage != nil && *service.ErrorMessage != "" {
			return true, fmt.Errorf("service entered %s state: %s", service.CurrentState, *service.ErrorMessage)
		}
		if detail := serviceChecklistFailureDetail(service.ServiceStatusChecklist); detail != "" {
			return true, fmt.Errorf("service entered %s state: %s", service.CurrentState, detail)
		}
		return true, fmt.Errorf("service entered %s state", service.CurrentState)
	default:
		return false, nil
	}
}

// serviceErrorConfirmed reports whether a service-level error state is backed by a component: a
// version or status checklist item in an error state, or no component reported at all (nothing
// to contradict it). A service whose reported components are all healthy or still starting is
// treated as converging, and the wait's timeout backstops one that never recovers.
func serviceErrorConfirmed(service *ServiceResult) bool {
	reported, failed := false, false
	note := func(state string) {
		reported = true
		failed = failed || serviceErrorStates[state]
	}
	for _, v := range []*ServiceVersionResult{service.PrimaryVersion, service.CanaryVersion} {
		if v != nil {
			note(v.CurrentState)
		}
	}
	if c := service.ServiceStatusChecklist; c != nil {
		for _, item := range c.Shared {
			note(item.State)
		}
		for _, group := range c.PerVersion {
			for _, item := range group.Items {
				note(item.State)
			}
		}
	}
	return failed || !reported
}

// serviceChecklistFailureDetail scans a service's status checklist for per-component failure
// messages, for use only as a fallback when the service's own top-level error_message is empty -
// confirmed via a real crash-diagnosis session that the backend can leave error_message null
// while the actual cause (e.g. "the user who created it has been removed from the organization")
// exists only on a specific checklist item, which previously left the wait error generic and
// undiagnosable without a manual API call. Scans both the Shared list and every PerVersion
// group's items, reusing serviceErrorStates since checklist items share the same state
// vocabulary as the top-level service (RUNNING/UNHEALTHY/STARTING/etc). Skips items whose
// message is empty (e.g. a downstream APPLICATION failure with nothing to say beyond "unhealthy")
// so only components that actually explain themselves get surfaced. checklist may be nil (the
// same nullable shape fixed for the data sources) - nil-safe, returns "" rather than panicking.
func serviceChecklistFailureDetail(checklist *ServiceStatusChecklistResult) string {
	if checklist == nil {
		return ""
	}

	var details []string
	collect := func(items []StatusChecklistItemResult) {
		for _, item := range items {
			if serviceErrorStates[item.State] && item.Message != "" {
				details = append(details, fmt.Sprintf("%s: %s", item.Kind, item.Message))
			}
		}
	}
	collect(checklist.Shared)
	for _, group := range checklist.PerVersion {
		collect(group.Items)
	}

	return strings.Join(details, "; ")
}

// waitForServiceState polls GET /{id} until the service's current_state reaches target, a
// terminal error bucket, or timeout, pinning the real poll interval. timeout remains a
// caller-supplied parameter (not pinned) because Create/Update/Delete all read it from the
// resource's own timeouts block - there is no single real constant to pin the way
// waitForBuildDigest pins both of its own (that wait has no user-facing timeout knob at all).
func waitForServiceState(ctx context.Context, client *Client, serviceID, target string, timeout time.Duration) (*ServiceResult, error) {
	return waitForServiceStateWithTiming(ctx, client, serviceID, target, timeout, defaultServiceRolloutPollInterval)
}

// waitForServiceStateWithTiming is waitForServiceState with the poll interval also exposed as
// a parameter, so tests can prove the success/error/timeout/context-cancelled paths without
// paying real wall-clock time. Production code should call waitForServiceState instead, which
// pins the real interval.
//
// One function covers all three call sites (Create, Update, Delete): only the target state
// differs (RUNNING vs TERMINATED), the predicate and polling shape are identical.
// The returned service may be nil when err is non-nil (e.g. a GET failure with nothing yet
// observed, including an already-cancelled context caught mid-request rather than at the
// select below) - callers must nil-check before dereferencing rather than assume the last
// exit path's shape (a GET error is uniformly nil-service, rather than papered over with a
// last-observed fallback).
func waitForServiceStateWithTiming(ctx context.Context, client *Client, serviceID, target string, timeout, interval time.Duration) (*ServiceResult, error) {
	return pollServiceState(ctx, client, serviceID, target, timeout, interval, nil)
}

// waitForServiceRollout waits for a deploy to settle at RUNNING, passing every polled service to
// observe (when non-nil) so the caller can record version names and failure reasons the settled
// service no longer reports. observe returning true ends the wait successfully with that
// service: the caller has seen enough to decide the outcome (a rolled-back rollout leaves the old
// version primary while the service can still pass through UNHEALTHY, which would otherwise end
// the wait with a generic error).
func waitForServiceRollout(ctx context.Context, client *Client, serviceID string, timeout time.Duration, observe func(*ServiceResult) bool) (*ServiceResult, error) {
	return pollServiceState(ctx, client, serviceID, serviceStateRunning, timeout, defaultServiceRolloutPollInterval, observe)
}

func pollServiceState(ctx context.Context, client *Client, serviceID, target string, timeout, interval time.Duration, observe func(*ServiceResult) bool) (*ServiceResult, error) {
	deadline := time.Now().Add(timeout)

	for {
		service, err := getServiceByID(ctx, client, serviceID)
		if err != nil {
			return nil, err
		}

		tflog.Debug(ctx, "Service state check", map[string]any{
			"service_id":    serviceID,
			"current_state": service.CurrentState,
			"target":        target,
			"has_canary":    service.CanaryVersion != nil,
		})

		if observe != nil && observe(service) {
			return service, nil
		}

		if done, evalErr := evaluateServiceState(service, target); done {
			return service, evalErr
		} else if !serviceContinueStates[service.CurrentState] && service.CurrentState != target &&
			!serviceErrorStates[service.CurrentState] {
			// An unrecognized current_state continues polling rather
			// than hard-erroring - a backend adding a new benign transitional state must not
			// break every apply against a healthy, still-converging service. The timeout below
			// backstops a genuinely stuck/new-terminal state; this only keeps it visible. An
			// is_error bucket that did not end the wait (waiting for TERMINATED, or not confirmed
			// by any component) is excluded from this warning too - it is a known, expected
			// bucket here (evaluateServiceState), not an unrecognized one.
			tflog.Warn(ctx, "Unrecognized service current_state, continuing to poll", map[string]any{
				"service_id":    serviceID,
				"current_state": service.CurrentState,
				"target":        target,
			})
		}

		if time.Now().After(deadline) {
			return service, fmt.Errorf(
				"timed out after %s waiting for service %s to reach state %s (currently %s)",
				timeout, serviceID, target, service.CurrentState,
			)
		}

		select {
		case <-ctx.Done():
			return service, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// getServiceByID fetches a single service by ID, returning DoRequestAndParse's error unwrapped.
// Shared by the wait loop, the resource's Create/Read/Update/Delete, and the data source's
// getService (which adds its own error wrapping).
func getServiceByID(ctx context.Context, client *Client, serviceID string) (*ServiceResult, error) {
	serviceResp, err := DoRequestAndParse[ServiceResponse](
		ctx, client, "GET", fmt.Sprintf("/api/v2/services-v2/%s", serviceID), nil, http.StatusOK,
	)
	if err != nil {
		return nil, err
	}
	return &serviceResp.Result, nil
}
