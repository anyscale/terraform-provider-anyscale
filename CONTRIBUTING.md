# Contributing

Thanks for contributing to the Anyscale Terraform provider. This document covers the PR
workflow; for building, testing, and project layout see the [README](README.md#development) and
[CLAUDE.md](CLAUDE.md).

## Before opening a PR

1. Follow [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework) conventions.
2. Add unit tests for new helper functions and acceptance tests for new/changed resources and data sources.
   Note: acceptance tests that create a Cloud with embedded `aws_config`/`gcp_config`/`kubernetes_config`
   are gated behind `ANYSCALE_TEST_REAL_INFRA=1` (see `internal/acctest/helpers.go`) and are **not**
   run in CI — a green CI run doesn't exercise these paths. Verify them yourself locally with
   `ANYSCALE_TEST_REAL_INFRA=1 make testacc` before relying on them as evidence, and prefer a mocked
   unit test for anything you need CI to enforce on every PR.

   The real-infra lifecycle tests for `anyscale_organization_invitation` and
   `anyscale_organization_collaborator` have their own separate opt-in gates (unrelated to
   `ANYSCALE_TEST_REAL_INFRA` above) — CI-enforced coverage for both resources comes from mocked
   `httptest`-based tests instead (see `TestAccProjectResource_WriteCollaboratorSymmetry` in
   `internal/acctest/resource_project_lifecycle_acc_test.go` for the established pattern), so a
   green CI run does not exercise the paths below either. Only set these locally, and only if you
   mean to:
   - `ANYSCALE_TEST_INVITATIONS=1` runs the invitation resource's real create/read/delete lifecycle.
     It sends a real email invitation every run.
   - `ANYSCALE_TEST_USER_IDENTITY_ID=<identity_id>` runs the collaborator resource's import/update
     lifecycle against that identity.
   - `ANYSCALE_TEST_USER_IDENTITY_ID_DELETABLE=<identity_id>` runs the collaborator resource's delete
     lifecycle against that identity.
   - **Both identity variables genuinely remove that identity from the organization at test teardown —
     pass or fail, every run, no undo.** Point them only at a disposable identity created for this
     purpose. A real shared test-org identity was deprovisioned this way once already during
     development; see the warning in `warnDestructiveCollaboratorTest` in the same test file before
     setting either variable.
3. Run `make docs` if you changed a schema (description, attribute, resource/data source) — docs are generated, don't hand-edit files under `docs/`.
4. Run `make fmt lint test` before pushing.
5. Run `pre-commit install` once, so formatting hooks run automatically on commit.
6. If your PR only touches `docs/`, `examples/`, `templates/`, `.changelog/`, or a `*.md` file, CI's
   `ci (acctest-data)`/`ci (acctest-resource)` checks skip the real-infra test run and report success
   immediately instead (see `.github/workflows/ci.yml`'s "Detect docs-only diff" step) — this is
   expected, not a stuck check. Real acceptance-test coverage for these paths still runs daily via
   `.github/workflows/scheduled-acctest.yml`, so a regression introduced by a docs-only PR (which
   shouldn't be possible, but) doesn't go unexercised indefinitely.

## Changelog fragments

Every PR that changes user-facing behavior needs a changelog fragment: a small file at
`.changelog/<PR_NUMBER>.txt` containing one plain-English, user-facing sentence describing the
change. CI checks for this file and fails the PR if it's missing.

This exists so `CHANGELOG.md` stays accurate automatically instead of depending on someone
remembering to hand-edit it after merge (which is exactly how it drifted before this convention
existed). A release-time tool consolidates all pending fragments into `CHANGELOG.md` and deletes
them — so the changelog is always a byproduct of the PRs that already merged, not a separate task.

**You won't know your PR number until the PR exists.** Open the PR first, then push a follow-up
commit that adds `.changelog/<that number>.txt`. Note that the ` ``` ` lines below are literal
required file content, not just this doc's formatting — the heredoc writes them into the file
on purpose:

`````bash
# after your PR is open and you know its number, e.g. 142:
cat > .changelog/142.txt <<'EOF'
```
release-note:fixed
resource/anyscale_cloud: Fix a plan diff on `region` when the field is left unset.
```
EOF
git add .changelog/142.txt
git commit -m "docs: add changelog fragment"
git push
`````

See [`.changelog/README.md`](.changelog/README.md) for the full block syntax, the complete list
of valid types (`breaking-change`, `new-resource`, `new-data-source`, `new-ephemeral-resource`,
`new-action`, `added`, `changed`, `deprecated`, `removed`, `fixed`, `security`), and one worked
example per type.

**Shipping a breaking change?** Its fragment must use `release-note:breaking-change` and state
both what breaks and how to migrate, in one sentence — see the breaking-change section of
`.changelog/README.md` for the exact shape. This is the only mechanism for flagging a break; there
is no separate breaking-change label.

**No user-facing effect?** Internal refactors, test-only fixes, CI changes, and examples-only
edits outside `examples/resources/`, `examples/data-sources/`, and `examples/provider/` don't need
a fragment — apply the `skip-changelog` label instead of adding a file. Those three directories
are the exception: they feed `tfplugindocs` and are pulled into registry-published doc pages (e.g.
`examples/resources/anyscale_cloud/resource.tf` becomes the Example Usage block on
`docs/resources/cloud.md`), so a change there is provider-facing even though it's "just an
example." If you're contributing from a fork and can't apply labels yourself, say so in the PR
description and a maintainer will apply it during review.
