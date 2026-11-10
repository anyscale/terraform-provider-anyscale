# Design: `anyscale_cloud` create must not leave a tainted cloud for a refused setting

**Status: approved by the user 2026-11-02. Gate 1-1 closed (real API, 2026-11-02); Gate 1-2 ships on the source trace. Gate 2 not started.** WORKBENCH #41edca48.
Product monorepo read-only; backend facts traced at product `origin/master` `fb1dbaef97d`
(2026-11-02). The local product checkout's default branch predates the converted-organization
guard, so trace that ref, not the working tree.

## Problem

`Create` saves the cloud to state right after `POST /api/v2/clouds`, then adds the cloud resource,
waits up to 30 minutes for readiness, and only then applies `auto_add_user`,
`lineage_tracking_enabled` and `aggregated_logs_enabled` through their single-field `PUT` routes
(`resource_cloud.go`, Create step 4). Any failure there errors `Create` after state exists, so
Terraform taints the cloud and the next apply **replaces** it.

The deterministic case: in an organization converted to role bindings, `PUT
/clouds/{id}/auto_add_user` always answers 409 "This organization uses role bindings"
(`converted_organizations.py` `refuse_legacy_permission_writes`). A config with
`auto_add_user = true` therefore always produces a tainted cloud there. The current diagnostic
tells the user to run `terraform untaint`.

## Backend facts (source trace)

- `CreateCloud.auto_add_user` exists, but the create path forces it to `false` in a converted
  organization and in a directory-synced one (`clouds_resource.py` around `:613-639` and
  `:827-849` at `fb1dbaef97d`), silently. Sending it in the `POST` would turn the taint into
  "inconsistent result after apply", and would change behavior for directory-synced organizations,
  whose post-create `PUT` is not refused today.
- No read-only signal of the organization's mode exists for plan time:
  `GET /api/v2/iam_migration/status` performs a write and needs manage-IAM.
- `DELETE /api/v2/clouds/{id}` removes the cloud record. For a cloud this provider registers, the
  practitioner's AWS/GCP resources are untouched.

## Decisions

| # | Question | Decision |
|---|---|---|
| 1 | Where `auto_add_user` is applied on create | Keep the early `State.Set` right after `POST /api/v2/clouds`, so a process that dies after the `POST` still leaves the cloud in state. Apply `auto_add_user` immediately after it, before the resource add and the readiness wait, when the plan sets it `true`. Gate 1-1 confirmed the `PUT` succeeds on a cloud still `CREATING`/`pending` with no resource. |
| 2 | Converted-organization 409 on that `PUT` | `DELETE` the just-created cloud, `resp.State.RemoveResource`, and return an error: the cloud was not created because the organization uses role bindings; set `auto_add_user = false` or remove it, and grant access with `anyscale_role_binding`. Nothing is tainted and nothing is orphaned; the next apply after the config fix is an ordinary create. |
| 3 | `DELETE` fails | Fall back to today's behavior: save state, error naming the cloud ID and the `untaint` + config fix. |
| 4 | Other post-create setting failures (`lineage_tracking_enabled` 403 when the org flag is off, a transient 409 that outlasts the retry) | Unchanged: taint plus the existing diagnostic. They are not deterministic, and deleting a ready cloud on a transient error is worse than a taint. |
| 5 | Update path | Unchanged; it already refuses with a diagnostic and nothing is tainted on update. |
| 6 | Empty clouds (`is_empty_cloud`) | **Confirmed bug** (Gate 1-1): Create returns before step 4, so `auto_add_user = true` is never sent, and the apply fails with "Provider produced inconsistent result after apply: .auto_add_user: was cty.True, but now cty.False", leaving a tainted cloud. Decision 1's placement fixes it, because the `PUT` now runs before the empty-cloud early return. Covered by the same changelog entry. |
| 6b | `lineage_tracking_enabled` / `aggregated_logs_enabled` on empty clouds | Same early return, so `true` should fail the same way (code read, not run). Fixed in the same change: the empty-cloud path applies them too, with decision 4 semantics (a failure taints with the existing diagnostic, no `DELETE`). Gate 2: a mock that fails before the fix. |

## Alternatives rejected

- **Send `auto_add_user` in the create request.** Silently forced false in converted and
  directory-synced organizations (above).
- **Plan-time refusal.** No read-only mode signal.
- **A non-tainting error.** Not possible: once `Create` has saved state, any error taints. Removing
  state without deleting the cloud orphans it.

## Compatibility

Behavior change only for converted organizations with `auto_add_user = true` at create: the apply
still fails, but leaves no cloud instead of a tainted one. Legacy and directory-synced
organizations end in the same state as today. No schema or state change. Changelog: bug fix.

## Verification

- **Gate 1-1 — closed 2026-11-02** (legacy test org, provider built from `main` at `f9bf9a4`): an
  `anyscale_cloud` with no embedded config and `auto_add_user = true` failed with the decision-6
  inconsistent-result error and was tainted, readback `auto_add_user = false`, `is_empty_cloud =
  true`. `PUT /api/v2/clouds/{id}/auto_add_user?auto_add_user=true` on that cloud then answered
  **200**, and `GET` showed `auto_add_user: true` with the cloud still `state: CREATING`, `status:
  pending`. `terraform destroy` removed it; `GET` returns 404.
- **Gate 1-2**: the converted-organization 409 is source-confirmed only. The shared test org must
  never be converted, so a real capture needs an org the user designates; otherwise ship on the
  source trace, as the existing `isOrgConvertedConflict` handling did.
- **Gate 2** (mock `resource.Test`; mutation-prove each). The mock returns the real converted 409
  body for the positive case and a different 409 body for the negative one.
  1. Converted 409 on create: `DELETE` targets the just-created cloud ID, no cloud-resource add
     before it, the address is absent from state after the `ExpectError` step (not only the error
     text), not tainted.
  2. `DELETE` failure: tainted, fallback text.
  3. A non-converted 409 on the `auto_add_user` `PUT` that outlasts the retry: **no `DELETE`**,
     tainted, existing diagnostic. Guards against a classifier that matches any 409.
  4. `lineage_tracking_enabled` 403 or an `aggregated_logs_enabled` failure at step 4: no `DELETE`.
  5. Legacy: one `PUT`, no `DELETE`. `auto_add_user` unset or false: no `PUT` at all.
  6. Ordering: the `PUT` precedes the cloud-resource add and readiness polling, asserted on an
     ordered request log, not counts.
  7. Decision 6: empty cloud with `auto_add_user = true` applies cleanly with one `PUT`.

## Revision 2026-11-09

Status: approved by the user 2026-11-09 (merge of #478).

Supersedes decision 4 and decision 6b's taint clause; revises decision 3. Decisions 1, 2, 5
and 6 stand. Gate 1 re-run the same day (real API, test org, throwaway cloud, deleted; log
`g1-cloud-settings.log`) confirmed every settings refusal we know of fires on a still-`CREATING`,
resource-less cloud, so the rollback `DELETE` is cheap and never loses a provisioned resource.

- **All three settings writes now run before the resource add** (`auto_add_user`, then
  `lineage_tracking_enabled`, then `aggregated_logs_enabled`), each a `PUT` only where the plan
  differs from the all-false create baseline. Decision 1 covered `auto_add_user` alone; this
  covers lineage and aggregated logs the same way, and covers empty clouds on all three.
- **Any settings-write failure on the fresh-create path rolls back** (decision 4 superseded):
  `DELETE` the cloud, `RemoveResource`, and an error saying the cloud was not created and
  nothing was left behind. No taint, no replace, no orphan.
- **Adopt-by-name never deletes** a cloud the provider did not create. A settings failure on an
  adopted cloud is instead a warning naming the setting; `Create` succeeds.
- **A failed rollback `DELETE` is a warning too** (decision 3 revised): it names the cloud ID,
  the setting failure and the delete failure, and `Create` succeeds, untainted.
- **Warning-exit semantics:** state records the planned setting value, so until the next
  refresh state misreports the setting — it over-reports a refused `true`, but under-reports
  an adopted cloud whose live value stays on the refused `false`. The warning says so. The
  next plan then shows an in-place Update of that setting, and a failed Update never taints.
- **Post-ready readback mismatch** (a setting the backend reset during `add_resource`): re-`PUT`
  once at most; a failure or a persistent mismatch becomes a warning, never an error.
- **Gate 1-b (2026-11-09, real AWS, 2 runs):** `aggregated_logs_enabled` applied before the
  resource add survives `add_resource` and readiness, so the re-`PUT` above is defensive only.
  Lineage could not be tested (the test org refuses enabling it); its survival is unverified.
- **Unchanged and out of scope:** `add_resource` failure and readiness-wait timeout still
  taint; the converted-organization 409 keeps its decision-2 text; the Update path is
  untouched.

Compatibility: behavior change only, no schema or state change. Clouds already tainted by
older versions stay tainted — `terraform untaint` clears them.
