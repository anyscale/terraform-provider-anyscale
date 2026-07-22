package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// This file backs the anyscale_system_cluster resource/data source (shipwright's TF surface)
// with reusable client support for the Anyscale "System Cluster" feature. Naming note: the
// wire-level API and its backend router call this concept "system workload"
// (system_workload_router.py, SystemWorkloadService) - this file follows that vocabulary for
// anything that talks to the wire, reserving "SystemCluster" for the user-facing Terraform
// resource type and its schema/CRUD (resource_system_cluster.go). This also keeps the two
// clearly distinct from the removed anyscale_cloud.enable_system_cluster attribute and its Go
// identifiers (EnableSystemCluster, SystemClusterConfigID, updateCloudSystemClusterConfig).
//
// THREE-ENUM TRAP (read before touching status logic): this feature's status can come from two
// different endpoints, which use THREE different, similarly-shaped enums:
//   - describe_system_workload's `status` field: ClusterState (10 values, PascalCase - Running,
//     StartingUp, StartupErrored, Updating, UpdatingErrored, Terminating, AwaitingStartup,
//     TerminatingErrored, Terminated, Unknown). This is the ONE enum this provider ever
//     surfaces as System Cluster status.
//   - decorated_sessions' deprecated `state` field: SessionState (13 values, PascalCase but a
//     different set - adds Stopped/Stopping/AwaitingFileMounts/StoppingErrored).
//   - decorated_sessions' `status` field (the field that formally deprecates `state`):
//     ClusterStatus (only 6 values, ALL-CAPS - STARTING/RUNNING/RECOVERING/RESTARTING/
//     TERMINATING/TERMINATED).
// findSystemWorkloadCluster (backed by decorated_sessions) is used ONLY as a side-effect-free
// existence+cluster_id oracle and deliberately does not even model decorated_sessions' state/
// status fields (see DecoratedSessionResult in models.go) - the authoritative status always
// comes from describeSystemWorkload once existence is confirmed.
//
// CREATE-ON-READ HAZARD (why the two-call oracle+status split exists at all): calling describe
// with is_enabled=true and NO cluster yet created ALWAYS creates one, regardless of the
// start_cluster flag passed. A naive "read status" built directly on describe would silently
// provision backend state during what Terraform treats as a side-effect-free plan/refresh.
// findSystemWorkloadCluster (backed by decorated_sessions, a plain list/search that can never
// create anything) exists specifically to answer "does a cluster exist yet" before ever calling
// describe for status - see its own doc comment below.

// systemWorkloadNameRayObsEventsAPIService is the only SystemWorkloadName this provider ever
// sends or expects. The backend enum has more values, but the console/mission only ever deals
// with this one, and the mission explicitly forbids exposing workload_name as configurable
// (changing it on a live cluster forces a backend-triggered restart).
const systemWorkloadNameRayObsEventsAPIService = "RAY_OBS_EVENTS_API_SERVICE"

// systemWorkloadClusterNameFilter is the stable, hardcoded literal name the backend assigns a
// cloud's system workload cluster session at creation. Used only as a coarse server-side
// pre-filter in findSystemWorkloadCluster - decorated_sessions' name_match is a case-insensitive
// SUBSTRING match (confirmed against the real TextQuery(contains=...) usage), never treated as
// the authoritative match on its own; the client-side cloud_id/is_system_cluster check is.
const systemWorkloadClusterNameFilter = "system_workload_cluster"

// enableSystemCluster calls the system-cluster config PUT route for cloudID. This is the same
// endpoint previously called from resource_cloud.go's now-removed updateCloudSystemClusterConfig
// (anyscale_cloud.enable_system_cluster) - relocated here as part of consolidating System
// Cluster management into this resource, not reimplemented differently: same path, same query
// parameter, same empty body/response. enabled=false is a legitimate call this resource never
// makes itself (delete is state-only, and there is no exposed "disable" surface per the mission's
// non-goals) but is kept as a real parameter rather than hardcoded true, since it costs nothing
// and matches the underlying endpoint's real shape.
func enableSystemCluster(ctx context.Context, client *Client, cloudID string, enabled bool) error {
	path := fmt.Sprintf("/api/v2/clouds/%s/update_system_cluster_config?is_enabled=%t", cloudID, enabled)
	tflog.Debug(ctx, "PUT "+path)
	_, err := DoRequestRaw(ctx, client, "PUT", path, nil, http.StatusOK, http.StatusNoContent)
	return err
}

// describeSystemWorkload calls POST /api/v2/system_workload/{cloud_id}/describe.
//
// startCluster is always an explicit, required parameter - the backend defaults it to TRUE if
// the query parameter is omitted (confirmed against the real router), so a poll loop that ever
// forgets to pass false would silently re-request a start on every single tick. Every call site
// in this file passes it explicitly; do not add a bool zero-value default or an omitempty-style
// shortcut here.
//
// workload_name, cloud_resource_id, and start_cluster are query parameters, NOT a JSON request
// body - confirmed against the generated OpenAPI client (frontend/cli/anyscale/client/
// openapi_client/api/default_api.py), which is this provider's established tie-breaker for wire
// format when a bare FastAPI signature is ambiguous (see e.g. resource_cloud.go's
// auto_add_user precedent). cloud_resource_id is omitted entirely: every real caller (console,
// CLI) omits it too, and it only matters for a cloud with more than one anyscale_cloud_resource,
// where it would restrict to a non-primary resource - out of scope here.
//
// Safe to call with startCluster=true to request a start. Safe to call with startCluster=false
// ONLY once a cluster is already confirmed to exist (true unconditionally once this resource has
// completed Create, or after findSystemWorkloadCluster has confirmed existence) - calling it on
// a cloud with is_enabled=true and no cluster yet created ALWAYS creates one as a side effect,
// regardless of startCluster's value. See the package doc comment above.
//
// A 501 (unsupported provider/compute-stack combination, gated by a live feature flag) surfaces
// as a plain error through the normal DoRequestAndParse status-check path - callers must not
// swallow it as "missing"; it is a real, actionable diagnostic.
func describeSystemWorkload(ctx context.Context, client *Client, cloudID string, startCluster bool) (*DescribeSystemWorkloadResult, error) {
	path := fmt.Sprintf(
		"/api/v2/system_workload/%s/describe?workload_name=%s&start_cluster=%t",
		cloudID, systemWorkloadNameRayObsEventsAPIService, startCluster,
	)
	resp, err := DoRequestAndParse[DescribeSystemWorkloadResponse](ctx, client, "POST", path, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

// terminateSystemCluster calls POST /api/v2/system_workload/{cloud_id}/terminate. Not called by
// this resource's own Create/Read/Update/Delete (Delete is deliberately state-only - see the
// resource's own doc comment) - exposed here only for a possible future explicit-terminate
// capability and for acceptance-test teardown of real infra created during testing.
//
// The backend 404s if no system cluster exists for cloudID, and 409s if it is already
// Terminated (unlike start, already-terminated is an error here, not a silent no-op) - both
// surface as plain errors through the normal status-check path.
func terminateSystemCluster(ctx context.Context, client *Client, cloudID string) error {
	path := fmt.Sprintf("/api/v2/system_workload/%s/terminate", cloudID)
	tflog.Debug(ctx, "POST "+path)
	_, err := DoRequestRaw(ctx, client, "POST", path, nil, http.StatusAccepted)
	return err
}

// SystemWorkloadClusterExistence is what findSystemWorkloadCluster reports: whether a System
// Cluster session already exists for a cloud, and its cluster_id if so. Deliberately NOT a
// status - see the package doc comment's three-enum trap. Callers that need status call
// describeSystemWorkload next, now that existence is confirmed safe to do so.
type SystemWorkloadClusterExistence struct {
	ClusterID string
}

// findSystemWorkloadCluster reports whether cloudID's System Cluster session already exists,
// without ever risking describeSystemWorkload's create-on-read side effect: GET
// /api/v2/decorated_sessions/ is a plain list/search query that can never create anything.
//
// The cloud_id query parameter on this endpoint is DEAD - confirmed unused server-side (the real
// handler marks it `# noqa: ARG001` and never references it in its body) - so this cannot filter
// by cloud server-side. Instead it fetches every session matching the coarse name_match
// pre-filter (paginated via the shared PaginatedRequest helper) and filters CLIENT-SIDE for the
// exact cloud, mirroring this provider's established PickMostRecentMatch idiom of "fetch
// candidates broadly, match precisely client-side" already used for by-name lookups elsewhere.
//
// Returns (nil, nil) - not an error - when no matching cluster is found; that is the expected,
// common "never started" case for both the data source and a cold resource Read/Import.
func findSystemWorkloadCluster(ctx context.Context, client *Client, cloudID string) (*SystemWorkloadClusterExistence, error) {
	queryParams := url.Values{"name_match": {systemWorkloadClusterNameFilter}}

	candidates, err := PaginatedRequest[DecoratedSessionResult](
		ctx, client, "/api/v2/decorated_sessions/", queryParams,
		func(body []byte) ([]DecoratedSessionResult, *string, error) {
			var resp DecoratedSessionsListResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				return nil, nil, err
			}
			return resp.Results, resp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to search for existing system cluster session: %w", err)
	}

	matchCount := 0
	var match DecoratedSessionResult
	for _, c := range candidates {
		if !c.MatchesCloud(cloudID) || !c.IsSystemCluster {
			continue
		}
		matchCount++
		match = c
	}

	WarnIfMultipleMatches(ctx, "system cluster session", cloudID, matchCount, match.ID)
	if matchCount == 0 {
		return nil, nil
	}
	return &SystemWorkloadClusterExistence{ClusterID: match.ID}, nil
}

// systemClusterStateRunning is the target state Create/Update wait for. Unexported but
// package-visible, so resource_system_cluster.go (same package) can pass it to
// waitForSystemClusterState without hardcoding the literal string.
const systemClusterStateRunning = "Running"

// defaultSystemClusterPollInterval mirrors the CLI's own polling interval for this exact
// resource (cloud_sdk.py's terminate wait uses interval_s=10) and this provider's existing
// service-rollout default. Kept separate from the timeout (see waitForSystemClusterState) so
// unit tests can drive both down to milliseconds via waitForSystemClusterStateWithTiming
// directly. Consumed by resource_system_cluster.go's Create/Update, which lands on a separate
// quest branch and merges with this one - the nolint below is temporary and resolves on its own
// once merged (harmless no-op the moment a real caller exists; safe to leave or strip either way).
//
//nolint:unused // no caller within this branch alone - see comment above
const defaultSystemClusterPollInterval = 10 * time.Second

// systemClusterErrorStates are the ClusterState values that terminate the wait loop with a
// failure. Unlike Service's equivalent, describeSystemWorkload's response carries no separate
// error-message field, so the error can only name the state itself - documented gap, not an
// oversight.
var systemClusterErrorStates = map[string]bool{
	"StartupErrored":     true,
	"UpdatingErrored":    true,
	"TerminatingErrored": true,
}

// systemClusterContinueStates are the ClusterState values the wait loop keeps polling through
// without a warning log, regardless of target. This is deliberately narrower than the backend's
// own service-local NON_TERMINAL_STATES set (StartingUp/Running/Updating/AwaitingStartup, the
// bucket the server itself uses to decide whether a start request is a no-op): Running is
// excluded here because for OUR wait loop it is the success target, not a "still going" state.
//
// Terminated IS in this set, unlike Terminating/Unknown below - confirmed live (AC26 smoke
// test) that describe(start_cluster=true) against a cloud with no prior cluster creates one and
// returns immediately with status=Terminated, since the StartingUp transition is genuinely
// async and not visible in that same response. So the very first poll after every Create
// observes Terminated as a normal, expected step, not an anomaly - logging a warning on every
// single Create would be noise, not signal.
//
// Terminating and Unknown are deliberately NOT in this set. They fall through to the
// "unrecognized, continue with a warning" branch as any genuinely unknown future state (F6
// forward-compat, mirroring service_helpers.go's evaluateServiceState) rather than being treated
// as an expected transitional state - per architect's ruling, since the backend's own no-op-
// retry-start bucket excludes them (a start call against a Terminating cluster is an untraced
// risk). This wait loop never re-issues start_cluster=true after the initial Create request
// regardless (see describeSystemWorkload's own doc comment), so this only affects how loudly we
// log while waiting, not correctness.
var systemClusterContinueStates = map[string]bool{
	"StartingUp":      true,
	"Updating":        true,
	"AwaitingStartup": true,
	"Terminated":      true,
}

// evaluateSystemClusterState classifies result against target the same way
// evaluateServiceState/evaluateBuildStatus do: done=true+nil=terminal success, done=true+err=
// terminal failure, done=false=keep polling (including for a state not in either map - the
// caller is responsible for logging that case, matching service_helpers.go's F6 pattern).
// cloudID is included only to identify which cloud's error this is - describe's response has no
// separate message field to enrich it with (see systemClusterErrorStates' doc comment).
func evaluateSystemClusterState(result *DescribeSystemWorkloadResult, target, cloudID string) (done bool, err error) {
	status := ""
	if result.Status != nil {
		status = *result.Status
	}

	switch {
	case status == target:
		return true, nil
	case systemClusterErrorStates[status]:
		return true, fmt.Errorf("cloud %s's system cluster entered %s state", cloudID, status)
	default:
		return false, nil
	}
}

// waitForSystemClusterState polls describeSystemWorkload(startCluster=false) until status
// reaches target, a terminal error state, or timeout, pinning the real poll interval. timeout is
// caller-supplied (read from the resource's own start_timeout attribute), not pinned - there is
// no single real constant to pin the way e.g. waitForBuildDigest pins both of its own.
//
// Every poll in this loop passes startCluster=false - never true again after the initial Create
// request - so this can never re-trigger an actual start against a cloud whose cluster is
// Terminating or in any other state, regardless of how evaluateSystemClusterState/the continue
// map classify what comes back.
//
// Consumed by resource_system_cluster.go's Create/Update, which lands on a separate quest
// branch and merges with this one - the nolint below is temporary and resolves on its own once
// merged (harmless no-op the moment a real caller exists; safe to leave or strip either way).
//
//nolint:unused // no caller within this branch alone - see comment above
func waitForSystemClusterState(ctx context.Context, client *Client, cloudID, target string, timeout time.Duration) (*DescribeSystemWorkloadResult, error) {
	return waitForSystemClusterStateWithTiming(ctx, client, cloudID, target, timeout, defaultSystemClusterPollInterval)
}

// waitForSystemClusterStateWithTiming is waitForSystemClusterState with the poll interval also
// exposed as a parameter, so tests can prove the success/error/timeout/context-cancelled paths
// without paying real wall-clock time. Production code should call waitForSystemClusterState
// instead, which pins the real interval.
//
// The returned result may be non-nil even when err is non-nil (the last-observed status on
// failure/timeout) - callers must not assume nil-on-error, matching waitForServiceStateWithTiming's
// own documented contract.
//
// GET errors are NOT tolerated here - a single failed describeSystemWorkload call aborts the
// wait immediately (service-style fail-fast, a correctness gate per architect's ruling), not
// waitForBuildDigestWithTiming's tolerate-and-continue settle-wait style: a stuck/erroring
// System Cluster start is exactly the kind of thing Terraform must fail loudly on, not paper
// over.
func waitForSystemClusterStateWithTiming(ctx context.Context, client *Client, cloudID, target string, timeout, interval time.Duration) (*DescribeSystemWorkloadResult, error) {
	deadline := time.Now().Add(timeout)

	for {
		result, err := describeSystemWorkload(ctx, client, cloudID, false)
		if err != nil {
			return nil, err
		}

		status := ""
		if result.Status != nil {
			status = *result.Status
		}

		tflog.Debug(ctx, "System cluster state check", map[string]any{
			"cloud_id": cloudID,
			"status":   status,
			"target":   target,
		})

		if done, evalErr := evaluateSystemClusterState(result, target, cloudID); done {
			return result, evalErr
		} else if !systemClusterContinueStates[status] {
			// F6: an unrecognized state (including the deliberately-excluded Terminating and
			// Unknown, see systemClusterContinueStates) continues polling rather than hard-
			// erroring immediately - the timeout below still backstops a state that never
			// resolves, this only keeps the gap visible.
			tflog.Warn(ctx, "Unrecognized or edge system cluster state, continuing to poll", map[string]any{
				"cloud_id": cloudID,
				"status":   status,
				"target":   target,
			})
		}

		if time.Now().After(deadline) {
			return result, fmt.Errorf(
				"timed out after %s waiting for cloud %s's system cluster to reach state %s (currently %s)",
				timeout, cloudID, target, status,
			)
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(interval):
		}
	}
}
