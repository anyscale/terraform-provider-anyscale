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

1. The cloud member-search response shape, and whether it is genuinely unpaginated. A suspected
   paging defect, where response metadata is computed from unfiltered rows, needs confirming or
   ruling out: if real, `total` can never be trusted and paging must follow tokens to exhaustion.
2. Whether a legacy-only collaborator — one who has never had a roles entry — appears in the roles
   listing at all. If they can be absent, the roles listing alone cannot enumerate the member set and
   the membership search becomes mandatory rather than supplementary.
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
6. **Whether the cloud-scope roles endpoints are unconditionally 501 on Azure deployments.** This was
   asserted while drafting the reconcile rulings and is **not** substantiated: the two-flag warning
   recovered from the removed resource says nothing about Azure, and the flag name does not appear in
   the backend service paths where such a gate would live. Treat "this resource cannot function on an
   Azure cloud" as unverified. It matters disproportionately because, if true, it belongs on the
   resource page rather than in a footnote.
7. **CLOSED, and the cascade is real.** Confirmed live: an identity added as both a cloud
   collaborator and a project collaborator, revoked at cloud scope only, had its project
   collaborator entry gone on the next read with no project-scope delete issued. The reconcile must
   therefore **not** attempt a project-scope revoke after a cloud-scope revoke of the same member —
   the grant is already gone, and the attempt would either 404 or hit nothing, and a 404 misread as
   a failure would produce a false `unmanaged_grants` alarm. The same capture incidentally
   re-confirmed that the identity ID is consistent across the cloud and project collaborator
   endpoints.
8. Whether the directory-sync blocker behaves as traced — that it spares service accounts, that it
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
