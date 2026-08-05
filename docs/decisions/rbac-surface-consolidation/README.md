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
  *(That describes what PR #228 shipped and is left as written. It is no longer the current state:
  registration ahead of the write path was decided on 2026-08-04 — see the reconcile rulings below.
  Anything downstream that keys off "unregistered", including the absence of a changelog entry,
  changes with it.)*

## Vocabulary collisions

The organization, cloud, and project scopes don't just use different words for access levels —
they reuse the **same** words for **different** things. Four instances, all now documented in
`docs/guides/rbac.md`:

1. **`collaborator` means three different things**: the non-owner organization role, one of six
   possible cloud `base_role` values (an unrelated concept), and the Anyscale console's own display
   name for the legacy `write` permission level.
2. **Project `write` and cloud `writer` are not the same role**, despite being near-homographs, and
   the typo trap runs in one direction: a project only accepts `write` — sending `writer` to a
   project 422s.

   **Correction (2026-08-04): "cloud versus project" names the wrong axis, and this entry said so
   for months.** There are **three** role vocabularies on this surface, not two:

   1. Cloud **roles** `base_role`, on the roles write — `owner`, `writer`, `collaborator`,
      `project_viewer`, `compute_config_viewer`, `workload_operator`.
   2. Cloud **legacy** `permission_level`, on the membership write — `owner`, `write`, `readonly`.
   3. Project `permission_level` — `owner`, `write`, `readonly`.

   So (2) and (3) share a *spelling* while (1) and (2) share a *scope*. Splitting the world into
   "cloud" and "project" therefore picks the wrong axis and makes `write` at cloud scope look like
   an error when it is a legal legacy value. A validator or diagnostic here should state what a
   **project** accepts rather than assert what `writer` is.

   This has a direct consequence worth recording: a symmetric validator — one that rejected `write`
   as a cloud `base_role` the way it rejects `writer` as a project role — would reject working
   configurations. The decision not to make it symmetric was originally argued from `base_role`
   having no `OneOf`; it turns out to be load-bearing for a stronger reason than the one given.

   The six values in (1) are confirmed rather than asserted: `CloudBaseRole` in the backend model
   and the published `openapi.json` enum agree exactly, including order. `CloudDenyRole` likewise
   has exactly one member, `cloud_read_only`, so that is the **complete** set rather than merely the
   only value currently known — which is a stronger claim than the schema currently makes and the
   page may as well make it. One detail is source-only and cannot be confirmed from the published
   schema because it is a comment: the enum's iteration order is contractual and fixes the order of
   `base_roles` in list responses, which means a member holding more than one cannot produce a
   spurious diff from reordering.
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
2. **The caller's own identity must be excluded from the authoritative set — and it does NOT
   prevent a lockout. Do not write that it does.** The exclusion prevents a revoke that the backend
   would refuse anyway from being *attempted on every apply*, which would park a permanent entry in
   `unmanaged_grants` and destroy the value of an alarm this resource tells users to watch. That is
   the whole reason, and it is sufficient. The token running Terraform is usually a collaborator on
   the cloud it manages and frequently its owner, so an authoritative Create would otherwise attempt
   this on the first apply. It must land before or with the reconcile rather than as later tidying.

   This warning leads the entry because the false version has already propagated once. A correction
   recorded further down was read past, and "the first apply could revoke the operator's own access"
   reached a published guide within hours of being corrected here. It is the more dramatic sentence
   and it is the one that travels.

   **Correction (2026-08-04): this was previously justified as preventing a self-inflicted lockout,
   and that justification is wrong.** The backend refuses self-removal on its own —
   `remove_cloud_collaborator` raises 403 ("You cannot remove yourself from the cloud") when the
   caller's identity matches the target — so an operator cannot lock themselves out through this
   path whatever the provider does. What the exclusion actually prevents is the original and
   narrower problem: without it, every apply in which the caller is undeclared attempts a revoke
   that fails, parking a permanent entry in `unmanaged_grants` and destroying the value of an alarm
   this resource's own documentation tells users to watch. That is sufficient on its own. The ruling
   is unchanged; only the argument for it is corrected. Recorded because the overstated version is
   more persuasive than the true one, and so is the version likely to be repeated.
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
changed — `cloud_user_role` is untouched by that PR.

**Ruling: `anyscale_cloud_user_role` is removed outright rather than deprecated for a cycle.** A
deprecation window only helps if both resources can coexist safely for a time, and here they cannot
— leaving `cloud_user_role` usable alongside a registered `cloud_access` ships a silent-revoke
footgun rather than a graceful transition. This is a second breaking change for a resource that
shipped new in v0.24.0, only shortly before; the changelog fragment for the removal should say
plainly that the shape was corrected against the real authority-model conflict before wide adoption,
rather than left to harden.

**Amendment (2026-08-04): the removal shipped *before* the replacement, deliberately departing from
this ruling's original "in the same release that registers `anyscale_cloud_access`" timing.** The
removal landed as PR #259 (`ab2c3b5`) while `cloud_access` is still unregistered and still refuses
every CRUD call. This was chosen over both "remove when the replacement lands" and "deprecate first",
conditional on the replacement not being close to done — a condition that was measured rather than
estimated at the time and held (schema and validation only; zero API calls in
`resource_cloud_access.go`; no `ImportState`).

Record the consequence rather than the preference, because it is the part that affects users: there
is now a window in which the provider cannot manage cloud-scoped roles **at all**. Combined with the
`anyscale_project` `collaborator` removal at v0.25.0, three of the four steps in the ordinary
new-member journey — grant a cloud, grant a project, revoke either — have no provider surface during
this window. Only adding a member to the organization with a role still works. Anything written
against that window's provider version (release notes, the RBAC guide's resource table, a migration
answer) is correct only until `cloud_access` registers, and must be revisited then rather than left
to read as durable prose.

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
  endpoint (501s in organizations without the feature) and manages the set authoritatively. **This
  sentence previously added "and unconditionally on Azure". That was unsubstantiated and has been
  removed — see the Azure entry under verification owed.** This asymmetry with
  `cloud_user_role.deny_roles` (plain Optional, "omitted
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

## Reconcile rulings for `anyscale_cloud_access`

These govern the unbuilt reconcile. They are recorded here, rather than only alongside the
implementation, because every one of them is a decision about user-facing behavior that outlives
whatever code first expresses it.

**J.10 — authority over projects is scoped to those named in configuration.** `anyscale_cloud_access`
is authoritative over the cloud's **whole member set** but only over the **config-named subset** of
each member's project roles. The schema's own wording already points this way, describing `member` as
"the **complete** set" and `projects` as no such thing.

The wider alternative — authoritative over every project under the cloud — was originally rejected on
cost, and that argument is the weaker of the two available. It is O(projects × pages) on every plan
and every apply and grows with the cloud rather than with the configuration, which is a real recurring
expense but one a determined implementer can always argue is affordable. **The stronger objection is
authority surprise.** Under the wider scope, adopting `anyscale_cloud_access` to manage a cloud's
membership silently takes ownership of every project beneath that cloud, so the resource would strip
project roles the practitioner never mentioned anywhere in their configuration. That is a
categorically larger claim than "these are the members of this cloud", and the two costs land
differently: the narrower scope's blindness is *disclosed in documentation*, while the wider scope's
escalation is *discovered during an apply*. Prefer the failure a reader can be warned about.

Two consequences, both of which must reach the resource page rather than living here:

- A project role granted out-of-band on a project no configuration names is **invisible** to
  Terraform. This is the accepted cost of the narrower scope, not an oversight.
- `unmanaged_grants.project_id` therefore covers only config-named projects, and the attribute's
  description must not imply broader coverage.

**The `projects` read follows from J.10: read the projects named in configuration and in prior
state, and no others.** Reading only config-named projects would make a dropped project entry
unobservable and so unrevokable; reading every project under the cloud is the scope J.10 rejected.
The union is the smallest read that keeps both grant and revoke working, and its cost is bounded by
configuration size rather than by the cloud's project count. Two shape facts this rests on, each
confirmed against working provider code rather than assumed: the per-project collaborator endpoint
returns a **list** of collaborators, so the read is O(projects) and never O(projects × members); and
projects under a cloud can be listed with a single `parent_cloud_id`-filtered request rather than a
client-side sweep.

Two alternatives were considered and rejected on their consequences rather than their cost, and both
rejections generalize:

- **An import-only frozen snapshot** would make the provider report a compliance it cannot verify —
  a clean plan against a configuration that claims authority, while reality has diverged. The
  precedent it appears to follow (the cloud config blocks, which are deliberately not
  Read-refreshed) exists to avoid a specific "provider produced inconsistent result after apply"
  failure that does **not** apply here, because `projects` is a plain attribute rather than a
  framework Block. Borrowing a workaround's shape without inheriting its cause is a recurring error
  in this provider's history.
- **Gating the read behind an opt-in** would make `terraform import` populate differently depending
  on a flag. If cost ever forces a gate, it must gate the resource's declared *scope* — and reject
  `projects` being set at all under that gate — never silently reduce read fidelity while the
  configuration still declares project roles.
- **Leaving `projects` unread and preserving whatever prior state held** is not available at all,
  and the reason is a schema consequence worth stating on its own: because `projects` is plain
  `Optional` with no `Computed`, a `Read` that leaves it null against a configuration that declares
  it produces a **permanent phantom diff** — plan wants to set it, apply sets it, the next `Read`
  nulls it again. `Read` therefore cannot decline to populate this attribute. Any future proposal to
  economize on this read has to satisfy that constraint first.

**Detection and authority are separable, and the wider scope conflates them.** The one real thing the
narrower scope gives up is the ability to *notice* an out-of-band grant on an undeclared project.
That does not require taking authority over those projects: a read-only surfacing — a `Computed`
attribute listing undeclared project grants, or a data source — reports the drift without claiming
the right to revoke it. This is the preferred route if the blindness ever proves unacceptable in
practice, both because it avoids the authority escalation and because adding a `Computed` attribute
is additive where widening authority is not. Recorded here so that "we need to detect out-of-band
project grants" does not automatically get read as "we need the wider scope."

**Decided by the user (2026-08-04): dropping a project entry from a member REVOKES that project
role.** The schema as committed already implied it — `projects` is plain `Optional` with no
`Computed`, which is Terraform's shape for "configuration wins, omission means absent" — and the
alternative would have left the provider able to grant a project role but never remove one, which
is to say unable to complete the revoke half of the journey this resource exists to serve.

It was put to the user rather than decided in engineering because it is data-loss-shaped, and
because it is *not* the same question as the member-set authority settled above. The asymmetry that
made it worth asking is worth keeping on the record now that it is answered: `deny_roles` is also
plain `Optional`, so omitting it removes a **restriction** and fails toward more access, while
omitting `projects` removes a **grant** and fails toward less. One structural rule, opposite blast
directions — which means a typo in each place does a different kind of damage, and the documentation
should not describe them in one breath as though they behave alike.

**Decided by the user (2026-08-04): `anyscale_cloud_access` registers while still read-only, ahead
of the write path.** The deciding argument was not preference but testability: an unregistered
resource cannot be acceptance-tested at all, because the test provider factory is built from the
real provider with no seam for injecting resources, and the resource's one committed acceptance test
had therefore never run. Leaving it unregistered would have deferred the read path's first genuine
plan and apply into the same change as the write path, concentrating both risks in the dangerous
one. The four unconditional refusals are what make this safe — a registered resource that refuses
every write cannot revoke anyone, and that floor is enforced by code rather than by the resource
merely being absent.

Two obligations follow directly from registering, both of which exist only because a published
documentation page now exists:

- The description must state plainly that write operations are not implemented in this version, and
  must carry the two-flag 501 warning this resource still lacks.
- Carrying `projects` forward from prior state, which was an acceptable interim while unregistered,
  is not acceptable once registered. The reason is sharper for a read-only resource rather than
  softer: read-only is precisely the shape someone reaches for to *audit* access, and a carried
  forward map reports a clean plan against project state that was never verified. Either the real
  read lands with registration, or the page states that project roles are not refreshed and are an
  import-time snapshot. Silence is the one option ruled out.

**J.2 — the caller's own identity is excluded from the authoritative set, resolved via
`GET /api/v2/userinfo`.** Never grant, revoke, or record it. The alternatives fail on their
consequences: recording it in `unmanaged_grants` permanently occupies the alarm the schema itself
recommends users watch, and an alarm that is always on is not an alarm; refusing to run when the
caller is a member of the cloud refuses the near-universal case, since the token running Terraform is
usually a collaborator on the cloud it manages and often its owner. `userinfo` is already this
provider's established route to connection-level identity, so this reuses a pattern rather than
introducing one. The exclusion is the single stated exception to "anyone not declared is revoked" and
belongs on the resource page in those terms, not in a code comment.

**J.2 extended — the exclusion must hold in `Read` as well as in the reconcile, and declaring the
caller in `member` is therefore a plan-time error.** Excluding the caller from revocation while
`Read` still reports them is not a partial implementation, it is a broken one, and both of the
obvious repairs fail:

- **Report the caller honestly and skip revoking them.** Reality holds the caller, configuration does
  not, so the plan proposes removing them and the reconcile declines. The post-apply state then
  either includes the caller — contradicting the plan, which trips "provider produced inconsistent
  result after apply" — or omits them, and the next `Read` restores them and proposes the same
  removal forever. There is no third outcome.
- **Omit the caller from `Read` and leave the rest alone.** This fixes the undeclared case and breaks
  its mirror: a configuration that *declares* the caller now has a state that never contains them, so
  the plan proposes adding them forever.

`Read` cannot arbitrate between those two cases, and the reason is structural rather than incidental:
`resource.ReadRequest` carries `State`, `Private`, `ProviderMeta` and `ClientCapabilities` — **no
`Config`** (confirmed in `terraform-plugin-framework` v1.19.0). A `Read` implementation cannot know
whether the configuration declares the caller.

So the caller is absent from read, from plan, and from write, consistently: this resource manages
other people's access to a cloud, and the operator's own access is outside its scope by construction.
Declaring it is rejected at plan time.

Two implementation constraints that are part of the ruling rather than detail:

- The check belongs in `ModifyPlan`, not `ValidateConfig`, for the same reason the empty-member-set
  guard does: `ValidateConfig` can run before the provider is configured, so the client may be nil
  there and the caller unresolvable.
- If the caller cannot be resolved, **skip the check without erroring, and do not treat the skip as a
  pass.** The reconcile's own exclusion is what keeps the alarm usable; the plan-time error is only
  the friendly early warning, and an unresolvable identity must not become a hard failure. Nothing
  here is protecting against a lockout — the backend refuses self-removal on its own, as recorded
  above — so a skipped check costs a good error message, not safety.

**State on the resource page that the excluded identity is token-dependent.** It is scoped to
whoever runs a given apply, not a protected-persons list. Whoever runs an apply has their identity
excluded during that apply; a colleague who ran it last week and is not declared in configuration
remains revokable, which is correct and is the resource's whole contract. Left unstated, "the caller
is excluded" reads as "anyone who has run Terraform is permanently safe," which is both false and
dangerous to rely on.

A subtler alternative was considered and rejected: `Read` could report the caller only when prior
state already contains them, which is implementable because `Read` does have `State`, and which
converges in both the declared and undeclared cases. It was rejected because it makes identical
reality read differently depending on history, and because a cold import has no prior state, so
import and steady state would disagree. Recorded so it is not re-proposed as an obvious improvement.

**J.5 — ordering within an apply: narrow first, then add, then remove.** Project drops and role
changes on retained members run before adds; revokes run last. The residual window is that a member
slated for removal keeps access while new members are being added — over-privilege for someone the
*prior* state already granted, never a grant nobody intended. That is the correct trade against
leaving a cloud transiently ownerless if an apply dies partway through.

**J.7 — `base_role = "owner"` combined with `deny_roles = ["cloud_read_only"]` is a plan-time
error.** The backend rejects the combination outright, it is checkable from configuration alone, and
the whole reason projects nest inside members is to turn this class of failure into a plan-time error
rather than a confusing mid-apply one. Implement it as a targeted equality test against the literal
`owner`, **not** as an enum validator — `base_role` deliberately carries no `OneOf` because the roles
API is actively extended, and a closed enum would reject a new backend role until the next provider
release.

**J.9 — on a cloud with `auto_add_user` enabled, `Create` and `Update` refuse; `Delete` does not.**
The flag structurally blocks every revoke, so the resource cannot honor its authority claim there.
Refusing uniformly is a contract statement rather than a per-apply judgment: a resource that works
only when a given apply happens not to need a revoke is harder to reason about than one that refuses.

**That argument undersells the ruling, and a live finding shows why.** Enabling `auto_add_user` on a
cloud that already has organization members retroactively adds **all of them** as collaborators,
immediately — not merely future members. Confirmed against a real cloud, and documented nowhere.
Combine that with the flag blocking every revoke and the resource being authoritative, and the
outcome is not a degraded mode but a permanently broken one: every apply would attempt to revoke
every member of the organization, fail on all of them, and produce one `unmanaged_grants` entry per
person. The honest reason for refusing is that the resource cannot function on such a cloud at all.

Two consequences follow. The refusal message must say *why* and name the remedy — disable the flag,
then retry — because an operator has no way to derive that from a refusal alone. And the retroactive
grant deserves its own line on the resource page: enabling that flag on an already-populated cloud
is itself a silent grant of access to everyone in the organization, which is worth knowing whether
or not Terraform manages that cloud.
`Delete` is the deliberate exception, because refusing it would strand the resource in state with no
exit but `terraform state rm` — attempt the revokes, record every failure, and name the real remedy
(disable the flag on the cloud, then retry).

**J.3 — run the membership bootstrap unconditionally, including on a role-only change.** The
narrower reading, that a bootstrap buys nothing when only a role changes, may well be correct on the
mechanics. It is deferred to anyway, because "reasoning that the bootstrap was unnecessary here" is
the move a prior design record in this repo identifies as its own most consequential mistake, and the
rule that mistake produced is explicit: never skip it on the strength of what a search appears to
show. The asymmetry of costs settles it — being wrong toward an extra call costs one absorbed
conflict response per member per apply; being wrong the other way costs a user an unrepairable grant
whose only exit is `terraform state rm`.

**Revisited on evidence (2026-08-04). The ruling holds; one premise behind it is now known false.**
The condition for reopening this was explicitly "with evidence, not reasoning", and evidence arrived,
so it was reopened rather than deferred a second time.

*The falsified premise, which matters more than the ruling and has the longer half-life.* The removed
per-user resource carried a comment asserting that the API has **no** endpoint that both grants a
role and establishes membership, which is why its `Create` always issued the legacy bootstrap first.
Against this endpoint pair that is **false**: calling the roles write directly for a user with no
prior relationship to the cloud succeeded, and both the roles listing and the membership search
showed the user immediately afterward, with a legacy permission level auto-derived from the base
role. Whatever was true when that comment was written, it is not true now. It is recorded here
because a load-bearing premise sitting in a comment is exactly what a later change designs around
without re-checking.

*Why the ruling nonetheless stands.* The capture covers **one** starting state — a user with no prior
relationship to that cloud. The bootstrap is defensive across all of them: a previously-removed user,
a partially-established one, a directory-sync-managed identity, a service account. "The roles write
establishes membership for a fresh user" is a narrower sentence than "the bootstrap is unnecessary",
and the gap between them is precisely where a failure would live. The cost asymmetry above is
untouched by the finding, and there is nothing on the other side of the trade worth buying — no one
is constrained by create latency.

*What would change it.* Not a repeat of the same case, which would add confidence to a result already
believed. Vary the starting state: a user previously added and then revoked from that cloud, a
service account, and if reachable an identity under directory sync. If the roles write establishes
membership across those too, the case for dropping the bootstrap becomes strong rather than
suggestive, and this should be revisited a third time.

**Batch endpoints are not used.** Their all-or-nothing validation is hostile to per-member
partial-failure recording, there is no batch roles write to begin with, and batching only the
membership half saves N−1 of 2N calls while making the failure mode strictly worse.

**Never return an error after a successful mutation. Error freely before the first write; never
after it.** This is the rule that keeps `unmanaged_grants` meaning what it is supposed to mean, and
it exists because of a verified Core behavior rather than a preference.

A real `resource.Test` confirmed that Core does persist state written before an errored `Create` —
but persists it **tainted**, so the following apply is a full destroy-and-recreate rather than a
retriable reconcile. For a resource whose destroy revokes a cloud's entire member list, that turns
one member's failure into a scheduled revoke of every member the apply *did* manage to write. It is
the precise inverse of the containment `unmanaged_grants` exists to provide.

The converge-and-record path is unaffected and was never at risk: a failed revoke records an entry
and the apply **succeeds** with a warning, so there is no error and nothing taints. What the finding
changes is the **fatal** path — an API outage partway through a reconcile — where the instinct to
return the error is now actively wrong.

On a fatal mid-reconcile failure: write the **full planned** member map to state, record everything
not achieved in `unmanaged_grants`, warn loudly, and return success. State then matches the plan, so
no inconsistent-result error arises; the next `Read` corrects state to reality; the next plan shows
the remaining work and retries it. The cost is real and belongs on the page rather than buried here:
between a failed apply and the next refresh, state claims members that were never actually granted.
That is strictly better than the alternative and it is not free.

Two boundaries on the rule, so it is not over-applied. Errors *before* any mutation should stay
exactly as they are — caller resolution failing, the member list not listing, the `auto_add_user`
preflight refusing — because none of them leave partial state and none can taint. And `Delete` is
unaffected: a failed `Delete` that never calls `RemoveResource` leaves state untouched and retries
to an empty plan, also confirmed by real test.

`Update` takes the same rule for a weaker reason: taint is a create-time concept and a failed
`Update` most likely does not taint at all, but that half could not be tested in isolation without a
write path to drive it. The rule costs nothing and is correct under either answer, so it applies
until someone has evidence to narrow it.

**This makes the partial-write Framework/Core check load-bearing rather than a formality.** The
fatal-path shape above depends entirely on writing a full planned map to state after partial
real-world writes without tripping "provider produced inconsistent result after apply". If that
check fails, the fatal path has no safe shape and this ruling must be revisited before any write
path ships.

**Every `unmanaged_grants` entry also emits a warning naming the email and the reason.** The schema's
argument against a warning as the *only* channel — warnings recur on every plan and get scrolled past
— is not an argument against having both. The attribute is the machine-readable alarm; the warning is
what stops the very first apply from looking clean.

**Feature gating: two independent flags, and a 501 that does not say which one is off.** The removed
per-user resource carried this warning verbatim, and it transfers unchanged: the feature is gated
behind two separately-controlled backend flags, one for reading roles and one for writing them; if
either is off, operations fail as an HTTP 501, and the provider cannot detect it ahead of time short
of trying. Because the read and write paths return the *identical* 501 detail string from
*different* flags, a 501 alone does not tell an operator which half is disabled — so the message must
name both possibilities. `anyscale_cloud_access` does not currently carry any version of this
warning and must gain one before it registers.

## The contract, stated plainly

Everything else in this document is a reasoning trail. This section is the contract itself, for a reader
who needs to know what the resource does rather than why. **Where this summary and a ruling disagree, the
ruling wins** — it carries the evidence and this does not. Rationale, rejected alternatives and the
evidence behind each line live in the ruling named beside it.

**What it is authoritative over.** The complete member set of one cloud, and within each declared member,
only the project roles the configuration names (J.10). Anyone not declared is revoked, on `Create` as well
as `Update`. Two exceptions, both structural rather than courtesies: the caller's own identity (J.2) and
organization admins (J.22), who are outside its scope in both directions — never granted, never revoked,
never reported.

**One instance per cloud** (J.11). Two Terraform states managing one cloud revoke each other's members
indefinitely while both report success. Unsupported by contract, undetectable by the provider, documented
rather than guarded.

**Identity and shape.** Keyed by `cloud_id` (`Required`, `RequiresReplace` — the only replacement trigger)
and, within `member`, by email, case-insensitively (a map so duplicates fail at plan time, plus an explicit
fold because Terraform's own check is case-sensitive). `base_role` `Required` with no closed enum, since the
roles API is extended. `deny_roles` and `projects` `Optional` — omission means absent, so dropping a project
entry revokes that role, and the two fail in opposite directions: omitting `deny_roles` removes a
restriction and grants access, omitting `projects` removes a grant. `unmanaged_grants` and the ungranted
list are `Computed` diagnostics only and never influence planning (J.12).

**Read.** The membership search is the enumeration source; the roles listing is supplementary. A member
created through the legacy path reads back the role they held **at creation** — populated, not null, and
never updated by a later legacy write (J.21, as corrected). Null is reachable only for a member with a
relational row and no authorization-service group, which is a narrower population than first claimed. A genuine not-found removes the resource from state; any other error is a
diagnostic (criterion 31).

**Drift.** An out-of-band grant or revoke through the RBAC path is detected. A role change made through the
CLI or console is detected only if it crosses the read-only boundary, which moves `deny_roles`; `base_role`
itself is frozen at creation and never moves on a legacy write (J.19, J.21). So a console change between
`write` and `owner` is invisible in **both** fields — including a promotion to `owner`, which is a
privilege-escalation blind spot rather than a reporting one. Any change a practitioner does declare is
repaired by a normal apply, in both stores; there is no self-heal on refresh, because `Read` never writes.

**Refusals, all before any write.** `auto_add_user` enabled (J.9), a group policy binding present (J.20),
an organization admin declared (J.22), `owner` combined with `cloud_read_only` (J.7), an empty `member` map
without `allow_empty_member_set`. `Delete` is exempt from the first two — refusing it would strand the
resource with no exit but `terraform state rm`.

**Ordering within an apply.** Narrow first, then add, then revoke (J.5). A member slated for removal keeps
access while others are added, which is over-privilege for someone the prior state already granted rather
than a grant nobody intended.

**Failure.** Per-member and independent; no batch endpoints. Never an error after a successful write,
because Core persists pre-error `Create` state **tainted** and this resource's destroy revokes everyone it
believes it manages. A failed revoke records and warns and the apply succeeds. A grant failure suppresses
the destructive half of the apply and not the additive half, records both the failed grants and the
consequently-skipped revokes, and still succeeds. Retry only genuinely transient conditions, bounded, with
exhaustion treated as record-and-warn (J.13). A post-apply read may write only `Computed` attributes; the
correction arrives on the next refresh, not within the failed apply (AC-21a/AC-21c).

**Import.** `cloud_id` is the import ID. A configuration declaring exactly the imported members plans empty
(AC-2), with `projects` covering only config-named projects. A cloud whose members were created through the
legacy path imports with their creation-time roles, so a configuration declaring those values plans clean;
a role since changed in the console imports stale, and declaring the true value repairs it on apply
(J.21 as corrected — AC-31's premise is under revision).

**Compatibility.** Additive with **no affected practitioners**, conditional on J.18: no released tag
contains this resource, so nobody can have imported it. Schema version 0, no upgrader, none needed —
enabling writes changes no schema. That classification expires the moment a tag ships the read-only
resource, which is why not shipping one is a release constraint rather than a preference.

## Contract rulings added for the full-CRUD round (J.11–J.22)

The rulings above govern behavior that was designed before a write path existed. These cover the
questions that only become answerable — or only become dangerous — once one does. They are recorded
as a separate group rather than folded into the list above so that it stays clear which decisions
predate the write path and which were made with it in view.

**J.18 comes first because it changes what the other rulings cost.**

**J.18 — the read-only resource has never been released, and that must remain true until the write
path ships.** `git tag --contains 5a220b1` is empty; the latest tag reachable from `main` is
`v0.25.1`, which predates PR #260. `anyscale_cloud_access` therefore exists only on unreleased
`main`.

Four consequences, the third of which is a constraint on the release rather than an observation:

- The compatibility question this round was convened to answer — whether enabling writes is a hidden
  breaking change for practitioners who imported the read-only resource — resolves to *there can be
  no such practitioners*. That answer is conditional, not permanent.
- Every schema change to this resource is currently free: no state upgrader, no migration note, no
  compatibility cost. The schema carries no `Version`, i.e. 0, and flipping a Go bool changes no
  schema, so the gate flip alone never needs an upgrader under any circumstances.
- **Do not tag a release containing a read-only-only `anyscale_cloud_access`.** That one act
  manufactures precisely the population whose breakage this round is meant to prevent, and it
  converts J.16 below from free into breaking. Read and write ship in one release, or neither ships
  yet. This is the strongest available argument for the sequencing and it is a release constraint,
  not a preference.
- The window this opens is the reason J.16 is worth acting on now rather than filing.

**J.11 — one instance per `cloud_id`; two Terraform states cannot safely manage one cloud.** Two
authoritative instances over the same `cloud_id` revoke each other's members indefinitely, and both
report success on every apply: the first grants Alice and revokes Bob, the second grants Bob and
revokes Alice, and neither ever converges. The provider cannot detect this, because it cannot see
another state file, so the resolution is contractual and documented rather than guarded.

State it on the resource page as the price of authority rather than as a limitation. A practitioner
splitting a cloud's membership across two configurations is doing something the resource's central
claim forbids, and the failure gives them no signal — an apply that reports success is the worst
available shape for a misconfiguration this severe.

**J.12 — `unmanaged_grants` is diagnostics only and never influences planning.** It is `Computed`,
never compared against configuration, never consulted to decide whether to attempt a write, and never
an input to anything. Each plan re-attempts everything still undone regardless of what the attribute
holds.

The rejected alternative is worth naming because it looks like an optimization: letting a recorded
entry suppress a retry would mean a stale entry silently suppressing a real grant — the resource
declining to do work the configuration asked for, on the strength of a failure that may have been
transient and hours old. That is the exact inverse of the attribute's purpose, which is to make an
unachieved intent *visible*, never to make it permanent.

**J.13 — bounded retry, and only for genuinely transient conditions.** No retry or backoff exists in
the write path today. Retry `429` and `502`/`503`/`504` and connection-level failures. Retry nothing
else: `403`, `409`, `422` and `501` are terminal decisions, and retrying a decision converts a clear
diagnostic into a slow one. At most three attempts per member, exponential backoff with a cap, the
whole reconcile bounded by a `timeouts` block — which this resource does not currently have and
should gain, an additive change.

Two properties make per-member retry safe rather than reckless, and both are already established
rather than assumed: the roles write has SET semantics, confirmed live as V8, so re-issuing an
identical write is idempotent; and the membership bootstrap already absorbs its own conflict
response under J.3. A retry would not be safe against a batch endpoint, which is one more reason
those stay unused.

**A retry budget exhausted mid-reconcile is a converge-and-record outcome — `unmanaged_grants` plus a
warning — not an error.** The never-error-after-a-successful-write rule admits no exemption for
retry exhaustion, and the reason is that exhaustion is *more* likely to occur after some writes have
already landed than before, which is exactly the case the rule exists for.

**J.14 — whether a group or team can hold cloud access is UNRESOLVED, and it is a hole in the
authority claim rather than a missing feature.** If a non-user principal can be granted access to a
cloud, then a `member` map keyed by user email is structurally blind to it, and "authoritative over
the cloud's whole member set" is false in a way documentation cannot repair. The resource would
either ignore such grants silently, overstating its authority, or attempt to revoke a principal the
schema has no way to express.

No group handling appears anywhere in `cloud_access_api.go`. **That is not evidence about the
backend** — it is evidence about our code, and the two are only related if someone checked. Settle
this against the API before the gate comes off. If group principals exist, the honest options are to
model them or to narrow the documented authority claim; continuing to claim whole-member-set
authority is not among them.

**J.15 — service accounts and Anyscale-managed identities are UNRESOLVED.** Two parts: whether
service accounts appear in the member search and the roles listing as ordinary members keyed by
something email-shaped, and whether any Anyscale-managed identity exists on a cloud that must never
be revoked in the way the caller must not be.

The backend demonstrably distinguishes service accounts already — the directory-sync revoke blocker
spares them while failing every human revoke — so the provider cannot treat them as ordinary
members by default. An authoritative resource that revokes a service account some other system
depends on produces an outage with no obvious cause, and the member map gives a practitioner no
natural place to notice the identity is special.

**J.16 — `deny_roles` is a `ListAttribute`, and that is an ordering hazard worth closing while it is
free.** `resource_cloud_access.go:164` and `:305`. Terraform lists are order-sensitive, so a backend
response ordering the values differently from configuration produces a diff on every plan forever.

`SetAttribute` is the semantically correct shape — `deny_roles` is a set of restrictions and order
carries no meaning — and under J.18 changing it is free today and breaking after the first tag.

**Revised on further evidence: do not change the shape. Fix it by semantic comparison instead, and
only if the order proves unstable.** `anyscale_organization_user_role` models its own `deny_roles` as
a `types.List` and **shipped that way in v0.25.0**, so its shape is frozen. Converting only the cloud
resource leaves two sibling attributes with the same name and the same semantics carrying different
types, permanently — a real cost against a hazard that is still unmeasured.

**CLOSED, and vacuously — there is no ordering hazard to have at cloud scope.** The cloud deny-role enum
has exactly one member, `cloud_read_only`, so a `deny_roles` list can never hold two elements and can
never differ from configuration in order. Keep `List`. No shape change, no semantic-equality modifier,
no capture.

**The instruction this ruling originally gave was impossible to carry out, and the way it went wrong is
worth more than the ruling.** It said to confirm the returned order by reading a member holding both enum
values. There is no second value to hold. The two-member enum — `image_reader` and
`image_reader_no_base_images` — is the **organization**-scope one, and it was carried into a cloud-scope
discussion. This document states the correct fact about 380 lines above the ruling that contradicted it.
**When adding to a long record, the facts most likely to be contradicted are the ones already in it**,
because the section being edited is what is in view and the rest is not.

**What would make the hazard live, recorded because the shape decision is only free until it isn't.**
`deny_roles` deliberately carries no closed enum validator, since the roles API is actively extended and
a closed enum would reject a new backend value until the next provider release. So if a second cloud deny
role is ever added, the ordering question becomes real — and by then this resource will have shipped, so
the shape will be frozen exactly as the organization sibling's already is. The answer at that point is the
semantic-equality fix, not a shape change, for the reason already given: it works on a released schema
where a type change does not.

The `null`-versus-empty handling at `resource_cloud_access.go:1075-1091` is untouched by any of this. It
concerns whether the collection is absent or empty, not the order of what is in it.

Note what the correct normalization is not: sorting the value into state would diff forever against a
configuration that lists the two values in the other order. The fix is to keep the practitioner's order
when the sets are equal, not to impose a canonical one.

**Consistency did not win here; a released constraint did.** The rule against propagating a flawed
pattern for uniformity still holds — it simply does not apply until the pattern is shown to be flawed,
and one read decides that.

**J.17 — a configuration declaring exactly the imported members must plan empty after
`terraform import`,** with `projects` covering only config-named projects per J.10.

This criterion cannot be proved by the shape the committed fixture uses. An `ImportState` step
without `ImportStatePersist` runs in a throwaway working directory, so any later step plans against
what `Create` left rather than against what import recovered. Proving it needs the two-test shape
this repository already documents: `ImportStateCheck` asserting inside the import step what was
actually recovered, plus two sequential `Config`-only steps reconstructing the recovered state shape
and asserting the plan action on the second.

**J.19 — an out-of-band role change made through the CLI or the console may be INVISIBLE to `Read`,
and that must be settled before the resource claims drift detection.** Both first-party
administrative surfaces write the legacy three-value `permission_level` through the collaborators
endpoints, not the RBAC roles path this resource reads. At organization scope, the equivalent split
is confirmed and consequential: the `permission_level` write touches Postgres only while `base_role`
is read from SpiceDB, so `base_role` from the singular GET goes *permanently* stale after a
`permission_level` change.

If cloud scope carries the same split, the failure is severe and quiet: an administrator changes a
member's role in the console, and `anyscale_cloud_access` reports a clean plan against a role that no
longer matches reality. This is the same defect as the import-only frozen snapshot rejected under
J.10 — reporting a compliance the resource cannot verify — arriving through a different door.

Establish per-field provenance for the cloud-scope read the way it was established at organization
scope: which endpoint is authoritative for `base_role` after a legacy `permission_level` write, and
which for `deny_roles`. Do not assume the roles endpoint is the better source for both; at
organization scope it is the better source for one field and the worse source for the other. Until
this is answered, no documentation should promise that out-of-band role changes are detected.

**CONFIRMED LIVE, and the split is sharper than at organization scope.** The sequence below was run
against an ephemeral cloud, since it alters a member's access: an RBAC `base_role` was written and read
back, `permission_level` was then changed through the legacy collaborators endpoint and confirmed
changed in the search response, and the roles listing was re-read. `base_role` was unchanged;
`deny_roles` had gained the read-only entry. The cloud was destroyed afterwards and its deletion
verified.

Three separately-established facts predicted it, and are kept because the composition is what made the
capture cheap and narrowly targeted: the roles listing reads exclusively from
the authorization service's managed groups with no relational fallback; the legacy collaborator PUT
changes `permission_level` in the relational store and toggles **only** the read-only deny group in
the authorization service, never a base-role group; and both first-party administrative surfaces write
only that legacy path.

- **`base_role` drift through the CLI or console is invisible.** A legacy `permission_level` change
  writes no base-role group, and `base_role` is read only from those groups, so the value does not
  move. For a member who never held an RBAC role that means null indefinitely (J.21). For a member who
  does hold one, it means `base_role` reports the **old** RBAC role indefinitely after an administrator
  changes their permission in the console — the same staleness as organization scope.
- **The read-only restriction crossing its boundary is visible in `deny_roles`**, because the legacy
  PUT does toggle the read-only deny group and that is where `deny_roles` is read from.

So two fields inside one nested object have **opposite** drift-detection properties, and documentation
must not describe them in one breath. AC-27 converts to a documented limitation for `base_role`.

**State the `deny_roles` half narrowly; "`deny_roles` drift is detectable" overstates it in the
direction that hurts someone.** The capture exercised `writer` → `permission_level = readonly`, so it
confirms the read-only direction. A legacy change **between `write` and `owner` touches no deny group at
all**, so `base_role` stays frozen *and* `deny_roles` stays empty — that change is invisible in both
fields. What is detectable is the read-only restriction appearing or clearing, not out-of-band role
changes in general.

**One limit on the capture, recorded rather than glossed.** The test subject was a service account,
because every human identity in the test organization was either the caller or an organization admin and
both are guarded (see J.22). The mechanism — which store each endpoint reads and writes — has no
identity-type branch that any trace found, so the result is expected to generalize; but it was confirmed
on a service account, and this record does not claim more than that.

**ANSWERED — the design holds on this axis.** Enforcement is decided entirely by the relational
permissions table, never by the authorization service: every cloud-access permission check on that router
passes a bare action, and the dependency that would consult the authorization service returns false
unconditionally for a bare action. And the RBAC write this resource grants through **dual-writes** both
stores, as does the revoke path in reverse.

Two properties carry that conclusion, and it is only as strong as they are, so they are named rather than
left implicit:

- **No middle state is reachable where the authorization service is written and the relational store is
  not.** The write reverts the former on a failure of the latter, and when the feature flag is off the
  endpoint returns 501 and writes nothing. Had a one-sided outcome been reachable, the catastrophic
  version of this question would have returned in a subtler form — the write appears to succeed, the roles
  listing confirms it, and enforced access never changes.
- **The enumeration source is the membership search, which reads the table that decides access.** So a
  member holding enforced access always appears, even when the roles listing knows nothing about them. The
  design committed to search-as-primary for an unrelated reason and it turns out to be load-bearing here
  too.

**One narrow question this raises rather than settles, and it is the most consequential thing left on this
axis: can a legacy-only member be revoked?** Someone with a relational permissions row and no
authorization-service group. The revoke performs a group removal *and* a relational delete; if it assumes
the group exists, revoking such a member could fail or partially fail. That matters disproportionately
because legacy-only is what the CLI and console produce, making it **the most common revoke this resource
will ever perform**. An authority claim that cannot revoke the typical member is not an authority claim.
Two things may already answer it: authorization-service relationship deletes are normally idempotent, and
the cascade capture recorded as owed item 7 revoked an identity that may itself have been legacy-only —
check what that capture actually did before spending anything new.

**Out of scope, deliberately:** the revert-on-failure path is source-read rather than live-exercised. That
is a backend reliability question, and inducing a mid-write failure of the relational store is not
something this work should attempt.

**The original question, kept because the answer is only meaningful against it:** If authorization is
enforced from the relational `permission_level` for legacy members and from authorization-service
base-role groups for RBAC members, then two members carrying the same `base_role` in our state can hold
different real access, and this resource reports both as converged. That is not a drift-detection gap —
it is a question about whether the field this resource manages is the field that decides anything.
Establish where cloud access is enforced and which store that path consults; if both are consulted,
establish which takes precedence. Answer this before shipping a write path, because a resource that
manages the losing field is worse than no resource.

**J.20 — a cloud carrying any group policy binding is a structural blocker: `Create` and `Update`
refuse, `Delete` attempts and records.** This is J.9's shape rather than a new mechanism, and it is
the third structural blocker on this surface after `auto_add_user` and directory sync.

A separate Policy API grants cloud access to user-group principals, with a real CLI command behind it,
so it clears this repository's bar for a surface with a genuine consumption path. An asynchronous
reconciler then flattens group membership into ordinary per-user collaborator rows **in the same table
this resource reads**, carrying no field that marks the origin. Three consequences compose into the
ruling:

- The resource cannot distinguish a group-derived member from a manually granted one, so an
  authoritative reconcile revokes it.
- The reconciler re-synchronizes on policy or membership change rather than continuously, so a revoke
  may hold and then silently reverse. That is **permanent non-convergence against another
  controller**, which differs in kind from the undisclosed revoke this contract already accepts: that
  one is a single convergent act against someone no configuration named.
- The resource cannot express the group binding, so it cannot repair what it breaks. Authority over a
  set whose membership another system also writes is not authority.

**Detection is tri-state and must not be collapsed into a boolean.** Binding confirmed present:
refuse, and name the binding. Confirmed absent: proceed silently. **Undetermined**: proceed, warn
loudly, and never record the skip as a pass. Refusing on undetermined would brick the resource for
every token that cannot see the answer, which is the same trade J.2 makes for an unresolvable caller
identity.

**Which HTTP response maps to which branch was wrong in the first version of this ruling, and the
correction matters more than the error.** The original framing guessed that a 404 or a 501 meant
undetermined. Traced against the handler, a **404 is confirmed-absent** — it is what the endpoint
returns when no policy row exists, which is the ordinary state of nearly every cloud — and there is no
feature-flag gate on the read at all, so no real 501 path exists. The undetermined case is **403**,
because the read requires the same update-tier permission as the write and the caller must be an
organization admin.

That correction is not cosmetic: coding 404 as undetermined would emit a warning on essentially every
apply forever, which is precisely the always-on alarm J.2 rejects. **A ruling that contradicts another
ruling in this record is wrong on its face.**

**The undetermined branch is evaluated only when a revoke is pending.** 403 is not rare — this record
already establishes that the token running Terraform is usually a cloud collaborator or owner rather
than an organization admin — so warning on every 403 moves the always-on alarm from one branch to
another rather than removing it. J.9 already rules where a check like this belongs: at the entry of
the operation that would revoke, not in `ModifyPlan`. An apply that revokes nobody cannot clobber a
group-derived grant, so the question is irrelevant and the check should neither run nor speak. An
apply that will revoke gets the check, and a warning attached to a genuinely risky operation is an
alarm rather than noise.

This does not reopen J.9's uniformity argument, which governs **refusals** — a resource that refuses
only when a given apply happens to need a revoke is harder to reason about. There is no refusal in the
undetermined branch; only a warning is being scoped. The confirmed-present branch refuses uniformly,
exactly as J.9 does.

**A non-empty bindings list is the group case, confirmed rather than assumed.** Narrowing the refusal
to group principals specifically was proposed, on the reasoning that a policy carrying only individual
user bindings would not be this hazard. It collapses: the validation path treats every entry in a
binding's principals as a user-group identifier unconditionally and rejects the request if any fails to
resolve, so there is no user-principal variant and no type discriminator to inspect. A 200 with an
empty list remains confirmed-absent.

The rejected alternative is accepting the collision and documenting it. It fails because Terraform
silently fighting another reconciler is not a disclosable cost — there is no plan output that shows
it, the diff reappears at intervals governed by a system outside the configuration, and the practical
result is an operator who cannot tell whether their access policy is converged.

**J.21 — PARTIALLY FALSIFIED BY CAPTURE. Read the correction below before relying on any of it.** A
member created through the legacy path reads back the role they held **at creation**, not null. The
ruling's shape survives only for a member with a relational row and no authorization-service group, which
may be a far narrower population than the common case claimed below.

Confirmed live: a member added through the legacy create route alone, with no RBAC role write, appeared in
the roles listing immediately with a base role derived from the legacy permission level. A subsequent
legacy *alter* did not move it.

**Those two observations jointly rule out the obvious explanation, which is worth stating because it
settles the mechanism without visibility into the authorization service.** Were the roles listing falling
back to the relational permission level at read time, the alter would have changed the derived value too,
since the relational store did change. It did not. So the create path establishes the group and the alter
path never revisits it. The read really is authorization-service-only, as traced; what it holds is a value
**frozen at creation**.

**Two consequences, and the second is worse than what it replaces.**

- **The claim that the read-only build is non-functional on realistic clouds is withdrawn to what the
  capture supports.** It rested on every member reading back null against a `Required` attribute. If
  legacy-created members read back a real role, a configuration declaring that value plans empty and the
  read-only resource serves an audit. J.18 does not depend on this — no released tag contains the resource,
  so nothing is lost by not tagging — but the argument called the strongest one for it does not survive.
- **J.19's drift gap gets sharper, not milder.** A null role is visibly wrong and self-heals on the first
  apply. A role frozen at creation **looks right while being stale** after any console change, so a
  configuration declaring the stale value shows no diff and the resource reports converged while the
  console shows something else. That is the reports-a-compliance-it-cannot-verify failure rejected under
  J.10, arriving by a third door.

**Both named limits are now closed, and the gap is narrower and sharper than either version above.**

*Not identity-specific.* The same create-then-alter sequence on a human identity produced an identical
result: the roles listing populated at create, frozen across a legacy alter, with the deny role moving.

*A stale `base_role` IS repairable, and the repair is real rather than cosmetic.* Writing the role through
the RBAC path corrected the roles listing **and** moved the relational permission level back — on a member
with prior legacy history, which independently re-confirms the dual-write property that closed the
enforcement question. So the gap is **detection-only**. Stated precisely: this resource has no repair
mechanism, and it cannot self-heal on refresh, because `Read` never writes. Repair is simply what a normal
apply already does, applied here — once a practitioner declares the correct role, the resource makes it
true in both stores.

**What remains invisible is exactly one class of change, and it is the privilege-relevant one.** Compose
J.19's per-field split with the frozen-at-creation behavior:

- A console change **crossing the read-only boundary** is visible — not through `base_role`, which stays
  frozen, but through `deny_roles`, which moves. It produces a real diff, and applying repairs both fields.
- A console change **between `write` and `owner`** touches no deny group and does not move `base_role`. It
  is invisible in **both** fields: no diff, no repair, and the resource reports converged while enforcement
  says otherwise.

**Say the dangerous direction out loud in the documentation.** A member promoted to `owner` in the console
stays `owner`, undetected, while state and plan both say `writer`. That is a privilege-escalation blind
spot rather than a reporting inconvenience, and it is the one drift case this resource cannot surface at
all. The mitigation available is a practitioner declaring roles explicitly and applying, which repairs
whatever the console did — but they have no signal telling them to.

**The correction's own limits, recorded because the original ruling over-read a source trace.** The
create-side mechanism remains unexplained: the legacy create path traces as a relational insert with no
authorization-service call. The read-time-fallback hypothesis is excluded by deduction from two
observations rather than by its own capture.

**The original ruling follows, superseded in its premise but not in its reasoning about
derivation.** A member holding only a legacy grant reads back with `base_role` null, and that is correct;
do not repair it by deriving a role from `permission_level`. The roles listing has no Postgres
fallback, and both first-party administrative surfaces write only the legacy path, so this is the
common case on a real cloud rather than an edge. `base_role` is `Required` with no `Computed`
(`resource_cloud_access.go:296`), and the read leaves it null when the roles listing says nothing
about a member (`:1060-1073`).

**The consequence is a defect in the read-only build, not in the write path.** A configuration
declaring such members diffs on every plan, and while `Create` and `Update` refuse, that diff can
never be applied. So the read-only resource produces a permanent unresolvable plan on a realistic
cloud — and the use that breaks, a configuration-managed audit, is the one that justified registering
it read-only in the first place. Once writes exist it self-heals: the first apply writes through the
RBAC path, which creates the group the roles listing reads, and the second plan is empty. **This is
the strongest argument for J.18's sequencing**, and it is stronger than the compatibility argument,
because it is about the resource being broken rather than about who might be affected.

Deriving `base_role` from `permission_level` was considered and rejected. It does not even remove the
diff: `permission_level` collapses a larger role set into three values, so a member the practitioner
declared as `writer` would read back as `collaborator` and diff anyway — the derivation changes
*which* members diff rather than whether they do. And it would report a role the roles system does not
record, which is inventing state to make a plan look clean. Null is the honest answer: this member has
no RBAC role recorded, and the first write establishes one.

**J.22 — declaring an organization admin as a member is an error raised before the first write.** The
backend refuses the roles write for such a member outright: *"You cannot modify an organization admin
cloud role."* Observed live, alongside the already-known self-modification guard. This is the **fourth**
structural constraint on this surface, after `auto_add_user`, directory sync, and group bindings.

**What makes it a ruling rather than a footnote is where the failure would otherwise land.** Discovering
it mid-reconcile forces a choice between two things this contract forbids. Returning the error taints
the state, per the never-error-after-a-successful-write rule. Recording it in `unmanaged_grants` violates
the asymmetry that governs that attribute: we may warn about what we could not **un**-apply, but must
never silently fail to apply what configuration **declared**. A failed *grant* is not an
`unmanaged_grants` case, and treating it as one would quietly convert a declared intent into a logged
regret.

So it is preflighted: raised in `ModifyPlan`, with the same placement reasoning and the same
skip-if-unresolvable discipline as the caller check, since it needs an API call and errors before any
mutation are explicitly permitted.

The mirror side is already consistent, which is what makes this coherent rather than a special case:
organization admins hold cloud roles while being filtered out of the member-search endpoint, so they are
never revoked either. Admins are outside this resource's scope in **both** directions, exactly as the
caller is — and for the same reason, that the backend refuses to let this resource touch them.

**A test-fixture ruling that follows from how this was found.** The identity used to confirm J.19 was a
newly created service account, chosen on the premise that no safe non-admin human identity existed. That
premise was wrong: this repository designates one, `ANYSCALE_TEST_USER_EMAIL` — an existing accepted
organization member kept for testing, with **no clouds assigned**. That is precisely the right shape
here, because granting it access to an *ephemeral* cloud and revoking it again is fully reversible and
touches no organization-level role. Its documentation sits under the organization-user tests and does not
announce itself as usable for cloud scope, which is why the next person would make the same call.

**J.22 revised — the preflight is a best-effort early warning, not a guarantee, and the reconcile
carries the real protection.** The first version of this ruling assumed a readable field could predict
the refusal. None can, and the reason generalizes.

The backend guard that raises the refusal checks **organization-owner managed-group membership in the
authorization service** — not the `is_admin` column on the permissions table, and not any relational
role field. That signal is subject to the same read/write split confirmed under J.19: legacy
`permission_level` writes touch the relational store only. So a member promoted through the legacy path
is absent from the group and the backend does **not** refuse them, while a member in the group whose
`permission_level` later changed relationally still trips the refusal. The second direction is the
damaging one, and its mechanism is a confirmed fact on this surface rather than a hypothetical.

Three parts follow:

1. **The preflight is best-effort by construction.** This must be stated in the code and in the
   documentation, because a guard *described* as a guarantee is worse than one described as a courtesy:
   it discourages writing the reconcile defensively, which is where the actual protection lives.
2. **Use the proxy whose staleness matches the target's.** `base_role` from the **singular**
   organization-collaborator GET is read from the same store as the group membership, so the two go
   stale *together* against a legacy `permission_level` write — which is exactly the property a proxy
   needs. The relational listing's `permission_level` drifts against the real signal instead. **The
   field's known defect is what makes it the right choice here**, and an earlier instruction in this
   round said the opposite for the opposite question: the right source depends on which store the thing
   being predicted lives in.

   **On the `is_admin` column, corrected.** It was briefly recorded here as possibly a stale
   migration-era backfill. That was wrong — it is maintained live, written on owner promotion. The
   correction does not restore it as the right signal, because the store-matching argument is unchanged:
   maintained by the *relational* promotion path, its drift matches `permission_level`'s rather than the
   group membership's. **One sub-question is genuinely open and would flip this** — if the
   authorization-service write path *also* maintains `is_admin`, then the column is updated on ownership
   changes by either route, which would make it a union signal and better than `base_role`, tracking the
   target without inheriting the target's staleness. Settle it by reading whether that path writes both
   the group and the column, or only the column alongside a relational change. Until then `base_role`
   from the singular GET stands, labelled a proxy.

   The mischaracterization is worth keeping visible for its shape rather than its content: it came from a
   grep that found no current writers, which missed the real call site on a singular-versus-plural
   function name. **A grep returning nothing is evidence only if the pattern could have matched.**
3. **A refusal arriving mid-apply must remain visible on the next plan.** This is where two standing
   rules appear to collide — never error after a successful write, and never silently fail to apply what
   configuration declared — and visibility resolves it. It needs no knowledge of who is an admin, which
   is why it and not the preflight is what protects a practitioner.

   **Corrected, because the first wording of this part was too strong and pointed at a shape that cannot
   work.** It said state must not claim a grant that did not happen. Taken literally that means omitting
   the failed member from state or writing its `base_role` null, and **both diverge from the plan on a
   path Core checks, so both trip "provider produced inconsistent result after apply"** — the very error
   AC-21 exists to avoid.

   The requirement is narrower: the failure must be visible **on the next plan**, not in the state this
   apply writes. The refresh delivers that for free. A member whose grant failed has no roles entry, so
   `base_role` reads back null under J.21; a member whose grant failed outright is absent from
   member-search and drops out of state. Either way the following plan re-proposes the grant.

   So the shape is: write the full planned map, return **success** with a warning, record the ungranted
   members, and let the next refresh surface it. The accepted cost is the one already recorded for the
   fatal path — between a failed apply and the next refresh, state claims members that were never
   granted.

**The grant path has this bug today, and it is worse than the revoke path ever was.** Verified in code:
the reconcile's grant loop returns on the first failure after earlier members in the same loop may
already have been granted, and the apply writes `state.Set(plan)` then returns on error, skipping the
read-back. An errored apply with persisted state is tainted, so the next apply is a destroy-and-recreate
— and this resource's destroy revokes every member state believes it manages, **including the ones that
really did get granted**. One grant failure schedules a mass revoke. It is not specific to the org-admin
refusal; any grant failure reaches it.

Three constraints on the fix:

- **A grant failure suppresses the destructive half of the apply, not the additive half.** There are two
  distinct aborts in that code path and the original return conflated them: stopping the *remaining
  grants*, and stopping before the *destructive phases* — project drops and cloud revokes. Only the
  second is correct.

  Attempt every declared grant, and every additive project grant. Skip project drops and cloud revokes.
  Continuing the grant loop is not merely permitted but required: grants are per-member and idempotent
  under the SET semantics V8 confirmed, a grant can only add access, and skipping one because an earlier
  one failed is silently declining to attempt something the configuration declared — which the
  asymmetric rule forbids. We may fail to un-apply; we must never fail to *try* to apply.

  The original comment's reasoning survives under this narrower reading and is worth carrying into the
  replacement. Configuration declares A, B and C; B's grant fails; revoking everyone outside that set
  would leave the cloud with less access than intended and, at worst, without an owner. That is the J.5
  hazard, and it concerns destructive actions taken while the intended end state was not reached — not
  grants.

- **A post-apply read may write only `Computed` attributes.** This is the general form of the rule, and
  it is broader than the partial-failure path that exposed it.

  `member` is `Required` with no `Computed` sub-attributes, so Core requires the post-apply state to
  equal the plan. A post-apply re-read can therefore only return exactly the plan, in which case writing
  it accomplishes nothing, or return something different, in which case writing it trips
  "provider produced inconsistent result after apply". Zero value, non-zero risk.

  The happy path has survived this because a successful write usually reads back identical — usually, not
  always. These reads are served from the authorization service and nothing establishes that a read is
  immediately consistent with the write that preceded it; J.19's capture observed a prompt read for one
  sequence, which is not a guarantee. **A transiently stale re-read on a fully successful apply would
  trip the same error with no failure anywhere.**

  So the fix is not to skip the read-back on the failure path but to stop it touching `member` on any
  path — fewer branches, and it removes a latent eventual-consistency defect rather than one instance of
  a partial-failure one. `Computed` attributes are exempt because their planned value is unknown, which
  is the latitude Core grants them and the reason `unmanaged_grants` can be populated this way at all.
  `projects` is `Optional` with no `Computed` and falls under the same prohibition as `member`.

  Blast radius, stated so it is not mistaken for a shipped defect: this path is in code merged with
  #260, but `applyCloudAccess` runs only on write operations and every one of those refuses today. Gated,
  not reachable. It does not change the no-premature-tag position under J.18 and it is not a hotfix.

- **Record the skipped revokes, not only the failed grants.** When a grant failure suppresses the revoke
  phase, those members really do still hold access the configuration says they should not, which is
  exactly what `unmanaged_grants` means — so they belong there, with a reason distinguishing
  *skipped-because-a-grant-failed* from *attempted-and-refused*. Without this the apply reports a grant
  problem and stays silent about nobody having been revoked, and an operator reading the ungranted list
  would reasonably conclude the rest of the apply completed.
- **Grants need their own recording channel, separate from `unmanaged_grants`.** That attribute means
  *could not revoke*. Overloading it makes `length(...) > 0` ambiguous between someone holding access
  they should not and someone lacking access they should have — opposite problems demanding opposite
  responses.
- **The warning names the member and the reason**, as on the revoke side.

**Both of this round's part-2 and part-3 answers turned on two rulings interacting rather than on either
alone.** Part 2 chose the closest-matching signal over the superset one *because* part 3 makes a false
negative degrade gracefully while a false positive hard-blocks a valid configuration with no workaround.
Under part 3's earlier shape, where a miss was catastrophic, the superset signal was the correct choice.
The same pair of costs inverts depending on a ruling made elsewhere, so neither can be settled in
isolation.

**A live confirmation of the group-membership signal was proposed and deliberately declined.** Both
available forms require temporarily promoting an identity to organization owner, which is a real
privilege escalation on a real organization and is not covered by the standing real-infrastructure
authorization — that covers clouds and what they provision, and a disposable test identity does not make
organization ownership disposable. It was declined on value rather than only on cost: under part 1 the
capture could only sharpen a warning, while part 3 needs no such knowledge at all.

**Do not create service accounts a test cannot delete.** No `api/v2` delete path for them was found, and
they do not appear in the organization-collaborator search, so one leaks per run — which is worse than
leaking once. If a delete path exists only through the CLI, drive cleanup through the CLI rather than
reverse-engineering an endpoint; if it exists nowhere, that is a reason not to create them in automation
at all.

## Acceptance criteria for the write path (AC-1 – AC-36)

Numbered `AC-` rather than continuing the legacy `criterion N` sequence, which runs to 31 and whose
authoritative list is not in this repository — a collision there would be silent and unresolvable.

Each criterion is stated so it can be exercised without reference to how the resource is built. Where
a criterion needs a live backend, that is marked, and the distinction is not cosmetic: a mock cannot
establish that a request shape is one the real API accepts, and this write path's request shapes were
recovered from a deleted implementation rather than confirmed.

**Two rules govern the set as a whole.**

*A criterion guarding a regression is not met until it has been proven red.* Introduce the
regression, confirm the test fails, revert byte-clean. This is repeated here because the failure it
prevents has occurred on this exact surface: a `mount_targets` import test passed only because its
mock omitted the field the fix concerned.

*Live write coverage never runs against the shared static cloud.* An authoritative `Create` revokes
every undeclared member, and that fixture's member list plausibly includes real identities including
whoever is running the tests. Write criteria require a freshly created cloud whose entire member set
the test owns and destroys. Read criteria against the static cloud are fine.

**Read and import**

- **AC-1** *(live)* A cold import of a cloud with several members populates `member` with exactly the
  real member set minus the caller. Asserted with `ImportStateCheck` inside the import step, which is
  the only place that sees what import actually recovered.
- **AC-2** A configuration declaring exactly the imported members plans empty. Requires the two-test
  shape of J.17; a three-step create-import-reapply sequence cannot prove it.
- **AC-3** `projects` is populated only for configuration-named projects. A member holding a project
  role on a project no configuration names reports none, per J.10.
- **AC-4** `Read` removes the resource from state only on a genuine not-found. A 500 or a timeout
  surfaces as a diagnostic and leaves state intact — criterion 31 applied to this resource.
- **AC-5** A 501 from any path names both feature flags. A 501 surfacing as a generic read failure
  fails this criterion, because it sends the operator to look at permissions or networking instead of
  at the flags.

**Create**

- **AC-6** *(live)* `Create` against a cloud holding undeclared pre-existing members revokes them.
- **AC-7** `Create` never revokes the caller, including when the caller is a pre-existing member and
  is undeclared.
- **AC-8** Declaring the caller in `member` is a plan-time error, raised from `ModifyPlan`. An
  unresolvable caller identity skips the check without erroring and without counting as a pass.
- **AC-9** `Create` and `Update` refuse on an `auto_add_user` cloud, and the message names the remedy
  rather than only the condition.
- **AC-10** `base_role = "owner"` with `deny_roles = ["cloud_read_only"]` is a plan-time error,
  implemented as a literal equality test. A test that would also pass against an `OneOf` enum
  validator does not meet this criterion, because a closed enum on `base_role` is itself a defect.
- **AC-11** An empty `member` map without `allow_empty_member_set` is a plan-time error.

**Update**

- **AC-12** Within one apply, project drops and role changes on retained members are issued before
  adds, and revokes last, per J.5. Assert on request ordering against a mock.
- **AC-13** Re-applying an unchanged configuration produces an empty plan and issues no writes.
- **AC-14** A role change on a retained member converges in one apply; the following plan is empty.
- **AC-15** *(live)* Dropping a project entry from a member revokes that project role.
- **AC-16** A member drop issues **no** subsequent project-scope revoke — the cloud-scope revoke
  already cascaded. Assert that no such request was made; a 404 absorbed there would look like
  success while hiding a false `unmanaged_grants` entry.

**Delete**

- **AC-17** *(live)* `Delete` revokes every managed member and leaves the caller's access intact.
- **AC-18** `Delete` on an `auto_add_user` cloud attempts the revokes, records each failure, and does
  not refuse — the deliberate asymmetry with AC-9, since refusing would strand the resource in state.
- **AC-19** A failed `Delete` leaves state untouched and retries to an empty plan.

**Partial failure and state**

- **AC-20** A failed revoke records an `unmanaged_grants` entry, emits a warning naming the email and
  the reason, and the apply **succeeds**.
- **AC-21a** **ANSWERED — design blocker cleared.** Core permits the fatal-path shape: writing a full
  planned collection to state after partial real writes, recording the shortfall, warning, and
  returning success does **not** trip "provider produced inconsistent result after apply". Confirmed by
  a real `resource.Test` against a throwaway provider built for this question alone, run early
  deliberately because it needed no write path. The reasoning it verifies is about unknown-ness — with
  no `Computed` sub-attribute, Core's plan for the collection is never unknown and equals configuration,
  so writing the planned value back matches the plan — and that argument holds identically for a nested
  map whose sub-attributes are `Required` or `Optional`. The never-error-after-a-write ruling therefore
  has a safe shape and is not revisited.
- **AC-21b** **NOT met by AC-21a, and the gap is specific.** The gate resource's collection is a flat
  `MapAttribute` of strings; the real `member` is a `MapNestedAttribute` whose object carries
  `base_role`, `deny_roles` and `projects`. Core compares consistency per attribute **path**,
  recursively, so a nested object map has strictly more paths on which an implementation can write
  something differing from the plan — and this resource does something non-trivial on two of them.
  `deny_roles` deliberately keeps prior state's shape when the API returns empty, because the wire
  cannot distinguish never-declared from declared-as-empty (`resource_cloud_access.go:1075-1091`); if
  the fatal path emits null where the plan held an empty list, that path diverges and trips the exact
  error AC-21 concerns. A flat string map cannot surface it. Re-assert against the real schema once a
  write path exists. Two inputs separate correct from broken: a member whose planned `deny_roles` is an
  **empty list** — not null and not populated — and a **partial-failure apply whose post-apply re-read
  genuinely differs from the plan**. The second is the sharper of the two, because it is the case the
  read-back prohibition under J.22 exists for. `projects` is the same class.
- **AC-21c** **CONFIRMED.** The converse of AC-21a, which nothing had established until it was built.
  AC-21a proved only that writing a value **matching** the plan succeeds; the read-back prohibition rests
  on the other direction — that writing a value **differing** from the plan for a `Required`
  non-`Computed` attribute really does trip the inconsistency error rather than being tolerated. A real
  `resource.Test` now writes one diverging entry as a stale-read-back stand-in and asserts Core's own
  inconsistency error. So the rule rests on a measured mechanism in both directions rather than on the
  half that happened to be convenient.

  **Do not "improve" this test by giving it the real nested schema — that would confuse it with AC-21b.**
  AC-21c is about **Core's** behavior, so a flat `Required` collection demonstrates the mechanism
  completely; nesting adds more paths on which divergence can occur without changing whether divergence
  is rejected. AC-21b is about **our implementation's** behavior, which is why it needs the real schema.
  The two look similar and are asking different questions of different systems.
- **AC-22** Following AC-21, the next refresh corrects state to reality and the next plan shows the
  remaining work.
- **AC-23** No write path produces a tainted state. Exercised as the consequence rather than the
  mechanism: fail a write mid-reconcile and assert the next plan is **not** destroy-and-recreate.
  Stating it this way matters — the mechanism is invisible from configuration, but a scheduled
  recreate of a resource whose destroy revokes a whole member list is exactly the harm.
- **AC-24** A recorded `unmanaged_grants` entry never suppresses a later attempt: with the backend
  failure removed, the next apply converges and the attribute empties, per J.12.
- **AC-25** Retry is bounded and selective, per J.13: a 429 followed by success converges, and a 403
  is attempted exactly once. Assert the request count, not merely the outcome.

**Drift**

- **AC-26** *(live)* An out-of-band grant made through the roles path is detected on refresh and
  revoked on the next apply.
- **AC-27** **Conditional on J.19.** An out-of-band role change made through the legacy
  `permission_level` path — which is what the CLI and console actually write — is detected on refresh.
  If J.19 establishes that cloud scope carries the organization-scope read/write split, this criterion
  is not met and cannot be met, and it converts into a documented limitation. It must not be quietly
  dropped: the resource would then be blind to changes made through the only two administrative
  surfaces most operators use.

**Guards and consistency**

- **AC-28** Two `member` keys differing only in case are rejected at plan time. Terraform's own
  duplicate-key check is case-sensitive, so it does not cover this.
- **AC-29** `unmanaged_grants` is empty after a fully successful apply, which is what makes
  `length(...) > 0` a usable alarm rather than a permanent one.
- **AC-30** The criteria above hold on at least one non-AWS compute stack, or this record states the
  coverage limit explicitly. No cloud-provider-specific gate is known on these endpoints, but "no
  gate found in source" and "confirmed on another stack" are different claims.

**Added with J.20 and J.21**

- **AC-31** *(live)* **Premise under revision — see J.21's correction.** Written on the assumption that
  legacy-only members read back null; a legacy-*created* member reads back a role frozen at creation
  instead, so the self-heal this criterion describes may not be the behavior to assert. Re-derive it before
  building it: the case worth asserting is now most likely a member whose console-changed role is stale in
  the roles listing, and whether a configuration declaring the *current* role converges in one apply. As
  written: on a cloud whose members hold **only** legacy grants — no RBAC roles entry, which
  per J.21 is the common real-world case — the first apply converges and the second plan is empty. This
  is the self-heal, and it is the most realistic adoption path this resource has. Note that AC-2 must
  therefore be exercised against members granted through the RBAC path; run against a legacy-only cloud
  AC-2 fails, and that is the criterion working rather than a defect in it.
- **AC-32** A cloud carrying a group policy binding refuses `Create` and `Update` with a message naming
  the binding, and `Delete` proceeds. All three branches of J.20's detection are covered with their
  corrected response mapping: **200 with a non-empty bindings list** refuses, **404 or 200-with-empty**
  proceeds *silently*, and **403 or any other non-2xx** proceeds *with a warning*. Two assertions are
  easy to omit and are the point of the criterion: the confirmed-absent branch must emit **no** warning,
  since a warning there fires on nearly every cloud; and the undetermined branch must be evaluated only
  when a revoke is pending, so an apply that revokes nobody stays silent even under a 403.
- **AC-35** A grant failure part-way through a reconcile leaves the apply **successful**, records the
  ungranted members in their own attribute, warns naming member and reason, and aborts the remaining
  phases without revoking anyone. The criterion that catches the live bug: after such an apply, the next
  plan must **not** be a destroy-and-recreate, and the members that did grant successfully must still
  hold their access. Assert the second part against the backend, not against state — state is exactly
  what is untrustworthy on this path.

  Two further assertions, because a fix can satisfy the above and still leave the operator with a false
  picture: that the revoke set is **untouched** — nobody revoked — and that **both** channels are
  populated, the failed grants in their own attribute and the consequently-skipped revokes in
  `unmanaged_grants` with a distinguishing reason. A fix that records the failed grant and silently drops
  the revokes passes a narrower reading of this criterion.
- **AC-36** After a grant failure, the next refresh surfaces the shortfall and the following plan
  re-proposes the failed grant. This is what makes AC-35's write-the-planned-map safe rather than
  concealing; without it, the same apply looks identical to a clean one.
- **AC-34** Declaring an organization admin in `member` is an error raised at plan time, before any
  write is attempted, per J.22. Two assertions carry the criterion: that **no** mutation was issued, and
  that an unresolvable admin lookup skips the check without erroring and without being recorded as a
  pass. A test that only asserts the error message would pass against an implementation that discovers
  the problem mid-reconcile, which is the failure the ruling exists to prevent.
- **AC-33** A member list spanning more than one page is enumerated completely. Pagination on these
  endpoints is in the **query string**, and a request that omits it returns a valid first page with no
  error, so the regression is silent and its consequence is revoking everyone past the page boundary.
  Prove it with a mock returning a `next_paging_token`, and mutation-prove it by removing the paging
  loop: a test that passes with the loop gone has not tested this.

## Wire-shape facts the read path depends on

Recorded separately from the rulings because these are properties of the API rather than decisions,
and because each one, if forgotten, produces a member list that is **wrong** rather than one that
errors — and a wrong member list, in an authoritative resource, is a revoke.

**Reading one cloud's member list is a join, not a call.** The collaborator search endpoint supplies
identity only (email, identity ID, user ID); the roles endpoint supplies base roles and deny roles
keyed by user ID and carries no email. Neither alone can produce a map keyed by email and valued by
role. The join key is the user ID, which is **not** the identity ID — the collaborator delete takes
the identity ID while the roles endpoints take the user ID, and conflating the two is a live hazard
rather than a naming inconvenience.

**Pagination on the search endpoint is a split transport, and getting it wrong fails silently.**
`count` and the paging token belong in the URL query string while the filter belongs in the JSON
body. Sending pagination in the body returns a valid first page and **no error**, so the observable
failure is a truncated member list. For a resource that revokes whoever it was not shown, that means
quietly revoking every member past the page boundary. This is the same silent-truncation shape this
repo has been bitten by before when a call moved between API generations, and it is the reason this
class of bug is worth a paragraph rather than a comment.

**Do not trust the response's total; follow the paging token to exhaustion.** Response metadata is
suspected of computing totals from unfiltered rows. The robust implementation does not merely avoid
reading the total — it declines to parse the field at all, so no later change can start trusting it.
The same reasoning applies to the search endpoint's `permission_level`: leaving a known-untrustworthy
field out of the parse struct is stronger than parsing and ignoring it, because a struct field is an
invitation.

**The search endpoint's `permission_level` is lossy and must not be used as the member's role.** It
has been observed misreporting a `workload_operator` as a plain `readonly`. **The provenance of that
observation is not recorded and should be re-confirmed by a live capture** — the entire join design
rests on this field being untrustworthy, so it deserves better than an undocumented assertion. Until
then, treat the join as correct for its own reasons (only the roles endpoint carries deny roles at
all) rather than as justified solely by this field's unreliability.

**Organization admins are invisible to this resource, and that is load-bearing rather than
incidental.** The search endpoint filters them out server-side, so a roles entry with no matching
collaborator has no email to key it by and is dropped. The consequence cuts both ways: an admin's
access can neither be seen nor revoked here, which is why a non-admin cloud owner is the genuinely
exposed case in the authoritative-Create ruling above.

**A collaborator with no roles entry is kept, never dropped.** That is the ordinary shape of anyone
added through the console's legacy path, and an existing member missing from state is invisible to an
authoritative write — which is to say, silently revoked. For the same reason a member whose roles
entry is absent or ambiguous reports a null `base_role` and stays visible, rather than being omitted
or having an element indexed out of a list that can legitimately hold more than one.

**Preserve the prior null-versus-empty-list shape of `deny_roles` when the API reports none.** The
wire cannot distinguish an omitted attribute from an explicit `[]`, so reproducing one shape
unconditionally guarantees a permanent diff against the other. On a cold import there is no prior
shape to preserve, so it lands null and a configuration declaring `[]` shows one first-plan diff that
then stabilizes — minor, self-healing, and worth asserting in a test rather than discovering.

**Fold email case on every prior-state lookup.** Anyscale treats email identity as case-insensitive.
An exact-match lookup reads one person as simultaneously a new member and a departed one, which in an
authoritative resource means granting and revoking the same human in one apply.

## What can block a revoke

Source-traced from the cloud collaborator removal path, not live-confirmed. Enough to design
against; not enough to skip the captures below.

**There are three structural blockers, not one.** Only the first was previously accounted for.

1. **`auto_add_user` on the cloud** — 409, detail "Users cannot be removed from clouds which have
   auto add users enabled." Per-cloud, and it blocks every revoke on that cloud.
2. **Directory sync combined with the Policy API** — 409, raised for any identity that is **not** a
   service account, when the organization has both a directory ID and at least one resource
   permissions row. Organization-wide rather than per-cloud. Narrower than "SCIM breaks revokes":
   a SCIM organization with no Policy API bindings stays on the legacy path and is unaffected. But
   where it does apply, every revoke of a human member fails while service accounts still succeed —
   a split that will look like random failure if it is not anticipated.
3. **The target is not a member of the cloud** — 404, distinct from the two above and not an error
   condition worth surfacing as one during a reconcile.

**The two 409s are mutually ambiguous, and the diagnostic must not guess between them.** They arise
from unrelated causes on the same operation and differ only in their detail text. This is the same
shape as the two-flag 501 already documented on this surface, and it takes the same answer: name both
possibilities, or distinguish on the detail string — never pick the likelier one and report it as
fact.

**The cascade is real.** Removing a cloud collaborator recursively revokes permissions on child
resources, projects included, so a member drop must not then attempt per-project revokes the cascade
has already performed. Recorded with its limit: the handler's stated contract and its call into the
batch delete helper were traced; the helper's own body was not. Design against it, but confirm by
capture that a member drop leaves no orphaned project grants before relying on it in the reconcile.

**A fixture hazard worth stating because it would not be caught by review.** The directory-sync
check's own docstring says it raises 403. The code raises 409. A fixture built from the prose is
wrong in exactly the way that makes a broken revoke path pass green. Read the raise, not the comment
above it.

## Verification still owed before the reconcile is built

Nothing in the reconcile rulings above has been confirmed against a live API or a real Terraform
run; all of it is source-traced or derived. The design-verification policy in `CLAUDE.md` requires
both gates at design-confirmation time, so these are listed as owed work rather than as background
detail.

**Live API shape:**

1. **CLOSED, with one part reclassified as moot rather than verified.** All four routes the import
   fixture pins were confirmed against the live API and the published OpenAPI document, and match as
   pinned — which retires the hazard noted below about the correction whose source PR #259 deleted.
   Pagination was confirmed live and behaves exactly as the wire-shape section above states: a
   `count=1` in the **query string** truncates the response, and a real `next_paging_token` comes back.

   The suspected metadata defect — `total` computed from unfiltered rows — is **not closed by that
   capture and does not need to be.** The capture sent no filter, and with no filter applied both the
   defect and its absence predict the same `total`, so the observation cannot distinguish them.
   Establishing it would need a *filtered* search with a `count` below the filtered result count. It is
   moot for this resource because the provider deliberately sends an empty body and never filters, so
   even a metadata value derived from unfiltered rows would be the true total for the request actually
   made — and the read follows the token to exhaustion rather than trusting `total` in any case. Record
   it as inapplicable, not as verified; the distinction matters if a filtered search is ever added,
   which would reopen it.

   The route confirmation also carries a lesson about how the pagination question was first answered
   wrongly, and it is worth keeping because the error was reasonable. The search body schema declares
   `additionalProperties: false` with a single field, from which it was concluded that no pagination
   input exists at all. A body schema cannot describe query parameters. Separately, a live capture
   against a cloud with fewer members than the default page size cannot distinguish "unpaginated" from
   "one page sufficed" — the observation was sound and the inference was not licensed by it.
2. **CLOSED, and the answer is the contingency rather than the hoped-for case.** A legacy-only
   collaborator is invisible on the roles listing entirely — that listing reads only from the
   authorization service with no fallback, while the legacy add path writes only the relational row.
   Since both first-party administrative surfaces use the legacy path exclusively, this is the common
   case rather than an edge, so the membership search is **mandatory** as the enumeration source and
   the roles listing is supplementary only. The shipped read already works this way. See J.21 for the
   consequence, which is that such members read back with a null `base_role`.
3. Whether the identity ID returned by the organization-collaborator listing is the same identity the
   **project** collaborator write and delete endpoints match against. The cloud-scope delete already
   ships on this assumption; the project endpoints are new to this design and inherit nothing.
4. J.3's underlying question: does the membership bootstrap conflict for a roles-write-only user, or
   does it repair the missing membership edge?
5. **CLOSED.** The `auto_add_user` conflict envelope was captured live against a disposable cloud
   and matches the source trace byte-for-byte: 409, detail "Users cannot be removed from clouds
   which have auto add users enabled." Worth noting as a datum in its own right — on this surface, a
   careful backend trace predicted the wire exactly. That is evidence about how far such traces can
   be trusted here; it is not licence to skip the next one, since a trace that happened to be right
   and a trace that was verified are still different things.
6. **Whether the roles endpoints are unconditionally 501 on Azure deployments — at EITHER scope.
   Unsubstantiated, and it reached released documentation before anyone checked.**

   Every 501 site on both surfaces gates on a feature flag and none reads the cloud provider: at
   cloud scope, a read-only-role flag and two SpiceDB checks for cloud member and cloud role; at
   organization scope, a SpiceDB dual-write check for organization role and a workspace-blocking
   flag. Organization scope was checked *separately* rather than inferred from the cloud-scope
   result, since they are different services and the claim could have held in one and not the other.

   **The limit of that finding, stated so it is not over-read.** Absence of an Azure condition in
   code does not prove the claim false. Flag targeting could exclude Azure organizations as
   operational configuration, which is invisible here and would make the *outcome* true. What it
   does establish is that the stated *mechanism* is wrong: "unavailable regardless of the feature
   flag" contradicts a gate that is entirely the feature flag, and "support cannot change that" is
   advice against contacting the people most likely able to change flag targeting.

   **Do not try to settle this by testing.** There is no Azure cloud available, Azure infrastructure
   creation is not authorized, and the answer most likely lives with whoever owns the flag targeting
   rather than in any repository. The correct action is to stop asserting it, not to prove it.

   How it spread is the part worth keeping: it was copied out of `anyscale_organization_user_role`,
   which states it in several places and is already released, on the reasoning that an existing
   claim in this repository is a source. It is not. Each copy also strengthened it — the derived
   version added absolute emphasis and the instruction not to contact support, neither of which
   appeared in the original.
7. **CLOSED, and the cascade is real.** Confirmed live: an identity added as both a cloud
   collaborator and a project collaborator, revoked at cloud scope only, had its project
   collaborator entry gone on the next read with no project-scope delete issued. The reconcile must
   therefore **not** attempt a project-scope revoke after a cloud-scope revoke of the same member —
   the grant is already gone, and the attempt would either 404 or hit nothing, and a 404 misread as
   a failure would produce a false `unmanaged_grants` alarm. The same capture incidentally
   re-confirmed that the identity ID is consistent across the cloud and project collaborator
   endpoints.
8. **CLOSED as not testable and not needing a mechanism.** Recorded as a ruling rather than left open,
   because "unverified" invited someone to keep trying.

   **Not testable.** Reproducing it requires an organization with directory sync configured *and* at least
   one Policy API binding. Directory sync is an identity-provider integration at organization level, not a
   setting to toggle for a test, and configuring it on the test organization is out of scope for this work.

   **No mechanism is needed, and that is the substantive point.** A directory-sync 409 arrives on a
   *revoke*, which is already the converge-and-record case: record the entry, warn, succeed. Nothing about
   this blocker calls for a new code path. A preflight was considered and rejected for J.20's reason — the
   condition is organization-wide, so refusing on it would brick the resource for every directory-synced
   organization, which is a worse outcome than a recorded revoke failure.

   **The requirement reduces to the diagnostic, and the implementation already satisfies it.** The
   revoke-failure reason names `auto_add_user`, self-removal and the Policy API case each on their own
   detail substring, and its default branch names **both** known blockers and tells the operator to check
   both before treating the failure as transient. That is the discipline this document already demanded for
   two mutually ambiguous 409s: name both possibilities or distinguish on the detail string, never pick the
   likelier one and report it as fact.

   **Two things remain owed, neither of them a capture.** The documentation must name the
   service-account-versus-human split — on such an organization every human revoke fails while service
   accounts succeed, which reads as random failure to anyone who has not been told. And any fixture for
   this path must be built from the **raise**, not the docstring above it, which says 403 where the code
   raises 409; a fixture built from the prose would let a broken revoke path pass green.

   Original wording of the item, kept for the record: whether the directory-sync blocker behaves as traced — that it spares service accounts, that it
   requires a Policy API binding rather than merely a directory ID, and that it surfaces as a 409
   distinguishable from the `auto_add_user` one. This blocker was found late, is not in any earlier
   version of this design, and has never been exercised against anything.

**Framework and Core contract**, each needing a real `resource.Test` rather than a unit test, since
framework source describes the mechanism without revealing every constraint Core enforces:

1. **ANSWERED, and it changed the design rather than confirming it.** Core *does* persist state
   written via `resp.State` before an error return — but on `Create` it persists it **tainted**, so
   the next apply is a destroy-and-recreate rather than a retriable reconcile. The consequence, and
   the never-error-after-a-write rule it forces, are in the reconcile rulings above. The `Delete`
   half is clean: a failed `Delete` that never calls `RemoveResource` leaves state untouched and
   retries to an empty plan. The `Update` half remains untested — it cannot be driven without a
   write path — and should be closed once one exists.

   Two things about *how* this was answered are worth keeping. It was expected to surprise us and it
   did, in a direction nobody proposed. And it was caught because the first version of the test was
   **wrong** — it assumed `Update` would run next, failed for real, and that failure is what
   revealed the tainting. A test written to confirm the assumption would have passed and taught us
   nothing. An earlier attempt to settle this from framework source concluded the opposite-shaped
   answer for a defensible reason: the framework does put the state on the wire unconditionally. It
   simply does not decide what Core writes to disk.
2. **Answered** on the strength of this repo's own recorded history — a `Computed` list-of-objects
   left unknown has failed here repeatedly. No new test required.
3. That partial `member` writes followed by a full write on success do not trip "provider produced
   inconsistent result after apply". **No longer a formality.** Since item 1's answer, the entire
   fatal-path design rests on this: writing a full planned map to state after partial real-world
   writes is what avoids the taint, and if it trips an inconsistency error instead then the fatal
   path has no safe shape. Sequence it after a write path exists to drive it, and treat a failure
   here as a design blocker rather than a bug to work around.

**A committed fixture hazard, recorded because its evidence no longer exists.** The import
acceptance test for this resource pins the HTTP methods it expects on four routes, and two of those
four were identified as wrong. The correction was derived from the per-user resource that PR #259
deleted, so the source that substantiated it is gone from the working tree. Do **not** correct that
route list from this document, from memory, or from the deleted file's history: confirm all four
against the live API as part of item 1 above, which is now the only remaining source of truth for
them. The general lesson the finding carried is worth keeping — pinning a route in a comment is not
verifying it, and a permissive mock will happily serve routes the real API does not expose.

Separately, that fixture's premise no longer holds. It depends on an undeclared, pre-existing member
surviving all three of its steps, which authoritative `Create` now revokes at step one. Rework the
fixture rather than weakening the resource to preserve it; the cleanest rework makes that member's
revoke *fail* in the mock, so it legitimately persists through `unmanaged_grants` and the fixture
keeps testing what it was written to test while also exercising the converge-and-record path.

**Added with the J.11–J.19 rulings.** Four further items, each blocking the gate rather than merely
desirable, and each stated with what would close it:

9. **Can a group or team hold cloud access?** (J.14) Closed by establishing from the API whether any
   non-user principal can appear on a cloud's member set. A negative answer is as valuable as a
   positive one and must be recorded either way, because the authority claim currently rests on the
   answer being no and nobody has checked.
10. **Do service accounts appear as ordinary members, and is any identity structurally unrevokable?**
    (J.15) Closed by a read against a cloud that has at least one service account, plus whatever the
    directory-sync capture in item 8 reveals about how the backend classifies them.
11. **What order does the API return `deny_roles` in?** (J.16) Closed by one read of a member holding
    both enum values. Cheap, and the answer decides whether the attribute changes shape while that
    change is still free under J.18.
12. **Per-field provenance for the cloud-scope read after a legacy `permission_level` write.** (J.19)
    Closed by a live sequence: write `permission_level` through the collaborators endpoint, then read
    `base_role` from the roles path, and see whether it moved. This is the highest-value capture of
    the four — at organization scope the equivalent answer was *no, it goes permanently stale*, and if
    cloud scope behaves the same way then drift detection against console-made changes does not work
    and must not be documented as though it does.

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
