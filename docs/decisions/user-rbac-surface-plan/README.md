# Design record: user-management and RBAC surface

**Status: decisions recorded; the work they govern has shipped except where marked open.**

Companion to [`../rbac-surface-consolidation/README.md`](../rbac-surface-consolidation/README.md)
(the design this measures against) and
[`../user-management-surface-audit/README.md`](../user-management-surface-audit/README.md) (the prior
audit, measured at `0e862fd`).

Measured against `543e796`, at which `anyscale_cloud_access` is registered with its authoritative
write path live (#260, #261, #262 merged, unreleased).

> **Measure against `origin/main`, not a local checkout.** Both the prior audit and the first draft
> of this record were written three commits behind, and reached conclusions that inverted on
> re-measurement — the audit's headline, "cloud access is not manageable," was true of what it read
> and false of `main`. The consolidation record is also 1949 lines on `main` and was 272 at that
> older commit, so a ruling can be made against a design document that is missing the section
> governing it. Name the commit, or the finding rots.

---

## What a practitioner can do

| Task | Released (`v0.25.1`) | On `main`, unreleased |
|---|---|---|
| Add a person to the organization with a role | works | works, unchanged |
| Grant access to one cloud | **not possible** | works, authoritatively |
| Grant a project role | **not possible** | via `cloud_access.member[*].projects` |
| Revoke a person's cloud access | **not expressible** | expressible |

The capability gap the prior audit recorded is real in the released provider and already closed in
code. Organization-scope management (`anyscale_organization_user` / `anyscale_organization_user_role`
/ `anyscale_organization_invitation`) is sound and is endorsed as-is.

---

## Ruling 1 — an authoritative first apply must disclose the revoke at plan time

`anyscale_cloud_access` is authoritative from the first apply: anyone not declared in `member` is
revoked, including on create. The consolidation record's own Create ruling identifies the hazard and
states it has no available signal — the people about to lose access have never been read by
Terraform, so they appear nowhere in the plan.

That "no signal" claim is true of signals derivable from **configuration**. It is not true of
`ModifyPlan`, which may issue API calls. Confirmed by real execution rather than a source trace: a
probe warning was added to `ModifyPlan`, a binary built, and `terraform plan -json` run against a
scratch config. Core **does** render a `ModifyPlan`-added warning on create with `State` null.

**Ruling: read and write may ship in one release, provided the first-apply revoke is disclosed at
plan time.** The disclosure is strictly better than the release staging it substitutes for — it
protects every apply, not only those that followed an import.

Four conditions, none optional:

1. **Name the members** who would lose access. A count is not disclosure.
2. **Warning, not error** — a legitimate first apply must still proceed.
3. **Fail loudly if the member-list read fails**, stating the revoke is therefore undisclosed. A
   guard that silently no-ops on a failed read restores the exact hazard it exists to remove while
   looking like protection. Use the established `(value, determined)` pattern, not a bare bool.
4. **State its own blind spot** — organization admins cannot be enumerated.

On (4): admins being invisible means they also cannot be *revoked* here, and `main` refuses outright
to declare one. The population genuinely at risk is a cloud owner who is **not** an org admin, and
that person *is* visible and therefore disclosable. Coverage matches what an apply can affect.

**Status: implemented and verified.** `discloseFirstApplyRevoke` meets all four conditions. A null or
empty `member` map is deliberately **not** skipped — declaring nobody is precisely when every remote
member is revoked. Two acceptance tests, both mutation-proof; the load-bearing one is the negative:
changing the read-failure branch to silently return "determined, nobody found" left the success test
green and was caught only by the failure test. Both drive a real `terraform plan -json` against a
built binary, because `resource.Test`'s reattach path cannot surface `ModifyPlan` warnings at all.

**Cost: neither bounded nor opt-out.** Three API calls minimum, ~21 on a 500-member cloud, and it
runs only while state is null — the few plans before one create, never again. An opt-out on a safety
disclosure is an attractive nuisance: whoever disables it to quiet the warning is the population it
protects. A bounded list would print a truncated set that reads as complete, the same failure mode
rejected under condition (4). Revisit with data if real usage makes it bite.

### Superseded reasoning, recorded because the correction generalises

This ruling was first argued on the grounds that the design staged read before write and that #261
collapsed that staging without deciding to. That was wrong. **J.18** of the consolidation record says
the opposite as a release constraint: *"Read and write ship in one release, or neither ships yet."*
Shipping a read-only-only tag would manufacture the very population that round existed to protect.
#261 followed the design.

The outlier was a `provider.go` comment promising the write path would ship separately — which
contradicted J.18 from the moment J.18 was ruled, and was wrong before any gate moved. It has since
been corrected, along with five further artifacts describing a write gate that no longer exists.

What survives is narrow and is the only part that was ever load-bearing: J.18 governs *when* the two
paths ship and says nothing about the undisclosed first-apply revoke. The disclosure closes that.

## Ruling 2 — the write gate is removed, not retained

`cloudAccessWriteEnabled` was a `var` permanently set to `true`, kept non-`const` so its branches
would not fold away as dead code. A gate that cannot close is not a safety mechanism; its only
remaining function was keeping alive a refusal diagnostic telling practitioners the write path had
not shipped. Gate, both constants, `refuseWriteWhileReadOnly` and its three call sites are removed.

J.18 is marked spent in the consolidation record — **met, not abandoned**: the write path was enabled
before any tag, so no released version ever carried the read-only-only shape it existed to prevent.
Its reasoning is left standing as history rather than rewritten to match current state.

## Ruling 3 — import recovers the full remote member set, unfiltered

There is no declared-set filter at import and there must not be one. Filtering to the declared set
would make state claim the cloud contains only the people in configuration; `plan` would then show no
diff, and undeclared members would be revoked on the next apply invisibly. Unfiltered import is what
makes the plan honest — state holds the true population, so a configuration that omits someone shows
a real diff.

The two mechanisms cover different entry points and neither is redundant: the plan-time disclosure
protects whoever never imported; unfiltered import protects whoever did. The resource page already
states this in user-facing terms.

A standing diff from a failed revoke is **intended**, not a perpetual-diff defect: J.12 rules that
`unmanaged_grants` never influences planning and every plan re-attempts what is outstanding. The
alternative is a resource that claims authority over a population and quietly tolerates a member it
failed to remove.

## Ruling 4 — build nothing on the group axis

Anyscale has a group primitive (`/api/v2/user_groups`: list, read memberships, create, rename,
delete, set roles) and a declarative binding surface (`/api/v2/resource_policies`, set semantics over
cloud, project and organization, principals being groups rather than users). Neither is usable.

**Group membership is written exclusively by WorkOS directory-sync webhook events**
(`dsync.group.user_added` / `dsync.group.user_removed`). The DAO has membership writers; nothing
reaches them by any other route — no endpoint, no CLI, no SDK. Groups are keyed to WorkOS group IDs.
Directory sync is not available to customers, so **no group can hold a member.**

That blocks the whole axis, not merely membership. A resource over `resource_policies` would bind a
role to a group nobody can join: it would apply cleanly, report success, and grant access to nobody —
worse than not shipping it. **Revisit only when directory sync is live.**

Two consequences:

- `templates/guides/rbac.md`'s `for_each`-over-emails guidance is **correct as written**. An earlier
  draft of this record called it a workaround steering practitioners onto the least scalable path;
  that was wrong. It is the idiomatic Terraform answer and the only one available. A `locals` map
  driving several resources gives a named, single-source-of-truth team definition; a module gives a
  reusable one. Neither needs anything from the provider.
- **A state-only "group" resource is rejected.** It would have no remote counterpart, so it could not
  drift, refresh, or be meaningfully imported — a variable wearing a resource costume — while
  implying to anyone reading the registry that it creates something in Anyscale. When a real API
  lands there would be two things called groups and no clean path between them, since a state-only
  object cannot be upgraded into a remote one and the identities would not match.

**The generalisable error:** this finding originally read routers and concluded capability. Code
presence is not product availability. The consumption-path rule catches a surface with no CLI or SDK;
it does **not** catch one whose mechanism is switched off for customers, and the monorepo cannot
reveal which those are. When a design depends on a feature being *live* rather than merely
implemented, that is a product question, not a grep.

### What survives independently

- **Service accounts** — create, list, delete, rotate API key, with a real CLI and SDK path and no
  provider surface today. The strongest remaining candidate. Two constraints: `create` mints a
  **100-year** API key as a *client-side CLI convention*, not a server default, so the provider must
  choose that TTL rather than inherit it; and the returned key is a secret, which is **Ephemeral
  Resource** territory, not `Sensitive`-in-state.
- **`GET /api/v2/scim/list-user-permissions`** — the only *effective* permissions read in the
  product, direct plus inherited, across clouds and projects. It carries no directory-sync gate and
  its own docstring describes it as a tool for verifying access *while migrating to* SCIM, explicitly
  excluding group details: a pre-SCIM surface whose path prefix made it look dependent. Three caveats
  before building: it requires an org-owner token, so a non-owner needs an explicit diagnostic rather
  than an empty result; it is migration-oriented, so its shape may change or retire; and only its
  route definition has been read — **the real-response-shape gate is unmet**, and is a cheap
  read-only call.

**Schema constraint:** `is_service_account` is a query **filter** only and `is_sso_user` is
create-input only. Neither is readable from the collaborator model, so neither can be drift-detected
and neither may be modelled as a `Computed` attribute.

## Finding — the organization surface is tested almost entirely against mocks

The green checkmark means less than it appears. Measured against the three long-registered
organization resources:

- In CI the **entire** RBAC resource surface is mock-only. `ANYSCALE_TEST_USER_EMAIL` and
  `ANYSCALE_TEST_INVITATIONS` are never set by `ci.yml`, so every real-API user, invitation and role
  test skips on every run. A skip summarizer reports this; nothing gates on it.
- `anyscale_organization_user_role` has `ImportState` **implemented and zero import tests**.
- The `orgSelfModification` guard has **no test at all** — not the predicate, not the diagnostic. The
  only related machinery is a real-infra *precondition* that avoids triggering the case.
- **No refresh-drift test anywhere in this surface**, for any of the three resources.
- No sweeper for organization users or roles. A role test dying mid-run leaves a real identity's
  `base_role` and `additional_roles` mutated with nothing to restore them.
- Three data-source acceptance tests assert only that a `.#` count attribute is set, which holds for
  an empty list, and so cannot fail.

Drift tests belong on the mock, not real infrastructure: out-of-band mutation against the real API
means mutating a shared disposable identity mid-test with no sweeper to restore it.

---

## Open

- **Real-API tests in CI** — agreed in principle, with one sequencing constraint: close the
  mock-based gaps first. Pointing real credentials at tests that currently cannot fail buys flakiness
  and rate limits, not signal. Fixture identities must follow the user-fixture rules.
- **The effective-permissions data source** — blocked on its real-response-shape check above.
- **Enumeration cost of the disclosure** on very large clouds. Dropping the disclosure is not on the
  table; bounded-vs-opt-out is the decision if it ever bites.

## Alternatives rejected

- **Editing the `provider.go` comment to match the code, before the disclosure existed.** That would
  have documented the collapse rather than earning it. Disclosure first; then the comment describes
  something true.
- **Retaining the write gate with corrected text.** Keeps an unreachable branch and a diagnostic that
  can never be read — the same "looks like protection, isn't" shape rejected in condition (3).
- **Stubbing the group axis**, on evidence from this repo rather than principle: `cloud_access`
  shipped as 441 lines of schema with every CRUD method refusing, and produced a resource nobody
  could use, a registration comment that outlived its own decision, and a changelog fragment
  describing behaviour that changed underneath it. A stub is not a placeholder; it accrues.
- **Expanding the current release to cover service accounts or the data source.** New types with
  their own contracts; folding them into a release already carrying a breaking change delays it.
- **Treating the missing project-role write surface as its own workstream.** Closed by
  `cloud_access.member[*].projects`, and the backend enforces cross-field invariants only a nesting
  resource can check at plan time.

## Confirmed vs assumed

**Confirmed by direct reading** during this work: the `cloud_access` states at each commit; the stale
`provider.go` comment; the WorkOS-only membership write path and the absence of any member add/remove
endpoint; the `list-user-permissions` route definition and its permission dependency; the
`organization_user.md` self-contradiction and `README.md` staleness.

**Confirmed by execution, not source-tracing:** that `ModifyPlan` may call the API on create and have
Core render the resulting warning. The disclosure ruling rests on this.

**Reported by evaluation, not personally re-verified** — high confidence, confirm before acting: the
upstream endpoint and enum inventory; the CLI/SDK consumption-path matrix; the test-coverage claims
above.

**Assumed:** that no artifact beyond those already corrected describes `cloud_access` as read-only or
non-authoritative. Verify by grep rather than review.
