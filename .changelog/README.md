# Changelog fragments

Every pull request that changes user-facing behavior adds one file here describing that
change in plain English. A release-time tool folds these fragments into `CHANGELOG.md` and
deletes them — so the changelog is always current without anyone having to hand-edit it after
the fact. See the root [`CONTRIBUTING.md`](../CONTRIBUTING.md) for the full contributor workflow;
this file is the format reference.

> [!IMPORTANT]
> In every example below, the ` ``` ` lines are **literal characters your fragment file must
> contain** — they aren't markdown decoration around the example, they're part of the syntax the
> parser scans for. If you copy only the two lines *between* them, your fragment fails with
> "no release-note fragments found" and CI will reject the PR. Copy the whole fenced block,
> backticks included.

If your PR has no user-facing effect (internal refactor, test fix, CI change), skip the fragment
and apply the `skip-changelog` label instead. If you can't apply labels yourself (e.g. you're
contributing from a fork), ask a maintainer to apply it during review.

## Filename

```
.changelog/<PR_NUMBER>.txt
```

Use the number of the pull request the change ships in — not an issue number. This makes
filenames collision-proof (PR numbers are unique) and self-documenting (anyone reading a fragment
can trace it straight back to the PR that introduced it).

Because you won't know your PR number until after you open the PR, add the fragment in a
follow-up commit on the same branch once GitHub has assigned the number.

## Contents

One or more blocks, each shaped exactly like this — **including the backtick fence lines**:

`````
```
release-note:<type>
<resource/NAME | data-source/NAME | provider>: <one sentence, present tense, user-facing>
```
`````

A single PR can carry multiple blocks in its one file if the change spans more than one type or
component — for example, a PR that adds a resource and fixes an unrelated bug drops two blocks in
the same `.changelog/<PR#>.txt`. Nothing else may appear in the file outside a fenced block.

## Types

Types render into `CHANGELOG.md` in this fixed order:

| type              | section            | when to use it                                                        |
|-------------------|--------------------|------------------------------------------------------------------------|
| `breaking-change` | Breaking Changes   | Forces a config change, a plan diff, or a resource replacement.        |
| `new-resource`    | New Resources      | A new `resource` block.                                                |
| `new-data-source` | New Data Sources   | A new `data` block.                                                    |
| `added`           | Added              | A new attribute or capability on something that already exists.       |
| `changed`         | Changed            | Existing behavior changed, non-breaking.                               |
| `deprecated`      | Deprecated         | Still works today, but will be removed later.                          |
| `removed`         | Removed            | Something is gone — and removing it has zero user-visible effect.      |
| `fixed`           | Fixed              | A bug fix.                                                              |
| `security`        | Security           | A security-relevant fix or hardening change.                           |

A few boundaries that trip people up:

- **New resource/data source vs. `added`.** A brand new `anyscale_thing` resource is
  `new-resource`, never `added` — `added` is for growing something that already exists (a new
  attribute on an existing resource, for example), not for the resource itself.
- **`breaking-change` vs. `removed`.** Breaking-ness is always explicit, never inferred from the
  type. If removing something forces anyone to touch their config, re-run `terraform plan` and see
  a diff, or triggers a replace — that's `breaking-change` with migration text, not `removed`.
  Reserve `removed` for the rare case where deleting something changes nothing a user could
  observe (dead internal code, an already-inert field).

## Breaking changes

`breaking-change` is the only mechanism for flagging a break — there's no separate label or side
channel. The fragment body must state **both** what breaks and how to fix it, in one sentence:

`````
```
release-note:breaking-change
resource/anyscale_cloud: `aws_config.anyscale_iam_role_id` is renamed to `controlplane_iam_role_arn`; to migrate, update the attribute name in your configuration and re-run `terraform plan`.
```
`````

Deprecations aren't breaking (nothing stops working yet), but call out what replaces the
deprecated thing and, if known, when it goes away:

`````
```
release-note:deprecated
resource/anyscale_cloud: `is_empty_cloud` is deprecated in favor of checking whether `cloud_deployment_id` is null; it will be removed in a future major version.
```
`````

## Examples by type

**New resource**
`````
```
release-note:new-resource
resource/anyscale_service: Manage Anyscale Services.
```
`````

**New data source**
`````
```
release-note:new-data-source
data-source/anyscale_project: Look up an existing Anyscale project by name or ID.
```
`````

**Added**
`````
```
release-note:added
resource/anyscale_cloud: Add support for GCP Filestore in the `file_storage` block.
```
`````

**Changed**
`````
```
release-note:changed
resource/anyscale_compute_config: `flags` and `advanced_configurations_json` now accept native HCL instead of requiring `jsonencode()`.
```
`````

**Removed** (non-breaking only — see above)
`````
```
release-note:removed
provider: Remove an unused internal request field from compute-config lookups; no user-visible behavior changes.
```
`````

**Fixed**
`````
```
release-note:fixed
resource/anyscale_compute_config: Fix data source lookups by `name` returning a 422 error.
```
`````

**Security**
`````
```
release-note:security
provider: Mark credential and token fields as sensitive in state and plan output.
```
`````
