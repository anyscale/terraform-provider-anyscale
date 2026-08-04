# Design record: RBAC surface consolidation

**Status: shipped** (PR #228, squash-merged as `9dd5ecc`, 2026-08-01; released in v0.25.0,
2026-08-03). This document reconstructs the design record that PR #228's own commit messages cite
repeatedly, at this exact path, as their rationale source — the file was referenced throughout that
PR's development but never actually committed. It is assembled from the shipped code, PR #228's
squashed commit history, and the user-facing guide that history describes as this design's
"first-class deliverable" (`docs/guides/rbac.md`, criterion 20 of this design).

## Scope

What this consolidation shipped, in one PR:

- **New:** `anyscale_organization_user_role` — one organization member's `base_role` and
  `deny_roles`, keyed by email.
- **Breaking:** `anyscale_organization_user` loses `permission_level`, `base_role`, and
  `additional_roles` — role management moves to the new resource above (the organization-role split,
  below). These two changes shipped together deliberately: landing either half alone would put two
  resources writing the same organization role field through two different endpoints, in two
  different vocabularies, at the same time.
- **Breaking:** `anyscale_project` loses its `collaborator` block, with a state upgrader (every
  existing `anyscale_project` state carries the key regardless of whether it was ever set). There
  is deliberately no in-provider replacement yet — collaborators stay console/API-managed until a
  resource models a cloud's members together with their project roles. That resource is
  `anyscale_cloud_access`, below, which is why the two changes belong to the same design even
  though they shipped at different levels of completeness.
- **Groundwork, not shipped:** `anyscale_cloud_access` — schema, `ValidateConfig`, and the
  empty-member-set `ModifyPlan` guard only. It is **not registered** in `provider.go` and its CRUD
  methods unconditionally refuse to run. It carries no changelog entry for the same reason: no
  registration, no docs page, no reachable user surface yet.

## Vocabulary collisions

The organization, cloud, and project scopes don't just use different words for access levels —
they reuse the **same** words for **different** things. Four instances, all now documented in
`docs/guides/rbac.md`:

1. **`collaborator` means three different things**: the non-owner organization role, one of six
   possible cloud `base_role` values (an unrelated concept), and the Anyscale console's own display
   name for the legacy `write` permission level.
2. **Project `write` and cloud `writer` are not the same role**, despite being near-homographs, and
   the typo trap runs in one direction: the cloud role's real spelling is `writer`, but a project
   only accepts `write` — sending `writer` to a project 422s.
3. **Project `readonly` and cloud `cloud_read_only` are different kinds of thing, not just
   different spellings.** Project `readonly` is a whole permission tier, on equal footing with
   `owner` and `write`. Cloud `cloud_read_only` is a **deny role** — a restriction layered on a
   separate `base_role`, never a tier by itself. `base_role = "owner"` plus
   `deny_roles = ["cloud_read_only"]` still holds the underlying `owner` role, with reads enforced
   on top of it.
4. **Organization `deny_roles` and cloud `deny_roles` are the same mechanism and deliberately share
   the name** — both restrict a separate base role rather than constituting a tier on their own.
   Where their **reach** differs is blast radius: an organization deny role overrides even an
   organization owner's implicit permissions, while a cloud deny role explicitly does **not**
   restrict organization or project owners. Same mechanism, different reach — this is the point of
   the naming ruling below, and it is a reach difference, not a naming or direction mismatch.
   (Their Terraform schema optionality also differs deliberately — see the two write paths below.)

## Authority model: cloud_access vs the per-user role resources

Both `anyscale_organization_user_role` and `anyscale_cloud_user_role` use the word
"authoritative," and for both it means the same, narrow thing: authoritative over the **one row**
each manages — one user's role at one scope — never over a population. Neither resource discovers
or revokes anyone Terraform was not explicitly told to manage; a person nobody wrote a resource for
is invisible to it, not evicted by it. `anyscale_organization_user_role` is additionally, and
deliberately, **not** authoritative over organization membership itself — that stays with
`anyscale_organization_user`, and destroying the role resource never evicts anyone.

`anyscale_cloud_access` means something categorically larger by the same word: it is authoritative
over **one cloud's entire member list**. Anyone not declared in its `member` map is revoked —
including someone granted access through the Anyscale console, and including on the **first** apply
(see the Create ruling below). This is why it is keyed
by `cloud_id` and not by user: authority over a set requires one resource owning the whole set, and
a per-user resource is structurally blind to a member it was never told about — it can enforce "does
Alice have access" but never "are these the only members of cloud X." Only the second guarantee
catches an out-of-band grant, which is the entire point of an authoritative resource.

Both role resources make one identical design choice for the same reason: `base_role` is
**Required, with no Default**, at both organization and cloud scope. A default would need to pick
some role for adopting a person who already holds real access — and if that guess were ever lower
than what they actually hold, adoption would silently demote them on the first `apply`, with
nothing unusual in the plan to reveal it. Stating the role every time trades config brevity for
making sure a demotion, if it ever happens, is a value someone typed and can see in the diff.

`anyscale_cloud_access`'s schema also ships two typo guards that follow from the same
"authoritative over a population" property: a case-insensitive duplicate-email check in
`ValidateConfig` (Terraform's own map-key duplicate check is case-sensitive, so two spellings of one
address look like two people to Terraform and are one identity to Anyscale), and an
empty-member-set guard in `ModifyPlan` (`allow_empty_member_set`, default `false`) that refuses an
apply that would empty a currently-populated cloud, naming how many members would lose access. A
third case in the same family is deliberately **not** guarded and cannot be: a typo in the
`cloud_id` used as a `for_each` key is indistinguishable, to Terraform, from a deliberate removal —
destroying an `anyscale_cloud_access` correctly revokes that cloud's members, and the provider
cannot tell that from a mistake. Mitigation for that case lives outside the provider
(`lifecycle { prevent_destroy = true }` on production clouds).

### Create is authoritative too, and the revoke it performs is undisclosed

**Ruling: `Create` revokes undeclared pre-existing members, exactly as `Update` does.** Terraform is
the source of truth for a cloud's member list from the first apply onward, not from the second. The
alternative — adopt-on-create, enforce-later — was rejected: it would mean the resource's stated
guarantee ("these are the only members") is false for the entire life of the first apply, and the
moment it *became* true would be some unrelated later change.

This is not a guard-shaped problem, and it is important not to file it as one. The two guards above
work because each has a signal to fire on (two spellings of one address; a populated cloud going
empty). A first apply that revokes a real person has **no such signal** — a member the operator
never knew about is, by construction, indistinguishable from one they deliberately omitted. So there
is nothing to detect, and **documentation is the only available mitigation.** Three consequences
follow, and the resource's own page must carry all three:

1. **"Review the plan" does not protect a reader here, and the page must not imply it does.** The
   `for_each`-typo guidance above genuinely is plan-reviewable — the plan shows a destroy. A
   first-apply revoke is not: the members about to lose access are ones Terraform has never read,
   so they appear nowhere in the plan output. A page that offers plan review as the general
   mitigation for this resource's sharp edges, without excluding this case, actively misleads.
2. **The caller's own identity must be excluded from the authoritative set.** The token running
   Terraform is usually a collaborator on the cloud it manages and frequently its owner, so without
   this exclusion the first apply can revoke the operator's own access. This was originally argued
   on narrower grounds (a permanently-occupied `unmanaged_grants` entry is an alarm that is always
   on, i.e. no alarm at all); under authoritative-Create it is load-bearing, and it must land before
   or with the reconcile rather than as later tidying.
3. **A cloud owner who is not an organization admin can be revoked on the first apply.** Org admins
   happen to be safe, but only by accident of plumbing — they are invisible to the endpoint this
   resource reads, so it cannot revoke what it cannot see. A non-admin owner has no such accidental
   protection. This is the worst realistic outcome of the ruling and belongs on the page explicitly,
   not left for a reader to infer from the general authority statement.

## Relationship to anyscale_cloud_user_role

`anyscale_cloud_access` and `anyscale_cloud_user_role` operate on the same per-cloud collaborator
surface with incompatible authority models: `cloud_access` is authoritative over a cloud's **whole**
member set (anyone undeclared is revoked); `cloud_user_role` is authoritative over only **one**
(cloud, user) pair. Registering both means `cloud_access` will silently revoke members that
`cloud_user_role` manages, on `cloud_user_role`'s very next apply — the two cannot safely coexist
once `cloud_access` is actually wired up. This follows directly from the authority-model section
above; it is recorded here as its own ruling because it does not arise from anything PR #228 itself
changed — `cloud_user_role` is untouched by that PR, and as of this writing it remains registered
in `provider.go` while `cloud_access` remains schema-and-validation-only. The conflict is latent,
not yet live: it becomes real the moment `cloud_access`'s reconcile lands and it is registered.

**Ruling: `anyscale_cloud_user_role` is removed outright in the same release that registers
`anyscale_cloud_access`, rather than deprecated for a cycle.** A deprecation window only helps if
both resources can coexist safely for a time, and here they cannot — leaving `cloud_user_role`
usable alongside a registered `cloud_access` ships a silent-revoke footgun rather than a graceful
transition. This is a second breaking change for a resource that shipped new in v0.24.0, only
shortly before; the changelog fragment for the removal should say plainly that the shape was
corrected against the real authority-model conflict before wide adoption, rather than left to
harden.

## The `deny_roles` naming ruling

**Final ruling: `deny_roles` is the Terraform schema attribute name at both organization and cloud
scope.** The wire spelling is not uniform, and that is fine because practitioners write the schema,
not the wire: at cloud scope the wire field is already `deny_roles` natively
(`SetCloudRolesRequest.DenyRoles`, `json:"deny_roles"`); at organization scope the wire field is
`additional_roles` (`SetOrganizationRolesRequest.AdditionalRoles`, `json:"additional_roles"`),
kept as-is on the wire while the schema deliberately does not copy that name.

The organization-scope mismatch is deliberate, not a typo: the API's own field name and description
read as "additional capability," but Anyscale's published permission docs state that container image
roles are genuinely deny roles, and the permissions table shows both legal values
(`image_reader`, `image_reader_no_base_images`) strictly *subtracting* from the default tier, never
adding to it. The OpenAPI spec is authoritative for paths, shapes, and legal values — not for what a
field means.

This went through one reversal worth recording because it explains *why* the final answer is
trusted: an earlier ruling had this right — `deny_roles` at both scopes, same mechanism,
same name — but was then overturned to keep `additional_roles` at org scope, on reasoning inferred
from the OpenAPI enum's name and description alone. That inference was wrong, confirmed once the
real product docs (not just the spec) were checked: container image roles are deny roles. A later
ruling reversed the overturn and restored the earlier answer. The lesson generalizes beyond this one
field: the spec is authoritative for shape, not semantics — check the actual product docs before
naming a field for what it *means*.

Two further points are load-bearing for anyone touching this resource:

- **Two write paths, forced by wire arity, never sequenced in one apply.** `permission_level` is a
  single flattened enum and cannot express a base role and a deny role at once — only the roles
  endpoint (`PUT .../organization_collaborators/{user_id}/roles`) can carry both, and it is a SET
  over the pair, so it would clobber a base role a legacy write just wrote. `deny_roles` is
  Optional+Computed: omitting it leaves the org's existing value alone and stays on the ungated
  legacy endpoint (works everywhere); declaring it — including `[]` — routes to the gated roles
  endpoint (501s in organizations without the feature, and unconditionally on Azure) and manages
  the set authoritatively. This asymmetry with `cloud_user_role.deny_roles` (plain Optional, "omitted
  means none") is deliberate: porting the cloud contract to org scope would force every org apply
  onto the gated endpoint. Adopting an existing member through this resource is also the intended
  migration path off an older, ad hoc access-management script — the write must be authoritative
  rather than assume the pre-existing value is already correct.
- **Authority must be decided from Config, never from State or Plan.** `deny_roles` being
  Optional+Computed means its *value* is populated on every refresh regardless of whether the
  config ever declared it, and `UseStateForUnknown` carries the prior value into the plan on an
  omitted attribute. A real acceptance test caught a high-severity bug from exactly this: destroy
  was clearing a member's deny roles even when config never declared the attribute, because Delete
  read `state.DenyRoles` (never null by the time Delete runs) and Update read `plan.DenyRoles`
  (looks "declared" even on a plain `base_role`-only change). The fix reads `req.Config` on
  Create/Update, records that decision in provider private state, and has Delete read it back —
  Delete has neither Config nor Plan. A real live-infra check (**V8**) also confirmed the roles
  endpoint's SET semantics directly — a *changed* `deny_roles` value genuinely persists as a
  replacement rather than accumulating — which is what disproved an earlier, stale claim that the
  organization side of this design was entirely "feature-gated/read-only": only `deny_roles` is
  gated; `base_role` alone uses an endpoint that works everywhere.

## Cross-field invariants: why projects nest inside cloud_access members

`anyscale_cloud_access.member[*].projects` is nested rather than a sibling resource because the
backend enforces two invariants that only a single nesting resource can check at plan time rather
than surface as a confusing apply-time failure:

1. **A project role cannot exist without a cloud role on the same cloud.** The backend 403s
   creating a project collaborator unless the grantee already passes a read-permission check on the
   parent cloud, and removing a cloud collaborator cascades to delete their permissions on that
   cloud's projects.
2. **A member holding the `cloud_read_only` deny role may only hold `readonly` on that cloud's
   projects.** Anything else 422s — this is a genuine cross-field constraint spanning a member's
   cloud deny roles and their project roles, and it is the concrete reason projects live inside
   members rather than in a sibling resource.

## Other rulings worth citing by name

These are recorded so that any of them, met in code or in prose, resolves to something. The first
group is cited by ID from committed test code; the second is referenced only from this document.

**Cited by ID from committed test code** — **R9**, **R12**, and criterion 31 in
`internal/acctest/resource_organization_user_role_acc_test.go`, plus **V8** in
`internal/acctest/resource_organization_user_role_realinfra_acc_test.go` (the live confirmation of
the roles endpoint's SET semantics, described under the two write paths above):

- **R9** — destroy does not revert `base_role` (no absent state exists for it — every member always
  has *some* base role, and reverting risks demoting an organization's last owner and locking out
  administration) but does clear a *declared* `deny_roles` back to empty (a real, reachable absent
  state this resource took authority over). Neither branch is silent: destroy always emits a
  warning naming what was left in place.
- **R12** — a declared `deny_roles` cleared on destroy is not a reduction: `deny_roles` are
  restrictions, so clearing them *increases* that person's access — the one destroy in this
  provider that grants capability rather than removing it. State that direction explicitly; nothing
  about the word "destroy" suggests a grant of new access.
- **Criterion 31** — Read must remove a resource from state only on a genuine not-found; any other
  error (a 500, a timeout) must surface as a real diagnostic. Treating every error as "gone" would
  let a transient failure silently evict a resource from state, and the next apply would then
  re-assert the role over whatever out-of-band change happened in the meantime.

**Referenced only from this document:**

- **The organization-role split** — organization role management moves into its own resource
  (`anyscale_organization_user_role`), landing in the same change as removing the role fields from
  `anyscale_organization_user`, rather than staggering the two.
- **R10** — a coherence ruling: state once, in one place, why `anyscale_cloud_access` destroy
  (revokes members) and `anyscale_organization_user_role` destroy (leaves the role in place) are
  opposite-looking behaviors from the *same* rule — destroy removes what a resource has authority
  over, only where an absent state exists for the thing it manages. Read separately, this looks like
  an inconsistency; stated once, side by side, it is one rule applied to two different object
  models, and the sharp edges point in opposite directions (leave-a-privilege vs.
  remove-access-at-scale) on purpose.
- **Criterion 20** — `docs/guides/rbac.md` is a first-class deliverable of this design, not
  incidental documentation: it is the one place the vocabulary collisions, the authority model, and
  the migration story are all explained together.

## What depends on this document

The path `docs/decisions/rbac-surface-consolidation/README.md` is cited from PR #228's squashed
commit (`9dd5ecc`) itself — in the guide-authoring commit's summary (citing criterion 20) and via
`Refs:` trailers on five further commits in the same squash (the `organization_user_role` feature
commit, both `cloud_access` schema/validation commits, the empty-member-set guard commit, and the
criterion-31 test commit) — not from any in-repo file content, which is exactly why the citation
previously resolved to nothing.

`docs/guides/rbac.md` is the closest surviving artifact of what this design produced, and should be
read alongside this document rather than instead of it: as shipped, it covers
`anyscale_organization_user` / `anyscale_organization_user_role` / `anyscale_cloud_user_role`, but
does not currently describe `anyscale_cloud_access`'s whole-member-list authority model, despite
that comparison being part of this design's original scope for the guide. That gap should close
when `anyscale_cloud_access` is registered and gets its own docs page.
