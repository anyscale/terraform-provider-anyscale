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

   The real-infra tests for `anyscale_organization_user_role` and `anyscale_cloud_access` have their
   own opt-in gate — CI-enforced coverage for both comes from mocked `httptest`-based tests, so a
   green CI run does not exercise these paths either. Only set these locally, and only if you mean
   to:
   - `ANYSCALE_TEST_USER_EMAIL=<email>` runs them against that organization member. They change the
     member's real organization and cloud roles (the cloud_access tests also create and destroy their own
     clouds); destroy leaves the member in the organization. Point it only at a disposable, non-owner
     member that is not the token's own identity.
   - `ANYSCALE_TEST_ORG_NAME=<org name>` is required whenever `ANYSCALE_TEST_USER_EMAIL` is set: the
     tests fail unless the token authenticates against that organization.
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
`.changelog/README.md` for the exact shape. A deliberate schema break also needs the
`breaking-change` label; see [Schema breaking-change gate](#schema-breaking-change-gate) below.

**No user-facing effect?** Internal refactors, test-only fixes, CI changes, and examples-only
edits outside `examples/resources/`, `examples/data-sources/`, and `examples/provider/` don't need
a fragment — apply the `skip-changelog` label instead of adding a file. Those three directories
are the exception: they feed `tfplugindocs` and are pulled into registry-published doc pages (e.g.
`examples/resources/anyscale_cloud/resource.tf` becomes the Example Usage block on
`docs/resources/cloud.md`), so a change there is provider-facing even though it's "just an
example." If you're contributing from a fork and can't apply labels yourself, say so in the PR
description and a maintainer will apply it during review.

## Schema breaking-change gate

The `Schema Gate` check compares the provider schema at the PR's merge-base with the schema at the PR head and fails on changes that would break existing configurations or state. It runs on every PR, including docs-only ones, and writes its report to the job summary (Actions run page → **Summary**), not as a PR comment.

**Reading the summary.** The header counts the findings (`N breaking, N needs review, N non-breaking`), followed by one list per severity. Each line names the affected resource or attribute, what changed, and the rule that fired:

- **Breaking** fails the check. Examples: a resource, data source, or attribute removed; a new Required attribute; Optional changed to Required; a type or nesting change; an attribute that users could set becoming Computed-only; Optional+Computed narrowed to Optional; `Sensitive` removed.
- **Needs review** passes but asks a reviewer to confirm something. A resource `version` bump needs a state upgrader; a deprecation added, Computed added to an Optional attribute, and `Sensitive` added are listed too (adding `Sensitive` can fail plan for a root module output that references the attribute without `sensitive = true`).
- **Non-breaking** is collapsed. New resources, new Optional or Computed attributes, and description changes.

**Shipping an intentional break.** Apply the `breaking-change` label **and** add a `release-note:breaking-change` entry to `.changelog/<PR#>.txt` (see [Changelog fragments](#changelog-fragments)). The label alone still fails the check: a deliberate break must reach the changelog. The check re-runs when the label is added or removed. From a fork, ask a maintainer to apply the label.

**If the check fails with "could not produce a provider schema".** The provider failed to build, or `terraform providers schema` failed, at the merge-base or the PR head. This is not a breaking-change finding, and the label does not bypass it. Fix the build or the schema-time error shown in the job log.

**Run it locally.** `.github/scripts/schema-gate.sh gate origin/main HEAD` (needs git, go, jq, and Terraform 1.15 or later). Set `PR_NUMBER` and `PR_LABELS_JSON` to exercise the label and fragment checks.

**Limitations.** The schema JSON does not include plan modifiers, so a newly added `RequiresReplace` is invisible to this check. The check also does not compare `min_items`/`max_items` on nested attributes, or resource identity schemas. Reviewers should still look for all three.
