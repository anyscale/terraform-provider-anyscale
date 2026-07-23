# Deferred: Terraform Actions adoption (`anyscale_system_cluster_terminate`)

**Status: fully designed, coded, tested, and real-infra confirmed — deliberately not shipped.**

This is not a stalled or half-finished effort. It stopped at the last step (opening the PR)
because of a floor-policy choice, not because anything about the work itself needed more time.
Nothing here should be redone from scratch when this gets picked back up.

## Why this is deferred

Terraform's Actions primitive (`action.Action` / `provider.ProviderWithActions`) requires
**Terraform 1.14 or later** — this is a Terraform Core version gate, not a documentation choice
or something this provider can work around. At the same time, the provider's first framework
adoption (ephemeral resources, `anyscale_service_credentials`) only needs **Terraform 1.10**,
and shipped on that basis.

The user chose to keep the provider's minimum documented Terraform version at 1.10 — exactly
what the already-shipped ephemeral resource needs — rather than bump the whole provider's floor
to 1.14 just to unlock this one action. That decision can be revisited at any time; it is not a
rejection of Actions as a pattern, and the code is kept ready for exactly that.

## What already exists (do not redo this)

- **Framework API trace** (against the vendored `terraform-plugin-framework` v1.19.0 source):
  `provider.ProviderWithActions.Actions()`; `action.Action{Metadata, Schema, Invoke}`;
  `action.ActionWithConfigure` for client injection via `ConfigureResponse.ActionData`;
  `InvokeRequest` carries only the practitioner's `Config` — no prior state, no plan diff, no
  link to a managed resource. `action/schema.StringAttribute` supports neither `Computed` nor
  `Sensitive` at all (hardcoded `false` in the framework source) — an action's schema can only
  ever describe practitioner-supplied input, never an output.
  - Actions are gated at Terraform 1.14+, but the framework's own documentation still describes
    the Action API as a **technical preview** as of this provider's framework version, even
    though its shape has had no breaking changes across several framework releases. Re-check
    this status before shipping — it may have changed to GA by the time this is revisited.
  - `terraform-plugin-testing` v1.16.0 has **no acceptance-test support for actions** (no
    `ActionInvoke` `TestStep` type) — only Go-level unit testing of `Invoke` against a mocked
    client is possible today. Re-check this too; if acceptance-test tooling for actions ships
    before this is revisited, the test suite should be upgraded to use it.

- **The code**, on the `pr2-terminate-action` branch (pushed to origin, not deleted — checkout
  directly, do not reconstruct):
  - `internal/provider/action_system_cluster_terminate.go` — `anyscale_system_cluster_terminate`,
    schema is just `cloud_id` (Required; see the Computed/Sensitive constraint above). `Invoke`
    calls `terminateSystemCluster`, maps HTTP 404 ("no System Cluster exists") and 409
    ("already Terminated") to distinct, clear diagnostics, reports progress via `SendProgress`,
    then polls for the `Terminated` state with a **bespoke wait loop** — deliberately not the
    resource's own `waitForSystemClusterState` helper, because that one is fail-fast on timeout
    (correct for a resource's `Create`/`Update`), which is the wrong shape for a fire-and-forget
    action: a timeout here means "confirmation is inconclusive," not "this failed," so it
    downgrades to a warning instead of an error.
  - `internal/provider/action_system_cluster_terminate_test.go` — unit tests against an
    `httptest` mock (no acceptance-test tooling exists, see above), covering success, both error
    codes, timeout, and context cancellation. **Gotcha for whoever extends this**: removing the
    early `return` after the terminate-error branch does not fail cleanly — it silently falls
    through into the real wait loop and hangs for the full default timeout. Bound any mutation
    test of this file with `go test -timeout <short>` rather than trusting a bare run to fail
    fast.
  - `internal/provider/provider.go` — `ProviderWithActions` assertion + `Actions()` registering
    the action; client wired through `ConfigureResponse.ActionData`.
  - `examples/actions/anyscale_system_cluster_terminate/action.tf` — a conservative,
    citation-backed example (standalone `-invoke` form only; deliberately does **not** show a
    `lifecycle.action_trigger` example, since auto-wiring a destructive terminate to another
    resource's lifecycle event is exactly the kind of surprising pattern this provider should
    not model as canonical).
  - The generated `docs/actions/system_cluster_terminate.md` page (from `make docs` against the
    combined branch) — hand-written `MarkdownDescription` covers the Terraform 1.14 floor, the
    technical-preview caveat, the exact `terraform apply -invoke=action.<type>.<label>` /
    `lifecycle.action_trigger` invocation syntax, and an explicit note that this action does
    **not** alter `anyscale_system_cluster`'s Terraform state.
  - `release-note:new-action` changelog-fragment type already exists in
    `tools/changelog-build/fragment.go` (it rode onto `main` via PR1's squash merge, since the
    type extension and the ephemeral-resource work shared a commit) — left inert and ready to
    use; no changelog fragment has been written for this action yet.

- **Real-infrastructure confirmation** (not just source-traced or unit-tested): a real
  `terraform plan` against a live cloud (`cld_dei5p9apgb61ynnqy6nnuk82wf`) showed exactly
  `0 to add, 0 to change, 0 to destroy, 1 to invoke` with nothing else touched; a real
  `terraform apply -invoke=action.anyscale_system_cluster_terminate.example` then ran through
  the actual CLI path a user would use (not just a Go-level `Invoke` call), terminated a real
  System Cluster (`ses_2iapcdmfbmetu88txutknwdtji`), streamed real progress messages
  (`Terminating` → `Terminating` → `Terminated`) over ~31 seconds, and was independently
  re-verified against the live API afterward (status `Terminated`, both URL and auth fields
  `null`). Critically, the same `apply` run's `anyscale_system_cluster` resource state was
  confirmed to stay exactly `Running` throughout — the "does not alter resource state" doc claim
  was observed live, not just asserted.

- **Review**: design, code, and docs independently reviewed and signed off by the team
  (framework API trace and code-correctness review; unit-test coverage plus an independently
  reproduced mutation-proof of the early-return hang bug above; doc accuracy, including catching
  and fixing a real invocation-syntax error before it shipped). Nothing outstanding was left
  open when this was set aside.

## Revisiting this

When the provider's minimum Terraform version is ready to move to 1.14 (or Actions otherwise
becomes acceptable to ship):

1. `git checkout pr2-terminate-action` (or cherry-pick/rebase its commits onto current `main` —
   expect real drift by then; do not assume it applies cleanly).
2. Re-verify against current `main`: `make build`, `make test`, `make docs`, `make docs-validate`.
3. Re-check the two "re-check this" callouts above (Actions' GA/technical-preview status, and
   whether `terraform-plugin-testing` has gained real acceptance-test support for actions since).
4. Re-run the real-infra confirmation (a fresh `terraform plan` + `apply -invoke` against a real,
   **dedicated throwaway** System Cluster, never the shared fixture — terminate is destructive)
   rather than trusting this document's snapshot — the backend may have changed since. Provision
   the throwaway cluster on a **VM cloud (AWS/GCP)**, not EKS/GKE: System Cluster on a K8S compute
   stack is gated behind the `enable_task_dashboard_k8s` backend feature flag and 501s outright if
   that flag is off for the org, whereas VM has no such gate.
5. Open it as a normal PR through the usual process (own `.changelog/<PR#>.txt` fragment using
   the already-existing `new-action` type; workbench item #90 tracks this).

## Why only `terminate` (not `stop`/`restart`/`start`)

The original ask hypothesized stop/restart/terminate. Tracing the real backend
(`system_workload_helpers.go`) found only `terminate` is a clean, standalone backend primitive:
`start` is `describeSystemWorkload(startCluster=true)`, tangled with a create-on-read hazard;
`restart` has no backend primitive at all (would be a synthetic terminate-then-start composite);
`disable` is a config toggle (`enableSystemCluster(false)`), not a live stop — the resource has no
`Stopped` state to represent one. Whoever revisits this should keep modeling Actions 1:1 to real
backend operations rather than synthesizing composite verbs the backend doesn't actually support.

Full design detail (the complete v1.19.0 framework trace, acceptance criteria, and the
architect/scribe/assayer/forge review history) is in this quest's chat log and design brief;
this file is the durable, in-repo summary meant to survive after that context is gone.
