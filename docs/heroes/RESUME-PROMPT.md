# Resume Prompt — terraform-provider-anyscale

Paste-ready context for the next session. Read these three first, in order:

1. **`HANDOFF.md`** (repo root) — current status, repository health, decisions, risks, next work,
   and per-hero next actions. This is your single source of "where things stand."
2. **`QUEST-LOG.md`** (repo root) — dated engineering journal; the newest entry explains *why* each
   change was made (the CC1–CC12 rationale), not just what.
3. **`CLAUDE.md`** (repo root) — repo conventions and agent behavior. Note especially the
   **"API generations: always prefer `api/v2`"** policy added this session.

## Where the work is

- Branch **`integration/compute-config-sync`** holds the complete, integrated Compute Config API
  sync (main untouched). It is built, docs-regenerated (no drift), unit-green, and **real-AWS
  acceptance-green** — independently verified four times (architect, assayer ×2, shipwright), one
  honest disclosed `_K8S` skip.
- The five hero branches (`crystl/tfp-{forge,assayer,scribe,shipwright,architect}`) remain intact.
- Release verdict: **MINOR**. Provider tip on `crystl/tfp-forge` is `8e5cef1`; certification tip
  is `crystl/tfp-assayer@d6fd629`; integration tip is `1cbd8e6`.

## First actions on resume

1. **Confirm the integration branch still reflects reality:** `git checkout integration/compute-config-sync`,
   then `make build && make docs` (expect no drift) and `go test ./...` (expect green). If `main`
   moved since 2026-07-08, rebase/re-verify before opening the PR.
2. **Open the PR** from `integration/compute-config-sync` → `main` if not already open (owner:
   tfp-shipwright). Push the branch to origin first. PR body: summary, provider changes, API
   synchronization, tests executed, documentation updated, breaking changes, release impact
   (all in `HANDOFF.md` / `QUEST-LOG.md`).
3. **Create the changelog fragment** `.changelog/<PR#>.txt` from the drafted breaking-change /
   fixed / feature blocks (filename needs the real PR number — repo convention). Until it exists,
   the changelog gate will flag the PR.

## First assumption to verify

That `origin/main` has not advanced since integration (base was `f5325a5`). If it has, the
integration branch must be rebased/re-merged and re-verified before the PR is trustworthy —
do not assume the four green verifications still hold against a moved base.

## Deferred follow-ups (not blockers)

- **CC5b** — converge the data-source search + CheckDestroy/exists helpers + sweeper off
  `ext/v0/cluster_computes` onto `api/v2/compute_templates`. Trace each site's real request/response
  shape first; the sweeper's body-vs-query pagination is a genuine code change with a silent
  sweep-truncation failure mode. See `CLAUDE.md` API-generations note.
- **GCP/K8s test fixtures** — provision cloud fixtures in the test org so the `_K8S` skip (and GCP)
  can actually run in CI.
- **`required_labels`** support (deferred: niche + heavy validation coupling).

## Consider delegating

The provider-facing design rulings live with tfp-architect; implementation with tfp-forge; tests/CI
with tfp-assayer; docs with tfp-scribe; release/PR with tfp-shipwright. Re-summon the relevant hero
for the follow-up rather than doing it solo.
