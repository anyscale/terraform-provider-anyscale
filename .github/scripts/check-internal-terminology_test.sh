#!/usr/bin/env bash
# Functional test for check-internal-terminology.sh. Hermetic: runs against a
# throwaway git repo in a temp dir, never against this checkout, so planting
# violations cannot touch real files. Run directly:
#   bash .github/scripts/check-internal-terminology_test.sh
# Also runs in CI ahead of the gate itself.

set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
gate="$here/check-internal-terminology.sh"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/terminology-gate-test.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
repo="$tmp/repo"
mkdir -p "$repo/.github/scripts" "$repo/.changelog" "$repo/templates" "$repo/internal/provider"
git -C "$repo" init -q
git -C "$repo" config user.email test@example.invalid
git -C "$repo" config user.name test

cp "$gate" "$repo/.github/scripts/check-internal-terminology.sh"
cp "$here/../internal-terminology.txt" "$repo/.github/internal-terminology.txt"

pass=0
fail=0

expect_fail() { # $1 = description, rest = command
    local desc="$1"; shift
    local out
    if out=$("$@" 2>&1); then
        echo "FAIL (expected a hit): $desc"
        fail=$((fail + 1))
    else
        pass=$((pass + 1))
    fi
}

expect_pass() { # $1 = description, rest = command
    local desc="$1"; shift
    local out
    if out=$("$@" 2>&1); then
        pass=$((pass + 1))
    else
        echo "FAIL (expected clean): $desc"
        echo "$out" | sed 's/^/    /'
        fail=$((fail + 1))
    fi
}

expect_output() { # $1 = description, $2 = substring, $3+ = command
    local desc="$1" want="$2"; shift 2
    local out
    out=$("$@" 2>&1)
    if [[ "$out" == *"$want"* ]]; then
        pass=$((pass + 1))
    else
        echo "FAIL (expected output to contain '$want'): $desc"
        echo "$out" | sed 's/^/    /'
        fail=$((fail + 1))
    fi
}

stage() { git -C "$repo" add -A >/dev/null; }
# The gate resolves its tree via git from the current directory, so run it
# from inside the fixture repo, not from this checkout.
tree() { (cd "$repo" && bash ./.github/scripts/check-internal-terminology.sh); }
msgf() { bash "$repo/.github/scripts/check-internal-terminology.sh" --message-file "$1"; }
clean_repo() { # remove any planted file so cases stay independent
    find "$repo" -mindepth 1 \( -path "$repo/.git" -o -path "$repo/.github" \) -prune -o -type f -print | xargs rm -f
}

write() { printf '%s\n' "$2" > "$repo/$1"; }

# --- one violation class at a time ----------------------------------------

clean_repo
write internal/provider/x.go "// TODO: ask TFP-Forge to look at this again."
stage
expect_fail "R1: shard name in a Go comment" tree

clean_repo
write internal/provider/x.go "// the forge's control run found this."
stage
expect_fail "R3: bare role word in a Go comment" tree

clean_repo
write templates/foo.md "Per the architect's spec this stays."
stage
expect_fail "R3: role word in a templates/ markdown file" tree

clean_repo
write .changelog/999.txt "description: fixed per the scribe's review."
stage
expect_fail "R1: role word in a changelog fragment" tree

clean_repo
write docs_note.md "on branch crystl/tfp-assayer this breaks."
write README.md "ok"
stage
expect_fail "R2: crystl/ branch ref in a committed file" tree

clean_repo
write docs_note.md "Shipped in Phase 2B."
stage
expect_fail "R4: denylist term" tree

msg="$tmp/COMMIT_EDITMSG"
printf 'fix: per tfp-scribe feedback\n' > "$msg"
expect_fail "R1: shard name in a commit message" msgf "$msg"
printf 'fix: rename per the group\nforge forge\n' > "$msg"
expect_fail "R3: role word in a commit message" msgf "$msg"
printf 'chore: Phase 2B cleanup\n' > "$msg"
expect_fail "R4: denylist term in a commit message" msgf "$msg"
printf 'fix: real message\n' > "$msg"
expect_pass "clean commit message passes" msgf "$msg"
printf 'fix: on branch crystl/tfp-assayer\n' > "$msg"
expect_fail "R2: branch ref in a commit message" msgf "$msg"

# --- git commit-msg hook mode ---------------------------------------------
# Git runs the commit-msg hook on the UNCLEANED file: editor "# On branch"
# template lines (a worktree branch path trips R2) and `git commit -v`
# scissors+diff must be ignored, mirroring git's own cleanup. PR
# title/body files keep "#": those are markdown headings.

# Hook-mode runs execute with the fixture repo as the working directory so
# git resolves its own config (core.commentChar) from the fixture.
gmsgf() { (cd "$repo" && bash ./.github/scripts/check-internal-terminology.sh --git-commit-msg "$1"); }

gitmsg="$tmp/COMMIT_EDITMSG.dirty"
{
    printf 'fix: real change\n'
    printf '\n'
    printf '# On branch crystl/tfp-assayer\n'
    printf '# Changes to be committed:\n'
} > "$gitmsg"
expect_pass "hook mode: git editor template comment lines are ignored" gmsgf "$gitmsg"

{
    printf 'fix: real change\n'
    printf '\n'
    printf '# ------------------------ >8 ------------------------\n'
    printf '# diff --git a/CLAUDE.md b/CLAUDE.md\n'
    printf '# -The tfp-forge note reads: ask the scribe.\n'
} > "$gitmsg"
expect_pass "hook mode: scissors line and verbose diff below it are ignored" gmsgf "$gitmsg"

{
    printf 'fix: ask tfp-forge\n'
    printf '\n'
    printf '# On branch main\n'
} > "$gitmsg"
expect_fail "hook mode: a real violation before the comment lines still fails" gmsgf "$gitmsg"
expect_output "hook mode: failure names the ORIGINAL message file" "$gitmsg" gmsgf "$gitmsg"

printf '# On branch crystl/tfp-assayer\n' > "$msg"
expect_fail "PR-body mode keeps \"#\" lines: same comment trips R2 in --message-file" msgf "$msg"

# core.commentChar honoured: with commentChar=x, lines starting with x are
# comments (stripped in hook mode), and the default '#' is NOT a comment.
git -C "$repo" config core.commentChar x
{
    printf 'fix: real change\n'
    printf '\n'
    printf 'x On branch crystl/tfp-assayer\n'
} > "$gitmsg"
expect_pass "hook mode: core.commentChar=x strips x-prefixed template lines" gmsgf "$gitmsg"
git -C "$repo" config --unset core.commentChar

# --- negative controls ------------------------------------------------------

clean_repo
write internal/provider/static.go "// resolve tfp-test-aws-useast1-STATIC first."
stage
expect_pass "negative control: tfp-test-aws-useast1-STATIC is not a shard name" tree

clean_repo
write .gitignore ".crystl/"
write note.md "docs at https://crystl.dev/docs are fine."
stage
expect_pass "negative control: .crystl/ path and crystl.dev URL pass" tree

clean_repo
write internal/provider/x.go "// Describe the architecture; forged credentials and Forgejo repos stay clean."
stage
expect_pass "negative control: architecture/forged/Forgejo/describe pass" tree

clean_repo
write internal/provider/x.go "// For forged tokens, describe the architecture (see Forgejo)."
stage
expect_pass "negative control: mixed-case negatives pass" tree

# Role words at start/end of file, plural and possessive forms must match.
clean_repo
write x.md "Architects"
stage
expect_fail "R3: plural at start of file" tree
clean_repo
write x.md "the shipwright's rule"
stage
expect_fail "R3: possessive" tree

# --- summary ----------------------------------------------------------------

if [[ $fail -gt 0 ]]; then
    echo "terminology gate test: ${fail} FAILED, ${pass} passed"
    exit 1
fi
echo "terminology gate test: all ${pass} cases passed"
