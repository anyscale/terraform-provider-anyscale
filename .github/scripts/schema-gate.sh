#!/usr/bin/env bash
# Schema breaking-change gate. Builds the provider at two git refs, dumps each
# schema with `terraform providers schema -json`, and classifies the
# difference with tools/schema-diff. Backs .github/workflows/schema-gate.yml;
# also runs locally.
#
# Usage:
#   schema-gate.sh dump <ref> <out.json>     build <ref> and write its schema JSON
#   schema-gate.sh gate <base-ref> <head-ref>
#       Compares the merge-base of the two refs against <head-ref>.
#
# gate inputs (environment; all optional locally):
#   PR_NUMBER        the PR number; locates .changelog/<PR_NUMBER>.txt in <head-ref>
#   PR_LABELS_JSON   JSON array of the PR's label names, e.g. '["breaking-change"]'
#   GITHUB_STEP_SUMMARY  markdown report destination (CI sets it)
#
# Exit codes: 0 pass; 1 breaking changes not acknowledged; 2 build, schema
# dump, or schema-diff input failure (never bypassed by the label).
#
# Requires git, go, jq, and Terraform >= 1.15 (dev_overrides without init).
set -euo pipefail

PROVIDER_ADDR="registry.terraform.io/anyscale/anyscale"
BYPASS_LABEL="breaking-change"
REPO_ROOT=$(git rev-parse --show-toplevel)
WORK=$(mktemp -d)
WORKTREES=()

cleanup() {
  for wt in "${WORKTREES[@]+"${WORKTREES[@]}"}"; do
    git -C "$REPO_ROOT" worktree remove --force "$wt" >/dev/null 2>&1 || true
  done
  rm -rf "$WORK"
}
trap cleanup EXIT

summary() {
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf '%s\n' "$@" >>"$GITHUB_STEP_SUMMARY"
  fi
}

# dump_failure prints the one message every build/dump failure shares, so a
# provider that cannot produce a schema can never read as an intentional break.
dump_failure() {
  local what="$1"
  echo "::error::schema-gate: could not produce a provider schema at ${what}. This is not a breaking-change finding, and the ${BYPASS_LABEL} label does not bypass it."
  summary "## Schema gate: failed" "" "Could not produce a provider schema at ${what}. The \`${BYPASS_LABEL}\` label does not bypass this failure; see the job log."
  exit 2
}

# dump <label> <ref> <out.json>
dump() {
  local label="$1" ref="$2" out="$3"
  local sha src bin cfg
  sha=$(git -C "$REPO_ROOT" rev-parse --verify "${ref}^{commit}") || dump_failure "${label} (${ref}: unknown ref)"
  src="$WORK/${label}-src"
  bin="$WORK/${label}-bin"
  cfg="$WORK/${label}-tf"
  mkdir -p "$bin" "$cfg"

  git -C "$REPO_ROOT" worktree add --detach --quiet "$src" "$sha" || dump_failure "${label} (${sha}: worktree)"
  WORKTREES+=("$src")

  echo "schema-gate: building provider at ${label} (${sha})"
  (cd "$src" && go build -o "$bin/terraform-provider-anyscale" .) || dump_failure "${label} (${sha}: go build failed)"

  cat >"$cfg/dev.tfrc" <<EOF
provider_installation {
  dev_overrides {
    "${PROVIDER_ADDR}" = "${bin}"
  }
  direct {}
}
EOF
  cat >"$cfg/main.tf" <<EOF
terraform {
  required_providers {
    anyscale = {
      source = "${PROVIDER_ADDR}"
    }
  }
}
EOF

  echo "schema-gate: dumping schema at ${label} (${sha})"
  if ! TF_CLI_CONFIG_FILE="$cfg/dev.tfrc" TF_IN_AUTOMATION=1 CHECKPOINT_DISABLE=1 \
    terraform -chdir="$cfg" providers schema -json >"$out" 2>"$cfg/stderr.log"; then
    cat "$cfg/stderr.log" >&2
    dump_failure "${label} (${sha}: terraform providers schema failed)"
  fi
  if ! jq -e --arg p "$PROVIDER_ADDR" '.provider_schemas[$p] != null' "$out" >/dev/null; then
    cat "$cfg/stderr.log" >&2
    dump_failure "${label} (${sha}: schema JSON has no ${PROVIDER_ADDR} entry)"
  fi
}

# has_breaking_fragment <file>: true when the fragment holds a
# release-note:breaking-change entry in either fence form changelog-build
# accepts: "```release-note:breaking-change" on one line, or a bare "```"
# followed by a "release-note:breaking-change" line.
has_breaking_fragment() {
  awk '
    function trim(s) { gsub(/^[ \t\r]+|[ \t\r]+$/, "", s); return s }
    {
      line = trim($0)
      if (substr(line, 1, 3) == "```") {
        if (in_fence) { in_fence = 0; expect = 0; next }
        in_fence = 1
        info = trim(substr(line, 4))
        if (info == "") { expect = 1 } else if (tolower(info) == "release-note:breaking-change") { found = 1 }
        next
      }
      if (expect && line != "") {
        expect = 0
        if (tolower(line) == "release-note:breaking-change") { found = 1 }
      }
    }
    END { exit found ? 0 : 1 }
  ' "$1"
}

gate() {
  local base_ref="$1" head_ref="$2"
  local head_sha merge_base
  head_sha=$(git -C "$REPO_ROOT" rev-parse --verify "${head_ref}^{commit}") || dump_failure "head (${head_ref}: unknown ref)"
  merge_base=$(git -C "$REPO_ROOT" merge-base "$base_ref" "$head_sha") || dump_failure "merge-base (no merge-base between ${base_ref} and ${head_ref})"
  echo "schema-gate: base ${merge_base} (merge-base), head ${head_sha}"

  dump base "$merge_base" "$WORK/base.json"
  dump head "$head_sha" "$WORK/head.json"

  (cd "$REPO_ROOT" && go build -o "$WORK/schema-diff" ./tools/schema-diff) || dump_failure "the schema-diff tool (go build failed)"

  local rc=0
  "$WORK/schema-diff" -base "$WORK/base.json" -head "$WORK/head.json" \
    -provider "$PROVIDER_ADDR" ${GITHUB_STEP_SUMMARY:+-summary "$GITHUB_STEP_SUMMARY"} || rc=$?

  case "$rc" in
  0)
    echo "schema-gate: no breaking schema changes."
    summary "" "**Schema gate: passed.** No breaking schema changes."
    return 0
    ;;
  1) ;;
  *) dump_failure "base or head (schema-diff could not read the dumps, exit ${rc})" ;;
  esac

  local has_label=false fragment="$WORK/fragment.txt"
  if [ -n "${PR_LABELS_JSON:-}" ] &&
    jq -e --arg l "$BYPASS_LABEL" 'index($l) != null' <<<"$PR_LABELS_JSON" >/dev/null; then
    has_label=true
  fi

  if [ "$has_label" != true ]; then
    echo "::error::schema-gate: breaking schema changes detected (listed above). If the break is intentional, apply the '${BYPASS_LABEL}' label and add a release-note:breaking-change entry to .changelog/${PR_NUMBER:-<PR#>}.txt."
    summary "" "**Schema gate: failed.** Breaking schema changes detected. If intentional, apply the \`${BYPASS_LABEL}\` label and add a \`release-note:breaking-change\` entry to \`.changelog/${PR_NUMBER:-<PR#>}.txt\`."
    return 1
  fi

  if [ -z "${PR_NUMBER:-}" ] ||
    ! git -C "$REPO_ROOT" show "${head_sha}:.changelog/${PR_NUMBER}.txt" >"$fragment" 2>/dev/null ||
    ! has_breaking_fragment "$fragment"; then
    echo "::error::schema-gate: the '${BYPASS_LABEL}' label is present, but .changelog/${PR_NUMBER:-<PR#>}.txt at head has no release-note:breaking-change entry. A deliberate break must reach the changelog."
    summary "" "**Schema gate: failed.** The \`${BYPASS_LABEL}\` label is present, but \`.changelog/${PR_NUMBER:-<PR#>}.txt\` has no \`release-note:breaking-change\` entry."
    return 1
  fi

  echo "schema-gate: breaking schema changes acknowledged by the '${BYPASS_LABEL}' label and a release-note:breaking-change fragment."
  summary "" "**Schema gate: passed (acknowledged).** Breaking changes are covered by the \`${BYPASS_LABEL}\` label and a \`release-note:breaking-change\` entry in \`.changelog/${PR_NUMBER}.txt\`."
  return 0
}

case "${1:-}" in
dump)
  [ $# -eq 3 ] || { echo "usage: $0 dump <ref> <out.json>" >&2; exit 2; }
  out=$(cd "$(dirname "$3")" && pwd)/$(basename "$3")
  dump ref "$2" "$out"
  echo "schema-gate: wrote ${out}"
  ;;
gate)
  [ $# -eq 3 ] || { echo "usage: $0 gate <base-ref> <head-ref>" >&2; exit 2; }
  gate "$2" "$3"
  ;;
*)
  echo "usage: $0 dump <ref> <out.json> | gate <base-ref> <head-ref>" >&2
  exit 2
  ;;
esac
