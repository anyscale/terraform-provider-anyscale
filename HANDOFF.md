# HANDOFF — terraform-provider-anyscale

_Last updated: 2026-07-25 (Compute Config Import & SDK Parity — merged as PR #215 (e582fe7) and
shipped in released version v0.22.0; a post-push CI regression was found and fixed same-day
before merge. Release independently verified: published body, checksums, GPG signature, and
Terraform Registry pickup all confirmed clean.)_

## Post-push incident (fixed before merge, shipped clean in v0.22.0)

PR #215's first push (20a977c) introduced a real, provider-wide regression missed by every
pre-push review: the new `ErrNotFound` sentinel (added for compute_config's own 404 handling)
changed `DoRequestRaw`'s 404 error text from `"unexpected status 404: ..."` to `"resource not
found: ..."`. Six already-shipped, unrelated call sites - `resource_service.go` (x2),
`data_source_project.go`, `data_source_service.go`, `resource_cloud_resource.go`, and
`resource_project.go` - detected 404s via `strings.Contains(err.Error(), "404")` and silently
stopped recognizing real 404s once the digits disappeared from the text. CI caught it (two
pre-existing service tests went red), not any design or code review.

**Fixed same-day, CI green on the fix commit (a287b5d):** restore the exact legacy phrase
verbatim inside the wrapped error - `fmt.Errorf("%w: unexpected status %d: %s", ErrNotFound,
status, body)` - so `errors.Is(err, ErrNotFound)`, the bare `"404"` substring, and the
phrase-anchored `UnknownServiceIDErrors` test assertion are all satisfied at once, with zero
edits to any of the 6 call sites. Landed with a permanent guard test
(`TestDoRequestRaw`'s new 404 subtest, asserting both properties, mutation-proven against the
wrong `"(HTTP 404)"`-only wording that was tried and rejected first). No changelog fragment
needed - the regression never reached a released version. Full incident detail in
`.crystl/quest/DRAFT-changelog-fragments.txt`'s Resolution-history section.

## Current Status

**Full agenda delivered and real-infra-validated.** Every item ratified through this quest's
design levels landed: 7 correctness fixes, 3 enhancements, 3 validators (2 warning-level, 1
hard, per a backend-enforcement-driven Tier-1/Tier-2 split), the full multi-resource feature
(`additional_resources`, Option C), and GAP-3 (recover more fields on import). One planned
enhancement (A3, `cloud_name` reverse-lookup on import) was built, then **reverted** after real
acceptance testing found it couldn't satisfy two conflicting guarantees at once — see below.

**Real acceptance testing (not just the unit suite) found three genuine bugs the static/unit
layer structurally could not catch**, all now fixed and verified with real `resource.Test` runs:
1. `required_resources.memory` crashed apply outright (Terraform Core "provider produced
   inconsistent result after apply") once the unit-string parsing fix landed - fixed via a
   `MemoryQuantityType`/`MemoryQuantityValue` custom type with `StringSemanticEquals`, verified
   both for fresh state and for an existing plain-string state decoding cleanly under the new type
   (no SchemaVersion bump needed - confirmed by running it, not by trusting the framework docs).
2. Worker-name disambiguation (F5) silently never fired on a fresh Create for two same-
   `instance_type` workers with no name set - root cause was two (later three, including a
   duplicated loop in the new `additional_resources` expand path) `IsNull`-only checks missing
   the `Unknown` case a fresh-create's `UseNonNullStateForUnknown` plan modifier produces. All
   three call sites fixed and covered by permanent, CI-gated regression tests.
3. A3 (`cloud_name` reverse-lookup on import) first caused a real regression on plain
   `cloud_id`-only imports (a spurious one-time diff, since the recovery ran unconditionally
   regardless of which selector the config used) - fixing that via `Optional+Computed+
   UseStateForUnknown` (mirroring `cloud_id`) then broke a *different*, pre-existing,
   v0.2.0-documented guarantee: switching an existing config from `cloud_name` to the equivalent
   `cloud_id` must clear `cloud_name` cleanly (CC3b, a test that predates this quest entirely).
   No single plan modifier gives both "absorb import recovery cleanly" and "clear on explicit
   removal" for the same droppable Optional input.

**A3 is REVERTED** (`cloud_name` back to plain Optional, byte-identical to pre-quest behavior)
rather than patched a third time - architect's call, reached after weighing a live redesign
against the cost of retrofitting unvalidated work into an already-clean state. The known
limitation A3 was trying to close (a `cloud_name`-configured resource imported by `cpt_` id shows
one benign, self-healing diff on its first post-import plan) is unchanged from before this quest
- a documented limitation, not new.

**Full 37-test real-infra acceptance suite against the final combined branch: 36 pass, 1 expected
skip (K8S — no K8S cloud in this test org), zero failures.** The first fully clean run this quest
had. Architect's final fanned-out re-review (3 subagents: correctness/accidental-changes,
test-strength/placebos/CI-sharding, fixture-cleanliness/comments) came back FUNCTIONALLY READY
with one outstanding item: a comment-cleanliness pass (quest-process narration - agent names,
gate IDs like "G1g", commit hashes - in shipped code comments) that an earlier pass missed some
of. That cleanup is the one thing between here and the push.

**No SchemaVersion bump anywhere in this agenda, including the reverted A3.** Recommended a
single MINOR version release (new capabilities + fixes, zero Breaking Changes fragments in this
PR). Changelog draft is at `.crystl/quest/DRAFT-changelog-fragments.txt` (shared quest state,
outside this repo); the real `.changelog/<PR#>.txt` gets written in a follow-up commit once the
PR number is known, per this repo's own convention.

**Backlog (fast-follow, not in this PR):** the user explicitly authorized breaking the
`cloud_id`/`cloud_name` switching guarantee for this resource specifically ("nobody is using this
particular resource yet... better break it today and not in 5 months") - too late in this quest
to safely build and validate before tonight's push, so it's logged as an immediate follow-up
(added to the project backlog by architect) rather than retrofitted here. See "Next Work" below
for the concrete direction.

## Repository Health

Base: `main @ 76fc6e8` (v0.21.0) is the last released state; none of its 5 commits ahead of the
quest's starting point touch compute_config. Integration branch `brent/compute-config-cleanup`:
build/vet/gofmt/lint (0 issues)/unit-suite (379 pass, 0 fail)/docs/docs-validate all green; 37-test
real-infra acceptance suite 36 pass / 1 expected skip / 0 fail. Not yet pushed as of this update.

## Locked Design (final, for this PR)

**Import identity**: `config_id` (the `cpt_` id) stays canonical, pins exactly one immutable
version - already satisfied "retain exact version" before this quest. `terraform import` now also
accepts a `name:version` string (colon-count discriminator; `cpt_` ids proven colon-free, so no
ambiguity), resolving to the matching `config_id` and erroring clearly on a cross-cloud-ambiguous
name.

**Not-found handling fixes two distinct bugs.** A typed not-found sentinel in `api_helpers.go`
(compute_config's `Read`/`ImportState` only - a provider-wide version is a flagged follow-up, not
done here): a genuinely-deleted (404) config self-heals on the next refresh; a transient
500/network error no longer gets misread as "not found" and silently wipes a healthy resource
(this second case does NOT self-heal for a resource already wiped before upgrading - remediation
is a fresh `terraform import` of the real `config_id`, the mirror of the ImportState phantom case
which needs `terraform state rm` instead).

**`custom_resources` fractional values are a hard plan-time error, not a warning** - the one item
whose classification flipped mid-quest once a live `terraform apply` proved the current behavior
is an outright Core crash, not a silently-wrong success. Nothing with this shape works today, so
the hard rejection breaks nothing. `instance_type`+`required_resources` co-setting and an
unrecognized `market_type` stay warning-level (both genuinely apply successfully today).

**Multi-resource support: Option C, additive generalization, not a new resource type.** The
existing top-level fields keep meaning exactly the primary `anyscale_cloud_resource`'s config,
byte-identical for every existing single-resource user; a new optional `additional_resources`
list block covers further cloud resources on the same cloud. Read/flatten matches wire entries
back to (primary, additional[]) by `cloud_resource` name, not position - the same
reorder-to-match-prior idiom used for `worker_nodes`, reused here after forge caught and fixed a
real ordering-crash bug (alphabetical sort of a non-Computed list) before it ever left his branch.

**Two things almost shipped wrong, caught before landing:**
1. GAP-3 (recovering `resources`/`required_resources`/`labels`/`required_labels`/node
   `cloud_deployment` on import) was verified safe early but fell out of the numbered fix agenda
   between L3 and L4 with no recorded decision - a scribe catch; restored once flagged.
2. The `docs/guides/*.md` vs `templates/guides/*.md` clobber trap - scribe's guide rewrite landed
   on the generated output rather than its template source, invisible until the mandatory
   combined `make docs` regen at integration nearly overwrote it. Any future guide edit belongs in
   `templates/guides/`, never the generated `docs/guides/` output.

## Current Risks / Known Limitations (disclosed, not blockers)

- A `cloud_name`-configured resource imported by `cpt_` id shows one benign, self-healing diff on
  its first post-import plan (unchanged from before this quest - A3 tried to close this and was
  reverted; see Backlog).
- `additional_resources` reordering in a user's own `.tf` still diffs (same List semantics as
  `worker_nodes` - a user-driven reorder isn't hidden, only a backend-driven one is).
- Cold multi-resource IMPORT has no signal for which entry is primary vs. additional - the
  deterministic fallback (first in API response order) is real but arbitrary; scribe has landed a
  required guide callout for this.
- Worker-name / `cloud_resource`-name collision protections are proven safe at the
  Terraform-state-storage layer; whether a genuine runtime collision reaching the underlying Ray
  autoscaler actually shadows one worker group is unverified without launching a real cluster.
- F7's fully-defensive "ambiguous multi-resource shape" diagnostic path has no live/Gate-2
  verification - structurally unreachable through anything this provider sends, accepted per
  CLAUDE.md's documented-stable-behavior allowance.
- F5's worker-name fix prevents new collisions; it cannot retroactively fix a collision already
  baked into an existing applied state (indistinguishable from a real explicit name by then).

## Next Work

**This PR: DONE.** Merged as `e582fe7` on `main`, released as `v0.22.0` (2026-07-25) - GitHub
Release verified published (not draft/prerelease), body byte-matches `CHANGELOG.md`'s `[0.22.0]`
section, all 9 signed assets present with checksums and GPG signature independently re-verified,
Terraform Registry confirmed serving `0.22.0` as latest.

**Backlog / fast-follow #1 (this week, explicit user direction):** cloud_id/cloud_name breaking
redesign. Direction sketched by forge: `cloud_name` stays `Optional+Computed` (fixes the plain
`cloud_id` import case that drove A3), and a config that drops an explicit `cloud_name` in favor
of `cloud_id` no longer auto-clears it automatically - a disclosed, intentional
`release-note:breaking-change` with migration text, the opposite trade-off from what CC3b protects
today. Needs its own design pass, implementation, a real CC3b-equivalent test proving the *new*
intended behavior, and full re-validation - not a quick patch. Full context (why A3 was reverted,
the exact plan-modifier tension, the three real acceptance-test findings) lives in
`approved-design.md`'s CC3b section and this entry.

**Backlog / fast-follow #2 (added by architect after the post-push 404 incident):** migrate the
6 legacy `strings.Contains(err.Error(), "404")` call sites - `resource_service.go` (x2),
`data_source_project.go`, `data_source_service.go`, `resource_cloud_resource.go`,
`resource_project.go` - to `errors.Is(err, ErrNotFound)`, the sentinel's actual intended purpose
and the correct long-term end state (this release's fix, "Option B," restored the legacy error
text verbatim so all 6 sites keep working unchanged - a deliberate, scoped, non-breaking fix
under CI-red pressure, not the final architecture). Requires per-site tracing that every error
path at each of the 6 sites genuinely flows through `DoRequestRaw` before converting it - the
one thing that made rushing this into #215 risky (a site whose error comes from elsewhere would
silently stop detecting 404s under a naive migration). Not urgent - today's fix is safe and
tested - but worth doing deliberately rather than leaving fragile string-matching as the
permanent state now that a real sentinel exists.

## Per-Hero Status

- **tfp-architect** - full design + contract owner throughout; ran the L10 integration review and
  the final fanned-out (3-subagent) re-review; made the CC3b revert call and the ship-now
  recommendation to the user.
- **tfp-forge** - implemented the entire fix/enhancement/validator/multi-resource agenda plus all
  three real-bug fixes found during acceptance testing; caught two real bugs in his own work
  before they shipped (the `additional_resources` ordering crash, and confirming the A3 revert
  empirically rather than assuming it).
- **tfp-assayer** - built and mutation-proof-verified the full regression suite, the two "retain
  exact version" acceptance proofs, and found all three real bugs plus the CC3b regression via
  live `resource.Test` runs against the integrated branch; owns the 36/37 clean baseline.
- **tfp-scribe** - full docs pass across every fix and the A3 revert; caught the GAP-3 scope-drop
  and the DS-side multi-resource parity gap; fixed her own templates/docs clobber mistake
  same-session (twice, self-caught both times).
- **tfp-shipwright (me)** - compat/changelog/release lane throughout; assembled, merged, and
  repeatedly re-validated the integration branch through several rounds of real bug fixes; caught
  the docs-template clobber during the first combined regen; owns the push once the last cleanup
  commit lands.
