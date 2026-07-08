# HANDOFF — terraform-provider-anyscale

_Last updated: 2026-07-08 (Compute Config API Sync wind-down)_

## Current Status

Compute Config API sync is **implementation-complete and certified**, assembled on the
integration branch **`integration/compute-config-sync`** (main untouched). Release verdict:
**READY TO SHIP AS MINOR**. Pull request pending (delegated to tfp-shipwright). Not yet merged
to `main` and not yet Crystl-integrated beyond this branch.

Scope: `anyscale_compute_config` resource + data source synchronized with the Platform API via
the design-first CC1–CC12 matrix (CC5b deferred). See `QUEST-LOG.md` for the full narrative.

## Repository Health

- Integration branch merges: forge → assayer → scribe → architect(CLAUDE.md). **Zero conflicts.**
- `make build` OK (provider 0.1.2). `make docs` regenerates with **no drift**. `go vet` clean,
  `gofmt` clean, no conflict markers, working tree clean.
- **Unit suite green** (forge's 6 `physical_resources` fixture failures are resolved once
  assayer's fixes are integrated alongside forge's code).
- **Real-AWS acceptance suite green** on the integrated tree (independently re-run by assayer via
  `git archive` export): full matrix executes live, **one honest disclosed skip: `_K8S`** (the CI
  org has no GCP/K8s cloud fixture; documented, not claimed).
- Docs (resource, data source, guide) independently confirmed byte-identical by scribe.

## Decisions

- **Release = MINOR.** CC1 (attribute rename `physical_resources`→`required_resources`) and CC3a
  (`name` `RequiresReplace`) are breaking-change fragments; CC3b/CC5a-cross-zone/CC11/CC12 are
  fixes; CC2/CC4/CC6 additive. Rule: ≥1 breaking-change fragment ⇒ minor pre-1.0.
- **CC3 identity lifecycle.** `name` → `RequiresReplace` (plan-detectable rename). Cloud change →
  Update-time **error guard** (not RequiresReplace — needs offline-impossible name→id resolution),
  classified `fixed`. This is the opposite of Cloud C11's call, deliberately (lightweight
  versioned template vs heavyweight cloud).
- **CC5b deferred**, with rationale: sweeper search's body-vs-query pagination difference risks
  silent sweep truncation; fails the near-free bar for a non-blocking cleanup.
- **`api/v2` over `ext/v0`** codified in `CLAUDE.md` as provider-wide policy (trace each site's
  real shape before converting — not a pure rename).
- **CC12 import** recovers write-only `flags`/`advanced_instance_config` at ImportState only.

## Current Risks

- **Nothing is merged to `main` or opened as a PR yet.** The work exists only on
  `integration/compute-config-sync` (local) and the five hero branches. Cold-restart depends on
  this branch surviving and being pushed.
- **The `.changelog/<PR#>.txt` fragment does not exist yet** — its filename needs the PR number
  (repo convention). shipwright owns creating it at PR time from the drafted content. Until then
  the changelog gate will flag the PR.
- **GCP/K8s acceptance coverage is unrunnable in CI** (no cloud fixture in the test org) — an
  honest, disclosed gap (`_K8S` skip), not a regression. Real GCP/K8s validation needs fixtures.
- Wind-down process docs (`QUEST-LOG.md`, `HANDOFF.md`, `docs/heroes/RESUME-PROMPT.md`) are
  committed on the integration branch, so they will appear in the PR; drop them from the final
  merge if a clean provider-only PR is preferred.

## Next Work

1. **Open the PR** from `integration/compute-config-sync` → `main` (Mission 4; owner shipwright).
   Push the branch to origin first.
2. **Create the changelog fragment** `.changelog/<PR#>.txt` from the drafted breaking-change /
   fixed / feature blocks once the PR number exists.
3. **Crystl branch integration** of the hero branches (Crystl's mechanism), if still desired
   separately from the git integration branch.
4. **Deferred follow-ups:** CC5b endpoint convergence (`ext/v0`→`api/v2` for the DS search +
   CheckDestroy/exists helpers + sweeper, handling the pagination-shape difference);
   `required_labels` support; GCP/K8s test fixtures for real multi-provider coverage.

## Per-Hero Next Actions

- **tfp-shipwright** — Open the PR (summary/provider-changes/API-sync/tests/docs/breaking-changes/
  release-impact); create the `.changelog/<PR#>.txt` fragment; final release-readiness sign-off on
  the complete integration branch (incl. the CLAUDE.md commit).
- **tfp-forge** — None. CC1–CC12 shipped. On tap for the CC5b follow-up when scheduled.
- **tfp-assayer** — None. On tap to un-skip `_K8S` once a cloud fixture exists, and to add CC5b
  sweeper-pagination coverage if CC5b is picked up.
- **tfp-scribe** — None. On tap for CC5b docs / any post-review wording.
- **tfp-architect** — Final consistency check (Mission 5); dismiss heroes. Owns the CC5b design
  ruling if/when that follow-up is scheduled.
