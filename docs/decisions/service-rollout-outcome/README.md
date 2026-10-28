# Design: `anyscale_service` reports a rollout's real outcome

**Status: implemented. Gate 1 and Gate 2 closed** (2026-10-28): Gate 2 by mock acceptance tests, Gate 1 against a real backend.

## The problem

- **A rolled-back rollout looked like success.** When the new version of an auto rollout errors,
  the backend rolls back and leaves the old version primary
  (`backend/server/services/dao/services_dao.py:5339`, ROLLING_BACK event at `:4325-4343`). The
  reconciler then reports the old version's health (`actors/service_reconciler.py:2856`). The wait
  loop treated ROLLING_BACK as "continue" and any RUNNING as success. Update had already written
  the new config to state, and Read never refreshes `ray_serve_config`, so the next plan was
  empty. On a real backend the service can also report UNHEALTHY after the rollback (see Gate 1),
  so the old code either failed with a bare "service entered UNHEALTHY state" or succeeded,
  depending on poll timing. Neither result named the rollback or left the change pending.
- **Out-of-band deploys and rollbacks were invisible.** Only `build_id` and `compute_config_id`
  changes surfaced.
- **Terminated services were never restarted.** The backend auto-terminates a service whose
  primary cluster dies with no rollout active (`service_reconciler.py:2775`). `PUT /apply` on a
  TERMINATED service restarts the same id with a new version (`services_dao.py:938-985`), but Read
  ignored `goal_state` and Update skipped the deploy when no deploy field changed.

## Design

Version identity is the signal. Every ROLLOUT apply creates a new version: an identical config
with no active rollout still does (`services_dao.py:3649-3678`), and names are random when unset
(`api/services_resource.py:735`). Private state key `service_version_tracking`
(`internal/provider/service_rollout_outcome.go`) records:

- `version_id`: the version whose config state's deploy fields describe;
- `pending`, `pending_version_id`: set while an apply's rollout has not been seen to settle.

**Update** (deploy path):

1. Before `PUT /apply`, it sets state to the prior values (B2).
2. It identifies the new version from the 202 response: the canary on a running service, or the
   primary on create or restart. If neither identifies it, it falls back to "primary changed from
   the pre-apply primary". It records the version as pending.
3. It waits, with three rules (`evaluateServiceState`, `observeUntilRolledBack`):
   - Once the canary is gone and another version is primary, the rollback is decided. The wait
     ends at once, even while the service still says ROLLING_BACK or UNHEALTHY.
   - RUNNING with a canary present keeps waiting. A `goal_state` of TERMINATED fails the wait.
   - A service-level error state fails the wait only when a component confirms it: a version or a
     checklist item in an error state, or no component reported at all
     (`serviceErrorConfirmed`). The health probe holds RUNNING back for
     `HEALTH_PROBE_JAIL_THRESHOLD` (3) consecutive healthy probes after any error
     (`services/actors/health_probe.py`), so a restarted service reports UNHEALTHY for about a
     minute after every component is RUNNING. The wait's timeout backstops an error that never
     clears. This rule applies to the Create wait too.
4. On a rollback, it restores the prior deploy fields, tracks the restored version and returns
   **Service Rollout Rolled Back**. The error names both versions as `"name" (id)`. For the reason
   it uses `error_message`, then failing checklist items for the new version. If neither has one,
   it points to the service's events.
5. On timeout or ctrl-C, state keeps the planned config and the tracking stays pending.

**Read** (`reconcileServiceVersionTracking`):

| Live service | Result |
|---|---|
| No tracking (legacy state, import) | Seed from the settled primary. No drift. |
| Terminated, no primary, or canary present | Leave it alone. While a canary is present, `build_id`/`compute_config_id` are not refreshed from the old primary either. |
| Pending, and the settled primary is the pending version | Promote it. No drift. |
| Pending, and the settled primary is any other version | Drift. |
| Not pending, and the primary is not the tracked version | Drift (out-of-band deploy or rollback). |

Drift sets `ray_serve_config` from the live primary using the ImportState conversion, so the plan
redeploys the configured value. **ImportState** seeds the tracking.

**Restart** (B6):

- `serviceRestartRequired(plan, state)` is the single "terminated, but config wants it running"
  predicate, so a later `stopped` argument can gate it.
- ModifyPlan marks the volatile Computed attributes unknown, which forces an in-place update, and
  warns that apply restarts the service.
- Update waits for TERMINATED if the service is still terminating, because the backend returns 409
  before then (`services_dao.py:944-959`). It then sends a standard ROLLOUT; the IN_PLACE path
  rejects non-running services (`:1185`). If config `connection_ids` is null, it sends the last
  version's connections, because the restart path does not carry them forward (only the RUNNING
  branch does, `:996-1002`).

**Known gap — IN_PLACE.** An in-place upgrade keeps the primary's id (`services_dao.py:1247`), and
in-place rollback restores config under that same id. Version identity therefore cannot detect an
IN_PLACE rollback or an out-of-band IN_PLACE deploy. This is unchanged from before; it needs a
different signal, such as `in_place_update_state`.

## Traced wire shapes

| Question | Trace | Status |
|---|---|---|
| Update apply response | `_get_full_service` after `_start_rollout`; formatter sets `canary_version` to the rollout goal (`api/services_formatter.py:668`) | Confirmed, Gate 1 |
| Create/restart apply response | New version persisted as primary (`_persist_service_and_version`) | Confirmed for both, Gate 1 |
| Terminated GET | `primary_version` is the most recent version, `canary_version` null (`services_formatter.py:635-642`) | Confirmed, Gate 1 |
| `error_message` on GET | Set only on the autoscaling-config endpoint (`services_resource.py:466`); null on GET | Confirmed null during rollback, Gate 1 |
| Rollback reason | ROLLING_BACK event message: "Rolling back rollout (reason): new -> old" (`services_dao.py:4325-4343`); events at `GET /api/v2/services-v2/{id}/events` (deprecated) | Confirmed: only in the event, not on GET, Gate 1 |

## Gate 2 — Framework/Core contract (confirmed, mock `resource.Test`, Terraform 1.17.0)

| Item | Test |
|---|---|
| Core persists the state returned with an errored Update, so restored deploy fields plan again | `TestAccServiceResource_RolledBackRolloutFails`, `TestAccServiceResource_ApplyFailureKeepsPriorConfig` |
| Core persists private state returned with an errored Update (pending tracking survives a timeout) | `TestAccServiceResource_InterruptedRolloutSucceededResource`, `TestAccServiceResource_InterruptedRolloutRolledBackResource` |
| ModifyPlan marking Computed attributes unknown is accepted as an in-place update, and Update runs | `TestAccServiceResource_TerminatedServiceRestartsResource` |
| Private state is absent for legacy state; the first Read seeds it silently | `TestAccServiceResource_LegacyStateSeedsTrackingResource` (a wrapper strips `Private` from every apply/read response, simulating an older provider) |
| ImportState private state reaches later plans | `TestAccServiceResource_ImportSeedsTrackingResource` |

**Correction to the original brief:** B2's premise was false. Framework v1.19.0 pre-populates
`UpdateResponse.State` from **prior** state (`internal/fwserver/server_updateresource.go`), not
from the plan. A `PUT /apply` error returned before any `State.Set` already kept the prior values,
and `TestAccServiceResource_ApplyFailureKeepsPriorConfig` passed against the old code. The
explicit restore guards that contract. Mutating it to `State.Set(plan)` makes the test fail
(`expected Update, got no-op`), which also confirms that Core saves whatever an errored Update
returns.

## Gate 1 — real API

**Closed 2026-10-28** against the test org on `tfp-test-aws-useast1-STATIC`, by
`TestAccServiceResource_RealInfra_RolledBackRollout` (the command is in its header). Run 6 is
the final code and passed all four steps. Runs 1-5 found the two wait-loop problems described
under Design (the rollback-then-UNHEALTHY sequence, and the restart's held-back UNHEALTHY). Each
run created one service, plus a compute config and an ephemeral project; all were destroyed.
`GET` on every service id returned 404 afterward, the org lists zero services, the six projects
return 404, and `make sweep-dry-run` reports `swept=0` for services, projects and compute
configs. IDs below are from run 6 unless noted. Logs were captured with `TF_LOG_PROVIDER=DEBUG`
plus the test's whitelisted poller, which records no tokens and no `ray_serve_config`.

**1. Apply response version fields** (provider debug line `Service apply response versions`):

| Apply | `current_state` | `primary_version` | `canary_version` |
|---|---|---|---|
| Create | STARTING | the new version | absent |
| Update of a RUNNING service | ROLLING_OUT | the old version | the new version |
| Restart of a TERMINATED service | STARTING | the new version (`srv_1dny…` replaced `srv_nhwh…`) | absent |

The 202 response identified the new version in every case, so the fallback never ran.

**2. Rollback state sequence, broken `import_path`** (GET every 5s; the sequence was the same in
all six runs):

```
ROLLING_OUT  primary=old/RUNNING canary=new/STARTING   (~2 min; checklist CLUSTER/APPLICATION/TARGET_NODE_GROUP STARTING)
ROLLING_OUT  primary=old/RUNNING canary=new/UPDATING
ROLLING_OUT  primary=old/RUNNING canary=new/UNHEALTHY  (checklist APPLICATION still STARTING/UPDATING)
ROLLING_BACK primary=old/RUNNING canary=null           (canary already gone)
UNHEALTHY    primary=old/RUNNING canary=null           (run 1, ~20s later; later runs moved on first)
```

The rollback completes before the service leaves ROLLING_BACK, so `canary_version: null` with
the old primary is the decision point. Apply failed with (run 2):

> Anyscale rolled back the rollout of service service2_13ay…: new version "v-vwe25zgmvt"
> (srv_311u…) failed and version "v-6jq52wg4ie" (srv_nqdx…) is serving again. …

The follow-up `PlanOnly` step planned an Update.

**3. Where the reason surfaces.** It does not appear on GET. `error_message` stayed null, and no
checklist item carried it: the canary's APPLICATION item never left STARTING/UPDATING. The reason
appears only in the ROLLING_BACK event (`GET /api/v2/services-v2/{id}/events`):

> Rolling back rollout (Service Version v-… errored: Service Version is unhealthy. Application is
> unhealthy (reason: deploying most recent Ray Serve config failed; … Deploying app 'default'
> failed with exception: Traceback …

The error message points there.

**4. Terminated GET shape** (`POST …/terminate`, out of band):

```
current_state=TERMINATED goal_state=TERMINATED primary=<last version>/TERMINATED canary=null ended_at=<timestamp>
```

**5. Restart.** ModifyPlan forced an in-place Update and the warning was emitted. `PUT /apply`
restarted the same service id on a new version:

```
STARTING   primary=new/STARTING
UNHEALTHY  primary=new/STARTING   (16s after apply; TARGET_NODE_GROUP STARTING)
UNHEALTHY  primary=new/UPDATING → RUNNING
UNHEALTHY  primary=new/RUNNING   (every checklist item RUNNING)
RUNNING                           (~56s later)
```

The post-apply plan was empty. Before `serviceErrorConfirmed`, runs 3-5 failed this step with
"service entered UNHEALTHY state".

Not exercised against a real backend: an auto-terminated service (`service_reconciler.py:2775`,
cluster death). Its GET shape is assumed to match the user-terminated one above, because both go
through `services_dao.initiate_termination` (`services_dao.py:1741`; reconciler `service_reconciler.py:3677`).

## Follow-ups

- IN_PLACE outcome detection (see Known gap).
- Consider reading the rollback reason from the service events endpoint once a non-deprecated
  route is confirmed (`/api/v2/events/service/{id}/events`).
