# Design: `anyscale_service` `stopped`

**Status: implemented. Gate 2 closed (mock `resource.Test`, Terraform 1.17.0). Gate 1 closed
(real API, 2026-10-28).**
Builds on `docs/decisions/service-rollout-outcome` (version tracking, rolled-back handling, restart
through `serviceRestartRequired`) and `docs/decisions/service-inputs-parity`. Product monorepo
read-only.

`stopped = true` terminates the service and keeps its record, id and hostname; `stopped = false`
(the default) restarts it through `PUT /apply` under the same id. The use case is turning a
service off, such as a dev service overnight, without destroying it.

## Backend facts

- **Stop.** `POST /api/v2/services-v2/{id}/terminate` (202) sets `goal_state` TERMINATED through
  `services_dao.initiate_termination` (`services/dao/services_dao.py:1741`), a no-op when the goal
  is already TERMINATED (`:1762-1767`). The service then passes through TERMINATING. The route
  returns the service (`api/internal/services_internal_router.py:534-561`,
  `api/services_resource.py:939-963`); the provider does not rely on that body and reads with GET.
- **Restart.** `PUT /apply` on a service whose goal is TERMINATED restarts the same id with a new
  version as primary (`services_dao.py:938-985`), keeping the hostname. It returns 409 while the
  latest state event is not yet TERMINATED (`:944-959`), and does not carry the previous
  version's connections forward (only the RUNNING branch does). The bearer token is changed only
  on an auth flip, before the branch (`:925-936`).
- **Terminated GET.** `goal_state` TERMINATED, `ended_at` set, the last version still
  `primary_version`, no canary (Gate 1 of service-rollout-outcome).
- **CLI.** `anyscale service terminate` is the same POST; `anyscale service deploy` of a
  TERMINATED service is the same restart (`frontend/cli/anyscale/service/_private/service_sdk.py`
  `:906-916`, `:1059-1073`). Nothing is derived client-side.

## Decisions

| # | Question | Decision |
|---|---|---|
| 1 | Refresh | Read sets `stopped = (goal_state == TERMINATED)`. This replaces the forced update from service-rollout-outcome (B6). |
| 2a | Stop and change a deploy field in one apply | Rejected at plan time. |
| 2b | Restart and change deploy fields in one apply | Allowed: one `PUT /apply` restarts with the new config. |
| 2c | Change a deploy field while staying stopped | Rejected at plan time. Non-deploy inputs (tags, `rollout_strategy`, `max_surge_percent`, timeouts) change freely. |
| 3 | Create with `stopped = true` | Rejected at plan time, and again in Create if the value was unknown at plan. This includes the create half of a replacement. |
| 4 | Timeouts | Stop and restart use the `update` timeout. A stop waits for TERMINATED. |
| 5 | Failed restart | The failure is handled as in service-rollout-outcome. A restart has no canary, so it cannot be rolled back. A failed new version fails the apply like a failed create, and state keeps the configured values. If Anyscale then terminates the service, the refresh plans another restart. |
| 6 | Interactions | Unchanged: `IN_PLACE` (a restart is always ROLLOUT), `version_name` (a restart needs a new name), `query_auth_token_enabled` (a restart keeps the token unless auth flips), Delete (terminate is a no-op on a stopped service, and the TERMINATED wait returns at once). |

**1. Refresh, and the end of the forced update.** `stopped` is Optional+Computed with
`Default: false`. A service terminated by anyone, whether through Terraform, the CLI or Anyscale
after its cluster died (`service_reconciler.py:2775`), refreshes to `true`. A config that omits
`stopped` or sets `false` then shows `~ stopped = true -> false`, an ordinary in-place update.
The framework marks the Computed service-state attributes unknown because the plan differs from
state, so `planServiceRestart` (forced unknowns plus a warning) was removed. The diff now says
what the warning used to say. An out-of-band restart of a stopped service refreshes `false` and
plans `false -> true`, which stops it again.

`serviceRestartRequired(plan, state)` is `goal_state == TERMINATED && !plan.stopped`. An unknown
`stopped` counts as running, so the restart is planned and `version_name` goes unknown.
`serviceStopRequired` is the mirror. Both key on the refreshed `goal_state`, not on the prior
`stopped`, so state that has a null `stopped` behaves the same as state that has the value.

**State written before `stopped`** has it null. The first refresh fills it from `goal_state`, so
a running service plans empty and a terminated one plans the restart it planned before. With
`-refresh=false`, null plans as `null -> false`: a one-time update that sends nothing (same
residual as `query_auth_token_enabled`).

**2. Deploy fields while stopped.** A stopped service deploys nothing, and a restart sends
whatever config is set at that point. Recording a change made while stopped would show a plan
that changes nothing live. It would also fight Read, which refreshes `build_id`,
`compute_config_id` and `version_name` from the terminated primary, and it would let a recorded
`version_name` fail "Version Name Must Change" at the restart. State while stopped therefore stays
equal to the last deployed version. `serviceDeployFieldChanges` (every field
`serviceDeployFieldsChanged` counts, `ray_serve_config` included) feeds the error "Cannot Change a
Stopped Service's Deployment", which names the fields and the fix: set `stopped = false` to deploy
the change, then stop again, or revert it. A reflection test enumerates the model, so a future
deploy field missing from the check fails.

**Known cost.** If a stopped service is restarted outside Terraform with a different config, the
refresh records that config (version-tracking drift). Stopping it again is then a stop plus a
`ray_serve_config` change, which is rejected. The way out is `stopped = false`, which redeploys
the configured version, followed by `stopped = true`. That is one extra rollout, for a rare case
that already overrides Terraform.

**3. Create.** Anyscale has no create-stopped call. Create-then-terminate would start a cluster
only to tear it down, and termination during STARTING is unverified. Rejecting is simpler, and
the message covers a replacement (`name`/`project_id`).

**4. Stop** (`stopService`): POST terminate, then wait for `current_state` TERMINATED with the
`update` timeout, so an immediate restart does not get the 409. Error states do not end the wait
(the same TERMINATED target as Delete). If the wait times out or is interrupted, state still
records the last observed service with `stopped = true`, because the goal is already TERMINATED.
A restart from TERMINATING waits for TERMINATED first (`prepareServiceRestart`, unchanged).

## Gate 2 — Framework/Core contract (closed, mock `resource.Test`, Terraform 1.17.0)

| Item | Test |
|---|---|
| A refreshed `stopped = true` against config `false` plans an in-place Update, with `current_state`/`primary_version` unknown and no forced unknowns (each of #372's restart cases) | `TestAccServiceResource_TerminatedServiceRestartsResource` (extended with plan checks), `TestAccServiceResource_VersionNameRestartNeedsNewName` (unchanged, passes) |
| Stop: Update planned (`stopped` known `true`), terminate sent once, wait passes TERMINATING, state TERMINATED with the same id, post-apply plan empty. Restart: Update planned, standard ROLLOUT, new primary, `version_name` recorded | `TestAccServiceResource_StopAndRestartResource` |
| Plan-time rejection of stop+change and change-while-stopped, freedom for non-deploy inputs, restart with a changed config, replacement-while-stopped rejected | `TestAccServiceResource_StoppedRejectsDeployChangeResource` |
| Create with `stopped = true` rejected at plan, nothing sent | `TestAccServiceResource_CreateStoppedRejectedResource` |
| Out-of-band restart plans `false -> true` and stops again; the drifted-config cost | `TestAccServiceResource_StoppedServiceRestartedOutOfBandResource` |
| State without `stopped` (a wrapper nulls it in every plan/apply/read): running plans empty; terminated plans the restart | `TestAccServiceResource_PreStoppedStateRestartsResource` |
| Import of a terminated service records `stopped = true` (Test A, `ImportStateCheck`); a persisted cold import plans no stop or restart (Test B) | `TestAccServiceResource_ImportStoppedServiceResource` |
| Failed restart fails the apply; the next plan is empty; a later auto-terminate plans a restart | `TestAccServiceResource_StoppedRestartFailsResource` |

Unit tests: `TestServiceDeployFieldsAreFrozenWhileStopped` (reflection) and
`TestServiceStopAndRestartRequired`. The outcome mock now models termination as traced:
TERMINATING polls, the 409 on an apply during them, and the restart that follows.

**Mutation proofs** (each regression introduced, the tests failed, and the file was restored
byte-identical):

| Mutation | Failed |
|---|---|
| Read does not refresh `stopped` | `TerminatedServiceRestarts`, `StoppedServiceRestartedOutOfBand`, `PreStoppedStateRestarts`, `ImportStoppedService`, `StoppedRestartFails` |
| ModifyPlan stopped-field check disabled (the apply-time check remains) | `StoppedRejectsDeployChange`, `StoppedServiceRestartedOutOfBand` (rejection steps are `PlanOnly`) |
| ModifyPlan create check disabled (the Create check remains) | `CreateStoppedRejected`, `StoppedRejectsDeployChange` |
| `serviceRestartRequired` ignores `stopped` | `StopAndRestart`, `StoppedRejectsDeployChange`, `ImportStoppedService` |
| Stop reads once instead of waiting for TERMINATED | `StopAndRestart` (`current_state` TERMINATING) |

## Gate 1 — real API

`TestAccServiceResource_RealInfra_StopAndRestart` (the command is in its header): create, then
`stopped = true`, then `stopped = false`, on `tfp-test-aws-useast1-STATIC`.

**Passed** (2026-10-28, 437s), torn down: service and project GET 404, `make sweep-dry-run` clean.

| After | `current_state` / `goal_state` | id, hostname | `primary_version` | canary |
|---|---|---|---|---|
| `stopped = true` | TERMINATED / TERMINATED | unchanged | the create's version, TERMINATED | null |
| `stopped = false` | RUNNING / RUNNING | unchanged | a new version, RUNNING | null |

The post-apply plan was empty after each step, so Read's `stopped = (goal_state == TERMINATED)`
round-trips against a real GET. A restart after an out-of-band terminate was confirmed separately
by service-rollout-outcome's Gate 1 (step 5).
