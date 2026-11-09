#!/usr/bin/env bash
# Release gate: refuse to publish a tag unless its tree is covered by
# acceptance tests that actually ran and passed.
#
# Usage: release-acctest-gate.sh <tag-sha> <previous-release-ref>
#        release-acctest-gate.sh --self-test
# Env:   GH_TOKEN (actions: read), GITHUB_REPOSITORY (owner/name)
#
# Why a walk-back and not "CI passed on the tag commit": the tag commit is the
# docs-only changelog commit, so CI skips the real-infra acceptance steps on
# it while the job still reports success; and main's cancel-in-progress can
# cancel its run outright. Neither says anything about whether tests ran.
#
# Rule: starting at the tag commit, walk first-parent history back to (not
# including) the previous release. The newest commit C in that range whose
# `ci (acctest-data)` job ran the "Acceptance Tests - Data Sources" step to
# success AND whose `ci (acctest-resource)` job ran "Acceptance Tests -
# Resources" to success (step conclusions, not job conclusions: a skipped step
# makes its job succeed) is the tested baseline. Every file changed between C
# and the tag commit must be docs-only per .github/docs-only-filter.yml.
# No baseline in range, an API error, or a non-docs change after C => exit 1.
# There is no override and no path that falls through to success.
set -euo pipefail

FILTER_FILE="${GATE_FILTER_FILE:-.github/docs-only-filter.yml}"
WORKFLOW_FILE="ci.yml"
DATA_JOB="ci (acctest-data)"
DATA_STEP="Acceptance Tests - Data Sources"
RES_JOB="ci (acctest-resource)"
RES_STEP="Acceptance Tests - Resources"

# Echo the filter's '!' exclusions as bash case-patterns. micromatch '**' and
# bash '*' both cross '/', so '**' -> '*'; a leading '**/' also matches a
# root-level file, which '*' (zero chars allowed) already covers.
exclusion_patterns() {
  local line pat
  while IFS= read -r line; do
    case "$line" in
      "  - '!"*"'")
        pat="${line#  - \'!}"; pat="${pat%\'}"
        pat="${pat//\*\*\//\*}"   # '**/x' -> '*x'
        pat="${pat//\*\*/\*}"
        printf '%s\n' "$pat"
        ;;
    esac
  done <"$FILTER_FILE"
}

# 0 if the file is docs-only (matches some exclusion), 1 otherwise.
is_docs_only_path() {
  local f="$1" pat
  while IFS= read -r pat; do
    # shellcheck disable=SC2254 # pattern is intentionally a glob
    case "$f" in $pat) return 0 ;; esac
  done < <(exclusion_patterns)
  return 1
}

self_test() {
  local n
  n="$(exclusion_patterns | wc -l | tr -d ' ')"
  [ "$n" -ge 1 ] || { echo "self-test: parsed 0 exclusions from $FILTER_FILE" >&2; exit 1; }
  # Every '!' line must have been understood.
  [ "$n" -eq "$(grep -c "^  - '!" "$FILTER_FILE")" ] || { echo "self-test: unparsed '!' lines" >&2; exit 1; }
  local f
  for f in docs/resources/cloud.md examples/resources/x/resource.tf README.md CHANGELOG.md \
           guides/a.md .changelog/441.txt templates/index.md.tmpl; do
    is_docs_only_path "$f" || { echo "self-test: '$f' should be docs-only" >&2; exit 1; }
  done
  for f in internal/provider/provider.go main.go go.mod Makefile .github/workflows/ci.yml \
           .github/scripts/release-acctest-gate.sh .golangci.yml tools/changelog-build/main.go; do
    is_docs_only_path "$f" && { echo "self-test: '$f' must NOT be docs-only" >&2; exit 1; }
  done
  echo "self-test ok ($n exclusions)"
}

if [ "${1:-}" = "--self-test" ]; then self_test; exit 0; fi

TAG_SHA="${1:?tag sha}"; PREV="${2:?previous release ref}"
REPO="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY}"
self_test >/dev/null

fail() { echo "::error::release gate: $*" >&2; exit 1; }

# Prints "ok" if some push run of ci.yml on $1 ran both acceptance steps green.
covered() {
  local sha="$1" runs id jobs
  runs="$(gh api "repos/$REPO/actions/workflows/$WORKFLOW_FILE/runs?head_sha=$sha&event=push&per_page=100" --jq '.workflow_runs[].id')" \
    || fail "cannot list CI runs for $sha"
  for id in $runs; do
    jobs="$(gh api "repos/$REPO/actions/runs/$id/jobs?filter=latest&per_page=100")" \
      || fail "cannot read jobs of run $id"
    if jq -e --arg dj "$DATA_JOB" --arg ds "$DATA_STEP" --arg rj "$RES_JOB" --arg rs "$RES_STEP" '
        def ok($j; $s): any(.jobs[]; .name == $j and any(.steps[]; .name == $s and .conclusion == "success"));
        ok($dj; $ds) and ok($rj; $rs)' <<<"$jobs" >/dev/null; then
      echo "ok run=$id"; return 0
    fi
  done
  return 1
}

git rev-parse --verify -q "$PREV^{commit}" >/dev/null || fail "previous release ref '$PREV' not found"
git merge-base --is-ancestor "$PREV" "$TAG_SHA" || fail "'$PREV' is not an ancestor of $TAG_SHA"

echo "Walking first-parent history ${PREV}..${TAG_SHA}"
checked=()
baseline=""; run_note=""
while read -r c; do
  if note="$(covered "$c")"; then baseline="$c"; run_note="$note"; break; fi
  checked+=("$c")
  echo "  $(git log -1 --format='%h %s' "$c"): no run with both acceptance steps green"
done < <(git rev-list --first-parent "${PREV}..${TAG_SHA}")

[ -n "$baseline" ] || fail "no commit in ${PREV}..${TAG_SHA} has a CI run whose acceptance steps (Data Sources and Resources) both ran green. Checked ${#checked[@]} commit(s): ${checked[*]:-none}. Re-run CI on the newest code-changing commit (gh run rerun), wait for it to complete green, then re-run the Release workflow against the EXISTING tag; do not delete or re-create the tag."

echo "Tested baseline: $(git log -1 --format='%h %s' "$baseline") ($run_note)"
bad=()
while IFS= read -r f; do
  [ -n "$f" ] || continue
  is_docs_only_path "$f" || bad+=("$f")
done < <(git diff --name-only "$baseline" "$TAG_SHA")

if [ "${#bad[@]}" -gt 0 ]; then
  printf '  changed after baseline, not docs-only: %s\n' "${bad[@]}"
  fail "non-docs files changed between tested baseline ${baseline:0:7} and ${TAG_SHA:0:7}; no acceptance run covers them."
fi
echo "Gate passed: ${TAG_SHA:0:7} differs from tested ${baseline:0:7} only in docs-only paths."
