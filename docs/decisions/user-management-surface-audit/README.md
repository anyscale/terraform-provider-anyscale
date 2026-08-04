# Audit: user-management and RBAC surface

**Status: audit, no code change.** Measured at `0e862fd`. Every claim cites the `file:line` it was
read from; nothing here is inferred from a published doc page.

Companion to [`../rbac-surface-consolidation/README.md`](../rbac-surface-consolidation/README.md),
which records the design this audit measures against. Where the two disagree, that disagreement is
the finding.

The audit is framed around **use**, not files: what a practitioner must write to (i) add a person
to the organization with a role, (ii) grant them one cloud, (iii) grant a project role, (iv) revoke
all of it.

---

## The four tasks, answered

### (i) Add a person to the organization with a role — two resources, and a third that overlaps

```hcl
resource "anyscale_organization_user"      "x" { email = "a@b.com" }
resource "anyscale_organization_user_role" "x" { email = "a@b.com", base_role = "collaborator" }
```

The split is deliberate and stated in the schema itself
(`internal/provider/resource_organization_user.go:129`): roles are managed separately "so only one
of them ever writes a member's role." Membership and role are genuinely different lifecycles.
**Endorsed as-is.**

One backend constraint, already handled: declaring `deny_roles` routes the write to
`PUT /api/v2/organization_collaborators/{user_id}/roles`, which is SpiceDB-gated and returns 501 in
organizations without the feature. Omitting it stays on the ungated legacy path.
`resource_organization_user_role.go:135-138` detects the 501 and `:162-163` translates it into a
diagnostic phrased in terms of what the practitioner wrote, rather than surfacing a raw status code.

A third resource also invites. `anyscale_organization_invitation` and `anyscale_organization_user`
both POST `/api/v2/organization_invitations` through one shared helper
(`internal/provider/organization_invitation_shared.go:113`), which exists precisely because "any
guard added to one silently misses the other" (`:15-18`). See Finding 2.

### (ii) Grant one cloud — one resource, on a doubly flag-gated path

```hcl
resource "anyscale_cloud_user_role" "x" { cloud_id = "…", email = "a@b.com", base_role = "writer" }
```

Its schema description states the constraint plainly
(`internal/provider/resource_cloud_user_role.go:70`): the feature is gated behind two independent,
separately-controlled backend flags — one for reading roles, one for writing — and the provider
cannot detect this ahead of time short of trying.

### (iii) Grant a project role — no write surface exists

This is the largest gap in the surface, and it was opened deliberately. `anyscale_project` lost its
`collaborator` block in v0.25.0 (breaking, with a state upgrader);
[`../rbac-surface-consolidation/README.md`](../rbac-surface-consolidation/README.md) records under
Scope that there is "deliberately no in-provider replacement yet."

The intended replacement is `anyscale_cloud_access.member[*].projects`. That attribute exists in
schema, but the resource is unregistered and issues no API calls at all — an endpoint sweep over the
file returns nothing:

```console
$ grep -oE '/api/v2/[a-z_/{}%s.-]+' internal/provider/resource_cloud_access.go
$
```

A read path does exist, as a data source: `internal/provider/data_source_project.go:281` GETs
`/api/v2/projects/{id}/collaborators/users`. The provider can therefore **show** project
collaborators and cannot **change** them — it can surface drift it is powerless to correct.

### (iv) Revoke all of it — three of four destroys do not revoke

| Destroy | What happens | Assessment |
|---|---|---|
| `organization_user` | Cancels only a still-pending invitation this resource sent. Never evicts a human, never evicts an adopted member. | Correct, and documented under its own "Destroying this resource never removes a human" heading. |
| `organization_user_role` | Leaves `base_role` in place with a warning; clears `deny_roles` only if the config ever declared them (`resource_organization_user_role.go:739`). | Correct. A member always holds some base role, so there is no "no role" state to return them to — and the warning says so. |
| `organization_default_cloud` | State-only; no API call (`resource_organization_default_cloud.go:242`). | Correct for a singleton pointer, and documented. |
| `cloud_user_role` | Genuinely revokes, via `DELETE /api/v2/clouds/{id}/collaborators/{identity_id}`. If the member's permissions row predates the resource, destroy hits a 404 that no API sequence can repair; the only exit is `terraform state rm`. | The diagnostic for this case is thorough. The failure mode is inherent to the endpoint, not to the resource. |

**Net:** "remove this person's access to Anyscale" is not expressible in Terraform today.

---

## Finding 1 — the `deny_roles` ruling stopped at the resource boundary

**Severity: correctness of the published schema vocabulary.** Recommended first fix.

[`../rbac-surface-consolidation/README.md`](../rbac-surface-consolidation/README.md) states, under
"The `deny_roles` naming ruling":

> **Final ruling: `deny_roles` is the Terraform schema attribute name at both organization and
> cloud scope.**

Implemented on the resources; never applied to the data sources:

| Surface | Attribute | Source |
|---|---|---|
| `anyscale_organization_user_role` (resource) | `deny_roles` | `resource_organization_user_role.go:281` |
| `anyscale_cloud_user_role` (resource) | `deny_roles` | `resource_cloud_user_role.go:128` |
| `anyscale_organization_user` (data source) | `additional_roles` | `data_source_organization_user.go:47` |
| `anyscale_organization_users` (data source) | `additional_roles` | `data_source_organization_users.go:54` |

Same backend field, two provider names — and the data-source name is the semantically wrong one.
The consolidation record spends twenty lines establishing that "additional" is a misreading of the
OpenAPI spec, that these values strictly *subtract* capability, and that trusting the spec's name
once caused a ruling to be reversed. The provider now ships the corrected name on the write side
and the discredited name on the read side.

The user-visible harm is concrete. A practitioner reads
`data.anyscale_organization_user.x.additional_roles` to see what a member holds, then writes
`anyscale_organization_user_role.deny_roles` to change it. Nothing on either doc page indicates
these are the same set. The data source's own description
(`data_source_organization_user.go:76`) still describes the model as "migrating … to `base_role`
plus `additional_roles`" — naming the spelling the provider has since ruled against.

Both data sources also still expose `permission_level`, deprecated backend-side (noted in-code at
`data_source_organization_user.go:44` and `data_source_organization_users.go:51`) and carrying no
`DeprecationMessage`. That is not a gap unique to these two: a repo-wide sweep finds
`DeprecationMessage` set on **zero** live attributes. The only two assignments
(`resource_cloud_resource_upgrade.go:195` and `:513`) sit inside prior-schema definitions used by
state upgraders, describing a `status` attribute that was *removed* at schema v2. So the provider
currently has no working example of an attribute deprecation to copy — establishing that pattern is
part of the work, not an afterthought to it.

**Recommendation.** Add `deny_roles` as a Computed alias on both data sources (additive,
non-breaking); set `DeprecationMessage` on `additional_roles` and `permission_level`; remove the old
names at the next major. Duplicate the deprecation text into `MarkdownDescription` —
`tfplugindocs` does not render `DeprecationMessage`.

## Finding 2 — `anyscale_organization_invitation` models an API object, not a desired state

It manages an invitation row rather than a fact about who should have access. Its own
`MarkdownDescription` redirects the reader to `anyscale_organization_user` three separate times:

- "Use the `anyscale_organization_user` resource to manage their permissions."
- "To remove an existing member's access, destroy their `anyscale_organization_user` resource
  instead - destroying this resource never does that."
- "Inviting an email address that is already an organization member fails with a clear error
  directing you to the `anyscale_organization_user` resource instead."

It must also explain that re-inviting silently invalidates the prior invitation and mints a new
`id`, so a second resource block tracking the same address simply starts reading `expired`. That is
an API-object lifecycle leaking into Terraform state, and it cannot be fixed without changing the
resource's identity model.

`anyscale_organization_user` dominates it for the actual intent: it adopts existing members with no
API call, is safe to re-apply, and carries the same SSO and SCIM guards via the shared helper. The
only thing `organization_invitation` uniquely exposes is invitation metadata — `expires_at`,
`accepted_at`, `status` — which is data-source-shaped, not resource-shaped.

**Recommendation.** Deprecate the resource; do not remove it in the same release as
`cloud_user_role` (Finding 4). If the invitation metadata has real consumers, replace it with a data
source rather than retaining a resource whose Create sends mail.

## Finding 3 — `id` carries five different meanings across this surface

Not cosmetic: `id` is the import string, and the import string is the first thing a practitioner
must get right.

| Surface | `id` holds | Source |
|---|---|---|
| `anyscale_organization_user` (resource) | the email | `resource_organization_user.go:142` |
| `anyscale_organization_user_role` (resource) | the email | `resource_organization_user_role.go:235` |
| `anyscale_cloud_user_role` (resource) | `cloud_id/email` composite | `resource_cloud_user_role.go:80` |
| `anyscale_organization_invitation` (resource) | the invitation ID | `resource_organization_invitation.go:73` |
| `anyscale_organization_default_cloud` (resource) | the **organization** ID | `resource_organization_default_cloud.go:66` |
| `anyscale_organization_user` (**data source**) | the **identity** ID | `data_source_organization_user.go:61` |

The last row is the sharp edge: a resource and a data source share a type name and disagree on what
`id` holds.

**Severity, stated precisely.** The import path is *already* defended: `resource_organization_user.go:968-994` rejects any import ID without an `@`,
and specifically recognises an `ide_` prefix, telling the reader it looks like an identity ID and
that this resource is keyed by email. So feeding a data-source `id` to `terraform import` fails
loudly with a good diagnostic rather than importing the wrong object.

What remains is a clarity problem plus one unguarded path: wiring
`data.anyscale_organization_user.x.id` into an attribute that expects an email — for example
`anyscale_organization_user_role.email` — passes an identity ID where an address is required, and
that is caught only at API-lookup time, with a less pointed error. The fix is documentation on both
`id` attributes, not a new validator.

Three identifier namespaces are in play for one human — `email`, `user_id`, `identity_id` — with
`identity_id` present on `cloud_user_role` purely as destroy plumbing
(`resource_cloud_user_role.go:112`).

**Recommendation.** Keying resources on `email` is right and consistent; keep it. Document the
resource-versus-data-source `id` divergence explicitly in both `MarkdownDescription`s
(non-breaking, and **done** — see the commit that accompanies this correction); treat aligning the
values as a major-version item.

## Finding 4 — three removals are queued behind one unwritten runtime

Queued against this surface: remove `cloud_user_role`, deprecate `organization_invitation`, rename
`additional_roles`. Landing these close together — while `cloud_access` issues no API calls and
project roles have no write path — would leave the provider with *less* access-management
capability than v0.24 and no replacement in sight.

Removing a resource **type** is also the harshest change this provider can make. An adopter gets no
deprecation warning and no diff: Terraform cannot decode the state at all, fails with
unsupported-resource-type, and no state upgrader can help because the type is gone. The only exit is
`terraform state rm` before upgrading. That sentence belongs verbatim in any changelog note
accompanying such a removal.

**Recommended ordering:**

1. **Additive only, safe now:** `deny_roles` alias plus deprecations on the two data sources
   (Finding 1); `id` documentation (Finding 3); a `DeprecationMessage` on
   `organization_invitation` (Finding 2). No breakage, and each one makes the eventual removals
   legible in advance.
2. `cloud_access` read path — Read, ImportState, and the `auto_add_user` refusal. Registers
   nothing, revokes nobody.
3. `cloud_access` write path, including `member[*].projects` — this is what closes gap (iii).
4. Remove `cloud_user_role`, in the PR that registers `cloud_access`.

> **Superseded before this document merged — step 4 did not survive review, and the reasoning above
> it is left standing deliberately so the decision is legible rather than silently rewritten.**
>
> The ordering above was the author's recommendation. It was put to the user as one of three
> options, alongside removing `cloud_user_role` immediately (A) and deprecating it now for later
> removal (C). **The user chose A — remove now — conditional on `cloud_access` not being close to
> finished.** That condition was then measured rather than estimated: `resource_cloud_access.go` is
> 441 lines of schema, `ValidateConfig` and a plan modifier; all four CRUD methods are single
> unconditional refusals; `ImportState` does not exist; and the file makes **zero** API calls. The
> condition does not trigger.
>
> So `cloud_user_role` is removed on its own, and a released version will exist with no cloud-role
> management surface until `cloud_access` gains a write path. That gap is accepted, not overlooked —
> it was the explicit cost of option A, weighed against a resource that had already shipped in two
> releases after the decision to remove it, still carrying no `DeprecationMessage`.
>
> Steps 1–3 are unaffected and still describe the right order for the work that remains.
> **Gap (iii) — no write surface for project roles — is unchanged by this and remains the largest
> hole in the surface.**
>
> The removal ships as a changelog note only; the user was asked directly and declined a migration
> guide. That makes the changelog entry the entire migration path rather than a summary of one,
> which is why the constraint below is written the way it is.

**Hard constraint on that removal, re-derived at `0e862fd`** with
`grep -rln '<name>' internal/provider/*.go`:

| helper | defined in | also used by |
|---|---|---|
| `resolveIdentityForEmail` | `resource_cloud_user_role.go` | `resource_organization_user_role.go` and its test |
| `stringListToSlice` | `resource_cloud_user_role.go` | `resource_organization_user_role.go` |

Deleting the file breaks the build. Relocate both helpers **and** delete the file in the **same
commit**: `.pre-commit-config.yaml` runs `golangci-lint` over the whole package, so any intermediate
state fails the hook and cannot be committed.

## Endorsed as-is

Recorded so a later pass does not "improve" something already correct:

- The `organization_user` / `organization_user_role` split and its membership-versus-role boundary.
- `organization_user` destroy never evicting a human, and saying so under a dedicated heading.
- `organization_user_role` destroy leaving `base_role` in place with a warning.
- `deny_roles` being plain `Optional` at cloud scope and `Optional+Computed` at organization scope.
  This reads as an inconsistency and is not: it is what keeps organization applies off the gated
  endpoint (see the two-write-paths note in the consolidation record).
- `organization_default_cloud`'s state-only destroy.
- `cloud_user_role`'s unrepairable-404 destroy diagnostic. If that resource is removed, this failure
  mode must be re-checked against `cloud_access`'s revoke path, which uses the same endpoint.
