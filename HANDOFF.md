# HANDOFF — terraform-provider-anyscale

_Last updated: 2026-07-24 (Compute Config Import & SDK Parity — implementation complete,
integration branch built and locally validated, holding for L10 review + real-infra case)_

## Current Status

**All four lanes (design, implementation, tests, docs) are functionally complete.** Full agenda
delivered: 7 correctness fixes (node `cloud_deployment` write-path 422, `required_resources.memory`
unit-string parsing, not-found handling for both symptoms, worker-name uniqueness, worker
reorder-stability, `max_nodes` bounds), 3 enhancements (`required_labels`, `name:version` import,
`cloud_name` reverse-lookup on import), 3 validators (2 warning-level, 1 hard, per a
backend-enforcement-driven Tier-1/Tier-2 split), the full multi-resource feature
(`additional_resources`, Option C — see Locked Design below), and a late-caught, confirmed-safe
addition (GAP-3: recover more fields on import instead of leaving them null).

**Integration branch `brent/compute-config-cleanup` is built off current `origin/main` (v0.21.0,
`76fc6e8`) and locally validated** — all three lane branches (`crystl/tfp-forge`,
`crystl/tfp-scribe`, `crystl/tfp-assayer`) merged with zero conflicts, combined `make
build`/`fmt`/`lint`/`test`/`vet`/`docs`/`docs-validate` all green. **Not yet pushed.** Holding on:
architect's L10 review of the combined diff, assayer's independent validation + the multi-resource
real-infra acceptance case (a dedicated 2-`anyscale_cloud_resource` cloud, 24h teardown), then push
+ open one PR to `main`. The user merges after CI is green; no shard self-merges.

**No SchemaVersion bump anywhere in this agenda.** Recommended a single MINOR version release
(new capabilities + fixes, zero Breaking Changes fragments). Changelog draft is complete at
`.crystl/quest/DRAFT-changelog-fragments.txt` (shared quest state); the real `.changelog/<PR#>.txt`
gets written in a follow-up commit once the PR number is known, per this repo's own convention.

## Repository Health

Base: `main @ 76fc6e8` (v0.21.0) is the last released state. Compute config's prior schema
version stays 1 (no bump this quest — see Compatibility below). Integration branch build/test/
lint/docs all pass; the multi-resource real-infra acceptance case is the one piece not yet run
against real infrastructure as of this update.

## Locked Design (final)

**Import identity unchanged, extended.** `config_id` (the `cpt_` id) stays canonical — pins
exactly one immutable version, already satisfied "retain exact version" before this quest.
`terraform import` now *also* accepts a `name:version` string (colon-count discriminator; `cpt_`
ids proven colon-free so there's no ambiguity), resolving to the matching `config_id` and erroring
clearly if the name is ambiguous across clouds. `cloud_name` is now recovered on import too, so a
`cloud_name`-configured resource plans clean immediately after import instead of showing a
one-time diff.

**Not-found handling fixes two distinct bugs, not one.** A typed not-found sentinel in
`api_helpers.go` (compute_config's `Read`/`ImportState` only — a provider-wide version of this
pattern is a flagged follow-up, not done here) makes Read remove a genuinely-deleted (404)
resource from state — self-heals automatically on the next refresh — **and** stops a transient
500/network error from being misread as "not found" and silently wiping a *healthy* resource
(this second case does not self-heal for a resource already wiped before upgrading; remediation
is a fresh `terraform import` of the real `config_id`, the mirror-image of the ImportState phantom
case which needs `terraform state rm` instead).

**`custom_resources` fractional values are now a hard plan-time error, not a warning** — the one
item that changed classification mid-quest after a live `terraform apply` proved the current
behavior is an outright Terraform Core crash (`provider produced inconsistent result after
apply`), not a silent, discoverable success. Since nothing with this shape works today, the hard
rejection breaks nothing. `instance_type`+`required_resources` co-setting and an unrecognized
`market_type` stay **warning-level** (both genuinely apply successfully today per live checks, so
a hard error there would be a real breaking change).

**Multi-resource support: Option C (additive generalization), not a new resource type.**
`MultiResourceComputeConfig` is a real, `customer_hosted_only`-tagged SDK model the provider never
touched. Per the user's direct correction, `customer_hosted_only` means "created via `cloud
register`" — exactly this provider's own `anyscale_cloud`/`anyscale_cloud_resource` path — so it
*is* real-infra-testable, not an environment gap as first assumed. Implementation: the existing
top-level fields keep meaning exactly the primary `anyscale_cloud_resource`'s config, byte-identical
to every existing single-resource user; a new optional `additional_resources` list block covers
any further cloud resources on the same cloud (`cloud_resource` required as the per-entry key).
Read/flatten matches wire entries back to (primary, additional[]) by `cloud_resource` **name**, not
position (backend order isn't guaranteed) — the same reorder-to-match-prior idiom already used for
`worker_nodes`, reused here after a genuine crash bug (an alphabetical sort of a non-Computed list)
was caught and fixed before it ever left forge's branch. A wholly separate resource type was
considered and rejected: it would make import ambiguous (which resource type owns a given `cpt_`
id?) and force a destroy/recreate to convert an existing single-resource config to multi.

**Two things almost shipped wrong, both caught before landing — worth knowing about, not just
fixing:**
1. **GAP-3** (recovering `resources`/`required_resources`/`labels`/`required_labels`/node
   `cloud_deployment` on import instead of leaving them null) was verified safe early but fell out
   of the final numbered fix agenda with no recorded decision — a scribe catch while writing
   final docs. Now implemented; a reminder that a full design's individual findings need explicit
   reconciliation against the final locked agenda, not just individual confirmation.
2. **The `docs/guides/*.md` vs `templates/guides/*.md` clobber trap.** Scribe's substantial guide
   rewrite landed on the *generated* `docs/guides/compute-config.md` rather than its
   `templates/guides/` source — invisible until the mandatory combined `make docs` regen at
   integration time nearly overwrote it. Fixed (both in scribe's own branch and reflected in the
   merge). **Any future guide edit belongs in `templates/guides/`, never the generated `docs/guides/`
   output** — same rule the resource/data-source doc pages already follow.

## Current Risks / Known Limitations (disclosed, not blockers)

- `additional_resources` reordering in a user's own `.tf` still shows a plan diff — same List
  semantics as `worker_nodes` (a user-driven reorder isn't hidden, only a *backend*-driven one is).
  Not a bug; matches existing precedent.
- A cold multi-resource **import** has no signal for user intent on which entry should be primary
  vs. additional — the deterministic fallback (first in API response order) is real but arbitrary.
  Scribe has landed a required guide callout for this; confirm it survived final integration.
- The worker-name / `cloud_resource`-name collision protections are proven safe at the
  Terraform-state-storage layer (live-verified); whether a genuine *runtime* collision (two
  identically-named worker groups reaching the underlying Ray autoscaler) actually shadows one is
  unverified without launching a real cluster — documented as a real, not just theoretical, risk.
- F7's fully-defensive "ambiguous multi-resource shape" diagnostic path has no live/Gate-2
  verification — it's structurally unreachable through anything this provider itself sends, so
  this is an accepted, disclosed gap, not a blocker.

## Next Work

1. tfp-assayer: multi-resource real-infra acceptance case (dedicated 2-`cloud_resource` cloud,
   24h teardown) against `brent/compute-config-cleanup`; independent validation of the combined
   branch.
2. tfp-architect: L10 integration review of the combined diff.
3. tfp-shipwright (me): once 1-2 pass, push `brent/compute-config-cleanup` and open one PR to
   `main`. Add the real `.changelog/<PR#>.txt` in a follow-up commit once the PR number exists.
   User merges after CI is green — no shard self-merges.
4. tfp-scribe: confirm the cold-multi-resource-import docs callout and the `templates/guides/`
   fix both survived into the final merged tree (should already be true per this update).

## Per-Hero Status

- **tfp-architect** — full design + contract owner throughout (parity matrix, import-identity
  contract, semantics/normalization doc, the Option C schema design, every Tier-1/Tier-2/severity
  ruling). Owes the L10 combined-diff review.
- **tfp-forge** — implemented the entire fix/enhancement/validator/multi-resource agenda across 3
  commits on `crystl/tfp-forge`; caught and fixed the additional_resources ordering crash bug
  himself before it shipped. Available for anything the L10 review or real-infra case surfaces.
- **tfp-assayer** — built and mutation-proof-verified the full regression suite plus both
  "retain exact version" acceptance proofs (AG-1/AG-2); found the `custom_resources` crash live;
  owns the multi-resource real-infra case now.
- **tfp-scribe** — full docs pass (guide, examples, import.sh, MarkdownDescription wording
  handoffs to forge); caught the GAP-3 scope-drop and the DS-side multi-resource parity gap; fixed
  her own templates/docs clobber mistake same-session.
- **tfp-shipwright (me)** — compat/changelog/release lane throughout; assembled and validated the
  integration branch; caught the docs-template clobber during the mandatory combined regen. Owns
  the eventual push + PR once L10/real-infra clear.
