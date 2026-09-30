#!/usr/bin/env bash
# Summarize acceptance-test SKIPs into the GitHub Actions step summary so a
# green run can't be silently mistaken for full coverage.
#
# internal/acctest/helpers.go's SkipIfNoRealInfra gates every acceptance test
# that creates real cloud infra (fake credentials can't pass real
# provisioning), and CI never sets ANYSCALE_TEST_REAL_INFRA=1 - by design, not
# a bug. Raw `go test -v` output already prints each individual `--- SKIP:`
# line, but that's easy to miss scrolling a long green log. This makes the
# count impossible to miss.
#
# It also fails the step when no test executed at all. Every test skipping
# (an expired token, an unreachable API, a -run pattern that matches only
# gated tests) otherwise produces a green job that verified nothing.
#
# Usage: summarize-acctest-skips.sh <go-test-log-file> <section-title>
set -euo pipefail

LOG_FILE="$1"
TITLE="$2"

TOTAL=$(grep -cE '^(--- PASS|--- FAIL|--- SKIP):' "$LOG_FILE" || true)
SKIPPED=$(grep -cE '^--- SKIP:' "$LOG_FILE" || true)
REALINFRA_SKIPPED=$(grep -c 'SKIP(no-real-infra)' "$LOG_FILE" || true)

{
  echo "### ${TITLE}"
  echo ""
  echo "${TOTAL} tests ran: $((TOTAL - SKIPPED)) executed, ${SKIPPED} skipped."
  if [ "${REALINFRA_SKIPPED}" -gt 0 ]; then
    echo ""
    echo "> ⚠️ **${REALINFRA_SKIPPED} of those skips are real-infra creation tests, gated behind \`ANYSCALE_TEST_REAL_INFRA=1\`, which this CI lane never sets.** They do NOT run here and a green check does NOT mean they passed - only that they were skipped by design (placeholder credentials can't pass real cloud provisioning). See \`internal/acctest/helpers.go\`'s \`SkipIfNoRealInfra\` doc comment. Treat real-infra creation coverage as unverified by CI until confirmed via a manual \`ANYSCALE_TEST_REAL_INFRA=1\` run or the \`make test-*\` example scenarios."
  fi
} >> "${GITHUB_STEP_SUMMARY:-/dev/stdout}"

EXECUTED=$((TOTAL - SKIPPED))
if [ "${EXECUTED}" -eq 0 ]; then
  {
    echo ""
    echo "> ❌ **No test executed (${SKIPPED} skipped of ${TOTAL}).** A shard that runs nothing is a failure, not a pass."
  } >> "${GITHUB_STEP_SUMMARY:-/dev/stdout}"
  echo "::error title=${TITLE}::no acceptance test executed (${SKIPPED} skipped of ${TOTAL})" >&2
  exit 1
fi
