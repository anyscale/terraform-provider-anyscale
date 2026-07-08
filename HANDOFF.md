# HANDOFF — terraform-provider-anyscale

_Last updated: 2026-07-08 (Compute Config API Sync wind-down)_

## Current Status

Compute Config API sync is assembled on integration branch **`integration/compute-config-sync`**
(tip `3a5165c`, main untouched), exposed via **PR #50** with `.changelog/50.txt`. Release verdict:
**MINOR**. **The CC12 per-node gate is CLEARED** — the worker-nested import test is written, has real
teeth, and is green (see below). Branch is READY to merge pending only the standard CI checks +
final independent verification. Merge remains shipwright's gate.

Scope: `anyscale_compute_config` resource + data source synchronized with the Platform API via the
design-first matrix CC1–CC15 (CC5b deferred). CC1–CC11 done+verified; CC12 top-level tested green;
CC13 = schema-contract pins; CC14 (cross-zone import phantom-diff) + CC15 (Dynamic-array List→Tuple)
found and fixed during the wind-down. See `QUEST-LOG.md`.

**CC12 status — fully proven, both levels.** Top-level `flags`/`advanced_instance_config` recovery on
import is tested green (mock-server + real-API), **including the array case** that surfaced CC15.
**Per-node** recovery (nested in `head_node`/`worker_nodes`) is now **also tested green** — the
user-directed worker-nested test (`f7e566f`, real teeth: fails on a wrong value, passes clean) proved
it round-trips to an empty plan on the first real run. **No forge fix was needed:** per-node
`flags`/`advanced_instance_config` are plain JSON strings (not the Dynamic structural type CC15
touched), and Go's `json.Marshal` output is byte-identical to `jsonencode` for realistic IAM-profile
shapes across two worker groups. The stale `ImportStateVerifyIgnore` skips were also removed, so these
fields are now genuinely asserted, not skipped. The per-node gap caught by scribe (confirmed by forge
+ shipwright) is resolved by real coverage, not a documented shrug.

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

- **Not yet merged to `main`.** Work is on `integration/compute-config-sync` (pushed to origin)
  and exposed via PR #50; the five hero branches remain intact. Cold-restart depends on the branch
  and PR surviving.
- **CI not fully green at hand-off:** changelog-gate + buildkite AWS e2e passed; the three GitHub
  Actions jobs (lint-and-unit, both acctest shards) were still pending. Confirm all green before
  merge. (`.changelog/50.txt` exists, so the changelog gate has a real fragment.)
- **GCP/K8s acceptance coverage is unrunnable in CI** (no cloud fixture in the test org) — an
  honest, disclosed gap (`_K8S` skip), not a regression. Real GCP/K8s validation needs fixtures.
- Wind-down process docs (`QUEST-LOG.md`, `HANDOFF.md`, `docs/heroes/RESUME-PROMPT.md`) are
  committed on the integration branch, so they will appear in the PR; drop them from the final
  merge if a clean provider-only PR is preferred.

## Next Work

1. **Merge when CI is green.** The CC12 per-node/worker gate is CLEARED (test `f7e566f`, green).
   Integration tip `3a5165c` is pushed to PR #50; branch builds, docs no-drift, vet/gofmt/unit clean.
   Confirm the three GitHub Actions jobs (lint-and-unit, both acctest shards) + changelog-gate +
   buildkite e2e are green, then shipwright merges PR #50 → `main`.
2. **(done) Claims reconciled** to full-green — top-level AND per-node import recovery are tested;
   PR #50 / this file / scribe's guide all match. shipwright: restore the PR body's coverage line
   from the interim known-gap disclosure to the now-true full-coverage statement at merge time.
4. **Deferred follow-ups (not blockers):** CC5b endpoint convergence (`ext/v0`→`api/v2`, handling
   the sweeper pagination-shape difference); `required_labels`; GCP/K8s test fixtures; optionally
   drop the wind-down process docs from the final merge if a provider-only PR is preferred.

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
