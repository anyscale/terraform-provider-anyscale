#!/usr/bin/env bash
# check-internal-terminology.sh — keep internal agent/role terminology out of
# the repository. Internal process words (who found or authored a change)
# mean nothing to a reader of this repo and date badly; the worst case was a
# squash-merged release whose body carried 15 agent references to main.
#
# Modes:
#   check-internal-terminology.sh                            # tree: all tracked files
#   check-internal-terminology.sh --message-file <path>      # one commit/PR text
#   check-internal-terminology.sh --git-commit-msg <path>    # a git commit-msg hook target
#
# --git-commit-msg exists because git runs its commit-msg hook on the
# UNCLEANED message file: the editor template carries "# On branch ..."
# lines (a worktree branch path would trip R2 in every worktree) and
# `git commit -v` appends the scissors line plus the whole diff, which
# would trip R1/R3 the moment the diff touches allowlisted files like
# CLAUDE.md. The mode cuts at the scissors line and drops comment lines
# (git stripspace --strip-comments, honouring core.commentChar) - exactly
# the cleanup git itself does right after the hook. --message-file, used
# for PR titles/bodies, must NOT strip "#": those are markdown headings.
#
# Rules (index used in output):
#   R1  shard names: tfp-(architect|forge|assayer|scribe|shipwright), -i.
#       NEVER a bare "tfp-" pattern - the static test fixture
#       tfp-test-aws-useast1-STATIC feeds the acctest resolver, the sweeper
#       org guard and CI, and must keep passing.
#   R2  branch refs: crystl/ not preceded by "." (.gitignore's .crystl/ passes).
#   R3  bare role words, tree-wide (not just Go comments): word-bounded
#       architect/forge/assayer/scribe/shipwright, -i. Plural and possessive
#       forms are caught by the non-letter boundary on each side. Comment
#       detection by regex is fragile and the founding incident was a
#       changelog fragment, so this applies everywhere. There is no inline
#       suppression marker - escape hatches become the leak path; the fix
#       is to rephrase the text.
#   R4  denylist (.github/internal-terminology.txt), fixed-string, -i. That
#       file holds programme-internal terms only.
#
# Tree mode skips exactly: CLAUDE.md, AGENTS.md, this script, its test and
# the denylist itself (they quote the patterns). No directory-level
# allowlists - widen the allowlist only with a demonstrated false positive.
#
# Output: one line per hit, "path:line: R<n>: <offending line>"; exit 1 on
# any hit. Existing main history is out of scope (forward-only).

set -uo pipefail

usage() {
    sed -n '2,12p' "$0" >&2
    exit 2
}

# --- mode -----------------------------------------------------------------
message_file=""
reported_message=""
git_commit_msg=0
if [[ "${1:-}" == "--message-file" ]]; then
    [[ $# -ge 2 ]] || usage
    message_file="$2"
    shift 2
elif [[ "${1:-}" == "--git-commit-msg" ]]; then
    [[ $# -ge 2 ]] || usage
    message_file="$2"
    git_commit_msg=1
    shift 2
fi
[[ $# -eq 0 ]] || usage

# --- denylist -------------------------------------------------------------
# Resolved relative to this script's location, not the repository root: CI
# runs the script from the PR's merge-base (a PR must not edit the logic that
# judges it), next to that revision's denylist. Fail closed if missing.
denylist="$(cd "$(dirname "$0")/.." && pwd)/internal-terminology.txt"
if [[ ! -r "$denylist" ]]; then
    echo "check-internal-terminology: denylist not readable: $denylist (fail closed)" >&2
    exit 2
fi
repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
denylist_terms=()
while IFS= read -r line; do
    line="${line%"${line##*[![:space:]]}"}"    # rstrip
    [[ -z "$line" || "$line" == \#* ]] && continue
    denylist_terms+=("$line")
done < "$denylist"

# --- rules ----------------------------------------------------------------
# Alternation of role words with plural/possessive tail. Word boundaries are
# approximated with an explicit non-letter (or string edge) on both sides so
# the pattern works on both BSD and GNU grep; this keeps "architecture",
# "describe", "forged", "forgiveness" and "Forgejo" clean while catching
# "architects", "architect's" and heading/title uses.
role_words='(architect|forge|assayer|scribe|shipwright)(s)?'
role_pattern="(^|[^a-zA-Z])${role_words}(\$|[^a-zA-Z])"

# -I on every rule: git ls-files hands us tracked binaries too, and grep's
# "binary file matches" line would be mangled into a garbled locator by the
# consumers below; binary content is out of scope for a prose guard.
r1() { grep -nIEi 'tfp-(architect|forge|assayer|scribe|shipwright)' "$1"; }
r2() { grep -nIE '(^|[^.])crystl/' "$1"; }
r3() { grep -nIEi "$role_pattern" "$1"; }
r4() {
    local file="$1" out=""
    local term
    for term in "${denylist_terms[@]}"; do
        out=$(grep -niIF -- "$term" "$file") || continue
        printf '%s\n' "$out"
    done
    return 0
}

check_file() {
    # $1 = path as reported, $2 = path to read. Hit lines go to stdout with
    # the "path:line: R<n>: content" locator; counters are globals. grep
    # output is "lineno:content"; one strip of the leading "lineno:" leaves
    # even colon-heavy URLs intact.
    local reported="$1" file="$2" hits=0
    local tag text line_no content
    for tag in R1 R2 R3; do
        case "$tag" in
            R1) text=$(r1 "$file") ;;
            R2) text=$(r2 "$file") ;;
            R3) text=$(r3 "$file") ;;
        esac
        while IFS= read -r hit; do
            [[ -z "$hit" ]] && continue
            line_no="${hit%%:*}"
            content="${hit#*:}"
            printf '%s\n' "${reported}:${line_no}: ${tag}:${content}"
            hits=1
            hit_count=$((hit_count + 1))
        done <<<"$text"
    done
    text=$(r4 "$file")
    while IFS= read -r hit; do
        [[ -z "$hit" ]] && continue
        line_no="${hit%%:*}"
        content="${hit#*:}"
        printf '%s\n' "${reported}:${line_no}: R4:${content}"
        hits=1
        hit_count=$((hit_count + 1))
    done <<<"$text"
    [[ $hits -eq 1 ]] && file_hit_count=$((file_hit_count + 1))
    [[ $hits -eq 1 ]] && return 1
    return 0
}

failures=0
hit_count=0
file_hit_count=0

if [[ -n "$message_file" ]]; then
    if [[ ! -r "$message_file" ]]; then
        echo "check-internal-terminology: message file not readable: $message_file" >&2
        exit 2
    fi
    reported_message="$message_file"
    if [[ $git_commit_msg -eq 1 ]]; then
        # Mirror git's own post-hook cleanup: cut at the scissors line
        # (`git commit -v` appends the diff below it), then drop comment
        # lines. The scissors line is the constant git prints (only the
        # leading comment char varies).
        comment_char=$(git config --get core.commentChar || printf '#')
        cleaned=$(mktemp "${TMPDIR:-/tmp}/terminology-msg.XXXXXX")
        awk -v prefix="${comment_char} ------------------------ >8 ------------------------" \
            'index($0, prefix) == 1 { exit } { print }' "$message_file" \
            | git stripspace --strip-comments > "$cleaned"
        message_file="$cleaned"
    fi
    check_file "${reported_message}" "$message_file" || failures=$((failures + 1))
else
    # Tree mode: every tracked file except the explicit allowlist.
    allowlist=(
        "CLAUDE.md"
        "AGENTS.md"
        ".github/scripts/check-internal-terminology.sh"
        ".github/scripts/check-internal-terminology_test.sh"
        ".github/internal-terminology.txt"
    )
    is_allowlisted() {
        local candidate="$1" allowed
        for allowed in "${allowlist[@]}"; do
            [[ "$candidate" == "$allowed" ]] && return 0
        done
        return 1
    }
    while IFS= read -r file; do
        [[ -z "$file" ]] && continue
        is_allowlisted "$file" && continue
        # Skip binaries and other files grep would call text but that no
        # human writes by hand; only regular text files carry prose.
        [[ -f "$repo_root/$file" ]] || continue
        check_file "$file" "$repo_root/$file" || failures=$((failures + 1))
    done < <(git ls-files)
fi

if [[ $failures -gt 0 ]]; then
    if [[ -n "$reported_message" ]]; then
        echo "check-internal-terminology: internal terminology found in $reported_message (${hit_count} hits)" >&2
    else
        echo "check-internal-terminology: internal terminology found in ${file_hit_count} tracked files (${hit_count} hits)" >&2
    fi
    exit 1
fi
exit 0
