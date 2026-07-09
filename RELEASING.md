# Releasing

How a new version of `terraform-provider-anyscale` gets from merged PRs to a signed release on the
Terraform Registry. This is the canonical description of the release process; the Makefile,
`.github/workflows/release.yml`, and `.goreleaser.yml` all point here.

For **authoring** changelog entries (the per-PR part every contributor does), see
[CONTRIBUTING.md](CONTRIBUTING.md#changelog-fragments) and [`.changelog/README.md`](.changelog/README.md).
This document is for **cutting** a release.

---

## At a glance

```
per PR:      add .changelog/<PR#>.txt   ──▶  changelog-gate (required) + release-preflight
                                                       │ merge to main
release:  1. make changelog-release VERSION=x.y.z   →  opens a PR that folds fragments into CHANGELOG.md
          2. review + merge that changelog PR
          3. make tag VERSION=x.y.z               →  tags + pushes vX.Y.Z
                                                       │ tag push
automated:   release.yml → GoReleaser (build, GPG-sign, GitHub Release) → Registry webhook ingests
```

The changelog is a **byproduct of the fragments that already merged** — there is no separate
"remember to update the changelog" step. If a PR changed user-facing behavior, its fragment is
already in `.changelog/`; the release just consolidates them.

---

## Versioning

- [Semantic Versioning](https://semver.org/), tags are `vX.Y.Z` (the `v` prefix is required by the
  Registry).
- **Patch** (`x.y.Z`) — bug fixes, no new surface. **Minor** (`x.Y.0`) — new resources/data
  sources/attributes, backward compatible. **Major** (`X.0.0`) — breaking changes.
- Breaking-ness is declared explicitly by a `release-note:breaking-change` fragment (see
  `.changelog/README.md`), never inferred. A release containing one must be a major bump (pre-1.0,
  treat a breaking change as at least a minor bump and call it out prominently).
- Released versions are **immutable**: never move or re-cut a published tag (it breaks Registry
  checksums for anyone who already installed it). To fix a bad release, ship the next patch.

---

## Cutting a release

### 0. Preconditions
- You are on `main`, up to date, with a clean working tree.
- Everything you intend to ship has merged, and each user-facing PR left a `.changelog/<PR#>.txt`
  fragment (enforced by `changelog-gate` — see below).
- Optional local confidence check of the release machinery (CI also does this on every PR, see
  [Release preflight](#ci-safety-gates)):
  ```bash
  make release-check      # goreleaser check — validates .goreleaser.yml
  make release-snapshot   # full local build, no publish, no tag required
  ```

### 1. Finalize the changelog (via a reviewed PR)
```bash
make changelog-release VERSION=x.y.z
```
This folds every pending fragment into a dated `## [x.y.z]` section in `CHANGELOG.md`, deletes the
consumed fragments, commits to a `changelog/vx.y.z` branch, and opens a PR. It refuses to run unless
you are on `main` with a clean tree and a semver-shaped `VERSION`.

Review the PR like any other change and merge it. Because the entries were each reviewed as their
own fragment PR, this review is mostly a sanity check on grouping/wording. See
[Why the changelog goes through a PR](#why-the-changelog-goes-through-a-pr).

### 2. Tag the release
Once the changelog PR is merged into `main`:
```bash
git checkout main && git pull
make tag VERSION=x.y.z
```
`make tag` refuses unless you are on `main`, the tree is clean, **and `CHANGELOG.md` already has a
`## [x.y.z]` section** (i.e. step 1 was merged). It then creates an annotated `vx.y.z` tag and pushes
it. Pushing the tag is what triggers the automated release.

### 3. Verify
- Watch the **Release** workflow run to completion in the Actions tab.
- Confirm the [GitHub Release](https://github.com/anyscale/terraform-provider-anyscale/releases)
  exists, its **body matches the `CHANGELOG.md` section** for this version, and it has the signed
  assets (`..._SHA256SUMS`, `..._SHA256SUMS.sig`, the per-OS/arch zips, and `..._manifest.json`).
- Within a few minutes the version should appear on the
  [Terraform Registry](https://registry.terraform.io/providers/anyscale/anyscale/latest).

---

## What happens on tag push (automated)

`.github/workflows/release.yml` runs on `push` of a `v*.*.*` tag:

1. Imports the GPG signing key from the `GPG_PRIVATE_KEY` / `GPG_PASSPHRASE` repo secrets.
2. Re-extracts this version's section from the committed `CHANGELOG.md` into `release-notes.md`
   (written **outside** `dist/`, which `goreleaser release --clean` wipes), and **fails early if that
   section is missing or empty** — so a tag pushed without a finalized changelog never publishes.
3. Runs `goreleaser release --clean`, which (per `.goreleaser.yml`) builds the cross-platform matrix,
   zips them, produces `..._SHA256SUMS`, GPG-detach-signs the checksums, and uploads everything —
   plus `terraform-registry-manifest.json` (as `..._manifest.json`, advertising **protocol 6.0** for
   this Plugin Framework provider) — to a GitHub Release whose body is `release-notes.md`.
4. The Terraform Registry ingests the new release through its GitHub webhook and publishes it under
   the **`anyscale/anyscale`** namespace.

---

## CI safety gates

Two checks make it hard to reach a release in a broken state:

- **`changelog-gate`** (required on `main`) — fails a PR that has neither a `.changelog/<PR#>.txt`
  fragment nor the `skip-changelog` label. This is what keeps `CHANGELOG.md` complete without anyone
  hand-editing it. See [CONTRIBUTING.md](CONTRIBUTING.md#changelog-fragments).
- **`release-preflight`** — on every PR/`main` push, validates `.goreleaser.yml` (`goreleaser
  check`), builds a snapshot (no publish, no signing), asserts the Registry manifest is present with
  protocol 6.0, and checks that fragments parse. This surfaces a broken release config on a PR rather
  than after a tag is already public.

---

## Why the changelog goes through a PR

`main` requires a review + passing checks (branch protection). `enforce_admins` is intentionally
**off**, so an admin token *could* push straight to `main` — but the release process deliberately
does not rely on that bypass. Finalizing the changelog (`make changelog-release`) and cutting the tag
(`make tag`) are therefore two steps with a normal, reviewed PR in between, exactly like every other
change to `main`. This keeps the reviewed changelog PR as the audit record of what shipped.

---

## Prerequisites and secrets

- **Repo secrets** (already configured; needed by `release.yml`): `GPG_PRIVATE_KEY` (armor-exported
  private key), `GPG_PASSPHRASE`. `GITHUB_TOKEN` is provided automatically.
- **Registry signing key**: the *public* half of the GPG key is registered on the Terraform Registry
  under the `anyscale` namespace's signing keys. The key must be RSA or DSA (the Registry rejects the
  modern ECC/Ed25519 default).
- **Local tooling** (only if you run a release or dry-run locally rather than via CI):
  [`goreleaser`](https://goreleaser.com/), plus a GPG key and `GPG_FINGERPRINT` set for `make release`.

---

## Prereleases (release candidates)

Tag with a SemVer prerelease suffix, e.g. `v1.2.3-rc1`. `.goreleaser.yml` sets `prerelease: auto`, so
GoReleaser marks the GitHub Release as a prerelease automatically. Terraform never auto-selects a
prerelease unless a user pins it with an exact version constraint, so cutting an RC is safe. Finalize
its changelog section the same way (`make changelog-release VERSION=1.2.3-rc1`).

---

## If something goes wrong

- **The release workflow failed after the tag was pushed** (e.g. bad config that slipped past
  preflight, transient GoReleaser/GPG error): delete the tag, fix the problem on `main`, and re-tag.
  ```bash
  make tag-delete VERSION=x.y.z   # deletes the tag locally and on origin
  ```
  Only safe while the version is **not yet published to the Registry**. Deleting a tag also removes
  its (failed/draft) GitHub Release. If the Registry already ingested the version, treat it as
  immutable and ship the next patch instead.
- **Empty or wrong GitHub Release body**: the body is sliced from `CHANGELOG.md` at release time. Fix
  the `## [x.y.z]` section on `main` and re-cut only if the version was never published; otherwise
  correct it in the next release. (An empty body now fails the workflow before publishing.)
- **A local dry-run before tagging** to catch config problems without touching anything remote:
  ```bash
  make release-dry-run    # goreleaser release --skip=publish --clean
  ```
