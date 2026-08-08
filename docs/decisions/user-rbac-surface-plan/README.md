# Plan: user-management and RBAC surface

**Status: assessment and plan, no code change.**

Follow-up to [`../user-management-surface-audit/README.md`](../user-management-surface-audit/README.md)
(measured at `0e862fd`) and to the design it measures against,
[`../rbac-surface-consolidation/README.md`](../rbac-surface-consolidation/README.md).

## Measurement points — read this before trusting any claim below

Every finding below names which state it was measured against.

| Label | Commit | `anyscale_cloud_access` state |
|---|---|---|
| **STALE** | `ab2c3b5` | not registered, pure schema/validation stub, zero API calls |
| **MAIN** | `543e796` (`origin/main`) | registered, **authoritative write path live**; #260, #261, #262 all merged |

**`MAIN` is the only state that matters.** Everything below is measured against `origin/main` unless
it explicitly says otherwise.

`ab2c3b5` is recorded only because it is a trap: it is what every local checkout in this working set
carried, three commits behind `origin/main`, and it is what the prior audit and the first draft of
*this* document were measured against. A fast-forward closes the gap.

Three findings in this document's first draft turned on that staleness. Twice the conclusion
inverted. The prior audit's headline — "cloud access is not manageable" — was correct for what it
measured and wrong about `main`. So: **name the commit, or the finding rots.** Where a claim below
was withdrawn on re-measurement, the withdrawal is left in place rather than deleted, so the
correction is legible.

---

## The four tasks, answered

Framed around use, not files — matching the prior audit so the two can be diffed.

| Task | `ab2c3b5` (stale local) | MAIN (`543e796`, merged, unreleased) |
|---|---|---|
| (i) Add a person to the org with a role | works | works, unchanged |
| (ii) Grant one cloud | **not possible** | works, authoritatively |
| (iii) Grant a project role | **not possible** | via `cloud_access.member[*].projects` |
| (iv) Revoke a person's access | **not expressible** | expressible at cloud scope |

So the capability regression the prior audit recorded is real in the **released** provider — `main`
is unreleased — and already closed in the code. What follows is about the gaps that survive.

---

## Finding A — one shipped artifact still describes the resource as read-only

**Severity: blocks tagging. Narrower than first filed.** Measured at MAIN.

**Withdrawn on re-measurement, recorded so the correction is legible.** This finding was first filed
against three artifacts, on the strength of a branch that predated #261. Two of the three do not
exist on `main`:

- `.changelog/260.txt` — **gone**. #261 replaced it with `.changelog/261.txt`, which is accurate and
  unusually thorough: it states that the resource is authoritative, that anyone undeclared is
  revoked *including on the first apply*, the structural refusals, the two-instance mutual-revoke
  hazard, and a drift limitation nothing else in this surface documents — an administrator changing
  a role through the console or CLI writes through a different store than this resource reads, so
  most such changes plan clean while the real role has moved. Better than what this document would
  have specified. Read it before drafting release notes.
- `docs/resources/cloud_access.md` — rewritten in #261/#262; no read-only text remains.

**What survives, and it is on `main`, not on a branch:** `internal/provider/provider.go:142-146`
still reads *"anyscale_cloud_access is registered READ-ONLY: refresh and import work, and the three
write methods refuse with an explicit error... the write path ships in its own release rather than
this one."* Every clause is false at MAIN.

That is a comment rather than a user-facing string, so it misleads maintainers rather than
practitioners — a smaller blast radius than first claimed, but a live one: it is the only remaining
in-repo statement of the *staging decision*, and it now contradicts the code it sits above. See
Finding B for why that contradiction is not fixed by editing the comment.

**Residual sweep:** any remaining schema `MarkdownDescription`, guide, or example characterising
`cloud_access` as read-only or non-authoritative. The two largest instances are already closed.

## Finding B — an authoritative first apply revokes people the plan never shows

**Severity: was highest in this document. CLOSED — disclosure implemented and accepted.**
Measured at MAIN.

**Corrected after this finding was first written. The correction is against this document's own
reasoning and is left in place rather than rewritten away.**

This finding originally claimed the design staged read before write, that #261 collapsed that
staging, and that the collapse had never been decided. All three are wrong. Ruling **J.18** of the
consolidation record says the opposite, in terms:

> Do not tag a release containing a read-only-only `anyscale_cloud_access`. That one act manufactures
> precisely the population whose breakage this round is meant to prevent... **Read and write ship in
> one release, or neither ships yet.** This is the strongest available argument for the sequencing
> and it is a release constraint, not a preference.

So #261 *followed* the design. The outlier was the `provider.go` comment claiming the write path
would ship in its own release — which contradicted J.18 from the moment J.18 was ruled, and was
therefore wrong before the gate ever moved, not made stale by it.

**Cause, and it is this document's own stated failure mode:** the consolidation record is 1949 lines
on `main` and 272 lines at `ab2c3b5`. J.18 is not in the short version. This finding ruled on a
design document while holding an obsolete copy of it — after twice telling others to name the commit
before trusting a claim. Third instance in one assessment of the same root cause.

**What survives.** J.18 rules on *when* read and write ship. It does not address the separate hazard
that an authoritative first apply revokes people who appear nowhere in the plan — which the design
record's own Create ruling names as having no available signal. The disclosure below supplies one.
That is a genuine addition, and it is the only part of this finding that was ever load-bearing.

A third option dissolves the tradeoff, and it is now confirmed to work. The design record's claim
that a first-apply revoke "has no signal" is a claim about signals derivable from **configuration**.
It is not true of `ModifyPlan`, which can issue API calls — `main` already does so for its org-admin
and caller-identity guards.

**Confirmed by real execution**, per the repo's gate rather than a source trace: a probe warning was
added to `ModifyPlan`, a binary built, and `terraform plan -json` run against a scratch config.
Terraform Core **does** render a `ModifyPlan`-added warning on Create with `State` null, attributed
to the correct resource. Probe reverted; tree clean.

### Ruling

**`anyscale_cloud_access` may ship its read and write paths in one release, provided the first-apply
revoke is disclosed at plan time.** The staging existed solely to give practitioners a version in
which they could see a cloud's real members before anything could revoke them. Plan-time disclosure
delivers that guarantee inside a single release, and delivers it better — it protects every apply,
not only those that happened to follow an import.

The condition is not satisfied by any warning. It must:

1. **Name the members** who would lose access. A count is not disclosure.
2. Be a **warning, not an error** — a legitimate first apply must still proceed.
3. **Fail loudly if the member-list read fails**, stating that the revoke is therefore undisclosed.
   A guard that silently no-ops on a failed read restores the exact hazard it exists to remove while
   looking like protection. Use the established determined-flag pattern, not a bare bool.
4. **State its own blind spot** — organization admins are invisible to the roles endpoint and cannot
   be enumerated.

On (4), a point that strengthens the ruling rather than weakening it: org admins being invisible
means they also cannot be *revoked* by this resource — it cannot revoke what it cannot see. `main`
additionally refuses outright to declare one. The population genuinely at risk is the design
record's stated worst case, a cloud owner who is **not** an org admin, and that person *is* visible
and therefore *is* disclosable. The disclosure covers exactly the dangerous population.

### Status: implemented and accepted

Filed as unmet — `ModifyPlan` carried only the empty-member-set guard — and closed during this
assessment. `discloseFirstApplyRevoke` runs from `ModifyPlan` on create only, and was verified
against all four conditions by reading the code and tests rather than by report:

- Names the members with a join, not a count.
- `AddWarning` on both branches; the plan still proposes the create either way.
- The read-failure branch states that the plan **cannot name** who would be revoked, on the
  `(value, determined)` shape already used by `organizationRequiresSSO`.
- States the org-admin blind spot *and* why it is not a coverage gap: an admin is invisible to the
  disclosure exactly as they are to Create's own reconcile, so the warning's reach matches what the
  apply can affect.

One subtlety it gets the right way round: a null or empty `member` map is **not** skipped, because
declaring nobody is precisely the case where every remote member is revoked.

Two acceptance tests, both proven mutation-proof. The load-bearing one: changing the read-failure
branch to silently return "determined, nobody found" left the success test green and was caught
*specifically* by the failure test — which is what demonstrates the second test carries weight
rather than riding along. Both drive a real `terraform plan -json` against a built binary, because
`resource.Test`'s reattach path cannot surface `ModifyPlan` warnings at all.

**Cost ruling: neither bounded nor opt-out.** Three API calls minimum, ~21 on a 500-member cloud,
and it runs *only* while state is null — the handful of plans before one create, never again. An
opt-out on a safety disclosure is an attractive nuisance: whoever disables it to quiet the warning
is the population it protects. Bounding would print a truncated list that reads as complete — the
same failure mode rejected for the org-admin case. Revisit with data if real usage makes it bite.

## Finding C — the provider models the per-user axis; the product's Terraform-shaped axis is groups

**Severity: strategic. This is the largest genuine gap, and it is not a gap the current design was
trying to fill.** Measured against the upstream API and CLI, not against provider code.

Every RBAC surface this provider models is per-user. The product's most Terraform-shaped RBAC
surface is group-based and almost entirely unmodelled:

- **`/api/v2/resource_policies`** — `PUT|GET /policy/{resource_type}/{resource_id}`, plus a list by
  type. Declarative, full-replace **set** semantics over bindings, covering cloud, project **and**
  organization. Principals are **user groups, not users**.
- **`/api/v2/user_groups`** — `GET /`, `GET /memberships/list`, `POST /` (create, name only),
  `PATCH /{id}` (rename), `DELETE /{id}`, `PUT /{id}/roles`.

> **RETRACTED, 2026-08-07 — the group axis is blocked entirely, and this finding's recommendation
> was wrong at its highest-priority item.** Directory sync is not available to customers. Group
> membership is written *exclusively* by WorkOS directory-sync webhook events
> (`dsync.group.user_added` / `dsync.group.user_removed`); the DAO has membership writers and nothing
> reaches them by any other route — no endpoint, no CLI, no SDK. Groups are keyed to WorkOS group IDs
> (`delete_user_group_by_workos_group_id`), i.e. they are directory-shaped objects, not native ones.
>
> With no directory sync, **no group can ever hold a member.** That invalidates the ordering below at
> item 1, not merely at the membership item already excluded: `anyscale_resource_policy` was ranked
> first precisely because it binds *existing* groups and needs neither creation nor membership — which
> only holds if groups can be populated some other way. Binding a role to a group nobody can join
> grants access to nobody. It would apply cleanly, report success, and do nothing, which is worse than
> not shipping it.
>
> **Build nothing on the group axis.** Not the policy resource, not the groups data source, not
> membership. Revisit only when directory sync is actually live.
>
> Two consequences recorded so they are not re-derived:
> - `templates/guides/rbac.md`'s `for_each`-over-emails guidance is **correct as written**. This
>   document previously called it misleading and had it frozen; both were errors. Unfrozen, unchanged.
> - Service accounts survive independently — real CLI and SDK path, no dependency on any of this.
> - The effective-permissions data source **also survives**, re-checked rather than assumed.
>   `GET /api/v2/scim/list-user-permissions` carries no directory-sync gate and no feature flag; it
>   requires only org-update permission, and its own docstring states it is "intended to help
>   customers verify access configuration **while migrating to** SCIM" and that "the response does not
>   include user-group details." It is a *pre*-SCIM tool by design — the `/scim/` path prefix is what
>   made it look like it fell with the rest. Three caveats before building: it needs an org-owner
>   token, so a non-owner must get an explicit diagnostic rather than an empty result; it is
>   migration-oriented, so its shape may change or retire when SCIM lands; and only its route
>   definition has been read — **Gate 1 (real response shape) is unmet** and is a cheap read-only
>   call.
>
> **The generalisable error:** this finding read routers and concluded capability. Code presence is
> not product availability. The repo's consumption-path rule catches a surface with no CLI or SDK; it
> does **not** catch a surface whose mechanism is switched off for customers, and the monorepo cannot
> reveal which those are. When a design depends on a feature being *live* rather than merely
> implemented, that is a question for the user, not for grep. Fourth correction in this assessment,
> and the second of this exact shape.

**Correction, and it narrows this finding materially.** The first draft called the group axis simply
"unmodelled," which overstates what is available. Verified directly against the router: there is
**no endpoint to add or remove a group member**. Membership is written by SCIM directory sync from
the customer's IdP, not by the API. So group *definition* and group *role binding* are modellable
today; group *membership* — the operation anyone would actually reach for — is not, and is expected
to remain so for roughly six months.

That is a smaller gap than first filed, and it changes the shape of the fix rather than its
direction: membership being IdP-owned is a clean ownership boundary, not a blocker. It is the same
split as IAM roles versus SSO assignments — the identity provider owns *who*, Terraform owns *what
they can do*. Most of the value here does not require owning membership.

The consumption-path test inverts our current priorities. Per repo policy a surface needs a real
end-user consumption path before it is exposed. `resource_policies` is the **only writable RBAC path
in the product with both a CLI (`anyscale policy set/get/list`) and a public SDK
(`anyscale.policy`)**. Meanwhile organization role writes, cloud role writes, cloud collaborator
update/delete/list, and project collaborator list/update/delete are all **console-only** — no CLI,
no SDK. We built on the paths that fail that test and skipped the one that passes it.

This is corroborated from inside our own code rather than only from upstream: `main` already calls
`/api/v2/policy/cloud/{cloud_id}` for a group-policy guard. `cloud_access` has to defend against
group-granted access precisely because groups are the real mechanism.

**Consequence for documentation.** `templates/guides/rbac.md` tells practitioners there is no group
concept and to use `for_each` over emails as "the closest thing to a group." Groups exist and are
bindable to roles declaratively. That guidance steers people onto the least scalable and least
supported path. It should not be rewritten until the replacement is decided.

Two adjacent gaps, both with full CLI **and** SDK coverage and zero provider surface:

- **Service accounts** — create, list, delete, rotate API key. The cleanest candidate in the whole
  area, and exactly what practitioners want in Terraform for CI identities. Two design constraints:
  `create` mints a **100-year** API key as a *client-side CLI convention*, not a server default — so
  modelling it means choosing that TTL ourselves rather than inheriting one; and the returned key is
  a secret, which is **Ephemeral Resource** territory, not `Sensitive`-in-state.
- **`GET /api/v2/scim/list-user-permissions`** — the only *effective* permissions read in the entire
  product: direct plus group-inherited, per user, across clouds and projects. The natural
  drift-detection data source.

**Schema constraint worth recording now:** `is_service_account` is a query **filter** only and
`is_sso_user` is create-input only. Neither is readable from the collaborator model, so neither can
be drift-detected and neither may be modelled as a `Computed` attribute.

## Finding D — the surface is tested almost entirely against mocks

**Severity: the green checkmark means less than it appears.** Measured at `ab2c3b5`; every claim
here concerns the three long-registered organization resources and is unaffected by the `cloud_access`
merges, except the final paragraph.

- In CI, the **entire** RBAC resource surface is mock-only. `ANYSCALE_TEST_USER_EMAIL` and
  `ANYSCALE_TEST_INVITATIONS` are never set by `ci.yml`, so every real-API user, invitation, and
  role test skips on every run. A skip summarizer reports this; nothing gates on it.
- `anyscale_organization_user_role` has `ImportState` **implemented and zero import tests** — the
  largest untested implemented path here.
- The `orgSelfModification` guard has **no test at all** — not the predicate, not the diagnostic.
  The only related machinery is a real-infra *precondition* that refuses to point the test at the
  authenticated identity, which prevents the case rather than testing it.
- **No refresh-drift test anywhere in this surface.** Nothing mutates the backend out of band and
  asserts a non-empty plan, for any of the three registered resources.
- No sweeper for org users or roles. Invitations have one and its prefix genuinely matches. A role
  test dying mid-run leaves a real identity's `base_role` and `additional_roles` permanently mutated
  with nothing to restore them.
- Three data-source acceptance tests are tautological — each asserts only that a `.#` count
  attribute is set, which holds even for an empty list: `OrganizationUsersDataSource_Basic`,
  `UserDataSource_CloudAccess`, `UserDataSource_MultipleFields`.
- Stale `CURRENTLY FAILS` comments on two role tests that now pass. Live code described as broken
  invites someone to "fix" the code to match the comment.

The one `cloud_access` acceptance test must be re-checked against MAIN: at `ab2c3b5` it is
*unconditionally* skipped, and an unregistered resource cannot be acceptance-tested at all — so if
that skip survived the merges, a green CI proves nothing about the authoritative revoke.

## Finding E — docs are good prose that nobody can reach

Measured at `ab2c3b5`. Concerns the organization surface only, so unaffected by the `cloud_access`
merges.

`docs/guides/rbac.md` is genuinely strong — it explains the base/deny mechanism, the
`collaborator`-means-three-things trap, `write` vs `writer`, and why destroying a role resource can
*grant* access. It has **zero inbound links** from any resource page, and `templates/index.md.tmpl`
carries no guide index at all.

Two concrete defects, both verified directly:

- `docs/resources/organization_user.md` **contradicts itself on the same rendered page**: the body
  says the resource sends an invitation; the embedded example says "This resource cannot create
  members." The body is current; the example predates the resource gaining invite-on-create.
- `README.md` describes the resource by its old name and claims it "manages membership and role" —
  role management moved out in v0.25.0. `anyscale_organization_user_role` appears in the README
  **zero** times.

*Not* a defect, recorded so nobody fixes it: the `cloud_user_role` removal is absent from
`CHANGELOG.md` because `.changelog/259.txt` has not been folded yet. That is the normal release flow.

---

## Plan

Sequenced by risk, not by size. Each step names its compatibility class.

**Step 1 — implement the plan-time revoke disclosure (Finding B). Behaviour-affecting; blocks
tagging.** This is the only step that must land before the next tag. `ModifyPlan`, on create, reads
the cloud's current member set and warns naming every member who would lose access, meeting all four
conditions of the ruling.
*Acceptance, and both halves are required:* an acceptance test where a member exists on the cloud,
is absent from configuration, and the plan output **names them**; and its negative, where the
member-list read fails and the plan states that disclosure did not run. Both proven mutation-proof —
introduce the regression, confirm the test fails, revert byte-clean.
*Open sub-question, not blocking:* enumeration cost per plan, and whether the `count=50` pagination
limit bites on a large cloud. If expensive, that is a bounded-vs-opt-out decision, not a reason to
drop disclosure.

**Step 0 — artifact sweep. Additive; blocks tagging; sequenced AFTER Step 1.**
Correct `internal/provider/provider.go:142-146` and any residual read-only or non-authoritative text
in schema descriptions, guides, and examples. The two largest instances (`.changelog/260.txt`,
`docs/resources/cloud_access.md`) are already closed on `main`.
*Deliberately second:* fixing the comment while the hazard is still undisclosed documents the
collapse rather than earning it. Disclosure first, then the comment describes something true.
*Acceptance:* grep, not review, shows no artifact describing `cloud_access` as read-only or
non-authoritative.

**Step 2 — close the test gaps that make CI's green meaningless (Finding D). Non-functional.**
Priority order: `organization_user_role` import tests; a real test for the `orgSelfModification`
guard; a refresh-drift test for each registered resource; delete or fix the three tautological
tests; correct the stale `CURRENTLY FAILS` comments. Then decide whether CI should set the real-API
env vars or whether the skip summarizer should gate.
*Acceptance:* each new test is proven mutation-proof — introduce the regression, confirm the test
fails, revert byte-clean.

**Step 3 — documentation reachability (Finding E). Additive.**
Fix the `organization_user` self-contradiction at its source under `examples/`, not in `docs/`; fix
the README lines; add inbound links to the RBAC guide; add a guide index to `index.md.tmpl`.

**Step 4 — the group axis (Finding C). Additive, new types, own release.**
Not part of the current release. Ordered so that the missing membership API is never on the critical
path:

1. **`anyscale_resource_policy`** — binds an *existing* group to a role on a cloud, project, or the
   organization. Highest value, and it needs no group-creation or membership API at all, because it
   references groups the customer's IdP already syncs. Set semantics, and `sync_status` is
   eventually consistent, so it must poll to confirm convergence.
2. **`anyscale_user_groups` data source** — read groups and their memberships. Zero lifecycle risk,
   and it lets configurations reference IdP-managed groups by name today.
3. **Service accounts**, with an Ephemeral Resource for the minted key. Note the 100-year TTL is a
   CLI-side convention, not a server default, so the provider must choose rather than inherit it.
4. **A data source over `scim/list-user-permissions`** — the only effective-permissions read in the
   product.

**Deliberately excluded: group membership management.** There is no API. Do not stub one.

**Ruling against stubbing, on evidence from this repo rather than principle:** `cloud_access` shipped
as 441 lines of schema with every CRUD method refusing. The result was a resource nobody could use,
a registration comment that outlived the decision it recorded, a changelog fragment describing
behaviour that changed underneath it, and simultaneous work measured against three different commits.
A stub is not a placeholder; it accrues. If a membership API lands, `anyscale_user_group` absorbs it
additively — which is the test this ordering is built to pass.

---

## Alternatives considered and rejected

- **Fix the `provider.go` comment to match the code.** Rejected: resolves a decision/code
  disagreement in favour of whichever moved last, and silently discards a safety ruling.
- **Rewrite `templates/guides/rbac.md`'s "no groups" guidance now.** Rejected: the guidance is
  wrong, but the correct replacement depends on Step 4's design. Replacing wrong advice with
  provisional advice on a page whose whole value is authority is worse than leaving it pending.
- **Expand the current release to cover groups or service accounts.** Rejected, and independently
  flagged from release engineering. These are new resource and data-source types with their own
  contracts; folding them in would delay a release that already carries a breaking change.
- **Treat the missing project-role write surface as its own workstream.** Rejected: it is closed by
  `cloud_access.member[*].projects` on MAIN, and the backend enforces cross-field invariants that
  only a nesting resource can check at plan time.

## Open decisions — these are the user's, not ours

Decisions 1 and 2 of the first draft were closed by fact rather than by choice: #260, #261 and #262
are already merged to `main` and unreleased, so the next tag carries the authoritative write path
whatever anyone would have preferred, and the read/write staging is already collapsed.

The remaining three were **answered by the user** during this assessment:

1. **Does the tag wait for Step 1?** — **Yes.** The disclosure lands before the tag. The user asked
   to see the plan output first; the shape was shown and accepted in principle.
2. **Should CI run real-API user/invitation tests?** — **Yes.** With one sequencing constraint added
   here: close the mock-based test gaps in Step 2 *first*. Pointing real credentials at tests that
   currently cannot fail buys flakiness and rate limits, not signal. Fixture identities must follow
   the user-fixture rules — never a colleague's account.
3. **Is the group axis wanted?** — **Yes, in the reduced form Finding C now describes.** The user
   correctly doubted that Groups was usable; verification showed why. Membership stays IdP-owned;
   the provider models role bindings and reads.

What remains genuinely open:

- **Enumeration cost of the plan-time disclosure** — calls per plan, and whether `count=50`
  pagination bites on a large cloud. If expensive, the choice is bounded enumeration or an opt-out.
  Dropping the disclosure is not on the table.
- **Whether `templates/guides/rbac.md`'s group guidance is rewritten now or with Step 4.** It
  currently tells practitioners to use `for_each` over emails as "the closest thing to a group,"
  which is defensible only while nothing better exists. Frozen until Step 4's shape is fixed.
## Confirmed vs assumed

**Confirmed** — read directly from code, or from branch objects, during this assessment: the three
measurement points and their `cloud_access` states; the stale `provider.go` comment and
`.changelog/260.txt` text; the absence of a write-path fragment; the `organization_user.md`
self-contradiction; the `README.md` staleness and the zero occurrences of
`anyscale_organization_user_role`; the existence of `.changelog/259.txt`.

**Reported by evaluation, not personally re-verified** — treat as high-confidence but confirm before
acting: the upstream endpoint and enum inventory in Finding C; the CLI/SDK consumption-path matrix;
every test-coverage claim in Finding D; the RBAC-guide inbound-link and `index.md.tmpl` claims in
Finding E.

**Was assumed, now confirmed:** that `ModifyPlan` can issue an API call on create and have Core
render the resulting warning (Finding B). This was the one load-bearing assumption in the plan; it
was settled by a real probe-and-plan run, not by a source trace, and the ruling rests on that.

**Still assumed:** that the residual sweep in Finding A turns up nothing beyond the `provider.go`
comment. Verified by grep, not by review, before Step 0 is called done.
