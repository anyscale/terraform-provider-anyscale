# Design: `anyscale_service` `canary_percent`

**Status: approved 2026-11-02 (decisions 3 and 15 ruled by the user). Gate 1 closed (real API,
2026-11-02). Gate 2 mock tests in `internal/acctest/resource_service_canary_percent_acc_test.go`,
with the mock modelling the backend's version matching. Real-infra end-to-end run passed
2026-11-02 (below).**
Builds on `docs/decisions/service-rollout-outcome` (version tracking, wait rules, rolled-back
handling), `service-inputs-parity` and `service-stopped`. Product monorepo read-only.

## Workflow

Bring a new version up with no traffic, test it, then shift traffic to it over several applies:

```hcl
resource "anyscale_service" "app" {
  # ... new build_id / ray_serve_config ...
  canary_percent = 0   # apply 1: new version starts, serves nothing
  # canary_percent = 25  # apply 2: same version, 25% of traffic
  # canary_percent = null  # apply 3: finish the rollout (removed from config)
}
```

Today this is impossible: the resource never sends `canary_percent`, and every apply rolls to
100% and waits.

## Backend facts

Confirmed against the static AWS cloud on 2026-11-02 by the two probes in
`internal/acctest/resource_service_canary_probe_realinfra_acc_test.go`:
`TestAccServiceResource_RealInfra_CanaryPercentProbe` (G1-1..4, run 2) and
`..._CanaryPercentProbeCompletion` (G1-5..8). Each created one service, compute config and
ephemeral project; all were destroyed and the service and project GETs return 404. Weights are
shown as `weight/current_weight/target_weight`; `current_weight` lags `weight` by 20-40s and is
the settled value.

| # | Question | Result |
|---|---|---|
| G1-1 | Same body, only `canary_percent` 0 -> 50 | **The existing canary moves.** Same version id and name; `target_weight` 0 -> 50 in the 202 response, `weight` 50 after ~30s, `current_weight` 50 after ~70s. No new version. |
| G1-2 | Same body at 50, different explicit `version` | 202, **name silently ignored**: the canary keeps its generated name. |
| G1-3 | Same body, `canary_percent` omitted | Rollout switches to automatic: `target_weight` 100, ramp 50 -> 75 -> 100, then the canary is promoted under the **same id** and `canary_version` is null (~2.5 min). |
| G1-4 | Current provider, plan while a canary is held at 0 | No-op. Read leaves deploy fields alone while a canary is present, so a held rollout is invisible. After the out-of-band completion (G1-3) it plans an update (drift, per service-rollout-outcome). |
| G1-5 | Held at 0, then `canary_percent = 100` | **Completed** in the test org: `target_weight` 100 at once, traffic 0 -> 100 in one step (no ramp), canary promoted under the same id ~1.5 min later. So the completion flag is on for this cloud; it is not known for other orgs. |
| G1-6 | Broken version (missing `import_path`) held at 0 | **Not rolled back.** Canary STARTING -> UPDATING -> UNHEALTHY in ~3 min; service `current_state` UNHEALTHY, `goal_state` RUNNING; old primary keeps 100%. Still held and UNHEALTHY when the probe stopped watching at 30 min; a rollback then cleared it like G1-8. Matches source: a manual rollout never rolls back on error; at 0 it posts an UNHEALTHY event, above 0 it "yields full control to the user" (`reconcile_one_service_rollout`, `services_dao.py:4425-4460`). |
| G1-7 | Primary's own body re-sent, no rollout in flight | **A new version starts** (new id, automatic ramp 5 -> 10 -> 20 -> 30 -> 50 -> 75 -> 100, ~9 min). |
| G1-8 | `POST /api/v2/services-v2/{id}/rollback` of a healthy canary held at 0 | 202 with `canary_version` already null and `current_state` ROLLING_BACK; RUNNING ~20s later. A fresh hold at 0 afterward works and gets a new id and name. |

Source trace (rows marked with a G1 number are also confirmed above):

- Matching is by config hash, not name: `_version_matches` compares
  `base_ha_jobs_config.md5_static_hash` (`services/dao/services_dao.py`); a match updates the rollout
  (`_update_existing_rollout`), a mismatch returns **409** "There is already a rollout ... in
  progress ... rollback previous rollout first". The CLI's traffic-only update relies on the same
  match: it re-sends the canary's own config and name with a new percent
  (`service_sdk.py` `_build_apply_service_model_for_weight_update`).
- A matching apply **without** `canary_percent` switches the rollout to automatic
  (`_update_existing_rollout`), which G1-3 confirms.
- Manual completion at 100 requires the LaunchDarkly flag `services_v2_enable_auto_complete_rollout`
  for the cloud (`_apply_manual_rollout`, `services_dao.py:4865`); on in the test org (G1-5). Without
  it a rollout held at 100 never completes. Automatic completion (G1-3) is not flag-gated.
- GET exposes `auto_rollout_enabled` (`services_formatter.py:509`): false exactly when a manual
  `goal_canary_percent` is set, so Read can tell a held canary from an automatic rollout.
- Rollback: `POST /api/v2/services-v2/{id}/rollback` (202). Termination rolls back in-flight
  rollouts. The CLI rejects `--canary-percent` with `--in-place` and documents it as ignored on
  create and restart.

## Decisions

| # | Question | Decision |
|---|---|---|
| 1 | Shape | `canary_percent`: Optional `Int64`, 0-100. Additive; no state upgrade (absent decodes as null). Not an Action: Actions need Terraform 1.14 and the floor stays 1.10. |
| 2 | Meaning | The traffic share for the version being rolled out. Null (default) is today's behavior: roll to 100% and complete. Non-null holds the rollout at that share. |
| 3 | What "done" is | **Null or `100`** (user ruling 2026-11-02). Null completes with the automatic ramp everywhere (G1-3). `100` is an immediate full cutover, matching `anyscale service deploy --canary-percent 100`; it completes where the backend's auto-complete flag is on (G1-5). The apply waits for completion like null. If the canary is still present after `current_weight` reaches 100 plus a grace period, the wait fails with a diagnostic saying the rollout is held at 100% and to remove `canary_percent` to finish. State keeps the planned `100`, and a follow-up apply with null completes it. |
| 4 | Deploy change with `canary_percent` set | One `PUT /apply` with `canary_percent`. Wait until the canary is healthy (version and every one of its checklist rows RUNNING) and `current_weight == target_weight == canary_percent`. State: deploy fields are the new config; `primary_version` the old version, `canary_version` the new. |
| 5 | `canary_percent`-only change while the canary is held | Re-send the held version's body with the new percent (G1-1). Same wait as 4. No new version. |
| 6 | `canary_percent` change with no canary present | State-only. No API call: there is nothing to split, and re-sending a primary's body may start a new version (G1-7). It takes effect on the next deploy. |
| 7 | Deploy-field change while a canary is held | `POST .../rollback` (G1-8), wait for the canary to go, then `PUT /apply` with the new config and planned `canary_percent`. ModifyPlan warns that the held canary will be rolled back. If the new config equals the pre-canary config (kept in private state when the hold starts), the `PUT` is skipped: re-sending it would start a new version (G1-7). |
| 8 | `version_name` change while a canary is held, deploy fields otherwise unchanged | **Rejected at plan time.** The backend accepts and ignores it (G1-2), so allowing it would record a name that was never applied. |
| 9 | Create, restart (`stopped` true -> false) | `canary_percent` has no effect: there is no previous version to split with (CLI documents it ignored). Allowed and stored, so the next deploy honors it; documented on the attribute. |
| 10 | `stopped = true` while held | Terminate as today; the backend rolls back the canary first. |
| 11 | `IN_PLACE` + non-null `canary_percent` | Rejected at plan time (backend and CLI reject). |
| 12 | Read | Set `canary_percent` from the canary's `target_weight` only when a canary is present, `auto_rollout_enabled` is false, **and** the canary is the one this resource holds (private tracking's held version id equals `canary_version.id`). Otherwise keep the prior value. An out-of-band held canary therefore stays invisible (decision 15); refreshing from it would plan `10 -> null`, and applying that would complete someone else's rollout. A weight change to this resource's own held canary is still detected. Version tracking and the "canary present" skip are unchanged. |
| 13 | Import | No tracking exists yet, so import recovers `canary_percent` from any held manual canary and records it as this resource's held version. A config that omits it then plans null, i.e. completing the held rollout, visible in plan. |
| 14 | Canary fails during the apply that holds it | The backend keeps a failed held canary (G1-6), so the provider rolls it back itself (`POST .../rollback`), restores the prior deploy fields, tracks the old primary and returns **Service Rollout Rolled Back**, the same contract as an automatic rollback. The next plan proposes the change again. A canary that fails *after* the apply returned is not acted on by Read; it shows as `current_state` UNHEALTHY and `canary_version.current_state`, and the user changes config (decision 7) to recover. |
| 15 | Out-of-band held canary (not started by this resource) | **Out of scope for v1** (user ruling 2026-11-02). Unchanged: plan shows nothing; a deploy change whose config differs from the held canary gets the backend 409 "rollout ... in progress"; a change to no deploy field sends no `PUT` and does not wait (`Update`'s no-deploy branch), so it is unaffected. The attribute docs say to finish or roll back an out-of-band canary before applying. A later change can treat an untracked canary as drift and roll it back. |

## Alternatives rejected

- **Promote/rollback Actions.** Imperative, need Terraform 1.14 (floor 1.10), and the backend
  supports the declarative shape directly.
- **Rejecting `100` at plan time.** Stricter than the CLI, which accepts it; the flag-gated case is handled by a bounded wait (decision 3).
- **Separate `anyscale_service_rollout` resource.** Splits one Terraform-owned object; the rollout
  has no identity outside the service and its config must equal the service's.
- **Replacement on a deploy change while held.** Destroys the service and its hostname; rollback +
  apply is the real backend path.

## Gate 2 (mock `resource.Test`, two live versions)

Mock fidelity first; without it the tests cannot detect what they target:

- Match by config hash: an apply body that differs from the in-flight canary gets the 409
  "rollout ... in progress"; an identical body updates `target_weight`.
- The primary's own body with no rollout in flight starts a new version (G1-7).
- `current_weight` lags `target_weight` by at least one poll; `auto_rollout_enabled` is false
  exactly when a manual percent is set; completion promotes the canary under the same id.
- Per-endpoint write counters (apply `PUT`, rollback `POST`) and an ordered request log.

Cases:

1. Deploy with `canary_percent = 0`, then 25, then null: one `PUT` each; the second carries the
   same body; post-apply plans empty; no "inconsistent result after apply".
2. Plan-time rejections: `IN_PLACE` + non-null, `version_name` change while held.
3. Deploy-field change while held: rollback `POST` precedes the `PUT`; warning emitted. Revert to
   the pre-canary config: rollback only, no `PUT`.
4. Canary in error during a holding apply: rollback `POST`, prior deploy fields in state,
   **Service Rollout Rolled Back**, next plan non-empty.
5. `canary_percent`-only change with no canary: zero API writes.
   Also: `100` held past the grace period fails the wait with the "remove `canary_percent`"
   diagnostic and keeps `100` in state; a follow-up null completes it.
6. Read refresh of `canary_percent` from a manual canary's `target_weight`; the prior value kept
   during an automatic rollout and with no canary.
7. `100`, flag on: the canary is promoted, `canary_version` null, apply succeeds.
8. Decision 9: create, and restart (`stopped` true -> false), with `canary_percent = 25`: stored,
   no split.
9. Decision 13 import: Test A (`ImportStateCheck` sees `canary_percent` recovered from a held
   manual canary) and Test B (two `Config` steps reconstructing that shape, asserting the plan
   action). A Create -> Import -> re-apply sequence cannot prove it.
10. Decision 12 negative: an automatic rollout in flight leaves the prior `canary_percent`.
11. Decision 15 boundary: an out-of-band held canary still plans empty.
12. `canary_percent` null: the apply body is byte-identical to today's.
13. Validator: -1 and 101 rejected at plan.
14. Mutation proof for each: break the behavior, confirm the test fails, revert.

## Real-infra end-to-end

Run 2026-11-02 against the static AWS cloud by
`internal/acctest/resource_service_canary_percent_realinfra_acc_test.go` (all three tests PASS,
26 min total). Each test created its own compute config, ephemeral project and service and
destroyed them; `CheckDestroy` confirmed the services 404. Request order is from the provider's
INFO lines in the `TF_LOG=DEBUG` log, each of which immediately precedes one request; the
provider logs no request bodies, so "same body" is shown by the outcome (same canary id, no new
version).

| # | Scenario | Requests (in order) | Result |
|---|---|---|---|
| E1 | Deploy change, `canary_percent = 0` | `PUT /apply` | Old version primary, new version canary at weight 0; post-apply plan empty. |
| E2 | `canary_percent = 25` | one `PUT /apply` (the shift); no deploy `PUT` | Same canary id as E1, weights 25; plan empty. |
| E3 | `canary_percent` removed | one `PUT /apply` (shift, percent omitted) | Rollout completed: `canary_version` null, primary is the E1 canary's id; plan empty. |
| E4 | Broken deploy held at 0 (decision 14) | `PUT /apply`, then `POST /rollback` | Apply failed with **Service Rollout Rolled Back**; old primary still primary and RUNNING; state kept the prior deploy fields; next plan non-empty. |
| E5 | Deploy change at `canary_percent = 100` (decision 3) | `PUT /apply` | **Completed** in the test org: the canary was promoted (`canary_version` null, new primary). Removing `canary_percent` afterwards sent no request (decision 6). The grace-period path stays covered only by the mock test. |

## Compatibility

Additive. Null keeps today's plans byte-for-byte; existing state decodes `canary_percent` as null.
No migration guide.
